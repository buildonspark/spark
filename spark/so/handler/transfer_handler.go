package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
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
	"github.com/lightsparkdev/spark/common/sighash"
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
	entsigningkeyshare "github.com/lightsparkdev/spark/so/ent/signingkeyshare"
	enttransfer "github.com/lightsparkdev/spark/so/ent/transfer"
	enttransferleaf "github.com/lightsparkdev/spark/so/ent/transferleaf"
	enttreenode "github.com/lightsparkdev/spark/so/ent/treenode"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/helper"
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/lightsparkdev/spark/so/mimo"
	"github.com/lightsparkdev/spark/so/partner"
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
// response format used by StartTransfer and StartTransferV3. Each of the three
// variants (cpfp / direct / direct-from-cpfp) is checked independently — a leaf
// that has only a direct or dfc result still gets that field populated.
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
			cpfpProto = res.MarshalProto()
		}
		if res, ok := directSigningResultMap[leafID]; ok {
			directProto = res.MarshalProto()
		}
		if res, ok := directFromCpfpSigningResultMap[leafID]; ok {
			directFromCpfpProto = res.MarshalProto()
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
	if err := authz.EnforceWalletNotKillSwitched(ctx, reqOwnerIdentityPubKey); err != nil {
		return nil, err
	}

	transferID, err := uuid.Parse(req.GetTransferId())
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid transfer id: %w", err))
	}
	leafTweakMap, err := h.ValidateTransferPackage(ctx, transferID, req.GetTransferPackage(), reqOwnerIdentityPubKey, !transferType.IsSwap())
	if err != nil {
		return nil, fmt.Errorf("failed to validate transfer package for transfer %s: %w", transferID, err)
	}
	if req.GetTransferPackage() == nil {
		if err := validateLegacyLeafRefundTxSigningJobs(req.GetLeavesToSend()); err != nil {
			return nil, err
		}
	}

	knobService := knobs.GetKnobsService(ctx)
	if knobService != nil {
		transferLimit := knobService.GetValue(knobs.KnobSoTransferLimit, 0)
		if transferLimit > 0 && (len(leafTweakMap) > int(transferLimit) || len(req.GetLeavesToSend()) > int(transferLimit)) {
			return nil, status.Errorf(codes.InvalidArgument, "transfer limit reached, please send %d leaves at a time", int(transferLimit))
		}

		// Validate that TransferTypeTransfer requires a transfer package when October deprecation is enabled
		if req.GetTransferPackage() == nil && transferType == st.TransferTypeTransfer {
			return nil, status.Errorf(codes.InvalidArgument, "transfer package is required for TransferTypeTransfer")
		}
	}

	leafCpfpRefundMap, leafDirectRefundMap, leafDirectFromCpfpRefundMap := loadLeafRefundMaps(req)

	receiverIdentityPubKey, err := keys.ParsePublicKey(req.GetReceiverIdentityPublicKey())
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("failed to parse receiver identity public key: %w", err))
	}

	if len(req.GetSparkInvoice()) > 0 {
		leafIDsToSend, err := uuids.ParseSliceFunc(req.GetTransferPackage().GetLeavesToSend(), (*pb.UserSignedTxSigningJob).GetLeafId)
		if err != nil {
			return nil, fmt.Errorf("failed to parse leaf id: %w", err)
		}

		err = validateSatsSparkInvoice(ctx, req.GetSparkInvoice(), receiverIdentityPubKey, reqOwnerIdentityPubKey, leafIDsToSend, true)
		if err != nil {
			return nil, fmt.Errorf("failed to validate sats spark invoice: %s for transfer id: %s. error: %w", req.GetSparkInvoice(), transferID, err)
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
			req.ExpiryTime = swapPrimaryTransferExpiryOverride()
		} else {
			primaryTransferId = swapV3Package.primaryTransferId
		}
	}
	transfer, leafMap, err := h.createTransfer(
		ctx,
		transferID,
		req.GetTransferPackage(),
		transferType,
		req.GetExpiryTime().AsTime(),
		reqOwnerIdentityPubKey,
		receiverIdentityPubKey,
		leafCpfpRefundMap,
		leafDirectRefundMap,
		leafDirectFromCpfpRefundMap,
		leafTweakMap,
		role,
		requireDirectTx,
		req.GetSparkInvoice(),
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
			return nil, fmt.Errorf("unable to update primary transfer for counter transfer %s status: %w ", req.GetTransferId(), err)
		}
	}

	var signingResultProtos []*pb.LeafRefundTxSigningResult
	var finalCpfpSignatureMap map[string][]byte
	var finalDirectSignatureMap map[string][]byte
	var finalDirectFromCpfpSignatureMap map[string][]byte
	if req.GetTransferPackage() == nil {
		signingResultProtos, err = signRefunds(ctx, h.config, req, leafMap, cpfpAdaptorPubKey, directAdaptorPubKey, directFromCpfpAdaptorPubKey, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to sign refunds for transfer %s: %w", transferID, err)
		}
	} else {
		refundSignatures, err := h.signAggregateAndUpdateRefunds(
			ctx, transfer, req.GetTransferId(), req.GetTransferPackage(), leafMap,
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
		senderKeyTweakProofs[leaf.Proto().GetLeafId()] = &pb.SecretProof{
			Proofs: leaf.Proto().GetSecretShareTweak().GetProofs(),
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

	switch transferType { //nolint:exhaustive // only specific types need partner tracking
	case st.TransferTypeTransfer:
		partner.SaveTransferPartner(ctx, transfer.ID, st.TransferPartnerTypeTransfer)
	case st.TransferTypeUtxoSwap:
		partner.SaveTransferPartner(ctx, transfer.ID, st.TransferPartnerTypeDeposit)
	}

	if req.GetTransferPackage() != nil {
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
		// The transfer itself succeeded at this point; failing here lets the sender recover via
		// the normal pending-transfer query/resume paths instead of receiving a success response
		// with a nil Transfer.
		logger.With(zap.Error(err)).Sugar().Errorf("Unable to marshal transfer %s", transfer.ID)
		return nil, fmt.Errorf("transfer %s was initiated but the response could not be built: %w", transfer.ID, err)
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
		nodeOutPoint, nodeTxOut, err := getNodeTxOutputForRefundVerification(nodeTx, leaf.Edges.Leaf.ID.String())
		if err != nil {
			return err
		}

		updatedCpfpRefundTxBytes, err := common.UpdateTxWithSignature(leaf.IntermediateRefundTx, 0, cpfpSignatureMap[leaf.Edges.Leaf.ID.String()])
		if err != nil {
			return fmt.Errorf("unable to update leaf cpfp refund tx signature for leaf %s: %w", leaf.Edges.Leaf.ID, err)
		}
		updatedCpfpRefundTx, err := common.TxFromRawTxBytes(updatedCpfpRefundTxBytes)
		if err != nil {
			return fmt.Errorf("unable to get cpfp refund tx for leaf %s: %w", leaf.Edges.Leaf.ID, err)
		}
		if err := validateSignedRefundInputSpendsParent(updatedCpfpRefundTx, nodeTx, "cpfp"); err != nil {
			return fmt.Errorf("invalid cpfp refund tx for leaf %s: %w", leaf.Edges.Leaf.ID, err)
		}
		if err := validateRefundInputCountForConnector(updatedCpfpRefundTx, connectorPrevOuts, "cpfp"); err != nil {
			return fmt.Errorf("invalid cpfp refund tx for leaf %s: %w", leaf.Edges.Leaf.ID, err)
		}
		if len(updatedCpfpRefundTx.TxIn) > 1 && connectorPrevOuts != nil {
			prevOutFetcher := txscript.NewMultiPrevOutFetcher(nil)
			prevOutFetcher.AddPrevOut(nodeOutPoint, nodeTxOut)
			for _, txIn := range updatedCpfpRefundTx.TxIn[1:] {
				prevOut, ok := connectorPrevOuts[txIn.PreviousOutPoint]
				if !ok {
					return fmt.Errorf("missing connector prevout for cpfp refund tx input %s in leaf %s", txIn.PreviousOutPoint, leaf.Edges.Leaf.ID)
				}
				prevOutFetcher.AddPrevOut(txIn.PreviousOutPoint, prevOut)
			}
			err = common.VerifySignatureInput(updatedCpfpRefundTx, 0, prevOutFetcher)
		} else {
			err = common.VerifySignatureSingleInput(updatedCpfpRefundTx, 0, nodeTxOut)
		}
		if err != nil {
			return fmt.Errorf("unable to verify leaf cpfp refund tx signature for leaf %s: %w", leaf.Edges.Leaf.ID, err)
		}

		// Compute final values for each field (nil = clear)
		var intermediateDirectFromCpfpRefundTx []byte
		if len(leaf.Edges.Leaf.DirectFromCpfpRefundTx) > 0 && len(directFromCpfpSignatureMap[leaf.Edges.Leaf.ID.String()]) > 0 {
			updatedDirectFromCpfpRefundTxBytes, err := common.UpdateTxWithSignature(leaf.IntermediateDirectFromCpfpRefundTx, 0, directFromCpfpSignatureMap[leaf.Edges.Leaf.ID.String()])
			if err != nil {
				return fmt.Errorf("unable to update leaf direct from cpfp refund tx signature for leaf %s: %w", leaf.Edges.Leaf.ID, err)
			}
			updatedDirectFromCpfpRefundTx, err := common.TxFromRawTxBytes(updatedDirectFromCpfpRefundTxBytes)
			if err != nil {
				return fmt.Errorf("unable to get direct from cpfp refund tx for leaf %s: %w", leaf.Edges.Leaf.ID, err)
			}
			if err := validateSignedRefundInputSpendsParent(updatedDirectFromCpfpRefundTx, nodeTx, "direct-from-cpfp"); err != nil {
				return fmt.Errorf("invalid direct-from-cpfp refund tx for leaf %s: %w", leaf.Edges.Leaf.ID, err)
			}
			if err := validateRefundInputCountForConnector(updatedDirectFromCpfpRefundTx, connectorPrevOuts, "direct-from-cpfp"); err != nil {
				return fmt.Errorf("invalid direct-from-cpfp refund tx for leaf %s: %w", leaf.Edges.Leaf.ID, err)
			}
			if len(updatedDirectFromCpfpRefundTx.TxIn) > 1 && connectorPrevOuts != nil {
				prevOutFetcher := txscript.NewMultiPrevOutFetcher(nil)
				prevOutFetcher.AddPrevOut(nodeOutPoint, nodeTxOut)
				for _, txIn := range updatedDirectFromCpfpRefundTx.TxIn[1:] {
					prevOut, ok := connectorPrevOuts[txIn.PreviousOutPoint]
					if !ok {
						return fmt.Errorf("missing connector prevout for direct-from-cpfp refund tx input %s in leaf %s", txIn.PreviousOutPoint, leaf.Edges.Leaf.ID)
					}
					prevOutFetcher.AddPrevOut(txIn.PreviousOutPoint, prevOut)
				}
				err = common.VerifySignatureInput(updatedDirectFromCpfpRefundTx, 0, prevOutFetcher)
			} else {
				err = common.VerifySignatureSingleInput(updatedDirectFromCpfpRefundTx, 0, nodeTxOut)
			}
			if err != nil {
				return fmt.Errorf("unable to verify leaf direct from cpfp refund tx signature for leaf %s: %w", leaf.Edges.Leaf.ID, err)
			}

			intermediateDirectFromCpfpRefundTx = updatedDirectFromCpfpRefundTxBytes
		}
		// else: stays nil, which will clear the field

		var intermediateDirectRefundTx []byte
		if len(leaf.Edges.Leaf.DirectTx) > 0 && len(directSignatureMap[leaf.Edges.Leaf.ID.String()]) > 0 {
			directNodeTx, err := common.TxFromRawTxBytes(leaf.Edges.Leaf.DirectTx)
			if err != nil {
				return fmt.Errorf("unable to get direct node tx for leaf %s: %w", leaf.Edges.Leaf.ID, err)
			}
			directNodeOutPoint, directNodeTxOut, err := getNodeTxOutputForRefundVerification(directNodeTx, leaf.Edges.Leaf.ID.String())
			if err != nil {
				return err
			}

			updatedDirectRefundTxBytes, err := common.UpdateTxWithSignature(leaf.IntermediateDirectRefundTx, 0, directSignatureMap[leaf.Edges.Leaf.ID.String()])
			if err != nil {
				return fmt.Errorf("unable to update leaf signature for leaf %s: %w", leaf.Edges.Leaf.ID, err)
			}
			updatedDirectRefundTx, err := common.TxFromRawTxBytes(updatedDirectRefundTxBytes)
			if err != nil {
				return fmt.Errorf("unable to get direct refund tx for leaf %s: %w", leaf.Edges.Leaf.ID, err)
			}
			if err := validateSignedRefundInputSpendsParent(updatedDirectRefundTx, directNodeTx, "direct"); err != nil {
				return fmt.Errorf("invalid direct refund tx for leaf %s: %w", leaf.Edges.Leaf.ID, err)
			}
			if err := validateRefundInputCountForConnector(updatedDirectRefundTx, connectorPrevOuts, "direct"); err != nil {
				return fmt.Errorf("invalid direct refund tx for leaf %s: %w", leaf.Edges.Leaf.ID, err)
			}

			if len(updatedDirectRefundTx.TxIn) > 1 && connectorPrevOuts != nil {
				prevOutFetcher := txscript.NewMultiPrevOutFetcher(nil)
				prevOutFetcher.AddPrevOut(directNodeOutPoint, directNodeTxOut)
				for _, txIn := range updatedDirectRefundTx.TxIn[1:] {
					prevOut, ok := connectorPrevOuts[txIn.PreviousOutPoint]
					if !ok {
						return fmt.Errorf("missing connector prevout for direct refund tx input %s in leaf %s", txIn.PreviousOutPoint, leaf.Edges.Leaf.ID)
					}
					prevOutFetcher.AddPrevOut(txIn.PreviousOutPoint, prevOut)
				}
				err = common.VerifySignatureInput(updatedDirectRefundTx, 0, prevOutFetcher)
			} else {
				err = common.VerifySignatureSingleInput(updatedDirectRefundTx, 0, directNodeTxOut)
			}
			if err != nil {
				return fmt.Errorf("unable to verify leaf signature for leaf %s: %w", leaf.Edges.Leaf.ID, err)
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

func validateSignedRefundInputSpendsParent(refundTx *wire.MsgTx, parentTx *wire.MsgTx, refundType string) error {
	if len(refundTx.TxIn) == 0 {
		return fmt.Errorf("%s refund tx must have at least 1 input", refundType)
	}

	expectedOutPoint := wire.OutPoint{Hash: parentTx.TxHash(), Index: 0}
	if refundTx.TxIn[0].PreviousOutPoint != expectedOutPoint {
		return fmt.Errorf("%s refund tx input 0 must spend parent tx output 0", refundType)
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
			return fmt.Errorf("unable to get cpfp node tx for leaf %s: %w", leaf.Edges.Leaf.ID, err)
		}
		nodeOutPoint, nodeTxOut, err := getNodeTxOutputForRefundVerification(nodeTx, leaf.Edges.Leaf.ID.String())
		if err != nil {
			return err
		}

		updatedTx, err := ApplySignatureToTxAndVerify(leaf.IntermediateRefundTx, finalSignatureMap[leaf.Edges.Leaf.ID.String()], cpfpAdaptorPubKey, nodeOutPoint, nodeTxOut, leaf.Edges.Leaf.VerifyingPubkey)
		if err != nil {
			return fmt.Errorf("unable to apply signature to tx and verify for leaf %s: %w", leaf.Edges.Leaf.ID, err)
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
		conn, err := operator.NewOperatorInternalGRPCConnection(ctx)
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
	// Only regular transfers carrying a TransferPackage can be expressed as a
	// v3 send-transfer flow. The legacy leaves_to_send / swap / coop-exit shapes
	// fall through to startTransferInternal.
	if req.GetTransferPackage() != nil {
		return h.startTransferV3Consensus(ctx, convertV2ToV3SendTransferRequest(req), req.GetSparkInvoice())
	}
	return h.startTransferInternal(ctx, req, st.TransferTypeTransfer, keys.Public{}, keys.Public{}, keys.Public{}, true, nil)
}

func (h *TransferHandler) StartTransferV3(ctx context.Context, req *pb.StartTransferV3Request) (*pb.StartTransferResponse, error) {
	return h.startTransferV3Consensus(ctx, req, "")
}

func (h *TransferHandler) StartLeafSwap(ctx context.Context, req *pb.StartTransferRequest) (*pb.StartTransferResponse, error) {
	return h.startTransferInternal(ctx, req, st.TransferTypeSwap, keys.Public{}, keys.Public{}, keys.Public{}, false, nil)
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

	// Route through the 2PC consensus engine when enabled; otherwise fall
	// through to the legacy startTransferInternal fanout below. Only
	// package-carrying requests can be expressed as a consensus flow (the
	// swap wallets always send one).
	if req.GetTransfer().GetTransferPackage() != nil &&
		knobs.GetKnobsService(ctx).GetValue(knobs.KnobUseConsensusInitiateSwapPrimaryTransfer, 0) > 0 {
		return h.initiateSwapPrimaryTransferConsensus(ctx, req)
	}

	return h.startTransferInternal(ctx, req.GetTransfer(), st.TransferTypePrimarySwapV3, adaptorPublicKey, keys.Public{}, keys.Public{}, true, &SwapV3Package{primaryTransferId: uuid.Nil})
}

// CounterLeafSwap initiates a leaf swap for the other side, signing refunds with an adaptor public key.
func (h *TransferHandler) CounterLeafSwap(ctx context.Context, req *pb.CounterLeafSwapRequest) (*pb.CounterLeafSwapResponse, error) {
	adaptorPublicKey, err := keys.ParsePublicKey(req.GetAdaptorPublicKey())
	if err != nil {
		return nil, fmt.Errorf("unable to parse adaptor public key: %w", err)
	}
	directAdaptorPublicKey, err := parsePublicKeyIfPresent(req.GetDirectAdaptorPublicKey())
	if err != nil {
		return nil, fmt.Errorf("unable to parse direct adaptor public key: %w", err)
	}
	directFromCpfpAdaptorPublicKey, err := parsePublicKeyIfPresent(req.GetDirectFromCpfpAdaptorPublicKey())
	if err != nil {
		return nil, fmt.Errorf("unable to parse direct from cpfp adaptor public key: %w", err)
	}
	startTransferResponse, err := h.startTransferInternal(ctx, req.GetTransfer(), st.TransferTypeCounterSwap, adaptorPublicKey, directAdaptorPublicKey, directFromCpfpAdaptorPublicKey, false, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to start counter leaf swap for request %s: %w", logging.FormatProto("counter_leaf_swap_request", req), err)
	}
	return &pb.CounterLeafSwapResponse{Transfer: startTransferResponse.GetTransfer(), SigningResults: startTransferResponse.GetSigningResults()}, nil
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
	for _, leaf := range req.GetLeavesToSend() {
		var directRefundTx []byte
		if leaf.GetDirectRefundTxSigningJob() != nil {
			directRefundTx = leaf.GetDirectRefundTxSigningJob().GetRawTx()
		}
		var directFromCpfpRefundTx []byte
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
		TransferId:                     req.GetTransferId(),
		SenderIdentityPublicKey:        req.GetOwnerIdentityPublicKey(),
		ReceiverIdentityPublicKey:      req.GetReceiverIdentityPublicKey(),
		ExpiryTime:                     req.GetExpiryTime(),
		Leaves:                         leaves,
		SenderKeyTweakProofs:           senderKeyTweakProofs,
		Type:                           *transferTypeProto,
		TransferPackage:                req.GetTransferPackage(),
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
		conn, err := operator.NewOperatorInternalGRPCConnection(ctx)
		if err != nil {
			return nil, err
		}
		defer conn.Close()

		client := pbinternal.NewSparkInternalServiceClient(conn)
		return client.InitiateTransfer(ctx, initTransferRequest)
	})
	return err
}

func (h *TransferHandler) syncDeliverSenderKeyTweak(ctx context.Context, req *pb.FinalizeTransferWithTransferPackageRequest, transferType st.TransferType, coordinatorKeyTweakMap map[string]validatedKeyTweak) error {
	ctx, span := tracer.Start(ctx, "TransferHandler.syncDeliverSenderKeyTweak", trace.WithAttributes(
		transferTypeKey.String(string(transferType)),
	))
	defer span.End()
	if req.GetTransferPackage() == nil {
		return fmt.Errorf("expected transfer package to be populated")
	}

	// Forward the coordinator's plaintext SecretShareTweak.Proofs per leaf so each non-coordinator
	// SO can verify its decrypted proofs match — ensuring every SO's encrypted share comes from
	// the same polynomial.
	senderKeyTweakProofs := make(map[string]*pb.SecretProof, len(coordinatorKeyTweakMap))
	for leafID, leafTweak := range coordinatorKeyTweakMap {
		senderKeyTweakProofs[leafID] = &pb.SecretProof{
			Proofs: leafTweak.Proto().GetSecretShareTweak().GetProofs(),
		}
	}

	deliverSenderKeyTweakRequest := &pbinternal.DeliverSenderKeyTweakRequest{
		TransferId:              req.GetTransferId(),
		SenderIdentityPublicKey: req.GetOwnerIdentityPublicKey(),
		TransferPackage:         req.GetTransferPackage(),
		SenderKeyTweakProofs:    senderKeyTweakProofs,
	}
	selection := helper.OperatorSelection{
		Option: helper.OperatorSelectionOptionExcludeSelf,
	}
	_, err := helper.ExecuteTaskWithAllOperators(ctx, h.config, &selection, func(ctx context.Context, operator *so.SigningOperator) (any, error) {
		conn, err := operator.NewOperatorInternalGRPCConnection(ctx)
		if err != nil {
			return nil, err
		}
		defer conn.Close()

		logger := logging.GetLoggerFromContext(ctx)
		logger.Sugar().Infof("Delivering key tweak for transfer %s to SO %d", req.GetTransferId(), operator.ID)
		client := pbinternal.NewSparkInternalServiceClient(conn)
		return client.DeliverSenderKeyTweak(ctx, deliverSenderKeyTweakRequest)
	})
	return err
}

func signRefunds(ctx context.Context, config *so.Config, requests *pb.StartTransferRequest, leafMap map[string]*ent.TreeNode, cpfpAdaptorPubKey keys.Public, directAdaptorPubKey keys.Public, directFromCpfpAdaptorPubKey keys.Public, connectorTx []byte) ([]*pb.LeafRefundTxSigningResult, error) {
	ctx, span := tracer.Start(ctx, "TransferHandler.signRefunds")
	defer span.End()

	if requests.GetTransferPackage() != nil {
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

	if len(requests.GetLeavesToSend()) == 0 {
		return nil, fmt.Errorf("leaves to send is empty when signing refunds")
	}

	// Process each leaf's signing jobs
	for _, req := range requests.GetLeavesToSend() {
		leaf, exists := leafMap[req.GetLeafId()]
		if !exists {
			return nil, fmt.Errorf("leaf %s not found in leafMap", req.GetLeafId())
		}
		cpfpRefundTx, err := common.TxFromRawTxBytes(req.GetRefundTxSigningJob().GetRawTx())
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
		if err := validateRefundInputCountForConnector(cpfpRefundTx, connectorPrevOuts, "cpfp"); err != nil {
			return nil, err
		}

		var cpfpRefundTxSigHash sighash.Hash
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

			cpfpRefundTxSigHash, err = sighash.FromMultiPrevOutTx(cpfpRefundTx, 0, prevOuts)
		} else {
			// Single-input sighash (legacy flow):
			// - Single-input refund tx
			cpfpRefundTxSigHash, err = sighash.FromTx(cpfpRefundTx, 0, cpfpLeafTx.TxOut[0])
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
		if req.GetDirectRefundTxSigningJob() != nil && len(leaf.DirectTx) > 0 {
			directRefundTx, err := common.TxFromRawTxBytes(req.GetDirectRefundTxSigningJob().GetRawTx())
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
			if err := validateRefundInputCountForConnector(directRefundTx, connectorPrevOuts, "direct"); err != nil {
				return nil, err
			}
			var directRefundTxSigHash sighash.Hash
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

				directRefundTxSigHash, err = sighash.FromMultiPrevOutTx(directRefundTx, 0, prevOuts)
			} else {
				// Single-input sighash (legacy flow):
				// - Single-input refund tx

				directRefundTxSigHash, err = sighash.FromTx(directRefundTx, 0, directLeafTx.TxOut[0])
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
		if req.GetDirectFromCpfpRefundTxSigningJob() != nil {
			directFromCpfpRefundTx, err := common.TxFromRawTxBytes(req.GetDirectFromCpfpRefundTxSigningJob().GetRawTx())
			if err != nil {
				return nil, fmt.Errorf("unable to load new refund tx: %w", err)
			}
			if err := validateRefundInputCountForConnector(directFromCpfpRefundTx, connectorPrevOuts, "direct-from-cpfp"); err != nil {
				return nil, err
			}
			var directFromCpfpRefundTxSigHash sighash.Hash
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

				directFromCpfpRefundTxSigHash, err = sighash.FromMultiPrevOutTx(directFromCpfpRefundTx, 0, prevOuts)
			} else {
				// Single-input sighash (legacy flow):
				// - Single-input refund tx

				directFromCpfpRefundTxSigHash, err = sighash.FromTx(directFromCpfpRefundTx, 0, cpfpLeafTx.TxOut[0])
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

		resultsByLeafID[leafID] = &pb.LeafRefundTxSigningResult{
			LeafId:                leafID,
			RefundTxSigningResult: result.MarshalProto(),
			VerifyingKey:          leaf.VerifyingPubkey.Serialize(),
		}
	}

	// Process Direct results
	for _, result := range directSigningResults {
		leaf := leafJobMap[result.JobID]
		leafID := leaf.ID.String()

		if existing, ok := resultsByLeafID[leafID]; ok {
			existing.DirectRefundTxSigningResult = result.MarshalProto()
		}
	}

	// Process DirectFromCpfp results
	for _, result := range directFromCpfpSigningResults {
		leaf := leafJobMap[result.JobID]
		leafID := leaf.ID.String()

		if existing, ok := resultsByLeafID[leafID]; ok {
			existing.DirectFromCpfpRefundTxSigningResult = result.MarshalProto()
		}
	}

	return slices.Collect(maps.Values(resultsByLeafID)), nil
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
	for _, req := range pkg.GetLeavesToSend() {
		leaf, exists := leafMap[req.GetLeafId()]
		if !exists {
			return nil, nil, nil, fmt.Errorf("leaf %s not found in leafMap", req.GetLeafId())
		}
		refundTx, err := common.TxFromRawTxBytes(req.GetRawTx())
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
		if err := validateRefundInputCountForConnector(refundTx, connectorPrevOuts, "cpfp"); err != nil {
			return nil, nil, nil, err
		}

		var refundTxSigHash sighash.Hash
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

			refundTxSigHash, err = sighash.FromMultiPrevOutTx(refundTx, 0, prevOuts)
		} else {
			refundTxSigHash, err = sighash.FromTx(refundTx, 0, leafTx.TxOut[0])
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
			return nil, nil, nil, fmt.Errorf("missing signing_commitments for leaf_id %s", req.GetLeafId())
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
	for _, req := range pkg.GetDirectLeavesToSend() {
		leaf, exists := leafMap[req.GetLeafId()]
		if !exists {
			return nil, nil, nil, fmt.Errorf("leaf %s not found in leafMap", req.GetLeafId())
		}
		directRefundTx, err := common.TxFromRawTxBytes(req.GetRawTx())
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
		if err := validateRefundInputCountForConnector(directRefundTx, connectorPrevOuts, "direct"); err != nil {
			return nil, nil, nil, err
		}
		var directRefundTxSigHash sighash.Hash
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

			directRefundTxSigHash, err = sighash.FromMultiPrevOutTx(directRefundTx, 0, prevOuts)
		} else {
			directRefundTxSigHash, err = sighash.FromTx(directRefundTx, 0, directTx.TxOut[0])
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
			return nil, nil, nil, fmt.Errorf("missing signing_commitments for leaf_id %s", req.GetLeafId())
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
	for _, req := range pkg.GetDirectFromCpfpLeavesToSend() {
		leaf, exists := leafMap[req.GetLeafId()]
		if !exists {
			return nil, nil, nil, fmt.Errorf("leaf %s not found in leafMap", req.GetLeafId())
		}
		directFromCpfpRefundTx, err := common.TxFromRawTxBytes(req.GetRawTx())
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
		if err := validateRefundInputCountForConnector(directFromCpfpRefundTx, connectorPrevOuts, "direct-from-cpfp"); err != nil {
			return nil, nil, nil, err
		}

		var directFromCpfpRefundTxSigHash sighash.Hash
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

			directFromCpfpRefundTxSigHash, err = sighash.FromMultiPrevOutTx(directFromCpfpRefundTx, 0, prevOuts)
		} else {
			directFromCpfpRefundTxSigHash, err = sighash.FromTx(directFromCpfpRefundTx, 0, directFromCpfpLeafTx.TxOut[0])
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
			return nil, nil, nil, fmt.Errorf("missing signing_commitments for leaf_id %s", req.GetLeafId())
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
	frostConn, err := config.NewFrostGRPCConnection()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("unable to connect to frost: %w", err)
	}
	defer frostConn.Close()
	frostClient := pbfrost.NewFrostServiceClient(frostConn)

	return aggregateSignaturesWithClient(
		ctx, config, frostClient, transferID, pkg,
		cpfpAdaptorPubKey, directAdaptorPubKey, directFromCpfpAdaptorPubKey,
		cpfpSigningResultMap, directSigningResultMap, directFromCpfpSigningResultMap,
		leafMap,
	)
}

// aggregateSignaturesWithClient is AggregateSignatures with the FROST client
// injected, so the per-leaf per-variant enqueue/readback routing is unit
// testable without a signer connection.
func aggregateSignaturesWithClient(
	ctx context.Context,
	config *so.Config,
	frostClient pbfrost.FrostServiceClient,
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
	cpfpUserRefundMap := make(map[string]*pb.UserSignedTxSigningJob)
	directUserRefundMap := make(map[string]*pb.UserSignedTxSigningJob)
	directFromCpfpUserRefundMap := make(map[string]*pb.UserSignedTxSigningJob)
	for _, userSignedRefund := range pkg.GetLeavesToSend() {
		cpfpUserRefundMap[userSignedRefund.GetLeafId()] = userSignedRefund
	}
	for _, userSignedRefund := range pkg.GetDirectLeavesToSend() {
		directUserRefundMap[userSignedRefund.GetLeafId()] = userSignedRefund
	}
	for _, userSignedRefund := range pkg.GetDirectFromCpfpLeavesToSend() {
		directFromCpfpUserRefundMap[userSignedRefund.GetLeafId()] = userSignedRefund
	}

	batch := newFrostAggregationBatch(config)
	addVariant := func(txKind string, signingResultMap map[string]*helper.SigningResult, userRefundMap map[string]*pb.UserSignedTxSigningJob, adaptorPubKey keys.Public) error {
		for leafID, signingResult := range signingResultMap {
			leaf, exists := leafMap[leafID]
			if !exists {
				return fmt.Errorf("leaf %s not found in leafMap", leafID)
			}
			if err := batch.addSigningResultJob(leafAggregationJobKey(leafID, txKind), signingResult, userRefundMap[leafID], leaf.VerifyingPubkey, leaf.OwnerSigningPubkey, adaptorPubKey); err != nil {
				return err
			}
		}
		return nil
	}
	if err := addVariant(txKindCPFP, cpfpSigningResultMap, cpfpUserRefundMap, cpfpAdaptorPubKey); err != nil {
		return nil, nil, nil, err
	}
	if err := addVariant(txKindDirect, directSigningResultMap, directUserRefundMap, directAdaptorPubKey); err != nil {
		return nil, nil, nil, err
	}
	if err := addVariant(txKindDirectFromCPFP, directFromCpfpSigningResultMap, directFromCpfpUserRefundMap, directFromCpfpAdaptorPubKey); err != nil {
		return nil, nil, nil, err
	}

	logger := logging.GetLoggerFromContext(ctx)
	logger.Sugar().Infof(
		"Aggregating frost signatures for transfer %s (%d cpfp, %d direct, %d direct-from-cpfp jobs)",
		transferID, len(cpfpSigningResultMap), len(directSigningResultMap), len(directFromCpfpSigningResultMap),
	)
	signatures, err := batch.aggregate(ctx, frostClient)
	if err != nil {
		logger.With(zap.Error(err)).Sugar().Errorf("Unable to aggregate frost signatures for transfer %s", transferID)
		return nil, nil, nil, fmt.Errorf("unable to aggregate frost signatures for transfer %s: %w", transferID, err)
	}

	finalCpfpSignatureMap := make(map[string][]byte, len(cpfpSigningResultMap))
	finalDirectSignatureMap := make(map[string][]byte, len(directSigningResultMap))
	finalDirectFromCpfpSignatureMap := make(map[string][]byte, len(directFromCpfpSigningResultMap))
	for leafID := range cpfpSigningResultMap {
		finalCpfpSignatureMap[leafID] = signatures[leafAggregationJobKey(leafID, txKindCPFP)]
	}
	for leafID := range directSigningResultMap {
		finalDirectSignatureMap[leafID] = signatures[leafAggregationJobKey(leafID, txKindDirect)]
	}
	for leafID := range directFromCpfpSigningResultMap {
		finalDirectFromCpfpSignatureMap[leafID] = signatures[leafAggregationJobKey(leafID, txKindDirectFromCPFP)]
	}
	return finalCpfpSignatureMap, finalDirectSignatureMap, finalDirectFromCpfpSignatureMap, nil
}

// aggregateClaimRefundSignatures batches the legacy claim path's refund
// aggregation jobs (cpfp required per leaf, direct and direct-from-cpfp
// optional) and routes each leaf's signatures into its NodeSignatures proto.
// Takes the FROST client so the per-leaf per-variant routing is unit testable
// without a signer connection.
func aggregateClaimRefundSignatures(
	ctx context.Context,
	config *so.Config,
	frostClient pbfrost.FrostServiceClient,
	cpfpResults map[string]*helper.SigningResult,
	directResults map[string]*helper.SigningResult,
	directFromCpfpResults map[string]*helper.SigningResult,
	cpfpUserRefundMap map[string]*pb.UserSignedTxSigningJob,
	directUserRefundMap map[string]*pb.UserSignedTxSigningJob,
	directFromCpfpUserRefundMap map[string]*pb.UserSignedTxSigningJob,
	leavesById map[string]*ent.TreeNode,
) ([]*pb.NodeSignatures, error) {
	batch := newFrostAggregationBatch(config)
	for leafID, signingResult := range cpfpResults {
		leaf, exists := leavesById[leafID]
		if !exists {
			return nil, fmt.Errorf("leaf %s not found", leafID)
		}
		if err := batch.addSigningResultJob(leafAggregationJobKey(leafID, txKindCPFP), signingResult, cpfpUserRefundMap[leafID], leaf.VerifyingPubkey, leaf.OwnerSigningPubkey, keys.Public{}); err != nil {
			return nil, err
		}
		if directResult, ok := directResults[leafID]; ok {
			if err := batch.addSigningResultJob(leafAggregationJobKey(leafID, txKindDirect), directResult, directUserRefundMap[leafID], leaf.VerifyingPubkey, leaf.OwnerSigningPubkey, keys.Public{}); err != nil {
				return nil, err
			}
		}
		if directFromCpfpResult, ok := directFromCpfpResults[leafID]; ok {
			if err := batch.addSigningResultJob(leafAggregationJobKey(leafID, txKindDirectFromCPFP), directFromCpfpResult, directFromCpfpUserRefundMap[leafID], leaf.VerifyingPubkey, leaf.OwnerSigningPubkey, keys.Public{}); err != nil {
				return nil, err
			}
		}
	}

	signatures, err := batch.aggregate(ctx, frostClient)
	if err != nil {
		return nil, err
	}

	nodeSignatures := make([]*pb.NodeSignatures, 0, len(cpfpResults))
	for leafID := range cpfpResults {
		nodeSig := &pb.NodeSignatures{
			NodeId:                          leafID,
			NodeTxSignature:                 []byte{},
			DirectNodeTxSignature:           []byte{},
			RefundTxSignature:               signatures[leafAggregationJobKey(leafID, txKindCPFP)],
			DirectRefundTxSignature:         []byte{},
			DirectFromCpfpRefundTxSignature: []byte{},
		}
		if _, ok := directResults[leafID]; ok {
			nodeSig.DirectRefundTxSignature = signatures[leafAggregationJobKey(leafID, txKindDirect)]
		}
		if _, ok := directFromCpfpResults[leafID]; ok {
			nodeSig.DirectFromCpfpRefundTxSignature = signatures[leafAggregationJobKey(leafID, txKindDirectFromCPFP)]
		}
		nodeSignatures = append(nodeSignatures, nodeSig)
	}
	return nodeSignatures, nil
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
	senderPubkey, err := mimo.GetSingleTransferSender(transfer)
	if err != nil {
		return nil, err
	}
	err = authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, senderPubkey)
	if err != nil {
		return nil, err
	}
	if err := authz.EnforceWalletNotKillSwitched(ctx, senderPubkey); err != nil {
		return nil, err
	}
	// Verify that the request's owner_identity_public_key matches the DB-stored sender identity.
	// This prevents a caller from supplying a different identity key that could bypass
	// signature verification in ValidateTransferPackage.
	reqOwnerPubKey, err := keys.ParsePublicKey(req.GetOwnerIdentityPublicKey())
	if err != nil {
		return nil, fmt.Errorf("failed to parse owner identity public key: %w", err)
	}
	if !reqOwnerPubKey.Equals(senderPubkey) {
		return nil, fmt.Errorf("owner_identity_public_key in request does not match the transfer sender identity")
	}
	if transfer.Status != st.TransferStatusSenderInitiated {
		return nil, sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("transfer %s is in state %s; expected sender initiated status", transferID, transfer.Status))
	}
	coordinatorKeyTweakMap, err := h.ValidateTransferPackage(ctx, transferID, req.GetTransferPackage(), senderPubkey, !transfer.Type.IsSwap())
	if err != nil {
		return nil, fmt.Errorf("failed to validate transfer package: %w", err)
	}
	logger := logging.GetLoggerFromContext(ctx)
	logger.Sugar().Infof("Preparing to send key tweaks to other SOs for transfer %s", transferID)
	err = h.syncDeliverSenderKeyTweak(ctx, req, transfer.Type, coordinatorKeyTweakMap)
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
			return nil, fmt.Errorf("unable to find preimage request for transfer %s: %w", transfer.ID, err)
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
	if err = h.setSoCoordinatorKeyTweaks(ctx, transfer, coordinatorKeyTweakMap); err != nil {
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

	// Update().Save() above strips edges; reload so MarshalProto can populate Senders/Receivers.
	transfer, err = h.loadTransferNoUpdate(ctx, transferID)
	if err != nil {
		return nil, fmt.Errorf("unable to reload transfer for marshal: %w", err)
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

// checkTransferAccessMIMO grants access if the viewer matches any sender or
// receiver of the transfer. Requires WithTransferSenders/WithTransferReceivers
// to be pre-loaded. Errors if either edge is empty — every transfer must have
// at least one of each.
func (h *TransferHandler) checkTransferAccessMIMO(
	ctx context.Context,
	transfer *ent.Transfer,
	accessMap map[keys.Public]bool,
) (bool, error) {
	if len(transfer.Edges.TransferSenders) == 0 || len(transfer.Edges.TransferReceivers) == 0 {
		return false, fmt.Errorf("transfer %s has no TransferSenders and/or TransferReceivers edges — every transfer must have at least one of each", transfer.ID)
	}

	walletHandler := NewWalletSettingHandler(h.config)
	check := func(pubkey keys.Public) (bool, error) {
		if access, ok := accessMap[pubkey]; ok {
			return access, nil
		}
		access, err := walletHandler.HasReadAccessToWallet(ctx, pubkey)
		if err != nil {
			return false, fmt.Errorf("failed to check viewer access to transfer %s: %w", transfer.ID, err)
		}
		accessMap[pubkey] = access
		return access, nil
	}
	for _, s := range transfer.Edges.TransferSenders {
		if access, err := check(s.IdentityPubkey); err != nil || access {
			return access, err
		}
	}
	for _, r := range transfer.Edges.TransferReceivers {
		if access, err := check(r.IdentityPubkey); err != nil || access {
			return access, err
		}
	}
	return false, nil
}

// withTransferQueryEdges applies the eager-load graph shared by the transfer
// read paths. Centralized so a new edge reaches every query path at once.
func withTransferQueryEdges(q *ent.TransferQuery) *ent.TransferQuery {
	return q.
		WithSparkInvoice().
		WithTransferSenders().
		WithTransferReceivers().
		WithTransferLeaves(func(q *ent.TransferLeafQuery) {
			q.WithLeaf(func(q *ent.TreeNodeQuery) {
				q.WithTree().WithSigningKeyshare().WithParent()
			})
		})
}

// marshalTransferForWallet marshals a transfer with the receiver projection
// (claim view) when wp is one of its receivers, and the full transfer otherwise.
// wp == nil always marshals the full transfer — the by-id path has no
// participant to scope to.
func marshalTransferForWallet(ctx context.Context, t *ent.Transfer, wp *keys.Public) (*pb.Transfer, error) {
	if wp != nil && t.HasReceiver(*wp) {
		return t.MarshalProtoForReceiver(ctx, *wp)
	}
	return t.MarshalProto(ctx)
}

// QueryTransfersByID fetches transfers by ID with no status/type/pagination
// filter — under MIMO a whole-transfer status filter is ambiguous, so the caller
// reads per-participant status itself. Access is checked per transfer via
// checkTransferAccessMIMO, matching the legacy by-id path.
func (h *TransferHandler) QueryTransfersByID(ctx context.Context, req *pb.QueryTransfersByIdRequest) (resp *pb.QueryTransfersResponse, err error) {
	ctx, span := tracer.Start(ctx, "TransferHandler.QueryTransfersByID")
	defer span.End()

	start := time.Now()
	defer func() {
		resultCount := 0
		if resp != nil {
			resultCount = len(resp.GetTransfers())
		}
		logQueryTransfersInvocation(ctx, "query_transfers_by_id",
			&pb.TransferFilter{TransferIds: req.GetTransferIds(), Network: req.GetNetwork()},
			time.Since(start),
			zap.Int("result_count", resultCount),
			zap.Error(err),
		)
	}()

	// Non-empty / max-count / UUID / network validation is enforced at the gRPC
	// boundary by ValidationInterceptor (generated Validate() from validate.rules).
	network, err := btcnetwork.FromProtoNetwork(req.GetNetwork())
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("failed to convert proto network to schema network: %w", err))
	}
	transferUUIDs, err := uuids.ParseSlice(req.GetTransferIds())
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("unable to parse transfer IDs as UUIDs: %w", err))
	}

	metrics := newTransferQueryRecorder(transferQueryAttrs{
		QueryPath:      "query_transfers_by_id",
		FilterType:     "none",
		HasTransferIDs: true,
	})

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, sparkerrors.InternalDatabaseReadError(fmt.Errorf("failed to get db from context: %w", err))
	}

	transfers, err := withTransferQueryEdges(
		db.Transfer.Query().Where(
			enttransfer.IDIn(transferUUIDs...),
			enttransfer.NetworkEQ(network),
		),
	).Order(ent.Desc(enttransfer.FieldCreateTime), ent.Desc(enttransfer.FieldID)).All(ctx)
	if err != nil {
		metrics.record(ctx, 0, err)
		return nil, sparkerrors.InternalDatabaseReadError(fmt.Errorf("unable to query transfers by id: %w", err))
	}

	transferProtos := make([]*pb.Transfer, 0, len(transfers))
	accessMap := make(map[keys.Public]bool)
	for _, transfer := range transfers {
		hasReadAccess, accessErr := h.checkTransferAccessMIMO(ctx, transfer, accessMap)
		if accessErr != nil {
			return nil, accessErr
		}
		if !hasReadAccess {
			continue
		}
		transferProto, marshalErr := marshalTransferForWallet(ctx, transfer, nil)
		if marshalErr != nil {
			return nil, fmt.Errorf("unable to marshal transfer: %w", marshalErr)
		}
		transferProtos = append(transferProtos, transferProto)
	}

	// Record the returned count (after the per-transfer access filter), not the raw
	// row count. offset is always -1 — a by-id fetch is a bounded, terminal set.
	metrics.record(ctx, len(transferProtos), nil)
	return &pb.QueryTransfersResponse{Transfers: transferProtos, Offset: -1}, nil
}

// queryTransfers is a critical customer-facing read endpoint — the catch-all
// for shapes no specialized handler claims. With shape-based routing in
// QueryAllTransfers, only nil-participant requests land here, and those must
// carry TransferIds (enforced below) — efficient through direct ID predicates.
func (h *TransferHandler) queryTransfers(ctx context.Context, filter *pb.TransferFilter, pendingOnly bool, isSSP bool) (resp *pb.QueryTransfersResponse, err error) {
	ctx, span := tracer.Start(ctx, "TransferHandler.queryTransfers")
	defer span.End()

	start := time.Now()
	defer func() {
		resultCount := 0
		if resp != nil {
			resultCount = len(resp.GetTransfers())
		}
		logQueryTransfersInvocation(ctx, "query_transfers", filter, time.Since(start),
			zap.Bool("pending_only", pendingOnly),
			zap.Bool("is_ssp", isSSP),
			zap.Int("result_count", resultCount),
			zap.Error(err),
		)
	}()

	if filter.GetLimit() < 0 {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("limit must be non-negative"))
	}
	if filter.GetOffset() < 0 {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("offset must be non-negative"))
	}

	if filter.GetParticipant() == nil && len(filter.GetTransferIds()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "must specify either filter.Participant or filter.TransferIds")
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, sparkerrors.InternalDatabaseReadError(fmt.Errorf("failed to get or create current tx for request: %w", err))
	}

	if pendingOnly && len(filter.GetStatuses()) > 0 {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("cannot specify both pendingOnly=true and filter.Statuses"))
	}

	if filter.GetNetwork() == pb.Network_UNSPECIFIED {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("filter.Network must be specified"))
	}
	network, err := btcnetwork.FromProtoNetwork(filter.GetNetwork())
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("failed to convert proto network to schema network: %w", err))
	}
	var filterType string
	switch filter.GetParticipant().(type) {
	case *pb.TransferFilter_ReceiverIdentityPublicKey:
		filterType = "receiver"
	case *pb.TransferFilter_SenderIdentityPublicKey:
		filterType = "sender"
	case *pb.TransferFilter_SenderOrReceiverIdentityPublicKey:
		filterType = "sender_or_receiver"
	default:
		filterType = "none"
	}
	metrics := newTransferQueryRecorder(transferQueryAttrs{
		QueryPath:       "query_transfers",
		FilterType:      filterType,
		HasStatusFilter: len(filter.GetStatuses()) > 0,
		HasTypeFilter:   len(filter.GetTypes()) > 0,
		HasTransferIDs:  len(filter.GetTransferIds()) > 0,
		PendingOnly:     pendingOnly,
	})

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
	switch filter.GetParticipant().(type) {
	case *pb.TransferFilter_ReceiverIdentityPublicKey:
		receiverIDPubKey, err := keys.ParsePublicKey(filter.GetReceiverIdentityPublicKey())
		if err != nil {
			return nil, sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("invalid receiver identity public key: %w", err))
		}
		transferPredicate = append(transferPredicate, enttransfer.ReceiverIdentityPubkeyEQ(receiverIDPubKey))
		if pendingOnly {
			transferPredicate = append(transferPredicate, enttransfer.StatusIn(receiverPendingStatuses...))
		}
		walletIdentityPubkey = &receiverIDPubKey
	case *pb.TransferFilter_SenderIdentityPublicKey:
		senderIDPubKey, err := keys.ParsePublicKey(filter.GetSenderIdentityPublicKey())
		if err != nil {
			return nil, sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("invalid sender identity public key: %w", err))
		}
		transferPredicate = append(transferPredicate, enttransfer.SenderIdentityPubkeyEQ(senderIDPubKey))
		if pendingOnly {
			transferPredicate = append(transferPredicate,
				enttransfer.StatusIn(senderPendingStatuses...),
				enttransfer.ExpiryTimeLT(time.Now()),
			)
		}
		walletIdentityPubkey = &senderIDPubKey
	case *pb.TransferFilter_SenderOrReceiverIdentityPublicKey:
		identityPubKey, err := keys.ParsePublicKey(filter.GetSenderOrReceiverIdentityPublicKey())
		if err != nil {
			return nil, sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("invalid sender or receiver identity public key: %w", err))
		}
		receiverMatchesIdentity := enttransfer.ReceiverIdentityPubkeyEQ(identityPubKey)
		senderMatchesIdentity := enttransfer.SenderIdentityPubkeyEQ(identityPubKey)
		if pendingOnly {
			transferPredicate = append(transferPredicate, enttransfer.Or(
				enttransfer.And(receiverMatchesIdentity, enttransfer.StatusIn(receiverPendingStatuses...)),
				enttransfer.And(senderMatchesIdentity, enttransfer.StatusIn(senderPendingStatuses...), enttransfer.ExpiryTimeLT(time.Now())),
			))
		} else {
			transferPredicate = append(transferPredicate, enttransfer.Or(receiverMatchesIdentity, senderMatchesIdentity))
		}
		walletIdentityPubkey = &identityPubKey
	default:
		if pendingOnly {
			transferPredicate = append(
				transferPredicate,
				enttransfer.StatusIn(append(senderPendingStatuses, receiverPendingStatuses...)...),
			)
		}
	}

	if !isSSP && walletIdentityPubkey != nil {
		hasReadAccess, err := NewWalletSettingHandler(h.config).HasReadAccessToWallet(ctx, *walletIdentityPubkey)
		if err != nil {
			return nil, fmt.Errorf("failed to check if viewer has read access to wallet %s: %w", walletIdentityPubkey, err)
		}
		if !hasReadAccess {
			return &pb.QueryTransfersResponse{
				Offset: -1,
			}, nil
		}
	}

	if len(filter.GetTransferIds()) > 0 {
		if len(filter.GetTransferIds()) > maxTransferIDFilterValues {
			return nil, sparkerrors.InvalidArgumentOutOfRange(fmt.Errorf("there were %d transfer ids provided, but the max is %d", len(filter.GetTransferIds()), maxTransferIDFilterValues))
		}
		transferUUIDs, err := uuids.ParseSlice(filter.GetTransferIds())
		if err != nil {
			return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("unable to parse transfer IDs as UUIDs: %w", err))
		}
		transferPredicate = append(transferPredicate, enttransfer.IDIn(transferUUIDs...))
	}

	if len(filter.GetTypes()) > 0 {
		transferTypes := make([]st.TransferType, len(filter.GetTypes()))
		for i, protoType := range filter.GetTypes() {
			schemaType, err := st.TransferTypeFromProto(protoType.String())
			if err != nil {
				return nil, status.Errorf(codes.InvalidArgument, "invalid transfer type: %s", protoType.String())
			}
			transferTypes[i] = schemaType
		}
		transferPredicate = append(transferPredicate, enttransfer.TypeIn(transferTypes...))
	}

	transferPredicate = append(transferPredicate, enttransfer.NetworkEQ(network))

	if len(filter.GetStatuses()) > 0 {
		statuses := make([]st.TransferStatus, len(filter.GetStatuses()))
		for i, stat := range filter.GetStatuses() {
			var err error
			statuses[i], err = ent.TransferStatusSchema(stat)
			if err != nil {
				return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid transfer status: %w", err))
			}
		}
		transferPredicate = append(transferPredicate, enttransfer.StatusIn(statuses...))
	}

	if filter.GetCreatedAfter() != nil && filter.GetCreatedBefore() != nil {
		return nil, status.Error(codes.InvalidArgument, "cannot specify both created_after and created_before filters")
	}

	if filter.GetCreatedAfter() != nil {
		createdAfter := filter.GetCreatedAfter().AsTime().UTC()
		transferPredicate = append(transferPredicate, enttransfer.CreateTimeGT(createdAfter))
	} else if filter.GetCreatedBefore() != nil {
		createdBefore := filter.GetCreatedBefore().AsTime().UTC()
		transferPredicate = append(transferPredicate, enttransfer.CreateTimeLT(createdBefore))
	}

	baseQuery := withTransferQueryEdges(db.Transfer.Query())
	if len(transferPredicate) > 0 {
		baseQuery = baseQuery.Where(enttransfer.And(transferPredicate...))
	}

	// ORDER BY create_time only — tied rows return in indeterminate Postgres
	// order. Pre-existing; the MIMO paths fix this by ordering on (create_time, id).
	var query *ent.TransferQuery
	if filter.GetOrder() == pb.Order_ASCENDING {
		query = baseQuery.Order(ent.Asc(enttransfer.FieldCreateTime))
	} else {
		query = baseQuery.Order(ent.Desc(enttransfer.FieldCreateTime))
	}

	if filter.GetLimit() > maxTransferPageSize || filter.GetLimit() == 0 {
		filter.Limit = maxTransferPageSize
	}
	query = query.Limit(int(filter.GetLimit()))

	if filter.GetOffset() > 0 {
		query = query.Offset(int(filter.GetOffset()))
	}

	transfers, err := query.All(ctx)
	metrics.record(ctx, len(transfers), err)
	if err != nil {
		return nil, fmt.Errorf("unable to query transfers: %w", err)
	}

	// Pre-existing bug: pagination already ran, so dropping access-rejected rows here
	// returns a short page.
	var transferProtos []*pb.Transfer
	accessMap := make(map[keys.Public]bool)
	for _, transfer := range transfers {
		if walletIdentityPubkey == nil && !isSSP {
			// No participant filter scoped the query, so check viewer access per row.
			var hasReadAccess bool
			hasReadAccess, err = h.checkTransferAccessMIMO(ctx, transfer, accessMap)
			if err != nil {
				return nil, err
			}
			if !hasReadAccess {
				continue
			}
		}

		transferProto, marshalErr := marshalTransferForWallet(ctx, transfer, walletIdentityPubkey)
		if marshalErr != nil {
			return nil, fmt.Errorf("unable to marshal transfer: %w", marshalErr)
		}
		transferProtos = append(transferProtos, transferProto)
	}

	var nextOffset int64
	if len(transfers) == int(filter.GetLimit()) {
		nextOffset = filter.GetOffset() + int64(len(transfers))
	} else {
		nextOffset = -1
	}

	return &pb.QueryTransfersResponse{
		Transfers: transferProtos,
		Offset:    nextOffset,
	}, nil
}

