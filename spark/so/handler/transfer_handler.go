package handler

import (
	"bytes"
	"context"
	"fmt"
	"math/big"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/logging"
	secretsharing "github.com/lightsparkdev/spark/common/secret_sharing"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authz"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/blockheight"
	"github.com/lightsparkdev/spark/so/ent/cooperativeexit"
	"github.com/lightsparkdev/spark/so/ent/predicate"
	"github.com/lightsparkdev/spark/so/ent/preimagerequest"
	"github.com/lightsparkdev/spark/so/ent/schema"
	enttransfer "github.com/lightsparkdev/spark/so/ent/transfer"
	enttransferleaf "github.com/lightsparkdev/spark/so/ent/transferleaf"
	enttreenode "github.com/lightsparkdev/spark/so/ent/treenode"
	"github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/helper"
	"github.com/lightsparkdev/spark/so/objects"
	events "github.com/lightsparkdev/spark/so/stream"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/proto"
)

// TransferHandler is a helper struct to handle leaves transfer request.
type TransferHandler struct {
	BaseTransferHandler
	config *so.Config
}

var transferTypeKey = attribute.Key("transfer_type")

// NewTransferHandler creates a new TransferHandler.
func NewTransferHandler(config *so.Config) *TransferHandler {
	return &TransferHandler{BaseTransferHandler: NewBaseTransferHandler(config), config: config}
}

func (h *TransferHandler) loadLeafRefundMap(req *pb.StartTransferRequest) map[string][]byte {
	leafRefundMap := make(map[string][]byte)
	if req.TransferPackage != nil {
		for _, leaf := range req.TransferPackage.LeavesToSend {
			leafRefundMap[leaf.LeafId] = leaf.RawTx
		}
	} else {
		for _, leaf := range req.LeavesToSend {
			leafRefundMap[leaf.LeafId] = leaf.RefundTxSigningJob.RawTx
		}
	}
	return leafRefundMap
}

// startTransferInternal starts a transfer, signing refunds, and saving the transfer to the DB
// for the first time. This optionally takes an adaptorPubKey to modify the refund signatures.
func (h *TransferHandler) startTransferInternal(ctx context.Context, req *pb.StartTransferRequest, transferType schema.TransferType, adaptorPubKey []byte) (*pb.StartTransferResponse, error) {
	ctx, span := tracer.Start(ctx, "TransferHandler.startTransferInternal", trace.WithAttributes(
		transferTypeKey.String(string(transferType)),
	))
	defer span.End()

	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, req.OwnerIdentityPublicKey); err != nil {
		return nil, err
	}

	leafTweakMap, err := h.validateTransferPackage(ctx, req.TransferId, req.TransferPackage, req.OwnerIdentityPublicKey)
	if err != nil {
		return nil, err
	}

	leafRefundMap := h.loadLeafRefundMap(req)
	logger := logging.GetLoggerFromContext(ctx)
	logger.Info("leafRefundMap", "leafRefundMap", leafRefundMap)
	transfer, leafMap, err := h.createTransfer(
		ctx,
		req.TransferId,
		transferType,
		req.ExpiryTime.AsTime(),
		req.OwnerIdentityPublicKey,
		req.ReceiverIdentityPublicKey,
		leafRefundMap,
		leafTweakMap,
		TransferRoleCoordinator,
	)
	if err != nil {
		return nil, err
	}

	transferProto, err := transfer.MarshalProto(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to marshal transfer: %v", err)
	}

	signingResults, err := signRefunds(ctx, h.config, req, leafMap, adaptorPubKey)
	if err != nil {
		return nil, err
	}

	finalSignatureMap := make(map[string][]byte)
	// TODO (zhen): Implement aggregate signature and update transfer leaves with finalized signatures

	// This call to other SOs will check the validity of the transfer package. If no error is
	// returned, it means the transfer package is valid and the transfer is considered sent.
	err = h.syncTransferInit(ctx, req, transferType, finalSignatureMap)
	if err != nil {
		_ = h.settleSenderKeyTweaks(ctx, req.TransferId, false)
		return nil, err
	}

	// After this point, the transfer send is considered successful.

	if req.TransferPackage != nil {
		// If all other SOs have settled the sender key tweaks, we can commit the sender key tweaks.
		// If there's any error, it means one or more of the SOs are down at the time, we will have a
		// cron job to retry the key commit.
		err = h.settleSenderKeyTweaks(ctx, req.TransferId, true)
		if err == nil {
			err = h.commitSenderKeyTweaks(ctx, transfer)
			if err != nil {
				// Too bad, at this point there's a bug where all other SOs has tweaked the key but
				// the coordinator failed so the fund is lost.
				return nil, err
			}
		}
	}

	return &pb.StartTransferResponse{Transfer: transferProto, SigningResults: signingResults}, nil
}

// settleSenderKeyTweaks calls the other SOs to settle the sender key tweaks.
func (h *TransferHandler) settleSenderKeyTweaks(ctx context.Context, transferID string, shouldCommit bool) error {
	operatorSelection := helper.OperatorSelection{
		Option: helper.OperatorSelectionOptionExcludeSelf,
	}
	var action pbinternal.SettleKeyTweakAction
	if shouldCommit {
		action = pbinternal.SettleKeyTweakAction_COMMIT
	} else {
		action = pbinternal.SettleKeyTweakAction_ROLLBACK
	}
	_, err := helper.ExecuteTaskWithAllOperators(ctx, h.config, &operatorSelection, func(ctx context.Context, operator *so.SigningOperator) (interface{}, error) {
		conn, err := operator.NewGRPCConnection()
		if err != nil {
			return nil, err
		}
		defer conn.Close()

		client := pbinternal.NewSparkInternalServiceClient(conn)
		return client.SettleSenderKeyTweak(ctx, &pbinternal.SettleSenderKeyTweakRequest{
			TransferId: transferID,
			Action:     action,
		})
	})
	return err
}

