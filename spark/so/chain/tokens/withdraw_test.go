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
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/l1tokenoutputwithdrawal"
	"github.com/lightsparkdev/spark/so/ent/l1withdrawaltransaction"
	"github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/tokenoutput"
	"github.com/lightsparkdev/spark/so/entfixtures"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseTokenWithdrawal_ValidWithdrawal(t *testing.T) {
	// Generate test keys
	sePrivKey := keys.GeneratePrivateKey()
	sePubKey := sePrivKey.Public()

	// Build a valid withdrawal script
	sparkTxHash := make([]byte, 32)
	for i := range sparkTxHash {
		sparkTxHash[i] = byte(i)
	}
	ownerSignature := make([]byte, 64)
	for i := range ownerSignature {
		ownerSignature[i] = byte(i + 100)
	}

	script := buildWithdrawalScript(t, sePubKey.Serialize(), ownerSignature, []withdrawalRecord{
		{bitcoinVout: 0, sparkTxHash: sparkTxHash, sparkTxVout: 1},
	})

	parsedTx, parsedOutputs, err := parseTokenWithdrawal(script)
	require.NoError(t, err)
	require.NotNil(t, parsedTx)
	require.Len(t, parsedOutputs, 1)

	assert.Equal(t, sePubKey.Serialize(), parsedTx.seEntityPubKey.Serialize())
	assert.Equal(t, ownerSignature, parsedTx.entity.OwnerSignature)
	assert.Equal(t, uint16(0), parsedOutputs[0].withdrawal.BitcoinVout)
	assert.Equal(t, sparkTxHash, parsedOutputs[0].sparkTxHash)
	assert.Equal(t, uint32(1), parsedOutputs[0].sparkTxVout)
}

func TestParseTokenWithdrawal_MultipleOutputs(t *testing.T) {
	sePrivKey := keys.GeneratePrivateKey()
	sePubKey := sePrivKey.Public()

	sparkTxHash1 := make([]byte, 32)
	sparkTxHash2 := make([]byte, 32)
	for i := range sparkTxHash1 {
		sparkTxHash1[i] = byte(i)
		sparkTxHash2[i] = byte(i + 50)
	}
	ownerSignature := make([]byte, 64)

	script := buildWithdrawalScript(t, sePubKey.Serialize(), ownerSignature, []withdrawalRecord{
		{bitcoinVout: 0, sparkTxHash: sparkTxHash1, sparkTxVout: 0},
		{bitcoinVout: 1, sparkTxHash: sparkTxHash2, sparkTxVout: 2},
	})

	parsedTx, parsedOutputs, err := parseTokenWithdrawal(script)
	require.NoError(t, err)
	require.NotNil(t, parsedTx)
	require.Len(t, parsedOutputs, 2)

	assert.Equal(t, uint16(0), parsedOutputs[0].withdrawal.BitcoinVout)
	assert.Equal(t, sparkTxHash1, parsedOutputs[0].sparkTxHash)
	assert.Equal(t, uint32(0), parsedOutputs[0].sparkTxVout)

	assert.Equal(t, uint16(1), parsedOutputs[1].withdrawal.BitcoinVout)
	assert.Equal(t, sparkTxHash2, parsedOutputs[1].sparkTxHash)
	assert.Equal(t, uint32(2), parsedOutputs[1].sparkTxVout)
}

func TestParseTokenWithdrawal_NotOpReturn(t *testing.T) {
	// P2PKH script - not an OP_RETURN
	script := []byte{txscript.OP_DUP, txscript.OP_HASH160, 0x14}
	script = append(script, make([]byte, 20)...)
	script = append(script, txscript.OP_EQUALVERIFY, txscript.OP_CHECKSIG)

	parsedTx, parsedOutputs, err := parseTokenWithdrawal(script)
	require.NoError(t, err)
	assert.Nil(t, parsedTx)
	assert.Nil(t, parsedOutputs)
}

func TestParseTokenWithdrawal_NotBTKN(t *testing.T) {
	// OP_RETURN with different prefix
	script, err := txscript.NewScriptBuilder().
		AddOp(txscript.OP_RETURN).
		AddData([]byte("NOT_BTKN_DATA")).
		Script()
	require.NoError(t, err)

	parsedTx, parsedOutputs, err := parseTokenWithdrawal(script)
	require.NoError(t, err)
	assert.Nil(t, parsedTx)
	assert.Nil(t, parsedOutputs)
}