// participantPubkeyHex returns a lowercase-hex representation of the pubkey
// from filter.Participant, or "" if no (non-empty) participant is set.
// QueryPendingTransfers routes "" to legacy queryTransfers, since the MIMO
// path requires a participant to scope the query.
//
// Each proto-generated Get* accessor returns nil if a different variant
// is set, so we walk the three variants in order and hex-encode whichever
// is non-nil. hex.EncodeToString(nil) returns "".
func participantPubkeyHex(filter *pb.TransferFilter) string {
	pk := filter.GetReceiverIdentityPublicKey()
	if pk == nil {
		pk = filter.GetSenderIdentityPublicKey()
	}
	if pk == nil {
		pk = filter.GetSenderOrReceiverIdentityPublicKey()
	}
	return hex.EncodeToString(pk)
}

func (h *TransferHandler) QueryPendingTransfers(ctx context.Context, filter *pb.TransferFilter) (*pb.QueryTransfersResponse, error) {
	// Validate pagination up front so both the MIMO and legacy paths reject
	// malformed input identically (the MIMO path doesn't re-check).
	if filter.GetLimit() < 0 {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("limit must be non-negative"))
	}
	if filter.GetOffset() < 0 {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("offset must be non-negative"))
	}

	// The MIMO path needs a participant to scope its per-participant query.
	// Requests without one (e.g. by-transfer-id lookups) fall back to legacy
	// queryTransfers.
	if participantPubkeyHex(filter) != "" {
		return h.queryPendingTransfersMIMO(ctx, filter)
	}
	queryPendingNilParticipantFallback.Add(ctx, 1)
	return h.queryTransfers(ctx, filter, true, false)
}

