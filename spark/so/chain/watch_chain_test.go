package chain

import (
	"bytes"
	"math/rand/v2"
	"testing"

	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	sparktesting "github.com/lightsparkdev/spark/testing"

	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/rpcclient"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent/schema/schematype"
	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProcessTransactions(t *testing.T) {
	// Create test network params
	params := &chaincfg.TestNet3Params

	tests := []struct {
		name           string
		txs            []wire.MsgTx
		expectedAddrs  int
		expectedTxids  int
		expectedError  bool
		checkAddresses func(t *testing.T, addresses []string, utxoMap map[string][]AddressDepositUtxo)
	}{
		{
			name:          "empty transactions",
			txs:           []wire.MsgTx{},
			expectedAddrs: 0,
			expectedTxids: 0,
			expectedError: false,
			checkAddresses: func(t *testing.T, addresses []string, utxoMap map[string][]AddressDepositUtxo) {
				assert.Empty(t, addresses)
				assert.Empty(t, utxoMap)
			},
		},
		{
			name: "single transaction with one output",
			txs: func() []wire.MsgTx {
				tx := wire.MsgTx{}
				// Create a simple P2PKH output script (OP_DUP OP_HASH160 <pubkeyhash> OP_EQUALVERIFY OP_CHECKSIG)
				script := []byte{
					txscript.OP_DUP,
					txscript.OP_HASH160,
					0x14, // 20 bytes
					0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09,
					0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10, 0x11, 0x12, 0x9,
					txscript.OP_EQUALVERIFY,
					txscript.OP_CHECKSIG,
				}
				tx.AddTxOut(wire.NewTxOut(1000, script))
				return []wire.MsgTx{tx}
			}(),
			expectedAddrs: 1,
			expectedTxids: 1,
			expectedError: false,
			checkAddresses: func(t *testing.T, addresses []string, utxoMap map[string][]AddressDepositUtxo) {
				assert.Len(t, addresses, 1)
				assert.Len(t, utxoMap, 1)
				utxos, exists := utxoMap[addresses[0]]
				assert.True(t, exists)
				assert.EqualValues(t, 1000, utxos[0].amount)
				assert.Zero(t, utxos[0].idx)
			},
		},
		{
			name: "multiple transactions with multiple outputs",
			txs: func() []wire.MsgTx {
				tx1 := wire.MsgTx{}
				tx2 := wire.MsgTx{}

				// Create two different P2PKH output scripts
				script1 := []byte{
					txscript.OP_DUP,
					txscript.OP_HASH160,
					0x14, // 20 bytes
					0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09,
					0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10, 0x11, 0x12, 0x9,
					txscript.OP_EQUALVERIFY,
					txscript.OP_CHECKSIG,
				}
				script2 := []byte{
					txscript.OP_DUP,
					txscript.OP_HASH160,
					0x14, // 20 bytes
					0x9, 0x12, 0x11, 0x10, 0x0f, 0x0e, 0x0d, 0x0c, 0x0b, 0x0a,
					0x09, 0x08, 0x07, 0x06, 0x05, 0x04, 0x03, 0x02, 0x01, 0x00,
					txscript.OP_EQUALVERIFY,
					txscript.OP_CHECKSIG,
				}

				tx1.AddTxOut(wire.NewTxOut(1000, script1))
				tx1.AddTxOut(wire.NewTxOut(2000, script2))
				tx2.AddTxOut(wire.NewTxOut(3000, script1))

				return []wire.MsgTx{tx1, tx2}
			}(),
			expectedAddrs: 2, // Two unique addresses
			expectedTxids: 2, // Two transactions
			expectedError: false,
			checkAddresses: func(t *testing.T, addresses []string, utxoMap map[string][]AddressDepositUtxo) {
				assert.Len(t, addresses, 2)
				assert.Len(t, utxoMap, 2)
				foundSingleUtxoAddress := false
				foundMultipleUtxoAddress := false
				for _, utxos := range utxoMap {
					if len(utxos) == 2 {
						foundMultipleUtxoAddress = true
					} else if len(utxos) == 1 {
						foundSingleUtxoAddress = true
					}
				}
				assert.True(t, foundSingleUtxoAddress)
				assert.True(t, foundMultipleUtxoAddress)
			},
		},
		{
			name: "multiple transactions to the same address",
			txs: func() []wire.MsgTx {
				tx1 := wire.MsgTx{}
				tx2 := wire.MsgTx{}

				// Create two different P2PKH output scripts
				script1 := []byte{
					txscript.OP_DUP,
					txscript.OP_HASH160,
					0x14, // 20 bytes
					0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09,
					0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10, 0x11, 0x12, 0x9,
					txscript.OP_EQUALVERIFY,
					txscript.OP_CHECKSIG,
				}

				tx1.AddTxOut(wire.NewTxOut(1000, script1))
				tx2.AddTxOut(wire.NewTxOut(3000, script1))

				return []wire.MsgTx{tx1, tx2}
			}(),
			expectedAddrs: 1, // One unique address
			expectedTxids: 2, // Two transactions
			expectedError: false,
			checkAddresses: func(t *testing.T, addresses []string, utxoMap map[string][]AddressDepositUtxo) {
				assert.Len(t, addresses, 1)
				assert.Len(t, utxoMap, 1)
				assert.Len(t, utxoMap[addresses[0]], 2)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			confirmedTxHashSet, creditedAddresses, addressToUtxoMap, err := processTransactions(tt.txs, params)

			if tt.expectedError {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Len(t, confirmedTxHashSet, tt.expectedTxids)
			tt.checkAddresses(t, creditedAddresses, addressToUtxoMap)
		})
	}
}

func TestHandleBlock_MixedTransactions(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{})
	ctx, _ := db.NewTestSQLiteContext(t)
	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	// A refund transaction that will be used to refund the tree node
	refundTx := wire.MsgTx{Version: 1, TxIn: []*wire.TxIn{{}}, TxOut: []*wire.TxOut{{Value: 1000}}}
	var buf bytes.Buffer
	err = refundTx.Serialize(&buf)
	require.NoError(t, err)
	rawRefundTx := buf.Bytes()

	// A transaction to create the treenode's output.
	nodeCreatingTx := wire.MsgTx{Version: 1, TxIn: []*wire.TxIn{{}}, TxOut: []*wire.TxOut{{Value: 1000}}}
	var nodeTxBuf bytes.Buffer
	err = nodeCreatingTx.Serialize(&nodeTxBuf)
	require.NoError(t, err)
	rawNodeTx := nodeTxBuf.Bytes()

	secretShare := keys.MustGeneratePrivateKeyFromRand(rng)
	ownerIDPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	signingPublicKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	verifyingPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	validIssuerPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	// The node needs a dummy tree to satisfy foreign key constraints.
	tree, err := dbTx.Tree.Create().
		SetStatus(schematype.TreeStatusPending).
		SetBaseTxid([]byte("dummytxid")).
		SetOwnerIdentityPubkey(ownerIDPubKey).
		SetNetwork(common.SchemaNetwork(common.Testnet)).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)

	signingKeyshare, err := dbTx.SigningKeyshare.Create().
		SetPublicKey(signingPublicKey).
		SetSecretShare(secretShare.Serialize()).
		SetMinSigners(1).
		SetPublicShares(map[string]keys.Public{}).
		SetStatus(schematype.KeyshareStatusAvailable).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)

	// Create EntityDkgKey so that token_scanner.go can get the entity DKG key public key which
	// is necessary for writing the TokenCreate ent.
	_, err = dbTx.EntityDkgKey.Create().
		SetSigningKeyshare(signingKeyshare).
		Save(ctx)
	require.NoError(t, err)

	// Reuse the signing key from above because we don't enforce it to be anything specific for this test.
	treeNode, err := dbTx.TreeNode.Create().
		SetRawRefundTx(rawRefundTx).
		SetDirectRefundTx(rawRefundTx).
		SetDirectTx(rawNodeTx).
		SetDirectFromCpfpRefundTx(rawRefundTx).
		SetStatus(schematype.TreeNodeStatusOnChain).
		SetNodeConfirmationHeight(100).
		SetOwnerIdentityPubkey(ownerIDPubKey.Serialize()).
		SetRawTx(rawNodeTx).
		SetTree(tree).
		SetValue(1000).
		SetVerifyingPubkey(verifyingPubKey.Serialize()).
		SetOwnerSigningPubkey(ownerIDPubKey.Serialize()).
		SetVout(0).
		SetSigningKeyshare(signingKeyshare).
		Save(ctx)
	require.NoError(t, err)

	// A valid token announcement
	validScriptData := func() []byte {
		s := []byte(announcementPrefix)
		s = append(s, creationAnnouncementKind[:]...)
		s = append(s, validIssuerPubKey.Serialize()...)
		s = append(s, 9) // "TestToken"
		s = append(s, []byte("TestToken")...)
		s = append(s, 4) // "TICK"
		s = append(s, []byte("TICK")...)
		s = append(s, 8)
		s = append(s, make([]byte, 16)...)
		return append(s, 1)
	}()
	t.Logf("Valid script data length: %d bytes", len(validScriptData))
	b := txscript.NewScriptBuilder()
	b.AddOp(txscript.OP_RETURN)
	b.AddData(validScriptData)
	validScript, err := b.Script()
	require.NoError(t, err)
	t.Logf("Valid script hex: %x", validScript)
	validTokenTx := wire.MsgTx{TxOut: []*wire.TxOut{{Value: 0, PkScript: validScript}}}

	// A second valid token announcement with the same issuer pubkey (should be rejected as duplicate)
	duplicateScriptData := func() []byte {
		s := []byte(announcementPrefix)
		s = append(s, creationAnnouncementKind[:]...)
		s = append(s, validIssuerPubKey.Serialize()...) // Same issuer pubkey
		s = append(s, 4)
		s = append(s, []byte("DUP1")...)
		s = append(s, 4)
		s = append(s, []byte("DUP1")...)
		s = append(s, 6)
		s = append(s, make([]byte, 16)...)
		return append(s, 0)
	}()
	b2 := txscript.NewScriptBuilder()
	b2.AddOp(txscript.OP_RETURN)
	b2.AddData(duplicateScriptData)
	duplicateScript, err := b2.Script()
	require.NoError(t, err)
	duplicateTokenTx := wire.MsgTx{TxOut: []*wire.TxOut{{Value: 0, PkScript: duplicateScript}}}

	// An invalid token announcement script that should cause a parsing error
	invalidScriptData := func() []byte {
		s := []byte(announcementPrefix)
		s = append(s, creationAnnouncementKind[:]...)
		s = append(s, make([]byte, 33)...)
		return append(s, 1) // Invalid name length
	}()
	b3 := txscript.NewScriptBuilder()
	b3.AddOp(txscript.OP_RETURN)
	b3.AddData(invalidScriptData)
	invalidScript, err := b3.Script()
	require.NoError(t, err)
	invalidTokenTx := wire.MsgTx{TxOut: []*wire.TxOut{{Value: 0, PkScript: invalidScript}}}

	// A script that isn't a token announcement at all
	nonAnnouncementScript := []byte{txscript.OP_DUP, txscript.OP_HASH160}
	nonAnnouncementTx := wire.MsgTx{TxOut: []*wire.TxOut{{Value: 1000, PkScript: nonAnnouncementScript}}}

	txs := []wire.MsgTx{validTokenTx, duplicateTokenTx, invalidTokenTx, nonAnnouncementTx, refundTx}

	// Disable LRC20 RPCs because we are only interested in testing SO logic.
	config := so.Config{
		SupportedNetworks: []common.Network{common.Testnet},
		Lrc20Configs: map[string]so.Lrc20Config{
			common.Testnet.String(): {
				DisableRpcs: true,
			},
		},
		FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{},
	}

	connCfg := &rpcclient.ConnConfig{DisableTLS: true, HTTPPostMode: true}

	bitcoinClient, err := rpcclient.New(connCfg, nil)
	require.NoError(t, err)
	blockHeight := int64(101)
	err = handleBlock(ctx, &config, dbTx, bitcoinClient, txs, blockHeight, common.Testnet)
	require.NoError(t, err)

	// Both token announcements should be created as L1TokenCreate, but only one TokenCreate should be created
	l1CreatedTokens, err := dbTx.L1TokenCreate.Query().All(ctx)
	require.NoError(t, err)
	require.Len(t, l1CreatedTokens, 2)

	// Verify the first token (valid announcement)
	var validToken, duplicateToken *ent.L1TokenCreate
	for _, token := range l1CreatedTokens {
		switch token.TokenName {
		case "TestToken":
			validToken = token
		case "DUP1":
			duplicateToken = token
		}
	}
	require.NotNil(t, validToken)
	require.NotNil(t, duplicateToken)
	assert.Equal(t, "TestToken", validToken.TokenName)
	assert.Equal(t, "TICK", validToken.TokenTicker)
	assert.Equal(t, validIssuerPubKey, validToken.IssuerPublicKey)
	assert.Equal(t, "DUP1", duplicateToken.TokenName)
	assert.Equal(t, "DUP1", duplicateToken.TokenTicker)
	assert.Equal(t, validIssuerPubKey, duplicateToken.IssuerPublicKey)

	// Only one TokenCreate should be created (duplicate issuer filtered out)
	createdTokens, err := dbTx.TokenCreate.Query().All(ctx)
	require.NoError(t, err)
	require.Len(t, createdTokens, 1)
	assert.Equal(t, "TestToken", createdTokens[0].TokenName)
	assert.Equal(t, "TICK", createdTokens[0].TokenTicker)
	assert.Equal(t, validIssuerPubKey, createdTokens[0].IssuerPublicKey)

	// And the tree node should have been refunded
	node, err := dbTx.TreeNode.Get(ctx, treeNode.ID)
	require.NoError(t, err)
	require.Equal(t, schematype.TreeNodeStatusExited, node.Status)
}
