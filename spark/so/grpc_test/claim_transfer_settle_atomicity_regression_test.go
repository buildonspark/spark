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
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	enttransfer "github.com/lightsparkdev/spark/so/ent/transfer"
	enttransferleaf "github.com/lightsparkdev/spark/so/ent/transferleaf"
	enttransferreceiver "github.com/lightsparkdev/spark/so/ent/transferreceiver"
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

// TestClaimTransferV2_DivergentStoredPolynomialRollsPeerBackToSKT verifies that
// a divergent stored polynomial aborts and rolls the staged peer back to SKT.
func TestClaimTransferV2_DivergentStoredPolynomialRollsPeerBackToSKT(t *testing.T) {
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

	claimed, err := wallet.ClaimTransferV2(receiverCtx, receiverTransfer, receiverConfig, []wallet.LeafKeyTweak{claimLeaf})
	require.Error(t, err)
	require.Nil(t, claimed)

	stagedClient := db.NewPostgresEntClientForIntegrationTest(t, operatorDatabasePath(t, int(stagedPeer.ID)))
	t.Cleanup(func() { _ = stagedClient.Close() })
	transferID, err := uuid.Parse(senderTransfer.GetId())
	require.NoError(t, err)
	stagedTransfer, err := stagedClient.Transfer.Get(t.Context(), transferID)
	require.NoError(t, err)
	assert.Equal(t, st.TransferStatusSenderKeyTweaked, stagedTransfer.Status)
	stagedLeaf, err := stagedClient.TransferLeaf.Query().
		Where(
			enttransferleaf.HasTransferWith(enttransfer.IDEQ(transferID)),
			enttransferleaf.HasLeafWith(enttreenode.IDEQ(uuid.MustParse(claimLeaf.Leaf.GetId()))),
		).
		Only(t.Context())
	require.NoError(t, err)
	assert.Empty(t, stagedLeaf.KeyTweak)

	leafID, err := uuid.Parse(claimLeaf.Leaf.GetId())
	require.NoError(t, err)
	assertKeysharesConsistentAcrossOperators(t, receiverConfig, leafID)
}

func TestClaimTransferV2_MatchingAppliedAndStagedPolynomialCompletes(t *testing.T) {
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
	matchingTweaks := buildClaimLeafTweaksAcrossOperators(t, receiverConfig, claimLeaf)

	var appliedPeer *so.SigningOperator
	for identifier, op := range receiverConfig.SigningOperators {
		if identifier == receiverConfig.CoordinatorIdentifier {
			continue
		}
		appliedPeer = op
		break
	}
	require.NotNil(t, appliedPeer, "test cluster must have at least one non-coordinator peer")
	stagePeerLockedAtRKL(
		t, appliedPeer,
		senderTransfer.GetId(), claimLeaf.Leaf.GetId(),
		matchingTweaks[appliedPeer.Identifier],
	)
	applyPeerClaimTweak(t, appliedPeer, senderTransfer.GetId(), receiverPrivKey.Public())

	tweaksByOperator := make(map[string][]*sparkpb.ClaimLeafKeyTweak, len(matchingTweaks))
	for identifier, tweak := range matchingTweaks {
		tweaksByOperator[identifier] = []*sparkpb.ClaimLeafKeyTweak{tweak}
	}
	transferID := uuid.MustParse(senderTransfer.GetId())
	claimPackage, err := wallet.PrepareClaimPackage(
		receiverCtx,
		receiverConfig,
		transferID,
		tweaksByOperator,
		[]wallet.LeafKeyTweak{{Leaf: claimLeaf.Leaf, SigningPrivKey: finalLeafPrivKey}},
	)
	require.NoError(t, err, "failed to prepare claim package")
	conn, err := receiverConfig.NewCoordinatorGRPCConnection()
	require.NoError(t, err)
	defer conn.Close()
	resp, err := sparkpb.NewSparkServiceClient(conn).ClaimTransfer(receiverCtx, &sparkpb.ClaimTransferRequest{
		TransferId:             senderTransfer.GetId(),
		OwnerIdentityPublicKey: receiverPrivKey.Public().Serialize(),
		ClaimPackage:           claimPackage,
	})
	require.NoError(t, err, "matching applied/staged post-tweak keyshares must complete the claim")
	require.Equal(t, sparkpb.TransferStatus_TRANSFER_STATUS_COMPLETED, resp.GetTransfer().GetStatus())

	assertKeysharesConsistentAcrossOperators(t, receiverConfig, uuid.MustParse(claimLeaf.Leaf.GetId()))
}

func applyPeerClaimTweak(
	t *testing.T,
	operator *so.SigningOperator,
	transferID string,
	receiverIdentityPublicKey keys.Public,
) {
	t.Helper()
	conn, err := operator.NewOperatorGRPCConnection()
	require.NoError(t, err)
	defer conn.Close()
	_, err = pbinternal.NewSparkInternalServiceClient(conn).SettleReceiverKeyTweak(
		t.Context(),
		&pbinternal.SettleReceiverKeyTweakRequest{
			TransferId:                transferID,
			Action:                    pbinternal.SettleKeyTweakAction_COMMIT,
			ReceiverIdentityPublicKey: receiverIdentityPublicKey.Serialize(),
		},
	)
	require.NoError(t, err, "failed to pre-apply matching claim tweak on operator %d", operator.ID)

	client := db.NewPostgresEntClientForIntegrationTest(t, operatorDatabasePath(t, int(operator.ID)))
	t.Cleanup(func() { _ = client.Close() })
	transferUUID := uuid.MustParse(transferID)
	receiver, err := client.TransferReceiver.Query().
		Where(
			enttransferreceiver.TransferIDEQ(transferUUID),
			enttransferreceiver.IdentityPubkeyEQ(receiverIdentityPublicKey),
		).
		Only(t.Context())
	require.NoError(t, err)
	require.Equal(t, st.TransferReceiverStatusKeyTweakApplied, receiver.Status,
		"operator %d must be in the applied half of the mixed-state fixture", operator.ID)
	transferLeaves, err := client.TransferLeaf.Query().
		Where(enttransferleaf.HasTransferWith(enttransfer.IDEQ(transferUUID))).
		All(t.Context())
	require.NoError(t, err)
	require.NotEmpty(t, transferLeaves)
	for _, transferLeaf := range transferLeaves {
		require.Empty(t, transferLeaf.KeyTweak,
			"operator %d must have consumed its stored tweak before the retry", operator.ID)
	}
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

// stagePeerLockedAtRKL simulates a peer whose Phase 1 committed before the
// coordinator aborted.
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
