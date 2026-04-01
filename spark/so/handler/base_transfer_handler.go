package handler

import (
	"bytes"
	"context"
	dbSql "database/sql"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"slices"
	"time"

	"entgo.io/ent/dialect/sql"
	"entgo.io/ent/dialect/sql/sqlgraph"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/uuids"
	enttransferleaf "github.com/lightsparkdev/spark/so/ent/transferleaf"
	"go.uber.org/zap"

	"github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	eciesgo "github.com/ecies/go/v2"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common"
	bitcointransaction "github.com/lightsparkdev/spark/common/bitcoin_transaction"
	"github.com/lightsparkdev/spark/common/logging"
	secretsharing "github.com/lightsparkdev/spark/common/secret_sharing"
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
	enttransferreceiver "github.com/lightsparkdev/spark/so/ent/transferreceiver"
	"github.com/lightsparkdev/spark/so/ent/treenode"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/helper"
	"github.com/lightsparkdev/spark/so/knobs"
	"google.golang.org/protobuf/proto"
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
	for _, leaf := range pkg.LeavesToSend {
		cpfp[leaf.LeafId] = leaf.RawTx
	}
	direct = make(map[string][]byte)
	for _, leaf := range pkg.DirectLeavesToSend {
		direct[leaf.LeafId] = leaf.RawTx
	}
	directFromCpfp = make(map[string][]byte)
	for _, leaf := range pkg.DirectFromCpfpLeavesToSend {
		directFromCpfp[leaf.LeafId] = leaf.RawTx
	}
	return cpfp, direct, directFromCpfp
}

// loadLeafRefundMaps extracts refund maps from a StartTransferRequest,
// delegating to loadLeafRefundMapsFromTransferPackage when a TransferPackage
// is present and falling back to the legacy LeavesToSend field otherwise.
func loadLeafRefundMaps(req *pbspark.StartTransferRequest) (cpfp, direct, directFromCpfp map[string][]byte) {
	if req.TransferPackage != nil {
		return loadLeafRefundMapsFromTransferPackage(req.TransferPackage)
	}
	cpfp = make(map[string][]byte)
	direct = make(map[string][]byte)
	directFromCpfp = make(map[string][]byte)
	for _, leaf := range req.LeavesToSend {
		cpfp[leaf.LeafId] = leaf.RefundTxSigningJob.RawTx
		if leaf.DirectRefundTxSigningJob != nil {
			direct[leaf.LeafId] = leaf.DirectRefundTxSigningJob.RawTx
		}
		if leaf.DirectFromCpfpRefundTxSigningJob != nil {
			directFromCpfp[leaf.LeafId] = leaf.DirectFromCpfpRefundTxSigningJob.RawTx
		}
	}
	return cpfp, direct, directFromCpfp
}

