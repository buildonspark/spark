package multisig

import (
	"crypto/sha256"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2/ecdsa"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/multisig"
	"github.com/stretchr/testify/require"
)

func testHash(data []byte) []byte {
	h := sha256.Sum256(data)
	return h[:]
}

func signSchnorr(t *testing.T, privKey keys.Private, message []byte) []byte {
	t.Helper()
	sig, err := schnorr.Sign(privKey.ToBTCEC(), message)
	require.NoError(t, err)
	return sig.Serialize()
}

func signECDSA(t *testing.T, privKey keys.Private, message []byte) []byte {
	t.Helper()
	sig := ecdsa.Sign(privKey.ToBTCEC(), message)
	return sig.Serialize()
}

func makeConfig(t *testing.T, threshold uint32, privKeys ...keys.Private) *pb.MultisigConfig {
	t.Helper()
	pubKeys := make([][]byte, len(privKeys))
	for i, pk := range privKeys {
		pubKeys[i] = pk.Public().Serialize()
	}
	config := NormalizeMultisigConfig(&pb.MultisigConfig{
		Version:    0,
		Threshold:  threshold,
		PublicKeys: pubKeys,
	})
	return config
}

func TestValidateMultisigSignatures_Valid2of3(t *testing.T) {
	config := makeConfig(t, 2, testPrivKey1, testPrivKey2, testPrivKey3)
	message := testHash([]byte("test message"))

	sigSet := &pb.MultisigSignatureSet{
		MultisigConfig: config,
		Signatures: []*pb.KeyedSignature{
			{PublicKey: testPrivKey1.Public().Serialize(), Signature: signSchnorr(t, testPrivKey1, message)},
			{PublicKey: testPrivKey2.Public().Serialize(), Signature: signSchnorr(t, testPrivKey2, message)},
		},
	}

	err := ValidateMultisigSignatures(config, message, sigSet)
	require.NoError(t, err)
}

func TestValidateMultisigSignatures_Valid3of3(t *testing.T) {
	config := makeConfig(t, 3, testPrivKey1, testPrivKey2, testPrivKey3)
	message := testHash([]byte("test message"))

	sigSet := &pb.MultisigSignatureSet{
		MultisigConfig: config,
		Signatures: []*pb.KeyedSignature{
			{PublicKey: testPrivKey1.Public().Serialize(), Signature: signSchnorr(t, testPrivKey1, message)},
			{PublicKey: testPrivKey2.Public().Serialize(), Signature: signSchnorr(t, testPrivKey2, message)},
			{PublicKey: testPrivKey3.Public().Serialize(), Signature: signSchnorr(t, testPrivKey3, message)},
		},
	}

	err := ValidateMultisigSignatures(config, message, sigSet)
	require.NoError(t, err)
}

func TestValidateMultisigSignatures_ThresholdNotMet(t *testing.T) {
	config := makeConfig(t, 2, testPrivKey1, testPrivKey2, testPrivKey3)
	message := testHash([]byte("test message"))

	sigSet := &pb.MultisigSignatureSet{
		MultisigConfig: config,
		Signatures: []*pb.KeyedSignature{
			{PublicKey: testPrivKey1.Public().Serialize(), Signature: signSchnorr(t, testPrivKey1, message)},
		},
	}

	err := ValidateMultisigSignatures(config, message, sigSet)
	require.Error(t, err)
	require.Contains(t, err.Error(), "threshold")
}

func TestValidateMultisigSignatures_DuplicateSignerRejected(t *testing.T) {
	config := makeConfig(t, 2, testPrivKey1, testPrivKey2, testPrivKey3)
	message := testHash([]byte("test message"))

	sig := signSchnorr(t, testPrivKey1, message)
	sigSet := &pb.MultisigSignatureSet{
		MultisigConfig: config,
		Signatures: []*pb.KeyedSignature{
			{PublicKey: testPrivKey1.Public().Serialize(), Signature: sig},
			{PublicKey: testPrivKey1.Public().Serialize(), Signature: sig},
		},
	}

	err := ValidateMultisigSignatures(config, message, sigSet)
	require.Error(t, err)
	require.Contains(t, err.Error(), "duplicate")
}

func TestValidateMultisigSignatures_NonMemberRejected(t *testing.T) {
	config := makeConfig(t, 2, testPrivKey1, testPrivKey2)
	message := testHash([]byte("test message"))

	sigSet := &pb.MultisigSignatureSet{
		MultisigConfig: config,
		Signatures: []*pb.KeyedSignature{
			{PublicKey: testPrivKey1.Public().Serialize(), Signature: signSchnorr(t, testPrivKey1, message)},
			{PublicKey: testPrivKey3.Public().Serialize(), Signature: signSchnorr(t, testPrivKey3, message)},
		},
	}

	err := ValidateMultisigSignatures(config, message, sigSet)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not a member")
}

