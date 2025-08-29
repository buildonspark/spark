package handler

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/lightsparkdev/spark/common/keys"

	"github.com/btcsuite/btcd/wire"
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
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/helper"
	"github.com/lightsparkdev/spark/so/knobs"
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
	// Map to track active `claim_transfer_sign_refund` requests to prevent BitBit from overwhelming
	// us.
	activeClaimTransferSignRefunds sync.Map
}

var transferTypeKey = attribute.Key("transfer_type")

// NewTransferHandler creates a new TransferHandler.
func NewTransferHandler(config *so.Config) *TransferHandler {
	return &TransferHandler{BaseTransferHandler: NewBaseTransferHandler(config), config: config, activeClaimTransferSignRefunds: sync.Map{}}
}

func (h *TransferHandler) SetMockAction(mockAction *common.MockAction) {
	h.mockAction = mockAction
}

func (h *TransferHandler) loadCpfpLeafRefundMap(req *pb.StartTransferRequest) map[string][]byte {
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

func (h *TransferHandler) loadDirectLeafRefundMap(req *pb.StartTransferRequest) map[string][]byte {
	leafRefundMap := make(map[string][]byte)
	if req.TransferPackage != nil {
		for _, leaf := range req.TransferPackage.DirectLeavesToSend {
			leafRefundMap[leaf.LeafId] = leaf.RawTx
		}
	} else {
		for _, leaf := range req.LeavesToSend {
			if leaf.DirectRefundTxSigningJob != nil {
				leafRefundMap[leaf.LeafId] = leaf.DirectRefundTxSigningJob.RawTx
			}
		}
	}
	return leafRefundMap
}

func (h *TransferHandler) loadDirectFromCpfpLeafRefundMap(req *pb.StartTransferRequest) map[string][]byte {
	leafRefundMap := make(map[string][]byte)
	if req.TransferPackage != nil {
		for _, leaf := range req.TransferPackage.DirectFromCpfpLeavesToSend {
			leafRefundMap[leaf.LeafId] = leaf.RawTx
		}
	} else {
		for _, leaf := range req.LeavesToSend {
			if leaf.DirectFromCpfpRefundTxSigningJob != nil {
				leafRefundMap[leaf.LeafId] = leaf.DirectFromCpfpRefundTxSigningJob.RawTx
			}
		}
	}
	return leafRefundMap
}

// startTransferInternal starts a transfer, signing refunds, and saving the transfer to the DB
// for the first time. This optionally takes an adaptorPubKey to modify the refund signatures.
func (h *TransferHandler) startTransferInternal(ctx context.Context, req *pb.StartTransferRequest, transferType st.TransferType, cpfpAdaptorPubKey keys.Public, directAdaptorPubKey keys.Public, directFromCpfpAdaptorPubKey keys.Public, requireDirectTx bool) (*pb.StartTransferResponse, error) {
	logger := logging.GetLoggerFromContext(ctx)

	ctx, span := tracer.Start(ctx, "TransferHandler.startTransferInternal", trace.WithAttributes(
		transferTypeKey.String(string(transferType)),
	))
	defer span.End()

	reqOwnerIDPubKey, err := keys.ParsePublicKey(req.OwnerIdentityPublicKey)
	if err != nil {
		return nil, fmt.Errorf("invalid identity public key: %w", err)
	}
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, reqOwnerIDPubKey); err != nil {
		return nil, err
	}

	leafTweakMap, err := h.validateTransferPackage(ctx, req.TransferId, req.TransferPackage, reqOwnerIDPubKey)
	if err != nil {
		return nil, fmt.Errorf("failed to validate transfer package for transfer %s: %w", req.TransferId, err)
	}

	knobService := knobs.GetKnobsService(ctx)
	if knobService != nil {
		transferLimit := knobService.GetValue(knobs.KnobSoTransferLimit, 0)
		if transferLimit > 0 && (len(leafTweakMap) > int(transferLimit) || len(req.LeavesToSend) > int(transferLimit)) {
			return nil, status.Errorf(codes.InvalidArgument, "transfer limit reached, please send %d leaves at a time", int(transferLimit))
		}
	}

	leafCpfpRefundMap := h.loadCpfpLeafRefundMap(req)
	leafDirectRefundMap := h.loadDirectLeafRefundMap(req)
	leafDirectFromCpfpRefundMap := h.loadDirectFromCpfpLeafRefundMap(req)

	reqReceiverIDPubKey, err := keys.ParsePublicKey(req.ReceiverIdentityPublicKey)
	if err != nil {
		return nil, fmt.Errorf("invalid receiver identity public key: %w", err)
	}
	transfer, leafMap, err := h.createTransfer(
		ctx,
		req.TransferId,
		transferType,
		req.ExpiryTime.AsTime(),
		reqOwnerIDPubKey,
		reqReceiverIDPubKey,
		leafCpfpRefundMap,
		leafDirectRefundMap,
		leafDirectFromCpfpRefundMap,
		leafTweakMap,
		TransferRoleCoordinator,
		requireDirectTx,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create transfer for transfer %s: %w", req.TransferId, err)
	}

	var signingResults []*pb.LeafRefundTxSigningResult
	var finalCpfpSignatureMap map[string][]byte
	var finalDirectSignatureMap map[string][]byte
	var finalDirectFromCpfpSignatureMap map[string][]byte
	if req.TransferPackage == nil {
		signingResults, err = signRefunds(ctx, h.config, req, leafMap, cpfpAdaptorPubKey, directAdaptorPubKey, directFromCpfpAdaptorPubKey)
		if err != nil {
			return nil, fmt.Errorf("failed to sign refunds for transfer %s: %w", req.TransferId, err)
		}
	} else {
		cpfpSigningResultMap, directSigningResultMap, directFromCpfpSigningResultMap, err := signRefundsWithPregeneratedNonce(ctx, h.config, req, leafMap, cpfpAdaptorPubKey, directAdaptorPubKey, directFromCpfpAdaptorPubKey)
		if err != nil {
			return nil, fmt.Errorf("failed to sign refunds with pregenerated nonce: %w", err)
		}
		finalCpfpSignatureMap, finalDirectSignatureMap, finalDirectFromCpfpSignatureMap, err = aggregateSignatures(ctx, h.config, req, cpfpAdaptorPubKey, directAdaptorPubKey, directFromCpfpAdaptorPubKey, cpfpSigningResultMap, directSigningResultMap, directFromCpfpSigningResultMap, leafMap)
		if err != nil {
			return nil, fmt.Errorf("failed to aggregate signatures: %w", err)
		}

		// Update the leaves with the final signatures for refunds
		err = h.updateCpfpTransferLeavesSignatures(ctx, transfer, finalCpfpSignatureMap)
		if err != nil {
			return nil, fmt.Errorf("failed to update CPFP transfer leaves signatures: %w", err)
		}

		if len(finalDirectSignatureMap) > 0 && len(finalDirectFromCpfpSignatureMap) > 0 {
			err = h.updateDirectTransferLeavesSignatures(ctx, transfer, finalDirectSignatureMap)
			if err != nil {
				return nil, fmt.Errorf("failed to update direct transfer leaves signatures: %w", err)
			}
			err = h.updateDirectFromCpfpTransferLeavesSignatures(ctx, transfer, finalDirectFromCpfpSignatureMap)
			if err != nil {
				return nil, fmt.Errorf("failed to update direct from cpfp transfer leaves signatures: %w", err)
			}
		}
		// Build the proto signing results including both CPFP and direct refund signatures.
		for leafID := range leafMap {
			var cpfpProto *pb.SigningResult
			var directProto *pb.SigningResult
			var directFromCpfpProto *pb.SigningResult
			if res, ok := cpfpSigningResultMap[leafID]; ok {
				cpfRes, err := res.MarshalProto()
				if err != nil {
					return nil, fmt.Errorf("unable to marshal cpfp signing result: %w", err)
				}
				cpfpProto = cpfRes
				if res, ok := directSigningResultMap[leafID]; ok && len(directSigningResultMap) > 0 {
					dirRes, err := res.MarshalProto()
					if err != nil {
						return nil, fmt.Errorf("unable to marshal direct signing result: %w", err)
					}
					directProto = dirRes
				}
				if res, ok := directFromCpfpSigningResultMap[leafID]; ok && len(directFromCpfpSigningResultMap) > 0 {
					dirFromCpfpRes, err := res.MarshalProto()
					if err != nil {
						return nil, fmt.Errorf("unable to marshal direct from cpfp signing result: %w", err)
					}
					directFromCpfpProto = dirFromCpfpRes
				}
			}

			signingResults = append(signingResults, &pb.LeafRefundTxSigningResult{
				LeafId:                              leafID,
				RefundTxSigningResult:               cpfpProto,
				DirectRefundTxSigningResult:         directProto,
				DirectFromCpfpRefundTxSigningResult: directFromCpfpProto,
				VerifyingKey:                        leafMap[leafID].VerifyingPubkey,
			})
		}
	}

	// This call to other SOs will check the validity of the transfer package. If no error is
	// returned, it means the transfer package is valid and the transfer is considered sent.
	err = h.syncTransferInit(ctx, req, transferType, finalCpfpSignatureMap, finalDirectSignatureMap, finalDirectFromCpfpSignatureMap)
	if err != nil {
		cancelErr := h.CreateCancelTransferGossipMessage(ctx, req.TransferId)
		if cancelErr != nil {
			logger.Error("failed to create cancel transfer gossip message", "error", cancelErr, "transfer_id", req.TransferId)
		}
		logger.Error("failed to sync transfer init for transfer", "transfer_id", req.TransferId, "error", err)
		return nil, fmt.Errorf("failed to sync transfer init for transfer %s: %w", req.TransferId, err)
	}

	// After this point, the transfer send is considered successful.

	if req.TransferPackage != nil {
		db, err := ent.GetDbFromContext(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to get db before sync transfer init: %w", err)
		}
		err = db.Commit()
		if err != nil {
			return nil, fmt.Errorf("unable to commit db before sync transfer init: %w", err)
		}
		// If all other SOs have settled the sender key tweaks, we can commit the sender key tweaks.
		// If there's any error, it means one or more of the SOs are down at the time, we will have a
		// cron job to retry the key commit.
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
		transfer, err = h.loadTransferForUpdate(ctx, req.TransferId)
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

func (h *TransferHandler) updateCpfpTransferLeavesSignatures(ctx context.Context, transfer *ent.Transfer, finalSignatureMap map[string][]byte) error {
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
			return fmt.Errorf("unable to get cpfp refund tx: %w", err)
		}
		nodeTx, err := common.TxFromRawTxBytes(leaf.Edges.Leaf.RawTx)
		if err != nil {
			return fmt.Errorf("unable to get cpfp node tx: %w", err)
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

func (h *TransferHandler) updateDirectTransferLeavesSignatures(ctx context.Context, transfer *ent.Transfer, finalSignatureMap map[string][]byte) error {
	transferLeaves, err := transfer.QueryTransferLeaves().WithLeaf().All(ctx)
	if err != nil {
		return fmt.Errorf("unable to get transfer leaves: %w", err)
	}
	for _, leaf := range transferLeaves {
		// Skip leaves that don't have direct refund transactions
		if len(leaf.IntermediateDirectRefundTx) == 0 {
			continue
		}

		// Skip leaves that don't have signatures in the map
		signature, exists := finalSignatureMap[leaf.Edges.Leaf.ID.String()]
		if !exists {
			continue
		}

		updatedTx, err := common.UpdateTxWithSignature(leaf.IntermediateDirectRefundTx, 0, signature)
		if err != nil {
			return fmt.Errorf("unable to update leaf signature: %w", err)
		}

		refundTx, err := common.TxFromRawTxBytes(updatedTx)
		if err != nil {
			return fmt.Errorf("unable to get direct refund tx: %w", err)
		}
		nodeTx, err := common.TxFromRawTxBytes(leaf.Edges.Leaf.DirectTx)
		if err != nil {
			return fmt.Errorf("unable to get direct node tx: %w", err)
		}
		err = common.VerifySignatureSingleInput(refundTx, 0, nodeTx.TxOut[0])
		if err != nil {
			return fmt.Errorf("unable to verify leaf signature: %w", err)
		}

		_, err = leaf.Update().SetIntermediateDirectRefundTx(updatedTx).Save(ctx)
		if err != nil {
			return fmt.Errorf("unable to save leaf: %w", err)
		}
	}
	return nil
}

func (h *TransferHandler) updateDirectFromCpfpTransferLeavesSignatures(ctx context.Context, transfer *ent.Transfer, finalSignatureMap map[string][]byte) error {
	transferLeaves, err := transfer.QueryTransferLeaves().WithLeaf().All(ctx)
	if err != nil {
		return fmt.Errorf("unable to get transfer leaves: %w", err)
	}
	for _, leaf := range transferLeaves {
		// Skip leaves that don't have direct from cpfp refund transactions
		if len(leaf.IntermediateDirectFromCpfpRefundTx) == 0 {
			continue
		}

		// Skip leaves that don't have signatures in the map
		signature, exists := finalSignatureMap[leaf.Edges.Leaf.ID.String()]
		if !exists {
			continue
		}

		updatedTx, err := common.UpdateTxWithSignature(leaf.IntermediateDirectFromCpfpRefundTx, 0, signature)
		if err != nil {
			return fmt.Errorf("unable to update leaf signature: %w", err)
		}

		refundTx, err := common.TxFromRawTxBytes(updatedTx)
		if err != nil {
			return fmt.Errorf("unable to get direct from cpfp refund tx: %w", err)
		}
		nodeTx, err := common.TxFromRawTxBytes(leaf.Edges.Leaf.RawTx)
		if err != nil {
			return fmt.Errorf("unable to get direct node tx: %w", err)
		}
		err = common.VerifySignatureSingleInput(refundTx, 0, nodeTx.TxOut[0])
		if err != nil {
			return fmt.Errorf("unable to verify leaf signature: %w", err)
		}

		_, err = leaf.Update().SetIntermediateDirectFromCpfpRefundTx(updatedTx).Save(ctx)
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
	_, err := helper.ExecuteTaskWithAllOperators(ctx, h.config, &operatorSelection, func(ctx context.Context, operator *so.SigningOperator) (any, error) {
		conn, err := operator.NewOperatorGRPCConnection()
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
	return h.startTransferInternal(ctx, req, st.TransferTypeTransfer, keys.Public{}, keys.Public{}, keys.Public{}, false)
}

func (h *TransferHandler) StartTransferV2(ctx context.Context, req *pb.StartTransferRequest) (*pb.StartTransferResponse, error) {
	return h.startTransferInternal(ctx, req, st.TransferTypeTransfer, keys.Public{}, keys.Public{}, keys.Public{}, true)
}

func (h *TransferHandler) StartLeafSwap(ctx context.Context, req *pb.StartTransferRequest) (*pb.StartTransferResponse, error) {
	return h.startTransferInternal(ctx, req, st.TransferTypeSwap, keys.Public{}, keys.Public{}, keys.Public{}, false)
}

func (h *TransferHandler) StartLeafSwapV2(ctx context.Context, req *pb.StartTransferRequest) (*pb.StartTransferResponse, error) {
	return h.startTransferInternal(ctx, req, st.TransferTypeSwap, keys.Public{}, keys.Public{}, keys.Public{}, true)
}

// CounterLeafSwap initiates a leaf swap for the other side, signing refunds with an adaptor public key.
func (h *TransferHandler) CounterLeafSwap(ctx context.Context, req *pb.CounterLeafSwapRequest) (*pb.CounterLeafSwapResponse, error) {
	adaptorPublicKey, err := keys.ParsePublicKey(req.AdaptorPublicKey)
	if err != nil {
		return nil, fmt.Errorf("unable to parse adaptor public key: %w", err)
	}
	directAdaptorPublicKey, err := parsePublicKeyIfPresent(req.DirectAdaptorPublicKey)
	if err != nil {
		return nil, fmt.Errorf("unable to parse direct adaptor public key: %w", err)
	}
	directFromCpfpAdaptorPublicKey, err := parsePublicKeyIfPresent(req.DirectFromCpfpAdaptorPublicKey)
	if err != nil {
		return nil, fmt.Errorf("unable to parse direct from cpfp adaptor public key: %w", err)
	}
	startTransferResponse, err := h.startTransferInternal(ctx, req.Transfer, st.TransferTypeCounterSwap, adaptorPublicKey, directAdaptorPublicKey, directFromCpfpAdaptorPublicKey, false)
	if err != nil {
		return nil, fmt.Errorf("failed to start counter leaf swap for request %s: %w", logging.FormatProto("counter_leaf_swap_request", req), err)
	}
	return &pb.CounterLeafSwapResponse{Transfer: startTransferResponse.Transfer, SigningResults: startTransferResponse.SigningResults}, nil
}

// CounterLeafSwapV2 initiates a leaf swap for the other side, signing refunds with an adaptor public key.
func (h *TransferHandler) CounterLeafSwapV2(ctx context.Context, req *pb.CounterLeafSwapRequest) (*pb.CounterLeafSwapResponse, error) {
	adaptorPublicKey, err := keys.ParsePublicKey(req.AdaptorPublicKey)
	if err != nil {
		return nil, fmt.Errorf("unable to parse adaptor public key: %w", err)
	}

	directAdaptorPublicKey, err := parsePublicKeyIfPresent(req.DirectAdaptorPublicKey)
	if err != nil {
		return nil, fmt.Errorf("unable to parse direct adaptor public key: %w", err)
	}
	directFromCpfpAdaptorPublicKey, err := parsePublicKeyIfPresent(req.DirectFromCpfpAdaptorPublicKey)
	if err != nil {
		return nil, fmt.Errorf("unable to parse direct from cpfp adaptor public key: %w", err)
	}
	startTransferResponse, err := h.startTransferInternal(ctx, req.Transfer, st.TransferTypeCounterSwap, adaptorPublicKey, directAdaptorPublicKey, directFromCpfpAdaptorPublicKey, true)
	if err != nil {
		return nil, fmt.Errorf("failed to start counter leaf swap for request %s: %w", logging.FormatProto("counter_leaf_swap_request", req), err)
	}
	return &pb.CounterLeafSwapResponse{Transfer: startTransferResponse.Transfer, SigningResults: startTransferResponse.SigningResults}, nil
}

func parsePublicKeyIfPresent(raw []byte) (keys.Public, error) {
	if len(raw) == 0 {
		return keys.Public{}, nil
	}
	return keys.ParsePublicKey(raw)
}

func (h *TransferHandler) syncTransferInit(ctx context.Context, req *pb.StartTransferRequest, transferType st.TransferType, cpfpRefundSignatures map[string][]byte, directRefundSignatures map[string][]byte, directFromCpfpRefundSignatures map[string][]byte) error {
	ctx, span := tracer.Start(ctx, "TransferHandler.syncTransferInit", trace.WithAttributes(
		transferTypeKey.String(string(transferType)),
	))
	defer span.End()
	var leaves []*pbinternal.InitiateTransferLeaf
	for _, leaf := range req.LeavesToSend {
		var directRefundTx []byte
		if leaf.DirectRefundTxSigningJob != nil {
			directRefundTx = leaf.DirectRefundTxSigningJob.RawTx
		}
		var directFromCpfpRefundTx []byte
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
	transferTypeProto, err := ent.TransferTypeProto(transferType)
	if err != nil {
		return fmt.Errorf("unable to get transfer type proto: %w", err)
	}
	initTransferRequest := &pbinternal.InitiateTransferRequest{
		TransferId:                     req.TransferId,
		SenderIdentityPublicKey:        req.OwnerIdentityPublicKey,
		ReceiverIdentityPublicKey:      req.ReceiverIdentityPublicKey,
		ExpiryTime:                     req.ExpiryTime,
		Leaves:                         leaves,
		Type:                           *transferTypeProto,
		TransferPackage:                req.TransferPackage,
		RefundSignatures:               cpfpRefundSignatures,
		DirectRefundSignatures:         directRefundSignatures,
		DirectFromCpfpRefundSignatures: directFromCpfpRefundSignatures,
	}
	selection := helper.OperatorSelection{
		Option: helper.OperatorSelectionOptionExcludeSelf,
	}
	_, err = helper.ExecuteTaskWithAllOperators(ctx, h.config, &selection, func(ctx context.Context, operator *so.SigningOperator) (any, error) {
		conn, err := operator.NewOperatorGRPCConnection()
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
	_, err := helper.ExecuteTaskWithAllOperators(ctx, h.config, &selection, func(ctx context.Context, operator *so.SigningOperator) (any, error) {
		conn, err := operator.NewOperatorGRPCConnection()
		if err != nil {
			return nil, err
		}
		defer conn.Close()

		logger := logging.GetLoggerFromContext(ctx)
		logger.Info("Delivering key tweak", "transfer_id", req.TransferId, "so", operator.ID)
		client := pbinternal.NewSparkInternalServiceClient(conn)
		return client.DeliverSenderKeyTweak(ctx, deliverSenderKeyTweakRequest)
	})
	return err
}

func signRefunds(ctx context.Context, config *so.Config, requests *pb.StartTransferRequest, leafMap map[string]*ent.TreeNode, cpfpAdaptorPubKey keys.Public, directAdaptorPubKey keys.Public, directFromCpfpAdaptorPubKey keys.Public) ([]*pb.LeafRefundTxSigningResult, error) {
	ctx, span := tracer.Start(ctx, "TransferHandler.signRefunds")
	defer span.End()

	if requests.TransferPackage != nil {
		return nil, fmt.Errorf("transfer package is not nil, should call signRefundsWithPregeneratedNonce instead")
	}

	leafJobMap := make(map[string]*ent.TreeNode)
	var cpfpSigningResults []*helper.SigningResult
	var directSigningResults []*helper.SigningResult
	var directFromCpfpSigningResults []*helper.SigningResult

	var cpfpSigningJobs []*helper.SigningJob
	var directSigningJobs []*helper.SigningJob
	var directFromCpfpSigningJobs []*helper.SigningJob

	// Process each leaf's signing jobs
	for _, req := range requests.LeavesToSend {
		leaf := leafMap[req.LeafId]
		cpfpRefundTx, err := common.TxFromRawTxBytes(req.RefundTxSigningJob.RawTx)
		if err != nil {
			return nil, fmt.Errorf("unable to load new refund tx: %w", err)
		}
		cpfpLeafTx, err := common.TxFromRawTxBytes(leaf.RawTx)
		if err != nil {
			return nil, fmt.Errorf("unable to load cpfp leaf tx: %w", err)
		}

		if len(cpfpLeafTx.TxOut) <= 0 {
			return nil, fmt.Errorf("cpfp vout out of bounds")
		}

		cpfpRefundTxSigHash, err := common.SigHashFromTx(cpfpRefundTx, 0, cpfpLeafTx.TxOut[0])
		if err != nil {
			return nil, fmt.Errorf("unable to calculate sighash from cpfp refund tx: %w", err)
		}

		cpfpUserNonceCommitment, err := objects.NewSigningCommitment(req.RefundTxSigningJob.SigningNonceCommitment.Binding, req.RefundTxSigningJob.SigningNonceCommitment.Hiding)
		if err != nil {
			return nil, fmt.Errorf("unable to create cpfp signing commitment: %w", err)
		}
		cpfpJobID := uuid.New().String()
		signingKeyshare, err := leaf.QuerySigningKeyshare().Only(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get signing keyshare id: %w", err)
		}

		leafVerifyingPubKey, err := keys.ParsePublicKey(leaf.VerifyingPubkey)
		if err != nil {
			return nil, fmt.Errorf("failed to parse verifying public key: %w", err)
		}

		cpfpSigningJobs = append(
			cpfpSigningJobs,
			&helper.SigningJob{
				JobID:             cpfpJobID,
				SigningKeyshareID: signingKeyshare.ID,
				Message:           cpfpRefundTxSigHash,
				VerifyingKey:      &leafVerifyingPubKey,
				UserCommitment:    cpfpUserNonceCommitment,
				AdaptorPublicKey:  &cpfpAdaptorPubKey,
			},
		)
		leafJobMap[cpfpJobID] = leaf

		if req.DirectRefundTxSigningJob != nil && req.DirectFromCpfpRefundTxSigningJob != nil && len(leaf.DirectTx) > 0 {
			directRefundTx, err := common.TxFromRawTxBytes(req.DirectRefundTxSigningJob.RawTx)
			if err != nil {
				return nil, fmt.Errorf("unable to load new refund tx: %w", err)
			}
			directFromCpfpRefundTx, err := common.TxFromRawTxBytes(req.DirectFromCpfpRefundTxSigningJob.RawTx)
			if err != nil {
				return nil, fmt.Errorf("unable to load new refund tx: %w", err)
			}
			directLeafTx, err := common.TxFromRawTxBytes(leaf.DirectTx)
			if err != nil {
				return nil, fmt.Errorf("unable to load direct leaf tx: %w", err)
			}
			if len(directLeafTx.TxOut) <= 0 {
				return nil, fmt.Errorf("direct vout out of bounds")
			}
			directRefundTxSigHash, err := common.SigHashFromTx(directRefundTx, 0, directLeafTx.TxOut[0])
			if err != nil {
				return nil, fmt.Errorf("unable to calculate sighash from direct refund tx: %w", err)
			}
			directFromCpfpRefundTxSigHash, err := common.SigHashFromTx(directFromCpfpRefundTx, 0, cpfpLeafTx.TxOut[0])
			if err != nil {
				return nil, fmt.Errorf("unable to calculate sighash from direct from cpfp refund tx: %w", err)
			}
			directUserNonceCommitment, err := objects.NewSigningCommitment(req.DirectRefundTxSigningJob.SigningNonceCommitment.Binding, req.DirectRefundTxSigningJob.SigningNonceCommitment.Hiding)
			if err != nil {
				return nil, fmt.Errorf("unable to create direct signing commitment: %w", err)
			}
			directFromCpfpUserNonceCommitment, err := objects.NewSigningCommitment(req.DirectFromCpfpRefundTxSigningJob.SigningNonceCommitment.Binding, req.DirectFromCpfpRefundTxSigningJob.SigningNonceCommitment.Hiding)
			if err != nil {
				return nil, fmt.Errorf("unable to create direct from cpfp signing commitment: %w", err)
			}
			directJobID := uuid.New().String()
			directFromCpfpJobID := uuid.New().String()

			directSigningJobs = append(
				directSigningJobs,
				&helper.SigningJob{
					JobID:             directJobID,
					SigningKeyshareID: signingKeyshare.ID,
					Message:           directRefundTxSigHash,
					VerifyingKey:      &leafVerifyingPubKey,
					UserCommitment:    directUserNonceCommitment,
					AdaptorPublicKey:  &directAdaptorPubKey,
				},
			)
			directFromCpfpSigningJobs = append(
				directFromCpfpSigningJobs,
				&helper.SigningJob{
					JobID:             directFromCpfpJobID,
					SigningKeyshareID: signingKeyshare.ID,
					Message:           directFromCpfpRefundTxSigHash,
					VerifyingKey:      &leafVerifyingPubKey,
					UserCommitment:    directFromCpfpUserNonceCommitment,
					AdaptorPublicKey:  &directFromCpfpAdaptorPubKey,
				},
			)
			leafJobMap[directJobID] = leaf
			leafJobMap[directFromCpfpJobID] = leaf
		}
	}

	allSigningJobs := append(cpfpSigningJobs, directSigningJobs...)
	allSigningJobs = append(allSigningJobs, directFromCpfpSigningJobs...)

	allSigningResults, err := helper.SignFrost(ctx, config, allSigningJobs)
	if err != nil {
		return nil, fmt.Errorf("unable to sign frost for all signing jobs: %w", err)
	}

	cpfpSigningResults = allSigningResults[:len(cpfpSigningJobs)]
	directSigningResults = allSigningResults[len(cpfpSigningJobs) : len(cpfpSigningJobs)+len(directSigningJobs)]
	directFromCpfpSigningResults = allSigningResults[len(cpfpSigningJobs)+len(directSigningJobs):]

	// Create map to store results by leaf ID
	resultsByLeafID := make(map[string]*pb.LeafRefundTxSigningResult)

	// Process CPFP results
	for _, result := range cpfpSigningResults {
		leaf := leafJobMap[result.JobID]
		leafID := leaf.ID.String()

		cpfpSigningResultProto, err := result.MarshalProto()
		if err != nil {
			return nil, fmt.Errorf("unable to marshal cpfp signing result: %w", err)
		}

		resultsByLeafID[leafID] = &pb.LeafRefundTxSigningResult{
			LeafId:                leafID,
			RefundTxSigningResult: cpfpSigningResultProto,
			VerifyingKey:          leaf.VerifyingPubkey,
		}
	}

	// Process Direct results
	for _, result := range directSigningResults {
		leaf := leafJobMap[result.JobID]
		leafID := leaf.ID.String()

		directSigningResultProto, err := result.MarshalProto()
		if err != nil {
			return nil, fmt.Errorf("unable to marshal direct signing result: %w", err)
		}

		if existing, ok := resultsByLeafID[leafID]; ok {
			existing.DirectRefundTxSigningResult = directSigningResultProto
		}
	}

	// Process DirectFromCpfp results
	for _, result := range directFromCpfpSigningResults {
		leaf := leafJobMap[result.JobID]
		leafID := leaf.ID.String()

		directFromCpfpSigningResultProto, err := result.MarshalProto()
		if err != nil {
			return nil, fmt.Errorf("unable to marshal direct from cpfp signing result: %w", err)
		}

		if existing, ok := resultsByLeafID[leafID]; ok {
			existing.DirectFromCpfpRefundTxSigningResult = directFromCpfpSigningResultProto
		}
	}

	// Convert map to slice
	pbSigningResults := make([]*pb.LeafRefundTxSigningResult, 0, len(resultsByLeafID))
	for _, result := range resultsByLeafID {
		pbSigningResults = append(pbSigningResults, result)
	}

	return pbSigningResults, nil
}

func signRefundsWithPregeneratedNonce(
	ctx context.Context,
	config *so.Config,
	requests *pb.StartTransferRequest,
	leafMap map[string]*ent.TreeNode,
	cpfpAdaptorPubKey keys.Public,
	directAdaptorPubKey keys.Public,
	directFromCpfpAdaptorPubKey keys.Public,
) (map[string]*helper.SigningResult, map[string]*helper.SigningResult, map[string]*helper.SigningResult, error) {
	ctx, span := tracer.Start(ctx, "TransferHandler.signRefunds")
	defer span.End()

	leafJobMap := make(map[string]*ent.TreeNode)
	jobIsDirectRefund := make(map[string]bool)
	jobIsDirectFromCpfpRefund := make(map[string]bool)

	if requests.TransferPackage == nil {
		return nil, nil, nil, fmt.Errorf("transfer package is nil")
	}

	signingJobs := make([]*helper.SigningJobWithPregeneratedNonce, 0)
	for _, req := range requests.TransferPackage.LeavesToSend {
		leaf := leafMap[req.LeafId]
		refundTx, err := common.TxFromRawTxBytes(req.RawTx)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("unable to load new refund tx: %w", err)
		}

		leafTx, err := common.TxFromRawTxBytes(leaf.RawTx)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("unable to load leaf tx: %w", err)
		}
		if len(leafTx.TxOut) <= 0 {
			return nil, nil, nil, fmt.Errorf("vout out of bounds")
		}
		refundTxSigHash, err := common.SigHashFromTx(refundTx, 0, leafTx.TxOut[0])
		if err != nil {
			return nil, nil, nil, fmt.Errorf("unable to calculate sighash from refund tx: %w", err)
		}

		userNonceCommitment := objects.SigningCommitment{}
		err = userNonceCommitment.UnmarshalProto(req.SigningNonceCommitment)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("unable to unmarshal signing nonce commitment: %w", err)
		}
		cpfpJobID := uuid.New().String()
		jobIsDirectRefund[cpfpJobID] = false
		jobIsDirectFromCpfpRefund[cpfpJobID] = false

		signingKeyshare, err := leaf.QuerySigningKeyshare().Only(ctx)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("failed to get signing keyshare id: %w", err)
		}

		round1Packages := make(map[string]objects.SigningCommitment)
		for key, commitment := range req.SigningCommitments.SigningCommitments {
			obj := objects.SigningCommitment{}
			err = obj.UnmarshalProto(commitment)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("unable to unmarshal signing commitment: %w", err)
			}
			round1Packages[key] = obj
			if len(obj.Hiding) == 0 || len(obj.Binding) == 0 {
				return nil, nil, nil, fmt.Errorf("cpfp signing commitment is invalid for key %s: hiding or binding is empty", key)
			}
		}
		leafVerifyingPubKey, err := keys.ParsePublicKey(leaf.VerifyingPubkey)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("failed to parse verifying public key: %w", err)
		}
		signingJobs = append(
			signingJobs,
			&helper.SigningJobWithPregeneratedNonce{
				SigningJob: helper.SigningJob{
					JobID:             cpfpJobID,
					SigningKeyshareID: signingKeyshare.ID,
					Message:           refundTxSigHash,
					VerifyingKey:      &leafVerifyingPubKey,
					UserCommitment:    &userNonceCommitment,
					AdaptorPublicKey:  &cpfpAdaptorPubKey,
				},
				Round1Packages: round1Packages,
			},
		)
		leafJobMap[cpfpJobID] = leaf
	}

	// Create signing jobs for DIRECT refund txs.
	for _, req := range requests.TransferPackage.DirectLeavesToSend {
		leaf := leafMap[req.LeafId]
		directRefundTx, err := common.TxFromRawTxBytes(req.RawTx)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("unable to load new direct refund tx: %w", err)
		}

		directTx, err := common.TxFromRawTxBytes(leaf.DirectTx)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("unable to load leaf tx: %w", err)
		}
		if len(directTx.TxOut) <= 0 {
			return nil, nil, nil, fmt.Errorf("vout out of bounds")
		}
		directRefundTxSigHash, err := common.SigHashFromTx(directRefundTx, 0, directTx.TxOut[0])
		if err != nil {
			return nil, nil, nil, fmt.Errorf("unable to calculate sighash from direct refund tx: %w", err)
		}

		userNonceCommitment := objects.SigningCommitment{}
		err = userNonceCommitment.UnmarshalProto(req.SigningNonceCommitment)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("unable to unmarshal signing nonce commitment: %w", err)
		}

		directJobID := uuid.New().String()
		jobIsDirectRefund[directJobID] = true
		signingKeyshare, err := leaf.QuerySigningKeyshare().Only(ctx)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("failed to get signing keyshare id: %w", err)
		}

		round1Packages := make(map[string]objects.SigningCommitment)
		for key, commitment := range req.SigningCommitments.SigningCommitments {
			obj := objects.SigningCommitment{}
			if err = obj.UnmarshalProto(commitment); err != nil {
				return nil, nil, nil, fmt.Errorf("unable to unmarshal signing commitment: %w", err)
			}
			round1Packages[key] = obj
		}
		leafVerifyingPubKey, err := keys.ParsePublicKey(leaf.VerifyingPubkey)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("failed to parse verifying public key: %w", err)
		}
		signingJobs = append(signingJobs, &helper.SigningJobWithPregeneratedNonce{
			SigningJob: helper.SigningJob{
				JobID:             directJobID,
				SigningKeyshareID: signingKeyshare.ID,
				Message:           directRefundTxSigHash,
				VerifyingKey:      &leafVerifyingPubKey,
				UserCommitment:    &userNonceCommitment,
				AdaptorPublicKey:  &directAdaptorPubKey,
			},
			Round1Packages: round1Packages,
		})
		leafJobMap[directJobID] = leaf
	}
	// Create signing jobs for DIRECT FROM CPFP refund txs.
	for _, req := range requests.TransferPackage.DirectFromCpfpLeavesToSend {
		leaf := leafMap[req.LeafId]
		directFromCpfpRefundTx, err := common.TxFromRawTxBytes(req.RawTx)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("unable to load new direct from cpfp refund tx: %w", err)
		}
		directFromCpfpLeafTx, err := common.TxFromRawTxBytes(leaf.RawTx)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("unable to load leaf tx: %w", err)
		}
		if len(directFromCpfpLeafTx.TxOut) <= 0 {
			return nil, nil, nil, fmt.Errorf("vout out of bounds")
		}
		directFromCpfpRefundTxSigHash, err := common.SigHashFromTx(directFromCpfpRefundTx, 0, directFromCpfpLeafTx.TxOut[0])
		if err != nil {
			return nil, nil, nil, fmt.Errorf("unable to calculate sighash from direct from cpfp refund tx: %w", err)
		}

		userNonceCommitment := objects.SigningCommitment{}
		err = userNonceCommitment.UnmarshalProto(req.SigningNonceCommitment)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("unable to unmarshal signing nonce commitment: %w", err)
		}

		directFromCpfpJobID := uuid.New().String()
		jobIsDirectFromCpfpRefund[directFromCpfpJobID] = true
		signingKeyshare, err := leaf.QuerySigningKeyshare().Only(ctx)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("failed to get signing keyshare id: %w", err)
		}

		round1Packages := make(map[string]objects.SigningCommitment)
		for key, commitment := range req.SigningCommitments.SigningCommitments {
			obj := objects.SigningCommitment{}
			if err = obj.UnmarshalProto(commitment); err != nil {
				return nil, nil, nil, fmt.Errorf("unable to unmarshal signing commitment: %w", err)
			}
			round1Packages[key] = obj
		}
		leafVerifyingPubKey, err := keys.ParsePublicKey(leaf.VerifyingPubkey)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("failed to parse verifying public key: %w", err)
		}
		signingJobs = append(signingJobs, &helper.SigningJobWithPregeneratedNonce{
			SigningJob: helper.SigningJob{
				JobID:             directFromCpfpJobID,
				SigningKeyshareID: signingKeyshare.ID,
				Message:           directFromCpfpRefundTxSigHash,
				VerifyingKey:      &leafVerifyingPubKey,
				UserCommitment:    &userNonceCommitment,
				AdaptorPublicKey:  &directFromCpfpAdaptorPubKey,
			},
			Round1Packages: round1Packages,
		})
		leafJobMap[directFromCpfpJobID] = leaf
	}

	// Validate that no signing jobs have empty round1Packages
	for _, job := range signingJobs {
		if len(job.Round1Packages) == 0 {
			return nil, nil, nil, fmt.Errorf("signing job %s has empty round1Packages (message: %x)", job.SigningJob.JobID, job.SigningJob.Message)
		}
		for key, commitment := range job.Round1Packages {
			if len(commitment.Hiding) == 0 || len(commitment.Binding) == 0 {
				return nil, nil, nil, fmt.Errorf("signing job %s has invalid commitment for key %s: hiding or binding is empty (message: %x)", job.SigningJob.JobID, key, job.SigningJob.Message)
			}
		}
	}

	signingResults, err := helper.SignFrostWithPregeneratedNonce(ctx, config, signingJobs)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("unable to sign frost: %w", err)
	}

	cpfpResults := make(map[string]*helper.SigningResult)
	directResults := make(map[string]*helper.SigningResult)
	directFromCpfpResults := make(map[string]*helper.SigningResult)

	for _, signingResult := range signingResults {
		leaf := leafJobMap[signingResult.JobID]
		if jobIsDirectRefund[signingResult.JobID] {
			directResults[leaf.ID.String()] = signingResult
		} else if jobIsDirectFromCpfpRefund[signingResult.JobID] {
			directFromCpfpResults[leaf.ID.String()] = signingResult
		} else {
			cpfpResults[leaf.ID.String()] = signingResult
		}
	}
	return cpfpResults, directResults, directFromCpfpResults, nil
}

