package bitcointransaction

import (
	"bytes"
	"testing"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testTimeLock         = 1000
	testSourceValue      = 100000
	expectedCpfpTimelock = testTimeLock - spark.TimeLockInterval
)

// newTestTx creates a new transaction for testing.
func newTestTx(value int64, pkScript []byte, sequence uint32, prevTxHash *chainhash.Hash) *wire.MsgTx {
	tx := wire.NewMsgTx(defaultVersion)

	// Create a dummy previous outpoint if none provided
	if prevTxHash == nil {
		prevTxHash = &chainhash.Hash{}
	}

	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{
			Hash:  *prevTxHash,
			Index: 0,
		},
		Sequence: sequence,
	})

	tx.AddTxOut(&wire.TxOut{
		Value:    value,
		PkScript: pkScript,
	})
	return tx
}

// serializeTx serializes a transaction to bytes.
func serializeTx(t *testing.T, tx *wire.MsgTx) []byte {
	var buf bytes.Buffer
	err := tx.Serialize(&buf)
	require.NoError(t, err)
	return buf.Bytes()
}

// newTestLeafNode creates a new tree node for testing.
func newTestLeafNode(t *testing.T) (*ent.TreeNode, keys.Public) {
	pubKey := keys.GeneratePrivateKey().Public()
	pkScript, err := common.P2TRScriptFromPubKey(pubKey)
	require.NoError(t, err)

	// Create source transactions
	nodeTx := newTestTx(testSourceValue, pkScript, 0, nil)
	nodeTxHash := nodeTx.TxHash()
	directTx := newTestTx(testSourceValue, pkScript, 0, nil)
	directTxHash := directTx.TxHash()

	// Create refund transactions to be stored in the DB leaf
	cpfpRefundTx := newTestTx(testSourceValue, pkScript, testTimeLock, &nodeTxHash)
	directRefundTx := newTestTx(testSourceValue, pkScript, testTimeLock, &directTxHash)
	directFromCpfpRefundTx := newTestTx(testSourceValue, pkScript, testTimeLock, &nodeTxHash)

	return &ent.TreeNode{
		ID:                     uuid.New(),
		RawTx:                  serializeTx(t, nodeTx),
		RawTxid:                nodeTxHash[:],
		DirectTx:               serializeTx(t, directTx),
		DirectTxid:             directTxHash[:],
		RawRefundTx:            serializeTx(t, cpfpRefundTx),
		DirectRefundTx:         serializeTx(t, directRefundTx),
		DirectFromCpfpRefundTx: serializeTx(t, directFromCpfpRefundTx),
	}, pubKey
}

