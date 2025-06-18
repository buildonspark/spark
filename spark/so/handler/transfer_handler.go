package handler

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/logging"
	secretsharing "github.com/lightsparkdev/spark/common/secret_sharing"
	pbfrost "github.com/lightsparkdev/spark/proto/frost"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authz"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/blockheight"
	"github.com/lightsparkdev/spark/so/ent/cooperativeexit"
	"github.com/lightsparkdev/spark/so/ent/predicate"
	"github.com/lightsparkdev/spark/so/ent/preimagerequest"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	enttransfer "github.com/lightsparkdev/spark/so/ent/transfer"
	enttransferleaf "github.com/lightsparkdev/spark/so/ent/transferleaf"
	enttree "github.com/lightsparkdev/spark/so/ent/tree"
	"github.com/lightsparkdev/spark/so/ent/treenode"
	enttreenode "github.com/lightsparkdev/spark/so/ent/treenode"
	"github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/helper"
	"github.com/lightsparkdev/spark/so/objects"
	events "github.com/lightsparkdev/spark/so/stream"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

// TransferHandler is a helper struct to handle leaves transfer request.
type TransferHandler struct {
	BaseTransferHandler
	config     *so.Config
	mockAction *common.MockAction
}

var transferTypeKey = attribute.Key("transfer_type")

// NewTransferHandler creates a new TransferHandler.
func NewTransferHandler(config *so.Config) *TransferHandler {
	return &TransferHandler{BaseTransferHandler: NewBaseTransferHandler(config), config: config}
}

func (h *TransferHandler) SetMockAction(mockAction *common.MockAction) {
	h.mockAction = mockAction
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
func (h *TransferHandler) startTransferInternal(ctx context.Context, req *pb.StartTransferRequest, transferType st.TransferType, adaptorPubKey []byte) (*pb.StartTransferResponse, error) {
	logger := logging.GetLoggerFromContext(ctx)

	ctx, span := tracer.Start(ctx, "TransferHandler.startTransferInternal", trace.WithAttributes(
		transferTypeKey.String(string(transferType)),
	))
	defer span.End()

	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, req.OwnerIdentityPublicKey); err != nil {
		return nil, err
	}

	leafTweakMap, err := h.validateTransferPackage(ctx, req.TransferId, req.TransferPackage, req.OwnerIdentityPublicKey)
	if err != nil {
		return nil, fmt.Errorf("failed to validate transfer package for request %s: %w", logging.FormatProto("start_transfer_request", req), err)
	}

	leafRefundMap := h.loadLeafRefundMap(req)
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
		return nil, fmt.Errorf("failed to create transfer for request %s: %w", logging.FormatProto("start_transfer_request", req), err)
	}

	var signingResults []*pb.LeafRefundTxSigningResult
	var finalSignatureMap map[string][]byte
	if req.TransferPackage == nil {
		signingResults, err = signRefunds(ctx, h.config, req, leafMap, adaptorPubKey)
		if err != nil {
			return nil, fmt.Errorf("failed to sign refunds for request %s: %w", logging.FormatProto("start_transfer_request", req), err)
		}
	} else {
		signingResultMap, err := signRefundsWithPregeneratedNonce(ctx, h.config, req, leafMap, adaptorPubKey)
		if err != nil {
			return nil, err
		}
		finalSignatureMap, err = aggregateSignatures(ctx, h.config, req, adaptorPubKey, signingResultMap, leafMap)
		if err != nil {
			return nil, err
		}
		// Update the leaves with the final signatures
		err = h.updateTransferLeavesSignatures(ctx, transfer, finalSignatureMap)
		if err != nil {
			return nil, err
		}
		for leafID, signingResult := range signingResultMap {
			signingResultProto, err := signingResult.MarshalProto()
			if err != nil {
				return nil, fmt.Errorf("unable to marshal signing result: %w", err)
			}
			signingResults = append(signingResults, &pb.LeafRefundTxSigningResult{
				LeafId:                leafID,
				RefundTxSigningResult: signingResultProto,
				VerifyingKey:          leafMap[leafID].VerifyingPubkey,
			})
		}
	}

	// This call to other SOs will check the validity of the transfer package. If no error is
	// returned, it means the transfer package is valid and the transfer is considered sent.
	err = h.syncTransferInit(ctx, req, transferType, finalSignatureMap)
	if err != nil {
		err = h.CreateCancelTransferGossipMessage(ctx, req.TransferId)
		if err != nil {
			logger.Error("failed to create cancel transfer gossip message", "error", err, "transfer_id", req.TransferId)
		}
		logger.Error("failed to sync transfer init for request %s: %w", logging.FormatProto("start_transfer_request", req), err)
		return nil, fmt.Errorf("failed to sync transfer init for request %s: %w", logging.FormatProto("start_transfer_request", req), err)
	}

	// After this point, the transfer send is considered successful.

	if req.TransferPackage != nil {
		// If all other SOs have settled the sender key tweaks, we can commit the sender key tweaks.
		// If there's any error, it means one or more of the SOs are down at the time, we will have a
		// cron job to retry the key commit.
		err = h.settleSenderKeyTweaks(ctx, req.TransferId, pbinternal.SettleKeyTweakAction_COMMIT)
		if err == nil {
			keyTweakProofMap := make(map[string]*pb.SecretProof)
			for _, leaf := range leafTweakMap {
				keyTweakProofMap[leaf.LeafId] = &pb.SecretProof{
					Proofs: leaf.SecretShareTweak.Proofs,
				}
			}

			sendGossipHandler := NewSendGossipHandler(h.config)
			selection := helper.OperatorSelection{
				Option: helper.OperatorSelectionOptionExcludeSelf,
			}
			participants, err := selection.OperatorIdentifierList(h.config)
			if err != nil {
				return nil, fmt.Errorf("unable to get operator list: %w", err)
			}
			_, err = sendGossipHandler.CreateAndSendGossipMessage(ctx, &pbgossip.GossipMessage{
				Message: &pbgossip.GossipMessage_SettleSenderKeyTweak{
					SettleSenderKeyTweak: &pbgossip.GossipMessageSettleSenderKeyTweak{
						TransferId:           req.TransferId,
						SenderKeyTweakProofs: keyTweakProofMap,
					},
				},
			}, participants)
			if err != nil {
				logger.Error("failed to create and send gossip message to settle sender key tweak", "error", err, "transfer_id", req.TransferId)
				return nil, fmt.Errorf("failed to create and send gossip message to settle sender key tweak: %w", err)
			}
		}
		transfer, err = h.loadTransfer(ctx, req.TransferId)
		if err != nil {
			return nil, fmt.Errorf("unable to load transfer: %w", err)
		}
	}

	transferProto, err := transfer.MarshalProto(ctx)
	if err != nil {
		logger.Error("unable to marshal transfer", "error", err, "transfer_id", transfer.ID)
	}

	return &pb.StartTransferResponse{Transfer: transferProto, SigningResults: signingResults}, nil
}

