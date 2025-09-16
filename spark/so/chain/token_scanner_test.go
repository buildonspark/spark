package chain

import (
	"context"
	"encoding/binary"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/lightsparkdev/spark/common/keys"

	"github.com/btcsuite/btcd/wire"
	"github.com/stretchr/testify/require"

	"github.com/btcsuite/btcd/txscript"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/assert"
)

var (
	seededRand   = rand.NewChaCha8([32]byte{})
	maxSupply    = []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	issuerPubKey = keys.MustGeneratePrivateKeyFromRand(seededRand).Public()
)

// setupIsolatedTest creates a fresh database context for a test or subtest with proper cleanup.
// This ensures complete isolation between test runs without manual cleanup.
// It also sets up a basic EntityDkgKey required for token operations.
func setupIsolatedTest(t *testing.T) (context.Context, *ent.Tx) {
	ctx, _ := db.NewTestSQLiteContext(t)
	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	// Create required EntityDkgKey for token operations
	secretShare := keys.MustGeneratePrivateKeyFromRand(seededRand)
	entityDkgPublicKey := keys.MustGeneratePrivateKeyFromRand(seededRand).Public()
	signingKeyshare, err := dbTx.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(secretShare.Serialize()).
		SetPublicShares(map[string]keys.Public{}).
		SetPublicKey(entityDkgPublicKey).
		SetMinSigners(1).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)

	_, err = dbTx.EntityDkgKey.Create().
		SetSigningKeyshare(signingKeyshare).
		Save(ctx)
	require.NoError(t, err)

	return ctx, dbTx
}

