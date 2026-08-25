//go:build lightspark

package handler

import (
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	eciesgo "github.com/ecies/go/v2"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// buildKeyTweakPackageForLeaves encrypts key tweaks for the given leaf IDs into
// a TransferPackage.KeyTweakPackage map. Only the leaf IDs listed in tweakedLeafIDs
// will have entries in the encrypted payload. The second return value is the
// leaf-ID-keyed proof map a coordinator would attach as SenderKeyTweakProofs.
func buildKeyTweakPackageForLeaves(
	t *testing.T,
	cfg *so.Config,
	rng *rand.ChaCha8,
	tweakedLeafIDs []uuid.UUID,
) (map[string][]byte, map[string]*pb.SecretProof) {
	t.Helper()

	var leafTweaks []*pb.SendLeafKeyTweak
	proofs := make(map[string]*pb.SecretProof, len(tweakedLeafIDs))
	for _, leafID := range tweakedLeafIDs {
		secretShare, pubkeySharesTweak := createValidSecretShares(cfg, rng)
		publicKey, err := eciesgo.NewPublicKeyFromBytes(cfg.IdentityPublicKey().Serialize())
		require.NoError(t, err)
		secretCipher, err := eciesgo.Encrypt(publicKey, secretShare.GetSecretShare())
		require.NoError(t, err)

		leafTweaks = append(leafTweaks, &pb.SendLeafKeyTweak{
			LeafId:            leafID.String(),
			SecretShareTweak:  secretShare,
			PubkeySharesTweak: pubkeySharesTweak,
			SecretCipher:      secretCipher,
			Sig:               &pb.SendLeafKeyTweak_Signature{Signature: []byte("mock_signature_for_testing")},
		})
		proofs[leafID.String()] = &pb.SecretProof{Proofs: secretShare.GetProofs()}
	}

	publicKey, err := eciesgo.NewPublicKeyFromBytes(cfg.IdentityPublicKey().Serialize())
	require.NoError(t, err)

	leafTweaksProto := &pb.SendLeafKeyTweaks{LeavesToSend: leafTweaks}
	data, err := proto.Marshal(leafTweaksProto)
	require.NoError(t, err)
	encrypted, err := eciesgo.Encrypt(publicKey, data)
	require.NoError(t, err)

	return map[string][]byte{cfg.Identifier: encrypted}, proofs
}

// signTransferPackage signs the given TransferPackage and sets UserSignature.
func signTransferPackage(
	t *testing.T,
	pkg *pb.TransferPackage,
	transferID uuid.UUID,
	senderPrivKey keys.Private,
) {
	t.Helper()
	payload := common.GetTransferPackageSigningPayload(transferID, pkg)
	sig := ecdsa.Sign(senderPrivKey.ToBTCEC(), payload)
	pkg.UserSignature = sig.Serialize()
}

// TestValidateTransferPackage_MissingKeyTweakForRefundLeaf verifies that
// ValidateTransferPackage rejects a package where a refund-transaction leaf
// has no corresponding entry in the encrypted key tweak payload.
func TestValidateTransferPackage_MissingKeyTweakForRefundLeaf(t *testing.T) {
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{42})

	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	transferID := uuid.New()

	leafWithTweak := uuid.New()
	leafWithoutTweak := uuid.New()

	// Encrypt key tweaks for ONLY leafWithTweak — leafWithoutTweak is missing.
	keyTweakPackage, _ := buildKeyTweakPackageForLeaves(t, cfg, rng, []uuid.UUID{leafWithTweak})

	pkg := &pb.TransferPackage{
		LeavesToSend: []*pb.UserSignedTxSigningJob{
			{LeafId: leafWithTweak.String(), RawTx: createTestTxBytes(t, 1000)},
			{LeafId: leafWithoutTweak.String(), RawTx: createTestTxBytes(t, 2000)},
		},
		KeyTweakPackage: keyTweakPackage,
	}
	signTransferPackage(t, pkg, transferID, senderPrivKey)

	h := NewBaseTransferHandler(cfg)
	_, err := h.ValidateTransferPackage(
		t.Context(),
		transferID,
		pkg,
		senderPrivKey.Public(),
		false,
		asCoordinator(),
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "key tweak count mismatch")
}

