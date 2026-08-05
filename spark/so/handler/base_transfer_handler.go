package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	dbSql "database/sql"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"entgo.io/ent/dialect/sql"
	"entgo.io/ent/dialect/sql/sqlgraph"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/uuids"

	"go.uber.org/zap"

	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common"
	bitcointransaction "github.com/lightsparkdev/spark/common/bitcoin_transaction"
	"github.com/lightsparkdev/spark/common/logging"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	pbspark "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authz"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/preimagerequest"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/sparkinvoice"
	enttransfer "github.com/lightsparkdev/spark/so/ent/transfer"
	enttransferleaf "github.com/lightsparkdev/spark/so/ent/transferleaf"
	enttransferreceiver "github.com/lightsparkdev/spark/so/ent/transferreceiver"
	"github.com/lightsparkdev/spark/so/ent/treenode"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/helper"
	"github.com/lightsparkdev/spark/so/mimo"
	transferpkg "github.com/lightsparkdev/spark/so/transfer"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Validation constants to prevent resource exhaustion and DoS attacks
const (
	MaxLeavesToSend         = 1000              // Default fallback limit for leaf processing (can be overridden by knobs)
	MaxKeyTweakPackageSize  = 4 * 1024 * 1024   // 4MB limit for encrypted package
	MaxLeafIdLength         = 256               // Prevent extremely long leaf IDs
	MaxSecretShareSize      = 32                // Limit secret share size
	MaxSignatureSize        = 73                // Reasonable limit for ECDSA secp256k1 signatures
	MaxEstimatedMemoryUsage = 100 * 1024 * 1024 // 100MB limit for estimated memory usage

	// Buffer to prevent primary transfer creation too close to expiry time. The
	// buffer should allow enough time for a counter transfer to be created and
	// switch both transfers to non-cancellable status.
	//
	// |<-- Primary transfer expiration time --->|
	//                             |Safety buffer|
	// A ----------- B ----------- C ----------- D ----------- E
	// |             |             |             |             |
	// Primary      Can create   Deadline       Deadline      Primary
	// transfer     counter      for counter    for primary   transfer
	// created      transfer     transfer       transfer      cancelled
	PrimaryTransferExpiryTimeSafetyBuffer = 120 * time.Second
)

// swapPrimaryTransferExpiryOverride returns the server-enforced expiry for a
// swap v3 primary transfer: double the safety buffer so the user has enough
// time to reach the SSP for the counter transfer (diagram above). Shared by
// the legacy startTransferInternal path and the consensus coordinator entry so
// the two can never drift.
func swapPrimaryTransferExpiryOverride() *timestamppb.Timestamp {
	return timestamppb.New(time.Now().Add(2 * PrimaryTransferExpiryTimeSafetyBuffer))
}

type TransferRole int

const (
	// TransferRoleCoordinator is the role of the coordinator in a transfer.
	// The coordinator is reponsible to make sure that the transfer key tweak is applied to all other participants,
	// if the participants agree to the key tweak.
	TransferRoleCoordinator TransferRole = iota
	// TransferRoleParticipant is the role of a participant in a transfer.
	TransferRoleParticipant
)

// BaseTransferHandler is the base transfer handler that is shared for internal and external transfer handlers.
type BaseTransferHandler struct {
	config *so.Config
}

// NewBaseTransferHandler creates a new BaseTransferHandler.
func NewBaseTransferHandler(config *so.Config) BaseTransferHandler {
	return BaseTransferHandler{
		config: config,
	}
}

// loadLeafRefundMapsFromTransferPackage extracts CPFP, direct, and direct-from-CPFP
// refund maps from a TransferPackage. Returns three maps keyed by leaf ID.
func loadLeafRefundMapsFromTransferPackage(pkg *pbspark.TransferPackage) (cpfp, direct, directFromCpfp map[string][]byte) {
	cpfp = make(map[string][]byte)
	for _, leaf := range pkg.GetLeavesToSend() {
		cpfp[leaf.GetLeafId()] = leaf.GetRawTx()
	}
	direct = make(map[string][]byte)
	for _, leaf := range pkg.GetDirectLeavesToSend() {
		direct[leaf.GetLeafId()] = leaf.GetRawTx()
	}
	directFromCpfp = make(map[string][]byte)
	for _, leaf := range pkg.GetDirectFromCpfpLeavesToSend() {
		directFromCpfp[leaf.GetLeafId()] = leaf.GetRawTx()
	}
	return cpfp, direct, directFromCpfp
}

// loadLeafRefundMaps extracts refund maps from a StartTransferRequest,
// delegating to loadLeafRefundMapsFromTransferPackage when a TransferPackage
// is present and falling back to the legacy LeavesToSend field otherwise.
func loadLeafRefundMaps(req *pbspark.StartTransferRequest) (cpfp, direct, directFromCpfp map[string][]byte) {
	if req.GetTransferPackage() != nil {
		return loadLeafRefundMapsFromTransferPackage(req.GetTransferPackage())
	}
	cpfp = make(map[string][]byte)
	direct = make(map[string][]byte)
	directFromCpfp = make(map[string][]byte)
	for _, leaf := range req.GetLeavesToSend() {
		cpfp[leaf.GetLeafId()] = leaf.GetRefundTxSigningJob().GetRawTx()
		if leaf.GetDirectRefundTxSigningJob() != nil {
			direct[leaf.GetLeafId()] = leaf.GetDirectRefundTxSigningJob().GetRawTx()
		}
		if leaf.GetDirectFromCpfpRefundTxSigningJob() != nil {
			directFromCpfp[leaf.GetLeafId()] = leaf.GetDirectFromCpfpRefundTxSigningJob().GetRawTx()
		}
	}
	return cpfp, direct, directFromCpfp
}

func validateLegacyLeafRefundTxSigningJobs(leaves []*pbspark.LeafRefundTxSigningJob) error {
	seenLeafIDs := make(map[string]struct{}, len(leaves))
	for i, leaf := range leaves {
		if leaf == nil {
			return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("leaves_to_send[%d] is required", i))
		}
		leafIDValue := leaf.GetLeafId()
		parsedLeafID, err := uuid.Parse(leafIDValue)
		if err != nil {
			return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("unable to parse leaves_to_send[%d].leaf_id as a uuid: %s: %w", i, leafIDValue, err))
		}
		leafID := parsedLeafID.String()
		if _, exists := seenLeafIDs[leafID]; exists {
			return sparkerrors.InvalidArgumentDuplicateField(fmt.Errorf("duplicate leaf id in leaves_to_send: %s", leafID))
		}
		seenLeafIDs[leafID] = struct{}{}
		if leaf.GetRefundTxSigningJob() == nil {
			return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("leaves_to_send[%d].refund_tx_signing_job is required", i))
		}
	}
	return nil
}

// loadInternalLeafRefundMaps extracts refund maps from an InitiateTransferRequest,
// delegating to loadLeafRefundMapsFromTransferPackage when a TransferPackage
// is present and falling back to the legacy Leaves field otherwise.
func loadInternalLeafRefundMaps(req *pbinternal.InitiateTransferRequest) (cpfp, direct, directFromCpfp map[string][]byte) {
	if req.GetTransferPackage() != nil {
		return loadLeafRefundMapsFromTransferPackage(req.GetTransferPackage())
	}
	cpfp = make(map[string][]byte)
	direct = make(map[string][]byte)
	directFromCpfp = make(map[string][]byte)
	for _, leaf := range req.GetLeaves() {
		cpfp[leaf.GetLeafId()] = leaf.GetRawRefundTx()
		direct[leaf.GetLeafId()] = leaf.GetDirectRefundTx()
		directFromCpfp[leaf.GetLeafId()] = leaf.GetDirectFromCpfpRefundTx()
	}
	return cpfp, direct, directFromCpfp
}

func validateInternalInitiateTransferLeaves(leaves []*pbinternal.InitiateTransferLeaf) error {
	for i, leaf := range leaves {
		if leaf == nil {
			return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("leaves[%d] is required", i))
		}
	}
	return nil
}

// applyRefundSignatures applies sender-provided refund signatures to the three
// refund maps (CPFP, direct, direct-from-CPFP) using zero adaptor keys.
// Any nil signature map is skipped; direct and direct-from-CPFP signatures are
// applied together only when both are present.
func applyRefundSignatures(
	ctx context.Context,
	transferID string,
	cpfpMap, directMap, directFromCpfpMap map[string][]byte,
	cpfpSigs, directSigs, directFromCpfpSigs map[string][]byte,
) (map[string][]byte, map[string][]byte, map[string][]byte, error) {
	var err error
	if cpfpSigs != nil {
		cpfpMap, err = applySignaturesToTransactionsAndVerify(ctx, cpfpMap, cpfpSigs, false, keys.Public{})
		if err != nil {
			return nil, nil, nil, fmt.Errorf("failed to apply signatures to leaf cpfp refund map for transfer id: %s: %w", transferID, err)
		}
	}
	if directSigs != nil && directFromCpfpSigs != nil {
		directMap, err = applySignaturesToTransactionsAndVerify(ctx, directMap, directSigs, true, keys.Public{})
		if err != nil {
			return nil, nil, nil, fmt.Errorf("failed to apply signatures to leaf direct refund map for transfer id: %s: %w", transferID, err)
		}
		directFromCpfpMap, err = applySignaturesToTransactionsAndVerify(ctx, directFromCpfpMap, directFromCpfpSigs, false, keys.Public{})
		if err != nil {
			return nil, nil, nil, fmt.Errorf("failed to apply signatures to leaf direct from cpfp refund map for transfer id: %s: %w", transferID, err)
		}
	}
	return cpfpMap, directMap, directFromCpfpMap, nil
}

func validateLeafRefundTxOutput(refundTx *wire.MsgTx, receiverIdentityPubKey keys.Public) error {
	if len(refundTx.TxOut) == 0 {
		return fmt.Errorf("refund tx must have at least 1 output")
	}
	receiverP2trScript, err := common.P2TRScriptFromPubKey(receiverIdentityPubKey)
	if err != nil {
		return fmt.Errorf("unable to generate p2tr script from receiver pubkey: %w", err)
	}
	if !bytes.Equal(receiverP2trScript, refundTx.TxOut[0].PkScript) {
		return fmt.Errorf("refund tx is expected to send to receiver identity pubkey")
	}
	return nil
}

func parseRefundTx(refundBytes []byte) (*wire.MsgTx, error) {
	refundTx, err := common.TxFromRawTxBytes(refundBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse bytes: %w", err)
	}

	if err := common.ValidateBitcoinTxVersion(refundTx); err != nil {
		return nil, fmt.Errorf("refund tx version validation failed: %w", err)
	}

	if len(refundTx.TxIn) < 1 {
		return nil, fmt.Errorf("refund tx must have at least 1 input")
	}

	return refundTx, nil
}

// validateLeafRefundTxInputExact replaces the non "exact" variant in all instances except one, where it's not clear how to replace it.
func validateLeafRefundTxInputExact(refundTx *wire.MsgTx, expectedSequence uint32, expectedOutPoint *wire.OutPoint, expectedInputCount uint32) error {
	if refundTx.TxIn[0].Sequence != expectedSequence {
		return fmt.Errorf("wrong sequence number (timelock), expected %d, got %d", expectedSequence, refundTx.TxIn[0].Sequence)
	}

	if len(refundTx.TxIn) != int(expectedInputCount) {
		return fmt.Errorf("refund tx should have %d inputs, but has %d", expectedInputCount, len(refundTx.TxIn))
	}

	if refundTx.TxIn[0].PreviousOutPoint != *expectedOutPoint {
		return fmt.Errorf("unexpected input in refund tx")
	}

	return nil
}

// validateLeafRefundTxInput is meant to be replaced by the "exact" variant.
// That checks that time locks have an exact value
// while this only checks that time locks lie in a range, which is too lenient.
// Ideally, this function would be replaced and removed,
// but it's not clear how to replace one of its call sites, so that is deferred.
func validateLeafRefundTxInput(refundTx *wire.MsgTx, oldSequence uint32, expectedOutPoint *wire.OutPoint, expectedInputCount uint32) error {
	if refundTx.TxIn[0].Sequence&(1<<31) != 0 {
		return fmt.Errorf("refund tx input 0 sequence must have bit 31 clear to enable relative locktime, got %d", refundTx.TxIn[0].Sequence)
	}
	if oldSequence&(1<<22) != 0 {
		return fmt.Errorf("old sequence must have bit 22 clear to enable block-based relative locktime, got %d", oldSequence)
	}
	if refundTx.TxIn[0].Sequence&(1<<22) != 0 {
		return fmt.Errorf("refund tx input 0 sequence must have bit 22 clear to enable block-based relative locktime, got %d", refundTx.TxIn[0].Sequence)
	}

	newTimeLock := refundTx.TxIn[0].Sequence & 0xFFFF
	oldTimeLock := oldSequence & 0xFFFF
	if newTimeLock == 0 {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("time lock on the new refund tx must be greater than 0"))
	}
	if oldTimeLock <= spark.TimeLockInterval {
		return sparkerrors.InvalidArgumentLeafRenewalRequired(fmt.Errorf("time lock on the old refund tx %d is too small to transfer without reaching zero", oldTimeLock))
	}
	if newTimeLock+spark.TimeLockInterval > oldTimeLock {
		return sparkerrors.InvalidArgumentTimelockMismatch(fmt.Errorf("time lock on the new refund tx %d must be less than the old one %d", newTimeLock, oldTimeLock), oldTimeLock-spark.TimeLockInterval, newTimeLock)
	}
	if len(refundTx.TxIn) != int(expectedInputCount) {
		return fmt.Errorf("refund tx should have %d inputs, but has %d", expectedInputCount, len(refundTx.TxIn))
	}
	if refundTx.TxIn[0].PreviousOutPoint != *expectedOutPoint {
		return fmt.Errorf("unexpected input in refund tx")
	}
	return nil
}

func validateSendLeafDirectRefundTxs(senderLeaf *ent.TreeNode, receiverDirectRefundTxBytes []byte, receiverDirectFromCpfpRefundTxBytes []byte, receiverIdentityPubKey keys.Public, expectedInputCount uint32) error {
	senderDirectTx, err := parseRefundTx(senderLeaf.DirectTx)
	if err != nil {
		return fmt.Errorf("bad sender direct tx: %w", err)
	}

	senderRefundTx, err := parseRefundTx(senderLeaf.RawRefundTx)
	if err != nil {
		return fmt.Errorf("bad sender refund tx: %w", err)
	}

	receiverDirectRefundTx, err := parseRefundTx(receiverDirectRefundTxBytes)
	if err != nil {
		return fmt.Errorf("bad receiver direct refund tx: %w", err)
	}

	receiverDirectFromCpfpRefundTx, err := parseRefundTx(receiverDirectFromCpfpRefundTxBytes)
	if err != nil {
		return fmt.Errorf("bad receiver direct from cpfp refund tx: %w", err)
	}

	expectedReceiverDirectRefundOutPoint := wire.OutPoint{
		Hash:  senderDirectTx.TxHash(),
		Index: 0,
	}
	cpfpTimelock := bitcointransaction.GetTimelockFromSequence(senderRefundTx.TxIn[0].Sequence)

	expectedReceiverDirectRefundTxSequence, err := bitcointransaction.ValidateSequence(cpfpTimelock, bitcointransaction.TxTypeRefundDirect, receiverDirectRefundTx.TxIn[0].Sequence)
	if err != nil {
		return sparkerrors.WrapErrorWithMetadata(
			fmt.Errorf("unable to validate direct refund tx inputs: %w", err),
			map[string]string{sparkerrors.ErrorMetadataLeafID: senderLeaf.ID.String()})
	}
	if err := validateLeafRefundTxInputExact(receiverDirectRefundTx, expectedReceiverDirectRefundTxSequence, &expectedReceiverDirectRefundOutPoint, expectedInputCount); err != nil {
		return fmt.Errorf("unable to validate direct refund tx inputs: %w", err)
	}

	expectedReceiverDirectFromCpfpRefundTxSequence, err := bitcointransaction.ValidateSequence(cpfpTimelock, bitcointransaction.TxTypeRefundDirectFromCPFP, receiverDirectFromCpfpRefundTx.TxIn[0].Sequence)
	if err != nil {
		return sparkerrors.WrapErrorWithMetadata(
			fmt.Errorf("unable to validate direct from cpfp refund tx inputs: %w", err),
			map[string]string{sparkerrors.ErrorMetadataLeafID: senderLeaf.ID.String()})
	}
	if err := validateLeafRefundTxInputExact(receiverDirectFromCpfpRefundTx, expectedReceiverDirectFromCpfpRefundTxSequence, new(senderRefundTx.TxIn[0].PreviousOutPoint), expectedInputCount); err != nil {
		return fmt.Errorf("unable to validate direct from cpfp refund tx inputs: %w", err)
	}

	if err := validateLeafRefundTxOutput(receiverDirectRefundTx, receiverIdentityPubKey); err != nil {
		return fmt.Errorf("unable to validate direct refund tx output: %w", err)
	}
	if err := validateLeafRefundTxOutput(receiverDirectFromCpfpRefundTx, receiverIdentityPubKey); err != nil {
		return fmt.Errorf("unable to validate direct from cpfp refund tx output: %w", err)
	}

	return nil
}