func TestParseTokenAnnouncement(t *testing.T) {
	tests := []struct {
		name   string
		script []byte
		want   *common.TokenMetadata
	}{
		{name: "empty script", script: []byte{}, want: nil},
		{name: "nil", script: nil, want: nil},
		{name: "not OP_RETURN", script: []byte{txscript.OP_DUP, txscript.OP_HASH160}, want: nil},
		{name: "OP_RETURN but too short", script: []byte{txscript.OP_RETURN}, want: nil},
		{name: "not announcement", script: directPush([]byte("NOTOK")), want: nil},
		{name: "OP_RETURN with invalid kind", script: directPush(append([]byte(announcementPrefix), 9, 9)), want: nil},
		{
			name:   "valid token announcement",
			script: directPush(createValidTokenData()),
			want:   createExpectedTokenMetadata(),
		},
		{
			name: "valid UTF-8 with unicode characters",
			script: func() []byte {
				nameBytes := []byte("🚀BTC")
				tickerBytes := []byte("🚀")
				return pushData1(slices.Concat(
					[]byte(announcementPrefix),
					creationAnnouncementKind[:],
					issuerPubKey.Serialize(),
					[]byte{byte(len(nameBytes))},
					nameBytes,
					[]byte{byte(len(tickerBytes))},
					tickerBytes,
					[]byte{8},
					maxSupply,
					[]byte{0},
				))
			}(),
			want: &common.TokenMetadata{
				IssuerPublicKey:         issuerPubKey,
				TokenName:               "🚀BTC",
				TokenTicker:             "🚀",
				Decimals:                8,
				MaxSupply:               maxSupply,
				IsFreezable:             false,
				CreationEntityPublicKey: common.L1CreationEntityPublicKey,
				Network:                 common.Testnet,
			},
		},
		{
			name: "invalid push operation",
			script: []byte{
				txscript.OP_RETURN,
				0xff, // Invalid push operation
			},
			want: nil,
		},
		{
			name:   "valid direct push (OP_1 to OP_75)",
			script: directPush(createValidTokenData()),
			want:   createExpectedTokenMetadata(),
		},
		{
			name:   "valid OP_PUSHDATA1",
			script: pushData1(createValidTokenData()),
			want:   createExpectedTokenMetadata(),
		},
		{
			name:   "valid OP_PUSHDATA2",
			script: pushData2(createValidTokenData()),
			want:   createExpectedTokenMetadata(),
		},
		{
			name:   "valid OP_PUSHDATA4",
			script: pushData4(createValidTokenData()),
			want:   createExpectedTokenMetadata(),
		},
		{
			name:   "incomplete OP_PUSHDATA1",
			script: []byte{txscript.OP_RETURN, txscript.OP_PUSHDATA1}, // OP_PUSHDATA1 without length byte
			want:   nil,
		},
		{
			name:   "incomplete OP_PUSHDATA2",
			script: []byte{txscript.OP_RETURN, txscript.OP_PUSHDATA2, 0x01}, // Only one byte of length
			want:   nil,
		},
		{
			name:   "incomplete OP_PUSHDATA4",
			script: []byte{txscript.OP_RETURN, txscript.OP_PUSHDATA4, 0x01, 0x02, 0x03}, // Only one byte of length
			want:   nil,
		},
		{
			name: "insufficient data after length",
			script: []byte{
				txscript.OP_RETURN,
				txscript.OP_PUSHDATA1,
				10,      // Claim 10 bytes follow
				1, 2, 3, // Only provide 3
			},
			want: nil,
		},
		{
			name:   "extra data after push",
			script: append(directPush(createValidTokenData()), 0), // Extra unwanted byte
			want:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseTokenAnnouncement(tt.script, common.Testnet)
			require.NoError(t, err)

			if tt.want == nil {
				require.Nil(t, result)
				return
			}
			require.NotNil(t, result)

			got, err := result.ToTokenMetadata()
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseTokenAnnouncement_Errors(t *testing.T) {
	tests := []struct {
		name    string
		script  []byte
		wantErr string
	}{
		{
			name: "extra data after push",
			script: pushData1(slices.Concat(
				[]byte(announcementPrefix),
				creationAnnouncementKind[:],
				issuerPubKey.Serialize(),
				[]byte{4}, []byte("ABCD"),
				[]byte{4}, []byte("TICK"),
				[]byte{8},
				maxSupply,
				[]byte{1},
				[]byte{1}, // An extra, invalid byte
			)),
			wantErr: "unexpected data after token announcement",
		},
		{
			name: "name too short",
			script: directPush(slices.Concat(
				[]byte(announcementPrefix),
				creationAnnouncementKind[:],
				issuerPubKey.Serialize(),
				[]byte{2}, []byte("AB"), // Too short
				[]byte{4}, []byte("TICK"),
				[]byte{8},
				maxSupply,
				[]byte{1},
			)),
			wantErr: "invalid length: expected between 3 and 20, got 2",
		},
		{
			name: "name too long",
			script: pushData1(slices.Concat(
				[]byte(announcementPrefix),
				creationAnnouncementKind[:],
				issuerPubKey.Serialize(),
				[]byte{25}, []byte("1234567890123456789012345"), // Too long
				[]byte{4}, []byte("TICK"),
				[]byte{8},
				maxSupply,
				[]byte{1},
			)),
			wantErr: "invalid length: expected between 3 and 20, got 25",
		},
		{
			name: "ticker too short",
			script: directPush(slices.Concat(
				[]byte(announcementPrefix),
				creationAnnouncementKind[:],
				issuerPubKey.Serialize(),
				[]byte{9}, []byte("TestToken"),
				[]byte{2}, []byte("XY"), // Too short
				[]byte{8},
				maxSupply,
				[]byte{1},
			)),
			wantErr: "invalid length: expected between 3 and 6, got 2",
		},
		{
			name: "ticker too long",
			script: pushData1(slices.Concat(
				[]byte(announcementPrefix),
				creationAnnouncementKind[:],
				issuerPubKey.Serialize(),
				[]byte{9}, []byte("TestToken"),
				[]byte{7}, []byte("1234567"), // Too long
				[]byte{8},
				maxSupply,
				[]byte{1},
			)),
			wantErr: "invalid length: expected between 3 and 6, got 7",
		},
		{
			name: "invalid max supply length",
			script: pushData1(slices.Concat(
				[]byte(announcementPrefix),
				creationAnnouncementKind[:],
				issuerPubKey.Serialize(),
				[]byte{9}, []byte("TestToken"),
				[]byte{4}, []byte("TICK"),
				[]byte{8},
				maxSupply[:15], // 15 bytes for max supply
			)),
			wantErr: "invalid max supply: insufficient data",
		},
		{
			name: "missing is_freezable",
			script: directPush(slices.Concat(
				[]byte(announcementPrefix),
				creationAnnouncementKind[:],
				issuerPubKey.Serialize(),
				[]byte{9}, []byte("TestToken"),
				[]byte{4}, []byte("TICK"),
				[]byte{8},
				maxSupply,
			)),
			wantErr: "invalid is_freezable: insufficient data",
		},
		{
			name: "invalid UTF-8 in name",
			script: directPush(slices.Concat(
				[]byte(announcementPrefix),
				creationAnnouncementKind[:],
				issuerPubKey.Serialize(),
				[]byte{3}, []byte{0xFF, 0x80, 0x80}, // Invalid UTF-8
				[]byte{4}, []byte("TICK"),
				[]byte{8},
				maxSupply,
				[]byte{1},
			)),
			wantErr: "invalid name: invalid UTF-8",
		},
		{
			name: "invalid UTF-8 in ticker",
			script: directPush(slices.Concat(
				[]byte(announcementPrefix),
				creationAnnouncementKind[:],
				issuerPubKey.Serialize(),
				[]byte{9}, []byte("TestToken"),
				[]byte{4}, []byte{0xC0, 0x80, 0xFF, 0x80}, // Invalid UTF-8
				[]byte{8},
				maxSupply,
				[]byte{1},
			)),
			wantErr: "invalid ticker: invalid UTF-8",
		},
		{
			name: "valid non-normalized UFT-8 in name",
			script: directPush(slices.Concat(
				[]byte(announcementPrefix),
				creationAnnouncementKind[:],
				issuerPubKey.Serialize(),
				[]byte{3}, []byte("e\u0301"), // Non-normalized name
				[]byte{4}, []byte("TICK"),
				[]byte{8},
				maxSupply,
				[]byte{1},
			)),
			wantErr: "invalid name: not NFC-normalized",
		},
		{
			name: "valid non-normalized UTF-8 in ticker",
			script: directPush(slices.Concat(
				[]byte(announcementPrefix),
				creationAnnouncementKind[:],
				issuerPubKey.Serialize(),
				[]byte{9}, []byte("TestToken"),
				[]byte{3}, []byte("e\u0301"), // Non-normalized ticker
				[]byte{8},
				maxSupply,
				[]byte{1},
			)),
			wantErr: "invalid ticker: not NFC-normalized",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseTokenAnnouncement(tt.script, common.Testnet)
			require.ErrorContains(t, err, tt.wantErr)
			assert.Nil(t, result)
		})
	}
}

func directPush(data []byte) []byte {
	script := append([]byte{txscript.OP_RETURN}, byte(len(data))) // Direct push (length between 1-75)
	return append(script, data...)
}

func pushData1(data []byte) []byte {
	script := []byte{
		txscript.OP_RETURN,
		txscript.OP_PUSHDATA1,
		byte(len(data)), // Length byte for OP_PUSHDATA1
	}
	return append(script, data...)
}

func pushData2(data []byte) []byte {
	script := []byte{txscript.OP_RETURN, txscript.OP_PUSHDATA2}
	script = binary.LittleEndian.AppendUint16(script, uint16(len(data)))
	return append(script, data...)
}

func pushData4(data []byte) []byte {
	script := []byte{txscript.OP_RETURN, txscript.OP_PUSHDATA4}
	script = binary.LittleEndian.AppendUint32(script, uint32(len(data)))
	return append(script, data...)
}

func createValidTokenData() []byte {
	return slices.Concat(
		[]byte(announcementPrefix),
		creationAnnouncementKind[:],
		issuerPubKey.Serialize(),
		[]byte{9}, []byte("TestToken"),
		[]byte{4}, []byte("TICK"),
		[]byte{8},
		maxSupply,
		[]byte{1},
	)
}

func createExpectedTokenMetadata() *common.TokenMetadata {
	return &common.TokenMetadata{
		IssuerPublicKey:         issuerPubKey,
		TokenName:               "TestToken",
		TokenTicker:             "TICK",
		Decimals:                8,
		MaxSupply:               maxSupply,
		IsFreezable:             true,
		CreationEntityPublicKey: common.L1CreationEntityPublicKey,
		Network:                 common.Testnet,
	}
}

// Helper function to verify entity counts in the database
func verifyEntityCounts(t *testing.T, ctx context.Context, dbTx *ent.Tx, expectedL1Count, expectedSparkCount int, messagePrefix string) {
	t.Helper()

	l1TokenCount, err := dbTx.L1TokenCreate.Query().Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, expectedL1Count, l1TokenCount, "%s: Should have exactly %d L1TokenCreate entity/entities", messagePrefix, expectedL1Count)

	sparkTokenCount, err := dbTx.TokenCreate.Query().Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, expectedSparkCount, sparkTokenCount, "%s: Should have exactly %d TokenCreate entity/entities", messagePrefix, expectedSparkCount)
}

