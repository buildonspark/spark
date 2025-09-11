package grpctest

import (
	"crypto/sha256"
	"math/rand/v2"
	"testing"

	"github.com/lightsparkdev/spark/common/keys"

	"github.com/stretchr/testify/require"

	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/txscript"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pbfrost "github.com/lightsparkdev/spark/proto/frost"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/helper"
	"github.com/lightsparkdev/spark/so/objects"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/assert"
)

// TestFrostSign tests the frost signing process.
// It mimics both the user and signing coordinator side of the frost signing process.
// Since the FROST signer is a stateless signer except for DKG, it is reused for both the user and the operator.
func TestFrostSign(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{1})
	// Step 1: Setup config
	config, err := sparktesting.TestConfig()
	require.NoError(t, err)

	ctx, _ := db.NewTestContext(t, config.DatabaseDriver(), config.DatabasePath)

	msg := []byte("hello")
	msgHash := sha256.Sum256(msg)

	// Step 2: Get operator key share
	operatorKeyShares, err := ent.GetUnusedSigningKeyshares(ctx, config, 1)
	require.NoError(t, err)
	operatorKeyShare := operatorKeyShares[0]
	operatorPubKey, err := keys.ParsePublicKey(operatorKeyShare.PublicKey)
	require.NoError(t, err)

	// Step 3: Get user key pubkey
	privKey := keys.MustGeneratePrivateKeyFromRand(rng)
	userPubKey := privKey.Public()

	// Step 4: Calculate verifying key
	verifyingKey := operatorPubKey.Add(userPubKey)

	// User identifier will not be used in this test, so we can use any string.
	userIdentifier := "0000000000000000000000000000000000000000000000000000000000000063"
	userKeyPackage := pbfrost.KeyPackage{
		Identifier:  userIdentifier,
		SecretShare: privKey.Serialize(),
		PublicShares: map[string][]byte{
			userIdentifier: userPubKey.Serialize(),
		},
		PublicKey:  verifyingKey.Serialize(),
		MinSigners: uint32(config.Threshold),
	}

	// Step 5: Generate user side of nonce.
	hidingPriv := keys.MustGeneratePrivateKeyFromRand(rng)

	bindingPriv := keys.MustGeneratePrivateKeyFromRand(rng)

	hidingPubBytes := hidingPriv.Public().Serialize()
	bindingPubBytes := bindingPriv.Public().Serialize()
	userNonceCommitment, err := objects.NewSigningCommitment(bindingPubBytes, hidingPubBytes)
	require.NoError(t, err)
	userNonce, err := objects.NewSigningNonce(bindingPriv.Serialize(), hidingPriv.Serialize())
	require.NoError(t, err)
	userNonceProto, err := userNonce.MarshalProto()
	require.NoError(t, err)
	userNonceCommitmentProto, err := userNonceCommitment.MarshalProto()
	require.NoError(t, err)

	// Step 6: Operator signing
	signingJobs := []*helper.SigningJob{
		{
			JobID:             uuid.New().String(),
			SigningKeyshareID: operatorKeyShare.ID,
			Message:           msgHash[:],
			VerifyingKey:      &verifyingKey,
			UserCommitment:    userNonceCommitment,
		},
		{
			JobID:             uuid.New().String(),
			SigningKeyshareID: operatorKeyShare.ID,
			Message:           msgHash[:],
			VerifyingKey:      &verifyingKey,
			UserCommitment:    userNonceCommitment,
		},
	}
	signingResult, err := helper.SignFrost(ctx, config, signingJobs)
	require.NoError(t, err)
	operatorCommitments := signingResult[0].SigningCommitments
	operatorCommitmentsProto := make(map[string]*pbcommon.SigningCommitment)
	for id, commitment := range operatorCommitments {
		commitmentProto, err := commitment.MarshalProto()
		require.NoError(t, err)
		operatorCommitmentsProto[id] = commitmentProto
	}

	// Step 7: User signing
	conn, err := sparktesting.DangerousNewGRPCConnectionWithoutTLS(config.SignerAddress, nil)
	require.NoError(t, err)
	defer conn.Close()
	client := pbfrost.NewFrostServiceClient(conn)

	userJobID := uuid.New().String()
	userSigningJobs := []*pbfrost.FrostSigningJob{{
		JobId:           userJobID,
		Message:         msgHash[:],
		KeyPackage:      &userKeyPackage,
		VerifyingKey:    verifyingKey.Serialize(),
		Nonce:           userNonceProto,
		Commitments:     operatorCommitmentsProto,
		UserCommitments: userNonceCommitmentProto,
	}}
	userSignatures, err := client.SignFrost(ctx, &pbfrost.SignFrostRequest{
		SigningJobs: userSigningJobs,
		Role:        pbfrost.SigningRole_USER,
	})
	require.NoError(t, err)

	// Step 7.5: Validate all signature shares
	// SE part
	for identifier, signature := range signingResult[0].SignatureShares {
		_, err = client.ValidateSignatureShare(ctx, &pbfrost.ValidateSignatureShareRequest{
			Identifier:      identifier,
			Role:            pbfrost.SigningRole_STATECHAIN,
			Message:         msgHash[:],
			SignatureShare:  signature,
			PublicShare:     signingResult[0].PublicKeys[identifier],
			VerifyingKey:    verifyingKey.Serialize(),
			Commitments:     operatorCommitmentsProto,
			UserCommitments: userNonceCommitmentProto,
		})
		require.NoError(t, err)
	}

	// User part
	_, err = client.ValidateSignatureShare(ctx, &pbfrost.ValidateSignatureShareRequest{
		Role:            pbfrost.SigningRole_USER,
		Message:         msgHash[:],
		SignatureShare:  userSignatures.Results[userJobID].SignatureShare,
		PublicShare:     userPubKey.Serialize(),
		VerifyingKey:    verifyingKey.Serialize(),
		Commitments:     operatorCommitmentsProto,
		UserCommitments: userNonceCommitmentProto,
	})
	require.NoError(t, err)

	// Step 8: Signature aggregation - The aggregation is successful only if the signature is valid.
	signatureShares := signingResult[0].SignatureShares
	publicKeys := signingResult[0].PublicKeys
	signatureResult, err := client.AggregateFrost(ctx, &pbfrost.AggregateFrostRequest{
		Message:            msgHash[:],
		SignatureShares:    signatureShares,
		PublicShares:       publicKeys,
		VerifyingKey:       verifyingKey.Serialize(),
		Commitments:        operatorCommitmentsProto,
		UserCommitments:    userNonceCommitmentProto,
		UserPublicKey:      userPubKey.Serialize(),
		UserSignatureShare: userSignatures.Results[userJobID].SignatureShare,
	})
	require.NoError(t, err)

	// Step 9: Verify signature using go lib.
	sig, err := schnorr.ParseSignature(signatureResult.Signature)
	require.NoError(t, err)

	taprootKey := txscript.ComputeTaprootKeyNoScript(verifyingKey.ToBTCEC())

	verified := sig.Verify(msgHash[:], taprootKey)
	if !verified {
		t.Fatal("signature verification failed")
	}
}