// TestValidateTransferPackage_AllLeavesHaveKeyTweaks verifies that
// ValidateTransferPackage succeeds when every refund-transaction leaf
// has a matching key tweak entry.
func TestValidateTransferPackage_AllLeavesHaveKeyTweaks(t *testing.T) {
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{43})

	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	transferID := uuid.New()

	leaf1 := uuid.New()
	leaf2 := uuid.New()

	// Encrypt key tweaks for BOTH leaves.
	keyTweakPackage, _ := buildKeyTweakPackageForLeaves(t, cfg, rng, []uuid.UUID{leaf1, leaf2})

	pkg := &pb.TransferPackage{
		LeavesToSend: []*pb.UserSignedTxSigningJob{
			{LeafId: leaf1.String(), RawTx: createTestTxBytes(t, 1000)},
			{LeafId: leaf2.String(), RawTx: createTestTxBytes(t, 2000)},
		},
		KeyTweakPackage: keyTweakPackage,
	}
	signTransferPackage(t, pkg, transferID, senderPrivKey)

	h := NewBaseTransferHandler(cfg)
	tweaksMap, err := h.ValidateTransferPackage(
		t.Context(),
		transferID,
		pkg,
		senderPrivKey.Public(),
		false,
		asCoordinator(),
	)

	require.NoError(t, err)
	assert.Len(t, tweaksMap, 2)
	assert.Contains(t, tweaksMap, leaf1.String())
	assert.Contains(t, tweaksMap, leaf2.String())
}

// TestValidateTransferPackage_MismatchedKeyTweakLeafID verifies that
// ValidateTransferPackage rejects a package where the key tweak count matches
// the refund transaction count but one tweak is for a leaf ID not in the
// refund transactions (covers the per-leaf ID check after the count check).
func TestValidateTransferPackage_MismatchedKeyTweakLeafID(t *testing.T) {
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{46})

	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	transferID := uuid.New()

	leaf1 := uuid.New()
	leaf2 := uuid.New()
	wrongLeaf := uuid.New()

	// Encrypt key tweaks for leaf1 and wrongLeaf (not leaf2).
	// Count matches (2 vs 2) but leaf2 has no tweak.
	keyTweakPackage, _ := buildKeyTweakPackageForLeaves(t, cfg, rng, []uuid.UUID{leaf1, wrongLeaf})

	pkg := &pb.TransferPackage{
		LeavesToSend: []*pb.UserSignedTxSigningJob{
			{LeafId: leaf1.String(), RawTx: createTestTxBytes(t, 1000)},
			{LeafId: leaf2.String(), RawTx: createTestTxBytes(t, 2000)},
		},
		KeyTweakPackage: keyTweakPackage,
	}
	signTransferPackage(t, pkg, transferID, senderPrivKey)

	h := NewBaseTransferHandler(cfg)
	_, err := h.ValidateTransferPackage(
		t.Context(),
		transferID,
		pkg,
		senderPrivKey.Public(),
		false,
		asCoordinator(),
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "key tweak missing for leaf")
	assert.Contains(t, err.Error(), leaf2.String())
}

// TestValidateTransferPackage_ExtraKeyTweakForUnknownLeaf verifies that
// ValidateTransferPackage rejects a package where the encrypted key tweak
// payload contains entries for leaf IDs not present in the refund transactions.
func TestValidateTransferPackage_ExtraKeyTweakForUnknownLeaf(t *testing.T) {
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{45})

	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	transferID := uuid.New()

	realLeaf := uuid.New()
	extraLeaf := uuid.New()

	// Encrypt key tweaks for both realLeaf AND extraLeaf, but only include
	// realLeaf in LeavesToSend. The extra entry should be rejected.
	keyTweakPackage, _ := buildKeyTweakPackageForLeaves(t, cfg, rng, []uuid.UUID{realLeaf, extraLeaf})

	pkg := &pb.TransferPackage{
		LeavesToSend: []*pb.UserSignedTxSigningJob{
			{LeafId: realLeaf.String(), RawTx: createTestTxBytes(t, 1000)},
		},
		KeyTweakPackage: keyTweakPackage,
	}
	signTransferPackage(t, pkg, transferID, senderPrivKey)

	h := NewBaseTransferHandler(cfg)
	_, err := h.ValidateTransferPackage(
		t.Context(),
		transferID,
		pkg,
		senderPrivKey.Public(),
		false,
		asCoordinator(),
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "key tweak count mismatch")
}