func TestParseTokenWithdrawal_InvalidSEPubKey(t *testing.T) {
	// Build script with invalid SE public key (wrong length)
	invalidPubKey := make([]byte, 30) // Should be 33 bytes
	ownerSignature := make([]byte, 64)
	sparkTxHash := make([]byte, 32)

	var buf bytes.Buffer
	buf.WriteString(btknWithdrawal.Prefix)
	buf.Write(btknWithdrawal.Kind[:])
	buf.Write(invalidPubKey)
	buf.Write(ownerSignature)
	buf.WriteByte(1) // count
	require.NoError(t, binary.Write(&buf, binary.BigEndian, uint16(0)))
	buf.Write(sparkTxHash)
	require.NoError(t, binary.Write(&buf, binary.BigEndian, uint32(0)))

	script, err := txscript.NewScriptBuilder().
		AddOp(txscript.OP_RETURN).
		AddData(buf.Bytes()).
		Script()
	require.NoError(t, err)

	_, _, err = parseTokenWithdrawal(script)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid SE public key")
}

func TestParseTokenWithdrawal_ZeroWithdrawals(t *testing.T) {
	sePrivKey := keys.GeneratePrivateKey()
	sePubKey := sePrivKey.Public()
	ownerSignature := make([]byte, 64)

	var buf bytes.Buffer
	buf.WriteString(btknWithdrawal.Prefix)
	buf.Write(btknWithdrawal.Kind[:])
	buf.Write(sePubKey.Serialize())
	buf.Write(ownerSignature)
	buf.WriteByte(0) // zero withdrawals

	script, err := txscript.NewScriptBuilder().
		AddOp(txscript.OP_RETURN).
		AddData(buf.Bytes()).
		Script()
	require.NoError(t, err)

	_, _, err = parseTokenWithdrawal(script)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be greater than zero")
}

func TestParseTokenWithdrawal_TrailingData(t *testing.T) {
	sePrivKey := keys.GeneratePrivateKey()
	sePubKey := sePrivKey.Public()
	ownerSignature := make([]byte, 64)
	sparkTxHash := make([]byte, 32)

	var buf bytes.Buffer
	buf.WriteString(btknWithdrawal.Prefix)
	buf.Write(btknWithdrawal.Kind[:])
	buf.Write(sePubKey.Serialize())
	buf.Write(ownerSignature)
	buf.WriteByte(1) // count
	require.NoError(t, binary.Write(&buf, binary.BigEndian, uint16(0)))
	buf.Write(sparkTxHash)
	require.NoError(t, binary.Write(&buf, binary.BigEndian, uint32(0)))
	buf.WriteString("extra_data") // trailing data

	script, err := txscript.NewScriptBuilder().
		AddOp(txscript.OP_RETURN).
		AddData(buf.Bytes()).
		Script()
	require.NoError(t, err)

	_, _, err = parseTokenWithdrawal(script)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected trailing data")
}

func TestValidateOutputWithdrawable_Success(t *testing.T) {
	sparkTxHash := make([]byte, 32)
	key := sparkTxHashVoutKey(sparkTxHash, 0)

	tokenOutput := &ent.TokenOutput{
		ID:     uuid.New(),
		Status: schematype.TokenOutputStatusCreatedFinalized,
	}

	tokenOutputMap := map[string]*ent.TokenOutput{
		key: tokenOutput,
	}
	withdrawnInBlock := make(map[string]struct{})

	output := parsedOutputWithdrawal{
		sparkTxHash: sparkTxHash,
		sparkTxVout: 0,
	}

	result, err := validateOutputWithdrawable(output, withdrawnInBlock, tokenOutputMap)
	require.NoError(t, err)
	assert.Equal(t, tokenOutput, result)
}

func TestValidateOutputWithdrawable_AlreadyWithdrawnInBlock(t *testing.T) {
	sparkTxHash := make([]byte, 32)
	key := sparkTxHashVoutKey(sparkTxHash, 0)

	tokenOutput := &ent.TokenOutput{
		ID:     uuid.New(),
		Status: schematype.TokenOutputStatusCreatedFinalized,
	}

	tokenOutputMap := map[string]*ent.TokenOutput{
		key: tokenOutput,
	}
	withdrawnInBlock := map[string]struct{}{
		key: {},
	}

	output := parsedOutputWithdrawal{
		sparkTxHash: sparkTxHash,
		sparkTxVout: 0,
	}

	_, err := validateOutputWithdrawable(output, withdrawnInBlock, tokenOutputMap)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrOutputAlreadyWithdrawnInBlock)
}

