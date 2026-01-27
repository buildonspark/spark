package handler

import (
	"bytes"
	"context"
	"fmt"
	"math/big"
	"slices"
	"strings"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/uuids"
	"github.com/lightsparkdev/spark/so/frost"
	"go.uber.org/zap"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	bitcointransaction "github.com/lightsparkdev/spark/common/bitcoin_transaction"
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
	"github.com/lightsparkdev/spark/so/ent/pendingsendtransfer"
	"github.com/lightsparkdev/spark/so/ent/predicate"
	"github.com/lightsparkdev/spark/so/ent/preimagerequest"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	enttransfer "github.com/lightsparkdev/spark/so/ent/transfer"
	enttransferleaf "github.com/lightsparkdev/spark/so/ent/transferleaf"
	enttreenode "github.com/lightsparkdev/spark/so/ent/treenode"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/helper"
	"github.com/lightsparkdev/spark/so/knobs"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
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

type TransferAdaptorPublicKeys struct {
	cpfpAdaptorPubKey           keys.Public
	directAdaptorPubKey         keys.Public
	directFromCpfpAdaptorPubKey keys.Public
}

// StartCounterTransferInternal is a helper function to call startTransferInternal from the SSP handler for Swap V3 counter swap initiation.
// Will pass adaptor pubkeys and enable key tweak for both transfers of the swap.
func (h *TransferHandler) StartCounterTransferInternal(ctx context.Context, req *pb.StartTransferRequest, adaptorPublicKeys TransferAdaptorPublicKeys, primaryTransferId uuid.UUID) (*pb.StartTransferResponse, error) {
	return h.startTransferInternal(ctx, req, st.TransferTypeCounterSwapV3, adaptorPublicKeys.cpfpAdaptorPubKey, adaptorPublicKeys.directAdaptorPubKey, adaptorPublicKeys.directFromCpfpAdaptorPubKey, false, &SwapV3Package{primaryTransferId: primaryTransferId})
}

// If this package is provided then the handler should execute SwapV3 logic.
type SwapV3Package struct {
	primaryTransferId uuid.UUID
}

// startTransferInternal initiates a transfer between two parties by validating the transfer request,
// creating transfer records, signing refund transactions, and coordinating with other service operators.
//
// This is the core internal method that handles the transfer initiation logic for different transfer types
// including regular transfers, swaps, cooperative exits, and preimage swaps.
//
// Parameters:
//   - ctx: Request context for tracing and logging
//   - req: StartTransferRequest containing transfer details, leaves to send, and participant public keys
//   - transferType: Type of transfer (TRANSFER, SWAP, COOPERATIVE_EXIT, PREIMAGE_SWAP, etc.)
//   - cpfpAdaptorPubKey: Adaptor signature / public key used for CPFP (Child Pays for Parent) refund transaction signing
//   - directAdaptorPubKey: Adaptor signature / public key used for direct refund transaction signing
//   - directFromCpfpAdaptorPubKey: Adaptor signature / public key used for direct-from-CPFP refund transaction signing
//   - requireDirectTx: Whether direct transactions are required for this flow. If true and there is no direct transaction, the validation will fail.
//   - tweakKeys: Whether to perform sender key tweaking operations as part of the transfer. Normally set to true. Only needed for Swap V3 flow when initiating a primary transfer.
//
// The method performs the following key operations:
//  1. Validates the owner's identity and enforces authorization
//  2. Validates the transfer package containing leaves and key tweaks
//  3. Enforces transfer limits if configured via knobs
//  4. Creates the transfer record and associated leaf mappings in the database
//  5. Signs refund transactions (CPFP, direct, and direct-from-CPFP variants)
//  6. Coordinates with other service operators to validate and finalize the transfer
//  7. Optionally handles key tweaking and settlement
//
// Returns:
//   - StartTransferResponse: Contains the created transfer details and signing results for refund transactions
//   - error: Any validation, signing, or coordination errors encountered during the process
//
// The method ensures atomicity by rolling back changes if any step fails, and marks the transfer
// as successful only after all service operators have validated the transfer package.
func (h *TransferHandler) startTransferInternal(ctx context.Context, req *pb.StartTransferRequest, transferType st.TransferType, cpfpAdaptorPubKey keys.Public, directAdaptorPubKey keys.Public, directFromCpfpAdaptorPubKey keys.Public, requireDirectTx bool, swapV3Package *SwapV3Package) (*pb.StartTransferResponse, error) {
	logger := logging.GetLoggerFromContext(ctx)

	ctx, span := tracer.Start(ctx, "TransferHandler.startTransferInternal", trace.WithAttributes(
		transferTypeKey.String(string(transferType)),
	))
	defer span.End()

	reqOwnerIdentityPubKey, err := keys.ParsePublicKey(req.GetOwnerIdentityPublicKey())
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("failed to parse owner identity public key: %w", err))
	}
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, reqOwnerIdentityPubKey); err != nil {
		return nil, err
	}

	transferID, err := uuid.Parse(req.GetTransferId())
	if err != nil {
		return nil, fmt.Errorf("invalid transfer id: %w", err)
	}
	leafTweakMap, err := h.ValidateTransferPackage(ctx, transferID, req.TransferPackage, reqOwnerIdentityPubKey)
	if err != nil {
		return nil, fmt.Errorf("failed to validate transfer package for transfer %s: %w", transferID, err)
	}

	knobService := knobs.GetKnobsService(ctx)
	if knobService != nil {
		transferLimit := knobService.GetValue(knobs.KnobSoTransferLimit, 0)
		if transferLimit > 0 && (len(leafTweakMap) > int(transferLimit) || len(req.LeavesToSend) > int(transferLimit)) {
			return nil, status.Errorf(codes.InvalidArgument, "transfer limit reached, please send %d leaves at a time", int(transferLimit))
		}

		// Validate that TransferTypeTransfer requires a transfer package when October deprecation is enabled
		if req.TransferPackage == nil && transferType == st.TransferTypeTransfer {
			return nil, status.Errorf(codes.InvalidArgument, "transfer package is required for TransferTypeTransfer")
		}
	}

	leafCpfpRefundMap := h.loadCpfpLeafRefundMap(req)
	leafDirectRefundMap := h.loadDirectLeafRefundMap(req)
	leafDirectFromCpfpRefundMap := h.loadDirectFromCpfpLeafRefundMap(req)

	receiverIdentityPubKey, err := keys.ParsePublicKey(req.GetReceiverIdentityPublicKey())
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("failed to parse receiver identity public key: %w", err))
	}

	if len(req.SparkInvoice) > 0 {
		leafIDsToSend, err := uuids.ParseSliceFunc(req.GetTransferPackage().GetLeavesToSend(), (*pb.UserSignedTxSigningJob).GetLeafId)
		if err != nil {
			return nil, fmt.Errorf("failed to parse leaf id: %w", err)
		}

		err = validateSatsSparkInvoice(ctx, req.SparkInvoice, receiverIdentityPubKey, reqOwnerIdentityPubKey, leafIDsToSend, true)
		if err != nil {
			return nil, fmt.Errorf("failed to validate sats spark invoice: %s for transfer id: %s. error: %w", req.SparkInvoice, transferID, err)
		}
	}

	entTx, err := ent.GetTxFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get database transaction: %w", err)
	}
	if _, err = ent.CreateOrResetPendingSendTransfer(ctx, transferID); err != nil {
		return nil, fmt.Errorf("unable to create pending send transfer: %w", err)
	}
	if err := entTx.Commit(); err != nil {
		return nil, fmt.Errorf("unable to commit database transaction: %w", err)
	}

	role := TransferRoleCoordinator
	var primaryTransferId uuid.UUID
	tweakKeys := true

	if swapV3Package != nil {
		if transferType == st.TransferTypePrimarySwapV3 {
			tweakKeys = false
			// Override the expiry time to be double of the safety buffer time so the user have
			// enough time to call the SSP to create a counter transfer.
			req.ExpiryTime = timestamppb.New(time.Now().Add(2 * PrimaryTransferExpiryTimeSafetyBuffer))
		} else {
			primaryTransferId = swapV3Package.primaryTransferId
		}
	}
	transfer, leafMap, err := h.createTransfer(
		ctx,
		nil,
		transferID,
		transferType,
		req.ExpiryTime.AsTime(),
		reqOwnerIdentityPubKey,
		receiverIdentityPubKey,
		leafCpfpRefundMap,
		leafDirectRefundMap,
		leafDirectFromCpfpRefundMap,
		leafTweakMap,
		role,
		requireDirectTx,
		req.SparkInvoice,
		primaryTransferId,
		nil,
	)
	if err != nil {
		originalErr := err
		entTx, err := ent.GetTxFromContext(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to get database transaction: %w while creating transfer: %w", err, originalErr)
		}
		if err := entTx.Rollback(); err != nil {
			return nil, fmt.Errorf("unable to rollback database transaction: %w while creating transfer: %w", err, originalErr)
		}
		entTx, err = ent.GetTxFromContext(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to get database transaction: %w while creating transfer: %w", err, originalErr)
		}
		dbClient := entTx.Client()
		_, err = dbClient.PendingSendTransfer.Update().Where(pendingsendtransfer.TransferID(transferID)).SetStatus(st.PendingSendTransferStatusFinished).Save(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to update pending send transfer: %w while creating transfer: %w", err, originalErr)
		}
		if err = entTx.Commit(); err != nil {
			return nil, fmt.Errorf("unable to commit database transaction: %w while creating transfer: %w", err, originalErr)
		}
		return nil, fmt.Errorf("failed to create transfer for transfer %s: %w", transferID, originalErr)
	}

	// If the SSP matched the user's primary transfer with a counter transfer, lock it from cancellation.
	// If other SO fails to accept the key tweaks, this status will be rolled back.
	if transferType == st.TransferTypeCounterSwapV3 {
		err := updateSwapPrimaryTransferToStatus(ctx, transfer, st.TransferStatusApplyingSenderKeyTweak)
		if err != nil {
			return nil, fmt.Errorf("unable to update primary transfer for counter transfer %s status: %w ", req.TransferId, err)
		}
	}

	var signingResults []*pb.LeafRefundTxSigningResult
	var finalCpfpSignatureMap map[string][]byte
	var finalDirectSignatureMap map[string][]byte
	var finalDirectFromCpfpSignatureMap map[string][]byte
	if req.TransferPackage == nil {
		signingResults, err = signRefunds(ctx, h.config, req, leafMap, cpfpAdaptorPubKey, directAdaptorPubKey, directFromCpfpAdaptorPubKey, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to sign refunds for transfer %s: %w", transferID, err)
		}
	} else {
		cpfpSigningResultMap, directSigningResultMap, directFromCpfpSigningResultMap, err := SignRefundsWithPregeneratedNonce(ctx, h.config, req, leafMap, cpfpAdaptorPubKey, directAdaptorPubKey, directFromCpfpAdaptorPubKey)
		if err != nil {
			return nil, fmt.Errorf("failed to sign refunds with pregenerated nonce: %w", err)
		}
		finalCpfpSignatureMap, finalDirectSignatureMap, finalDirectFromCpfpSignatureMap, err = AggregateSignatures(ctx, h.config, req, cpfpAdaptorPubKey, directAdaptorPubKey, directFromCpfpAdaptorPubKey, cpfpSigningResultMap, directSigningResultMap, directFromCpfpSigningResultMap, leafMap)
		if err != nil {
			return nil, fmt.Errorf("failed to aggregate signatures: %w", err)
		}

		// Update the leaves with the final signatures for refunds
		if len(finalDirectSignatureMap) > 0 || len(finalDirectFromCpfpSignatureMap) > 0 {
			err = h.UpdateTransferLeavesSignatures(ctx, transfer, finalCpfpSignatureMap, finalDirectSignatureMap, finalDirectFromCpfpSignatureMap)
			if err != nil {
				return nil, fmt.Errorf("failed to update transfer leaves signatures: %w", err)
			}
		} else {
			err = h.UpdateTransferLeavesSignaturesForRefundTxOnly(ctx, transfer, finalCpfpSignatureMap, cpfpAdaptorPubKey)
			if err != nil {
				return nil, fmt.Errorf("failed to update CPFP transfer leaves signatures: %w", err)
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
				VerifyingKey:                        leafMap[leafID].VerifyingPubkey.Serialize(),
			})
		}
	}

	// This call to other SOs will check the validity of the transfer package. If no error is
	// returned, it means the transfer package is valid and the transfer is considered sent.
	err = h.syncTransferInit(ctx, req, transferType, finalCpfpSignatureMap, finalDirectSignatureMap, finalDirectFromCpfpSignatureMap, cpfpAdaptorPubKey, directAdaptorPubKey, directFromCpfpAdaptorPubKey, swapV3Package)
	if err != nil {
		syncErr := err
		logger.With(zap.Error(syncErr)).Sugar().Errorf("Failed to sync transfer init for transfer %s", transferID)

		entTx, err := ent.GetTxFromContext(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to get database transaction: %w", err)
		}
		err = entTx.Rollback()
		if err != nil {
			return nil, fmt.Errorf("unable to rollback database transaction: %w", err)
		}

		entTx, err = ent.GetTxFromContext(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to get database transaction: %w", err)
		}
		dbClient := entTx.Client()
		_, err = dbClient.PendingSendTransfer.Update().Where(pendingsendtransfer.TransferID(transfer.ID)).SetStatus(st.PendingSendTransferStatusFinished).Save(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to update pending send transfer: %w", err)
		}
		cancelErr := h.CreateCancelTransferGossipMessage(ctx, transferID)
		if cancelErr != nil {
			logger.With(zap.Error(cancelErr)).Sugar().Errorf("Failed to create cancel transfer gossip message for transfer %s", transferID)
		}
		err = entTx.Commit()
		if err != nil {
			return nil, fmt.Errorf("unable to commit database transaction: %w", err)
		}

		return nil, fmt.Errorf("failed to sync transfer init for transfer %s: %w", transferID, syncErr)
	}

	// After this point, the transfer send is considered successful.

	if req.TransferPackage != nil {
		entTx, err := ent.GetTxFromContext(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to get db before sync transfer init: %w", err)
		}
		if err := entTx.Commit(); err != nil {
			return nil, fmt.Errorf("unable to commit db before sync transfer init: %w", err)
		}
		// Only false for Swap V3 flow when initiating a primary transfer for a swap.
		// Swap V3 postpones key tweaking for the primary transfer, until a counter transfer is submitted.
		if tweakKeys {
			var message *pbgossip.GossipMessage
			// Swap V3 requires both primary and counter transfer tweaks settled at the same time,
			// so there is a special handler for this case.
			// primaryTransferId is only passed in for swap v3.
			if transferType == st.TransferTypeCounterSwapV3 && primaryTransferId != uuid.Nil {
				message = &pbgossip.GossipMessage{
					Message: &pbgossip.GossipMessage_SettleSwapKeyTweak{
						SettleSwapKeyTweak: &pbgossip.GossipMessageSettleSwapKeyTweak{
							CounterTransferId: transfer.ID.String(),
						},
					},
				}
			} else {
				// If all other SOs have settled the sender key tweaks, we can commit the sender key tweaks.
				// If there's any error, it means one or more of the SOs are down at the time, we will have a
				// cron job to retry the key commit.
				keyTweakProofMap := make(map[string]*pb.SecretProof)
				for _, leaf := range leafTweakMap {
					keyTweakProofMap[leaf.LeafId] = &pb.SecretProof{
						Proofs: leaf.SecretShareTweak.Proofs,
					}
				}

				message = &pbgossip.GossipMessage{
					Message: &pbgossip.GossipMessage_SettleSenderKeyTweak{
						SettleSenderKeyTweak: &pbgossip.GossipMessageSettleSenderKeyTweak{
							TransferId:           transfer.ID.String(),
							SenderKeyTweakProofs: keyTweakProofMap,
						},
					},
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
			_, err = sendGossipHandler.CreateCommitAndSendGossipMessage(ctx, message, participants)
			if err != nil {
				logger.With(zap.Error(err)).Sugar().Errorf(
					"Failed to create and send gossip message to settle sender key tweak for transfer %s",
					transferID,
				)
				return nil, fmt.Errorf("failed to create and send gossip message to settle sender key tweak: %w", err)
			}
		}
		transfer, err = h.loadTransferForUpdate(ctx, transferID)
		if err != nil {
			return nil, fmt.Errorf("unable to load transfer: %w", err)
		}

		db, err := ent.GetDbFromContext(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to get database transaction: %w", err)
		}
		_, err = db.PendingSendTransfer.Update().Where(pendingsendtransfer.TransferID(transfer.ID)).SetStatus(st.PendingSendTransferStatusFinished).Save(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to update pending send transfer: %w", err)
		}
	}

	transferProto, err := transfer.MarshalProto(ctx)
	if err != nil {
		logger.With(zap.Error(err)).Sugar().Errorf("Unable to marshal transfer %s", transfer.ID)
	}

	return &pb.StartTransferResponse{Transfer: transferProto, SigningResults: signingResults}, nil
}

func (h *TransferHandler) UpdateTransferLeavesSignatures(ctx context.Context, transfer *ent.Transfer, cpfpSignatureMap map[string][]byte, directSignatureMap map[string][]byte, directFromCpfpSignatureMap map[string][]byte) error {
	transferLeaves, err := transfer.QueryTransferLeaves().WithLeaf().All(ctx)
	if err != nil {
		return fmt.Errorf("unable to get transfer leaves: %w", err)
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("unable to get db from context: %w", err)
	}

	// Collect all updates to batch them and avoid N+1 queries
	builders := make([]*ent.TransferLeafCreate, 0, len(transferLeaves))

	for _, leaf := range transferLeaves {

		nodeTx, err := common.TxFromRawTxBytes(leaf.Edges.Leaf.RawTx)
		if err != nil {
			return fmt.Errorf("unable to get node tx: %w", err)
		}

		updatedCpfpRefundTxBytes, err := common.UpdateTxWithSignature(leaf.IntermediateRefundTx, 0, cpfpSignatureMap[leaf.Edges.Leaf.ID.String()])
		if err != nil {
			return fmt.Errorf("unable to update leaf cpfp refund tx signature for leaf %s: %w", leaf.Edges.Leaf.ID.String(), err)
		}
		updatedCpfpRefundTx, err := common.TxFromRawTxBytes(updatedCpfpRefundTxBytes)
		if err != nil {
			return fmt.Errorf("unable to get cpfp refund tx for leaf %s: %w", leaf.Edges.Leaf.ID.String(), err)
		}
		err = common.VerifySignatureSingleInput(updatedCpfpRefundTx, 0, nodeTx.TxOut[0])
		if err != nil {
			return fmt.Errorf("unable to verify leaf cpfp refund tx signature for leaf %s: %w", leaf.Edges.Leaf.ID.String(), err)
		}

		// Compute final values for each field (nil = clear)
		var intermediateDirectFromCpfpRefundTx []byte
		if len(leaf.Edges.Leaf.DirectFromCpfpRefundTx) > 0 && len(directFromCpfpSignatureMap[leaf.Edges.Leaf.ID.String()]) > 0 {
			updatedDirectFromCpfpRefundTxBytes, err := common.UpdateTxWithSignature(leaf.IntermediateDirectFromCpfpRefundTx, 0, directFromCpfpSignatureMap[leaf.Edges.Leaf.ID.String()])
			if err != nil {
				return fmt.Errorf("unable to update leaf direct from cpfp refund tx signature for leaf %s: %w", leaf.Edges.Leaf.ID.String(), err)
			}
			updatedDirectFromCpfpRefundTx, err := common.TxFromRawTxBytes(updatedDirectFromCpfpRefundTxBytes)
			if err != nil {
				return fmt.Errorf("unable to get direct from cpfp refund tx for leaf %s: %w", leaf.Edges.Leaf.ID.String(), err)
			}
			err = common.VerifySignatureSingleInput(updatedDirectFromCpfpRefundTx, 0, nodeTx.TxOut[0])
			if err != nil {
				return fmt.Errorf("unable to verify leaf direct from cpfp refund tx signature for leaf %s: %w", leaf.Edges.Leaf.ID.String(), err)
			}

			intermediateDirectFromCpfpRefundTx = updatedDirectFromCpfpRefundTxBytes
		}
		// else: stays nil, which will clear the field

		var intermediateDirectRefundTx []byte
		if len(leaf.Edges.Leaf.DirectTx) > 0 && len(directSignatureMap[leaf.Edges.Leaf.ID.String()]) > 0 {
			directNodeTx, err := common.TxFromRawTxBytes(leaf.Edges.Leaf.DirectTx)
			if err != nil {
				return fmt.Errorf("unable to get direct node tx for leaf %s: %w", leaf.Edges.Leaf.ID.String(), err)
			}

			updatedDirectRefundTxBytes, err := common.UpdateTxWithSignature(leaf.IntermediateDirectRefundTx, 0, directSignatureMap[leaf.Edges.Leaf.ID.String()])
			if err != nil {
				return fmt.Errorf("unable to update leaf signature for leaf %s: %w", leaf.Edges.Leaf.ID.String(), err)
			}
			updatedDirectRefundTx, err := common.TxFromRawTxBytes(updatedDirectRefundTxBytes)
			if err != nil {
				return fmt.Errorf("unable to get direct refund tx for leaf %s: %w", leaf.Edges.Leaf.ID.String(), err)
			}

			err = common.VerifySignatureSingleInput(updatedDirectRefundTx, 0, directNodeTx.TxOut[0])
			if err != nil {
				return fmt.Errorf("unable to verify leaf signature for leaf %s: %w", leaf.Edges.Leaf.ID.String(), err)
			}

			intermediateDirectRefundTx = updatedDirectRefundTxBytes
		}

		// Build upsert for batch update. Since records always exist (queried above),
		// OnConflict will always UPDATE, never INSERT. We set ID (for matching), all required fields, and the fields we want to update.
		// Note: Setting byte fields to nil will clear them (set to NULL) on conflict.
		builders = append(builders,
			db.TransferLeaf.Create().
				SetID(leaf.ID).
				SetLeaf(leaf.Edges.Leaf).
				SetTransferID(transfer.ID).
				SetPreviousRefundTx(leaf.PreviousRefundTx).
				SetIntermediateRefundTx(updatedCpfpRefundTxBytes).
				SetIntermediateDirectRefundTx(intermediateDirectRefundTx).
				SetIntermediateDirectFromCpfpRefundTx(intermediateDirectFromCpfpRefundTx),
		)
	}

	// Execute all updates in batch to avoid N+1 queries.
	// We use CreateBulk with OnConflict as a workaround since Ent doesn't have native bulk UPDATE support.
	// Since all records exist (queried above), OnConflict will always UPDATE, never INSERT.
	// Batch in chunks to avoid PostgreSQL parameter limit (65535).
	const maxBatchSize = 1000

	for chunk := range slices.Chunk(builders, maxBatchSize) {
		err = db.TransferLeaf.CreateBulk(chunk...).
			OnConflictColumns(enttransferleaf.FieldID).
			Update(func(u *ent.TransferLeafUpsert) {
				u.UpdateIntermediateRefundTx()
				u.UpdateIntermediateDirectRefundTx()
				u.UpdateIntermediateDirectFromCpfpRefundTx()
			}).
			Exec(ctx)
		if err != nil {
			return fmt.Errorf("unable to batch update transfer leaf refund txs: %w", err)
		}
	}

	return nil
}

// Updates all transfer leaves associated with a transfer by applying final signatures to their intermediate refund transactions only.
// If the signatures were adapted then cpfpAdaptorPubKey should be provided for the signature verification.
func (h *TransferHandler) UpdateTransferLeavesSignaturesForRefundTxOnly(ctx context.Context, transfer *ent.Transfer, finalSignatureMap map[string][]byte, cpfpAdaptorPubKey keys.Public) error {
	transferLeaves, err := transfer.QueryTransferLeaves().WithLeaf().All(ctx)
	if err != nil {
		return fmt.Errorf("unable to get transfer leaves: %w", err)
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("unable to get db from context: %w", err)
	}

	builders := make([]*ent.TransferLeafCreate, 0, len(transferLeaves))

	for _, leaf := range transferLeaves {
		nodeTx, err := common.TxFromRawTxBytes(leaf.Edges.Leaf.RawTx)
		if err != nil {
			return fmt.Errorf("unable to get cpfp node tx for leaf %s: %w", leaf.Edges.Leaf.ID.String(), err)
		}

		updatedTx, err := ApplySignatureToTxAndVerify(leaf.IntermediateRefundTx, finalSignatureMap[leaf.Edges.Leaf.ID.String()], cpfpAdaptorPubKey, nodeTx.TxOut[0], leaf.Edges.Leaf.VerifyingPubkey)
		if err != nil {
			return fmt.Errorf("unable to apply signature to tx and verify for leaf %s: %w", leaf.Edges.Leaf.ID.String(), err)
		}

		// Build upsert for batch update. Since records always exist (queried above),
		// OnConflict will always UPDATE, never INSERT. We set ID (for matching), all required fields, and the fields we want to update.
		builders = append(builders,
			db.TransferLeaf.Create().
				SetID(leaf.ID).
				SetLeaf(leaf.Edges.Leaf).
				SetTransferID(transfer.ID).
				SetPreviousRefundTx(leaf.PreviousRefundTx).
				SetIntermediateRefundTx(updatedTx),
		)
	}

	// Execute all updates in batch to avoid N+1 queries.
	// We use CreateBulk with OnConflict as a workaround since Ent doesn't have native bulk UPDATE support.
	// Since all records exist (queried above), OnConflict will always UPDATE, never INSERT.
	// Batch in chunks to avoid PostgreSQL parameter limit (65535).
	const maxBatchSize = 1000
	for chunk := range slices.Chunk(builders, maxBatchSize) {
		err = db.TransferLeaf.CreateBulk(chunk...).
			OnConflictColumns(enttransferleaf.FieldID).
			Update(func(u *ent.TransferLeafUpsert) {
				u.UpdateIntermediateRefundTx()
			}).
			Exec(ctx)
		if err != nil {
			return fmt.Errorf("unable to batch update transfer leaf refund txs: %w", err)
		}
	}

	return nil
}

// settleSenderKeyTweaks calls the other SOs to settle the sender key tweaks.
func (h *TransferHandler) settleSenderKeyTweaks(ctx context.Context, transferID uuid.UUID, action pbinternal.SettleKeyTweakAction) error {
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
			TransferId: transferID.String(),
			Action:     action,
		})
	})
	return err
}

