package chain

import (
	"bytes"
	"context"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/handler"
	"github.com/lightsparkdev/spark/so/knobs"
	sparktesting "github.com/lightsparkdev/spark/testing"

	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/rpcclient"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
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
		knobs.KnobWatchChainTweakKeysForCoopExitDelayEnabled: 1.0,
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