// buildKeyTweakPackageWithMismatchedPubkey builds a TransferPackage.KeyTweakPackage
// where PubkeySharesTweak for corruptID is set to a random key. The remaining
// payload is valid, so the failure isolates the cross-verification check for that
// operator's entry.
func buildKeyTweakPackageWithMismatchedPubkey(
	t *testing.T,
	cfg *so.Config,
	rng *rand.ChaCha8,
	tweakedLeafIDs []uuid.UUID,
	corruptID string,
) map[string][]byte {
	t.Helper()

	var leafTweaks []*pb.SendLeafKeyTweak
	for _, leafID := range tweakedLeafIDs {
		secretShare, pubkeySharesTweak := createValidSecretShares(cfg, rng)

		wrongKey := keys.MustGeneratePrivateKeyFromRand(rng)
		pubkeySharesTweak[corruptID] = wrongKey.Public().Serialize()

		publicKey, err := eciesgo.NewPublicKeyFromBytes(cfg.IdentityPublicKey().Serialize())
		require.NoError(t, err)
		secretCipher, err := eciesgo.Encrypt(publicKey, secretShare.GetSecretShare())
		require.NoError(t, err)

		leafTweaks = append(leafTweaks, &pb.SendLeafKeyTweak{
			LeafId:            leafID.String(),
			SecretShareTweak:  secretShare,
			PubkeySharesTweak: pubkeySharesTweak,
			SecretCipher:      secretCipher,
			Sig:               &pb.SendLeafKeyTweak_Signature{Signature: []byte("mock_signature_for_testing")},
		})
	}

	publicKey, err := eciesgo.NewPublicKeyFromBytes(cfg.IdentityPublicKey().Serialize())
	require.NoError(t, err)

	leafTweaksProto := &pb.SendLeafKeyTweaks{LeavesToSend: leafTweaks}
	data, err := proto.Marshal(leafTweaksProto)
	require.NoError(t, err)
	encrypted, err := eciesgo.Encrypt(publicKey, data)
	require.NoError(t, err)

	return map[string][]byte{cfg.Identifier: encrypted}
}

// TestValidateTransferPackage_PubkeyShareTweakMismatch verifies that
// ValidateTransferPackage rejects a package where any operator's PubkeySharesTweak
// entry is inconsistent with the polynomial commitment derived from the supplied
// proofs. Covers both this SO's own entry and a peer operator's entry, since the
// validator must check every operator's tweak — not just its own.
func TestValidateTransferPackage_PubkeyShareTweakMismatch(t *testing.T) {
	cfg := sparktesting.TestConfig(t)

	var peerID string
	for id := range cfg.SigningOperatorMap {
		if id != cfg.Identifier {
			peerID = id
			break
		}
	}
	require.NotEmpty(t, peerID, "test config must include at least one peer operator")

	tests := []struct {
		name      string
		corruptID string
	}{
		{name: "self", corruptID: cfg.Identifier},
		{name: "peer", corruptID: peerID},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rng := rand.NewChaCha8([32]byte{99})
			senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
			transferID := uuid.New()
			leaf := uuid.New()

			keyTweakPackage := buildKeyTweakPackageWithMismatchedPubkey(t, cfg, rng, []uuid.UUID{leaf}, tc.corruptID)

			pkg := &pb.TransferPackage{
				LeavesToSend: []*pb.UserSignedTxSigningJob{
					{LeafId: leaf.String(), RawTx: createTestTxBytes(t, 1000)},
				},
				KeyTweakPackage: keyTweakPackage,
			}
			signTransferPackage(t, pkg, transferID, senderPrivKey)

			h := NewBaseTransferHandler(cfg)
			_, err := h.ValidateTransferPackage(
				t.Context(),
				transferID,
				pkg,
				senderPrivKey.Public(),
				false,
				asCoordinator(),
			)

			require.Error(t, err)
			assert.Contains(t, err.Error(), "does not match polynomial commitment")
			assert.Contains(t, err.Error(), tc.corruptID)
		})
	}
}