// StartTransfer initiates a transfer from sender.
func (h *TransferHandler) StartTransfer(ctx context.Context, req *pb.StartTransferRequest) (*pb.StartTransferResponse, error) {
	return h.startTransferInternal(ctx, req, schema.TransferTypeTransfer, nil)
}

func (h *TransferHandler) StartLeafSwap(ctx context.Context, req *pb.StartTransferRequest) (*pb.StartTransferResponse, error) {
	return h.startTransferInternal(ctx, req, schema.TransferTypeSwap, nil)
}

// CounterLeafSwap initiates a leaf swap for the other side, signing refunds with an adaptor public key.
func (h *TransferHandler) CounterLeafSwap(ctx context.Context, req *pb.CounterLeafSwapRequest) (*pb.CounterLeafSwapResponse, error) {
	startTransferResponse, err := h.startTransferInternal(ctx, req.Transfer, schema.TransferTypeCounterSwap, req.AdaptorPublicKey)
	if err != nil {
		return nil, err
	}
	return &pb.CounterLeafSwapResponse{Transfer: startTransferResponse.Transfer, SigningResults: startTransferResponse.SigningResults}, nil
}

func (h *TransferHandler) syncTransferInit(ctx context.Context, req *pb.StartTransferRequest, transferType schema.TransferType, _ map[string][]byte) error {
	ctx, span := tracer.Start(ctx, "TransferHandler.syncTransferInit", trace.WithAttributes(
		transferTypeKey.String(string(transferType)),
	))
	defer span.End()

	leaves := make([]*pbinternal.InitiateTransferLeaf, 0)
	for _, leaf := range req.LeavesToSend {
		leaves = append(leaves, &pbinternal.InitiateTransferLeaf{
			LeafId:      leaf.LeafId,
			RawRefundTx: leaf.RefundTxSigningJob.RawTx,
		})
	}
	transferTypeProto, err := ent.TransferTypeProto(transferType)
	if err != nil {
		return fmt.Errorf("unable to get transfer type proto: %v", err)
	}
	initTransferRequest := &pbinternal.InitiateTransferRequest{
		TransferId:                req.TransferId,
		SenderIdentityPublicKey:   req.OwnerIdentityPublicKey,
		ReceiverIdentityPublicKey: req.ReceiverIdentityPublicKey,
		ExpiryTime:                req.ExpiryTime,
		Leaves:                    leaves,
		Type:                      *transferTypeProto,
		TransferPackage:           req.TransferPackage,
	}
	selection := helper.OperatorSelection{
		Option: helper.OperatorSelectionOptionExcludeSelf,
	}
	_, err = helper.ExecuteTaskWithAllOperators(ctx, h.config, &selection, func(ctx context.Context, operator *so.SigningOperator) (interface{}, error) {
		conn, err := operator.NewGRPCConnection()
		if err != nil {
			return nil, err
		}
		defer conn.Close()

		client := pbinternal.NewSparkInternalServiceClient(conn)
		return client.InitiateTransfer(ctx, initTransferRequest)
	})
	return err
}

func signRefunds(ctx context.Context, config *so.Config, requests *pb.StartTransferRequest, leafMap map[string]*ent.TreeNode, adaptorPubKey []byte) ([]*pb.LeafRefundTxSigningResult, error) {
	ctx, span := tracer.Start(ctx, "TransferHandler.signRefunds")
	defer span.End()

	signingJobs := make([]*helper.SigningJob, 0)
	leafJobMap := make(map[string]*ent.TreeNode)
	if requests.TransferPackage == nil {
		for _, req := range requests.LeavesToSend {
			leaf := leafMap[req.LeafId]
			refundTx, err := common.TxFromRawTxBytes(req.RefundTxSigningJob.RawTx)
			if err != nil {
				return nil, fmt.Errorf("unable to load new refund tx: %v", err)
			}

			leafTx, err := common.TxFromRawTxBytes(leaf.RawTx)
			if err != nil {
				return nil, fmt.Errorf("unable to load leaf tx: %v", err)
			}
			if len(leafTx.TxOut) <= 0 {
				return nil, fmt.Errorf("vout out of bounds")
			}
			refundTxSigHash, err := common.SigHashFromTx(refundTx, 0, leafTx.TxOut[0])
			if err != nil {
				return nil, fmt.Errorf("unable to calculate sighash from refund tx: %v", err)
			}

			userNonceCommitment, err := objects.NewSigningCommitment(req.RefundTxSigningJob.SigningNonceCommitment.Binding, req.RefundTxSigningJob.SigningNonceCommitment.Hiding)
			if err != nil {
				return nil, err
			}
			jobID := uuid.New().String()
			signingKeyshare, err := leaf.QuerySigningKeyshare().Only(ctx)
			if err != nil {
				return nil, fmt.Errorf("failed to get signing keyshare id: %w", err)
			}
			signingJobs = append(
				signingJobs,
				&helper.SigningJob{
					JobID:             jobID,
					SigningKeyshareID: signingKeyshare.ID,
					Message:           refundTxSigHash,
					VerifyingKey:      leaf.VerifyingPubkey,
					UserCommitment:    userNonceCommitment,
					AdaptorPublicKey:  adaptorPubKey,
				},
			)
			leafJobMap[jobID] = leaf
		}
	}
	// TODO (zhen): Implement the other path where TransferPackage is not nil.

	signingResults, err := helper.SignFrost(ctx, config, signingJobs)
	if err != nil {
		return nil, err
	}
	pbSigningResults := make([]*pb.LeafRefundTxSigningResult, 0)
	for _, signingResult := range signingResults {
		leaf := leafJobMap[signingResult.JobID]
		signingResultProto, err := signingResult.MarshalProto()
		if err != nil {
			return nil, err
		}
		pbSigningResults = append(pbSigningResults, &pb.LeafRefundTxSigningResult{
			LeafId:                leaf.ID.String(),
			RefundTxSigningResult: signingResultProto,
			VerifyingKey:          leaf.VerifyingPubkey,
		})
	}
	return pbSigningResults, nil
}

