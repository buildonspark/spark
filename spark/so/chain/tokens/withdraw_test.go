package tokens

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"
	"time"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/l1tokenoutputwithdrawal"
	"github.com/lightsparkdev/spark/so/ent/l1withdrawaltransaction"
	"github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/tokenoutput"
	"github.com/lightsparkdev/spark/so/entfixtures"
	"github.com/lightsparkdev/spark/so/handler/tokens"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Helper types and functions

type withdrawalRecord struct {
	bitcoinVout uint16
	sparkTxHash []byte
	sparkTxVout uint32
}

func buildWithdrawalScript(t *testing.T, sePubKey []byte, ownerSignature []byte, records []withdrawalRecord) []byte {
	var buf bytes.Buffer
	buf.WriteString(btknWithdrawal.Prefix)
	buf.Write(btknWithdrawal.Kind[:])
	buf.Write(sePubKey)
	buf.Write(ownerSignature)
	buf.WriteByte(byte(len(records)))

	for _, r := range records {
		require.NoError(t, binary.Write(&buf, binary.BigEndian, r.bitcoinVout))
		buf.Write(r.sparkTxHash)
		require.NoError(t, binary.Write(&buf, binary.BigEndian, r.sparkTxVout))
	}

	script, err := txscript.NewScriptBuilder().
		AddOp(txscript.OP_RETURN).
		AddData(buf.Bytes()).
		Script()
	require.NoError(t, err)

	return script
}

func setupWithdrawalTestContext(t *testing.T) (ctx context.Context, dbClient *ent.Client, fixtures *entfixtures.Fixtures, config *so.Config, sePubKey keys.Public) {
	ctx, _ = db.NewTestSQLiteContext(t)
	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	fixtures = entfixtures.New(t, ctx, dbClient)

	seKeyshare := fixtures.CreateKeyshareWithEntityDkgKey()
	sePubKey = seKeyshare.PublicKey

	config = sparktesting.TestConfig(t)
	return ctx, dbClient, fixtures, config, sePubKey
}

// createQueryHandler creates a QueryTokenOutputsHandler for testing verification.
func createQueryHandler(t *testing.T, config *so.Config) *tokens.QueryTokenOutputsHandler {
	t.Helper()
	return tokens.NewQueryTokenOutputsHandler(config)
}

// assertOutputNotSpendable verifies that a token output no longer appears in spendable outputs.
func assertOutputNotSpendable(t *testing.T, ctx context.Context, config *so.Config, ownerPubKey keys.Public, sparkTxHash []byte) {
	t.Helper()
	handler := createQueryHandler(t, config)

	resp, err := handler.QueryTokenOutputs(ctx, &tokenpb.QueryTokenOutputsRequest{
		OwnerPublicKeys: [][]byte{ownerPubKey.Serialize()},
		Network:         sparkpb.Network_REGTEST,
	})
	require.NoError(t, err)

	// Verify the specific output is NOT in the response
	for _, output := range resp.OutputsWithPreviousTransactionData {
		if bytes.Equal(output.PreviousTransactionHash, sparkTxHash) {
			t.Errorf("Output with sparkTxHash %x should not appear in spendable outputs after withdrawal", sparkTxHash)
		}
	}
}

// assertOutputStillSpendable verifies that a token output still appears in spendable outputs.
func assertOutputStillSpendable(t *testing.T, ctx context.Context, config *so.Config, ownerPubKey keys.Public, sparkTxHash []byte) {
	t.Helper()
	handler := createQueryHandler(t, config)

	resp, err := handler.QueryTokenOutputs(ctx, &tokenpb.QueryTokenOutputsRequest{
		OwnerPublicKeys: [][]byte{ownerPubKey.Serialize()},
		Network:         sparkpb.Network_REGTEST,
	})
	require.NoError(t, err)

	// Verify the specific output IS in the response
	found := false
	for _, output := range resp.OutputsWithPreviousTransactionData {
		if bytes.Equal(output.PreviousTransactionHash, sparkTxHash) {
			found = true
			break
		}
	}
	assert.True(t, found, "Output with sparkTxHash %x should still appear in spendable outputs since withdrawal was rejected", sparkTxHash)
}

// assertNoWithdrawalCreated verifies that no withdrawal transaction was created by checking
// the DB count. Used for rejection tests where the output was already spent (SpentFinalized)
// and wouldn't appear in spendable queries regardless of withdrawal status.
func assertNoWithdrawalCreated(t *testing.T, ctx context.Context, dbClient *ent.Client) {
	t.Helper()
	count, err := dbClient.L1WithdrawalTransaction.Query().Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, count, "No withdrawal should have been created")
}