// Helper function to create a transaction with token announcement
func createTokenTransaction(tokenData []byte, lockTime uint32) wire.MsgTx {
	txOut := &wire.TxOut{
		Value:    0,
		PkScript: directPush(tokenData),
	}
	return wire.MsgTx{
		Version:  2,
		TxIn:     []*wire.TxIn{},
		TxOut:    []*wire.TxOut{txOut},
		LockTime: lockTime,
	}
}

// Helper function to create alternative token data with same issuer but different metadata
func createAlternativeTokenData() []byte {
	return slices.Concat(
		[]byte(announcementPrefix),
		creationAnnouncementKind[:],
		issuerPubKey.Serialize(),      // Same issuer public key
		[]byte{8}, []byte("DiffName"), // Different name
		[]byte{4}, []byte("DIFF"), // Different ticker
		[]byte{6}, // Different decimals
		maxSupply,
		[]byte{1}, // Different freezable setting
	)
}

// Helper function to process token announcements and verify success
func processTokenAnnouncements(t *testing.T, ctx context.Context, config *so.Config, dbTx *ent.Tx, transactions []wire.MsgTx, messagePrefix string) {
	t.Helper()

	err := handleTokenAnnouncements(ctx, config, dbTx, transactions, common.Testnet)
	require.NoError(t, err, "%s should succeed", messagePrefix)
}

