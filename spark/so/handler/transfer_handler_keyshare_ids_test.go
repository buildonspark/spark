//go:build lightspark

package handler

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	enttreenode "github.com/lightsparkdev/spark/so/ent/treenode"
	"github.com/stretchr/testify/require"
)

func loadNodeWithKeyshareEdge(t *testing.T, ctx context.Context, id uuid.UUID) *ent.TreeNode {
	t.Helper()
	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	node, err := dbTx.TreeNode.Query().
		Where(enttreenode.IDEQ(id)).
		WithSigningKeyshare().
		Only(ctx)
	require.NoError(t, err)
	return node
}

func TestSigningKeyshareIDsForLeaves_LoadedEdgesNeedNoDatabase(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	leaf := createDbLeaf(t, ctx, true)
	node := loadNodeWithKeyshareEdge(t, ctx, leaf.node.ID)

	// t.Context has no transaction provider, so success proves the
	// loaded-edge fast path issues no query.
	keyshareIDs, err := signingKeyshareIDsForLeaves(t.Context(), map[string]*ent.TreeNode{
		node.ID.String(): node,
	})
	require.NoError(t, err)
	require.Equal(t, node.Edges.SigningKeyshare.ID, keyshareIDs[node.ID.String()])
}

func TestSigningKeyshareIDsForLeaves_MissingEdgesResolveInOneBatch(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	loadedLeaf := createDbLeaf(t, ctx, true)
	loadedNode := loadNodeWithKeyshareEdge(t, ctx, loadedLeaf.node.ID)
	unloadedLeaf := createDbLeaf(t, ctx, true)
	require.Nil(t, unloadedLeaf.node.Edges.SigningKeyshare)

	keyshareIDs, err := signingKeyshareIDsForLeaves(ctx, map[string]*ent.TreeNode{
		loadedNode.ID.String():        loadedNode,
		unloadedLeaf.node.ID.String(): unloadedLeaf.node,
	})
	require.NoError(t, err)
	require.Len(t, keyshareIDs, 2)
	require.Equal(t, loadedNode.Edges.SigningKeyshare.ID, keyshareIDs[loadedNode.ID.String()])

	unloadedLeafKeyshare, err := unloadedLeaf.node.QuerySigningKeyshare().Only(ctx)
	require.NoError(t, err)
	require.Equal(t, unloadedLeafKeyshare.ID, keyshareIDs[unloadedLeaf.node.ID.String()])
}

func TestSigningKeyshareIDsForLeaves_UnknownLeafFails(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	ghost := &ent.TreeNode{ID: uuid.New()}

	_, err := signingKeyshareIDsForLeaves(ctx, map[string]*ent.TreeNode{
		ghost.ID.String(): ghost,
	})
	require.ErrorContains(t, err, "expected 1 leaves for keyshare lookup but found 0")
}