func createTestTokenOutput(
	t *testing.T,
	ctx context.Context,
	dbClient *ent.Client,
	fixtures *entfixtures.Fixtures,
	sparkTxHash []byte,
	sparkTxVout int32,
	ownerPubKey keys.Public,
	revocationCommitment []byte,
	bondSats uint64,
	csvBlocks uint64,
) *ent.TokenOutput {
	tokenCreate := fixtures.CreateTokenCreate(btcnetwork.Regtest, nil, nil)
	revocationKeyshare := fixtures.CreateKeyshare()

	tokenTx, err := dbClient.TokenTransaction.Create().
		SetPartialTokenTransactionHash(fixtures.RandomBytes(32)).
		SetFinalizedTokenTransactionHash(sparkTxHash).
		SetStatus(schematype.TokenTransactionStatusFinalized).
		SetCreate(tokenCreate).
		Save(ctx)
	require.NoError(t, err)

	tokenAmount := make([]byte, 16)
	tokenAmount[15] = 100
	tokenOutput, err := dbClient.TokenOutput.Create().
		SetStatus(schematype.TokenOutputStatusCreatedFinalized).
		SetOwnerPublicKey(ownerPubKey).
		SetWithdrawBondSats(bondSats).
		SetWithdrawRelativeBlockLocktime(csvBlocks).
		SetWithdrawRevocationCommitment(revocationCommitment).
		SetTokenAmount(tokenAmount).
		SetCreatedTransactionOutputVout(sparkTxVout).
		SetCreatedTransactionFinalizedHash(sparkTxHash).
		SetNetwork(btcnetwork.Regtest).
		SetTokenIdentifier(tokenCreate.TokenIdentifier).
		SetRevocationKeyshare(revocationKeyshare).
		SetOutputCreatedTokenTransaction(tokenTx).
		SetTokenCreate(tokenCreate).
		Save(ctx)
	require.NoError(t, err)

	return tokenOutput
}

func createTestTokenOutputWithStatus(
	t *testing.T,
	ctx context.Context,
	dbClient *ent.Client,
	fixtures *entfixtures.Fixtures,
	sparkTxHash []byte,
	sparkTxVout int32,
	ownerPubKey keys.Public,
	revocationCommitment []byte,
	bondSats uint64,
	csvBlocks uint64,
	status schematype.TokenOutputStatus,
	spendingTx *ent.TokenTransaction,
) *ent.TokenOutput {
	tokenCreate := fixtures.CreateTokenCreate(btcnetwork.Regtest, nil, nil)
	revocationKeyshare := fixtures.CreateKeyshare()

	tokenTx, err := dbClient.TokenTransaction.Create().
		SetPartialTokenTransactionHash(fixtures.RandomBytes(32)).
		SetFinalizedTokenTransactionHash(sparkTxHash).
		SetStatus(schematype.TokenTransactionStatusFinalized).
		SetCreate(tokenCreate).
		Save(ctx)
	require.NoError(t, err)

	tokenAmount := make([]byte, 16)
	tokenAmount[15] = 100
	builder := dbClient.TokenOutput.Create().
		SetStatus(status).
		SetOwnerPublicKey(ownerPubKey).
		SetWithdrawBondSats(bondSats).
		SetWithdrawRelativeBlockLocktime(csvBlocks).
		SetWithdrawRevocationCommitment(revocationCommitment).
		SetTokenAmount(tokenAmount).
		SetCreatedTransactionOutputVout(sparkTxVout).
		SetCreatedTransactionFinalizedHash(sparkTxHash).
		SetNetwork(btcnetwork.Regtest).
		SetTokenIdentifier(tokenCreate.TokenIdentifier).
		SetRevocationKeyshare(revocationKeyshare).
		SetOutputCreatedTokenTransaction(tokenTx).
		SetTokenCreate(tokenCreate)

	if spendingTx != nil {
		builder.SetOutputSpentTokenTransaction(spendingTx)
	}

	tokenOutput, err := builder.Save(ctx)
	require.NoError(t, err)

	return tokenOutput
}

// API-level tests for HandleTokenWithdrawals

func TestHandleTokenWithdrawals_SavesWithdrawalTransaction(t *testing.T) {
	ctx, dbClient, fixtures, config, sePubKey := setupWithdrawalTestContext(t)

	ownerPrivKey := keys.GeneratePrivateKey()
	ownerPubKey := ownerPrivKey.Public()
	revocationPrivKey := keys.GeneratePrivateKey()
	revocationXOnly := revocationPrivKey.Public().SerializeXOnly()
	revocationCommitment := revocationPrivKey.Public().Serialize()

	sparkTxHash := fixtures.RandomBytes(32)
	sparkTxVout := int32(0)
	bondSats := uint64(10000)
	csvBlocks := uint64(1000)

	tokenOutput := createTestTokenOutput(t, ctx, dbClient, fixtures, sparkTxHash, sparkTxVout, ownerPubKey, revocationCommitment, bondSats, csvBlocks)
	require.NotNil(t, tokenOutput)

	expectedOutput, err := ConstructRevocationCsvTaprootOutput(revocationXOnly, ownerPubKey.SerializeXOnly(), csvBlocks)
	require.NoError(t, err)

	ownerSignature := make([]byte, 64)
	withdrawalScript := buildWithdrawalScript(t, sePubKey.Serialize(), ownerSignature, []withdrawalRecord{
		{bitcoinVout: 0, sparkTxHash: sparkTxHash, sparkTxVout: uint32(sparkTxVout)},
	})

	tx := wire.NewMsgTx(2)
	tx.AddTxOut(&wire.TxOut{
		Value:    int64(bondSats),
		PkScript: expectedOutput.ScriptPubKey,
	})
	tx.AddTxOut(&wire.TxOut{
		Value:    0,
		PkScript: withdrawalScript,
	})

	blockHash := chainhash.Hash{}
	for i := range blockHash {
		blockHash[i] = byte(i + 200)
	}
	blockHeight := uint64(100)

	err = HandleTokenWithdrawals(ctx, config, dbClient, []wire.MsgTx{*tx}, btcnetwork.Regtest, blockHeight, blockHash)
	require.NoError(t, err)

	assertOutputNotSpendable(t, ctx, config, ownerPubKey, sparkTxHash)

	// Verify withdrawal metadata was recorded correctly
	withdrawalTxs, err := dbClient.L1WithdrawalTransaction.Query().All(ctx)
	require.NoError(t, err)
	require.Len(t, withdrawalTxs, 1)
	assert.Equal(t, blockHeight, withdrawalTxs[0].ConfirmationHeight)
	assert.Equal(t, blockHash[:], withdrawalTxs[0].ConfirmationBlockHash)
	assert.Equal(t, ownerSignature, withdrawalTxs[0].OwnerSignature)
}