func validateSendLeafRefundTxs(leaf *ent.TreeNode, rawRefundTx []byte, directRefundTx []byte, directFromCpfpRefundTx []byte, receiverIdentityPubKey keys.Public, expectedInputCount uint32, requireDirectTx bool) error {
	leafIsWatchtowerReady := len(leaf.DirectTx) > 0
	if leafIsWatchtowerReady {
		hasDirectRefundTx := len(directRefundTx) > 0
		hasDirectFromCpfpRefundTx := len(directFromCpfpRefundTx) > 0
		if hasDirectRefundTx != hasDirectFromCpfpRefundTx {
			return fmt.Errorf("both direct refund txs are required when either direct refund tx is provided")
		}
		if hasDirectRefundTx {
			if err := validateSendLeafDirectRefundTxs(leaf, directRefundTx, directFromCpfpRefundTx, receiverIdentityPubKey, expectedInputCount); err != nil {
				return err
			}
		} else if requireDirectTx {
			return fmt.Errorf("DirectNodeTxSignature is required. Please upgrade to the latest SDK version")
		}
	}

	newCpfpRefundTx, err := parseRefundTx(rawRefundTx)
	if err != nil {
		return fmt.Errorf("unable to load new cpfp refund tx: %w", err)
	}

	oldCpfpRefundTx, err := parseRefundTx(leaf.RawRefundTx)
	if err != nil {
		return fmt.Errorf("unable to load old cpfp refund tx: %w", err)
	}
	oldCpfpRefundTxIn := oldCpfpRefundTx.TxIn[0]

	nodeTx, err := parseRefundTx(leaf.RawTx)
	if err != nil {
		return fmt.Errorf("unable to load node tx: %w", err)
	}
	expectedOutPoint := wire.OutPoint{
		Hash:  nodeTx.TxHash(),
		Index: 0,
	}
	// expectedNewCpfpRefundSequence := oldCpfpRefundTxIn.Sequence - spark.TimeLockInterval

	if err := validateLeafRefundTxInput(newCpfpRefundTx, oldCpfpRefundTxIn.Sequence, &expectedOutPoint, expectedInputCount); err != nil {
		return wrapLeafValidationError(err, "unable to validate cpfp refund tx inputs", leaf.ID)
	}

	if err := validateLeafRefundTxOutput(newCpfpRefundTx, receiverIdentityPubKey); err != nil {
		return fmt.Errorf("unable to validate cpfp refund tx output: %w", err)
	}

	return nil
}

func (h *BaseTransferHandler) createTransfer(
	ctx context.Context,
	transferID uuid.UUID,
	pkg *pbspark.TransferPackage,
	transferType st.TransferType,
	expiryTime time.Time,
	senderIdentityPubKey keys.Public,
	receiverIdentityPubKey keys.Public,
	leafCpfpRefundMap map[string][]byte,
	leafDirectRefundMap map[string][]byte,
	leafDirectFromCpfpRefundMap map[string][]byte,
	leafTweakMap map[string]validatedKeyTweak,
	role TransferRole,
	requireDirectTx bool,
	sparkInvoice string,
	primaryTransferId uuid.UUID,
	connectorTx []byte,
) (*ent.Transfer, map[string]*ent.TreeNode, error) {
	if expiryTime.Unix() != 0 && expiryTime.Before(time.Now()) {
		return nil, nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid expiry_time %v", expiryTime))
	}

	if transferType == st.TransferTypePrimarySwapV3 {
		if expiryTime.Before(time.Now().Add(PrimaryTransferExpiryTimeSafetyBuffer)) {
			return nil, nil, fmt.Errorf("invalid expiry_time for primary swap transfer %s: less than safety buffer: %s", transferID, expiryTime)
		}
	}

	var status st.TransferStatus
	if len(leafTweakMap) > 0 {
		if role == TransferRoleCoordinator {
			status = st.TransferStatusSenderInitiatedCoordinator
		} else {
			status = st.TransferStatusSenderKeyTweakPending
		}
	} else {
		status = st.TransferStatusSenderInitiated
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to get database transaction: %w", err)
	}

	invoiceID := uuid.Nil
	if len(sparkInvoice) > 0 {
		invoiceID, err = createAndLockSparkInvoice(ctx, sparkInvoice)
		if err != nil {
			return nil, nil, fmt.Errorf("unable to create and lock spark invoice: %w", err)
		}
	}

	transferCreate := db.Transfer.Create().
		SetID(transferID).
		SetSenderIdentityPubkey(senderIdentityPubKey).
		SetReceiverIdentityPubkey(receiverIdentityPubKey).
		SetStatus(status).
		SetTotalValue(0).
		SetExpiryTime(expiryTime).
		SetType(transferType)

	if len(sparkInvoice) > 0 && invoiceID != uuid.Nil {
		transferCreate = transferCreate.SetSparkInvoiceID(invoiceID)
	}

	leaves, network, err := loadLeavesWithLock(ctx, db, leafCpfpRefundMap)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to load leaves: %w", err)
	}

	for _, leaf := range leaves {
		if err := leafAvailableStatus(leaf); err != nil {
			return nil, nil, fmt.Errorf("unable to validate leaf %s: %w", leaf.ID, err)
		}
	}

	// For counter swap v3, we need to validate the primary transfer is in the right status and has enough time left.
	if transferType == st.TransferTypeCounterSwapV3 {
		primaryTransfer, err := db.Transfer.Query().
			Where(enttransfer.IDEQ(primaryTransferId)).
			WithTransferSenders().
			WithTransferReceivers().
			ForUpdate().
			Only(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("unable to find primary swap transfer id=%s", primaryTransferId)
		}
		if primaryTransfer.Type != st.TransferTypePrimarySwapV3 {
			return nil, nil, fmt.Errorf("primary swap transfer %s has invalid type %s", primaryTransferId, primaryTransfer.Type)
		}
		// Check that the SO holds the correct refunds for the primary transfer.
		if primaryTransfer.Status != st.TransferStatusSenderKeyTweakPending && primaryTransfer.Status != st.TransferStatusSenderInitiatedCoordinator {
			return nil, nil, fmt.Errorf("primary swap transfer %s is not in the right status, got %s", primaryTransferId, primaryTransfer.Status)
		}
		// Add safety buffer to prevent counter transfer creation too close to expiry time
		if primaryTransfer.ExpiryTime.Before(time.Now().Add(PrimaryTransferExpiryTimeSafetyBuffer)) {
			return nil, nil, fmt.Errorf("primary swap transfer %s has expired or is about to expire (within safety buffer of %v), expiry time is %s", primaryTransferId, PrimaryTransferExpiryTimeSafetyBuffer, primaryTransfer.ExpiryTime)
		}
		if primaryTransfer.Network != network {
			return nil, nil, fmt.Errorf("primary swap transfer %s network %s does not match counter transfer network %s", primaryTransferId, primaryTransfer.Network, network)
		}
		transferCreate.SetPrimarySwapTransfer(primaryTransfer)
		// The counter transfer amount should be the same as the primary transfer amount until we implement fees. Then we should probably validate a statement from the user that they accepted the fees.
		counterTransferAmount := getTotalTransferValue(leaves)
		if primaryTransfer.TotalValue != counterTransferAmount {
			return nil, nil, fmt.Errorf("primary swap transfer %s amount %d does not match counter transfer amount %d", primaryTransferId, primaryTransfer.TotalValue, counterTransferAmount)
		}
		// Validate that the parties in the Swap V3 counter transfer are the reverse of the primary transfer to ensure atomic swap correctness
		primarySender, primaryReceiver, err := mimo.GetSingleTransferSenderReceiver(primaryTransfer)
		if err != nil {
			return nil, nil, err
		}
		if !primarySender.Equals(receiverIdentityPubKey) {
			return nil, nil, fmt.Errorf("counter transfer receiver must be the primary transfer sender: expected %s, got %s", primarySender, receiverIdentityPubKey)
		}
		if !primaryReceiver.Equals(senderIdentityPubKey) {
			return nil, nil, fmt.Errorf("counter transfer sender must be the primary transfer receiver: expected %s, got %s", primaryReceiver, senderIdentityPubKey)
		}
	}

	if transferType == st.TransferTypeTransfer || transferType == st.TransferTypeSwap || transferType == st.TransferTypeCounterSwap || transferType == st.TransferTypePrimarySwapV3 || transferType == st.TransferTypeCounterSwapV3 || transferType == st.TransferTypeCooperativeExit {
		if err := h.validateAndConstructBitcoinTransactions(ctx, transferPackageLeafIDLists(pkg), transferType, leaves, leafCpfpRefundMap, leafDirectRefundMap, leafDirectFromCpfpRefundMap, receiverIdentityPubKey, connectorTx); err != nil {
			return nil, nil, fmt.Errorf("unable to validate and construct bitcoin transactions: %w, transfer id: %s", err, transferID)
		}
	}

	transfer, err := transferCreate.SetNetwork(network).Save(ctx)
	if err != nil {
		if sqlgraph.IsUniqueConstraintError(err) {
			return nil, nil, sparkerrors.AlreadyExistsDuplicateOperation(fmt.Errorf("transfer already exists: %w", err))
		}
		return nil, nil, fmt.Errorf("unable to create transfer: %w", err)
	}

	transferSender, err := createTransferSender(ctx, db, transfer, senderIdentityPubKey)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to create transfer sender: %w", err)
	}
	transferReceiver, err := createTransferReceiver(ctx, db, transfer, receiverIdentityPubKey, st.TransferReceiverStatusInitiated)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to create transfer receiver: %w", err)
	}

	transfer, err = db.Transfer.Query().
		Where(enttransfer.ID(transfer.ID)).
		WithTransferSenders().
		WithTransferReceivers().
		Only(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to load transfer with edges: %w", err)
	}

	if len(leafCpfpRefundMap) == 0 {
		return nil, nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("must provide at least one leaf for transfer"))
	}

	switch transferType {
	case st.TransferTypeCooperativeExit:
		err = h.validateCooperativeExitLeaves(ctx, transfer, leaves, leafCpfpRefundMap, leafDirectRefundMap, leafDirectFromCpfpRefundMap, receiverIdentityPubKey, requireDirectTx)
	case st.TransferTypeTransfer, st.TransferTypeSwap, st.TransferTypeCounterSwap:
		err = h.validateTransferLeaves(ctx, transfer, leaves, leafCpfpRefundMap, leafDirectRefundMap, leafDirectFromCpfpRefundMap, receiverIdentityPubKey, requireDirectTx)
	case st.TransferTypeUtxoSwap:
		err = h.validateUtxoSwapLeaves(ctx, transfer, leaves, leafCpfpRefundMap, leafDirectRefundMap, leafDirectFromCpfpRefundMap, receiverIdentityPubKey, requireDirectTx)
	case st.TransferTypePreimageSwap:
		err = h.validatePreimageSwapLeaves(ctx, transfer, leaves, leafCpfpRefundMap, leafDirectRefundMap, leafDirectFromCpfpRefundMap, receiverIdentityPubKey, requireDirectTx)
	case st.TransferTypePrimarySwapV3, st.TransferTypeCounterSwapV3:
		err = h.validateSwapV3Leaves(ctx, transfer, leaves, leafCpfpRefundMap, receiverIdentityPubKey)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("unable to validate transfer leaves: %w", err)
	}

	err = createTransferLeaves(ctx, db, transfer, transferSender, transferReceiver, leaves, leafCpfpRefundMap, leafDirectRefundMap, leafDirectFromCpfpRefundMap, leafTweakMap)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to create transfer leaves: %w", err)
	}

	err = setTotalTransferValue(ctx, db, transfer, leaves)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to update transfer total value: %w", err)
	}

	leaves, err = lockLeaves(ctx, db, leaves)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to lock leaves: %w", err)
	}

	leafMap := make(map[string]*ent.TreeNode)
	for _, leaf := range leaves {
		leafMap[leaf.ID.String()] = leaf
	}

	return transfer, leafMap, nil
}

