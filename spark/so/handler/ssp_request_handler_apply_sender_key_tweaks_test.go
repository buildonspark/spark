//go:build lightspark

package handler

import (
	"math/big"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/stretchr/testify/require"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	secretsharing "github.com/lightsparkdev/spark/common/secret_sharing"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbssp "github.com/lightsparkdev/spark/proto/spark_ssp_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	enttransfer "github.com/lightsparkdev/spark/so/ent/transfer"
	enttransferleaf "github.com/lightsparkdev/spark/so/ent/transferleaf"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"google.golang.org/protobuf/proto"
)

func TestApplySenderKeyTweaks_RecoversApplyingSenderKeyTweak(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	rng := rand.NewChaCha8([32]byte{9})
	senderPub := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	receiverPub := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	leaf := createDbLeaf(t, ctx, true)
	originalOwnerSigningPubkey := leaf.node.OwnerSigningPubkey

	transfer, err := dbTx.Transfer.Create().
		SetNetwork(btcnetwork.Regtest).
		SetType(st.TransferTypeTransfer).
		SetStatus(st.TransferStatusApplyingSenderKeyTweak).
		SetExpiryTime(time.Now().Add(24 * time.Hour)).
		SetTotalValue(1000).
		SetSenderIdentityPubkey(senderPub).
		SetReceiverIdentityPubkey(receiverPub).
		Save(ctx)
	require.NoError(t, err)

	// commitSenderKeyTweaks -> helper.TweakLeafKeyUpdate now validates
	// pubkey_shares_tweak against the full cluster's operator map (#6867),
	// so this test needs the real test-cluster config rather than a
	// hand-rolled stub. The leaf's key tweak is built with valid
	// polynomial-derived pubshares for every operator in that config.
	cfg := sparktesting.TestConfig(t)

	_, err = dbTx.TransferLeaf.Create().
		SetTransfer(transfer).
		SetLeaf(leaf.node).
		SetKeyTweak(mustMarshalSimpleSendLeafKeyTweak(t, rng, leaf.node.ID.String(), cfg)).
		SetPreviousRefundTx(leaf.node.RawRefundTx).
		SetIntermediateRefundTx(leaf.node.RawRefundTx).
		Save(ctx)
	require.NoError(t, err)

	sspHandler := NewSspRequestHandler(cfg)
	resp, err := sspHandler.ApplySenderKeyTweaks(ctx, &pbssp.ApplySenderKeyTweaksRequest{
		TransferIds: []string{transfer.ID.String()},
	})
	require.NoError(t, err)
	require.Equal(t, []string{transfer.ID.String()}, resp.GetUpdatedTransferIds())

	readDb, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	updatedTransfer, err := readDb.Transfer.Query().
		Where(enttransfer.IDEQ(transfer.ID)).
		Only(ctx)
	require.NoError(t, err)
	require.Equal(t, st.TransferStatusSenderKeyTweaked, updatedTransfer.Status)

	updatedLeafs, err := updatedTransfer.QueryTransferLeaves().All(ctx)
	require.NoError(t, err)
	require.Len(t, updatedLeafs, 1)
	require.Nil(t, updatedLeafs[0].KeyTweak)
	require.NotNil(t, updatedLeafs[0].SecretCipher)
	require.NotNil(t, updatedLeafs[0].Signature)

	updatedNode, err := updatedLeafs[0].QueryLeaf().Only(ctx)
	require.NoError(t, err)
	require.NotEqual(t, originalOwnerSigningPubkey, updatedNode.OwnerSigningPubkey)
}