func TestValidateOutputWithdrawable_OutputNotFound(t *testing.T) {
	sparkTxHash := make([]byte, 32)

	tokenOutputMap := make(map[string]*ent.TokenOutput)
	withdrawnInBlock := make(map[string]struct{})

	output := parsedOutputWithdrawal{
		sparkTxHash: sparkTxHash,
		sparkTxVout: 0,
	}

	_, err := validateOutputWithdrawable(output, withdrawnInBlock, tokenOutputMap)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrOutputNotFound)
}

func TestValidateOutputWithdrawable_AlreadyWithdrawnOnChain(t *testing.T) {
	sparkTxHash := make([]byte, 32)
	key := sparkTxHashVoutKey(sparkTxHash, 0)

	blockHash := make([]byte, 32)
	tokenOutput := &ent.TokenOutput{
		ID:                         uuid.New(),
		Status:                     schematype.TokenOutputStatusCreatedFinalized,
		ConfirmedWithdrawBlockHash: blockHash,
	}

	tokenOutputMap := map[string]*ent.TokenOutput{
		key: tokenOutput,
	}
	withdrawnInBlock := make(map[string]struct{})

	output := parsedOutputWithdrawal{
		sparkTxHash: sparkTxHash,
		sparkTxVout: 0,
	}

	_, err := validateOutputWithdrawable(output, withdrawnInBlock, tokenOutputMap)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrOutputAlreadyWithdrawnOnChain)
}

func TestValidateOutputWithdrawable_ActiveSpendingTransaction(t *testing.T) {
	sparkTxHash := make([]byte, 32)
	key := sparkTxHashVoutKey(sparkTxHash, 0)

	// Create an active (non-expired) spending transaction in Started status
	spendingTx := &ent.TokenTransaction{
		ID:                      uuid.New(),
		Status:                  schematype.TokenTransactionStatusStarted,
		Version:                 3,
		ClientCreatedTimestamp:  time.Now(), // Not expired
		ValidityDurationSeconds: 3600,       // 1 hour validity
	}

	tokenOutput := &ent.TokenOutput{
		ID:     uuid.New(),
		Status: schematype.TokenOutputStatusSpentSigned, // Non-spendable status
		Edges: ent.TokenOutputEdges{
			OutputSpentTokenTransaction: spendingTx,
		},
	}

	tokenOutputMap := map[string]*ent.TokenOutput{
		key: tokenOutput,
	}
	withdrawnInBlock := make(map[string]struct{})

	output := parsedOutputWithdrawal{
		sparkTxHash: sparkTxHash,
		sparkTxVout: 0,
	}

	_, err := validateOutputWithdrawable(output, withdrawnInBlock, tokenOutputMap)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrOutputNotWithdrawable)
}

func TestValidateOutputWithdrawable_FinalizedSpendingTransaction(t *testing.T) {
	sparkTxHash := make([]byte, 32)
	key := sparkTxHashVoutKey(sparkTxHash, 0)

	// Create a finalized spending transaction (not expired, but already finalized)
	spendingTx := &ent.TokenTransaction{
		ID:                      uuid.New(),
		Status:                  schematype.TokenTransactionStatusFinalized,
		Version:                 3,
		ClientCreatedTimestamp:  time.Now(), // Not expired
		ValidityDurationSeconds: 3600,       // 1 hour validity
	}

	tokenOutput := &ent.TokenOutput{
		ID:     uuid.New(),
		Status: schematype.TokenOutputStatusSpentSigned,
		Edges: ent.TokenOutputEdges{
			OutputSpentTokenTransaction: spendingTx,
		},
	}

	tokenOutputMap := map[string]*ent.TokenOutput{
		key: tokenOutput,
	}
	withdrawnInBlock := make(map[string]struct{})

	output := parsedOutputWithdrawal{
		sparkTxHash: sparkTxHash,
		sparkTxVout: 0,
	}

	// Finalized transaction should block withdrawal
	_, err := validateOutputWithdrawable(output, withdrawnInBlock, tokenOutputMap)
	require.ErrorIs(t, err, ErrOutputNotWithdrawable)
	assert.Contains(t, err.Error(), "finalized")
}

