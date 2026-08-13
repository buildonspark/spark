//go:build lightspark

package handler

// These tests exercise ValidateTransferPackage directly rather than a gRPC
// boundary: the property under test is a cross-operator cryptographic
// comparison, and a divergent package is by construction indistinguishable
// from an honest one at any single operator's public API — catching it
// requires validating the same package as several operators, which no public
// entry point of one SO can express.

import (
	"math/big"
	"math/rand/v2"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	eciesgo "github.com/ecies/go/v2"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	secretsharing "github.com/lightsparkdev/spark/common/secret_sharing"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// sealSliceForOperator derives every field in one operator's slice from the
// polynomial its share came from, so the slice is internally self-consistent.
func sealSliceForOperator(
	t *testing.T,
	opCfg *so.Config,
	leafID uuid.UUID,
	share *secretsharing.VerifiableSecretShare,
) []byte {
	t.Helper()

	shareBytes := make([]byte, 32)
	share.Share.FillBytes(shareBytes)

	pubkeySharesTweak := make(map[string][]byte, len(opCfg.SigningOperatorMap))
	for identifier, op := range opCfg.SigningOperatorMap {
		index := new(big.Int).SetUint64(op.ID + 1)
		pub, err := secretsharing.EvaluatePolynomialCommitment(share.Proofs, index, secp256k1.S256().N)
		require.NoError(t, err)
		pubkeySharesTweak[identifier] = pub.Serialize()
	}

	ecPub, err := eciesgo.NewPublicKeyFromBytes(opCfg.IdentityPublicKey().Serialize())
	require.NoError(t, err)
	secretCipher, err := eciesgo.Encrypt(ecPub, shareBytes)
	require.NoError(t, err)

	tweaks := &pb.SendLeafKeyTweaks{LeavesToSend: []*pb.SendLeafKeyTweak{{
		LeafId:            leafID.String(),
		SecretShareTweak:  &pb.SecretShare{SecretShare: shareBytes, Proofs: share.Proofs},
		PubkeySharesTweak: pubkeySharesTweak,
		SecretCipher:      secretCipher,
		Sig:               &pb.SendLeafKeyTweak_Signature{Signature: []byte("mock_signature_for_testing")},
	}}}
	plaintext, err := proto.Marshal(tweaks)
	require.NoError(t, err)
	sealed, err := eciesgo.Encrypt(ecPub, plaintext)
	require.NoError(t, err)
	return sealed
}

// splitPackage seals sharesByOperator[i] to operator i in one signed package, so
// shares drawn from two polynomials produce the package a malicious sender sends.
func splitPackage(
	t *testing.T,
	cfgs []*so.Config,
	leafID uuid.UUID,
	transferID uuid.UUID,
	senderPrivKey keys.Private,
	sharesByOperator []*secretsharing.VerifiableSecretShare,
) *pb.TransferPackage {
	t.Helper()

	keyTweakPackage := make(map[string][]byte, len(cfgs))
	for i, cfg := range cfgs {
		keyTweakPackage[cfg.Identifier] = sealSliceForOperator(t, cfg, leafID, sharesByOperator[i])
	}
	pkg := &pb.TransferPackage{
		LeavesToSend: []*pb.UserSignedTxSigningJob{
			{LeafId: leafID.String(), RawTx: createTestTxBytes(t, 1000)},
		},
		KeyTweakPackage: keyTweakPackage,
	}
	signTransferPackage(t, pkg, transferID, senderPrivKey)
	return pkg
}

func operatorConfigs(t *testing.T) []*so.Config {
	t.Helper()
	cfgs := []*so.Config{sparktesting.TestConfig(t)}
	for i := 1; i < len(cfgs[0].SigningOperatorMap); i++ {
		cfgs = append(cfgs, sparktesting.SpecificOperatorTestConfig(t, i))
	}
	return cfgs
}

// Every slice passing slice-local validation on its own operator is the point:
// it is why the participants' cross-SO comparison has to exist to reject.
func TestCrossSOProofCheckRejectsDivergentPolynomials(t *testing.T) {
	cfgs := operatorConfigs(t)
	numOperators := len(cfgs)
	threshold := int(cfgs[0].Threshold)
	require.GreaterOrEqual(t, numOperators, 3, "need a participant that is not the coordinator")
	require.GreaterOrEqual(t, threshold, 2, "divergence needs a higher coefficient to differ in")

	rng := rand.NewChaCha8([32]byte{7})
	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	tweakSecret := new(big.Int).SetBytes(keys.MustGeneratePrivateKeyFromRand(rng).Serialize())

	// Same secret, so the constant-term commitments match and only the higher
	// coefficients differ.
	sharesA, err := secretsharing.SplitSecretWithProofs(tweakSecret, secp256k1.S256().N, threshold, numOperators)
	require.NoError(t, err)
	sharesB, err := secretsharing.SplitSecretWithProofs(tweakSecret, secp256k1.S256().N, threshold, numOperators)
	require.NoError(t, err)
	require.Equal(t, sharesA[0].Proofs[0], sharesB[0].Proofs[0])
	require.NotEqual(t, sharesA[0].Proofs[1], sharesB[0].Proofs[1])

	divergent := make([]*secretsharing.VerifiableSecretShare, numOperators)
	divergent[0] = sharesA[0]
	copy(divergent[1:], sharesB[1:])

	leafID, transferID := uuid.New(), uuid.New()
	pkg := splitPackage(t, cfgs, leafID, transferID, senderPrivKey, divergent)

	coordinator := NewBaseTransferHandler(cfgs[0])
	coordinatorProofs, err := coordinator.coordinatorSenderKeyTweakProofs(t.Context(), pkg)
	require.NoError(t, err)
	require.Len(t, coordinatorProofs, 1)

	for i, cfg := range cfgs {
		h := NewBaseTransferHandler(cfg)
		_, err := h.ValidateTransferPackage(
			t.Context(), transferID, pkg, senderPrivKey.Public(), false, asCoordinator())
		require.NoErrorf(t, err, "operator %d rejected its own slice, so the test no longer exercises divergence", i)

		_, err = h.ValidateTransferPackage(
			t.Context(), transferID, pkg, senderPrivKey.Public(), false, asParticipant(coordinatorProofs))
		if i == 0 {
			require.NoError(t, err, "the coordinator must agree with its own proofs")
			continue
		}
		require.Errorf(t, err, "operator %d accepted a slice from a different polynomial", i)
		assert.Contains(t, err.Error(), "sender key tweak proof mismatch")
	}
}

func TestCrossSOProofCheckAcceptsOnePolynomial(t *testing.T) {
	cfgs := operatorConfigs(t)
	numOperators := len(cfgs)
	threshold := int(cfgs[0].Threshold)

	rng := rand.NewChaCha8([32]byte{11})
	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	tweakSecret := new(big.Int).SetBytes(keys.MustGeneratePrivateKeyFromRand(rng).Serialize())

	shares, err := secretsharing.SplitSecretWithProofs(tweakSecret, secp256k1.S256().N, threshold, numOperators)
	require.NoError(t, err)

	leafID, transferID := uuid.New(), uuid.New()
	pkg := splitPackage(t, cfgs, leafID, transferID, senderPrivKey, shares)

	coordinator := NewBaseTransferHandler(cfgs[0])
	coordinatorProofs, err := coordinator.coordinatorSenderKeyTweakProofs(t.Context(), pkg)
	require.NoError(t, err)

	for i, cfg := range cfgs {
		h := NewBaseTransferHandler(cfg)
		_, err := h.ValidateTransferPackage(
			t.Context(), transferID, pkg, senderPrivKey.Public(), false, asParticipant(coordinatorProofs))
		require.NoErrorf(t, err, "operator %d rejected a consistent package", i)
	}
}

// Absent proofs must fail closed: a skipped comparison is the original gap.
func TestCrossSOProofCheckRejectsAbsentProofs(t *testing.T) {
	cfg := sparktesting.TestConfig(t)
	rng := rand.NewChaCha8([32]byte{13})
	senderPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)

	leafID, transferID := uuid.New(), uuid.New()
	keyTweakPackage, _ := buildKeyTweakPackageForLeaves(t, cfg, rng, []uuid.UUID{leafID})
	pkg := &pb.TransferPackage{
		LeavesToSend: []*pb.UserSignedTxSigningJob{
			{LeafId: leafID.String(), RawTx: createTestTxBytes(t, 1000)},
		},
		KeyTweakPackage: keyTweakPackage,
	}
	signTransferPackage(t, pkg, transferID, senderPrivKey)

	h := NewBaseTransferHandler(cfg)
	_, err := h.ValidateTransferPackage(
		t.Context(), transferID, pkg, senderPrivKey.Public(), false, asParticipant(nil))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not be nil")
	_, err = h.ValidateTransferPackage(
		t.Context(), transferID, pkg, senderPrivKey.Public(), false, asParticipant(map[string]*pb.SecretProof{}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "proof count mismatch")
}