// createTransferV3 is the entry into the transfer core: it creates a transfer
// with one sender and multiple receivers, consuming a transferSpec produced by
// a request front plus the transfer semantics (type, role, direct-refund
// requirement) chosen by the calling flow. Each leaf is associated with a
// specific receiver via leafReceiverMap, and validation is done per-receiver
// group since refund outputs must pay to the correct receiver.
func (h *BaseTransferHandler) createTransferV3(
	ctx context.Context,
	spec *transferSpec,
	transferType st.TransferType,
	role TransferRole,
	requireDirectTx bool,
) (*ent.Transfer, map[string]*ent.TreeNode, error) {
	transferID := spec.transferID
	pkgLeafIDs := spec.pkgLeafIDs
	expiryTime := spec.expiryTime
	senderIdentityPubKey := spec.senderIDPK
	receivers := spec.receivers
	leafReceiverMap := spec.leafReceiverMap
	leafCpfpRefundMap := spec.cpfpRefunds
	leafDirectRefundMap := spec.directRefunds
	leafDirectFromCpfpRefundMap := spec.dfcRefunds
	leafTweakMap := spec.keyTweaks
	sparkInvoice := spec.sparkInvoice

	if expiryTime.Unix() != 0 && expiryTime.Before(time.Now()) {
		return nil, nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid expiry_time %v", expiryTime))
	}

	var transferStatus st.TransferStatus
	if role == TransferRoleCoordinator {
		transferStatus = st.TransferStatusSenderInitiatedCoordinator
	} else {
		transferStatus = st.TransferStatusSenderKeyTweakPending
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to get database transaction: %w", err)
	}

	leaves, network, err := loadLeavesWithLock(ctx, db, leafCpfpRefundMap)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to load leaves: %w", err)
	}

	for _, leaf := range leaves {
		if err := leafAvailableStatus(leaf); err != nil {
			return nil, nil, fmt.Errorf("unable to validate leaf %s: %w", leaf.ID, err)
		}
	}

	// Group leaves by receiver for per-receiver claiming.
	type receiverGroup struct {
		receiverPubKey keys.Public
		leaves         []*ent.TreeNode
		cpfpMap        map[string][]byte
		directMap      map[string][]byte
		directCpfpMap  map[string][]byte
	}
	groupsByReceiver := make(map[keys.Public]*receiverGroup)
	for _, leaf := range leaves {
		recvPK, ok := leafReceiverMap[leaf.ID.String()]
		if !ok {
			return nil, nil, fmt.Errorf("leaf %s not found in leaf-receiver map", leaf.ID)
		}
		group, ok := groupsByReceiver[recvPK]
		if !ok {
			group = &receiverGroup{
				receiverPubKey: recvPK,
				cpfpMap:        make(map[string][]byte),
				directMap:      make(map[string][]byte),
				directCpfpMap:  make(map[string][]byte),
			}
			groupsByReceiver[recvPK] = group
		}
		group.leaves = append(group.leaves, leaf)
		leafID := leaf.ID.String()
		if v, ok := leafCpfpRefundMap[leafID]; ok {
			group.cpfpMap[leafID] = v
		}
		if v, ok := leafDirectRefundMap[leafID]; ok {
			group.directMap[leafID] = v
		}
		if v, ok := leafDirectFromCpfpRefundMap[leafID]; ok {
			group.directCpfpMap[leafID] = v
		}
	}

	// Validate bitcoin transactions per-receiver group (refund outputs must pay
	// to the correct receiver). UtxoSwap dispatches to its own validator, same
	// split the legacy createTransfer makes: validateAndConstructBitcoinTransactions
	// has no UtxoSwap case (utxo swaps don't require direct refund txs), but the
	// legacy path's validateUtxoSwapLeaves still output-validates direct refund
	// txs when the sender supplies them — every submitted tx gets FROST-signed,
	// so an unvalidated output would let the sender redirect the receiver's
	// direct unilateral-exit path to itself.
	for _, g := range groupsByReceiver {
		if transferType == st.TransferTypeUtxoSwap {
			if err := validateUtxoSwapLeafRefundTxsV3(g.leaves, g.cpfpMap, g.directMap, g.directCpfpMap, g.receiverPubKey, requireDirectTx); err != nil {
				return nil, nil, fmt.Errorf("unable to validate utxo swap refund txs for receiver %s: %w", g.receiverPubKey, err)
			}
			continue
		}
		// Preimage-swap refunds are validated against each leaf's own destination on the lightning
		// path before Prepare, and validateTransferLeaves still requires the cpfp refund to
		// decrement its timelock. The direct and direct-from-cpfp sequences stay unvalidated, as
		// they are on v3: their destinations are checked, but nothing enforces the exit ladder.
		if transferType == st.TransferTypePreimageSwap {
			continue
		}
		if err := h.validateAndConstructBitcoinTransactions(ctx, pkgLeafIDs, transferType, g.leaves, g.cpfpMap, g.directMap, g.directCpfpMap, g.receiverPubKey, nil); err != nil {
			return nil, nil, fmt.Errorf("unable to validate bitcoin transactions for receiver %s: %w", g.receiverPubKey, err)
		}
	}

	// Use the first receiver as the "primary" receiver for the deprecated Transfer.ReceiverIdentityPubkey field.
	primaryReceiver := receivers[0]

	transferCreate := db.Transfer.Create().
		SetID(transferID).
		SetSenderIdentityPubkey(senderIdentityPubKey).
		SetReceiverIdentityPubkey(primaryReceiver).
		SetStatus(transferStatus).
		SetTotalValue(0).
		SetExpiryTime(expiryTime).
		SetType(transferType).
		SetNetwork(network)

	if len(sparkInvoice) > 0 {
		invoiceID, err := createAndLockSparkInvoice(ctx, sparkInvoice)
		if err != nil {
			return nil, nil, fmt.Errorf("unable to create and lock spark invoice: %w", err)
		}
		if invoiceID != uuid.Nil {
			transferCreate = transferCreate.SetSparkInvoiceID(invoiceID)
		}
	}

	transfer, err := transferCreate.Save(ctx)
	if err != nil {
		if sqlgraph.IsUniqueConstraintError(err) {
			return nil, nil, sparkerrors.AlreadyExistsDuplicateOperation(fmt.Errorf("transfer already exists: %w", err))
		}
		return nil, nil, fmt.Errorf("unable to create transfer: %w", err)
	}

	transferSender, err := createTransferSender(ctx, db, transfer, senderIdentityPubKey)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to create transfer sender: %w", err)
	}

	// Create one TransferReceiver per receiver, then create transfer leaves for each group.
	for _, g := range groupsByReceiver {
		transferReceiver, err := createTransferReceiver(ctx, db, transfer, g.receiverPubKey, st.TransferReceiverStatusInitiated)
		if err != nil {
			return nil, nil, fmt.Errorf("unable to create transfer receiver: %w", err)
		}

		err = createTransferLeaves(ctx, db, transfer, transferSender, transferReceiver, g.leaves, g.cpfpMap, g.directMap, g.directCpfpMap, leafTweakMap)
		if err != nil {
			return nil, nil, fmt.Errorf("unable to create transfer leaves for receiver %s: %w", g.receiverPubKey, err)
		}
	}

	transfer, err = db.Transfer.Query().
		Where(enttransfer.ID(transfer.ID)).
		WithTransferSenders().
		WithTransferReceivers().
		Only(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to load transfer with edges: %w", err)
	}

	if len(leafCpfpRefundMap) == 0 {
		return nil, nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("must provide at least one leaf for transfer"))
	}

	// Validate transfer leaves per-receiver group.
	for _, g := range groupsByReceiver {
		err = h.validateTransferLeaves(ctx, transfer, g.leaves, g.cpfpMap, g.directMap, g.directCpfpMap, g.receiverPubKey, requireDirectTx)
		if err != nil {
			return nil, nil, fmt.Errorf("unable to validate transfer leaves for receiver %s: %w", g.receiverPubKey, err)
		}
	}

	err = setTotalTransferValue(ctx, db, transfer, leaves)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to update transfer total value: %w", err)
	}

	leaves, err = lockLeaves(ctx, db, leaves)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to lock leaves: %w", err)
	}

	leafMap := make(map[string]*ent.TreeNode)
	for _, leaf := range leaves {
		leafMap[leaf.ID.String()] = leaf
	}

	return transfer, leafMap, nil
}

func createAndLockSparkInvoice(ctx context.Context, sparkInvoice string) (uuid.UUID, error) {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return uuid.Nil, fmt.Errorf("unable to get database transaction: %w", err)
	}
	decoded, err := common.ParseSparkInvoice(sparkInvoice)
	if err != nil {
		return uuid.Nil, fmt.Errorf("unable to parse spark invoice: %w", err)
	}
	var expiry *time.Time
	if decoded.ExpiryTime != nil && decoded.ExpiryTime.IsValid() {
		expiry = new(decoded.ExpiryTime.AsTime())
	}
	err = db.SparkInvoice.Create().
		SetID(decoded.Id).
		SetSparkInvoice(sparkInvoice).
		SetReceiverPublicKey(decoded.ReceiverPublicKey).
		SetNillableExpiryTime(expiry).
		OnConflictColumns(sparkinvoice.FieldID).
		DoNothing().
		Exec(ctx)
	// Do not update an invoice if one already exists with the same ID.
	// Ent Create expects a returning row, but ON CONFLICT DO NOTHING returns 0 rows.
	// As 0 rows is expected in conflict cases, ignore dbSql.ErrNoRows.
	if err != nil && !errors.Is(err, dbSql.ErrNoRows) {
		return uuid.Nil, fmt.Errorf("unable to create spark invoice: %w", err)
	}

	storedInvoice, err := db.SparkInvoice.
		Query().
		Where(sparkinvoice.IDEQ(decoded.Id)).
		ForUpdate().
		Only(ctx)
	if err != nil {
		return uuid.Nil, fmt.Errorf("lock invoice: %w", err)
	}
	if storedInvoice.SparkInvoice != sparkInvoice {
		return uuid.Nil, sparkerrors.AlreadyExistsDuplicateOperation(fmt.Errorf("conflicting invoices found for id: %s. Decoded request invoice: %s", storedInvoice.ID, sparkInvoice))
	}

	// Check if an existing transfer is in flight or paid with this invoice.
	paidOrInFlightTransferExists, err := db.Transfer.
		Query().
		Where(
			enttransfer.HasSparkInvoiceWith(sparkinvoice.IDEQ(storedInvoice.ID)),
			enttransfer.StatusNotIn(
				// If an invoice has an edge to a transfer in any other state
				// that invoice is considered paid or in flight. Do not pay it again.
				st.TransferStatusReturned,
			),
		).
		Exist(ctx)
	if err != nil {
		return uuid.Nil, fmt.Errorf("failed to query transfer: %w", err)
	}
	if paidOrInFlightTransferExists {
		return uuid.Nil, sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("invoice has already been paid"))
	}
	return storedInvoice.ID, nil
}

func loadLeavesWithLock(ctx context.Context, db *ent.Client, leafRefundMap map[string][]byte) ([]*ent.TreeNode, btcnetwork.Network, error) {
	return loadLeaves(ctx, db, leafRefundMap, true)
}

// loadLeaves loads the leaves named by leafRefundMap with tree edges and the
// same presence/network consistency checks as loadLeavesWithLock. forUpdate
// selects FOR UPDATE row locks; pass false for read-only pre-checks (e.g.
// summing immutable leaf values for a signature fast-fail) where holding row
// locks across subsequent network round trips would block concurrent
// transfers/claims/exits touching the same leaves — the mutating path must
// still take the locking variant before any state change.
func loadLeaves(ctx context.Context, db *ent.Client, leafRefundMap map[string][]byte, forUpdate bool) ([]*ent.TreeNode, btcnetwork.Network, error) {
	leafUUIDs, err := uuids.ParseSeq(maps.Keys(leafRefundMap))
	if err != nil {
		return nil, btcnetwork.Unspecified, fmt.Errorf("unable to parse leaf IDs: %w", err)
	}

	query := db.TreeNode.Query().
		Where(treenode.IDIn(leafUUIDs...)).
		WithTree()
	if forUpdate {
		query = query.ForUpdate()
	}
	leaves, err := query.All(ctx)
	if err != nil {
		return nil, btcnetwork.Unspecified, fmt.Errorf("unable to find leaves: %w", err)
	}
	if len(leaves) != len(leafRefundMap) {
		return nil, btcnetwork.Unspecified, errors.New("some leaves not found")
	}

	var network *btcnetwork.Network
	for _, leaf := range leaves {
		tree := leaf.Edges.Tree
		if tree == nil {
			return nil, btcnetwork.Unspecified, fmt.Errorf("unable to find tree for leaf %s", leaf.ID)
		}
		if network == nil {
			network = &tree.Network
		} else if tree.Network != *network {
			return nil, btcnetwork.Unspecified, errors.New("leaves sent for transfer must be on the same network")
		}
	}
	if network == nil {
		return nil, btcnetwork.Unspecified, errors.New("no network found")
	}
	return leaves, *network, nil
}

func (h *BaseTransferHandler) validateCooperativeExitLeaves(ctx context.Context, transfer *ent.Transfer, leaves []*ent.TreeNode, leafCpfpRefundMap map[string][]byte, leafDirectRefundMap map[string][]byte, leafDirectFromCpfpRefundMap map[string][]byte, receiverIdentityPublicKey keys.Public, requireDirectTx bool) error {
	for _, leaf := range leaves {
		directRefundTx := leafDirectRefundMap[leaf.ID.String()]
		intermediateDirectFromCpfpRefundTx := leafDirectFromCpfpRefundMap[leaf.ID.String()]

		rawRefundTx, exist := leafCpfpRefundMap[leaf.ID.String()]
		if !exist {
			return fmt.Errorf("leaf %s not found in cpfp refund map", leaf.ID)
		}

		err := validateSendLeafRefundTxs(leaf, rawRefundTx, directRefundTx, intermediateDirectFromCpfpRefundTx, receiverIdentityPublicKey, 2, requireDirectTx)
		if err != nil {
			return fmt.Errorf("unable to validate refund tx for leaf %s: %w", leaf.ID, err)
		}
		err = h.LeafAvailableToTransfer(ctx, leaf, transfer)
		if err != nil {
			return fmt.Errorf("unable to validate leaf %s: %w", leaf.ID, err)
		}
	}
	return nil
}

func (h *BaseTransferHandler) validatePreimageSwapLeaves(
	ctx context.Context,
	transfer *ent.Transfer,
	leaves []*ent.TreeNode,
	leafCpfpRefundMap map[string][]byte,
	leafDirectRefundMap map[string][]byte,
	leafDirectFromCpfpRefundMap map[string][]byte,
	receiverIdentityPublicKey keys.Public,
	requireDirectTx bool,
) error {
	for _, leaf := range leaves {
		err := h.LeafAvailableToTransfer(ctx, leaf, transfer)
		if err != nil {
			return fmt.Errorf("unable to validate leaf %s: %w", leaf.ID, err)
		}
	}
	return nil
}

func (h *BaseTransferHandler) validateUtxoSwapLeaves(
	ctx context.Context,
	transfer *ent.Transfer,
	leaves []*ent.TreeNode,
	leafCpfpRefundMap map[string][]byte,
	leafDirectRefundMap map[string][]byte,
	leafDirectFromCpfpRefundMap map[string][]byte,
	receiverIdentityPublicKey keys.Public,
	requireDirectTx bool,
) error {
	for _, leaf := range leaves {
		directRefundTx := leafDirectRefundMap[leaf.ID.String()]
		intermediateDirectFromCpfpRefundTx := leafDirectFromCpfpRefundMap[leaf.ID.String()]

		rawRefundTx, exist := leafCpfpRefundMap[leaf.ID.String()]
		if !exist {
			return fmt.Errorf("leaf %s not found in cpfp refund map", leaf.ID)
		}

		err := validateSendLeafRefundTxs(leaf, rawRefundTx, directRefundTx, intermediateDirectFromCpfpRefundTx, receiverIdentityPublicKey, 1, requireDirectTx)
		if err != nil {
			return fmt.Errorf("unable to validate refund tx for leaf %s: %w", leaf.ID, err)
		}
		err = h.LeafAvailableToTransfer(ctx, leaf, transfer)
		if err != nil {
			return fmt.Errorf("unable to validate leaf %s: %w", leaf.ID, err)
		}
	}
	return nil
}

// validateUtxoSwapLeafRefundTxsV3 is the v3 (consensus-path) counterpart of
// validateUtxoSwapLeaves' refund-tx validation: cpfp refund txs are required,
// direct/direct-from-cpfp refund txs are optional for utxo swaps
// (requireDirectTx=false on the legacy wire contract) but MUST be
// output-validated when present, since every submitted tx is FROST-signed.
// LeafAvailableToTransfer is intentionally not repeated here — createTransferV3
// runs it via validateTransferLeaves after the transfer row exists.
func validateUtxoSwapLeafRefundTxsV3(
	leaves []*ent.TreeNode,
	leafCpfpRefundMap map[string][]byte,
	leafDirectRefundMap map[string][]byte,
	leafDirectFromCpfpRefundMap map[string][]byte,
	receiverIdentityPubKey keys.Public,
	requireDirectTx bool,
) error {
	for _, leaf := range leaves {
		leafID := leaf.ID.String()
		rawRefundTx, exist := leafCpfpRefundMap[leafID]
		if !exist {
			return fmt.Errorf("leaf %s not found in cpfp refund map", leafID)
		}
		if err := validateSendLeafRefundTxs(leaf, rawRefundTx, leafDirectRefundMap[leafID], leafDirectFromCpfpRefundMap[leafID], receiverIdentityPubKey, 1, requireDirectTx); err != nil {
			return fmt.Errorf("unable to validate refund txs for utxo swap leaf %s: %w", leafID, err)
		}
	}
	return nil
}