// FinalizeTransfer completes a transfer from sender.
func (h *TransferHandler) FinalizeTransfer(ctx context.Context, req *pb.FinalizeTransferRequest) (*pb.FinalizeTransferResponse, error) {
	ctx, span := tracer.Start(ctx, "TransferHandler.FinalizeTransfer")
	defer span.End()

	logger := logging.GetLoggerFromContext(ctx)

	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, req.OwnerIdentityPublicKey); err != nil {
		return nil, err
	}

	transfer, err := h.loadTransfer(ctx, req.TransferId)
	if err != nil {
		return nil, fmt.Errorf("unable to load transfer %s: %v", req.TransferId, err)
	}
	span.SetAttributes(transferTypeKey.String(string(transfer.Type)))
	if !bytes.Equal(transfer.SenderIdentityPubkey, req.OwnerIdentityPublicKey) || transfer.Status != schema.TransferStatusSenderInitiated {
		return nil, fmt.Errorf("send transfer cannot be completed %s", req.TransferId)
	}

	db := ent.GetDbFromContext(ctx)
	shouldTweakKey := true
	switch transfer.Type {
	case schema.TransferTypePreimageSwap:
		preimageRequest, err := db.PreimageRequest.Query().Where(preimagerequest.HasTransfersWith(enttransfer.ID(transfer.ID))).Only(ctx)
		if err != nil || preimageRequest == nil {
			return nil, fmt.Errorf("unable to find preimage request for transfer %s: %v", transfer.ID.String(), err)
		}
		shouldTweakKey = preimageRequest.Status == schema.PreimageRequestStatusPreimageShared
	case schema.TransferTypeCooperativeExit:
		err = checkCoopExitTxBroadcasted(ctx, db, transfer)
		shouldTweakKey = err == nil
	default:
		// do nothing
	}

	for _, leaf := range req.LeavesToSend {
		err = h.completeSendLeaf(ctx, transfer, leaf, shouldTweakKey)
		if err != nil {
			return nil, fmt.Errorf("unable to complete send leaf transfer for leaf %s: %v", leaf.LeafId, err)
		}
	}

	// Update transfer status
	statusToSet := schema.TransferStatusSenderKeyTweaked
	if !shouldTweakKey {
		statusToSet = schema.TransferStatusSenderKeyTweakPending
	}
	transfer, err = transfer.Update().SetStatus(statusToSet).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to update transfer status %s: %v", transfer.ID.String(), err)
	}
	transferProto, err := transfer.MarshalProto(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to marshal transfer: %v", err)
	}
	eventRouter := events.GetDefaultRouter()
	err = eventRouter.NotifyUser(transfer.ReceiverIdentityPubkey, &pb.SubscribeToEventsResponse{
		Event: &pb.SubscribeToEventsResponse_Transfer{
			Transfer: &pb.TransferEvent{
				Transfer: transferProto,
			},
		},
	})
	if err != nil {
		logger.Error("failed to notify user about transfer event", "error", err, "identity_public_key", logging.Pubkey{Pubkey: transfer.ReceiverIdentityPubkey})
	}

	return &pb.FinalizeTransferResponse{Transfer: transferProto}, nil
}