// Helper function to verify token differences
func verifyTokenDifferences(t *testing.T, ctx context.Context, dbTx *ent.Tx) {
	t.Helper()

	l1Tokens, err := dbTx.L1TokenCreate.Query().All(ctx)
	require.NoError(t, err)
	require.Len(t, l1Tokens, 2)

	// Both should have the same issuer public key
	assert.Equal(t, l1Tokens[0].IssuerPublicKey, l1Tokens[1].IssuerPublicKey, "Both tokens should have the same issuer public key")

	// But different token identifiers and metadata
	assert.NotEqual(t, l1Tokens[0].TokenIdentifier, l1Tokens[1].TokenIdentifier, "Tokens should have different token identifiers")
	assert.NotEqual(t, l1Tokens[0].TokenName, l1Tokens[1].TokenName, "Tokens should have different names")
	assert.NotEqual(t, l1Tokens[0].TokenTicker, l1Tokens[1].TokenTicker, "Tokens should have different tickers")
}

func TestHandleTokenAnnouncements_DuplicateConstraints(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)

	config := sparktesting.TestConfig(t)

	// Get database transaction from context
	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	// Create test data
	tokenData := createValidTokenData()
	txOut := &wire.TxOut{
		Value:    0,
		PkScript: directPush(tokenData),
	}
	tx := wire.MsgTx{
		Version:  2,
		TxIn:     []*wire.TxIn{},
		TxOut:    []*wire.TxOut{txOut},
		LockTime: 0,
	}

	// Create required EntityDkgKey for Spark token creation (reused across tests)
	secretShare := keys.MustGeneratePrivateKeyFromRand(seededRand)
	entityDkgPublicKey := keys.MustGeneratePrivateKeyFromRand(seededRand).Public()
	signingKeyshare, err := dbTx.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(secretShare.Serialize()).
		SetPublicShares(map[string]keys.Public{}).
		SetPublicKey(entityDkgPublicKey).
		SetMinSigners(1).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)

	_, err = dbTx.EntityDkgKey.Create().
		SetSigningKeyshare(signingKeyshare).
		Save(ctx)
	require.NoError(t, err)

	// Helper function to create a clean subtest context - now uses the reusable helper
	setupSubtest := func(t *testing.T) (context.Context, *ent.Tx) {
		return setupIsolatedTest(t)
	}

	t.Run("L1 and spark token creation", func(t *testing.T) {
		subtestCtx, subtestTx := setupSubtest(t)

		// Test 1: First call should succeed and create L1TokenCreate and TokenCreate entities
		config.Token.DisableSparkTokenCreationForL1TokenAnnouncements = false

		processTokenAnnouncements(t, subtestCtx, config, subtestTx, []wire.MsgTx{tx}, "First call to handleTokenAnnouncements")
		verifyEntityCounts(t, subtestCtx, subtestTx, 1, 1, "After first call")

		// Test 2: Second call with same issuer public key should not error and not create duplicate
		processTokenAnnouncements(t, subtestCtx, config, subtestTx, []wire.MsgTx{tx}, "Second call to handleTokenAnnouncements")
		verifyEntityCounts(t, subtestCtx, subtestTx, 1, 1, "After second call")
	})

	t.Run("duplicate transactions in same block with spark token creation disabled", func(t *testing.T) {
		subtestCtx, subtestTx := setupSubtest(t)

		config.Token.DisableSparkTokenCreationForL1TokenAnnouncements = true

		// Create two identical transactions with the same token announcement
		tx1 := createTokenTransaction(tokenData, 0)
		tx2 := createTokenTransaction(tokenData, 1) // Different lock time to make it a different transaction

		// Process both transactions in the same block
		processTokenAnnouncements(t, subtestCtx, config, subtestTx, []wire.MsgTx{tx1, tx2}, "Processing duplicate transactions in same block")
		verifyEntityCounts(t, subtestCtx, subtestTx, 1, 0, "After processing duplicate transactions")
	})

	t.Run("duplicate transactions in same block with spark token creation enabled", func(t *testing.T) {
		subtestCtx, subtestTx := setupSubtest(t)

		config.Token.DisableSparkTokenCreationForL1TokenAnnouncements = false

		// Create two identical transactions with the same token announcement
		tx1 := createTokenTransaction(tokenData, 0)
		tx2 := createTokenTransaction(tokenData, 1) // Different lock time to make it a different transaction

		// Process both transactions in the same block
		processTokenAnnouncements(t, subtestCtx, config, subtestTx, []wire.MsgTx{tx1, tx2}, "Processing duplicate transactions in same block")
		verifyEntityCounts(t, subtestCtx, subtestTx, 1, 1, "After processing duplicate transactions")
	})

	t.Run("duplicate transactions across different blocks", func(t *testing.T) {
		subtestCtx, subtestTx := setupSubtest(t)

		config.Token.DisableSparkTokenCreationForL1TokenAnnouncements = false

		// Create transactions with token announcements for different blocks
		txBlock1 := createTokenTransaction(tokenData, 0)
		txBlock2 := createTokenTransaction(tokenData, 100) // Different lock time to make it a different transaction

		// Process first block
		processTokenAnnouncements(t, subtestCtx, config, subtestTx, []wire.MsgTx{txBlock1}, "Processing first block")
		verifyEntityCounts(t, subtestCtx, subtestTx, 1, 1, "After first block")

		// Process second block with duplicate announcement
		processTokenAnnouncements(t, subtestCtx, config, subtestTx, []wire.MsgTx{txBlock2}, "Processing second block with duplicate")
		verifyEntityCounts(t, subtestCtx, subtestTx, 1, 1, "After second block")
	})

	t.Run("same issuer different tokens in different blocks", func(t *testing.T) {
		subtestCtx, subtestTx := setupSubtest(t)

		config.Token.DisableSparkTokenCreationForL1TokenAnnouncements = false

		// Create transactions with different token announcements from same issuer
		tokenData1 := createValidTokenData()
		tokenData2 := createAlternativeTokenData()
		tx1 := createTokenTransaction(tokenData1, 0)
		tx2 := createTokenTransaction(tokenData2, 1)

		// Process first token
		processTokenAnnouncements(t, subtestCtx, config, subtestTx, []wire.MsgTx{tx1}, "Processing first token")
		verifyEntityCounts(t, subtestCtx, subtestTx, 1, 1, "After first token")

		// Process second token with same issuer but different token metadata
		processTokenAnnouncements(t, subtestCtx, config, subtestTx, []wire.MsgTx{tx2}, "Processing second token")
		verifyEntityCounts(t, subtestCtx, subtestTx, 2, 1, "After second token (business rule: one Spark token per issuer)")

		// Verify the tokens have different identifiers
		verifyTokenDifferences(t, subtestCtx, subtestTx)
	})

	t.Run("same issuer different tokens in same block", func(t *testing.T) {
		subtestCtx, subtestTx := setupSubtest(t)

		config.Token.DisableSparkTokenCreationForL1TokenAnnouncements = false

		// Create transactions with different token announcements from same issuer
		tokenData1 := createValidTokenData()
		tokenData2 := createAlternativeTokenData()
		tx1 := createTokenTransaction(tokenData1, 0)
		tx2 := createTokenTransaction(tokenData2, 1)

		// Process both tokens in the same block
		processTokenAnnouncements(t, subtestCtx, config, subtestTx, []wire.MsgTx{tx1, tx2}, "Processing both different tokens in same block")
		verifyEntityCounts(t, subtestCtx, subtestTx, 2, 1, "After processing both tokens (business rule: one Spark token per issuer)")

		// Verify the tokens have different identifiers
		verifyTokenDifferences(t, subtestCtx, subtestTx)
	})
}