// validateTransferLeaves checks that each leaf exists in the refund map,
// has a valid refund tx, and is available to transfer.
func (h *BaseTransferHandler) validateTransferLeaves(
	ctx context.Context,
	transfer *ent.Transfer,
	leaves []*ent.TreeNode,
	leafCpfpRefundMap map[string][]byte,
	leafDirectRefundMap map[string][]byte,
	leafDirectFromCpfpRefundMap map[string][]byte,
	receiverIdentityPublicKey keys.Public,
	requireDirectTx bool,
) error {
	for _, leaf := range leaves {
		rawRefundTx, exist := leafCpfpRefundMap[leaf.ID.String()]
		if !exist {
			return fmt.Errorf("leaf %s not found in cpfp refund map", leaf.ID)
		}

		err := validateSendLeafRefundTxs(leaf, rawRefundTx, nil, nil, receiverIdentityPublicKey, 1, false)
		if err != nil {
			return fmt.Errorf("unable to validate refund tx for leaf %s: %w", leaf.ID, err)
		}
		err = h.LeafAvailableToTransfer(ctx, leaf, transfer)
		if err != nil {
			return fmt.Errorf("unable to validate leaf %s: %w", leaf.ID, err)
		}
	}
	return nil
}

func (h *BaseTransferHandler) validateSwapV3Leaves(
	ctx context.Context,
	transfer *ent.Transfer,
	leaves []*ent.TreeNode,
	leafCpfpRefundMap map[string][]byte,
	receiverIdentityPublicKey keys.Public,
) error {
	for _, leaf := range leaves {
		rawRefundTx, exist := leafCpfpRefundMap[leaf.ID.String()]
		if !exist {
			return fmt.Errorf("leaf %s not found in cpfp refund map", leaf.ID)
		}

		err := validateSendLeafRefundTxs(leaf, rawRefundTx, nil, nil, receiverIdentityPublicKey, 1, false)
		if err != nil {
			return fmt.Errorf("unable to validate refund tx for leaf %s: %w", leaf.ID, err)
		}
		err = h.LeafAvailableToTransfer(ctx, leaf, transfer)
		if err != nil {
			return fmt.Errorf("unable to validate leaf %s: %w", leaf.ID, err)
		}
	}
	return nil
}

func leafAvailableStatus(leaf *ent.TreeNode) error {
	if leaf.Status != st.TreeNodeStatusAvailable {
		return sparkerrors.FailedPreconditionLeafUnavailable(fmt.Errorf("leaf %v is not available to transfer, status: %s", leaf.ID, leaf.Status))
	}
	return nil
}

func (h *BaseTransferHandler) LeafAvailableToTransfer(ctx context.Context, leaf *ent.TreeNode, transfer *ent.Transfer) error {
	if err := leafAvailableStatus(leaf); err != nil {
		return err
	}
	// SP-2784: update for multi-sender
	senderPubkey, err := mimo.GetSingleTransferSender(transfer)
	if err != nil {
		return err
	}
	if !leaf.OwnerIdentityPubkey.Equals(senderPubkey) {
		return fmt.Errorf("leaf %v is not owned by sender", leaf.ID)
	}
	return nil
}

// createTransferSender creates a TransferSender row whose create_time and
// transfer_type are denormalized from the parent transfer, so (transfer,
// sender, receiver) share a single timestamp and type within the same SO.
func createTransferSender(
	ctx context.Context,
	db *ent.Client,
	transfer *ent.Transfer,
	identityPubKey keys.Public,
) (*ent.TransferSender, error) {
	return db.TransferSender.Create().
		SetTransferID(transfer.ID).
		SetIdentityPubkey(identityPubKey).
		SetCreateTime(transfer.CreateTime).
		SetTransferType(transfer.Type).
		Save(ctx)
}

// createTransferReceiver creates a TransferReceiver row whose create_time and
// transfer_type are denormalized from the parent transfer. See
// createTransferSender.
func createTransferReceiver(
	ctx context.Context,
	db *ent.Client,
	transfer *ent.Transfer,
	identityPubKey keys.Public,
	status st.TransferReceiverStatus,
) (*ent.TransferReceiver, error) {
	return db.TransferReceiver.Create().
		SetTransferID(transfer.ID).
		SetIdentityPubkey(identityPubKey).
		SetStatus(status).
		SetCreateTime(transfer.CreateTime).
		SetTransferType(transfer.Type).
		Save(ctx)
}

func createTransferLeaves(
	ctx context.Context,
	db *ent.Client,
	transfer *ent.Transfer,
	transferSender *ent.TransferSender,
	transferReceiver *ent.TransferReceiver,
	leaves []*ent.TreeNode,
	cpfpLeafRefundMap map[string][]byte,
	directLeafRefundMap map[string][]byte,
	directFromCpfpLeafRefundMap map[string][]byte,
	leafTweakMap map[string]validatedKeyTweak,
) error {
	mutators := make([]*ent.TransferLeafCreate, 0, len(leaves))
	for _, leaf := range leaves {
		rawRefundTx := cpfpLeafRefundMap[leaf.ID.String()]
		directRefundTx := directLeafRefundMap[leaf.ID.String()]
		intermediateDirectFromCpfpRefundTx := directFromCpfpLeafRefundMap[leaf.ID.String()]
		mutator := db.TransferLeaf.Create().
			SetTransfer(transfer).
			SetLeaf(leaf).
			SetTransferSender(transferSender).
			SetTransferReceiver(transferReceiver).
			SetPreviousRefundTx(leaf.RawRefundTx).
			SetPreviousDirectRefundTx(leaf.DirectRefundTx).
			SetPreviousDirectFromCpfpRefundTx(leaf.DirectFromCpfpRefundTx).
			SetIntermediateRefundTx(rawRefundTx).
			SetIntermediateDirectRefundTx(directRefundTx).
			SetIntermediateDirectFromCpfpRefundTx(intermediateDirectFromCpfpRefundTx)
		if leafTweakMap != nil {
			leafTweak, ok := leafTweakMap[leaf.ID.String()]
			if !ok {
				return fmt.Errorf("key tweak not found for leaf %s in transfer %s", leaf.ID, transfer.ID)
			}
			leafTweakBinary, err := proto.Marshal(leafTweak.Proto())
			if err != nil {
				return fmt.Errorf("unable to marshal leaf tweak: %w", err)
			}
			mutator = mutator.SetKeyTweak(leafTweakBinary)
		}
		mutators = append(mutators, mutator)
	}
	if len(mutators) > 0 {
		_, err := db.TransferLeaf.CreateBulk(mutators...).Save(ctx)
		if err != nil {
			if sqlgraph.IsUniqueConstraintError(err) {
				return sparkerrors.AlreadyExistsDuplicateOperation(fmt.Errorf("transfer leaf already exists: %w", err))
			}
			if sqlgraph.IsForeignKeyConstraintError(err) {
				return sparkerrors.NotFoundMissingEntity(fmt.Errorf("referenced entity not found: %w", err))
			}
			return fmt.Errorf("unable to create transfer leaf: %w", err)
		}
	}
	return nil
}

func setTotalTransferValue(ctx context.Context, db *ent.Client, transfer *ent.Transfer, leaves []*ent.TreeNode) error {
	totalAmount := getTotalTransferValue(leaves)
	_, err := db.Transfer.UpdateOne(transfer).SetTotalValue(totalAmount).Save(ctx)
	if err != nil {
		return fmt.Errorf("unable to update transfer total value: %w", err)
	}
	return nil
}

func getTotalTransferValue(leaves []*ent.TreeNode) uint64 {
	totalAmount := uint64(0)
	for _, leaf := range leaves {
		totalAmount += leaf.Value
	}
	return totalAmount
}

func lockLeaves(ctx context.Context, db *ent.Client, leaves []*ent.TreeNode) ([]*ent.TreeNode, error) {
	ids := make([]uuid.UUID, len(leaves))
	for i, leaf := range leaves {
		ids[i] = leaf.ID
	}

	err := db.TreeNode.Update().
		Where(treenode.IDIn(ids...)).
		SetStatus(st.TreeNodeStatusTransferLocked).
		Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to update leaf statuses: %w", err)
	}

	// SigningKeyshare is eager-loaded for the downstream signing-job
	// builders, which need each leaf's keyshare ID.
	updatedLeaves, err := db.TreeNode.Query().
		Where(treenode.IDIn(ids...)).
		WithSigningKeyshare().
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to fetch updated leaves: %w", err)
	}

	if len(updatedLeaves) != len(leaves) {
		return nil, fmt.Errorf("some leaves not found")
	}
	return updatedLeaves, nil
}

// If open this function in spark.proto, need to take TransferStatusSenderKeyTweakPending out from the allowed status list for TransferTypePreimageSwap.
func (h *BaseTransferHandler) CancelTransfer(ctx context.Context, req *pbspark.CancelTransferRequest) (*pbspark.CancelTransferResponse, error) {
	reqSenderIDPubKey, err := keys.ParsePublicKey(req.GetSenderIdentityPublicKey())
	if err != nil {
		return nil, fmt.Errorf("unable to parse sender identity public key: %w", err)
	}
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, reqSenderIDPubKey); err != nil {
		return nil, err
	}
	if err := authz.EnforceWalletNotKillSwitched(ctx, reqSenderIDPubKey); err != nil {
		return nil, err
	}

	transferID, err := uuid.Parse(req.GetTransferId())
	if err != nil {
		return nil, fmt.Errorf("unable to parse transfer ID: %w", err)
	}
	transfer, err := h.loadTransferNoUpdate(ctx, transferID)
	if err != nil {
		logger := logging.GetLoggerFromContext(ctx)
		logger.Sugar().Infof("Transfer %v not found", transferID)
		return &pbspark.CancelTransferResponse{}, nil
	}
	// SP-2784: update for multi-sender
	senderPubkey, err := mimo.GetSingleTransferSender(transfer)
	if err != nil {
		return nil, err
	}
	if !senderPubkey.Equals(reqSenderIDPubKey) {
		return nil, fmt.Errorf("only sender is eligible to cancel the transfer %s", transferID)
	}

	if transfer.Type == st.TransferTypePreimageSwap {
		if transfer.Status != st.TransferStatusSenderInitiated &&
			transfer.Status != st.TransferStatusSenderKeyTweakPending &&
			transfer.Status != st.TransferStatusReturned {
			return nil, fmt.Errorf("preimage swap transfer %v is expected to be at status TransferStatusSenderInitiated, TransferStatusSenderKeyTweakPending, or TransferStatusReturned but %s found", transfer.ID, transfer.Status)
		}
	} else {
		if transfer.Status != st.TransferStatusSenderInitiated &&
			transfer.Status != st.TransferStatusReturned {
			return nil, fmt.Errorf("transfer %v is expected to be at status TransferStatusSenderInitiated or TransferStatusReturned but %s found", transfer.ID, transfer.Status)
		}
	}

	// The expiry time is only checked for coordinator SO because the creation time of each SO could be different.
	if transfer.Status != st.TransferStatusSenderInitiated && transfer.ExpiryTime.After(time.Now()) {
		return nil, fmt.Errorf("transfer %s has not expired, expires at %s", transferID, transfer.ExpiryTime)
	}

	// Check to see if preimage has already been shared before cancelling
	// Only check external requests as there currently exists some internal
	// use case for cancelling transfers after preimage share, e.g. preimage
	// is incorrect

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}

	preimageRequest, err := db.PreimageRequest.Query().Where(preimagerequest.HasTransfersWith(enttransfer.ID(transfer.ID))).Only(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return nil, fmt.Errorf("encountered error when fetching preimage request for transfer id %s: %w", transferID, err)
	}
	if preimageRequest != nil && preimageRequest.Status == st.PreimageRequestStatusPreimageShared {
		return nil, sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("cannot cancel an invoice whose preimage has already been revealed"))
	}

	err = h.CreateCancelTransferGossipMessage(ctx, transferID)
	if err != nil {
		return nil, fmt.Errorf("unable to create and send gossip message: %w", err)
	}
	return &pbspark.CancelTransferResponse{}, nil
}

func (h *BaseTransferHandler) CreateCancelTransferGossipMessage(ctx context.Context, transferID uuid.UUID) error {
	selection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}
	participants, err := selection.OperatorIdentifierList(h.config)
	if err != nil {
		return fmt.Errorf("unable to get operator list: %w", err)
	}
	sendGossipHandler := NewSendGossipHandler(h.config)
	_, err = sendGossipHandler.CreateAndSendGossipMessage(ctx, &pbgossip.GossipMessage{
		Message: &pbgossip.GossipMessage_CancelTransfer{
			CancelTransfer: &pbgossip.GossipMessageCancelTransfer{
				TransferId: transferID.String(),
			},
		},
	}, participants)
	if err != nil {
		return fmt.Errorf("unable to create and send gossip message: %w", err)
	}
	return nil
}

func (h *BaseTransferHandler) CreateRollbackTransferGossipMessage(ctx context.Context, transferID uuid.UUID) error {
	selection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}
	participants, err := selection.OperatorIdentifierList(h.config)
	if err != nil {
		return fmt.Errorf("unable to get operator list: %w", err)
	}
	sendGossipHandler := NewSendGossipHandler(h.config)
	_, err = sendGossipHandler.CreateAndSendGossipMessage(ctx, &pbgossip.GossipMessage{
		Message: &pbgossip.GossipMessage_RollbackTransfer{
			RollbackTransfer: &pbgossip.GossipMessageRollbackTransfer{
				TransferId: transferID.String(),
			},
		},
	}, participants)
	if err != nil {
		return fmt.Errorf("unable to create and send gossip message: %w", err)
	}
	return nil
}

// syncReceiversToTerminalStatus updates all TransferReceivers for a transfer to
// match the expected status for the transfer's terminal state.
//
// For RETURNED/EXPIRED: receivers are set to CANCELLED.
// For COMPLETED: receivers are set to COMPLETED with the given completionTime.
//
// Receivers already in the expected status are skipped (idempotent).
func syncReceiversToTerminalStatus(ctx context.Context, transferID uuid.UUID, transferStatus st.TransferStatus, completionTime time.Time) error {
	var expectedStatus st.TransferReceiverStatus
	switch transferStatus {
	case st.TransferStatusReturned, st.TransferStatusExpired:
		expectedStatus = st.TransferReceiverStatusCancelled
	case st.TransferStatusCompleted:
		expectedStatus = st.TransferReceiverStatusCompleted
	default:
		return fmt.Errorf("syncReceiversToTerminalStatus called with non-terminal transfer status %s", transferStatus)
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to get db for receiver sync: %w", err)
	}

	receivers, err := db.TransferReceiver.Query().
		Where(
			enttransferreceiver.TransferID(transferID),
			enttransferreceiver.StatusNEQ(expectedStatus),
		).
		ForUpdate().
		All(ctx)
	if err != nil {
		return fmt.Errorf("failed to query receivers for transfer %s: %w", transferID, err)
	}

	for _, r := range receivers {
		update := r.Update().SetStatus(expectedStatus)
		switch expectedStatus {
		case st.TransferReceiverStatusCompleted:
			// Preserve an already-recorded completion time; only stamp one when absent.
			if r.CompletionTime.IsZero() {
				update = update.SetCompletionTime(completionTime)
			}
		case st.TransferReceiverStatusCancelled:
			// A cancelled receiver must not retain a completion time.
			update = update.ClearCompletionTime()
		default:
			// Non-terminal statuses carry no completion-time rule; this helper
			// is only invoked with terminal statuses (completed/cancelled).
		}
		if _, err := update.Save(ctx); err != nil {
			return fmt.Errorf("failed to update receiver %s to %s: %w", r.ID, expectedStatus, err)
		}
	}

	return nil
}

func (h *BaseTransferHandler) CancelTransferInternal(ctx context.Context, transferID uuid.UUID) error {
	transfer, err := h.loadTransferForUpdate(ctx, transferID)
	if err != nil {
		return fmt.Errorf("unable to load transfer: %w", err)
	}

	return h.executeCancelTransfer(ctx, transfer)
}

