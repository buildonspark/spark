package tree

import (
	"context"
	"math/rand/v2"
	"testing"

	"github.com/lightsparkdev/spark/common/keys"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/proto/spark_tree"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

var seeded = rand.NewChaCha8([32]byte{0})

func TestPolarityScorer_Score(t *testing.T) {
	sspKey := keys.MustGeneratePrivateKeyFromRand(seeded).Public()
	userKey := keys.MustGeneratePrivateKeyFromRand(seeded).Public()
	otherKey := keys.MustGeneratePrivateKeyFromRand(seeded).Public()
	leafID := seededUUID()

	tests := []struct {
		name        string
		setupScores map[uuid.UUID]map[keys.Public]float32
		want        float32
	}{
		{
			name: "leaf exists with both scores",
			setupScores: map[uuid.UUID]map[keys.Public]float32{
				leafID: {
					sspKey:  0.8,
					userKey: 0.3,
				},
			},
			want: 0.5,
		},
		{
			name: "leaf exists with only ssp score",
			setupScores: map[uuid.UUID]map[keys.Public]float32{
				leafID: {sspKey: 0.8},
			},
			want: 0.8,
		},
		{
			name: "leaf exists with only user score",
			setupScores: map[uuid.UUID]map[keys.Public]float32{
				leafID: {userKey: 0.3},
			},
			want: -0.3,
		},
		{
			name: "leaf exists with neither score",
			setupScores: map[uuid.UUID]map[keys.Public]float32{
				leafID: {otherKey: 0.5},
			},
			want: 0.0,
		},
		{
			name:        "leaf does not exist",
			setupScores: map[uuid.UUID]map[keys.Public]float32{},
			want:        0.0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, dbCtx := db.NewTestSQLiteContext(t)

			scorer := NewPolarityScorer(dbCtx.Client)
			scorer.probPubKeyCanClaim = tc.setupScores

			score := scorer.Score(leafID, sspKey, userKey)
			assert.InDelta(t, tc.want, score, 0.01)
		})
	}
}

func TestPolarityScorer_UpdateLeaves(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	scorer := NewPolarityScorer(dbTx.Client())

	treeOwnerPubKey := keys.MustGeneratePrivateKeyFromRand(seeded).Public()
	tree := dbTx.Tree.Create().
		SetOwnerIdentityPubkey(treeOwnerPubKey.Serialize()).
		SetStatus(st.TreeStatusAvailable).
		SetNetwork(st.NetworkMainnet).
		SetBaseTxid([]byte("base_txid")).
		SetVout(0).
		SaveX(ctx)

	keyshareSecret := keys.MustGeneratePrivateKeyFromRand(seeded)
	keysharePubKey := keyshareSecret.Public()
	keyshare := dbTx.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(keyshareSecret.Serialize()).
		SetPublicShares(map[string][]uint8{}).
		SetPublicKey(keysharePubKey.Serialize()).
		SetMinSigners(2).
		SetCoordinatorIndex(1).
		SaveX(ctx)

	parentOwner := keys.MustGeneratePrivateKeyFromRand(seeded).Public()
	verifyingPubKey := keys.MustGeneratePrivateKeyFromRand(seeded).Public()
	parentNode := dbTx.TreeNode.Create().
		SetTree(tree).
		SetStatus(st.TreeNodeStatusAvailable).
		SetOwnerIdentityPubkey(parentOwner.Serialize()).
		SetOwnerSigningPubkey(parentOwner.Serialize()).
		SetValue(1000).
		SetVerifyingPubkey(verifyingPubKey.Serialize()).
		SetSigningKeyshare(keyshare).
		SetRawTx([]byte("raw_tx")).
		SetVout(0).
		SaveX(ctx)

	// Create child nodes
	owner1PubKey := keys.MustGeneratePrivateKeyFromRand(seeded).Public()
	verifyingPubKey1 := keys.MustGeneratePrivateKeyFromRand(seeded).Public()
	child1 := dbTx.TreeNode.Create().
		SetTree(tree).
		SetStatus(st.TreeNodeStatusAvailable).
		SetOwnerIdentityPubkey(owner1PubKey.Serialize()).
		SetOwnerSigningPubkey(owner1PubKey.Serialize()).
		SetValue(500).
		SetVerifyingPubkey(verifyingPubKey1.Serialize()).
		SetSigningKeyshare(keyshare).
		SetRawTx([]byte("raw_tx1")).
		SetVout(0).
		SetParent(parentNode).
		SaveX(ctx)

	owner2PubKey := keys.MustGeneratePrivateKeyFromRand(seeded).Public()
	verifyingPubKey2 := keys.MustGeneratePrivateKeyFromRand(seeded).Public()
	child2 := dbTx.TreeNode.Create().
		SetTree(tree).
		SetStatus(st.TreeNodeStatusAvailable).
		SetOwnerIdentityPubkey(owner2PubKey.Serialize()).
		SetOwnerSigningPubkey(owner2PubKey.Serialize()).
		SetValue(500).
		SetVerifyingPubkey(verifyingPubKey2.Serialize()).
		SetSigningKeyshare(keyshare).
		SetRawTx([]byte("raw_tx2")).
		SetVout(1).
		SetParent(parentNode).
		SaveX(ctx)

	scorer.UpdateLeaves(ctx, parentNode)

	assert.Len(t, scorer.probPubKeyCanClaim, 2)
	for _, leaf := range []*ent.TreeNode{child1, child2} {
		scores, exists := scorer.probPubKeyCanClaim[leaf.ID]
		assert.True(t, exists, "Leaf %s should have scores", leaf.ID)
		assert.NotEmpty(t, scores, "Leaf %s should have non-empty scores", leaf.ID)
	}
}