func (h *TransferHandler) updateTransferLeavesSignatures(ctx context.Context, transfer *ent.Transfer, finalSignatureMap map[string][]byte) error {
	transferLeaves, err := transfer.QueryTransferLeaves().WithLeaf().All(ctx)
	if err != nil {
		return fmt.Errorf("unable to get transfer leaves: %w", err)
	}
	for _, leaf := range transferLeaves {
		updatedTx, err := common.UpdateTxWithSignature(leaf.IntermediateRefundTx, 0, finalSignatureMap[leaf.Edges.Leaf.ID.String()])
		if err != nil {
			return fmt.Errorf("unable to update leaf signature: %w", err)
		}

		refundTx, err := common.TxFromRawTxBytes(updatedTx)
		if err != nil {
			return fmt.Errorf("unable to get refund tx: %w", err)
		}
		nodeTx, err := common.TxFromRawTxBytes(leaf.Edges.Leaf.RawTx)
		if err != nil {
			return fmt.Errorf("unable to get node tx: %w", err)
		}
		err = common.VerifySignatureSingleInput(refundTx, 0, nodeTx.TxOut[0])
		if err != nil {
			return fmt.Errorf("unable to verify leaf signature: %w", err)
		}

		_, err = leaf.Update().SetIntermediateRefundTx(updatedTx).Save(ctx)
		if err != nil {
			return fmt.Errorf("unable to save leaf: %w", err)
		}
	}
	return nil
}

