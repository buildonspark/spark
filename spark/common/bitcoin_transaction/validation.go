package bitcointransaction

import (
	"fmt"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/ent"
)

const (
	defaultVersion = 3
)

// RefundTxType represents the type of refund transaction expected
type RefundTxType int

const (
	RefundTxTypeCPFP RefundTxType = iota
	RefundTxTypeDirect
	RefundTxTypeDirectFromCPFP
)

// VerifyTransactionWithDatabase validates a Bitcoin transaction by reconstructing it
func VerifyTransactionWithDatabase(clientRawTxBytes []byte, dbLeaf *ent.TreeNode, txType RefundTxType, refundDestPubkey keys.Public) error {
	clientTx, err := common.TxFromRawTxBytes(clientRawTxBytes)
	if err != nil {
		return fmt.Errorf("failed to parse client tx for leaf %s: %w", dbLeaf.ID, err)
	}

	clientSequence, err := GetAndValidateUserSequence(clientRawTxBytes)
	if err != nil {
		return fmt.Errorf("failed to validate user sequence: %w", err)
	}

	// Construct the expected transaction based on the type
	expectedTx, err := constructExpectedTransaction(dbLeaf, txType, refundDestPubkey, clientSequence)
	if err != nil {
		return fmt.Errorf("failed to construct expected transaction for leaf %s: %w", dbLeaf.ID, err)
	}

	err = common.CompareTransactions(expectedTx, clientTx)
	if err != nil {
		return fmt.Errorf("transaction does not match expected construction for leaf %s: %w", dbLeaf.ID, err)
	}

	return nil
}

// constructExpectedTransaction constructs the expected Bitcoin transaction based on leaf data from DB and transaction type
func constructExpectedTransaction(dbLeaf *ent.TreeNode, txType RefundTxType, refundDestPubkey keys.Public, clientSequence uint32) (*wire.MsgTx, error) {
	// Validate transaction type early
	if txType != RefundTxTypeCPFP && txType != RefundTxTypeDirect && txType != RefundTxTypeDirectFromCPFP {
		return nil, fmt.Errorf("unknown transaction type: %d", txType)
	}

	// Build the server-side sequence (validate timelock and construct sequence bits)
	serverSequence, err := validateSequence(dbLeaf, txType, clientSequence)
	if err != nil {
		return nil, fmt.Errorf("failed to validate client sequence: %w", err)
	}

	switch txType {
	case RefundTxTypeCPFP:
		return constructCPFPRefundTransaction(dbLeaf, refundDestPubkey, serverSequence)
	case RefundTxTypeDirect:
		return constructDirectRefundTransaction(dbLeaf, refundDestPubkey, serverSequence)
	case RefundTxTypeDirectFromCPFP:
		return constructDirectFromCPFPRefundTransaction(dbLeaf, refundDestPubkey, serverSequence)
	default:
		return nil, fmt.Errorf("unknown transaction type: %d", txType)
	}
}

// constructRefundTransactionGeneric creates a refund transaction with configurable parameters
// to avoid duplication across specific refund constructors.
func constructRefundTransactionGeneric(
	prevTxHash chainhash.Hash,
	sourceTxRaw []byte,
	refundDestPubkey keys.Public,
	clientSequence uint32,
	watchtowerTxs bool,
	parseTxName string,
) (*wire.MsgTx, error) {
	// Validate public key before attempting to use it
	if refundDestPubkey.IsZero() {
		return nil, fmt.Errorf("invalid public key is zero")
	}

	tx := wire.NewMsgTx(defaultVersion)

	// Add input spending the provided prevTxHash at index 0
	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{
			Hash:  prevTxHash,
			Index: uint32(0),
		},
		Sequence: clientSequence,
	})

	// Build refund output script
	userScript, err := common.P2TRScriptFromPubKey(refundDestPubkey)
	if err != nil {
		return nil, fmt.Errorf("failed to create user refund script: %w", err)
	}

	// Parse source transaction to determine available value
	parsedTx, err := common.TxFromRawTxBytes(sourceTxRaw)
	if err != nil {
		return nil, fmt.Errorf("failed to parse %s: %w", parseTxName, err)
	}

	sourceValue := parsedTx.TxOut[0].Value
	var refundAmount int64
	if watchtowerTxs {
		refundAmount = common.MaybeApplyFee(sourceValue)
	} else {
		refundAmount = sourceValue
	}

	tx.AddTxOut(&wire.TxOut{
		Value:    refundAmount,
		PkScript: userScript,
	})

	if !watchtowerTxs {
		tx.AddTxOut(common.EphemeralAnchorOutput())
	}

	return tx, nil
}

