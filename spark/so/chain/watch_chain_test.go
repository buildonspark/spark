package chain

import (
	"bytes"
	"context"
	"database/sql"
	"math/rand/v2"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	transferent "github.com/lightsparkdev/spark/so/ent/transfer"
	"github.com/lightsparkdev/spark/so/ent/transferleaf"
	"github.com/lightsparkdev/spark/so/ent/treenode"
	"github.com/lightsparkdev/spark/so/entephemeral"
	ephemeralenttest "github.com/lightsparkdev/spark/so/entephemeral/enttest"
	"github.com/lightsparkdev/spark/so/entephemeral/signingkeysharesecret"
	"github.com/lightsparkdev/spark/so/handler"
	"github.com/lightsparkdev/spark/so/knobs"
	sparktesting "github.com/lightsparkdev/spark/testing"

	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/rpcclient"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	pbspark "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent/schema/schematype"
	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

func TestMain(m *testing.M) {
	stop := db.StartPostgresServer()
	code := m.Run()
	stop()
	os.Exit(code)
}

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
	dummyTxid := schematype.NewRandomTxIDForTesting(t)

	tree, err := dbTx.Tree.Create().
		SetStatus(schematype.TreeStatusPending).
		SetBaseTxid(dummyTxid).
		SetOwnerIdentityPubkey(ownerIDPubKey).
		SetNetwork(btcnetwork.Testnet).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)

	signingKeyshare, err := dbTx.SigningKeyshare.Create().
		SetPublicKey(signingPublicKey).
		SetSecretShare(secretShare).
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
		SetOwnerIdentityPubkey(ownerIDPubKey).
		SetRawTx(rawNodeTx).
		SetTree(tree).
		SetNetwork(tree.Network).
		SetValue(1000).
		SetVerifyingPubkey(verifyingPubKey).
		SetOwnerSigningPubkey(ownerIDPubKey).
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

	// A duplicate token announcement with the same issuer pubkey and the same token metadata (should be rejected as duplicate)
	duplicateScriptData := func() []byte {
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
	b2 := txscript.NewScriptBuilder()
	b2.AddOp(txscript.OP_RETURN)
	b2.AddData(duplicateScriptData)
	duplicateScript, err := b2.Script()
	require.NoError(t, err)
	duplicateTokenTx := wire.MsgTx{TxOut: []*wire.TxOut{{Value: 0, PkScript: duplicateScript}}}

	// A valid token announcement with the same issuer pubkey and different token metadata (should be created as a new token)
	validScriptData2 := func() []byte {
		s := []byte(announcementPrefix)
		s = append(s, creationAnnouncementKind[:]...)
		s = append(s, validIssuerPubKey.Serialize()...) // Same issuer pubkey
		s = append(s, 10)
		s = append(s, []byte("TestToken2")...)
		s = append(s, 5)
		s = append(s, []byte("TICK2")...)
		s = append(s, 8)
		s = append(s, make([]byte, 16)...)
		return append(s, 1)
	}()
	b3 := txscript.NewScriptBuilder()
	b3.AddOp(txscript.OP_RETURN)
	b3.AddData(validScriptData2)
	validScript2, err := b3.Script()
	require.NoError(t, err)
	validTokenTx2 := wire.MsgTx{TxOut: []*wire.TxOut{{Value: 0, PkScript: validScript2}}}

	// An invalid token announcement script that should cause a parsing error
	invalidScriptData := func() []byte {
		s := []byte(announcementPrefix)
		s = append(s, creationAnnouncementKind[:]...)
		s = append(s, make([]byte, 33)...)
		return append(s, 1) // Invalid name length
	}()
	b4 := txscript.NewScriptBuilder()
	b4.AddOp(txscript.OP_RETURN)
	b4.AddData(invalidScriptData)
	invalidScript, err := b4.Script()
	require.NoError(t, err)
	invalidTokenTx := wire.MsgTx{TxOut: []*wire.TxOut{{Value: 0, PkScript: invalidScript}}}

	// A script that isn't a token announcement at all
	nonAnnouncementScript := []byte{txscript.OP_DUP, txscript.OP_HASH160}
	nonAnnouncementTx := wire.MsgTx{TxOut: []*wire.TxOut{{Value: 1000, PkScript: nonAnnouncementScript}}}

	txs := []wire.MsgTx{validTokenTx, duplicateTokenTx, validTokenTx2, invalidTokenTx, nonAnnouncementTx, refundTx}

	// Disable LRC20 RPCs because we are only interested in testing SO logic.
	config := so.Config{
		SupportedNetworks: []btcnetwork.Network{btcnetwork.Testnet},
		Lrc20Configs: map[string]so.Lrc20Config{
			btcnetwork.Testnet.String(): {
				DisableRpcs: true,
			},
		},
		FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{},
	}

	connCfg := &rpcclient.ConnConfig{DisableTLS: true, HTTPPostMode: true}

	bitcoinClient, err := rpcclient.New(connCfg, nil)
	require.NoError(t, err)
	blockHeight := int64(101)
	err = handleBlock(ctx, &config, dbTx, bitcoinClient, txs, blockHeight, chainhash.Hash{}, btcnetwork.Testnet)
	require.NoError(t, err)

	// Both valid token announcements should be created as L1TokenCreate, the duplicate token announcement should not get created as a L1TokenCreate
	l1CreatedTokens, err := dbTx.L1TokenCreate.Query().All(ctx)
	require.NoError(t, err)
	require.Len(t, l1CreatedTokens, 2)

	// Verify the first token (valid announcement)
	var validToken, duplicateToken *ent.L1TokenCreate
	for _, token := range l1CreatedTokens {
		switch token.TokenName {
		case "TestToken":
			validToken = token
		case "TestToken2":
			duplicateToken = token
		}
	}
	require.NotNil(t, validToken)
	require.NotNil(t, duplicateToken)
	assert.Equal(t, "TestToken", validToken.TokenName)
	assert.Equal(t, "TICK", validToken.TokenTicker)
	assert.Equal(t, validIssuerPubKey, validToken.IssuerPublicKey)
	assert.Equal(t, "TestToken2", duplicateToken.TokenName)
	assert.Equal(t, "TICK2", duplicateToken.TokenTicker)
	assert.Equal(t, validIssuerPubKey, duplicateToken.IssuerPublicKey)

	// Two TokenCreates should be created (one for each valid token announcement)
	createdTokens, err := dbTx.TokenCreate.Query().All(ctx)
	require.NoError(t, err)
	require.Len(t, createdTokens, 2)
	assert.Equal(t, "TestToken", createdTokens[0].TokenName)
	assert.Equal(t, "TICK", createdTokens[0].TokenTicker)
	assert.Equal(t, validIssuerPubKey, createdTokens[0].IssuerPublicKey)
	assert.Equal(t, "TestToken2", createdTokens[1].TokenName)
	assert.Equal(t, "TICK2", createdTokens[1].TokenTicker)
	assert.Equal(t, validIssuerPubKey, createdTokens[1].IssuerPublicKey)

	// And the tree node should have been refunded
	node, err := dbTx.TreeNode.Get(ctx, treeNode.ID)
	require.NoError(t, err)
	require.Equal(t, schematype.TreeNodeStatusExited, node.Status)
}