// settleSenderKeyTweaks calls the other SOs to settle the sender key tweaks.
func (h *TransferHandler) settleSenderKeyTweaks(ctx context.Context, transferID string, action pbinternal.SettleKeyTweakAction) error {
	operatorSelection := helper.OperatorSelection{
		Option: helper.OperatorSelectionOptionExcludeSelf,
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
	return h.startTransferInternal(ctx, req, st.TransferTypeTransfer, nil)
}

func (h *TransferHandler) StartLeafSwap(ctx context.Context, req *pb.StartTransferRequest) (*pb.StartTransferResponse, error) {
	return h.startTransferInternal(ctx, req, st.TransferTypeSwap, nil)
}

// CounterLeafSwap initiates a leaf swap for the other side, signing refunds with an adaptor public key.
func (h *TransferHandler) CounterLeafSwap(ctx context.Context, req *pb.CounterLeafSwapRequest) (*pb.CounterLeafSwapResponse, error) {
	startTransferResponse, err := h.startTransferInternal(ctx, req.Transfer, st.TransferTypeCounterSwap, req.AdaptorPublicKey)
	if err != nil {
		return nil, fmt.Errorf("failed to start counter leaf swap for request %s: %w", logging.FormatProto("counter_leaf_swap_request", req), err)
	}
	return &pb.CounterLeafSwapResponse{Transfer: startTransferResponse.Transfer, SigningResults: startTransferResponse.SigningResults}, nil
}

func (h *TransferHandler) syncTransferInit(ctx context.Context, req *pb.StartTransferRequest, transferType st.TransferType, refundSignatures map[string][]byte) error {
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
		return fmt.Errorf("unable to get transfer type proto: %w", err)
	}
	initTransferRequest := &pbinternal.InitiateTransferRequest{
		TransferId:                req.TransferId,
		SenderIdentityPublicKey:   req.OwnerIdentityPublicKey,
		ReceiverIdentityPublicKey: req.ReceiverIdentityPublicKey,
		ExpiryTime:                req.ExpiryTime,
		Leaves:                    leaves,
		Type:                      *transferTypeProto,
		TransferPackage:           req.TransferPackage,
		RefundSignatures:          refundSignatures,
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

func (h *TransferHandler) syncDeliverSenderKeyTweak(ctx context.Context, req *pb.FinalizeTransferWithTransferPackageRequest, transferType st.TransferType) error {
	ctx, span := tracer.Start(ctx, "TransferHandler.syncDeliverSenderKeyTweak", trace.WithAttributes(
		transferTypeKey.String(string(transferType)),
	))
	defer span.End()
	if req.TransferPackage == nil {
		return fmt.Errorf("expected transfer package to be populated")
	}
	deliverSenderKeyTweakRequest := &pbinternal.DeliverSenderKeyTweakRequest{
		TransferId:              req.TransferId,
		SenderIdentityPublicKey: req.OwnerIdentityPublicKey,
		TransferPackage:         req.TransferPackage,
	}
	selection := helper.OperatorSelection{
		Option: helper.OperatorSelectionOptionExcludeSelf,
	}
	_, err := helper.ExecuteTaskWithAllOperators(ctx, h.config, &selection, func(ctx context.Context, operator *so.SigningOperator) (interface{}, error) {
		conn, err := operator.NewGRPCConnection()
		if err != nil {
			return nil, err
		}
		defer conn.Close()

		client := pbinternal.NewSparkInternalServiceClient(conn)
		return client.DeliverSenderKeyTweak(ctx, deliverSenderKeyTweakRequest)
	})
	return err
}

func signRefunds(ctx context.Context, config *so.Config, requests *pb.StartTransferRequest, leafMap map[string]*ent.TreeNode, adaptorPubKey []byte) ([]*pb.LeafRefundTxSigningResult, error) {
	ctx, span := tracer.Start(ctx, "TransferHandler.signRefunds")
	defer span.End()

	if requests.TransferPackage != nil {
		return nil, fmt.Errorf("transfer package is not nil, should call signRefundsWithPregeneratedNonce instead")
	}

	leafJobMap := make(map[string]*ent.TreeNode)
	var signingResults []*helper.SigningResult

	signingJobs := make([]*helper.SigningJob, 0)
	for _, req := range requests.LeavesToSend {
		leaf := leafMap[req.LeafId]
		refundTx, err := common.TxFromRawTxBytes(req.RefundTxSigningJob.RawTx)
		if err != nil {
			return nil, fmt.Errorf("unable to load new refund tx: %w", err)
		}

		leafTx, err := common.TxFromRawTxBytes(leaf.RawTx)
		if err != nil {
			return nil, fmt.Errorf("unable to load leaf tx: %w", err)
		}
		if len(leafTx.TxOut) <= 0 {
			return nil, fmt.Errorf("vout out of bounds")
		}
		refundTxSigHash, err := common.SigHashFromTx(refundTx, 0, leafTx.TxOut[0])
		if err != nil {
			return nil, fmt.Errorf("unable to calculate sighash from refund tx: %w", err)
		}

		userNonceCommitment, err := objects.NewSigningCommitment(req.RefundTxSigningJob.SigningNonceCommitment.Binding, req.RefundTxSigningJob.SigningNonceCommitment.Hiding)
		if err != nil {
			return nil, fmt.Errorf("unable to create signing commitment: %w", err)
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
	var err error
	signingResults, err = helper.SignFrost(ctx, config, signingJobs)
	if err != nil {
		return nil, fmt.Errorf("unable to sign frost: %w", err)
	}

	pbSigningResults := make([]*pb.LeafRefundTxSigningResult, 0)
	for _, signingResult := range signingResults {
		leaf := leafJobMap[signingResult.JobID]
		signingResultProto, err := signingResult.MarshalProto()
		if err != nil {
			return nil, fmt.Errorf("unable to marshal signing result: %w", err)
		}
		pbSigningResults = append(pbSigningResults, &pb.LeafRefundTxSigningResult{
			LeafId:                leaf.ID.String(),
			RefundTxSigningResult: signingResultProto,
			VerifyingKey:          leaf.VerifyingPubkey,
		})
	}
	return pbSigningResults, nil
}

func signRefundsWithPregeneratedNonce(
	ctx context.Context,
	config *so.Config,
	requests *pb.StartTransferRequest,
	leafMap map[string]*ent.TreeNode,
	adaptorPubKey []byte,
) (map[string]*helper.SigningResult, error) {
	ctx, span := tracer.Start(ctx, "TransferHandler.signRefunds")
	defer span.End()

	leafJobMap := make(map[string]*ent.TreeNode)

	if requests.TransferPackage == nil {
		return nil, fmt.Errorf("transfer package is nil")
	}

	signingJobs := make([]*helper.SigningJobWithPregeneratedNonce, 0)
	for _, req := range requests.TransferPackage.LeavesToSend {
		leaf := leafMap[req.LeafId]
		refundTx, err := common.TxFromRawTxBytes(req.RawTx)
		if err != nil {
			return nil, fmt.Errorf("unable to load new refund tx: %w", err)
		}

		leafTx, err := common.TxFromRawTxBytes(leaf.RawTx)
		if err != nil {
			return nil, fmt.Errorf("unable to load leaf tx: %w", err)
		}
		if len(leafTx.TxOut) <= 0 {
			return nil, fmt.Errorf("vout out of bounds")
		}
		refundTxSigHash, err := common.SigHashFromTx(refundTx, 0, leafTx.TxOut[0])
		if err != nil {
			return nil, fmt.Errorf("unable to calculate sighash from refund tx: %w", err)
		}

		userNonceCommitment := objects.SigningCommitment{}
		err = userNonceCommitment.UnmarshalProto(req.SigningNonceCommitment)
		if err != nil {
			return nil, fmt.Errorf("unable to unmarshal signing nonce commitment: %w", err)
		}
		jobID := uuid.New().String()
		signingKeyshare, err := leaf.QuerySigningKeyshare().Only(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get signing keyshare id: %w", err)
		}

		round1Packages := make(map[string]objects.SigningCommitment)
		for key, commitment := range req.SigningCommitments.SigningCommitments {
			obj := objects.SigningCommitment{}
			err = obj.UnmarshalProto(commitment)
			if err != nil {
				return nil, fmt.Errorf("unable to unmarshal signing commitment: %w", err)
			}
			round1Packages[key] = obj
		}
		signingJobs = append(
			signingJobs,
			&helper.SigningJobWithPregeneratedNonce{
				SigningJob: helper.SigningJob{
					JobID:             jobID,
					SigningKeyshareID: signingKeyshare.ID,
					Message:           refundTxSigHash,
					VerifyingKey:      leaf.VerifyingPubkey,
					UserCommitment:    &userNonceCommitment,
					AdaptorPublicKey:  adaptorPubKey,
				},
				Round1Packages: round1Packages,
			},
		)
		leafJobMap[jobID] = leaf
	}
	signingResults, err := helper.SignFrostWithPregeneratedNonce(ctx, config, signingJobs)
	if err != nil {
		return nil, fmt.Errorf("unable to sign frost: %w", err)
	}

	results := make(map[string]*helper.SigningResult)
	for _, signingResult := range signingResults {
		leaf := leafJobMap[signingResult.JobID]
		results[leaf.ID.String()] = signingResult
	}
	return results, nil
}

func aggregateSignatures(
	ctx context.Context,
	config *so.Config,
	req *pb.StartTransferRequest,
	adaptorPubKey []byte,
	signingResultMap map[string]*helper.SigningResult,
	leafMap map[string]*ent.TreeNode,
) (map[string][]byte, error) {
	finalSignatureMap := make(map[string][]byte)
	frostConn, err := common.NewGRPCConnectionWithoutTLS(config.SignerAddress, nil)
	if err != nil {
		return nil, fmt.Errorf("unable to connect to frost: %w", err)
	}
	defer frostConn.Close()
	frostClient := pbfrost.NewFrostServiceClient(frostConn)
	userSignedRefunds := req.TransferPackage.LeavesToSend
	userRefundMap := make(map[string]*pb.UserSignedTxSigningJob)
	for _, userSignedRefund := range userSignedRefunds {
		userRefundMap[userSignedRefund.LeafId] = userSignedRefund
	}
	logger := logging.GetLoggerFromContext(ctx)
	for leafID, signingResult := range signingResultMap {
		logger.Info("aggregating frost signature", "leaf_id", leafID, "signing_msg", hex.EncodeToString(signingResult.Message))
		userSignedRefund := userRefundMap[leafID]
		leaf := leafMap[leafID]
		signatureResult, err := frostClient.AggregateFrost(ctx, &pbfrost.AggregateFrostRequest{
			Message:            signingResult.Message,
			SignatureShares:    signingResult.SignatureShares,
			PublicShares:       signingResult.PublicKeys,
			VerifyingKey:       leaf.VerifyingPubkey,
			Commitments:        userSignedRefund.SigningCommitments.SigningCommitments,
			UserCommitments:    userSignedRefund.SigningNonceCommitment,
			UserPublicKey:      leaf.OwnerSigningPubkey,
			UserSignatureShare: userSignedRefund.UserSignature,
			AdaptorPublicKey:   adaptorPubKey,
		})
		if err != nil {
			logger.Error("unable to aggregate frost", "error", err, "leaf_id", leaf.ID)
			return nil, fmt.Errorf("unable to aggregate frost: %w, leaf_id: %s", err, leaf.ID)
		}
		finalSignatureMap[leaf.ID.String()] = signatureResult.Signature
	}
	return finalSignatureMap, nil
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
		return nil, fmt.Errorf("unable to load transfer %s: %w", req.TransferId, err)
	}
	span.SetAttributes(transferTypeKey.String(string(transfer.Type)))
	if !bytes.Equal(transfer.SenderIdentityPubkey, req.OwnerIdentityPublicKey) || transfer.Status != st.TransferStatusSenderInitiated {
		return nil, fmt.Errorf("send transfer cannot be completed %s, status: %s", req.TransferId, transfer.Status)
	}

	db := ent.GetDbFromContext(ctx)
	shouldTweakKey := true
	switch transfer.Type {
	case st.TransferTypePreimageSwap:
		preimageRequest, err := db.PreimageRequest.Query().Where(preimagerequest.HasTransfersWith(enttransfer.ID(transfer.ID))).Only(ctx)
		if err != nil || preimageRequest == nil {
			return nil, fmt.Errorf("unable to find preimage request for transfer %s: %w", transfer.ID.String(), err)
		}
		shouldTweakKey = preimageRequest.Status == st.PreimageRequestStatusPreimageShared
	case st.TransferTypeCooperativeExit:
		err = checkCoopExitTxBroadcasted(ctx, db, transfer)
		shouldTweakKey = err == nil
	default:
		// do nothing
	}

	for _, leaf := range req.LeavesToSend {
		err = h.completeSendLeaf(ctx, transfer, leaf, shouldTweakKey)
		if err != nil {
			return nil, fmt.Errorf("unable to complete send leaf transfer for leaf %s: %w", leaf.LeafId, err)
		}
	}

	// Update transfer status
	statusToSet := st.TransferStatusSenderKeyTweaked
	if !shouldTweakKey {
		statusToSet = st.TransferStatusSenderKeyTweakPending
	}
	transfer, err = transfer.Update().SetStatus(statusToSet).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to update transfer status %s: %w", transfer.ID.String(), err)
	}
	transferProto, err := transfer.MarshalProto(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to marshal transfer: %w", err)
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

func (h *TransferHandler) FinalizeTransferWithTransferPackage(ctx context.Context, req *pb.FinalizeTransferWithTransferPackageRequest) (*pb.FinalizeTransferResponse, error) {
	transfer, err := h.loadTransfer(ctx, req.TransferId)
	if err != nil {
		return nil, err
	}
	if transfer.Status != st.TransferStatusSenderInitiated {
		return nil, fmt.Errorf("transfer %s is in state %s; expected sender initiated status", req.TransferId, transfer.Status)
	}
	err = h.syncDeliverSenderKeyTweak(ctx, req, transfer.Type)
	if err != nil {
		logger := logging.GetLoggerFromContext(ctx)
		// Counterswaps are from the SSP. We need to allow SSP to
		// perform retries, so don't cancel the transfer, just reset it
		if transfer.Type == st.TransferTypeCounterSwap {
			rollbackErr := h.CreateRollbackTransferGossipMessage(ctx, req.TransferId)
			if rollbackErr != nil {
				logger.Error("Error when rolling back sender key tweaks", "error", rollbackErr, "transfer_id", req.TransferId)
			}
		} else {
			cancelErr := h.CreateCancelTransferGossipMessage(ctx, req.TransferId)
			if cancelErr != nil {
				logger.Error("Error when canceling transfer", "error", cancelErr, "transfer_id", req.TransferId)
			}
		}
		errorMsg := fmt.Sprintf("failed to sync deliver sender key tweak for request %s", logging.FormatProto("finalize_transfer_with_transfer_package", req))
		if st, ok := status.FromError(err); ok && st.Code() == codes.Unavailable {
			return nil, errors.UnavailableErrorf("%s: %w", errorMsg, err)
		}
		return nil, fmt.Errorf("%s: %w", errorMsg, err)
	}

	transfer, err = transfer.Update().SetStatus(st.TransferStatusSenderInitiatedCoordinator).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to update status of transfer %s: %w", req.TransferId, err)
	}

	err = h.settleSenderKeyTweaks(ctx, req.TransferId, pbinternal.SettleKeyTweakAction_COMMIT)
	if err != nil {
		return nil, err
	}

	err = h.setSoCoordinatorKeyTweaks(ctx, transfer, req.TransferPackage, req.OwnerIdentityPublicKey)
	if err != nil {
		return nil, err
	}

	transfer, err = h.commitSenderKeyTweaks(ctx, transfer, true)
	if err != nil {
		// Too bad, at this point there's a bug where all other SOs has tweaked the key but
		// the coordinator failed so the fund is lost.
		return nil, err
	}

	transferProto, err := transfer.MarshalProto(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to marshal transfer: %w", err)
	}
	return &pb.FinalizeTransferResponse{Transfer: transferProto}, err
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
		return fmt.Errorf("unable to validate share: %w", err)
	}

	// TODO (zhen): Verify possession

	// Find leaves in db
	leafID, err := uuid.Parse(req.LeafId)
	if err != nil {
		return fmt.Errorf("unable to parse leaf_id %s: %w", req.LeafId, err)
	}

	db := ent.GetDbFromContext(ctx)
	leaf, err := db.TreeNode.Get(ctx, leafID)
	if err != nil || leaf == nil {
		return fmt.Errorf("unable to find leaf %s: %w", req.LeafId, err)
	}
	if leaf.Status != st.TreeNodeStatusTransferLocked ||
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
		return fmt.Errorf("unable to get transfer leaf %s: %w", req.LeafId, err)
	}

	// Optional verify if the sender key tweak proof is the same as the one in previous call.
	if transferLeaf.SenderKeyTweakProof != nil {
		proof := &pb.SecretProof{}
		err = proto.Unmarshal(transferLeaf.SenderKeyTweakProof, proof)
		if err != nil {
			return fmt.Errorf("unable to unmarshal sender key tweak proof: %w", err)
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
		return fmt.Errorf("unable to update refund tx with signature: %w", err)
	}

	if transfer.Type != st.TransferTypePreimageSwap && transfer.Type != st.TransferTypeUtxoSwap {
		// Verify signature
		refundTx, err := common.TxFromRawTxBytes(refundTxBytes)
		if err != nil {
			return fmt.Errorf("unable to deserialize refund tx: %w", err)
		}
		leafNodeTx, err := common.TxFromRawTxBytes(leaf.RawTx)
		if err != nil {
			return fmt.Errorf("unable to deserialize leaf tx: %w", err)
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
			return fmt.Errorf("unable to verify refund tx signature: %w", err)
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
			return fmt.Errorf("unable to marshal key tweak: %w", err)
		}
		transferLeafMutator.SetKeyTweak(keyTweak)
	}
	_, err = transferLeafMutator.Save(ctx)
	if err != nil {
		return fmt.Errorf("unable to update transfer leaf: %w", err)
	}

	if shouldTweakKey {
		err = helper.TweakLeafKey(ctx, leaf, req, refundTxBytes)
		if err != nil {
			return fmt.Errorf("unable to tweak leaf key: %w", err)
		}
	}

	return nil
}

func (h *TransferHandler) queryTransfers(ctx context.Context, filter *pb.TransferFilter, isPending bool) (*pb.QueryTransfersResponse, error) {
	ctx, span := tracer.Start(ctx, "TransferHandler.queryTransfers")
	defer span.End()

	db := ent.GetDbFromContext(ctx)

	if isPending && len(filter.Statuses) > 0 {
		return nil, fmt.Errorf("cannot specify both isPending=true and filter.Statuses")
	}

	var transferPredicate []predicate.Transfer

	receiverPendingStatuses := []st.TransferStatus{
		st.TransferStatusSenderKeyTweaked,
		st.TransferStatusReceiverKeyTweaked,
		st.TransferStatusReceiverKeyTweakLocked,
		st.TransferStatusReceiverKeyTweakApplied,
		st.TransferStatusReceiverRefundSigned,
	}
	senderPendingStatuses := []st.TransferStatus{
		st.TransferStatusSenderKeyTweakPending,
		st.TransferStatusSenderInitiated,
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
				return nil, fmt.Errorf("unable to parse transfer id as a uuid %s: %w", transferID, err)
			}
			transferUUIDs = append(transferUUIDs, transferUUID)
		}
		transferPredicate = append([]predicate.Transfer{enttransfer.IDIn(transferUUIDs...)}, transferPredicate...)
	}

	if len(filter.Types) > 0 {
		transferTypes := make([]st.TransferType, len(filter.Types))
		for i, transferType := range filter.Types {
			transferTypes[i] = st.TransferType(transferType.String())
		}
		transferPredicate = append(transferPredicate, enttransfer.TypeIn(transferTypes...))
	}

	var network st.Network
	if filter.GetNetwork() == pb.Network_UNSPECIFIED {
		network = st.NetworkMainnet
	} else {
		var err error
		network, err = common.SchemaNetworkFromProtoNetwork(filter.GetNetwork())
		if err != nil {
			return nil, fmt.Errorf("failed to convert proto network to schema network: %w", err)
		}
	}
	transferPredicate = append(transferPredicate, enttransfer.HasTransferLeavesWith(
		enttransferleaf.HasLeafWith(
			enttreenode.HasTreeWith(
				enttree.NetworkEQ(network),
			),
		),
	))

	if len(filter.Statuses) > 0 {
		statuses := make([]st.TransferStatus, len(filter.Statuses))
		for i, status := range filter.Statuses {
			var err error
			statuses[i], err = ent.TransferStatusSchema(status)
			if err != nil {
				return nil, fmt.Errorf("invalid transfer status: %w", err)
			}
		}
		transferPredicate = append(transferPredicate, enttransfer.StatusIn(statuses...))
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
		return nil, fmt.Errorf("unable to query transfers: %w", err)
	}

	transferProtos := []*pb.Transfer{}
	for _, transfer := range transfers {
		transferProto, err := transfer.MarshalProto(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to marshal transfer: %w", err)
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
		return fmt.Errorf("failed to find coop exit for transfer %s: %w", transfer.ID.String(), err)
	}

	transferLeaves, err := transfer.QueryTransferLeaves().All(ctx)
	if err != nil {
		return fmt.Errorf("failed to find leaves for transfer %s: %w", transfer.ID.String(), err)
	}
	// Leaf and tree are required to exist by our schema and
	// transfers must be initialized with at least 1 leaf
	tree := transferLeaves[0].QueryLeaf().QueryTree().OnlyX(ctx)

	blockHeight, err := db.BlockHeight.Query().Where(
		blockheight.NetworkEQ(tree.Network),
	).Only(ctx)
	if err != nil {
		return fmt.Errorf("failed to find block height: %w", err)
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
		return fmt.Errorf("unable to load transfer %s: %w", req.TransferId, err)
	}
	span.SetAttributes(transferTypeKey.String(string(transfer.Type)))
	if !bytes.Equal(transfer.ReceiverIdentityPubkey, req.OwnerIdentityPublicKey) {
		return fmt.Errorf("cannot claim transfer %s, receiver identity public key mismatch", req.TransferId)
	}
	if transfer.Status != st.TransferStatusSenderKeyTweaked && transfer.Status != st.TransferStatusReceiverKeyTweaked {
		return errors.FailedPreconditionErrorf("transfer cannot be claimed %s, status: %s", req.TransferId, transfer.Status)
	}

	db := ent.GetDbFromContext(ctx)
	if err := checkCoopExitTxBroadcasted(ctx, db, transfer); err != nil {
		return fmt.Errorf("failed to unlock transfer %s: %w", req.TransferId, err)
	}

	// Validate leaves count
	transferLeaves, err := transfer.QueryTransferLeaves().WithLeaf().All(ctx)
	if err != nil {
		return fmt.Errorf("unable to get transfer leaves for transfer %s: %w", req.TransferId, err)
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
			return fmt.Errorf("unable to marshal leaf tweak: %w", err)
		}
		leaf, err = leaf.Update().SetKeyTweak(leafTweakBytes).Save(ctx)
		if err != nil {
			return fmt.Errorf("unable to update leaf %s: %w", leaf.ID.String(), err)
		}
	}

	// Update transfer status
	_, err = transfer.Update().SetStatus(st.TransferStatusReceiverKeyTweaked).Save(ctx)
	if err != nil {
		return fmt.Errorf("unable to update transfer status %s: %w", transfer.ID.String(), err)
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
		return fmt.Errorf("unable to validate share: %w", err)
	}

	if leaf.Status != st.TreeNodeStatusTransferLocked {
		return fmt.Errorf("unable to transfer leaf %s", leaf.ID.String())
	}

	// Tweak keyshare
	keyshare, err := leaf.QuerySigningKeyshare().First(ctx)
	if err != nil {
		return fmt.Errorf("unable to load keyshare for leaf %s: %w", leaf.ID.String(), err)
	}
	keyshare, err = keyshare.TweakKeyShare(
		ctx,
		req.SecretShareTweak.SecretShare,
		req.SecretShareTweak.Proofs[0],
		req.PubkeySharesTweak,
	)
	if err != nil {
		return fmt.Errorf("unable to tweak keyshare %s for leaf %s: %w", keyshare.ID.String(), leaf.ID.String(), err)
	}

	signingPubkey, err := common.SubtractPublicKeys(leaf.VerifyingPubkey, keyshare.PublicKey)
	if err != nil {
		return fmt.Errorf("unable to calculate new signing pubkey for leaf %s: %w", req.LeafId, err)
	}
	_, err = leaf.
		Update().
		SetOwnerIdentityPubkey(ownerIdentityPubkey).
		SetOwnerSigningPubkey(signingPubkey).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("unable to update leaf %s: %w", req.LeafId, err)
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
		return nil, fmt.Errorf("unable to get leaves for transfer %s: %w", transfer.ID.String(), err)
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
			return fmt.Errorf("unable to get tree node for leaf %s: %w", leaf.ID.String(), err)
		}
		proof, exists := keyTweakProofs[treeNode.ID.String()]
		if !exists {
			return fmt.Errorf("key tweak proof for leaf %s not found", leaf.ID.String())
		}
		keyTweakProto := &pb.ClaimLeafKeyTweak{}
		err = proto.Unmarshal(leaf.KeyTweak, keyTweakProto)
		if err != nil {
			return fmt.Errorf("unable to unmarshal key tweak for leaf %s: %w", leaf.ID.String(), err)
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

	switch transfer.Status {
	case st.TransferStatusReceiverKeyTweakApplied:
	case st.TransferStatusCompleted:
	case st.TransferStatusReturned:
	case st.TransferStatusReceiverRefundSigned:
		return fmt.Errorf("transfer %s key tweak is already applied, but other operator is trying to revert it", transfer.ID.String())
	case st.TransferStatusReceiverKeyTweakLocked:
	case st.TransferStatusReceiverKeyTweaked:
		// do nothing
	default:
		// do nothing and return to prevent advance state
		return nil
	}

	_, err := transfer.Update().SetStatus(st.TransferStatusSenderKeyTweaked).Save(ctx)
	if err != nil {
		return fmt.Errorf("unable to update transfer status %s: %w", transfer.ID.String(), err)
	}
	for _, leaf := range transferLeaves {
		leaf, err := leaf.Update().SetKeyTweak(nil).Save(ctx)
		if err != nil {
			return fmt.Errorf("unable to update leaf %s: %w", leaf.ID.String(), err)
		}
	}
	return nil
}