// StartTransfer initiates a transfer from sender.
func (h *TransferHandler) StartTransfer(ctx context.Context, req *pb.StartTransferRequest) (*pb.StartTransferResponse, error) {
	return h.startTransferInternal(ctx, req, st.TransferTypeTransfer, keys.Public{}, keys.Public{}, keys.Public{}, false, nil)
}

func (h *TransferHandler) StartTransferV2(ctx context.Context, req *pb.StartTransferRequest) (*pb.StartTransferResponse, error) {
	return h.startTransferInternal(ctx, req, st.TransferTypeTransfer, keys.Public{}, keys.Public{}, keys.Public{}, true, nil)
}

func (h *TransferHandler) StartLeafSwap(ctx context.Context, req *pb.StartTransferRequest) (*pb.StartTransferResponse, error) {
	return h.startTransferInternal(ctx, req, st.TransferTypeSwap, keys.Public{}, keys.Public{}, keys.Public{}, false, nil)
}

func (h *TransferHandler) StartLeafSwapV2(ctx context.Context, req *pb.StartTransferRequest) (*pb.StartTransferResponse, error) {
	return h.startTransferInternal(ctx, req, st.TransferTypeSwap, keys.Public{}, keys.Public{}, keys.Public{}, true, nil)
}

// Initiate a primary swap transfer in Swap V3 protocol. This will create a
// transfer to the SSP with adapted refunds with key tweaks stored but not yet
// applied, awaiting a counter swap transfer.
// Swap V3 flow requires adapted signatures, so the User must provide the adaptor public keys.
func (h *TransferHandler) InitiateSwapPrimaryTransfer(ctx context.Context, req *pb.InitiateSwapPrimaryTransferRequest) (*pb.StartTransferResponse, error) {
	adaptorPublicKey, err := keys.ParsePublicKey(req.GetAdaptorPublicKeys().GetAdaptorPublicKey())
	if err != nil {
		return nil, fmt.Errorf("unable to parse adaptor public key: %w", err)
	}

	if len(req.GetTransfer().GetTransferPackage().GetDirectLeavesToSend()) > 0 || len(req.GetTransfer().GetTransferPackage().GetDirectFromCpfpLeavesToSend()) > 0 {
		return nil, fmt.Errorf("direct transactions should not be provided for primary transfer %s", req.GetTransfer().GetTransferId())
	}

	return h.startTransferInternal(ctx, req.GetTransfer(), st.TransferTypePrimarySwapV3, adaptorPublicKey, keys.Public{}, keys.Public{}, true, &SwapV3Package{primaryTransferId: uuid.Nil})
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
	startTransferResponse, err := h.startTransferInternal(ctx, req.Transfer, st.TransferTypeCounterSwap, adaptorPublicKey, directAdaptorPublicKey, directFromCpfpAdaptorPublicKey, false, nil)
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
	startTransferResponse, err := h.startTransferInternal(ctx, req.Transfer, st.TransferTypeCounterSwap, adaptorPublicKey, directAdaptorPublicKey, directFromCpfpAdaptorPublicKey, true, nil)
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

func (h *TransferHandler) syncTransferInit(
	ctx context.Context,
	req *pb.StartTransferRequest,
	transferType st.TransferType,
	cpfpRefundSignatures map[string][]byte,
	directRefundSignatures map[string][]byte,
	directFromCpfpRefundSignatures map[string][]byte,
	cpfpAdaptorPubKey keys.Public,
	directAdaptorPubKey keys.Public,
	directFromCpfpAdaptorPubKey keys.Public,
	swapV3Package *SwapV3Package,
) error {
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

	// Swap V3 flow requires adaptor public keys to be provided.
	// However direct transactions are not used so these adaptors
	// are not required.
	var adaptorPublicKeyPackage *pb.AdaptorPublicKeyPackage
	var primaryTransferId uuid.UUID
	if swapV3Package != nil {
		adaptorPublicKeyPackage = &pb.AdaptorPublicKeyPackage{
			AdaptorPublicKey:               cpfpAdaptorPubKey.Serialize(),
			DirectAdaptorPublicKey:         directAdaptorPubKey.Serialize(),
			DirectFromCpfpAdaptorPublicKey: directFromCpfpAdaptorPubKey.Serialize(),
		}
		if transferType == st.TransferTypeCounterSwapV3 {
			primaryTransferId = swapV3Package.primaryTransferId
		}
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
		AdaptorPublicKeys:              adaptorPublicKeyPackage,
		PrimaryTransferId:              primaryTransferId.String(),
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
		logger.Sugar().Infof("Delivering key tweak for transfer %s to SO %d", req.TransferId, operator.ID)
		client := pbinternal.NewSparkInternalServiceClient(conn)
		return client.DeliverSenderKeyTweak(ctx, deliverSenderKeyTweakRequest)
	})
	return err
}