func TestFrostWithoutUserSign(t *testing.T) {
	// Step 1: Setup config
	config, err := sparktesting.TestConfig()
	require.NoError(t, err)

	ctx, _ := db.NewTestContext(t, config.DatabaseDriver(), config.DatabasePath)

	msg := []byte("hello")
	msgHash := sha256.Sum256(msg)

	// Step 2: Get operator key share
	operatorKeyShares, err := ent.GetUnusedSigningKeyshares(ctx, config, 1)
	require.NoError(t, err)
	operatorKeyShare := operatorKeyShares[0]
	operatorPubKey, err := keys.ParsePublicKey(operatorKeyShare.PublicKey)
	require.NoError(t, err)

	// Step 3: Operator signing
	signingJobs := []*helper.SigningJob{{
		JobID:             uuid.New().String(),
		SigningKeyshareID: operatorKeyShare.ID,
		Message:           msgHash[:],
		VerifyingKey:      &operatorPubKey,
		UserCommitment:    nil,
	}}
	signingResult, err := helper.SignFrost(ctx, config, signingJobs)
	require.NoError(t, err)
	operatorCommitments := signingResult[0].SigningCommitments
	operatorCommitmentsProto := make(map[string]*pbcommon.SigningCommitment)
	for id, commitment := range operatorCommitments {
		commitmentProto, err := commitment.MarshalProto()
		require.NoError(t, err)
		operatorCommitmentsProto[id] = commitmentProto
	}

	// Step 5: Signature aggregation
	conn, err := sparktesting.DangerousNewGRPCConnectionWithoutTLS(config.SignerAddress, nil)
	require.NoError(t, err)
	defer conn.Close()
	client := pbfrost.NewFrostServiceClient(conn)
	signatureShares := signingResult[0].SignatureShares
	publicKeys := signingResult[0].PublicKeys
	_, err = client.AggregateFrost(ctx, &pbfrost.AggregateFrostRequest{
		Message:         msgHash[:],
		SignatureShares: signatureShares,
		PublicShares:    publicKeys,
		VerifyingKey:    operatorPubKey.Serialize(),
		Commitments:     operatorCommitmentsProto,
	})
	require.NoError(t, err)
}

