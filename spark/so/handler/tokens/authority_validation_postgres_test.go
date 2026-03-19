package tokens

import (
	"bytes"
	"math/big"
	"sort"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	multisigpb "github.com/lightsparkdev/spark/proto/multisig"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/entfixtures"
	"github.com/lightsparkdev/spark/so/knobs"
)

// signAndBuildRequestWithSingleSig builds a BroadcastTransactionRequest using
// the single_signature oneof variant (not the deprecated signature field).
func (s *broadcastTokenPostgresTestSetup) signAndBuildRequestWithSingleSig(
	partial *tokenpb.PartialTokenTransaction,
	signerKey keys.Private,
) *tokenpb.BroadcastTransactionRequest {
	partialHash, _ := s.computeHashes(partial)
	sig, err := schnorr.Sign(signerKey.ToBTCEC(), partialHash)
	require.NoError(s.t, err)

	return &tokenpb.BroadcastTransactionRequest{
		PartialTokenTransaction: partial,
		TokenTransactionOwnerSignatures: []*tokenpb.SignatureWithIndex{
			{
				InputIndex: 0,
				AuthoritySignatures: &tokenpb.SignatureWithIndex_SingleSignature{
					SingleSignature: &multisigpb.KeyedSignature{
						PublicKey: signerKey.Public().Serialize(),
						Signature: sig.Serialize(),
					},
				},
			},
		},
		IdentityPublicKey: signerKey.Public().Serialize(),
	}
}

// createMultisigConfig generates n key pairs, builds a t-of-n MultisigConfig
// proto, and returns the sorted private keys and the config.
func createMultisigConfig(
	t *testing.T,
	numKeys int,
	threshold uint32,
) ([]keys.Private, *multisigpb.MultisigConfig) {
	t.Helper()

	privKeys := make([]keys.Private, numKeys)
	pubKeyBytes := make([][]byte, numKeys)
	for i := range numKeys {
		privKeys[i] = keys.GeneratePrivateKey()
		pubKeyBytes[i] = privKeys[i].Public().Serialize()
	}

	sort.Slice(pubKeyBytes, func(i, j int) bool {
		return bytes.Compare(pubKeyBytes[i], pubKeyBytes[j]) < 0
	})

	sortedPrivKeys := make([]keys.Private, numKeys)
	for i, pkBytes := range pubKeyBytes {
		for _, priv := range privKeys {
			if bytes.Equal(priv.Public().Serialize(), pkBytes) {
				sortedPrivKeys[i] = priv
				break
			}
		}
	}

	protoConfig := &multisigpb.MultisigConfig{
		Version:    0,
		Threshold:  threshold,
		PublicKeys: pubKeyBytes,
	}

	return sortedPrivKeys, protoConfig
}

// ---------- Tests: Broadcast with single_signature oneof ----------

func TestBroadcastTokenTransaction_Phase2_MintWithSingleSignatureOneof(t *testing.T) {
	setup := setUpPhase2BroadcastTestHandlerPostgres(t)
	ctx := knobs.InjectKnobsService(setup.ctx, v3Phase2EnabledKnobs())

	issuerPriv, tokenCreate := setup.fixtures.CreateTokenCreateWithIssuer(btcnetwork.Regtest, nil, nil)
	setup.fixtures.CreateKeyshare()

	partial := setup.buildMintPartial(issuerPriv, tokenCreate)
	req := setup.signAndBuildRequestWithSingleSig(partial, issuerPriv)

	resp, err := setup.handler.BroadcastTokenTransaction(ctx, req)

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, tokenpb.CommitStatus_COMMIT_FINALIZED, resp.CommitStatus)
	assert.NotNil(t, resp.FinalTokenTransaction)
}