func (h *TransferHandler) QueryAllTransfers(ctx context.Context, filter *pb.TransferFilter, isSSP bool) (*pb.QueryTransfersResponse, error) {
	if shouldRouteToByTypes(filter) {
		return h.queryByTypes(ctx, filter, isSSP)
	}
	if shouldRouteToOutgoingInFlight(filter) {
		return h.queryOutgoingInFlight(ctx, filter, isSSP)
	}
	if shouldRouteToReceiverByTypeStatus(filter) {
		return h.queryReceiverByTypeStatus(ctx, filter, isSSP)
	}
	if shouldRouteToCounterSwap(filter) {
		return h.queryCounterSwap(ctx, filter, isSSP)
	}
	if shouldRouteToByParticipantFallback(filter) {
		return h.queryByParticipantFallback(ctx, filter, isSSP)
	}
	return h.queryTransfers(ctx, filter, false, isSSP)
}

// queryPendingTransfersMIMO is the MIMO-native implementation of
// QueryPendingTransfers. The routing in QueryPendingTransfers guarantees:
//   - filter.Participant is non-nil (caller-required; nil-participant traffic
//     is counted and routed to legacy queryTransfers)
//   - this is the pendingOnly path; isSSP is irrelevant — the public RPC
//     never sets it. SSP traffic lands here.
//
// Status filtering is partitioned by participant role:
//   - receiver-side filters on transfer_receivers.status (per-receiver claim
//     state) for the 5 pending statuses (RECEIVER_CLAIM_PENDING + 4
//     RECEIVER_*). Drives idx_transferreceiver_claim_pending_pubkey_time.
//   - sender-side filters on transfers.status + expiry_time < args.now.
//
// Marshaling: when the participant is a receiver of the transfer,
// MarshalProtoForReceiver filters leaves to just that receiver's leaves
// (claim flow). For multi-receiver MIMO transfers this is load-bearing.
//
// The participant-nil branch from legacy queryTransfers is intentionally
// not implemented here — the routing layer handles it.
func (h *TransferHandler) queryPendingTransfersMIMO(ctx context.Context, filter *pb.TransferFilter) (resp *pb.QueryTransfersResponse, err error) {
	ctx, span := tracer.Start(ctx, "TransferHandler.queryPendingTransfersMIMO")
	defer span.End()

	start := time.Now()
	defer func() {
		resultCount := 0
		if resp != nil {
			resultCount = len(resp.GetTransfers())
		}
		logQueryTransfersInvocation(ctx, "query_pending_transfers", filter, time.Since(start),
			zap.Int("result_count", resultCount),
			zap.Error(err),
		)
	}()

	if filter.GetParticipant() == nil {
		// Defensive: routing should have sent us to legacy. If we got here
		// with nil participant, the routing has a bug.
		return nil, status.Error(codes.InvalidArgument, "queryPendingTransfersMIMO requires a participant")
	}
	if len(filter.GetStatuses()) > 0 {
		return nil, status.Error(codes.InvalidArgument, "cannot specify filter.Statuses on QueryPendingTransfers")
	}
	if filter.GetCreatedAfter() != nil && filter.GetCreatedBefore() != nil {
		return nil, status.Error(codes.InvalidArgument, "cannot specify both created_after and created_before filters")
	}
	if filter.GetNetwork() == pb.Network_UNSPECIFIED {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("filter.Network must be specified"))
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get db from context: %w", err)
	}

	walletIdentityPubkey, role, filterType, err := extractParticipant(filter)
	if err != nil {
		return nil, err
	}

	metrics := newTransferQueryRecorder(transferQueryAttrs{
		QueryPath:      "query_pending_transfers",
		FilterType:     filterType,
		HasTypeFilter:  len(filter.GetTypes()) > 0,
		HasTransferIDs: len(filter.GetTransferIds()) > 0,
		PendingOnly:    true,
	})

	// Upfront access check.
	hasReadAccess, err := NewWalletSettingHandler(h.config).HasReadAccessToWallet(ctx, walletIdentityPubkey)
	if err != nil {
		return nil, fmt.Errorf("failed to check read access for wallet %s: %w", walletIdentityPubkey, err)
	}
	if !hasReadAccess {
		metrics.record(ctx, 0, nil)
		return &pb.QueryTransfersResponse{Offset: -1}, nil
	}

	limit, offset := normalizeTransferPagination(filter.GetLimit(), filter.GetOffset())

	// Step 1: raw-SQL UNION ALL returning paginated transfer IDs. All filter
	// predicates and pagination are applied here so step 2 just loads by ID.
	transferIDs, err := queryMIMOPendingTransferIDs(ctx, db, queryMIMOPendingArgs{
		participant:       role,
		walletPubkey:      walletIdentityPubkey,
		network:           filter.GetNetwork(),
		types:             filter.GetTypes(),
		transferIDsFilter: filter.GetTransferIds(),
		createdAfter:      timeOrZero(filter.GetCreatedAfter()),
		createdBefore:     timeOrZero(filter.GetCreatedBefore()),
		hasCreatedAfter:   filter.GetCreatedAfter() != nil,
		hasCreatedBefore:  filter.GetCreatedBefore() != nil,
		order:             filter.GetOrder(),
		limit:             limit,
		offset:            offset,
		now:               time.Now(),
	})
	if err != nil {
		metrics.record(ctx, 0, err)
		return nil, fmt.Errorf("failed to query pending transfer IDs: %w", err)
	}
	if len(transferIDs) == 0 {
		metrics.record(ctx, 0, nil)
		return &pb.QueryTransfersResponse{Offset: -1}, nil
	}

	// Step 2: load transfers + edges by ID. Both ORDER BY columns must use the
	// same direction as the step-1 SQL (`ORDER BY create_time <dir>, id <dir>`);
	// mismatching the secondary sort reverses tied-row order on transfers that
	// share a `create_time`.
	transferProtos, err := loadAndMarshalTransfersByIDs(ctx, db, transferIDs, walletIdentityPubkey, filter.GetOrder())
	metrics.record(ctx, len(transferProtos), err)
	if err != nil {
		return nil, err
	}

	// Gate and advance by SQL ID count, not ORM count — concurrent deletes shouldn't reshape pagination.
	nextOffset := int64(-1)
	if len(transferIDs) == limit {
		nextOffset = int64(offset + len(transferIDs))
	}
	return &pb.QueryTransfersResponse{
		Transfers: transferProtos,
		Offset:    nextOffset,
	}, nil
}

