package helper_test

import (
	"context"
	"encoding/hex"
	"math/big"
	"math/rand/v2"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	secretsharing "github.com/lightsparkdev/spark/common/secret_sharing"

	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"

	"github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/helper"
	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// singleOperatorConfig builds a minimal so.Config whose SigningOperatorMap
// has exactly one operator. The operator's polynomial index is ID+1 = 1.
func singleOperatorConfig(identifier string) *so.Config {
	return &so.Config{
		SigningOperatorMap: map[string]*so.SigningOperator{
			identifier: {ID: 0, Identifier: identifier},
		},
	}
}

// setupLeafForTweakTest creates a TreeNode + SigningKeyshare pair for use
// with TweakLeafKeyUpdate. Returns the leaf, its underlying keyshare, the
// keyshare's secret share, and the keyshare's public key.
func setupLeafForTweakTest(t *testing.T, ctx context.Context, rng *rand.ChaCha8) (*ent.TreeNode, *ent.SigningKeyshare, keys.Private, keys.Public) {
	t.Helper()

	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	ownerPub := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	rawTxBytes, err := hex.DecodeString("030000000001017fe94b2a47dfe4c240824fadfb632086e9c0720c82f8c64cc0d4a9793e5c2df20000000000ffffffff02c0120000000000002251203c0433dfd1ce2d8dc7679c6d6a4261f4eba1469132bdfca507147828463cdf1400000000000000000451024e73014002e3a227a1940c62bd1490c02b6154b7879fe14e4fb15e9f2641db88405580264bbc1b902dab3793a1f1c83343796293bea8e1e06cdfca8ca357131c5e79898f00000000")
	require.NoError(t, err)
	baseTxid := schematype.NewRandomTxIDForTesting(t)

	keysharePriv := keys.MustGeneratePrivateKeyFromRand(rng)
	keysharePub := keysharePriv.Public()
	pubSharePub := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	verifyingPub := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	ownerSigningPub := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	tree, err := dbClient.Tree.Create().
		SetOwnerIdentityPubkey(ownerPub).
		SetStatus(schematype.TreeStatusAvailable).
		SetNetwork(btcnetwork.Mainnet).
		SetBaseTxid(baseTxid).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)

	keyshare, err := dbClient.SigningKeyshare.Create().
		SetStatus(schematype.KeyshareStatusInUse).
		SetSecretShare(keysharePriv).
		SetPublicShares(map[string]keys.Public{"operator1": pubSharePub}).
		SetPublicKey(keysharePub).
		SetMinSigners(2).
		SetCoordinatorIndex(1).
		Save(ctx)
	require.NoError(t, err)

	leaf, err := dbClient.TreeNode.Create().
		SetTree(tree).
		SetNetwork(tree.Network).
		SetValue(1000).
		SetStatus(schematype.TreeNodeStatusAvailable).
		SetVerifyingPubkey(verifyingPub).
		SetOwnerIdentityPubkey(ownerPub).
		SetOwnerSigningPubkey(ownerSigningPub).
		SetRawTx(rawTxBytes).
		SetVout(0).
		SetSigningKeyshare(keyshare).
		Save(ctx)
	require.NoError(t, err)

	return leaf, keyshare, keysharePriv, keysharePub
}