// constructCPFPRefundTransaction constructs a CPFP refund transaction
// Format: 1 input (spending the leaf UTXO), 2 outputs (refund to user + ephemeral anchor)
func constructCPFPRefundTransaction(dbLeaf *ent.TreeNode, refundDestPubkey keys.Public, clientSequence uint32) (*wire.MsgTx, error) {
	tx, err := constructRefundTransactionGeneric(
		dbLeaf.RawTxid.Hash(),
		dbLeaf.RawTx,
		refundDestPubkey,
		clientSequence,
		/*watchtowerTxs=*/ false,
		/*parseTxName=*/ "node tx",
	)
	if err != nil {
		return nil, fmt.Errorf("failed to construct CPFP refund transaction: %w", err)
	}
	return tx, nil
}

// constructDirectRefundTransaction constructs a direct refund transaction
// Format: 1 input (spending DirectTx), 1 output (refund to user)
func constructDirectRefundTransaction(dbLeaf *ent.TreeNode, refundDestPubkey keys.Public, clientSequence uint32) (*wire.MsgTx, error) {
	tx, err := constructRefundTransactionGeneric(
		dbLeaf.DirectTxid.Hash(),
		dbLeaf.DirectTx,
		refundDestPubkey,
		clientSequence,
		/*watchtowerTxs=*/ true,
		/*parseTxName=*/ "direct tx",
	)
	if err != nil {
		return nil, fmt.Errorf("failed to construct direct refund transaction: %w", err)
	}
	return tx, nil
}

// constructDirectFromCPFPRefundTransaction constructs a DirectFromCPFP refund transaction
// Format: 1 input (spending from NodeTx), 1 output (refund to user)
func constructDirectFromCPFPRefundTransaction(dbLeaf *ent.TreeNode, refundDestPubkey keys.Public, clientSequence uint32) (*wire.MsgTx, error) {
	tx, err := constructRefundTransactionGeneric(
		dbLeaf.RawTxid.Hash(),
		dbLeaf.RawTx,
		refundDestPubkey,
		clientSequence,
		/*watchtowerTxs=*/ true,
		/*parseTxName=*/ "node tx",
	)
	if err != nil {
		return nil, fmt.Errorf("failed to construct DirectFromCPFP refund transaction: %w", err)
	}
	return tx, nil
}

