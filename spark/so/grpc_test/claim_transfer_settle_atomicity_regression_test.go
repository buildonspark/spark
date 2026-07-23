package grpctest

import (
	"math/big"
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

	keysharesByOperatorID := readKeyshareFromAllOperators(t, receiverConfig, leafID)
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
		// view of every other SO's post-tweak public share. This is the
		// invariant the bug broke — the coordinator thought a peer held
		// P_X(idx)·G while the peer actually held P_Y(idx)·G after
		// divergent Phase 2 commits across the two halves of the 2PC.
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

// TestClaimTransferV2_FreshPolynomialRejectedWhenPeerLockedAtRKL is the
// anti-replay companion to the override-at-RKT test. The override-allowed
// pre-condition is "no peer has committed Phase 1 yet"; this test pins
// down what must still happen when that pre-condition is false.
//
// The "mid-2PC" state that's actually reachable in production is: attempt
// 1's Phase 1 fan-out partially succeeded (some peer durably committed
// Phase 1 with proofs_X — peer middleware committed RKL with proofs_X)
// before Phase 1 fan-out's aggregate error returned codes.Unavailable to
// the coordinator. Attempt 2 with a fresh polynomial proofs_Y must NOT
// silently override and apply proofs_Y on the peers that haven't locked
// yet while leaving the RKL peer holding proofs_X — that's the
// divergent-keyshare state the fix prevents.
//
// The protection comes from peer InitiateSettleReceiverKeyTweak's
// alreadyLocked branch combined with ValidateKeyTweakProof: a peer at RKL
// keeps its stored proofs_X, then validates the incoming request's proofs
// against them and returns AbortedConcurrentClaimConflict on mismatch. The
// coordinator promotes that to action=ROLLBACK and the 2PC cleanup runs.
//
// Setup: stage a non-coordinator peer at RECEIVER_KEY_TWEAK_LOCKED with
// proofs_X. ClaimTransferV2 then dispatches with fresh proofs_Y. Test
// asserts the call fails with the proof-mismatch error class — i.e. the
// peer-side check fired before any divergent commit could land.
func TestClaimTransferV2_FreshPolynomialRejectedWhenPeerLockedAtRKL(t *testing.T) {
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
	// polynomial P_Y, which must NOT silently overwrite the RKL peer's
	// proofs_X. Expect ValidateKeyTweakProof on the RKL peer to fire and
	// surface as the proof-mismatch error class.
	_, err = wallet.ClaimTransferV2(receiverCtx, receiverTransfer, receiverConfig, []wallet.LeafKeyTweak{claimLeaf})
	require.Error(t, err, "ClaimTransferV2 must reject a fresh-polynomial retry when a peer is locked at RKL with the prior polynomial")
	assert.Contains(t, err.Error(), "key tweak proof",
		"rejection must surface from ValidateKeyTweakProof on the locked peer; "+
			"the exact wording (\"key tweak proof for leaf %%s is invalid, the proof provided is not the same as key tweak proof\") "+
			"is what guards the divergent-commit failure mode the fix prevents.\n  got: %v", err)
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

// orderedOperators returns operators sorted by their numeric ID so the
// reference operator picked by the test is stable across runs.
func orderedOperators(config *wallet.TestWalletConfig) []*so.SigningOperator {
	ops := make([]*so.SigningOperator, 0, len(config.SigningOperators))
	for _, op := range config.SigningOperators {
		ops = append(ops, op)
	}
	// Simple insertion sort — n is small (≤5 in test envs).
	for i := 1; i < len(ops); i++ {
		j := i
		for j > 0 && ops[j-1].ID > ops[j].ID {
			ops[j-1], ops[j] = ops[j], ops[j-1]
			j--
		}
	}
	return ops
}