func TestValidateTransferPackage_RejectsDuplicateEncryptedKeyTweakLeafID(t *testing.T) {
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{47})
	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	transferID := uuid.New()
	leafID := uuid.New()

	keyTweakPackage, _ := buildKeyTweakPackageForLeaves(t, cfg, rng, []uuid.UUID{leafID, leafID})

	pkg := &pb.TransferPackage{
		LeavesToSend: []*pb.UserSignedTxSigningJob{{
			LeafId: leafID.String(),
			RawTx:  createTestTxBytes(t, 1000),
		}},
		KeyTweakPackage: keyTweakPackage,
	}
	signTransferPackage(t, pkg, transferID, senderPrivKey)

	h := NewBaseTransferHandler(cfg)
	_, err := h.ValidateTransferPackage(
		t.Context(),
		transferID,
		pkg,
		senderPrivKey.Public(),
		false,
		asCoordinator(),
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate leaf id in encrypted key tweaks")
}

// buildKeyTweakPackageWithMutation builds an encrypted key-tweak package for a
// single leaf, applying mutate to the SendLeafKeyTweak before encryption so
// tests can characterize how ValidateTransferPackage rejects tampered fields.
func buildKeyTweakPackageWithMutation(
	t *testing.T,
	cfg *so.Config,
	rng *rand.ChaCha8,
	leafID uuid.UUID,
	mutate func(*pb.SendLeafKeyTweak),
) map[string][]byte {
	t.Helper()

	secretShare, pubkeySharesTweak := createValidSecretShares(cfg, rng)
	publicKey, err := eciesgo.NewPublicKeyFromBytes(cfg.IdentityPublicKey().Serialize())
	require.NoError(t, err)
	secretCipher, err := eciesgo.Encrypt(publicKey, secretShare.GetSecretShare())
	require.NoError(t, err)

	leafTweak := &pb.SendLeafKeyTweak{
		LeafId:            leafID.String(),
		SecretShareTweak:  secretShare,
		PubkeySharesTweak: pubkeySharesTweak,
		SecretCipher:      secretCipher,
		Sig:               &pb.SendLeafKeyTweak_Signature{Signature: []byte("mock_signature_for_testing")},
	}
	mutate(leafTweak)

	data, err := proto.Marshal(&pb.SendLeafKeyTweaks{LeavesToSend: []*pb.SendLeafKeyTweak{leafTweak}})
	require.NoError(t, err)
	encrypted, err := eciesgo.Encrypt(publicKey, data)
	require.NoError(t, err)
	return map[string][]byte{cfg.Identifier: encrypted}
}

// singleLeafPackage assembles a one-leaf TransferPackage around the given
// key-tweak package.
func singleLeafPackage(t *testing.T, leafID uuid.UUID, keyTweakPackage map[string][]byte) *pb.TransferPackage {
	t.Helper()
	return &pb.TransferPackage{
		LeavesToSend: []*pb.UserSignedTxSigningJob{
			{LeafId: leafID.String(), RawTx: createTestTxBytes(t, 1000)},
		},
		KeyTweakPackage: keyTweakPackage,
	}
}

// The tests below pin ValidateTransferPackage's behavior per failure mode
// (message fragment and success/failure), so the validation can be
// restructured with confidence that observable behavior is unchanged.

func TestValidateTransferPackage_NilPackageIsNoop(t *testing.T) {
	cfg := sparktesting.TestConfig(t)
	h := NewBaseTransferHandler(cfg)

	tweaksMap, err := h.ValidateTransferPackage(t.Context(), uuid.New(), nil, keys.Public{}, false, asCoordinator())

	require.NoError(t, err)
	assert.Nil(t, tweaksMap)
}

func TestValidateTransferPackage_EmptyKeyTweakPackage(t *testing.T) {
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{44})
	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	transferID := uuid.New()
	leafID := uuid.New()

	pkg := singleLeafPackage(t, leafID, nil)
	signTransferPackage(t, pkg, transferID, senderPrivKey)

	h := NewBaseTransferHandler(cfg)
	_, err := h.ValidateTransferPackage(t.Context(), transferID, pkg, senderPrivKey.Public(), false, asCoordinator())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "key tweak package is empty")
}