func TestHandleTokenWithdrawals_MultipleOutputsInOneTransaction(t *testing.T) {
	ctx, dbClient, fixtures, config, sePubKey := setupWithdrawalTestContext(t)

	ownerPrivKey := keys.GeneratePrivateKey()
	ownerPubKey := ownerPrivKey.Public()
	revocationPrivKey1 := keys.GeneratePrivateKey()
	revocationPrivKey2 := keys.GeneratePrivateKey()

	revocationXOnly1 := revocationPrivKey1.Public().SerializeXOnly()
	revocationXOnly2 := revocationPrivKey2.Public().SerializeXOnly()
	revocationCommitment1 := revocationPrivKey1.Public().Serialize()
	revocationCommitment2 := revocationPrivKey2.Public().Serialize()

	bondSats := uint64(10000)
	csvBlocks := uint64(1000)

	sparkTxHash1 := fixtures.RandomBytes(32)
	sparkTxHash2 := fixtures.RandomBytes(32)

	tokenOutput1 := createTestTokenOutput(t, ctx, dbClient, fixtures, sparkTxHash1, 0, ownerPubKey, revocationCommitment1, bondSats, csvBlocks)
	tokenOutput2 := createTestTokenOutput(t, ctx, dbClient, fixtures, sparkTxHash2, 0, ownerPubKey, revocationCommitment2, bondSats, csvBlocks)
	require.NotNil(t, tokenOutput1)
	require.NotNil(t, tokenOutput2)

	expectedOutput1, err := ConstructRevocationCsvTaprootOutput(revocationXOnly1, ownerPubKey.SerializeXOnly(), csvBlocks)
	require.NoError(t, err)
	expectedOutput2, err := ConstructRevocationCsvTaprootOutput(revocationXOnly2, ownerPubKey.SerializeXOnly(), csvBlocks)
	require.NoError(t, err)

	ownerSignature := make([]byte, 64)
	withdrawalScript := buildWithdrawalScript(t, sePubKey.Serialize(), ownerSignature, []withdrawalRecord{
		{bitcoinVout: 0, sparkTxHash: sparkTxHash1, sparkTxVout: 0},
		{bitcoinVout: 1, sparkTxHash: sparkTxHash2, sparkTxVout: 0},
	})

	tx := wire.NewMsgTx(2)
	tx.AddTxOut(&wire.TxOut{Value: int64(bondSats), PkScript: expectedOutput1.ScriptPubKey})
	tx.AddTxOut(&wire.TxOut{Value: int64(bondSats), PkScript: expectedOutput2.ScriptPubKey})
	tx.AddTxOut(&wire.TxOut{Value: 0, PkScript: withdrawalScript})

	blockHash := chainhash.Hash{}
	err = HandleTokenWithdrawals(ctx, config, dbClient, []wire.MsgTx{*tx}, btcnetwork.Regtest, 100, blockHash)
	require.NoError(t, err)

	assertOutputNotSpendable(t, ctx, config, ownerPubKey, sparkTxHash1)
	assertOutputNotSpendable(t, ctx, config, ownerPubKey, sparkTxHash2)
}

func TestHandleTokenWithdrawals_RejectsWrongSEPubKey(t *testing.T) {
	ctx, dbClient, fixtures, config, _ := setupWithdrawalTestContext(t)

	wrongSEPrivKey := keys.GeneratePrivateKey()
	wrongSEPubKey := wrongSEPrivKey.Public()

	ownerPrivKey := keys.GeneratePrivateKey()
	ownerPubKey := ownerPrivKey.Public()
	revocationPrivKey := keys.GeneratePrivateKey()
	revocationXOnly := revocationPrivKey.Public().SerializeXOnly()
	revocationCommitment := revocationPrivKey.Public().Serialize()

	sparkTxHash := fixtures.RandomBytes(32)
	bondSats := uint64(10000)
	csvBlocks := uint64(1000)

	tokenOutput := createTestTokenOutput(t, ctx, dbClient, fixtures, sparkTxHash, 0, ownerPubKey, revocationCommitment, bondSats, csvBlocks)
	require.NotNil(t, tokenOutput)

	expectedOutput, err := ConstructRevocationCsvTaprootOutput(revocationXOnly, ownerPubKey.SerializeXOnly(), csvBlocks)
	require.NoError(t, err)

	ownerSignature := make([]byte, 64)
	withdrawalScript := buildWithdrawalScript(t, wrongSEPubKey.Serialize(), ownerSignature, []withdrawalRecord{
		{bitcoinVout: 0, sparkTxHash: sparkTxHash, sparkTxVout: 0},
	})

	tx := wire.NewMsgTx(2)
	tx.AddTxOut(&wire.TxOut{Value: int64(bondSats), PkScript: expectedOutput.ScriptPubKey})
	tx.AddTxOut(&wire.TxOut{Value: 0, PkScript: withdrawalScript})

	blockHash := chainhash.Hash{}
	err = HandleTokenWithdrawals(ctx, config, dbClient, []wire.MsgTx{*tx}, btcnetwork.Regtest, 100, blockHash)
	require.NoError(t, err)

	// Verify the output is still spendable (withdrawal was rejected)
	assertOutputStillSpendable(t, ctx, config, ownerPubKey, sparkTxHash)
}

