package bitcointransaction_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common"
	bitcointransaction "github.com/lightsparkdev/spark/common/bitcoin_transaction"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func withKnob(ctx context.Context, enabled bool) context.Context {
	v := 0.0
	if enabled {
		v = 1.0
	}
	k := knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobDisableV2TXs + "@REGTEST": v,
	})
	return knobs.InjectKnobsService(ctx, k)
}

const (
	defaultVersion       = 3
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
		RawTxid:                st.NewTxID(nodeTxHash),
		DirectTx:               serializeTx(t, directTx),
		DirectTxid:             st.NewTxID(directTxHash),
		Network:                btcnetwork.Regtest,
		RawRefundTx:            serializeTx(t, cpfpRefundTx),
		DirectRefundTx:         serializeTx(t, directRefundTx),
		DirectFromCpfpRefundTx: serializeTx(t, directFromCpfpRefundTx),
	}, pubKey
}

// createClientTx is a helper to construct a raw transaction for tests.
func createClientTx(t *testing.T, prevTxHash chainhash.Hash, sequence uint32, outputs ...*wire.TxOut) []byte {
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

// Verifies CPFP refund transaction matches expected construction.
func TestVerifyTransactionWithDatabase_Success_CPFP(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	dbLeaf, refundDestPubkey := newTestLeafNode(t)
	networkString := dbLeaf.Network.String()
	userScript, err := common.P2TRScriptFromPubKey(refundDestPubkey)
	require.NoError(t, err)

	clientRawTx := createClientTx(t,
		dbLeaf.RawTxid.Hash(),
		expectedCpfpTimelock,
		&wire.TxOut{Value: testSourceValue, PkScript: userScript},
		common.EphemeralAnchorOutput(),
	)

	require.NoError(t, bitcointransaction.VerifyTransactionWithDatabase(ctx, clientRawTx, dbLeaf, bitcointransaction.TxTypeRefundCPFP, refundDestPubkey, networkString))
}

// Verifies Direct refund transaction matches expected construction.
func TestVerifyTransactionWithDatabase_Success_Direct(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	dbLeaf, refundDestPubkey := newTestLeafNode(t)
	networkString := dbLeaf.Network.String()
	userScript, err := common.P2TRScriptFromPubKey(refundDestPubkey)
	require.NoError(t, err)

	clientRawTx := createClientTx(t,
		dbLeaf.DirectTxid.Hash(),
		expectedCpfpTimelock+50,
		&wire.TxOut{Value: common.MaybeApplyFee(testSourceValue), PkScript: userScript},
	)

	require.NoError(t, bitcointransaction.VerifyTransactionWithDatabase(ctx, clientRawTx, dbLeaf, bitcointransaction.TxTypeRefundDirect, refundDestPubkey, networkString))
}

// Verifies Direct-from-CPFP refund transaction matches expected construction.
func TestVerifyTransactionWithDatabase_Success_DirectFromCPFP(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	dbLeaf, refundDestPubkey := newTestLeafNode(t)
	networkString := dbLeaf.Network.String()
	userScript, err := common.P2TRScriptFromPubKey(refundDestPubkey)
	require.NoError(t, err)

	clientRawTx := createClientTx(t,
		dbLeaf.RawTxid.Hash(),
		expectedCpfpTimelock+50,
		&wire.TxOut{Value: common.MaybeApplyFee(testSourceValue), PkScript: userScript},
	)

	require.NoError(t, bitcointransaction.VerifyTransactionWithDatabase(ctx, clientRawTx, dbLeaf, bitcointransaction.TxTypeRefundDirectFromCPFP, refundDestPubkey, networkString))
}

// Errors on invalid client transaction bytes.
func TestVerifyTransactionWithDatabase_Error_InvalidClientTxBytes(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	dbLeaf, refundDestPubkey := newTestLeafNode(t)
	networkString := dbLeaf.Network.String()
	err := bitcointransaction.VerifyTransactionWithDatabase(ctx, []byte("invalid tx"), dbLeaf, bitcointransaction.TxTypeRefundCPFP, refundDestPubkey, networkString)
	require.ErrorContains(t, err, "failed to parse client tx")
}

// Errors when the client transaction has no inputs.
func TestVerifyTransactionWithDatabase_Error_ClientTxNoInputs(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	dbLeaf, refundDestPubkey := newTestLeafNode(t)
	networkString := dbLeaf.Network.String()
	userScript, err := common.P2TRScriptFromPubKey(refundDestPubkey)
	require.NoError(t, err)

	tx := wire.NewMsgTx(defaultVersion)
	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: chainhash.Hash{}, Index: 0},
		Sequence:         0,
	})
	tx.AddTxOut(&wire.TxOut{Value: testSourceValue, PkScript: userScript})
	// Remove the input to create a transaction with no inputs
	tx.TxIn = nil
	clientRawTx := serializeTx(t, tx)

	err = bitcointransaction.VerifyTransactionWithDatabase(ctx, clientRawTx, dbLeaf, bitcointransaction.TxTypeRefundCPFP, refundDestPubkey, networkString)
	require.ErrorContains(t, err, "failed to parse client tx")
}