func normalizeTransferPagination(limit, offset int64) (int, int) {
	if limit <= 0 || limit > maxTransferPageSize {
		limit = maxTransferPageSize
	}
	offset = max(offset, 0)
	return int(limit), int(offset)
}

func timeOrZero(ts *timestamppb.Timestamp) time.Time {
	if ts == nil {
		return time.Time{}
	}
	return ts.AsTime().UTC()
}

// maxTransferPageSize caps the LIMIT on QueryPendingTransfers /
// QueryAllTransfers responses.
const maxTransferPageSize = 100

// maxTransferIDFilterValues caps caller-provided transfer ID filters before
// UUID parsing and SQL IN predicate construction.
const maxTransferIDFilterValues = 1000

// participantRole enumerates which side of a transfer a query targets:
// receiver, sender, or either (sender_or_receiver). Lets handlers extract
// the wallet pubkey from the proto oneof once and then dispatch to per-arm
// SQL builders without repeating the type-switch downstream.
type participantRole int

const (
	participantRoleReceiver participantRole = iota
	participantRoleSender
	participantRoleSenderOrReceiver
)

type queryMIMOPendingArgs struct {
	participant       participantRole
	walletPubkey      keys.Public
	network           pb.Network
	types             []pb.TransferType
	transferIDsFilter []string
	createdAfter      time.Time
	createdBefore     time.Time
	hasCreatedAfter   bool
	hasCreatedBefore  bool
	order             pb.Order
	limit             int
	offset            int
	now               time.Time // sampled at handler entry; bound into the sender-pending expiry predicate
}

// queryMIMOPendingTransferIDs returns paginated pending-transfer IDs ordered
// by create_time and id (direction per args.order). Dispatches to the per-arm
// SQL builder matching args.participant. Each arm produces a stream sorted
// on (create_time, id) with consistent column aliases at the union level so
// the planner picks Merge Append for the UNION ALL variant.
//
// Receiver-arm ordering uses r.create_time. The cross-participant
// create_time invariant (app-layer enforced) makes this equivalent
// to t.create_time, keeping the union's merge key uniform across
// both arms.
func queryMIMOPendingTransferIDs(ctx context.Context, db *ent.Client, args queryMIMOPendingArgs) ([]uuid.UUID, error) {
	if args.now.IsZero() {
		return nil, sparkerrors.InternalObjectMissingField(fmt.Errorf("queryMIMOPendingArgs.now is required"))
	}

	var query string
	var sqlArgs []any
	var err error

	switch args.participant {
	case participantRoleReceiver:
		query, sqlArgs, err = buildPendingIDsQueryReceiver(args)
	case participantRoleSender:
		query, sqlArgs, err = buildPendingIDsQuerySender(args)
	case participantRoleSenderOrReceiver:
		query, sqlArgs, err = buildPendingIDsQuerySenderOrReceiver(args)
	default:
		return nil, fmt.Errorf("unsupported participant role: %d", args.participant)
	}
	if err != nil {
		return nil, err
	}

	//nolint:forbidigo // raw SQL needed for partial-index-driven query with role-partitioned status filter.
	rows, err := db.QueryContext(ctx, query, sqlArgs...)
	if err != nil {
		return nil, fmt.Errorf("failed to execute pending-transfer-IDs query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	ids := make([]uuid.UUID, 0, args.limit)
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("failed to scan pending transfer ID: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// buildPendingIDsQueryReceiver builds the receiver-only pending-IDs query.
// Drives idx_transferreceiver_claim_pending_pubkey_time:
//
//	transfer_receivers (identity_pubkey, create_time DESC, transfer_id DESC)
//	WHERE status IN (RECEIVER_CLAIM_PENDING + 4 RECEIVER_* statuses)
//
// Outer ordering uses r.create_time, equivalent to t.create_time under the
// cross-participant create_time invariant.
func buildPendingIDsQueryReceiver(args queryMIMOPendingArgs) (string, []any, error) {
	sqlArgs := []any{
		args.walletPubkey.Serialize(),            // $1 - identity_pubkey
		pq.Array(mimo.PendingReceiverStatuses()), // $2 - r.status
		args.limit,                               // $3 - LIMIT
		args.offset,                              // $4 - OFFSET
	}

	sqlArgs, commonFilters, err := mimo.AppendPendingCommonFilters(sqlArgs, args.network, args.types, args.transferIDsFilter)
	if err != nil {
		return "", nil, err
	}
	sqlArgs, timeFilter := mimo.AppendPendingTimeFilter(
		sqlArgs,
		args.hasCreatedAfter, args.createdAfter,
		args.hasCreatedBefore, args.createdBefore,
		mimo.ReceiverCreateTimeColumn,
	)

	direction := "DESC"
	if args.order == pb.Order_ASCENDING {
		direction = "ASC"
	}

	query := fmt.Sprintf(`
		SELECT r.transfer_id
		FROM transfer_receivers r
		INNER JOIN transfers t ON t.id = r.transfer_id
		WHERE r.identity_pubkey = $1
		  AND r.status = ANY($2::text[])%s%s
		ORDER BY r.create_time %s, r.transfer_id %s
		LIMIT $3 OFFSET $4
	`, commonFilters, timeFilter, direction, direction)

	return query, sqlArgs, nil
}

// buildPendingIDsQuerySender builds the sender-only pending-IDs query.
// Joins transfer_senders for pubkey scope via UNIQUE(transfer_id,
// identity_pubkey). Sender-pending requires expiry_time < args.now — the
// sender has missed its handoff window.
//
// **Known planner-flip footgun (MIMO MVP only).** With the new
// idx_transfers_pending_sender_pubkey_time partial in scope, the planner can
// pick that partial for this JOIN-based query at medium cardinality —
// without the leading-equality predicate it needs (the query supplies
// s.identity_pubkey, not t.sender_identity_pubkey), so the partial gets
// walked without the leading-column scope, materialized, sorted, and
// JOINed. ~80 ms warm at ~100 sender-pending vs <1 ms in the column-based
// shape. Acceptable to ship as-is because (i) participant=Sender has zero
// production callers per audit on the parent PR, and (ii) SP-2914 retires
// both this shape and the SR sender arm in lockstep when transfer_senders
// gets a denormalized status column + its own partial — at which point both
// arms switch to JOIN-with-s.status filtering, mirroring the receiver arm.
// Until then, this code path's perf is bounded by the no-prod-callers fact,
// not by planner stability.
//
// See buildPendingIDsQuerySenderOrReceiver below for the contrasting
// column-based sender-arm shape used by the sender_or_receiver path — same
// logical query, different physical predicate (t.sender_identity_pubkey
// directly), driving the new partial cleanly without the planner-flip risk.
func buildPendingIDsQuerySender(args queryMIMOPendingArgs) (string, []any, error) {
	sqlArgs := []any{
		args.walletPubkey.Serialize(),          // $1 - identity_pubkey
		pq.Array(mimo.PendingSenderStatuses()), // $2 - t.status
		args.limit,                             // $3 - LIMIT
		args.offset,                            // $4 - OFFSET
	}

	sqlArgs = append(sqlArgs, args.now)
	nowPos := len(sqlArgs)

	sqlArgs, commonFilters, err := mimo.AppendPendingCommonFilters(sqlArgs, args.network, args.types, args.transferIDsFilter)
	if err != nil {
		return "", nil, err
	}
	sqlArgs, timeFilter := mimo.AppendPendingTimeFilter(
		sqlArgs,
		args.hasCreatedAfter, args.createdAfter,
		args.hasCreatedBefore, args.createdBefore,
		mimo.SenderCreateTimeColumn,
	)

	direction := "DESC"
	if args.order == pb.Order_ASCENDING {
		direction = "ASC"
	}

	query := fmt.Sprintf(`
		SELECT t.id
		FROM transfers t
		INNER JOIN transfer_senders s ON s.transfer_id = t.id AND s.identity_pubkey = $1
		WHERE t.status = ANY($2::text[])
		  AND t.expiry_time < $%d%s%s
		ORDER BY t.create_time %s, t.id %s
		LIMIT $3 OFFSET $4
	`, nowPos, commonFilters, timeFilter, direction, direction)

	return query, sqlArgs, nil
}

// buildPendingIDsQuerySenderOrReceiver UNIONs the sender and receiver arms,
// applies the outer ORDER BY on the merged stream, then paginates. Per-arm
// LIMIT is offset+limit so the outer pagination has enough candidates.
//
// Both arms apply the same network / types / transfer_ids filters. Time
// filters use each arm's own table column (t.create_time vs r.create_time)
// sharing the same param positions for index-drive efficiency.
//
// **Asymmetric arm structure (intentional, MIMO MVP only):**
//
// Receiver arm uses transfer_receivers + idx_transferreceiver_claim_pending_pubkey_time
// (per-row receiver status, multi-receiver-ready today). Sender arm uses the
// direct column predicate t.sender_identity_pubkey + idx_transfers_pending_sender_pubkey_time
// rather than JOINing transfer_senders. This is correct in MIMO MVP because
// every transfer has exactly one sender (transfers.sender_identity_pubkey ==
// transfer_senders.identity_pubkey 1:1), and it sidesteps a planner-flip on
// the JOIN-based shape that wipes out at medium-cardinality pubkeys (~10s
// vs ~10ms; verified with EXPLAIN ANALYZE against a 65M-row dbseed).
//
// When MIMO v1 multi-sender lands (SP-2208), this asymmetry must be retired:
// transfer_senders gets a status column, sender arm switches to filter on
// s.status directly, and idx_transfers_pending_sender_pubkey_time is dropped
// in favor of an edge-table partial mirroring the receiver pattern. Tracked
// in SP-2914.
//
// The sender_or_receiver path returns each transfer at most once thanks to two invariants:
//
//  1. PendingSenderStatuses() ∩ PendingReceiverStatuses() = ∅ — while t.status
//     is sender-pending, receivers are still INITIATED (not receiver-pending);
//     the sender→receiver handoff flips t.status out of PendingSenderStatuses
//     as it marks receivers RECEIVER_CLAIM_PENDING. So a transfer matches at
//     most one arm's filter. Locked by TestPendingStatusesDisjoint in so/mimo.
//
//  2. UNIQUE(transfer_id, identity_pubkey) on transfer_receivers + the
//     single-sender-per-transfer invariant (MIMO MVP) — at most one row per
//     (transfer, pubkey) per arm.
//
// If invariant #1 is ever broken (a status added to both sets, a state-
// machine path that produces overlap), the sender_or_receiver path silently
// emits duplicates and the dual-role fixture in the equivalence test doesn't catch it because
// the statuses it uses are in receiverPending only. The unit test is the
// guard.
func buildPendingIDsQuerySenderOrReceiver(args queryMIMOPendingArgs) (string, []any, error) {
	perArmLimit := args.offset + args.limit

	sqlArgs := []any{
		args.walletPubkey.Serialize(),            // $1 - identity_pubkey (used by both arms)
		pq.Array(mimo.PendingSenderStatuses()),   // $2 - sender pending statuses
		pq.Array(mimo.PendingReceiverStatuses()), // $3 - receiver pending statuses
		perArmLimit,                              // $4 - per-arm LIMIT
		args.limit,                               // $5 - outer LIMIT
		args.offset,                              // $6 - outer OFFSET
	}

	sqlArgs = append(sqlArgs, args.now)
	nowPos := len(sqlArgs)

	sqlArgs, commonFilters, err := mimo.AppendPendingCommonFilters(sqlArgs, args.network, args.types, args.transferIDsFilter)
	if err != nil {
		return "", nil, err
	}

	// Bind time-filter args once and reference from both arms with each
	// arm's own column alias.
	var senderTimeFilter, receiverTimeFilter strings.Builder
	if args.hasCreatedAfter {
		sqlArgs = append(sqlArgs, args.createdAfter)
		pos := len(sqlArgs)
		fmt.Fprintf(&senderTimeFilter, " AND t.create_time > $%d", pos)
		fmt.Fprintf(&receiverTimeFilter, " AND r.create_time > $%d", pos)
	}
	if args.hasCreatedBefore {
		sqlArgs = append(sqlArgs, args.createdBefore)
		pos := len(sqlArgs)
		fmt.Fprintf(&senderTimeFilter, " AND t.create_time < $%d", pos)
		fmt.Fprintf(&receiverTimeFilter, " AND r.create_time < $%d", pos)
	}

	direction := "DESC"
	if args.order == pb.Order_ASCENDING {
		direction = "ASC"
	}

	query := fmt.Sprintf(`
		SELECT id FROM (
			(SELECT t.id AS id, t.create_time AS ct
			 FROM transfers t
			 WHERE t.sender_identity_pubkey = $1
			   AND t.status = ANY($2::text[])
			   AND t.expiry_time < $%d%s%s
			 ORDER BY t.create_time %s, t.id %s
			 LIMIT $4)
			UNION ALL
			(SELECT r.transfer_id AS id, r.create_time AS ct
			 FROM transfer_receivers r
			 INNER JOIN transfers t ON t.id = r.transfer_id
			 WHERE r.identity_pubkey = $1
			   AND r.status = ANY($3::text[])%s%s
			 ORDER BY r.create_time %s, r.transfer_id %s
			 LIMIT $4)
		) u
		ORDER BY ct %s, id %s
		LIMIT $5 OFFSET $6
	`,
		nowPos, commonFilters, senderTimeFilter.String(), direction, direction,
		commonFilters, receiverTimeFilter.String(), direction, direction,
		direction, direction)

	return query, sqlArgs, nil
}

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
		return fmt.Errorf("failed to find coop exit for transfer %s: %w", transfer.ID, err)
	}

	transferLeaves, err := transfer.QueryTransferLeaves().All(ctx)
	if err != nil {
		return fmt.Errorf("failed to find leaves for transfer %s: %w", transfer.ID, err)
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
	requiredConfirmations := int64(knobs.CoopExitConfirmationThreshold)
	if blockHeight.Height-*coopExit.ConfirmationHeight+1 < requiredConfirmations {
		return sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("coop exit tx doesn't have enough confirmations: confirmation height: %d current block height: %d", *coopExit.ConfirmationHeight, blockHeight.Height))
	}
	return nil
}

// claimLockConflictError logs the underlying Postgres FOR UPDATE NOWAIT
// failure and returns the client-facing AbortedConcurrentClaimConflict for
// the three claim_transfer entry points. The wire message is generic ("locked
// by another operation") because the same row lock is held during many
// sender/receiver state transitions and we don't want to overpromise
// specificity to integrators. The original Postgres error stays available via
// errors.Unwrap on the returned grpcError for server-side debugging.
func claimLockConflictError(ctx context.Context, transferID uuid.UUID, lockErr error) error {
	logging.GetLoggerFromContext(ctx).With(zap.Error(lockErr)).Sugar().Infof(
		"concurrent claim conflict on transfer %s", transferID)
	return sparkerrors.AbortedConcurrentClaimConflict(
		fmt.Errorf("transfer %s is currently locked by another operation; please retry", transferID))
}

// claimLeafKeyTweakProofsDigest returns a SHA-256 over the ordered VSS proofs
// in a ClaimLeafKeyTweak. The proofs are the tweak polynomial's public
// commitments and identical across every SO's slice, so equal digests mean
// equal polynomials. Used to detect (and refuse) claims that would tweak
// keyshares with divergent polynomials across SOs. Returns nil for a tweak
// with no secret share tweak.
func claimLeafKeyTweakProofsDigest(tweak *pb.ClaimLeafKeyTweak) []byte {
	if tweak == nil || tweak.GetSecretShareTweak() == nil {
		return nil
	}
	h := sha256.New()
	var lenBuf [4]byte
	for _, p := range tweak.GetSecretShareTweak().GetProofs() {
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(p)))
		h.Write(lenBuf[:])
		h.Write(p)
	}
	return h.Sum(nil)
}