// validateSequence validates the client's sequence number against existing database transactions
func validateSequence(dbLeaf *ent.TreeNode, txType RefundTxType, clientSequence uint32) (uint32, error) {
	rawRefundTx, err := common.TxFromRawTxBytes(dbLeaf.RawRefundTx)
	if err != nil {
		return 0, fmt.Errorf("failed to parse CPFP refund transaction: %w", err)
	}

	if len(rawRefundTx.TxIn) == 0 {
		return 0, fmt.Errorf("CPFP refund transaction has no inputs")
	}

	// Extract the current timelock from the transaction (bits 0-15)
	cpfpRefundTxTimelock := GetTimelockFromSequence(rawRefundTx.TxIn[0].Sequence)

	// Validate that the timelock is large enough to subtract TimeLockInterval
	if cpfpRefundTxTimelock < spark.TimeLockInterval {
		return 0, fmt.Errorf("current timelock %d in CPFP refund transaction is too small to subtract TimeLockInterval %d",
			cpfpRefundTxTimelock, spark.TimeLockInterval)
	}

	// Calculate the expected new timelock (should be TimeLockInterval shorter)
	expectedCPFPRefundTxTimelock := cpfpRefundTxTimelock - spark.TimeLockInterval

	// Get the expected timelock based on transaction type
	var expectedTimelock uint32
	switch txType {
	case RefundTxTypeDirect, RefundTxTypeDirectFromCPFP:
		expectedTimelock = expectedCPFPRefundTxTimelock + spark.DirectTimelockOffset
	case RefundTxTypeCPFP:
		expectedTimelock = expectedCPFPRefundTxTimelock
	default:
		return 0, fmt.Errorf("unknown transaction type: %d", txType)
	}

	providedTimelock := GetTimelockFromSequence(clientSequence)
	if providedTimelock != expectedTimelock {
		return 0, fmt.Errorf("provided timelock 0x%08X does not match expected timelock 0x%08X", providedTimelock, expectedTimelock)
	}

	// Validate that the client's timelock (bits 0-15) matches expected
	err = ValidateSequenceTimelock(clientSequence, expectedTimelock)
	if err != nil {
		return 0, fmt.Errorf("failed to validate client sequence timelock for tx type %d: %w", txType, err)
	}

	return constructServerSequence(clientSequence, expectedTimelock), nil
}

func constructServerSequence(clientSequence uint32, expectedTimelock uint32) uint32 {
	upperBits := clientSequence & 0xFFFF0000
	maskClear := wire.SequenceLockTimeDisabled | wire.SequenceLockTimeIsSeconds
	sanitizedUpper := upperBits &^ uint32(maskClear)
	return sanitizedUpper | GetTimelockFromSequence(expectedTimelock)
}

func GetAndValidateUserSequence(rawTxBytes []byte) (uint32, error) {
	// Validate that bit 31 (disable flag) and bit 22 (type flag) are NOT set
	tx, err := common.TxFromRawTxBytes(rawTxBytes)
	if err != nil {
		return 0, err
	}

	if len(tx.TxIn) == 0 {
		return 0, fmt.Errorf("transaction has no inputs")
	}
	userSequence := tx.TxIn[0].Sequence

	if userSequence&wire.SequenceLockTimeDisabled != 0 {
		return 0, fmt.Errorf("client sequence has bit 31 set (timelock disabled)")
	}
	if userSequence&wire.SequenceLockTimeIsSeconds != 0 {
		return 0, fmt.Errorf("client sequence has bit 22 set (time-based timelock not supported)")
	}

	return userSequence, nil
}

func GetAndValidateUserTimelock(rawTxBytes []byte) (uint32, error) {
	sequence, err := GetAndValidateUserSequence(rawTxBytes)
	if err != nil {
		return 0, err
	}
	return GetTimelockFromSequence(sequence), nil
}

func ValidateSequenceTimelock(sequence uint32, expectedTimelock uint32) error {
	providedTimelock := GetTimelockFromSequence(sequence)
	if providedTimelock != expectedTimelock {
		return fmt.Errorf("provided timelock 0x%08X does not match expected timelock 0x%08X", providedTimelock, expectedTimelock)
	}
	return nil
}

// GetTimelockFromSequence extracts the timelock from a sequence
func GetTimelockFromSequence(sequence uint32) uint32 {
	return sequence & wire.SequenceLockTimeMask
}

// Decrement the timelock in the provided sequence by one step, preserving any other bits that are set.
// Use GetAndValidateUserSequence to get the valid currSequence for this function.
func NextSequence(currSequence uint32) (nextSequence uint32, nextDirectSequence uint32, err error) {
	currTimelock := GetTimelockFromSequence(currSequence)
	nextTimelock := int32(currTimelock) - spark.TimeLockInterval

	if nextTimelock < 0 {
		return 0, 0, fmt.Errorf("next timelock interval is less than 0, call renew node timelock")
	}

	// reset timelock
	currSequence = currSequence & 0xFFFF0000

	// Construct the new sequence
	nextSequence = uint32(nextTimelock) | currSequence
	nextDirectSequence = nextSequence + spark.DirectTimelockOffset

	return
}