func aggregateSignatures(
	ctx context.Context,
	config *so.Config,
	req *pb.StartTransferRequest,
	cpfpAdaptorPubKey keys.Public,
	directAdaptorPubKey keys.Public,
	directFromCpfpAdaptorPubKey keys.Public,
	cpfpSigningResultMap map[string]*helper.SigningResult,
	directSigningResultMap map[string]*helper.SigningResult,
	directFromCpfpSigningResultMap map[string]*helper.SigningResult,
	leafMap map[string]*ent.TreeNode,
) (map[string][]byte, map[string][]byte, map[string][]byte, error) {
	finalCpfpSignatureMap := make(map[string][]byte)
	finalDirectSignatureMap := make(map[string][]byte)
	finalDirectFromCpfpSignatureMap := make(map[string][]byte)
	frostConn, err := config.NewFrostGRPCConnection()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("unable to connect to frost: %w", err)
	}
	defer frostConn.Close()
	frostClient := pbfrost.NewFrostServiceClient(frostConn)
	cpfpUserSignedRefunds := req.TransferPackage.LeavesToSend
	directUserSignedRefunds := req.TransferPackage.DirectLeavesToSend
	directFromCpfpUserSignedRefunds := req.TransferPackage.DirectFromCpfpLeavesToSend

	cpfpUserRefundMap := make(map[string]*pb.UserSignedTxSigningJob)
	directUserRefundMap := make(map[string]*pb.UserSignedTxSigningJob)
	directFromCpfpUserRefundMap := make(map[string]*pb.UserSignedTxSigningJob)
	for _, userSignedRefund := range cpfpUserSignedRefunds {
		cpfpUserRefundMap[userSignedRefund.LeafId] = userSignedRefund
	}
	for _, userSignedRefund := range directUserSignedRefunds {
		directUserRefundMap[userSignedRefund.LeafId] = userSignedRefund
	}
	for _, userSignedRefund := range directFromCpfpUserSignedRefunds {
		directFromCpfpUserRefundMap[userSignedRefund.LeafId] = userSignedRefund
	}
	logger := logging.GetLoggerFromContext(ctx)
	for leafID, signingResult := range cpfpSigningResultMap {
		logger.Info("aggregating cpfp frost signature", "leaf_id", leafID, "signing_msg", hex.EncodeToString(signingResult.Message))
		cpfpUserSignedRefund := cpfpUserRefundMap[leafID]
		leaf := leafMap[leafID]
		signatureResult, err := frostClient.AggregateFrost(ctx, &pbfrost.AggregateFrostRequest{
			Message:            signingResult.Message,
			SignatureShares:    signingResult.SignatureShares,
			PublicShares:       signingResult.PublicKeys,
			VerifyingKey:       leaf.VerifyingPubkey,
			Commitments:        cpfpUserSignedRefund.SigningCommitments.SigningCommitments,
			UserCommitments:    cpfpUserSignedRefund.SigningNonceCommitment,
			UserPublicKey:      leaf.OwnerSigningPubkey,
			UserSignatureShare: cpfpUserSignedRefund.UserSignature,
			AdaptorPublicKey:   cpfpAdaptorPubKey.Serialize(),
		})
		if err != nil {
			logger.Error("unable to aggregate frost for cpfp results", "error", err, "leaf_id", leaf.ID)
			return nil, nil, nil, fmt.Errorf("unable to aggregate frost for cpfp results: %w, leaf_id: %s", err, leaf.ID)
		}
		finalCpfpSignatureMap[leaf.ID.String()] = signatureResult.Signature
	}
	for leafID, signingResult := range directSigningResultMap {
		logger.Info("aggregating direct frost signature for direct results", "leaf_id", leafID, "signing_msg", hex.EncodeToString(signingResult.Message))
		directUserSignedRefund := directUserRefundMap[leafID]
		leaf := leafMap[leafID]
		signatureResult, err := frostClient.AggregateFrost(ctx, &pbfrost.AggregateFrostRequest{
			Message:            signingResult.Message,
			SignatureShares:    signingResult.SignatureShares,
			PublicShares:       signingResult.PublicKeys,
			VerifyingKey:       leaf.VerifyingPubkey,
			Commitments:        directUserSignedRefund.SigningCommitments.SigningCommitments,
			UserCommitments:    directUserSignedRefund.SigningNonceCommitment,
			UserPublicKey:      leaf.OwnerSigningPubkey,
			UserSignatureShare: directUserSignedRefund.UserSignature,
			AdaptorPublicKey:   directAdaptorPubKey.Serialize(),
		})
		if err != nil {
			logger.Error("unable to aggregate frost for direct results", "error", err, "leaf_id", leaf.ID)
			return nil, nil, nil, fmt.Errorf("unable to aggregate frost for direct results: %w, leaf_id: %s", err, leaf.ID)
		}
		finalDirectSignatureMap[leaf.ID.String()] = signatureResult.Signature
	}
	for leafID, signingResult := range directFromCpfpSigningResultMap {
		logger.Info("aggregating direct from cpfp frost signature for direct from cpfp results", "leaf_id", leafID, "signing_msg", hex.EncodeToString(signingResult.Message))
		directFromCpfpUserSignedRefund := directFromCpfpUserRefundMap[leafID]
		leaf := leafMap[leafID]
		signatureResult, err := frostClient.AggregateFrost(ctx, &pbfrost.AggregateFrostRequest{
			Message:            signingResult.Message,
			SignatureShares:    signingResult.SignatureShares,
			PublicShares:       signingResult.PublicKeys,
			VerifyingKey:       leaf.VerifyingPubkey,
			Commitments:        directFromCpfpUserSignedRefund.SigningCommitments.SigningCommitments,
			UserCommitments:    directFromCpfpUserSignedRefund.SigningNonceCommitment,
			UserPublicKey:      leaf.OwnerSigningPubkey,
			UserSignatureShare: directFromCpfpUserSignedRefund.UserSignature,
			AdaptorPublicKey:   directFromCpfpAdaptorPubKey.Serialize(),
		})
		if err != nil {
			logger.Error("unable to aggregate frost for direct from cpfp results", "error", err, "leaf_id", leaf.ID)
			return nil, nil, nil, fmt.Errorf("unable to aggregate frost for direct from cpfp results: %w, leaf_id: %s", err, leaf.ID)
		}
		finalDirectFromCpfpSignatureMap[leaf.ID.String()] = signatureResult.Signature
	}
	return finalCpfpSignatureMap, finalDirectSignatureMap, finalDirectFromCpfpSignatureMap, nil
}