// TestFrostSign tests the frost signing process.
// It mimics both the user and signing coordinator side of the frost signing process.
// Since the FROST signer is a stateless signer except for DKG, it is reused for both the user and the operator.
func TestFrostSignWithAdaptor(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{2})
	// Step 0: Create adaptor.
	sk := keys.MustGeneratePrivateKeyFromRand(rng)
	pk := sk.Public()

	msg := []byte("hello")
	msgHash := sha256.Sum256(msg)
	senderSig, err := schnorr.Sign(sk.ToBTCEC(), msgHash[:], schnorr.FastSign())
	require.NoError(t, err)

	assert.True(t, senderSig.Verify(msgHash[:], pk.ToBTCEC()))

	adaptorSig, adaptorPrivKeyBytes, err := common.GenerateAdaptorFromSignature(senderSig.Serialize())
	require.NoError(t, err)

	adaptorPrivKey, err := keys.ParsePrivateKey(adaptorPrivKeyBytes)
	require.NoError(t, err)
	adaptorPub := adaptorPrivKey.Public()

	err = common.ValidateOutboundAdaptorSignature(pk.ToBTCEC(), msgHash[:], adaptorSig, adaptorPub.Serialize())
	require.NoError(t, err)

	// Step 1: Setup config
	config, err := sparktesting.TestConfig()
	require.NoError(t, err)

	ctx, _ := db.NewTestContext(t, config.DatabaseDriver(), config.DatabasePath)

	// Step 2: Get operator key share
	operatorKeyShares, err := ent.GetUnusedSigningKeyshares(ctx, config, 1)
	require.NoError(t, err)
	operatorKeyShare := operatorKeyShares[0]
	operatorPubKey, err := keys.ParsePublicKey(operatorKeyShare.PublicKey)
	require.NoError(t, err)

	// Step 3: Get user key pubkey
	privKey := keys.MustGeneratePrivateKeyFromRand(rng)
	userPubKey := privKey.Public()

	// Step 4: Calculate verifying key
	verifyingKey := operatorPubKey.Add(userPubKey)

	// User identifier will not be used in this test, so we can use any string.
	userIdentifier := "0000000000000000000000000000000000000000000000000000000000000063"
	userKeyPackage := pbfrost.KeyPackage{
		Identifier:  userIdentifier,
		SecretShare: privKey.Serialize(),
		PublicShares: map[string][]byte{
			userIdentifier: userPubKey.Serialize(),
		},
		PublicKey:  verifyingKey.Serialize(),
		MinSigners: uint32(config.Threshold),
	}

	// Step 5: Generate user side of nonce.
	hidingPriv := keys.MustGeneratePrivateKeyFromRand(rng)

	bindingPriv := keys.MustGeneratePrivateKeyFromRand(rng)

	hidingPubBytes := hidingPriv.Public().Serialize()
	bindingPubBytes := bindingPriv.Public().Serialize()
	userNonceCommitment, err := objects.NewSigningCommitment(bindingPubBytes, hidingPubBytes)
	require.NoError(t, err)
	userNonce, err := objects.NewSigningNonce(bindingPriv.Serialize(), hidingPriv.Serialize())
	require.NoError(t, err)
	userNonceProto, err := userNonce.MarshalProto()
	require.NoError(t, err)
	userNonceCommitmentProto, err := userNonceCommitment.MarshalProto()
	require.NoError(t, err)

	// Step 6: Operator signing
	signingJobs := []*helper.SigningJob{{
		JobID:             uuid.New().String(),
		SigningKeyshareID: operatorKeyShare.ID,
		Message:           msgHash[:],
		VerifyingKey:      &verifyingKey,
		UserCommitment:    userNonceCommitment,
		AdaptorPublicKey:  &adaptorPub,
	}}
	signingResult, err := helper.SignFrost(ctx, config, signingJobs)
	require.NoError(t, err)
	operatorCommitments := signingResult[0].SigningCommitments
	operatorCommitmentsProto := make(map[string]*pbcommon.SigningCommitment)
	for id, commitment := range operatorCommitments {
		commitmentProto, err := commitment.MarshalProto()
		require.NoError(t, err)
		operatorCommitmentsProto[id] = commitmentProto
	}

	// Step 7: User signing
	conn, err := sparktesting.DangerousNewGRPCConnectionWithoutTLS(config.SignerAddress, nil)
	require.NoError(t, err)
	defer conn.Close()
	client := pbfrost.NewFrostServiceClient(conn)
	userJobID := uuid.New().String()
	userSigningJobs := []*pbfrost.FrostSigningJob{{
		JobId:            userJobID,
		Message:          msgHash[:],
		KeyPackage:       &userKeyPackage,
		VerifyingKey:     verifyingKey.Serialize(),
		Nonce:            userNonceProto,
		Commitments:      operatorCommitmentsProto,
		UserCommitments:  userNonceCommitmentProto,
		AdaptorPublicKey: adaptorPub.Serialize(),
	}}
	userSignatures, err := client.SignFrost(ctx, &pbfrost.SignFrostRequest{
		SigningJobs: userSigningJobs,
		Role:        pbfrost.SigningRole_USER,
	})
	require.NoError(t, err)

	// Step 8: Signature aggregation - The aggregation is successful only if the signature is valid.
	signatureShares := signingResult[0].SignatureShares
	publicKeys := signingResult[0].PublicKeys
	signatureResp, err := client.AggregateFrost(ctx, &pbfrost.AggregateFrostRequest{
		Message:            msgHash[:],
		SignatureShares:    signatureShares,
		PublicShares:       publicKeys,
		VerifyingKey:       verifyingKey.Serialize(),
		Commitments:        operatorCommitmentsProto,
		UserCommitments:    userNonceCommitmentProto,
		UserPublicKey:      userPubKey.Serialize(),
		UserSignatureShare: userSignatures.Results[userJobID].SignatureShare,
		AdaptorPublicKey:   adaptorPub.Serialize(),
	})
	require.NoError(t, err)

	taprootKey := txscript.ComputeTaprootKeyNoScript(verifyingKey.ToBTCEC())
	_, err = common.ApplyAdaptorToSignature(taprootKey, msgHash[:], signatureResp.Signature, adaptorPrivKeyBytes)
	require.NoError(t, err)
}