// Errors when client transaction outputs/values don't match expected.
func TestVerifyTransactionWithDatabase_Error_MismatchedTransaction(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	dbLeaf, refundDestPubkey := newTestLeafNode(t)
	networkString := dbLeaf.Network.String()
	userScript, err := common.P2TRScriptFromPubKey(refundDestPubkey)
	require.NoError(t, err)

	clientRawTx := createClientTx(t,
		dbLeaf.RawTxid.Hash(),
		expectedCpfpTimelock,
		&wire.TxOut{Value: testSourceValue - 1, PkScript: userScript},
		common.EphemeralAnchorOutput(),
	)

	err = bitcointransaction.VerifyTransactionWithDatabase(ctx, clientRawTx, dbLeaf, bitcointransaction.TxTypeRefundCPFP, refundDestPubkey, networkString)
	require.ErrorContains(t, err, "transaction does not match expected construction")
}

// Errors when client sequence bit 31 is set.
func TestVerifyTransactionWithDatabase_Error_SequenceValidationBit31Set(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	dbLeaf, refundDestPubkey := newTestLeafNode(t)
	networkString := dbLeaf.Network.String()
	userScript, err := common.P2TRScriptFromPubKey(refundDestPubkey)
	require.NoError(t, err)

	clientRawTx := createClientTx(t,
		dbLeaf.RawTxid.Hash(),
		expectedCpfpTimelock|(1<<31),
		&wire.TxOut{Value: testSourceValue, PkScript: userScript},
		common.EphemeralAnchorOutput(),
	)

	err = bitcointransaction.VerifyTransactionWithDatabase(ctx, clientRawTx, dbLeaf, bitcointransaction.TxTypeRefundCPFP, refundDestPubkey, networkString)
	require.ErrorContains(t, err, "client sequence has bit 31 set")
}

// Errors when client sequence bit 22 is set.
func TestVerifyTransactionWithDatabase_Error_SequenceValidationBit22Set(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	dbLeaf, refundDestPubkey := newTestLeafNode(t)
	networkString := dbLeaf.Network.String()
	userScript, err := common.P2TRScriptFromPubKey(refundDestPubkey)
	require.NoError(t, err)

	clientRawTx := createClientTx(t,
		dbLeaf.RawTxid.Hash(),
		expectedCpfpTimelock|(1<<22),
		&wire.TxOut{Value: testSourceValue, PkScript: userScript},
		common.EphemeralAnchorOutput(),
	)

	err = bitcointransaction.VerifyTransactionWithDatabase(ctx, clientRawTx, dbLeaf, bitcointransaction.TxTypeRefundCPFP, refundDestPubkey, networkString)
	require.ErrorContains(t, err, "client sequence has bit 22 set")
}

// Verifies that a version 2 transaction is not accepted.
func TestVerifyTransactionWithDatabase_Fail_Version2(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	dbLeaf, refundDestPubkey := newTestLeafNode(t)
	networkString := dbLeaf.Network.String()
	userScript, err := common.P2TRScriptFromPubKey(refundDestPubkey)
	require.NoError(t, err)

	tx := wire.NewMsgTx(2)
	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: dbLeaf.RawTxid.Hash(), Index: 0},
		Sequence:         expectedCpfpTimelock,
	})
	tx.AddTxOut(&wire.TxOut{Value: testSourceValue, PkScript: userScript})
	tx.AddTxOut(common.EphemeralAnchorOutput())
	clientRawTx := serializeTx(t, tx)

	err = bitcointransaction.VerifyTransactionWithDatabase(ctx, clientRawTx, dbLeaf, bitcointransaction.TxTypeRefundCPFP, refundDestPubkey, networkString)
	require.ErrorContains(t, err, "unsupported transaction version")
}