func (h *TransferHandler) completeSendLeaf(ctx context.Context, transfer *ent.Transfer, req *pb.SendLeafKeyTweak, shouldTweakKey bool) error {
	ctx, span := tracer.Start(ctx, "TransferHandler.completeSendLeaf", trace.WithAttributes(
		transferTypeKey.String(string(transfer.Type)),
	))
	defer span.End()

	logger := logging.GetLoggerFromContext(ctx)

	// Use Feldman's verifiable secret sharing to verify the share.
	err := secretsharing.ValidateShare(
		&secretsharing.VerifiableSecretShare{
			SecretShare: secretsharing.SecretShare{
				FieldModulus: secp256k1.S256().N,
				Threshold:    int(h.config.Threshold),
				Index:        big.NewInt(int64(h.config.Index + 1)),
				Share:        new(big.Int).SetBytes(req.SecretShareTweak.SecretShare),
			},
			Proofs: req.SecretShareTweak.Proofs,
		},
	)
	if err != nil {
		return fmt.Errorf("unable to validate share: %v", err)
	}

	// TODO (zhen): Verify possession

	// Find leaves in db
	leafID, err := uuid.Parse(req.LeafId)
	if err != nil {
		return fmt.Errorf("unable to parse leaf_id %s: %v", req.LeafId, err)
	}

	db := ent.GetDbFromContext(ctx)
	leaf, err := db.TreeNode.Get(ctx, leafID)
	if err != nil || leaf == nil {
		return fmt.Errorf("unable to find leaf %s: %v", req.LeafId, err)
	}
	if leaf.Status != schema.TreeNodeStatusTransferLocked ||
		!bytes.Equal(leaf.OwnerIdentityPubkey, transfer.SenderIdentityPubkey) {
		return fmt.Errorf("leaf %s is not available to transfer", req.LeafId)
	}

	transferLeaf, err := db.TransferLeaf.
		Query().
		Where(
			enttransferleaf.HasTransferWith(enttransfer.IDEQ(transfer.ID)),
			enttransferleaf.HasLeafWith(enttreenode.IDEQ(leafID)),
		).
		Only(ctx)
	if err != nil || transferLeaf == nil {
		return fmt.Errorf("unable to get transfer leaf %s: %v", req.LeafId, err)
	}

	// Optional verify if the sender key tweak proof is the same as the one in previous call.
	if transferLeaf.SenderKeyTweakProof != nil {
		proof := &pb.SecretProof{}
		err = proto.Unmarshal(transferLeaf.SenderKeyTweakProof, proof)
		if err != nil {
			return fmt.Errorf("unable to unmarshal sender key tweak proof: %v", err)
		}
		shareProof := req.SecretShareTweak.Proofs
		for i, proof := range proof.Proofs {
			if !bytes.Equal(proof, shareProof[i]) {
				return fmt.Errorf("sender key tweak proof mismatch")
			}
		}
	}

	refundTxBytes, err := common.UpdateTxWithSignature(transferLeaf.IntermediateRefundTx, 0, req.RefundSignature)
	if err != nil {
		return fmt.Errorf("unable to update refund tx with signature: %v", err)
	}

	if transfer.Type != schema.TransferTypePreimageSwap && transfer.Type != schema.TransferTypeUtxoSwap {
		// Verify signature
		refundTx, err := common.TxFromRawTxBytes(refundTxBytes)
		if err != nil {
			return fmt.Errorf("unable to deserialize refund tx: %v", err)
		}
		leafNodeTx, err := common.TxFromRawTxBytes(leaf.RawTx)
		if err != nil {
			return fmt.Errorf("unable to deserialize leaf tx: %v", err)
		}
		if len(leafNodeTx.TxOut) <= 0 {
			return fmt.Errorf("vout out of bounds")
		}
		if !refundTx.HasWitness() {
			logger.Warn("transaction has no witness", "tx", refundTx)
		}
		err = common.VerifySignatureSingleInput(refundTx, 0, leafNodeTx.TxOut[0])
		if err != nil {
			logger.Error("unable to verify refund tx signature", "error", err, "refundTx", refundTx)
			return fmt.Errorf("unable to verify refund tx signature: %v", err)
		}
	}

	transferLeafMutator := db.TransferLeaf.
		UpdateOne(transferLeaf).
		SetIntermediateRefundTx(refundTxBytes).
		SetSecretCipher(req.SecretCipher).
		SetSignature(req.Signature)
	if !shouldTweakKey {
		keyTweak, err := proto.Marshal(req)
		if err != nil {
			return fmt.Errorf("unable to marshal key tweak: %v", err)
		}
		transferLeafMutator.SetKeyTweak(keyTweak)
	}
	_, err = transferLeafMutator.Save(ctx)
	if err != nil {
		return fmt.Errorf("unable to update transfer leaf: %v", err)
	}

	if shouldTweakKey {
		err = helper.TweakLeafKey(ctx, leaf, req, refundTxBytes)
		if err != nil {
			return fmt.Errorf("unable to tweak leaf key: %v", err)
		}
	}

	return nil
}

func (h *TransferHandler) queryTransfers(ctx context.Context, filter *pb.TransferFilter, isPending bool) (*pb.QueryTransfersResponse, error) {
	ctx, span := tracer.Start(ctx, "TransferHandler.queryTransfers")
	defer span.End()

	db := ent.GetDbFromContext(ctx)
	var transferPredicate []predicate.Transfer

	receiverPendingStatuses := []schema.TransferStatus{
		schema.TransferStatusSenderKeyTweaked,
		schema.TransferStatusReceiverKeyTweaked,
		schema.TransferStatusReceiverRefundSigned,
	}
	senderPendingStatuses := []schema.TransferStatus{
		schema.TransferStatusSenderKeyTweakPending,
		schema.TransferStatusSenderInitiated,
	}

	switch filter.Participant.(type) {
	case *pb.TransferFilter_ReceiverIdentityPublicKey:
		if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, filter.GetReceiverIdentityPublicKey()); err != nil {
			return nil, err
		}
		transferPredicate = append(transferPredicate, enttransfer.ReceiverIdentityPubkeyEQ(filter.GetReceiverIdentityPublicKey()))
		if isPending {
			transferPredicate = append(transferPredicate,
				enttransfer.StatusIn(receiverPendingStatuses...),
			)
		}
	case *pb.TransferFilter_SenderIdentityPublicKey:
		if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, filter.GetSenderIdentityPublicKey()); err != nil {
			return nil, err
		}
		transferPredicate = append(transferPredicate, enttransfer.SenderIdentityPubkeyEQ(filter.GetSenderIdentityPublicKey()))
		if isPending {
			transferPredicate = append(transferPredicate,
				enttransfer.StatusIn(senderPendingStatuses...),
				enttransfer.ExpiryTimeLT(time.Now()),
			)
		}
	case *pb.TransferFilter_SenderOrReceiverIdentityPublicKey:
		identityPubkey := filter.GetSenderOrReceiverIdentityPublicKey()
		if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, identityPubkey); err != nil {
			return nil, err
		}
		if isPending {
			transferPredicate = append(transferPredicate, enttransfer.Or(
				enttransfer.And(
					enttransfer.ReceiverIdentityPubkeyEQ(identityPubkey),
					enttransfer.StatusIn(receiverPendingStatuses...),
				),
				enttransfer.And(
					enttransfer.SenderIdentityPubkeyEQ(identityPubkey),
					enttransfer.StatusIn(senderPendingStatuses...),
					enttransfer.ExpiryTimeLT(time.Now()),
				),
			))
		} else {
			transferPredicate = append(transferPredicate, enttransfer.Or(
				enttransfer.ReceiverIdentityPubkeyEQ(identityPubkey),
				enttransfer.SenderIdentityPubkeyEQ(identityPubkey),
			))
		}
	}

	if filter.TransferIds != nil {
		transferUUIDs := make([]uuid.UUID, len(filter.TransferIds))
		for _, transferID := range filter.TransferIds {
			transferUUID, err := uuid.Parse(transferID)
			if err != nil {
				return nil, fmt.Errorf("unable to parse transfer id as a uuid %s: %v", transferID, err)
			}
			transferUUIDs = append(transferUUIDs, transferUUID)
		}
		transferPredicate = append([]predicate.Transfer{enttransfer.IDIn(transferUUIDs...)}, transferPredicate...)
	}

	if len(filter.Types) > 0 {
		transferTypes := make([]schema.TransferType, len(filter.Types))
		for i, transferType := range filter.Types {
			transferTypes[i] = schema.TransferType(transferType.String())
		}
		transferPredicate = append(transferPredicate, enttransfer.TypeIn(transferTypes...))
	}

	baseQuery := db.Transfer.Query()
	if len(transferPredicate) > 0 {
		baseQuery = baseQuery.Where(enttransfer.And(transferPredicate...))
	}

	query := baseQuery.Order(ent.Desc(enttransfer.FieldUpdateTime))

	if filter.Limit > 100 || filter.Limit == 0 {
		filter.Limit = 100
	}
	query = query.Limit(int(filter.Limit))

	if filter.Offset > 0 {
		query = query.Offset(int(filter.Offset))
	}

	transfers, err := query.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to query transfers: %v", err)
	}

	transferProtos := []*pb.Transfer{}
	for _, transfer := range transfers {
		transferProto, err := transfer.MarshalProto(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to marshal transfer: %v", err)
		}
		transferProtos = append(transferProtos, transferProto)
	}

	var nextOffset int64
	if len(transfers) == int(filter.Limit) {
		nextOffset = filter.Offset + int64(len(transfers))
	} else {
		nextOffset = -1
	}

	return &pb.QueryTransfersResponse{
		Transfers: transferProtos,
		Offset:    nextOffset,
	}, nil
}

