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
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/lib/pq"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/uuids"
	"github.com/lightsparkdev/spark/so/frost"
	"go.uber.org/zap"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	eciesgo "github.com/ecies/go/v2"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	bitcointransaction "github.com/lightsparkdev/spark/common/bitcoin_transaction"
	"github.com/lightsparkdev/spark/common/logging"
	secretsharing "github.com/lightsparkdev/spark/common/secret_sharing"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pbfrost "github.com/lightsparkdev/spark/proto/frost"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authz"
	sparkdb "github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/blockheight"
	"github.com/lightsparkdev/spark/so/ent/cooperativeexit"
	"github.com/lightsparkdev/spark/so/ent/pendingsendtransfer"
	"github.com/lightsparkdev/spark/so/ent/predicate"
	"github.com/lightsparkdev/spark/so/ent/preimagerequest"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	enttransfer "github.com/lightsparkdev/spark/so/ent/transfer"
	enttransferleaf "github.com/lightsparkdev/spark/so/ent/transferleaf"
	enttransferreceiver "github.com/lightsparkdev/spark/so/ent/transferreceiver"
	enttransfersender "github.com/lightsparkdev/spark/so/ent/transfersender"
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

// createPendingSendTransferAndCommit creates (or resets) a PendingSendTransfer
// record for the given transfer and commits the current database transaction.
func createPendingSendTransferAndCommit(ctx context.Context, transferID uuid.UUID) error {
	entTx, err := ent.GetTxFromContext(ctx)
	if err != nil {
		return fmt.Errorf("unable to get database transaction: %w", err)
	}
	if _, err = ent.CreateOrResetPendingSendTransfer(ctx, transferID); err != nil {
		return fmt.Errorf("unable to create pending send transfer: %w", err)
	}
	if err := entTx.Commit(); err != nil {
		return fmt.Errorf("unable to commit database transaction: %w", err)
	}
	return nil
}

// buildSigningResultProtos marshals per-leaf signing result maps into the proto
// response format used by StartTransfer and StartTransferV3.
func buildSigningResultProtos(
	leafMap map[string]*ent.TreeNode,
	cpfpSigningResultMap map[string]*helper.SigningResult,
	directSigningResultMap map[string]*helper.SigningResult,
	directFromCpfpSigningResultMap map[string]*helper.SigningResult,
) ([]*pb.LeafRefundTxSigningResult, error) {
	var results []*pb.LeafRefundTxSigningResult
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

		results = append(results, &pb.LeafRefundTxSigningResult{
			LeafId:                              leafID,
			RefundTxSigningResult:               cpfpProto,
			DirectRefundTxSigningResult:         directProto,
			DirectFromCpfpRefundTxSigningResult: directFromCpfpProto,
			VerifyingKey:                        leafMap[leafID].VerifyingPubkey.Serialize(),
		})
	}
	return results, nil
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

// rollbackTransferInit rolls back the current DB transaction, marks the
// PendingSendTransfer as finished, and optionally sends a cancel-transfer
// gossip message. Use cancelGossip=true when the transfer was already synced
// to other SOs (so they need to know it's cancelled); use false when
// createTransfer itself failed (nothing was synced yet).
func (h *TransferHandler) rollbackTransferInit(ctx context.Context, transferID uuid.UUID, cancelGossip bool) error {
	rollbackTx, err := ent.GetTxFromContext(ctx)
	if err != nil {
		return fmt.Errorf("unable to get database transaction: %w", err)
	}
	if err := rollbackTx.Rollback(); err != nil {
		return fmt.Errorf("unable to rollback database transaction: %w", err)
	}

	cleanupTx, err := ent.GetTxFromContext(ctx)
	if err != nil {
		return fmt.Errorf("unable to get database transaction for cleanup: %w", err)
	}
	dbClient := cleanupTx.Client()
	_, err = dbClient.PendingSendTransfer.Update().
		Where(pendingsendtransfer.TransferID(transferID)).
		SetStatus(st.PendingSendTransferStatusFinished).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("unable to update pending send transfer: %w", err)
	}

	if cancelGossip {
		if cancelErr := h.CreateCancelTransferGossipMessage(ctx, transferID); cancelErr != nil {
			logging.GetLoggerFromContext(ctx).With(zap.Error(cancelErr)).Sugar().Errorf(
				"Failed to create cancel transfer gossip message for transfer %s", transferID,
			)
		}
	}

	if err := cleanupTx.Commit(); err != nil {
		return fmt.Errorf("unable to commit cleanup transaction: %w", err)
	}
	return nil
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
func (h *TransferHandler) startTransferInternal(
	ctx context.Context,
	req *pb.StartTransferRequest,
	transferType st.TransferType,
	cpfpAdaptorPubKey keys.Public,
	directAdaptorPubKey keys.Public,
	directFromCpfpAdaptorPubKey keys.Public,
	requireDirectTx bool,
	swapV3Package *SwapV3Package,
) (resp *pb.StartTransferResponse, retErr error) {
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
	leafTweakMap, err := h.ValidateTransferPackage(ctx, transferID, req.TransferPackage, reqOwnerIdentityPubKey, !transferType.IsSwap())
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

	leafCpfpRefundMap, leafDirectRefundMap, leafDirectFromCpfpRefundMap := loadLeafRefundMaps(req)

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

	// Mutual exclusivity
	if err := createPendingSendTransferAndCommit(ctx, transferID); err != nil {
		return nil, err
	}

	// Rollback PendingSendTransfer on any failure between here and the success
	// point. cancelGossip is set to true before syncTransferInit so that a
	// sync failure also cancels the gossip messages sent to other SOs.
	needsRollback := true
	cancelGossip := false
	defer func() {
		if !needsRollback || retErr == nil {
			return
		}
		if rbErr := h.rollbackTransferInit(ctx, transferID, cancelGossip); rbErr != nil {
			retErr = fmt.Errorf("rollback failed: %w while processing transfer %s: %w", rbErr, transferID, retErr)
		}
	}()

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
		transferID,
		req.GetTransferPackage(),
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
		return nil, fmt.Errorf("failed to create transfer for transfer %s: %w", transferID, err)
	}

	// If the SSP matched the user's primary transfer with a counter transfer, lock it from cancellation.
	// If other SO fails to accept the key tweaks, this status will be rolled back.
	if transferType == st.TransferTypeCounterSwapV3 {
		err := updateSwapPrimaryTransferToStatus(ctx, transfer, st.TransferStatusApplyingSenderKeyTweak)
		if err != nil {
			return nil, fmt.Errorf("unable to update primary transfer for counter transfer %s status: %w ", req.TransferId, err)
		}
	}

	var signingResultProtos []*pb.LeafRefundTxSigningResult
	var finalCpfpSignatureMap map[string][]byte
	var finalDirectSignatureMap map[string][]byte
	var finalDirectFromCpfpSignatureMap map[string][]byte
	if req.TransferPackage == nil {
		signingResultProtos, err = signRefunds(ctx, h.config, req, leafMap, cpfpAdaptorPubKey, directAdaptorPubKey, directFromCpfpAdaptorPubKey, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to sign refunds for transfer %s: %w", transferID, err)
		}
	} else {
		refundSignatures, err := h.signAggregateAndUpdateRefunds(
			ctx, transfer, req.GetTransferId(), req.TransferPackage, leafMap,
			cpfpAdaptorPubKey, directAdaptorPubKey, directFromCpfpAdaptorPubKey, nil,
		)
		if err != nil {
			return nil, err
		}

		finalCpfpSignatureMap = refundSignatures.finalCpfpSignatureMap
		finalDirectSignatureMap = refundSignatures.finalDirectSignatureMap
		finalDirectFromCpfpSignatureMap = refundSignatures.finalDfcSignatureMap
		signingResultProtos, err = buildSigningResultProtos(
			leafMap, refundSignatures.cpfpSigningResultMap,
			refundSignatures.directSigningResultMap, refundSignatures.directFromCpfpSigningResultMap,
		)
		if err != nil {
			return nil, err
		}
	}

	// Send our version of the proof map when syncing the transfer with other SOs
	// so that they can validate it against the version they decrypt
	senderKeyTweakProofs := make(map[string]*pb.SecretProof)
	for _, leaf := range leafTweakMap {
		senderKeyTweakProofs[leaf.LeafId] = &pb.SecretProof{
			Proofs: leaf.SecretShareTweak.Proofs,
		}
	}

	// This call to other SOs will check the validity of the transfer package. If no error is
	// returned, it means the transfer package is valid and the transfer is considered sent.
	cancelGossip = true
	err = h.syncTransferInit(
		ctx,
		req,
		transferType,
		senderKeyTweakProofs,
		finalCpfpSignatureMap,
		finalDirectSignatureMap,
		finalDirectFromCpfpSignatureMap,
		cpfpAdaptorPubKey,
		directAdaptorPubKey,
		directFromCpfpAdaptorPubKey,
		swapV3Package,
	)
	if err != nil {
		logger.With(zap.Error(err)).Sugar().Errorf("Failed to sync transfer init for transfer %s", transferID)
		return nil, fmt.Errorf("failed to sync transfer init for transfer %s: %w", transferID, err)
	}

	// After this point, the transfer send is considered successful.
	needsRollback = false

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
			// Swap V3 requires both primary and counter transfer tweaks settled at the same time,
			// so there is a special handler for this case.
			// primaryTransferId is only passed in for swap v3.
			if transferType == st.TransferTypeCounterSwapV3 && primaryTransferId != uuid.Nil {
				message := &pbgossip.GossipMessage{
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
					return nil, fmt.Errorf("unable to get operator list: %w", err)
				}
				_, err = sendGossipHandler.CreateCommitAndSendGossipMessage(ctx, message, participants)
				if err != nil {
					return nil, fmt.Errorf("failed to settle swap key tweak for transfer %s: %w", transferID, err)
				}
			} else {
				if err := h.syncSettleSenderKeyTweaks(ctx, transfer.ID.String(), senderKeyTweakProofs); err != nil {
					return nil, err
				}
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

	return &pb.StartTransferResponse{Transfer: transferProto, SigningResults: signingResultProtos}, nil
}