func TestValidateTransferPackage_EmptyUserSignature(t *testing.T) {
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{45})
	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	transferID := uuid.New()
	leafID := uuid.New()

	keyTweakPackage, _ := buildKeyTweakPackageForLeaves(t, cfg, rng, []uuid.UUID{leafID})
	pkg := singleLeafPackage(t, leafID, keyTweakPackage)
	// Deliberately not signed.

	h := NewBaseTransferHandler(cfg)
	_, err := h.ValidateTransferPackage(t.Context(), transferID, pkg, senderPrivKey.Public(), false, asCoordinator())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "user signature cannot be empty")
}

func TestValidateTransferPackage_WrongSigner(t *testing.T) {
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{46})
	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	otherPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	transferID := uuid.New()
	leafID := uuid.New()

	keyTweakPackage, _ := buildKeyTweakPackageForLeaves(t, cfg, rng, []uuid.UUID{leafID})
	pkg := singleLeafPackage(t, leafID, keyTweakPackage)
	signTransferPackage(t, pkg, transferID, otherPrivKey)

	h := NewBaseTransferHandler(cfg)
	_, err := h.ValidateTransferPackage(t.Context(), transferID, pkg, senderPrivKey.Public(), false, asCoordinator())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unable to verify user signature")
}

func TestValidateTransferPackage_NoKeyTweaksForThisOperator(t *testing.T) {
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{47})
	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	transferID := uuid.New()
	leafID := uuid.New()

	keyTweakPackage, _ := buildKeyTweakPackageForLeaves(t, cfg, rng, []uuid.UUID{leafID})
	// Re-key the ciphertext to a different operator identifier so this SO
	// finds no entry for itself.
	for k, v := range keyTweakPackage {
		delete(keyTweakPackage, k)
		keyTweakPackage["not-"+k] = v
	}
	pkg := singleLeafPackage(t, leafID, keyTweakPackage)
	signTransferPackage(t, pkg, transferID, senderPrivKey)

	h := NewBaseTransferHandler(cfg)
	_, err := h.ValidateTransferPackage(t.Context(), transferID, pkg, senderPrivKey.Public(), false, asCoordinator())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no key tweaks found for SO")
}

func TestValidateTransferPackage_UndecryptableKeyTweaks(t *testing.T) {
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{48})
	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	transferID := uuid.New()
	leafID := uuid.New()

	keyTweakPackage := map[string][]byte{cfg.Identifier: []byte("not a valid ECIES ciphertext")}
	pkg := singleLeafPackage(t, leafID, keyTweakPackage)
	signTransferPackage(t, pkg, transferID, senderPrivKey)

	h := NewBaseTransferHandler(cfg)
	_, err := h.ValidateTransferPackage(t.Context(), transferID, pkg, senderPrivKey.Public(), false, asCoordinator())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to decrypt key tweaks")
}

func TestValidateTransferPackage_TooManyLeaves(t *testing.T) {
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{49})
	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	transferID := uuid.New()

	leaves := make([]*pb.UserSignedTxSigningJob, MaxLeavesToSend+1)
	for i := range leaves {
		leaves[i] = &pb.UserSignedTxSigningJob{LeafId: uuid.NewString()}
	}
	pkg := &pb.TransferPackage{
		LeavesToSend:    leaves,
		KeyTweakPackage: map[string][]byte{cfg.Identifier: []byte("placeholder")},
	}
	signTransferPackage(t, pkg, transferID, senderPrivKey)

	h := NewBaseTransferHandler(cfg)
	_, err := h.ValidateTransferPackage(t.Context(), transferID, pkg, senderPrivKey.Public(), false, asCoordinator())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "too many leaves to send")
}

func TestValidateTransferPackage_InvalidSecretShare(t *testing.T) {
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{50})
	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	transferID := uuid.New()
	leafID := uuid.New()

	keyTweakPackage := buildKeyTweakPackageWithMutation(t, cfg, rng, leafID, func(lt *pb.SendLeafKeyTweak) {
		// Corrupt the share so it no longer lies on the committed polynomial.
		lt.SecretShareTweak.SecretShare[0] ^= 0x01
	})
	pkg := singleLeafPackage(t, leafID, keyTweakPackage)
	signTransferPackage(t, pkg, transferID, senderPrivKey)

	h := NewBaseTransferHandler(cfg)
	_, err := h.ValidateTransferPackage(t.Context(), transferID, pkg, senderPrivKey.Public(), false, asCoordinator())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unable to validate share")
}