// isCancellableTransferStatus reports whether a transfer in the given status
// may still be cancelled. Only early sender-side states qualify; once the
// sender key tweak has been applied (or the transfer is terminal) it must not
// be cancelled.
func isCancellableTransferStatus(status st.TransferStatus) bool {
	switch status {
	case st.TransferStatusSenderInitiated,
		st.TransferStatusSenderKeyTweakPending,
		st.TransferStatusSenderInitiatedCoordinator:
		return true
	default:
		return false
	}
}

func (h *BaseTransferHandler) executeCancelTransfer(ctx context.Context, transfer *ent.Transfer) error {
	// Don't error if the transfer is already returned.
	logger := logging.GetLoggerFromContext(ctx)
	if transfer.Status == st.TransferStatusReturned {
		logger.Sugar().Infof("Transfer %s already returned", transfer.ID)
		return nil
	}
	// Prevent cancellation of transfers in terminal or advanced states
	if transfer.Status == st.TransferStatusCompleted ||
		transfer.Status == st.TransferStatusExpired {
		return fmt.Errorf("transfer %s is already in terminal state %s and cannot be cancelled", transfer.ID, transfer.Status)
	}
	if !isCancellableTransferStatus(transfer.Status) {
		return fmt.Errorf("transfer %s cannot be cancelled from status %s", transfer.ID, transfer.Status)
	}

	var err error
	transfer, err = transfer.Update().SetStatus(st.TransferStatusReturned).Save(ctx)
	if err != nil {
		return fmt.Errorf("unable to update transfer status: %w", err)
	}

	// Receivers can only advance past SenderInitiated once the transfer reaches
	// SenderKeyTweaked, which is blocked from cancellation above. So receivers
	// here should only be in SenderInitiated or already Cancelled.
	receivers, err := transfer.QueryTransferReceivers().All(ctx)
	if err != nil {
		return fmt.Errorf("unable to query transfer receivers: %w", err)
	}
	for _, r := range receivers {
		switch r.Status {
		case st.TransferReceiverStatusCancelled:
			// Already cancelled, nothing to do.
		case st.TransferReceiverStatusInitiated:
			if _, err := r.Update().SetStatus(st.TransferReceiverStatusCancelled).Save(ctx); err != nil {
				return fmt.Errorf("unable to update transfer receiver %s to cancelled: %w", r.ID, err)
			}
		default:
			return fmt.Errorf("transfer receiver %s in unexpected status %s during cancellation", r.ID, r.Status)
		}
	}

	err = h.cancelTransferUnlockLeaves(ctx, transfer)
	if err != nil {
		return fmt.Errorf("unable to unlock leaves in the transfer: %w", err)
	}

	err = h.cancelTransferCancelRequest(ctx, transfer)
	if err != nil {
		return fmt.Errorf("unable to cancel associated request: %w", err)
	}

	return nil
}

func (h *BaseTransferHandler) RollbackTransfer(ctx context.Context, transferID uuid.UUID) error {
	logger := logging.GetLoggerFromContext(ctx)

	transfer, err := h.loadTransferForUpdate(ctx, transferID)
	if err != nil {
		return fmt.Errorf("unable to load transfer %s: %w", transferID, err)
	}

	if transfer.Status == st.TransferStatusSenderInitiated {
		logger.Sugar().Infof("Transfer %s already in sender initiated state", transferID)
		return nil
	} else if transfer.Status != st.TransferStatusSenderKeyTweakPending && transfer.Status != st.TransferStatusSenderInitiatedCoordinator {
		return fmt.Errorf("expected transfer %s to be in sender key tweak pending state, instead got %s", transferID, transfer.Status)
	}

	// Get all transfer leaves
	transferLeaves, err := transfer.QueryTransferLeaves().All(ctx)
	if err != nil {
		return fmt.Errorf("unable to get leaves for transfer %s: %w", transferID, err)
	}

	// Clear key tweak on each transfer leaf
	for _, transferLeaf := range transferLeaves {
		_, err = transferLeaf.Update().
			ClearKeyTweak().
			ClearSenderKeyTweakProof().
			Save(ctx)
		if err != nil {
			return fmt.Errorf("unable to clear key tweak from transfer leaf %s: %w", transferLeaf.ID, err)
		}
	}

	// Update transfer status to sender initiated
	_, err = transfer.Update().SetStatus(st.TransferStatusSenderInitiated).Save(ctx)
	if err != nil {
		return fmt.Errorf("unable to update status for transfer %s: %w", transferID, err)
	}

	return nil
}

func (h *BaseTransferHandler) cancelTransferUnlockLeaves(ctx context.Context, transfer *ent.Transfer) error {
	logger := logging.GetLoggerFromContext(ctx)
	transferLeaves, err := transfer.QueryTransferLeaves().All(ctx)
	if err != nil {
		return fmt.Errorf("unable to get transfer leaves: %w", err)
	}

	for _, leaf := range transferLeaves {
		treeNode, err := leaf.QueryLeaf().ForUpdate().Only(ctx)
		if err != nil {
			return fmt.Errorf("unable to get tree node: %w", err)
		}
		// Cancel only owns the TRANSFER_LOCKED lock it placed when the transfer
		// was initiated. AVAILABLE means the unlock already happened (idempotent
		// retry). Any other status means another state machine has since claimed
		// the leaf (e.g. EXITED/ON_CHAIN after an on-chain spend — SP-3049 — or
		// safety/lock states like INVESTIGATION, LOST, FROZEN_BY_ISSUER,
		// RENEW_LOCKED, SPLIT_LOCKED, AGGREGATE_LOCK); reviving it to AVAILABLE
		// here would bypass that quarantine/lock, so leave it untouched.
		if treeNode.Status == st.TreeNodeStatusAvailable {
			continue
		}
		if treeNode.Status != st.TreeNodeStatusTransferLocked {
			logger.Sugar().Infof("Skipping unlock of tree node %s in non-transfer-locked status %s during cancel of transfer %s", treeNode.ID, treeNode.Status, transfer.ID)
			continue
		}
		_, err = treeNode.Update().SetStatus(st.TreeNodeStatusAvailable).Save(ctx)
		if err != nil {
			return fmt.Errorf("unable to update tree node status: %w", err)
		}
	}
	return nil
}

func (h *BaseTransferHandler) cancelTransferCancelRequest(ctx context.Context, transfer *ent.Transfer) error {
	if transfer.Type == st.TransferTypePreimageSwap {
		db, err := ent.GetDbFromContext(ctx)
		if err != nil {
			return err
		}

		preimageRequest, err := db.PreimageRequest.Query().Where(preimagerequest.HasTransfersWith(enttransfer.ID(transfer.ID))).Only(ctx)
		if err != nil || preimageRequest == nil {
			return fmt.Errorf("cannot find preimage request for transfer %s", transfer.ID)
		}
		// Clear the preimage_shares edge so a retry can re-link the share to a new
		// preimage_request. The share is unique per payment_hash and must be reusable
		// after this attempt is abandoned.
		err = preimageRequest.Update().SetStatus(st.PreimageRequestStatusReturned).ClearPreimageShares().Exec(ctx)
		if err != nil {
			return fmt.Errorf("unable to update preimage request status: %w", err)
		}
	}
	return nil
}

func (h *BaseTransferHandler) loadTransferForUpdate(ctx context.Context, transferID uuid.UUID, opts ...sql.LockOption) (*ent.Transfer, error) {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}

	transfer, err := db.Transfer.Query().
		Where(enttransfer.ID(transferID)).
		ForUpdate(opts...).
		WithTransferSenders().
		WithTransferReceivers().
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, sparkerrors.NotFoundMissingEntity(fmt.Errorf("unable to find transfer %s: %w", transferID, err))
		}
		return nil, fmt.Errorf("unable to find transfer %s: %w", transferID, err)
	}
	if transfer == nil {
		return nil, sparkerrors.NotFoundMissingEntity(fmt.Errorf("unable to find transfer %s", transferID))
	}
	return transfer, nil
}

func (h *BaseTransferHandler) loadTransferNoUpdate(ctx context.Context, transferID uuid.UUID) (*ent.Transfer, error) {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}

	transfer, err := db.Transfer.Query().
		Where(enttransfer.ID(transferID)).
		WithTransferSenders().
		WithTransferReceivers().
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, sparkerrors.NotFoundMissingEntity(fmt.Errorf("unable to find transfer %s: %w", transferID, err))
		}
		return nil, fmt.Errorf("unable to find transfer %s: %w", transferID, err)
	}
	if transfer == nil {
		return nil, sparkerrors.NotFoundMissingEntity(fmt.Errorf("unable to find transfer %s", transferID))
	}
	return transfer, nil
}

// Returns a non-nil receiver or an error. Resolving the row by the requester's
// own pubkey is what enforces claimer identity on the claim paths.
func (h *BaseTransferHandler) loadTransferReceiverByPublicKeyForUpdate(ctx context.Context, transfer *ent.Transfer, pubkey *keys.Public) (*ent.TransferReceiver, error) {
	if transfer == nil || pubkey == nil {
		return nil, sparkerrors.InternalObjectNull(fmt.Errorf("transfer and receiver identity public key are both required"))
	}

	receivers, err := transfer.QueryTransferReceivers().
		Where(enttransferreceiver.IdentityPubkeyEQ(*pubkey)).
		ForUpdate(sql.WithLockAction(sql.NoWait)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to query transfer receivers: %w", err)
	}

	switch len(receivers) {
	case 0:
		return nil, fmt.Errorf("no transfer receivers found for transfer %s matching identity public key %s", transfer.ID, pubkey)
	case 1:
		return receivers[0], nil
	default:
		return nil, fmt.Errorf("multiple transfer receivers found for transfer %s", transfer.ID)
	}
}

// isMimoReceiverStatusAuthoritative holds for >1 receiver: callers then leave the
// parent transfers.status at SENDER_KEY_TWEAKED until every receiver finishes.
func isMimoReceiverStatusAuthoritative(ctx context.Context, transfer *ent.Transfer) (bool, error) {
	count, err := transfer.QueryTransferReceivers().Count(ctx)
	if err != nil {
		return false, fmt.Errorf("unable to count transfer receivers for transfer %s: %w", transfer.ID, err)
	}
	return count > 1, nil
}

// loadSingleTransferReceiver loads the sole TransferReceiver for a transfer, if one exists.
// Returns an error if the transfer has multiple receivers (legacy endpoints do not support MIMO).
func (h *BaseTransferHandler) loadSingleTransferReceiverForUnsupportedMimoPath(ctx context.Context, transfer *ent.Transfer) (*ent.TransferReceiver, error) {
	receivers, err := transfer.QueryTransferReceivers().ForUpdate().All(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to query transfer receivers for transfer %s: %w", transfer.ID, err)
	}
	if len(receivers) > 1 {
		return nil, fmt.Errorf("transfer %s has multiple receivers; please upgrade to the latest SDK version and use ClaimTransfer", transfer.ID)
	}
	if len(receivers) == 1 {
		return receivers[0], nil
	}
	return nil, nil
}

func (h *BaseTransferHandler) validateAndConstructBitcoinTransactions(
	ctx context.Context,
	pkgLeafIDs *transferPackageLeafIDs,
	transferType st.TransferType,
	leaves []*ent.TreeNode,
	leafCpfpRefundMap map[string][]byte,
	leafDirectRefundMap map[string][]byte,
	leafDirectFromCpfpRefundMap map[string][]byte,
	refundDestPubkey keys.Public,
	connectorTx []byte,
) error {
	if len(leaves) == 0 {
		return fmt.Errorf("leaves cannot be empty")
	}

	nodesByID := leavesToMap(leaves)

	switch transferType {
	case st.TransferTypeTransfer:
		if pkgLeafIDs == nil {
			return validateLegacyLeavesToSend_transfer(ctx, nodesByID, leafCpfpRefundMap, leafDirectRefundMap, leafDirectFromCpfpRefundMap, refundDestPubkey)
		}
		return validateLeaves_transfer(ctx, pkgLeafIDs, nodesByID, leafCpfpRefundMap, leafDirectRefundMap, leafDirectFromCpfpRefundMap, refundDestPubkey)

	case st.TransferTypeSwap, st.TransferTypeCounterSwap, st.TransferTypePrimarySwapV3, st.TransferTypeCounterSwapV3:
		return validateLeaves_swap(ctx, nodesByID, leafCpfpRefundMap, leafDirectRefundMap, leafDirectFromCpfpRefundMap, refundDestPubkey, transferType)

	case st.TransferTypeCooperativeExit:
		if len(connectorTx) == 0 {
			return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("connector_tx is required for cooperative exit validation. Please upgrade to the latest SDK version"))
		}

		return validateTransactionCooperativeExitLeavesToSend(ctx, nodesByID, leafCpfpRefundMap, leafDirectRefundMap, leafDirectFromCpfpRefundMap, refundDestPubkey, connectorTx)

	default:
		return fmt.Errorf("invalid transfer type: %s", transferType)
	}
}

// wrapLeafValidationError adds context while preserving an existing structured
// classification, attaching the leaf id as metadata; errors without a
// classification keep the historical MALFORMED_FIELD class.
func wrapLeafValidationError(err error, msg string, leafID uuid.UUID) error {
	if sparkerrors.HasReason(err) {
		return sparkerrors.WrapErrorWithMessageAndMetadata(
			err, msg, map[string]string{sparkerrors.ErrorMetadataLeafID: leafID.String()})
	}
	return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("%s: %w", msg, err))
}

// refundTimelockAnchorTx, when non-empty, is the refund tx whose input-0
// sequence anchors the expected-timelock computation instead of
// node.RawRefundTx. The claim path passes the transfer leaf's immutable
// previous_refund_tx: claim Prepare overwrites node.RawRefundTx with the
// claimer's unsigned refund tx and a consensus rollback does not restore it,
// so anchoring on the node would demand a double-decremented timelock from
// the next claim attempt. Send-side callers pass nil (the node value is the
// correct baseline there).
func validateSingleLeafRefundTxs(
	ctx context.Context,
	node *ent.TreeNode,
	cpfpRefundTx []byte,
	directFromCpfpRefundTx []byte,
	directRefundTx []byte,
	refundDestPubkey keys.Public,
	transferType st.TransferType,
	refundTimelockAnchorTx []byte,
) error {
	if len(cpfpRefundTx) == 0 {
		return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("missing required CPFP refund tx for leaf"))
	}

	networkString := node.Network.String()

	timelockAnchorTx := refundTimelockAnchorTx
	if len(timelockAnchorTx) == 0 {
		timelockAnchorTx = node.RawRefundTx
	}
	cpfpRefundTxTimelock, err := bitcointransaction.GetCpfpTimelockFromRawRefundTx(timelockAnchorTx)
	if err != nil {
		return wrapLeafValidationError(err, "failed to get CPFP timelock anchor for leaf", node.ID)
	}

	if err := bitcointransaction.VerifyTransactionWithDatabaseTimelock(
		ctx,
		cpfpRefundTx,
		node,
		bitcointransaction.TxTypeRefundCPFP,
		refundDestPubkey,
		networkString,
		cpfpRefundTxTimelock,
	); err != nil {
		return wrapLeafValidationError(err, "CPFP refund tx validation failed for leaf", node.ID)
	}

	validateDirectRefunds := transferType == st.TransferTypeTransfer ||
		transferType == st.TransferTypeCooperativeExit ||
		transferType == st.TransferTypeSwap ||
		transferType == st.TransferTypeCounterSwap ||
		transferType == st.TransferTypePrimarySwapV3 ||
		transferType == st.TransferTypeCounterSwapV3
	requireDirectFromCpfpRefund := transferType == st.TransferTypeTransfer || transferType == st.TransferTypeCooperativeExit

	if validateDirectRefunds {
		if len(directFromCpfpRefundTx) == 0 {
			if requireDirectFromCpfpRefund {
				return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("missing required direct from CPFP refund tx for leaf"))
			}
		} else if err := bitcointransaction.VerifyTransactionWithDatabaseTimelock(
			ctx,
			directFromCpfpRefundTx,
			node,
			bitcointransaction.TxTypeRefundDirectFromCPFP,
			refundDestPubkey,
			networkString,
			cpfpRefundTxTimelock,
		); err != nil {
			return wrapLeafValidationError(err, "direct from CPFP refund tx validation failed for leaf", node.ID)
		}

		hasDirectRefundTx := len(directRefundTx) > 0
		hasDirectNodeTx := len(node.DirectTx) > 0
		isZeroNode, err := bitcointransaction.IsZeroNode(node)
		if err != nil {
			return sparkerrors.InternalDataInconsistency(fmt.Errorf("failed to determine if node is zero node: %w", err))
		}

		// If the node is not a zero node, enforce direct refund tx validation
		if hasDirectRefundTx {
			if isZeroNode {
				return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("leaf %s is a zero node, zero nodes must not have a direct refund tx", node.ID))
			}
			if err := bitcointransaction.VerifyTransactionWithDatabaseTimelock(
				ctx,
				directRefundTx,
				node,
				bitcointransaction.TxTypeRefundDirect,
				refundDestPubkey,
				networkString,
				cpfpRefundTxTimelock,
			); err != nil {
				return wrapLeafValidationError(err, "direct refund tx validation failed for leaf", node.ID)
			}
		} else if requireDirectFromCpfpRefund && hasDirectNodeTx && !isZeroNode {
			return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("leaf %s does not have a direct refund tx and it is not a zero node, non-zero nodes must have a direct refund tx", node.ID))
		}
	}

	return nil
}