func TestTweakLeafKey(t *testing.T) {
	ctx, client := db.NewTestSQLiteContext(t)
	entTx, err := ent.GetTxFromContext(ctx)
	require.NoError(t, err)
	rng := rand.NewChaCha8([32]byte{})

	leaf, keyshare, _, keysharePub := setupLeafForTweakTest(t, ctx, rng)

	// Build a degree-0 tweak polynomial g(x) = tweakPriv (a single coefficient).
	// For this polynomial, f(i)·G = tweakPub at every i, so the pubkey_shares_tweak
	// entry for operator1 (polynomial index 1) is tweakPub.
	tweakPriv := keys.MustGeneratePrivateKeyFromRand(rng)
	tweakPub := tweakPriv.Public()

	req := &spark.SendLeafKeyTweak{
		LeafId: leaf.ID.String(),
		SecretShareTweak: &spark.SecretShare{
			SecretShare: tweakPriv.Serialize(),
			Proofs:      [][]byte{tweakPub.Serialize()},
		},
		PubkeySharesTweak: map[string][]byte{
			"operator1": tweakPub.Serialize(),
		},
	}

	treeNodeUpdate, err := helper.TweakLeafKeyUpdate(ctx, singleOperatorConfig("operator1"), leaf, req)
	require.NoError(t, err)

	err = treeNodeUpdate.Exec(ctx)
	require.NoError(t, err)

	err = entTx.Commit()
	require.NoError(t, err)

	updatedLeaf, err := client.Client.TreeNode.Get(ctx, leaf.ID)
	require.NoError(t, err)
	assert.NotNil(t, updatedLeaf)

	updatedKeyshare, err := client.Client.SigningKeyshare.Get(ctx, keyshare.ID)
	require.NoError(t, err)
	assert.NotNil(t, updatedKeyshare)

	// New secret share = original + tweak.
	expectedNewSecretShare := keyshare.SecretShare.Add(tweakPriv)
	assert.NotNil(t, updatedKeyshare.SecretShare)
	assert.Equal(t, expectedNewSecretShare, *updatedKeyshare.SecretShare)

	// New combined pubkey = original + tweakPub.
	expectedNewPublicKey := keysharePub.Add(tweakPub)
	assert.Equal(t, expectedNewPublicKey, updatedKeyshare.PublicKey)

	// New per-operator public share = original + tweakPub (degree-0 polynomial).
	expectedNewPublicShares := make(map[string]keys.Public)
	for op, share := range keyshare.PublicShares {
		expectedNewPublicShares[op] = share.Add(tweakPub)
	}
	assert.Equal(t, expectedNewPublicShares, updatedKeyshare.PublicShares)
}

func TestTweakLeafKey_EmptySecretShareTweakProofsList(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	rng := rand.NewChaCha8([32]byte{})

	leaf, _, tweakPrivLike, _ := setupLeafForTweakTest(t, ctx, rng)
	_ = tweakPrivLike // unused; we just need the leaf
	tweakPriv := keys.MustGeneratePrivateKeyFromRand(rng)
	pubkeyShareTweakPub := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	req := &spark.SendLeafKeyTweak{
		LeafId: leaf.ID.String(),
		SecretShareTweak: &spark.SecretShare{
			SecretShare: tweakPriv.Serialize(),
			Proofs:      [][]byte{},
		},
		PubkeySharesTweak: map[string][]byte{
			"operator1": pubkeyShareTweakPub.Serialize(),
		},
	}

	_, err := helper.TweakLeafKeyUpdate(ctx, singleOperatorConfig("operator1"), leaf, req)
	require.ErrorContains(t, err, "no proofs provided for secret share tweak for leaf")
}

// TestTweakLeafKey_RejectsPubkeySharesTweakInconsistentWithProofs is a
// regression test for the prod incident on 2026-05-15. Without the
// `ValidatePubkeySharesTweak` check, a client could hand each SO a per-id
// `pubkey_shares_tweak` map drawn from a *different* polynomial than the one
// committed to by `SecretShareTweak.Proofs` (sharing only the constant term),
// causing `signing_keyshares.public_shares` to silently diverge across SOs.
// The downstream FROST aggregate signature then fails with
// "calculated R point was not given R" the next time the leaf is signed.
func TestTweakLeafKey_RejectsPubkeySharesTweakInconsistentWithProofs(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	rng := rand.NewChaCha8([32]byte{})

	leaf, _, _, _ := setupLeafForTweakTest(t, ctx, rng)

	tweakPriv := keys.MustGeneratePrivateKeyFromRand(rng)
	tweakPub := tweakPriv.Public()
	// pubkeyShareTweakPub is a fresh random key with no relation to the
	// polynomial committed by Proofs = [tweakPub]. For a degree-0 polynomial,
	// every operator's evaluation is tweakPub, so providing anything else for
	// operator1 must be rejected.
	pubkeyShareTweakPub := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	require.False(t, pubkeyShareTweakPub.Equals(tweakPub),
		"sanity check: the adversarial pubkey_shares_tweak must differ from the polynomial evaluation")

	req := &spark.SendLeafKeyTweak{
		LeafId: leaf.ID.String(),
		SecretShareTweak: &spark.SecretShare{
			SecretShare: tweakPriv.Serialize(),
			Proofs:      [][]byte{tweakPub.Serialize()},
		},
		PubkeySharesTweak: map[string][]byte{
			"operator1": pubkeyShareTweakPub.Serialize(),
		},
	}

	_, err := helper.TweakLeafKeyUpdate(ctx, singleOperatorConfig("operator1"), leaf, req)
	require.ErrorContains(t, err, "inconsistent with secret_share_tweak proofs")
}