// TestApplySenderKeyTweaks_BatchMultiLeaf exercises the batched sender
// key-tweak commit over several leaves at once, mixing legacy and typed
// signatures, and verifies each leaf's keyshare rotation landed.
func TestApplySenderKeyTweaks_BatchMultiLeaf(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	rng := rand.NewChaCha8([32]byte{7})
	senderPub := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	receiverPub := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	cfg := sparktesting.TestConfig(t)

	transfer, err := dbTx.Transfer.Create().
		SetNetwork(btcnetwork.Regtest).
		SetType(st.TransferTypeTransfer).
		SetStatus(st.TransferStatusApplyingSenderKeyTweak).
		SetExpiryTime(time.Now().Add(24 * time.Hour)).
		SetTotalValue(3000).
		SetSenderIdentityPubkey(senderPub).
		SetReceiverIdentityPubkey(receiverPub).
		Save(ctx)
	require.NoError(t, err)

	const numLeaves = 3
	leaves := make([]*testLeaf, 0, numLeaves)
	for i := range numLeaves {
		leaf := createDbLeaf(t, ctx, true)
		leaves = append(leaves, leaf)

		keyTweak := mustBuildSimpleSendLeafKeyTweak(t, rng, leaf.node.ID.String(), cfg)
		if i == numLeaves-1 {
			// One typed signature among legacy ones: the batch must persist
			// the scheme for this row and leave it NULL for the others.
			keyTweak.Sig = &pb.SendLeafKeyTweak_TypedSignature{
				TypedSignature: &pbcommon.Signature{
					Signature: []byte("valid-typed-signature"),
					Scheme:    pbcommon.SignatureScheme_SIGNATURE_SCHEME_SCHNORR,
				},
			}
		}
		keyTweakBinary, err := proto.Marshal(keyTweak)
		require.NoError(t, err)

		_, err = dbTx.TransferLeaf.Create().
			SetTransfer(transfer).
			SetLeaf(leaf.node).
			SetKeyTweak(keyTweakBinary).
			SetPreviousRefundTx(leaf.node.RawRefundTx).
			SetIntermediateRefundTx(leaf.node.RawRefundTx).
			Save(ctx)
		require.NoError(t, err)
	}

	sspHandler := NewSspRequestHandler(cfg)
	resp, err := sspHandler.ApplySenderKeyTweaks(ctx, &pbssp.ApplySenderKeyTweaksRequest{
		TransferIds: []string{transfer.ID.String()},
	})
	require.NoError(t, err)
	require.Equal(t, []string{transfer.ID.String()}, resp.GetUpdatedTransferIds())

	readDb, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	updatedTransfer, err := readDb.Transfer.Query().
		Where(enttransfer.IDEQ(transfer.ID)).
		Only(ctx)
	require.NoError(t, err)
	require.Equal(t, st.TransferStatusSenderKeyTweaked, updatedTransfer.Status)

	updatedLeaves, err := updatedTransfer.QueryTransferLeaves().
		WithLeaf(func(tnq *ent.TreeNodeQuery) { tnq.WithSigningKeyshare() }).
		All(ctx)
	require.NoError(t, err)
	require.Len(t, updatedLeaves, numLeaves)

	for _, updatedLeaf := range updatedLeaves {
		require.Nil(t, updatedLeaf.KeyTweak)
		require.Equal(t, []byte("valid-secret-cipher"), updatedLeaf.SecretCipher)
		require.NotEmpty(t, updatedLeaf.Signature)

		// The rotated keyshare and the tree node's new owner signing key must
		// agree: owner = verifying - keyshare public key.
		node := updatedLeaf.Edges.Leaf
		require.NotNil(t, node)
		keyshare := node.Edges.SigningKeyshare
		require.NotNil(t, keyshare)
		require.Equal(t, node.VerifyingPubkey.Sub(keyshare.PublicKey), node.OwnerSigningPubkey)
	}

	// Legacy signatures must persist a NULL scheme (ent scans both NULL and
	// an explicit 0 into SignatureScheme == 0, so assert at the SQL level),
	// and the one typed signature must persist its scheme.
	legacyLeaves, err := updatedTransfer.QueryTransferLeaves().
		Where(enttransferleaf.SignatureSchemeIsNil()).
		All(ctx)
	require.NoError(t, err)
	require.Len(t, legacyLeaves, numLeaves-1)

	typedLeaves, err := updatedTransfer.QueryTransferLeaves().
		Where(enttransferleaf.SignatureSchemeNotNil()).
		All(ctx)
	require.NoError(t, err)
	require.Len(t, typedLeaves, 1)
	require.Equal(t, int32(pbcommon.SignatureScheme_SIGNATURE_SCHEME_SCHNORR), typedLeaves[0].SignatureScheme)
	require.Equal(t, []byte("valid-typed-signature"), typedLeaves[0].Signature)

	// Every leaf's owner signing key rotated away from its original value.
	for _, leaf := range leaves {
		updatedNode, err := readDb.TreeNode.Get(ctx, leaf.node.ID)
		require.NoError(t, err)
		require.NotEqual(t, leaf.node.OwnerSigningPubkey, updatedNode.OwnerSigningPubkey)
	}
}