func (h *TransferHandler) settleReceiverKeyTweak(ctx context.Context, transfer *ent.Transfer, keyTweakProofs map[string]*pb.SecretProof, userPublicKeys map[string][]byte) error {
	ctx, span := tracer.Start(ctx, "TransferHandler.settleReceiverKeyTweak", trace.WithAttributes(
		transferTypeKey.String(string(transfer.Type)),
	))
	defer span.End()

	action := pbinternal.SettleKeyTweakAction_COMMIT
	selection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionAll}
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
			UserPublicKeys: userPublicKeys,
		})
	})
	if err != nil {
		action = pbinternal.SettleKeyTweakAction_ROLLBACK
	}

	if h.mockAction != nil {
		if h.mockAction.InterruptTransfer {
			return fmt.Errorf("transfer interrupted")
		}
	}

	_, err = helper.ExecuteTaskWithAllOperators(ctx, h.config, &selection, func(ctx context.Context, operator *so.SigningOperator) (interface{}, error) {
		conn, err := operator.NewGRPCConnection()
		if err != nil {
			return nil, err
		}
		defer conn.Close()
		client := pbinternal.NewSparkInternalServiceClient(conn)
		return client.SettleReceiverKeyTweak(ctx, &pbinternal.SettleReceiverKeyTweakRequest{
			TransferId: transfer.ID.String(),
			Action:     action,
		})
	})
	if err != nil {
		// At this point, this is not recoverable. But this should not happen in theory.
		return fmt.Errorf("unable to settle receiver key tweak: %w", err)
	}
	if action == pbinternal.SettleKeyTweakAction_ROLLBACK {
		return fmt.Errorf("unable to settle receiver key tweak: %w, you might have a race condition in your implementation", err)
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
		return nil, fmt.Errorf("unable to load transfer %s: %w", req.TransferId, err)
	}
	span.SetAttributes(transferTypeKey.String(string(transfer.Type)))
	if !bytes.Equal(transfer.ReceiverIdentityPubkey, req.OwnerIdentityPublicKey) {
		return nil, fmt.Errorf("cannot claim transfer %s, receiver identity public key mismatch", req.TransferId)
	}

	switch transfer.Status {
	case st.TransferStatusReceiverKeyTweaked:
	case st.TransferStatusReceiverRefundSigned:
	case st.TransferStatusReceiverKeyTweakLocked:
	case st.TransferStatusReceiverKeyTweakApplied:
		// do nothing
	default:
		return nil, fmt.Errorf("transfer %s is expected to be at status TransferStatusKeyTweaked or TransferStatusReceiverRefundSigned or TransferStatusReceiverKeyTweakLocked or TransferStatusReceiverKeyTweakApplied but %s found", req.TransferId, transfer.Status)
	}

	// Validate leaves count
	leavesToTransfer, err := transfer.QueryTransferLeaves().All(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to load leaves to transfer for transfer %s: %w", req.TransferId, err)
	}
	if len(leavesToTransfer) != len(req.SigningJobs) {
		return nil, fmt.Errorf("inconsistent leaves to claim for transfer %s", req.TransferId)
	}

	leaves, err := h.getLeavesFromTransfer(ctx, transfer)
	if err != nil {
		return nil, err
	}

	keyTweakProofs := map[string]*pb.SecretProof{}
	for _, leaf := range leavesToTransfer {
		treeNode, err := leaf.QueryLeaf().Only(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to get tree node for leaf %s: %w", leaf.ID.String(), err)
		}
		leafKeyTweak := &pb.ClaimLeafKeyTweak{}
		if leaf.KeyTweak != nil {
			err = proto.Unmarshal(leaf.KeyTweak, leafKeyTweak)
			if err != nil {
				return nil, fmt.Errorf("unable to unmarshal key tweak for leaf %s: %w", leaf.ID.String(), err)
			}
			keyTweakProofs[treeNode.ID.String()] = &pb.SecretProof{
				Proofs: leafKeyTweak.SecretShareTweak.Proofs,
			}
		}
	}

	userPublicKeys := make(map[string][]byte)
	for _, job := range req.SigningJobs {
		userPublicKeys[job.LeafId] = job.RefundTxSigningJob.SigningPublicKey
	}
	err = h.settleReceiverKeyTweak(ctx, transfer, keyTweakProofs, userPublicKeys)
	if err != nil {
		return nil, fmt.Errorf("unable to settle receiver key tweak: %w", err)
	}

	// Update transfer status.
	_, err = transfer.Update().SetStatus(st.TransferStatusReceiverRefundSigned).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to update transfer status %s: %w", transfer.ID.String(), err)
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
			return nil, fmt.Errorf("unable to update leaf refund tx %s: %w", leaf.ID.String(), err)
		}

		signingJob, err := h.getRefundTxSigningJob(ctx, leaf, job.RefundTxSigningJob)
		if err != nil {
			return nil, fmt.Errorf("unable to create signing job for leaf %s: %w", leaf.ID.String(), err)
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
		return nil, fmt.Errorf("unable to load keyshare for leaf %s: %w", leaf.ID.String(), err)
	}
	leafTx, err := common.TxFromRawTxBytes(leaf.RawTx)
	if err != nil {
		return nil, fmt.Errorf("unable to load leaf tx for leaf %s: %w", leaf.ID.String(), err)
	}
	if len(leafTx.TxOut) <= 0 {
		return nil, fmt.Errorf("vout out of bounds")
	}
	refundSigningJob, _, err := helper.NewSigningJob(keyshare, job, leafTx.TxOut[0], nil)
	if err != nil {
		return nil, fmt.Errorf("unable to create signing job for leaf %s: %w", leaf.ID.String(), err)
	}
	return refundSigningJob, nil
}