func TestHandleBlock_NodeTransactionMarkingTreeNodeStatus(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{})
	ctx, _ := db.NewTestSQLiteContext(t)
	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	// Create a parent node transaction that will be confirmed in this block
	parentNodeTx := wire.MsgTx{
		Version: 1,
		TxIn:    []*wire.TxIn{{}},
		TxOut:   []*wire.TxOut{{Value: 10000}},
	}
	var parentNodeTxBuf bytes.Buffer
	err = parentNodeTx.Serialize(&parentNodeTxBuf)
	require.NoError(t, err)
	rawParentNodeTx := parentNodeTxBuf.Bytes()

	// Create a refund transaction for the parent node
	parentRefundTx := wire.MsgTx{
		Version: 1,
		TxIn:    []*wire.TxIn{{}},
		TxOut:   []*wire.TxOut{{Value: 9500}},
	}
	var parentRefundTxBuf bytes.Buffer
	err = parentRefundTx.Serialize(&parentRefundTxBuf)
	require.NoError(t, err)
	rawParentRefundTx := parentRefundTxBuf.Bytes()

	// Create child node transactions
	childNodeTx1 := wire.MsgTx{
		Version: 1,
		TxIn:    []*wire.TxIn{{}},
		TxOut:   []*wire.TxOut{{Value: 5000}},
	}
	var childNodeTxBuf1 bytes.Buffer
	err = childNodeTx1.Serialize(&childNodeTxBuf1)
	require.NoError(t, err)
	rawChildNodeTx1 := childNodeTxBuf1.Bytes()

	childNodeTx2 := wire.MsgTx{
		Version: 1,
		TxIn:    []*wire.TxIn{{}},
		TxOut:   []*wire.TxOut{{Value: 4500}},
	}
	var childNodeTxBuf2 bytes.Buffer
	err = childNodeTx2.Serialize(&childNodeTxBuf2)
	require.NoError(t, err)
	rawChildNodeTx2 := childNodeTxBuf2.Bytes()

	// Generate test keys
	ownerIDPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	signingPublicKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	verifyingPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	secretShare := keys.MustGeneratePrivateKeyFromRand(rng)

	// Create a tree
	treeTxid := schematype.NewRandomTxIDForTesting(t)

	tree, err := dbTx.Tree.Create().
		SetStatus(schematype.TreeStatusAvailable).
		SetBaseTxid(treeTxid).
		SetOwnerIdentityPubkey(ownerIDPubKey).
		SetNetwork(btcnetwork.Testnet).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)

	// Create signing keyshare
	signingKeyshare, err := dbTx.SigningKeyshare.Create().
		SetPublicKey(signingPublicKey).
		SetSecretShare(secretShare).
		SetMinSigners(1).
		SetPublicShares(map[string]keys.Public{}).
		SetStatus(schematype.KeyshareStatusAvailable).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)

	// Create parent tree node
	parentNode, err := dbTx.TreeNode.Create().
		SetRawRefundTx(rawParentRefundTx).
		SetDirectRefundTx(rawParentRefundTx).
		SetDirectTx(rawParentNodeTx).
		SetDirectFromCpfpRefundTx(rawParentRefundTx).
		SetStatus(schematype.TreeNodeStatusAvailable).
		SetOwnerIdentityPubkey(ownerIDPubKey).
		SetRawTx(rawParentNodeTx).
		SetTree(tree).
		SetNetwork(tree.Network).
		SetValue(10000).
		SetVerifyingPubkey(verifyingPubKey).
		SetOwnerSigningPubkey(ownerIDPubKey).
		SetVout(0).
		SetSigningKeyshare(signingKeyshare).
		Save(ctx)
	require.NoError(t, err)

	// Create child nodes
	childNode1, err := dbTx.TreeNode.Create().
		SetRawRefundTx(rawChildNodeTx1). // Using the same tx for simplicity
		SetDirectRefundTx(rawChildNodeTx1).
		SetDirectTx(rawChildNodeTx1).
		SetDirectFromCpfpRefundTx(rawChildNodeTx1).
		SetStatus(schematype.TreeNodeStatusAvailable).
		SetOwnerIdentityPubkey(ownerIDPubKey).
		SetRawTx(rawChildNodeTx1).
		SetTree(tree).
		SetNetwork(tree.Network).
		SetParent(parentNode).
		SetValue(5000).
		SetVerifyingPubkey(verifyingPubKey).
		SetOwnerSigningPubkey(ownerIDPubKey).
		SetVout(0).
		SetSigningKeyshare(signingKeyshare).
		Save(ctx)
	require.NoError(t, err)

	childNode2, err := dbTx.TreeNode.Create().
		SetRawRefundTx(rawChildNodeTx2).
		SetDirectRefundTx(rawChildNodeTx2).
		SetDirectTx(rawChildNodeTx2).
		SetDirectFromCpfpRefundTx(rawChildNodeTx2).
		SetStatus(schematype.TreeNodeStatusAvailable).
		SetOwnerIdentityPubkey(ownerIDPubKey).
		SetRawTx(rawChildNodeTx2).
		SetTree(tree).
		SetNetwork(tree.Network).
		SetParent(parentNode).
		SetValue(4500).
		SetVerifyingPubkey(verifyingPubKey).
		SetOwnerSigningPubkey(ownerIDPubKey).
		SetVout(0).
		SetSigningKeyshare(signingKeyshare).
		Save(ctx)
	require.NoError(t, err)

	// Create test transfer
	senderIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiverIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	transfer, err := dbTx.Transfer.Create().
		SetNetwork(tree.Network).
		SetStatus(schematype.TransferStatusSenderInitiated).
		SetType(schematype.TransferTypeTransfer).
		SetSenderIdentityPubkey(senderIdentityPrivKey.Public()).
		SetReceiverIdentityPubkey(receiverIdentityPrivKey.Public()).
		SetTotalValue(1000).
		SetExpiryTime(time.Now().Add(24 * time.Hour)).
		Save(ctx)
	require.NoError(t, err)

	// Create a block with tree node node transaction
	blockTxs := []wire.MsgTx{parentNodeTx}

	// Create mock config
	config := so.Config{
		SupportedNetworks: []btcnetwork.Network{btcnetwork.Testnet},
		BitcoindConfigs: map[string]so.BitcoindConfig{
			"testnet": {
				ProcessNodesForWatchtowers: func() *bool { b := true; return &b }(),
			},
		},
		Lrc20Configs: map[string]so.Lrc20Config{
			btcnetwork.Testnet.String(): {
				DisableRpcs: true,
			},
		},
		FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{},
	}

	// Create a mock bitcoin client
	connCfg := &rpcclient.ConnConfig{DisableTLS: true, HTTPPostMode: true}
	bitcoinClient, err := rpcclient.New(connCfg, nil)
	require.NoError(t, err)

	blockHeight := int64(500)

	// Call handleBlock
	err = handleBlock(ctx, &config, dbTx, bitcoinClient, blockTxs, blockHeight, chainhash.Hash{}, btcnetwork.Testnet)
	require.NoError(t, err)

	// Verify parent node status is updated to OnChain
	updatedParentNode, err := dbTx.TreeNode.Get(ctx, parentNode.ID)
	require.NoError(t, err)
	assert.Equal(t, schematype.TreeNodeStatusOnChain, updatedParentNode.Status)
	assert.Equal(t, uint64(blockHeight), updatedParentNode.NodeConfirmationHeight)

	// Verify child nodes are marked as ParentExited
	updatedChildNode1, err := dbTx.TreeNode.Get(ctx, childNode1.ID)
	require.NoError(t, err)
	assert.Equal(t, schematype.TreeNodeStatusParentExited, updatedChildNode1.Status)

	updatedChildNode2, err := dbTx.TreeNode.Get(ctx, childNode2.ID)
	require.NoError(t, err)
	assert.Equal(t, schematype.TreeNodeStatusParentExited, updatedChildNode2.Status)

	// Verify all 3 are not available for transfer
	baseHandler := handler.NewBaseTransferHandler(&config)
	for _, treeNode := range []*ent.TreeNode{updatedParentNode, updatedChildNode1, updatedChildNode2} {
		err = baseHandler.LeafAvailableToTransfer(ctx, treeNode, transfer)
		require.Error(t, err)
		require.Contains(t, err.Error(), "is not available to transfer")
	}

	// Create a block with tree node refund transaction
	blockTxs = []wire.MsgTx{parentRefundTx}

	blockHeight = int64(505)

	// Call handleBlock
	err = handleBlock(ctx, &config, dbTx, bitcoinClient, blockTxs, blockHeight, chainhash.Hash{}, btcnetwork.Testnet)
	require.NoError(t, err)

	// Verify parent node status is updated to Exited
	updatedParentNode, err = dbTx.TreeNode.Get(ctx, parentNode.ID)
	require.NoError(t, err)
	assert.Equal(t, schematype.TreeNodeStatusExited, updatedParentNode.Status)
	assert.Equal(t, uint64(blockHeight), updatedParentNode.RefundConfirmationHeight)

	// Verify child nodes are still marked as ParentExited
	updatedChildNode1, err = dbTx.TreeNode.Get(ctx, childNode1.ID)
	require.NoError(t, err)
	assert.Equal(t, schematype.TreeNodeStatusParentExited, updatedChildNode1.Status)

	updatedChildNode2, err = dbTx.TreeNode.Get(ctx, childNode2.ID)
	require.NoError(t, err)
	assert.Equal(t, schematype.TreeNodeStatusParentExited, updatedChildNode2.Status)

	// Verify all 3 still not available for transfer
	for _, treeNode := range []*ent.TreeNode{updatedParentNode, updatedChildNode1, updatedChildNode2} {
		err = baseHandler.LeafAvailableToTransfer(ctx, treeNode, transfer)
		require.Error(t, err)
		require.Contains(t, err.Error(), "is not available to transfer")
	}
}

