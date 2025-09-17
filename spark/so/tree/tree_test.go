package tree_test

import (
	"context"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/lightsparkdev/spark/common/keys"

	"github.com/google/go-cmp/cmp"
	"github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/tree"
	sparktesting "github.com/lightsparkdev/spark/testing"

	pb "github.com/lightsparkdev/spark/proto/spark_tree"
)

var (
	rng           = rand.NewChaCha8([32]byte{})
	irrelevantKey = keys.MustGeneratePrivateKeyFromRand(rng)
	userPubKey    = keys.MustGeneratePrivateKeyFromRand(rng).Public()
	otherPubKey   = keys.MustGeneratePrivateKeyFromRand(rng).Public()
)

func TestGetLeafDenominationCounts_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	conn, err := sparktesting.DangerousNewGRPCConnectionWithoutVerifyTLS("localhost:8535", nil)
	if err != nil {
		t.Fatalf("failed to connect to operator: %v", err)
	}
	defer conn.Close()

	client := pb.NewSparkTreeServiceClient(conn)

	req := &pb.GetLeafDenominationCountsRequest{OwnerIdentityPublicKey: userPubKey.Serialize()}
	resp, err := client.GetLeafDenominationCounts(t.Context(), req)
	if err != nil {
		t.Fatalf("failed to get leaf denomination counts: %v", err)
	}
	t.Logf("leaf denomination counts: %v", resp.Counts)
}