func (h *TransferHandler) QueryPendingTransfers(ctx context.Context, filter *pb.TransferFilter) (*pb.QueryTransfersResponse, error) {
	return h.queryTransfers(ctx, filter, true)
}

func (h *TransferHandler) QueryAllTransfers(ctx context.Context, filter *pb.TransferFilter) (*pb.QueryTransfersResponse, error) {
	return h.queryTransfers(ctx, filter, false)
}

const CoopExitConfirmationThreshold = 6

func checkCoopExitTxBroadcasted(ctx context.Context, db *ent.Tx, transfer *ent.Transfer) error {
	ctx, span := tracer.Start(ctx, "TransferHandler.checkCoopExitTxBroadcasted")
	defer span.End()

	coopExit, err := db.CooperativeExit.Query().Where(
		cooperativeexit.HasTransferWith(enttransfer.ID(transfer.ID)),
	).Only(ctx)
	if ent.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to find coop exit for transfer %s: %v", transfer.ID.String(), err)
	}

	transferLeaves, err := transfer.QueryTransferLeaves().All(ctx)
	if err != nil {
		return fmt.Errorf("failed to find leaves for transfer %s: %v", transfer.ID.String(), err)
	}
	// Leaf and tree are required to exist by our schema and
	// transfers must be initialized with at least 1 leaf
	tree := transferLeaves[0].QueryLeaf().QueryTree().OnlyX(ctx)

	blockHeight, err := db.BlockHeight.Query().Where(
		blockheight.NetworkEQ(tree.Network),
	).Only(ctx)
	if err != nil {
		return fmt.Errorf("failed to find block height: %v", err)
	}
	if coopExit.ConfirmationHeight == 0 {
		return errors.FailedPreconditionErrorf("coop exit tx hasn't been broadcasted")
	}
	if coopExit.ConfirmationHeight+CoopExitConfirmationThreshold-1 > blockHeight.Height {
		return errors.FailedPreconditionErrorf("coop exit tx doesn't have enough confirmations: confirmation height: %d current block height: %d", coopExit.ConfirmationHeight, blockHeight.Height)
	}
	return nil
}

// ClaimTransferTweakKeys starts claiming a pending transfer by tweaking keys of leaves.
func (h *TransferHandler) ClaimTransferTweakKeys(ctx context.Context, req *pb.ClaimTransferTweakKeysRequest) error {
	ctx, span := tracer.Start(ctx, "TransferHandler.ClaimTransferTweakKeys")
	defer span.End()

	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, req.OwnerIdentityPublicKey); err != nil {
		return err
	}

	transfer, err := h.loadTransfer(ctx, req.TransferId)
	if err != nil {
		return fmt.Errorf("unable to load transfer %s: %v", req.TransferId, err)
	}
	span.SetAttributes(transferTypeKey.String(string(transfer.Type)))
	if !bytes.Equal(transfer.ReceiverIdentityPubkey, req.OwnerIdentityPublicKey) {
		return fmt.Errorf("cannot claim transfer %s, receiver identity public key mismatch", req.TransferId)
	}
	if transfer.Status != schema.TransferStatusSenderKeyTweaked && transfer.Status != schema.TransferStatusReceiverKeyTweaked {
		return errors.FailedPreconditionErrorf("transfer cannot be claimed %s, status: %s", req.TransferId, transfer.Status)
	}

	db := ent.GetDbFromContext(ctx)
	if err := checkCoopExitTxBroadcasted(ctx, db, transfer); err != nil {
		return fmt.Errorf("failed to unlock transfer %s: %w", req.TransferId, err)
	}

	// Validate leaves count
	transferLeaves, err := transfer.QueryTransferLeaves().WithLeaf().All(ctx)
	if err != nil {
		return fmt.Errorf("unable to get transfer leaves for transfer %s: %v", req.TransferId, err)
	}
	if len(transferLeaves) != len(req.LeavesToReceive) {
		return fmt.Errorf("inconsistent leaves to claim for transfer %s", req.TransferId)
	}

	leafMap := make(map[string]*ent.TransferLeaf)
	for _, leaf := range transferLeaves {
		leafMap[leaf.Edges.Leaf.ID.String()] = leaf
	}

	// Store key tweaks
	for _, leafTweak := range req.LeavesToReceive {
		leaf, exists := leafMap[leafTweak.LeafId]
		if !exists {
			return fmt.Errorf("unexpected leaf id %s", leafTweak.LeafId)
		}
		leafTweakBytes, err := proto.Marshal(leafTweak)
		if err != nil {
			return fmt.Errorf("unable to marshal leaf tweak: %v", err)
		}
		leaf, err = leaf.Update().SetKeyTweak(leafTweakBytes).Save(ctx)
		if err != nil {
			return fmt.Errorf("unable to update leaf %s: %v", leaf.ID.String(), err)
		}
	}

	// Update transfer status
	_, err = transfer.Update().SetStatus(schema.TransferStatusReceiverKeyTweaked).Save(ctx)
	if err != nil {
		return fmt.Errorf("unable to update transfer status %s: %v", transfer.ID.String(), err)
	}

	return nil
}