func (h *TransferHandler) InitiateSettleReceiverKeyTweak(ctx context.Context, req *pbinternal.InitiateSettleReceiverKeyTweakRequest) error {
	ctx, span := tracer.Start(ctx, "TransferHandler.InitiateSettleReceiverKeyTweak")
	defer span.End()

	transfer, err := h.loadTransfer(ctx, req.TransferId)
	if err != nil {
		return fmt.Errorf("unable to load transfer %s: %w", req.TransferId, err)
	}
	span.SetAttributes(transferTypeKey.String(string(transfer.Type)))

	applied, err := h.checkIfKeyTweakApplied(ctx, transfer, req.UserPublicKeys)
	if err != nil {
		return fmt.Errorf("unable to check if key tweak is applied: %w", err)
	}
	if applied {
		_, err = transfer.Update().SetStatus(st.TransferStatusReceiverKeyTweakApplied).Save(ctx)
		if err != nil {
			return fmt.Errorf("unable to update transfer status %s: %v", transfer.ID.String(), err)
		}
		return nil
	}

	switch transfer.Status {
	case st.TransferStatusReceiverKeyTweaked:
	case st.TransferStatusReceiverKeyTweakLocked:
		// do nothing
	case st.TransferStatusReceiverKeyTweakApplied:
		// The key tweak is already applied, return early.
		return nil
	default:
		return fmt.Errorf("transfer %s is expected to be at status TransferStatusReceiverKeyTweaked or TransferStatusReceiverKeyTweakLocked or TransferStatusReceiverKeyTweakApplied but %s found", req.TransferId, transfer.Status)
	}

	leaves, err := transfer.QueryTransferLeaves().All(ctx)
	if err != nil {
		return fmt.Errorf("unable to get leaves from transfer %s: %w", req.TransferId, err)
	}

	if req.KeyTweakProofs != nil {
		err = h.ValidateKeyTweakProof(ctx, leaves, req.KeyTweakProofs)
		if err != nil {
			return fmt.Errorf("unable to validate key tweak proof: %w", err)
		}
	} else {
		return fmt.Errorf("key tweak proof is required")
	}

	_, err = transfer.Update().SetStatus(st.TransferStatusReceiverKeyTweakLocked).Save(ctx)
	if err != nil {
		return fmt.Errorf("unable to update transfer status %s: %v", transfer.ID.String(), err)
	}

	return nil
}