// TestTweakLeafKey_RejectsPubkeySharesTweakWithMissingOperator verifies that
// the validation rejects maps that don't cover every operator in the
// cluster — otherwise the loop in `(*SigningKeyshare).TweakKeyShare` would
// silently `Add` the zero public point to that operator's recorded public
// share, leaving the cluster in an inconsistent state without ever raising
// an error.
func TestTweakLeafKey_RejectsPubkeySharesTweakWithMissingOperator(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	rng := rand.NewChaCha8([32]byte{})

	leaf, _, _, _ := setupLeafForTweakTest(t, ctx, rng)

	tweakPriv := keys.MustGeneratePrivateKeyFromRand(rng)
	tweakPub := tweakPriv.Public()

	// Two-operator config; the wire map only covers one of them.
	cfg := &so.Config{
		SigningOperatorMap: map[string]*so.SigningOperator{
			"operator1": {ID: 0, Identifier: "operator1"},
			"operator2": {ID: 1, Identifier: "operator2"},
		},
	}

	req := &spark.SendLeafKeyTweak{
		LeafId: leaf.ID.String(),
		SecretShareTweak: &spark.SecretShare{
			SecretShare: tweakPriv.Serialize(),
			Proofs:      [][]byte{tweakPub.Serialize()},
		},
		PubkeySharesTweak: map[string][]byte{
			"operator1": tweakPub.Serialize(),
		},
	}

	_, err := helper.TweakLeafKeyUpdate(ctx, cfg, leaf, req)
	require.ErrorContains(t, err, "pubkey_shares_tweak has 1 entries, expected one per operator (2)")
}

// TestValidatePubkeySharesTweak_AcceptsDegree1Polynomial exercises the
// non-trivial polynomial case: a true 2-of-N tweak split where each operator
// receives a distinct share f(i)·G.
func TestValidatePubkeySharesTweak_AcceptsDegree1Polynomial(t *testing.T) {
	const numOperators = 3
	const threshold = 2
	fieldModulus := secp256k1.S256().N

	rng := rand.NewChaCha8([32]byte{7})
	tweakSecret := keys.MustGeneratePrivateKeyFromRand(rng)
	shares, err := secretsharing.SplitSecretWithProofs(
		new(big.Int).SetBytes(tweakSecret.Serialize()),
		fieldModulus,
		threshold,
		numOperators,
	)
	require.NoError(t, err)
	require.Len(t, shares[0].Proofs, threshold,
		"degree-(t-1) polynomial commits to %d coefficients", threshold)

	identifiers := []string{
		"0000000000000000000000000000000000000000000000000000000000000001",
		"0000000000000000000000000000000000000000000000000000000000000002",
		"0000000000000000000000000000000000000000000000000000000000000003",
	}
	pubKeySharesTweak := make(map[string]keys.Public, numOperators)
	for i, id := range identifiers {
		sharePriv, err := keys.PrivateKeyFromBigInt(shares[i].Share)
		require.NoError(t, err)
		pubKeySharesTweak[id] = sharePriv.Public()
	}

	cfg := &so.Config{SigningOperatorMap: map[string]*so.SigningOperator{}}
	for i, id := range identifiers {
		cfg.SigningOperatorMap[id] = &so.SigningOperator{ID: uint64(i), Identifier: id}
	}

	require.NoError(t, helper.ValidatePubkeySharesTweak(cfg, shares[0].Proofs, pubKeySharesTweak),
		"legitimate per-operator shares of the proofs polynomial must validate")

	// Swap one entry for a different polynomial's share with the same constant term.
	otherShares, err := secretsharing.SplitSecretWithProofs(
		new(big.Int).SetBytes(tweakSecret.Serialize()),
		fieldModulus, threshold, numOperators,
	)
	require.NoError(t, err)
	require.Equal(t, shares[0].Proofs[0], otherShares[0].Proofs[0],
		"both polynomials commit to the same constant term")
	otherSharePriv, err := keys.PrivateKeyFromBigInt(otherShares[1].Share)
	require.NoError(t, err)
	pubKeySharesTweak[identifiers[1]] = otherSharePriv.Public()

	err = helper.ValidatePubkeySharesTweak(cfg, shares[0].Proofs, pubKeySharesTweak)
	require.ErrorContains(t, err, "inconsistent with secret_share_tweak proofs")
}