func TestHandleBlock_CoopExitProcessing_KnobEnabled(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{})
	ctx, _ := db.NewTestSQLiteContext(t)

	knobsService := knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobWatchChainTweakKeysForCoopExitDelayEnabled:      1.0,
		knobs.KnobWatchChainCoopExitKeyTweakRequiredConfirmations: 3.0,
	})
	ctx = knobs.InjectKnobsService(ctx, knobsService)

	// Mock tweakKeysForCoopExit to succeed immediately
	originalTweakFunc := tweakKeysForCoopExitFunc
	tweakKeysForCoopExitFunc = func(ctx context.Context, coopExit *ent.CooperativeExit, blockHeight int64) error {
		return nil
	}
	defer func() { tweakKeysForCoopExitFunc = originalTweakFunc }()

	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	// Create test transactions for coop exits
	// Output 1: withdrawal amount to user, Output 2: intermediate amount to connector
	coopExitTx1 := wire.MsgTx{
		Version: 2,
		TxIn:    []*wire.TxIn{{}},
		TxOut: []*wire.TxOut{
			{Value: 900},
			{Value: 100},
		},
	}
	coopExitTx2 := wire.MsgTx{
		Version: 2,
		TxIn:    []*wire.TxIn{{}},
		TxOut: []*wire.TxOut{
			{Value: 1800},
			{Value: 200},
		},
	}
	// Transaction not in block (should not be confirmed)
	coopExitTxNotInBlock := wire.MsgTx{
		Version: 2,
		TxIn:    []*wire.TxIn{{}},
		TxOut: []*wire.TxOut{
			{Value: 3600},
			{Value: 400},
		},
	}

	// Create transfers and coop exits
	senderIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiverIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)

	transfer1, err := dbTx.Transfer.Create().
		SetNetwork(btcnetwork.Testnet).
		SetStatus(schematype.TransferStatusSenderInitiated).
		SetType(schematype.TransferTypeCooperativeExit).
		SetSenderIdentityPubkey(senderIdentityPrivKey.Public()).
		SetReceiverIdentityPubkey(receiverIdentityPrivKey.Public()).
		SetTotalValue(1000).
		SetExpiryTime(time.Now().Add(24 * time.Hour)).
		Save(ctx)
	require.NoError(t, err)

	transfer2, err := dbTx.Transfer.Create().
		SetNetwork(btcnetwork.Testnet).
		SetStatus(schematype.TransferStatusSenderInitiated).
		SetType(schematype.TransferTypeCooperativeExit).
		SetSenderIdentityPubkey(senderIdentityPrivKey.Public()).
		SetReceiverIdentityPubkey(receiverIdentityPrivKey.Public()).
		SetTotalValue(2000).
		SetExpiryTime(time.Now().Add(24 * time.Hour)).
		Save(ctx)
	require.NoError(t, err)

	transfer3, err := dbTx.Transfer.Create().
		SetNetwork(btcnetwork.Testnet).
		SetStatus(schematype.TransferStatusSenderInitiated).
		SetType(schematype.TransferTypeCooperativeExit).
		SetSenderIdentityPubkey(senderIdentityPrivKey.Public()).
		SetReceiverIdentityPubkey(receiverIdentityPrivKey.Public()).
		SetTotalValue(3000).
		SetExpiryTime(time.Now().Add(24 * time.Hour)).
		Save(ctx)
	require.NoError(t, err)

	// Create cooperative exits with reversed byte order (matching production behavior)
	// Exit 1: Reversed bytes - should be found
	txHash1 := coopExitTx1.TxHash()
	reversedTxHash1 := make([]byte, len(txHash1))
	copy(reversedTxHash1, txHash1[:])
	for i := 0; i < len(reversedTxHash1)/2; i++ {
		reversedTxHash1[i], reversedTxHash1[len(reversedTxHash1)-1-i] = reversedTxHash1[len(reversedTxHash1)-1-i], reversedTxHash1[i]
	}
	exitTxid1, err := schematype.NewTxIDFromBytes(reversedTxHash1)
	require.NoError(t, err)
	_, err = dbTx.CooperativeExit.Create().
		SetTransfer(transfer1).
		SetExitTxid(exitTxid1).
		Save(ctx)
	require.NoError(t, err)

	// Exit 2: Direct bytes - should be found
	txHash2 := coopExitTx2.TxHash()
	exitTxid2, err := schematype.NewTxIDFromBytes(txHash2[:])
	require.NoError(t, err)
	_, err = dbTx.CooperativeExit.Create().
		SetTransfer(transfer2).
		SetExitTxid(exitTxid2).
		Save(ctx)
	require.NoError(t, err)

	// Exit 3: Reversed bytes - not in the block, should not be found
	txHash3 := coopExitTxNotInBlock.TxHash()
	reversedTxHash3 := make([]byte, len(txHash3))
	copy(reversedTxHash3, txHash3[:])
	for i := 0; i < len(reversedTxHash3)/2; i++ {
		reversedTxHash3[i], reversedTxHash3[len(reversedTxHash3)-1-i] = reversedTxHash3[len(reversedTxHash3)-1-i], reversedTxHash3[i]
	}
	exitTxid3, err := schematype.NewTxIDFromBytes(reversedTxHash3)
	require.NoError(t, err)
	_, err = dbTx.CooperativeExit.Create().
		SetTransfer(transfer3).
		SetExitTxid(exitTxid3).
		Save(ctx)
	require.NoError(t, err)

	config := so.Config{
		SupportedNetworks: []btcnetwork.Network{btcnetwork.Testnet},
		Lrc20Configs: map[string]so.Lrc20Config{
			btcnetwork.Testnet.String(): {
				DisableRpcs: true,
			},
		},
		FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{},
	}

	connCfg := &rpcclient.ConnConfig{DisableTLS: true, HTTPPostMode: true}
	bitcoinClient, err := rpcclient.New(connCfg, nil)
	require.NoError(t, err)

	blockHeight := int64(100)
	blockTxs := []wire.MsgTx{coopExitTx1, coopExitTx2}

	err = handleBlock(ctx, &config, dbTx, bitcoinClient, blockTxs, blockHeight, chainhash.Hash{}, btcnetwork.Testnet)
	require.NoError(t, err)

	// Verify: exits 1 and 2 should be confirmed
	// Exit 3 is not in the block
	allCoopExits, err := dbTx.CooperativeExit.Query().WithTransfer().All(ctx)
	require.NoError(t, err)
	require.Len(t, allCoopExits, 3)

	confirmedCount := 0
	confirmedTransferIDs := make(map[uuid.UUID]bool)
	for _, exit := range allCoopExits {
		if exit.ConfirmationHeight != nil {
			confirmedCount++
			assert.Equal(t, blockHeight, *exit.ConfirmationHeight)
			// Key tweaked height should still be nil (not enough blocks passed)
			assert.Nil(t, exit.KeyTweakedHeight)
			// Track which transfers were confirmed
			require.NotNil(t, exit.Edges.Transfer)
			confirmedTransferIDs[exit.Edges.Transfer.ID] = true
		}
	}
	assert.Equal(t, 2, confirmedCount, "Expected 2 coop exits to be confirmed (both reversed and non-reversed)")
	assert.True(t, confirmedTransferIDs[transfer1.ID], "Exit 1 (transfer1) should be confirmed")
	assert.True(t, confirmedTransferIDs[transfer2.ID], "Exit 2 (transfer2) should be confirmed")
	assert.False(t, confirmedTransferIDs[transfer3.ID], "Exit 3 (transfer3) should NOT be confirmed")

	// Process a few more blocks to trigger key tweaking (need >= 2 blocks)
	blockHeight = int64(102)
	err = handleBlock(ctx, &config, dbTx, bitcoinClient, []wire.MsgTx{}, blockHeight, chainhash.Hash{}, btcnetwork.Testnet)
	require.NoError(t, err)

	// Verify: After 2+ blocks, keys should be tweaked for all confirmed exits
	allCoopExits, err = dbTx.CooperativeExit.Query().WithTransfer().All(ctx)
	require.NoError(t, err)

	tweakedCount := 0
	tweakedTransferIDs := make(map[uuid.UUID]bool)
	for _, exit := range allCoopExits {
		if exit.ConfirmationHeight != nil {
			// All confirmed exits should now have KeyTweakedHeight set
			assert.NotNil(t, exit.KeyTweakedHeight, "KeyTweakedHeight should be set after 2+ blocks")
			if exit.KeyTweakedHeight != nil {
				assert.Equal(t, blockHeight, *exit.KeyTweakedHeight)
				require.NotNil(t, exit.Edges.Transfer)
				tweakedTransferIDs[exit.Edges.Transfer.ID] = true
				tweakedCount++
			}
		}
	}
	assert.Equal(t, 2, tweakedCount, "Expected 2 coop exits to have keys tweaked (both reversed and non-reversed)")
	assert.True(t, tweakedTransferIDs[transfer1.ID], "Exit 1 (transfer1) should be tweaked")
	assert.True(t, tweakedTransferIDs[transfer2.ID], "Exit 2 (transfer2) should be tweaked")
	assert.False(t, tweakedTransferIDs[transfer3.ID], "Exit 3 (transfer3) should NOT be tweaked")
}

// TestHandleBlock_CoopExitProcessing_Reorg tests that when a block is re-processed
// after a chain reorganization, previously-missed coop exits are picked up and
// already-confirmed exits are not duplicated.
func TestHandleBlock_CoopExitProcessing_Reorg(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{})
	ctx, _ := db.NewTestSQLiteContext(t)

	knobsService := knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobWatchChainTweakKeysForCoopExitDelayEnabled:      1.0,
		knobs.KnobWatchChainCoopExitKeyTweakRequiredConfirmations: 3.0,
	})
	ctx = knobs.InjectKnobsService(ctx, knobsService)

	originalTweakFunc := tweakKeysForCoopExitFunc
	tweakKeysForCoopExitFunc = func(ctx context.Context, coopExit *ent.CooperativeExit, blockHeight int64) error {
		return nil
	}
	defer func() { tweakKeysForCoopExitFunc = originalTweakFunc }()

	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	// Transaction in the original block (pre-reorg)
	txInOriginalBlock := wire.MsgTx{
		Version: 2,
		TxIn:    []*wire.TxIn{{}},
		TxOut:   []*wire.TxOut{{Value: 900}, {Value: 100}},
	}
	// Transaction only in the reorged block (missed during first processing)
	txOnlyInReorgBlock := wire.MsgTx{
		Version: 2,
		TxIn:    []*wire.TxIn{{PreviousOutPoint: wire.OutPoint{Index: 1}}},
		TxOut:   []*wire.TxOut{{Value: 1800}, {Value: 200}},
	}

	senderIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiverIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)

	transfer1, err := dbTx.Transfer.Create().
		SetNetwork(btcnetwork.Testnet).
		SetStatus(schematype.TransferStatusSenderInitiated).
		SetType(schematype.TransferTypeCooperativeExit).
		SetSenderIdentityPubkey(senderIdentityPrivKey.Public()).
		SetReceiverIdentityPubkey(receiverIdentityPrivKey.Public()).
		SetTotalValue(1000).
		SetExpiryTime(time.Now().Add(24 * time.Hour)).
		Save(ctx)
	require.NoError(t, err)

	transfer2, err := dbTx.Transfer.Create().
		SetNetwork(btcnetwork.Testnet).
		SetStatus(schematype.TransferStatusSenderInitiated).
		SetType(schematype.TransferTypeCooperativeExit).
		SetSenderIdentityPubkey(senderIdentityPrivKey.Public()).
		SetReceiverIdentityPubkey(receiverIdentityPrivKey.Public()).
		SetTotalValue(2000).
		SetExpiryTime(time.Now().Add(24 * time.Hour)).
		Save(ctx)
	require.NoError(t, err)

	// Create coop exits with reversed byte order (matching production behavior)
	txHash1 := txInOriginalBlock.TxHash()
	reversedHash1 := make([]byte, chainhash.HashSize)
	copy(reversedHash1, txHash1[:])
	for i := 0; i < len(reversedHash1)/2; i++ {
		reversedHash1[i], reversedHash1[len(reversedHash1)-1-i] = reversedHash1[len(reversedHash1)-1-i], reversedHash1[i]
	}
	exitTxid1, err := schematype.NewTxIDFromBytes(reversedHash1)
	require.NoError(t, err)
	_, err = dbTx.CooperativeExit.Create().
		SetTransfer(transfer1).
		SetExitTxid(exitTxid1).
		Save(ctx)
	require.NoError(t, err)

	txHash2 := txOnlyInReorgBlock.TxHash()
	reversedHash2 := make([]byte, chainhash.HashSize)
	copy(reversedHash2, txHash2[:])
	for i := 0; i < len(reversedHash2)/2; i++ {
		reversedHash2[i], reversedHash2[len(reversedHash2)-1-i] = reversedHash2[len(reversedHash2)-1-i], reversedHash2[i]
	}
	exitTxid2, err := schematype.NewTxIDFromBytes(reversedHash2)
	require.NoError(t, err)
	_, err = dbTx.CooperativeExit.Create().
		SetTransfer(transfer2).
		SetExitTxid(exitTxid2).
		Save(ctx)
	require.NoError(t, err)

	config := so.Config{
		SupportedNetworks: []btcnetwork.Network{btcnetwork.Testnet},
		Lrc20Configs: map[string]so.Lrc20Config{
			btcnetwork.Testnet.String(): {DisableRpcs: true},
		},
		FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{},
	}

	connCfg := &rpcclient.ConnConfig{DisableTLS: true, HTTPPostMode: true}
	bitcoinClient, err := rpcclient.New(connCfg, nil)
	require.NoError(t, err)

	// Create the BlockHeight record (normally created by scanChainUpdates on first run)
	_, err = dbTx.BlockHeight.Create().
		SetHeight(99).
		SetNetwork(btcnetwork.Testnet).
		Save(ctx)
	require.NoError(t, err)

	blockHeight := int64(100)

	// --- Step 1: Process original block (only contains txInOriginalBlock) ---
	originalBlockHash := chainhash.HashH([]byte("original-block"))
	err = handleBlock(ctx, &config, dbTx, bitcoinClient, []wire.MsgTx{txInOriginalBlock}, blockHeight, originalBlockHash, btcnetwork.Testnet)
	require.NoError(t, err)

	allExits, err := dbTx.CooperativeExit.Query().WithTransfer().All(ctx)
	require.NoError(t, err)
	require.Len(t, allExits, 2)

	confirmedCount := 0
	for _, exit := range allExits {
		if exit.ConfirmationHeight != nil {
			confirmedCount++
			assert.Equal(t, blockHeight, *exit.ConfirmationHeight)
			assert.Equal(t, transfer1.ID, exit.Edges.Transfer.ID, "Only exit 1 should be confirmed from original block")
		}
	}
	assert.Equal(t, 1, confirmedCount, "Only 1 coop exit should be confirmed from original block")

	// Verify block hash was stored
	storedBlockHeight, err := dbTx.BlockHeight.Query().Only(ctx)
	require.NoError(t, err)
	require.NotNil(t, storedBlockHeight.BlockHash)
	assert.Equal(t, originalBlockHash.CloneBytes(), *storedBlockHeight.BlockHash)

	// --- Step 2: Re-process same height with reorged block (contains BOTH transactions) ---
	reorgBlockHash := chainhash.HashH([]byte("reorged-block"))
	err = handleBlock(ctx, &config, dbTx, bitcoinClient, []wire.MsgTx{txInOriginalBlock, txOnlyInReorgBlock}, blockHeight, reorgBlockHash, btcnetwork.Testnet)
	require.NoError(t, err)

	allExits, err = dbTx.CooperativeExit.Query().WithTransfer().All(ctx)
	require.NoError(t, err)
	require.Len(t, allExits, 2)

	// Both exits should now be confirmed at the same block height
	for _, exit := range allExits {
		require.NotNil(t, exit.ConfirmationHeight, "Exit for transfer %s should be confirmed after reorg", exit.Edges.Transfer.ID)
		assert.Equal(t, blockHeight, *exit.ConfirmationHeight)
	}

	// Verify block hash was updated to the reorged block
	storedBlockHeight, err = dbTx.BlockHeight.Query().Only(ctx)
	require.NoError(t, err)
	require.NotNil(t, storedBlockHeight.BlockHash)
	assert.Equal(t, reorgBlockHash.CloneBytes(), *storedBlockHeight.BlockHash)

	// --- Step 3: Process enough blocks for key tweaking ---
	blockHeight = int64(102)
	err = handleBlock(ctx, &config, dbTx, bitcoinClient, []wire.MsgTx{}, blockHeight, chainhash.Hash{}, btcnetwork.Testnet)
	require.NoError(t, err)

	allExits, err = dbTx.CooperativeExit.Query().WithTransfer().All(ctx)
	require.NoError(t, err)

	tweakedCount := 0
	for _, exit := range allExits {
		if exit.KeyTweakedHeight != nil {
			tweakedCount++
		}
	}
	assert.Equal(t, 2, tweakedCount, "Both exits should have keys tweaked after enough confirmations")
}