// hashClaimLeafKeyTweakProofs is the hex form of claimLeafKeyTweakProofsDigest
// for operator logs.
func hashClaimLeafKeyTweakProofs(tweak *pb.ClaimLeafKeyTweak) string {
	digest := claimLeafKeyTweakProofsDigest(tweak)
	if digest == nil {
		return "<nil>"
	}
	return hex.EncodeToString(digest)
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
	if err := authz.EnforceWalletNotKillSwitched(ctx, reqOwnerIDPubKey); err != nil {
		return err
	}

	transferID, err := uuid.Parse(req.GetTransferId())
	if err != nil {
		return fmt.Errorf("invalid transfer ID: %w", err)
	}

	transfer, err := h.loadTransferForUpdate(ctx, transferID, sql.WithLockAction(sql.NoWait))
	if err != nil {
		if sparkdb.IsLockNotAvailableError(err) {
			return claimLockConflictError(ctx, transferID, err)
		}
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
	// If the transfer is already past the key-tweak phase, return success
	// for idempotency. This handles the case where a concurrent or retried
	// ClaimTransferTweakKeys call arrives after the first one already
	// advanced the transfer to RECEIVER_KEY_TWEAKED or beyond.
	switch transfer.Status {
	case st.TransferStatusSenderKeyTweaked:
		// Expected status — proceed with key tweaking
	case st.TransferStatusReceiverKeyTweaked,
		st.TransferStatusReceiverKeyTweakLocked,
		st.TransferStatusReceiverKeyTweakApplied,
		st.TransferStatusReceiverRefundSigned:
		// Already past the tweak-keys phase — return success for idempotency
		return nil
	default:
		return sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("transfer %s is not in a claimable status: %s", transferID, transfer.Status))
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to get or create current tx for request: %w", err)
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
	if len(transferLeaves) != len(req.GetLeavesToReceive()) {
		return fmt.Errorf("inconsistent leaves to claim for transfer %s", transferID)
	}

	leafMap := make(map[string]*ent.TransferLeaf)
	for _, leaf := range transferLeaves {
		leafMap[leaf.Edges.Leaf.ID.String()] = leaf
	}

	// Store key tweaks - batch all updates into a single SQL statement
	leafIDs := make([]uuid.UUID, 0, len(req.GetLeavesToReceive()))
	keyTweakValues := make([][]byte, 0, len(req.GetLeavesToReceive()))
	seenLeafIDs := make(map[string]struct{}, len(req.GetLeavesToReceive()))
	for _, leafTweak := range req.GetLeavesToReceive() {
		leafID, err := uuid.Parse(leafTweak.GetLeafId())
		if err != nil {
			return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid leaf id %s: %w", leafTweak.GetLeafId(), err))
		}
		leafIDString := leafID.String()
		if _, exists := seenLeafIDs[leafIDString]; exists {
			return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("duplicate leaf id: %s", leafIDString))
		}
		seenLeafIDs[leafIDString] = struct{}{}

		leaf, exists := leafMap[leafIDString]
		if !exists {
			return fmt.Errorf("unexpected leaf id %s", leafIDString)
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
		for _, leafTweak := range req.GetLeavesToReceive() {
			logging.GetLoggerFromContext(ctx).Sugar().Infof(
				"claim key tweak stored (coordinator) for transfer %s leaf %s: num_proofs=%d proofs_hash=%s",
				transferID, leafTweak.GetLeafId(), len(leafTweak.GetSecretShareTweak().GetProofs()), hashClaimLeafKeyTweakProofs(leafTweak),
			)
		}
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

// validateClaimLeafTweak validates a stored receiver key tweak against the
// leaf and returns the parsed tweak components. The keyshare rotation itself
// is applied for all leaves at once via ent.TweakSigningKeyshares in the
// SettleReceiverKeyTweak commit loop.
func (h *TransferHandler) validateClaimLeafTweak(leaf *ent.TreeNode, req *pb.ClaimLeafKeyTweak) (keys.Private, keys.Public, map[string]keys.Public, error) {
	if req.GetSecretShareTweak() == nil {
		return keys.Private{}, keys.Public{}, nil, fmt.Errorf("secret share tweak is required")
	}
	if len(req.GetSecretShareTweak().GetSecretShare()) == 0 {
		return keys.Private{}, keys.Public{}, nil, fmt.Errorf("secret share is required")
	}
	err := secretsharing.ValidateShare(
		&secretsharing.VerifiableSecretShare{
			SecretShare: secretsharing.SecretShare{
				FieldModulus: secp256k1.S256().N,
				Threshold:    int(h.config.Threshold),
				Index:        big.NewInt(int64(h.config.Index + 1)),
				Share:        new(big.Int).SetBytes(req.GetSecretShareTweak().GetSecretShare()),
			},
			Proofs: req.GetSecretShareTweak().GetProofs(),
		},
	)
	if err != nil {
		return keys.Private{}, keys.Public{}, nil, fmt.Errorf("unable to validate share: %w", err)
	}

	// A leaf that exited (or is exiting) to L1 mid-transfer stays claimable:
	// the receiver already owns the funds, and claiming lets the watchtower
	// broadcast the newest refund tx for them instead of forcing a unilateral
	// exit of the transfer's remaining leaves. Only ownership and the keyshare
	// are updated — the leaf keeps its on-chain status.
	if leaf.Status != st.TreeNodeStatusTransferLocked && !leaf.Status.IsExitedToL1() {
		return keys.Private{}, keys.Public{}, nil, sparkerrors.FailedPreconditionInvalidState(
			fmt.Errorf("leaf %s must be %s or exited to L1 to claim receiver key tweak, got %s", leaf.ID, st.TreeNodeStatusTransferLocked, leaf.Status),
		)
	}

	secretShare, err := keys.ParsePrivateKey(req.GetSecretShareTweak().GetSecretShare())
	if err != nil {
		return keys.Private{}, keys.Public{}, nil, fmt.Errorf("unable to parse secret share: %w", err)
	}
	pubKeyTweak, err := keys.ParsePublicKey(req.GetSecretShareTweak().GetProofs()[0])
	if err != nil {
		return keys.Private{}, keys.Public{}, nil, fmt.Errorf("unable to parse public key: %w", err)
	}
	pubKeySharesTweak, err := keys.ParsePublicKeyMap(req.GetPubkeySharesTweak())
	if err != nil {
		return keys.Private{}, keys.Public{}, nil, fmt.Errorf("unable to parse public key shares tweaks: %w", err)
	}
	if err := helper.ValidatePubkeySharesTweak(h.config, req.GetSecretShareTweak().GetProofs(), pubKeySharesTweak); err != nil {
		return keys.Private{}, keys.Public{}, nil, fmt.Errorf("invalid pubkey_shares_tweak for leaf %s: %w", leaf.ID, err)
	}
	return secretShare, pubKeyTweak, pubKeySharesTweak, nil
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
		return nil, fmt.Errorf("unable to get leaves for transfer %s: %w", transfer.ID, err)
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
			return fmt.Errorf("tree node edge not loaded for transfer leaf %s: ensure WithLeaf() is used when querying", leaf.ID)
		}
		proof, exists := keyTweakProofs[treeNode.ID.String()]
		if !exists {
			return fmt.Errorf("key tweak proof for leaf %s not found", leaf.ID)
		}
		if proof == nil {
			return fmt.Errorf("key tweak proof value is nil for leaf %s", leaf.ID)
		}
		keyTweakProto := &pb.ClaimLeafKeyTweak{}
		err := proto.Unmarshal(leaf.KeyTweak, keyTweakProto)
		if err != nil {
			return fmt.Errorf("unable to unmarshal key tweak for leaf %s: %w", leaf.ID, err)
		}
		if keyTweakProto.GetSecretShareTweak() == nil {
			return fmt.Errorf("missing secret share tweak for leaf %s", leaf.ID)
		}
		if len(keyTweakProto.GetSecretShareTweak().GetProofs()) != len(proof.GetProofs()) {
			return fmt.Errorf("leaf %s has %d proofs but %d were provided", leaf.ID, len(keyTweakProto.GetSecretShareTweak().GetProofs()), len(proof.GetProofs()))
		}
		for i, p := range proof.GetProofs() {
			if !bytes.Equal(keyTweakProto.GetSecretShareTweak().GetProofs()[i], p) {
				return sparkerrors.AbortedConcurrentClaimConflict(fmt.Errorf("key tweak proof for leaf %s is invalid, the proof provided is not the same as key tweak proof. please check your implementation to see if you are claiming the same transfer multiple times at the same time", leaf.ID))
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

	if receiver != nil {
		switch receiver.Status {
		case st.TransferReceiverStatusKeyTweakApplied,
			st.TransferReceiverStatusRefundSigned,
			st.TransferReceiverStatusCompleted:
			return fmt.Errorf("transfer %s key tweak is already applied, cannot revert it", transfer.ID)
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
			return fmt.Errorf("transfer %s key tweak is already applied, cannot revert it", transfer.ID)
		case st.TransferStatusReceiverKeyTweakLocked,
			st.TransferStatusReceiverKeyTweaked:
			// ok to revert
		default:
			return nil
		}
	}

	// Revert this receiver to RECEIVER_CLAIM_PENDING so it can retry, and the
	// parent to SENDER_KEY_TWEAKED. Only the passed-in receiver is touched —
	// for multi-receiver the others advance independently on their own edge
	// rows, and under receiver-authoritative status the parent never left
	// SENDER_KEY_TWEAKED (its revert is then a no-op).
	// MIMO - Dual write status changes
	_, err := transfer.Update().SetStatus(st.TransferStatusSenderKeyTweaked).Save(ctx)
	if err != nil {
		return fmt.Errorf("unable to update transfer status %v: %w", transfer.ID, err)
	}
	if receiver != nil {
		_, err = receiver.Update().SetStatus(st.TransferReceiverStatusReceiverClaimPending).Save(ctx)
		if err != nil {
			return fmt.Errorf("unable to update transfer receiver status %v: %w", receiver.ID, err)
		}
	}

	// Revert key tweaks and restore each leaf tree node's refund txs. A
	// Prepare that consensus later rolled back has already overwritten the
	// node's refund columns with the claimer's unsigned txs; without the
	// restore, readers of the node columns during the still-pending transfer
	// (SSP SyncTransfer repair, lightning HTLC checks, unilateral exit) see
	// an unsigned tx as the leaf's canonical exit tx.
	for _, leaf := range transferLeaves {
		_, err := leaf.Update().SetKeyTweak(nil).Save(ctx)
		if err != nil {
			return fmt.Errorf("unable to update leaf %v: %w", leaf.ID, err)
		}
		if err := restoreLeafNodeRefundTxs(ctx, leaf); err != nil {
			return err
		}
	}
	return nil
}

// restoreLeafNodeRefundTxs restores the tree node's refund tx columns from
// the transfer leaf's immutable previous_* snapshots captured at send start.
// Restores exactly the statuses the claim path accepts and overwrites
// (validateClaimLeafTweak): TRANSFER_LOCKED plus the exited-to-L1 set. Each
// column restores only when its snapshot exists —
// previous_direct_from_cpfp_refund_tx rows predating its capture stay
// untouched. The tree node ent hooks recompute the txid columns.
//
// The update re-asserts the status set and the raw_refund_tx bytes just read
// as predicates, so a concurrent transition (e.g. a finalization committing
// newer refund txs) between the read and this write is never clobbered — the
// predicate misses and the restore becomes a no-op.
func restoreLeafNodeRefundTxs(ctx context.Context, transferLeaf *ent.TransferLeaf) error {
	node := transferLeaf.Edges.Leaf
	if node == nil {
		var err error
		node, err = transferLeaf.QueryLeaf().Only(ctx)
		if err != nil {
			return fmt.Errorf("unable to load tree node for transfer leaf %s: %w", transferLeaf.ID, err)
		}
	}
	if node.Status != st.TreeNodeStatusTransferLocked && !node.Status.IsExitedToL1() {
		return nil
	}
	update := node.Update()
	changed := false
	if len(transferLeaf.PreviousRefundTx) > 0 && !bytes.Equal(node.RawRefundTx, transferLeaf.PreviousRefundTx) {
		update.SetRawRefundTx(transferLeaf.PreviousRefundTx)
		changed = true
	}
	if len(transferLeaf.PreviousDirectRefundTx) > 0 && !bytes.Equal(node.DirectRefundTx, transferLeaf.PreviousDirectRefundTx) {
		update.SetDirectRefundTx(transferLeaf.PreviousDirectRefundTx)
		changed = true
	}
	if len(transferLeaf.PreviousDirectFromCpfpRefundTx) > 0 && !bytes.Equal(node.DirectFromCpfpRefundTx, transferLeaf.PreviousDirectFromCpfpRefundTx) {
		update.SetDirectFromCpfpRefundTx(transferLeaf.PreviousDirectFromCpfpRefundTx)
		changed = true
	}
	if !changed {
		return nil
	}
	// One equality predicate per column this update can write, so the guard
	// does not depend on every concurrent writer also touching
	// raw_refund_tx. Empty reads assert IS NULL; a predicate miss skips the
	// restore (fail-safe: never clobber, at worst leave debris for the next
	// successful claim to overwrite).
	preds := []predicate.TreeNode{
		enttreenode.StatusIn(
			st.TreeNodeStatusTransferLocked,
			st.TreeNodeStatusOnChain,
			st.TreeNodeStatusExited,
			st.TreeNodeStatusParentExited,
		),
		enttreenode.RawRefundTxEQ(node.RawRefundTx),
	}
	if len(node.DirectRefundTx) > 0 {
		preds = append(preds, enttreenode.DirectRefundTxEQ(node.DirectRefundTx))
	} else {
		preds = append(preds, enttreenode.DirectRefundTxIsNil())
	}
	if len(node.DirectFromCpfpRefundTx) > 0 {
		preds = append(preds, enttreenode.DirectFromCpfpRefundTxEQ(node.DirectFromCpfpRefundTx))
	} else {
		preds = append(preds, enttreenode.DirectFromCpfpRefundTxIsNil())
	}
	update.Where(preds...)
	if _, err := update.Save(ctx); err != nil {
		if ent.IsNotFound(err) {
			logging.GetLoggerFromContext(ctx).Sugar().Warnf(
				"claim rollback skipped refund tx restore for tree node %s (transfer leaf %s): node changed concurrently since read",
				node.ID, transferLeaf.ID)
			return nil
		}
		return fmt.Errorf("unable to restore refund txs for tree node %s: %w", node.ID, err)
	}
	return nil
}

func (h *TransferHandler) settleReceiverKeyTweak(ctx context.Context, transfer *ent.Transfer, receiver *ent.TransferReceiver, keyTweakProofs map[string]*pb.SecretProof, userPublicKeys map[string][]byte) error {
	return h.settleReceiverKeyTweakInternal(ctx, transfer, receiver, keyTweakProofs, userPublicKeys, nil, nil)
}

func (h *TransferHandler) settleReceiverKeyTweakInternal(ctx context.Context, transfer *ent.Transfer, receiver *ent.TransferReceiver, keyTweakProofs map[string]*pb.SecretProof, userPublicKeys map[string][]byte, encryptedKeyTweakPackage map[string][]byte, claimSignature []byte) error {
	ctx, span := tracer.Start(ctx, "TransferHandler.settleReceiverKeyTweak", trace.WithAttributes(
		transferTypeKey.String(string(transfer.Type)),
	))
	defer span.End()

	// receiver is nil only for a transfer carrying no receiver rows at all, in
	// which case the peer's settle fails resolving one rather than proceeding.
	var receiverIdentityPublicKeyBytes []byte
	if receiver != nil {
		receiverIdentityPublicKeyBytes = receiver.IdentityPubkey.Serialize()
	}

	// Phase 1: PREPARE - Distribute the receiver's key tweak request to all SOs
	action := pbinternal.SettleKeyTweakAction_COMMIT
	selection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}
	_, err := helper.ExecuteTaskWithAllOperators(ctx, h.config, &selection, func(ctx context.Context, operator *so.SigningOperator) (any, error) {
		conn, err := operator.NewOperatorInternalGRPCConnection(ctx)
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
		conn, err := operator.NewOperatorInternalGRPCConnection(ctx)
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

	// Commit the settle phase. On COMMIT, this persists Phase 1 SELF's
	// status transition AND Phase 2 SELF's keyshare mutation on this SO,
	// matching the peers' middleware-committed Phase 2 mutations. Without
	// this commit, any downstream failure in the caller (refund signing,
	// FROST aggregation, finalize) would roll the outer tx back — reverting
	// the coordinator's keyshare apply while the peers' Phase 2 commits
	// remain durable, reproducing the same partial-commit divergent state
	// just shifted later in the call chain.
	//
	// On ROLLBACK, the commit persists revertClaimTransfer's revert of the
	// transfer's RKL/RKT row + cleared leaf.KeyTweak — i.e. the rollback
	// itself is made durable.
	entTx, err := ent.GetTxFromContext(ctx)
	if err != nil {
		return fmt.Errorf("unable to get db for settle-phase commit: %w", err)
	}
	if err := entTx.Commit(); err != nil {
		return fmt.Errorf("unable to commit settle phase: %w", err)
	}

	if action == pbinternal.SettleKeyTweakAction_ROLLBACK {
		return fmt.Errorf("unable to settle receiver key tweak; rolled back: %w", rollbackCause)
	}
	return nil
}

func validateRefundSigningRetryMatchesStored(job *pb.LeafRefundTxSigningJob, leaf *ent.TreeNode) error {
	rawTx := func(signingJob *pb.SigningJob) []byte {
		if signingJob == nil {
			return nil
		}
		return signingJob.GetRawTx()
	}

	if !bytes.Equal(rawTx(job.GetRefundTxSigningJob()), leaf.RawRefundTx) {
		return fmt.Errorf("refund signing retry for leaf %s must not change refund transaction", job.GetLeafId())
	}
	if !bytes.Equal(rawTx(job.GetDirectRefundTxSigningJob()), leaf.DirectRefundTx) {
		return fmt.Errorf("refund signing retry for leaf %s must not change direct refund transaction", job.GetLeafId())
	}
	if !bytes.Equal(rawTx(job.GetDirectFromCpfpRefundTxSigningJob()), leaf.DirectFromCpfpRefundTx) {
		return fmt.Errorf("refund signing retry for leaf %s must not change direct from cpfp refund transaction", job.GetLeafId())
	}
	return nil
}

// validateReceivedRefundTransactions checks the refund-tx signing pubkey
// against the leaf's expected owner. By default (expectedOwnerSigningPubKey ==
// nil) it compares against the leaf's current OwnerSigningPubkey — the legacy
// claim flow has already applied the receiver key tweak before this check
// fires. The consensus claim flow passes a per-leaf predicted owner pubkey
// (leaf.OwnerSigningPubkey - pubkey_tweak) so it can validate without first
// mutating the on-disk keyshare; the durable apply happens in Commit.
//
// refundTimelockAnchorTx (the transfer leaf's previous_refund_tx) anchors the
// expected-timelock derivation (see validateSingleLeafRefundTxs).
func validateReceivedRefundTransactions(ctx context.Context, job *pb.LeafRefundTxSigningJob, leaf *ent.TreeNode, transferType st.TransferType, expectedOwnerSigningPubKey *keys.Public, refundTimelockAnchorTx []byte) error {
	if job.GetRefundTxSigningJob() == nil {
		return fmt.Errorf("missing RefundTxSigningJob for leaf %s", job.GetLeafId())
	}

	// Helper function to safely extract RawTx from signing job
	getRawTx := func(signingJob *pb.SigningJob) []byte {
		if signingJob == nil {
			return nil
		}
		return signingJob.GetRawTx()
	}

	// If ALL incoming txs match what's already in the DB,
	// this is a retry of a previous signing request - skip validation
	if bytes.Equal(job.GetRefundTxSigningJob().GetRawTx(), leaf.RawRefundTx) {
		if !bytes.Equal(getRawTx(job.GetDirectRefundTxSigningJob()), leaf.DirectRefundTx) ||
			!bytes.Equal(getRawTx(job.GetDirectFromCpfpRefundTxSigningJob()), leaf.DirectFromCpfpRefundTx) {
			return fmt.Errorf("refund signing retry for leaf %s must not change direct refund transactions", job.GetLeafId())
		}
		return nil
	}

	refundDestPubKey, err := keys.ParsePublicKey(job.GetRefundTxSigningJob().GetSigningPublicKey())
	if err != nil {
		return fmt.Errorf("invalid refund signing public key for leaf %s: %w", job.GetLeafId(), err)
	}

	expected := leaf.OwnerSigningPubkey
	if expectedOwnerSigningPubKey != nil {
		expected = *expectedOwnerSigningPubKey
	}
	if !refundDestPubKey.Equals(expected) {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("refund signing public key %x does not match expected owner signing pubkey %x for leaf %s", refundDestPubKey.Serialize(), expected.Serialize(), job.GetLeafId()))
	}

	if err := validateSingleLeafRefundTxs(
		ctx,
		leaf,
		getRawTx(job.GetRefundTxSigningJob()),
		getRawTx(job.GetDirectFromCpfpRefundTxSigningJob()),
		getRawTx(job.GetDirectRefundTxSigningJob()),
		refundDestPubKey,
		transferType,
		refundTimelockAnchorTx,
	); err != nil {
		return fmt.Errorf("refund transaction validation failed for leaf %s: %w", job.GetLeafId(), err)
	}

	return nil
}

// assert that the claim package contains a valid signature over the contained key tweak package
func verifyClaimPackageSignature(transferID uuid.UUID, claimPackage *pb.ClaimPackage, reqOwnerIDPubKey keys.Public) error {
	if claimPackage.GetHashVariant() != pb.HashVariant_HASH_VARIANT_V2 {
		return fmt.Errorf("claim package must use HASH_VARIANT_V2, got %s", claimPackage.GetHashVariant())
	}
	if len(claimPackage.GetUserSignature()) == 0 {
		return fmt.Errorf("claim package user_signature is required")
	}
	signingPayload := common.GetClaimPackageSigningPayload(transferID, claimPackage.GetKeyTweakPackage())
	if err := common.VerifyECDSASignature(reqOwnerIDPubKey, claimPackage.GetUserSignature(), signingPayload); err != nil {
		return fmt.Errorf("unable to verify claim package signature: %w", err)
	}
	return nil
}

// Create a query to fetch all the leaves for the current transfer; scoped to the receiver if one is
// provided. No current caller passes nil — the unscoped branch is defensive.
func getTransferLeavesForReceiverQuery(transfer *ent.Transfer, receiver *ent.TransferReceiver) *ent.TransferLeafQuery {
	transferLeavesQuery := transfer.QueryTransferLeaves()
	if receiver != nil {
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
	return h.claimTransferConsensus(ctx, req)
}

func parseSigningCommitments(job *pb.UserSignedTxSigningJob) (map[string]frost.SigningCommitment, error) {
	signingCommitments := job.GetSigningCommitments()
	if signingCommitments == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("missing signing_commitments for leaf_id %s", job.GetLeafId()))
	}
	rawCommitments := signingCommitments.GetSigningCommitments()
	if len(rawCommitments) == 0 {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("signing_commitments map is empty for leaf_id %s; expected at least one operator's round 1 commitment. Fetch commitments via GetSigningCommitments before submitting the signing request", job.GetLeafId()))
	}
	round1Packages := make(map[string]frost.SigningCommitment, len(rawCommitments))
	for key, commitment := range rawCommitments {
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

// leafSigningKeyshare returns the leaf's signing keyshare, preferring the
// eager-loaded edge (claim callers query leaves with .WithSigningKeyshare) and
// falling back to a query when the edge isn't populated. The queried keyshare
// is cached back on the edge so repeated lookups for the same leaf — one per
// refund variant in prepareClaimRefundSigningJobs — don't re-issue the query.
//
// The cache write is unsynchronized and the returned keyshare is a plain
// (non-row-locked) read used only to build signing jobs; callers iterate
// leaves in a single goroutine and must not mutate the keyshare through it.
func leafSigningKeyshare(ctx context.Context, leaf *ent.TreeNode) (*ent.SigningKeyshare, error) {
	if ks := leaf.Edges.SigningKeyshare; ks != nil {
		return ks, nil
	}
	ks, err := leaf.QuerySigningKeyshare().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get signing keyshare for leaf %s: %w", leaf.ID, err)
	}
	leaf.Edges.SigningKeyshare = ks
	return ks, nil
}

// prepareClaimRefundSigningJobs validates refund transactions (cpfp, direct,
// and direct-from-cpfp) from the claim package and persists them on the
// corresponding leaves, then builds FROST signing jobs with pre-generated
// nonces and returns lookup maps (leaf-to-job, job type) to assist with
// signing and aggregation. Direct-from-cpfp is required for all leaves;
// direct refund is required only for non-zero-timelock leaves with a DirectTx.
//
// predictedOwnerByLeaf controls the destination check in
// validateReceivedRefundTransactions:
//   - nil (legacy claim flow): the receiver key tweak is already applied
//     before this runs, so validation compares the refund's signing pubkey to
//     the post-tweak leaf.OwnerSigningPubkey already in the DB.
//   - non-nil (consensus claim flow): this runs BEFORE applying the tweak, so
//     callers pass a per-leaf predicted post-tweak owner pubkey
//     (leaf.OwnerSigningPubkey - pubkey_tweak) for validation to use.
//
// refundAnchorByLeaf maps leaf id to the transfer leaf's previous_refund_tx,
// the expected-timelock anchor (see validateSingleLeafRefundTxs).
func (h *TransferHandler) prepareClaimRefundSigningJobs(
	ctx context.Context,
	claimPackage *pb.ClaimPackage,
	leaves map[string]*ent.TreeNode,
	transfer *ent.Transfer,
	predictedOwnerByLeaf map[string]keys.Public,
	refundAnchorByLeaf map[string][]byte,
) (*claimRefundSigningJobsResult, error) {
	leafJobMap := make(map[uuid.UUID]*ent.TreeNode)
	jobIsDirectRefund := make(map[uuid.UUID]bool)
	jobIsDirectFromCpfpRefund := make(map[uuid.UUID]bool)
	var signingJobs []*helper.SigningJobWithPregeneratedNonce

	cpfpUserRefundMap := make(map[string]*pb.UserSignedTxSigningJob)
	directUserRefundMap := make(map[string]*pb.UserSignedTxSigningJob)
	directFromCpfpUserRefundMap := make(map[string]*pb.UserSignedTxSigningJob)

	for _, job := range claimPackage.GetLeavesToClaim() {
		cpfpUserRefundMap[job.GetLeafId()] = job
	}
	for _, job := range claimPackage.GetDirectLeavesToClaim() {
		directUserRefundMap[job.GetLeafId()] = job
	}
	for _, job := range claimPackage.GetDirectFromCpfpLeavesToClaim() {
		directFromCpfpUserRefundMap[job.GetLeafId()] = job
	}

	for _, job := range claimPackage.GetLeavesToClaim() {
		leaf, exists := leaves[job.GetLeafId()]
		if !exists {
			return nil, fmt.Errorf("unexpected leaf id %s", job.GetLeafId())
		}

		// Validate refund transactions.
		leafRefundJob := &pb.LeafRefundTxSigningJob{
			LeafId: job.GetLeafId(),
			RefundTxSigningJob: &pb.SigningJob{
				RawTx:            job.GetRawTx(),
				SigningPublicKey: job.GetSigningPublicKey(),
			},
		}
		// Direct refund is only required when the leaf has a DirectTx and is not a zero-timelock node.
		if directJob, ok := directUserRefundMap[job.GetLeafId()]; ok {
			leafRefundJob.DirectRefundTxSigningJob = &pb.SigningJob{
				RawTx:            directJob.GetRawTx(),
				SigningPublicKey: directJob.GetSigningPublicKey(),
			}
		} else if len(leaf.DirectTx) > 0 {
			isZeroNode, err := bitcointransaction.IsZeroNode(leaf)
			if err != nil {
				return nil, fmt.Errorf("failed to determine if node is zero node: %w", err)
			}
			if !isZeroNode {
				return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("missing direct refund transaction for leaf %s", job.GetLeafId()))
			}
		}
		// Direct-from-cpfp refund is always required (validated early in ClaimTransfer).
		dfcJob := directFromCpfpUserRefundMap[job.GetLeafId()]
		leafRefundJob.DirectFromCpfpRefundTxSigningJob = &pb.SigningJob{
			RawTx:            dfcJob.GetRawTx(),
			SigningPublicKey: dfcJob.GetSigningPublicKey(),
		}
		var expectedOwner *keys.Public
		if predictedOwnerByLeaf != nil {
			if predicted, ok := predictedOwnerByLeaf[job.GetLeafId()]; ok {
				expectedOwner = &predicted
			}
		}
		// previous_refund_tx is schema-NotEmpty, so a missing anchor for a
		// claim leaf is always a plumbing bug — fail loudly instead of
		// silently anchoring on the possibly-poisoned node refund tx.
		refundAnchorTx, ok := refundAnchorByLeaf[job.GetLeafId()]
		if !ok || len(refundAnchorTx) == 0 {
			return nil, fmt.Errorf("internal: missing previous refund tx timelock anchor for leaf %s", job.GetLeafId())
		}
		if err := validateReceivedRefundTransactions(ctx, leafRefundJob, leaf, transfer.Type, expectedOwner, refundAnchorTx); err != nil {
			return nil, err
		}

		// Update CPFP refund tx on existing leaf.
		rawRefundTx, err := common.TxFromRawTxBytes(job.GetRawTx())
		if err != nil {
			return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("failed to parse cpfp raw_refund_tx for leaf %s: %w", job.GetLeafId(), err))
		}
		rawRefundTxid := st.NewTxID(rawRefundTx.TxHash())

		updateOp := leaf.Update().
			SetRawRefundTx(job.GetRawTx()).
			SetRawRefundTxid(rawRefundTxid)

		if directJob, ok := directUserRefundMap[job.GetLeafId()]; ok {
			directRefundTxParsed, err := common.TxFromRawTxBytes(directJob.GetRawTx())
			if err != nil {
				return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("failed to parse direct_refund_tx for leaf %s: %w", job.GetLeafId(), err))
			}
			updateOp = updateOp.
				SetDirectRefundTx(directJob.GetRawTx()).
				SetDirectRefundTxid(st.NewTxID(directRefundTxParsed.TxHash()))
		}

		directFromCpfpRefundTxParsed, err := common.TxFromRawTxBytes(dfcJob.GetRawTx())
		if err != nil {
			return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("failed to parse direct_from_cpfp_refund_tx for leaf %s: %w", job.GetLeafId(), err))
		}
		updateOp = updateOp.
			SetDirectFromCpfpRefundTx(dfcJob.GetRawTx()).
			SetDirectFromCpfpRefundTxid(st.NewTxID(directFromCpfpRefundTxParsed.TxHash()))

		if _, err := updateOp.Save(ctx); err != nil {
			return nil, fmt.Errorf("unable to update refund txs for leaf %s: %w", job.GetLeafId(), err)
		}

		// Create CPFP signing job with pregenerated nonces.
		cpfpLeafTx, err := common.TxFromRawTxBytes(leaf.RawTx)
		if err != nil {
			return nil, fmt.Errorf("unable to load cpfp leaf tx for leaf %s: %w", job.GetLeafId(), err)
		}
		if len(cpfpLeafTx.TxOut) == 0 {
			return nil, fmt.Errorf("vout out of bounds for cpfp tx of leaf %s", job.GetLeafId())
		}
		refundTxSigHash, err := sighash.FromTx(rawRefundTx, 0, cpfpLeafTx.TxOut[0])
		if err != nil {
			return nil, fmt.Errorf("unable to calculate sighash for cpfp refund of leaf %s: %w", job.GetLeafId(), err)
		}

		userNonceCommitment := frost.SigningCommitment{}
		if err := userNonceCommitment.UnmarshalProto(job.GetSigningNonceCommitment()); err != nil {
			return nil, fmt.Errorf("unable to unmarshal signing nonce commitment for leaf %s: %w", job.GetLeafId(), err)
		}

		signingKeyshare, err := leafSigningKeyshare(ctx, leaf)
		if err != nil {
			return nil, err
		}

		round1Packages, err := parseSigningCommitments(job)
		if err != nil {
			return nil, fmt.Errorf("unable to parse signing commitments for cpfp refund of leaf %s: %w", job.GetLeafId(), err)
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
	for _, job := range claimPackage.GetDirectLeavesToClaim() {
		leaf, exists := leaves[job.GetLeafId()]
		if !exists {
			return nil, fmt.Errorf("unexpected leaf id %s for direct refund", job.GetLeafId())
		}
		directRefundTx, err := common.TxFromRawTxBytes(job.GetRawTx())
		if err != nil {
			return nil, fmt.Errorf("unable to parse direct refund tx for leaf %s: %w", job.GetLeafId(), err)
		}
		directTx, err := common.TxFromRawTxBytes(leaf.DirectTx)
		if err != nil {
			return nil, fmt.Errorf("unable to load direct leaf tx for leaf %s: %w", job.GetLeafId(), err)
		}
		if len(directTx.TxOut) == 0 {
			return nil, fmt.Errorf("vout out of bounds for direct tx of leaf %s", job.GetLeafId())
		}
		directRefundTxSigHash, err := sighash.FromTx(directRefundTx, 0, directTx.TxOut[0])
		if err != nil {
			return nil, fmt.Errorf("unable to calculate sighash for direct refund of leaf %s: %w", job.GetLeafId(), err)
		}

		userNonceCommitment := frost.SigningCommitment{}
		if err := userNonceCommitment.UnmarshalProto(job.GetSigningNonceCommitment()); err != nil {
			return nil, fmt.Errorf("unable to unmarshal signing nonce commitment for leaf %s: %w", job.GetLeafId(), err)
		}
		signingKeyshare, err := leafSigningKeyshare(ctx, leaf)
		if err != nil {
			return nil, err
		}
		round1Packages, err := parseSigningCommitments(job)
		if err != nil {
			return nil, fmt.Errorf("unable to parse signing commitments for direct refund of leaf %s: %w", job.GetLeafId(), err)
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
	for _, job := range claimPackage.GetDirectFromCpfpLeavesToClaim() {
		leaf, exists := leaves[job.GetLeafId()]
		if !exists {
			return nil, fmt.Errorf("unexpected leaf id %s for direct from cpfp refund", job.GetLeafId())
		}
		directFromCpfpRefundTx, err := common.TxFromRawTxBytes(job.GetRawTx())
		if err != nil {
			return nil, fmt.Errorf("unable to parse direct from cpfp refund tx for leaf %s: %w", job.GetLeafId(), err)
		}
		cpfpLeafTx, err := common.TxFromRawTxBytes(leaf.RawTx)
		if err != nil {
			return nil, fmt.Errorf("unable to load cpfp leaf tx for leaf %s: %w", job.GetLeafId(), err)
		}
		if len(cpfpLeafTx.TxOut) == 0 {
			return nil, fmt.Errorf("vout out of bounds for cpfp tx of leaf %s", job.GetLeafId())
		}
		directFromCpfpSigHash, err := sighash.FromTx(directFromCpfpRefundTx, 0, cpfpLeafTx.TxOut[0])
		if err != nil {
			return nil, fmt.Errorf("unable to calculate sighash for direct from cpfp refund of leaf %s: %w", job.GetLeafId(), err)
		}

		userNonceCommitment := frost.SigningCommitment{}
		if err := userNonceCommitment.UnmarshalProto(job.GetSigningNonceCommitment()); err != nil {
			return nil, fmt.Errorf("unable to unmarshal signing nonce commitment for leaf %s: %w", job.GetLeafId(), err)
		}
		signingKeyshare, err := leafSigningKeyshare(ctx, leaf)
		if err != nil {
			return nil, err
		}
		round1Packages, err := parseSigningCommitments(job)
		if err != nil {
			return nil, fmt.Errorf("unable to parse signing commitments for direct from cpfp refund of leaf %s: %w", job.GetLeafId(), err)
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
	reqOwnerIDPubKey, err := keys.ParsePublicKey(req.GetOwnerIdentityPublicKey())
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("invalid identity public key: %w", err))
	}
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, reqOwnerIDPubKey); err != nil {
		return nil, err
	}
	if err := authz.EnforceWalletNotKillSwitched(ctx, reqOwnerIDPubKey); err != nil {
		return nil, err
	}

	transferID, err := uuid.Parse(req.GetTransferId())
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid transfer ID: %w", err))
	}

	transfer, err := h.loadTransferForUpdate(ctx, transferID, sql.WithLockAction(sql.NoWait))
	if err != nil {
		if sparkdb.IsLockNotAvailableError(err) {
			return nil, claimLockConflictError(ctx, transferID, err)
		}
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
		return nil, sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("transfer %s is expected to be at status TransferStatusKeyTweaked or TransferStatusReceiverRefundSigned or TransferStatusReceiverKeyTweakLocked or TransferStatusReceiverKeyTweakApplied but %s found", transferID, transfer.Status))
	}

	// This guarantees that the transfer has only one receiver and logic changes to filter leaves, etc
	// are not necessary for this endpoint. We only dual-write the status changes to the receiver object for consistency.
	receiver, err := h.loadSingleTransferReceiverForUnsupportedMimoPath(ctx, transfer)
	if err != nil {
		return nil, err
	}
	isRefundSigningRetry := transfer.Status == st.TransferStatusReceiverRefundSigned
	if receiver != nil && receiver.Status == st.TransferReceiverStatusRefundSigned {
		isRefundSigningRetry = true
	}

	// Validate leaves count
	leavesToTransfer, err := transfer.QueryTransferLeaves().All(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to load leaves to transfer for transfer %s: %w", transferID, err)
	}
	if len(leavesToTransfer) != len(req.GetSigningJobs()) {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("inconsistent leaves to claim for transfer %s", transferID))
	}

	keyTweakProofs := map[string]*pb.SecretProof{}
	leavesByID := make(map[string]*ent.TreeNode, len(leavesToTransfer))
	refundAnchorByLeaf := make(map[string][]byte, len(leavesToTransfer))
	for _, leaf := range leavesToTransfer {
		treeNode, err := leaf.QueryLeaf().Only(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to get tree node for leaf %s: %w", leaf.ID, err)
		}
		leavesByID[treeNode.ID.String()] = treeNode
		refundAnchorByLeaf[treeNode.ID.String()] = leaf.PreviousRefundTx
		leafKeyTweak := &pb.ClaimLeafKeyTweak{}
		if leaf.KeyTweak != nil {
			err = proto.Unmarshal(leaf.KeyTweak, leafKeyTweak)
			if err != nil {
				return nil, fmt.Errorf("unable to unmarshal key tweak for leaf %s: %w", leaf.ID, err)
			}
			keyTweakProofs[treeNode.ID.String()] = &pb.SecretProof{
				Proofs: leafKeyTweak.GetSecretShareTweak().GetProofs(),
			}
		}
	}

	if isRefundSigningRetry {
		for i, job := range req.GetSigningJobs() {
			if job == nil {
				return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("signing_jobs[%d] is required", i))
			}
			if job.GetRefundTxSigningJob() == nil {
				return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("signing_jobs[%d].refund_tx_signing_job is required", i))
			}
			leaf, ok := leavesByID[job.GetLeafId()]
			if !ok {
				return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("unexpected leaf id %s", job.GetLeafId()))
			}
			if err := validateRefundSigningRetryMatchesStored(job, leaf); err != nil {
				return nil, err
			}
		}
	}

	userPublicKeys := make(map[string][]byte)
	for _, job := range req.GetSigningJobs() {
		userPublicKeys[job.GetLeafId()] = job.GetRefundTxSigningJob().GetSigningPublicKey()
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
	builders := make([]*ent.TreeNodeCreate, 0, len(req.GetSigningJobs()))

	var signingJobs []*helper.SigningJob
	jobToLeafMap := make(map[uuid.UUID]uuid.UUID)
	isDirectSigningJob := make(map[uuid.UUID]bool)
	isDirectFromCpfpSigningJob := make(map[uuid.UUID]bool)
	isSwap := transfer.Type == st.TransferTypeCounterSwap || transfer.Type == st.TransferTypeSwap || transfer.Type == st.TransferTypePrimarySwapV3 || transfer.Type == st.TransferTypeCounterSwapV3
	isSupportedTransferType := transfer.Type == st.TransferTypeTransfer || transfer.Type == st.TransferTypeCounterSwap || transfer.Type == st.TransferTypeSwap || transfer.Type == st.TransferTypePrimarySwapV3 || transfer.Type == st.TransferTypeCounterSwapV3 || transfer.Type == st.TransferTypeCooperativeExit

	for _, job := range req.GetSigningJobs() {
		leaf, exists := leaves[job.GetLeafId()]
		if !exists {
			return nil, fmt.Errorf("unexpected leaf id %s", job.GetLeafId())
		}

		if isSupportedTransferType {
			// Same fail-loud contract as prepareClaimRefundSigningJobs: an
			// absent anchor on the claim path is a plumbing bug.
			refundAnchorTx, ok := refundAnchorByLeaf[job.GetLeafId()]
			if !ok || len(refundAnchorTx) == 0 {
				return nil, fmt.Errorf("internal: missing previous refund tx timelock anchor for leaf %s", job.GetLeafId())
			}
			if err := validateReceivedRefundTransactions(ctx, job, leaf, transfer.Type, nil, refundAnchorTx); err != nil {
				return nil, err
			}
		}

		directRefundTxSigningJob := (*pb.SigningJob)(nil)
		directFromCpfpRefundTxSigningJob := (*pb.SigningJob)(nil)
		if job.GetDirectRefundTxSigningJob() != nil {
			directRefundTxSigningJob = job.GetDirectRefundTxSigningJob()
		} else if !isSwap && requireDirectTx && len(leaf.DirectTx) > 0 {
			isZeroNode, err := bitcointransaction.IsZeroNode(leaf)
			if err != nil {
				return nil, fmt.Errorf("failed to determine if node is zero node: %w", err)
			}

			if !isZeroNode {
				return nil, fmt.Errorf("DirectRefundTxSigningJob is required. Please upgrade to the latest SDK version")
			}
		}
		if job.GetDirectFromCpfpRefundTxSigningJob() != nil {
			directFromCpfpRefundTxSigningJob = job.GetDirectFromCpfpRefundTxSigningJob()
		} else if !isSwap && requireDirectTx {
			if len(leaf.DirectTx) > 0 {
				return nil, fmt.Errorf("DirectFromCpfpRefundTxSigningJob is required. Please upgrade to the latest SDK version")
			}
		}
		var directRefundTx []byte
		var directFromCpfpRefundTx []byte
		if directRefundTxSigningJob != nil {
			directRefundTx = directRefundTxSigningJob.GetRawTx()
		}
		if directFromCpfpRefundTxSigningJob != nil {
			directFromCpfpRefundTx = directFromCpfpRefundTxSigningJob.GetRawTx()
		}

		leafID := leaf.ID.String()

		// Compute txids from transaction bytes (same logic as ent hooks)
		rawRefundTx, err := common.TxFromRawTxBytes(job.GetRefundTxSigningJob().GetRawTx())
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
			SetRawRefundTx(job.GetRefundTxSigningJob().GetRawTx()).
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

		cpfpSigningJob, directSigningJob, directFromCpfpSigningJob, err := h.getRefundTxSigningJobs(ctx, leaf, job.GetRefundTxSigningJob(), job.GetDirectRefundTxSigningJob(), job.GetDirectFromCpfpRefundTxSigningJob())
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
		signingResultProto := signingResult.MarshalProto()
		if isDirectSigningJob[signingResult.JobID] {
			leafResult.DirectRefundTxSigningResult = signingResultProto
		} else if isDirectFromCpfpSigningJob[signingResult.JobID] {
			leafResult.DirectFromCpfpRefundTxSigningResult = signingResultProto
		} else {
			leafResult.RefundTxSigningResult = signingResultProto
		}
	}

	return &pb.ClaimTransferSignRefundsResponse{SigningResults: slices.Collect(maps.Values(leafSigningResults))}, nil
}

