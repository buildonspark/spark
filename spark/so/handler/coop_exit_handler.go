package handler

import (
	"context"
	"fmt"

	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/logging"
	"go.uber.org/zap"

	"github.com/google/uuid"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authz"
	"github.com/lightsparkdev/spark/so/consensus"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/pendingsendtransfer"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/helper"
	"github.com/lightsparkdev/spark/so/partner"
)

// CooperativeExitHandler tracks transfers
// and on-chain txs events for cooperative exits.
type CooperativeExitHandler struct {
	config *so.Config
}

// NewCooperativeExitHandler creates a new CooperativeExitHandler.
func NewCooperativeExitHandler(config *so.Config) *CooperativeExitHandler {
	return &CooperativeExitHandler{
		config: config,
	}
}

// CooperativeExitV2 signs refund transactions for leaves, spending connector outputs.
// It will lock the transferred leaves based on seeing a txid confirming on-chain.
// It enforces the use of direct transactions for unilateral exits.
func (h *CooperativeExitHandler) CooperativeExitV2(ctx context.Context, req *pb.CooperativeExitRequest) (resp *pb.CooperativeExitResponse, retErr error) {
	if req == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("request is required"))
	}
	if req.GetTransfer() == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("transfer is required"))
	}

	reqTransferOwnerIdentityPubKey, err := keys.ParsePublicKey(req.GetTransfer().GetOwnerIdentityPublicKey())
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("unable to parse transfer owner identity public key: %w", err))
	}
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, reqTransferOwnerIdentityPubKey); err != nil {
		return nil, err
	}
	if err := authz.EnforceWalletNotKillSwitched(ctx, reqTransferOwnerIdentityPubKey); err != nil {
		return nil, err
	}

	if req.GetTransfer().GetTransferPackage() != nil {
		return h.cooperativeExitConsensus(ctx, req)
	}

	transferHandler := NewTransferHandler(h.config)

	cpfpLeafRefundMap := make(map[string][]byte)
	directLeafRefundMap := make(map[string][]byte)
	directFromCpfpLeafRefundMap := make(map[string][]byte)
	if len(req.GetTransfer().GetLeavesToSend()) == 0 {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("at least one leaf to send is required"))
	}
	// Reject duplicate or malformed leaf IDs before collapsing the slice into
	// maps: signRefunds iterates the raw slice and would sign every entry,
	// while validation only ever sees the last entry per leaf ID.
	if err := validateLegacyLeafRefundTxSigningJobs(req.GetTransfer().GetLeavesToSend()); err != nil {
		return nil, err
	}
	for _, job := range req.GetTransfer().GetLeavesToSend() {
		if job == nil {
			return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("leaf refund tx signing job is required"))
		}
		if job.GetRefundTxSigningJob() == nil {
			return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("refund tx signing job is required for leaf %s", job.GetLeafId()))
		}
		cpfpLeafRefundMap[job.GetLeafId()] = job.GetRefundTxSigningJob().GetRawTx()
		if job.GetDirectRefundTxSigningJob() != nil {
			directLeafRefundMap[job.GetLeafId()] = job.GetDirectRefundTxSigningJob().GetRawTx()
		}
		if job.GetDirectFromCpfpRefundTxSigningJob() == nil {
			return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("DirectFromCpfpRefundTxSigningJob is required. Please upgrade to the latest SDK version"))
		}
		directFromCpfpLeafRefundMap[job.GetLeafId()] = job.GetDirectFromCpfpRefundTxSigningJob().GetRawTx()
	}

	reqTransferReceiverIdentityPubKey, err := keys.ParsePublicKey(req.GetTransfer().GetReceiverIdentityPublicKey())
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("unable to parse transfer receiver identity public key: %w", err))
	}

	// Validate exit_txid <-> connector_tx binding before any DB write, leaf
	// lookup, or FROST work. See parseAndValidateCoopExitTxid.
	exitTxid, err := parseAndValidateCoopExitTxid(req.GetTransfer().GetTransferId(), req.GetExitTxid(), req.GetConnectorTx())
	if err != nil {
		return nil, err
	}

	entTx, err := ent.GetTxFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get database transaction: %w", err)
	}

	transferUUID, err := uuid.Parse(req.GetTransfer().GetTransferId())
	if err != nil {
		return nil, fmt.Errorf("unable to parse transfer_id as a uuid %s: %w", req.GetTransfer().GetTransferId(), err)
	}
	_, err = ent.CreateOrResetPendingSendTransfer(ctx, transferUUID)
	if err != nil {
		return nil, fmt.Errorf("unable to create pending send transfer: %w", err)
	}
	err = entTx.Commit()
	if err != nil {
		return nil, fmt.Errorf("unable to commit database transaction: %w", err)
	}

	// Rollback PendingSendTransfer on any failure between here and the success
	// point. cancelGossip is set to true before syncing to other SOs.
	needsRollback := true
	cancelGossip := false
	defer func() {
		if !needsRollback || retErr == nil {
			return
		}
		if rbErr := transferHandler.rollbackTransferInit(ctx, transferUUID, cancelGossip); rbErr != nil {
			retErr = fmt.Errorf("rollback failed: %w while processing coop exit %s: %w", rbErr, transferUUID, retErr)
		}
	}()

	transfer, leafMap, err := transferHandler.createTransfer(
		ctx,
		transferUUID,
		nil,
		st.TransferTypeCooperativeExit,
		req.GetTransfer().GetExpiryTime().AsTime(),
		reqTransferOwnerIdentityPubKey,
		reqTransferReceiverIdentityPubKey,
		cpfpLeafRefundMap,
		directLeafRefundMap,
		directFromCpfpLeafRefundMap,
		nil,
		TransferRoleCoordinator,
		true,
		"",
		uuid.Nil,
		req.GetConnectorTx(),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create transfer %s: %w", req.GetTransfer().GetTransferId(), err)
	}

	exitUUID, err := uuid.Parse(req.GetExitId())
	if err != nil {
		return nil, fmt.Errorf("unable to parse exit_id %x: %w", req.GetExitId(), err)
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for transfer id %s exit txid %x: %w", req.GetTransfer().GetTransferId(), req.GetExitTxid(), err)
	}

	// exit_txid was already parsed + validated above.
	_, err = db.CooperativeExit.Create().
		SetID(exitUUID).
		SetTransfer(transfer).
		SetExitTxid(exitTxid).
		// ConfirmationHeight is nil since the transaction is not confirmed yet.
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create cooperative exit for exit id %s exit txid %s: %w", req.GetExitId(), exitTxid, err)
	}

	transferProto, err := transfer.MarshalProto(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal transfer for transfer id %s exit id %s: %w", req.GetTransfer().GetTransferId(), req.GetExitId(), err)
	}

	if len(req.GetConnectorTx()) == 0 {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("connector tx required for cooperative exit validation. Please upgrade to the latest SDK version"))
	}

	signingResults, err := signRefunds(ctx, h.config, req.GetTransfer(), leafMap, keys.Public{}, keys.Public{}, keys.Public{}, req.GetConnectorTx())
	if err != nil {
		return nil, fmt.Errorf("failed to sign refund transactions for transfer id %s exit id %s: %w", req.GetTransfer().GetTransferId(), req.GetExitId(), err)
	}

	cancelGossip = true
	err = transferHandler.syncCoopExitInit(ctx, req, nil, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to sync transfer init for transfer id %s exit id %s: %w", req.GetTransfer().GetTransferId(), req.GetExitId(), err)
	}

	// After this point, the coop exit sync is considered successful.
	needsRollback = false

	// Commit the current transaction to persist the transfer data, ensuring
	// consistency with non-coordinator SOs.
	entTx, err = ent.GetTxFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get database transaction: %w", err)
	}
	if err = entTx.Commit(); err != nil {
		return nil, fmt.Errorf("unable to commit transfer data after successful sync: %w", err)
	}

	partner.SaveTransferPartner(ctx, transferUUID, st.TransferPartnerTypeCooperativeExit)

	// Mark PendingSendTransfer finished on success.
	db, err = ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get database context: %w", err)
	}
	_, err = db.PendingSendTransfer.Update().Where(pendingsendtransfer.TransferID(transferUUID)).SetStatus(st.PendingSendTransferStatusFinished).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to update pending send transfer: %w", err)
	}

	response := &pb.CooperativeExitResponse{
		Transfer:       transferProto,
		SigningResults: signingResults,
	}
	return response, nil
}