func (h *TransferHandler) checkIfKeyTweakApplied(ctx context.Context, transfer *ent.Transfer, userPublicKeys map[string][]byte) (bool, error) {
	leaves, err := transfer.QueryTransferLeaves().QueryLeaf().WithSigningKeyshare().All(ctx)
	if err != nil {
		return false, fmt.Errorf("unable to get leaves from transfer %s: %v", transfer.ID.String(), err)
	}

	var tweaked *bool
	for _, leaf := range leaves {
		userPublicKey, ok := userPublicKeys[leaf.ID.String()]
		if !ok {
			return false, fmt.Errorf("user public key for leaf %s not found", leaf.ID.String())
		}
		sparkPublicKey := leaf.Edges.SigningKeyshare.PublicKey
		combinedPublicKey, err := common.AddPublicKeys(sparkPublicKey, userPublicKey)
		if err != nil {
			return false, fmt.Errorf("unable to add public keys for leaf %s: %v", leaf.ID.String(), err)
		}
		localTweaked := bytes.Equal(combinedPublicKey, leaf.VerifyingPubkey)
		if tweaked == nil {
			tweaked = &localTweaked
		} else if *tweaked != localTweaked {
			return false, fmt.Errorf("inconsistent key tweak status for transfer %s", transfer.ID.String())
		}
	}
	return *tweaked, nil
}