func (h *TransferHandler) getRefundTxSigningJobs(ctx context.Context, leaf *ent.TreeNode, cpfpJob *pb.SigningJob, directJob *pb.SigningJob, directFromCpfpJob *pb.SigningJob) (*helper.SigningJob, *helper.SigningJob, *helper.SigningJob, error) {
	ctx, span := tracer.Start(ctx, "TransferHandler.getRefundTxSigningJob")
	defer span.End()

	keyshare, err := leaf.QuerySigningKeyshare().First(ctx)
	if err != nil || keyshare == nil {
		return nil, nil, nil, fmt.Errorf("unable to load keyshare for leaf %s: %w", leaf.ID, err)
	}
	cpfpLeafTx, err := common.TxFromRawTxBytes(leaf.RawTx)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("unable to load cpfp leaf tx for leaf %s: %w", leaf.ID, err)
	}
	directRefundSigningJob := (*helper.SigningJob)(nil)
	directFromCpfpRefundSigningJob := (*helper.SigningJob)(nil)

	// Create direct refund signing job if direct tx exists and job is provided
	if len(leaf.DirectTx) > 0 && directJob != nil {
		directLeafTx, err := common.TxFromRawTxBytes(leaf.DirectTx)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("unable to load direct leaf tx for leaf %s: %w", leaf.ID, err)
		}
		if len(directLeafTx.TxOut) == 0 {
			return nil, nil, nil, fmt.Errorf("vout out of bounds for direct tx")
		}
		directRefundSigningJob, _, err = helper.NewSigningJob(keyshare, directJob, directLeafTx.TxOut[0])
		if err != nil {
			return nil, nil, nil, fmt.Errorf("unable to create direct signing job for leaf %s: %w", leaf.ID, err)
		}
	}

	// Always create direct from cpfp refund signing job if provided
	if directFromCpfpJob != nil {
		directFromCpfpRefundSigningJob, _, err = helper.NewSigningJob(keyshare, directFromCpfpJob, cpfpLeafTx.TxOut[0])
		if err != nil {
			return nil, nil, nil, fmt.Errorf("unable to create direct from cpfp signing job for leaf %s: %w", leaf.ID, err)
		}
	}
	if len(cpfpLeafTx.TxOut) == 0 {
		return nil, nil, nil, fmt.Errorf("vout out of bounds for cpfp tx")
	}
	cpfpRefundSigningJob, _, err := helper.NewSigningJob(keyshare, cpfpJob, cpfpLeafTx.TxOut[0])
	if err != nil {
		return nil, nil, nil, fmt.Errorf("unable to create cpfp signing job for leaf %s: %w", leaf.ID, err)
	}
	return cpfpRefundSigningJob, directRefundSigningJob, directFromCpfpRefundSigningJob, nil
}