func TestHandleBlock_CoopExitProcessing_KnobDisabled(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{})
	ctx, _ := db.NewTestSQLiteContext(t)

	// Disable the knob to use old code path
	knobsService := knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobWatchChainTweakKeysForCoopExitDelayEnabled: 0.0,
	})
	ctx = knobs.InjectKnobsService(ctx, knobsService)

	// Mock tweakKeysForCoopExit to succeed immediately
	originalTweakFunc := tweakKeysForCoopExitFunc
	tweakKeysForCoopExitFunc = func(ctx context.Context, coopExit *ent.CooperativeExit, blockHeight int64) error {
		return nil
	}
	defer func() { tweakKeysForCoopExitFunc = originalTweakFunc }()

	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	// Create test transactions for coop exits (version 2 with two outputs)
	// Output 1: withdrawal amount to user, Output 2: intermediate amount to connector
	coopExitTx1 := wire.MsgTx{
		Version: 2,
		TxIn:    []*wire.TxIn{{}},
		TxOut: []*wire.TxOut{
			{Value: 900},
			{Value: 100},
		},
	}
	coopExitTx2 := wire.MsgTx{
		Version: 2,
		TxIn:    []*wire.TxIn{{}},
		TxOut: []*wire.TxOut{
			{Value: 1800},
			{Value: 200},
		},
	}
	// Transaction not in block
	coopExitTxNotInBlock := wire.MsgTx{
		Version: 2,
		TxIn:    []*wire.TxIn{{}},
		TxOut: []*wire.TxOut{
			{Value: 3600},
			{Value: 400},
		},
	}

	// Create transfers and coop exits
	senderIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiverIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)

	transfer1, err := dbTx.Transfer.Create().
		SetNetwork(btcnetwork.Testnet).
		SetStatus(schematype.TransferStatusSenderInitiated).
		SetType(schematype.TransferTypeCooperativeExit).
		SetSenderIdentityPubkey(senderIdentityPrivKey.Public()).
		SetReceiverIdentityPubkey(receiverIdentityPrivKey.Public()).
		SetTotalValue(1000).
		SetExpiryTime(time.Now().Add(24 * time.Hour)).
		Save(ctx)
	require.NoError(t, err)

	transfer2, err := dbTx.Transfer.Create().
		SetNetwork(btcnetwork.Testnet).
		SetStatus(schematype.TransferStatusSenderInitiated).
		SetType(schematype.TransferTypeCooperativeExit).
		SetSenderIdentityPubkey(senderIdentityPrivKey.Public()).
		SetReceiverIdentityPubkey(receiverIdentityPrivKey.Public()).
		SetTotalValue(2000).
		SetExpiryTime(time.Now().Add(24 * time.Hour)).
		Save(ctx)
	require.NoError(t, err)

	transfer3, err := dbTx.Transfer.Create().
		SetNetwork(btcnetwork.Testnet).
		SetStatus(schematype.TransferStatusSenderInitiated).
		SetType(schematype.TransferTypeCooperativeExit).
		SetSenderIdentityPubkey(senderIdentityPrivKey.Public()).
		SetReceiverIdentityPubkey(receiverIdentityPrivKey.Public()).
		SetTotalValue(3000).
		SetExpiryTime(time.Now().Add(24 * time.Hour)).
		Save(ctx)
	require.NoError(t, err)

	// Create cooperative exits with reversed byte order (matching production behavior)
	// Exit 1: Reversed bytes - should be found
	txHash1 := coopExitTx1.TxHash()
	reversedTxHash1 := make([]byte, len(txHash1))
	copy(reversedTxHash1, txHash1[:])
	for i := 0; i < len(reversedTxHash1)/2; i++ {
		reversedTxHash1[i], reversedTxHash1[len(reversedTxHash1)-1-i] = reversedTxHash1[len(reversedTxHash1)-1-i], reversedTxHash1[i]
	}
	exitTxid1, err := schematype.NewTxIDFromBytes(reversedTxHash1)
	require.NoError(t, err)
	_, err = dbTx.CooperativeExit.Create().
		SetTransfer(transfer1).
		SetExitTxid(exitTxid1).
		Save(ctx)
	require.NoError(t, err)

	// Exit 2: Direct bytes (not reversed) - should be found
	txHash2 := coopExitTx2.TxHash()
	exitTxid2, err := schematype.NewTxIDFromBytes(txHash2[:])
	require.NoError(t, err)
	_, err = dbTx.CooperativeExit.Create().
		SetTransfer(transfer2).
		SetExitTxid(exitTxid2).
		Save(ctx)
	require.NoError(t, err)

	// Exit 3: Reversed bytes - not in the block, should not be found
	txHash3 := coopExitTxNotInBlock.TxHash()
	reversedTxHash3 := make([]byte, len(txHash3))
	copy(reversedTxHash3, txHash3[:])
	for i := 0; i < len(reversedTxHash3)/2; i++ {
		reversedTxHash3[i], reversedTxHash3[len(reversedTxHash3)-1-i] = reversedTxHash3[len(reversedTxHash3)-1-i], reversedTxHash3[i]
	}
	exitTxid3, err := schematype.NewTxIDFromBytes(reversedTxHash3)
	require.NoError(t, err)
	_, err = dbTx.CooperativeExit.Create().
		SetTransfer(transfer3).
		SetExitTxid(exitTxid3).
		Save(ctx)
	require.NoError(t, err)

	// Create config
	config := so.Config{
		SupportedNetworks: []btcnetwork.Network{btcnetwork.Testnet},
		Lrc20Configs: map[string]so.Lrc20Config{
			btcnetwork.Testnet.String(): {
				DisableRpcs: true,
			},
		},
		FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{},
	}

	connCfg := &rpcclient.ConnConfig{DisableTLS: true, HTTPPostMode: true}
	bitcoinClient, err := rpcclient.New(connCfg, nil)
	require.NoError(t, err)

	blockHeight := int64(100)
	blockTxs := []wire.MsgTx{coopExitTx1, coopExitTx2}

	err = handleBlock(ctx, &config, dbTx, bitcoinClient, blockTxs, blockHeight, chainhash.Hash{}, btcnetwork.Testnet)
	require.NoError(t, err)

	// Verify: exits 1 and 2 should be confirmed, exit 3 should not
	allCoopExits, err := dbTx.CooperativeExit.Query().WithTransfer().All(ctx)
	require.NoError(t, err)
	require.Len(t, allCoopExits, 3)

	confirmedCount := 0
	unconfirmedCount := 0
	confirmedTransferIDs := make(map[uuid.UUID]bool)
	for _, exit := range allCoopExits {
		if exit.ConfirmationHeight != nil {
			confirmedCount++
			assert.Equal(t, blockHeight, *exit.ConfirmationHeight)
			// In legacy path, KeyTweakedHeight should be set immediately
			assert.NotNil(t, exit.KeyTweakedHeight, "KeyTweakedHeight should be set immediately in legacy path")
			assert.Equal(t, blockHeight, *exit.KeyTweakedHeight)
			// Track which transfers were confirmed
			require.NotNil(t, exit.Edges.Transfer)
			confirmedTransferIDs[exit.Edges.Transfer.ID] = true
		} else {
			unconfirmedCount++
		}
	}
	assert.Equal(t, 2, confirmedCount, "Expected 2 coop exits to be confirmed")
	assert.Equal(t, 1, unconfirmedCount, "Expected 1 coop exit to remain unconfirmed")
	// Verify exits 1 and 2 (transfer1 and transfer2) were confirmed
	assert.True(t, confirmedTransferIDs[transfer1.ID], "Exit 1 (transfer1) should be confirmed")
	assert.True(t, confirmedTransferIDs[transfer2.ID], "Exit 2 (transfer2) should be confirmed")
	assert.False(t, confirmedTransferIDs[transfer3.ID], "Exit 3 (transfer3) should NOT be confirmed")
}