// cooperativeExitConsensus runs the TransferPackage coop-exit path through the
// 2PC consensus engine. Mirrors startTransferV3Consensus: build the coordinator
// flow, fetch the engine from context, and Execute across all operators. The
// engine drives createTransfer + CooperativeExit row + FROST round-2 in Prepare,
// aggregation in BuildCommitPayload, and the refund-signature application in
// Commit on every SO. Key tweaks stay deferred to the chain watcher.
//
// The stale-flow sweep does not need to undo coordinator domain writes: the
// engine records the COMMITTED decision atomically with the coordinator's
// createTransfer + CooperativeExit row writes (single request-tx DbCommit),
// so a crash before that commit rolls the domain work back with an IN_FLIGHT
// row (sweep → ROLLED_BACK, consistent) and a crash after it leaves a
// COMMITTED row the reconciler drives forward (SP-3195).
func (h *CooperativeExitHandler) cooperativeExitConsensus(ctx context.Context, req *pb.CooperativeExitRequest) (*pb.CooperativeExitResponse, error) {
	flow, err := buildCoopExitCoordinatorFlow(ctx, h.config, req)
	if err != nil {
		return nil, err
	}
	engine, err := consensus.GetEngine(ctx)
	if err != nil {
		return nil, err
	}
	selection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionAll}
	if _, err := engine.Execute(ctx,
		pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_COOP_EXIT,
		&selection,
		flow,
	); err != nil {
		return nil, fmt.Errorf("consensus coop exit failed: %w", err)
	}
	if flow.response == nil {
		return nil, fmt.Errorf("internal: consensus coop exit for %s succeeded but produced no response", req.GetTransfer().GetTransferId())
	}
	return flow.response, nil
}