func TestFrostRound1(t *testing.T) {
	config, err := sparktesting.TestConfig()
	require.NoError(t, err)

	ctx, _ := db.NewTestContext(t, config.DatabaseDriver(), config.DatabasePath)

	operatorKeyShares, err := ent.GetUnusedSigningKeyshares(ctx, config, 5)
	require.NoError(t, err)

	operatorKeyShareIDs := make([]uuid.UUID, len(operatorKeyShares))
	for i, keyShare := range operatorKeyShares {
		operatorKeyShareIDs[i] = keyShare.ID
	}

	t.Run("GetSigningCommitments with count 1", func(t *testing.T) {
		operatorNonceCommitments, err := helper.GetSigningCommitments(ctx, config, operatorKeyShareIDs, 1)
		require.NoError(t, err)

		for _, commitments := range operatorNonceCommitments {
			assert.Len(t, commitments, 5)
		}
	})

	t.Run("GetSigningCommitments with count 5", func(t *testing.T) {
		operatorNonceCommitments, err := helper.GetSigningCommitments(ctx, config, operatorKeyShareIDs, 5)
		require.NoError(t, err)

		for _, commitments := range operatorNonceCommitments {
			assert.Len(t, commitments, 5*5)
		}
	})
}

func TestFrostSigningWithPregeneratedNonce(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{3})
	// Step 1: Setup config
	config, err := sparktesting.TestConfig()
	require.NoError(t, err)

	ctx, _ := db.NewTestContext(t, config.DatabaseDriver(), config.DatabasePath)

	msg := []byte("hello")
	msgHash := sha256.Sum256(msg)

	// Step 2: Get operator key share
	operatorKeyShares, err := ent.GetUnusedSigningKeyshares(ctx, config, 1)
	require.NoError(t, err)
	operatorKeyShare := operatorKeyShares[0]
	operatorPubKey, err := keys.ParsePublicKey(operatorKeyShare.PublicKey)
	require.NoError(t, err)

	// Step 3: Get user key pubkey
	privKey := keys.MustGeneratePrivateKeyFromRand(rng)
	userPubKey := privKey.Public()

	// Step 4: Calculate verifying key
	verifyingKey := operatorPubKey.Add(userPubKey)

	// User identifier will not be used in this test, so we can use any string.
	userIdentifier := "0000000000000000000000000000000000000000000000000000000000000063"
	userKeyPackage := pbfrost.KeyPackage{
		Identifier:  userIdentifier,
		SecretShare: privKey.Serialize(),
		PublicShares: map[string][]byte{
			userIdentifier: userPubKey.Serialize(),
		},
		PublicKey:  verifyingKey.Serialize(),
		MinSigners: uint32(config.Threshold),
	}

	// Step 5: Generate user side of nonce.
	hidingPriv := keys.MustGeneratePrivateKeyFromRand(rng)
	bindingPriv := keys.MustGeneratePrivateKeyFromRand(rng)

	hidingPubBytes := hidingPriv.Public().Serialize()
	bindingPubBytes := bindingPriv.Public().Serialize()
	userNonceCommitment, err := objects.NewSigningCommitment(bindingPubBytes, hidingPubBytes)
	require.NoError(t, err)
	userNonce, err := objects.NewSigningNonce(bindingPriv.Serialize(), hidingPriv.Serialize())
	require.NoError(t, err)
	userNonceProto, err := userNonce.MarshalProto()
	require.NoError(t, err)
	userNonceCommitmentProto, err := userNonceCommitment.MarshalProto()
	require.NoError(t, err)

	// Step 6: Generate operator side of nonce.
	operatorNonceCommitments, err := helper.GetSigningCommitments(ctx, config, []uuid.UUID{operatorKeyShare.ID}, 1)
	require.NoError(t, err)
	operatorNonceCommitmentArray := common.MapOfArrayToArrayOfMap(operatorNonceCommitments)
	operatorCommitmentsProto := make(map[string]*pbcommon.SigningCommitment)
	for id, commitment := range operatorNonceCommitmentArray[0] {
		commitmentProto, err := commitment.MarshalProto()
		require.NoError(t, err)
		operatorCommitmentsProto[id] = commitmentProto
	}

	// Step 7: User signing
	conn, err := sparktesting.DangerousNewGRPCConnectionWithoutTLS(config.SignerAddress, nil)
	require.NoError(t, err)
	defer conn.Close()
	client := pbfrost.NewFrostServiceClient(conn)
	userJobID := uuid.New().String()
	userSigningJobs := []*pbfrost.FrostSigningJob{{
		JobId:           userJobID,
		Message:         msgHash[:],
		KeyPackage:      &userKeyPackage,
		VerifyingKey:    verifyingKey.Serialize(),
		Nonce:           userNonceProto,
		Commitments:     operatorCommitmentsProto,
		UserCommitments: userNonceCommitmentProto,
	}}
	userSignatures, err := client.SignFrost(ctx, &pbfrost.SignFrostRequest{
		SigningJobs: userSigningJobs,
		Role:        pbfrost.SigningRole_USER,
	})
	require.NoError(t, err)

	// Step 8: Operator signing
	signingJobs := []*helper.SigningJobWithPregeneratedNonce{{
		SigningJob: helper.SigningJob{
			JobID:             uuid.New().String(),
			SigningKeyshareID: operatorKeyShare.ID,
			Message:           msgHash[:],
			VerifyingKey:      &verifyingKey,
			UserCommitment:    userNonceCommitment,
		},
		Round1Packages: operatorNonceCommitmentArray[0],
	}}
	signingResult, err := helper.SignFrostWithPregeneratedNonce(ctx, config, signingJobs)
	require.NoError(t, err)

	// Step 9: Validate all signature shares
	// SE part
	for identifier, signature := range signingResult[0].SignatureShares {
		_, err = client.ValidateSignatureShare(ctx, &pbfrost.ValidateSignatureShareRequest{
			Identifier:      identifier,
			Role:            pbfrost.SigningRole_STATECHAIN,
			Message:         msgHash[:],
			SignatureShare:  signature,
			PublicShare:     signingResult[0].PublicKeys[identifier],
			VerifyingKey:    verifyingKey.Serialize(),
			Commitments:     operatorCommitmentsProto,
			UserCommitments: userNonceCommitmentProto,
		})
		require.NoError(t, err)
	}

	// User part
	_, err = client.ValidateSignatureShare(ctx, &pbfrost.ValidateSignatureShareRequest{
		Role:            pbfrost.SigningRole_USER,
		Message:         msgHash[:],
		SignatureShare:  userSignatures.Results[userJobID].SignatureShare,
		PublicShare:     userPubKey.Serialize(),
		VerifyingKey:    verifyingKey.Serialize(),
		Commitments:     operatorCommitmentsProto,
		UserCommitments: userNonceCommitmentProto,
	})
	require.NoError(t, err)

	// Step 10: Signature aggregation - The aggregation is successful only if the signature is valid.
	signatureShares := signingResult[0].SignatureShares
	publicKeys := signingResult[0].PublicKeys
	signatureResult, err := client.AggregateFrost(ctx, &pbfrost.AggregateFrostRequest{
		Message:            msgHash[:],
		SignatureShares:    signatureShares,
		PublicShares:       publicKeys,
		VerifyingKey:       verifyingKey.Serialize(),
		Commitments:        operatorCommitmentsProto,
		UserCommitments:    userNonceCommitmentProto,
		UserPublicKey:      userPubKey.Serialize(),
		UserSignatureShare: userSignatures.Results[userJobID].SignatureShare,
	})
	require.NoError(t, err)

	// Step 11: Verify signature using go lib.
	sig, err := schnorr.ParseSignature(signatureResult.Signature)
	require.NoError(t, err)
	taprootKey := txscript.ComputeTaprootKeyNoScript(verifyingKey.ToBTCEC())
	require.True(t, sig.Verify(msgHash[:], taprootKey), "signature verification failed")
}
