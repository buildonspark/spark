package bitcointransaction

import (
	"bytes"
	"fmt"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/google/go-cmp/cmp"
	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/ent"
)

// TxType represents the type of refund transaction expected
type TxType int

const (
	TxTypeRefundCPFP TxType = iota
	TxTypeRefundDirect
	TxTypeRefundDirectFromCPFP
	TxTypeNodeCPFP
	TxTypeNodeDirect
)

// VerifyTransactionWithDatabase validates a Bitcoin transaction by reconstructing it based on node in the database
func VerifyTransactionWithDatabase(clientRawTxBytes []byte, dbLeaf *ent.TreeNode, txType TxType, refundDestPubkey keys.Public) error {
	var sourceRawTxBytes []byte

	cpfpRefundTxTimelock, err := GetCpfpTimelockFromLeaf(dbLeaf)
	if err != nil {
		return fmt.Errorf("failed to get CPFP timelock from leaf: %w, tx type: %d", err, txType)
	}

	switch txType {
	// valid types
	case TxTypeRefundCPFP:
		sourceRawTxBytes = dbLeaf.RawTx
	case TxTypeRefundDirect:
		sourceRawTxBytes = dbLeaf.DirectTx
	case TxTypeRefundDirectFromCPFP:
		sourceRawTxBytes = dbLeaf.RawTx
	default:
		return fmt.Errorf("unknown transaction type: %d", txType)
	}
	err = VerifyTransactionWithSource(clientRawTxBytes, sourceRawTxBytes, 0, cpfpRefundTxTimelock, txType, refundDestPubkey)
	if err != nil {
		return fmt.Errorf("failed to verify transaction of leaf %s: %w", dbLeaf.ID, err)
	}
	return nil
}

// VerifyTransactionWithSource validates a Bitcoin transaction by reconstructing it from a source transaction
func VerifyTransactionWithSource(clientRawTxBytes []byte, sourceRawTxBytes []byte, vout uint32, cpfpRefundTxTimelock uint32, txType TxType, destPubkey keys.Public) error {
	clientTx, err := common.TxFromRawTxBytes(clientRawTxBytes)
	if err != nil {
		return fmt.Errorf("failed to parse client tx: %w, tx type: %d", err, txType)
	}

	clientSequence, err := GetAndValidateUserSequence(clientRawTxBytes)
	if err != nil {
		return fmt.Errorf("failed to validate user sequence: %w, tx type: %d", err, txType)
	}

	// Allow only V2 or V3 client transactions and use the client's version for construction.
	if clientTx.Version != 2 && clientTx.Version != 3 {
		return fmt.Errorf("unsupported transaction version: %d, tx type: %d", clientTx.Version, txType)
	}

	// Construct the expected transaction based on the type
	expectedTx, err := constructExpectedTransaction(sourceRawTxBytes, vout, cpfpRefundTxTimelock, txType, destPubkey, clientSequence, clientTx.Version)
	if err != nil {
		return fmt.Errorf("failed to construct expected transaction: %w, tx type: %d", err, txType)
	}

	// Compare the expected and client transactions with CompareTransactions first to return a more helpful error message
	err = common.CompareTransactions(expectedTx, clientTx)
	if err != nil {
		return fmt.Errorf("transaction does not match expected construction: %w, tx type: %d", err, txType)
	}

	// Serialize the expected and client transactions to compare the raw bytes for more extensive validation.
	expectedTxBytes, err := common.SerializeTxNoWitness(expectedTx)
	if err != nil {
		return fmt.Errorf("failed to serialize expected transaction: %w, tx type: %d", err, txType)
	}

	clientTxBytes, err := common.SerializeTxNoWitness(clientTx)

	if err != nil {
		return fmt.Errorf("failed to serialize client transaction: %w, tx type: %d", err, txType)
	}
	if !bytes.Equal(expectedTxBytes, clientTxBytes) {
		diff := cmp.Diff(expectedTxBytes, clientTxBytes)
		return fmt.Errorf("transaction does not match expected construction: %s, tx type: %d", diff, txType)
	}

	return nil
}

// constructExpectedTransaction constructs the expected Bitcoin transaction based on source transaction, transaction type and timelock
func constructExpectedTransaction(sourceRawTxBytes []byte, vout uint32, cpfpRefundTxTimelock uint32, txType TxType, refundDestPubkey keys.Public, clientSequence uint32, txVersion int32) (*wire.MsgTx, error) {

	// Parse source tx
	sourceTx, err := common.TxFromRawTxBytes(sourceRawTxBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse source tx: %w", err)
	}
	// Build the server-side sequence (validate timelock and construct sequence bits)
	serverSequence, err := validateSequence(cpfpRefundTxTimelock, txType, clientSequence)
	if err != nil {
		return nil, fmt.Errorf("failed to validate client sequence: %w", err)
	}

	switch txType {
	case TxTypeRefundCPFP, TxTypeNodeCPFP:
		return constructCPFPRefundTransaction(sourceTx, vout, refundDestPubkey, serverSequence, txVersion)
	case TxTypeRefundDirect, TxTypeNodeDirect:
		return constructDirectRefundTransaction(sourceTx, vout, refundDestPubkey, serverSequence, txVersion)
	case TxTypeRefundDirectFromCPFP:
		return constructDirectFromCPFPRefundTransaction(sourceTx, vout, refundDestPubkey, serverSequence, txVersion)
	default:
		return nil, fmt.Errorf("unknown transaction type: %d", txType)
	}
}