func TestTweakKeysForCoopExit_UsesEphemeralSecrets(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{7})
	ctx, _ := db.ConnectToTestPostgres(t)
	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	ephemeralClient := ephemeralenttest.Open(t, "sqlite3", "file:watch_chain_ephemeral?mode=memory&_fk=1")
	t.Cleanup(func() { _ = ephemeralClient.Close() })

	ephemeralSession := db.NewDefaultEphemeralSessionFactory(ephemeralClient).NewSession(ctx)
	t.Cleanup(func() {
		if tx := ephemeralSession.GetTxIfExists(); tx != nil {
			_ = tx.Rollback()
		}
	})
	ctxWithEphemeral := entephemeral.Inject(ctx, ephemeralSession)

	ownerIdentity := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	initialSecret := keys.MustGeneratePrivateKeyFromRand(rng)
	initialPub := initialSecret.Public()
	verifyingPub := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	tweakSecret := keys.MustGeneratePrivateKeyFromRand(rng)
	tweakPub := tweakSecret.Public()

	treeTxid := schematype.NewRandomTxIDForTesting(t)
	tree, err := dbClient.Tree.Create().
		SetStatus(schematype.TreeStatusAvailable).
		SetBaseTxid(treeTxid).
		SetOwnerIdentityPubkey(ownerIdentity).
		SetNetwork(btcnetwork.Testnet).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)

	version := int32(0)
	keyshare, err := dbClient.SigningKeyshare.Create().
		SetPublicKey(initialPub).
		SetSecretVersion(version).
		SetMinSigners(1).
		SetPublicShares(map[string]keys.Public{}).
		SetStatus(schematype.KeyshareStatusInUse).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)

	// Seed the ephemeral secret directly (bypasses advisory lock; safe for
	// single-goroutine test setup).
	_, err = ephemeralClient.SigningKeyshareSecret.Create().
		SetSigningKeyshareID(keyshare.ID).
		SetVersion(version).
		SetSecretShare(initialSecret).
		Save(ctx)
	require.NoError(t, err)

	baseTx := wire.MsgTx{
		Version: 1,
		TxIn:    []*wire.TxIn{{}},
		TxOut:   []*wire.TxOut{{Value: 1_000}},
	}
	var rawTxBuf bytes.Buffer
	err = baseTx.Serialize(&rawTxBuf)
	require.NoError(t, err)
	rawTx := rawTxBuf.Bytes()
	leaf, err := dbClient.TreeNode.Create().
		SetTree(tree).
		SetNetwork(btcnetwork.Testnet).
		SetValue(1_000).
		SetStatus(schematype.TreeNodeStatusAvailable).
		SetVerifyingPubkey(verifyingPub).
		SetOwnerIdentityPubkey(ownerIdentity).
		SetOwnerSigningPubkey(ownerIdentity).
		SetRawTx(rawTx).
		SetVout(0).
		SetSigningKeyshare(keyshare).
		Save(ctx)
	require.NoError(t, err)

	transfer, err := dbClient.Transfer.Create().
		SetNetwork(btcnetwork.Testnet).
		SetStatus(schematype.TransferStatusSenderInitiatedCoordinator).
		SetType(schematype.TransferTypeCooperativeExit).
		SetSenderIdentityPubkey(ownerIdentity).
		SetReceiverIdentityPubkey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
		SetTotalValue(1_000).
		SetExpiryTime(time.Now().Add(1 * time.Hour)).
		Save(ctx)
	require.NoError(t, err)

	keyTweakPayload := &pbspark.SendLeafKeyTweak{
		LeafId: leaf.ID.String(),
		SecretShareTweak: &pbspark.SecretShare{
			SecretShare: tweakSecret.Serialize(),
			Proofs:      [][]byte{tweakPub.Serialize()},
		},
		PubkeySharesTweak: map[string][]byte{},
	}
	keyTweakBytes, err := proto.Marshal(keyTweakPayload)
	require.NoError(t, err)

	refundTx := rawTx
	_, err = dbClient.TransferLeaf.Create().
		SetTransfer(transfer).
		SetLeaf(leaf).
		SetPreviousRefundTx(refundTx).
		SetIntermediateRefundTx(refundTx).
		SetKeyTweak(keyTweakBytes).
		Save(ctx)
	require.NoError(t, err)

	exitTx := wire.MsgTx{
		Version: 2,
		TxIn:    []*wire.TxIn{{}},
		TxOut:   []*wire.TxOut{{Value: 1_000}, {Value: 100}},
	}
	exitHash := exitTx.TxHash()
	exitTxid, err := schematype.NewTxIDFromBytes(exitHash[:])
	require.NoError(t, err)

	coopExit, err := dbClient.CooperativeExit.Create().
		SetTransfer(transfer).
		SetExitTxid(exitTxid).
		Save(ctx)
	require.NoError(t, err)

	err = tweakKeysForCoopExit(ctx, coopExit, 200)
	require.ErrorContains(t, err, "ephemeral DB is unavailable")

	err = tweakKeysForCoopExit(ctxWithEphemeral, coopExit, 200)
	require.NoError(t, err)

	updatedTransfer, err := dbClient.Transfer.Get(ctxWithEphemeral, transfer.ID)
	require.NoError(t, err)
	assert.Equal(t, schematype.TransferStatusSenderKeyTweaked, updatedTransfer.Status)

	updatedTransferLeaf, err := dbClient.TransferLeaf.Query().
		Where(
			transferleaf.HasTransferWith(transferent.IDEQ(transfer.ID)),
			transferleaf.HasLeafWith(treenode.IDEQ(leaf.ID)),
		).
		Only(ctxWithEphemeral)
	require.NoError(t, err)
	assert.Nil(t, updatedTransferLeaf.KeyTweak)

	updatedKeyshare, err := dbClient.SigningKeyshare.Get(ctxWithEphemeral, keyshare.ID)
	require.NoError(t, err)
	require.NotNil(t, updatedKeyshare.SecretVersion)

	expectedSecret := initialSecret.Add(tweakSecret)
	resolvedSecret, err := updatedKeyshare.GetSecretShare(ctxWithEphemeral)
	require.NoError(t, err)
	assert.Equal(t, expectedSecret, *resolvedSecret)
}

type coopExitLeafFixture struct {
	leafID         uuid.UUID
	transferLeafID uuid.UUID
	keyshareID     uuid.UUID
	initialSecret  keys.Private
	tweakSecret    keys.Private
}

func setupCoopExitWithKeyTweaks(
	t *testing.T,
	ctx context.Context,
	dbClient *ent.Client,
	ephemeralClient *entephemeral.Client,
	rng *rand.ChaCha8,
	leafCount int,
) (*ent.CooperativeExit, uuid.UUID, []coopExitLeafFixture) {
	t.Helper()

	ownerIdentity := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	treeTxid := schematype.NewRandomTxIDForTesting(t)
	tree, err := dbClient.Tree.Create().
		SetStatus(schematype.TreeStatusAvailable).
		SetBaseTxid(treeTxid).
		SetOwnerIdentityPubkey(ownerIdentity).
		SetNetwork(btcnetwork.Testnet).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)

	transfer, err := dbClient.Transfer.Create().
		SetNetwork(btcnetwork.Testnet).
		SetStatus(schematype.TransferStatusSenderInitiatedCoordinator).
		SetType(schematype.TransferTypeCooperativeExit).
		SetSenderIdentityPubkey(ownerIdentity).
		SetReceiverIdentityPubkey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
		SetTotalValue(uint64(leafCount) * 1_000).
		SetExpiryTime(time.Now().Add(1 * time.Hour)).
		Save(ctx)
	require.NoError(t, err)

	baseTx := wire.MsgTx{
		Version: 1,
		TxIn:    []*wire.TxIn{{}},
		TxOut:   []*wire.TxOut{{Value: 1_000}},
	}
	var rawTxBuf bytes.Buffer
	err = baseTx.Serialize(&rawTxBuf)
	require.NoError(t, err)
	rawTx := rawTxBuf.Bytes()

	fixtures := make([]coopExitLeafFixture, 0, leafCount)
	version := int32(0)
	for i := range leafCount {
		initialSecret := keys.MustGeneratePrivateKeyFromRand(rng)
		tweakSecret := keys.MustGeneratePrivateKeyFromRand(rng)
		verifyingPub := keys.MustGeneratePrivateKeyFromRand(rng).Public()

		keyshare, err := dbClient.SigningKeyshare.Create().
			SetPublicKey(initialSecret.Public()).
			SetSecretVersion(version).
			SetMinSigners(1).
			SetPublicShares(map[string]keys.Public{}).
			SetStatus(schematype.KeyshareStatusInUse).
			SetCoordinatorIndex(0).
			Save(ctx)
		require.NoError(t, err)

		_, err = ephemeralClient.SigningKeyshareSecret.Create().
			SetSigningKeyshareID(keyshare.ID).
			SetVersion(version).
			SetSecretShare(initialSecret).
			Save(ctx)
		require.NoError(t, err)

		leaf, err := dbClient.TreeNode.Create().
			SetTree(tree).
			SetNetwork(btcnetwork.Testnet).
			SetValue(1_000).
			SetStatus(schematype.TreeNodeStatusAvailable).
			SetVerifyingPubkey(verifyingPub).
			SetOwnerIdentityPubkey(ownerIdentity).
			SetOwnerSigningPubkey(ownerIdentity).
			SetRawTx(rawTx).
			SetVout(int16(i)).
			SetSigningKeyshare(keyshare).
			Save(ctx)
		require.NoError(t, err)

		keyTweakPayload := &pbspark.SendLeafKeyTweak{
			LeafId: leaf.ID.String(),
			SecretShareTweak: &pbspark.SecretShare{
				SecretShare: tweakSecret.Serialize(),
				Proofs:      [][]byte{tweakSecret.Public().Serialize()},
			},
			PubkeySharesTweak: map[string][]byte{},
		}
		keyTweakBytes, err := proto.Marshal(keyTweakPayload)
		require.NoError(t, err)

		transferLeaf, err := dbClient.TransferLeaf.Create().
			SetTransfer(transfer).
			SetLeaf(leaf).
			SetPreviousRefundTx(rawTx).
			SetIntermediateRefundTx(rawTx).
			SetKeyTweak(keyTweakBytes).
			Save(ctx)
		require.NoError(t, err)

		fixtures = append(fixtures, coopExitLeafFixture{
			leafID:         leaf.ID,
			transferLeafID: transferLeaf.ID,
			keyshareID:     keyshare.ID,
			initialSecret:  initialSecret,
			tweakSecret:    tweakSecret,
		})
	}

	coopExit, err := dbClient.CooperativeExit.Create().
		SetTransfer(transfer).
		SetExitTxid(schematype.NewRandomTxIDForTesting(t)).
		Save(ctx)
	require.NoError(t, err)

	return coopExit, transfer.ID, fixtures
}