// Errors when client timelock does not match expected.
func TestVerifyTransactionWithDatabase_Error_TimelockMismatch(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	dbLeaf, refundDestPubkey := newTestLeafNode(t)
	networkString := dbLeaf.Network.String()
	userScript, err := common.P2TRScriptFromPubKey(refundDestPubkey)
	require.NoError(t, err)

	clientRawTx := createClientTx(t,
		dbLeaf.RawTxid.Hash(),
		expectedCpfpTimelock+spark.DirectTimelockOffset, // Wrong timelock
		&wire.TxOut{Value: testSourceValue, PkScript: userScript},
		common.EphemeralAnchorOutput(),
	)

	err = bitcointransaction.VerifyTransactionWithDatabase(ctx, clientRawTx, dbLeaf, bitcointransaction.TxTypeRefundCPFP, refundDestPubkey, networkString)
	require.ErrorContains(t, err, "does not match expected timelock")
}

// Errors when DB-stored node transaction data is corrupt.
func TestVerifyTransactionWithDatabase_Error_CorruptedDBData(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	dbLeaf, refundDestPubkey := newTestLeafNode(t)
	networkString := dbLeaf.Network.String()
	userScript, err := common.P2TRScriptFromPubKey(refundDestPubkey)
	require.NoError(t, err)

	clientRawTx := createClientTx(t,
		dbLeaf.RawTxid.Hash(),
		expectedCpfpTimelock,
		&wire.TxOut{Value: testSourceValue, PkScript: userScript},
		common.EphemeralAnchorOutput(),
	)

	badLeaf, _ := newTestLeafNode(t)
	badLeaf.RawTx = []byte("bad raw tx")

	err = bitcointransaction.VerifyTransactionWithDatabase(ctx, clientRawTx, badLeaf, bitcointransaction.TxTypeRefundCPFP, refundDestPubkey, networkString)
	require.ErrorContains(t, err, "failed to parse source tx")
}

// Errors when DB refund timelock is too small to subtract interval.
func TestVerifyTransactionWithDatabase_Error_InsufficientTimelockInDB(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	dbLeaf, refundDestPubkey := newTestLeafNode(t)
	networkString := dbLeaf.Network.String()
	userScript, err := common.P2TRScriptFromPubKey(refundDestPubkey)
	require.NoError(t, err)

	clientRawTx := createClientTx(t,
		dbLeaf.RawTxid.Hash(),
		expectedCpfpTimelock,
		&wire.TxOut{Value: testSourceValue, PkScript: userScript},
		common.EphemeralAnchorOutput(),
	)

	badLeaf, key := newTestLeafNode(t)
	pkScript, _ := common.P2TRScriptFromPubKey(key)
	nodeTxHash := badLeaf.RawTxid.Hash()
	// Create a refund tx with a timelock smaller than the interval
	badRefundTx := newTestTx(testSourceValue, pkScript, spark.TimeLockInterval-1, &nodeTxHash)
	badLeaf.RawRefundTx = serializeTx(t, badRefundTx)

	err = bitcointransaction.VerifyTransactionWithDatabase(ctx, clientRawTx, badLeaf, bitcointransaction.TxTypeRefundCPFP, refundDestPubkey, networkString)
	require.ErrorContains(t, err, "is too small to subtract TimeLockInterval")
}

// Errors on unknown refund transaction type.
func TestVerifyTransactionWithDatabase_Error_UnknownTxType(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	dbLeaf, refundDestPubkey := newTestLeafNode(t)
	networkString := dbLeaf.Network.String()
	userScript, err := common.P2TRScriptFromPubKey(refundDestPubkey)
	require.NoError(t, err)

	clientRawTx := createClientTx(t,
		dbLeaf.RawTxid.Hash(),
		expectedCpfpTimelock,
		&wire.TxOut{Value: testSourceValue, PkScript: userScript},
		common.EphemeralAnchorOutput(),
	)

	err = bitcointransaction.VerifyTransactionWithDatabase(ctx, clientRawTx, dbLeaf, bitcointransaction.TxType(99), refundDestPubkey, networkString)
	require.ErrorContains(t, err, "unknown transaction type: 99")
}

// TestConstructExpectedTransaction covers the sub-flows of constructing transactions.
func TestConstructExpectedTransaction_UnknownTransactionType(t *testing.T) {
	// Errors when constructing expected transaction with unknown type.
	dbLeaf, refundDestPubkey := newTestLeafNode(t)
	currTimelock, err := bitcointransaction.GetCpfpTimelockFromLeaf(dbLeaf)
	require.NoError(t, err)
	_, err = bitcointransaction.ConstructExpectedTransaction(dbLeaf.RawRefundTx, uint32(0), currTimelock, bitcointransaction.TxType(99), refundDestPubkey, 0, defaultVersion)
	require.ErrorContains(t, err, "unknown transaction type: 99")
}