func leavesToMap(leaves []*ent.TreeNode) map[string]*ent.TreeNode {
	nodesByID := make(map[string]*ent.TreeNode, len(leaves))
	for _, node := range leaves {
		nodesByID[node.ID.String()] = node
	}
	return nodesByID
}

// removeTxIn parse the raw bytes of transaction, remove the input at index vin
// and return raw bytes of modified transaction.
func removeTxIn(rawTx []byte, vin int) ([]byte, error) {

	if len(rawTx) == 0 {
		return nil, fmt.Errorf("raw transaction is empty")
	}

	parsedTx, err := common.TxFromRawTxBytes(rawTx)
	if err != nil {
		return nil, fmt.Errorf("failed to parse raw transaction: %w", err)
	}
	// Check for out-of-bounds vin
	if vin < 0 || vin > len(parsedTx.TxIn)-1 {
		return nil, fmt.Errorf("out of bounds vin %d for transaction with %d inputs", vin, len(parsedTx.TxIn))
	}

	// Copy Version, TxOut, and LockTime from the original transaction
	modifiedTx := wire.NewMsgTx(parsedTx.Version)
	modifiedTx.TxOut = parsedTx.Copy().TxOut
	modifiedTx.LockTime = parsedTx.LockTime

	// Copy all TxIn except TxIn[vin]
	oldTxIn := parsedTx.Copy().TxIn
	modifiedTxIn := make([]*wire.TxIn, 0, len(parsedTx.TxIn)-1)
	for i, TxIn := range oldTxIn {
		if i != vin {
			modifiedTxIn = append(modifiedTxIn, TxIn)
		}
	}
	modifiedTx.TxIn = modifiedTxIn

	// Serialize the modified transaction and return
	modifiedTxRaw, err := common.SerializeTx(modifiedTx)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize modified transaction: %w", err)
	}

	return modifiedTxRaw, nil
}

// parseAndValidateCoopExitTxid runs the cheap, request-only validation that
// must succeed before any coop-exit DB write, leaf lookup, or FROST signing.
// It (1) parses req.ExitTxid into a typed TxID and (2) verifies that
// connector_tx.TxIn[0].PreviousOutPoint.Hash binds to
// exit_txid -- the invariant that prevents a malicious SSP from pairing a
// structurally-valid connector_tx with an unrelated alibi exit_txid.
//
// Errors are returned with InvalidArgument codes so callers can rely on gRPC
// status mapping without re-wrapping.
func parseAndValidateCoopExitTxid(transferID string, exitTxidBytes []byte, connectorTxBytes []byte) (st.TxID, error) {
	if len(exitTxidBytes) != 32 {
		return st.TxID{}, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("exit_txid %x is not 32 bytes", exitTxidBytes))
	}
	exitTxid, err := st.NewTxIDFromBytes(exitTxidBytes)
	if err != nil {
		return st.TxID{}, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("failed to parse exit txid for transfer id %s exit txid %x: %w", transferID, exitTxidBytes, err))
	}
	if err := validateConnectorTxBindsToExitTxid(connectorTxBytes, exitTxid); err != nil {
		return st.TxID{}, fmt.Errorf("coop exit %s: %w", transferID, err)
	}
	return exitTxid, nil
}

// validateConnectorTxBindsToExitTxid ensures the connector_tx spends the same
// L1 exit transaction whose txid the SO will hand to the chain watcher. Without
// this binding, a malicious SSP can pair a structurally-valid connector_tx with
// an unrelated alibi exit_txid; the watcher confirms on the alibi tx, the Spark
// leaves rotate to the receiver, and the legitimate L1 exit is never broadcast.
//
// The chain watcher accepts ExitTxid in either internal (little-endian) or
// display (big-endian) byte order, so this validator mirrors that tolerance.
func validateConnectorTxBindsToExitTxid(connectorTxBytes []byte, exitTxid st.TxID) error {
	connectorTx, err := parseCanonicalConnectorTx(connectorTxBytes)
	if err != nil {
		return err
	}

	parent := connectorTx.TxIn[0].PreviousOutPoint.Hash
	expectedNormal := exitTxid.Hash()
	expectedReversed := expectedNormal
	slices.Reverse(expectedReversed[:])
	if parent != expectedNormal && parent != expectedReversed {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf(
			"connector_tx parent %s does not match exit_txid %s",
			parent,
			exitTxid,
		))
	}
	return nil
}

func parseCanonicalConnectorTx(connectorTxBytes []byte) (*wire.MsgTx, error) {
	if len(connectorTxBytes) == 0 {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("connector_tx is required for cooperative exit validation"))
	}
	connectorTx, err := common.TxFromRawTxBytes(connectorTxBytes)
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("failed to parse connector transaction: %w", err))
	}
	if err := validateConnectorTxCanonicalShape(connectorTx); err != nil {
		return nil, err
	}
	return connectorTx, nil
}

func validateConnectorTxCanonicalShape(connectorTx *wire.MsgTx) error {
	if connectorTx == nil {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("connector transaction is required"))
	}
	if connectorTx.Version != 3 {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("connector transaction must use version 3, got %d", connectorTx.Version))
	}
	if len(connectorTx.TxIn) != 1 {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("connector transaction must have exactly 1 input, got %d", len(connectorTx.TxIn)))
	}
	for inputIndex, txIn := range connectorTx.TxIn {
		if txIn == nil {
			return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("connector transaction input %d is required", inputIndex))
		}
		if len(txIn.SignatureScript) != 0 {
			return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("connector transaction input %d must not include signature script", inputIndex))
		}
		if len(txIn.Witness) != 0 {
			return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("connector transaction input %d must not include witness", inputIndex))
		}
	}
	return nil
}

func parseConnectorTxOutputs(connectorTx []byte) (map[wire.OutPoint]*wire.TxOut, error) {
	if len(connectorTx) == 0 {
		return nil, nil
	}

	tx, err := parseCanonicalConnectorTx(connectorTx)
	if err != nil {
		return nil, err
	}

	connectorTxHash := tx.TxHash()
	prevOuts := make(map[wire.OutPoint]*wire.TxOut, len(tx.TxOut))
	for i, txOut := range tx.TxOut {
		outpoint := wire.OutPoint{
			Hash:  connectorTxHash,
			Index: uint32(i),
		}
		prevOuts[outpoint] = txOut
	}

	return prevOuts, nil
}

func validateRefundInputCountForConnector(refundTx *wire.MsgTx, connectorPrevOuts map[wire.OutPoint]*wire.TxOut, refundType string) error {
	inputCount := len(refundTx.TxIn)
	if inputCount == 0 {
		return fmt.Errorf("%s refund tx must have at least 1 input", refundType)
	}

	if connectorPrevOuts != nil {
		if inputCount != 2 {
			return fmt.Errorf("%s refund tx must have exactly 2 inputs when connector tx is provided, got %d", refundType, inputCount)
		}
		return nil
	}

	if inputCount == 1 {
		return nil
	}
	if inputCount == 2 {
		return fmt.Errorf("%s refund tx has 2 inputs but no connector tx was provided", refundType)
	}
	return fmt.Errorf("%s refund tx has %d inputs; refund transactions support at most 2 inputs", refundType, inputCount)
}

func parseConnectorRefundTx(refundTxBytes []byte, connectorPrevOuts map[wire.OutPoint]*wire.TxOut, refundType string) (*wire.MsgTx, wire.OutPoint, error) {
	if len(refundTxBytes) == 0 {
		return nil, wire.OutPoint{}, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("%s refund transaction is empty", refundType))
	}

	refundTx, err := common.TxFromRawTxBytes(refundTxBytes)
	if err != nil {
		return nil, wire.OutPoint{}, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("failed to parse %s refund transaction: %w", refundType, err))
	}
	if err := validateRefundInputCountForConnector(refundTx, connectorPrevOuts, refundType); err != nil {
		return nil, wire.OutPoint{}, sparkerrors.InvalidArgumentMalformedField(err)
	}

	if connectorPrevOuts == nil {
		return refundTx, wire.OutPoint{}, nil
	}

	connectorOutpoint := refundTx.TxIn[1].PreviousOutPoint
	if _, exists := connectorPrevOuts[connectorOutpoint]; !exists {
		return nil, wire.OutPoint{}, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("%s refund tx input 1 does not reference a valid connector output: %v", refundType, connectorOutpoint))
	}
	return refundTx, connectorOutpoint, nil
}

func validateCoopExitConnectorLayout(
	connectorPrevOuts map[wire.OutPoint]*wire.TxOut,
	leafCpfpRefundMap map[string][]byte,
	leafDirectRefundMap map[string][]byte,
	leafDirectFromCpfpRefundMap map[string][]byte,
) error {
	if connectorPrevOuts == nil {
		return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("connector_tx is required for cooperative exit validation"))
	}

	expectedConnectorOutputs := len(leafCpfpRefundMap) + 1 // One connector output per leaf, plus one fee-bump output.
	if len(connectorPrevOuts) != expectedConnectorOutputs {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf(
			"connector transaction must have exactly one output per leaf plus one fee-bump output, got %d outputs for %d leaves",
			len(connectorPrevOuts),
			len(leafCpfpRefundMap),
		))
	}

	usedConnectorOutpoints := make(map[wire.OutPoint]string, len(leafCpfpRefundMap))
	for leafID, cpfpRefundTx := range leafCpfpRefundMap {
		_, cpfpConnectorOutpoint, err := parseConnectorRefundTx(cpfpRefundTx, connectorPrevOuts, "cpfp")
		if err != nil {
			return fmt.Errorf("leaf %s CPFP refund validation failed: %w", leafID, err)
		}
		if otherLeafID, exists := usedConnectorOutpoints[cpfpConnectorOutpoint]; exists {
			return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf(
				"connector output %s is used by multiple leaves: %s and %s",
				cpfpConnectorOutpoint,
				otherLeafID,
				leafID,
			))
		}
		if cpfpConnectorOutpoint.Index >= uint32(len(leafCpfpRefundMap)) {
			return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf(
				"leaf %s refund transactions spend connector output index %d, but only indexes 0 through %d are leaf connector outputs",
				leafID,
				cpfpConnectorOutpoint.Index,
				len(leafCpfpRefundMap)-1,
			))
		}
		usedConnectorOutpoints[cpfpConnectorOutpoint] = leafID

		directFromCpfpRefundTx, exists := leafDirectFromCpfpRefundMap[leafID]
		if !exists || len(directFromCpfpRefundTx) == 0 {
			return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("direct-from-CPFP refund tx is required for leaf %s", leafID))
		}
		_, directFromCpfpConnectorOutpoint, err := parseConnectorRefundTx(directFromCpfpRefundTx, connectorPrevOuts, "direct-from-cpfp")
		if err != nil {
			return fmt.Errorf("leaf %s direct-from-CPFP refund validation failed: %w", leafID, err)
		}
		if directFromCpfpConnectorOutpoint != cpfpConnectorOutpoint {
			return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf(
				"leaf %s refund transactions must spend the same connector output; CPFP spends %s, direct-from-CPFP spends %s",
				leafID,
				cpfpConnectorOutpoint,
				directFromCpfpConnectorOutpoint,
			))
		}

		directRefundTx := leafDirectRefundMap[leafID]
		if len(directRefundTx) == 0 {
			continue
		}
		_, directConnectorOutpoint, err := parseConnectorRefundTx(directRefundTx, connectorPrevOuts, "direct")
		if err != nil {
			return fmt.Errorf("leaf %s direct refund validation failed: %w", leafID, err)
		}
		if directConnectorOutpoint != cpfpConnectorOutpoint {
			return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf(
				"leaf %s refund transactions must spend the same connector output; CPFP spends %s, direct spends %s",
				leafID,
				cpfpConnectorOutpoint,
				directConnectorOutpoint,
			))
		}
	}
	return nil
}

func refundTypeName(txType bitcointransaction.TxType) string {
	switch txType {
	case bitcointransaction.TxTypeRefundCPFP:
		return "cpfp"
	case bitcointransaction.TxTypeRefundDirect:
		return "direct"
	case bitcointransaction.TxTypeRefundDirectFromCPFP:
		return "direct-from-cpfp"
	default:
		return "refund"
	}
}

func validateRefundTxWithConnector(
	ctx context.Context,
	refundTxBytes []byte,
	node *ent.TreeNode,
	connectorPrevOuts map[wire.OutPoint]*wire.TxOut,
	txType bitcointransaction.TxType,
	refundDestPubkey keys.Public,
	networkString string,
) error {
	refundTx, _, err := parseConnectorRefundTx(refundTxBytes, connectorPrevOuts, refundTypeName(txType))
	if err != nil {
		return err
	}

	// Build the node tx prevout for input 0
	var nodeRawTx []byte
	if txType == bitcointransaction.TxTypeRefundDirect {
		nodeRawTx = node.DirectTx
	} else {
		nodeRawTx = node.RawTx
	}
	nodeTx, err := common.TxFromRawTxBytes(nodeRawTx)
	if err != nil {
		return sparkerrors.InternalDataInconsistency(fmt.Errorf("failed to parse node transaction: %w", err))
	}
	nodeTxHash := nodeTx.TxHash()

	// Verify input 0 references the node tx
	nodeOutpoint := refundTx.TxIn[0].PreviousOutPoint
	if nodeOutpoint.Hash != nodeTxHash || nodeOutpoint.Index != 0 {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("refund tx input 0 does not reference the node tx"))
	}

	// Validate the transaction structure by stripping input 1 for structural validation
	modifiedTxBytes, err := removeTxIn(refundTxBytes, 1)
	if err != nil {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("failed to remove connector input for structural validation: %w", err))
	}

	if err := bitcointransaction.VerifyTransactionWithDatabase(
		ctx,
		modifiedTxBytes,
		node,
		txType,
		refundDestPubkey,
		networkString,
	); err != nil {
		return wrapLeafValidationError(err, "transaction structure validation failed", node.ID)
	}

	return nil
}

