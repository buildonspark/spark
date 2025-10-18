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

	// Validate the client's sequence against database transactions when possible
	err := validateSequence(dbLeaf, txType, clientSequence)
	if err != nil {
		return nil, fmt.Errorf("failed to validate client sequence: %w", err)
	}

	// Construct the expected transaction based on type
	switch txType {
	case RefundTxTypeCPFP:
		return constructCPFPRefundTransaction(dbLeaf, refundDestPubkey, clientSequence)
	case RefundTxTypeDirect:
		return constructDirectRefundTransaction(dbLeaf, refundDestPubkey, clientSequence)
	case RefundTxTypeDirectFromCPFP:
		return constructDirectFromCPFPRefundTransaction(dbLeaf, refundDestPubkey, clientSequence)
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
		chainhash.Hash(dbLeaf.RawTxid),
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
		chainhash.Hash(dbLeaf.DirectTxid),
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
		chainhash.Hash(dbLeaf.RawTxid),
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
func validateSequence(dbLeaf *ent.TreeNode, txType RefundTxType, clientSequence uint32) error {
	// Parse the transaction
	rawRefundTx, err := common.TxFromRawTxBytes(dbLeaf.RawRefundTx)
	if err != nil {
		return fmt.Errorf("failed to parse CPFP refund transaction: %w", err)
	}

	if len(rawRefundTx.TxIn) == 0 {
		return fmt.Errorf("CPFP refund transaction has no inputs")
	}

	// Extract the current timelock from the transaction (bits 0-15)
	cpfpRefundTxTimelock := rawRefundTx.TxIn[0].Sequence & 0xFFFF

	// Validate that the timelock is large enough to subtract TimeLockInterval
	if cpfpRefundTxTimelock < spark.TimeLockInterval {
		return fmt.Errorf("current timelock %d in CPFP refund transaction is too small to subtract TimeLockInterval %d",
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
		return fmt.Errorf("unknown transaction type: %d", txType)
	}

	// Validate that the client's timelock (bits 0-15) matches expected
	err = ValidateSequenceTimelock(clientSequence, expectedTimelock)
	if err != nil {
		return fmt.Errorf("failed to validate client sequence timelock for tx type %d: %w", txType, err)
	}

	return nil
}

func GetAndValidateUserSequence(rawTxBytes []byte) (uint32, error) {
	// Validate that bit 31 (disable flag) and bit 22 (type flag) are NOT set
	const (
		disableBit = uint32(1 << 31) // Bit 31: disables BIP68 relative timelock
		typeBit    = uint32(1 << 22) // Bit 22: 0=block height, 1=time-based
	)

	tx, err := common.TxFromRawTxBytes(rawTxBytes)
	if err != nil {
		return 0, err
	}

	if len(tx.TxIn) == 0 {
		return 0, fmt.Errorf("transaction has no inputs")
	}
	userSequence := tx.TxIn[0].Sequence

	if userSequence&disableBit != 0 {
		return 0, fmt.Errorf("client sequence has bit 31 set (timelock disabled)")
	}
	if userSequence&typeBit != 0 {
		return 0, fmt.Errorf("client sequence has bit 22 set (time-based timelock not supported)")
	}

	return userSequence, nil
}

func GetTimelockFromSequence(sequence uint32) uint32 {
	return sequence & 0xFFFF
}

func ValidateSequenceTimelock(sequence uint32, expectedTimelock uint32) error {
	providedTimelock := GetTimelockFromSequence(sequence)
	if providedTimelock != expectedTimelock {
		return fmt.Errorf("provided timelock %d does not match expected timelock %d", providedTimelock, expectedTimelock)
	}
	return nil
}