func TestValidateOutputWithdrawable_ExpiredSpendingTransaction(t *testing.T) {
	sparkTxHash := make([]byte, 32)
	key := sparkTxHashVoutKey(sparkTxHash, 0)

	// Create an expired spending transaction
	spendingTx := &ent.TokenTransaction{
		ID:                      uuid.New(),
		Status:                  schematype.TokenTransactionStatusStarted,
		Version:                 3,
		ClientCreatedTimestamp:  time.Now().Add(-2 * time.Hour), // Created 2 hours ago
		ValidityDurationSeconds: 3600,                           // 1 hour validity - expired
	}

	tokenOutput := &ent.TokenOutput{
		ID:     uuid.New(),
		Status: schematype.TokenOutputStatusSpentSigned, // Non-spendable status
		Edges: ent.TokenOutputEdges{
			OutputSpentTokenTransaction: spendingTx,
		},
	}

	tokenOutputMap := map[string]*ent.TokenOutput{
		key: tokenOutput,
	}
	withdrawnInBlock := make(map[string]struct{})

	output := parsedOutputWithdrawal{
		sparkTxHash: sparkTxHash,
		sparkTxVout: 0,
	}

	// Expired transaction should allow withdrawal
	result, err := validateOutputWithdrawable(output, withdrawnInBlock, tokenOutputMap)
	require.NoError(t, err)
	assert.Equal(t, tokenOutput, result)
}

func TestValidateWithdrawalTxOutput_Success(t *testing.T) {
	// Generate test keys
	revocationPrivKey := keys.GeneratePrivateKey()
	ownerPrivKey := keys.GeneratePrivateKey()

	revocationXOnly := revocationPrivKey.Public().SerializeXOnly()
	ownerXOnly := ownerPrivKey.Public().SerializeXOnly()
	csvBlocks := uint64(1000)
	bondSats := uint64(10000)

	expectedOutput, err := ConstructRevocationCsvTaprootOutput(revocationXOnly, ownerXOnly, csvBlocks)
	require.NoError(t, err)

	tx := wire.NewMsgTx(2)
	tx.AddTxOut(&wire.TxOut{
		Value:    int64(bondSats),
		PkScript: expectedOutput.ScriptPubKey,
	})

	withdrawal := &ent.L1TokenOutputWithdrawal{
		BitcoinVout: 0,
	}

	ownerPubKey := ownerPrivKey.Public()
	revocationCommitment := revocationPrivKey.Public().Serialize()

	tokenOutput := &ent.TokenOutput{
		WithdrawBondSats:              bondSats,
		WithdrawRevocationCommitment:  revocationCommitment,
		OwnerPublicKey:                ownerPubKey,
		WithdrawRelativeBlockLocktime: csvBlocks,
	}

	err = validateWithdrawalTxOutput(tx, withdrawal, tokenOutput)
	assert.NoError(t, err)
}

func TestValidateWithdrawalTxOutput_InsufficientBond(t *testing.T) {
	revocationPrivKey := keys.GeneratePrivateKey()
	ownerPrivKey := keys.GeneratePrivateKey()

	revocationXOnly := revocationPrivKey.Public().SerializeXOnly()
	ownerXOnly := ownerPrivKey.Public().SerializeXOnly()
	csvBlocks := uint64(1000)
	bondSats := uint64(10000)

	expectedOutput, err := ConstructRevocationCsvTaprootOutput(revocationXOnly, ownerXOnly, csvBlocks)
	require.NoError(t, err)

	tx := wire.NewMsgTx(2)
	tx.AddTxOut(&wire.TxOut{
		Value:    5000, // Less than required bond
		PkScript: expectedOutput.ScriptPubKey,
	})

	withdrawal := &ent.L1TokenOutputWithdrawal{
		BitcoinVout: 0,
	}

	ownerPubKey := ownerPrivKey.Public()
	revocationCommitment := revocationPrivKey.Public().Serialize()

	tokenOutput := &ent.TokenOutput{
		WithdrawBondSats:              bondSats,
		WithdrawRevocationCommitment:  revocationCommitment,
		OwnerPublicKey:                ownerPubKey,
		WithdrawRelativeBlockLocktime: csvBlocks,
	}

	err = validateWithdrawalTxOutput(tx, withdrawal, tokenOutput)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInsufficientBond)
}