func TestConstructExpectedTransaction_P2TRScriptCreationFailure(t *testing.T) {
	// Errors when constructing expected transaction with a zero public key.
	dbLeaf, _ := newTestLeafNode(t)
	currTimelock, err := bitcointransaction.GetCpfpTimelockFromLeaf(dbLeaf)
	require.NoError(t, err)
	var invalidPubKey keys.Public
	_, err = bitcointransaction.ConstructExpectedTransaction(dbLeaf.RawRefundTx, uint32(0), currTimelock, bitcointransaction.TxTypeRefundCPFP, invalidPubKey, expectedCpfpTimelock, defaultVersion)
	require.ErrorContains(t, err, "public key is zero")
}

// Creates a valid P2TR script from a public key.
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
	tests := []struct {
		name          string
		currSeq       uint32
		wantSeq       uint32
		wantDirectSeq uint32
	}{
		{name: "basic", currSeq: 1000, wantSeq: 900, wantDirectSeq: 950},
		{name: "mixed upper-word pattern", currSeq: 0xAAAA0500, wantSeq: 0xAAAA049C, wantDirectSeq: 0xAAAA04CE},
		{name: "large timelock value", currSeq: 65535, wantSeq: 65435, wantDirectSeq: 65485},
		{name: "boundary at exactly one TimeLockInterval", currSeq: 100, wantSeq: 0, wantDirectSeq: 50},
		{name: "multiple higher-order bits", currSeq: 1<<30 | 1<<29 | 1<<16 | 2000, wantSeq: 1<<30 | 1<<29 | 1<<16 | 1900, wantDirectSeq: 1<<30 | 1<<29 | 1<<16 | 1950},
		{name: "preserves higher-order bits", currSeq: 1<<30 | 1000, wantSeq: 1<<30 | 900, wantDirectSeq: 1<<30 | 950},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			nextSeq, nextDirectSeq, err := bitcointransaction.NextSequence(tc.currSeq)
			require.NoError(t, err)
			assert.Equal(t, tc.wantSeq, nextSeq)
			assert.Equal(t, tc.wantDirectSeq, nextDirectSeq)

			inputTimelock := tc.currSeq & 0xFFFF
			inputUpperBits := tc.currSeq & 0xFFFF0000
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

// Errors when timelock minus interval would be negative.
func TestNextSequence_ErrorTimelockTooSmall(t *testing.T) {
	cases := []struct {
		name         string
		currSequence uint32
	}{
		{"zero timelock", 0},
		{"less than interval", 99},
		{"less than interval with higher bits", 1<<30 | 50},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			nextSeq, nextDirectSeq, err := bitcointransaction.NextSequence(tc.currSequence)
			require.ErrorContains(t, err, "next timelock interval is less than 0")
			assert.Zero(t, nextSeq)
			assert.Zero(t, nextDirectSeq)
		})
	}
}

// Ensure the server constructs the sequence from the client's provided sequence by:
// - Clearing forbidden upper bits (31 and 22)
// - Forcing the lower 16 bits (timelock) to the expected value based on tx type
func TestValidateSequence_ServerSequenceConstruction(t *testing.T) {
	dbLeaf, refundDestPubkey := newTestLeafNode(t)

	rawRefundTx, err := common.TxFromRawTxBytes(dbLeaf.RawRefundTx)
	require.NoError(t, err)
	currTimelock := rawRefundTx.TxIn[0].Sequence & 0xFFFF
	expectedCpfp := currTimelock - spark.TimeLockInterval

	testCases := []struct {
		name             string
		txType           bitcointransaction.TxType
		expectedTimelock uint32
	}{
		{name: "CPFP", txType: bitcointransaction.TxTypeRefundCPFP, expectedTimelock: expectedCpfp},
		{name: "Direct", txType: bitcointransaction.TxTypeRefundDirect, expectedTimelock: expectedCpfp + spark.DirectTimelockOffset},
		{name: "DirectFromCPFP", txType: bitcointransaction.TxTypeRefundDirectFromCPFP, expectedTimelock: expectedCpfp + spark.DirectTimelockOffset},
	}

	const (
		disableBit = uint32(1 << 31)
		typeBit    = uint32(1 << 22)
	)

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Provide a client sequence where forbidden bits are set and lower 16 bits match expected
			upperWithForbidden := uint32(0xAAAA0000) | disableBit | typeBit
			clientSeq := upperWithForbidden | (tc.expectedTimelock & 0xFFFF)

			// validateSequence should clear the forbidden bits and keep the expected timelock
			serverSeq, err := bitcointransaction.ValidateSequence(currTimelock, tc.txType, clientSeq)
			require.NoError(t, err)

			sanitizedUpper := (upperWithForbidden & 0xFFFF0000) &^ (disableBit | typeBit)
			expectedServerSeq := sanitizedUpper | (tc.expectedTimelock & 0xFFFF)
			assert.Equal(t, expectedServerSeq, serverSeq)

			// The constructed transaction should use exactly the server-generated sequence

			tx, err := bitcointransaction.ConstructExpectedTransaction(dbLeaf.RawTx, uint32(0), currTimelock, tc.txType, refundDestPubkey, clientSeq, defaultVersion)
			require.NoError(t, err)
			require.Len(t, tx.TxIn, 1)
			assert.Equal(t, expectedServerSeq, tx.TxIn[0].Sequence)
		})
	}
}