func assertCoopExitTweaked(
	t *testing.T,
	ctx context.Context,
	dbClient *ent.Client,
	transferID uuid.UUID,
	fixtures []coopExitLeafFixture,
) {
	t.Helper()

	updatedTransfer, err := dbClient.Transfer.Get(ctx, transferID)
	require.NoError(t, err)
	assert.Equal(t, schematype.TransferStatusSenderKeyTweaked, updatedTransfer.Status)

	for _, fixture := range fixtures {
		updatedTransferLeaf, err := dbClient.TransferLeaf.Get(ctx, fixture.transferLeafID)
		require.NoError(t, err)
		assert.Nil(t, updatedTransferLeaf.KeyTweak)

		updatedKeyshare, err := dbClient.SigningKeyshare.Get(ctx, fixture.keyshareID)
		require.NoError(t, err)
		require.NotNil(t, updatedKeyshare.SecretVersion)
		assert.Equal(t, int32(1), *updatedKeyshare.SecretVersion)

		expectedSecret := fixture.initialSecret.Add(fixture.tweakSecret)
		resolvedSecret, err := updatedKeyshare.GetSecretShare(ctx)
		require.NoError(t, err)
		assert.Equal(t, expectedSecret, *resolvedSecret)
	}
}

func TestTweakKeysForCoopExit_TxBackedEphemeralSessionReopensForMultipleLeaves(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	ctx = knobs.InjectKnobsService(ctx, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobSoSigningKeyshareDualWriteSecret: 100,
	}))
	rng := rand.NewChaCha8([32]byte{8})
	mainClient := tc.Client

	ephemeralClient := ephemeralenttest.Open(t, "sqlite3", "file:watch_chain_multi_leaf_ephemeral?mode=memory&_fk=1")
	t.Cleanup(func() { _ = ephemeralClient.Close() })

	coopExit, transferID, fixtures := setupCoopExitWithKeyTweaks(t, ctx, mainClient, ephemeralClient, rng, 2)

	mainTx, err := mainClient.Tx(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = mainTx.Rollback() })

	ephemeralTx, err := ephemeralClient.Tx(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ephemeralTx.Rollback() })

	ephemeralSession := newTxBackedEphemeralSession(ephemeralClient, ephemeralTx)
	chainCtx := ent.Inject(ctx, &txBackedSession{tx: mainTx})
	chainCtx = entephemeral.Inject(chainCtx, ephemeralSession)

	txCoopExit, err := mainTx.Client().CooperativeExit.Get(chainCtx, coopExit.ID)
	require.NoError(t, err)
	err = tweakKeysForCoopExit(chainCtx, txCoopExit, 200)
	require.NoError(t, err)
	require.Nil(t, ephemeralSession.GetTxIfExists(), "each leaf rotation should commit its ephemeral tx inline")

	chainTip := Tip{Height: 1, Hash: chainhash.Hash{}}
	var ephemeralCommitted bool
	err = commitBlockTransactions(ephemeralSession, mainTx, chainTip, zap.NewNop(), &ephemeralCommitted)
	require.NoError(t, err)

	verifyCtx := entephemeral.Inject(ctx, db.NewDefaultEphemeralSessionFactory(ephemeralClient).NewSession(ctx))
	assertCoopExitTweaked(t, verifyCtx, mainClient, transferID, fixtures)
}

func TestTweakKeysForCoopExit_ResumesWhenLeafKeyTweakAlreadyCleared(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	ctx = knobs.InjectKnobsService(ctx, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobSoSigningKeyshareDualWriteSecret: 100,
	}))
	rng := rand.NewChaCha8([32]byte{9})
	mainClient := tc.Client

	ephemeralClient := ephemeralenttest.Open(t, "sqlite3", "file:watch_chain_resume_ephemeral?mode=memory&_fk=1")
	t.Cleanup(func() { _ = ephemeralClient.Close() })

	coopExit, transferID, fixtures := setupCoopExitWithKeyTweaks(t, ctx, mainClient, ephemeralClient, rng, 2)
	appliedFixture := fixtures[0]
	appliedSecret := appliedFixture.initialSecret.Add(appliedFixture.tweakSecret)
	appliedPublicKey := appliedFixture.initialSecret.Public().Add(appliedFixture.tweakSecret.Public())

	_, err := mainClient.SigningKeyshare.UpdateOneID(appliedFixture.keyshareID).
		SetPublicKey(appliedPublicKey).
		SetSecretShare(appliedSecret).
		SetSecretVersion(1).
		Save(ctx)
	require.NoError(t, err)
	_, err = ephemeralClient.SigningKeyshareSecret.Create().
		SetSigningKeyshareID(appliedFixture.keyshareID).
		SetVersion(1).
		SetSecretShare(appliedSecret).
		Save(ctx)
	require.NoError(t, err)
	appliedLeaf, err := mainClient.TreeNode.Get(ctx, appliedFixture.leafID)
	require.NoError(t, err)
	_, err = appliedLeaf.Update().
		SetOwnerSigningPubkey(appliedLeaf.VerifyingPubkey.Sub(appliedPublicKey)).
		Save(ctx)
	require.NoError(t, err)
	_, err = mainClient.TransferLeaf.UpdateOneID(appliedFixture.transferLeafID).
		ClearKeyTweak().
		Save(ctx)
	require.NoError(t, err)

	mainTx, err := mainClient.Tx(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = mainTx.Rollback() })

	ephemeralTx, err := ephemeralClient.Tx(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ephemeralTx.Rollback() })

	ephemeralSession := newTxBackedEphemeralSession(ephemeralClient, ephemeralTx)
	chainCtx := ent.Inject(ctx, &txBackedSession{tx: mainTx})
	chainCtx = entephemeral.Inject(chainCtx, ephemeralSession)

	txCoopExit, err := mainTx.Client().CooperativeExit.Get(chainCtx, coopExit.ID)
	require.NoError(t, err)
	err = tweakKeysForCoopExit(chainCtx, txCoopExit, 200)
	require.NoError(t, err)

	chainTip := Tip{Height: 1, Hash: chainhash.Hash{}}
	var ephemeralCommitted bool
	err = commitBlockTransactions(ephemeralSession, mainTx, chainTip, zap.NewNop(), &ephemeralCommitted)
	require.NoError(t, err)

	verifyCtx := entephemeral.Inject(ctx, db.NewDefaultEphemeralSessionFactory(ephemeralClient).NewSession(ctx))
	assertCoopExitTweaked(t, verifyCtx, mainClient, transferID, fixtures)
}

func TestTweakKeysForCoopExit_EmptyKeyTweakDoesNotResume(t *testing.T) {
	ctx, tc := db.ConnectToTestPostgres(t)
	ctx = knobs.InjectKnobsService(ctx, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobSoSigningKeyshareDualWriteSecret: 100,
	}))
	rng := rand.NewChaCha8([32]byte{10})
	mainClient := tc.Client

	ephemeralClient := ephemeralenttest.Open(t, "sqlite3", "file:watch_chain_empty_key_tweak_ephemeral?mode=memory&_fk=1")
	t.Cleanup(func() { _ = ephemeralClient.Close() })

	coopExit, transferID, fixtures := setupCoopExitWithKeyTweaks(t, ctx, mainClient, ephemeralClient, rng, 1)
	_, err := mainClient.TransferLeaf.UpdateOneID(fixtures[0].transferLeafID).
		SetKeyTweak([]byte{}).
		Save(ctx)
	require.NoError(t, err)

	mainTx, err := mainClient.Tx(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = mainTx.Rollback() })

	ephemeralTx, err := ephemeralClient.Tx(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ephemeralTx.Rollback() })

	ephemeralSession := newTxBackedEphemeralSession(ephemeralClient, ephemeralTx)
	chainCtx := ent.Inject(ctx, &txBackedSession{tx: mainTx})
	chainCtx = entephemeral.Inject(chainCtx, ephemeralSession)

	txCoopExit, err := mainTx.Client().CooperativeExit.Get(chainCtx, coopExit.ID)
	require.NoError(t, err)
	err = tweakKeysForCoopExit(chainCtx, txCoopExit, 200)
	require.ErrorContains(t, err, "secret share tweak is not provided")

	updatedTransfer, err := mainTx.Client().Transfer.Get(chainCtx, transferID)
	require.NoError(t, err)
	assert.Equal(t, schematype.TransferStatusSenderInitiatedCoordinator, updatedTransfer.Status)
}

func TestUpdateSigningKeyshareWithRotatedSecret_MainRollbackCleansUpWithTxBackedEphemeralSession(t *testing.T) {
	ctx := knobs.InjectKnobsService(t.Context(), knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobSoSigningKeyshareDualWriteSecret: 100,
	}))
	mainClient := db.NewTestSQLiteClient(t)
	t.Cleanup(func() { _ = mainClient.Close() })

	ephemeralClient := ephemeralenttest.Open(t, "sqlite3", "file:watch_chain_cleanup_ephemeral?mode=memory&_fk=1")
	t.Cleanup(func() { _ = ephemeralClient.Close() })

	oldSecret := keys.MustParsePrivateKeyHex("4b0f0f4bc26b635f8146bc06d130ad2fbde7f93334e9e48f9697e66b4dcf3f89")
	newSecret := keys.MustParsePrivateKeyHex("2e3389bf1649f6f4f56cfd6f1fff404a08dbcf65f1d95f18dd1265f832f2bff6")
	version := int32(0)

	keyshare, err := mainClient.SigningKeyshare.Create().
		SetPublicKey(oldSecret.Public()).
		SetSecretShare(oldSecret).
		SetSecretVersion(version).
		SetMinSigners(1).
		SetPublicShares(map[string]keys.Public{}).
		SetStatus(schematype.KeyshareStatusInUse).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)

	_, err = ephemeralClient.SigningKeyshareSecret.Create().
		SetSigningKeyshareID(keyshare.ID).
		SetVersion(version).
		SetSecretShare(oldSecret).
		Save(ctx)
	require.NoError(t, err)

	mainTx, err := mainClient.Tx(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = mainTx.Rollback() })

	ephemeralTx, err := ephemeralClient.Tx(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ephemeralTx.Rollback() })

	chainCtx := ent.Inject(ctx, &txBackedSession{tx: mainTx})
	chainCtx = entephemeral.Inject(chainCtx, newTxBackedEphemeralSession(ephemeralClient, ephemeralTx))

	updated, err := ent.UpdateSigningKeyshareWithRotatedSecret(
		chainCtx,
		keyshare.ID,
		newSecret,
		nil,
	)
	require.NoError(t, err)
	require.NotNil(t, updated.SecretVersion)
	require.Equal(t, int32(1), *updated.SecretVersion)

	require.NoError(t, mainTx.Rollback())

	versionOneCount, err := ephemeralClient.SigningKeyshareSecret.Query().
		Where(
			signingkeysharesecret.SigningKeyshareIDEQ(keyshare.ID),
			signingkeysharesecret.VersionEQ(1),
		).
		Count(ctx)
	require.NoError(t, err)
	require.Zero(t, versionOneCount)

	remainingSecret, err := ephemeralClient.SigningKeyshareSecret.Query().
		Where(
			signingkeysharesecret.SigningKeyshareIDEQ(keyshare.ID),
			signingkeysharesecret.VersionEQ(version),
		).
		Only(ctx)
	require.NoError(t, err)
	require.True(t, remainingSecret.SecretShare.Equals(oldSecret))
}