// loadInternalLeafRefundMaps extracts refund maps from an InitiateTransferRequest,
// delegating to loadLeafRefundMapsFromTransferPackage when a TransferPackage
// is present and falling back to the legacy Leaves field otherwise.
func loadInternalLeafRefundMaps(req *pbinternal.InitiateTransferRequest) (cpfp, direct, directFromCpfp map[string][]byte) {
	if req.TransferPackage != nil {
		return loadLeafRefundMapsFromTransferPackage(req.TransferPackage)
	}
	cpfp = make(map[string][]byte)
	direct = make(map[string][]byte)
	directFromCpfp = make(map[string][]byte)
	for _, leaf := range req.Leaves {
		cpfp[leaf.LeafId] = leaf.RawRefundTx
		direct[leaf.LeafId] = leaf.DirectRefundTx
		directFromCpfp[leaf.LeafId] = leaf.DirectFromCpfpRefundTx
	}
	return cpfp, direct, directFromCpfp
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
	if newTimeLock+spark.TimeLockInterval > oldTimeLock {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("time lock on the new refund tx %d must be less than the old one %d", newTimeLock, oldTimeLock))
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
	expectedReceiverDirectFromCpfpRefundOutPoint := senderRefundTx.TxIn[0].PreviousOutPoint

	cpfpTimelock := bitcointransaction.GetTimelockFromSequence(senderRefundTx.TxIn[0].Sequence)

	expectedReceiverDirectRefundTxSequence, err := bitcointransaction.ValidateSequence(cpfpTimelock, bitcointransaction.TxTypeRefundDirect, receiverDirectRefundTx.TxIn[0].Sequence)
	if err != nil {
		return fmt.Errorf("unable to validate direct refund tx inputs: %w", err)
	}
	if err := validateLeafRefundTxInputExact(receiverDirectRefundTx, expectedReceiverDirectRefundTxSequence, &expectedReceiverDirectRefundOutPoint, expectedInputCount); err != nil {
		return fmt.Errorf("unable to validate direct refund tx inputs: %w", err)
	}

	expectedReceiverDirectFromCpfpRefundTxSequence, err := bitcointransaction.ValidateSequence(cpfpTimelock, bitcointransaction.TxTypeRefundDirectFromCPFP, receiverDirectFromCpfpRefundTx.TxIn[0].Sequence)
	if err != nil {
		return fmt.Errorf("unable to validate direct from cpfp refund tx inputs: %w", err)
	}
	if err := validateLeafRefundTxInputExact(receiverDirectFromCpfpRefundTx, expectedReceiverDirectFromCpfpRefundTxSequence, &expectedReceiverDirectFromCpfpRefundOutPoint, expectedInputCount); err != nil {
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
		receivedDirectTxs := len(directRefundTx) > 0 && len(directFromCpfpRefundTx) > 0
		if receivedDirectTxs {
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
		return fmt.Errorf("unable to validate cpfp refund tx inputs: %w", err)
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
	leafTweakMap map[string]*pbspark.SendLeafKeyTweak,
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
			return nil, nil, fmt.Errorf("invalid expiry_time for primary swap transfer %s: less than safety buffer: %s", transferID, expiryTime.String())
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

	// For counter swap v3, we need to validate the primary transfer is in the right status and has enough time left.
	if transferType == st.TransferTypeCounterSwapV3 {
		primaryTransfer, err := db.Transfer.Query().
			Where(enttransfer.IDEQ(primaryTransferId)).
			WithTransferSenders().
			WithTransferReceivers().
			ForUpdate().
			Only(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("unable to find primary swap transfer id=%s", primaryTransferId.String())
		}
		// Check that the SO holds the correct refunds for the primary transfer.
		if primaryTransfer.Status != st.TransferStatusSenderKeyTweakPending && primaryTransfer.Status != st.TransferStatusSenderInitiatedCoordinator {
			return nil, nil, fmt.Errorf("primary swap transfer %s is not in the right status, got %s", primaryTransferId.String(), primaryTransfer.Status)
		}
		// Add safety buffer to prevent counter transfer creation too close to expiry time
		if primaryTransfer.ExpiryTime.Before(time.Now().Add(PrimaryTransferExpiryTimeSafetyBuffer)) {
			return nil, nil, fmt.Errorf("primary swap transfer %s has expired or is about to expire (within safety buffer of %v), expiry time is %s", primaryTransferId.String(), PrimaryTransferExpiryTimeSafetyBuffer, primaryTransfer.ExpiryTime.String())
		}
		transferCreate.SetPrimarySwapTransfer(primaryTransfer)
		// The counter transfer amount should be the same as the primary transfer amount until we implement fees. Then we should probably validate a statement from the user that they accepted the fees.
		counterTransferAmount := getTotalTransferValue(leaves)
		if primaryTransfer.TotalValue != counterTransferAmount {
			return nil, nil, fmt.Errorf("primary swap transfer %s amount %d does not match counter transfer amount %d", primaryTransferId.String(), primaryTransfer.TotalValue, counterTransferAmount)
		}
		// Validate that the parties in the Swap V3 counter transfer are the reverse of the primary transfer to ensure atomic swap correctness
		var primarySender, primaryReceiver keys.Public
		if knobs.GetKnobsService(ctx).GetValue(knobs.KnobReadMIMODataModelTransferSend, 0) > 0 {
			primarySender, primaryReceiver, err = GetTransferSenderReceiver(primaryTransfer)
			if err != nil {
				return nil, nil, err
			}
		} else {
			primarySender = primaryTransfer.SenderIdentityPubkey
			primaryReceiver = primaryTransfer.ReceiverIdentityPubkey
		}
		if !primarySender.Equals(receiverIdentityPubKey) {
			return nil, nil, fmt.Errorf("counter transfer receiver must be the primary transfer sender: expected %s, got %s", primarySender, receiverIdentityPubKey)
		}
		if !primaryReceiver.Equals(senderIdentityPubKey) {
			return nil, nil, fmt.Errorf("counter transfer sender must be the primary transfer receiver: expected %s, got %s", primaryReceiver, senderIdentityPubKey)
		}
	}

	if transferType == st.TransferTypeTransfer || transferType == st.TransferTypeSwap || transferType == st.TransferTypeCounterSwap || transferType == st.TransferTypePrimarySwapV3 || transferType == st.TransferTypeCounterSwapV3 || transferType == st.TransferTypeCooperativeExit {
		if err := h.validateAndConstructBitcoinTransactions(ctx, pkg, transferType, leaves, leafCpfpRefundMap, leafDirectRefundMap, leafDirectFromCpfpRefundMap, receiverIdentityPubKey, connectorTx); err != nil {
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

	transferSender, err := db.TransferSender.Create().
		SetTransferID(transfer.ID).
		SetIdentityPubkey(senderIdentityPubKey).
		Save(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to create transfer sender: %w", err)
	}
	transferReceiver, err := db.TransferReceiver.Create().
		SetTransferID(transfer.ID).
		SetIdentityPubkey(receiverIdentityPubKey).
		SetStatus(st.TransferReceiverStatusSenderInitiated).
		Save(ctx)
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

// createTransferV3 creates a transfer with one sender and multiple receivers.
// Each leaf is associated with a specific receiver via leafReceiverMap.
// Validation is done per-receiver group since refund outputs must pay to the correct receiver.
func (h *BaseTransferHandler) createTransferV3(
	ctx context.Context,
	transferID uuid.UUID,
	pkg *pbspark.TransferPackage,
	expiryTime time.Time,
	senderIdentityPubKey keys.Public,
	receivers []keys.Public,
	leafReceiverMap map[string]keys.Public,
	leafCpfpRefundMap map[string][]byte,
	leafDirectRefundMap map[string][]byte,
	leafDirectFromCpfpRefundMap map[string][]byte,
	leafTweakMap map[string]*pbspark.SendLeafKeyTweak,
	role TransferRole,
	requireDirectTx bool,
) (*ent.Transfer, map[string]*ent.TreeNode, error) {
	transferType := st.TransferTypeTransfer

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

	// Group leaves by receiver for per-receiver claiming.
	type receiverGroup struct {
		receiverPubKey keys.Public
		leaves         []*ent.TreeNode
		cpfpMap        map[string][]byte
		directMap      map[string][]byte
		directCpfpMap  map[string][]byte
	}
	groupsByReceiver := make(map[string]*receiverGroup)
	for _, leaf := range leaves {
		recvPK, ok := leafReceiverMap[leaf.ID.String()]
		if !ok {
			return nil, nil, fmt.Errorf("leaf %s not found in leaf-receiver map", leaf.ID)
		}
		recvKey := string(recvPK.Serialize())
		group, ok := groupsByReceiver[recvKey]
		if !ok {
			group = &receiverGroup{
				receiverPubKey: recvPK,
				cpfpMap:        make(map[string][]byte),
				directMap:      make(map[string][]byte),
				directCpfpMap:  make(map[string][]byte),
			}
			groupsByReceiver[recvKey] = group
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

	// Validate bitcoin transactions per-receiver group (refund outputs must pay to the correct receiver).
	for _, g := range groupsByReceiver {
		if err := h.validateAndConstructBitcoinTransactions(ctx, pkg, transferType, g.leaves, g.cpfpMap, g.directMap, g.directCpfpMap, g.receiverPubKey, nil); err != nil {
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

	transfer, err := transferCreate.Save(ctx)
	if err != nil {
		if sqlgraph.IsUniqueConstraintError(err) {
			return nil, nil, sparkerrors.AlreadyExistsDuplicateOperation(fmt.Errorf("transfer already exists: %w", err))
		}
		return nil, nil, fmt.Errorf("unable to create transfer: %w", err)
	}

	transferSender, err := db.TransferSender.Create().
		SetTransferID(transfer.ID).
		SetIdentityPubkey(senderIdentityPubKey).
		Save(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to create transfer sender: %w", err)
	}

	// Create one TransferReceiver per receiver, then create transfer leaves for each group.
	for _, g := range groupsByReceiver {
		transferReceiver, err := db.TransferReceiver.Create().
			SetTransferID(transfer.ID).
			SetIdentityPubkey(g.receiverPubKey).
			SetStatus(st.TransferReceiverStatusSenderInitiated).
			Save(ctx)
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
		t := decoded.ExpiryTime.AsTime()
		expiry = &t
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
	leafUUIDs, err := uuids.ParseSeq(maps.Keys(leafRefundMap))
	if err != nil {
		return nil, btcnetwork.Unspecified, fmt.Errorf("unable to parse leaf IDs: %w", err)
	}

	leaves, err := db.TreeNode.Query().
		Where(treenode.IDIn(leafUUIDs...)).
		WithTree().
		ForUpdate().
		All(ctx)
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
		// TODO(LIG-7719) reinstate direct tx validation once sync_transfer_refunds job has been added.
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

func (h *BaseTransferHandler) LeafAvailableToTransfer(ctx context.Context, leaf *ent.TreeNode, transfer *ent.Transfer) error {
	if leaf.Status != st.TreeNodeStatusAvailable {
		if leaf.Status == st.TreeNodeStatusTransferLocked {
			db, err := ent.GetDbFromContext(ctx)
			if err != nil {
				return fmt.Errorf("unable to get db from context: %w", err)
			}
			transferLeaves, err := db.TransferLeaf.Query().
				Where(enttransferleaf.HasLeafWith(treenode.IDEQ(leaf.ID))).
				WithTransfer().
				All(ctx)
			if err != nil {
				return fmt.Errorf("unable to find transfer leaf for leaf %v: %w", leaf.ID, err)
			}
			cancelledExpiredLock := false
			now := time.Now()
			for _, transferLeaf := range transferLeaves {
				expiredTransfer := transferLeaf.Edges.Transfer
				if expiredTransfer == nil {
					continue
				}
				if expiredTransfer.Status == st.TransferStatusSenderInitiated && expiredTransfer.ExpiryTime.Before(now) {
					err := h.CancelTransferInternal(ctx, expiredTransfer.ID)
					if err != nil {
						return fmt.Errorf("unable to cancel transfer: %w", err)
					}
					cancelledExpiredLock = true
				}
			}
			// If the expired transfer was successfully cancelled, the leaf is now unlocked
			// within the current transaction. Fall through to the ownership check so the
			// new transfer can proceed atomically in the same transaction. This avoids
			// rolling back the cancellation: running it in a separate transaction would
			// deadlock because the current transaction already holds a FOR UPDATE lock on
			// the leaf's TreeNode row (acquired in loadLeavesWithLock).
			if !cancelledExpiredLock {
				return sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("leaf %v is not available to transfer, status: %s", leaf.ID, leaf.Status))
			}
		} else {
			return sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("leaf %v is not available to transfer, status: %s", leaf.ID, leaf.Status))
		}
	}
	var senderPubkey keys.Public
	if knobs.GetKnobsService(ctx).GetValue(knobs.KnobReadMIMODataModelTransferSend, 0) > 0 {
		var err error
		senderPubkey, err = GetTransferSender(transfer)
		if err != nil {
			return err
		}
	} else {
		senderPubkey = transfer.SenderIdentityPubkey
	}
	if !leaf.OwnerIdentityPubkey.Equals(senderPubkey) {
		return fmt.Errorf("leaf %v is not owned by sender", leaf.ID)
	}
	return nil
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
	leafTweakMap map[string]*pbspark.SendLeafKeyTweak,
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
			SetIntermediateRefundTx(rawRefundTx).
			SetIntermediateDirectRefundTx(directRefundTx).
			SetIntermediateDirectFromCpfpRefundTx(intermediateDirectFromCpfpRefundTx)
		if leafTweakMap != nil {
			leafTweak, ok := leafTweakMap[leaf.ID.String()]
			if !ok {
				return fmt.Errorf("key tweak not found for leaf %s in transfer %s", leaf.ID, transfer.ID)
			}
			leafTweakBinary, err := proto.Marshal(leafTweak)
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

	updatedLeaves, err := db.TreeNode.Query().
		Where(treenode.IDIn(ids...)).
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
	reqSenderIDPubKey, err := keys.ParsePublicKey(req.SenderIdentityPublicKey)
	if err != nil {
		return nil, fmt.Errorf("unable to parse sender identity public key: %w", err)
	}
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, reqSenderIDPubKey); err != nil {
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
	var senderPubkey keys.Public
	if knobs.GetKnobsService(ctx).GetValue(knobs.KnobReadMIMODataModelTransferSend, 0) > 0 {
		var err error
		senderPubkey, err = GetTransferSender(transfer)
		if err != nil {
			return nil, err
		}
	} else {
		senderPubkey = transfer.SenderIdentityPubkey
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
		return nil, fmt.Errorf("transfer %s has not expired, expires at %s", transferID, transfer.ExpiryTime.String())
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
		if expectedStatus == st.TransferReceiverStatusCompleted {
			update = update.SetCompletionTime(completionTime)
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
	// Only allow cancellation from early states
	if transfer.Status != st.TransferStatusSenderInitiated &&
		transfer.Status != st.TransferStatusSenderKeyTweakPending &&
		transfer.Status != st.TransferStatusSenderInitiatedCoordinator {
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
		case st.TransferReceiverStatusSenderInitiated:
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
	transferLeaves, err := transfer.QueryTransferLeaves().All(ctx)
	if err != nil {
		return fmt.Errorf("unable to get transfer leaves: %w", err)
	}

	for _, leaf := range transferLeaves {
		treeNode, err := leaf.QueryLeaf().ForUpdate().Only(ctx)
		if err != nil {
			return fmt.Errorf("unable to get tree node: %w", err)
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
			return fmt.Errorf("cannot find preimage request for transfer %s", transfer.ID.String())
		}
		err = preimageRequest.Update().SetStatus(st.PreimageRequestStatusReturned).Exec(ctx)
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
	if err != nil || transfer == nil {
		return nil, fmt.Errorf("unable to find transfer %s: %w", transferID, err)
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
	if err != nil || transfer == nil {
		return nil, fmt.Errorf("unable to find transfer %s: %w", transferID, err)
	}
	return transfer, nil
}

// Fetch all TransferReceivers for this Transfer, returns the one associated with this request
// Returns whether MIMO receive is enabled, the receiver, and an error if one occurred
func (h *BaseTransferHandler) loadTransferReceiverByPublicKeyForUpdate(ctx context.Context, transfer *ent.Transfer, pubkey *keys.Public) (bool, *ent.TransferReceiver, error) {
	if transfer == nil || pubkey == nil {
		return false, nil, nil
	}

	isMimoReceiveEnabled := knobs.GetKnobsService(ctx).GetValue(knobs.KnobMimoTransferMultiReceiverEnabled, 0) > 0

	receivers, err := transfer.QueryTransferReceivers().
		Where(enttransferreceiver.IdentityPubkeyEQ(*pubkey)).
		ForUpdate(sql.WithLockAction(sql.NoWait)).
		All(ctx)
	if err != nil {
		return false, nil, fmt.Errorf("unable to query transfer receivers: %w", err)
	}

	// MIMO receive is enabled IFF the knob is enabled and there is a corresponding receiver.
	switch len(receivers) {
	case 0:
		if isMimoReceiveEnabled {
			return false, nil, fmt.Errorf("no transfer receivers found for transfer %s", transfer.ID)
		}
		return false, nil, nil
	case 1:
		return isMimoReceiveEnabled, receivers[0], nil
	default:
		return false, nil, fmt.Errorf("multiple transfer receivers found for transfer %s", transfer.ID)
	}
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

// ValidateTransferPackage validates the transfer package, to ensure the key tweaks are valid.
func (h *BaseTransferHandler) ValidateTransferPackage(
	ctx context.Context,
	transferID uuid.UUID,
	pkg *pbspark.TransferPackage,
	senderIdentityPubKey keys.Public,
	requireDirectFromCpfpLeaves bool,
) (map[string]*pbspark.SendLeafKeyTweak, error) {
	// If the transfer package is nil, we don't need to validate it.
	if pkg == nil {
		return nil, nil
	}

	if len(pkg.KeyTweakPackage) == 0 {
		return nil, fmt.Errorf("key tweak package is empty")
	}
	// Get the transfer limit from knobs if available
	// This allows runtime configuration of transfer limits without code changes
	// If KnobSoTransferLimit is set to 0, it uses the default MaxLeavesToSend constant
	transferLimit := MaxLeavesToSend // Default fallback
	knobService := knobs.GetKnobsService(ctx)
	if knobService != nil {
		knobLimit := knobService.GetValue(knobs.KnobSoTransferLimit, 0)
		if knobLimit > 0 {
			transferLimit = int(knobLimit)
		}
	}

	// Input size and count validation - prevent resource exhaustion
	if len(pkg.LeavesToSend) > transferLimit {
		return nil, fmt.Errorf("too many leaves to send: %d (max: %d)", len(pkg.LeavesToSend), transferLimit)
	}

	if len(pkg.DirectLeavesToSend) > transferLimit {
		return nil, fmt.Errorf("too many direct leaves to send: %d (max: %d)", len(pkg.DirectLeavesToSend), transferLimit)
	}

	if len(pkg.DirectFromCpfpLeavesToSend) > transferLimit {
		return nil, fmt.Errorf("too many direct from cpfp leaves to send: %d (max: %d)", len(pkg.DirectFromCpfpLeavesToSend), transferLimit)
	}

	// Validate key tweak package size
	totalSize := 0
	for _, ciphertext := range pkg.KeyTweakPackage {
		totalSize += len(ciphertext)
	}
	if totalSize > MaxKeyTweakPackageSize {
		return nil, fmt.Errorf("key tweak package too large: %d bytes (max: %d)", totalSize, MaxKeyTweakPackageSize)
	}

	// Validate leaf IDs and check for duplicates/orphans/mismatches across lists.
	leavesToSendIDs := make(map[string]struct{}, len(pkg.LeavesToSend))
	for _, leaf := range pkg.LeavesToSend {
		parsed, err := uuid.Parse(leaf.LeafId)
		if err != nil {
			return nil, fmt.Errorf("unable to parse leaf_id as a uuid %s: %w", leaf.LeafId, err)
		}
		leafID := parsed.String()
		if _, exists := leavesToSendIDs[leafID]; exists {
			return nil, fmt.Errorf("duplicate leaf id in LeavesToSend: %s", leafID)
		}
		leavesToSendIDs[leafID] = struct{}{}
	}

	directLeafIDs := make(map[string]struct{}, len(pkg.DirectLeavesToSend))
	for _, leaf := range pkg.DirectLeavesToSend {
		parsed, err := uuid.Parse(leaf.LeafId)
		if err != nil {
			return nil, fmt.Errorf("unable to parse direct_leaves_to_send leaf_id as a uuid %s: %w", leaf.LeafId, err)
		}
		leafID := parsed.String()
		if _, ok := leavesToSendIDs[leafID]; !ok {
			return nil, fmt.Errorf("orphan leaf in DirectLeavesToSend with ID %s not found in LeavesToSend", leaf.LeafId)
		}
		if _, exists := directLeafIDs[leafID]; exists {
			return nil, fmt.Errorf("duplicate leaf id in DirectLeavesToSend: %s", leafID)
		}
		directLeafIDs[leafID] = struct{}{}
	}

	if requireDirectFromCpfpLeaves {
		if len(pkg.LeavesToSend) != len(pkg.DirectFromCpfpLeavesToSend) {
			return nil, fmt.Errorf("mismatched number of leaves: LeavesToSend (%d) and DirectFromCpfpLeavesToSend (%d) must be equal", len(pkg.LeavesToSend), len(pkg.DirectFromCpfpLeavesToSend))
		}
	} else if len(pkg.DirectFromCpfpLeavesToSend) > 0 && len(pkg.LeavesToSend) != len(pkg.DirectFromCpfpLeavesToSend) {
		return nil, fmt.Errorf("mismatched number of leaves: LeavesToSend (%d) and DirectFromCpfpLeavesToSend (%d) must be equal", len(pkg.LeavesToSend), len(pkg.DirectFromCpfpLeavesToSend))
	}
	directFromCpfpLeafIDs := make(map[string]struct{}, len(pkg.DirectFromCpfpLeavesToSend))
	for _, leaf := range pkg.DirectFromCpfpLeavesToSend {
		parsed, err := uuid.Parse(leaf.LeafId)
		if err != nil {
			return nil, fmt.Errorf("unable to parse direct_from_cpfp_leaves_to_send leaf_id as a uuid %s: %w", leaf.LeafId, err)
		}
		leafID := parsed.String()
		if _, ok := leavesToSendIDs[leafID]; !ok {
			return nil, fmt.Errorf("mismatched leaves: DirectFromCpfpLeavesToSend contains leaf ID %s not in LeavesToSend", leaf.LeafId)
		}
		if _, exists := directFromCpfpLeafIDs[leafID]; exists {
			return nil, fmt.Errorf("duplicate leaf id in DirectFromCpfpLeavesToSend: %s", leafID)
		}
		directFromCpfpLeafIDs[leafID] = struct{}{}
	}

	// Signature validation - prevent replay/DoS
	if len(pkg.UserSignature) == 0 {
		return nil, fmt.Errorf("user signature cannot be empty")
	}

	if len(pkg.UserSignature) > MaxSignatureSize {
		return nil, fmt.Errorf("user signature too large: %d bytes (max: %d)", len(pkg.UserSignature), MaxSignatureSize)
	}

	// Decrypt the key tweaks
	leafTweaksCipherText := pkg.KeyTweakPackage[h.config.Identifier]
	if leafTweaksCipherText == nil {
		return nil, fmt.Errorf("no key tweaks found for SO %s", h.config.Identifier)
	}

	// Encrypted data validation - prevent decryption attacks
	if len(leafTweaksCipherText) == 0 {
		return nil, fmt.Errorf("encrypted key tweaks cannot be empty")
	}

	if len(leafTweaksCipherText) > MaxKeyTweakPackageSize {
		return nil, fmt.Errorf("encrypted key tweaks too large: %d bytes (max: %d)", len(leafTweaksCipherText), MaxKeyTweakPackageSize)
	}

	decryptionPrivateKey := eciesgo.NewPrivateKeyFromBytes(h.config.IdentityPrivateKey.Serialize())
	leafTweaksBinary, err := eciesgo.Decrypt(decryptionPrivateKey, leafTweaksCipherText)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt key tweaks: %w", err)
	}

	leafTweaks := &pbspark.SendLeafKeyTweaks{}
	err = proto.Unmarshal(leafTweaksBinary, leafTweaks)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal key tweaks: %w", err)
	}

	// Memory usage validation - prevent OOM
	totalLeafCount := len(leafTweaks.LeavesToSend)
	if totalLeafCount > transferLimit {
		return nil, fmt.Errorf("too many leaves in key tweaks: %d (max: %d)", totalLeafCount, transferLimit)
	}

	// This should equal the number of SOs
	maxPubkeySharesTweakCount := len(h.config.GetSigningOperatorList())
	maxProofsCount := int(h.config.Threshold)

	// Estimate memory usage for the map
	estimatedMemory := totalLeafCount * (MaxLeafIdLength + MaxSecretShareSize + maxProofsCount*33 + maxPubkeySharesTweakCount*33)
	if estimatedMemory > MaxEstimatedMemoryUsage {
		return nil, fmt.Errorf("estimated memory usage too high: %d bytes (max: %d)", estimatedMemory, MaxEstimatedMemoryUsage)
	}

	leafTweaksMap := make(map[string]*pbspark.SendLeafKeyTweak)
	for _, leafTweak := range leafTweaks.LeavesToSend {
		// Validate leaf ID in key tweaks
		if err := uuid.Validate(leafTweak.LeafId); err != nil {
			return nil, fmt.Errorf("unable to parse key tweaks leaf_id as a uuid %s: %w", leafTweak.LeafId, err)
		}

		// Validate secret share size
		if len(leafTweak.SecretShareTweak.SecretShare) > MaxSecretShareSize {
			return nil, fmt.Errorf("secret share too large: %d bytes (max: %d)", len(leafTweak.SecretShareTweak.SecretShare), MaxSecretShareSize)
		}

		// Validate proofs count
		if len(leafTweak.SecretShareTweak.Proofs) > maxProofsCount {
			return nil, fmt.Errorf("too many proofs: %d (max: %d)", len(leafTweak.SecretShareTweak.Proofs), maxProofsCount)
		}

		// Validate pubkey shares count
		if len(leafTweak.PubkeySharesTweak) > maxPubkeySharesTweakCount {
			return nil, fmt.Errorf("too many pubkey shares: %d (max: %d)", len(leafTweak.PubkeySharesTweak), maxPubkeySharesTweakCount)
		}

		leafTweaksMap[leafTweak.LeafId] = leafTweak
	}

	// The refund transactions and key tweak package must cover exactly the same set of leaves.
	if len(leafTweaksMap) != len(leavesToSendIDs) {
		return nil, fmt.Errorf("key tweak count mismatch in transfer %s: refund transactions have %d leaves, key tweak package has %d entries",
			transferID, len(leavesToSendIDs), len(leafTweaksMap))
	}
	for leafID := range leavesToSendIDs {
		if _, ok := leafTweaksMap[leafID]; !ok {
			return nil, fmt.Errorf("key tweak missing for leaf %s in transfer %s",
				leafID, transferID)
		}
	}

	payloadToVerify := common.GetTransferPackageSigningPayload(transferID, pkg)

	if err := common.VerifyECDSASignature(senderIdentityPubKey, pkg.UserSignature, payloadToVerify); err != nil {
		return nil, fmt.Errorf("unable to verify user signature: %w", err)
	}

	for _, leafTweak := range leafTweaksMap {
		err := secretsharing.ValidateShare(
			&secretsharing.VerifiableSecretShare{
				SecretShare: secretsharing.SecretShare{
					FieldModulus: secp256k1.S256().N,
					Threshold:    int(h.config.Threshold),
					Index:        big.NewInt(int64(h.config.Index + 1)),
					Share:        new(big.Int).SetBytes(leafTweak.SecretShareTweak.SecretShare),
				},
				Proofs: leafTweak.SecretShareTweak.Proofs,
			},
		)
		if err != nil {
			return nil, fmt.Errorf("unable to validate share: %w", err)
		}
		for _, pubkeyTweak := range leafTweak.PubkeySharesTweak {
			if _, err := keys.ParsePublicKey(pubkeyTweak); err != nil {
				return nil, fmt.Errorf("encountered error when parsing pubkey tweak: %w", err)
			}
		}
	}

	return leafTweaksMap, nil
}

func (h *BaseTransferHandler) validateAndConstructBitcoinTransactions(
	ctx context.Context,
	pkg *pbspark.TransferPackage,
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
	networkString := leaves[0].Network.String()
	enhancedValidationEnabled := knobs.GetKnobsService(ctx).GetValueTarget(knobs.KnobSoEnhancedBitcoinTxValidation, &networkString, 0) > 0
	if !enhancedValidationEnabled {
		return nil
	}

	nodesByID := leavesToMap(leaves)

	switch transferType {
	case st.TransferTypeTransfer:
		if pkg == nil {
			return validateLegacyLeavesToSend_transfer(ctx, nodesByID, leafCpfpRefundMap, leafDirectRefundMap, leafDirectFromCpfpRefundMap, refundDestPubkey)
		}
		return validateLeaves_transfer(ctx, pkg, nodesByID, leafCpfpRefundMap, leafDirectRefundMap, leafDirectFromCpfpRefundMap, refundDestPubkey)

	case st.TransferTypeSwap, st.TransferTypeCounterSwap, st.TransferTypePrimarySwapV3, st.TransferTypeCounterSwapV3:
		return validateLeaves_swap(ctx, nodesByID, leafCpfpRefundMap, refundDestPubkey, transferType)

	case st.TransferTypeCooperativeExit:
		if len(connectorTx) == 0 {
			requireConnectorTx := knobs.GetKnobsService(ctx).GetValueTarget(
				knobs.KnobRequireConnectorTxValidation, &networkString, 0) > 0
			if requireConnectorTx {
				return fmt.Errorf("connector_tx is required for cooperative exit validation. Please upgrade to the latest SDK version")
			}
		}

		return validateTransactionCooperativeExitLeavesToSend(ctx, nodesByID, leafCpfpRefundMap, leafDirectRefundMap, leafDirectFromCpfpRefundMap, refundDestPubkey, connectorTx)

	default:
		return fmt.Errorf("invalid transfer type: %s", transferType)
	}
}

func validateSingleLeafRefundTxs(
	ctx context.Context,
	node *ent.TreeNode,
	cpfpRefundTx []byte,
	directFromCpfpRefundTx []byte,
	directRefundTx []byte,
	refundDestPubkey keys.Public,
	transferType st.TransferType,
) error {
	if len(cpfpRefundTx) == 0 {
		return fmt.Errorf("missing required CPFP refund tx for leaf")
	}

	networkString := node.Network.String()

	if err := bitcointransaction.VerifyTransactionWithDatabase(
		ctx,
		cpfpRefundTx,
		node,
		bitcointransaction.TxTypeRefundCPFP,
		refundDestPubkey,
		networkString,
	); err != nil {
		return fmt.Errorf("CPFP refund tx validation failed for leaf: %w", err)
	}

	if transferType == st.TransferTypeTransfer || transferType == st.TransferTypeCooperativeExit {
		if len(directFromCpfpRefundTx) == 0 {
			return fmt.Errorf("missing required direct from CPFP refund tx for leaf")
		}

		if err := bitcointransaction.VerifyTransactionWithDatabase(
			ctx,
			directFromCpfpRefundTx,
			node,
			bitcointransaction.TxTypeRefundDirectFromCPFP,
			refundDestPubkey,
			networkString,
		); err != nil {
			return fmt.Errorf("direct from CPFP refund tx validation failed for leaf: %w", err)
		}

		hasDirectRefundTx := len(directRefundTx) > 0
		hasDirectNodeTx := len(node.DirectTx) > 0
		isZeroNode, err := bitcointransaction.IsZeroNode(node)
		if err != nil {
			return fmt.Errorf("failed to determine if node is zero node: %w", err)
		}

		// If the knob is enabled and the node is not a zero node, enforce direct refund tx validation
		enforceDirectRefundTxValidation := knobs.GetKnobsService(ctx).RolloutRandomTarget(knobs.KnobDirectRefundTxValidation, &networkString, 0)

		if hasDirectRefundTx {
			if isZeroNode && enforceDirectRefundTxValidation {
				return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("leaf %s is a zero node, zero nodes must not have a direct refund tx", node.ID.String()))
			}
			if err := bitcointransaction.VerifyTransactionWithDatabase(
				ctx,
				directRefundTx,
				node,
				bitcointransaction.TxTypeRefundDirect,
				refundDestPubkey,
				networkString,
			); err != nil {
				return fmt.Errorf("direct refund tx validation failed for leaf: %w", err)
			}
		} else if !hasDirectRefundTx && hasDirectNodeTx && !isZeroNode && enforceDirectRefundTxValidation {
			return fmt.Errorf("leaf %s does not have a direct refund tx and it is not a zero node, non-zero nodes must have a direct refund tx", node.ID.String())
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

func parseConnectorTxOutputs(connectorTx []byte) (map[wire.OutPoint]*wire.TxOut, error) {
	if len(connectorTx) == 0 {
		return nil, nil
	}

	tx, err := common.TxFromRawTxBytes(connectorTx)
	if err != nil {
		return nil, fmt.Errorf("failed to parse connector transaction: %w", err)
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

func validateRefundTxWithConnector(
	ctx context.Context,
	refundTxBytes []byte,
	node *ent.TreeNode,
	connectorPrevOuts map[wire.OutPoint]*wire.TxOut,
	txType bitcointransaction.TxType,
	refundDestPubkey keys.Public,
	networkString string,
) error {
	if len(refundTxBytes) == 0 {
		return fmt.Errorf("refund transaction is empty")
	}

	refundTx, err := common.TxFromRawTxBytes(refundTxBytes)
	if err != nil {
		return fmt.Errorf("failed to parse refund transaction: %w", err)
	}

	// Verify transaction has exactly 2 inputs
	if len(refundTx.TxIn) != 2 {
		return fmt.Errorf("expected 2 inputs in refund tx, got %d", len(refundTx.TxIn))
	}

	// Verify input 1 references a connector output
	connectorOutpoint := refundTx.TxIn[1].PreviousOutPoint
	_, exists := connectorPrevOuts[connectorOutpoint]
	if !exists {
		return fmt.Errorf("refund tx input 1 does not reference a valid connector output: %v", connectorOutpoint)
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
		return fmt.Errorf("failed to parse node transaction: %w", err)
	}
	nodeTxHash := nodeTx.TxHash()

	// Verify input 0 references the node tx
	nodeOutpoint := refundTx.TxIn[0].PreviousOutPoint
	if nodeOutpoint.Hash != nodeTxHash || nodeOutpoint.Index != 0 {
		return fmt.Errorf("refund tx input 0 does not reference the node tx")
	}

	// Validate the transaction structure by stripping input 1 for structural validation
	modifiedTxBytes, err := removeTxIn(refundTxBytes, 1)
	if err != nil {
		return fmt.Errorf("failed to remove connector input for structural validation: %w", err)
	}

	if err := bitcointransaction.VerifyTransactionWithDatabase(
		ctx,
		modifiedTxBytes,
		node,
		txType,
		refundDestPubkey,
		networkString,
	); err != nil {
		return fmt.Errorf("transaction structure validation failed: %w", err)
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
		); err != nil {
			return fmt.Errorf("leaf %s validation for legacy transfer failed: %w", leafID, err)
		}
	}
	return nil
}

func validateLeaves_transfer(
	ctx context.Context,
	pkg *pbspark.TransferPackage,
	nodesByID map[string]*ent.TreeNode,
	leafCpfpRefundMap map[string][]byte,
	leafDirectRefundMap map[string][]byte,
	leafDirectFromCpfpRefundMap map[string][]byte,
	refundDestPubkey keys.Public,
) error {
	leavesToSendByID := make(map[string]*pbspark.UserSignedTxSigningJob, len(pkg.LeavesToSend))
	for _, leaf := range pkg.LeavesToSend {
		parsed, err := uuid.Parse(leaf.LeafId)
		if err != nil {
			return fmt.Errorf("unable to parse leaf_id %s: %w", leaf.LeafId, err)
		}
		leafID := parsed.String()
		if _, exists := leavesToSendByID[leafID]; exists {
			return fmt.Errorf("duplicate leaf id: %s", leafID)
		}
		leavesToSendByID[leafID] = leaf
	}

	directLeavesByID := make(map[string]*pbspark.UserSignedTxSigningJob, len(pkg.DirectLeavesToSend))
	for _, leaf := range pkg.DirectLeavesToSend {
		parsed, err := uuid.Parse(leaf.LeafId)
		if err != nil {
			return fmt.Errorf("unable to parse leaf_id %s: %w", leaf.LeafId, err)
		}
		directLeafID := parsed.String()
		if _, ok := leavesToSendByID[directLeafID]; !ok {
			return fmt.Errorf("found orphan leaf in DirectLeavesToSend with ID %s that does not correspond to any leaf in LeavesToSend", leaf.LeafId)
		}
		if _, exists := directLeavesByID[directLeafID]; exists {
			return fmt.Errorf("duplicate leaf id: %s", directLeafID)
		}
		directLeavesByID[directLeafID] = leaf
	}

	if len(pkg.LeavesToSend) != len(pkg.DirectFromCpfpLeavesToSend) {
		return fmt.Errorf("mismatched number of leaves: LeavesToSend (%d) and DirectFromCpfpLeavesToSend (%d) must be equal", len(pkg.LeavesToSend), len(pkg.DirectFromCpfpLeavesToSend))
	}

	directFromCpfpLeavesByID := make(map[string]*pbspark.UserSignedTxSigningJob, len(pkg.DirectFromCpfpLeavesToSend))
	for _, leaf := range pkg.DirectFromCpfpLeavesToSend {
		parsed, err := uuid.Parse(leaf.LeafId)
		if err != nil {
			return fmt.Errorf("unable to parse leaf_id %s: %w", leaf.LeafId, err)
		}
		directFromCpfpLeafID := parsed.String()
		if _, ok := leavesToSendByID[directFromCpfpLeafID]; !ok {
			return fmt.Errorf("mismatched leaves: DirectFromCpfpLeavesToSend contains leaf ID %s which is not in LeavesToSend", leaf.LeafId)
		}
		if _, exists := directFromCpfpLeavesByID[directFromCpfpLeafID]; exists {
			return fmt.Errorf("duplicate leaf id: %s", directFromCpfpLeafID)
		}
		directFromCpfpLeavesByID[directFromCpfpLeafID] = leaf
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
	refundDestPubkey keys.Public,
	transferType st.TransferType,
) error {
	for leafID := range leafCpfpRefundMap {
		node, exists := nodesByID[leafID]
		if !exists {
			return fmt.Errorf("leaf %s not found in loaded leaves", leafID)
		}

		cpfpRefundTx := leafCpfpRefundMap[leafID]

		if err := validateSingleLeafRefundTxs(
			ctx,
			node,
			cpfpRefundTx,
			nil,
			nil,
			refundDestPubkey,
			transferType,
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
				return fmt.Errorf("failed to remove second input from CPFP refund tx %x: %w", cpfpRefundTx, err)
			}

			modifiedDirectFromCpfpRefundTx, err := removeTxIn(directFromCpfpRefundTx, 1)
			if err != nil {
				return fmt.Errorf("failed to remove second input from Direct-from-CPFP refund tx %x: %w", directFromCpfpRefundTx, err)
			}

			var modifiedDirectRefundTx []byte
			if len(directRefundTx) > 0 {
				modifiedDirectRefundTx, err = removeTxIn(directRefundTx, 1)
				if err != nil {
					return fmt.Errorf("failed to remove second input from Direct refund tx %x: %w", directRefundTx, err)
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
func verifySenderKeyTweakProofsMatch(keyTweakMap map[string]*pbspark.SendLeafKeyTweak, senderKeyTweakProofs map[string]*pbspark.SecretProof) error {
	if keyTweakMap == nil || senderKeyTweakProofs == nil {
		return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("key tweak map and sender key tweak proofs must not be nil"))
	}
	if len(keyTweakMap) != len(senderKeyTweakProofs) {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("sender key tweak proof count mismatch: expected %d, got %d", len(keyTweakMap), len(senderKeyTweakProofs)))
	}

	for leafID, leafTweak := range keyTweakMap {
		if leafTweak.SecretShareTweak == nil {
			return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("secret share tweak missing for leaf %s", leafID))
		}
		proof, ok := senderKeyTweakProofs[leafID]
		if !ok {
			return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("sender key tweak proof missing for leaf %s", leafID))
		}
		if proof == nil {
			return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("sender key tweak proof value is nil for leaf %s", leafID))
		}
		if !slices.EqualFunc(proof.Proofs, leafTweak.SecretShareTweak.Proofs, bytes.Equal) {
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

	for _, leaf := range transferLeaves {
		keyTweakProto := &pbspark.SendLeafKeyTweak{}
		err := proto.Unmarshal(leaf.KeyTweak, keyTweakProto)
		if err != nil {
			return fmt.Errorf("unable to unmarshal key tweak: %w", err)
		}

		keyTweakProof, ok := senderKeyTweakProofs[keyTweakProto.LeafId]
		if !ok {
			return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("key tweak proof not found for leaf: %s", keyTweakProto.LeafId))
		}

		if !slices.EqualFunc(keyTweakProof.Proofs, keyTweakProto.SecretShareTweak.Proofs, bytes.Equal) {
			return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("sender key tweak proof mismatch"))
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
	transfer, err := h.loadTransferForUpdate(ctx, transfer.ID)
	if err != nil {
		return nil, fmt.Errorf("unable to load transfer: %w", err)
	}
	logger := logging.GetLoggerFromContext(ctx)
	logger.Sugar().Infof("Checking commitSenderKeyTweaks for transfer %s (status: %s)", transfer.ID, transfer.Status)
	if transfer.Status == st.TransferStatusSenderInitiated {
		return nil, fmt.Errorf("transfer %s does not have key tweaks to commit", transfer.ID)
	}
	if transfer.Status != st.TransferStatusSenderKeyTweakPending && transfer.Status != st.TransferStatusSenderInitiatedCoordinator && transfer.Status != st.TransferStatusApplyingSenderKeyTweak {
		return transfer, nil
	}
	transferLeaves, err := transfer.QueryTransferLeaves().All(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get transfer leaves: %w", err)
	}
	logger.Sugar().Infof("Beginning to tweak keys for transfer %s", transfer.ID)
	for _, leaf := range transferLeaves {
		if len(leaf.KeyTweak) == 0 {
			treeNode, _ := leaf.QueryLeaf().Only(ctx)
			leafID := leaf.ID.String()
			if treeNode != nil {
				leafID = treeNode.ID.String()
			}
			return nil, fmt.Errorf("transfer leaf has no key tweak stored for leaf %s in transfer %s", leafID, transfer.ID)
		}
		keyTweak := &pbspark.SendLeafKeyTweak{}
		err := proto.Unmarshal(leaf.KeyTweak, keyTweak)
		if err != nil {
			return nil, fmt.Errorf("unable to unmarshal key tweak: %w", err)
		}
		treeNode, err := leaf.QueryLeaf().ForUpdate().Only(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to get tree node: %w", err)
		}
		logger.Sugar().Infof("Tweaking leaf %s for transfer %s", treeNode.ID, transfer.ID)
		treeNodeUpdate, err := helper.TweakLeafKeyUpdate(ctx, treeNode, keyTweak)
		if err != nil {
			return nil, fmt.Errorf("unable to tweak leaf key: %w", err)
		}
		err = treeNodeUpdate.Exec(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to update tree node: %w", err)
		}
		_, err = leaf.Update().
			SetKeyTweak(nil).
			SetSecretCipher(keyTweak.SecretCipher).
			SetSignature(keyTweak.Signature).
			Save(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to update leaf key tweak: %w", err)
		}
	}
	transfer, err = transfer.Update().SetStatus(st.TransferStatusSenderKeyTweaked).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to update transfer status: %w", err)
	}

	return transfer, nil
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
	// Sanity check. This should never happen because key tweaking is atomic.
	if primaryTransfer.Status == st.TransferStatusSenderKeyTweaked {
		if counterTransfer.Status != st.TransferStatusSenderKeyTweaked {
			return fmt.Errorf("primary transfer %s is in sender key tweaked status, but the counter transfer %s is not", primaryTransfer.ID.String(), counterTransfer.ID.String())
		}
		return nil
	}

	logger.Sugar().Infof("Checking commitSwapKeyTweaks for primary transfer %s (status: %s) and counter transfer %s (status: %s)", primaryTransfer.ID, primaryTransfer.Status, counterTransfer.ID, counterTransfer.Status)

	for _, transfer := range []*ent.Transfer{primaryTransfer, counterTransfer} {
		if _, err := h.commitSenderKeyTweaks(ctx, transfer); err != nil {
			return fmt.Errorf("commitSenderKeyTweaks failed for transfer %s: %w", transfer.ID, err)
		}
	}
	logger.Sugar().Infof("Successfully tweaked keys for primary transfer %s (status: %s) and counter transfer %s (status: %s)", primaryTransfer.ID, primaryTransfer.Status, counterTransfer.ID, counterTransfer.Status)

	return nil
}
