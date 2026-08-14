package handler

import (
	"context"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/sighash"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pbspark "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/frost"
	"github.com/lightsparkdev/spark/so/helper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recoverFixture is the stranded shape: a renewal chain where an older
// generation's direct tx confirmed, leaving its output — under the key every
// generation shares — as the only place the leaf's value can still be reached.
type recoverFixture struct {
	dbClient     *ent.Client
	leaf         *ent.TreeNode
	source       *ent.TreeNode
	sourceTx     *wire.MsgTx
	verifyingKey keys.Public
	destKey      keys.Public
	// ownerKey signs the recovery statement; the leaf carries its public half.
	ownerKey keys.Private
	keyshare *ent.SigningKeyshare
	tree     *ent.Tree
}

func newRecoverFixture(t *testing.T, ctx context.Context, rng *rand.ChaCha8) *recoverFixture {
	t.Helper()

	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	ownerKey := keys.MustGeneratePrivateKeyFromRand(rng)
	ownerIdentityKey := ownerKey.Public()
	ownerSigningKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	verifyingKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	destKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	keyshare := createTestRenewSigningKeyshare(t, ctx, rng)
	tree := createTestRenewTree(t, ctx, ownerIdentityKey)

	sourceTx := recoverBuildTx(t, wire.OutPoint{Hash: tree.BaseTxid.Hash(), Index: 0}, 100_000, verifyingKey)
	source := recoverCreateNode(t, ctx, dbClient, tree, keyshare, ownerIdentityKey, ownerSigningKey, verifyingKey, sourceTx, nil)
	source, err = source.Update().SetNodeConfirmationHeight(900_000).SetStatus(st.TreeNodeStatusOnChain).Save(ctx)
	require.NoError(t, err)

	// The live leaf: a later generation of the same chain, so the same key, whose
	// own transactions can no longer confirm.
	leafTx := recoverBuildTx(t, wire.OutPoint{Hash: sourceTx.TxHash(), Index: 0}, 100_000, verifyingKey)
	leaf := recoverCreateNode(t, ctx, dbClient, tree, keyshare, ownerIdentityKey, ownerSigningKey, verifyingKey, leafTx, source)
	leaf, err = leaf.Update().SetStatus(st.TreeNodeStatusWatchtowerExited).Save(ctx)
	require.NoError(t, err)

	return &recoverFixture{
		dbClient:     dbClient,
		leaf:         leaf,
		source:       source,
		sourceTx:     sourceTx,
		verifyingKey: verifyingKey,
		destKey:      destKey,
		ownerKey:     ownerKey,
		keyshare:     keyshare,
		tree:         tree,
	}
}

func recoverCreateNode(t *testing.T, ctx context.Context, dbClient *ent.Client, tree *ent.Tree, keyshare *ent.SigningKeyshare, ownerIdentityKey, ownerSigningKey, verifyingKey keys.Public, directTx *wire.MsgTx, parent *ent.TreeNode) *ent.TreeNode {
	t.Helper()

	directRaw, err := common.SerializeTx(directTx)
	require.NoError(t, err)
	// raw_tx is the cpfp sibling and is never the recovery target; it only needs
	// to be present and distinct so its txid cannot collide with direct_txid.
	rawTx := recoverBuildTx(t, directTx.TxIn[0].PreviousOutPoint, 99_999, verifyingKey)
	raw, err := common.SerializeTx(rawTx)
	require.NoError(t, err)

	create := dbClient.TreeNode.Create().
		SetTree(tree).
		SetNetwork(btcnetwork.Regtest).
		SetSigningKeyshare(keyshare).
		SetValue(100_000).
		SetVerifyingPubkey(verifyingKey).
		SetOwnerIdentityPubkey(ownerIdentityKey).
		SetOwnerSigningPubkey(ownerSigningKey).
		SetRawTx(raw).
		SetDirectTx(directRaw).
		SetVout(0).
		SetStatus(st.TreeNodeStatusAvailable)
	if parent != nil {
		create = create.SetParent(parent)
	}
	node, err := create.Save(ctx)
	require.NoError(t, err)
	return node
}

func recoverBuildTx(t *testing.T, prevOut wire.OutPoint, value int64, payTo keys.Public) *wire.MsgTx {
	t.Helper()

	script, err := common.P2TRScriptFromPubKey(payTo)
	require.NoError(t, err)
	tx := wire.NewMsgTx(3)
	tx.AddTxIn(&wire.TxIn{PreviousOutPoint: prevOut, Sequence: wire.MaxTxInSequenceNum})
	tx.AddTxOut(wire.NewTxOut(value, script))
	return tx
}

func recoverRawTx(t *testing.T, tx *wire.MsgTx) []byte {
	t.Helper()

	raw, err := common.SerializeTx(tx)
	require.NoError(t, err)
	return raw
}

func TestResolveRecoverableOutputAcceptsConfirmedAncestorOutput(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	f := newRecoverFixture(t, ctx, rand.NewChaCha8([32]byte{7}))

	recoveryTx := recoverBuildTx(t, wire.OutPoint{Hash: f.sourceTx.TxHash(), Index: 0}, 99_000, f.destKey)
	recoverable, err := resolveRecoverableOutput(ctx, f.dbClient, f.leaf, recoverRawTx(t, recoveryTx))
	require.NoError(t, err)

	assert.Equal(t, f.source.ID, recoverable.sourceNodeID)
	assert.Equal(t, int64(100_000), recoverable.prevOut.Value)
	assert.Equal(t, f.sourceTx.TxOut[0].PkScript, recoverable.prevOut.PkScript)
	assert.NotEqual(t, [32]byte{}, [32]byte(recoverable.sighash))
}

func TestResolveRecoverableOutputRejectsOutputUnderAnotherKey(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	rng := rand.NewChaCha8([32]byte{8})
	f := newRecoverFixture(t, ctx, rng)

	// A confirmed node in the same tree whose output pays a different key — the
	// tree-split case, where the value below is not this leaf's alone to claim.
	otherKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	tree, err := f.leaf.QueryTree().Only(ctx)
	require.NoError(t, err)
	keyshare, err := f.leaf.QuerySigningKeyshare().Only(ctx)
	require.NoError(t, err)
	strangerTx := recoverBuildTx(t, wire.OutPoint{Hash: tree.BaseTxid.Hash(), Index: 1}, 100_000, otherKey)
	stranger := recoverCreateNode(t, ctx, f.dbClient, tree, keyshare, f.leaf.OwnerIdentityPubkey, f.leaf.OwnerSigningPubkey, otherKey, strangerTx, nil)
	// Confirmed and still unspent, so only the script check can reject it — the
	// point of this test.
	_, err = stranger.Update().SetNodeConfirmationHeight(900_000).SetStatus(st.TreeNodeStatusOnChain).Save(ctx)
	require.NoError(t, err)

	recoveryTx := recoverBuildTx(t, wire.OutPoint{Hash: strangerTx.TxHash(), Index: 0}, 99_000, f.destKey)
	_, err = resolveRecoverableOutput(ctx, f.dbClient, f.leaf, recoverRawTx(t, recoveryTx))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not the leaf's verifying key")
}

func TestResolveRecoverableOutputRejectsUnconfirmedSourceNode(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	f := newRecoverFixture(t, ctx, rand.NewChaCha8([32]byte{9}))

	// The leaf's own direct tx: the script matches but nothing confirmed it, so
	// signing would hand back a spend of an output that does not exist.
	leafDirectTx, err := common.TxFromRawTxBytes(f.leaf.DirectTx)
	require.NoError(t, err)
	recoveryTx := recoverBuildTx(t, wire.OutPoint{Hash: leafDirectTx.TxHash(), Index: 0}, 99_000, f.destKey)

	_, err = resolveRecoverableOutput(ctx, f.dbClient, f.leaf, recoverRawTx(t, recoveryTx))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a confirmed watchtower exit")
}

func TestResolveRecoverableOutputRejectsSourceWhoseRefundConfirmed(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	f := newRecoverFixture(t, ctx, rand.NewChaCha8([32]byte{16}))

	// The source's own refund spends the output being recovered, and MarkExitingNodes
	// moves the node to EXITED once it confirms — nothing left to recover.
	_, err := f.source.Update().
		SetStatus(st.TreeNodeStatusExited).
		SetRefundConfirmationHeight(900_050).
		Save(ctx)
	require.NoError(t, err)

	recoveryTx := recoverBuildTx(t, wire.OutPoint{Hash: f.sourceTx.TxHash(), Index: 0}, 99_000, f.destKey)
	_, err = resolveRecoverableOutput(ctx, f.dbClient, f.leaf, recoverRawTx(t, recoveryTx))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a confirmed watchtower exit")
}

func TestResolveRecoverableOutputRejectsUnknownOutpoint(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	f := newRecoverFixture(t, ctx, rand.NewChaCha8([32]byte{10}))

	unknown := st.NewRandomTxIDForTesting(t)
	recoveryTx := recoverBuildTx(t, wire.OutPoint{Hash: unknown.Hash(), Index: 0}, 99_000, f.destKey)

	_, err := resolveRecoverableOutput(ctx, f.dbClient, f.leaf, recoverRawTx(t, recoveryTx))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a confirmed watchtower exit")
}

func TestResolveRecoverableOutputRejectsNodeInAnotherTree(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	rng := rand.NewChaCha8([32]byte{11})
	f := newRecoverFixture(t, ctx, rng)

	// Same key, confirmed, but a different tree: outside anything this leaf's
	// ownership can speak for.
	other := newRecoverFixture(t, ctx, rng)
	recoveryTx := recoverBuildTx(t, wire.OutPoint{Hash: other.sourceTx.TxHash(), Index: 0}, 99_000, f.destKey)

	_, err := resolveRecoverableOutput(ctx, f.dbClient, f.leaf, recoverRawTx(t, recoveryTx))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a confirmed watchtower exit")
}

func TestResolveRecoverableOutputRejectsMultipleInputs(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	f := newRecoverFixture(t, ctx, rand.NewChaCha8([32]byte{12}))

	recoveryTx := recoverBuildTx(t, wire.OutPoint{Hash: f.sourceTx.TxHash(), Index: 0}, 99_000, f.destKey)
	recoveryTx.AddTxIn(&wire.TxIn{PreviousOutPoint: wire.OutPoint{Hash: f.sourceTx.TxHash(), Index: 1}})

	_, err := resolveRecoverableOutput(ctx, f.dbClient, f.leaf, recoverRawTx(t, recoveryTx))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "exactly 1 input")
}