// Ensure a mismatch in client-provided timelock is surfaced clearly
func TestValidateSequence_TimelockMismatchErrorContains(t *testing.T) {
	dbLeaf, _ := newTestLeafNode(t)

	rawRefundTx, err := common.TxFromRawTxBytes(dbLeaf.RawRefundTx)
	require.NoError(t, err)
	currTimelock := rawRefundTx.TxIn[0].Sequence & 0xFFFF
	expectedCpfp := currTimelock - spark.TimeLockInterval

	// For CPFP, expected is expectedCpfp. Provide an off-by-one timelock.
	const (
		disableBit = uint32(1 << 31)
		typeBit    = uint32(1 << 22)
	)
	upperWithForbidden := uint32(0x12340000) | disableBit | typeBit
	mismatchedClientSeq := upperWithForbidden | ((expectedCpfp + 1) & 0xFFFF)

	_, err = bitcointransaction.ValidateSequence(currTimelock, bitcointransaction.TxTypeRefundCPFP, mismatchedClientSeq)
	require.ErrorContains(t, err, "does not match expected timelock")
}

// Errors when the client tx version does not match expected.
func TestVerifyTransactionWithDatabase_Error_MismatchedVersion(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	dbLeaf, refundDestPubkey := newTestLeafNode(t)
	networkString := dbLeaf.Network.String()
	userScript, err := common.P2TRScriptFromPubKey(refundDestPubkey)
	require.NoError(t, err)

	// Build a client tx identical to expected CPFP tx, except with a different version.
	tx := wire.NewMsgTx(defaultVersion - 2) // expected is version 2 or 3
	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: dbLeaf.RawTxid.Hash(), Index: 0},
		Sequence:         expectedCpfpTimelock,
	})
	tx.AddTxOut(&wire.TxOut{Value: testSourceValue, PkScript: userScript})
	tx.AddTxOut(common.EphemeralAnchorOutput())
	clientRawTx := serializeTx(t, tx)

	err = bitcointransaction.VerifyTransactionWithDatabase(ctx, clientRawTx, dbLeaf, bitcointransaction.TxTypeRefundCPFP, refundDestPubkey, networkString)
	require.ErrorContains(t, err, "unsupported transaction version")
}

// Errors when the client tx has a different number of inputs than expected.
func TestVerifyTransactionWithDatabase_Error_MismatchedNumInputs_CPFP(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	dbLeaf, refundDestPubkey := newTestLeafNode(t)
	networkString := dbLeaf.Network.String()
	userScript, err := common.P2TRScriptFromPubKey(refundDestPubkey)
	require.NoError(t, err)

	tx := wire.NewMsgTx(defaultVersion)
	// Expected single input spending node tx, add two inputs instead.
	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: dbLeaf.RawTxid.Hash(), Index: 0},
		Sequence:         expectedCpfpTimelock,
	})
	// Extra input to trigger mismatch
	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: chainhash.Hash{}, Index: 1},
		Sequence:         expectedCpfpTimelock,
	})
	tx.AddTxOut(&wire.TxOut{Value: testSourceValue, PkScript: userScript})
	tx.AddTxOut(common.EphemeralAnchorOutput())
	clientRawTx := serializeTx(t, tx)

	err = bitcointransaction.VerifyTransactionWithDatabase(ctx, clientRawTx, dbLeaf, bitcointransaction.TxTypeRefundCPFP, refundDestPubkey, networkString)
	require.ErrorContains(t, err, "transaction does not match expected construction")
	require.ErrorContains(t, err, "expected 1 inputs, got 2")
}