func (h *TransferHandler) claimLeafTweakKey(ctx context.Context, leaf *ent.TreeNode, req *pb.ClaimLeafKeyTweak, ownerIdentityPubkey []byte) error {
	ctx, span := tracer.Start(ctx, "TransferHandler.claimLeafTweakKey")
	defer span.End()

	if req.SecretShareTweak == nil {
		return fmt.Errorf("secret share tweak is required")
	}
	if len(req.SecretShareTweak.SecretShare) == 0 {
		return fmt.Errorf("secret share is required")
	}
	err := secretsharing.ValidateShare(
		&secretsharing.VerifiableSecretShare{
			SecretShare: secretsharing.SecretShare{
				FieldModulus: secp256k1.S256().N,
				Threshold:    int(h.config.Threshold),
				Index:        big.NewInt(int64(h.config.Index + 1)),
				Share:        new(big.Int).SetBytes(req.SecretShareTweak.SecretShare),
			},
			Proofs: req.SecretShareTweak.Proofs,
		},
	)
	if err != nil {
		return fmt.Errorf("unable to validate share: %v", err)
	}

	if leaf.Status != schema.TreeNodeStatusTransferLocked {
		return fmt.Errorf("unable to transfer leaf %s", leaf.ID.String())
	}

	// Tweak keyshare
	keyshare, err := leaf.QuerySigningKeyshare().First(ctx)
	if err != nil {
		return fmt.Errorf("unable to load keyshare for leaf %s: %v", leaf.ID.String(), err)
	}
	keyshare, err = keyshare.TweakKeyShare(
		ctx,
		req.SecretShareTweak.SecretShare,
		req.SecretShareTweak.Proofs[0],
		req.PubkeySharesTweak,
	)
	if err != nil {
		return fmt.Errorf("unable to tweak keyshare %s for leaf %s: %v", keyshare.ID.String(), leaf.ID.String(), err)
	}

	signingPubkey, err := common.SubtractPublicKeys(leaf.VerifyingPubkey, keyshare.PublicKey)
	if err != nil {
		return fmt.Errorf("unable to calculate new signing pubkey for leaf %s: %v", req.LeafId, err)
	}
	_, err = leaf.
		Update().
		SetOwnerIdentityPubkey(ownerIdentityPubkey).
		SetOwnerSigningPubkey(signingPubkey).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("unable to update leaf %s: %v", req.LeafId, err)
	}
	return nil
}

func (h *TransferHandler) getLeavesFromTransfer(ctx context.Context, transfer *ent.Transfer) (*map[string]*ent.TreeNode, error) {
	ctx, span := tracer.Start(ctx, "TransferHandler.getLeavesFromTransfer", trace.WithAttributes(
		transferTypeKey.String(string(transfer.Type)),
	))
	defer span.End()

	transferLeaves, err := transfer.QueryTransferLeaves().WithLeaf().All(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get leaves for transfer %s: %v", transfer.ID.String(), err)
	}
	leaves := make(map[string]*ent.TreeNode)
	for _, transferLeaf := range transferLeaves {
		leaves[transferLeaf.Edges.Leaf.ID.String()] = transferLeaf.Edges.Leaf
	}
	return &leaves, nil
}

func (h *TransferHandler) ValidateKeyTweakProof(ctx context.Context, transferLeaves []*ent.TransferLeaf, keyTweakProofs map[string]*pb.SecretProof) error {
	ctx, span := tracer.Start(ctx, "TransferHandler.ValidateKeyTweakProof")
	defer span.End()

	for _, leaf := range transferLeaves {
		treeNode, err := leaf.QueryLeaf().Only(ctx)
		if err != nil {
			return fmt.Errorf("unable to get tree node for leaf %s: %v", leaf.ID.String(), err)
		}
		proof, exists := keyTweakProofs[treeNode.ID.String()]
		if !exists {
			return fmt.Errorf("key tweak proof for leaf %s not found", leaf.ID.String())
		}
		keyTweakProto := &pb.ClaimLeafKeyTweak{}
		err = proto.Unmarshal(leaf.KeyTweak, keyTweakProto)
		if err != nil {
			return fmt.Errorf("unable to unmarshal key tweak for leaf %s: %v", leaf.ID.String(), err)
		}
		for i, proof := range proof.Proofs {
			if !bytes.Equal(keyTweakProto.SecretShareTweak.Proofs[i], proof) {
				return fmt.Errorf("key tweak proof for leaf %s is invalid, the proof provided is not the same as key tweak proof. please check your implementation to see if you are claiming the same transfer multiple times at the same time", leaf.ID.String())
			}
		}
	}
	return nil
}