func TestHandleTokenWithdrawals_RejectsOutputNotFound(t *testing.T) {
	ctx, dbClient, fixtures, config, sePubKey := setupWithdrawalTestContext(t)
	_ = fixtures

	sparkTxHash := make([]byte, 32)
	for i := range sparkTxHash {
		sparkTxHash[i] = byte(i + 99)
	}

	ownerSignature := make([]byte, 64)
	withdrawalScript := buildWithdrawalScript(t, sePubKey.Serialize(), ownerSignature, []withdrawalRecord{
		{bitcoinVout: 0, sparkTxHash: sparkTxHash, sparkTxVout: 0},
	})

	tx := wire.NewMsgTx(2)
	tx.AddTxOut(&wire.TxOut{Value: 10000, PkScript: []byte{txscript.OP_1}})
	tx.AddTxOut(&wire.TxOut{Value: 0, PkScript: withdrawalScript})

	blockHash := chainhash.Hash{}
	err := HandleTokenWithdrawals(ctx, config, dbClient, []wire.MsgTx{*tx}, btcnetwork.Regtest, 100, blockHash)
	require.NoError(t, err)

	count, err := dbClient.L1WithdrawalTransaction.Query().Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

func TestHandleTokenWithdrawals_RejectsAlreadyWithdrawn(t *testing.T) {
	ctx, dbClient, fixtures, config, sePubKey := setupWithdrawalTestContext(t)

	ownerPrivKey := keys.GeneratePrivateKey()
	ownerPubKey := ownerPrivKey.Public()
	revocationPrivKey := keys.GeneratePrivateKey()
	revocationXOnly := revocationPrivKey.Public().SerializeXOnly()
	revocationCommitment := revocationPrivKey.Public().Serialize()

	sparkTxHash := fixtures.RandomBytes(32)
	bondSats := uint64(10000)
	csvBlocks := uint64(1000)

	tokenOutput := createTestTokenOutput(t, ctx, dbClient, fixtures, sparkTxHash, 0, ownerPubKey, revocationCommitment, bondSats, csvBlocks)
	require.NotNil(t, tokenOutput)

	seEntity, err := ent.GetEntityDkgKey(ctx, dbClient)
	require.NoError(t, err)
	previousBlockHash := make([]byte, 32)
	previousTxid := make([]byte, 32)
	for i := range previousBlockHash {
		previousBlockHash[i] = byte(i + 100)
		previousTxid[i] = byte(i + 50)
	}
	txid, err := schematype.NewTxIDFromBytes(previousTxid)
	require.NoError(t, err)
	withdrawalTx, err := dbClient.L1WithdrawalTransaction.Create().
		SetConfirmationTxid(txid).
		SetConfirmationBlockHash(previousBlockHash).
		SetConfirmationHeight(50).
		SetDetectedAt(time.Now()).
		SetOwnerSignature(make([]byte, 64)).
		SetSeEntity(seEntity).
		Save(ctx)
	require.NoError(t, err)
	_, err = dbClient.L1TokenOutputWithdrawal.Create().
		SetBitcoinVout(0).
		SetTokenOutput(tokenOutput).
		SetL1WithdrawalTransaction(withdrawalTx).
		Save(ctx)
	require.NoError(t, err)

	expectedOutput, err := ConstructRevocationCsvTaprootOutput(revocationXOnly, ownerPubKey.SerializeXOnly(), csvBlocks)
	require.NoError(t, err)

	ownerSignature := make([]byte, 64)
	withdrawalScript := buildWithdrawalScript(t, sePubKey.Serialize(), ownerSignature, []withdrawalRecord{
		{bitcoinVout: 0, sparkTxHash: sparkTxHash, sparkTxVout: 0},
	})

	tx := wire.NewMsgTx(2)
	tx.AddTxOut(&wire.TxOut{Value: int64(bondSats), PkScript: expectedOutput.ScriptPubKey})
	tx.AddTxOut(&wire.TxOut{Value: 0, PkScript: withdrawalScript})

	blockHash := chainhash.Hash{}
	err = HandleTokenWithdrawals(ctx, config, dbClient, []wire.MsgTx{*tx}, btcnetwork.Regtest, 100, blockHash)
	require.NoError(t, err)

	count, err := dbClient.L1WithdrawalTransaction.Query().Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestHandleTokenWithdrawals_LinksWithdrawalToTokenOutput(t *testing.T) {
	ctx, dbClient, fixtures, config, sePubKey := setupWithdrawalTestContext(t)

	ownerPrivKey := keys.GeneratePrivateKey()
	ownerPubKey := ownerPrivKey.Public()
	revocationPrivKey := keys.GeneratePrivateKey()
	revocationXOnly := revocationPrivKey.Public().SerializeXOnly()
	revocationCommitment := revocationPrivKey.Public().Serialize()

	sparkTxHash := fixtures.RandomBytes(32)
	bondSats := uint64(10000)
	csvBlocks := uint64(1000)

	tokenOutput := createTestTokenOutput(t, ctx, dbClient, fixtures, sparkTxHash, 0, ownerPubKey, revocationCommitment, bondSats, csvBlocks)
	require.NotNil(t, tokenOutput)

	expectedOutput, err := ConstructRevocationCsvTaprootOutput(revocationXOnly, ownerPubKey.SerializeXOnly(), csvBlocks)
	require.NoError(t, err)

	ownerSignature := make([]byte, 64)
	withdrawalScript := buildWithdrawalScript(t, sePubKey.Serialize(), ownerSignature, []withdrawalRecord{
		{bitcoinVout: 0, sparkTxHash: sparkTxHash, sparkTxVout: 0},
	})

	tx := wire.NewMsgTx(2)
	tx.AddTxOut(&wire.TxOut{Value: int64(bondSats), PkScript: expectedOutput.ScriptPubKey})
	tx.AddTxOut(&wire.TxOut{Value: 0, PkScript: withdrawalScript})

	blockHash := chainhash.Hash{}
	err = HandleTokenWithdrawals(ctx, config, dbClient, []wire.MsgTx{*tx}, btcnetwork.Regtest, 100, blockHash)
	require.NoError(t, err)

	outputWithdrawal, err := dbClient.L1TokenOutputWithdrawal.Query().
		Where(l1tokenoutputwithdrawal.HasTokenOutputWith(tokenoutput.ID(tokenOutput.ID))).
		Only(ctx)
	require.NoError(t, err)
	require.NotNil(t, outputWithdrawal)

	withdrawalTxResult, err := dbClient.L1WithdrawalTransaction.Query().
		Where(l1withdrawaltransaction.HasWithdrawalsWith(l1tokenoutputwithdrawal.ID(outputWithdrawal.ID))).
		Only(ctx)
	require.NoError(t, err)
	require.NotNil(t, withdrawalTxResult)
}

func TestHandleTokenWithdrawals_IgnoresNonBTKNTransactions(t *testing.T) {
	ctx, dbClient, fixtures, config, _ := setupWithdrawalTestContext(t)
	_ = fixtures

	script, err := txscript.NewScriptBuilder().
		AddOp(txscript.OP_RETURN).
		AddData([]byte("NOT_BTKN_DATA")).
		Script()
	require.NoError(t, err)

	tx := wire.NewMsgTx(2)
	tx.AddTxOut(&wire.TxOut{Value: 10000, PkScript: []byte{txscript.OP_1}})
	tx.AddTxOut(&wire.TxOut{Value: 0, PkScript: script})

	blockHash := chainhash.Hash{}
	err = HandleTokenWithdrawals(ctx, config, dbClient, []wire.MsgTx{*tx}, btcnetwork.Regtest, 100, blockHash)
	require.NoError(t, err)

	count, err := dbClient.L1WithdrawalTransaction.Query().Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

func TestHandleTokenWithdrawals_IgnoresNonOpReturnScripts(t *testing.T) {
	ctx, dbClient, fixtures, config, _ := setupWithdrawalTestContext(t)
	_ = fixtures

	script := []byte{txscript.OP_DUP, txscript.OP_HASH160, 0x14}
	script = append(script, make([]byte, 20)...)
	script = append(script, txscript.OP_EQUALVERIFY, txscript.OP_CHECKSIG)

	tx := wire.NewMsgTx(2)
	tx.AddTxOut(&wire.TxOut{Value: 10000, PkScript: script})

	blockHash := chainhash.Hash{}
	err := HandleTokenWithdrawals(ctx, config, dbClient, []wire.MsgTx{*tx}, btcnetwork.Regtest, 100, blockHash)
	require.NoError(t, err)

	count, err := dbClient.L1WithdrawalTransaction.Query().Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

func TestHandleTokenWithdrawals_RejectsInsufficientBond(t *testing.T) {
	ctx, dbClient, fixtures, config, sePubKey := setupWithdrawalTestContext(t)

	ownerPrivKey := keys.GeneratePrivateKey()
	ownerPubKey := ownerPrivKey.Public()
	revocationPrivKey := keys.GeneratePrivateKey()
	revocationXOnly := revocationPrivKey.Public().SerializeXOnly()
	revocationCommitment := revocationPrivKey.Public().Serialize()

	sparkTxHash := fixtures.RandomBytes(32)
	bondSats := uint64(10000)
	csvBlocks := uint64(1000)

	tokenOutput := createTestTokenOutput(t, ctx, dbClient, fixtures, sparkTxHash, 0, ownerPubKey, revocationCommitment, bondSats, csvBlocks)
	require.NotNil(t, tokenOutput)

	expectedOutput, err := ConstructRevocationCsvTaprootOutput(revocationXOnly, ownerPubKey.SerializeXOnly(), csvBlocks)
	require.NoError(t, err)

	ownerSignature := make([]byte, 64)
	withdrawalScript := buildWithdrawalScript(t, sePubKey.Serialize(), ownerSignature, []withdrawalRecord{
		{bitcoinVout: 0, sparkTxHash: sparkTxHash, sparkTxVout: 0},
	})

	tx := wire.NewMsgTx(2)
	tx.AddTxOut(&wire.TxOut{Value: 5000, PkScript: expectedOutput.ScriptPubKey}) // Insufficient bond
	tx.AddTxOut(&wire.TxOut{Value: 0, PkScript: withdrawalScript})

	blockHash := chainhash.Hash{}
	err = HandleTokenWithdrawals(ctx, config, dbClient, []wire.MsgTx{*tx}, btcnetwork.Regtest, 100, blockHash)
	require.NoError(t, err)

	count, err := dbClient.L1WithdrawalTransaction.Query().Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, count)

	assertOutputStillSpendable(t, ctx, config, ownerPubKey, sparkTxHash)
}

func TestHandleTokenWithdrawals_RejectsScriptMismatch(t *testing.T) {
	ctx, dbClient, fixtures, config, sePubKey := setupWithdrawalTestContext(t)

	ownerPrivKey := keys.GeneratePrivateKey()
	ownerPubKey := ownerPrivKey.Public()
	revocationPrivKey := keys.GeneratePrivateKey()
	revocationCommitment := revocationPrivKey.Public().Serialize()

	sparkTxHash := fixtures.RandomBytes(32)
	bondSats := uint64(10000)
	csvBlocks := uint64(1000)

	tokenOutput := createTestTokenOutput(t, ctx, dbClient, fixtures, sparkTxHash, 0, ownerPubKey, revocationCommitment, bondSats, csvBlocks)
	require.NotNil(t, tokenOutput)

	wrongScript := []byte{txscript.OP_1, txscript.OP_DATA_32}
	wrongScript = append(wrongScript, make([]byte, 32)...)

	ownerSignature := make([]byte, 64)
	withdrawalScript := buildWithdrawalScript(t, sePubKey.Serialize(), ownerSignature, []withdrawalRecord{
		{bitcoinVout: 0, sparkTxHash: sparkTxHash, sparkTxVout: 0},
	})

	tx := wire.NewMsgTx(2)
	tx.AddTxOut(&wire.TxOut{Value: int64(bondSats), PkScript: wrongScript})
	tx.AddTxOut(&wire.TxOut{Value: 0, PkScript: withdrawalScript})

	blockHash := chainhash.Hash{}
	err := HandleTokenWithdrawals(ctx, config, dbClient, []wire.MsgTx{*tx}, btcnetwork.Regtest, 100, blockHash)
	require.NoError(t, err)

	assertOutputStillSpendable(t, ctx, config, ownerPubKey, sparkTxHash)
}

func TestHandleTokenWithdrawals_RejectsActiveSpendingTransaction(t *testing.T) {
	ctx, dbClient, fixtures, config, sePubKey := setupWithdrawalTestContext(t)

	ownerPrivKey := keys.GeneratePrivateKey()
	ownerPubKey := ownerPrivKey.Public()
	revocationPrivKey := keys.GeneratePrivateKey()
	revocationXOnly := revocationPrivKey.Public().SerializeXOnly()
	revocationCommitment := revocationPrivKey.Public().Serialize()

	sparkTxHash := fixtures.RandomBytes(32)
	bondSats := uint64(10000)
	csvBlocks := uint64(1000)

	tokenCreate := fixtures.CreateTokenCreate(btcnetwork.Regtest, nil, nil)

	activeSpendingTx, err := dbClient.TokenTransaction.Create().
		SetPartialTokenTransactionHash(fixtures.RandomBytes(32)).
		SetFinalizedTokenTransactionHash(fixtures.RandomBytes(32)).
		SetStatus(schematype.TokenTransactionStatusStarted).
		SetVersion(3).
		SetClientCreatedTimestamp(time.Now()).
		SetValidityDurationSeconds(3600).
		SetCreate(tokenCreate).
		Save(ctx)
	require.NoError(t, err)

	tokenOutput := createTestTokenOutputWithStatus(t, ctx, dbClient, fixtures, sparkTxHash, 0, ownerPubKey, revocationCommitment, bondSats, csvBlocks, schematype.TokenOutputStatusSpentStarted, activeSpendingTx)
	require.NotNil(t, tokenOutput)

	expectedOutput, err := ConstructRevocationCsvTaprootOutput(revocationXOnly, ownerPubKey.SerializeXOnly(), csvBlocks)
	require.NoError(t, err)

	ownerSignature := make([]byte, 64)
	withdrawalScript := buildWithdrawalScript(t, sePubKey.Serialize(), ownerSignature, []withdrawalRecord{
		{bitcoinVout: 0, sparkTxHash: sparkTxHash, sparkTxVout: 0},
	})

	tx := wire.NewMsgTx(2)
	tx.AddTxOut(&wire.TxOut{Value: int64(bondSats), PkScript: expectedOutput.ScriptPubKey})
	tx.AddTxOut(&wire.TxOut{Value: 0, PkScript: withdrawalScript})

	blockHash := chainhash.Hash{}
	err = HandleTokenWithdrawals(ctx, config, dbClient, []wire.MsgTx{*tx}, btcnetwork.Regtest, 100, blockHash)
	require.NoError(t, err)

	assertOutputStillSpendable(t, ctx, config, ownerPubKey, sparkTxHash)
}

func TestHandleTokenWithdrawals_AllowsExpiredSpendingTransaction(t *testing.T) {
	ctx, dbClient, fixtures, config, sePubKey := setupWithdrawalTestContext(t)

	ownerPrivKey := keys.GeneratePrivateKey()
	ownerPubKey := ownerPrivKey.Public()
	revocationPrivKey := keys.GeneratePrivateKey()
	revocationXOnly := revocationPrivKey.Public().SerializeXOnly()
	revocationCommitment := revocationPrivKey.Public().Serialize()

	sparkTxHash := fixtures.RandomBytes(32)
	bondSats := uint64(10000)
	csvBlocks := uint64(1000)

	tokenCreate := fixtures.CreateTokenCreate(btcnetwork.Regtest, nil, nil)

	expiredSpendingTx, err := dbClient.TokenTransaction.Create().
		SetPartialTokenTransactionHash(fixtures.RandomBytes(32)).
		SetFinalizedTokenTransactionHash(fixtures.RandomBytes(32)).
		SetStatus(schematype.TokenTransactionStatusStarted).
		SetVersion(3).
		SetClientCreatedTimestamp(time.Now().Add(-2 * time.Hour)).
		SetValidityDurationSeconds(3600).
		SetCreate(tokenCreate).
		Save(ctx)
	require.NoError(t, err)

	tokenOutput := createTestTokenOutputWithStatus(t, ctx, dbClient, fixtures, sparkTxHash, 0, ownerPubKey, revocationCommitment, bondSats, csvBlocks, schematype.TokenOutputStatusSpentStarted, expiredSpendingTx)
	require.NotNil(t, tokenOutput)

	expectedOutput, err := ConstructRevocationCsvTaprootOutput(revocationXOnly, ownerPubKey.SerializeXOnly(), csvBlocks)
	require.NoError(t, err)

	ownerSignature := make([]byte, 64)
	withdrawalScript := buildWithdrawalScript(t, sePubKey.Serialize(), ownerSignature, []withdrawalRecord{
		{bitcoinVout: 0, sparkTxHash: sparkTxHash, sparkTxVout: 0},
	})

	tx := wire.NewMsgTx(2)
	tx.AddTxOut(&wire.TxOut{Value: int64(bondSats), PkScript: expectedOutput.ScriptPubKey})
	tx.AddTxOut(&wire.TxOut{Value: 0, PkScript: withdrawalScript})

	blockHash := chainhash.Hash{}
	err = HandleTokenWithdrawals(ctx, config, dbClient, []wire.MsgTx{*tx}, btcnetwork.Regtest, 100, blockHash)
	require.NoError(t, err)

	assertOutputNotSpendable(t, ctx, config, ownerPubKey, sparkTxHash)
}

func TestHandleTokenWithdrawals_RejectsDuplicateInSameBlock(t *testing.T) {
	ctx, dbClient, fixtures, config, sePubKey := setupWithdrawalTestContext(t)

	ownerPrivKey := keys.GeneratePrivateKey()
	ownerPubKey := ownerPrivKey.Public()
	revocationPrivKey := keys.GeneratePrivateKey()
	revocationXOnly := revocationPrivKey.Public().SerializeXOnly()
	revocationCommitment := revocationPrivKey.Public().Serialize()

	sparkTxHash := fixtures.RandomBytes(32)
	bondSats := uint64(10000)
	csvBlocks := uint64(1000)

	tokenOutput := createTestTokenOutput(t, ctx, dbClient, fixtures, sparkTxHash, 0, ownerPubKey, revocationCommitment, bondSats, csvBlocks)
	require.NotNil(t, tokenOutput)

	expectedOutput, err := ConstructRevocationCsvTaprootOutput(revocationXOnly, ownerPubKey.SerializeXOnly(), csvBlocks)
	require.NoError(t, err)

	ownerSignature := make([]byte, 64)

	withdrawalScript1 := buildWithdrawalScript(t, sePubKey.Serialize(), ownerSignature, []withdrawalRecord{
		{bitcoinVout: 0, sparkTxHash: sparkTxHash, sparkTxVout: 0},
	})
	tx1 := wire.NewMsgTx(2)
	tx1.AddTxOut(&wire.TxOut{Value: int64(bondSats), PkScript: expectedOutput.ScriptPubKey})
	tx1.AddTxOut(&wire.TxOut{Value: 0, PkScript: withdrawalScript1})

	withdrawalScript2 := buildWithdrawalScript(t, sePubKey.Serialize(), ownerSignature, []withdrawalRecord{
		{bitcoinVout: 0, sparkTxHash: sparkTxHash, sparkTxVout: 0},
	})
	tx2 := wire.NewMsgTx(2)
	tx2.AddTxOut(&wire.TxOut{Value: int64(bondSats), PkScript: expectedOutput.ScriptPubKey})
	tx2.AddTxOut(&wire.TxOut{Value: 0, PkScript: withdrawalScript2})

	blockHash := chainhash.Hash{}
	err = HandleTokenWithdrawals(ctx, config, dbClient, []wire.MsgTx{*tx1, *tx2}, btcnetwork.Regtest, 100, blockHash)
	require.NoError(t, err)

	count, err := dbClient.L1WithdrawalTransaction.Query().Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	outputWithdrawals, err := dbClient.L1TokenOutputWithdrawal.Query().All(ctx)
	require.NoError(t, err)
	assert.Len(t, outputWithdrawals, 1)
}

func TestHandleTokenWithdrawals_RejectsVoutOutOfRange(t *testing.T) {
	ctx, dbClient, fixtures, config, sePubKey := setupWithdrawalTestContext(t)

	ownerPrivKey := keys.GeneratePrivateKey()
	ownerPubKey := ownerPrivKey.Public()
	revocationPrivKey := keys.GeneratePrivateKey()
	revocationCommitment := revocationPrivKey.Public().Serialize()

	sparkTxHash := fixtures.RandomBytes(32)
	bondSats := uint64(10000)
	csvBlocks := uint64(1000)

	tokenOutput := createTestTokenOutput(t, ctx, dbClient, fixtures, sparkTxHash, 0, ownerPubKey, revocationCommitment, bondSats, csvBlocks)
	require.NotNil(t, tokenOutput)

	ownerSignature := make([]byte, 64)
	withdrawalScript := buildWithdrawalScript(t, sePubKey.Serialize(), ownerSignature, []withdrawalRecord{
		{bitcoinVout: 5, sparkTxHash: sparkTxHash, sparkTxVout: 0}, // vout 5 doesn't exist
	})

	tx := wire.NewMsgTx(2)
	tx.AddTxOut(&wire.TxOut{Value: int64(bondSats), PkScript: []byte{txscript.OP_1}})
	tx.AddTxOut(&wire.TxOut{Value: 0, PkScript: withdrawalScript})

	blockHash := chainhash.Hash{}
	err := HandleTokenWithdrawals(ctx, config, dbClient, []wire.MsgTx{*tx}, btcnetwork.Regtest, 100, blockHash)
	require.NoError(t, err)

	assertOutputStillSpendable(t, ctx, config, ownerPubKey, sparkTxHash)
}

func TestHandleTokenWithdrawals_RejectsFinalizedSpendingTransaction(t *testing.T) {
	ctx, dbClient, fixtures, config, sePubKey := setupWithdrawalTestContext(t)

	ownerPrivKey := keys.GeneratePrivateKey()
	ownerPubKey := ownerPrivKey.Public()
	revocationPrivKey := keys.GeneratePrivateKey()
	revocationXOnly := revocationPrivKey.Public().SerializeXOnly()
	revocationCommitment := revocationPrivKey.Public().Serialize()

	sparkTxHash := fixtures.RandomBytes(32)
	bondSats := uint64(10000)
	csvBlocks := uint64(1000)

	tokenCreate := fixtures.CreateTokenCreate(btcnetwork.Regtest, nil, nil)

	finalizedSpendingTx, err := dbClient.TokenTransaction.Create().
		SetPartialTokenTransactionHash(fixtures.RandomBytes(32)).
		SetFinalizedTokenTransactionHash(fixtures.RandomBytes(32)).
		SetStatus(schematype.TokenTransactionStatusFinalized).
		SetVersion(3).
		SetClientCreatedTimestamp(time.Now()).
		SetValidityDurationSeconds(3600).
		SetCreate(tokenCreate).
		Save(ctx)
	require.NoError(t, err)

	tokenOutput := createTestTokenOutputWithStatus(t, ctx, dbClient, fixtures, sparkTxHash, 0, ownerPubKey, revocationCommitment, bondSats, csvBlocks, schematype.TokenOutputStatusSpentFinalized, finalizedSpendingTx)
	require.NotNil(t, tokenOutput)

	expectedOutput, err := ConstructRevocationCsvTaprootOutput(revocationXOnly, ownerPubKey.SerializeXOnly(), csvBlocks)
	require.NoError(t, err)

	ownerSignature := make([]byte, 64)
	withdrawalScript := buildWithdrawalScript(t, sePubKey.Serialize(), ownerSignature, []withdrawalRecord{
		{bitcoinVout: 0, sparkTxHash: sparkTxHash, sparkTxVout: 0},
	})

	tx := wire.NewMsgTx(2)
	tx.AddTxOut(&wire.TxOut{Value: int64(bondSats), PkScript: expectedOutput.ScriptPubKey})
	tx.AddTxOut(&wire.TxOut{Value: 0, PkScript: withdrawalScript})

	blockHash := chainhash.Hash{}
	err = HandleTokenWithdrawals(ctx, config, dbClient, []wire.MsgTx{*tx}, btcnetwork.Regtest, 100, blockHash)
	require.NoError(t, err)

	// Output is SpentFinalized so it won't appear in spendable queries regardless.
	// Verify no withdrawal was created for this already-spent output.
	assertNoWithdrawalCreated(t, ctx, dbClient)
}