// Errors when the client tx has a different number of outputs than expected.
func TestVerifyTransactionWithDatabase_Error_MismatchedNumOutputs_CPFP(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	dbLeaf, refundDestPubkey := newTestLeafNode(t)
	networkString := dbLeaf.Network.String()
	userScript, err := common.P2TRScriptFromPubKey(refundDestPubkey)
	require.NoError(t, err)

	tx := wire.NewMsgTx(defaultVersion)
	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: dbLeaf.RawTxid.Hash(), Index: 0},
		Sequence:         expectedCpfpTimelock,
	})
	// Only add the refund output; omit anchor to trigger mismatch (expected 2 outputs).
	tx.AddTxOut(&wire.TxOut{Value: testSourceValue, PkScript: userScript})
	clientRawTx := serializeTx(t, tx)

	err = bitcointransaction.VerifyTransactionWithDatabase(ctx, clientRawTx, dbLeaf, bitcointransaction.TxTypeRefundCPFP, refundDestPubkey, networkString)
	require.ErrorContains(t, err, "transaction does not match expected construction")
	require.ErrorContains(t, err, "expected 2 outputs, got 1")
}

// Errors when the client tx spends the wrong previous outpoint (TxID/index).
func TestVerifyTransactionWithDatabase_Error_MismatchedPrevTxID(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	dbLeaf, refundDestPubkey := newTestLeafNode(t)
	networkString := dbLeaf.Network.String()
	userScript, err := common.P2TRScriptFromPubKey(refundDestPubkey)
	require.NoError(t, err)

	tx := wire.NewMsgTx(defaultVersion)
	// Intentionally use a wrong previous outpoint (wrong index) to ensure mismatch.
	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: chainhash.Hash{}, Index: 0},
		Sequence:         expectedCpfpTimelock,
	})
	tx.AddTxOut(&wire.TxOut{Value: testSourceValue, PkScript: userScript})
	tx.AddTxOut(common.EphemeralAnchorOutput())
	clientRawTx := serializeTx(t, tx)

	err = bitcointransaction.VerifyTransactionWithDatabase(ctx, clientRawTx, dbLeaf, bitcointransaction.TxTypeRefundCPFP, refundDestPubkey, networkString)
	require.ErrorContains(t, err, "transaction does not match expected construction")
	require.ErrorContains(t, err, "expected previous outpoint")
}

// Errors when the client tx locktime does not match expected.
func TestVerifyTransactionWithDatabase_Error_MismatchedLocktime(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	dbLeaf, refundDestPubkey := newTestLeafNode(t)
	networkString := dbLeaf.Network.String()
	userScript, err := common.P2TRScriptFromPubKey(refundDestPubkey)
	require.NoError(t, err)

	tx := wire.NewMsgTx(defaultVersion)
	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: dbLeaf.RawTxid.Hash(), Index: 0},
		Sequence:         expectedCpfpTimelock,
	})
	tx.AddTxOut(&wire.TxOut{Value: testSourceValue, PkScript: userScript})
	tx.AddTxOut(common.EphemeralAnchorOutput())
	// Set a non-zero locktime; expected is 0.
	tx.LockTime = 12345
	clientRawTx := serializeTx(t, tx)

	err = bitcointransaction.VerifyTransactionWithDatabase(ctx, clientRawTx, dbLeaf, bitcointransaction.TxTypeRefundCPFP, refundDestPubkey, networkString)
	require.ErrorContains(t, err, "transaction does not match expected construction")
	require.ErrorContains(t, err, "expected locktime 0, got 12345")
}

