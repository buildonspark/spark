package grpctest

import (
	"cmp"
	"maps"
	"math/big"
	"slices"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	secretsharing "github.com/lightsparkdev/spark/common/secret_sharing"
	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	enttransfer "github.com/lightsparkdev/spark/so/ent/transfer"
	enttransferleaf "github.com/lightsparkdev/spark/so/ent/transferleaf"
	enttreenode "github.com/lightsparkdev/spark/so/ent/treenode"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// TestClaimTransfer_SettleAtomicity_KeysharesConsistentAcrossSOs is a
// regression test for a 2PC atomicity bug in `claim_transfer`. The pre-fix
// flow did:
//
//	settleReceiverKeyTweakInternal
//	├── Phase 1 fan-out               (peers commit via gRPC middleware)
//	├── Phase 1 SELF
//	│   └── entTx.Commit()             ← released the coordinator's FOR UPDATE row lock
//	├── Phase 2 fan-out                ← concurrent ROLLBACK could reset the
//	│                                    coordinator to SENDER_KEY_TWEAKED here
//	└── Phase 2 SELF                   ← saw mismatched status, failed
//	    └── entTx.Commit()
//
// After the fix:
//   - leaf.KeyTweak is durably stored before the settle 2PC starts
//     (ClaimTransferTweakKeys in the multi-call flow).
//   - InitiateSettleReceiverKeyTweak and SettleReceiverKeyTweak no longer
//     entTx.Commit() mid-flow; Phase 1 SELF, Phase 2 fan-out, and Phase 2
//     SELF all share one outer tx that holds the row lock throughout.
//
// What this test asserts end-to-end:
//
//  1. A normal multi-call claim completes successfully.
//  2. Every SO's stored `signing_keyshares.public_shares` row for the
//     claimed leaf agrees with every other SO's view of that same
//     polynomial — the invariant that broke in the bug, where the
//     coordinator's PublicShares disagreed with peers' view of the
//     coordinator's pubshare after divergent commits across the 2PC's
//     two halves.
//  3. Every SO's `signing_keyshares.public_key` matches — the constant
//     term invariant, which holds even when the per-share divergence is
//     present and is therefore not by itself sufficient evidence that the
//     bug is fixed. We check it as a sanity guard.
//
// Flow note: drives the multi-call claim flow (ClaimTransferTweakKeys +
// ClaimTransferSignRefunds + FinalizeTransfer via wallet.ClaimTransfer),
// which is the remaining production user of settleReceiverKeyTweakInternal.
// The single-call ClaimTransfer RPC routes through the consensus engine and
// no longer touches this settle path.
func TestClaimTransfer_SettleAtomicity_KeysharesConsistentAcrossSOs(t *testing.T) {
	if !sparktesting.HasLocalSparkIngressHost() {
		t.Skip("skipping cross-operator integration test without minikube ingress (set SPARK_LOCAL_INGRESS_HOST)")
	}

	// Sender side
	senderConfig := wallet.NewTestWalletConfig(t)
	leafPrivKey := keys.GeneratePrivateKey()
	rootNode, err := wallet.CreateNewTree(senderConfig, faucet, leafPrivKey, amountSatsToSend)
	require.NoError(t, err, "failed to create new tree")

	newLeafPrivKey := keys.GeneratePrivateKey()
	receiverPrivKey := keys.GeneratePrivateKey()
	receiverConfig := wallet.NewTestWalletConfigWithIdentityKey(t, receiverPrivKey)

	senderTransfer, err := wallet.SendTransferWithKeyTweaks(
		t.Context(), senderConfig,
		[]wallet.LeafKeyTweak{{Leaf: rootNode, SigningPrivKey: leafPrivKey, NewSigningPrivKey: newLeafPrivKey}},
		receiverPrivKey.Public(),
		time.Now().Add(10*time.Minute),
	)
	require.NoError(t, err, "failed to send transfer")

	// Receiver side
	receiverToken, err := wallet.AuthenticateWithServer(t.Context(), receiverConfig)
	require.NoError(t, err, "failed to authenticate receiver")
	receiverCtx := wallet.ContextWithToken(t.Context(), receiverToken)

	pending, err := wallet.QueryPendingTransfers(receiverCtx, receiverConfig)
	require.NoError(t, err, "failed to query pending transfers")
	require.Len(t, pending.GetTransfers(), 1)
	receiverTransfer := pending.GetTransfers()[0]
	require.Equal(t, senderTransfer.GetId(), receiverTransfer.GetId())

	finalLeafPrivKey := keys.GeneratePrivateKey()
	claimLeaf := wallet.LeafKeyTweak{
		Leaf:              receiverTransfer.GetLeaves()[0].GetLeaf(),
		SigningPrivKey:    newLeafPrivKey,
		NewSigningPrivKey: finalLeafPrivKey,
	}

	// Drive the multi-call claim end-to-end. The settle flow
	// holds the FOR UPDATE row lock across Phase 1 SELF + Phase 2 fan-out
	// + Phase 2 SELF (no mid-flow entTx.Commit), so this must succeed
	// without the "invalid status SENDER_KEY_TWEAKED" race seen in prod.
	claimedNodes, err := wallet.ClaimTransfer(receiverCtx, receiverTransfer, receiverConfig, []wallet.LeafKeyTweak{claimLeaf})
	require.NoError(t, err, "multi-call claim must succeed under the atomic settle flow")
	require.Len(t, claimedNodes, 1)

	// Verify keyshare consistency across SOs for the claimed leaf.
	leafID, err := uuid.Parse(claimLeaf.Leaf.GetId())
	require.NoError(t, err)
	assertKeysharesConsistentAcrossOperators(t, receiverConfig, leafID)
}

// assertKeysharesConsistentAcrossOperators asserts every SO's local view of
// the leaf's SigningKeyshare agrees with every other SO's — both the
// constant-term invariant (PublicKey) and per-operator pubshare agreement
// (PublicShares). The latter is what breaks when a claim commits divergent
// tweak polynomials across SOs: same aggregate pubkey, per-SO shares on
// different polynomials, after which any signing set spanning both
// polynomials produces invalid signatures.
func assertKeysharesConsistentAcrossOperators(t *testing.T, config *wallet.TestWalletConfig, leafID uuid.UUID) {
	t.Helper()
	keysharesByOperatorID := readKeyshareFromAllOperators(t, config, leafID)
	require.NotEmpty(t, keysharesByOperatorID)

	// Pick any one operator as the reference; all others must agree.
	var ref *ent.SigningKeyshare
	var refOpID uint64
	for opID, ks := range keysharesByOperatorID {
		ref = ks
		refOpID = opID
		break
	}
	require.NotNil(t, ref)

	for opID, ks := range keysharesByOperatorID {
		if opID == refOpID {
			continue
		}

		// Constant-term invariant: total verifying pubkey for the leaf is
		// the same across SOs. This holds even when divergence on the
		// per-operator pubshares is present (both polynomials encode the
		// same secret), so it isn't sufficient on its own — a divergence
		// here would indicate a different and more severe bug.
		assert.True(t, ks.PublicKey.Equals(ref.PublicKey),
			"keyshare PublicKey diverges between operator %d and operator %d for leaf %s\n  ref:  %x\n  this: %x",
			refOpID, opID, leafID, ref.PublicKey.Serialize(), ks.PublicKey.Serialize())

		// Per-operator pubshare agreement: every SO must hold the SAME
		// view of every other SO's post-tweak public share. Compare sizes
		// first so an extra entry on either side fails regardless of which
		// operator the map iteration picked as reference.
		require.Len(t, ks.PublicShares, len(ref.PublicShares),
			"operator %d holds %d PublicShares entries but operator %d holds %d",
			opID, len(ks.PublicShares), refOpID, len(ref.PublicShares))
		for identifier, refShare := range ref.PublicShares {
			thisShare, ok := ks.PublicShares[identifier]
			require.True(t, ok,
				"operator %d missing PublicShares entry for identifier %s", opID, identifier)
			assert.True(t, thisShare.Equals(refShare),
				"PublicShares[%s] diverges: operator %d view %x vs operator %d view %x — "+
					"this is the divergent-commit state the fix prevents",
				identifier, refOpID, refShare.Serialize(), opID, thisShare.Serialize())
		}
	}
}

// readKeyshareFromAllOperators reads each operator's local view of the
// SigningKeyshare row associated with the given leaf and returns them
// keyed by operator ID.
func readKeyshareFromAllOperators(
	t *testing.T, config *wallet.TestWalletConfig, leafID uuid.UUID,
) map[uint64]*ent.SigningKeyshare {
	t.Helper()
	result := make(map[uint64]*ent.SigningKeyshare, len(config.SigningOperators))
	for _, op := range orderedOperators(config) {
		client := db.NewPostgresEntClientForIntegrationTest(t, operatorDatabasePath(t, int(op.ID)))
		t.Cleanup(func() { _ = client.Close() })
		leaf, err := client.TreeNode.Get(t.Context(), leafID)
		require.NoError(t, err, "operator %d: load leaf %s", op.ID, leafID)
		ks, err := leaf.QuerySigningKeyshare().Only(t.Context())
		require.NoError(t, err, "operator %d: load keyshare for leaf %s", op.ID, leafID)
		result[op.ID] = ks
	}
	return result
}

// TestClaimTransferV2_FreshPolynomialHealsPeerLockedAtRKL pins what a claim
// retry must do when a prior attempt stranded ONE peer at
// RECEIVER_KEY_TWEAK_LOCKED with polynomial proofs_X while the other SOs
// never locked (a partial Phase-1 whose rollback missed the peer — the
// durable mid-2PC state reachable in production, and the trigger of the
// 2026-07 prod incident where a retry silently committed proofs_X on the
// stranded SO and fresh proofs_Y everywhere else, permanently diverging the
// leaf's keyshare).
//
// Under the digest-unanimity fix, the stranded peer's Prepare OVERWRITES its
// stale proofs_X with the freshly validated package's proofs_Y (the stale
// tweak is not applied anywhere, so the fresh package is authoritative),
// every SO reports the proofs_Y digest, the coordinator verifies unanimity,
// and Commit binds the digest so no SO can apply anything else. The claim
// must therefore SUCCEED and leave every operator's keyshare view identical.
//
// Setup: stage a non-coordinator peer at RECEIVER_KEY_TWEAK_LOCKED with
// proofs_X. ClaimTransferV2 then dispatches with fresh proofs_Y. Assert the
// claim completes and all operators' keyshares agree.
func TestClaimTransferV2_FreshPolynomialHealsPeerLockedAtRKL(t *testing.T) {
	senderConfig := wallet.NewTestWalletConfig(t)
	leafPrivKey := keys.GeneratePrivateKey()
	rootNode, err := wallet.CreateNewTree(senderConfig, faucet, leafPrivKey, amountSatsToSend)
	require.NoError(t, err, "failed to create new tree")

	newLeafPrivKey := keys.GeneratePrivateKey()
	receiverPrivKey := keys.GeneratePrivateKey()
	receiverConfig := wallet.NewTestWalletConfigWithIdentityKey(t, receiverPrivKey)

	senderTransfer, err := wallet.SendTransferWithKeyTweaks(
		t.Context(), senderConfig,
		[]wallet.LeafKeyTweak{{Leaf: rootNode, SigningPrivKey: leafPrivKey, NewSigningPrivKey: newLeafPrivKey}},
		receiverPrivKey.Public(),
		time.Now().Add(10*time.Minute),
	)
	require.NoError(t, err, "failed to send transfer")

	receiverToken, err := wallet.AuthenticateWithServer(t.Context(), receiverConfig)
	require.NoError(t, err, "failed to authenticate receiver")
	receiverCtx := wallet.ContextWithToken(t.Context(), receiverToken)

	pending, err := wallet.QueryPendingTransfers(receiverCtx, receiverConfig)
	require.NoError(t, err, "failed to query pending transfers")
	require.Len(t, pending.GetTransfers(), 1)
	receiverTransfer := pending.GetTransfers()[0]

	finalLeafPrivKey := keys.GeneratePrivateKey()
	claimLeaf := wallet.LeafKeyTweak{
		Leaf:              receiverTransfer.GetLeaves()[0].GetLeaf(),
		SigningPrivKey:    newLeafPrivKey,
		NewSigningPrivKey: finalLeafPrivKey,
	}

	// Build polynomial P_X across all operators (same Proofs, distinct
	// per-operator SecretShare).
	stagedTweaks := buildClaimLeafTweaksAcrossOperators(t, receiverConfig, claimLeaf)

	// Stage a non-coordinator peer at RECEIVER_KEY_TWEAK_LOCKED with
	// proofs_X — the durable middle-of-2PC state we need ValidateKeyTweakProof
	// to defend against.
	var stagedPeer *so.SigningOperator
	for identifier, op := range receiverConfig.SigningOperators {
		if identifier == receiverConfig.CoordinatorIdentifier {
			continue
		}
		stagedPeer = op
		break
	}
	require.NotNil(t, stagedPeer, "test cluster must have at least one non-coordinator peer")
	stagePeerLockedAtRKL(
		t, stagedPeer,
		senderTransfer.GetId(), claimLeaf.Leaf.GetId(),
		stagedTweaks[stagedPeer.Identifier],
	)

	// Drive the unified claim — wallet.ClaimTransferV2 generates fresh
	// polynomial P_Y. The RKL peer must overwrite its stale proofs_X with
	// P_Y during Prepare, the digest-unanimity check must pass, and the
	// claim must complete with every SO on P_Y.
	claimed, err := wallet.ClaimTransferV2(receiverCtx, receiverTransfer, receiverConfig, []wallet.LeafKeyTweak{claimLeaf})
	require.NoError(t, err, "a fresh-polynomial retry must heal a peer stranded at RKL with a stale polynomial, not wedge or diverge")
	require.NotNil(t, claimed)

	leafID, err := uuid.Parse(claimLeaf.Leaf.GetId())
	require.NoError(t, err)
	assertKeysharesConsistentAcrossOperators(t, receiverConfig, leafID)
}

// buildClaimLeafTweaksAcrossOperators returns a polynomial-derived
// ClaimLeafKeyTweak entry for every operator in `config`, sharing a single
// freshly-split polynomial across all operators (same Proofs[], distinct
// per-operator SecretShare).
func buildClaimLeafTweaksAcrossOperators(
	t *testing.T,
	config *wallet.TestWalletConfig,
	leaf wallet.LeafKeyTweak,
) map[string]*sparkpb.ClaimLeafKeyTweak {
	t.Helper()
	privKeyTweak := leaf.SigningPrivKey.Sub(leaf.NewSigningPrivKey)
	shares, err := secretsharing.SplitSecretWithProofs(
		new(big.Int).SetBytes(privKeyTweak.Serialize()),
		secp256k1.S256().N,
		config.Threshold,
		len(config.SigningOperators),
	)
	require.NoError(t, err)

	pubkeySharesTweak := make(map[string][]byte, len(config.SigningOperators))
	for identifier, op := range config.SigningOperators {
		var share *secretsharing.VerifiableSecretShare
		for _, s := range shares {
			if s.Index.Cmp(big.NewInt(int64(op.ID+1))) == 0 {
				share = s
				break
			}
		}
		require.NotNil(t, share)
		priv, err := keys.PrivateKeyFromBigInt(share.GetShare())
		require.NoError(t, err)
		pubkeySharesTweak[identifier] = priv.Public().Serialize()
	}

	result := make(map[string]*sparkpb.ClaimLeafKeyTweak, len(config.SigningOperators))
	for identifier, op := range config.SigningOperators {
		var share *secretsharing.VerifiableSecretShare
		for _, s := range shares {
			if s.Index.Cmp(big.NewInt(int64(op.ID+1))) == 0 {
				share = s
				break
			}
		}
		require.NotNil(t, share)
		secretShareBytes := make([]byte, 32)
		share.Share.FillBytes(secretShareBytes)
		result[identifier] = &sparkpb.ClaimLeafKeyTweak{
			LeafId: leaf.Leaf.GetId(),
			SecretShareTweak: &sparkpb.SecretShare{
				SecretShare: secretShareBytes,
				Proofs:      share.Proofs,
			},
			PubkeySharesTweak: pubkeySharesTweak,
		}
	}
	return result
}

// stagePeerLockedAtRKL writes the given ClaimLeafKeyTweak to the peer's
// transfer_leafs.key_tweak row and transitions the transfer status to
// RECEIVER_KEY_TWEAK_LOCKED — the durable post-Phase-1 state a peer
// arrives at when its middleware commits InitiateSettleReceiverKeyTweak
// successfully while the coordinator's outer 2PC fails or aborts before
// rollback can run. Used to simulate the partial-Phase-1-success state
// that the anti-replay invariant must defend against on a retry.
func stagePeerLockedAtRKL(
	t *testing.T,
	operator *so.SigningOperator,
	transferIDStr string,
	leafIDStr string,
	stagedTweak *sparkpb.ClaimLeafKeyTweak,
) {
	t.Helper()
	transferID, err := uuid.Parse(transferIDStr)
	require.NoError(t, err)
	leafID, err := uuid.Parse(leafIDStr)
	require.NoError(t, err)

	client := db.NewPostgresEntClientForIntegrationTest(t, operatorDatabasePath(t, int(operator.ID)))
	t.Cleanup(func() { _ = client.Close() })

	stagedBytes, err := proto.Marshal(stagedTweak)
	require.NoError(t, err)

	transferLeaf, err := client.TransferLeaf.Query().
		Where(
			enttransferleaf.HasTransferWith(enttransfer.IDEQ(transferID)),
			enttransferleaf.HasLeafWith(enttreenode.IDEQ(leafID)),
		).
		Only(t.Context())
	require.NoError(t, err, "operator %d: locate transfer_leaf joining transfer %s and leaf %s",
		operator.ID, transferID, leafID)

	_, err = transferLeaf.Update().SetKeyTweak(stagedBytes).Save(t.Context())
	require.NoError(t, err, "operator %d: write staged leaf.KeyTweak", operator.ID)

	_, err = client.Transfer.UpdateOneID(transferID).
		SetStatus(st.TransferStatusReceiverKeyTweakLocked).
		Save(t.Context())
	require.NoError(t, err, "operator %d: bump transfer status to RKL", operator.ID)
}

// orderedOperators returns SOs sorted by their numeric ID so the reference SO picked by the test is stable across runs.
func orderedOperators(config *wallet.TestWalletConfig) []*so.SigningOperator {
	return slices.SortedFunc(maps.Values(config.SigningOperators), func(a, b *so.SigningOperator) int { return cmp.Compare(a.ID, b.ID) })
}