func TestVerifyTransactionWithDatabase(t *testing.T) {
	dbLeaf, refundDestPubkey := newTestLeafNode(t)
	userScript, err := common.P2TRScriptFromPubKey(refundDestPubkey)
	require.NoError(t, err)

	// Helper to create a client transaction
	createClientTx := func(prevTxHash chainhash.Hash, sequence uint32, outputs ...*wire.TxOut) []byte {
		tx := wire.NewMsgTx(defaultVersion)
		tx.AddTxIn(&wire.TxIn{
			PreviousOutPoint: wire.OutPoint{Hash: prevTxHash, Index: 0},
			Sequence:         sequence,
		})
		for _, out := range outputs {
			tx.AddTxOut(out)
		}
		return serializeTx(t, tx)
	}

	testCases := []struct {
		name          string
		clientRawTx   []byte
		txType        RefundTxType
		dbLeaf        *ent.TreeNode
		refundDestKey keys.Public
		expectErr     bool
		errContains   string
	}{
		{
			name:   "Happy Path - CPFP",
			txType: RefundTxTypeCPFP,
			clientRawTx: createClientTx(
				chainhash.Hash(dbLeaf.RawTxid),
				expectedCpfpTimelock,
				&wire.TxOut{Value: testSourceValue, PkScript: userScript},
				common.EphemeralAnchorOutput(),
			),
			dbLeaf:        dbLeaf,
			refundDestKey: refundDestPubkey,
			expectErr:     false,
		},
		{
			name:   "Happy Path - Direct",
			txType: RefundTxTypeDirect,
			clientRawTx: createClientTx(
				chainhash.Hash(dbLeaf.DirectTxid),
				expectedCpfpTimelock+50,
				&wire.TxOut{Value: common.MaybeApplyFee(testSourceValue), PkScript: userScript},
			),
			dbLeaf:        dbLeaf,
			refundDestKey: refundDestPubkey,
			expectErr:     false,
		},
		{
			name:   "Happy Path - DirectFromCPFP",
			txType: RefundTxTypeDirectFromCPFP,
			clientRawTx: createClientTx(
				chainhash.Hash(dbLeaf.RawTxid),
				expectedCpfpTimelock+50,
				&wire.TxOut{Value: common.MaybeApplyFee(testSourceValue), PkScript: userScript},
			),
			dbLeaf:        dbLeaf,
			refundDestKey: refundDestPubkey,
			expectErr:     false,
		},
		{
			name:          "Error - Invalid client tx bytes",
			txType:        RefundTxTypeCPFP,
			clientRawTx:   []byte("invalid tx"),
			dbLeaf:        dbLeaf,
			refundDestKey: refundDestPubkey,
			expectErr:     true,
			errContains:   "failed to parse client tx",
		},
		{
			name:   "Error - Client tx no inputs",
			txType: RefundTxTypeCPFP,
			clientRawTx: func() []byte {
				tx := wire.NewMsgTx(defaultVersion)
				tx.AddTxIn(&wire.TxIn{
					PreviousOutPoint: wire.OutPoint{
						Hash:  chainhash.Hash{},
						Index: 0,
					},
					Sequence: 0,
				})
				tx.AddTxOut(&wire.TxOut{
					Value:    testSourceValue,
					PkScript: userScript,
				})
				// Remove the input to create a transaction with no inputs
				tx.TxIn = tx.TxIn[:0]
				return serializeTx(t, tx)
			}(),
			dbLeaf:        dbLeaf,
			refundDestKey: refundDestPubkey,
			expectErr:     true,
			errContains:   "failed to parse client tx",
		},
		{
			name:   "Error - Mismatched transaction",
			txType: RefundTxTypeCPFP,
			clientRawTx: createClientTx(
				chainhash.Hash(dbLeaf.RawTxid),
				expectedCpfpTimelock,
				&wire.TxOut{Value: testSourceValue - 1, PkScript: userScript},
				common.EphemeralAnchorOutput(),
			),
			dbLeaf:        dbLeaf,
			refundDestKey: refundDestPubkey,
			expectErr:     true,
			errContains:   "transaction does not match expected construction",
		},
		{
			name:   "Error - Sequence validation bit 31 set",
			txType: RefundTxTypeCPFP,
			clientRawTx: createClientTx(
				chainhash.Hash(dbLeaf.RawTxid),
				expectedCpfpTimelock|(1<<31),
				&wire.TxOut{Value: testSourceValue, PkScript: userScript},
				common.EphemeralAnchorOutput(),
			),
			dbLeaf:        dbLeaf,
			refundDestKey: refundDestPubkey,
			expectErr:     true,
			errContains:   "client sequence has bit 31 set",
		},
		{
			name:   "Error - Sequence validation bit 22 set",
			txType: RefundTxTypeCPFP,
			clientRawTx: createClientTx(
				chainhash.Hash(dbLeaf.RawTxid),
				expectedCpfpTimelock|(1<<22),
				&wire.TxOut{Value: testSourceValue, PkScript: userScript},
				common.EphemeralAnchorOutput(),
			),
			dbLeaf:        dbLeaf,
			refundDestKey: refundDestPubkey,
			expectErr:     true,
			errContains:   "client sequence has bit 22 set",
		},
		{
			name:   "Error - Timelock mismatch",
			txType: RefundTxTypeCPFP,
			clientRawTx: createClientTx(
				chainhash.Hash(dbLeaf.RawTxid),
				expectedCpfpTimelock+spark.DirectTimelockOffset, // Wrong timelock
				&wire.TxOut{Value: testSourceValue, PkScript: userScript},
				common.EphemeralAnchorOutput(),
			),
			dbLeaf:        dbLeaf,
			refundDestKey: refundDestPubkey,
			expectErr:     true,
			errContains:   "does not match expected timelock",
		},
		{
			name:   "Error - Corrupted DB data",
			txType: RefundTxTypeCPFP,
			clientRawTx: createClientTx(
				chainhash.Hash(dbLeaf.RawTxid),
				expectedCpfpTimelock,
				&wire.TxOut{Value: testSourceValue, PkScript: userScript},
				common.EphemeralAnchorOutput(),
			),
			dbLeaf: func() *ent.TreeNode {
				badLeaf, _ := newTestLeafNode(t)
				badLeaf.RawTx = []byte("bad raw tx")
				return badLeaf
			}(),
			refundDestKey: refundDestPubkey,
			expectErr:     true,
			errContains:   "failed to parse node tx",
		},
		{
			name:   "Error - Insufficient timelock in DB",
			txType: RefundTxTypeCPFP,
			clientRawTx: createClientTx(
				chainhash.Hash(dbLeaf.RawTxid),
				expectedCpfpTimelock,
				&wire.TxOut{Value: testSourceValue, PkScript: userScript},
				common.EphemeralAnchorOutput(),
			),
			dbLeaf: func() *ent.TreeNode {
				badLeaf, key := newTestLeafNode(t)
				pkScript, _ := common.P2TRScriptFromPubKey(key)
				nodeTxHash := chainhash.Hash(badLeaf.RawTxid)
				// Create a refund tx with a timelock smaller than the interval
				badRefundTx := newTestTx(testSourceValue, pkScript, spark.TimeLockInterval-1, &nodeTxHash)
				badLeaf.RawRefundTx = serializeTx(t, badRefundTx)
				return badLeaf
			}(),
			refundDestKey: refundDestPubkey,
			expectErr:     true,
			errContains:   "is too small to subtract TimeLockInterval",
		},
		{
			name:   "Error - Unknown tx type",
			txType: RefundTxType(99),
			clientRawTx: createClientTx(
				chainhash.Hash(dbLeaf.RawTxid),
				expectedCpfpTimelock,
				&wire.TxOut{Value: testSourceValue, PkScript: userScript},
				common.EphemeralAnchorOutput(),
			),
			dbLeaf:        dbLeaf,
			refundDestKey: refundDestPubkey,
			expectErr:     true,
			errContains:   "unknown transaction type: 99",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := VerifyTransactionWithDatabase(tc.clientRawTx, tc.dbLeaf, tc.txType, tc.refundDestKey)
			if tc.expectErr {
				require.ErrorContains(t, err, tc.errContains)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestConstructExpectedTransaction covers the sub-flows of constructing transactions.
func TestConstructExpectedTransaction(t *testing.T) {
	dbLeaf, refundDestPubkey := newTestLeafNode(t)

	// Test case for unknown transaction type
	t.Run("Unknown transaction type", func(t *testing.T) {
		_, err := constructExpectedTransaction(dbLeaf, RefundTxType(99), refundDestPubkey, 0)
		require.ErrorContains(t, err, "unknown transaction type: 99")
	})

	// Test case for failure in P2TR script creation
	t.Run("P2TR script creation failure", func(t *testing.T) {
		var invalidPubKey keys.Public
		_, err := constructExpectedTransaction(dbLeaf, RefundTxTypeCPFP, invalidPubKey, expectedCpfpTimelock)
		require.ErrorContains(t, err, "public key is zero")
	})
}

// TestP2TRScriptFromPubKey tests the P2TR script creation from a public key.
func TestP2TRScriptFromPubKey(t *testing.T) {
	pubKey := keys.GeneratePrivateKey().Public()

	// Create the P2TR script.
	script, err := common.P2TRScriptFromPubKey(pubKey)
	require.NoError(t, err)

	// The script should be 34 bytes long: 1 byte for OP_1, 1 byte for data push, 32 bytes for the key.
	require.Len(t, script, 34)
	assert.Equal(t, byte(txscript.OP_1), script[0])
	assert.Equal(t, byte(txscript.OP_DATA_32), script[1])
}

func TestNextSequence(t *testing.T) {
	testCases := []struct {
		name                   string
		currSequence           uint32
		expectErr              bool
		errContains            string
		expectedNextSequence   uint32
		expectedDirectSequence uint32
	}{
		{
			name:                   "Valid sequence - basic case",
			currSequence:           1000, // timelock = 1000, higher bits = 0
			expectErr:              false,
			expectedNextSequence:   900, // 1000 - 100
			expectedDirectSequence: 950, // 900 + 50
		},
		{
			name:                   "Valid sequence - with higher order bits",
			currSequence:           (1<<30 | 1000), // bit 30 set, timelock = 1000
			expectErr:              false,
			expectedNextSequence:   (1<<30 | 900), // preserve bit 30, timelock = 900
			expectedDirectSequence: (1<<30 | 950), // preserve bit 30, timelock = 950
		},
		{
			name:                   "Valid sequence - multiple higher bits",
			currSequence:           (1<<30 | 1<<29 | 1<<16 | 2000), // multiple bits set, timelock = 2000
			expectErr:              false,
			expectedNextSequence:   (1<<30 | 1<<29 | 1<<16 | 1900), // preserve higher bits, timelock = 1900
			expectedDirectSequence: (1<<30 | 1<<29 | 1<<16 | 1950), // preserve higher bits, timelock = 1950
		},
		{
			name:                   "Boundary case - exactly TimeLockInterval",
			currSequence:           100, // timelock = 100 (spark.TimeLockInterval)
			expectErr:              false,
			expectedNextSequence:   0,  // 100 - 100 = 0
			expectedDirectSequence: 50, // 0 + 50
		},
		{
			name:                   "Large timelock value",
			currSequence:           65535, // maximum 16-bit value for timelock
			expectErr:              false,
			expectedNextSequence:   65435, // 65535 - 100
			expectedDirectSequence: 65485, // 65435 + 50
		},
		{
			name:         "Error case - timelock less than TimeLockInterval",
			currSequence: 99, // timelock = 99 < TimeLockInterval (100)
			expectErr:    true,
			errContains:  "next timelock interval is less than 0",
		},
		{
			name:         "Error case - zero timelock",
			currSequence: 0,
			expectErr:    true,
			errContains:  "next timelock interval is less than 0",
		},
		{
			name:         "Error case - timelock = 50 with higher bits",
			currSequence: (1<<30 | 50), // bit 30 set, timelock = 50 < TimeLockInterval
			expectErr:    true,
			errContains:  "next timelock interval is less than 0",
		},
		{
			name:                   "Bit pattern test - alternating bits in upper word",
			currSequence:           0xAAAA0500, // alternating pattern in upper 16 bits, timelock = 0x0500 (1280)
			expectErr:              false,
			expectedNextSequence:   0xAAAA049C, // preserve pattern, timelock = 0x049C (1180)
			expectedDirectSequence: 0xAAAA04CE, // preserve pattern, timelock = 0x04CE (1230)
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			nextSeq, nextDirectSeq, err := NextSequence(tc.currSequence)

			if tc.expectErr {
				require.Error(t, err)
				require.ErrorContains(t, err, tc.errContains)
				// When error occurs, both sequences should be 0
				assert.Equal(t, uint32(0), nextSeq)
				assert.Equal(t, uint32(0), nextDirectSeq)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.expectedNextSequence, nextSeq,
				"nextSequence mismatch - input: 0x%x, expected: 0x%x, got: 0x%x",
				tc.currSequence, tc.expectedNextSequence, nextSeq)
			assert.Equal(t, tc.expectedDirectSequence, nextDirectSeq,
				"nextDirectSequence mismatch - input: 0x%x, expected: 0x%x, got: 0x%x",
				tc.currSequence, tc.expectedDirectSequence, nextDirectSeq)

			// Verify timelock extraction and bit preservation
			inputTimelock := tc.currSequence & 0xFFFF
			inputUpperBits := tc.currSequence & 0xFFFF0000
			expectedTimelock := inputTimelock - spark.TimeLockInterval

			// Check that upper bits are preserved
			assert.Equal(t, inputUpperBits, nextSeq&0xFFFF0000,
				"upper bits not preserved in nextSequence")
			assert.Equal(t, inputUpperBits, nextDirectSeq&0xFFFF0000,
				"upper bits not preserved in nextDirectSequence")

			// Check timelock calculations
			assert.Equal(t, expectedTimelock, nextSeq&0xFFFF,
				"timelock calculation incorrect in nextSequence")
			assert.Equal(t, expectedTimelock+spark.DirectTimelockOffset, nextDirectSeq&0xFFFF,
				"timelock calculation incorrect in nextDirectSequence")

		})
	}
}