// Errors when the client tx (Direct) has a different number of inputs than expected.
func TestVerifyTransactionWithDatabase_Error_MismatchedNumInputs_Direct(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	dbLeaf, refundDestPubkey := newTestLeafNode(t)
	networkString := dbLeaf.Network.String()
	userScript, err := common.P2TRScriptFromPubKey(refundDestPubkey)
	require.NoError(t, err)

	tx := wire.NewMsgTx(defaultVersion)
	// Expected single input spending direct tx, add two inputs instead.
	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: dbLeaf.DirectTxid.Hash(), Index: 0},
		Sequence:         expectedCpfpTimelock + spark.DirectTimelockOffset,
	})
	// Extra input to trigger mismatch
	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: chainhash.Hash{}, Index: 1},
		Sequence:         expectedCpfpTimelock + spark.DirectTimelockOffset,
	})
	// Direct refunds have a single output with fee applied.
	tx.AddTxOut(&wire.TxOut{Value: common.MaybeApplyFee(testSourceValue), PkScript: userScript})
	clientRawTx := serializeTx(t, tx)

	err = bitcointransaction.VerifyTransactionWithDatabase(ctx, clientRawTx, dbLeaf, bitcointransaction.TxTypeRefundDirect, refundDestPubkey, networkString)
	require.ErrorContains(t, err, "transaction does not match expected construction")
	require.ErrorContains(t, err, "expected 1 inputs, got 2")
}

// Errors when the client tx (Direct) has a different number of outputs than expected.
func TestVerifyTransactionWithDatabase_Error_MismatchedNumOutputs_Direct(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	dbLeaf, refundDestPubkey := newTestLeafNode(t)
	networkString := dbLeaf.Network.String()
	userScript, err := common.P2TRScriptFromPubKey(refundDestPubkey)
	require.NoError(t, err)

	tx := wire.NewMsgTx(defaultVersion)
	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: dbLeaf.DirectTxid.Hash(), Index: 0},
		Sequence:         expectedCpfpTimelock + spark.DirectTimelockOffset,
	})
	// Add refund output (expected) plus an extra output to trigger mismatch.
	tx.AddTxOut(&wire.TxOut{Value: common.MaybeApplyFee(testSourceValue), PkScript: userScript})
	// Add an extra anchor-like output to cause length mismatch.
	tx.AddTxOut(common.EphemeralAnchorOutput())
	clientRawTx := serializeTx(t, tx)

	err = bitcointransaction.VerifyTransactionWithDatabase(ctx, clientRawTx, dbLeaf, bitcointransaction.TxTypeRefundDirect, refundDestPubkey, networkString)
	require.ErrorContains(t, err, "transaction does not match expected construction")
	require.ErrorContains(t, err, "expected 1 outputs, got 2")
}

// Errors when the client tx (DirectFromCPFP) has a different number of inputs than expected.
func TestVerifyTransactionWithDatabase_Error_MismatchedNumInputs_DirectFromCPFP(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	dbLeaf, refundDestPubkey := newTestLeafNode(t)
	networkString := dbLeaf.Network.String()
	userScript, err := common.P2TRScriptFromPubKey(refundDestPubkey)
	require.NoError(t, err)

	tx := wire.NewMsgTx(defaultVersion)
	// Expected single input spending node tx, add two inputs instead.
	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: dbLeaf.RawTxid.Hash(), Index: 0},
		Sequence:         expectedCpfpTimelock + spark.DirectTimelockOffset,
	})
	// Extra input to trigger mismatch
	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: chainhash.Hash{}, Index: 1},
		Sequence:         expectedCpfpTimelock + spark.DirectTimelockOffset,
	})
	// Direct-from-CPFP refunds have a single output with fee applied.
	tx.AddTxOut(&wire.TxOut{Value: common.MaybeApplyFee(testSourceValue), PkScript: userScript})
	clientRawTx := serializeTx(t, tx)

	err = bitcointransaction.VerifyTransactionWithDatabase(ctx, clientRawTx, dbLeaf, bitcointransaction.TxTypeRefundDirectFromCPFP, refundDestPubkey, networkString)
	require.ErrorContains(t, err, "transaction does not match expected construction")
	require.ErrorContains(t, err, "expected 1 inputs, got 2")
}

// Errors when the client tx (DirectFromCPFP) has a different number of outputs than expected.
func TestVerifyTransactionWithDatabase_Error_MismatchedNumOutputs_DirectFromCPFP(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	dbLeaf, refundDestPubkey := newTestLeafNode(t)
	networkString := dbLeaf.Network.String()
	userScript, err := common.P2TRScriptFromPubKey(refundDestPubkey)
	require.NoError(t, err)

	tx := wire.NewMsgTx(defaultVersion)
	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: dbLeaf.RawTxid.Hash(), Index: 0},
		Sequence:         expectedCpfpTimelock + spark.DirectTimelockOffset,
	})
	// Add refund output (expected) plus an extra output to trigger mismatch.
	tx.AddTxOut(&wire.TxOut{Value: common.MaybeApplyFee(testSourceValue), PkScript: userScript})
	// Add an extra anchor-like output to cause length mismatch.
	tx.AddTxOut(common.EphemeralAnchorOutput())
	clientRawTx := serializeTx(t, tx)

	err = bitcointransaction.VerifyTransactionWithDatabase(ctx, clientRawTx, dbLeaf, bitcointransaction.TxTypeRefundDirectFromCPFP, refundDestPubkey, networkString)
	require.ErrorContains(t, err, "transaction does not match expected construction")
	require.ErrorContains(t, err, "expected 1 outputs, got 2")
}