func (h *TransferHandler) syncCoopExitInit(
	ctx context.Context,
	req *pb.CooperativeExitRequest,
	cpfpRefundSignatures map[string][]byte,
	directRefundSignatures map[string][]byte,
	directFromCpfpRefundSignatures map[string][]byte,
) error {
	transfer := req.GetTransfer()

	initTransferRequest := &pbinternal.InitiateTransferRequest{
		TransferId:                transfer.GetTransferId(),
		SenderIdentityPublicKey:   transfer.GetOwnerIdentityPublicKey(),
		ReceiverIdentityPublicKey: transfer.GetReceiverIdentityPublicKey(),
		ExpiryTime:                transfer.GetExpiryTime(),
	}

	if transfer.GetTransferPackage() != nil {
		senderKeyTweakProofs, err := h.coordinatorSenderKeyTweakProofs(ctx, transfer.GetTransferPackage())
		if err != nil {
			return fmt.Errorf("unable to read sender key tweak proofs from the coordinator's slice: %w", err)
		}
		initTransferRequest.TransferPackage = transfer.GetTransferPackage()
		initTransferRequest.SenderKeyTweakProofs = senderKeyTweakProofs
		initTransferRequest.RefundSignatures = cpfpRefundSignatures
		initTransferRequest.DirectRefundSignatures = directRefundSignatures
		initTransferRequest.DirectFromCpfpRefundSignatures = directFromCpfpRefundSignatures
	} else {
		var leaves []*pbinternal.InitiateTransferLeaf
		for _, leaf := range transfer.GetLeavesToSend() {
			var directRefundTx []byte
			var directFromCpfpRefundTx []byte
			if leaf.GetDirectRefundTxSigningJob() != nil {
				directRefundTx = leaf.GetDirectRefundTxSigningJob().GetRawTx()
			}
			if leaf.GetDirectFromCpfpRefundTxSigningJob() != nil {
				directFromCpfpRefundTx = leaf.GetDirectFromCpfpRefundTxSigningJob().GetRawTx()
			}
			leaves = append(leaves, &pbinternal.InitiateTransferLeaf{
				LeafId:                 leaf.GetLeafId(),
				RawRefundTx:            leaf.GetRefundTxSigningJob().GetRawTx(),
				DirectRefundTx:         directRefundTx,
				DirectFromCpfpRefundTx: directFromCpfpRefundTx,
			})
		}
		initTransferRequest.Leaves = leaves
	}

	coopExitRequest := &pbinternal.InitiateCooperativeExitRequest{
		Transfer:    initTransferRequest,
		ExitId:      req.GetExitId(),
		ExitTxid:    req.GetExitTxid(),
		ConnectorTx: req.GetConnectorTx(),
	}
	selection := helper.OperatorSelection{
		Option: helper.OperatorSelectionOptionExcludeSelf,
	}
	_, err := helper.ExecuteTaskWithAllOperators(ctx, h.config, &selection, func(ctx context.Context, operator *so.SigningOperator) (any, error) {
		logger := logging.GetLoggerFromContext(ctx)

		conn, err := operator.NewOperatorInternalGRPCConnection(ctx)
		if err != nil {
			logger.Error("Failed to connect to operator", zap.Error(err))
			return nil, err
		}
		defer conn.Close()

		client := pbinternal.NewSparkInternalServiceClient(conn)
		return client.InitiateCooperativeExit(ctx, coopExitRequest)
	})
	return err
}