// InitiateSettleReceiverKeyTweak performs the per-SO key-tweak prepare work
// (decrypt slice, persist, validate keyshares, status → ReceiverKeyTweakLocked).
// Does NOT commit the surrounding ent transaction — the caller (gRPC
// middleware for cross-SO settle, engine's request tx for the consensus
// claim-transfer 2PC flow handler) owns the commit lifecycle.
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
	receiver, err := h.loadTransferReceiverByPublicKeyForUpdate(ctx, transfer, receiverIdentityPublicKey)
	if err != nil {
		return err
	}

	isReceiverAuthoritative, err := isMimoReceiverStatusAuthoritative(ctx, transfer)
	if err != nil {
		return err
	}

	if err := validateTransferReadyForReceiverClaim(transfer); err != nil {
		return err
	}
	if receiver.Status == st.TransferReceiverStatusCompleted {
		// This receiver has already completed their claim, return early.
		return nil
	}

	hasClaimPackage := len(req.GetEncryptedClaimKeyTweakPackage()) > 0

	switch receiver.Status {
	case st.TransferReceiverStatusReceiverClaimPending,
		st.TransferReceiverStatusKeyTweaked,
		st.TransferReceiverStatusKeyTweakLocked:
		// Stored leaf tweaks may survive a rollback to any pre-apply status.
	case st.TransferReceiverStatusKeyTweakApplied,
		st.TransferReceiverStatusRefundSigned:
		// The key tweak is already applied, return early.
		return nil
	default:
		return fmt.Errorf("unexpected transfer receiver status %s for receiver %s", receiver.Status, receiver.ID)
	}

	// Existing tweaks are write-once until apply or rollback; proof validation
	// below binds the request to the stored polynomial.
	if hasClaimPackage {
		// Verify receiver signature over the full encrypted key tweak package.
		signingPayload := common.GetClaimPackageSigningPayload(transferID, req.GetEncryptedClaimKeyTweakPackage())
		if err := common.VerifyECDSASignature(*receiverIdentityPublicKey, req.GetClaimSignature(), signingPayload); err != nil {
			return fmt.Errorf("unable to verify claim package signature: %w", err)
		}

		// Decrypt this SO's portion.
		myCiphertext := req.GetEncryptedClaimKeyTweakPackage()[h.config.Identifier]
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

		transferLeaves, err := getTransferLeavesForReceiverQuery(transfer, receiver).WithLeaf(func(tnq *ent.TreeNodeQuery) {
			tnq.WithSigningKeyshare()
		}).All(ctx)
		if err != nil {
			return fmt.Errorf("unable to get transfer leaves for transfer %s: %w", transferID, err)
		}
		if len(transferLeaves) != len(claimKeyTweaks.GetLeavesToReceive()) {
			return fmt.Errorf("transfer has %d leaves but claim key tweaks has %d", len(transferLeaves), len(claimKeyTweaks.GetLeavesToReceive()))
		}
		// Reject duplicate keyshare assignments in Phase 1, where every SO can
		// still abort the two-phase commit cleanly; the same check during the
		// Phase-2 apply is defense-in-depth.
		if _, err := leafKeyshareIDsForClaim(transferLeaves, transferID); err != nil {
			return err
		}

		// Verify that all LeavesToReceive are found in the queried transfer leaves
		// and set the provided tweaks into the leaf if necessary
		leafMap := make(map[string]*ent.TransferLeaf)
		for _, leaf := range transferLeaves {
			leafMap[leaf.Edges.Leaf.ID.String()] = leaf
		}
		for _, leafTweak := range claimKeyTweaks.GetLeavesToReceive() {
			leaf, exists := leafMap[leafTweak.GetLeafId()]
			if !exists {
				return fmt.Errorf("unexpected leaf id %s in claim key tweaks", leafTweak.GetLeafId())
			}

			if len(leaf.KeyTweak) > 0 {
				storedTweak := &pb.ClaimLeafKeyTweak{}
				if unmarshalErr := proto.Unmarshal(leaf.KeyTweak, storedTweak); unmarshalErr != nil {
					// A durable proof anchor that no longer parses is data
					// corruption; preserve it as evidence and surface loudly
					// rather than silently replacing it.
					return sparkerrors.InternalDataInconsistency(fmt.Errorf(
						"stored key tweak for transfer %s leaf %s is unreadable: %w", transferID, leafTweak.GetLeafId(), unmarshalErr))
				}
				continue
			}
			leafTweakBytes, err := proto.Marshal(leafTweak)
			if err != nil {
				return fmt.Errorf("unable to marshal leaf tweak: %w", err)
			}
			_, err = leaf.Update().SetKeyTweak(leafTweakBytes).Save(ctx)
			if err != nil {
				return fmt.Errorf("unable to update leaf %s: %w", leafTweak.GetLeafId(), err)
			}
			logging.GetLoggerFromContext(ctx).Sugar().Infof(
				"claim key tweak stored (peer) for transfer %s leaf %s: num_proofs=%d proofs_hash=%s",
				transferID, leafTweak.GetLeafId(), len(leafTweak.GetSecretShareTweak().GetProofs()), hashClaimLeafKeyTweakProofs(leafTweak),
			)
		}

		// Skipped when receiver status is authoritative — parent stays at SenderKeyTweaked.
		if !isReceiverAuthoritative && transfer.Status == st.TransferStatusSenderKeyTweaked {
			_, err = transfer.Update().SetStatus(st.TransferStatusReceiverKeyTweaked).Save(ctx)
			if err != nil {
				return fmt.Errorf("unable to update transfer status %s: %w", transfer.ID, err)
			}
			transfer.Status = st.TransferStatusReceiverKeyTweaked
		}

		// Promote the receiver from RECEIVER_CLAIM_PENDING to KeyTweaked once
		// the claim's key tweak has been applied.
		if receiver != nil && receiver.Status == st.TransferReceiverStatusReceiverClaimPending {
			_, err = receiver.Update().SetStatus(st.TransferReceiverStatusKeyTweaked).Save(ctx)
			if err != nil {
				return fmt.Errorf("unable to update transfer receiver status %s: %w", transfer.ID, err)
			}
			receiver.Status = st.TransferReceiverStatusKeyTweaked
		}
	}

	transferLeaves, err := getTransferLeavesForReceiverQuery(transfer, receiver).WithLeaf().All(ctx)
	if err != nil {
		return fmt.Errorf("unable to get leaves from transfer %s: %w", transferID, err)
	}

	// This check must take place here and may not fail fast- retry attempts may load the key tweaks from db
	if req.KeyTweakProofs != nil {
		err = h.ValidateKeyTweakProof(ctx, transferLeaves, req.GetKeyTweakProofs())
		if err != nil {
			return fmt.Errorf("unable to validate key tweak proof: %w", err)
		}
	} else {
		return fmt.Errorf("key tweak proof is required")
	}

	// update transfer and transfer receiver states to TweakLocked
	// (parent skipped when receiver status is authoritative)
	if !isReceiverAuthoritative {
		if _, err = transfer.Update().SetStatus(st.TransferStatusReceiverKeyTweakLocked).Save(ctx); err != nil {
			return fmt.Errorf("unable to update transfer status %s: %w", transfer.ID, err)
		}
	}
	if receiver != nil {
		_, err = receiver.Update().SetStatus(st.TransferReceiverStatusKeyTweakLocked).Save(ctx)
		if err != nil {
			return fmt.Errorf("unable to update transfer receiver status %s: %w", transfer.ID, err)
		}
	}

	// Intentionally NOT committing the tx here. A mid-flow commit would
	// release the FOR UPDATE row lock between Phase 1 SELF and Phase 2 SELF;
	// a concurrent ROLLBACK could then revert the coordinator to
	// SENDER_KEY_TWEAKED while Phase 2 fan-out had already committed the
	// peers. Callers persist leaf.KeyTweak durably before this 2PC starts
	// (ClaimTransferTweakKeys in the multi-call claim flow); everything past
	// that point is part of one outer tx committed by the gRPC middleware on
	// handler return.
	//
	// The consensus claim-transfer 2PC flow handler also calls this
	// function directly: its engine's request tx owns the commit lifecycle.
	return nil
}