// syncSettleSenderKeyTweaks builds a SettleSenderKeyTweak gossip message
// from the given tweak proof map and broadcasts it to all other operators.
func (h *TransferHandler) syncSettleSenderKeyTweaks(
	ctx context.Context,
	transferID string,
	keyTweakProofMap map[string]*pb.SecretProof,
) error {
	message := &pbgossip.GossipMessage{
		Message: &pbgossip.GossipMessage_SettleSenderKeyTweak{
			SettleSenderKeyTweak: &pbgossip.GossipMessageSettleSenderKeyTweak{
				TransferId:           transferID,
				SenderKeyTweakProofs: keyTweakProofMap,
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
	_, err = sendGossipHandler.CreateCommitAndSendGossipMessage(ctx, message, participants)
	if err != nil {
		return fmt.Errorf("failed to settle sender key tweaks for transfer %s: %w", transferID, err)
	}
	return nil
}

// refundSigningOutput holds the results of the sign-aggregate-update pipeline.
type refundSigningOutput struct {
	cpfpSigningResultMap           map[string]*helper.SigningResult
	directSigningResultMap         map[string]*helper.SigningResult
	directFromCpfpSigningResultMap map[string]*helper.SigningResult
	finalCpfpSignatureMap          map[string][]byte
	finalDirectSignatureMap        map[string][]byte
	finalDfcSignatureMap           map[string][]byte // direct-from-cpfp
}

// signAggregateAndUpdateRefunds runs the 3-step pipeline: sign refunds with
// pregenerated nonces, aggregate the partial signatures, and update the
// transfer leaves with the final signatures. connectorTx is passed through to
// both SignRefundsWithPregeneratedNonce and UpdateTransferLeavesSignatures (used
// by cooperative exits).
func (h *TransferHandler) signAggregateAndUpdateRefunds(
	ctx context.Context,
	transfer *ent.Transfer,
	transferID string,
	transferPackage *pb.TransferPackage,
	leafMap map[string]*ent.TreeNode,
	cpfpAdaptorPubKey, directAdaptorPubKey, directFromCpfpAdaptorPubKey keys.Public,
	connectorTx []byte,
) (*refundSigningOutput, error) {
	cpfpResults, directResults, directFromCpfpResults, err := SignRefundsWithPregeneratedNonce(
		ctx, h.config, transferID, transferPackage, leafMap,
		cpfpAdaptorPubKey, directAdaptorPubKey, directFromCpfpAdaptorPubKey,
		connectorTx,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to sign refunds with pregenerated nonce: %w", err)
	}

	finalCpfpSigMap, finalDirectSigMap, finalDirectFromCpfpSigMap, err := AggregateSignatures(
		ctx, h.config, transferID, transferPackage,
		cpfpAdaptorPubKey, directAdaptorPubKey, directFromCpfpAdaptorPubKey,
		cpfpResults, directResults, directFromCpfpResults, leafMap,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate signatures: %w", err)
	}

	if len(finalDirectSigMap) > 0 || len(finalDirectFromCpfpSigMap) > 0 {
		if err := h.UpdateTransferLeavesSignatures(ctx, transfer, finalCpfpSigMap, finalDirectSigMap, finalDirectFromCpfpSigMap, connectorTx); err != nil {
			return nil, fmt.Errorf("failed to update transfer leaves signatures: %w", err)
		}
	} else {
		if err := h.UpdateTransferLeavesSignaturesForRefundTxOnly(ctx, transfer, finalCpfpSigMap, cpfpAdaptorPubKey); err != nil {
			return nil, fmt.Errorf("failed to update CPFP transfer leaves signatures: %w", err)
		}
	}

	return &refundSigningOutput{
		cpfpSigningResultMap:           cpfpResults,
		directSigningResultMap:         directResults,
		directFromCpfpSigningResultMap: directFromCpfpResults,
		finalCpfpSignatureMap:          finalCpfpSigMap,
		finalDirectSignatureMap:        finalDirectSigMap,
		finalDfcSignatureMap:           finalDirectFromCpfpSigMap,
	}, nil
}

func (h *TransferHandler) UpdateTransferLeavesSignatures(ctx context.Context, transfer *ent.Transfer, cpfpSignatureMap map[string][]byte, directSignatureMap map[string][]byte, directFromCpfpSignatureMap map[string][]byte, connectorTx ...[]byte) error {
	transferLeaves, err := transfer.QueryTransferLeaves().WithLeaf().All(ctx)
	if err != nil {
		return fmt.Errorf("unable to get transfer leaves: %w", err)
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("unable to get db from context: %w", err)
	}

	// Parse connector tx if provided for multi-input verification (cooperative exit)
	var rawConnectorTx []byte
	if len(connectorTx) > 0 {
		rawConnectorTx = connectorTx[0]
	}
	connectorPrevOuts, err := parseConnectorTxOutputs(rawConnectorTx)
	if err != nil {
		return fmt.Errorf("unable to parse connector tx: %w", err)
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
		if len(updatedCpfpRefundTx.TxIn) > 1 && connectorPrevOuts != nil {
			prevOutFetcher := txscript.NewMultiPrevOutFetcher(nil)
			prevOutFetcher.AddPrevOut(updatedCpfpRefundTx.TxIn[0].PreviousOutPoint, nodeTx.TxOut[0])
			for _, txIn := range updatedCpfpRefundTx.TxIn[1:] {
				prevOut, ok := connectorPrevOuts[txIn.PreviousOutPoint]
				if !ok {
					return fmt.Errorf("missing connector prevout for cpfp refund tx input %s in leaf %s", txIn.PreviousOutPoint, leaf.Edges.Leaf.ID.String())
				}
				prevOutFetcher.AddPrevOut(txIn.PreviousOutPoint, prevOut)
			}
			err = common.VerifySignatureInput(updatedCpfpRefundTx, 0, prevOutFetcher)
		} else {
			err = common.VerifySignatureSingleInput(updatedCpfpRefundTx, 0, nodeTx.TxOut[0])
		}
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
			if len(updatedDirectFromCpfpRefundTx.TxIn) > 1 && connectorPrevOuts != nil {
				prevOutFetcher := txscript.NewMultiPrevOutFetcher(nil)
				prevOutFetcher.AddPrevOut(updatedDirectFromCpfpRefundTx.TxIn[0].PreviousOutPoint, nodeTx.TxOut[0])
				for _, txIn := range updatedDirectFromCpfpRefundTx.TxIn[1:] {
					prevOut, ok := connectorPrevOuts[txIn.PreviousOutPoint]
					if !ok {
						return fmt.Errorf("missing connector prevout for direct-from-cpfp refund tx input %s in leaf %s", txIn.PreviousOutPoint, leaf.Edges.Leaf.ID.String())
					}
					prevOutFetcher.AddPrevOut(txIn.PreviousOutPoint, prevOut)
				}
				err = common.VerifySignatureInput(updatedDirectFromCpfpRefundTx, 0, prevOutFetcher)
			} else {
				err = common.VerifySignatureSingleInput(updatedDirectFromCpfpRefundTx, 0, nodeTx.TxOut[0])
			}
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

			if len(updatedDirectRefundTx.TxIn) > 1 && connectorPrevOuts != nil {
				prevOutFetcher := txscript.NewMultiPrevOutFetcher(nil)
				prevOutFetcher.AddPrevOut(updatedDirectRefundTx.TxIn[0].PreviousOutPoint, directNodeTx.TxOut[0])
				for _, txIn := range updatedDirectRefundTx.TxIn[1:] {
					prevOut, ok := connectorPrevOuts[txIn.PreviousOutPoint]
					if !ok {
						return fmt.Errorf("missing connector prevout for direct refund tx input %s in leaf %s", txIn.PreviousOutPoint, leaf.Edges.Leaf.ID.String())
					}
					prevOutFetcher.AddPrevOut(txIn.PreviousOutPoint, prevOut)
				}
				err = common.VerifySignatureInput(updatedDirectRefundTx, 0, prevOutFetcher)
			} else {
				err = common.VerifySignatureSingleInput(updatedDirectRefundTx, 0, directNodeTx.TxOut[0])
			}
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

func (h *TransferHandler) StartTransferV3(ctx context.Context, req *pb.StartTransferV3Request) (*pb.StartTransferResponse, error) {
	return h.startTransferV3Internal(ctx, req)
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
	senderKeyTweakProofs map[string]*pb.SecretProof,
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
		SenderKeyTweakProofs:           senderKeyTweakProofs,
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
	connectorPrevOuts, err := parseConnectorTxOutputs(connectorTx)
	if err != nil {
		return nil, fmt.Errorf("unable to parse connector tx: %w", err)
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
		leaf, exists := leafMap[req.LeafId]
		if !exists {
			return nil, fmt.Errorf("leaf %s not found in leafMap", req.LeafId)
		}
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
	transferID string,
	pkg *pb.TransferPackage,
	leafMap map[string]*ent.TreeNode,
	cpfpAdaptorPubKey keys.Public,
	directAdaptorPubKey keys.Public,
	directFromCpfpAdaptorPubKey keys.Public,
	connectorTx []byte,
) (map[string]*helper.SigningResult, map[string]*helper.SigningResult, map[string]*helper.SigningResult, error) {
	ctx, span := tracer.Start(ctx, "TransferHandler.signRefunds")
	defer span.End()

	leafJobMap := make(map[uuid.UUID]*ent.TreeNode)
	jobIsDirectRefund := make(map[uuid.UUID]bool)
	jobIsDirectFromCpfpRefund := make(map[uuid.UUID]bool)

	if pkg == nil {
		return nil, nil, nil, fmt.Errorf("transfer package is nil")
	}

	// Parse connector tx if provided for multi-input sighash calculation (cooperative exit)
	connectorPrevOuts, err := parseConnectorTxOutputs(connectorTx)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("unable to parse connector tx: %w", err)
	}

	var signingJobs []*helper.SigningJobWithPregeneratedNonce
	for _, req := range pkg.LeavesToSend {
		leaf, exists := leafMap[req.LeafId]
		if !exists {
			return nil, nil, nil, fmt.Errorf("leaf %s not found in leafMap", req.LeafId)
		}
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

		var refundTxSigHash []byte
		if len(refundTx.TxIn) > 1 && connectorPrevOuts != nil {
			leafTxHash := leafTx.TxHash()
			prevOuts := make(map[wire.OutPoint]*wire.TxOut, 2)
			prevOuts[wire.OutPoint{Hash: leafTxHash, Index: 0}] = leafTx.TxOut[0]

			connectorOutpoint := refundTx.TxIn[1].PreviousOutPoint
			connectorTxOut, exists := connectorPrevOuts[connectorOutpoint]
			if !exists {
				return nil, nil, nil, fmt.Errorf("cpfp refund tx input 1 does not reference a valid connector output: %v", connectorOutpoint)
			}
			prevOuts[connectorOutpoint] = connectorTxOut

			refundTxSigHash, err = common.SigHashFromMultiPrevOutTx(refundTx, 0, prevOuts)
		} else {
			refundTxSigHash, err = common.SigHashFromTx(refundTx, 0, leafTx.TxOut[0])
		}
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
	for _, req := range pkg.DirectLeavesToSend {
		leaf, exists := leafMap[req.LeafId]
		if !exists {
			return nil, nil, nil, fmt.Errorf("leaf %s not found in leafMap", req.LeafId)
		}
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
		var directRefundTxSigHash []byte
		if len(directRefundTx.TxIn) > 1 && connectorPrevOuts != nil {
			directTxHash := directTx.TxHash()
			prevOuts := make(map[wire.OutPoint]*wire.TxOut, 2)
			prevOuts[wire.OutPoint{Hash: directTxHash, Index: 0}] = directTx.TxOut[0]

			connectorOutpoint := directRefundTx.TxIn[1].PreviousOutPoint
			connectorTxOut, exists := connectorPrevOuts[connectorOutpoint]
			if !exists {
				return nil, nil, nil, fmt.Errorf("direct refund tx input 1 does not reference a valid connector output: %v", connectorOutpoint)
			}
			prevOuts[connectorOutpoint] = connectorTxOut

			directRefundTxSigHash, err = common.SigHashFromMultiPrevOutTx(directRefundTx, 0, prevOuts)
		} else {
			directRefundTxSigHash, err = common.SigHashFromTx(directRefundTx, 0, directTx.TxOut[0])
		}
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
	for _, req := range pkg.DirectFromCpfpLeavesToSend {
		leaf, exists := leafMap[req.LeafId]
		if !exists {
			return nil, nil, nil, fmt.Errorf("leaf %s not found in leafMap", req.LeafId)
		}
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

		var directFromCpfpRefundTxSigHash []byte
		if len(directFromCpfpRefundTx.TxIn) > 1 && connectorPrevOuts != nil {
			leafTxHash := directFromCpfpLeafTx.TxHash()
			prevOuts := make(map[wire.OutPoint]*wire.TxOut, 2)
			prevOuts[wire.OutPoint{Hash: leafTxHash, Index: 0}] = directFromCpfpLeafTx.TxOut[0]

			connectorOutpoint := directFromCpfpRefundTx.TxIn[1].PreviousOutPoint
			connectorTxOut, exists := connectorPrevOuts[connectorOutpoint]
			if !exists {
				return nil, nil, nil, fmt.Errorf("direct-from-cpfp refund tx input 1 does not reference a valid connector output: %v", connectorOutpoint)
			}
			prevOuts[connectorOutpoint] = connectorTxOut

			directFromCpfpRefundTxSigHash, err = common.SigHashFromMultiPrevOutTx(directFromCpfpRefundTx, 0, prevOuts)
		} else {
			directFromCpfpRefundTxSigHash, err = common.SigHashFromTx(directFromCpfpRefundTx, 0, directFromCpfpLeafTx.TxOut[0])
		}
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
	transferID string,
	pkg *pb.TransferPackage,
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
	cpfpUserSignedRefunds := pkg.LeavesToSend
	directUserSignedRefunds := pkg.DirectLeavesToSend
	directFromCpfpUserSignedRefunds := pkg.DirectFromCpfpLeavesToSend

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
		leaf, exists := leafMap[leafID]
		if !exists {
			return nil, nil, nil, fmt.Errorf("leaf %s not found in leafMap", leafID)
		}
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
		leaf, exists := leafMap[leafID]
		if !exists {
			return nil, nil, nil, fmt.Errorf("leaf %s not found in leafMap", leafID)
		}
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
		leaf, exists := leafMap[leafID]
		if !exists {
			return nil, nil, nil, fmt.Errorf("leaf %s not found in leafMap", leafID)
		}
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
	var senderPubkey keys.Public
	if knobs.GetKnobsService(ctx).GetValue(knobs.KnobReadMIMODataModelTransferSend, 0) > 0 {
		senderPubkey, err = GetTransferSender(transfer)
		if err != nil {
			return nil, err
		}
	} else {
		senderPubkey = transfer.SenderIdentityPubkey
	}
	err = authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, senderPubkey)
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

// checkTransferAccessWithPubkeys checks if the viewer has read access to either the sender or receiver wallet.
// It updates the accessMap cache to avoid redundant database queries.
func (h *TransferHandler) checkTransferAccessWithPubkeys(
	ctx context.Context,
	transferID uuid.UUID,
	senderPubkey, receiverPubkey keys.Public,
	accessMap map[keys.Public]bool,
) (bool, error) {
	hasReadAccess, exists := accessMap[senderPubkey]
	if !exists {
		var err error
		hasReadAccess, err = NewWalletSettingHandler(h.config).HasReadAccessToWallet(ctx, senderPubkey)
		if err != nil {
			return false, fmt.Errorf("failed to check if viewer has read access to transfer %s: %w", transferID.String(), err)
		}
		accessMap[senderPubkey] = hasReadAccess
	}
	if hasReadAccess {
		return true, nil
	}

	hasReadAccess, exists = accessMap[receiverPubkey]
	if !exists {
		var err error
		hasReadAccess, err = NewWalletSettingHandler(h.config).HasReadAccessToWallet(ctx, receiverPubkey)
		if err != nil {
			return false, fmt.Errorf("failed to check if viewer has read access to transfer %s: %w", transferID.String(), err)
		}
		accessMap[receiverPubkey] = hasReadAccess
	}
	return hasReadAccess, nil
}

// checkTransferAccessLegacy checks if the viewer has read access using the transfer's legacy sender/receiver identity fields.
func (h *TransferHandler) checkTransferAccessLegacy(
	ctx context.Context,
	transfer *ent.Transfer,
	accessMap map[keys.Public]bool,
) (bool, error) {
	return h.checkTransferAccessWithPubkeys(ctx, transfer.ID, transfer.SenderIdentityPubkey, transfer.ReceiverIdentityPubkey, accessMap)
}

// checkTransferAccessMIMO checks if the viewer has read access using the transfer's edges (transfer must be loaded with WithTransferSenders/WithTransferReceivers).
func (h *TransferHandler) checkTransferAccessMIMO(
	ctx context.Context,
	transfer *ent.Transfer,
	accessMap map[keys.Public]bool,
) (bool, error) {
	senderPubkey, receiverPubkey, err := GetTransferSenderReceiver(transfer)
	if err != nil {
		return false, err
	}
	return h.checkTransferAccessWithPubkeys(ctx, transfer.ID, senderPubkey, receiverPubkey, accessMap)
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

	if filter.GetNetwork() == pb.Network_UNSPECIFIED {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("filter.Network must be specified"))
	}
	network, err := btcnetwork.FromProtoNetwork(filter.GetNetwork())
	if err != nil {
		return nil, fmt.Errorf("failed to convert proto network to schema network: %w", err)
	}
	useMIMO := knobs.GetKnobsService(ctx).GetValue(knobs.KnobReadMIMODataModelQueryTransfers, 0) > 0

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
		if useMIMO {
			transferIDs, err := db.TransferReceiver.Query().
				Where(enttransferreceiver.IdentityPubkeyEQ(receiverIDPubKey)).
				QueryTransfer().
				Unique(false).
				Order(ent.Desc(enttransfer.FieldCreateTime)).
				Limit(maxMIMOTransferIDs).
				IDs(ctx)
			if err != nil {
				return nil, fmt.Errorf("failed to query receiver transfer IDs: %w", err)
			}
			if len(transferIDs) == 0 {
				return &pb.QueryTransfersResponse{Offset: -1}, nil
			}
			transferPredicate = append(transferPredicate, enttransfer.IDIn(transferIDs...))
		} else {
			transferPredicate = append(transferPredicate, enttransfer.ReceiverIdentityPubkeyEQ(receiverIDPubKey))
		}
		if isPending {
			transferPredicate = append(transferPredicate, enttransfer.StatusIn(receiverPendingStatuses...))
		}
		walletIdentityPubkey = &receiverIDPubKey
	case *pb.TransferFilter_SenderIdentityPublicKey:
		senderIDPubKey, err := keys.ParsePublicKey(filter.GetSenderIdentityPublicKey())
		if err != nil {
			return nil, fmt.Errorf("invalid sender identity public key: %w", err)
		}
		if useMIMO {
			transferIDs, err := db.TransferSender.Query().
				Where(enttransfersender.IdentityPubkeyEQ(senderIDPubKey)).
				QueryTransfer().
				Unique(false).
				Order(ent.Desc(enttransfer.FieldCreateTime)).
				Limit(maxMIMOTransferIDs).
				IDs(ctx)
			if err != nil {
				return nil, fmt.Errorf("failed to query sender transfer IDs: %w", err)
			}
			if len(transferIDs) == 0 {
				return &pb.QueryTransfersResponse{Offset: -1}, nil
			}
			transferPredicate = append(transferPredicate, enttransfer.IDIn(transferIDs...))
		} else {
			transferPredicate = append(transferPredicate, enttransfer.SenderIdentityPubkeyEQ(senderIDPubKey))
		}
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
		if useMIMO {
			// For MIMO, query TransferSender/TransferReceiver directly to get
			// transfer IDs. This avoids the slow OR + EXISTS pattern that causes
			// full table scans when querying from the transfers table.
			receiverTransferIDs, err := db.TransferReceiver.Query().
				Where(enttransferreceiver.IdentityPubkeyEQ(identityPubKey)).
				QueryTransfer().
				Unique(false).
				Order(ent.Desc(enttransfer.FieldCreateTime)).
				Limit(maxMIMOTransferIDs).
				IDs(ctx)
			if err != nil {
				return nil, fmt.Errorf("failed to query receiver transfer IDs: %w", err)
			}
			senderTransferIDs, err := db.TransferSender.Query().
				Where(enttransfersender.IdentityPubkeyEQ(identityPubKey)).
				QueryTransfer().
				Unique(false).
				Order(ent.Desc(enttransfer.FieldCreateTime)).
				Limit(maxMIMOTransferIDs).
				IDs(ctx)
			if err != nil {
				return nil, fmt.Errorf("failed to query sender transfer IDs: %w", err)
			}

			if len(receiverTransferIDs) == 0 && len(senderTransferIDs) == 0 {
				return &pb.QueryTransfersResponse{Offset: -1}, nil
			}

			if isPending {
				// Keep IDs separate to preserve role-based status filtering.
				// Guard each arm against empty ID slices — a wallet may have
				// only sent or only received transfers.
				var pendingParts []predicate.Transfer
				if len(receiverTransferIDs) > 0 {
					pendingParts = append(pendingParts, enttransfer.And(
						enttransfer.IDIn(receiverTransferIDs...),
						enttransfer.StatusIn(receiverPendingStatuses...),
					))
				}
				if len(senderTransferIDs) > 0 {
					pendingParts = append(pendingParts, enttransfer.And(
						enttransfer.IDIn(senderTransferIDs...),
						enttransfer.StatusIn(senderPendingStatuses...),
						enttransfer.ExpiryTimeLT(time.Now()),
					))
				}
				if len(pendingParts) == 0 {
					return &pb.QueryTransfersResponse{Offset: -1}, nil
				}
				transferPredicate = append(transferPredicate, enttransfer.Or(pendingParts...))
			} else {
				// Deduplicate transfer IDs for the non-pending path.
				seen := make(map[uuid.UUID]struct{}, len(receiverTransferIDs)+len(senderTransferIDs))
				allIDs := make([]uuid.UUID, 0, len(receiverTransferIDs)+len(senderTransferIDs))
				for _, id := range receiverTransferIDs {
					if _, ok := seen[id]; !ok {
						seen[id] = struct{}{}
						allIDs = append(allIDs, id)
					}
				}
				for _, id := range senderTransferIDs {
					if _, ok := seen[id]; !ok {
						seen[id] = struct{}{}
						allIDs = append(allIDs, id)
					}
				}
				if len(allIDs) > maxMIMOTransferIDs {
					allIDs = allIDs[:maxMIMOTransferIDs]
				}
				transferPredicate = append(transferPredicate, enttransfer.IDIn(allIDs...))
			}
		} else {
			receiverMatchesIdentity := enttransfer.ReceiverIdentityPubkeyEQ(identityPubKey)
			senderMatchesIdentity := enttransfer.SenderIdentityPubkeyEQ(identityPubKey)
			if isPending {
				transferPredicate = append(transferPredicate, enttransfer.Or(
					enttransfer.And(receiverMatchesIdentity, enttransfer.StatusIn(receiverPendingStatuses...)),
					enttransfer.And(senderMatchesIdentity, enttransfer.StatusIn(senderPendingStatuses...), enttransfer.ExpiryTimeLT(time.Now())),
				))
			} else {
				transferPredicate = append(transferPredicate, enttransfer.Or(receiverMatchesIdentity, senderMatchesIdentity))
			}
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
	if useMIMO {
		baseQuery = baseQuery.WithTransferSenders().WithTransferReceivers()
	}
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
			var hasReadAccess bool
			if useMIMO {
				hasReadAccess, err = h.checkTransferAccessMIMO(ctx, transfer, accessMap)
			} else {
				hasReadAccess, err = h.checkTransferAccessLegacy(ctx, transfer, accessMap)
			}
			if err != nil {
				return nil, err
			}
			if !hasReadAccess {
				continue
			}
		}

		var transferProto *pb.Transfer
		if useMIMO && walletIdentityPubkey != nil && transfer.HasReceiver(*walletIdentityPubkey) {
			transferProto, err = transfer.MarshalProtoForReceiver(ctx, *walletIdentityPubkey)
		} else {
			transferProto, err = transfer.MarshalProto(ctx)
		}
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
	useMIMO := knobs.GetKnobsService(ctx).GetValue(knobs.KnobReadMIMODataModelQueryTransfers, 0) > 0

	swapQuery := db.Transfer.Query().
		Where(enttransfer.And(
			enttransfer.TypeIn(st.TransferTypeSwap, st.TransferTypePrimarySwapV3),
			enttransfer.NetworkEQ(network),
		)).
		WithTransferSenders().
		WithTransferReceivers()
	if useMIMO {
		swapQuery = swapQuery.Where(enttransfer.HasTransferSendersWith(enttransfersender.IdentityPubkeyEQ(walletIdentityPubkey)))
	} else {
		swapQuery = swapQuery.Where(enttransfer.SenderIdentityPubkeyEQ(walletIdentityPubkey))
	}
	swap, err := swapQuery.Order(ent.Desc(enttransfer.FieldCreateTime)).First(ctx)

	if err != nil || swap == nil {
		logger := logging.GetLoggerFromContext(ctx)
		logger.Sugar().Warnf("failed to find swap for wallet %s: %v", walletIdentityPubkey.String(), err)
		// Don't want to fail the entire query if we can't find a swap or error here
		return nil
	}

	// include if !(sender is SSP and type is transfer)
	// i.e. exclude SSP counter-swap transfers
	if useMIMO {
		_, swapReceiverPubkey, err := GetTransferSenderReceiver(swap)
		if err != nil {
			logger := logging.GetLoggerFromContext(ctx)
			logger.Sugar().Warnf("failed to get swap receiver for wallet %s: %v", walletIdentityPubkey.String(), err)
			return nil
		}
		return enttransfer.Not(
			enttransfer.And(
				enttransfer.HasTransferSendersWith(enttransfersender.IdentityPubkeyEQ(swapReceiverPubkey)),
				enttransfer.TypeEQ(st.TransferTypeTransfer),
			),
		)
	}
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

// maxMIMOTransferIDs caps the number of transfer IDs fetched from
// TransferSender/TransferReceiver to stay within PostgreSQL's bind
// parameter limit (65,535). With other predicates also consuming
// parameters, 50,000 provides safe headroom.
const maxMIMOTransferIDs = 50_000

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

	// This guarantees that the transfer has only one receiver and logic changes to filter leaves, etc
	// are not necessary for this endpoint. We only dual-write the status changes to the receiver object for consistency.
	receiver, err := h.loadSingleTransferReceiverForUnsupportedMimoPath(ctx, transfer)
	if err != nil {
		return err
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

	// Store key tweaks - batch all updates into a single SQL statement
	leafIDs := make([]uuid.UUID, 0, len(req.LeavesToReceive))
	keyTweakValues := make([][]byte, 0, len(req.LeavesToReceive))
	for _, leafTweak := range req.LeavesToReceive {
		leaf, exists := leafMap[leafTweak.LeafId]
		if !exists {
			return fmt.Errorf("unexpected leaf id %s", leafTweak.LeafId)
		}
		leafTweakBytes, err := proto.Marshal(leafTweak)
		if err != nil {
			return fmt.Errorf("unable to marshal leaf tweak: %w", err)
		}
		leafIDs = append(leafIDs, leaf.ID)
		keyTweakValues = append(keyTweakValues, leafTweakBytes)
	}
	if len(leafIDs) > 0 {
		//nolint:forbidigo // Batch update with per-row values using unnest cannot be expressed with ent query builders.
		_, err = db.ExecContext(ctx, `
			UPDATE transfer_leafs
			SET key_tweak = data.key_tweak, update_time = NOW()
			FROM (SELECT unnest($1::uuid[]) AS id, unnest($2::bytea[]) AS key_tweak) AS data
			WHERE transfer_leafs.id = data.id
		`, pq.Array(leafIDs), pq.Array(keyTweakValues))
		if err != nil {
			return fmt.Errorf("unable to batch update key tweaks: %w", err)
		}
		ent.MarkTxDirty(ctx)
	}

	// MIMO - Dual write status changes
	_, err = transfer.Update().SetStatus(st.TransferStatusReceiverKeyTweaked).Save(ctx)
	if err != nil {
		return fmt.Errorf("unable to update transfer status %v: %w", transfer.ID, err)
	}
	if receiver != nil {
		_, err = receiver.Update().SetStatus(st.TransferReceiverStatusKeyTweaked).Save(ctx)
		if err != nil {
			return fmt.Errorf("unable to update transfer receiver status %v: %w", receiver.ID, err)
		}
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
	_, span := tracer.Start(ctx, "TransferHandler.ValidateKeyTweakProof")
	defer span.End()

	if len(transferLeaves) != len(keyTweakProofs) {
		return fmt.Errorf("transfer has %d leaves but %d key tweak proofs provided", len(transferLeaves), len(keyTweakProofs))
	}

	for _, leaf := range transferLeaves {
		treeNode := leaf.Edges.Leaf
		if treeNode == nil {
			return fmt.Errorf("tree node edge not loaded for transfer leaf %s: ensure WithLeaf() is used when querying", leaf.ID.String())
		}
		proof, exists := keyTweakProofs[treeNode.ID.String()]
		if !exists {
			return fmt.Errorf("key tweak proof for leaf %s not found", leaf.ID.String())
		}
		keyTweakProto := &pb.ClaimLeafKeyTweak{}
		err := proto.Unmarshal(leaf.KeyTweak, keyTweakProto)
		if err != nil {
			return fmt.Errorf("unable to unmarshal key tweak for leaf %s: %w", leaf.ID.String(), err)
		}
		if keyTweakProto.SecretShareTweak == nil {
			return fmt.Errorf("missing secret share tweak for leaf %s", leaf.ID.String())
		}
		if len(keyTweakProto.SecretShareTweak.Proofs) != len(proof.Proofs) {
			return fmt.Errorf("leaf %s has %d proofs but %d were provided", leaf.ID.String(), len(keyTweakProto.SecretShareTweak.Proofs), len(proof.Proofs))
		}
		for i, p := range proof.Proofs {
			if !bytes.Equal(keyTweakProto.SecretShareTweak.Proofs[i], p) {
				return sparkerrors.AbortedConcurrentClaimConflict(fmt.Errorf("key tweak proof for leaf %s is invalid, the proof provided is not the same as key tweak proof. please check your implementation to see if you are claiming the same transfer multiple times at the same time", leaf.ID.String()))
			}
		}
	}
	return nil
}

func (h *TransferHandler) revertClaimTransfer(ctx context.Context, transfer *ent.Transfer, receiver *ent.TransferReceiver, transferLeaves []*ent.TransferLeaf) error {
	ctx, span := tracer.Start(ctx, "TransferHandler.revertClaimTransfer", trace.WithAttributes(
		transferTypeKey.String(string(transfer.Type)),
	))
	defer span.End()

	if isMimoReceiveEnabled(ctx, receiver) {
		switch receiver.Status {
		case st.TransferReceiverStatusKeyTweakApplied,
			st.TransferReceiverStatusRefundSigned,
			st.TransferReceiverStatusCompleted:
			return fmt.Errorf("transfer %s key tweak is already applied, cannot revert it", transfer.ID.String())
		case st.TransferReceiverStatusKeyTweakLocked,
			st.TransferReceiverStatusKeyTweaked:
			// ok to revert
		default:
			return nil
		}
	} else {
		switch transfer.Status {
		case st.TransferStatusReceiverKeyTweakApplied,
			st.TransferStatusCompleted,
			st.TransferStatusReturned,
			st.TransferStatusReceiverRefundSigned:
			return fmt.Errorf("transfer %s key tweak is already applied, cannot revert it", transfer.ID.String())
		case st.TransferStatusReceiverKeyTweakLocked,
			st.TransferStatusReceiverKeyTweaked:
			// ok to revert
		default:
			return nil
		}
	}

	// Revert transfer status to sender key tweaked and transfer receiver to SenderInitiated
	// so the receiver can try to claim again
	// MIMO - Dual write status changes
	_, err := transfer.Update().SetStatus(st.TransferStatusSenderKeyTweaked).Save(ctx)
	if err != nil {
		return fmt.Errorf("unable to update transfer status %v: %w", transfer.ID, err)
	}
	if receiver != nil {
		_, err = receiver.Update().SetStatus(st.TransferReceiverStatusSenderInitiated).Save(ctx)
		if err != nil {
			return fmt.Errorf("unable to update transfer receiver status %v: %w", receiver.ID, err)
		}
	}

	// Revert key tweaks for all leaves
	for _, leaf := range transferLeaves {
		_, err := leaf.Update().SetKeyTweak(nil).Save(ctx)
		if err != nil {
			return fmt.Errorf("unable to update leaf %v: %w", leaf.ID, err)
		}
	}
	return nil
}

func (h *TransferHandler) settleReceiverKeyTweak(ctx context.Context, transfer *ent.Transfer, receiver *ent.TransferReceiver, keyTweakProofs map[string]*pb.SecretProof, userPublicKeys map[string][]byte) error {
	return h.settleReceiverKeyTweakInternal(ctx, transfer, receiver, keyTweakProofs, userPublicKeys, nil, nil)
}

// settleReceiverKeyTweakWithClaimPackage is like settleReceiverKeyTweak but also delivers
// ECIES-encrypted key tweaks to each SO as part of the two-phase commit.
// encryptedKeyTweakPackage is the full map (SO identifier -> ciphertext) and claimSignature
// is the user signature over the package. Both are forwarded so each SO can verify independently.
func (h *TransferHandler) settleReceiverKeyTweakWithClaimPackage(ctx context.Context, transfer *ent.Transfer, receiver *ent.TransferReceiver, keyTweakProofs map[string]*pb.SecretProof, userPublicKeys map[string][]byte, encryptedKeyTweakPackage map[string][]byte, claimSignature []byte) error {
	return h.settleReceiverKeyTweakInternal(ctx, transfer, receiver, keyTweakProofs, userPublicKeys, encryptedKeyTweakPackage, claimSignature)
}

func (h *TransferHandler) settleReceiverKeyTweakInternal(ctx context.Context, transfer *ent.Transfer, receiver *ent.TransferReceiver, keyTweakProofs map[string]*pb.SecretProof, userPublicKeys map[string][]byte, encryptedKeyTweakPackage map[string][]byte, claimSignature []byte) error {
	ctx, span := tracer.Start(ctx, "TransferHandler.settleReceiverKeyTweak", trace.WithAttributes(
		transferTypeKey.String(string(transfer.Type)),
	))
	defer span.End()

	// Send the receiver identity public key IFF MIMO receive is enabled
	var receiverIdentityPublicKeyBytes []byte
	if isMimoReceiveEnabled(ctx, receiver) {
		receiverIdentityPublicKeyBytes = receiver.IdentityPubkey.Serialize()
	}

	// Phase 1: PREPARE - Distribute the receiver's key tweak request to all SOs
	action := pbinternal.SettleKeyTweakAction_COMMIT
	selection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}
	_, err := helper.ExecuteTaskWithAllOperators(ctx, h.config, &selection, func(ctx context.Context, operator *so.SigningOperator) (any, error) {
		conn, err := operator.NewOperatorGRPCConnection()
		if err != nil {
			return nil, err
		}
		defer conn.Close()
		client := pbinternal.NewSparkInternalServiceClient(conn)
		req := &pbinternal.InitiateSettleReceiverKeyTweakRequest{
			TransferId:                transfer.ID.String(),
			KeyTweakProofs:            keyTweakProofs,
			UserPublicKeys:            userPublicKeys,
			ReceiverIdentityPublicKey: receiverIdentityPublicKeyBytes,
		}
		if encryptedKeyTweakPackage != nil {
			req.EncryptedClaimKeyTweakPackage = encryptedKeyTweakPackage
			req.ClaimSignature = claimSignature
		}
		return client.InitiateSettleReceiverKeyTweak(ctx, req)
	})
	logger := logging.GetLoggerFromContext(ctx)
	var rollbackCause error
	if err != nil {
		if status.Code(err) == codes.Unavailable ||
			status.Code(err) == codes.Canceled ||
			strings.Contains(err.Error(), "context canceled") ||
			strings.Contains(err.Error(), "unexpected HTTP status code") ||
			sparkdb.IsRetriableSQLStateError(err) {
			logger.Sugar().Error("Unable to settle receiver key tweak due to operator unavailability, please try again later", zap.Error(err))
			return fmt.Errorf("unable to settle receiver key tweak due to operator unavailability: %w, please try again later", err)
		}
		logger.Error("Unable to settle receiver key tweak, you might have a race condition in your implementation", zap.Error(err))
		action = pbinternal.SettleKeyTweakAction_ROLLBACK
		rollbackCause = err
	}

	initiateReq := &pbinternal.InitiateSettleReceiverKeyTweakRequest{
		TransferId:                transfer.ID.String(),
		KeyTweakProofs:            keyTweakProofs,
		UserPublicKeys:            userPublicKeys,
		ReceiverIdentityPublicKey: receiverIdentityPublicKeyBytes,
	}
	if encryptedKeyTweakPackage != nil {
		initiateReq.EncryptedClaimKeyTweakPackage = encryptedKeyTweakPackage
		initiateReq.ClaimSignature = claimSignature
	}
	err = h.InitiateSettleReceiverKeyTweak(ctx, initiateReq)
	if err != nil {
		logger.Error("Unable to settle receiver key tweak internally, you might have a race condition in your implementation", zap.Error(err))
		action = pbinternal.SettleKeyTweakAction_ROLLBACK
		rollbackCause = err
	}

	// Phase 2: COMMIT - Settle the receiver's key tweak request to all SOs
	_, err = helper.ExecuteTaskWithAllOperators(ctx, h.config, &selection, func(ctx context.Context, operator *so.SigningOperator) (any, error) {
		conn, err := operator.NewOperatorGRPCConnection()
		if err != nil {
			return nil, err
		}
		defer conn.Close()
		client := pbinternal.NewSparkInternalServiceClient(conn)
		return client.SettleReceiverKeyTweak(ctx, &pbinternal.SettleReceiverKeyTweakRequest{
			TransferId:                transfer.ID.String(),
			Action:                    action,
			ReceiverIdentityPublicKey: receiverIdentityPublicKeyBytes,
		})
	})
	if err != nil {
		// At this point, this is not recoverable. But this should not happen in theory.
		return fmt.Errorf("unable to settle receiver key tweak: %w", err)
	} else {
		err = h.SettleReceiverKeyTweak(ctx, &pbinternal.SettleReceiverKeyTweakRequest{
			TransferId:                transfer.ID.String(),
			Action:                    action,
			ReceiverIdentityPublicKey: receiverIdentityPublicKeyBytes,
		})
		if err != nil {
			return fmt.Errorf("unable to settle receiver key tweak: %w", err)
		}
	}
	if action == pbinternal.SettleKeyTweakAction_ROLLBACK {
		return fmt.Errorf("unable to settle receiver key tweak; rolled back: %w", rollbackCause)
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

// assert that the claim package contains a valid signature over the contained key tweak package
func verifyClaimPackageSignature(transferID uuid.UUID, claimPackage *pb.ClaimPackage, reqOwnerIDPubKey keys.Public) error {
	if claimPackage.HashVariant != pb.HashVariant_HASH_VARIANT_V2 {
		return fmt.Errorf("claim package must use HASH_VARIANT_V2, got %s", claimPackage.HashVariant)
	}
	if len(claimPackage.UserSignature) == 0 {
		return fmt.Errorf("claim package user_signature is required")
	}
	signingPayload := common.GetClaimPackageSigningPayload(transferID, claimPackage.KeyTweakPackage)
	if err := common.VerifyECDSASignature(reqOwnerIDPubKey, claimPackage.UserSignature, signingPayload); err != nil {
		return fmt.Errorf("unable to verify claim package signature: %w", err)
	}
	return nil
}

// MIMO receive is enabled IFF the knob is enabled and there is a corresponding receiver.
func isMimoReceiveEnabled(ctx context.Context, receiver *ent.TransferReceiver) bool {
	return receiver != nil && knobs.GetKnobsService(ctx).GetValue(knobs.KnobMimoTransferMultiReceiverEnabled, 0) > 0
}

// buildFinalizeGossipMessage constructs the gossip message for transfer finalization.
// MIMO-enabled transfers use a per-receiver message; legacy transfers use a transfer-level message.
func buildFinalizeGossipMessage(
	mimoEnabled bool,
	transferID uuid.UUID,
	receiver *ent.TransferReceiver,
	internalNodes []*pbinternal.TreeNode,
	completionTimestamp *timestamppb.Timestamp,
) *pbgossip.GossipMessage {
	if mimoEnabled {
		return &pbgossip.GossipMessage{
			Message: &pbgossip.GossipMessage_FinalizeTransferReceiver{
				FinalizeTransferReceiver: &pbgossip.GossipMessageFinalizeTransferReceiver{
					TransferId:                transferID.String(),
					ReceiverIdentityPublicKey: receiver.IdentityPubkey.Serialize(),
					InternalNodes:             internalNodes,
					CompletionTimestamp:       completionTimestamp,
				},
			},
		}
	}
	return &pbgossip.GossipMessage{
		Message: &pbgossip.GossipMessage_FinalizeTransfer{
			FinalizeTransfer: &pbgossip.GossipMessageFinalizeTransfer{
				TransferId:          transferID.String(),
				InternalNodes:       internalNodes,
				CompletionTimestamp: completionTimestamp,
			},
		},
	}
}

// Create a query to fetch all the leaves for the current transfer; scoped to the receiver if one is provided.
func getTransferLeavesForReceiverQuery(ctx context.Context, transfer *ent.Transfer, receiver *ent.TransferReceiver) *ent.TransferLeafQuery {
	transferLeavesQuery := transfer.QueryTransferLeaves()
	if isMimoReceiveEnabled(ctx, receiver) {
		transferLeavesQuery = transferLeavesQuery.Where(enttransferleaf.TransferReceiverID(receiver.ID))
	}
	return transferLeavesQuery
}

// ClaimTransferSignRefundsV2 signs new refund transactions as part of the transfer.
func (h *TransferHandler) ClaimTransferSignRefundsV2(ctx context.Context, req *pb.ClaimTransferSignRefundsRequest) (*pb.ClaimTransferSignRefundsResponse, error) {
	return h.claimTransferSignRefunds(ctx, req, true)
}

// validateTransferReadyForReceiverClaim checks that the transfer has progressed past
// sender-side processing and is not in a terminal state. The transfer must be at
// SENDER_KEY_TWEAKED or later for any receiver to begin claiming.
func validateTransferReadyForReceiverClaim(transfer *ent.Transfer) error {
	switch transfer.Status {
	case st.TransferStatusSenderInitiated,
		st.TransferStatusSenderInitiatedCoordinator,
		st.TransferStatusSenderKeyTweakPending,
		st.TransferStatusApplyingSenderKeyTweak:
		return sparkerrors.FailedPreconditionInvalidState(
			fmt.Errorf("transfer %s is not ready for receiver claim, sender-side status: %s",
				transfer.ID, transfer.Status))
	case st.TransferStatusExpired, st.TransferStatusReturned:
		return sparkerrors.FailedPreconditionInvalidState(
			fmt.Errorf("transfer %s is in terminal state %s", transfer.ID, transfer.Status))
	default:
		return nil
	}
}

// ClaimTransfer claims a transfer in a single call. It combines key tweak delivery,
// refund signing, signature aggregation, and finalization.
func (h *TransferHandler) ClaimTransfer(ctx context.Context, req *pb.ClaimTransferRequest) (*pb.ClaimTransferResponse, error) {
	ctx, span := tracer.Start(ctx, "TransferHandler.ClaimTransfer")
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

	claimPackage := req.ClaimPackage
	if claimPackage == nil {
		return nil, fmt.Errorf("claim_package is required")
	}

	transfer, err := h.loadTransferForUpdate(ctx, transferID, sql.WithLockAction(sql.NoWait))
	if err != nil {
		if sparkdb.IsLockNotAvailableError(err) {
			return nil, sparkerrors.AbortedConcurrentClaimConflict(fmt.Errorf("unable to load transfer %s: %w", transferID, err))
		}
		return nil, fmt.Errorf("unable to load transfer %s: %w", transferID, err)
	}
	span.SetAttributes(transferTypeKey.String(string(transfer.Type)))

	// find the transfer receiver associated with this request, if there is one
	isMimoReceiveEnabled, receiver, err := h.loadTransferReceiverByPublicKeyForUpdate(ctx, transfer, &reqOwnerIDPubKey)
	if err != nil {
		return nil, err
	}

	// If MIMO receive is enabled, the receiver is guaranteed to match the request owner identity public key.
	if !isMimoReceiveEnabled {
		if !transfer.ReceiverIdentityPubkey.Equals(reqOwnerIDPubKey) {
			return nil, fmt.Errorf("cannot claim transfer %s, receiver identity public key mismatch", transferID)
		}
	}

	// Read model determined by MIMO state
	if isMimoReceiveEnabled {
		if err := validateTransferReadyForReceiverClaim(transfer); err != nil {
			return nil, err
		}
		switch receiver.Status {
		case st.TransferReceiverStatusSenderInitiated:
		case st.TransferReceiverStatusKeyTweaked:
		case st.TransferReceiverStatusKeyTweakLocked:
		case st.TransferReceiverStatusKeyTweakApplied:
		case st.TransferReceiverStatusRefundSigned:
			// ok
		case st.TransferReceiverStatusCompleted:
			return nil, sparkerrors.AlreadyExistsDuplicateOperation(fmt.Errorf("transfer %s has already been claimed by this receiver", transferID))
		default:
			return nil, fmt.Errorf("transfer %s receiver is not in a claimable status, current status: %s", transferID, receiver.Status)
		}
	} else {
		switch transfer.Status {
		case st.TransferStatusSenderKeyTweaked:
		case st.TransferStatusReceiverKeyTweaked:
		case st.TransferStatusReceiverRefundSigned:
		case st.TransferStatusReceiverKeyTweakLocked:
		case st.TransferStatusReceiverKeyTweakApplied:
			// ok
		case st.TransferStatusCompleted:
			return nil, sparkerrors.AlreadyExistsDuplicateOperation(fmt.Errorf("transfer %s has already been claimed", transferID))
		case st.TransferStatusExpired, st.TransferStatusReturned:
			return nil, sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("transfer %s is in terminal state %s and cannot be claimed", transferID, transfer.Status))
		default:
			return nil, sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("transfer %s is not in a claimable status, current status: %s", transferID, transfer.Status))
		}
	}

	leavesToTransfer, err := getTransferLeavesForReceiverQuery(ctx, transfer, receiver).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to load transfer leaves for transfer %s: %w", transferID, err)
	}
	if len(leavesToTransfer) != len(claimPackage.LeavesToClaim) {
		return nil, fmt.Errorf("inconsistent leaves to claim for transfer %s: expected %d, got %d", transferID, len(leavesToTransfer), len(claimPackage.LeavesToClaim))
	}

	// Validate that every leaf in LeavesToClaim has a direct-from-cpfp refund entry.
	// Direct refund is only required when the leaf has a DirectTx, which is checked
	// later in prepareClaimRefundSigningJobs where leaf data is available.
	directFromCpfpLeafIDs := make(map[string]struct{}, len(claimPackage.DirectFromCpfpLeavesToClaim))
	for _, job := range claimPackage.DirectFromCpfpLeavesToClaim {
		directFromCpfpLeafIDs[job.LeafId] = struct{}{}
	}
	for _, job := range claimPackage.LeavesToClaim {
		if _, ok := directFromCpfpLeafIDs[job.LeafId]; !ok {
			return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("missing direct from CPFP refund transaction for leaf %s", job.LeafId))
		}
	}

	if len(claimPackage.KeyTweakPackage) == 0 {
		return nil, fmt.Errorf("claim package key_tweak_package is required and must be non-empty")
	}

	if err := verifyClaimPackageSignature(transferID, claimPackage, reqOwnerIDPubKey); err != nil {
		return nil, err
	}

	// Determine whether we should use the stored key tweaks (from a previous Phase 1 commit)
	// rather than the new claim package. When the transfer is already at ReceiverKeyTweakLocked
	// or later receiver-side states, Phase 1 has already committed the key tweaks on all SOs.
	// Using a new claim package would cause a mismatch: SOs that already stored the original
	// tweaks would keep them (due to the len(leaf.KeyTweak) == 0 guard), while the coordinator
	// would extract different proofs from the new package.
	useStoredKeyTweaks := false
	if isMimoReceiveEnabled {
		switch receiver.Status {
		case st.TransferReceiverStatusKeyTweakLocked,
			st.TransferReceiverStatusKeyTweakApplied,
			st.TransferReceiverStatusRefundSigned:
			useStoredKeyTweaks = true
		case st.TransferReceiverStatusSenderInitiated,
			st.TransferReceiverStatusKeyTweaked,
			st.TransferReceiverStatusCompleted,
			st.TransferReceiverStatusCancelled:
			// Use the new claim package.
		}
	} else {
		switch transfer.Status {
		case st.TransferStatusReceiverKeyTweakLocked,
			st.TransferStatusReceiverKeyTweakApplied,
			st.TransferStatusReceiverRefundSigned:
			useStoredKeyTweaks = true
		case st.TransferStatusSenderInitiated,
			st.TransferStatusSenderInitiatedCoordinator,
			st.TransferStatusSenderKeyTweakPending,
			st.TransferStatusApplyingSenderKeyTweak,
			st.TransferStatusSenderKeyTweaked,
			st.TransferStatusReceiverKeyTweaked,
			st.TransferStatusCompleted,
			st.TransferStatusExpired,
			st.TransferStatusReturned:
			// Use the new claim package.
		}
	}

	// Decrypt and extract key tweak proofs from the coordinator's portion of the claim package.
	keyTweakProofs := map[string]*pb.SecretProof{}
	coordinatorKeyTweaks := claimPackage.KeyTweakPackage[h.config.Identifier]
	if !useStoredKeyTweaks && len(coordinatorKeyTweaks) > 0 {
		decryptionPrivateKey := eciesgo.NewPrivateKeyFromBytes(h.config.IdentityPrivateKey.Serialize())
		decrypted, err := eciesgo.Decrypt(decryptionPrivateKey, coordinatorKeyTweaks)
		if err != nil {
			return nil, fmt.Errorf("unable to decrypt coordinator claim key tweaks: %w", err)
		}
		claimKeyTweaks := &pb.ClaimLeafKeyTweaks{}
		if err := proto.Unmarshal(decrypted, claimKeyTweaks); err != nil {
			return nil, fmt.Errorf("unable to unmarshal coordinator claim key tweaks: %w", err)
		}
		for _, leafTweak := range claimKeyTweaks.LeavesToReceive {
			if leafTweak.SecretShareTweak == nil {
				return nil, fmt.Errorf("missing secret share tweak for leaf %s", leafTweak.LeafId)
			}
			if len(leafTweak.SecretShareTweak.Proofs) != int(h.config.Threshold) {
				return nil, fmt.Errorf("expected %d proofs for leaf %s, got %d", h.config.Threshold, leafTweak.LeafId, len(leafTweak.SecretShareTweak.Proofs))
			}
			keyTweakProofs[leafTweak.LeafId] = &pb.SecretProof{
				Proofs: leafTweak.SecretShareTweak.Proofs,
			}
		}
	} else {
		// Key tweaks already stored (retry scenario), extract from transfer_leaves.
		for _, leaf := range leavesToTransfer {
			treeNode, err := leaf.QueryLeaf().Only(ctx)
			if err != nil {
				return nil, fmt.Errorf("unable to get tree node for leaf %s: %w", leaf.ID, err)
			}
			if leaf.KeyTweak != nil {
				leafKeyTweak := &pb.ClaimLeafKeyTweak{}
				if err := proto.Unmarshal(leaf.KeyTweak, leafKeyTweak); err != nil {
					return nil, fmt.Errorf("unable to unmarshal key tweak for leaf %s: %w", leaf.ID, err)
				}
				if leafKeyTweak.SecretShareTweak == nil {
					return nil, fmt.Errorf("missing secret share tweak for leaf %s", treeNode.ID)
				}
				if len(leafKeyTweak.SecretShareTweak.Proofs) != int(h.config.Threshold) {
					return nil, fmt.Errorf("expected %d proofs for leaf %s, got %d", h.config.Threshold, treeNode.ID, len(leafKeyTweak.SecretShareTweak.Proofs))
				}
				keyTweakProofs[treeNode.ID.String()] = &pb.SecretProof{
					Proofs: leafKeyTweak.SecretShareTweak.Proofs,
				}
			}
		}
	}

	// Perform the 2PC commit with other SOs
	userPublicKeys := make(map[string][]byte)
	for _, job := range claimPackage.LeavesToClaim {
		userPublicKeys[job.LeafId] = job.SigningPublicKey
	}

	// When using stored key tweaks, don't forward the new claim package to other SOs.
	// All SOs should already have the key tweaks stored from the original Phase 1 commit.
	var encryptedKeyTweakPackage map[string][]byte
	var claimSignature []byte
	if !useStoredKeyTweaks {
		encryptedKeyTweakPackage = claimPackage.KeyTweakPackage
		claimSignature = claimPackage.UserSignature
	}
	err = h.settleReceiverKeyTweakWithClaimPackage(ctx, transfer, receiver, keyTweakProofs, userPublicKeys, encryptedKeyTweakPackage, claimSignature)
	if err != nil {
		return nil, fmt.Errorf("unable to settle receiver key tweak: %w", err)
	}

	// Reload the transfer and transfer receiver after key tweak settlement.
	transfer, err = h.loadTransferForUpdate(ctx, transferID)
	if err != nil {
		return nil, fmt.Errorf("unable to load transfer %s: %w", transferID, err)
	}
	if receiver != nil {
		_, receiver, err = h.loadTransferReceiverByPublicKeyForUpdate(ctx, transfer, &receiver.IdentityPubkey)
		if err != nil {
			return nil, err
		}
	}

	if isMimoReceiveEnabled {
		if receiver.Status == st.TransferReceiverStatusCompleted {
			return nil, sparkerrors.AlreadyExistsDuplicateOperation(fmt.Errorf("transfer %s is already completed", transferID))
		}
	} else {
		if transfer.Status == st.TransferStatusCompleted {
			return nil, sparkerrors.AlreadyExistsDuplicateOperation(fmt.Errorf("transfer %s is already completed", transferID))
		}
	}

	// MIMO - Dual write status changes
	_, err = transfer.Update().SetStatus(st.TransferStatusReceiverRefundSigned).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to update transfer status %s: %w", transfer.ID, err)
	}
	if receiver != nil {
		_, err = receiver.Update().SetStatus(st.TransferReceiverStatusRefundSigned).Save(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to update transfer receiver status to refund signed: %w", err)
		}
	}

	transferLeaves, err := getTransferLeavesForReceiverQuery(ctx, transfer, receiver).WithLeaf(func(tnq *ent.TreeNodeQuery) {
		tnq.WithTree().WithSigningKeyshare()
	}).All(ctx)
	if err != nil {
		return nil, err
	}
	leavesById := make(map[string]*ent.TreeNode, len(transferLeaves))
	for _, transferLeaf := range transferLeaves {
		leavesById[transferLeaf.Edges.Leaf.ID.String()] = transferLeaf.Edges.Leaf
	}
	if len(leavesById) == 0 {
		return nil, fmt.Errorf("leaves cannot be empty")
	}

	result, err := h.prepareClaimRefundSigningJobs(ctx, claimPackage, leavesById, transfer)
	if err != nil {
		return nil, err
	}
	signingJobs := result.signingJobs
	leafJobMap := result.leafJobMap
	jobIsDirectRefund := result.jobIsDirectRefund
	jobIsDirectFromCpfpRefund := result.jobIsDirectFromCpfpRefund
	cpfpUserRefundMap := result.cpfpUserRefundMap
	directUserRefundMap := result.directUserRefundMap
	directFromCpfpUserRefundMap := result.directFromCpfpUserRefundMap

	// Sign with pregenerated nonces.
	signingResults, err := helper.SignFrostWithPregeneratedNonce(ctx, h.config, signingJobs)
	if err != nil {
		return nil, fmt.Errorf("unable to sign frost: %w", err)
	}

	// Group signing results by leaf and type.
	cpfpResults := make(map[string]*helper.SigningResult)
	directResults := make(map[string]*helper.SigningResult)
	directFromCpfpResults := make(map[string]*helper.SigningResult)

	for _, signingResult := range signingResults {
		leaf, ok := leafJobMap[signingResult.JobID]
		if !ok {
			return nil, fmt.Errorf("signing result for unknown job ID %s", signingResult.JobID)
		}
		if jobIsDirectRefund[signingResult.JobID] {
			directResults[leaf.ID.String()] = signingResult
		} else if jobIsDirectFromCpfpRefund[signingResult.JobID] {
			directFromCpfpResults[leaf.ID.String()] = signingResult
		} else {
			cpfpResults[leaf.ID.String()] = signingResult
		}
	}

	// Aggregate signatures (combine SO shares + user shares).
	logger := logging.GetLoggerFromContext(ctx)
	frostConn, err := h.config.NewFrostGRPCConnection()
	if err != nil {
		return nil, fmt.Errorf("unable to connect to frost: %w", err)
	}
	defer frostConn.Close()
	frostClient := pbfrost.NewFrostServiceClient(frostConn)

	nodeSignatures := make([]*pb.NodeSignatures, 0, len(cpfpResults))
	for leafID, signingResult := range cpfpResults {
		cpfpUserJob := cpfpUserRefundMap[leafID]
		leaf, exists := leavesById[leafID]
		if !exists {
			return nil, fmt.Errorf("leaf %s not found", leafID)
		}

		logger.Sugar().Infof("Aggregating cpfp frost signature for claim transfer leaf %s", leafID)
		cpfpSig, err := frostClient.AggregateFrost(ctx, &pbfrost.AggregateFrostRequest{
			Message:            signingResult.Message,
			SignatureShares:    signingResult.SignatureShares,
			PublicShares:       signingResult.PublicKeys,
			VerifyingKey:       leaf.VerifyingPubkey.Serialize(),
			Commitments:        cpfpUserJob.SigningCommitments.SigningCommitments,
			UserCommitments:    cpfpUserJob.SigningNonceCommitment,
			UserPublicKey:      leaf.OwnerSigningPubkey.Serialize(),
			UserSignatureShare: cpfpUserJob.UserSignature,
		})
		if err != nil {
			return nil, fmt.Errorf("unable to aggregate frost for cpfp refund of leaf %s: %w", leafID, err)
		}

		nodeSig := &pb.NodeSignatures{
			NodeId:                          leafID,
			NodeTxSignature:                 []byte{},
			DirectNodeTxSignature:           []byte{},
			RefundTxSignature:               cpfpSig.Signature,
			DirectRefundTxSignature:         []byte{},
			DirectFromCpfpRefundTxSignature: []byte{},
		}

		if directResult, ok := directResults[leafID]; ok {
			directUserJob := directUserRefundMap[leafID]
			logger.Sugar().Infof("Aggregating direct frost signature for claim transfer leaf %s", leafID)
			directSig, err := frostClient.AggregateFrost(ctx, &pbfrost.AggregateFrostRequest{
				Message:            directResult.Message,
				SignatureShares:    directResult.SignatureShares,
				PublicShares:       directResult.PublicKeys,
				VerifyingKey:       leaf.VerifyingPubkey.Serialize(),
				Commitments:        directUserJob.SigningCommitments.SigningCommitments,
				UserCommitments:    directUserJob.SigningNonceCommitment,
				UserPublicKey:      leaf.OwnerSigningPubkey.Serialize(),
				UserSignatureShare: directUserJob.UserSignature,
			})
			if err != nil {
				return nil, fmt.Errorf("unable to aggregate frost for direct refund of leaf %s: %w", leafID, err)
			}
			nodeSig.DirectRefundTxSignature = directSig.Signature
		}

		if directFromCpfpResult, ok := directFromCpfpResults[leafID]; ok {
			directFromCpfpUserJob := directFromCpfpUserRefundMap[leafID]
			logger.Sugar().Infof("Aggregating direct from cpfp frost signature for claim transfer leaf %s", leafID)
			directFromCpfpSig, err := frostClient.AggregateFrost(ctx, &pbfrost.AggregateFrostRequest{
				Message:            directFromCpfpResult.Message,
				SignatureShares:    directFromCpfpResult.SignatureShares,
				PublicShares:       directFromCpfpResult.PublicKeys,
				VerifyingKey:       leaf.VerifyingPubkey.Serialize(),
				Commitments:        directFromCpfpUserJob.SigningCommitments.SigningCommitments,
				UserCommitments:    directFromCpfpUserJob.SigningNonceCommitment,
				UserPublicKey:      leaf.OwnerSigningPubkey.Serialize(),
				UserSignatureShare: directFromCpfpUserJob.UserSignature,
			})
			if err != nil {
				return nil, fmt.Errorf("unable to aggregate frost for direct from cpfp refund of leaf %s: %w", leafID, err)
			}
			nodeSig.DirectFromCpfpRefundTxSignature = directFromCpfpSig.Signature
		}

		nodeSignatures = append(nodeSignatures, nodeSig)
	}

	// Finalize: update nodes with aggregated signatures and complete transfer.
	finalizeHandler := NewFinalizeSignatureHandler(h.config)
	var nodes []*pb.TreeNode
	var internalNodes []*pbinternal.TreeNode
	for _, nodeSig := range nodeSignatures {
		node, internalNode, err := finalizeHandler.updateNode(ctx, nodeSig, pbcommon.SignatureIntent_TRANSFER, true)
		if err != nil {
			return nil, fmt.Errorf("failed to update node %s: %w", nodeSig.NodeId, err)
		}
		nodes = append(nodes, node)
		internalNodes = append(internalNodes, internalNode)
	}

	// MIMO - Always write the Receiver status to completed when a receiver claims a transfer
	// In this case, the MIMO logic dictates that we should only update the transfer status
	// to completed when all receivers are completed.
	// We also must still do this for the legacy (non-MIMO) case for now.
	completionTime := time.Now()
	if receiver != nil {
		_, err = receiver.Update().
			SetStatus(st.TransferReceiverStatusCompleted).
			SetCompletionTime(completionTime).
			Save(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to update transfer receiver to completed: %w", err)
		}
	}

	// MIMO - Transfer is complete when all receivers are completed
	allReceiversComplete := true
	if isMimoReceiveEnabled {
		incompleteCount, err := transfer.QueryTransferReceivers().
			Where(enttransferreceiver.StatusNEQ(st.TransferReceiverStatusCompleted)).
			Count(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to count incomplete transfer receivers: %w", err)
		}
		allReceiversComplete = incompleteCount == 0
	}

	if !isMimoReceiveEnabled || allReceiversComplete {
		transfer, err = transfer.Update().
			SetStatus(st.TransferStatusCompleted).
			SetCompletionTime(completionTime).
			Save(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to update transfer to completed: %w", err)
		}
	}

	// Reload the transfer from a fresh DB client for marshaling.
	// The settle key tweak phase performs explicit commits, and ent entities
	// queried from a committed transaction cannot be used for further queries.
	marshalDb, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get db for marshal: %w", err)
	}
	freshTransferQuery := marshalDb.Transfer.Query().Where(enttransfer.ID(transfer.ID))
	if isMimoReceiveEnabled {
		freshTransferQuery = freshTransferQuery.WithTransferReceivers()
	}
	freshTransfer, err := freshTransferQuery.Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to reload transfer for marshal: %w", err)
	}
	var transferProto *pb.Transfer
	if isMimoReceiveEnabled {
		transferProto, err = freshTransfer.MarshalProtoForReceiver(ctx, reqOwnerIDPubKey)
	} else {
		transferProto, err = freshTransfer.MarshalProto(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("unable to marshal transfer: %w", err)
	}

	// Send gossip to other SOs.
	selection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}
	participants, err := selection.OperatorIdentifierList(h.config)
	if err != nil {
		return nil, fmt.Errorf("unable to get operator list: %w", err)
	}
	sendGossipHandler := NewSendGossipHandler(h.config)
	completionTimestamp := timestamppb.New(completionTime)

	gossipMsg := buildFinalizeGossipMessage(isMimoReceiveEnabled, transferID, receiver, internalNodes, completionTimestamp)
	_, err = sendGossipHandler.CreateCommitAndSendGossipMessage(ctx, gossipMsg, participants)
	if err != nil {
		return nil, fmt.Errorf("unable to send finalize transfer gossip: %w", err)
	}

	return &pb.ClaimTransferResponse{Transfer: transferProto}, nil
}

// parseSigningCommitments extracts SO signing commitments from a UserSignedTxSigningJob.
func parseSigningCommitments(job *pb.UserSignedTxSigningJob) (map[string]frost.SigningCommitment, error) {
	round1Packages := make(map[string]frost.SigningCommitment)
	signingCommitments := job.GetSigningCommitments()
	if signingCommitments == nil {
		return nil, fmt.Errorf("missing signing_commitments for leaf_id %s", job.LeafId)
	}
	for key, commitment := range signingCommitments.GetSigningCommitments() {
		obj := frost.SigningCommitment{}
		if err := obj.UnmarshalProto(commitment); err != nil {
			return nil, fmt.Errorf("unable to unmarshal signing commitment: %w", err)
		}
		if obj.IsZero() {
			return nil, fmt.Errorf("signing commitment is invalid for key %s: hiding or binding is empty", key)
		}
		round1Packages[key] = obj
	}
	return round1Packages, nil
}

type claimRefundSigningJobsResult struct {
	signingJobs                 []*helper.SigningJobWithPregeneratedNonce
	leafJobMap                  map[uuid.UUID]*ent.TreeNode
	jobIsDirectRefund           map[uuid.UUID]bool
	jobIsDirectFromCpfpRefund   map[uuid.UUID]bool
	cpfpUserRefundMap           map[string]*pb.UserSignedTxSigningJob
	directUserRefundMap         map[string]*pb.UserSignedTxSigningJob
	directFromCpfpUserRefundMap map[string]*pb.UserSignedTxSigningJob
}

// prepareClaimRefundSigningJobs validates refund transactions (cpfp, direct, and direct-from-cpfp) from the
// claim package and persists them on the corresponding leaves. Direct-from-cpfp is required for all leaves;
// direct refund is required only for non-zero-timelock leaves that have a DirectTx. It then builds FROST signing jobs with
// pre-generated nonces and returns lookup maps (leaf-to-job, job type) to assist with signing and aggregation.
func (h *TransferHandler) prepareClaimRefundSigningJobs(
	ctx context.Context,
	claimPackage *pb.ClaimPackage,
	leaves map[string]*ent.TreeNode,
	transfer *ent.Transfer,
) (*claimRefundSigningJobsResult, error) {
	leafJobMap := make(map[uuid.UUID]*ent.TreeNode)
	jobIsDirectRefund := make(map[uuid.UUID]bool)
	jobIsDirectFromCpfpRefund := make(map[uuid.UUID]bool)
	var signingJobs []*helper.SigningJobWithPregeneratedNonce

	cpfpUserRefundMap := make(map[string]*pb.UserSignedTxSigningJob)
	directUserRefundMap := make(map[string]*pb.UserSignedTxSigningJob)
	directFromCpfpUserRefundMap := make(map[string]*pb.UserSignedTxSigningJob)

	for _, job := range claimPackage.LeavesToClaim {
		cpfpUserRefundMap[job.LeafId] = job
	}
	for _, job := range claimPackage.DirectLeavesToClaim {
		directUserRefundMap[job.LeafId] = job
	}
	for _, job := range claimPackage.DirectFromCpfpLeavesToClaim {
		directFromCpfpUserRefundMap[job.LeafId] = job
	}

	for _, job := range claimPackage.LeavesToClaim {
		leaf, exists := leaves[job.LeafId]
		if !exists {
			return nil, fmt.Errorf("unexpected leaf id %s", job.LeafId)
		}

		// Validate refund transactions.
		leafRefundJob := &pb.LeafRefundTxSigningJob{
			LeafId: job.LeafId,
			RefundTxSigningJob: &pb.SigningJob{
				RawTx:            job.RawTx,
				SigningPublicKey: job.SigningPublicKey,
			},
		}
		// Direct refund is only required when the leaf has a DirectTx and is not a zero-timelock node.
		if directJob, ok := directUserRefundMap[job.LeafId]; ok {
			leafRefundJob.DirectRefundTxSigningJob = &pb.SigningJob{
				RawTx:            directJob.RawTx,
				SigningPublicKey: directJob.SigningPublicKey,
			}
		} else if len(leaf.DirectTx) > 0 {
			isZeroNode, err := bitcointransaction.IsZeroNode(leaf)
			if err != nil {
				return nil, fmt.Errorf("failed to determine if node is zero node: %w", err)
			}
			if !isZeroNode {
				return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("missing direct refund transaction for leaf %s", job.LeafId))
			}
		}
		// Direct-from-cpfp refund is always required (validated early in ClaimTransfer).
		dfcJob := directFromCpfpUserRefundMap[job.LeafId]
		leafRefundJob.DirectFromCpfpRefundTxSigningJob = &pb.SigningJob{
			RawTx:            dfcJob.RawTx,
			SigningPublicKey: dfcJob.SigningPublicKey,
		}
		if err := validateReceivedRefundTransactions(ctx, leafRefundJob, leaf, transfer.Type); err != nil {
			return nil, err
		}

		// Update CPFP refund tx on existing leaf.
		rawRefundTx, err := common.TxFromRawTxBytes(job.RawTx)
		if err != nil {
			return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("failed to parse cpfp raw_refund_tx for leaf %s: %w", job.LeafId, err))
		}
		rawRefundTxid := st.NewTxID(rawRefundTx.TxHash())

		updateOp := leaf.Update().
			SetRawRefundTx(job.RawTx).
			SetRawRefundTxid(rawRefundTxid)

		if directJob, ok := directUserRefundMap[job.LeafId]; ok {
			directRefundTxParsed, err := common.TxFromRawTxBytes(directJob.RawTx)
			if err != nil {
				return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("failed to parse direct_refund_tx for leaf %s: %w", job.LeafId, err))
			}
			updateOp = updateOp.
				SetDirectRefundTx(directJob.RawTx).
				SetDirectRefundTxid(st.NewTxID(directRefundTxParsed.TxHash()))
		}

		directFromCpfpRefundTxParsed, err := common.TxFromRawTxBytes(dfcJob.RawTx)
		if err != nil {
			return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("failed to parse direct_from_cpfp_refund_tx for leaf %s: %w", job.LeafId, err))
		}
		updateOp = updateOp.
			SetDirectFromCpfpRefundTx(dfcJob.RawTx).
			SetDirectFromCpfpRefundTxid(st.NewTxID(directFromCpfpRefundTxParsed.TxHash()))

		if _, err := updateOp.Save(ctx); err != nil {
			return nil, fmt.Errorf("unable to update refund txs for leaf %s: %w", job.LeafId, err)
		}

		// Create CPFP signing job with pregenerated nonces.
		cpfpLeafTx, err := common.TxFromRawTxBytes(leaf.RawTx)
		if err != nil {
			return nil, fmt.Errorf("unable to load cpfp leaf tx for leaf %s: %w", job.LeafId, err)
		}
		if len(cpfpLeafTx.TxOut) == 0 {
			return nil, fmt.Errorf("vout out of bounds for cpfp tx of leaf %s", job.LeafId)
		}
		refundTxSigHash, err := common.SigHashFromTx(rawRefundTx, 0, cpfpLeafTx.TxOut[0])
		if err != nil {
			return nil, fmt.Errorf("unable to calculate sighash for cpfp refund of leaf %s: %w", job.LeafId, err)
		}

		userNonceCommitment := frost.SigningCommitment{}
		if err := userNonceCommitment.UnmarshalProto(job.GetSigningNonceCommitment()); err != nil {
			return nil, fmt.Errorf("unable to unmarshal signing nonce commitment for leaf %s: %w", job.LeafId, err)
		}

		signingKeyshare, err := leaf.QuerySigningKeyshare().Only(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get signing keyshare for leaf %s: %w", job.LeafId, err)
		}

		round1Packages, err := parseSigningCommitments(job)
		if err != nil {
			return nil, fmt.Errorf("unable to parse signing commitments for cpfp refund of leaf %s: %w", job.LeafId, err)
		}

		cpfpJobID := uuid.New()
		signingJobs = append(signingJobs, &helper.SigningJobWithPregeneratedNonce{
			SigningJob: helper.SigningJob{
				JobID:             cpfpJobID,
				SigningKeyshareID: signingKeyshare.ID,
				Message:           refundTxSigHash,
				VerifyingKey:      &leaf.VerifyingPubkey,
				UserCommitment:    &userNonceCommitment,
			},
			Round1Packages: round1Packages,
		})
		leafJobMap[cpfpJobID] = leaf
		jobIsDirectRefund[cpfpJobID] = false
		jobIsDirectFromCpfpRefund[cpfpJobID] = false
	}

	// Create signing jobs for DIRECT refund txs.
	for _, job := range claimPackage.DirectLeavesToClaim {
		leaf, exists := leaves[job.LeafId]
		if !exists {
			return nil, fmt.Errorf("unexpected leaf id %s for direct refund", job.LeafId)
		}
		directRefundTx, err := common.TxFromRawTxBytes(job.RawTx)
		if err != nil {
			return nil, fmt.Errorf("unable to parse direct refund tx for leaf %s: %w", job.LeafId, err)
		}
		directTx, err := common.TxFromRawTxBytes(leaf.DirectTx)
		if err != nil {
			return nil, fmt.Errorf("unable to load direct leaf tx for leaf %s: %w", job.LeafId, err)
		}
		if len(directTx.TxOut) == 0 {
			return nil, fmt.Errorf("vout out of bounds for direct tx of leaf %s", job.LeafId)
		}
		directRefundTxSigHash, err := common.SigHashFromTx(directRefundTx, 0, directTx.TxOut[0])
		if err != nil {
			return nil, fmt.Errorf("unable to calculate sighash for direct refund of leaf %s: %w", job.LeafId, err)
		}

		userNonceCommitment := frost.SigningCommitment{}
		if err := userNonceCommitment.UnmarshalProto(job.GetSigningNonceCommitment()); err != nil {
			return nil, fmt.Errorf("unable to unmarshal signing nonce commitment for leaf %s: %w", job.LeafId, err)
		}
		signingKeyshare, err := leaf.QuerySigningKeyshare().Only(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get signing keyshare for leaf %s: %w", job.LeafId, err)
		}
		round1Packages, err := parseSigningCommitments(job)
		if err != nil {
			return nil, fmt.Errorf("unable to parse signing commitments for direct refund of leaf %s: %w", job.LeafId, err)
		}

		directJobID := uuid.New()
		signingJobs = append(signingJobs, &helper.SigningJobWithPregeneratedNonce{
			SigningJob: helper.SigningJob{
				JobID:             directJobID,
				SigningKeyshareID: signingKeyshare.ID,
				Message:           directRefundTxSigHash,
				VerifyingKey:      &leaf.VerifyingPubkey,
				UserCommitment:    &userNonceCommitment,
			},
			Round1Packages: round1Packages,
		})
		leafJobMap[directJobID] = leaf
		jobIsDirectRefund[directJobID] = true
	}

	// Create signing jobs for DIRECT FROM CPFP refund txs.
	for _, job := range claimPackage.DirectFromCpfpLeavesToClaim {
		leaf, exists := leaves[job.LeafId]
		if !exists {
			return nil, fmt.Errorf("unexpected leaf id %s for direct from cpfp refund", job.LeafId)
		}
		directFromCpfpRefundTx, err := common.TxFromRawTxBytes(job.RawTx)
		if err != nil {
			return nil, fmt.Errorf("unable to parse direct from cpfp refund tx for leaf %s: %w", job.LeafId, err)
		}
		cpfpLeafTx, err := common.TxFromRawTxBytes(leaf.RawTx)
		if err != nil {
			return nil, fmt.Errorf("unable to load cpfp leaf tx for leaf %s: %w", job.LeafId, err)
		}
		if len(cpfpLeafTx.TxOut) == 0 {
			return nil, fmt.Errorf("vout out of bounds for cpfp tx of leaf %s", job.LeafId)
		}
		directFromCpfpSigHash, err := common.SigHashFromTx(directFromCpfpRefundTx, 0, cpfpLeafTx.TxOut[0])
		if err != nil {
			return nil, fmt.Errorf("unable to calculate sighash for direct from cpfp refund of leaf %s: %w", job.LeafId, err)
		}

		userNonceCommitment := frost.SigningCommitment{}
		if err := userNonceCommitment.UnmarshalProto(job.GetSigningNonceCommitment()); err != nil {
			return nil, fmt.Errorf("unable to unmarshal signing nonce commitment for leaf %s: %w", job.LeafId, err)
		}
		signingKeyshare, err := leaf.QuerySigningKeyshare().Only(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get signing keyshare for leaf %s: %w", job.LeafId, err)
		}
		round1Packages, err := parseSigningCommitments(job)
		if err != nil {
			return nil, fmt.Errorf("unable to parse signing commitments for direct from cpfp refund of leaf %s: %w", job.LeafId, err)
		}

		directFromCpfpJobID := uuid.New()
		signingJobs = append(signingJobs, &helper.SigningJobWithPregeneratedNonce{
			SigningJob: helper.SigningJob{
				JobID:             directFromCpfpJobID,
				SigningKeyshareID: signingKeyshare.ID,
				Message:           directFromCpfpSigHash,
				VerifyingKey:      &leaf.VerifyingPubkey,
				UserCommitment:    &userNonceCommitment,
			},
			Round1Packages: round1Packages,
		})
		leafJobMap[directFromCpfpJobID] = leaf
		jobIsDirectFromCpfpRefund[directFromCpfpJobID] = true
	}

	return &claimRefundSigningJobsResult{
		signingJobs:                 signingJobs,
		leafJobMap:                  leafJobMap,
		jobIsDirectRefund:           jobIsDirectRefund,
		jobIsDirectFromCpfpRefund:   jobIsDirectFromCpfpRefund,
		cpfpUserRefundMap:           cpfpUserRefundMap,
		directUserRefundMap:         directUserRefundMap,
		directFromCpfpUserRefundMap: directFromCpfpUserRefundMap,
	}, nil
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
		return nil, sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("invalid identity public key: %w", err))
	}
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, reqOwnerIDPubKey); err != nil {
		return nil, err
	}

	transferID, err := uuid.Parse(req.GetTransferId())
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid transfer ID: %w", err))
	}

	transfer, err := h.loadTransferForUpdate(ctx, transferID, sql.WithLockAction(sql.NoWait))
	if err != nil {
		return nil, fmt.Errorf("unable to load transfer %s: %w", transferID, err)
	}
	span.SetAttributes(transferTypeKey.String(string(transfer.Type)))
	if !transfer.ReceiverIdentityPubkey.Equals(reqOwnerIDPubKey) {
		return nil, sparkerrors.InvalidArgumentPublicKeyMismatch(fmt.Errorf("cannot claim transfer %s, receiver identity public key mismatch", transferID))
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

	// This guarantees that the transfer has only one receiver and logic changes to filter leaves, etc
	// are not necessary for this endpoint. We only dual-write the status changes to the receiver object for consistency.
	receiver, err := h.loadSingleTransferReceiverForUnsupportedMimoPath(ctx, transfer)
	if err != nil {
		return nil, err
	}

	// Validate leaves count
	leavesToTransfer, err := transfer.QueryTransferLeaves().All(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to load leaves to transfer for transfer %s: %w", transferID, err)
	}
	if len(leavesToTransfer) != len(req.SigningJobs) {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("inconsistent leaves to claim for transfer %s", transferID))
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
	err = h.settleReceiverKeyTweak(ctx, transfer, receiver, keyTweakProofs, userPublicKeys)
	if err != nil {
		return nil, fmt.Errorf("unable to settle receiver key tweak: %w", err)
	}

	// Lock the transfer after the key tweak is settled. The settle phase commits the previous
	// transaction, so we must reload both transfer and receiver from the new transaction.
	transfer, err = h.loadTransferForUpdate(ctx, transferID)
	if err != nil {
		return nil, fmt.Errorf("unable to load transfer %s: %w", transferID, err)
	}
	if transfer.Status == st.TransferStatusCompleted {
		return nil, sparkerrors.AlreadyExistsDuplicateOperation(fmt.Errorf("transfer %s is already completed", transferID))
	}

	// Reload the receiver in the new transaction (the settle phase committed the old one).
	if receiver != nil {
		receiver, err = h.loadSingleTransferReceiverForUnsupportedMimoPath(ctx, transfer)
		if err != nil {
			return nil, fmt.Errorf("unable to reload transfer receiver for transfer %s: %w", transferID, err)
		}
	}

	// MIMO - Dual write status changes
	_, err = transfer.Update().SetStatus(st.TransferStatusReceiverRefundSigned).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to update transfer status %s: %w", transfer.ID, err)
	}
	if receiver != nil {
		_, err = receiver.Update().SetStatus(st.TransferReceiverStatusRefundSigned).Save(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to update transfer receiver status %v: %w", receiver.ID, err)
		}
	}

	leaves, err := h.getLeavesFromTransfer(ctx, transfer)
	if err != nil {
		return nil, err
	}

	if len(leaves) == 0 {
		return nil, fmt.Errorf("leaves cannot be empty")
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get db from context: %w", err)
	}

	// Collect all TreeNode updates to batch them and avoid N+1 queries
	builders := make([]*ent.TreeNodeCreate, 0, len(req.SigningJobs))

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

		if isSupportedTransferType {
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

	// get the receiver by identity public key from the request, currently optional
	var receiverIdentityPublicKey *keys.Public
	if len(req.GetReceiverIdentityPublicKey()) > 0 {
		publicKeyBytes := req.GetReceiverIdentityPublicKey()
		publicKey, err := keys.ParsePublicKey(publicKeyBytes)
		if err != nil {
			return fmt.Errorf("invalid identity public key: %w", err)
		}
		receiverIdentityPublicKey = &publicKey
	} else {
		receiverIdentityPublicKey = &transfer.ReceiverIdentityPubkey
	}
	isMimoReceiveEnabled, receiver, err := h.loadTransferReceiverByPublicKeyForUpdate(ctx, transfer, receiverIdentityPublicKey)
	if err != nil {
		return err
	}

	// Read logic determined by MIMO receive state
	if isMimoReceiveEnabled {
		if err := validateTransferReadyForReceiverClaim(transfer); err != nil {
			return err
		}
		if receiver.Status == st.TransferReceiverStatusCompleted {
			// This receiver has already completed their claim, return early.
			return nil
		}
	} else {
		if transfer.Status == st.TransferStatusCompleted {
			// The key tweak is already applied, return early.
			return nil
		}
	}

	hasClaimPackage := len(req.EncryptedClaimKeyTweakPackage) > 0

	// When the transfer is already at KeyTweakLocked or later, the key tweaks from a previous
	// Phase 1 are already stored on this SO. We must not accept a new claim package because it
	// could contain different key tweaks, leading to a mismatch between SOs.
	alreadyLocked := false

	// Read logic determined by MIMO receive state
	if isMimoReceiveEnabled {
		if receiver != nil {
			switch receiver.Status {
			case st.TransferReceiverStatusSenderInitiated:
				if !hasClaimPackage {
					return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("receiver %s is at status SenderInitiated but no encrypted_claim_key_tweak_package provided", receiver.ID))
				}
			case st.TransferReceiverStatusKeyTweaked:
				// do nothing
			case st.TransferReceiverStatusKeyTweakLocked:
				alreadyLocked = true
			case st.TransferReceiverStatusKeyTweakApplied,
				st.TransferReceiverStatusRefundSigned:
				// The key tweak is already applied, return early.
				return nil
			default:
				return fmt.Errorf("unexpected transfer receiver status %s for receiver %s", receiver.Status, receiver.ID)
			}
		}
	} else {
		switch transfer.Status {
		case st.TransferStatusSenderKeyTweaked:
			// Only valid when encrypted claim key tweak package is provided (from claim_transfer endpoint).
			if !hasClaimPackage {
				return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("transfer %s is at status SenderKeyTweaked but no encrypted_claim_key_tweak_package provided", transferID))
			}
		case st.TransferStatusReceiverKeyTweaked:
			// do nothing
		case st.TransferStatusReceiverKeyTweakLocked:
			alreadyLocked = true
		case st.TransferStatusReceiverKeyTweakApplied,
			st.TransferStatusReceiverRefundSigned:
			// The key tweak is already applied, return early.
			return nil
		default:
			return fmt.Errorf("transfer %s is expected to be at status TransferStatusSenderKeyTweaked, TransferStatusReceiverKeyTweaked, TransferStatusReceiverKeyTweakLocked, TransferStatusReceiverKeyTweakApplied, or TransferStatusReceiverRefundSigned but %s found", transferID, transfer.Status)
		}
	}

	// If encrypted claim key tweak package is provided AND we haven't already locked the key
	// tweaks from a prior Phase 1 commit, verify signature, decrypt, and store.
	// When already locked, skip this block entirely — the stored key tweaks must be used.
	if hasClaimPackage && !alreadyLocked {
		// Verify receiver signature over the full encrypted key tweak package.
		signingPayload := common.GetClaimPackageSigningPayload(transferID, req.EncryptedClaimKeyTweakPackage)
		if err := common.VerifyECDSASignature(*receiverIdentityPublicKey, req.ClaimSignature, signingPayload); err != nil {
			return fmt.Errorf("unable to verify claim package signature: %w", err)
		}

		// Decrypt this SO's portion.
		myCiphertext := req.EncryptedClaimKeyTweakPackage[h.config.Identifier]
		if len(myCiphertext) == 0 {
			return fmt.Errorf("no encrypted claim key tweaks found for SO %s", h.config.Identifier)
		}
		decryptionPrivateKey := eciesgo.NewPrivateKeyFromBytes(h.config.IdentityPrivateKey.Serialize())
		decryptedKeyTweaks, err := eciesgo.Decrypt(decryptionPrivateKey, myCiphertext)
		if err != nil {
			return fmt.Errorf("unable to decrypt claim key tweaks: %w", err)
		}
		claimKeyTweaks := &pb.ClaimLeafKeyTweaks{}
		if err := proto.Unmarshal(decryptedKeyTweaks, claimKeyTweaks); err != nil {
			return fmt.Errorf("unable to unmarshal claim key tweaks: %w", err)
		}

		transferLeaves, err := getTransferLeavesForReceiverQuery(ctx, transfer, receiver).WithLeaf().All(ctx)
		if err != nil {
			return fmt.Errorf("unable to get transfer leaves for transfer %s: %w", transferID, err)
		}
		if len(transferLeaves) != len(claimKeyTweaks.LeavesToReceive) {
			return fmt.Errorf("transfer has %d leaves but claim key tweaks has %d", len(transferLeaves), len(claimKeyTweaks.LeavesToReceive))
		}

		// Verify that all LeavesToReceive are found in the queried transfer leaves
		// and set the provided tweaks into the leaf if necessary
		leafMap := make(map[string]*ent.TransferLeaf)
		for _, leaf := range transferLeaves {
			leafMap[leaf.Edges.Leaf.ID.String()] = leaf
		}
		for _, leafTweak := range claimKeyTweaks.LeavesToReceive {
			leaf, exists := leafMap[leafTweak.LeafId]
			if !exists {
				return fmt.Errorf("unexpected leaf id %s in claim key tweaks", leafTweak.LeafId)
			}

			// Only store if not already stored.
			if len(leaf.KeyTweak) == 0 {
				leafTweakBytes, err := proto.Marshal(leafTweak)
				if err != nil {
					return fmt.Errorf("unable to marshal leaf tweak: %w", err)
				}
				_, err = leaf.Update().SetKeyTweak(leafTweakBytes).Save(ctx)
				if err != nil {
					return fmt.Errorf("unable to update leaf %s: %w", leafTweak.LeafId, err)
				}
			}
		}

		// Update status to ReceiverKeyTweaked if coming from SenderKeyTweaked.
		if transfer.Status == st.TransferStatusSenderKeyTweaked {
			_, err = transfer.Update().SetStatus(st.TransferStatusReceiverKeyTweaked).Save(ctx)
			if err != nil {
				return fmt.Errorf("unable to update transfer status %s: %w", transfer.ID, err)
			}
			transfer.Status = st.TransferStatusReceiverKeyTweaked
		}

		// Update receiver status to StatusKeyTweaked if coming from SenderInitiated.
		if receiver != nil && receiver.Status == st.TransferReceiverStatusSenderInitiated {
			_, err = receiver.Update().SetStatus(st.TransferReceiverStatusKeyTweaked).Save(ctx)
			if err != nil {
				return fmt.Errorf("unable to update transfer receiver status %s: %w", transfer.ID, err)
			}
			receiver.Status = st.TransferReceiverStatusKeyTweaked
		}
	}

	transferLeaves, err := getTransferLeavesForReceiverQuery(ctx, transfer, receiver).WithLeaf().All(ctx)
	if err != nil {
		return fmt.Errorf("unable to get leaves from transfer %s: %w", transferID, err)
	}

	// This check must take place here and may not fail fast- retry attempts may load the key tweaks from db
	if req.KeyTweakProofs != nil {
		err = h.ValidateKeyTweakProof(ctx, transferLeaves, req.KeyTweakProofs)
		if err != nil {
			return fmt.Errorf("unable to validate key tweak proof: %w", err)
		}
	} else {
		return fmt.Errorf("key tweak proof is required")
	}

	// update transfer and transfer receiver states to TweakLocked
	_, err = transfer.Update().SetStatus(st.TransferStatusReceiverKeyTweakLocked).Save(ctx)
	if err != nil {
		return fmt.Errorf("unable to update transfer status %s: %w", transfer.ID, err)
	}
	if receiver != nil {
		_, err = receiver.Update().SetStatus(st.TransferReceiverStatusKeyTweakLocked).Save(ctx)
		if err != nil {
			return fmt.Errorf("unable to update transfer receiver status %s: %w", transfer.ID, err)
		}
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

	// get the receiver by identity public key from the request, currently optional
	var receiverIdentityPublicKey *keys.Public
	if len(req.GetReceiverIdentityPublicKey()) > 0 {
		publicKeyBytes := req.GetReceiverIdentityPublicKey()
		publicKey, err := keys.ParsePublicKey(publicKeyBytes)
		if err != nil {
			return fmt.Errorf("invalid identity public key: %w", err)
		}
		receiverIdentityPublicKey = &publicKey
	} else {
		receiverIdentityPublicKey = &transfer.ReceiverIdentityPubkey
	}
	isMimoReceiveEnabled, receiver, err := h.loadTransferReceiverByPublicKeyForUpdate(ctx, transfer, receiverIdentityPublicKey)
	if err != nil {
		return err
	}

	if isMimoReceiveEnabled {
		if err := validateTransferReadyForReceiverClaim(transfer); err != nil {
			if req.Action == pbinternal.SettleKeyTweakAction_COMMIT {
				return err
			}
			// ROLLBACK always proceeds even when the transfer is not ready for receiver claim,
			// to prevent resource leaks in the two-phase commit protocol.
			logging.GetLoggerFromContext(ctx).Warn("SettleReceiverKeyTweak ROLLBACK proceeding despite transfer not ready for receiver claim",
				zap.String("transfer_id", transferID.String()),
				zap.String("transfer_status", string(transfer.Status)),
				zap.Error(err),
			)
		}
		switch receiver.Status {
		case st.TransferReceiverStatusKeyTweakApplied,
			st.TransferReceiverStatusRefundSigned,
			st.TransferReceiverStatusCompleted:
			// The receiver key tweak is already applied, return early.
			return nil
		case st.TransferReceiverStatusKeyTweakLocked,
			st.TransferReceiverStatusKeyTweaked,
			st.TransferReceiverStatusSenderInitiated:
			// Do nothing
		default:
			if req.Action == pbinternal.SettleKeyTweakAction_COMMIT {
				return fmt.Errorf("transfer receiver %s is in an invalid status %s to settle receiver key tweak", receiver.ID, receiver.Status)
			}
		}
	} else {
		switch transfer.Status {
		case st.TransferStatusReceiverKeyTweakApplied,
			st.TransferStatusCompleted,
			st.TransferStatusReceiverRefundSigned:
			// The receiver key tweak is already applied, return early.
			return nil
		case st.TransferStatusReceiverKeyTweakLocked,
			st.TransferStatusReceiverKeyTweaked:
			// Do nothing
		default:
			if req.Action == pbinternal.SettleKeyTweakAction_COMMIT {
				return fmt.Errorf("transfer %s is in an invalid status %s to settle receiver key tweak", transfer.ID, transfer.Status)
			}
		}
	}

	switch req.Action {
	case pbinternal.SettleKeyTweakAction_COMMIT:
		leaves, err := getTransferLeavesForReceiverQuery(ctx, transfer, receiver).WithLeaf(func(tnq *ent.TreeNodeQuery) {
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
			keyUpdate, err := h.claimLeafTweakKey(ctx, treeNode, keyTweakProto, *receiverIdentityPublicKey)
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

		// MIMO - Dual write status changes
		_, err = transfer.Update().SetStatus(st.TransferStatusReceiverKeyTweakApplied).Save(ctx)
		if err != nil {
			return fmt.Errorf("unable to update transfer status %v: %w", transferID, err)
		}
		if receiver != nil {
			_, err = receiver.Update().SetStatus(st.TransferReceiverStatusKeyTweakApplied).Save(ctx)
			if err != nil {
				return fmt.Errorf("unable to update transfer receiver status %v: %w", transferID, err)
			}
		}

	case pbinternal.SettleKeyTweakAction_ROLLBACK:
		leaves, err := getTransferLeavesForReceiverQuery(ctx, transfer, receiver).All(ctx)
		if err != nil {
			return fmt.Errorf("unable to get leaves from transfer %s: %w", transferID, err)
		}
		if err := h.revertClaimTransfer(ctx, transfer, receiver, leaves); err != nil {
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
	keyTweakMap, err := h.ValidateTransferPackage(ctx, transfer.ID, req, ownerIdentityPubKey, !transfer.Type.IsSwap())
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
	primaryTransfer, err := db.Transfer.QueryPrimarySwapTransfer(counterTransfer).ForUpdate().Only(ctx)
	if err != nil {
		return fmt.Errorf("unable to load primary transfer: %w", err)
	}
	_, err = db.Transfer.UpdateOne(primaryTransfer).SetStatus(status).Save(ctx)
	if err != nil {
		return fmt.Errorf("unable to update primary transfer for counter transfer %s status to applying sender key tweak: %w", counterTransfer.ID, err)
	}
	return nil
}