func TestBroadcastTokenTransaction_Phase2_TransferWithSingleSignatureOneof(t *testing.T) {
	setup := setUpPhase2BroadcastTestHandlerPostgres(t)
	ctx := knobs.InjectKnobsService(setup.ctx, v3Phase2EnabledKnobs())

	ownerPriv, tokenCreate := setup.fixtures.CreateTokenCreateWithIssuer(btcnetwork.Regtest, nil, nil)
	_, outputs := setup.fixtures.CreateMintTransaction(
		tokenCreate,
		entfixtures.OutputSpecsWithOwner(ownerPriv.Public(), big.NewInt(100)),
		st.TokenTransactionStatusFinalized,
	)
	inputTTXO := outputs[0]
	setup.fixtures.CreateKeyshare()

	partial := setup.buildTransferPartial(ownerPriv, tokenCreate, inputTTXO)
	req := setup.signAndBuildRequestWithSingleSig(partial, ownerPriv)

	resp, err := setup.handler.BroadcastTokenTransaction(ctx, req)

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, tokenpb.CommitStatus_COMMIT_PROCESSING, resp.CommitStatus)
	assert.NotNil(t, resp.FinalTokenTransaction)
}

func TestBroadcastTokenTransaction_Phase2_MintWithNoSignature(t *testing.T) {
	setup := setUpPhase2BroadcastTestHandlerPostgres(t)
	ctx := knobs.InjectKnobsService(setup.ctx, v3Phase2EnabledKnobs())

	issuerPriv, tokenCreate := setup.fixtures.CreateTokenCreateWithIssuer(btcnetwork.Regtest, nil, nil)
	setup.fixtures.CreateKeyshare()

	partial := setup.buildMintPartial(issuerPriv, tokenCreate)

	req := &tokenpb.BroadcastTransactionRequest{
		PartialTokenTransaction: partial,
		TokenTransactionOwnerSignatures: []*tokenpb.SignatureWithIndex{
			{InputIndex: 0},
		},
		IdentityPublicKey: issuerPriv.Public().Serialize(),
	}

	resp, err := setup.handler.BroadcastTokenTransaction(ctx, req)

	require.Error(t, err)
	require.Nil(t, resp)
	assert.Contains(t, err.Error(), "no signature provided")
}

// ---------- Tests: Multisig validation (via ValidateOwnershipSignatureFromAuthority) ----------

func TestValidateOwnershipSignatureFromAuthority_MultisigSuccess(t *testing.T) {
	ctx := t.Context()

	sortedPrivKeys, msConfig := createMultisigConfig(t, 3, 2)
	ownerPubKey := sortedPrivKeys[0].Public()

	hash := make([]byte, 32)
	hash[0] = 0xAB

	sigs := make([]*multisigpb.KeyedSignature, 2)
	for i := range 2 {
		sig, err := schnorr.Sign(sortedPrivKeys[i].ToBTCEC(), hash)
		require.NoError(t, err)
		sigs[i] = &multisigpb.KeyedSignature{
			PublicKey: sortedPrivKeys[i].Public().Serialize(),
			Signature: sig.Serialize(),
		}
	}

	sigWithIndex := &tokenpb.SignatureWithIndex{
		InputIndex: 0,
		AuthoritySignatures: &tokenpb.SignatureWithIndex_MultisigSignatures{
			MultisigSignatures: &multisigpb.MultisigSignatureSet{
				MultisigConfig: msConfig,
				Signatures:     sigs,
			},
		},
	}

	err := ValidateOwnershipSignatureFromAuthority(ctx, sigWithIndex, hash, ownerPubKey)
	require.NoError(t, err)
}

