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
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// TestClaimTransferV2_SettleAtomicity_KeysharesConsistentAcrossSOs is a
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
//   - leaf.KeyTweak is stored by ClaimTransfer via
//     persistCoordinatorClaimKeyTweak with its own commit (durable across
//     outer cancellation/rollback).
//   - InitiateSettleReceiverKeyTweak and SettleReceiverKeyTweak no longer
//     entTx.Commit() mid-flow; Phase 1 SELF, Phase 2 fan-out, and Phase 2
//     SELF all share one outer tx that holds the row lock throughout.
//
// What this test asserts end-to-end:
//
//  1. A normal `claim_transfer` to ClaimTransferV2 completes successfully.
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
func TestClaimTransferV2_SettleAtomicity_KeysharesConsistentAcrossSOs(t *testing.T) {
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
	require.Len(t, pending.Transfers, 1)
	receiverTransfer := pending.Transfers[0]
	require.Equal(t, senderTransfer.Id, receiverTransfer.Id)

	finalLeafPrivKey := keys.GeneratePrivateKey()
	claimLeaf := wallet.LeafKeyTweak{
		Leaf:              receiverTransfer.Leaves[0].Leaf,
		SigningPrivKey:    newLeafPrivKey,
		NewSigningPrivKey: finalLeafPrivKey,
	}

	// Drive the unified claim_transfer end-to-end. The settle flow now
	// holds the FOR UPDATE row lock across Phase 1 SELF + Phase 2 fan-out
	// + Phase 2 SELF (no mid-flow entTx.Commit), so this must succeed
	// without the "invalid status SENDER_KEY_TWEAKED" race seen in prod.
	claimedTransfer, err := wallet.ClaimTransferV2(receiverCtx, receiverTransfer, receiverConfig, []wallet.LeafKeyTweak{claimLeaf})
	require.NoError(t, err, "ClaimTransferV2 must succeed under the new atomic settle flow")
	require.Equal(t, "TRANSFER_STATUS_COMPLETED", claimedTransfer.Status.String())
	require.Len(t, claimedTransfer.Leaves, 1)

	// Verify keyshare consistency across SOs for the claimed leaf.
	leafID, err := uuid.Parse(claimLeaf.Leaf.Id)
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

// TestClaimTransferV2_StrandedRKTRollsBackOnRetryThenSucceeds pins down the
// recovery contract for the wedged-RKT state the prior "override-on-retry"
// design tried (and failed) to handle. Scenario:
//
//  1. Attempt 1 calls claim_transfer. persistCoordinatorClaimKeyTweak's T1
//     commits proofs_X plus transfer.status = RECEIVER_KEY_TWEAKED on the
//     coordinator (T1 is durable across outer T2 rollback).
//  2. Something downstream fails (Phase 1 fan-out hits Unavailable, the
//     process dies, etc.) so the rest of the 2PC never runs. Coordinator
//     is left stranded at RKT with proofs_X stored; no peer has committed
//     Phase 1.
//  3. The user retries. wallet.ClaimTransferV2 / prepareClaimLeafKeyTweaks
//     reseeds the polynomial on every call, so attempt 2 carries a fresh
//     proofs_Y in its claim_package.
//
// Behavior under test (driven entirely through the public ClaimTransfer
// API):
//
//   - Attempt 2 must NOT silently install proofs_Y on the coordinator.
//     With RKT in the useStoredKeyTweaks=true set, attempt 2 ignores the
//     fresh claim_package, drives the 2PC with the anchored proofs_X,
//     finds peers at SKT without a claim_package, and rolls the whole
//     cluster back to SKT. The error surfaces with "rolled back" to the
//     client.
//
//   - A third attempt with fresh proofs_Z then succeeds end-to-end. This
//     succeeding IS the observable signal that the rollback actually
//     cleared coordinator state: if it hadn't, attempt 3 would see RKT +
//     proofs_X and fail the same way attempt 2 did.
//
//   - Cluster keyshare view is internally consistent after recovery.
//
// This test fails under the prior "override unconditionally" behavior
// (attempt 2 would install proofs_Y on the coordinator and Phase 2 would
// apply divergent keyshares across SOs — the unrecoverable state observed
// in prod against transfer 019e2705-4b37-7f6f-a8c1-bae077a82d5a). It
// passes once persistCoordinatorClaimKeyTweak no-ops on populated
// leaf.KeyTweak AND useStoredKeyTweaks=true at RKT.
//
// Staging note: the wedged-RKT state can only be produced by a transient
// downstream failure between persistCoordinatorClaimKeyTweak's commit and
// the rest of the 2PC. There's no public API knob to inject that failure,
// so the test writes the post-T1 state directly via
// stageEarlyCommittedKeyTweakOnOperator. The actual behavior under test
// runs through wallet.ClaimTransferV2.
func TestClaimTransferV2_StrandedRKTRollsBackOnRetryThenSucceeds(t *testing.T) {
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
	require.Len(t, pending.Transfers, 1)
	receiverTransfer := pending.Transfers[0]

	finalLeafPrivKey := keys.GeneratePrivateKey()
	claimLeaf := wallet.LeafKeyTweak{
		Leaf:              receiverTransfer.Leaves[0].Leaf,
		SigningPrivKey:    newLeafPrivKey,
		NewSigningPrivKey: finalLeafPrivKey,
	}

	// Stage the coordinator's persisted state to mimic attempt 1's wedge:
	// leaf.KeyTweak populated with proofs_X, transfer.status = RKT, only
	// on the coordinator. Peers stay at SKT — no peer has yet committed
	// Phase 1.
	stagedTweaks := buildClaimLeafTweaksAcrossOperators(t, receiverConfig, claimLeaf)
	coordinator := receiverConfig.SigningOperators[receiverConfig.CoordinatorIdentifier]
	stageEarlyCommittedKeyTweakOnOperator(
		t, coordinator,
		senderTransfer.Id, claimLeaf.Leaf.Id,
		stagedTweaks[receiverConfig.CoordinatorIdentifier],
	)

	// Attempt 2: drive the unified claim with fresh polynomial P_Y. The
	// coordinator must ignore the fresh proofs (useStoredKeyTweaks=true at
	// RKT), proceed with anchored proofs_X, find peers at SKT without a
	// claim_package, and roll the whole cluster back to SKT.
	_, err = wallet.ClaimTransferV2(receiverCtx, receiverTransfer, receiverConfig, []wallet.LeafKeyTweak{claimLeaf})
	require.Error(t, err, "stranded-RKT retry must surface the rollback error rather than silently overriding the anchored polynomial")
	assert.Contains(t, err.Error(), "rolled back",
		"settle phase must report ROLLBACK to the client; got: %v", err)

	// Attempt 3: fresh polynomial P_Z. If the rollback in attempt 2
	// actually cleared coordinator state (RKT→SKT, leaf.KeyTweak cleared
	// via revertClaimTransfer + the explicit settle-phase commit), this
	// is indistinguishable from a first-ever claim and must succeed
	// end-to-end. Conversely, if rollback didn't run, attempt 3 would hit
	// the same wedged-RKT state and fail with the same rollback error.
	claimedTransfer, err := wallet.ClaimTransferV2(receiverCtx, receiverTransfer, receiverConfig, []wallet.LeafKeyTweak{claimLeaf})
	require.NoError(t, err, "post-rollback retry with fresh polynomial must succeed — attempt 3 succeeding is the observable signal that attempt 2's ROLLBACK cleared the stranded RKT state")
	require.Equal(t, "TRANSFER_STATUS_COMPLETED", claimedTransfer.Status.String())

	// Cluster keyshare view must be internally consistent after recovery
	// (same invariant as TestClaimTransferV2_SettleAtomicity_*).
	leafID, err := uuid.Parse(claimLeaf.Leaf.Id)
	require.NoError(t, err)
	keysharesByOperatorID := readKeyshareFromAllOperators(t, receiverConfig, leafID)
	require.NotEmpty(t, keysharesByOperatorID)
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
		assert.True(t, ks.PublicKey.Equals(ref.PublicKey),
			"keyshare PublicKey diverges between operators %d and %d after stranded-RKT recovery", refOpID, opID)
		for identifier, refShare := range ref.PublicShares {
			thisShare, ok := ks.PublicShares[identifier]
			require.True(t, ok, "operator %d missing PublicShares entry for %s", opID, identifier)
			assert.True(t, thisShare.Equals(refShare),
				"PublicShares[%s] diverges across operators %d and %d after stranded-RKT recovery",
				identifier, refOpID, opID)
		}
	}
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
	require.Len(t, pending.Transfers, 1)
	receiverTransfer := pending.Transfers[0]

	finalLeafPrivKey := keys.GeneratePrivateKey()
	claimLeaf := wallet.LeafKeyTweak{
		Leaf:              receiverTransfer.Leaves[0].Leaf,
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
		senderTransfer.Id, claimLeaf.Leaf.Id,
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
			LeafId: leaf.Leaf.Id,
			SecretShareTweak: &sparkpb.SecretShare{
				SecretShare: secretShareBytes,
				Proofs:      share.Proofs,
			},
			PubkeySharesTweak: pubkeySharesTweak,
		}
	}
	return result
}

// stageEarlyCommittedKeyTweakOnOperator simulates the post-
// persistCoordinatorClaimKeyTweak / pre-Phase-2-complete state on a
// single operator: writes the serialized ClaimLeafKeyTweak to that
// operator's transfer_leafs.key_tweak row for the given leaf and
// transitions the transfer status to RECEIVER_KEY_TWEAKED.
func stageEarlyCommittedKeyTweakOnOperator(
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
		SetStatus(st.TransferStatusReceiverKeyTweaked).
		Save(t.Context())
	require.NoError(t, err, "operator %d: bump transfer status to RKT", operator.ID)
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