func (h *TransferHandler) SettleReceiverKeyTweak(ctx context.Context, req *pbinternal.SettleReceiverKeyTweakRequest) error {
	ctx, span := tracer.Start(ctx, "TransferHandler.SettleReceiverKeyTweak")
	defer span.End()

	transfer, err := h.loadTransfer(ctx, req.TransferId)
	if err != nil {
		return fmt.Errorf("unable to load transfer %s: %w", req.TransferId, err)
	}
	span.SetAttributes(transferTypeKey.String(string(transfer.Type)))

	if transfer.Status == st.TransferStatusReceiverKeyTweakApplied {
		// The receiver key tweak is already applied, return early.
		return nil
	}

	leaves, err := transfer.QueryTransferLeaves().All(ctx)
	if err != nil {
		return fmt.Errorf("unable to get leaves from transfer %s: %w", req.TransferId, err)
	}

	switch req.Action {
	case pbinternal.SettleKeyTweakAction_COMMIT:
		for _, leaf := range leaves {
			treeNode, err := leaf.QueryLeaf().Only(ctx)
			if err != nil {
				return fmt.Errorf("unable to get tree node for leaf %s: %w", leaf.ID.String(), err)
			}
			if len(leaf.KeyTweak) == 0 {
				return fmt.Errorf("key tweak for leaf %s is not set", leaf.ID.String())
			}
			keyTweakProto := &pb.ClaimLeafKeyTweak{}
			err = proto.Unmarshal(leaf.KeyTweak, keyTweakProto)
			if err != nil {
				return fmt.Errorf("unable to unmarshal key tweak for leaf %s: %w", leaf.ID.String(), err)
			}
			err = h.claimLeafTweakKey(ctx, treeNode, keyTweakProto, transfer.ReceiverIdentityPubkey)
			if err != nil {
				return fmt.Errorf("unable to claim leaf tweak key for leaf %s: %w", leaf.ID.String(), err)
			}
			_, err = leaf.Update().SetKeyTweak(nil).Save(ctx)
			if err != nil {
				return fmt.Errorf("unable to update leaf key tweak %s: %w", leaf.ID.String(), err)
			}
		}
		_, err = transfer.Update().SetStatus(st.TransferStatusReceiverKeyTweakApplied).Save(ctx)
		if err != nil {
			return fmt.Errorf("unable to update transfer status %s: %w", transfer.ID.String(), err)
		}
	case pbinternal.SettleKeyTweakAction_ROLLBACK:
		return h.revertClaimTransfer(ctx, transfer, leaves)
	default:
		return fmt.Errorf("invalid action %s", req.Action)
	}

	return nil
}