func TestValidateOwnershipSignatureFromAuthority_MultisigThresholdNotMet(t *testing.T) {
	ctx := t.Context()

	sortedPrivKeys, msConfig := createMultisigConfig(t, 3, 2)
	ownerPubKey := sortedPrivKeys[0].Public()

	hash := make([]byte, 32)
	hash[0] = 0xAB

	// Only 1 signature for a 2-of-3 threshold.
	sig, err := schnorr.Sign(sortedPrivKeys[0].ToBTCEC(), hash)
	require.NoError(t, err)

	sigWithIndex := &tokenpb.SignatureWithIndex{
		InputIndex: 0,
		AuthoritySignatures: &tokenpb.SignatureWithIndex_MultisigSignatures{
			MultisigSignatures: &multisigpb.MultisigSignatureSet{
				MultisigConfig: msConfig,
				Signatures: []*multisigpb.KeyedSignature{
					{
						PublicKey: sortedPrivKeys[0].Public().Serialize(),
						Signature: sig.Serialize(),
					},
				},
			},
		},
	}

	err = ValidateOwnershipSignatureFromAuthority(ctx, sigWithIndex, hash, ownerPubKey)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "threshold not met")
}

func TestValidateOwnershipSignatureFromAuthority_SingleSignatureOneof(t *testing.T) {
	ctx := t.Context()

	privKey := keys.GeneratePrivateKey()
	hash := make([]byte, 32)
	hash[0] = 0xAB

	sig, err := schnorr.Sign(privKey.ToBTCEC(), hash)
	require.NoError(t, err)

	sigWithIndex := &tokenpb.SignatureWithIndex{
		InputIndex: 0,
		AuthoritySignatures: &tokenpb.SignatureWithIndex_SingleSignature{
			SingleSignature: &multisigpb.KeyedSignature{
				PublicKey: privKey.Public().Serialize(),
				Signature: sig.Serialize(),
			},
		},
	}

	err = ValidateOwnershipSignatureFromAuthority(ctx, sigWithIndex, hash, privKey.Public())
	require.NoError(t, err)
}

func TestValidateOwnershipSignatureFromAuthority_DeprecatedFieldFallback(t *testing.T) {
	ctx := t.Context()

	privKey := keys.GeneratePrivateKey()
	hash := make([]byte, 32)
	hash[0] = 0xAB

	sig, err := schnorr.Sign(privKey.ToBTCEC(), hash)
	require.NoError(t, err)

	sigWithIndex := &tokenpb.SignatureWithIndex{
		InputIndex: 0,
		Signature:  sig.Serialize(),
	}

	err = ValidateOwnershipSignatureFromAuthority(ctx, sigWithIndex, hash, privKey.Public())
	require.NoError(t, err)
}

func TestValidateOwnershipSignatureFromAuthority_NoSignatureAtAll(t *testing.T) {
	ctx := t.Context()

	privKey := keys.GeneratePrivateKey()
	hash := make([]byte, 32)
	hash[0] = 0xAB

	sigWithIndex := &tokenpb.SignatureWithIndex{
		InputIndex: 0,
	}

	err := ValidateOwnershipSignatureFromAuthority(ctx, sigWithIndex, hash, privKey.Public())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no signature provided")
}

func TestValidateOwnershipSignatureFromAuthority_RejectsMultisigWithDeprecatedField(t *testing.T) {
	ctx := t.Context()

	sortedPrivKeys, msConfig := createMultisigConfig(t, 3, 2)
	ownerPubKey := sortedPrivKeys[0].Public()

	hash := make([]byte, 32)
	hash[0] = 0xAB

	sigs := make([]*multisigpb.KeyedSignature, 2)
	for i := range 2 {
		sig, err := schnorr.Sign(sortedPrivKeys[i].ToBTCEC(), hash)
		require.NoError(t, err)
		sigs[i] = &multisigpb.KeyedSignature{
			PublicKey: sortedPrivKeys[i].Public().Serialize(),
			Signature: sig.Serialize(),
		}
	}

	sigWithIndex := &tokenpb.SignatureWithIndex{
		InputIndex: 0,
		Signature:  []byte{0xFF},
		AuthoritySignatures: &tokenpb.SignatureWithIndex_MultisigSignatures{
			MultisigSignatures: &multisigpb.MultisigSignatureSet{
				MultisigConfig: msConfig,
				Signatures:     sigs,
			},
		},
	}

	err := ValidateOwnershipSignatureFromAuthority(ctx, sigWithIndex, hash, ownerPubKey)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deprecated signature field must not be set")
}