func signRefunds(ctx context.Context, config *so.Config, requests *pb.StartTransferRequest, leafMap map[string]*ent.TreeNode, cpfpAdaptorPubKey keys.Public, directAdaptorPubKey keys.Public, directFromCpfpAdaptorPubKey keys.Public, connectorTx []byte) ([]*pb.LeafRefundTxSigningResult, error) {
	ctx, span := tracer.Start(ctx, "TransferHandler.signRefunds")
	defer span.End()

	if requests.TransferPackage != nil {
		return nil, fmt.Errorf("transfer package is not nil, should call signRefundsWithPregeneratedNonce instead")
	}

	// Parse connector tx if provided for multi-input sighash calculation (cooperative exit)
	var connectorPrevOuts map[wire.OutPoint]*wire.TxOut
	if len(connectorTx) > 0 {
		parsedConnectorTx, err := common.TxFromRawTxBytes(connectorTx)
		if err != nil {
			return nil, fmt.Errorf("unable to parse connector tx: %w", err)
		}
		connectorTxHash := parsedConnectorTx.TxHash()
		connectorPrevOuts = make(map[wire.OutPoint]*wire.TxOut, len(parsedConnectorTx.TxOut))
		for i, txOut := range parsedConnectorTx.TxOut {
			outpoint := wire.OutPoint{
				Hash:  connectorTxHash,
				Index: uint32(i),
			}
			connectorPrevOuts[outpoint] = txOut
		}
	}

	leafJobMap := make(map[uuid.UUID]*ent.TreeNode)
	var cpfpSigningResults []*helper.SigningResult
	var directSigningResults []*helper.SigningResult
	var directFromCpfpSigningResults []*helper.SigningResult

	var cpfpSigningJobs []*helper.SigningJob
	var directSigningJobs []*helper.SigningJob
	var directFromCpfpSigningJobs []*helper.SigningJob

	if len(requests.LeavesToSend) == 0 {
		return nil, fmt.Errorf("leaves to send is empty when signing refunds")
	}

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

		if len(cpfpLeafTx.TxOut) == 0 {
			return nil, fmt.Errorf("cpfp vout out of bounds")
		}

		var cpfpRefundTxSigHash []byte
		if len(cpfpRefundTx.TxIn) > 1 && connectorPrevOuts != nil {
			// Multi-input refund tx with connector tx provided (new coop exit flow)
			// Use multi-input sighash for 2-input coop exit refund transactions
			cpfpLeafTxHash := cpfpLeafTx.TxHash()
			prevOuts := make(map[wire.OutPoint]*wire.TxOut, 2)
			prevOuts[wire.OutPoint{Hash: cpfpLeafTxHash, Index: 0}] = cpfpLeafTx.TxOut[0]

			connectorOutpoint := cpfpRefundTx.TxIn[1].PreviousOutPoint
			connectorTxOut, exists := connectorPrevOuts[connectorOutpoint]
			if !exists {
				return nil, fmt.Errorf("cpfp refund tx input 1 does not reference a valid connector output: %v", connectorOutpoint)
			}
			prevOuts[connectorOutpoint] = connectorTxOut

			cpfpRefundTxSigHash, err = common.SigHashFromMultiPrevOutTx(cpfpRefundTx, 0, prevOuts)
		} else {
			// Single-input sighash (legacy flow):
			// - Single-input refund tx
			// - OR multi-input refund tx without connector tx (backwards compatibility)
			cpfpRefundTxSigHash, err = common.SigHashFromTx(cpfpRefundTx, 0, cpfpLeafTx.TxOut[0])
		}
		if err != nil {
			return nil, fmt.Errorf("unable to calculate sighash from cpfp refund tx for leaf %s: %w", leaf.ID, err)
		}

		cpfpUserNonceCommitment := frost.SigningCommitment{}
		if err := cpfpUserNonceCommitment.UnmarshalProto(req.GetRefundTxSigningJob().GetSigningNonceCommitment()); err != nil {
			return nil, fmt.Errorf("unable to create cpfp signing commitment: %w", err)
		}
		cpfpJobID := uuid.New()
		signingKeyshare, err := leaf.QuerySigningKeyshare().Only(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get signing keyshare id: %w", err)
		}

		cpfpSigningJobs = append(
			cpfpSigningJobs,
			&helper.SigningJob{
				JobID:             cpfpJobID,
				SigningKeyshareID: signingKeyshare.ID,
				Message:           cpfpRefundTxSigHash,
				VerifyingKey:      &leaf.VerifyingPubkey,
				UserCommitment:    &cpfpUserNonceCommitment,
				AdaptorPublicKey:  &cpfpAdaptorPubKey,
			},
		)
		leafJobMap[cpfpJobID] = leaf

		// Create direct refund tx signing job if present and direct tx exists
		if req.DirectRefundTxSigningJob != nil && len(leaf.DirectTx) > 0 {
			directRefundTx, err := common.TxFromRawTxBytes(req.DirectRefundTxSigningJob.RawTx)
			if err != nil {
				return nil, fmt.Errorf("unable to load new refund tx: %w", err)
			}
			directLeafTx, err := common.TxFromRawTxBytes(leaf.DirectTx)
			if err != nil {
				return nil, fmt.Errorf("unable to load direct leaf tx: %w", err)
			}
			if len(directLeafTx.TxOut) == 0 {
				return nil, fmt.Errorf("direct vout out of bounds")
			}
			var directRefundTxSigHash []byte
			if len(directRefundTx.TxIn) > 1 && connectorPrevOuts != nil {
				// Multi-input refund tx with connector tx provided (new coop exit flow)
				// Use multi-input sighash for 2-input coop exit refund transactions
				directLeafTxHash := directLeafTx.TxHash()
				prevOuts := make(map[wire.OutPoint]*wire.TxOut, 2)
				prevOuts[wire.OutPoint{Hash: directLeafTxHash, Index: 0}] = directLeafTx.TxOut[0]

				connectorOutpoint := directRefundTx.TxIn[1].PreviousOutPoint
				connectorTxOut, exists := connectorPrevOuts[connectorOutpoint]
				if !exists {
					return nil, fmt.Errorf("direct refund tx input 1 does not reference a valid connector output: %v", connectorOutpoint)
				}
				prevOuts[connectorOutpoint] = connectorTxOut

				directRefundTxSigHash, err = common.SigHashFromMultiPrevOutTx(directRefundTx, 0, prevOuts)
			} else {
				// Single-input sighash (legacy flow):
				// - Single-input refund tx
				// - OR multi-input refund tx without connector tx (backwards compatibility)
				directRefundTxSigHash, err = common.SigHashFromTx(directRefundTx, 0, directLeafTx.TxOut[0])
			}
			if err != nil {
				return nil, fmt.Errorf("unable to calculate sighash from direct refund tx: %w", err)
			}
			directUserNonceCommitment := frost.SigningCommitment{}
			if err := directUserNonceCommitment.UnmarshalProto(req.GetDirectRefundTxSigningJob().GetSigningNonceCommitment()); err != nil {
				return nil, fmt.Errorf("unable to create direct signing commitment: %w", err)
			}
			directJobID := uuid.New()

			directSigningJobs = append(
				directSigningJobs,
				&helper.SigningJob{
					JobID:             directJobID,
					SigningKeyshareID: signingKeyshare.ID,
					Message:           directRefundTxSigHash,
					VerifyingKey:      &leaf.VerifyingPubkey,
					UserCommitment:    &directUserNonceCommitment,
					AdaptorPublicKey:  &directAdaptorPubKey,
				},
			)
			leafJobMap[directJobID] = leaf
		}

		// Always create direct from cpfp refund tx signing job if present
		if req.DirectFromCpfpRefundTxSigningJob != nil {
			directFromCpfpRefundTx, err := common.TxFromRawTxBytes(req.DirectFromCpfpRefundTxSigningJob.RawTx)
			if err != nil {
				return nil, fmt.Errorf("unable to load new refund tx: %w", err)
			}
			var directFromCpfpRefundTxSigHash []byte
			if len(directFromCpfpRefundTx.TxIn) > 1 && connectorPrevOuts != nil {
				// Multi-input refund tx with connector tx provided (new coop exit flow)
				// Use multi-input sighash for 2-input coop exit refund transactions
				cpfpLeafTxHash := cpfpLeafTx.TxHash()
				prevOuts := make(map[wire.OutPoint]*wire.TxOut, 2)
				prevOuts[wire.OutPoint{Hash: cpfpLeafTxHash, Index: 0}] = cpfpLeafTx.TxOut[0]

				connectorOutpoint := directFromCpfpRefundTx.TxIn[1].PreviousOutPoint
				connectorTxOut, exists := connectorPrevOuts[connectorOutpoint]
				if !exists {
					return nil, fmt.Errorf("direct-from-cpfp refund tx input 1 does not reference a valid connector output: %v", connectorOutpoint)
				}
				prevOuts[connectorOutpoint] = connectorTxOut

				directFromCpfpRefundTxSigHash, err = common.SigHashFromMultiPrevOutTx(directFromCpfpRefundTx, 0, prevOuts)
			} else {
				// Single-input sighash (legacy flow):
				// - Single-input refund tx
				// - OR multi-input refund tx without connector tx (backwards compatibility)
				directFromCpfpRefundTxSigHash, err = common.SigHashFromTx(directFromCpfpRefundTx, 0, cpfpLeafTx.TxOut[0])
			}
			if err != nil {
				return nil, fmt.Errorf("unable to calculate sighash from direct from cpfp refund tx for leaf %s: %w", leaf.ID, err)
			}

			directFromCpfpUserNonceCommitment := frost.SigningCommitment{}
			if err := directFromCpfpUserNonceCommitment.UnmarshalProto(req.GetDirectFromCpfpRefundTxSigningJob().GetSigningNonceCommitment()); err != nil {
				return nil, fmt.Errorf("unable to create direct from cpfp signing commitment: %w", err)
			}
			directFromCpfpJobID := uuid.New()
			directFromCpfpSigningJobs = append(
				directFromCpfpSigningJobs,
				&helper.SigningJob{
					JobID:             directFromCpfpJobID,
					SigningKeyshareID: signingKeyshare.ID,
					Message:           directFromCpfpRefundTxSigHash,
					VerifyingKey:      &leaf.VerifyingPubkey,
					UserCommitment:    &directFromCpfpUserNonceCommitment,
					AdaptorPublicKey:  &directFromCpfpAdaptorPubKey,
				},
			)
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
			VerifyingKey:          leaf.VerifyingPubkey.Serialize(),
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

func SignRefundsWithPregeneratedNonce(
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

	leafJobMap := make(map[uuid.UUID]*ent.TreeNode)
	jobIsDirectRefund := make(map[uuid.UUID]bool)
	jobIsDirectFromCpfpRefund := make(map[uuid.UUID]bool)

	if requests.TransferPackage == nil {
		return nil, nil, nil, fmt.Errorf("transfer package is nil")
	}

	var signingJobs []*helper.SigningJobWithPregeneratedNonce
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
		if len(leafTx.TxOut) == 0 {
			return nil, nil, nil, fmt.Errorf("vout out of bounds")
		}
		refundTxSigHash, err := common.SigHashFromTx(refundTx, 0, leafTx.TxOut[0])
		if err != nil {
			return nil, nil, nil, fmt.Errorf("unable to calculate sighash from refund tx: %w", err)
		}

		userNonceCommitment := frost.SigningCommitment{}
		if err := userNonceCommitment.UnmarshalProto(req.GetSigningNonceCommitment()); err != nil {
			return nil, nil, nil, fmt.Errorf("unable to unmarshal signing nonce commitment: %w", err)
		}
		cpfpJobID := uuid.New()
		jobIsDirectRefund[cpfpJobID] = false
		jobIsDirectFromCpfpRefund[cpfpJobID] = false

		signingKeyshare, err := leaf.QuerySigningKeyshare().Only(ctx)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("failed to get signing keyshare id: %w", err)
		}

		round1Packages := make(map[string]frost.SigningCommitment)

		signingCommitments := req.GetSigningCommitments()
		if signingCommitments == nil {
			return nil, nil, nil, fmt.Errorf("missing signing_commitments for leaf_id %s", req.LeafId)
		}

		for key, commitment := range signingCommitments.GetSigningCommitments() {
			obj := frost.SigningCommitment{}
			if err := obj.UnmarshalProto(commitment); err != nil {
				return nil, nil, nil, fmt.Errorf("unable to unmarshal signing commitment: %w", err)
			}
			if obj.IsZero() {
				return nil, nil, nil, fmt.Errorf("cpfp signing commitment is invalid for key %s: hiding or binding is empty", key)
			}
			round1Packages[key] = obj
		}
		signingJobs = append(
			signingJobs,
			&helper.SigningJobWithPregeneratedNonce{
				SigningJob: helper.SigningJob{
					JobID:             cpfpJobID,
					SigningKeyshareID: signingKeyshare.ID,
					Message:           refundTxSigHash,
					VerifyingKey:      &leaf.VerifyingPubkey,
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
		if len(directTx.TxOut) == 0 {
			return nil, nil, nil, fmt.Errorf("vout out of bounds")
		}
		directRefundTxSigHash, err := common.SigHashFromTx(directRefundTx, 0, directTx.TxOut[0])
		if err != nil {
			return nil, nil, nil, fmt.Errorf("unable to calculate sighash from direct refund tx: %w", err)
		}

		userNonceCommitment := frost.SigningCommitment{}
		if err := userNonceCommitment.UnmarshalProto(req.GetSigningNonceCommitment()); err != nil {
			return nil, nil, nil, fmt.Errorf("unable to unmarshal signing nonce commitment: %w", err)
		}

		directJobID := uuid.New()
		jobIsDirectRefund[directJobID] = true
		signingKeyshare, err := leaf.QuerySigningKeyshare().Only(ctx)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("failed to get signing keyshare id: %w", err)
		}

		round1Packages := make(map[string]frost.SigningCommitment)

		signingCommitments := req.GetSigningCommitments()
		if signingCommitments == nil {
			return nil, nil, nil, fmt.Errorf("missing signing_commitments for leaf_id %s", req.LeafId)
		}

		for key, commitment := range signingCommitments.GetSigningCommitments() {
			obj := frost.SigningCommitment{}
			if err := obj.UnmarshalProto(commitment); err != nil {
				return nil, nil, nil, fmt.Errorf("unable to unmarshal signing commitment: %w", err)
			}
			round1Packages[key] = obj
		}
		signingJobs = append(signingJobs, &helper.SigningJobWithPregeneratedNonce{
			SigningJob: helper.SigningJob{
				JobID:             directJobID,
				SigningKeyshareID: signingKeyshare.ID,
				Message:           directRefundTxSigHash,
				VerifyingKey:      &leaf.VerifyingPubkey,
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
		if len(directFromCpfpLeafTx.TxOut) == 0 {
			return nil, nil, nil, fmt.Errorf("vout out of bounds")
		}
		directFromCpfpRefundTxSigHash, err := common.SigHashFromTx(directFromCpfpRefundTx, 0, directFromCpfpLeafTx.TxOut[0])
		if err != nil {
			return nil, nil, nil, fmt.Errorf("unable to calculate sighash from direct from cpfp refund tx: %w", err)
		}

		userNonceCommitment := frost.SigningCommitment{}
		if err := userNonceCommitment.UnmarshalProto(req.GetSigningNonceCommitment()); err != nil {
			return nil, nil, nil, fmt.Errorf("unable to unmarshal signing nonce commitment: %w", err)
		}

		directFromCpfpJobID := uuid.New()
		jobIsDirectFromCpfpRefund[directFromCpfpJobID] = true
		signingKeyshare, err := leaf.QuerySigningKeyshare().Only(ctx)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("failed to get signing keyshare id: %w", err)
		}

		round1Packages := make(map[string]frost.SigningCommitment)

		signingCommitments := req.GetSigningCommitments()
		if signingCommitments == nil {
			return nil, nil, nil, fmt.Errorf("missing signing_commitments for leaf_id %s", req.LeafId)
		}

		for key, commitment := range signingCommitments.GetSigningCommitments() {
			obj := frost.SigningCommitment{}
			if err := obj.UnmarshalProto(commitment); err != nil {
				return nil, nil, nil, fmt.Errorf("unable to unmarshal signing commitment: %w", err)
			}
			round1Packages[key] = obj
		}
		signingJobs = append(signingJobs, &helper.SigningJobWithPregeneratedNonce{
			SigningJob: helper.SigningJob{
				JobID:             directFromCpfpJobID,
				SigningKeyshareID: signingKeyshare.ID,
				Message:           directFromCpfpRefundTxSigHash,
				VerifyingKey:      &leaf.VerifyingPubkey,
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
			return nil, nil, nil, fmt.Errorf("signing job %s has empty round1Packages (message: %x)", job.JobID, job.Message)
		}
		for key, commitment := range job.Round1Packages {
			if commitment.IsZero() {
				return nil, nil, nil, fmt.Errorf("signing job %s has invalid commitment for key %s: hiding or binding is empty (message: %x)", job.JobID, key, job.Message)
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

func AggregateSignatures(
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
		logger.Sugar().Infof("Aggregating cpfp frost signature for leaf %s (message: %x)", leafID, signingResult.Message)
		cpfpUserSignedRefund := cpfpUserRefundMap[leafID]
		leaf := leafMap[leafID]
		signatureResult, err := frostClient.AggregateFrost(ctx, &pbfrost.AggregateFrostRequest{
			Message:            signingResult.Message,
			SignatureShares:    signingResult.SignatureShares,
			PublicShares:       signingResult.PublicKeys,
			VerifyingKey:       leaf.VerifyingPubkey.Serialize(),
			Commitments:        cpfpUserSignedRefund.SigningCommitments.SigningCommitments,
			UserCommitments:    cpfpUserSignedRefund.SigningNonceCommitment,
			UserPublicKey:      leaf.OwnerSigningPubkey.Serialize(),
			UserSignatureShare: cpfpUserSignedRefund.UserSignature,
			AdaptorPublicKey:   cpfpAdaptorPubKey.Serialize(),
		})
		if err != nil {
			logger.With(zap.Error(err)).Sugar().Errorf("Unable to aggregate frost for cpfp results for leaf %s", leaf.ID)
			return nil, nil, nil, fmt.Errorf("unable to aggregate frost for cpfp results: %w, leaf_id: %s", err, leaf.ID)
		}
		finalCpfpSignatureMap[leaf.ID.String()] = signatureResult.Signature
	}
	for leafID, signingResult := range directSigningResultMap {
		logger.Sugar().Infof("Aggregating direct frost signature for direct results for leaf %s (message: %x)", leafID, signingResult.Message)
		directUserSignedRefund := directUserRefundMap[leafID]
		leaf := leafMap[leafID]
		signatureResult, err := frostClient.AggregateFrost(ctx, &pbfrost.AggregateFrostRequest{
			Message:            signingResult.Message,
			SignatureShares:    signingResult.SignatureShares,
			PublicShares:       signingResult.PublicKeys,
			VerifyingKey:       leaf.VerifyingPubkey.Serialize(),
			Commitments:        directUserSignedRefund.SigningCommitments.SigningCommitments,
			UserCommitments:    directUserSignedRefund.SigningNonceCommitment,
			UserPublicKey:      leaf.OwnerSigningPubkey.Serialize(),
			UserSignatureShare: directUserSignedRefund.UserSignature,
			AdaptorPublicKey:   directAdaptorPubKey.Serialize(),
		})
		if err != nil {
			logger.With(zap.Error(err)).Sugar().Errorf("Unable to aggregate frost for direct results for leaf %s", leaf.ID)
			return nil, nil, nil, fmt.Errorf("unable to aggregate frost for direct results: %w, leaf_id: %s", err, leaf.ID)
		}
		finalDirectSignatureMap[leaf.ID.String()] = signatureResult.Signature
	}
	for leafID, signingResult := range directFromCpfpSigningResultMap {
		logger.Sugar().Infof(
			"Aggregating direct from cpfp frost signature for direct from cpfp results for leaf %s (message: %x)",
			leafID,
			signingResult.Message,
		)
		directFromCpfpUserSignedRefund := directFromCpfpUserRefundMap[leafID]
		leaf := leafMap[leafID]
		signatureResult, err := frostClient.AggregateFrost(ctx, &pbfrost.AggregateFrostRequest{
			Message:            signingResult.Message,
			SignatureShares:    signingResult.SignatureShares,
			PublicShares:       signingResult.PublicKeys,
			VerifyingKey:       leaf.VerifyingPubkey.Serialize(),
			Commitments:        directFromCpfpUserSignedRefund.SigningCommitments.SigningCommitments,
			UserCommitments:    directFromCpfpUserSignedRefund.SigningNonceCommitment,
			UserPublicKey:      leaf.OwnerSigningPubkey.Serialize(),
			UserSignatureShare: directFromCpfpUserSignedRefund.UserSignature,
			AdaptorPublicKey:   directFromCpfpAdaptorPubKey.Serialize(),
		})
		if err != nil {
			logger.With(zap.Error(err)).Sugar().Errorf("Unable to aggregate frost for direct from cpfp results for leaf %s", leaf.ID)
			return nil, nil, nil, fmt.Errorf("unable to aggregate frost for direct from cpfp results: %w, leaf_id: %s", err, leaf.ID)
		}
		finalDirectFromCpfpSignatureMap[leaf.ID.String()] = signatureResult.Signature
	}
	return finalCpfpSignatureMap, finalDirectSignatureMap, finalDirectFromCpfpSignatureMap, nil
}

func (h *TransferHandler) FinalizeTransferWithTransferPackage(ctx context.Context, req *pb.FinalizeTransferWithTransferPackageRequest) (*pb.FinalizeTransferResponse, error) {
	transferID, err := uuid.Parse(req.GetTransferId())
	if err != nil {
		return nil, fmt.Errorf("unable to parse transfer id %s: %w", req.GetTransferId(), err)
	}
	transfer, err := h.loadTransferForUpdate(ctx, transferID)
	if err != nil {
		return nil, err
	}
	err = authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, transfer.SenderIdentityPubkey)
	if err != nil {
		return nil, err
	}
	if transfer.Status != st.TransferStatusSenderInitiated {
		return nil, fmt.Errorf("transfer %s is in state %s; expected sender initiated status", transferID, transfer.Status)
	}
	logger := logging.GetLoggerFromContext(ctx)
	logger.Sugar().Infof("Preparing to send key tweaks to other SOs for transfer %s", transferID)
	err = h.syncDeliverSenderKeyTweak(ctx, req, transfer.Type)
	if err != nil {
		entTx, dbErr := ent.GetTxFromContext(ctx)
		if dbErr != nil {
			logger.Error("failed to get db tx", zap.Error(dbErr))
		}
		if entTx != nil {
			dbErr = entTx.Rollback()
			if dbErr != nil {
				logger.Error("failed to rollback db tx", zap.Error(dbErr))
			}
		}
		// Counterswaps are from the SSP. We need to allow SSP to
		// perform retries, so don't cancel the transfer, just reset it
		if transfer.Type == st.TransferTypeCounterSwap {
			rollbackErr := h.CreateRollbackTransferGossipMessage(ctx, transferID)
			if rollbackErr != nil {
				logger.With(zap.Error(rollbackErr)).Sugar().Errorf("Error when rolling back sender key tweaks for transfer %s", transferID)
			}
		} else {
			cancelErr := h.CreateCancelTransferGossipMessage(ctx, transferID)
			if cancelErr != nil {
				logger.With(zap.Error(cancelErr)).Sugar().Errorf("Error when canceling transfer %s", transferID)
			}
		}
		errorMsg := fmt.Sprintf("failed to sync deliver sender key tweak for transfer %s", transferID)
		if stat, ok := status.FromError(err); ok && stat.Code() == codes.Unavailable {
			// Preserve external error's gRPC code and reason, prefixing with external coordinator context
			enriched := sparkerrors.WrapErrorWithMessage(err, errorMsg)
			return nil, sparkerrors.WrapErrorWithReasonPrefix(enriched, sparkerrors.ErrorReasonPrefixFailedWithExternalCoordinator)
		}
		entTx, dbErr = ent.GetTxFromContext(ctx)
		if dbErr != nil {
			logger.Error("failed to get db tx", zap.Error(dbErr))
		}
		if entTx != nil {
			dbErr = entTx.Commit()
			if dbErr != nil {
				logger.Error("failed to commit db tx", zap.Error(dbErr))
			}
		}
		return nil, fmt.Errorf("%s: %w", errorMsg, err)
	}
	logger.Sugar().Infof("Successfully delivered key tweaks to other SOs for transfer %s", transferID)

	entTx, err := ent.GetTxFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}
	db := entTx.Client()
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
		return nil, fmt.Errorf("failed to update status of transfer %s: %w", transferID, err)
	}
	ownerIDPubKey, err := keys.ParsePublicKey(req.OwnerIdentityPublicKey)
	if err != nil {
		return nil, fmt.Errorf("failed to parse owner identity public key: %w", err)
	}
	if err = h.setSoCoordinatorKeyTweaks(ctx, transfer, req.TransferPackage, ownerIDPubKey); err != nil {
		return nil, err
	}

	if shouldTweakKey {
		if err = entTx.Commit(); err != nil {
			return nil, fmt.Errorf("failed to commit transaction: %w", err)
		}
		err = h.settleSenderKeyTweaks(ctx, transferID, pbinternal.SettleKeyTweakAction_COMMIT)
		if err != nil {
			return nil, err
		}

		transfer, err = h.loadTransferForUpdate(ctx, transferID)
		if err != nil {
			return nil, fmt.Errorf("failed to load transfer for update: %w", err)
		}
		transfer, err = h.commitSenderKeyTweaks(ctx, transfer)
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

	db, err = ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get database transaction: %w", err)
	}
	_, err = db.PendingSendTransfer.Update().Where(pendingsendtransfer.TransferID(transfer.ID)).SetStatus(st.PendingSendTransferStatusFinished).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to update pending send transfer: %w", err)
	}
	return &pb.FinalizeTransferResponse{Transfer: transferProto}, err
}

// checkTransferAccess checks if the viewer has read access to either the sender or receiver wallet.
// It updates the accessMap cache to avoid redundant database queries.
// Returns true if the viewer has access, false otherwise, and an error if the check fails.
func (h *TransferHandler) checkTransferAccess(
	ctx context.Context,
	transfer *ent.Transfer,
	accessMap map[keys.Public]bool,
) (bool, error) {
	// Check sender access first
	hasReadAccess, exists := accessMap[transfer.SenderIdentityPubkey]
	if !exists {
		var err error
		hasReadAccess, err = NewWalletSettingHandler(h.config).HasReadAccessToWallet(ctx, transfer.SenderIdentityPubkey)
		if err != nil {
			return false, fmt.Errorf("failed to check if viewer has read access to transfer %s: %w", transfer.ID.String(), err)
		}
		accessMap[transfer.SenderIdentityPubkey] = hasReadAccess
	}
	if hasReadAccess {
		return true, nil
	}

	// If sender doesn't have access, check receiver access
	hasReadAccess, exists = accessMap[transfer.ReceiverIdentityPubkey]
	if !exists {
		var err error
		hasReadAccess, err = NewWalletSettingHandler(h.config).HasReadAccessToWallet(ctx, transfer.ReceiverIdentityPubkey)
		if err != nil {
			return false, fmt.Errorf("failed to check if viewer has read access to transfer %s: %w", transfer.ID.String(), err)
		}
		accessMap[transfer.ReceiverIdentityPubkey] = hasReadAccess
	}
	return hasReadAccess, nil
}

func (h *TransferHandler) queryTransfers(ctx context.Context, filter *pb.TransferFilter, isPending bool, isSSP bool) (*pb.QueryTransfersResponse, error) {
	ctx, span := tracer.Start(ctx, "TransferHandler.queryTransfers")
	defer span.End()

	if filter.GetParticipant() == nil && len(filter.TransferIds) == 0 {
		return nil, status.Error(codes.InvalidArgument, "must specify either filter.Participant or filter.TransferIds")
	}

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

	var walletIdentityPubkey *keys.Public
	switch filter.Participant.(type) {
	case *pb.TransferFilter_ReceiverIdentityPublicKey:
		receiverIDPubKey, err := keys.ParsePublicKey(filter.GetReceiverIdentityPublicKey())
		if err != nil {
			return nil, fmt.Errorf("invalid receiver identity public key: %w", err)
		}
		transferPredicate = append(transferPredicate, enttransfer.ReceiverIdentityPubkeyEQ(receiverIDPubKey))
		if isPending {
			transferPredicate = append(transferPredicate, enttransfer.StatusIn(receiverPendingStatuses...))
		}
		walletIdentityPubkey = &receiverIDPubKey
	case *pb.TransferFilter_SenderIdentityPublicKey:
		senderIDPubKey, err := keys.ParsePublicKey(filter.GetSenderIdentityPublicKey())
		if err != nil {
			return nil, fmt.Errorf("invalid sender identity public key: %w", err)
		}
		transferPredicate = append(transferPredicate, enttransfer.SenderIdentityPubkeyEQ(senderIDPubKey))
		if isPending {
			transferPredicate = append(transferPredicate,
				enttransfer.StatusIn(senderPendingStatuses...),
				enttransfer.ExpiryTimeLT(time.Now()),
			)
		}
		walletIdentityPubkey = &senderIDPubKey
	case *pb.TransferFilter_SenderOrReceiverIdentityPublicKey:
		identityPubKey, err := keys.ParsePublicKey(filter.GetSenderOrReceiverIdentityPublicKey())
		if err != nil {
			return nil, fmt.Errorf("invalid sender or receiver identity public key: %w", err)
		}
		if isPending {
			transferPredicate = append(transferPredicate, enttransfer.Or(
				enttransfer.And(
					enttransfer.ReceiverIdentityPubkeyEQ(identityPubKey),
					enttransfer.StatusIn(receiverPendingStatuses...),
				),
				enttransfer.And(
					enttransfer.SenderIdentityPubkeyEQ(identityPubKey),
					enttransfer.StatusIn(senderPendingStatuses...),
					enttransfer.ExpiryTimeLT(time.Now()),
				),
			))
		} else {
			transferPredicate = append(transferPredicate, enttransfer.Or(
				enttransfer.ReceiverIdentityPubkeyEQ(identityPubKey),
				enttransfer.SenderIdentityPubkeyEQ(identityPubKey),
			))
		}
		walletIdentityPubkey = &identityPubKey
	default:
		if isPending {
			transferPredicate = append(
				transferPredicate,
				enttransfer.StatusIn(append(senderPendingStatuses, receiverPendingStatuses...)...),
			)
		}
	}

	if !isSSP && walletIdentityPubkey != nil {
		hasReadAccess, err := NewWalletSettingHandler(h.config).HasReadAccessToWallet(ctx, *walletIdentityPubkey)
		if err != nil {
			return nil, fmt.Errorf("failed to check if viewer has read access to wallet %s: %w", walletIdentityPubkey.String(), err)
		}
		if !hasReadAccess {
			return &pb.QueryTransfersResponse{
				Offset: -1,
			}, nil
		}
	}

	if len(filter.TransferIds) > 0 {
		transferUUIDs, err := uuids.ParseSlice(filter.GetTransferIds())
		if err != nil {
			return nil, fmt.Errorf("unable to parse transfer IDs as UUIDs: %w", err)
		}
		transferPredicate = append(transferPredicate, enttransfer.IDIn(transferUUIDs...))
	}

	var network btcnetwork.Network
	if filter.GetNetwork() == pb.Network_UNSPECIFIED {
		network = btcnetwork.Mainnet
	} else {
		var err error
		network, err = btcnetwork.FromProtoNetwork(filter.GetNetwork())
		if err != nil {
			return nil, fmt.Errorf("failed to convert proto network to schema network: %w", err)
		}
	}

	if len(filter.Types) > 0 {
		transferTypes := make([]st.TransferType, len(filter.Types))

		networkString := network.String()
		filterSSPCounterSwap := knobs.GetKnobsService(ctx).GetValueTarget(knobs.KnobFilterSSPCounterSwapAsTransfer, &networkString, 0) > 0

		for i, protoType := range filter.Types {
			schemaType, err := st.TransferTypeFromProto(protoType.String())
			if err != nil {
				return nil, status.Errorf(codes.InvalidArgument, "invalid transfer type: %s", protoType.String())
			}
			transferTypes[i] = schemaType

			if filterSSPCounterSwap && (schemaType == st.TransferTypeCounterSwap || schemaType == st.TransferTypeCounterSwapV3) {
				filterSSPCounterSwap = false
			}
		}
		transferPredicate = append(transferPredicate, enttransfer.TypeIn(transferTypes...))

		// Find the most recent swap sent by the participant to find the SSP identity public key to filter out
		if filterSSPCounterSwap && walletIdentityPubkey != nil {
			if pred := h.getSSPCounterSwapFilter(ctx, db, network, *walletIdentityPubkey); pred != nil {
				transferPredicate = append(transferPredicate, pred)
			}
		}
	}

	transferPredicate = append(transferPredicate, enttransfer.NetworkEQ(network))

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

	// Validate time filter - both cannot be set simultaneously
	if filter.GetCreatedAfter() != nil && filter.GetCreatedBefore() != nil {
		return nil, status.Error(codes.InvalidArgument, "cannot specify both created_after and created_before filters")
	}

	// Apply time filter if provided (mutually exclusive - only one can be set)
	if filter.GetCreatedAfter() != nil {
		createdAfter := filter.GetCreatedAfter().AsTime().UTC()
		transferPredicate = append(transferPredicate, enttransfer.CreateTimeGT(createdAfter))
	} else if filter.GetCreatedBefore() != nil {
		createdBefore := filter.GetCreatedBefore().AsTime().UTC()
		transferPredicate = append(transferPredicate, enttransfer.CreateTimeLT(createdBefore))
	}

	baseQuery := db.Transfer.Query().WithSparkInvoice()
	if len(transferPredicate) > 0 {
		baseQuery = baseQuery.Where(enttransfer.And(transferPredicate...))
	}

	var query *ent.TransferQuery
	if filter.Order == pb.Order_ASCENDING {
		query = baseQuery.Order(ent.Asc(enttransfer.FieldCreateTime))
	} else {
		query = baseQuery.Order(ent.Desc(enttransfer.FieldCreateTime))
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
	accessMap := make(map[keys.Public]bool)
	for _, transfer := range transfers {
		if walletIdentityPubkey == nil && !isSSP {
			// If no participant is set and not SSP, we need to check if the viewer has read access to either the sender or receiver
			hasReadAccess, err := h.checkTransferAccess(ctx, transfer, accessMap)
			if err != nil {
				return nil, err
			}
			if !hasReadAccess {
				continue
			}
		}

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

func (h *TransferHandler) getSSPCounterSwapFilter(ctx context.Context, db *ent.Client, network btcnetwork.Network, walletIdentityPubkey keys.Public) predicate.Transfer {
	swap, err := db.Transfer.Query().
		Where(
			enttransfer.And(
				enttransfer.TypeIn(st.TransferTypeSwap, st.TransferTypePrimarySwapV3),
				enttransfer.NetworkEQ(network),
				enttransfer.SenderIdentityPubkeyEQ(walletIdentityPubkey),
			),
		).
		Order(ent.Desc(enttransfer.FieldCreateTime)).
		First(ctx)

	if err != nil || swap == nil {
		logger := logging.GetLoggerFromContext(ctx)
		logger.Sugar().Warnf("failed to find swap for wallet %s: %v", walletIdentityPubkey.String(), err)
		// Don't want to fail the entire query if we can't find a swap or error here, just return nil
		return nil
	}

	// include if !(sender is SSP and type is transfer)
	return enttransfer.Not(
		enttransfer.And(
			enttransfer.SenderIdentityPubkeyEQ(swap.ReceiverIdentityPubkey),
			enttransfer.TypeEQ(st.TransferTypeTransfer),
		),
	)
}

func (h *TransferHandler) QueryPendingTransfers(ctx context.Context, filter *pb.TransferFilter) (*pb.QueryTransfersResponse, error) {
	return h.queryTransfers(ctx, filter, true, false)
}

func (h *TransferHandler) QueryAllTransfers(ctx context.Context, filter *pb.TransferFilter, isSSP bool) (*pb.QueryTransfersResponse, error) {
	return h.queryTransfers(ctx, filter, false, isSSP)
}

const CoopExitConfirmationThreshold = 6

func checkCoopExitTxBroadcasted(ctx context.Context, db *ent.Client, transfer *ent.Transfer) error {
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
	if coopExit.ConfirmationHeight == nil {
		return sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("coop exit tx hasn't been broadcasted"))
	}
	if *coopExit.ConfirmationHeight+CoopExitConfirmationThreshold-1 > blockHeight.Height {
		return sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("coop exit tx doesn't have enough confirmations: confirmation height: %d current block height: %d", coopExit.ConfirmationHeight, blockHeight.Height))
	}
	return nil
}

// ClaimTransferTweakKeys starts claiming a pending transfer by tweaking keys of leaves.
func (h *TransferHandler) ClaimTransferTweakKeys(ctx context.Context, req *pb.ClaimTransferTweakKeysRequest) error {
	ctx, span := tracer.Start(ctx, "TransferHandler.ClaimTransferTweakKeys")
	defer span.End()
	reqOwnerIDPubKey, err := keys.ParsePublicKey(req.GetOwnerIdentityPublicKey())
	if err != nil {
		return fmt.Errorf("invalid identity public key: %w", err)
	}
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, reqOwnerIDPubKey); err != nil {
		return err
	}

	transferID, err := uuid.Parse(req.GetTransferId())
	if err != nil {
		return fmt.Errorf("invalid transfer ID: %w", err)
	}

	transfer, err := h.loadTransferForUpdate(ctx, transferID, sql.WithLockAction(sql.NoWait))
	if err != nil {
		return fmt.Errorf("unable to load transfer %s: %w", transferID, err)
	}
	span.SetAttributes(transferTypeKey.String(string(transfer.Type)))
	if !transfer.ReceiverIdentityPubkey.Equals(reqOwnerIDPubKey) {
		return fmt.Errorf("cannot claim transfer %s, receiver identity public key mismatch", transferID)
	}
	// Validate transfer is not in terminal states
	if transfer.Status == st.TransferStatusCompleted {
		return sparkerrors.AlreadyExistsDuplicateOperation(fmt.Errorf("transfer %s has already been claimed", transferID))
	}
	if transfer.Status == st.TransferStatusExpired ||
		transfer.Status == st.TransferStatusReturned {
		return sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("transfer %s is in terminal state %s and cannot be processed", transferID, transfer.Status))
	}
	if transfer.Status != st.TransferStatusSenderKeyTweaked {
		return sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("please call ClaimTransferSignRefunds to claim the transfer %s, the transfer is not in SENDER_KEY_TWEAKED status. transferstatus: %s", transferID, transfer.Status))
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to get or create current tx for request: %w", err)
	}
	if err := checkCoopExitTxBroadcasted(ctx, db, transfer); err != nil {
		return fmt.Errorf("failed to unlock transfer %s: %w", transferID, err)
	}

	// Validate leaves count
	transferLeaves, err := transfer.QueryTransferLeaves().WithLeaf().All(ctx)
	if err != nil {
		return fmt.Errorf("unable to get transfer leaves for transfer %s: %w", transferID, err)
	}
	if len(transferLeaves) != len(req.LeavesToReceive) {
		return fmt.Errorf("inconsistent leaves to claim for transfer %s", transferID)
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
		_, err = leaf.Update().SetKeyTweak(leafTweakBytes).Save(ctx)
		if err != nil {
			return fmt.Errorf("unable to update leaf %s: %w", leafTweak.LeafId, err)
		}
	}

	// Update transfer status
	_, err = transfer.Update().SetStatus(st.TransferStatusReceiverKeyTweaked).Save(ctx)
	if err != nil {
		return fmt.Errorf("unable to update transfer status %v: %w", transfer.ID, err)
	}

	return nil
}

func (h *TransferHandler) claimLeafTweakKey(ctx context.Context, leaf *ent.TreeNode, req *pb.ClaimLeafKeyTweak, ownerIdentityPubKey keys.Public) (*ent.TreeNodeKeyUpdateInput, error) {
	ctx, span := tracer.Start(ctx, "TransferHandler.claimLeafTweakKey")
	defer span.End()

	if req.SecretShareTweak == nil {
		return nil, fmt.Errorf("secret share tweak is required")
	}
	if len(req.SecretShareTweak.SecretShare) == 0 {
		return nil, fmt.Errorf("secret share is required")
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
		return nil, fmt.Errorf("unable to validate share: %w", err)
	}

	logger := logging.GetLoggerFromContext(ctx)

	if leaf.Status != st.TreeNodeStatusTransferLocked {
		// This should be safe to continue because SO holds the transfer and this should be a
		// self healing process if something when in between transfers and forcibly set the leaf to
		// available.
		// TODO: Revisit this to make sure this won't cause problems.
		logger.Sugar().Warnf("Leaf %s is not in transfer locked status, status: %s", leaf.ID.String(), leaf.Status)
	}

	// Tweak keyshare
	keyshare, err := leaf.QuerySigningKeyshare().First(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to load keyshare for leaf %s: %w", leaf.ID.String(), err)
	}

	secretShare, err := keys.ParsePrivateKey(req.SecretShareTweak.SecretShare)
	if err != nil {
		return nil, fmt.Errorf("unable to parse secret share: %w", err)
	}
	pubKeyTweak, err := keys.ParsePublicKey(req.SecretShareTweak.Proofs[0])
	if err != nil {
		return nil, fmt.Errorf("unable to parse public key: %w", err)
	}
	pubKeySharesTweak, err := keys.ParsePublicKeyMap(req.PubkeySharesTweak)
	if err != nil {
		return nil, fmt.Errorf("unable to parse public key shares tweaks: %w", err)
	}
	tweakedKeyshare, err := keyshare.TweakKeyShare(ctx, secretShare, pubKeyTweak, pubKeySharesTweak)
	if err != nil {
		return nil, fmt.Errorf("unable to tweak keyshare %v for leaf %v: %w", keyshare.ID, leaf.ID, err)
	}

	signingPubKey := leaf.VerifyingPubkey.Sub(tweakedKeyshare.PublicKey)
	return &ent.TreeNodeKeyUpdateInput{
		ID:                  leaf.ID,
		OwnerIdentityPubkey: ownerIdentityPubKey,
		OwnerSigningPubkey:  signingPubKey,
	}, nil
}

func (h *TransferHandler) getLeavesFromTransfer(ctx context.Context, transfer *ent.Transfer) (map[string]*ent.TreeNode, error) {
	ctx, span := tracer.Start(ctx, "TransferHandler.getLeavesFromTransfer", trace.WithAttributes(
		transferTypeKey.String(string(transfer.Type)),
	))
	defer span.End()

	transferLeaves, err := transfer.QueryTransferLeaves().WithLeaf(func(tnq *ent.TreeNodeQuery) {
		tnq.WithTree().WithSigningKeyshare()
	}).All(ctx)
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
		return fmt.Errorf("unable to update transfer status %v: %w", transfer.ID, err)
	}
	for _, leaf := range transferLeaves {
		_, err := leaf.Update().SetKeyTweak(nil).Save(ctx)
		if err != nil {
			return fmt.Errorf("unable to update leaf %v: %w", leaf.ID, err)
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
		if status.Code(err) == codes.Unavailable ||
			status.Code(err) == codes.Canceled ||
			strings.Contains(err.Error(), "context canceled") ||
			strings.Contains(err.Error(), "unexpected HTTP status code") ||
			strings.Contains(err.Error(), "SQLSTATE") {
			logger.Sugar().Error("Unable to settle receiver key tweak due to operator unavailability, please try again later", zap.Error(err))
			return fmt.Errorf("unable to settle receiver key tweak due to operator unavailability: %w, please try again later", err)
		}
		logger.Error("Unable to settle receiver key tweak, you might have a race condition in your implementation", zap.Error(err))
		action = pbinternal.SettleKeyTweakAction_ROLLBACK
	}

	err = h.InitiateSettleReceiverKeyTweak(ctx, &pbinternal.InitiateSettleReceiverKeyTweakRequest{
		TransferId:     transfer.ID.String(),
		KeyTweakProofs: keyTweakProofs,
		UserPublicKeys: userPublicKeys,
	})
	if err != nil {
		logger.Error("Unable to settle receiver key tweak internally, you might have a race condition in your implementation", zap.Error(err))
		action = pbinternal.SettleKeyTweakAction_ROLLBACK
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

func validateReceivedRefundTransactions(ctx context.Context, job *pb.LeafRefundTxSigningJob, leaf *ent.TreeNode, transferType st.TransferType) error {
	if job.RefundTxSigningJob == nil {
		return fmt.Errorf("missing RefundTxSigningJob for leaf %s", job.LeafId)
	}

	// Helper function to safely extract RawTx from signing job
	getRawTx := func(signingJob *pb.SigningJob) []byte {
		if signingJob == nil {
			return nil
		}
		return signingJob.RawTx
	}

	// If ALL incoming txs match what's already in the DB,
	// this is a retry of a previous signing request - skip validation
	if bytes.Equal(job.RefundTxSigningJob.RawTx, leaf.RawRefundTx) {
		if !bytes.Equal(getRawTx(job.DirectRefundTxSigningJob), leaf.DirectRefundTx) ||
			!bytes.Equal(getRawTx(job.DirectFromCpfpRefundTxSigningJob), leaf.DirectFromCpfpRefundTx) {
			return fmt.Errorf("refund signing retry for leaf %s must not change direct refund transactions", job.LeafId)
		}
		return nil
	}

	refundDestPubKey, err := keys.ParsePublicKey(job.RefundTxSigningJob.SigningPublicKey)
	if err != nil {
		return fmt.Errorf("invalid refund signing public key for leaf %s: %w", job.LeafId, err)
	}

	if err := validateSingleLeafRefundTxs(
		ctx,
		leaf,
		getRawTx(job.RefundTxSigningJob),
		getRawTx(job.DirectFromCpfpRefundTxSigningJob),
		getRawTx(job.DirectRefundTxSigningJob),
		refundDestPubKey,
		transferType,
	); err != nil {
		return fmt.Errorf("refund transaction validation failed for leaf %s: %w", job.LeafId, err)
	}

	return nil
}

// ClaimTransferSignRefundsV2 signs new refund transactions as part of the transfer.
func (h *TransferHandler) ClaimTransferSignRefundsV2(ctx context.Context, req *pb.ClaimTransferSignRefundsRequest) (*pb.ClaimTransferSignRefundsResponse, error) {
	return h.claimTransferSignRefunds(ctx, req, true)
}

// ClaimTransferSignRefunds signs new refund transactions as part of the transfer.
func (h *TransferHandler) ClaimTransferSignRefunds(ctx context.Context, req *pb.ClaimTransferSignRefundsRequest) (*pb.ClaimTransferSignRefundsResponse, error) {
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

	transferID, err := uuid.Parse(req.GetTransferId())
	if err != nil {
		return nil, fmt.Errorf("invalid transfer ID: %w", err)
	}

	transfer, err := h.loadTransferForUpdate(ctx, transferID, sql.WithLockAction(sql.NoWait))
	if err != nil {
		return nil, fmt.Errorf("unable to load transfer %s: %w", transferID, err)
	}
	span.SetAttributes(transferTypeKey.String(string(transfer.Type)))
	if !transfer.ReceiverIdentityPubkey.Equals(reqOwnerIDPubKey) {
		return nil, fmt.Errorf("cannot claim transfer %s, receiver identity public key mismatch", transferID)
	}

	switch transfer.Status {
	case st.TransferStatusReceiverKeyTweaked:
	case st.TransferStatusReceiverRefundSigned:
	case st.TransferStatusReceiverKeyTweakLocked:
	case st.TransferStatusReceiverKeyTweakApplied:
		// do nothing
	case st.TransferStatusCompleted:
		return nil, sparkerrors.AlreadyExistsDuplicateOperation(fmt.Errorf("transfer %s has already been claimed", transferID))
	default:
		return nil, fmt.Errorf("transfer %s is expected to be at status TransferStatusKeyTweaked or TransferStatusReceiverRefundSigned or TransferStatusReceiverKeyTweakLocked or TransferStatusReceiverKeyTweakApplied but %s found", transferID, transfer.Status)
	}

	// Validate leaves count
	leavesToTransfer, err := transfer.QueryTransferLeaves().All(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to load leaves to transfer for transfer %s: %w", transferID, err)
	}
	if len(leavesToTransfer) != len(req.SigningJobs) {
		return nil, fmt.Errorf("inconsistent leaves to claim for transfer %s", transferID)
	}

	keyTweakProofs := map[string]*pb.SecretProof{}
	for _, leaf := range leavesToTransfer {
		treeNode, err := leaf.QueryLeaf().Only(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to get tree node for leaf %s: %w", leaf.ID, err)
		}
		leafKeyTweak := &pb.ClaimLeafKeyTweak{}
		if leaf.KeyTweak != nil {
			err = proto.Unmarshal(leaf.KeyTweak, leafKeyTweak)
			if err != nil {
				return nil, fmt.Errorf("unable to unmarshal key tweak for leaf %s: %w", leaf.ID, err)
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
	transfer, err = h.loadTransferForUpdate(ctx, transferID)
	if err != nil {
		return nil, fmt.Errorf("unable to load transfer %s: %w", transferID, err)
	}
	if transfer.Status == st.TransferStatusCompleted {
		return nil, sparkerrors.AlreadyExistsDuplicateOperation(fmt.Errorf("transfer %s is already completed", transferID))
	}

	// Update transfer status.
	_, err = transfer.Update().SetStatus(st.TransferStatusReceiverRefundSigned).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to update transfer status %s: %w", transfer.ID, err)
	}

	leaves, err := h.getLeavesFromTransfer(ctx, transfer)
	if err != nil {
		return nil, err
	}

	if len(leaves) == 0 {
		return nil, fmt.Errorf("leaves cannot be empty")
	}

	// Extract network from first leaf (all leaves should be on the same network)
	var networkString string
	for _, leaf := range leaves {
		networkString = leaf.Network.String()
		break
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get db from context: %w", err)
	}

	// Collect all TreeNode updates to batch them and avoid N+1 queries
	builders := make([]*ent.TreeNodeCreate, 0, len(req.SigningJobs))

	enhancedTransferReceiveValidationEnabled := knobs.GetKnobsService(ctx).GetValueTarget(knobs.KnobEnhancedTransferReceiveValidation, &networkString, 0) > 0

	var signingJobs []*helper.SigningJob
	jobToLeafMap := make(map[uuid.UUID]uuid.UUID)
	isDirectSigningJob := make(map[uuid.UUID]bool)
	isDirectFromCpfpSigningJob := make(map[uuid.UUID]bool)
	isSwap := transfer.Type == st.TransferTypeCounterSwap || transfer.Type == st.TransferTypeSwap || transfer.Type == st.TransferTypePrimarySwapV3 || transfer.Type == st.TransferTypeCounterSwapV3
	isSupportedTransferType := transfer.Type == st.TransferTypeTransfer || transfer.Type == st.TransferTypeCounterSwap || transfer.Type == st.TransferTypeSwap || transfer.Type == st.TransferTypePrimarySwapV3 || transfer.Type == st.TransferTypeCounterSwapV3 || transfer.Type == st.TransferTypeCooperativeExit

	for _, job := range req.SigningJobs {
		leaf, exists := leaves[job.LeafId]
		if !exists {
			return nil, fmt.Errorf("unexpected leaf id %s", job.LeafId)
		}

		if isSupportedTransferType && enhancedTransferReceiveValidationEnabled {
			if err := validateReceivedRefundTransactions(ctx, job, leaf, transfer.Type); err != nil {
				return nil, err
			}
		}

		directRefundTxSigningJob := (*pb.SigningJob)(nil)
		directFromCpfpRefundTxSigningJob := (*pb.SigningJob)(nil)
		if job.DirectRefundTxSigningJob != nil {
			directRefundTxSigningJob = job.DirectRefundTxSigningJob
		} else if !isSwap && requireDirectTx && len(leaf.DirectTx) > 0 {
			isZeroNode, err := bitcointransaction.IsZeroNode(leaf)
			if err != nil {
				return nil, fmt.Errorf("failed to determine if node is zero node: %w", err)
			}

			if !isZeroNode {
				return nil, fmt.Errorf("DirectRefundTxSigningJob is required. Please upgrade to the latest SDK version")
			}
		}
		if job.DirectFromCpfpRefundTxSigningJob != nil {
			directFromCpfpRefundTxSigningJob = job.DirectFromCpfpRefundTxSigningJob
		} else if !isSwap && requireDirectTx {
			networkString := transfer.Network.String()
			if knobs.GetKnobsService(ctx).GetValueTarget(knobs.KnobRequireDirectFromCPFPRefund, &networkString, 0) > 0 {
				return nil, fmt.Errorf("DirectFromCpfpRefundTxSigningJob is required. Please upgrade to the latest SDK version")
			}
			if len(leaf.DirectTx) > 0 {
				return nil, fmt.Errorf("DirectFromCpfpRefundTxSigningJob is required. Please upgrade to the latest SDK version")
			}
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

		// Compute txids from transaction bytes (same logic as ent hooks)
		rawRefundTx, err := common.TxFromRawTxBytes(job.RefundTxSigningJob.RawTx)
		if err != nil {
			return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("failed to parse raw_refund_tx for leaf %s: %w", leafID, err))
		}
		rawRefundTxid := st.NewTxID(rawRefundTx.TxHash())

		// Build upsert for batch update. Since records always exist (queried above),
		// OnConflict will always UPDATE, never INSERT. We set ID (for matching), all required fields, and the fields we want to update.
		builder := db.TreeNode.Create().
			SetID(leaf.ID).
			SetTree(leaf.Edges.Tree).
			SetNetwork(leaf.Edges.Tree.Network).
			SetSigningKeyshare(leaf.Edges.SigningKeyshare).
			SetValue(leaf.Value).
			SetVerifyingPubkey(leaf.VerifyingPubkey).
			SetOwnerIdentityPubkey(leaf.OwnerIdentityPubkey).
			SetOwnerSigningPubkey(leaf.OwnerSigningPubkey).
			SetRawTx(leaf.RawTx).
			SetVout(leaf.Vout).
			SetStatus(leaf.Status).
			SetRawRefundTx(job.RefundTxSigningJob.RawTx).
			SetRawRefundTxid(rawRefundTxid)

		if directRefundTx != nil {
			builder = builder.SetDirectRefundTx(directRefundTx)
			directRefundTxParsed, err := common.TxFromRawTxBytes(directRefundTx)
			if err != nil {
				return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("failed to parse direct_refund_tx for leaf %s: %w", leafID, err))
			}
			builder = builder.SetDirectRefundTxid(st.NewTxID(directRefundTxParsed.TxHash()))
		}

		if directFromCpfpRefundTx != nil {
			builder = builder.SetDirectFromCpfpRefundTx(directFromCpfpRefundTx)
			directFromCpfpRefundTxParsed, err := common.TxFromRawTxBytes(directFromCpfpRefundTx)
			if err != nil {
				return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("failed to parse direct_from_cpfp_refund_tx for leaf %s: %w", leafID, err))
			}
			builder = builder.SetDirectFromCpfpRefundTxid(st.NewTxID(directFromCpfpRefundTxParsed.TxHash()))
		}

		builders = append(builders, builder)

		cpfpSigningJob, directSigningJob, directFromCpfpSigningJob, err := h.getRefundTxSigningJobs(ctx, leaf, job.RefundTxSigningJob, job.DirectRefundTxSigningJob, job.DirectFromCpfpRefundTxSigningJob)
		if err != nil {
			return nil, fmt.Errorf("unable to create signing jobs for leaf %s: %w", leafID, err)
		}
		signingJobs = append(signingJobs, cpfpSigningJob)
		jobToLeafMap[cpfpSigningJob.JobID] = leaf.ID
		isDirectSigningJob[cpfpSigningJob.JobID] = false
		isDirectFromCpfpSigningJob[cpfpSigningJob.JobID] = false
		if directSigningJob != nil {
			signingJobs = append(signingJobs, directSigningJob)
			jobToLeafMap[directSigningJob.JobID] = leaf.ID
			isDirectSigningJob[directSigningJob.JobID] = true
		}
		if directFromCpfpSigningJob != nil {
			signingJobs = append(signingJobs, directFromCpfpSigningJob)
			jobToLeafMap[directFromCpfpSigningJob.JobID] = leaf.ID
			isDirectFromCpfpSigningJob[directFromCpfpSigningJob.JobID] = true
		}
	}

	// Execute all TreeNode updates in batch to avoid N+1 queries.
	// We use CreateBulk with OnConflict as a workaround since Ent doesn't have native bulk UPDATE support.
	// Since all records exist (queried above), OnConflict will always UPDATE, never INSERT.
	// Batch in chunks to avoid PostgreSQL parameter limit (65535).
	const maxBatchSize = 1000
	for chunk := range slices.Chunk(builders, maxBatchSize) {
		err = db.TreeNode.CreateBulk(chunk...).
			OnConflictColumns(enttreenode.FieldID).
			Update(func(u *ent.TreeNodeUpsert) {
				u.UpdateRawRefundTx()
				u.UpdateRawRefundTxid()
				u.UpdateDirectRefundTx()
				u.UpdateDirectRefundTxid()
				u.UpdateDirectFromCpfpRefundTx()
				u.UpdateDirectFromCpfpRefundTxid()
			}).
			Exec(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to batch update tree node refund txs: %w", err)
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
				VerifyingKey: leaf.VerifyingPubkey.Serialize(),
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

	// Create direct refund signing job if direct tx exists and job is provided
	if len(leaf.DirectTx) > 0 && directJob != nil {
		directLeafTx, err := common.TxFromRawTxBytes(leaf.DirectTx)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("unable to load direct leaf tx for leaf %s: %w", leaf.ID.String(), err)
		}
		if len(directLeafTx.TxOut) == 0 {
			return nil, nil, nil, fmt.Errorf("vout out of bounds for direct tx")
		}
		directRefundSigningJob, _, err = helper.NewSigningJob(keyshare, directJob, directLeafTx.TxOut[0])
		if err != nil {
			return nil, nil, nil, fmt.Errorf("unable to create direct signing job for leaf %s: %w", leaf.ID.String(), err)
		}
	}

	// Always create direct from cpfp refund signing job if provided
	if directFromCpfpJob != nil {
		directFromCpfpRefundSigningJob, _, err = helper.NewSigningJob(keyshare, directFromCpfpJob, cpfpLeafTx.TxOut[0])
		if err != nil {
			return nil, nil, nil, fmt.Errorf("unable to create direct from cpfp signing job for leaf %s: %w", leaf.ID.String(), err)
		}
	}
	if len(cpfpLeafTx.TxOut) == 0 {
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

	transferID, err := uuid.Parse(req.GetTransferId())
	if err != nil {
		return fmt.Errorf("invalid transfer ID: %w", err)
	}

	transfer, err := h.loadTransferForUpdate(ctx, transferID)
	if err != nil {
		return fmt.Errorf("unable to load transfer %s: %w", transferID, err)
	}
	span.SetAttributes(transferTypeKey.String(string(transfer.Type)))

	if transfer.Status == st.TransferStatusCompleted {
		// The transfer is already completed, return early.
		return nil
	}

	userPubKeys, err := keys.ParsePublicKeyMap(req.GetUserPublicKeys())
	if err != nil {
		return err
	}
	applied, err := h.checkIfKeyTweakApplied(ctx, transfer, userPubKeys)
	if err != nil {
		return fmt.Errorf("unable to check if key tweak is applied: %w", err)
	}
	if applied {
		_, err = transfer.Update().SetStatus(st.TransferStatusReceiverKeyTweakApplied).Save(ctx)
		if err != nil {
			return fmt.Errorf("unable to update transfer status %s: %w", transfer.ID, err)
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
		return fmt.Errorf("transfer %s is expected to be at status TransferStatusReceiverKeyTweaked or TransferStatusReceiverKeyTweakLocked or TransferStatusReceiverKeyTweakApplied but %s found", transferID, transfer.Status)
	}

	leaves, err := transfer.QueryTransferLeaves().All(ctx)
	if err != nil {
		return fmt.Errorf("unable to get leaves from transfer %s: %w", transferID, err)
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
		return fmt.Errorf("unable to update transfer status %s: %w", transfer.ID, err)
	}

	entTx, err := ent.GetTxFromContext(ctx)
	if err != nil {
		return fmt.Errorf("unable to get db: %w", err)
	}
	err = entTx.Commit()
	if err != nil {
		return fmt.Errorf("unable to commit db: %w", err)
	}

	return nil
}

func (h *TransferHandler) checkIfKeyTweakApplied(ctx context.Context, transfer *ent.Transfer, userPublicKeys map[string]keys.Public) (bool, error) {
	leaves, err := transfer.QueryTransferLeaves().QueryLeaf().WithSigningKeyshare().All(ctx)
	if err != nil {
		return false, fmt.Errorf("unable to get leaves from transfer %v: %w", transfer.ID, err)
	}

	var tweaked, tweakedSet bool
	for _, leaf := range leaves {
		userPublicKey, ok := userPublicKeys[leaf.ID.String()]
		if !ok {
			return false, fmt.Errorf("user public key for leaf %v not found", leaf.ID)
		}
		sparkPublicKey := leaf.Edges.SigningKeyshare.PublicKey
		combinedPublicKey := sparkPublicKey.Add(userPublicKey)

		localTweaked := combinedPublicKey.Equals(leaf.VerifyingPubkey)
		if !tweakedSet {
			tweaked = localTweaked
			tweakedSet = true
		} else if tweaked != localTweaked {
			return false, fmt.Errorf("inconsistent key tweak status for transfer %v", transfer.ID)
		}
	}
	return tweaked, nil
}

func (h *TransferHandler) SettleReceiverKeyTweak(ctx context.Context, req *pbinternal.SettleReceiverKeyTweakRequest) error {
	ctx, span := tracer.Start(ctx, "TransferHandler.SettleReceiverKeyTweak")
	defer span.End()
	transferID, err := uuid.Parse(req.GetTransferId())
	if err != nil {
		return fmt.Errorf("invalid transfer ID: %w", err)
	}
	transfer, err := h.loadTransferForUpdate(ctx, transferID)
	if err != nil {
		return fmt.Errorf("unable to load transfer %s: %w", transferID, err)
	}
	span.SetAttributes(transferTypeKey.String(string(transfer.Type)))

	switch transfer.Status {
	case st.TransferStatusReceiverKeyTweakApplied, st.TransferStatusCompleted, st.TransferStatusReceiverRefundSigned:
		// The receiver key tweak is already applied, return early.
		return nil
	case st.TransferStatusReceiverKeyTweakLocked, st.TransferStatusReceiverKeyTweaked:
		// Do nothing
	default:
		if req.Action == pbinternal.SettleKeyTweakAction_COMMIT {
			return fmt.Errorf("transfer %s is in an invalid status %s to settle receiver key tweak", transfer.ID, transfer.Status)
		}
	}
	switch req.Action {
	case pbinternal.SettleKeyTweakAction_COMMIT:
		leaves, err := transfer.QueryTransferLeaves().WithLeaf(func(tnq *ent.TreeNodeQuery) {
			tnq.WithTree().WithSigningKeyshare()
		}).All(ctx)
		if err != nil {
			return fmt.Errorf("unable to get leaves from transfer %s: %w", transferID, err)
		}

		db, err := ent.GetDbFromContext(ctx)
		if err != nil {
			return fmt.Errorf("unable to get db: %w", err)
		}

		// Track successful leaf IDs to clear key_tweak in a single batch.
		clearedIDs := make([]uuid.UUID, 0, len(leaves))
		builders := make([]*ent.TreeNodeCreate, 0, len(leaves))
		for _, leaf := range leaves {
			treeNode := leaf.Edges.Leaf
			if treeNode == nil {
				return fmt.Errorf("unable to get tree node for leaf %v: %w", leaf.ID, err)
			}
			if len(leaf.KeyTweak) == 0 {
				return fmt.Errorf("key tweak for leaf %v is not set", leaf.ID)
			}
			keyTweakProto := &pb.ClaimLeafKeyTweak{}
			if err := proto.Unmarshal(leaf.KeyTweak, keyTweakProto); err != nil {
				return fmt.Errorf("unable to unmarshal key tweak for leaf %v: %w", leaf.ID, err)
			}
			// claimLeafTweakKey now returns the key update instead of mutating the leaf
			keyUpdate, err := h.claimLeafTweakKey(ctx, treeNode, keyTweakProto, transfer.ReceiverIdentityPubkey)
			if err != nil {
				return fmt.Errorf("unable to claim leaf tweak key for leaf %v: %w", leaf.ID, err)
			}

			// Build upsert for batch update. Since records always exist (queried above),
			// OnConflict will always UPDATE, never INSERT. We set ID (for matching), all required fields, and the fields we want to update.
			builders = append(builders,
				db.TreeNode.Create().
					SetID(treeNode.ID).
					SetTree(treeNode.Edges.Tree).
					SetNetwork(treeNode.Edges.Tree.Network).
					SetSigningKeyshare(treeNode.Edges.SigningKeyshare).
					SetValue(treeNode.Value).
					SetVerifyingPubkey(treeNode.VerifyingPubkey).
					SetOwnerIdentityPubkey(keyUpdate.OwnerIdentityPubkey).
					SetOwnerSigningPubkey(keyUpdate.OwnerSigningPubkey).
					SetRawTx(treeNode.RawTx).
					SetVout(treeNode.Vout).
					SetStatus(treeNode.Status),
			)
			clearedIDs = append(clearedIDs, leaf.ID)
		}

		// Execute all TreeNode updates in batch to avoid N+1 queries.
		// We use CreateBulk with OnConflict as a workaround since Ent doesn't have native bulk UPDATE support.
		// Since all records exist (queried above), OnConflict will always UPDATE, never INSERT.
		// Batch in chunks to avoid PostgreSQL parameter limit (65535).
		const maxBatchSize = 1000
		for chunk := range slices.Chunk(builders, maxBatchSize) {
			err = db.TreeNode.CreateBulk(chunk...).
				OnConflictColumns(enttreenode.FieldID).
				Update(func(u *ent.TreeNodeUpsert) {
					u.UpdateOwnerIdentityPubkey()
					u.UpdateOwnerSigningPubkey()
				}).
				Exec(ctx)
			if err != nil {
				return fmt.Errorf("unable to batch update tree node keys: %w", err)
			}
		}
		if len(clearedIDs) > 0 {
			if _, err := db.TransferLeaf.Update().Where(enttransferleaf.IDIn(clearedIDs...)).ClearKeyTweak().Save(ctx); err != nil {
				return fmt.Errorf("unable to batch clear leaf key tweaks: %w", err)
			}
		}
		_, err = transfer.Update().SetStatus(st.TransferStatusReceiverKeyTweakApplied).Save(ctx)
		if err != nil {
			return fmt.Errorf("unable to update transfer status %v: %w", transferID, err)
		}
	case pbinternal.SettleKeyTweakAction_ROLLBACK:
		leaves, err := transfer.QueryTransferLeaves().All(ctx)
		if err != nil {
			return fmt.Errorf("unable to get leaves from transfer %s: %w", transferID, err)
		}
		if err := h.revertClaimTransfer(ctx, transfer, leaves); err != nil {
			return fmt.Errorf("unable to revert claim transfer %v: %w", transferID, err)
		}
	default:
		return fmt.Errorf("invalid action %s", req.Action)
	}

	entTx, err := ent.GetTxFromContext(ctx)
	if err != nil {
		return fmt.Errorf("unable to get db: %w", err)
	}
	if err := entTx.Commit(); err != nil {
		return fmt.Errorf("unable to commit db: %w", err)
	}
	return nil
}

// Complete sending of a valid transfer. This function moves the transfer
// to SenderKeyTweaked status, meaning it's fully submitted (awaiting recipient claim).
func (h *TransferHandler) ResumeSendTransfer(ctx context.Context, transfer *ent.Transfer) error {
	ctx, span := tracer.Start(ctx, "TransferHandler.ResumeSendTransfer")
	defer span.End()

	logger := logging.GetLoggerFromContext(ctx)

	switch transfer.Status {
	case st.TransferStatusSenderInitiatedCoordinator, st.TransferStatusApplyingSenderKeyTweak:
		// Acceptable status
	default:
		return nil
	}

	switch transfer.Type {
	case st.TransferTypePrimarySwapV3:
		// Disable retry settling key tweaks in `resume_send_transfer` cron task if the transfer is a primary transfer.
		return nil
	case st.TransferTypeCounterSwapV3:
		// Allow settling both primary and counter transfer key tweaks if the transfer is a counter transfer.
		message := pbgossip.GossipMessage{
			Message: &pbgossip.GossipMessage_SettleSwapKeyTweak{
				SettleSwapKeyTweak: &pbgossip.GossipMessageSettleSwapKeyTweak{
					CounterTransferId: transfer.ID.String(),
				},
			},
		}

		sendGossipHandler := NewSendGossipHandler(h.config)
		selection := helper.OperatorSelection{
			Option: helper.OperatorSelectionOptionExcludeSelf,
		}
		participants, err := selection.OperatorIdentifierList(h.config)
		if err != nil {
			return fmt.Errorf("unable to get operator list: %w", err)
		}
		_, err = sendGossipHandler.CreateCommitAndSendGossipMessage(ctx, &message, participants)
		if err != nil {
			logger.With(zap.Error(err)).Sugar().Errorf(
				"Failed to create and commit gossip message to retry settle swap v3 sender key tweaks for counter transfer %s",
				transfer.ID,
			)
			return nil
		}
	default:
		// All other transfers
		err := h.settleSenderKeyTweaks(ctx, transfer.ID, pbinternal.SettleKeyTweakAction_COMMIT)
		if err == nil {
			// If there's no error, it means all SOs have tweaked the key. The coordinator can tweak the key here.
			_, err = h.commitSenderKeyTweaks(ctx, transfer)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

// setSoCoordinatorKeyTweaks sets the key tweaks for each transfer leaf based on the validated transfer package.
func (h *TransferHandler) setSoCoordinatorKeyTweaks(ctx context.Context, transfer *ent.Transfer, req *pb.TransferPackage, ownerIdentityPubKey keys.Public) error {
	// Get key tweak map from transfer package
	keyTweakMap, err := h.ValidateTransferPackage(ctx, transfer.ID, req, ownerIdentityPubKey)
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

func updateSwapPrimaryTransferToStatus(ctx context.Context, counterTransfer *ent.Transfer, status st.TransferStatus) error {
	if counterTransfer == nil {
		return fmt.Errorf("counter transfer is nil")
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("unable to get db before updating transfer status: %w", err)
	}
	primaryTransfer, err := db.Transfer.QueryPrimarySwapTransfer(counterTransfer).Only(ctx)
	if err != nil {
		return fmt.Errorf("unable to load primary transfer: %w", err)
	}
	_, err = db.Transfer.UpdateOne(primaryTransfer).SetStatus(status).Save(ctx)
	if err != nil {
		return fmt.Errorf("unable to update primary transfer for counter transfer %s status to applying sender key tweak: %w", counterTransfer.ID, err)
	}
	return nil
}