func TestUpdateSigningKeyshareWithRotatedSecret_TxBackedEphemeralSessionReopensForConsecutiveRotations(t *testing.T) {
	ctx := knobs.InjectKnobsService(t.Context(), knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobSoSigningKeyshareDualWriteSecret: 100,
	}))
	mainClient := db.NewTestSQLiteClient(t)
	t.Cleanup(func() { _ = mainClient.Close() })

	ephemeralClient := ephemeralenttest.Open(t, "sqlite3", "file:watch_chain_reopen_ephemeral?mode=memory&_fk=1")
	t.Cleanup(func() { _ = ephemeralClient.Close() })

	oldSecret := keys.MustParsePrivateKeyHex("4b0f0f4bc26b635f8146bc06d130ad2fbde7f93334e9e48f9697e66b4dcf3f89")
	firstSecret := keys.MustParsePrivateKeyHex("2e3389bf1649f6f4f56cfd6f1fff404a08dbcf65f1d95f18dd1265f832f2bff6")
	secondSecret := keys.MustParsePrivateKeyHex("681d1f5c7a12d2c54e1e0a21e51afdfdbf47533e6c4c81a07e35f96cf5ab1539")
	version := int32(0)

	keyshare, err := mainClient.SigningKeyshare.Create().
		SetPublicKey(oldSecret.Public()).
		SetSecretShare(oldSecret).
		SetSecretVersion(version).
		SetMinSigners(1).
		SetPublicShares(map[string]keys.Public{}).
		SetStatus(schematype.KeyshareStatusInUse).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)

	_, err = ephemeralClient.SigningKeyshareSecret.Create().
		SetSigningKeyshareID(keyshare.ID).
		SetVersion(version).
		SetSecretShare(oldSecret).
		Save(ctx)
	require.NoError(t, err)

	mainTx, err := mainClient.Tx(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = mainTx.Rollback() })

	ephemeralTx, err := ephemeralClient.Tx(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ephemeralTx.Rollback() })

	ephemeralSession := newTxBackedEphemeralSession(ephemeralClient, ephemeralTx)
	chainCtx := ent.Inject(ctx, &txBackedSession{tx: mainTx})
	chainCtx = entephemeral.Inject(chainCtx, ephemeralSession)

	updated, err := ent.UpdateSigningKeyshareWithRotatedSecret(chainCtx, keyshare.ID, firstSecret, nil)
	require.NoError(t, err)
	require.NotNil(t, updated.SecretVersion)
	require.Equal(t, int32(1), *updated.SecretVersion)
	require.Nil(t, ephemeralSession.GetTxIfExists(), "first rotation should commit the current ephemeral tx")

	updated, err = ent.UpdateSigningKeyshareWithRotatedSecret(chainCtx, keyshare.ID, secondSecret, nil)
	require.NoError(t, err)
	require.NotNil(t, updated.SecretVersion)
	require.Equal(t, int32(2), *updated.SecretVersion)
	require.Nil(t, ephemeralSession.GetTxIfExists(), "second rotation should use a reopened tx and commit it")

	chainTip := Tip{Height: 1, Hash: chainhash.Hash{}}
	var ephemeralCommitted bool
	err = commitBlockTransactions(ephemeralSession, mainTx, chainTip, zap.NewNop(), &ephemeralCommitted)
	require.NoError(t, err)

	reloaded, err := mainClient.SigningKeyshare.Get(ctx, keyshare.ID)
	require.NoError(t, err)
	require.NotNil(t, reloaded.SecretVersion)
	assert.Equal(t, int32(2), *reloaded.SecretVersion)

	v2, err := ephemeralClient.SigningKeyshareSecret.Query().
		Where(
			signingkeysharesecret.SigningKeyshareIDEQ(keyshare.ID),
			signingkeysharesecret.VersionEQ(2),
		).
		Only(ctx)
	require.NoError(t, err)
	assert.True(t, v2.SecretShare.Equals(secondSecret))
}

func TestTxBackedEphemeralSession_RollbackUsesCurrentReopenedTx(t *testing.T) {
	ctx := t.Context()
	ephemeralClient := ephemeralenttest.Open(t, "sqlite3", "file:watch_chain_rollback_current_ephemeral?mode=memory&_fk=1")
	t.Cleanup(func() { _ = ephemeralClient.Close() })

	initialTx, err := ephemeralClient.Tx(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = initialTx.Rollback() })

	session := newTxBackedEphemeralSession(ephemeralClient, initialTx)
	require.NoError(t, initialTx.Commit())
	require.Nil(t, session.GetTxIfExists())

	reopenedTx, err := session.GetOrBeginTx(ctx)
	require.NoError(t, err)
	require.NotEqual(t, initialTx, reopenedTx)

	signingKeyshareID := uuid.New()
	_, err = reopenedTx.SigningKeyshareSecret.Create().
		SetSigningKeyshareID(signingKeyshareID).
		SetVersion(0).
		SetSecretShare(keys.GeneratePrivateKey()).
		Save(ctx)
	require.NoError(t, err)

	currentTx := session.GetTxIfExists()
	require.NotNil(t, currentTx)
	require.NoError(t, currentTx.Rollback())

	count, err := ephemeralClient.SigningKeyshareSecret.Query().
		Where(signingkeysharesecret.SigningKeyshareIDEQ(signingKeyshareID)).
		Count(ctx)
	require.NoError(t, err)
	assert.Zero(t, count)
}

// TestCommitBlockTransactions_SurvivesInlineEphemeralCommit covers the bug tracked
// in SP-2913: prepareSigningKeyshareSecretRotation commits the per-block ephemeral
// tx inline, so connectBlocks' post-handler commit must tolerate finding the tx
// already finalized. Without commitBlockTransactions' GetTxIfExists guard, the
// second Commit() call would return sql.ErrTxDone and the test would fail.
func TestCommitBlockTransactions_SurvivesInlineEphemeralCommit(t *testing.T) {
	ctx := knobs.InjectKnobsService(t.Context(), knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobSoSigningKeyshareDualWriteSecret: 100,
	}))
	mainClient := db.NewTestSQLiteClient(t)
	t.Cleanup(func() { _ = mainClient.Close() })

	ephemeralClient := ephemeralenttest.Open(t, "sqlite3", "file:watch_chain_commit_ephemeral?mode=memory&_fk=1")
	t.Cleanup(func() { _ = ephemeralClient.Close() })

	oldSecret := keys.MustParsePrivateKeyHex("4b0f0f4bc26b635f8146bc06d130ad2fbde7f93334e9e48f9697e66b4dcf3f89")
	newSecret := keys.MustParsePrivateKeyHex("2e3389bf1649f6f4f56cfd6f1fff404a08dbcf65f1d95f18dd1265f832f2bff6")
	version := int32(0)

	keyshare, err := mainClient.SigningKeyshare.Create().
		SetPublicKey(oldSecret.Public()).
		SetSecretShare(oldSecret).
		SetSecretVersion(version).
		SetMinSigners(1).
		SetPublicShares(map[string]keys.Public{}).
		SetStatus(schematype.KeyshareStatusInUse).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)

	_, err = ephemeralClient.SigningKeyshareSecret.Create().
		SetSigningKeyshareID(keyshare.ID).
		SetVersion(version).
		SetSecretShare(oldSecret).
		Save(ctx)
	require.NoError(t, err)

	mainTx, err := mainClient.Tx(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = mainTx.Rollback() })

	ephemeralTx, err := ephemeralClient.Tx(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ephemeralTx.Rollback() })

	ephemeralSession := newTxBackedEphemeralSession(ephemeralClient, ephemeralTx)
	chainCtx := ent.Inject(ctx, &txBackedSession{tx: mainTx})
	chainCtx = entephemeral.Inject(chainCtx, ephemeralSession)

	// Simulates the chain watcher's handleBlock path triggering keyshare rotation,
	// which commits the per-block ephemeral tx inline today (SP-2913).
	_, err = ent.UpdateSigningKeyshareWithRotatedSecret(chainCtx, keyshare.ID, newSecret, nil)
	require.NoError(t, err)
	require.Nil(t, ephemeralSession.GetTxIfExists(), "callee should have finalized the ephemeral tx")
	// The captured local tx pointer (the pre-fix pattern in connectBlocks) is now
	// finalized. Calling Commit on it directly returns sql.ErrTxDone — which is
	// what would have aborted the block before commitBlockTransactions added the
	// GetTxIfExists guard.
	require.ErrorIs(t, ephemeralTx.Commit(), sql.ErrTxDone, "captured tx should already be committed by the callee")

	chainTip := Tip{Height: 1, Hash: chainhash.Hash{}}
	var ephemeralCommitted bool
	err = commitBlockTransactions(ephemeralSession, mainTx, chainTip, zap.NewNop(), &ephemeralCommitted)
	require.NoError(t, err)
	assert.False(t, ephemeralCommitted, "sentinel resets to false after both commits succeed")

	reloaded, err := mainClient.SigningKeyshare.Get(ctx, keyshare.ID)
	require.NoError(t, err)
	require.NotNil(t, reloaded.SecretVersion)
	assert.Equal(t, int32(1), *reloaded.SecretVersion)

	v1, err := ephemeralClient.SigningKeyshareSecret.Query().
		Where(
			signingkeysharesecret.SigningKeyshareIDEQ(keyshare.ID),
			signingkeysharesecret.VersionEQ(1),
		).
		Only(ctx)
	require.NoError(t, err)
	assert.True(t, v1.SecretShare.Equals(newSecret))
}