func validateLegacyLeavesToSend_transfer(
	ctx context.Context,
	nodesByID map[string]*ent.TreeNode,
	leafCpfpRefundMap map[string][]byte,
	leafDirectRefundMap map[string][]byte,
	leafDirectFromCpfpRefundMap map[string][]byte,
	refundDestPubkey keys.Public,
) error {
	for leafID := range leafCpfpRefundMap {
		node, exists := nodesByID[leafID]
		if !exists {
			return fmt.Errorf("leaf %s not found in loaded leaves", leafID)
		}

		cpfpRefundTx := leafCpfpRefundMap[leafID]
		directFromCpfpRefundTx := leafDirectFromCpfpRefundMap[leafID]
		directRefundTx := leafDirectRefundMap[leafID]

		if err := validateSingleLeafRefundTxs(
			ctx,
			node,
			cpfpRefundTx,
			directFromCpfpRefundTx,
			directRefundTx,
			refundDestPubkey,
			st.TransferTypeTransfer,
			nil,
		); err != nil {
			return fmt.Errorf("leaf %s validation for legacy transfer failed: %w", leafID, err)
		}
	}
	return nil
}

func validateLeaves_transfer(
	ctx context.Context,
	pkgLeafIDs *transferPackageLeafIDs,
	nodesByID map[string]*ent.TreeNode,
	leafCpfpRefundMap map[string][]byte,
	leafDirectRefundMap map[string][]byte,
	leafDirectFromCpfpRefundMap map[string][]byte,
	refundDestPubkey keys.Public,
) error {
	leavesToSendByID := make(map[string]struct{}, len(pkgLeafIDs.leavesToSend))
	for _, rawLeafID := range pkgLeafIDs.leavesToSend {
		parsed, err := uuid.Parse(rawLeafID)
		if err != nil {
			return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("unable to parse leaf_id %s: %w", rawLeafID, err))
		}
		leafID := parsed.String()
		if _, exists := leavesToSendByID[leafID]; exists {
			return sparkerrors.InvalidArgumentDuplicateField(fmt.Errorf("duplicate leaf id: %s", leafID))
		}
		leavesToSendByID[leafID] = struct{}{}
	}

	directLeavesByID := make(map[string]struct{}, len(pkgLeafIDs.directLeavesToSend))
	for _, rawLeafID := range pkgLeafIDs.directLeavesToSend {
		parsed, err := uuid.Parse(rawLeafID)
		if err != nil {
			return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("unable to parse leaf_id %s: %w", rawLeafID, err))
		}
		directLeafID := parsed.String()
		if _, ok := leavesToSendByID[directLeafID]; !ok {
			return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("found orphan leaf in DirectLeavesToSend with ID %s that does not correspond to any leaf in LeavesToSend", rawLeafID))
		}
		if _, exists := directLeavesByID[directLeafID]; exists {
			return sparkerrors.InvalidArgumentDuplicateField(fmt.Errorf("duplicate leaf id: %s", directLeafID))
		}
		directLeavesByID[directLeafID] = struct{}{}
	}

	if len(pkgLeafIDs.leavesToSend) != len(pkgLeafIDs.directFromCpfpLeaves) {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("mismatched number of leaves: LeavesToSend (%d) and DirectFromCpfpLeavesToSend (%d) must be equal", len(pkgLeafIDs.leavesToSend), len(pkgLeafIDs.directFromCpfpLeaves)))
	}

	directFromCpfpLeavesByID := make(map[string]struct{}, len(pkgLeafIDs.directFromCpfpLeaves))
	for _, rawLeafID := range pkgLeafIDs.directFromCpfpLeaves {
		parsed, err := uuid.Parse(rawLeafID)
		if err != nil {
			return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("unable to parse leaf_id %s: %w", rawLeafID, err))
		}
		directFromCpfpLeafID := parsed.String()
		if _, ok := leavesToSendByID[directFromCpfpLeafID]; !ok {
			return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("mismatched leaves: DirectFromCpfpLeavesToSend contains leaf ID %s which is not in LeavesToSend", rawLeafID))
		}
		if _, exists := directFromCpfpLeavesByID[directFromCpfpLeafID]; exists {
			return sparkerrors.InvalidArgumentDuplicateField(fmt.Errorf("duplicate leaf id: %s", directFromCpfpLeafID))
		}
		directFromCpfpLeavesByID[directFromCpfpLeafID] = struct{}{}
	}

	for leafID := range leafCpfpRefundMap {
		node, exists := nodesByID[leafID]
		if !exists {
			return fmt.Errorf("leaf %s not found in loaded leaves", leafID)
		}

		cpfpRefundTx := leafCpfpRefundMap[leafID]
		directFromCpfpRefundTx := leafDirectFromCpfpRefundMap[leafID]
		directRefundTx := leafDirectRefundMap[leafID]

		if err := validateSingleLeafRefundTxs(
			ctx,
			node,
			cpfpRefundTx,
			directFromCpfpRefundTx,
			directRefundTx,
			refundDestPubkey,
			st.TransferTypeTransfer,
			nil,
		); err != nil {
			return fmt.Errorf("leaf %s validation for transfer failed: %w", leafID, err)
		}
	}

	return nil
}

func validateLeaves_swap(
	ctx context.Context,
	nodesByID map[string]*ent.TreeNode,
	leafCpfpRefundMap map[string][]byte,
	leafDirectRefundMap map[string][]byte,
	leafDirectFromCpfpRefundMap map[string][]byte,
	refundDestPubkey keys.Public,
	transferType st.TransferType,
) error {
	for leafID := range leafCpfpRefundMap {
		node, exists := nodesByID[leafID]
		if !exists {
			return fmt.Errorf("leaf %s not found in loaded leaves", leafID)
		}

		if err := validateSingleLeafRefundTxs(
			ctx,
			node,
			leafCpfpRefundMap[leafID],
			leafDirectFromCpfpRefundMap[leafID],
			leafDirectRefundMap[leafID],
			refundDestPubkey,
			transferType,
			nil,
		); err != nil {
			return fmt.Errorf("leaf %s validation for %s failed: %w", leafID, transferType, err)
		}
	}

	return nil
}

func validateTransactionCooperativeExitLeavesToSend(
	ctx context.Context,
	nodesByID map[string]*ent.TreeNode,
	leafCpfpRefundMap map[string][]byte,
	leafDirectRefundMap map[string][]byte,
	leafDirectFromCpfpRefundMap map[string][]byte,
	refundDestPubkey keys.Public,
	connectorTx []byte,
) error {
	// Parse connector tx outputs if provided
	connectorPrevOuts, err := parseConnectorTxOutputs(connectorTx)
	if err != nil {
		return fmt.Errorf("failed to parse connector transaction: %w", err)
	}
	useMultiInputValidation := connectorPrevOuts != nil
	if useMultiInputValidation {
		if err := validateCoopExitConnectorLayout(connectorPrevOuts, leafCpfpRefundMap, leafDirectRefundMap, leafDirectFromCpfpRefundMap); err != nil {
			return err
		}
	}

	networkString := ""

	for leafID := range leafCpfpRefundMap {
		node, exists := nodesByID[leafID]
		if !exists {
			return fmt.Errorf("leaf %s not found in loaded leaves", leafID)
		}

		if networkString == "" {
			networkString = node.Network.String()
		}

		cpfpRefundTx := leafCpfpRefundMap[leafID]
		directFromCpfpRefundTx := leafDirectFromCpfpRefundMap[leafID]
		directRefundTx := leafDirectRefundMap[leafID]

		if useMultiInputValidation {
			// Use proper multi-input validation with connector prevouts
			if err := validateRefundTxWithConnector(
				ctx, cpfpRefundTx, node, connectorPrevOuts,
				bitcointransaction.TxTypeRefundCPFP, refundDestPubkey, networkString,
			); err != nil {
				return fmt.Errorf("leaf %s CPFP refund validation failed: %w", leafID, err)
			}

			if err := validateRefundTxWithConnector(
				ctx, directFromCpfpRefundTx, node, connectorPrevOuts,
				bitcointransaction.TxTypeRefundDirectFromCPFP, refundDestPubkey, networkString,
			); err != nil {
				return fmt.Errorf("leaf %s direct-from-CPFP refund validation failed: %w", leafID, err)
			}

			if len(directRefundTx) > 0 {
				if err := validateRefundTxWithConnector(
					ctx, directRefundTx, node, connectorPrevOuts,
					bitcointransaction.TxTypeRefundDirect, refundDestPubkey, networkString,
				); err != nil {
					return fmt.Errorf("leaf %s direct refund validation failed: %w", leafID, err)
				}
			}
		} else {
			// Legacy validation: remove input 1 and validate single-input
			// All refund tx in Coop Exit flow has 2 inputs: one from leaf's RawTx and
			// one from connector tx. SOs only verify 1st input and let SSP verifies 2nd input.
			modifiedCpfpRefundTx, err := removeTxIn(cpfpRefundTx, 1)
			if err != nil {
				return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("failed to remove second input from CPFP refund tx %x: %w", cpfpRefundTx, err))
			}

			modifiedDirectFromCpfpRefundTx, err := removeTxIn(directFromCpfpRefundTx, 1)
			if err != nil {
				return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("failed to remove second input from Direct-from-CPFP refund tx %x: %w", directFromCpfpRefundTx, err))
			}

			var modifiedDirectRefundTx []byte
			if len(directRefundTx) > 0 {
				modifiedDirectRefundTx, err = removeTxIn(directRefundTx, 1)
				if err != nil {
					return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("failed to remove second input from Direct refund tx %x: %w", directRefundTx, err))
				}
			}

			if err := validateSingleLeafRefundTxs(
				ctx,
				node,
				modifiedCpfpRefundTx,
				modifiedDirectFromCpfpRefundTx,
				modifiedDirectRefundTx,
				refundDestPubkey,
				st.TransferTypeTransfer,
				nil,
			); err != nil {
				return fmt.Errorf("leaf %s validation for legacy transfer failed: %w", leafID, err)
			}
		}
	}
	return nil
}

// verifySenderKeyTweakProofsMatch checks that the coordinator's plaintext proofs match
// the proofs each SO independently decrypted from the transfer package.
// Used before the transfer is persisted.
func verifySenderKeyTweakProofsMatch(keyTweakMap map[string]validatedKeyTweak, senderKeyTweakProofs map[string]*pbspark.SecretProof) error {
	if keyTweakMap == nil || senderKeyTweakProofs == nil {
		return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("key tweak map and sender key tweak proofs must not be nil"))
	}
	if len(keyTweakMap) != len(senderKeyTweakProofs) {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("sender key tweak proof count mismatch: expected %d, got %d", len(keyTweakMap), len(senderKeyTweakProofs)))
	}

	for leafID, leafTweak := range keyTweakMap {
		if leafTweak.Proto().GetSecretShareTweak() == nil {
			return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("secret share tweak missing for leaf %s", leafID))
		}
		proof, ok := senderKeyTweakProofs[leafID]
		if !ok {
			return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("sender key tweak proof missing for leaf %s", leafID))
		}
		if proof == nil {
			return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("sender key tweak proof value is nil for leaf %s", leafID))
		}
		if !slices.EqualFunc(proof.GetProofs(), leafTweak.Proto().GetSecretShareTweak().GetProofs(), bytes.Equal) {
			return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("sender key tweak proof mismatch for leaf %s", leafID))
		}
	}
	return nil
}

// validateKeyTweakProofs checks that the provided proofs match the proofs stored in
// the database on the transfer's leaves. Used after the transfer has already been persisted.
func (h *BaseTransferHandler) validateKeyTweakProofs(ctx context.Context, transfer *ent.Transfer, senderKeyTweakProofs map[string]*pbspark.SecretProof) error {
	transferLeaves, err := transfer.QueryTransferLeaves().All(ctx)
	if err != nil {
		return fmt.Errorf("unable to get transfer leaves: %w", err)
	}
	if len(senderKeyTweakProofs) != len(transferLeaves) {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("sender key tweak proof count mismatch: expected %d, got %d", len(transferLeaves), len(senderKeyTweakProofs)))
	}

	for _, leaf := range transferLeaves {
		keyTweakProto := &pbspark.SendLeafKeyTweak{}
		err := proto.Unmarshal(leaf.KeyTweak, keyTweakProto)
		if err != nil {
			return fmt.Errorf("unable to unmarshal key tweak: %w", err)
		}
		if keyTweakProto.GetSecretShareTweak() == nil {
			return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("secret share tweak missing for leaf %s", keyTweakProto.GetLeafId()))
		}

		keyTweakProof, ok := senderKeyTweakProofs[keyTweakProto.GetLeafId()]
		if !ok {
			return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("key tweak proof not found for leaf: %s", keyTweakProto.GetLeafId()))
		}
		if keyTweakProof == nil {
			return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("key tweak proof value is nil for leaf: %s", keyTweakProto.GetLeafId()))
		}

		if !slices.EqualFunc(keyTweakProof.GetProofs(), keyTweakProto.GetSecretShareTweak().GetProofs(), bytes.Equal) {
			return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("sender key tweak proof mismatch"))
		}
	}
	return nil
}

// validateTransferLeavesNotExitedToL1 rejects operations that assume the transfer
// is still fully off-chain when any transfer leaf has already been exited to L1.
func (h *BaseTransferHandler) validateTransferLeavesNotExitedToL1(ctx context.Context, transfer *ent.Transfer, operation string) error {
	transferLeafNodes, err := transfer.QueryTransferLeaves().QueryLeaf().All(ctx)
	if err != nil {
		return fmt.Errorf("unable to query transfer leaf nodes: %w", err)
	}
	for _, node := range transferLeafNodes {
		if node.Status.IsExitedToL1() {
			return sparkerrors.FailedPreconditionInvalidState(
				fmt.Errorf("cannot %s: leaf %s has been exited to L1 (status: %s)", operation, node.ID, node.Status))
		}
	}
	return nil
}

func (h *BaseTransferHandler) CommitSenderKeyTweaks(ctx context.Context, transferID uuid.UUID, senderKeyTweakProofs map[string]*pbspark.SecretProof) (*ent.Transfer, error) {
	transfer, err := h.loadTransferForUpdate(ctx, transferID)
	if err != nil {
		return nil, fmt.Errorf("unable to load transfer: %w", err)
	}
	err = h.validateKeyTweakProofs(ctx, transfer, senderKeyTweakProofs)
	if err != nil {
		logger := logging.GetLoggerFromContext(ctx)
		logger.With(zap.Error(err)).Sugar().Errorf("Unable to validate key tweak proofs for transfer %s", transferID)
		return nil, err
	}
	return h.commitSenderKeyTweaks(ctx, transfer)
}

func (h *BaseTransferHandler) commitSenderKeyTweaks(ctx context.Context, transfer *ent.Transfer) (*ent.Transfer, error) {
	return h.commitSenderKeyTweaksWithMode(ctx, transfer, false)
}

func (h *BaseTransferHandler) commitSenderKeyTweaksForAtomicSwap(ctx context.Context, transfer *ent.Transfer) (*ent.Transfer, error) {
	return h.commitSenderKeyTweaksWithMode(ctx, transfer, true)
}