// FinalizeTransfer completes a transfer from sender.
// Deprecated: use FinalizeTransferWithTransferPackage instead.
func (h *TransferHandler) FinalizeTransfer(ctx context.Context, req *pb.FinalizeTransferRequest) (*pb.FinalizeTransferResponse, error) {
	ctx, span := tracer.Start(ctx, "TransferHandler.FinalizeTransfer")
	defer span.End()

	logger := logging.GetLoggerFromContext(ctx)
	reqOwnerIDPubKey, err := keys.ParsePublicKey(req.OwnerIdentityPublicKey)
	if err != nil {
		return nil, fmt.Errorf("invalid identity public key: %w", err)
	}
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, reqOwnerIDPubKey); err != nil {
		return nil, err
	}

	transfer, err := h.loadTransferForUpdate(ctx, req.TransferId)
	if err != nil {
		return nil, fmt.Errorf("unable to load transfer %s: %w", req.TransferId, err)
	}
	span.SetAttributes(transferTypeKey.String(string(transfer.Type)))
	if !bytes.Equal(transfer.SenderIdentityPubkey, req.OwnerIdentityPublicKey) || transfer.Status != st.TransferStatusSenderInitiated {
		return nil, fmt.Errorf("send transfer cannot be completed %s, status: %s", req.TransferId, transfer.Status)
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}
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
	receiverIDPubKey, err := keys.ParsePublicKey(transfer.ReceiverIdentityPubkey)
	if err != nil {
		return nil, fmt.Errorf("unable to parse receiver identity public key: %w", err)
	}
	err = eventRouter.NotifyUser(receiverIDPubKey, &pb.SubscribeToEventsResponse{
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
	transfer, err := h.loadTransferForUpdate(ctx, req.TransferId)
	if err != nil {
		return nil, err
	}
	if transfer.Status != st.TransferStatusSenderInitiated {
		return nil, fmt.Errorf("transfer %s is in state %s; expected sender initiated status", req.TransferId, transfer.Status)
	}
	logger := logging.GetLoggerFromContext(ctx)
	logger.Info("Preparing to send key tweaks to other SOs", "transfer_id", req.TransferId)
	err = h.syncDeliverSenderKeyTweak(ctx, req, transfer.Type)
	if err != nil {
		dbTx, dbErr := ent.GetDbFromContext(ctx)
		if dbErr != nil {
			logger.Error("failed to get db tx", "error", dbErr)
		}
		if dbTx != nil {
			dbErr = dbTx.Rollback()
			if dbErr != nil {
				logger.Error("failed to rollback db tx", "error", dbErr)
			}
		}
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
		errorMsg := fmt.Sprintf("failed to sync deliver sender key tweak for transfer %s", req.TransferId)
		if stat, ok := status.FromError(err); ok && stat.Code() == codes.Unavailable {
			return nil, sparkerrors.UnavailableErrorf("%s: %w", errorMsg, err)
		}
		dbTx, dbErr = ent.GetDbFromContext(ctx)
		if dbErr != nil {
			logger.Error("failed to get db tx", "error", dbErr)
		}
		if dbTx != nil {
			dbErr = dbTx.Commit()
			if dbErr != nil {
				logger.Error("failed to commit db tx", "error", dbErr)
			}
		}
		return nil, fmt.Errorf("%s: %w", errorMsg, err)
	}
	logger.Info("Successfully delivered key tweaks to other SOs", "transfer_id", req.TransferId)

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}
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

	var stat st.TransferStatus
	if shouldTweakKey {
		stat = st.TransferStatusSenderInitiatedCoordinator
	} else {
		stat = st.TransferStatusSenderKeyTweakPending
	}
	transfer, err = transfer.Update().SetStatus(stat).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to update status of transfer %s: %w", req.TransferId, err)
	}
	ownerIDPubKey, err := keys.ParsePublicKey(req.OwnerIdentityPublicKey)
	if err != nil {
		return nil, fmt.Errorf("failed to parse owner identity public key: %w", err)
	}
	if err = h.setSoCoordinatorKeyTweaks(ctx, transfer, req.TransferPackage, ownerIDPubKey); err != nil {
		return nil, err
	}

	if shouldTweakKey {
		if err = db.Commit(); err != nil {
			return nil, fmt.Errorf("failed to commit transaction: %w", err)
		}
		err = h.settleSenderKeyTweaks(ctx, req.TransferId, pbinternal.SettleKeyTweakAction_COMMIT)
		if err != nil {
			return nil, err
		}

		transfer, err = h.loadTransferForUpdate(ctx, req.TransferId)
		if err != nil {
			return nil, fmt.Errorf("failed to load transfer for update: %w", err)
		}
		transfer, err = h.commitSenderKeyTweaks(ctx, transfer, true)
		if err != nil {
			// Too bad, at this point there's a bug where all other SOs has tweaked the key but
			// the coordinator failed so the fund is lost.
			return nil, err
		}
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

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to get or create current tx for request: %w", err)
	}
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

	cpfpRefundTxBytes, err := common.UpdateTxWithSignature(transferLeaf.IntermediateRefundTx, 0, req.RefundSignature)
	if err != nil {
		return fmt.Errorf("unable to update cpfp refund tx with signature: %w", err)
	}
	var directRefundTxBytes []byte
	var directFromCpfpRefundTxBytes []byte
	if transferLeaf.IntermediateDirectRefundTx != nil && req.DirectRefundSignature != nil && transferLeaf.IntermediateDirectFromCpfpRefundTx != nil && req.DirectFromCpfpRefundSignature != nil {
		directRefundTxBytes, err = common.UpdateTxWithSignature(transferLeaf.IntermediateDirectRefundTx, 0, req.DirectRefundSignature)
		if err != nil {
			return fmt.Errorf("unable to update direct refund tx with signature: %w", err)
		}
		directFromCpfpRefundTxBytes, err = common.UpdateTxWithSignature(transferLeaf.IntermediateDirectFromCpfpRefundTx, 0, req.DirectFromCpfpRefundSignature)
		if err != nil {
			return fmt.Errorf("unable to update direct from cpfp refund tx with signature: %w", err)
		}
	}

	if transfer.Type != st.TransferTypePreimageSwap && transfer.Type != st.TransferTypeUtxoSwap {
		// Verify signature
		cpfpRefundTx, err := common.TxFromRawTxBytes(cpfpRefundTxBytes)
		if err != nil {
			return fmt.Errorf("unable to deserialize cpfp refund tx: %w", err)
		}

		cpfpLeafNodeTx, err := common.TxFromRawTxBytes(leaf.RawTx)
		if err != nil {
			return fmt.Errorf("unable to deserialize cpfp leaf tx: %w", err)
		}

		if len(cpfpLeafNodeTx.TxOut) <= 0 {
			return fmt.Errorf("vout out of bounds")
		}
		if !cpfpRefundTx.HasWitness() {
			logger.Warn("transaction has no witness", "cpfpRefundtx", cpfpRefundTx)
		}
		err = common.VerifySignatureSingleInput(cpfpRefundTx, 0, cpfpLeafNodeTx.TxOut[0])
		if err != nil {
			logger.Error("unable to verify cpfp refund tx signature", "error", err, "refundTx", cpfpRefundTx)
			return fmt.Errorf("unable to verify cpfp refund tx signature: %w", err)
		}

		directRefundTx := &wire.MsgTx{}
		directFromCpfpRefundTx := &wire.MsgTx{}
		if len(directRefundTxBytes) > 0 && len(directFromCpfpRefundTxBytes) > 0 {
			directRefundTx, err = common.TxFromRawTxBytes(directRefundTxBytes)
			if err != nil {
				return fmt.Errorf("unable to deserialize direct refund tx: %w", err)
			}
			directFromCpfpRefundTx, err = common.TxFromRawTxBytes(directFromCpfpRefundTxBytes)
			if err != nil {
				return fmt.Errorf("unable to deserialize direct from cpfp refund tx: %w", err)
			}
			directLeafNodeTx, err := common.TxFromRawTxBytes(leaf.DirectTx)
			if err != nil {
				return fmt.Errorf("unable to deserialize direct leaf tx: %w", err)
			}
			if len(directLeafNodeTx.TxOut) <= 0 {
				return fmt.Errorf("vout out of bounds")
			}
			if !directRefundTx.HasWitness() {
				logger.Warn("transaction has no witness", "directRefundTx", directRefundTx)
			}
			if !directFromCpfpRefundTx.HasWitness() {
				logger.Warn("transaction has no witness", "directFromCpfpRefundTx", directFromCpfpRefundTx)
			}
			err = common.VerifySignatureSingleInput(directRefundTx, 0, directLeafNodeTx.TxOut[0])
			if err != nil {
				logger.Error("unable to verify direct refund tx signature", "error", err, "refundTx", directRefundTx)
				return fmt.Errorf("unable to verify direct refund tx signature: %w", err)
			}
			err = common.VerifySignatureSingleInput(directFromCpfpRefundTx, 0, cpfpLeafNodeTx.TxOut[0])
			if err != nil {
				logger.Error("unable to verify direct from cpfp refund tx signature", "error", err, "refundTx", directFromCpfpRefundTx)
				return fmt.Errorf("unable to verify direct from cpfp refund tx signature: %w", err)
			}
		}
	}

	transferLeafMutator := db.TransferLeaf.
		UpdateOne(transferLeaf).
		SetIntermediateRefundTx(cpfpRefundTxBytes).
		SetIntermediateDirectRefundTx(directRefundTxBytes).
		SetIntermediateDirectFromCpfpRefundTx(directFromCpfpRefundTxBytes).
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
		treeNodeUpdate, err := helper.TweakLeafKeyUpdate(ctx, leaf, req)
		if err != nil {
			return fmt.Errorf("unable to tweak leaf key: %w", err)
		}
		if len(cpfpRefundTxBytes) > 0 {
			treeNodeUpdate.SetRawRefundTx(cpfpRefundTxBytes)
		}
		if len(directRefundTxBytes) > 0 {
			treeNodeUpdate.SetDirectRefundTx(directRefundTxBytes)
		}
		if len(directFromCpfpRefundTxBytes) > 0 {
			treeNodeUpdate.SetDirectFromCpfpRefundTx(directFromCpfpRefundTxBytes)
		}
		err = treeNodeUpdate.Exec(ctx)
		if err != nil {
			return fmt.Errorf("unable to update tree node: %w", err)
		}
	}

	return nil
}