func (h *TransferHandler) ResumeSendTransfer(ctx context.Context, transfer *ent.Transfer) error {
	ctx, span := tracer.Start(ctx, "TransferHandler.ResumeSendTransfer")
	defer span.End()

	logger := logging.GetLoggerFromContext(ctx)

	if transfer.Status != st.TransferStatusSenderInitiatedCoordinator {
		// Noop
		return nil
	}

	err := h.settleSenderKeyTweaks(ctx, transfer.ID.String(), pbinternal.SettleKeyTweakAction_COMMIT)
	if err == nil {
		// If there's no error, it means all SOs have tweaked the key. The coordinator can tweak the key here.
		transfer, err = h.commitSenderKeyTweaks(ctx, transfer, false)
		if err != nil {
			return err
		}
		transferProto, err := transfer.MarshalProto(ctx)
		if err != nil {
			return fmt.Errorf("unable to marshal transfer %s: %w", transfer.ID.String(), err)
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
	}

	// If there's an error, it means some SOs are not online. We can retry later.
	logger.Warn("Failed to settle sender key tweaks", "error", err, "transfer_id", transfer.ID.String())
	return nil
}

func (h *TransferHandler) InvestigateLeaves(ctx context.Context, req *pb.InvestigateLeavesRequest) (*emptypb.Empty, error) {
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, req.OwnerIdentityPublicKey); err != nil {
		return nil, err
	}

	db := ent.GetDbFromContext(ctx)

	if len(req.TransferId) > 0 {
		transfer, err := h.loadTransferWithoutUpdate(ctx, req.TransferId)
		if err != nil {
			return nil, fmt.Errorf("unable to load transfer %s: %w", req.GetTransferId(), err)
		}
		// validate that all leaves in this query belongs to the transfer
		leaves, err := transfer.QueryTransferLeaves().QueryLeaf().All(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to find leaves for transfer %s: %w", req.GetTransferId(), err)
		}
		trasnferLeafMap := make(map[string]bool)
		for _, leaf := range leaves {
			trasnferLeafMap[leaf.ID.String()] = true
		}
		for _, leafID := range req.GetLeafIds() {
			if !trasnferLeafMap[leafID] {
				return nil, fmt.Errorf("leaf %s is not a leaf of transfer %s", leafID, req.GetTransferId())
			}
		}

		err = h.CreateCancelTransferGossipMessage(ctx, req.GetTransferId())
		if err != nil {
			return nil, fmt.Errorf("unable to cancel transfer %s: %w", req.GetTransferId(), err)
		}
	}

	leafIDs := make([]uuid.UUID, len(req.GetLeafIds()))
	for i, leafID := range req.GetLeafIds() {
		leafUUID, err := uuid.Parse(leafID)
		if err != nil {
			return nil, fmt.Errorf("unable to parse leaf id as a uuid %s: %w", leafID, err)
		}
		leafIDs[i] = leafUUID
	}
	query := db.TreeNode.Query()
	query = query.Where(treenode.IDIn(leafIDs...)).ForUpdate()

	nodes, err := query.All(ctx)
	if err != nil {
		return nil, err
	}

	for _, node := range nodes {
		if node.Status != st.TreeNodeStatusAvailable {
			return nil, fmt.Errorf("node %s is not available", node.ID)
		}
		if !bytes.Equal(node.OwnerIdentityPubkey, req.OwnerIdentityPublicKey) {
			return nil, fmt.Errorf("node %s is not owned by the identity public key %s", node.ID, req.OwnerIdentityPublicKey)
		}
		_, err := node.Update().SetStatus(st.TreeNodeStatusInvestigation).Save(ctx)
		logger := logging.GetLoggerFromContext(ctx)
		logger.Warn("Tree Node is marked as investigation", "node_id", node.ID)
		if err != nil {
			return nil, err
		}
	}

	return &emptypb.Empty{}, nil
}

func (h *TransferHandler) TransferMagicSwapLeaves(ctx context.Context, transferID string, leafIDs []string, senderIdentityPublicKey []byte, receiverIdentityPublicKey []byte) (*ent.Transfer, error) {
	db := ent.GetDbFromContext(ctx)

	transferUUID, err := uuid.Parse(transferID)
	if err != nil {
		return nil, fmt.Errorf("unable to parse transfer id as a uuid %s: %w", transferID, err)
	}
	transfer, err := db.Transfer.Create().
		SetID(transferUUID).
		SetSenderIdentityPubkey(senderIdentityPublicKey).
		SetReceiverIdentityPubkey(receiverIdentityPublicKey).
		SetStatus(st.TransferStatusCompleted).
		SetTotalValue(0).
		SetExpiryTime(time.Unix(0, 0)).
		SetType(st.TransferTypeSwap).
		SetCompletionTime(time.Now()).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to create transfer: %w", err)
	}

	totalAmount := uint64(0)
	for _, leafID := range leafIDs {
		leafUUID, err := uuid.Parse(leafID)
		if err != nil {
			return nil, fmt.Errorf("unable to parse leaf id as a uuid %s: %w", leafUUID, err)
		}
		leaf, err := db.TreeNode.Get(ctx, leafUUID)
		if err != nil {
			return nil, fmt.Errorf("unable to get leaf %s: %w", leafID, err)
		}
		totalAmount += leaf.Value

		_, err = db.TransferLeaf.Create().
			SetTransfer(transfer).
			SetLeaf(leaf).
			SetPreviousRefundTx(leaf.RawRefundTx).
			SetIntermediateRefundTx(leaf.RawRefundTx).
			Save(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to create transfer leaf: %w", err)
		}

		_, err = leaf.Update().SetOwnerIdentityPubkey(receiverIdentityPublicKey).Save(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to update leaf %s: %w", leafID, err)
		}
	}

	transfer, err = transfer.Update().SetTotalValue(totalAmount).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to update transfer %s total value: %w", transferID, err)
	}
	return transfer, nil
}

// setSoCoordinatorKeyTweaks sets the key tweaks for each transfer leaf based on the validated transfer package.
func (h *TransferHandler) setSoCoordinatorKeyTweaks(ctx context.Context, transfer *ent.Transfer, req *pb.TransferPackage, ownerIdentityPublicKey []byte) error {
	// Get key tweak map from transfer package
	keyTweakMap, err := h.validateTransferPackage(ctx, transfer.ID.String(), req, ownerIdentityPublicKey)
	if err != nil {
		return fmt.Errorf("failed to validate transfer package: %w", err)
	}
	// Query all transfer leaves associated with the transfer
	transferLeaves, err := transfer.QueryTransferLeaves().All(ctx)
	if err != nil {
		return fmt.Errorf("failed to query transfer leaves: %w", err)
	}
	// For each transfer leaf, set its key tweak if there's a matching entry in the key tweak map
	for _, transferLeaf := range transferLeaves {
		leaf, err := transferLeaf.QueryLeaf().Only(ctx)
		if err != nil {
			return fmt.Errorf("failed to query leaf for transfer leaf %s: %w", transferLeaf.ID, err)
		}
		if keyTweak, ok := keyTweakMap[leaf.ID.String()]; ok {
			keyTweakBinary, err := proto.Marshal(keyTweak)
			if err != nil {
				return fmt.Errorf("failed to marshal key tweak for leaf %s: %w", leaf.ID, err)
			}
			_, err = transferLeaf.Update().SetKeyTweak(keyTweakBinary).Save(ctx)
			if err != nil {
				return fmt.Errorf("failed to set key tweak for transfer leaf %s: %w", transferLeaf.ID, err)
			}
		}
	}
	return nil
}
