package handler

import (
	"context"
	"math/rand/v2"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/sighash"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/stretchr/testify/require"
)

func TestFinalizeRenewNodeTimelockDBResetsLeafVoutAfterSplit(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	rng := rand.NewChaCha8([32]byte{91})
	fixture := createRenewVoutResetFixture(t, ctx, rng)

	signingJob := createTestRenewNodeTimelockSigningJob(t, rng, fixture.leaf, 0)
	renewTxs, err := constructRenewNodeTransactions(fixture.leaf, fixture.parent, signingJob)
	require.NoError(t, err)

	parentOutput := fixture.parentTx.TxOut[fixture.leaf.Vout]
	signatures := [][]byte{
		signRenewVoutResetTx(t, renewTxs.NodeTx, renewTxs.SplitNodeTx.TxOut[0], fixture.verifyingKey),
		signRenewVoutResetTx(t, renewTxs.RefundTx, renewTxs.NodeTx.TxOut[0], fixture.verifyingKey),
		signRenewVoutResetTx(t, renewTxs.SplitNodeTx, parentOutput, fixture.verifyingKey),
		signRenewVoutResetTx(t, renewTxs.DirectSplitNodeTx, parentOutput, fixture.verifyingKey),
		signRenewVoutResetTx(t, renewTxs.DirectNodeTx, renewTxs.SplitNodeTx.TxOut[0], fixture.verifyingKey),
		signRenewVoutResetTx(t, renewTxs.DirectRefundTx, renewTxs.DirectNodeTx.TxOut[0], fixture.verifyingKey),
		signRenewVoutResetTx(t, renewTxs.DirectFromCpfpRefundTx, renewTxs.NodeTx.TxOut[0], fixture.verifyingKey),
	}

	result, err := finalizeRenewNodeTimelockDB(ctx, fixture.leaf, fixture.parent, renewTxs, fixture.keyshare, signatures)
	require.NoError(t, err)
	require.Equal(t, int16(0), result.leaf.Vout)
	require.Equal(t, int16(1), result.splitNode.Vout)

	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	leaf, err := dbClient.TreeNode.Get(ctx, fixture.leaf.ID)
	require.NoError(t, err)
	require.Equal(t, int16(0), leaf.Vout)
	parent, err := leaf.QueryParent().Only(ctx)
	require.NoError(t, err)
	require.Equal(t, result.splitNode.ID, parent.ID)
}

type renewVoutResetFixture struct {
	parent       *ent.TreeNode
	leaf         *ent.TreeNode
	parentTx     *wire.MsgTx
	keyshare     *ent.SigningKeyshare
	verifyingKey keys.Private
}

func createRenewVoutResetFixture(t *testing.T, ctx context.Context, rng *rand.ChaCha8) *renewVoutResetFixture {
	t.Helper()

	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	ownerIdentityKey := keys.MustGeneratePrivateKeyFromRand(rng)
	ownerSigningKey := keys.MustGeneratePrivateKeyFromRand(rng)
	verifyingKey := keys.MustGeneratePrivateKeyFromRand(rng)
	keyshare := createTestRenewSigningKeyshare(t, ctx, rng)
	tree := createTestRenewTree(t, ctx, ownerIdentityKey.Public())

	parentTx := wire.NewMsgTx(3)
	parentTx.AddTxIn(&wire.TxIn{PreviousOutPoint: wire.OutPoint{Hash: tree.BaseTxid.Hash(), Index: uint32(tree.Vout)}})
	addRenewVoutResetOutput(t, parentTx, 100_001, keys.GeneratePrivateKey().Public())
	addRenewVoutResetOutput(t, parentTx, 100_000, verifyingKey.Public())
	parentTx.AddTxOut(common.EphemeralAnchorOutput())
	parentRaw, err := common.SerializeTx(parentTx)
	require.NoError(t, err)

	parent, err := dbClient.TreeNode.Create().
		SetTree(tree).
		SetNetwork(btcnetwork.Regtest).
		SetSigningKeyshare(keyshare).
		SetValue(100_000).
		SetVerifyingPubkey(verifyingKey.Public()).
		SetOwnerIdentityPubkey(ownerIdentityKey.Public()).
		SetOwnerSigningPubkey(ownerSigningKey.Public()).
		SetRawTx(parentRaw).
		SetVout(0).
		SetStatus(st.TreeNodeStatusSplitted).
		Save(ctx)
	require.NoError(t, err)

	leafRawTx := buildRenewVoutResetOutputTx(t, wire.OutPoint{Hash: parentTx.TxHash(), Index: 1}, 1, 100_000, verifyingKey.Public(), true)
	leafRaw, err := common.SerializeTx(leafRawTx)
	require.NoError(t, err)
	leafRefundTx := buildRenewVoutResetOutputTx(t, wire.OutPoint{Hash: leafRawTx.TxHash(), Index: 0}, 1, 100_000, ownerSigningKey.Public(), true)
	leafRefundRaw, err := common.SerializeTx(leafRefundTx)
	require.NoError(t, err)

	leaf, err := dbClient.TreeNode.Create().
		SetTree(tree).
		SetParent(parent).
		SetNetwork(btcnetwork.Regtest).
		SetSigningKeyshare(keyshare).
		SetValue(100_000).
		SetVerifyingPubkey(verifyingKey.Public()).
		SetOwnerIdentityPubkey(ownerIdentityKey.Public()).
		SetOwnerSigningPubkey(ownerSigningKey.Public()).
		SetRawTx(leafRaw).
		SetRawRefundTx(leafRefundRaw).
		SetVout(1).
		SetStatus(st.TreeNodeStatusAvailable).
		Save(ctx)
	require.NoError(t, err)

	return &renewVoutResetFixture{
		parent:       parent,
		leaf:         leaf,
		parentTx:     parentTx,
		keyshare:     keyshare,
		verifyingKey: verifyingKey,
	}
}

func buildRenewVoutResetOutputTx(t *testing.T, prevOut wire.OutPoint, sequence uint32, value int64, pubKey keys.Public, includeAnchor bool) *wire.MsgTx {
	t.Helper()

	tx := wire.NewMsgTx(3)
	tx.AddTxIn(&wire.TxIn{PreviousOutPoint: prevOut, Sequence: sequence})
	addRenewVoutResetOutput(t, tx, value, pubKey)
	if includeAnchor {
		tx.AddTxOut(common.EphemeralAnchorOutput())
	}
	return tx
}

func addRenewVoutResetOutput(t *testing.T, tx *wire.MsgTx, value int64, pubKey keys.Public) {
	t.Helper()

	script, err := common.P2TRScriptFromPubKey(pubKey)
	require.NoError(t, err)
	tx.AddTxOut(wire.NewTxOut(value, script))
}

func signRenewVoutResetTx(t *testing.T, tx *wire.MsgTx, prevOut *wire.TxOut, signer keys.Private) []byte {
	t.Helper()

	hash, err := sighash.FromTx(tx, 0, prevOut)
	require.NoError(t, err)
	return createValidRenewVoutResetTaprootSignature(t, signer, hash)
}

func createValidRenewVoutResetTaprootSignature(t *testing.T, signer keys.Private, sighash sighash.Hash) []byte {
	t.Helper()

	taprootKey := txscript.TweakTaprootPrivKey(*signer.ToBTCEC(), []byte{})
	sig, err := schnorr.Sign(taprootKey, sighash.Serialize())
	require.NoError(t, err)
	return sig.Serialize()
}