func TestValidateTransferPackage_MissingPubkeyShareForOperator(t *testing.T) {
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{51})
	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	transferID := uuid.New()
	leafID := uuid.New()

	keyTweakPackage := buildKeyTweakPackageWithMutation(t, cfg, rng, leafID, func(lt *pb.SendLeafKeyTweak) {
		for k := range lt.GetPubkeySharesTweak() {
			delete(lt.GetPubkeySharesTweak(), k)
			break
		}
	})
	pkg := singleLeafPackage(t, leafID, keyTweakPackage)
	signTransferPackage(t, pkg, transferID, senderPrivKey)

	h := NewBaseTransferHandler(cfg)
	_, err := h.ValidateTransferPackage(t.Context(), transferID, pkg, senderPrivKey.Public(), false, asCoordinator())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "pubkey share tweak missing for operator")
}

func TestValidateTransferPackage_AcceptsValidTypedSignature(t *testing.T) {
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{52})
	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	transferID := uuid.New()
	leafID := uuid.New()

	keyTweakPackage := buildKeyTweakPackageWithMutation(t, cfg, rng, leafID, func(lt *pb.SendLeafKeyTweak) {
		lt.Sig = &pb.SendLeafKeyTweak_TypedSignature{TypedSignature: &pbcommon.Signature{
			Scheme:    pbcommon.SignatureScheme_SIGNATURE_SCHEME_SCHNORR,
			Signature: []byte("mock_signature_for_testing"),
		}}
	})
	pkg := singleLeafPackage(t, leafID, keyTweakPackage)
	signTransferPackage(t, pkg, transferID, senderPrivKey)

	h := NewBaseTransferHandler(cfg)
	tweaksMap, err := h.ValidateTransferPackage(t.Context(), transferID, pkg, senderPrivKey.Public(), false, asCoordinator())

	require.NoError(t, err)
	typed := tweaksMap[leafID.String()].Proto().GetTypedSignature()
	require.NotNil(t, typed)
	assert.Equal(t, pbcommon.SignatureScheme_SIGNATURE_SCHEME_SCHNORR, typed.GetScheme())
}

func TestValidateTransferPackage_RejectsTypedSignatureWithUnspecifiedScheme(t *testing.T) {
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{53})
	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	transferID := uuid.New()
	leafID := uuid.New()

	keyTweakPackage := buildKeyTweakPackageWithMutation(t, cfg, rng, leafID, func(lt *pb.SendLeafKeyTweak) {
		lt.Sig = &pb.SendLeafKeyTweak_TypedSignature{TypedSignature: &pbcommon.Signature{
			Scheme:    pbcommon.SignatureScheme_SIGNATURE_SCHEME_UNSPECIFIED,
			Signature: []byte("mock_signature_for_testing"),
		}}
	})
	pkg := singleLeafPackage(t, leafID, keyTweakPackage)
	signTransferPackage(t, pkg, transferID, senderPrivKey)

	h := NewBaseTransferHandler(cfg)
	_, err := h.ValidateTransferPackage(t.Context(), transferID, pkg, senderPrivKey.Public(), false, asCoordinator())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid typed signature")
}

func TestValidateTransferPackage_RejectsTypedSignatureWithUndefinedScheme(t *testing.T) {
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{54})
	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	transferID := uuid.New()
	leafID := uuid.New()

	keyTweakPackage := buildKeyTweakPackageWithMutation(t, cfg, rng, leafID, func(lt *pb.SendLeafKeyTweak) {
		lt.Sig = &pb.SendLeafKeyTweak_TypedSignature{TypedSignature: &pbcommon.Signature{
			Scheme:    pbcommon.SignatureScheme(99),
			Signature: []byte("mock_signature_for_testing"),
		}}
	})
	pkg := singleLeafPackage(t, leafID, keyTweakPackage)
	signTransferPackage(t, pkg, transferID, senderPrivKey)

	h := NewBaseTransferHandler(cfg)
	_, err := h.ValidateTransferPackage(t.Context(), transferID, pkg, senderPrivKey.Public(), false, asCoordinator())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid typed signature")
}