func TestRoundDownToTimelockInterval(t *testing.T) {
	tests := []struct {
		name     string
		timelock uint32
		expected uint32
	}{
		{name: "aligned 100", timelock: 100, expected: 100},
		{name: "aligned 1000", timelock: 1000, expected: 1000},
		{name: "misaligned 740", timelock: 740, expected: 700},
		{name: "misaligned 670", timelock: 670, expected: 600},
		{name: "zero", timelock: 0, expected: 0},
		{name: "htlc offset 1970", timelock: 1970, expected: 1900},
		{name: "htlc offset 1870", timelock: 1870, expected: 1800},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := bitcointransaction.RoundDownToTimelockInterval(tc.timelock)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestValidateSequence_MisalignedTimelock(t *testing.T) {
	dbTimelock := uint32(740)
	clientTimelock := uint32(600) // Correctly rounded by SDK

	serverSeq, err := bitcointransaction.ValidateSequence(dbTimelock, bitcointransaction.TxTypeRefundCPFP, clientTimelock)
	require.NoError(t, err)
	assert.Equal(t, clientTimelock, serverSeq&0xFFFF)
}

func TestValidateSequence_MisalignedTimelock_Direct(t *testing.T) {
	dbTimelock := uint32(740)
	clientTimelock := uint32(650) // 600 + DirectTimelockOffset

	serverSeq, err := bitcointransaction.ValidateSequence(dbTimelock, bitcointransaction.TxTypeRefundDirect, clientTimelock)
	require.NoError(t, err)
	assert.Equal(t, clientTimelock, serverSeq&0xFFFF)
}

// Tests that aligned timelocks continue to work
func TestValidateSequence_AlignedTimelock(t *testing.T) {
	dbTimelock := uint32(700)
	clientTimelock := uint32(600)

	serverSeq, err := bitcointransaction.ValidateSequence(dbTimelock, bitcointransaction.TxTypeRefundCPFP, clientTimelock)
	require.NoError(t, err)
	assert.Equal(t, clientTimelock, serverSeq&0xFFFF)
}

// Tests that wrong timelocks are still rejected
func TestValidateSequence_MisalignedTimelock_WrongClient(t *testing.T) {
	dbTimelock := uint32(740)
	clientTimelock := uint32(640) // Wrong - should be 600

	_, err := bitcointransaction.ValidateSequence(dbTimelock, bitcointransaction.TxTypeRefundCPFP, clientTimelock)
	require.ErrorContains(t, err, "does not match expected timelock")
}

// Tests full verification with misaligned timelock in database
func TestVerifyTransactionWithDatabase_MisalignedTimelock(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = withKnob(ctx, true)

	networkString := btcnetwork.Mainnet.String()

	pubKey := keys.GeneratePrivateKey().Public()
	pkScript, err := common.P2TRScriptFromPubKey(pubKey)
	require.NoError(t, err)

	nodeTx := newTestTx(testSourceValue, pkScript, 0, nil)
	nodeTxHash := nodeTx.TxHash()

	misalignedTimelock := uint32(740)
	cpfpRefundTx := newTestTx(testSourceValue, pkScript, misalignedTimelock, &nodeTxHash)

	dbLeaf := &ent.TreeNode{
		ID:          uuid.New(),
		RawTx:       serializeTx(t, nodeTx),
		RawTxid:     st.NewTxID(nodeTxHash),
		RawRefundTx: serializeTx(t, cpfpRefundTx),
	}

	expectedClientTimelock := uint32(600)
	clientRawTx := createClientTx(t,
		dbLeaf.RawTxid.Hash(),
		expectedClientTimelock,
		&wire.TxOut{Value: testSourceValue, PkScript: pkScript},
		common.EphemeralAnchorOutput(),
	)

	require.NoError(t, bitcointransaction.VerifyTransactionWithDatabase(ctx, clientRawTx, dbLeaf, bitcointransaction.TxTypeRefundCPFP, pubKey, networkString))
}
