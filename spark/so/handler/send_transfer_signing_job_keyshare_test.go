package handler

import (
	"math/rand/v2"
	"testing"

	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/ent"
	enttreenode "github.com/lightsparkdev/spark/so/ent/treenode"
	"github.com/stretchr/testify/require"
)

// Pins buildSigningJobForRefund's loaded-edge fast path below the application
// boundary: a public-API test passes identically whether or not the per-leaf
// keyshare query is issued.

func sendTransferKeyshareTestRefundJob(t *testing.T, rng *rand.ChaCha8, parentTx *wire.MsgTx) []byte {
	t.Helper()
	refundScript, err := common.P2TRScriptFromPubKey(keys.MustGeneratePrivateKeyFromRand(rng).Public())
	require.NoError(t, err)
	return createSendTransferSigningJobTestTx(
		t,
		wire.OutPoint{Hash: parentTx.TxHash(), Index: 0},
		900,
		refundScript,
		nil,
	)
}

func TestBuildSigningJobForRefundLoadedKeyshareEdgeNeedsNoDatabase(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{11})
	ctx, leaf, parentTx := createSendTransferSigningJobTestLeaf(t, rng)

	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	node, err := dbTx.TreeNode.Query().
		Where(enttreenode.IDEQ(leaf.ID)).
		WithSigningKeyshare().
		Only(ctx)
	require.NoError(t, err)

	refundRaw := sendTransferKeyshareTestRefundJob(t, rng, parentTx)
	job := parseSendRefundJob(t, createSendTransferUserSignedJob(t, rng, node.ID.String(), refundRaw))

	// Entity edge queries run through the entity's embedded config, not the
	// ctx transaction provider, so only a detached copy (where any query
	// panics) proves the loaded-edge path issues none.
	detached := &ent.TreeNode{
		ID:              node.ID,
		RawTx:           node.RawTx,
		VerifyingPubkey: node.VerifyingPubkey,
	}
	detached.Edges.SigningKeyshare = node.Edges.SigningKeyshare

	helperJob, err := buildSigningJobForRefund(t.Context(), job, detached, detached.RawTx, uuid.New(), keys.Public{})
	require.NoError(t, err)
	require.Equal(t, node.Edges.SigningKeyshare.ID, helperJob.SigningKeyshareID)
}

func TestBuildSigningJobForRefundMissingKeyshareEdgeFallsBackToQuery(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{12})
	ctx, leaf, parentTx := createSendTransferSigningJobTestLeaf(t, rng)
	require.Nil(t, leaf.Edges.SigningKeyshare)

	refundRaw := sendTransferKeyshareTestRefundJob(t, rng, parentTx)
	job := parseSendRefundJob(t, createSendTransferUserSignedJob(t, rng, leaf.ID.String(), refundRaw))

	helperJob, err := buildSigningJobForRefund(ctx, job, leaf, leaf.RawTx, uuid.New(), keys.Public{})
	require.NoError(t, err)

	expectedID, err := leaf.QuerySigningKeyshare().OnlyID(ctx)
	require.NoError(t, err)
	require.Equal(t, expectedID, helperJob.SigningKeyshareID)
}