func TestPolarityScorer_FetchPolarityScores(t *testing.T) {
	pubKey1 := keys.MustGeneratePrivateKeyFromRand(seeded).Public()
	pubKey2 := keys.MustGeneratePrivateKeyFromRand(seeded).Public()
	pubKey3 := keys.MustGeneratePrivateKeyFromRand(seeded).Public()

	tests := []struct {
		name           string
		request        *spark_tree.FetchPolarityScoreRequest
		setupScores    map[uuid.UUID]map[keys.Public]float32
		expectedCount  int
		expectedScores map[keys.Public]float32 // key: leafID_pubkey
	}{
		{
			name: "fetch all scores",
			request: &spark_tree.FetchPolarityScoreRequest{
				PublicKeys: [][]byte{},
			},
			setupScores: map[uuid.UUID]map[keys.Public]float32{
				seededUUID(): {
					pubKey1: 0.5,
					pubKey2: 0.3,
				},
				seededUUID(): {
					pubKey3: 0.7,
				},
			},
			expectedCount: 3,
		},
		{
			name: "fetch specific pubkeys",
			request: &spark_tree.FetchPolarityScoreRequest{
				PublicKeys: [][]byte{
					pubKey1.Serialize(),
					pubKey3.Serialize(),
				},
			},
			setupScores: map[uuid.UUID]map[keys.Public]float32{
				seededUUID(): {
					pubKey1: 0.5,
					pubKey2: 0.3,
				},
				seededUUID(): {
					pubKey3: 0.7,
				},
			},
			expectedCount: 2,
		},
		{
			name: "no matching pubkeys",
			request: &spark_tree.FetchPolarityScoreRequest{
				PublicKeys: [][]byte{keys.MustGeneratePrivateKeyFromRand(seeded).Public().Serialize()},
			},
			setupScores: map[uuid.UUID]map[keys.Public]float32{
				seededUUID(): {pubKey1: 0.5},
			},
			expectedCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, dbCtx := db.NewTestSQLiteContext(t)

			scorer := NewPolarityScorer(dbCtx.Client)
			scorer.probPubKeyCanClaim = tt.setupScores

			mockStream := &mockSparkTreeServiceFetchPolarityScoresServer{
				ctx:    ctx,
				scores: []*spark_tree.PolarityScore{},
			}

			err := scorer.FetchPolarityScores(tt.request, mockStream)
			require.NoError(t, err)

			for _, score := range mockStream.scores {
				assert.NotEmpty(t, score.LeafId)
				assert.NotEmpty(t, score.PublicKey)
				assert.NotZero(t, score.Score)
			}
		})
	}
}

// Mock implementation for testing FetchPolarityScores
type mockSparkTreeServiceFetchPolarityScoresServer struct {
	grpc.ServerStream
	ctx    context.Context
	scores []*spark_tree.PolarityScore
}

func (m *mockSparkTreeServiceFetchPolarityScoresServer) Context() context.Context {
	return m.ctx
}

func (m *mockSparkTreeServiceFetchPolarityScoresServer) Send(score *spark_tree.PolarityScore) error {
	m.scores = append(m.scores, score)
	return nil
}

func seededUUID() uuid.UUID {
	return uuid.Must(uuid.NewRandomFromReader(seeded))
}