func (h *BaseTransferHandler) commitSenderKeyTweaksWithMode(ctx context.Context, transfer *ent.Transfer, allowSwapV3 bool) (*ent.Transfer, error) {
	transfer, err := h.loadTransferForUpdate(ctx, transfer.ID)
	if err != nil {
		return nil, fmt.Errorf("unable to load transfer: %w", err)
	}

	// Reject the transfer if any leaf has been exited to L1. This is a unified
	// guard that protects all transfer types (preimage swaps, standard transfers,
	// etc.) against double-spend via concurrent unilateral exit.
	if err := h.validateTransferLeavesNotExitedToL1(ctx, transfer, "commit sender key tweaks"); err != nil {
		return nil, err
	}

	if err := h.validateSenderKeyTweakCommitPreconditions(ctx, transfer, allowSwapV3); err != nil {
		return nil, err
	}

	logger := logging.GetLoggerFromContext(ctx)
	logger.Sugar().Infof("Checking commitSenderKeyTweaks for transfer %s (status: %s)", transfer.ID, transfer.Status)
	if transfer.Status == st.TransferStatusSenderInitiated {
		return nil, fmt.Errorf("transfer %s does not have key tweaks to commit", transfer.ID)
	}
	if transfer.Status != st.TransferStatusSenderKeyTweakPending && transfer.Status != st.TransferStatusSenderInitiatedCoordinator && transfer.Status != st.TransferStatusApplyingSenderKeyTweak {
		return transfer, nil
	}
	transferLeaves, err := transfer.QueryTransferLeaves().
		WithLeaf(func(tnq *ent.TreeNodeQuery) {
			tnq.WithTree().WithSigningKeyshare()
		}).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get transfer leaves: %w", err)
	}
	logger.Sugar().Infof("Beginning to tweak keys for %d leaves of transfer %s", len(transferLeaves), transfer.ID)

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get db: %w", err)
	}

	if len(transferLeaves) > 0 {
		if err := h.applySenderKeyTweaks(ctx, db, transfer, transferLeaves); err != nil {
			return nil, err
		}
	}
	transfer, err = transfer.Update().SetStatus(st.TransferStatusSenderKeyTweaked).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to update transfer status: %w", err)
	}
	if err := transferpkg.MarkReceiversClaimPending(ctx, db, transfer.ID); err != nil {
		return nil, fmt.Errorf("unable to mark receivers claim pending for transfer %s: %w", transfer.ID, err)
	}

	return transfer, nil
}

// applySenderKeyTweaks applies every leaf's stored sender key tweak in one
// batched pass with O(1) database round trips. Per-leaf work here runs on the
// coordinator and again on every participant applying the settle gossip
// inline, so it scales the sender's synchronous gossip wait with leaf count.
//
// Every transferLeaves entry must carry the Leaf edge with Tree and
// SigningKeyshare loaded.
func (h *BaseTransferHandler) applySenderKeyTweaks(ctx context.Context, db *ent.Client, transfer *ent.Transfer, transferLeaves []*ent.TransferLeaf) error {
	nodeIDs := make([]uuid.UUID, 0, len(transferLeaves))
	for _, leaf := range transferLeaves {
		if leaf.Edges.Leaf == nil {
			return sparkerrors.InternalDatabaseMissingEdge(fmt.Errorf("tree node edge not loaded for transfer leaf %s", leaf.ID))
		}
		nodeIDs = append(nodeIDs, leaf.Edges.Leaf.ID)
	}

	// Pin the tree-node rows until the transaction commits so a concurrent
	// claim or exit can't mutate them mid-rotation; deterministic order to
	// avoid lock-order deadlocks between concurrent batch lockers.
	lockedNodes, err := db.TreeNode.Query().
		Where(treenode.IDIn(nodeIDs...)).
		Order(ent.Asc(treenode.FieldID)).
		ForUpdate().
		All(ctx)
	if err != nil {
		return fmt.Errorf("unable to lock tree nodes for transfer %s: %w", transfer.ID, err)
	}
	if len(lockedNodes) != len(nodeIDs) {
		return sparkerrors.InternalDataInconsistency(fmt.Errorf(
			"expected %d tree nodes for transfer %s but locked %d", len(nodeIDs), transfer.ID, len(lockedNodes)))
	}
	// Re-check the exit guard on the now-locked rows: the earlier
	// validateTransferLeavesNotExitedToL1 read was unlocked, so a chain
	// watcher marking a leaf exited to L1 could have committed in between.
	for _, node := range lockedNodes {
		if node.Status.IsExitedToL1() {
			return sparkerrors.FailedPreconditionInvalidState(fmt.Errorf(
				"cannot commit sender key tweaks: leaf %s has been exited to L1 (status: %s)", node.ID, node.Status))
		}
	}

	keysharesByID, err := lockAndHydrateLeafKeyshares(ctx, db, transferLeaves, transfer.ID)
	if err != nil {
		return err
	}

	type pendingSendLeaf struct {
		transferLeaf *ent.TransferLeaf
		treeNode     *ent.TreeNode
		keyTweak     *pbspark.SendLeafKeyTweak
		keyshareID   uuid.UUID
	}
	pending := make([]pendingSendLeaf, 0, len(transferLeaves))
	tweaks := make([]*ent.SigningKeyshareTweak, 0, len(transferLeaves))
	leafIDByKeyshareID := make(map[uuid.UUID]uuid.UUID, len(transferLeaves))
	for _, leaf := range transferLeaves {
		// Leaf and keyshare edges were validated non-nil by
		// lockAndHydrateLeafKeyshares over this same slice.
		treeNode := leaf.Edges.Leaf
		if len(leaf.KeyTweak) == 0 {
			return fmt.Errorf("transfer leaf has no key tweak stored for leaf %s in transfer %s", treeNode.ID, transfer.ID)
		}
		keyTweak := &pbspark.SendLeafKeyTweak{}
		if err := proto.Unmarshal(leaf.KeyTweak, keyTweak); err != nil {
			return fmt.Errorf("unable to unmarshal key tweak: %w", err)
		}
		keyshare, ok := keysharesByID[treeNode.Edges.SigningKeyshare.ID]
		if !ok {
			return sparkerrors.InternalDataInconsistency(fmt.Errorf(
				"signing keyshare %s for leaf %s not found during sender key tweak commit", treeNode.Edges.SigningKeyshare.ID, treeNode.ID))
		}
		parts, err := helper.ValidateSendLeafKeyTweak(h.config, keyTweak)
		if err != nil {
			return fmt.Errorf("unable to tweak leaf key: %w", err)
		}
		tweaks = append(tweaks, &ent.SigningKeyshareTweak{
			Keyshare:       keyshare,
			SecretTweak:    parts.SecretShare,
			PubKeyTweak:    parts.PubKeyTweak,
			PubSharesTweak: parts.PubKeySharesTweak,
		})
		pending = append(pending, pendingSendLeaf{
			transferLeaf: leaf,
			treeNode:     treeNode,
			keyTweak:     keyTweak,
			keyshareID:   keyshare.ID,
		})
		leafIDByKeyshareID[keyshare.ID] = treeNode.ID
	}

	rotatedKeyshares, err := rotateLeafKeyshares(ctx, transfer.ID, tweaks, leafIDByKeyshareID)
	if err != nil {
		return err
	}

	updates := make([]treeNodeOwnerKeyUpdate, 0, len(pending))
	leafBuilders := make([]*ent.TransferLeafCreate, 0, len(pending))
	for _, p := range pending {
		rotated, ok := rotatedKeyshares[p.keyshareID]
		if !ok {
			return sparkerrors.InternalDataInconsistency(fmt.Errorf(
				"rotated signing keyshare %s for leaf %s missing from batch result", p.keyshareID, p.treeNode.ID))
		}
		updates = append(updates, treeNodeOwnerKeyUpdate{
			node:               p.treeNode,
			ownerSigningPubkey: p.treeNode.VerifyingPubkey.Sub(rotated.PublicKey),
		})

		// Builders set ID (for conflict matching), all required fields, and
		// the fields being updated; every row exists, so the upsert always
		// takes the UPDATE branch.
		leafBuilder := db.TransferLeaf.Create().
			SetID(p.transferLeaf.ID).
			SetLeaf(p.treeNode).
			SetTransferID(transfer.ID).
			SetPreviousRefundTx(p.transferLeaf.PreviousRefundTx).
			SetIntermediateRefundTx(p.transferLeaf.IntermediateRefundTx).
			SetSecretCipher(p.keyTweak.GetSecretCipher())
		// A typed signature persists its scheme; a legacy one leaves the
		// scheme unset so the conflict update writes NULL over any stored
		// value. key_tweak is deliberately never set: the same mechanism
		// clears it.
		if typed := p.keyTweak.GetTypedSignature(); typed != nil {
			leafBuilder.SetSignature(typed.GetSignature()).SetSignatureScheme(int32(typed.GetScheme()))
		} else {
			leafBuilder.SetSignature(p.keyTweak.GetSignature())
		}
		leafBuilders = append(leafBuilders, leafBuilder)
	}

	if err := applyTreeNodeOwnerKeyUpdates(ctx, db, transfer.ID, updates, false); err != nil {
		return err
	}

	// CreateBulk with OnConflict is the bulk-UPDATE workaround (ent has no
	// native one). Chunk to respect PostgreSQL's 65535-parameter limit.
	const maxBatchSize = 1000
	for chunk := range slices.Chunk(leafBuilders, maxBatchSize) {
		if err := db.TransferLeaf.CreateBulk(chunk...).
			OnConflictColumns(enttransferleaf.FieldID).
			Update(func(u *ent.TransferLeafUpsert) {
				u.UpdateSignature()
				u.UpdateSignatureScheme()
				u.UpdateSecretCipher()
				u.UpdateKeyTweak()
				// Carried explicitly: custom upsert resolvers bypass ent's
				// UpdateDefault, and incremental consumers cursor on it.
				u.UpdateUpdateTime()
			}).
			Exec(ctx); err != nil {
			return fmt.Errorf("unable to batch update transfer leaf key tweaks for transfer %s: %w", transfer.ID, err)
		}
	}
	return nil
}

func (h *BaseTransferHandler) validateSenderKeyTweakCommitPreconditions(ctx context.Context, transfer *ent.Transfer, allowSwapV3 bool) error {
	switch transfer.Type {
	case st.TransferTypePrimarySwapV3, st.TransferTypeCounterSwapV3:
		if !allowSwapV3 {
			return sparkerrors.FailedPreconditionInvalidState(
				fmt.Errorf("swap v3 sender key tweaks must be committed atomically through CommitSwapKeyTweaks for transfer %s", transfer.ID),
			)
		}
		return nil
	case st.TransferTypePreimageSwap:
	default:
		return nil
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return sparkerrors.InternalDatabaseReadError(fmt.Errorf("unable to get db: %w", err))
	}
	preimageRequest, err := db.PreimageRequest.Query().
		Where(preimagerequest.HasTransfersWith(enttransfer.ID(transfer.ID))).
		Only(ctx)
	if ent.IsNotFound(err) {
		return sparkerrors.FailedPreconditionInvalidState(
			fmt.Errorf("cannot commit preimage swap sender key tweaks: preimage request not found for transfer %s", transfer.ID),
		)
	}
	if ent.IsNotSingular(err) {
		return sparkerrors.FailedPreconditionInvalidState(
			fmt.Errorf("cannot commit preimage swap sender key tweaks: multiple preimage requests found for transfer %s", transfer.ID),
		)
	}
	if err != nil {
		return sparkerrors.InternalDatabaseReadError(fmt.Errorf("unable to query preimage request for transfer %s: %w", transfer.ID, err))
	}
	if preimageRequest.Status != st.PreimageRequestStatusPreimageShared {
		return sparkerrors.FailedPreconditionInvalidState(
			fmt.Errorf("cannot commit preimage swap sender key tweaks: preimage has not been shared for transfer %s (status: %s)", transfer.ID, preimageRequest.Status),
		)
	}
	if len(preimageRequest.Preimage) != sha256.Size {
		return sparkerrors.FailedPreconditionInvalidState(
			fmt.Errorf("cannot commit preimage swap sender key tweaks: preimage request %s is marked shared but does not have a stored %d-byte preimage", preimageRequest.ID, sha256.Size),
		)
	}
	paymentHash := sha256.Sum256(preimageRequest.Preimage)
	if !bytes.Equal(paymentHash[:], preimageRequest.PaymentHash) {
		return sparkerrors.FailedPreconditionInvalidState(
			fmt.Errorf("cannot commit preimage swap sender key tweaks: preimage request %s is marked shared but stored preimage does not match payment hash", preimageRequest.ID),
		)
	}
	return nil
}

// CommitSwapKeyTweaks handles CommitSwapKeyTweaks gossip messages from the coordinator. It is used in
// Swap V3 to finalize the swap by tweaking the sender keys for both primary and
// counter transfers. The tweaks are applied in the same DB transaction, so
// either both of them succeed or both of them fail.
func (h *BaseTransferHandler) CommitSwapKeyTweaks(
	ctx context.Context,
	counterTransferID uuid.UUID,
) error {
	logger := logging.GetLoggerFromContext(ctx)
	counterTransfer, err := h.loadTransferForUpdate(ctx, counterTransferID)
	if err != nil {
		return fmt.Errorf("unable to load counter transfer: %w", err)
	}
	primaryTransfer, err := counterTransfer.QueryPrimarySwapTransfer().ForUpdate().Only(ctx)
	if err != nil {
		return fmt.Errorf("unable to load primary transfer: %w", err)
	}
	if counterTransfer.Type != st.TransferTypeCounterSwapV3 {
		return fmt.Errorf("counter transfer %s has invalid type %s", counterTransfer.ID, counterTransfer.Type)
	}
	if primaryTransfer.Type != st.TransferTypePrimarySwapV3 {
		return fmt.Errorf("primary transfer %s has invalid type %s", primaryTransfer.ID, primaryTransfer.Type)
	}
	// Sanity check. This should never happen because key tweaking is atomic.
	if primaryTransfer.Status == st.TransferStatusSenderKeyTweaked || counterTransfer.Status == st.TransferStatusSenderKeyTweaked {
		if primaryTransfer.Status != st.TransferStatusSenderKeyTweaked || counterTransfer.Status != st.TransferStatusSenderKeyTweaked {
			return fmt.Errorf("swap key tweaks must be committed atomically: primary transfer %s status %s, counter transfer %s status %s", primaryTransfer.ID, primaryTransfer.Status, counterTransfer.ID, counterTransfer.Status)
		}
		return nil
	}
	if !isSwapKeyTweakCommitStatus(primaryTransfer.Status) {
		return fmt.Errorf("primary transfer %s is not in a committable swap key tweak status: %s", primaryTransfer.ID, primaryTransfer.Status)
	}
	if !isSwapKeyTweakCommitStatus(counterTransfer.Status) {
		return fmt.Errorf("counter transfer %s is not in a committable swap key tweak status: %s", counterTransfer.ID, counterTransfer.Status)
	}

	logger.Sugar().Infof("Checking commitSwapKeyTweaks for primary transfer %s (status: %s) and counter transfer %s (status: %s)", primaryTransfer.ID, primaryTransfer.Status, counterTransfer.ID, counterTransfer.Status)

	for _, transfer := range []*ent.Transfer{primaryTransfer, counterTransfer} {
		if _, err := h.commitSenderKeyTweaksForAtomicSwap(ctx, transfer); err != nil {
			return fmt.Errorf("commitSenderKeyTweaks failed for transfer %s: %w", transfer.ID, err)
		}
	}
	logger.Sugar().Infof("Successfully tweaked keys for primary transfer %s (status: %s) and counter transfer %s (status: %s)", primaryTransfer.ID, primaryTransfer.Status, counterTransfer.ID, counterTransfer.Status)

	return nil
}

func isSwapKeyTweakCommitStatus(status st.TransferStatus) bool {
	return status == st.TransferStatusSenderKeyTweakPending ||
		status == st.TransferStatusSenderInitiatedCoordinator ||
		status == st.TransferStatusApplyingSenderKeyTweak
}

// applyLeafSignature records the sender's per-leaf authorization signature from
// the wire's mutually exclusive sig oneof. A typed signature stores its scheme
// alongside the bytes; legacy bare bytes clear any previously stored scheme
// (e.g. left behind by a rolled-back typed attempt, since RollbackTransfer does
// not touch this column) so bytes and scheme are always written together.
func applyLeafSignature(update *ent.TransferLeafUpdateOne, legacy []byte, typed *pbcommon.Signature) *ent.TransferLeafUpdateOne {
	if typed != nil {
		return update.SetSignature(typed.GetSignature()).SetSignatureScheme(int32(typed.GetScheme()))
	}
	return update.SetSignature(legacy).ClearSignatureScheme()
}