func TestValidateWithdrawalTxOutput_ScriptMismatch(t *testing.T) {
	revocationPrivKey := keys.GeneratePrivateKey()
	ownerPrivKey := keys.GeneratePrivateKey()

	csvBlocks := uint64(1000)
	bondSats := uint64(10000)

	// Use wrong script
	wrongScript := []byte{txscript.OP_1, txscript.OP_DATA_32}
	wrongScript = append(wrongScript, make([]byte, 32)...)

	tx := wire.NewMsgTx(2)
	tx.AddTxOut(&wire.TxOut{
		Value:    int64(bondSats),
		PkScript: wrongScript,
	})

	withdrawal := &ent.L1TokenOutputWithdrawal{
		BitcoinVout: 0,
	}

	ownerPubKey := ownerPrivKey.Public()
	revocationCommitment := revocationPrivKey.Public().Serialize()

	tokenOutput := &ent.TokenOutput{
		WithdrawBondSats:              bondSats,
		WithdrawRevocationCommitment:  revocationCommitment,
		OwnerPublicKey:                ownerPubKey,
		WithdrawRelativeBlockLocktime: csvBlocks,
	}

	err := validateWithdrawalTxOutput(tx, withdrawal, tokenOutput)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrScriptMismatch)
}

func TestValidateWithdrawalTxOutput_VoutOutOfRange(t *testing.T) {
	tx := wire.NewMsgTx(2)
	tx.AddTxOut(&wire.TxOut{
		Value:    10000,
		PkScript: []byte{},
	})

	withdrawal := &ent.L1TokenOutputWithdrawal{
		BitcoinVout: 5, // Out of range
	}

	tokenOutput := &ent.TokenOutput{}

	err := validateWithdrawalTxOutput(tx, withdrawal, tokenOutput)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrVoutOutOfRange)
}

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

// Database tests for HandleTokenWithdrawals

// setupWithdrawalTestContext creates all required entities for withdrawal testing.
// Returns context, db client, fixtures, config, SE public key.
func setupWithdrawalTestContext(t *testing.T) (ctx context.Context, dbClient *ent.Client, fixtures *entfixtures.Fixtures, config *so.Config, sePubKey keys.Public) {
	ctx, _ = db.NewTestSQLiteContext(t)
	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	fixtures = entfixtures.New(t, ctx, dbClient)

	// Create SE entity signing keyshare
	sePrivKey := keys.GeneratePrivateKey()
	sePubKey = sePrivKey.Public()

	signingKeyshare, err := dbClient.SigningKeyshare.Create().
		SetStatus(schematype.KeyshareStatusAvailable).
		SetSecretShare(sePrivKey).
		SetPublicShares(map[string]keys.Public{}).
		SetPublicKey(sePubKey).
		SetMinSigners(1).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)

	_, err = dbClient.EntityDkgKey.Create().
		SetSigningKeyshare(signingKeyshare).
		Save(ctx)
	require.NoError(t, err)

	config = &so.Config{}
	return ctx, dbClient, fixtures, config, sePubKey
}

// createTestTokenOutput creates a TokenOutput with all required relationships for withdrawal testing.
// Uses entfixtures for token metadata but customizes withdrawal-specific fields
// (WithdrawRevocationCommitment must be a valid 33-byte compressed public key for P2TR script construction).
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
	// Use fixtures for token create and keyshare
	tokenCreate := fixtures.CreateTokenCreate(btcnetwork.Regtest, nil, nil)
	revocationKeyshare := fixtures.CreateKeyshare()

	// Create token transaction linked to tokenCreate so it bypasses balance validation hook
	tokenTx, err := dbClient.TokenTransaction.Create().
		SetPartialTokenTransactionHash(fixtures.RandomBytes(32)).
		SetFinalizedTokenTransactionHash(sparkTxHash).
		SetStatus(schematype.TokenTransactionStatusFinalized).
		SetCreate(tokenCreate).
		Save(ctx)
	require.NoError(t, err)

	// Create token output with withdrawal-specific fields
	// Note: We can't use fixtures.CreateOutputForTransaction because it sets
	// WithdrawRevocationCommitment to random bytes, but withdrawals need a valid
	// 33-byte compressed public key for P2TR script construction.
	tokenAmount := make([]byte, 16)
	tokenAmount[15] = 100 // 100 tokens
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