func TestValidateTransferPackage_RejectsTypedSignatureWithEmptyBytes(t *testing.T) {
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{55})
	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	transferID := uuid.New()
	leafID := uuid.New()

	keyTweakPackage := buildKeyTweakPackageWithMutation(t, cfg, rng, leafID, func(lt *pb.SendLeafKeyTweak) {
		lt.Sig = &pb.SendLeafKeyTweak_TypedSignature{TypedSignature: &pbcommon.Signature{
			Scheme: pbcommon.SignatureScheme_SIGNATURE_SCHEME_ECDSA,
		}}
	})
	pkg := singleLeafPackage(t, leafID, keyTweakPackage)
	signTransferPackage(t, pkg, transferID, senderPrivKey)

	h := NewBaseTransferHandler(cfg)
	_, err := h.ValidateTransferPackage(t.Context(), transferID, pkg, senderPrivKey.Public(), false, asCoordinator())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid typed signature")
}

// buildKeyTweakPackageForLeafSpellings mirrors buildKeyTweakPackageForLeaves but
// lets the test control the verbatim leaf_id spelling inside the encrypted
// tweaks, to exercise UUID-spelling normalization. The returned proof map stays
// keyed by canonical leaf ID.
func buildKeyTweakPackageForLeafSpellings(
	t *testing.T,
	cfg *so.Config,
	rng *rand.ChaCha8,
	leafSpellings map[string]uuid.UUID,
) (map[string][]byte, map[string]*pb.SecretProof) {
	t.Helper()

	var leafTweaks []*pb.SendLeafKeyTweak
	proofs := make(map[string]*pb.SecretProof, len(leafSpellings))
	for spelling, leafID := range leafSpellings {
		secretShare, pubkeySharesTweak := createValidSecretShares(cfg, rng)
		publicKey, err := eciesgo.NewPublicKeyFromBytes(cfg.IdentityPublicKey().Serialize())
		require.NoError(t, err)
		secretCipher, err := eciesgo.Encrypt(publicKey, secretShare.GetSecretShare())
		require.NoError(t, err)

		leafTweaks = append(leafTweaks, &pb.SendLeafKeyTweak{
			LeafId:            spelling,
			SecretShareTweak:  secretShare,
			PubkeySharesTweak: pubkeySharesTweak,
			SecretCipher:      secretCipher,
			Sig:               &pb.SendLeafKeyTweak_Signature{Signature: []byte("mock_signature_for_testing")},
		})
		proofs[leafID.String()] = &pb.SecretProof{Proofs: secretShare.GetProofs()}
	}

	publicKey, err := eciesgo.NewPublicKeyFromBytes(cfg.IdentityPublicKey().Serialize())
	require.NoError(t, err)

	leafTweaksProto := &pb.SendLeafKeyTweaks{LeavesToSend: leafTweaks}
	data, err := proto.Marshal(leafTweaksProto)
	require.NoError(t, err)
	encrypted, err := eciesgo.Encrypt(publicKey, data)
	require.NoError(t, err)

	return map[string][]byte{cfg.Identifier: encrypted}, proofs
}

// TestValidateTransferPackage_PreservesStoredLeafIDSpelling verifies that a
// package whose leaf IDs use a non-canonical UUID spelling (uppercase) is
// validated by UUID equality, and that the tweak proto kept for persistence
// passes the client's verbatim spelling through: rewriting it would break
// settle-time interop with operators on older builds, which match the stored
// spelling exactly.
func TestValidateTransferPackage_PreservesStoredLeafIDSpelling(t *testing.T) {
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{47})

	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	transferID := uuid.New()

	leaf := uuid.New()
	upperLeafID := strings.ToUpper(leaf.String())

	keyTweakPackage, _ := buildKeyTweakPackageForLeafSpellings(t, cfg, rng, map[string]uuid.UUID{upperLeafID: leaf})

	pkg := &pb.TransferPackage{
		LeavesToSend: []*pb.UserSignedTxSigningJob{
			{LeafId: upperLeafID, RawTx: createTestTxBytes(t, 1000)},
		},
		KeyTweakPackage: keyTweakPackage,
	}
	signTransferPackage(t, pkg, transferID, senderPrivKey)

	h := NewBaseTransferHandler(cfg)
	tweaksMap, err := h.ValidateTransferPackage(
		t.Context(),
		transferID,
		pkg,
		senderPrivKey.Public(),
		false,
		asCoordinator(),
	)

	require.NoError(t, err)
	require.Len(t, tweaksMap, 1)
	require.Contains(t, tweaksMap, leaf.String())
	assert.Equal(t, upperLeafID, tweaksMap[leaf.String()].Proto().GetLeafId())
}