func TestResolveRecoverableOutputRejectsOutputsExceedingPrevOut(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	f := newRecoverFixture(t, ctx, rand.NewChaCha8([32]byte{13}))

	recoveryTx := recoverBuildTx(t, wire.OutPoint{Hash: f.sourceTx.TxHash(), Index: 0}, 100_001, f.destKey)

	_, err := resolveRecoverableOutput(ctx, f.dbClient, f.leaf, recoverRawTx(t, recoveryTx))

	require.Error(t, err)
	assert.ErrorIs(t, err, helper.ErrTotalOutputValueGreaterThanPrevOutputValue)
}

func TestResolveRecoverableOutputRejectsVoutPastEndOfSourceTx(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	f := newRecoverFixture(t, ctx, rand.NewChaCha8([32]byte{14}))

	recoveryTx := recoverBuildTx(t, wire.OutPoint{Hash: f.sourceTx.TxHash(), Index: 5}, 99_000, f.destKey)

	_, err := resolveRecoverableOutput(ctx, f.dbClient, f.leaf, recoverRawTx(t, recoveryTx))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "names output 5")
}

func TestResolveRecoverableOutputRejectsMalformedTx(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	f := newRecoverFixture(t, ctx, rand.NewChaCha8([32]byte{15}))

	_, err := resolveRecoverableOutput(ctx, f.dbClient, f.leaf, []byte{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty")

	_, err = resolveRecoverableOutput(ctx, f.dbClient, f.leaf, []byte{0xde, 0xad})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse")
}

// recoverNonceCommitment builds a commitment well-formed enough to get past the
// parse authorizeRecoverWatchtowerExitedLeaf does before anything interesting.
func recoverNonceCommitment(t *testing.T, rng *rand.ChaCha8) *pbcommon.SigningCommitment {
	t.Helper()

	commitment, err := frost.NewSigningCommitment(
		keys.MustGeneratePrivateKeyFromRand(rng).Public(),
		keys.MustGeneratePrivateKeyFromRand(rng).Public(),
	)
	require.NoError(t, err)
	return commitment.MarshalProto()
}

func recoverSignStatement(t *testing.T, key keys.Private, leafID uuid.UUID, network btcnetwork.Network, txSighash sighash.Hash) []byte {
	t.Helper()

	statement := createRecoverWatchtowerExitedLeafStatement(leafID, network, txSighash)
	return ecdsa.Sign(key.ToBTCEC(), statement).Serialize()
}

// A renewal split node shares its child's verifying key and keyshare but keeps
// the owner it was created with, so without the leaf check a previous owner
// authorises a recovery of the current owner's output.
func TestAuthorizeRecoverWatchtowerExitedLeafRejectsNodeWithChildren(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{31})
	// Postgres rather than SQLite: the lookup under test takes a row lock.
	ctx, _ := db.ConnectToTestPostgres(t)
	f := newRecoverFixture(t, ctx, rng)

	// A later renewal generation under the leaf, owned by whoever holds the value
	// now. That is what turns f.leaf into a split node with a stale owner.
	childTx := recoverBuildTx(t, wire.OutPoint{Hash: f.leaf.RawTxid.Hash(), Index: 0}, 100_000, f.verifyingKey)
	recoverCreateNode(t, ctx, f.dbClient, f.tree, f.keyshare,
		keys.MustGeneratePrivateKeyFromRand(rng).Public(),
		keys.MustGeneratePrivateKeyFromRand(rng).Public(),
		f.verifyingKey, childTx, f.leaf)

	recoveryTx := recoverBuildTx(t, wire.OutPoint{Hash: f.sourceTx.TxHash(), Index: 0}, 99_000, f.destKey)
	_, _, err := authorizeRecoverWatchtowerExitedLeaf(ctx, &pbspark.RecoverWatchtowerExitedLeafRequest{
		LeafId: f.leaf.ID.String(),
		RecoveryTxSigningJob: &pbspark.SigningJob{
			RawTx:                  recoverRawTx(t, recoveryTx),
			SigningNonceCommitment: recoverNonceCommitment(t, rng),
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not a leaf")
}

// The control for the test above: a childless leaf still gets through.
func TestAuthorizeRecoverWatchtowerExitedLeafAcceptsALeaf(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{32})
	// Postgres rather than SQLite: the lookup under test takes a row lock.
	ctx, _ := db.ConnectToTestPostgres(t)
	f := newRecoverFixture(t, ctx, rng)

	recoveryTx := recoverBuildTx(t, wire.OutPoint{Hash: f.sourceTx.TxHash(), Index: 0}, 99_000, f.destKey)
	rawTx := recoverRawTx(t, recoveryTx)
	recoverable, err := resolveRecoverableOutput(ctx, f.dbClient, f.leaf, rawTx)
	require.NoError(t, err)

	leaf, resolved, err := authorizeRecoverWatchtowerExitedLeaf(ctx, &pbspark.RecoverWatchtowerExitedLeafRequest{
		LeafId: f.leaf.ID.String(),
		RecoveryTxSigningJob: &pbspark.SigningJob{
			RawTx:                  rawTx,
			SigningNonceCommitment: recoverNonceCommitment(t, rng),
		},
		UserSignature: recoverSignStatement(t, f.ownerKey, f.leaf.ID, f.leaf.Network, recoverable.sighash),
	})
	require.NoError(t, err)
	assert.Equal(t, f.leaf.ID, leaf.ID)
	assert.Equal(t, f.source.ID, resolved.sourceNodeID)
}

// Every non-coordinator operator's safety rests on this signature: the
// ConsensusPrepare channel carries no session, so nothing else survives the hop.
func TestValidateRecoverWatchtowerExitedLeafSignature(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{33})
	ctx, _ := db.NewTestSQLiteContext(t)
	f := newRecoverFixture(t, ctx, rng)

	recoveryTx := recoverBuildTx(t, wire.OutPoint{Hash: f.sourceTx.TxHash(), Index: 0}, 99_000, f.destKey)
	recoverable, err := resolveRecoverableOutput(ctx, f.dbClient, f.leaf, recoverRawTx(t, recoveryTx))
	require.NoError(t, err)

	otherKey := keys.MustGeneratePrivateKeyFromRand(rng)
	otherSighash := sighash.Hash{}

	tests := []struct {
		name      string
		signature []byte
		rejected  bool
	}{
		{
			name:      "the owner's signature over this leaf and this sighash",
			signature: recoverSignStatement(t, f.ownerKey, f.leaf.ID, f.leaf.Network, recoverable.sighash),
		},
		{
			name:      "signed by somebody other than the owner",
			signature: recoverSignStatement(t, otherKey, f.leaf.ID, f.leaf.Network, recoverable.sighash),
			rejected:  true,
		},
		{
			name:      "the owner's signature naming a different leaf",
			signature: recoverSignStatement(t, f.ownerKey, uuid.New(), f.leaf.Network, recoverable.sighash),
			rejected:  true,
		},
		{
			// Without this a mainnet authorisation replays onto regtest.
			name:      "the owner's signature naming a different network",
			signature: recoverSignStatement(t, f.ownerKey, f.leaf.ID, btcnetwork.Mainnet, recoverable.sighash),
			rejected:  true,
		},
		{
			// The sighash is in the statement so an earlier authorisation cannot be
			// replayed onto a new transaction.
			name:      "the owner's signature over a different transaction",
			signature: recoverSignStatement(t, f.ownerKey, f.leaf.ID, f.leaf.Network, otherSighash),
			rejected:  true,
		},
		{
			name:      "no signature at all",
			signature: nil,
			rejected:  true,
		},
		{
			name:      "not a signature",
			signature: []byte{0xde, 0xad, 0xbe, 0xef},
			rejected:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateRecoverWatchtowerExitedLeafSignature(f.leaf, f.leaf.Network, tt.signature, recoverable.sighash)
			if tt.rejected {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

// A transfer whose sender key tweaks have landed has already made the value the
// receiver's, but owner_identity_pubkey still names the sender until the claim
// rotates it — so without this the sender co-signs a spend of value the SE
// treats as the receiver's. TRANSFER_LOCKED cannot stand in for the check: the
// watchtower sweep overwrites it with WATCHTOWER_EXITED, which is the very
// status this endpoint requires.
func TestAuthorizeRecoverWatchtowerExitedLeafRejectsLeafWithTransferInFlight(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{34})
	// Postgres rather than SQLite: the lookup under test takes a row lock.
	ctx, _ := db.ConnectToTestPostgres(t)
	f := newRecoverFixture(t, ctx, rng)

	_, receiver := recoverCreateTransferForLeaf(t, ctx, f, rng, st.TransferStatusSenderKeyTweaked, st.TransferReceiverStatusKeyTweaked)

	recoveryTx := recoverBuildTx(t, wire.OutPoint{Hash: f.sourceTx.TxHash(), Index: 0}, 99_000, f.destKey)
	rawTx := recoverRawTx(t, recoveryTx)
	recoverable, err := resolveRecoverableOutput(ctx, f.dbClient, f.leaf, rawTx)
	require.NoError(t, err)

	req := &pbspark.RecoverWatchtowerExitedLeafRequest{
		LeafId: f.leaf.ID.String(),
		RecoveryTxSigningJob: &pbspark.SigningJob{
			RawTx:                  rawTx,
			SigningNonceCommitment: recoverNonceCommitment(t, rng),
		},
		UserSignature: recoverSignStatement(t, f.ownerKey, f.leaf.ID, f.leaf.Network, recoverable.sighash),
	}
	_, _, err = authorizeRecoverWatchtowerExitedLeaf(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "transfer in flight")

	// The same call succeeds once that receiver settles, so the guard defers
	// recovery rather than forfeiting it. The transfer is left non-terminal to
	// show the receiver is what decides.
	_, err = receiver.Update().SetStatus(st.TransferReceiverStatusCompleted).Save(ctx)
	require.NoError(t, err)
	_, _, err = authorizeRecoverWatchtowerExitedLeaf(ctx, req)
	require.NoError(t, err)
}

// recoverCreateTransferForLeaf puts f.leaf into a transfer with one receiver, at
// the given transfer and receiver statuses.
func recoverCreateTransferForLeaf(t *testing.T, ctx context.Context, f *recoverFixture, rng *rand.ChaCha8, transferStatus st.TransferStatus, receiverStatus st.TransferReceiverStatus) (*ent.Transfer, *ent.TransferReceiver) {
	t.Helper()

	transfer, err := f.dbClient.Transfer.Create().
		SetSenderIdentityPubkey(f.leaf.OwnerIdentityPubkey).
		SetReceiverIdentityPubkey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
		SetStatus(transferStatus).
		SetTotalValue(f.leaf.Value).
		SetExpiryTime(time.Now().Add(10 * time.Minute)).
		SetType(st.TransferTypeTransfer).
		SetNetwork(btcnetwork.Regtest).
		Save(ctx)
	require.NoError(t, err)

	receiver, err := f.dbClient.TransferReceiver.Create().
		SetTransfer(transfer).
		SetIdentityPubkey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
		SetStatus(receiverStatus).
		SetTransferType(st.TransferTypeTransfer).
		Save(ctx)
	require.NoError(t, err)

	_, err = f.dbClient.TransferLeaf.Create().
		SetTransfer(transfer).
		SetLeaf(f.leaf).
		SetTransferReceiver(receiver).
		SetPreviousRefundTx(createTestTxBytes(t, 4000)).
		SetIntermediateRefundTx(createTestTxBytes(t, 4001)).
		Save(ctx)
	require.NoError(t, err)
	return transfer, receiver
}

// A MIMO transfer stays non-terminal until every receiver has claimed, so a
// receiver whose own claim completed — and who is therefore already the leaf's
// owner of record — must not be held behind a co-receiver who has not.
func TestAuthorizeRecoverWatchtowerExitedLeafAllowsLeafWhoseReceiverClaimed(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{35})
	// Postgres rather than SQLite: the lookup under test takes a row lock.
	ctx, _ := db.ConnectToTestPostgres(t)
	f := newRecoverFixture(t, ctx, rng)

	transfer, _ := recoverCreateTransferForLeaf(t, ctx, f, rng, st.TransferStatusSenderKeyTweaked, st.TransferReceiverStatusCompleted)
	// The co-receiver that keeps the shared transfer non-terminal. It holds no
	// leaf of ours, so it has no bearing on whether this one may be recovered.
	_, err := f.dbClient.TransferReceiver.Create().
		SetTransfer(transfer).
		SetIdentityPubkey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
		SetStatus(st.TransferReceiverStatusKeyTweaked).
		SetTransferType(st.TransferTypeTransfer).
		Save(ctx)
	require.NoError(t, err)

	recoveryTx := recoverBuildTx(t, wire.OutPoint{Hash: f.sourceTx.TxHash(), Index: 0}, 99_000, f.destKey)
	rawTx := recoverRawTx(t, recoveryTx)
	recoverable, err := resolveRecoverableOutput(ctx, f.dbClient, f.leaf, rawTx)
	require.NoError(t, err)

	_, _, err = authorizeRecoverWatchtowerExitedLeaf(ctx, &pbspark.RecoverWatchtowerExitedLeafRequest{
		LeafId: f.leaf.ID.String(),
		RecoveryTxSigningJob: &pbspark.SigningJob{
			RawTx:                  rawTx,
			SigningNonceCommitment: recoverNonceCommitment(t, rng),
		},
		UserSignature: recoverSignStatement(t, f.ownerKey, f.leaf.ID, f.leaf.Network, recoverable.sighash),
	})
	require.NoError(t, err)
}