func TestHandleTokenWithdrawals_SavesWithdrawalTransaction(t *testing.T) {
	ctx, dbClient, fixtures, config, sePubKey := setupWithdrawalTestContext(t)

	// Create test keys for the withdrawal
	ownerPrivKey := keys.GeneratePrivateKey()
	ownerPubKey := ownerPrivKey.Public()
	revocationPrivKey := keys.GeneratePrivateKey()
	revocationXOnly := revocationPrivKey.Public().SerializeXOnly()
	revocationCommitment := revocationPrivKey.Public().Serialize()

	// Create withdrawal parameters
	sparkTxHash := fixtures.RandomBytes(32)
	sparkTxVout := int32(0)
	bondSats := uint64(10000)
	csvBlocks := uint64(1000)

	// Create token output in database
	tokenOutput := createTestTokenOutput(t, ctx, dbClient, fixtures, sparkTxHash, sparkTxVout, ownerPubKey, revocationCommitment, bondSats, csvBlocks)
	require.NotNil(t, tokenOutput)

	// Build expected P2TR output
	expectedOutput, err := ConstructRevocationCsvTaprootOutput(revocationXOnly, ownerPubKey.SerializeXOnly(), csvBlocks)
	require.NoError(t, err)

	// Build withdrawal transaction
	ownerSignature := make([]byte, 64)
	withdrawalScript := buildWithdrawalScript(t, sePubKey.Serialize(), ownerSignature, []withdrawalRecord{
		{bitcoinVout: 0, sparkTxHash: sparkTxHash, sparkTxVout: uint32(sparkTxVout)},
	})

	tx := wire.NewMsgTx(2)
	// Add the P2TR output that holds the bond
	tx.AddTxOut(&wire.TxOut{
		Value:    int64(bondSats),
		PkScript: expectedOutput.ScriptPubKey,
	})
	// Add the OP_RETURN announcement
	tx.AddTxOut(&wire.TxOut{
		Value:    0,
		PkScript: withdrawalScript,
	})

	// Call HandleTokenWithdrawals
	blockHash := chainhash.Hash{}
	for i := range blockHash {
		blockHash[i] = byte(i + 200)
	}
	blockHeight := uint64(100)

	err = HandleTokenWithdrawals(ctx, config, dbClient, []wire.MsgTx{*tx}, btcnetwork.Regtest, blockHeight, blockHash)
	require.NoError(t, err)

	// Verify L1WithdrawalTransaction was created
	withdrawalTxs, err := dbClient.L1WithdrawalTransaction.Query().All(ctx)
	require.NoError(t, err)
	require.Len(t, withdrawalTxs, 1)
	assert.Equal(t, blockHeight, withdrawalTxs[0].ConfirmationHeight)
	assert.Equal(t, blockHash[:], withdrawalTxs[0].ConfirmationBlockHash)
	assert.Equal(t, ownerSignature, withdrawalTxs[0].OwnerSignature)

	// Verify L1TokenOutputWithdrawal was created
	outputWithdrawals, err := dbClient.L1TokenOutputWithdrawal.Query().All(ctx)
	require.NoError(t, err)
	require.Len(t, outputWithdrawals, 1)
	assert.Equal(t, uint16(0), outputWithdrawals[0].BitcoinVout)

	// Verify TokenOutput.ConfirmedWithdrawBlockHash was set
	updatedOutput, err := dbClient.TokenOutput.Get(ctx, tokenOutput.ID)
	require.NoError(t, err)
	assert.Equal(t, blockHash[:], updatedOutput.ConfirmedWithdrawBlockHash)
}