func createTestDepositAddress(
	t *testing.T,
	ctx context.Context,
	dbClient *ent.Client,
	rng *rand.ChaCha8,
	address string,
	isStatic bool,
	network btcnetwork.Network,
) *ent.DepositAddress {
	t.Helper()
	ownerPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	signingPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	secretShare := keys.MustGeneratePrivateKeyFromRand(rng)

	keyshare, err := dbClient.SigningKeyshare.Create().
		SetPublicKey(signingPubKey).
		SetSecretShare(secretShare).
		SetMinSigners(1).
		SetPublicShares(map[string]keys.Public{}).
		SetStatus(schematype.KeyshareStatusAvailable).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)

	deposit, err := dbClient.DepositAddress.Create().
		SetAddress(address).
		SetOwnerIdentityPubkey(ownerPubKey).
		SetOwnerSigningPubkey(ownerPubKey).
		SetSigningKeyshare(keyshare).
		SetIsStatic(isStatic).
		SetNetwork(network).
		Save(ctx)
	require.NoError(t, err)
	return deposit
}

func createTestTx(t *testing.T, value int64, pkScript []byte) *wire.MsgTx {
	t.Helper()
	tx := &wire.MsgTx{
		Version: 1,
		TxIn:    []*wire.TxIn{{}},
		TxOut:   []*wire.TxOut{{Value: value, PkScript: pkScript}},
	}
	return tx
}

func TestStoreUtxosForAddress(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{})
	ctx, _ := db.NewTestSQLiteContext(t)
	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	pkScript := []byte{0x51, 0x20, 0x01, 0x02, 0x03}

	t.Run("single utxo", func(t *testing.T) {
		deposit := createTestDepositAddress(t, ctx, dbClient, rng, "addr-single", false, btcnetwork.Regtest)
		tx := createTestTx(t, 5000, pkScript)
		utxos := []AddressDepositUtxo{{tx: tx, amount: 5000, idx: 0}}

		err := storeUtxosForAddress(ctx, dbClient, deposit, utxos, btcnetwork.Regtest, 100)
		require.NoError(t, err)

		stored, err := dbClient.DepositAddress.QueryUtxo(deposit).All(ctx)
		require.NoError(t, err)
		require.Len(t, stored, 1)
		assert.Equal(t, uint64(5000), stored[0].Amount)
		assert.Equal(t, uint32(0), stored[0].Vout)
		assert.Equal(t, int64(100), stored[0].BlockHeight)
	})

	t.Run("multiple utxos", func(t *testing.T) {
		deposit := createTestDepositAddress(t, ctx, dbClient, rng, "addr-multi", false, btcnetwork.Regtest)
		tx1 := createTestTx(t, 3000, pkScript)
		tx2 := createTestTx(t, 7000, pkScript)
		utxos := []AddressDepositUtxo{
			{tx: tx1, amount: 3000, idx: 0},
			{tx: tx2, amount: 7000, idx: 0},
		}

		err := storeUtxosForAddress(ctx, dbClient, deposit, utxos, btcnetwork.Regtest, 200)
		require.NoError(t, err)

		stored, err := dbClient.DepositAddress.QueryUtxo(deposit).All(ctx)
		require.NoError(t, err)
		require.Len(t, stored, 2)
	})

	t.Run("upsert does not duplicate", func(t *testing.T) {
		deposit := createTestDepositAddress(t, ctx, dbClient, rng, "addr-upsert", false, btcnetwork.Regtest)
		tx := createTestTx(t, 4000, pkScript)
		utxos := []AddressDepositUtxo{{tx: tx, amount: 4000, idx: 0}}

		err := storeUtxosForAddress(ctx, dbClient, deposit, utxos, btcnetwork.Regtest, 300)
		require.NoError(t, err)

		// Call again with the same UTXO
		err = storeUtxosForAddress(ctx, dbClient, deposit, utxos, btcnetwork.Regtest, 300)
		require.NoError(t, err)

		stored, err := dbClient.DepositAddress.QueryUtxo(deposit).All(ctx)
		require.NoError(t, err)
		require.Len(t, stored, 1)
	})
}

func TestStoreDepositUtxos(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{1})
	ctx, _ := db.NewTestSQLiteContext(t)
	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	pkScript := []byte{0x51, 0x20, 0x01, 0x02, 0x03}

	t.Run("stores utxos for both static and non-static addresses", func(t *testing.T) {
		staticAddr := createTestDepositAddress(t, ctx, dbClient, rng, "static-addr-1", true, btcnetwork.Regtest)
		nonStaticAddr := createTestDepositAddress(t, ctx, dbClient, rng, "nonstatic-addr-1", false, btcnetwork.Regtest)

		tx1 := createTestTx(t, 1000, pkScript)
		tx2 := createTestTx(t, 2000, pkScript)

		addressToUtxoMap := map[string][]AddressDepositUtxo{
			staticAddr.Address:    {{tx: tx1, amount: 1000, idx: 0}},
			nonStaticAddr.Address: {{tx: tx2, amount: 2000, idx: 0}},
		}
		creditedAddresses := []string{staticAddr.Address, nonStaticAddr.Address}

		err := storeDepositUtxos(ctx, dbClient, creditedAddresses, addressToUtxoMap, btcnetwork.Regtest, 100)
		require.NoError(t, err)

		staticUtxos, err := dbClient.DepositAddress.QueryUtxo(staticAddr).All(ctx)
		require.NoError(t, err)
		require.Len(t, staticUtxos, 1)
		assert.Equal(t, uint64(1000), staticUtxos[0].Amount)

		nonStaticUtxos, err := dbClient.DepositAddress.QueryUtxo(nonStaticAddr).All(ctx)
		require.NoError(t, err)
		require.Len(t, nonStaticUtxos, 1)
		assert.Equal(t, uint64(2000), nonStaticUtxos[0].Amount)
	})

	t.Run("non-static address picks up utxos across blocks", func(t *testing.T) {
		addr := createTestDepositAddress(t, ctx, dbClient, rng, "nonstatic-multiblock", false, btcnetwork.Regtest)

		// Block 1: first UTXO arrives
		tx1 := createTestTx(t, 5000, pkScript)
		addressToUtxoMap := map[string][]AddressDepositUtxo{
			addr.Address: {{tx: tx1, amount: 5000, idx: 0}},
		}
		err := storeDepositUtxos(ctx, dbClient, []string{addr.Address}, addressToUtxoMap, btcnetwork.Regtest, 100)
		require.NoError(t, err)

		stored, err := dbClient.DepositAddress.QueryUtxo(addr).All(ctx)
		require.NoError(t, err)
		require.Len(t, stored, 1)

		// Block 2: second UTXO arrives at the same address
		tx2 := createTestTx(t, 8000, pkScript)
		addressToUtxoMap = map[string][]AddressDepositUtxo{
			addr.Address: {{tx: tx2, amount: 8000, idx: 0}},
		}
		err = storeDepositUtxos(ctx, dbClient, []string{addr.Address}, addressToUtxoMap, btcnetwork.Regtest, 101)
		require.NoError(t, err)

		stored, err = dbClient.DepositAddress.QueryUtxo(addr).All(ctx)
		require.NoError(t, err)
		require.Len(t, stored, 2)
	})

	t.Run("ignores addresses not in database", func(t *testing.T) {
		utxoCountBefore, err := dbClient.Utxo.Query().Count(ctx)
		require.NoError(t, err)

		addressToUtxoMap := map[string][]AddressDepositUtxo{
			"unknown-addr": {{tx: createTestTx(t, 1000, pkScript), amount: 1000, idx: 0}},
		}
		err = storeDepositUtxos(ctx, dbClient, []string{"unknown-addr"}, addressToUtxoMap, btcnetwork.Regtest, 500)
		require.NoError(t, err)

		utxoCountAfter, err := dbClient.Utxo.Query().Count(ctx)
		require.NoError(t, err)
		assert.Equal(t, utxoCountBefore, utxoCountAfter)
	})
}

func TestHandleBlock_NonStaticDeposit_SetsConfirmationTxid(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{2})
	ctx, _ := db.NewTestSQLiteContext(t)
	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	// Create a taproot-style script so processTransactions can decode an address
	params := &chaincfg.RegressionNetParams
	privKey := keys.MustGeneratePrivateKeyFromRand(rng)
	taprootKey := txscript.ComputeTaprootKeyNoScript(privKey.Public().ToBTCEC())
	taprootScript, err := txscript.PayToTaprootScript(taprootKey)
	require.NoError(t, err)

	// Decode the address string for the deposit address entity
	_, addrs, _, err := txscript.ExtractPkScriptAddrs(taprootScript, params)
	require.NoError(t, err)
	require.Len(t, addrs, 1)
	addrStr := addrs[0].EncodeAddress()

	deposit := createTestDepositAddress(t, ctx, dbClient, rng, addrStr, false, btcnetwork.Regtest)

	// Two transactions to the same address with different amounts
	smallTx := wire.MsgTx{Version: 1, TxIn: []*wire.TxIn{{}}, TxOut: []*wire.TxOut{{Value: 1000, PkScript: taprootScript}}}
	largeTx := wire.MsgTx{Version: 1, TxIn: []*wire.TxIn{{}}, TxOut: []*wire.TxOut{{Value: 5000, PkScript: taprootScript}}}

	config := so.Config{
		SupportedNetworks: []btcnetwork.Network{btcnetwork.Regtest},
		Lrc20Configs: map[string]so.Lrc20Config{
			btcnetwork.Regtest.String(): {DisableRpcs: true},
		},
		FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{},
	}
	connCfg := &rpcclient.ConnConfig{DisableTLS: true, HTTPPostMode: true}
	bitcoinClient, err := rpcclient.New(connCfg, nil)
	require.NoError(t, err)

	// Block 1: both transactions confirm
	blockTxs := []wire.MsgTx{smallTx, largeTx}
	err = handleBlock(ctx, &config, dbClient, bitcoinClient, blockTxs, 100, chainhash.Hash{}, btcnetwork.Regtest)
	require.NoError(t, err)

	// Verify confirmation_txid is set to the largest UTXO's txid
	updated, err := dbClient.DepositAddress.Get(ctx, deposit.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(100), updated.ConfirmationHeight)
	assert.Equal(t, largeTx.TxHash().String(), updated.ConfirmationTxid)

	// Verify both UTXOs are stored as Utxo entities
	utxos, err := dbClient.DepositAddress.QueryUtxo(updated).All(ctx)
	require.NoError(t, err)
	assert.Len(t, utxos, 2)
}