func TestGetLeafDenominationCounts(t *testing.T) {
	cases := []struct {
		name    string
		nodes   []treeNodeOpts
		network spark.Network
		want    map[uint64]uint64
	}{
		{
			name:    "single leaf",
			network: spark.Network_MAINNET,
			nodes: []treeNodeOpts{
				{
					ownerIDPubKey: userPubKey,
					status:        st.TreeNodeStatusAvailable,
					value:         8,
				},
			},
			want: map[uint64]uint64{8: 1},
		},
		{
			name:    "different owner ID public keys",
			network: spark.Network_MAINNET,
			nodes: []treeNodeOpts{
				{
					ownerIDPubKey: userPubKey,
					status:        st.TreeNodeStatusAvailable,
					value:         8,
				},
				{
					ownerIDPubKey: otherPubKey,
					status:        st.TreeNodeStatusAvailable,
					value:         8, // Not counted
				},
			},
			want: map[uint64]uint64{8: 1},
		},
		{
			name:    "only available keys counted",
			network: spark.Network_MAINNET,
			nodes: []treeNodeOpts{
				{
					ownerIDPubKey: userPubKey,
					status:        st.TreeNodeStatusAvailable,
					value:         4,
				},
				{
					ownerIDPubKey: userPubKey,
					status:        st.TreeNodeStatusCreating,
					value:         4, // Not counted
				},
			},
			want: map[uint64]uint64{4: 1},
		},
		{
			name:    "only matching network counted",
			network: spark.Network_REGTEST, // Tree created with MAINNET
			nodes: []treeNodeOpts{
				{
					ownerIDPubKey: userPubKey,
					status:        st.TreeNodeStatusAvailable,
					value:         4,
				},
			},
			want: map[uint64]uint64{},
		},
		{
			name:    "non-powers of two ignored",
			network: spark.Network_MAINNET,
			nodes: []treeNodeOpts{
				{
					ownerIDPubKey: userPubKey,
					status:        st.TreeNodeStatusAvailable,
					value:         4,
				},
				{
					ownerIDPubKey: userPubKey,
					status:        st.TreeNodeStatusAvailable,
					value:         9, // Not a power of 2
				},
				{
					ownerIDPubKey: userPubKey,
					status:        st.TreeNodeStatusAvailable,
					value:         7, // Not a power of 2
				},
			},
			want: map[uint64]uint64{4: 1},
		},
		{
			name:    "value greater than max denomination ignored",
			network: spark.Network_MAINNET,
			nodes: []treeNodeOpts{
				{
					ownerIDPubKey: userPubKey,
					status:        st.TreeNodeStatusAvailable,
					value:         4,
				},
				{
					ownerIDPubKey: userPubKey,
					status:        st.TreeNodeStatusAvailable,
					value:         tree.DenominationMax + 1, // Too big
				},
			},
			want: map[uint64]uint64{4: 1},
		},
		{
			name:    "max denomination allowed",
			network: spark.Network_MAINNET,
			nodes: []treeNodeOpts{
				{
					ownerIDPubKey: userPubKey,
					status:        st.TreeNodeStatusAvailable,
					value:         tree.DenominationMax,
				},
			},
			want: map[uint64]uint64{tree.DenominationMax: 1},
		},
		{
			name:    "counts all valid values",
			network: spark.Network_MAINNET,
			nodes: []treeNodeOpts{
				{
					ownerIDPubKey: userPubKey,
					status:        st.TreeNodeStatusAvailable,
					value:         4,
				},
				{
					ownerIDPubKey: userPubKey,
					status:        st.TreeNodeStatusAvailable,
					value:         4,
				},
				{
					ownerIDPubKey: userPubKey,
					status:        st.TreeNodeStatusAvailable,
					value:         8,
				},
				{
					ownerIDPubKey: userPubKey,
					status:        st.TreeNodeStatusAvailable,
					value:         32,
				},
			},
			want: map[uint64]uint64{4: 2, 8: 1, 32: 1},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, _ := db.NewTestSQLiteContext(t)

			seedTreeNode(t, ctx, st.NetworkMainnet, tc.nodes...)

			req := &pb.GetLeafDenominationCountsRequest{OwnerIdentityPublicKey: userPubKey.Serialize(), Network: tc.network}
			resp, err := tree.GetLeafDenominationCounts(ctx, req)
			if err != nil {
				t.Fatalf("failed to get leaf denomination counts: %v", err)
			}
			if diff := cmp.Diff(tc.want, resp.Counts); diff != "" {
				t.Fatalf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestGetLeafDenominationCounts_InvalidNetwork_Errors(t *testing.T) {
	req := &pb.GetLeafDenominationCountsRequest{OwnerIdentityPublicKey: userPubKey.Serialize(), Network: spark.Network_UNSPECIFIED}
	resp, err := tree.GetLeafDenominationCounts(t.Context(), req)
	if err == nil {
		t.Fatal("expecting error, got none")
	}
	if !strings.Contains(err.Error(), "unknown network: 0") {
		t.Errorf("expecting error to contain `unknown network: 0`, got %v", err)
	}
	if resp != nil {
		t.Errorf("expecting nil response, got %v", resp)
	}
}

type treeNodeOpts struct {
	ownerIDPubKey keys.Public
	status        st.TreeNodeStatus
	value         uint64
}

func seedTreeNode(t *testing.T, ctx context.Context, network st.Network, opts ...treeNodeOpts) []*ent.TreeNode {
	t.Helper()

	tx, err := ent.GetDbFromContext(ctx)
	if err != nil {
		t.Fatalf("failed to get or create current tx: %v", err)
	}

	irrelevant := []byte{1, 2, 3, 4}

	tr, err := tx.Tree.Create().
		SetOwnerIdentityPubkey(opts[0].ownerIDPubKey).
		SetNetwork(network).
		SetStatus(st.TreeStatusAvailable).
		SetBaseTxid(irrelevant).
		SetVout(1).
		Save(ctx)
	if err != nil {
		t.Fatalf("failed to create tree: %v", err)
	}

	ks, err := tx.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(irrelevantKey.Serialize()).
		SetPublicShares(map[string]keys.Public{}).
		SetPublicKey(irrelevantKey.Public()).
		SetMinSigners(2).
		SetCoordinatorIndex(1).
		Save(ctx)
	if err != nil {
		t.Fatalf("failed to create signing keyshare: %v", err)
	}

	nodes := make([]*ent.TreeNode, len(opts))
	for i, opt := range opts {
		node, err := tx.TreeNode.Create().
			SetOwnerIdentityPubkey(opt.ownerIDPubKey.Serialize()).
			SetOwnerSigningPubkey(opt.ownerIDPubKey.Serialize()).
			SetVerifyingPubkey(irrelevantKey.Public().Serialize()).
			SetRawTx(irrelevant).
			SetStatus(opt.status).
			SetTree(tr).
			SetSigningKeyshare(ks).
			SetVout(1).
			SetValue(opt.value).
			Save(ctx)
		if err != nil {
			t.Fatalf("failed to create tree node: %v", err)
		}
		nodes[i] = node
	}
	return nodes
}