func (h *TransferHandler) queryTransfers(ctx context.Context, filter *pb.TransferFilter, isPending bool) (*pb.QueryTransfersResponse, error) {
	ctx, span := tracer.Start(ctx, "TransferHandler.queryTransfers")
	defer span.End()

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

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
		receiverIDPubKey, err := keys.ParsePublicKey(filter.GetReceiverIdentityPublicKey())
		if err != nil {
			return nil, fmt.Errorf("invalid receiver identity public key: %w", err)
		}
		if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, receiverIDPubKey); err != nil {
			return nil, err
		}
		transferPredicate = append(transferPredicate, enttransfer.ReceiverIdentityPubkeyEQ(filter.GetReceiverIdentityPublicKey()))
		if isPending {
			transferPredicate = append(transferPredicate,
				enttransfer.StatusIn(receiverPendingStatuses...),
			)
		}
	case *pb.TransferFilter_SenderIdentityPublicKey:
		senderIDPubKey, err := keys.ParsePublicKey(filter.GetSenderIdentityPublicKey())
		if err != nil {
			return nil, fmt.Errorf("invalid sender identity public key: %w", err)
		}
		if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, senderIDPubKey); err != nil {
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
		identityPubKey, err := keys.ParsePublicKey(filter.GetSenderOrReceiverIdentityPublicKey())
		if err != nil {
			return nil, fmt.Errorf("invalid identity public key: %w", err)
		}
		if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, identityPubKey); err != nil {
			return nil, err
		}
		if isPending {
			transferPredicate = append(transferPredicate, enttransfer.Or(
				enttransfer.And(
					enttransfer.ReceiverIdentityPubkeyEQ(identityPubKey.Serialize()),
					enttransfer.StatusIn(receiverPendingStatuses...),
				),
				enttransfer.And(
					enttransfer.SenderIdentityPubkeyEQ(identityPubKey.Serialize()),
					enttransfer.StatusIn(senderPendingStatuses...),
					enttransfer.ExpiryTimeLT(time.Now()),
				),
			))
		} else {
			transferPredicate = append(transferPredicate, enttransfer.Or(
				enttransfer.ReceiverIdentityPubkeyEQ(identityPubKey.Serialize()),
				enttransfer.SenderIdentityPubkeyEQ(identityPubKey.Serialize()),
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
		for i, stat := range filter.Statuses {
			var err error
			statuses[i], err = ent.TransferStatusSchema(stat)
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

	var query *ent.TransferQuery
	if filter.Order == pb.Order_ASCENDING {
		query = baseQuery.Order(ent.Asc(enttransfer.FieldUpdateTime))
	} else {
		query = baseQuery.Order(ent.Desc(enttransfer.FieldUpdateTime))
	}

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

	var transferProtos []*pb.Transfer
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
		return sparkerrors.FailedPreconditionErrorf("coop exit tx hasn't been broadcasted")
	}
	if coopExit.ConfirmationHeight+CoopExitConfirmationThreshold-1 > blockHeight.Height {
		return sparkerrors.FailedPreconditionErrorf("coop exit tx doesn't have enough confirmations: confirmation height: %d current block height: %d", coopExit.ConfirmationHeight, blockHeight.Height)
	}
	return nil
}

// ClaimTransferTweakKeys starts claiming a pending transfer by tweaking keys of leaves.
func (h *TransferHandler) ClaimTransferTweakKeys(ctx context.Context, req *pb.ClaimTransferTweakKeysRequest) error {
	ctx, span := tracer.Start(ctx, "TransferHandler.ClaimTransferTweakKeys")
	defer span.End()
	reqOwnerIDPubKey, err := keys.ParsePublicKey(req.OwnerIdentityPublicKey)
	if err != nil {
		return fmt.Errorf("invalid identity public key: %w", err)
	}
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, reqOwnerIDPubKey); err != nil {
		return err
	}

	transfer, err := h.loadTransferForUpdate(ctx, req.TransferId)
	if err != nil {
		return fmt.Errorf("unable to load transfer %s: %w", req.TransferId, err)
	}
	span.SetAttributes(transferTypeKey.String(string(transfer.Type)))
	if !bytes.Equal(transfer.ReceiverIdentityPubkey, req.OwnerIdentityPublicKey) {
		return fmt.Errorf("cannot claim transfer %s, receiver identity public key mismatch", req.TransferId)
	}
	// Validate transfer is not in terminal states
	if transfer.Status == st.TransferStatusCompleted {
		return sparkerrors.AlreadyExistsErrorf("transfer %s has already been claimed", req.TransferId)
	}
	if transfer.Status == st.TransferStatusExpired ||
		transfer.Status == st.TransferStatusReturned {
		return sparkerrors.FailedPreconditionErrorf("transfer %s is in terminal state %s and cannot be processed", req.TransferId, transfer.Status)
	}
	if transfer.Status != st.TransferStatusSenderKeyTweaked {
		return sparkerrors.FailedPreconditionErrorf("please call ClaimTransferSignRefunds to claim the transfer %s, the transfer is not in SENDER_KEY_TWEAKED status. transferstatus: %s,", req.TransferId, transfer.Status)
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to get or create current tx for request: %w", err)
	}
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
	keyshareID := keyshare.ID.String()
	keyshare, err = keyshare.TweakKeyShare(
		ctx,
		req.SecretShareTweak.SecretShare,
		req.SecretShareTweak.Proofs[0],
		req.PubkeySharesTweak,
	)
	if err != nil {
		return fmt.Errorf("unable to tweak keyshare %s for leaf %s: %w", keyshareID, leaf.ID.String(), err)
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

func (h *TransferHandler) getLeavesFromTransfer(ctx context.Context, transfer *ent.Transfer) (map[string]*ent.TreeNode, error) {
	ctx, span := tracer.Start(ctx, "TransferHandler.getLeavesFromTransfer", trace.WithAttributes(
		transferTypeKey.String(string(transfer.Type)),
	))
	defer span.End()

	transferLeaves, err := transfer.QueryTransferLeaves().WithLeaf().All(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get leaves for transfer %s: %w", transfer.ID.String(), err)
	}
	leaves := make(map[string]*ent.TreeNode, len(transferLeaves))
	for _, transferLeaf := range transferLeaves {
		leaves[transferLeaf.Edges.Leaf.ID.String()] = transferLeaf.Edges.Leaf
	}
	return leaves, nil
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
	selection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}
	_, err := helper.ExecuteTaskWithAllOperators(ctx, h.config, &selection, func(ctx context.Context, operator *so.SigningOperator) (any, error) {
		conn, err := operator.NewOperatorGRPCConnection()
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
	logger := logging.GetLoggerFromContext(ctx)
	if err != nil {
		logger.Error("Unable to settle receiver key tweak, you might have a race condition in your implementation", "error", err.Error())
		action = pbinternal.SettleKeyTweakAction_ROLLBACK
	}

	err = h.InitiateSettleReceiverKeyTweak(ctx, &pbinternal.InitiateSettleReceiverKeyTweakRequest{
		TransferId:     transfer.ID.String(),
		KeyTweakProofs: keyTweakProofs,
		UserPublicKeys: userPublicKeys,
	})
	if err != nil {
		logger.Error("Unable to settle receiver key tweak internally, you might have a race condition in your implementation", "error", err.Error())
		action = pbinternal.SettleKeyTweakAction_ROLLBACK
	}

	if h.mockAction != nil {
		if h.mockAction.InterruptTransfer {
			return fmt.Errorf("transfer interrupted")
		}
	}

	_, err = helper.ExecuteTaskWithAllOperators(ctx, h.config, &selection, func(ctx context.Context, operator *so.SigningOperator) (any, error) {
		conn, err := operator.NewOperatorGRPCConnection()
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
	} else {
		err = h.SettleReceiverKeyTweak(ctx, &pbinternal.SettleReceiverKeyTweakRequest{
			TransferId: transfer.ID.String(),
			Action:     action,
		})
		if err != nil {
			return fmt.Errorf("unable to settle receiver key tweak: %w", err)
		}
	}
	if action == pbinternal.SettleKeyTweakAction_ROLLBACK {
		return fmt.Errorf("unable to settle receiver key tweak; rolled back")
	}
	return nil
}

// ClaimTransferSignRefunds signs new refund transactions as part of the transfer.
func (h *TransferHandler) ClaimTransferSignRefundsV2(ctx context.Context, req *pb.ClaimTransferSignRefundsRequest) (*pb.ClaimTransferSignRefundsResponse, error) {
	return h.claimTransferSignRefunds(ctx, req, true)
}

// ClaimTransferSignRefunds signs new refund transactions as part of the transfer.
func (h *TransferHandler) ClaimTransferSignRefunds(ctx context.Context, req *pb.ClaimTransferSignRefundsRequest) (*pb.ClaimTransferSignRefundsResponse, error) {
	transferID := req.TransferId

	if _, loaded := h.activeClaimTransferSignRefunds.LoadOrStore(transferID, struct{}{}); loaded {
		return nil, status.Errorf(codes.ResourceExhausted, "transfer %s is being processed by another request, please try again later", transferID)
	}
	defer h.activeClaimTransferSignRefunds.Delete(transferID)

	return h.claimTransferSignRefunds(ctx, req, false)
}

// ClaimTransferSignRefunds signs new refund transactions as part of the transfer.
func (h *TransferHandler) claimTransferSignRefunds(ctx context.Context, req *pb.ClaimTransferSignRefundsRequest, requireDirectTx bool) (*pb.ClaimTransferSignRefundsResponse, error) {
	ctx, span := tracer.Start(ctx, "TransferHandler.ClaimTransferSignRefunds")
	defer span.End()
	reqOwnerIDPubKey, err := keys.ParsePublicKey(req.OwnerIdentityPublicKey)
	if err != nil {
		return nil, fmt.Errorf("invalid identity public key: %w", err)
	}
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, reqOwnerIDPubKey); err != nil {
		return nil, err
	}

	transfer, err := h.loadTransferNoUpdate(ctx, req.TransferId)
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
	case st.TransferStatusCompleted:
		return nil, sparkerrors.AlreadyExistsErrorf("transfer %s has already been claimed", req.TransferId)
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

	// Lock the transfer after the key tweak is settled.
	transfer, err = h.loadTransferForUpdate(ctx, req.TransferId)
	if err != nil {
		return nil, fmt.Errorf("unable to load transfer %s: %w", req.TransferId, err)
	}
	if transfer.Status == st.TransferStatusCompleted {
		return nil, fmt.Errorf("transfer %s is already completed", req.TransferId)
	}

	// Update transfer status.
	_, err = transfer.Update().SetStatus(st.TransferStatusReceiverRefundSigned).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to update transfer status %s: %w", transfer.ID.String(), err)
	}

	leaves, err := h.getLeavesFromTransfer(ctx, transfer)
	if err != nil {
		return nil, err
	}

	var signingJobs []*helper.SigningJob
	jobToLeafMap := make(map[string]uuid.UUID)
	isDirectSigningJob := make(map[string]bool)
	isDirectFromCpfpSigningJob := make(map[string]bool)
	for _, job := range req.SigningJobs {
		leaf, exists := leaves[job.LeafId]
		if !exists {
			return nil, fmt.Errorf("unexpected leaf id %s", job.LeafId)
		}

		directRefundTxSigningJob := (*pb.SigningJob)(nil)
		directFromCpfpRefundTxSigningJob := (*pb.SigningJob)(nil)
		if job.DirectRefundTxSigningJob != nil {
			directRefundTxSigningJob = job.DirectRefundTxSigningJob
		} else if requireDirectTx && len(leaf.DirectTx) > 0 {
			return nil, fmt.Errorf("DirectRefundTxSigningJob is required. Please upgrade to the latest SDK version")
		}
		if job.DirectFromCpfpRefundTxSigningJob != nil {
			directFromCpfpRefundTxSigningJob = job.DirectFromCpfpRefundTxSigningJob
		} else if requireDirectTx && len(leaf.DirectTx) > 0 {
			return nil, fmt.Errorf("DirectFromCpfpRefundTxSigningJob is required. Please upgrade to the latest SDK version")
		}
		var directRefundTx []byte
		var directFromCpfpRefundTx []byte
		if directRefundTxSigningJob != nil {
			directRefundTx = directRefundTxSigningJob.RawTx
		}
		if directFromCpfpRefundTxSigningJob != nil {
			directFromCpfpRefundTx = directFromCpfpRefundTxSigningJob.RawTx
		}

		leafID := leaf.ID.String()
		leaf, err := leaf.Update().
			SetRawRefundTx(job.RefundTxSigningJob.RawTx).
			SetDirectRefundTx(directRefundTx).
			SetDirectFromCpfpRefundTx(directFromCpfpRefundTx).
			Save(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to update leaf refund tx %s: %w", leafID, err)
		}

		cpfpSigningJob, directSigningJob, directFromCpfpSigningJob, err := h.getRefundTxSigningJobs(ctx, leaf, job.RefundTxSigningJob, job.DirectRefundTxSigningJob, job.DirectFromCpfpRefundTxSigningJob)
		if err != nil {
			return nil, fmt.Errorf("unable to create signing jobs for leaf %s: %w", leafID, err)
		}
		signingJobs = append(signingJobs, cpfpSigningJob)
		jobToLeafMap[cpfpSigningJob.JobID] = leaf.ID
		isDirectSigningJob[cpfpSigningJob.JobID] = false
		isDirectFromCpfpSigningJob[cpfpSigningJob.JobID] = false
		if directSigningJob != nil && directFromCpfpSigningJob != nil {
			signingJobs = append(signingJobs, directSigningJob, directFromCpfpSigningJob)
			jobToLeafMap[directSigningJob.JobID] = leaf.ID
			isDirectSigningJob[directSigningJob.JobID] = true
			jobToLeafMap[directFromCpfpSigningJob.JobID] = leaf.ID
			isDirectFromCpfpSigningJob[directFromCpfpSigningJob.JobID] = true
		}
	}

	// Signing
	signingResults, err := helper.SignFrost(ctx, h.config, signingJobs)
	if err != nil {
		return nil, err
	}

	// Group signing results by leaf ID
	leafSigningResults := make(map[string]*pb.LeafRefundTxSigningResult)

	for _, signingResult := range signingResults {
		leafID := jobToLeafMap[signingResult.JobID]
		leaf := leaves[leafID.String()]
		signingResultProto, err := signingResult.MarshalProto()
		if err != nil {
			return nil, err
		}

		// Get or create the signing result for this leaf
		leafResult, exists := leafSigningResults[leafID.String()]
		if !exists {
			leafResult = &pb.LeafRefundTxSigningResult{
				LeafId:       leafID.String(),
				VerifyingKey: leaf.VerifyingPubkey,
			}
			leafSigningResults[leafID.String()] = leafResult
		}

		// Set the appropriate field based on whether this is a direct signing job
		if isDirectSigningJob[signingResult.JobID] {
			leafResult.DirectRefundTxSigningResult = signingResultProto
		} else if isDirectFromCpfpSigningJob[signingResult.JobID] {
			leafResult.DirectFromCpfpRefundTxSigningResult = signingResultProto
		} else {
			leafResult.RefundTxSigningResult = signingResultProto
		}
	}

	// Convert map to slice
	signingResultProtos := make([]*pb.LeafRefundTxSigningResult, 0, len(leafSigningResults))
	for _, result := range leafSigningResults {
		signingResultProtos = append(signingResultProtos, result)
	}

	return &pb.ClaimTransferSignRefundsResponse{SigningResults: signingResultProtos}, nil
}

func (h *TransferHandler) getRefundTxSigningJobs(ctx context.Context, leaf *ent.TreeNode, cpfpJob *pb.SigningJob, directJob *pb.SigningJob, directFromCpfpJob *pb.SigningJob) (*helper.SigningJob, *helper.SigningJob, *helper.SigningJob, error) {
	ctx, span := tracer.Start(ctx, "TransferHandler.getRefundTxSigningJob")
	defer span.End()

	keyshare, err := leaf.QuerySigningKeyshare().First(ctx)
	if err != nil || keyshare == nil {
		return nil, nil, nil, fmt.Errorf("unable to load keyshare for leaf %s: %w", leaf.ID.String(), err)
	}
	cpfpLeafTx, err := common.TxFromRawTxBytes(leaf.RawTx)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("unable to load cpfp leaf tx for leaf %s: %w", leaf.ID.String(), err)
	}
	directRefundSigningJob := (*helper.SigningJob)(nil)
	directFromCpfpRefundSigningJob := (*helper.SigningJob)(nil)
	if len(leaf.DirectTx) > 0 && directJob != nil && directFromCpfpJob != nil {
		directLeafTx, err := common.TxFromRawTxBytes(leaf.DirectTx)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("unable to load direct leaf tx for leaf %s: %w", leaf.ID.String(), err)
		}
		if len(directLeafTx.TxOut) <= 0 {
			return nil, nil, nil, fmt.Errorf("vout out of bounds for direct tx")
		}
		directRefundSigningJob, _, err = helper.NewSigningJob(keyshare, directJob, directLeafTx.TxOut[0])
		if err != nil {
			return nil, nil, nil, fmt.Errorf("unable to create direct signing job for leaf %s: %w", leaf.ID.String(), err)
		}
		directFromCpfpRefundSigningJob, _, err = helper.NewSigningJob(keyshare, directFromCpfpJob, cpfpLeafTx.TxOut[0])
		if err != nil {
			return nil, nil, nil, fmt.Errorf("unable to create direct from cpfp signing job for leaf %s: %w", leaf.ID.String(), err)
		}
	}
	if len(cpfpLeafTx.TxOut) <= 0 {
		return nil, nil, nil, fmt.Errorf("vout out of bounds for cpfp tx")
	}
	cpfpRefundSigningJob, _, err := helper.NewSigningJob(keyshare, cpfpJob, cpfpLeafTx.TxOut[0])
	if err != nil {
		return nil, nil, nil, fmt.Errorf("unable to create cpfp signing job for leaf %s: %w", leaf.ID.String(), err)
	}
	return cpfpRefundSigningJob, directRefundSigningJob, directFromCpfpRefundSigningJob, nil
}

func (h *TransferHandler) InitiateSettleReceiverKeyTweak(ctx context.Context, req *pbinternal.InitiateSettleReceiverKeyTweakRequest) error {
	ctx, span := tracer.Start(ctx, "TransferHandler.InitiateSettleReceiverKeyTweak")
	defer span.End()

	transfer, err := h.loadTransferForUpdate(ctx, req.TransferId)
	if err != nil {
		return fmt.Errorf("unable to load transfer %s: %w", req.TransferId, err)
	}
	span.SetAttributes(transferTypeKey.String(string(transfer.Type)))

	if transfer.Status == st.TransferStatusCompleted {
		// The transfer is already completed, return early.
		return nil
	}

	applied, err := h.checkIfKeyTweakApplied(ctx, transfer, req.UserPublicKeys)
	if err != nil {
		return fmt.Errorf("unable to check if key tweak is applied: %w", err)
	}
	if applied {
		_, err = transfer.Update().SetStatus(st.TransferStatusReceiverKeyTweakApplied).Save(ctx)
		if err != nil {
			return fmt.Errorf("unable to update transfer status %s: %w", transfer.ID.String(), err)
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
		return fmt.Errorf("unable to update transfer status %s: %w", transfer.ID.String(), err)
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("unable to get db: %w", err)
	}
	err = db.Commit()
	if err != nil {
		return fmt.Errorf("unable to commit db: %w", err)
	}

	return nil
}

func (h *TransferHandler) checkIfKeyTweakApplied(ctx context.Context, transfer *ent.Transfer, userPublicKeys map[string][]byte) (bool, error) {
	leaves, err := transfer.QueryTransferLeaves().QueryLeaf().WithSigningKeyshare().All(ctx)
	if err != nil {
		return false, fmt.Errorf("unable to get leaves from transfer %s: %w", transfer.ID.String(), err)
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
			return false, fmt.Errorf("unable to add public keys for leaf %s: %w", leaf.ID.String(), err)
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

	transfer, err := h.loadTransferForUpdate(ctx, req.TransferId)
	if err != nil {
		return fmt.Errorf("unable to load transfer %s: %w", req.TransferId, err)
	}
	span.SetAttributes(transferTypeKey.String(string(transfer.Type)))

	if transfer.Status == st.TransferStatusReceiverKeyTweakApplied || transfer.Status == st.TransferStatusCompleted {
		// The receiver key tweak is already applied, return early.
		return nil
	}

	switch req.Action {
	case pbinternal.SettleKeyTweakAction_COMMIT:
		leaves, err := transfer.QueryTransferLeaves().WithLeaf().All(ctx)
		if err != nil {
			return fmt.Errorf("unable to get leaves from transfer %s: %w", req.TransferId, err)
		}
		for _, leaf := range leaves {
			treeNode := leaf.Edges.Leaf
			if treeNode == nil {
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
		leaves, err := transfer.QueryTransferLeaves().All(ctx)
		if err != nil {
			return fmt.Errorf("unable to get leaves from transfer %s: %w", req.TransferId, err)
		}
		err = h.revertClaimTransfer(ctx, transfer, leaves)
		if err != nil {
			return fmt.Errorf("unable to revert claim transfer %s: %w", transfer.ID.String(), err)
		}
	default:
		return fmt.Errorf("invalid action %s", req.Action)
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("unable to get db: %w", err)
	}
	err = db.Commit()
	if err != nil {
		return fmt.Errorf("unable to commit db: %w", err)
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

		receiverIDPubKey, err := keys.ParsePublicKey(transfer.ReceiverIdentityPubkey)
		if err != nil {
			return fmt.Errorf("unable to parse receiver identity public key: %w", err)
		}
		eventRouter := events.GetDefaultRouter()
		err = eventRouter.NotifyUser(receiverIDPubKey, &pb.SubscribeToEventsResponse{
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
	reqOwnerIDPubKey, err := keys.ParsePublicKey(req.OwnerIdentityPublicKey)
	if err != nil {
		return nil, fmt.Errorf("invalid identity public key: %w", err)
	}
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, reqOwnerIDPubKey); err != nil {
		return nil, err
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	if len(req.TransferId) > 0 {
		transfer, err := h.loadTransferNoUpdate(ctx, req.TransferId)
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

// setSoCoordinatorKeyTweaks sets the key tweaks for each transfer leaf based on the validated transfer package.
func (h *TransferHandler) setSoCoordinatorKeyTweaks(ctx context.Context, transfer *ent.Transfer, req *pb.TransferPackage, ownerIdentityPubKey keys.Public) error {
	// Get key tweak map from transfer package
	keyTweakMap, err := h.validateTransferPackage(ctx, transfer.ID.String(), req, ownerIdentityPubKey)
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
			_, err = transferLeaf.Update().SetKeyTweak(keyTweakBinary).SetSecretCipher(keyTweak.SecretCipher).SetSignature(keyTweak.Signature).Save(ctx)
			if err != nil {
				return fmt.Errorf("failed to set key tweak for transfer leaf %s: %w", transferLeaf.ID, err)
			}
		}
	}
	return nil
}