// TestApplySenderKeyTweaks_MissingKeyTweakFails verifies the batch refuses to
// commit when any leaf is missing its stored key tweak, and names the leaf.
func TestApplySenderKeyTweaks_MissingKeyTweakFails(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	rng := rand.NewChaCha8([32]byte{11})
	senderPub := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	receiverPub := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	cfg := sparktesting.TestConfig(t)

	transfer, err := dbTx.Transfer.Create().
		SetNetwork(btcnetwork.Regtest).
		SetType(st.TransferTypeTransfer).
		SetStatus(st.TransferStatusApplyingSenderKeyTweak).
		SetExpiryTime(time.Now().Add(24 * time.Hour)).
		SetTotalValue(2000).
		SetSenderIdentityPubkey(senderPub).
		SetReceiverIdentityPubkey(receiverPub).
		Save(ctx)
	require.NoError(t, err)

	tweakedLeaf := createDbLeaf(t, ctx, true)
	_, err = dbTx.TransferLeaf.Create().
		SetTransfer(transfer).
		SetLeaf(tweakedLeaf.node).
		SetKeyTweak(mustMarshalSimpleSendLeafKeyTweak(t, rng, tweakedLeaf.node.ID.String(), cfg)).
		SetPreviousRefundTx(tweakedLeaf.node.RawRefundTx).
		SetIntermediateRefundTx(tweakedLeaf.node.RawRefundTx).
		Save(ctx)
	require.NoError(t, err)

	missingLeaf := createDbLeaf(t, ctx, true)
	_, err = dbTx.TransferLeaf.Create().
		SetTransfer(transfer).
		SetLeaf(missingLeaf.node).
		SetPreviousRefundTx(missingLeaf.node.RawRefundTx).
		SetIntermediateRefundTx(missingLeaf.node.RawRefundTx).
		Save(ctx)
	require.NoError(t, err)

	sspHandler := NewSspRequestHandler(cfg)
	resp, err := sspHandler.ApplySenderKeyTweaks(ctx, &pbssp.ApplySenderKeyTweaksRequest{
		TransferIds: []string{transfer.ID.String()},
	})
	require.ErrorContains(t, err, "has no key tweak stored")
	require.ErrorContains(t, err, missingLeaf.node.ID.String())
	require.Empty(t, resp.GetUpdatedTransferIds())
}

func mustBuildSimpleSendLeafKeyTweak(t *testing.T, rng *rand.ChaCha8, leafID string, cfg *so.Config) *pb.SendLeafKeyTweak {
	t.Helper()

	// Degree-0 polynomial g(x) = sharePriv. Every operator evaluates to
	// the same point sharePriv.Public(), which is what
	// helper.ValidatePubkeySharesTweak expects when Proofs = [sharePub].
	sharePriv := keys.MustGeneratePrivateKeyFromRand(rng)
	proofs := [][]byte{sharePriv.Public().Serialize()}

	fieldModulus := secp256k1.S256().N
	pubkeySharesTweak := make(map[string][]byte, len(cfg.SigningOperatorMap))
	for identifier, operator := range cfg.SigningOperatorMap {
		index := new(big.Int).SetUint64(operator.ID)
		index.Add(index, big.NewInt(1))
		pub, err := secretsharing.EvaluatePolynomialCommitment(proofs, index, fieldModulus)
		require.NoError(t, err)
		pubkeySharesTweak[identifier] = pub.Serialize()
	}

	return &pb.SendLeafKeyTweak{
		LeafId: leafID,
		SecretShareTweak: &pb.SecretShare{
			SecretShare: sharePriv.Serialize(),
			Proofs:      proofs,
		},
		PubkeySharesTweak: pubkeySharesTweak,
		SecretCipher:      []byte("valid-secret-cipher"),
		Sig:               &pb.SendLeafKeyTweak_Signature{Signature: []byte("valid-signature")},
	}
}

func mustMarshalSimpleSendLeafKeyTweak(t *testing.T, rng *rand.ChaCha8, leafID string, cfg *so.Config) []byte {
	t.Helper()

	keyTweakBinary, err := proto.Marshal(mustBuildSimpleSendLeafKeyTweak(t, rng, leafID, cfg))
	require.NoError(t, err)
	return keyTweakBinary
}