// leafKeyshareIDsForClaim validates that every transfer leaf has its tree
// node and signing keyshare edges loaded and that no two leaves share a
// keyshare, returning the keyshare IDs in leaf order.
//
// Two leaves sharing a keyshare cannot both tweak it: each leaf's
// OwnerSigningPubkey is derived from the keyshare state after its own tweak,
// so stacked tweaks would leave the first leaf's stored owner key
// inconsistent with the final keyshare. The protocol assigns each spendable
// leaf its own SE keyshare (the 1:1 mapping is an application convention with
// no DB unique constraint, and the tree_node signing_keyshare edge is not
// schema-immutable), so this can only fire on data corruption or a bug in a
// leaf-creation path — rejecting the claim is strictly safer than the silent
// double-tweak that would otherwise happen.
func leafKeyshareIDsForClaim(leaves []*ent.TransferLeaf, transferID uuid.UUID) ([]uuid.UUID, error) {
	keyshareIDs := make([]uuid.UUID, 0, len(leaves))
	keyshareLeafIDs := make(map[uuid.UUID]uuid.UUID, len(leaves))
	for _, leaf := range leaves {
		treeNode := leaf.Edges.Leaf
		if treeNode == nil {
			return nil, sparkerrors.InternalDatabaseMissingEdge(fmt.Errorf("tree node edge not loaded for transfer leaf %s", leaf.ID))
		}
		keyshare := treeNode.Edges.SigningKeyshare
		if keyshare == nil {
			return nil, sparkerrors.InternalDatabaseMissingEdge(fmt.Errorf("signing keyshare edge not loaded for leaf %s", treeNode.ID))
		}
		if otherLeafID, ok := keyshareLeafIDs[keyshare.ID]; ok {
			return nil, sparkerrors.InternalDataInconsistency(fmt.Errorf(
				"signing keyshare %s is referenced by both leaf %s and leaf %s in transfer %s", keyshare.ID, otherLeafID, treeNode.ID, transferID))
		}
		keyshareLeafIDs[keyshare.ID] = treeNode.ID
		keyshareIDs = append(keyshareIDs, keyshare.ID)
	}
	return keyshareIDs, nil
}

// lockAndHydrateLeafKeyshares loads every leaf's keyshare with one row-locked
// batch read and hydrates their secrets in one ephemeral-DB query, returning
// them keyed by keyshare ID. FOR UPDATE pins the rows until the surrounding
// transaction commits, so a concurrent rotation can neither invalidate the
// hydrated secrets nor abort the per-leaf TweakKeyShare CAS. The IDs come
// from the eager-loaded edges, which is safe because a tree node's keyshare
// is never reassigned after creation (an application convention — the ent
// edge is not schema-immutable; see the matching note on
// leafKeyshareIDsForClaim).
//
// Deadlock safety does not depend on lock-acquisition order: same-transfer
// claims are serialized by the transfer row lock the caller already holds,
// and keyshares are never shared across transfers, so no two transactions
// can contend for an overlapping keyshare set here. Order(Asc(ID)) just
// keeps the batch read deterministic.
//
// A hydration failure is deliberately not returned: with the rows locked the
// secret versions cannot move, so a failure here means the ephemeral store is
// unavailable or missing a secret. TweakKeyShare re-attempts hydration per
// keyshare, which isolates such a failure to the affected leaf with the
// precise caller-facing error. The commit loop aborts on that first failing
// leaf, so even a store-wide outage costs at most one redundant round trip.
func lockAndHydrateLeafKeyshares(ctx context.Context, db *ent.Client, leaves []*ent.TransferLeaf, transferID uuid.UUID) (map[uuid.UUID]*ent.SigningKeyshare, error) {
	keyshareIDs, err := leafKeyshareIDsForClaim(leaves, transferID)
	if err != nil {
		return nil, err
	}
	lockedKeyshares, err := db.SigningKeyshare.Query().
		Where(entsigningkeyshare.IDIn(keyshareIDs...)).
		Order(ent.Asc(entsigningkeyshare.FieldID)).
		ForUpdate().
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to load keyshares for transfer %s: %w", transferID, err)
	}
	if len(lockedKeyshares) != len(keyshareIDs) {
		return nil, sparkerrors.InternalDataInconsistency(fmt.Errorf(
			"expected %d signing keyshares for transfer %s but locked %d", len(keyshareIDs), transferID, len(lockedKeyshares)))
	}
	keysharesByID := make(map[uuid.UUID]*ent.SigningKeyshare, len(lockedKeyshares))
	for _, keyshare := range lockedKeyshares {
		keysharesByID[keyshare.ID] = keyshare
	}
	if err := ent.HydrateSigningKeyshareSecrets(ctx, lockedKeyshares); err != nil {
		logging.GetLoggerFromContext(ctx).With(zap.Error(err), zap.Stringer("transfer_id", transferID)).Warn(
			"batch keyshare secret hydration failed, falling back to per-keyshare hydration")
	}
	return keysharesByID, nil
}

// SettleReceiverKeyTweak performs the per-SO Phase-2 work (apply tweaks,
// clear leaf.KeyTweak, status → KeyTweakApplied on COMMIT; or revert on
// ROLLBACK). Does NOT commit the surrounding ent transaction — the caller
// (settleReceiverKeyTweakInternal for cross-SO settle, engine's request tx
// for the consensus claim-transfer 2PC flow handler) owns the commit
// lifecycle.
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
	receiver, err := h.loadTransferReceiverByPublicKeyForUpdate(ctx, transfer, receiverIdentityPublicKey)
	if err != nil {
		return err
	}

	if err := validateTransferReadyForReceiverClaim(transfer); err != nil {
		if req.GetAction() == pbinternal.SettleKeyTweakAction_COMMIT {
			return err
		}
		// ROLLBACK always proceeds even when the transfer is not ready for receiver claim,
		// to prevent resource leaks in the two-phase commit protocol.
		logging.GetLoggerFromContext(ctx).Warn("SettleReceiverKeyTweak ROLLBACK proceeding despite transfer not ready for receiver claim",
			zap.Stringer("transfer_id", transferID),
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
		st.TransferReceiverStatusReceiverClaimPending:
		// Do nothing
	default:
		if req.GetAction() == pbinternal.SettleKeyTweakAction_COMMIT {
			return fmt.Errorf("transfer receiver %s is in an invalid status %s to settle receiver key tweak", receiver.ID, receiver.Status)
		}
	}

	switch req.GetAction() {
	case pbinternal.SettleKeyTweakAction_COMMIT:
		leaves, err := getTransferLeavesForReceiverQuery(transfer, receiver).WithLeaf(func(tnq *ent.TreeNodeQuery) {
			tnq.WithTree().WithSigningKeyshare()
		}).All(ctx)
		if err != nil {
			return fmt.Errorf("unable to get leaves from transfer %s: %w", transferID, err)
		}

		db, err := ent.GetDbFromContext(ctx)
		if err != nil {
			return fmt.Errorf("unable to get db: %w", err)
		}

		keysharesByID, err := lockAndHydrateLeafKeyshares(ctx, db, leaves, transferID)
		if err != nil {
			return err
		}

		// When the commit payload binds tweak digests, every stored tweak must
		// match before ANY keyshare is mutated — applying a tweak from a
		// different polynomial than the one the cluster signed with silently
		// diverges this SO's keyshare from its peers. Absent digests (an
		// old-binary coordinator) skip the check.
		expectedDigests := make(map[string][]byte, len(req.GetLeafTweakDigests()))
		expectedPostTweakKeyshareDigests := make(map[string][]byte, len(req.GetLeafTweakDigests()))
		for _, d := range req.GetLeafTweakDigests() {
			expectedDigests[d.GetLeafId()] = d.GetProofsHash()
			if len(d.GetPostTweakKeyshareHash()) > 0 {
				expectedPostTweakKeyshareDigests[d.GetLeafId()] = d.GetPostTweakKeyshareHash()
			}
		}

		// Validate every leaf's stored tweak and collect the keyshare tweaks,
		// then rotate all keyshares in one batched call — the per-leaf rotation
		// (ephemeral version bump + main CAS update) dominated large claims.
		type pendingClaimLeaf struct {
			transferLeaf *ent.TransferLeaf
			treeNode     *ent.TreeNode
			keyTweak     *pb.ClaimLeafKeyTweak
			keyshareID   uuid.UUID
		}
		pending := make([]pendingClaimLeaf, 0, len(leaves))
		tweaks := make([]*ent.SigningKeyshareTweak, 0, len(leaves))
		for _, leaf := range leaves {
			// Leaf and keyshare edges were validated non-nil by
			// lockAndHydrateLeafKeyshares over this same slice.
			treeNode := leaf.Edges.Leaf
			if len(leaf.KeyTweak) == 0 {
				return fmt.Errorf("key tweak for leaf %v is not set", leaf.ID)
			}
			keyTweakProto := &pb.ClaimLeafKeyTweak{}
			if err := proto.Unmarshal(leaf.KeyTweak, keyTweakProto); err != nil {
				return fmt.Errorf("unable to unmarshal key tweak for leaf %v: %w", leaf.ID, err)
			}
			if len(expectedDigests) > 0 {
				expected, ok := expectedDigests[treeNode.ID.String()]
				if !ok {
					return sparkerrors.FailedPreconditionInvalidState(fmt.Errorf(
						"commit payload for transfer %s carries tweak digests but none for leaf %s; refusing to apply an unbound tweak", transferID, treeNode.ID))
				}
				if stored := claimLeafKeyTweakProofsDigest(keyTweakProto); !bytes.Equal(stored, expected) {
					return sparkerrors.FailedPreconditionInvalidState(fmt.Errorf(
						"stored key tweak for transfer %s leaf %s has proofs digest %x but the commit bound tweak digest %x; applying it would tweak this SO's keyshare with a different polynomial than its peers",
						transferID, treeNode.ID, stored, expected))
				}
			}
			keyshare, ok := keysharesByID[treeNode.Edges.SigningKeyshare.ID]
			if !ok {
				return sparkerrors.InternalDataInconsistency(fmt.Errorf(
					"signing keyshare %s for leaf %s not found during claim commit", treeNode.Edges.SigningKeyshare.ID, treeNode.ID))
			}
			secretTweak, pubKeyTweak, pubSharesTweak, err := h.validateClaimLeafTweak(treeNode, keyTweakProto)
			if err != nil {
				return fmt.Errorf("unable to claim leaf tweak key for leaf %v: %w", leaf.ID, err)
			}
			if len(expectedPostTweakKeyshareDigests) > 0 {
				expected, ok := expectedPostTweakKeyshareDigests[treeNode.ID.String()]
				if !ok {
					return sparkerrors.FailedPreconditionInvalidState(fmt.Errorf(
						"commit payload for transfer %s carries post-tweak keyshare digests but none for leaf %s; refusing to apply an unbound tweak", transferID, treeNode.ID))
				}
				actual, err := claimPostTweakKeyshareDigest(keyshare, keyTweakProto)
				if err != nil {
					return fmt.Errorf("compute post-tweak keyshare digest for transfer %s leaf %s: %w", transferID, treeNode.ID, err)
				}
				if !bytes.Equal(actual, expected) {
					return sparkerrors.FailedPreconditionInvalidState(fmt.Errorf(
						"post-tweak keyshare digest for transfer %s leaf %s is %x but the commit bound %x; applying it would diverge this SO's keyshare from its peers",
						transferID, treeNode.ID, actual, expected))
				}
			}
			tweaks = append(tweaks, &ent.SigningKeyshareTweak{
				Keyshare:       keyshare,
				SecretTweak:    secretTweak,
				PubKeyTweak:    pubKeyTweak,
				PubSharesTweak: pubSharesTweak,
			})
			pending = append(pending, pendingClaimLeaf{
				transferLeaf: leaf,
				treeNode:     treeNode,
				keyTweak:     keyTweakProto,
				keyshareID:   keyshare.ID,
			})
		}

		rotatedKeyshares, err := ent.TweakSigningKeyshares(ctx, tweaks)
		if err != nil {
			// Restore per-leaf attribution: the batch error names the failing
			// keyshare; map it back to the leaf an operator would look up.
			var rotationErr *ent.KeyshareRotationError
			if errors.As(err, &rotationErr) {
				for _, p := range pending {
					if p.keyshareID == rotationErr.KeyshareID {
						return fmt.Errorf("unable to tweak keyshare %s for leaf %s in transfer %s: %w",
							rotationErr.KeyshareID, p.treeNode.ID, transferID, err)
					}
				}
			}
			return fmt.Errorf("unable to tweak keyshares for transfer %s: %w", transferID, err)
		}

		// Track successful leaf IDs to clear key_tweak in a single batch.
		clearedIDs := make([]uuid.UUID, 0, len(leaves))
		builders := make([]*ent.TreeNodeCreate, 0, len(leaves))
		appliedLogs := make([]struct {
			leafID     uuid.UUID
			numProofs  int
			proofsHash string
		}, 0, len(leaves))
		for _, p := range pending {
			leaf := p.transferLeaf
			treeNode := p.treeNode
			tweakedKeyshare, ok := rotatedKeyshares[p.keyshareID]
			if !ok {
				return sparkerrors.InternalDataInconsistency(fmt.Errorf(
					"rotated signing keyshare %s for leaf %s missing from batch result", p.keyshareID, treeNode.ID))
			}
			ownerSigningPubkey := treeNode.VerifyingPubkey.Sub(tweakedKeyshare.PublicKey)
			appliedLogs = append(appliedLogs, struct {
				leafID     uuid.UUID
				numProofs  int
				proofsHash string
			}{leaf.ID, len(p.keyTweak.GetSecretShareTweak().GetProofs()), hashClaimLeafKeyTweakProofs(p.keyTweak)})

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
					SetOwnerIdentityPubkey(*receiverIdentityPublicKey).
					SetOwnerSigningPubkey(ownerSigningPubkey).
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
					// Status is intentionally excluded from the update set:
					// claiming must never rewrite a leaf's status here — in
					// particular an exited-to-L1 leaf keeps its on-chain
					// status (see validateClaimLeafTweak), and adding
					// UpdateStatus() would let a stale read revive it.
					u.UpdateOwnerIdentityPubkey()
					u.UpdateOwnerSigningPubkey()
				}).
				Exec(ctx)
			if err != nil {
				return fmt.Errorf("unable to batch update tree node keys: %w", err)
			}
		}
		for _, l := range appliedLogs {
			logging.GetLoggerFromContext(ctx).Sugar().Infof(
				"claim key tweak applied for transfer %s leaf %s: num_proofs=%d proofs_hash=%s",
				transferID, l.leafID, l.numProofs, l.proofsHash,
			)
		}
		if len(clearedIDs) > 0 {
			if _, err := db.TransferLeaf.Update().Where(enttransferleaf.IDIn(clearedIDs...)).ClearKeyTweak().Save(ctx); err != nil {
				return fmt.Errorf("unable to batch clear leaf key tweaks: %w", err)
			}
		}

		// MIMO - Dual write status changes (parent skipped when receiver status is authoritative)
		isReceiverAuthoritative, authErr := isMimoReceiverStatusAuthoritative(ctx, transfer)
		if authErr != nil {
			return authErr
		}
		if !isReceiverAuthoritative {
			if _, err = transfer.Update().SetStatus(st.TransferStatusReceiverKeyTweakApplied).Save(ctx); err != nil {
				return fmt.Errorf("unable to update transfer status %v: %w", transferID, err)
			}
		}
		if receiver != nil {
			_, err = receiver.Update().SetStatus(st.TransferReceiverStatusKeyTweakApplied).Save(ctx)
			if err != nil {
				return fmt.Errorf("unable to update transfer receiver status %v: %w", transferID, err)
			}
		}

	case pbinternal.SettleKeyTweakAction_ROLLBACK:
		// WithLeaf: revertClaimTransfer restores each leaf tree node's refund txs.
		leaves, err := getTransferLeavesForReceiverQuery(transfer, receiver).WithLeaf().All(ctx)
		if err != nil {
			return fmt.Errorf("unable to get leaves from transfer %s: %w", transferID, err)
		}
		if err := h.revertClaimTransfer(ctx, transfer, receiver, leaves); err != nil {
			return fmt.Errorf("unable to revert claim transfer %v: %w", transferID, err)
		}
	default:
		return fmt.Errorf("invalid action %s", req.GetAction())
	}

	// Intentionally NOT committing the tx here — see the matching comment
	// in InitiateSettleReceiverKeyTweak. The mid-flow commit used to allow
	// concurrent ROLLBACKs to interleave between Phase 1 SELF and Phase 2
	// SELF on the coordinator; under the new design the outer
	// claim_transfer handler owns the single tx that spans both phases.
	// The consensus claim-transfer 2PC flow handler also calls this
	// function directly: its engine's request tx owns the commit lifecycle.
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

	// Gate the receiver-side kill switch: this background cron completes any
	// pending sender-initiated transfer including SSP-funded incoming ones,
	// so a freeze applied between the initial createTransfer commit and the
	// cron pickup must still stop the credit from landing in the frozen
	// wallet. Sender-side is intentionally not gated — completing a debit the
	// sender already pre-authorized is allowed even when the sender is frozen.
	if err := authz.EnforceWalletNotKillSwitched(ctx, transfer.ReceiverIdentityPubkey); err != nil {
		return err
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
func (h *TransferHandler) setSoCoordinatorKeyTweaks(ctx context.Context, transfer *ent.Transfer, keyTweakMap map[string]validatedKeyTweak) error {
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
			keyTweakBinary, err := proto.Marshal(keyTweak.Proto())
			if err != nil {
				return fmt.Errorf("failed to marshal key tweak for leaf %s: %w", leaf.ID, err)
			}
			_, err = applyLeafSignature(transferLeaf.Update(), keyTweak.Proto().GetSignature(), keyTweak.Proto().GetTypedSignature()).
				SetKeyTweak(keyTweakBinary).
				SetSecretCipher(keyTweak.Proto().GetSecretCipher()).
				Save(ctx)
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