// TestVerifySenderKeyTweakProofsMatch_CaseVariantProofKeys verifies that the
// coordinator-fanned proofs match this SO's decrypted tweaks even when the
// proof map is keyed by a different UUID spelling of the same leaves.
func TestVerifySenderKeyTweakProofsMatch_CaseVariantProofKeys(t *testing.T) {
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{48})

	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	transferID := uuid.New()

	leaf1 := uuid.New()
	leaf2 := uuid.New()

	keyTweakPackage, proofs := buildKeyTweakPackageForLeaves(t, cfg, rng, []uuid.UUID{leaf1, leaf2})

	pkg := &pb.TransferPackage{
		LeavesToSend: []*pb.UserSignedTxSigningJob{
			{LeafId: leaf1.String(), RawTx: createTestTxBytes(t, 1000)},
			{LeafId: leaf2.String(), RawTx: createTestTxBytes(t, 2000)},
		},
		KeyTweakPackage: keyTweakPackage,
	}
	signTransferPackage(t, pkg, transferID, senderPrivKey)

	h := NewBaseTransferHandler(cfg)
	tweaksMap, err := h.ValidateTransferPackage(
		t.Context(),
		transferID,
		pkg,
		senderPrivKey.Public(),
		false,
		asCoordinator(),
	)
	require.NoError(t, err)

	upperProofs := make(map[string]*pb.SecretProof, len(proofs))
	for leafID, proof := range proofs {
		upperProofs[strings.ToUpper(leafID)] = proof
	}
	require.NoError(t, verifySenderKeyTweakProofsMatch(tweaksMap, upperProofs))
}

func TestParseSecretProofMapKeys(t *testing.T) {
	leaf := uuid.New()
	other := uuid.New()

	t.Run("case variants of one leaf collapse", func(t *testing.T) {
		proof := &pb.SecretProof{Proofs: [][]byte{[]byte("proof")}}
		parsed, err := parseSecretProofMapKeys(map[string]*pb.SecretProof{
			leaf.String():                  proof,
			strings.ToUpper(leaf.String()): proof,
		})
		require.NoError(t, err)
		require.Len(t, parsed, 1)
		assert.Same(t, proof, parsed[leaf])
	})

	t.Run("conflicting duplicates fail closed", func(t *testing.T) {
		_, err := parseSecretProofMapKeys(map[string]*pb.SecretProof{
			leaf.String():                  {Proofs: [][]byte{[]byte("proof-a")}},
			strings.ToUpper(leaf.String()): {Proofs: [][]byte{[]byte("proof-b")}},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "conflicting sender key tweak proofs")
	})

	t.Run("unparseable key fails closed", func(t *testing.T) {
		_, err := parseSecretProofMapKeys(map[string]*pb.SecretProof{
			"not-a-uuid": {Proofs: [][]byte{[]byte("proof")}},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unable to parse leaf id")
	})

	t.Run("distinct leaves keep their entries", func(t *testing.T) {
		parsed, err := parseSecretProofMapKeys(map[string]*pb.SecretProof{
			strings.ToUpper(leaf.String()):  {Proofs: [][]byte{[]byte("proof-1")}},
			strings.ToUpper(other.String()): {Proofs: [][]byte{[]byte("proof-2")}},
		})
		require.NoError(t, err)
		require.Len(t, parsed, 2)
		assert.Contains(t, parsed, leaf)
		assert.Contains(t, parsed, other)
	})
}
