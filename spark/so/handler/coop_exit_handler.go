package handler

import (
	"context"
	"fmt"

	"github.com/lightsparkdev/spark/common/keys"
	"go.uber.org/zap"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/logging"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authz"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/helper"
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

// CooperativeExit signs refund transactions for leaves, spending connector outputs.
// It will lock the transferred leaves based on seeing a txid confirming on-chain.
func (h *CooperativeExitHandler) CooperativeExit(ctx context.Context, req *pb.CooperativeExitRequest) (*pb.CooperativeExitResponse, error) {
	return h.cooperativeExit(ctx, req, false)
}

// CooperativeExitV2 is the same as above, but it enforces the use of direct
// transactions for unilateral exits.
func (h *CooperativeExitHandler) CooperativeExitV2(ctx context.Context, req *pb.CooperativeExitRequest) (*pb.CooperativeExitResponse, error) {
	return h.cooperativeExit(ctx, req, true)
}

func (h *CooperativeExitHandler) cooperativeExit(ctx context.Context, req *pb.CooperativeExitRequest, requireDirectTx bool) (*pb.CooperativeExitResponse, error) {
	reqTransferOwnerIdentityPubKey, err := keys.ParsePublicKey(req.Transfer.OwnerIdentityPublicKey)
	if err != nil {
		return nil, fmt.Errorf("unable to parse transfer owner identity public key: %w", err)
	}
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, reqTransferOwnerIdentityPubKey); err != nil {
		return nil, err
	}

	transferHandler := NewTransferHandler(h.config)

	cpfpLeafRefundMap := make(map[string][]byte)
	directLeafRefundMap := make(map[string][]byte)
	directFromCpfpLeafRefundMap := make(map[string][]byte)
	for _, job := range req.Transfer.LeavesToSend {
		cpfpLeafRefundMap[job.LeafId] = job.RefundTxSigningJob.RawTx
		if job.DirectRefundTxSigningJob != nil && job.DirectFromCpfpRefundTxSigningJob != nil {
			directLeafRefundMap[job.LeafId] = job.DirectRefundTxSigningJob.RawTx
			directFromCpfpLeafRefundMap[job.LeafId] = job.DirectFromCpfpRefundTxSigningJob.RawTx
		} else if requireDirectTx {
			return nil, fmt.Errorf("DirectRefundTxSigningJob and DirectFromCpfpRefundTxSigningJob are required. Please upgrade to the latest SDK version")
		}
	}

	reqTransferReceiverIdentityPubKey, err := keys.ParsePublicKey(req.Transfer.ReceiverIdentityPublicKey)
	if err != nil {
		return nil, fmt.Errorf("unable to parse transfer receiver identity public key: %w", err)
	}

	entTx, err := ent.GetTxFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get database transaction: %w", err)
	}
	db := entTx.Client()
	transferUUID, err := uuid.Parse(req.Transfer.TransferId)
	if err != nil {
		return nil, fmt.Errorf("unable to parse transfer_id as a uuid %s: %w", req.Transfer.TransferId, err)
	}
	_, err = ent.CreateOrResetPendingSendTransfer(ctx, transferUUID)
	if err != nil {
		return nil, fmt.Errorf("unable to create pending send transfer: %w", err)
	}
	err = entTx.Commit()
	if err != nil {
		return nil, fmt.Errorf("unable to commit database transaction: %w", err)
	}

	transfer, leafMap, err := transferHandler.createTransfer(
		ctx,
		nil,
		req.Transfer.TransferId,
		st.TransferTypeCooperativeExit,
		req.Transfer.ExpiryTime.AsTime(),
		reqTransferOwnerIdentityPubKey,
		reqTransferReceiverIdentityPubKey,
		cpfpLeafRefundMap,
		directLeafRefundMap,
		directFromCpfpLeafRefundMap,
		nil,
		TransferRoleCoordinator,
		requireDirectTx,
		"",
		uuid.Nil,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create transfer %s: %w", req.Transfer.TransferId, err)
	}

	exitUUID, err := uuid.Parse(req.ExitId)
	if err != nil {
		return nil, fmt.Errorf("unable to parse exit_id %x: %w", req.ExitId, err)
	}

	if len(req.ExitTxid) != 32 {
		return nil, fmt.Errorf("exit_txid %x is not 32 bytes", req.ExitTxid)
	}

	db, err = ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for transfer id %s exit txid %x: %w", req.Transfer.TransferId, req.ExitTxid, err)
	}

	exitTxid, err := st.NewTxIDFromBytes(req.ExitTxid)
	if err != nil {
		return nil, fmt.Errorf("failed to parse exit txid for transfer id %s exit txid %x: %w", req.Transfer.TransferId, req.ExitTxid, err)
	}

	_, err = db.CooperativeExit.Create().
		SetID(exitUUID).
		SetTransfer(transfer).
		SetExitTxid(exitTxid).
		// ConfirmationHeight is nil since the transaction is not confirmed yet.
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create cooperative exit for exit id %s exit txid %s: %w", req.ExitId, exitTxid.String(), err)
	}

	transferProto, err := transfer.MarshalProto(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal transfer for transfer id %s exit id %s: %w", req.Transfer.TransferId, req.ExitId, err)
	}

	signingResults, err := signRefunds(ctx, h.config, req.Transfer, leafMap, keys.Public{}, keys.Public{}, keys.Public{})
	if err != nil {
		return nil, fmt.Errorf("failed to sign refund transactions for transfer id %s exit id %s: %w", req.Transfer.TransferId, req.ExitId, err)
	}

	err = transferHandler.syncCoopExitInit(ctx, req)
	if err != nil {

		cancelErr := transferHandler.CreateCancelTransferGossipMessage(ctx, req.Transfer.TransferId)
		if cancelErr != nil {
			return nil, fmt.Errorf("failed to create cancel transfer gossip message for transfer id %s exit id %s: %w", req.Transfer.TransferId, req.ExitId, err)
		}

		return nil, fmt.Errorf("failed to sync transfer init for transfer id %s exit id %s: %w", req.Transfer.TransferId, req.ExitId, err)
	}

	response := &pb.CooperativeExitResponse{
		Transfer:       transferProto,
		SigningResults: signingResults,
	}
	return response, nil
}