func TestHandleTokenWithdrawals_MultipleOutputsInOneTransaction(t *testing.T) {
	ctx, dbClient, fixtures, config, sePubKey := setupWithdrawalTestContext(t)

	// Create test keys
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

	// Create two unique spark tx hashes
	sparkTxHash1 := fixtures.RandomBytes(32)
	sparkTxHash2 := fixtures.RandomBytes(32)

	// Create two token outputs
	tokenOutput1 := createTestTokenOutput(t, ctx, dbClient, fixtures, sparkTxHash1, 0, ownerPubKey, revocationCommitment1, bondSats, csvBlocks)
	tokenOutput2 := createTestTokenOutput(t, ctx, dbClient, fixtures, sparkTxHash2, 0, ownerPubKey, revocationCommitment2, bondSats, csvBlocks)
	require.NotNil(t, tokenOutput1)
	require.NotNil(t, tokenOutput2)

	// Build expected P2TR outputs
	expectedOutput1, err := ConstructRevocationCsvTaprootOutput(revocationXOnly1, ownerPubKey.SerializeXOnly(), csvBlocks)
	require.NoError(t, err)
	expectedOutput2, err := ConstructRevocationCsvTaprootOutput(revocationXOnly2, ownerPubKey.SerializeXOnly(), csvBlocks)
	require.NoError(t, err)

	// Build withdrawal transaction with two outputs
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

	// Verify both outputs were withdrawn
	outputWithdrawals, err := dbClient.L1TokenOutputWithdrawal.Query().All(ctx)
	require.NoError(t, err)
	assert.Len(t, outputWithdrawals, 2)

	// Verify both TokenOutputs have ConfirmedWithdrawBlockHash set
	updatedOutput1, err := dbClient.TokenOutput.Get(ctx, tokenOutput1.ID)
	require.NoError(t, err)
	assert.NotNil(t, updatedOutput1.ConfirmedWithdrawBlockHash)

	updatedOutput2, err := dbClient.TokenOutput.Get(ctx, tokenOutput2.ID)
	require.NoError(t, err)
	assert.NotNil(t, updatedOutput2.ConfirmedWithdrawBlockHash)
}

func TestHandleTokenWithdrawals_RejectsWrongSEPubKey(t *testing.T) {
	ctx, dbClient, fixtures, config, _ := setupWithdrawalTestContext(t)

	// Use a different SE public key than what's in the database
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
	require.NoError(t, err) // Should not error, just skip the withdrawal

	// Verify no withdrawal was created
	count, err := dbClient.L1WithdrawalTransaction.Query().Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, count)

	// Verify TokenOutput was NOT marked as withdrawn
	updatedOutput, err := dbClient.TokenOutput.Get(ctx, tokenOutput.ID)
	require.NoError(t, err)
	assert.Nil(t, updatedOutput.ConfirmedWithdrawBlockHash)
}

func TestHandleTokenWithdrawals_RejectsOutputNotFound(t *testing.T) {
	ctx, dbClient, fixtures, config, sePubKey := setupWithdrawalTestContext(t)
	_ = fixtures // not used in this test

	// Create withdrawal for a non-existent token output
	sparkTxHash := make([]byte, 32)
	for i := range sparkTxHash {
		sparkTxHash[i] = byte(i + 99) // Different hash than any created output
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
	require.NoError(t, err) // Should not error, just skip the invalid output

	// Verify no withdrawal was created
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

	// Mark the output as already withdrawn
	previousBlockHash := make([]byte, 32)
	for i := range previousBlockHash {
		previousBlockHash[i] = byte(i + 100)
	}
	_, err := dbClient.TokenOutput.UpdateOneID(tokenOutput.ID).
		SetConfirmedWithdrawBlockHash(previousBlockHash).
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

	// Verify no new withdrawal was created
	count, err := dbClient.L1WithdrawalTransaction.Query().Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, count)
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

	// Verify the edge from L1TokenOutputWithdrawal to TokenOutput exists
	outputWithdrawal, err := dbClient.L1TokenOutputWithdrawal.Query().
		Where(l1tokenoutputwithdrawal.HasTokenOutputWith(tokenoutput.ID(tokenOutput.ID))).
		Only(ctx)
	require.NoError(t, err)
	require.NotNil(t, outputWithdrawal)

	// Verify the edge from L1TokenOutputWithdrawal to L1WithdrawalTransaction exists
	withdrawalTx, err := dbClient.L1WithdrawalTransaction.Query().
		Where(l1withdrawaltransaction.HasWithdrawalsWith(l1tokenoutputwithdrawal.ID(outputWithdrawal.ID))).
		Only(ctx)
	require.NoError(t, err)
	require.NotNil(t, withdrawalTx)
}