func TestValidateMultisigSignatures_InvalidSignatureRejected(t *testing.T) {
	config := makeConfig(t, 2, testPrivKey1, testPrivKey2, testPrivKey3)
	message := testHash([]byte("test message"))
	wrongMessage := testHash([]byte("wrong message"))

	sigSet := &pb.MultisigSignatureSet{
		MultisigConfig: config,
		Signatures: []*pb.KeyedSignature{
			{PublicKey: testPrivKey1.Public().Serialize(), Signature: signSchnorr(t, testPrivKey1, message)},
			{PublicKey: testPrivKey2.Public().Serialize(), Signature: signSchnorr(t, testPrivKey2, wrongMessage)},
		},
	}

	err := ValidateMultisigSignatures(config, message, sigSet)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid")
}

func TestValidateMultisigSignatures_MultisigConfigMismatch(t *testing.T) {
	config := makeConfig(t, 2, testPrivKey1, testPrivKey2)
	wrongConfig := makeConfig(t, 2, testPrivKey1, testPrivKey3)
	message := testHash([]byte("test message"))

	sigSet := &pb.MultisigSignatureSet{
		MultisigConfig: wrongConfig,
		Signatures: []*pb.KeyedSignature{
			{PublicKey: testPrivKey1.Public().Serialize(), Signature: signSchnorr(t, testPrivKey1, message)},
			{PublicKey: testPrivKey2.Public().Serialize(), Signature: signSchnorr(t, testPrivKey2, message)},
		},
	}

	err := ValidateMultisigSignatures(config, message, sigSet)
	require.Error(t, err)
	require.Contains(t, err.Error(), "mismatch")
}

func TestValidateMultisigSignatures_NilInputs(t *testing.T) {
	config := makeConfig(t, 2, testPrivKey1, testPrivKey2)
	message := testHash([]byte("test message"))
	sigSet := &pb.MultisigSignatureSet{
		MultisigConfig: config,
		Signatures: []*pb.KeyedSignature{
			{PublicKey: testPrivKey1.Public().Serialize(), Signature: signSchnorr(t, testPrivKey1, message)},
			{PublicKey: testPrivKey2.Public().Serialize(), Signature: signSchnorr(t, testPrivKey2, message)},
		},
	}

	require.Error(t, ValidateMultisigSignatures(nil, message, sigSet))
	require.Error(t, ValidateMultisigSignatures(config, nil, sigSet))
	require.Error(t, ValidateMultisigSignatures(config, message, nil))
}

func TestValidateMultisigSignatures_EmptySignatures(t *testing.T) {
	config := makeConfig(t, 2, testPrivKey1, testPrivKey2)
	message := testHash([]byte("test message"))

	sigSet := &pb.MultisigSignatureSet{
		MultisigConfig: config,
		Signatures:     []*pb.KeyedSignature{},
	}

	err := ValidateMultisigSignatures(config, message, sigSet)
	require.Error(t, err)
	require.Contains(t, err.Error(), "threshold")
}

func TestValidateMultisigSignatures_ECDSASignatures(t *testing.T) {
	config := makeConfig(t, 2, testPrivKey1, testPrivKey2, testPrivKey3)
	message := testHash([]byte("test message"))

	sigSet := &pb.MultisigSignatureSet{
		MultisigConfig: config,
		Signatures: []*pb.KeyedSignature{
			{PublicKey: testPrivKey1.Public().Serialize(), Signature: signECDSA(t, testPrivKey1, message)},
			{PublicKey: testPrivKey2.Public().Serialize(), Signature: signECDSA(t, testPrivKey2, message)},
		},
	}

	err := ValidateMultisigSignatures(config, message, sigSet)
	require.NoError(t, err)
}

func TestValidateMultisigSignatures_MixedSchnorrAndECDSA(t *testing.T) {
	config := makeConfig(t, 2, testPrivKey1, testPrivKey2, testPrivKey3)
	message := testHash([]byte("test message"))

	sigSet := &pb.MultisigSignatureSet{
		MultisigConfig: config,
		Signatures: []*pb.KeyedSignature{
			{PublicKey: testPrivKey1.Public().Serialize(), Signature: signSchnorr(t, testPrivKey1, message)},
			{PublicKey: testPrivKey2.Public().Serialize(), Signature: signECDSA(t, testPrivKey2, message)},
		},
	}

	err := ValidateMultisigSignatures(config, message, sigSet)
	require.NoError(t, err)
}

func TestValidateMultisigSignatures_MalformedSignatureBytes(t *testing.T) {
	config := makeConfig(t, 2, testPrivKey1, testPrivKey2)
	message := testHash([]byte("test message"))

	sigSet := &pb.MultisigSignatureSet{
		MultisigConfig: config,
		Signatures: []*pb.KeyedSignature{
			{PublicKey: testPrivKey1.Public().Serialize(), Signature: []byte("garbage bytes")},
		},
	}

	err := ValidateMultisigSignatures(config, message, sigSet)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid signature")
}