func (h *TransferHandler) syncCoopExitInit(ctx context.Context, req *pb.CooperativeExitRequest) error {
	transfer := req.Transfer
	leaves := make([]*pbinternal.InitiateTransferLeaf, 0)
	for _, leaf := range transfer.LeavesToSend {
		var directRefundTx []byte
		var directFromCpfpRefundTx []byte
		if leaf.DirectRefundTxSigningJob != nil {
			directRefundTx = leaf.DirectRefundTxSigningJob.RawTx
		}
		if leaf.DirectFromCpfpRefundTxSigningJob != nil {
			directFromCpfpRefundTx = leaf.DirectFromCpfpRefundTxSigningJob.RawTx
		}
		leaves = append(leaves, &pbinternal.InitiateTransferLeaf{
			LeafId:                 leaf.LeafId,
			RawRefundTx:            leaf.RefundTxSigningJob.RawTx,
			DirectRefundTx:         directRefundTx,
			DirectFromCpfpRefundTx: directFromCpfpRefundTx,
		})
	}
	initTransferRequest := &pbinternal.InitiateTransferRequest{
		TransferId:                transfer.TransferId,
		SenderIdentityPublicKey:   transfer.OwnerIdentityPublicKey,
		ReceiverIdentityPublicKey: transfer.ReceiverIdentityPublicKey,
		ExpiryTime:                transfer.ExpiryTime,
		Leaves:                    leaves,
	}
	coopExitRequest := &pbinternal.InitiateCooperativeExitRequest{
		Transfer: initTransferRequest,
		ExitId:   req.ExitId,
		ExitTxid: req.ExitTxid,
	}
	selection := helper.OperatorSelection{
		Option: helper.OperatorSelectionOptionExcludeSelf,
	}
	_, err := helper.ExecuteTaskWithAllOperators(ctx, h.config, &selection, func(ctx context.Context, operator *so.SigningOperator) (any, error) {
		logger := logging.GetLoggerFromContext(ctx)

		conn, err := operator.NewOperatorGRPCConnection()
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