func (h *TransferHandler) revertClaimTransfer(ctx context.Context, transfer *ent.Transfer, transferLeaves []*ent.TransferLeaf) error {
	ctx, span := tracer.Start(ctx, "TransferHandler.revertClaimTransfer", trace.WithAttributes(
		transferTypeKey.String(string(transfer.Type)),
	))
	defer span.End()

	_, err := transfer.Update().SetStatus(schema.TransferStatusSenderKeyTweaked).Save(ctx)
	if err != nil {
		return fmt.Errorf("unable to update transfer status %s: %v", transfer.ID.String(), err)
	}
	for _, leaf := range transferLeaves {
		leaf, err := leaf.Update().SetKeyTweak(nil).Save(ctx)
		if err != nil {
			return fmt.Errorf("unable to update leaf %s: %v", leaf.ID.String(), err)
		}
	}
	return nil
}

func (h *TransferHandler) settleReceiverKeyTweak(ctx context.Context, transfer *ent.Transfer, keyTweakProofs map[string]*pb.SecretProof) error {
	ctx, span := tracer.Start(ctx, "TransferHandler.settleReceiverKeyTweak", trace.WithAttributes(
		transferTypeKey.String(string(transfer.Type)),
	))
	defer span.End()

	tweakKey := true
	if keyTweakProofs != nil {
		// Only validate key tweak proof if it is provided for backward compatibility.
		selection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}
		_, err := helper.ExecuteTaskWithAllOperators(ctx, h.config, &selection, func(ctx context.Context, operator *so.SigningOperator) (interface{}, error) {
			conn, err := operator.NewGRPCConnection()
			if err != nil {
				return nil, err
			}
			defer conn.Close()
			client := pbinternal.NewSparkInternalServiceClient(conn)
			return client.InitiateSettleReceiverKeyTweak(ctx, &pbinternal.InitiateSettleReceiverKeyTweakRequest{
				TransferId:     transfer.ID.String(),
				KeyTweakProofs: keyTweakProofs,
			})
		})
		if err != nil {
			tweakKey = false
		}
	}

	selection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionAll}
	_, err := helper.ExecuteTaskWithAllOperators(ctx, h.config, &selection, func(ctx context.Context, operator *so.SigningOperator) (interface{}, error) {
		conn, err := operator.NewGRPCConnection()
		if err != nil {
			return nil, err
		}
		defer conn.Close()
		client := pbinternal.NewSparkInternalServiceClient(conn)
		return client.SettleReceiverKeyTweak(ctx, &pbinternal.SettleReceiverKeyTweakRequest{
			TransferId: transfer.ID.String(),
			TweakKey:   tweakKey,
		})
	})
	if err != nil {
		// At this point, this is not recoverable. But this should not happen in theory.
		return fmt.Errorf("unable to settle receiver key tweak: %v", err)
	}
	if !tweakKey {
		return fmt.Errorf("unable to settle receiver key tweak: %v, you might have a race condition in your implementation", err)
	}
	return nil
}

// ClaimTransferSignRefunds signs new refund transactions as part of the transfer.
func (h *TransferHandler) ClaimTransferSignRefunds(ctx context.Context, req *pb.ClaimTransferSignRefundsRequest) (*pb.ClaimTransferSignRefundsResponse, error) {
	ctx, span := tracer.Start(ctx, "TransferHandler.ClaimTransferSignRefunds")
	defer span.End()

	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, req.OwnerIdentityPublicKey); err != nil {
		return nil, err
	}

	transfer, err := h.loadTransferWithoutUpdate(ctx, req.TransferId)
	if err != nil {
		return nil, fmt.Errorf("unable to load transfer %s: %v", req.TransferId, err)
	}
	span.SetAttributes(transferTypeKey.String(string(transfer.Type)))
	if !bytes.Equal(transfer.ReceiverIdentityPubkey, req.OwnerIdentityPublicKey) {
		return nil, fmt.Errorf("cannot claim transfer %s, receiver identity public key mismatch", req.TransferId)
	}

	switch transfer.Status {
	case schema.TransferStatusReceiverKeyTweaked:
	case schema.TransferStatusReceiverRefundSigned:
	case schema.TransferStatusReceiverKeyTweakApplied:
		// do nothing
	default:
		return nil, fmt.Errorf("transfer %s is expected to be at status TransferStatusKeyTweaked or TransferStatusReceiverRefundSigned but %s found", req.TransferId, transfer.Status)
	}

	// Validate leaves count
	leavesToTransfer, err := transfer.QueryTransferLeaves().All(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to load leaves to transfer for transfer %s: %v", req.TransferId, err)
	}
	if len(leavesToTransfer) != len(req.SigningJobs) {
		return nil, fmt.Errorf("inconsistent leaves to claim for transfer %s", req.TransferId)
	}

	leaves, err := h.getLeavesFromTransfer(ctx, transfer)
	if err != nil {
		return nil, err
	}

	if transfer.Status != schema.TransferStatusReceiverRefundSigned {
		err = h.settleReceiverKeyTweak(ctx, transfer, req.KeyTweakProofs)
		if err != nil {
			return nil, fmt.Errorf("unable to settle receiver key tweak: %v", err)
		}
	}

	// Update transfer status.
	_, err = transfer.Update().SetStatus(schema.TransferStatusReceiverRefundSigned).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to update transfer status %s: %v", transfer.ID.String(), err)
	}

	signingJobs := []*helper.SigningJob{}
	jobToLeafMap := make(map[string]uuid.UUID)
	for _, job := range req.SigningJobs {
		leaf, exists := (*leaves)[job.LeafId]
		if !exists {
			return nil, fmt.Errorf("unexpected leaf id %s", job.LeafId)
		}

		leaf, err := leaf.Update().SetRawRefundTx(job.RefundTxSigningJob.RawTx).Save(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to update leaf refund tx %s: %v", leaf.ID.String(), err)
		}

		signingJob, err := h.getRefundTxSigningJob(ctx, leaf, job.RefundTxSigningJob)
		if err != nil {
			return nil, fmt.Errorf("unable to create signing job for leaf %s: %v", leaf.ID.String(), err)
		}
		signingJobs = append(signingJobs, signingJob)
		jobToLeafMap[signingJob.JobID] = leaf.ID
	}

	// Signing
	signingResults, err := helper.SignFrost(ctx, h.config, signingJobs)
	if err != nil {
		return nil, err
	}
	signingResultProtos := []*pb.LeafRefundTxSigningResult{}
	for _, signingResult := range signingResults {
		leafID := jobToLeafMap[signingResult.JobID]
		leaf := (*leaves)[leafID.String()]
		signingResultProto, err := signingResult.MarshalProto()
		if err != nil {
			return nil, err
		}
		signingResultProtos = append(signingResultProtos, &pb.LeafRefundTxSigningResult{
			LeafId:                leafID.String(),
			RefundTxSigningResult: signingResultProto,
			VerifyingKey:          leaf.VerifyingPubkey,
		})
	}

	return &pb.ClaimTransferSignRefundsResponse{SigningResults: signingResultProtos}, nil
}