// constructRefundTransactionGeneric creates a refund transaction with configurable parameters
// to avoid duplication across specific refund constructors.
func constructRefundTransactionGeneric(
	prevTxHash chainhash.Hash,
	sourceTxRaw []byte,
	vout uint32,
	refundDestPubkey keys.Public,
	clientSequence uint32,
	txVersion int32,
	watchtowerTxs bool,
	parseTxName string,
) (*wire.MsgTx, error) {
	// Validate public key before attempting to use it
	if refundDestPubkey.IsZero() {
		return nil, fmt.Errorf("invalid public key is zero")
	}

	tx := wire.NewMsgTx(txVersion)

	// Add input spending the provided prevTxHash at index 0
	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{
			Hash:  prevTxHash,
			Index: vout,
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

	sourceValue := parsedTx.TxOut[vout].Value
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

func constructCPFPRefundTransaction(sourceTx *wire.MsgTx, vout uint32, refundDestPubkey keys.Public, expectedSequence uint32, txVersion int32) (*wire.MsgTx, error) {
	sourceRawTxBytes, err := common.SerializeTx(sourceTx)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize source tx: %w", err)
	}
	tx, err := constructRefundTransactionGeneric(
		// Does this need reverse?
		sourceTx.TxHash(),
		sourceRawTxBytes,
		vout,
		refundDestPubkey,
		expectedSequence,
		txVersion,
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
func constructDirectRefundTransaction(sourceTx *wire.MsgTx, vout uint32, refundDestPubkey keys.Public, expectedSequence uint32, txVersion int32) (*wire.MsgTx, error) {
	sourceRawTxBytes, err := common.SerializeTx(sourceTx)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize source tx: %w", err)
	}
	tx, err := constructRefundTransactionGeneric(
		sourceTx.TxHash(),
		sourceRawTxBytes,
		vout,
		refundDestPubkey,
		expectedSequence,
		txVersion,
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
func constructDirectFromCPFPRefundTransaction(sourceTx *wire.MsgTx, vout uint32, refundDestPubkey keys.Public, clientSequence uint32, txVersion int32) (*wire.MsgTx, error) {
	sourceRawTxBytes, err := common.SerializeTx(sourceTx)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize source tx: %w", err)
	}

	tx, err := constructRefundTransactionGeneric(
		sourceTx.TxHash(),
		sourceRawTxBytes,
		vout,
		refundDestPubkey,
		clientSequence,
		txVersion,
		/*watchtowerTxs=*/ true,
		/*parseTxName=*/ "node tx",
	)
	if err != nil {
		return nil, fmt.Errorf("failed to construct DirectFromCPFP refund transaction: %w", err)
	}
	return tx, nil
}

// validateSequence validates the client's sequence number against existing database transactions
func validateSequence(cpfpTimelock uint32, txType TxType, clientSequence uint32) (uint32, error) {

	var expectedCPFPTimelock uint32

	roundedCpfpTimelock := roundDownToTimelockInterval(cpfpTimelock)

	// For node transaction, we don't need to subtract TimeLockInterval
	if (txType == TxTypeNodeCPFP) || (txType == TxTypeNodeDirect) {
		expectedCPFPTimelock = roundedCpfpTimelock
	} else {
		// For refund transaction, validate that the timelock is large enough to subtract TimeLockInterval
		if roundedCpfpTimelock < spark.TimeLockInterval {
			return 0, fmt.Errorf("current timelock %d (rounded from %d) in CPFP refund transaction is too small to subtract TimeLockInterval %d",
				roundedCpfpTimelock, cpfpTimelock, spark.TimeLockInterval)
		}
		// Calculate the expected new timelock (should be TimeLockInterval shorter)
		expectedCPFPTimelock = roundedCpfpTimelock - spark.TimeLockInterval
	}

	// Get the expected timelock based on transaction type
	var expectedTimelock uint32
	switch txType {
	case TxTypeRefundDirect, TxTypeRefundDirectFromCPFP, TxTypeNodeDirect:
		expectedTimelock = expectedCPFPTimelock + spark.DirectTimelockOffset
	case TxTypeRefundCPFP, TxTypeNodeCPFP:
		expectedTimelock = expectedCPFPTimelock
	default:
		return 0, fmt.Errorf("unknown transaction type: %d", txType)
	}

	providedTimelock := GetTimelockFromSequence(clientSequence)
	if providedTimelock != expectedTimelock {
		return 0, fmt.Errorf("provided timelock 0x%08X does not match expected timelock 0x%08X", providedTimelock, expectedTimelock)
	}

	// Validate that the client's timelock (bits 0-15) matches expected
	err := ValidateSequenceTimelock(clientSequence, expectedTimelock)
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

// roundDownToTimelockInterval handles leaves that have non-aligned timelocks (e.g., 740 instead of 700)
func roundDownToTimelockInterval(timelock uint32) uint32 {
	return timelock - (timelock % spark.TimeLockInterval)
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

func GetCpfpTimelockFromLeaf(dbLeaf *ent.TreeNode) (uint32, error) {
	rawRefundTx, err := common.TxFromRawTxBytes(dbLeaf.RawRefundTx)
	if err != nil {
		return 0, fmt.Errorf("failed to parse CPFP refund transaction: %w", err)
	}
	cpfpRefundTxTimelock := GetTimelockFromSequence(rawRefundTx.TxIn[0].Sequence)
	return cpfpRefundTxTimelock, nil
}