func (h *TransferHandler) getRefundTxSigningJob(ctx context.Context, leaf *ent.TreeNode, job *pb.SigningJob) (*helper.SigningJob, error) {
	ctx, span := tracer.Start(ctx, "TransferHandler.getRefundTxSigningJob")
	defer span.End()

	keyshare, err := leaf.QuerySigningKeyshare().First(ctx)
	if err != nil || keyshare == nil {
		return nil, fmt.Errorf("unable to load keyshare for leaf %s: %v", leaf.ID.String(), err)
	}
	leafTx, err := common.TxFromRawTxBytes(leaf.RawTx)
	if err != nil {
		return nil, fmt.Errorf("unable to load leaf tx for leaf %s: %v", leaf.ID.String(), err)
	}
	if len(leafTx.TxOut) <= 0 {
		return nil, fmt.Errorf("vout out of bounds")
	}
	refundSigningJob, _, err := helper.NewSigningJob(keyshare, job, leafTx.TxOut[0], nil)
	if err != nil {
		return nil, fmt.Errorf("unable to create signing job for leaf %s: %v", leaf.ID.String(), err)
	}
	return refundSigningJob, nil
}

func (h *TransferHandler) InitiateSettleReceiverKeyTweak(ctx context.Context, req *pbinternal.InitiateSettleReceiverKeyTweakRequest) error {
	ctx, span := tracer.Start(ctx, "TransferHandler.InitiateSettleReceiverKeyTweak")
	defer span.End()

	transfer, err := h.loadTransfer(ctx, req.TransferId)
	if err != nil {
		return fmt.Errorf("unable to load transfer %s: %v", req.TransferId, err)
	}
	span.SetAttributes(transferTypeKey.String(string(transfer.Type)))

	if transfer.Status != schema.TransferStatusReceiverKeyTweaked {
		return fmt.Errorf("transfer %s is expected to be at status TransferStatusReceiverKeyTweaked but %s found", req.TransferId, transfer.Status)
	}

	leaves, err := transfer.QueryTransferLeaves().All(ctx)
	if err != nil {
		return fmt.Errorf("unable to get leaves from transfer %s: %v", req.TransferId, err)
	}

	err = h.ValidateKeyTweakProof(ctx, leaves, req.KeyTweakProofs)
	if err != nil {
		return fmt.Errorf("unable to validate key tweak proof: %v", err)
	}

	_, err = transfer.Update().SetStatus(schema.TransferStatusReceiverKeyTweakLocked).Save(ctx)
	if err != nil {
		return fmt.Errorf("unable to update transfer status %s: %v", transfer.ID.String(), err)
	}

	return nil
}

func (h *TransferHandler) SettleReceiverKeyTweak(ctx context.Context, req *pbinternal.SettleReceiverKeyTweakRequest) error {
	ctx, span := tracer.Start(ctx, "TransferHandler.SettleReceiverKeyTweak")
	defer span.End()

	transfer, err := h.loadTransfer(ctx, req.TransferId)
	if err != nil {
		return fmt.Errorf("unable to load transfer %s: %v", req.TransferId, err)
	}
	span.SetAttributes(transferTypeKey.String(string(transfer.Type)))

	leaves, err := transfer.QueryTransferLeaves().All(ctx)
	if err != nil {
		return fmt.Errorf("unable to get leaves from transfer %s: %v", req.TransferId, err)
	}

	if req.TweakKey {
		for _, leaf := range leaves {
			treeNode, err := leaf.QueryLeaf().Only(ctx)
			if err != nil {
				return fmt.Errorf("unable to get tree node for leaf %s: %v", leaf.ID.String(), err)
			}
			if len(leaf.KeyTweak) == 0 {
				return fmt.Errorf("key tweak for leaf %s is not set", leaf.ID.String())
			}
			keyTweakProto := &pb.ClaimLeafKeyTweak{}
			err = proto.Unmarshal(leaf.KeyTweak, keyTweakProto)
			if err != nil {
				return fmt.Errorf("unable to unmarshal key tweak for leaf %s: %v", leaf.ID.String(), err)
			}
			err = h.claimLeafTweakKey(ctx, treeNode, keyTweakProto, transfer.ReceiverIdentityPubkey)
			if err != nil {
				return fmt.Errorf("unable to claim leaf tweak key for leaf %s: %v", leaf.ID.String(), err)
			}
			_, err = leaf.Update().SetKeyTweak(nil).Save(ctx)
			if err != nil {
				return fmt.Errorf("unable to update leaf key tweak %s: %v", leaf.ID.String(), err)
			}
		}
		_, err = transfer.Update().SetStatus(schema.TransferStatusReceiverKeyTweakApplied).Save(ctx)
		if err != nil {
			return fmt.Errorf("unable to update transfer status %s: %v", transfer.ID.String(), err)
		}
	} else {
		return h.revertClaimTransfer(ctx, transfer, leaves)
	}

	return nil
}
