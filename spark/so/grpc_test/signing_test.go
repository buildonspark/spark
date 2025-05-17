package grpctest

import (
	"crypto/sha256"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/txscript"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pbfrost "github.com/lightsparkdev/spark/proto/frost"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/helper"
	"github.com/lightsparkdev/spark/so/objects"
	testutil "github.com/lightsparkdev/spark/test_util"
	"github.com/stretchr/testify/assert"
)

// TestFrostSign tests the frost signing process.
// It mimics both the user and signing coordinator side of the frost signing process.
// Since the FROST signer is a stateless signer except for DKG, it is reused for both the user and the operator.
func TestFrostSign(t *testing.T) {
	// Step 1: Setup config
	config, err := testutil.TestConfig()
	if err != nil {
		t.Fatal(err)
	}

	ctx, dbClient, err := testutil.TestContext(config)
	if err != nil {
		t.Fatal(err)
	}

	msg := []byte("hello")
	msgHash := sha256.Sum256(msg)

	// Step 2: Get operator key share
	operatorKeyShares, err := ent.GetUnusedSigningKeyshares(ctx, dbClient, config, 1)
	if err != nil {
		t.Fatal(err)
	}
	operatorKeyShare := operatorKeyShares[0]
	operatorPubKeyBytes := operatorKeyShare.PublicKey

	// Step 3: Get user key pubkey
	privKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	userPubKey := privKey.PubKey()
	userPubKeyBytes := userPubKey.SerializeCompressed()

	// Step 4: Calculate verifying key
	verifyingKeyBytes, err := common.AddPublicKeys(operatorPubKeyBytes, userPubKeyBytes)
	if err != nil {
		t.Fatal(err)
	}

	// User identifier will not be used in this test, so we can use any string.
	userIdentifier := "0000000000000000000000000000000000000000000000000000000000000063"
	userKeyPackage := pbfrost.KeyPackage{
		Identifier:  userIdentifier,
		SecretShare: privKey.Serialize(),
		PublicShares: map[string][]byte{
			userIdentifier: userPubKeyBytes,
		},
		PublicKey:  verifyingKeyBytes,
		MinSigners: uint32(config.Threshold),
	}

	// Step 5: Generate user side of nonce.
	hidingPriv, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	bindingPriv, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	hidingPubBytes := hidingPriv.PubKey().SerializeCompressed()
	bindingPubBytes := bindingPriv.PubKey().SerializeCompressed()
	userNonceCommitment, err := objects.NewSigningCommitment(bindingPubBytes, hidingPubBytes)
	if err != nil {
		t.Fatal(err)
	}
	userNonce, err := objects.NewSigningNonce(bindingPriv.Serialize(), hidingPriv.Serialize())
	if err != nil {
		t.Fatal(err)
	}
	userNonceProto, err := userNonce.MarshalProto()
	if err != nil {
		t.Fatal(err)
	}
	userNonceCommitmentProto, err := userNonceCommitment.MarshalProto()
	if err != nil {
		t.Fatal(err)
	}

	// Step 6: Operator signing
	signingJobs := make([]*helper.SigningJob, 0)
	signingJobs = append(signingJobs, &helper.SigningJob{
		JobID:             uuid.New().String(),
		SigningKeyshareID: operatorKeyShare.ID,
		Message:           msgHash[:],
		VerifyingKey:      verifyingKeyBytes,
		UserCommitment:    userNonceCommitment,
	})
	signingJobs = append(signingJobs, &helper.SigningJob{
		JobID:             uuid.New().String(),
		SigningKeyshareID: operatorKeyShare.ID,
		Message:           msgHash[:],
		VerifyingKey:      verifyingKeyBytes,
		UserCommitment:    userNonceCommitment,
	})
	signingResult, err := helper.SignFrost(ctx, config, signingJobs)
	if err != nil {
		t.Fatal(err)
	}
	operatorCommitments := signingResult[0].SigningCommitments
	operatorCommitmentsProto := make(map[string]*pbcommon.SigningCommitment)
	for id, commitment := range operatorCommitments {
		commitmentProto, err := commitment.MarshalProto()
		if err != nil {
			t.Fatal(err)
		}
		operatorCommitmentsProto[id] = commitmentProto
	}

	// Step 7: User signing
	conn, err := common.NewGRPCConnectionWithoutTLS(config.SignerAddress, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := pbfrost.NewFrostServiceClient(conn)
	userSigningJobs := make([]*pbfrost.FrostSigningJob, 0)
	userJobID := uuid.New().String()
	userSigningJobs = append(userSigningJobs, &pbfrost.FrostSigningJob{
		JobId:           userJobID,
		Message:         msgHash[:],
		KeyPackage:      &userKeyPackage,
		VerifyingKey:    verifyingKeyBytes,
		Nonce:           userNonceProto,
		Commitments:     operatorCommitmentsProto,
		UserCommitments: userNonceCommitmentProto,
	})
	userSignatures, err := client.SignFrost(ctx, &pbfrost.SignFrostRequest{
		SigningJobs: userSigningJobs,
		Role:        pbfrost.SigningRole_USER,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Step 7.5: Validate all signature shares
	// SE part
	for identifier, signature := range signingResult[0].SignatureShares {
		_, err = client.ValidateSignatureShare(ctx, &pbfrost.ValidateSignatureShareRequest{
			Identifier:      identifier,
			Role:            pbfrost.SigningRole_STATECHAIN,
			Message:         msgHash[:],
			SignatureShare:  signature,
			PublicShare:     signingResult[0].PublicKeys[identifier],
			VerifyingKey:    verifyingKeyBytes,
			Commitments:     operatorCommitmentsProto,
			UserCommitments: userNonceCommitmentProto,
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	// User part
	_, err = client.ValidateSignatureShare(ctx, &pbfrost.ValidateSignatureShareRequest{
		Role:            pbfrost.SigningRole_USER,
		Message:         msgHash[:],
		SignatureShare:  userSignatures.Results[userJobID].SignatureShare,
		PublicShare:     userPubKeyBytes,
		VerifyingKey:    verifyingKeyBytes,
		Commitments:     operatorCommitmentsProto,
		UserCommitments: userNonceCommitmentProto,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Step 8: Signature aggregation - The aggregation is successful only if the signature is valid.
	signatureShares := signingResult[0].SignatureShares
	publicKeys := signingResult[0].PublicKeys
	signatureResult, err := client.AggregateFrost(ctx, &pbfrost.AggregateFrostRequest{
		Message:            msgHash[:],
		SignatureShares:    signatureShares,
		PublicShares:       publicKeys,
		VerifyingKey:       verifyingKeyBytes,
		Commitments:        operatorCommitmentsProto,
		UserCommitments:    userNonceCommitmentProto,
		UserPublicKey:      userPubKeyBytes,
		UserSignatureShare: userSignatures.Results[userJobID].SignatureShare,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Step 9: Verify signature using go lib.
	sig, err := schnorr.ParseSignature(signatureResult.Signature)
	if err != nil {
		t.Fatal(err)
	}

	pubKey, err := btcec.ParsePubKey(verifyingKeyBytes)
	if err != nil {
		t.Fatal(err)
	}
	taprootKey := txscript.ComputeTaprootKeyNoScript(pubKey)

	verified := sig.Verify(msgHash[:], taprootKey)
	if !verified {
		t.Fatal("signature verification failed")
	}
}

func TestFrostWithoutUserSign(t *testing.T) {
	// Step 1: Setup config
	config, err := testutil.TestConfig()
	if err != nil {
		t.Fatal(err)
	}

	ctx, dbClient, err := testutil.TestContext(config)
	if err != nil {
		t.Fatal(err)
	}

	msg := []byte("hello")
	msgHash := sha256.Sum256(msg)

	// Step 2: Get operator key share
	operatorKeyShares, err := ent.GetUnusedSigningKeyshares(ctx, dbClient, config, 1)
	if err != nil {
		t.Fatal(err)
	}
	operatorKeyShare := operatorKeyShares[0]
	operatorPubKeyBytes := operatorKeyShare.PublicKey

	// Step 3: Operator signing
	signingJobs := make([]*helper.SigningJob, 0)
	signingJobs = append(signingJobs, &helper.SigningJob{
		JobID:             uuid.New().String(),
		SigningKeyshareID: operatorKeyShare.ID,
		Message:           msgHash[:],
		VerifyingKey:      operatorPubKeyBytes,
		UserCommitment:    nil,
	})
	signingResult, err := helper.SignFrost(ctx, config, signingJobs)
	if err != nil {
		t.Fatal(err)
	}
	operatorCommitments := signingResult[0].SigningCommitments
	operatorCommitmentsProto := make(map[string]*pbcommon.SigningCommitment)
	for id, commitment := range operatorCommitments {
		commitmentProto, err := commitment.MarshalProto()
		if err != nil {
			t.Fatal(err)
		}
		operatorCommitmentsProto[id] = commitmentProto
	}

	// Step 5: Signature aggregation
	conn, err := common.NewGRPCConnectionWithoutTLS(config.SignerAddress, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := pbfrost.NewFrostServiceClient(conn)
	signatureShares := signingResult[0].SignatureShares
	publicKeys := signingResult[0].PublicKeys
	_, err = client.AggregateFrost(ctx, &pbfrost.AggregateFrostRequest{
		Message:         msgHash[:],
		SignatureShares: signatureShares,
		PublicShares:    publicKeys,
		VerifyingKey:    operatorPubKeyBytes,
		Commitments:     operatorCommitmentsProto,
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestFrostSign tests the frost signing process.
// It mimics both the user and signing coordinator side of the frost signing process.
// Since the FROST signer is a stateless signer except for DKG, it is reused for both the user and the operator.
func TestFrostSignWithAdaptor(t *testing.T) {
	// Step 0: Create adaptor.
	sk, err := btcec.NewPrivateKey()
	assert.NoError(t, err)
	pk := sk.PubKey()

	msg := []byte("hello")
	msgHash := sha256.Sum256(msg)
	senderSig, err := schnorr.Sign(sk, msgHash[:], schnorr.FastSign())
	assert.NoError(t, err)

	assert.True(t, senderSig.Verify(msgHash[:], pk))

	adaptorSig, adaptorPrivKey, err := common.GenerateAdaptorFromSignature(senderSig.Serialize())
	assert.NoError(t, err)

	_, adaptorPub := btcec.PrivKeyFromBytes(adaptorPrivKey)

	err = common.ValidateOutboundAdaptorSignature(pk, msgHash[:], adaptorSig, adaptorPub.SerializeCompressed())
	assert.NoError(t, err)

	// Step 1: Setup config
	config, err := testutil.TestConfig()
	if err != nil {
		t.Fatal(err)
	}

	ctx, dbClient, err := testutil.TestContext(config)
	if err != nil {
		t.Fatal(err)
	}

	// Step 2: Get operator key share
	operatorKeyShares, err := ent.GetUnusedSigningKeyshares(ctx, dbClient, config, 1)
	if err != nil {
		t.Fatal(err)
	}
	operatorKeyShare := operatorKeyShares[0]
	operatorPubKeyBytes := operatorKeyShare.PublicKey

	// Step 3: Get user key pubkey
	privKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	userPubKey := privKey.PubKey()
	userPubKeyBytes := userPubKey.SerializeCompressed()

	// Step 4: Calculate verifying key
	verifyingKeyBytes, err := common.AddPublicKeys(operatorPubKeyBytes, userPubKeyBytes)
	if err != nil {
		t.Fatal(err)
	}

	// User identifier will not be used in this test, so we can use any string.
	userIdentifier := "0000000000000000000000000000000000000000000000000000000000000063"
	userKeyPackage := pbfrost.KeyPackage{
		Identifier:  userIdentifier,
		SecretShare: privKey.Serialize(),
		PublicShares: map[string][]byte{
			userIdentifier: userPubKeyBytes,
		},
		PublicKey:  verifyingKeyBytes,
		MinSigners: uint32(config.Threshold),
	}

	// Step 5: Generate user side of nonce.
	hidingPriv, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	bindingPriv, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	hidingPubBytes := hidingPriv.PubKey().SerializeCompressed()
	bindingPubBytes := bindingPriv.PubKey().SerializeCompressed()
	userNonceCommitment, err := objects.NewSigningCommitment(bindingPubBytes, hidingPubBytes)
	if err != nil {
		t.Fatal(err)
	}
	userNonce, err := objects.NewSigningNonce(bindingPriv.Serialize(), hidingPriv.Serialize())
	if err != nil {
		t.Fatal(err)
	}
	userNonceProto, err := userNonce.MarshalProto()
	if err != nil {
		t.Fatal(err)
	}
	userNonceCommitmentProto, err := userNonceCommitment.MarshalProto()
	if err != nil {
		t.Fatal(err)
	}

	// Step 6: Operator signing
	signingJobs := make([]*helper.SigningJob, 0)
	signingJobs = append(signingJobs, &helper.SigningJob{
		JobID:             uuid.New().String(),
		SigningKeyshareID: operatorKeyShare.ID,
		Message:           msgHash[:],
		VerifyingKey:      verifyingKeyBytes,
		UserCommitment:    userNonceCommitment,
		AdaptorPublicKey:  adaptorPub.SerializeCompressed(),
	})
	signingResult, err := helper.SignFrost(ctx, config, signingJobs)
	if err != nil {
		t.Fatal(err)
	}
	operatorCommitments := signingResult[0].SigningCommitments
	operatorCommitmentsProto := make(map[string]*pbcommon.SigningCommitment)
	for id, commitment := range operatorCommitments {
		commitmentProto, err := commitment.MarshalProto()
		if err != nil {
			t.Fatal(err)
		}
		operatorCommitmentsProto[id] = commitmentProto
	}

	// Step 7: User signing
	conn, err := common.NewGRPCConnectionWithoutTLS(config.SignerAddress, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := pbfrost.NewFrostServiceClient(conn)
	userSigningJobs := make([]*pbfrost.FrostSigningJob, 0)
	userJobID := uuid.New().String()
	userSigningJobs = append(userSigningJobs, &pbfrost.FrostSigningJob{
		JobId:            userJobID,
		Message:          msgHash[:],
		KeyPackage:       &userKeyPackage,
		VerifyingKey:     verifyingKeyBytes,
		Nonce:            userNonceProto,
		Commitments:      operatorCommitmentsProto,
		UserCommitments:  userNonceCommitmentProto,
		AdaptorPublicKey: adaptorPub.SerializeCompressed(),
	})
	userSignatures, err := client.SignFrost(ctx, &pbfrost.SignFrostRequest{
		SigningJobs: userSigningJobs,
		Role:        pbfrost.SigningRole_USER,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Step 8: Signature aggregation - The aggregation is successful only if the signature is valid.
	signatureShares := signingResult[0].SignatureShares
	publicKeys := signingResult[0].PublicKeys
	signatureResp, err := client.AggregateFrost(ctx, &pbfrost.AggregateFrostRequest{
		Message:            msgHash[:],
		SignatureShares:    signatureShares,
		PublicShares:       publicKeys,
		VerifyingKey:       verifyingKeyBytes,
		Commitments:        operatorCommitmentsProto,
		UserCommitments:    userNonceCommitmentProto,
		UserPublicKey:      userPubKeyBytes,
		UserSignatureShare: userSignatures.Results[userJobID].SignatureShare,
		AdaptorPublicKey:   adaptorPub.SerializeCompressed(),
	})
	if err != nil {
		t.Fatal(err)
	}

	pubKey, err := btcec.ParsePubKey(verifyingKeyBytes)
	if err != nil {
		t.Fatal(err)
	}
	taprootKey := txscript.ComputeTaprootKeyNoScript(pubKey)

	_, err = common.ApplyAdaptorToSignature(taprootKey, msgHash[:], signatureResp.Signature, adaptorPrivKey)
	assert.NoError(t, err)
}

func TestFrostSigningWithPregeneratedNonce(t *testing.T) {
	// Step 1: Setup config
	config, err := testutil.TestConfig()
	if err != nil {
		t.Fatal(err)
	}

	ctx, dbClient, err := testutil.TestContext(config)
	if err != nil {
		t.Fatal(err)
	}

	msg := []byte("hello")
	msgHash := sha256.Sum256(msg)

	// Step 2: Get operator key share
	operatorKeyShares, err := ent.GetUnusedSigningKeyshares(ctx, dbClient, config, 1)
	if err != nil {
		t.Fatal(err)
	}
	operatorKeyShare := operatorKeyShares[0]
	operatorPubKeyBytes := operatorKeyShare.PublicKey

	// Step 3: Get user key pubkey
	privKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	userPubKey := privKey.PubKey()
	userPubKeyBytes := userPubKey.SerializeCompressed()

	// Step 4: Calculate verifying key
	verifyingKeyBytes, err := common.AddPublicKeys(operatorPubKeyBytes, userPubKeyBytes)
	if err != nil {
		t.Fatal(err)
	}

	// User identifier will not be used in this test, so we can use any string.
	userIdentifier := "0000000000000000000000000000000000000000000000000000000000000063"
	userKeyPackage := pbfrost.KeyPackage{
		Identifier:  userIdentifier,
		SecretShare: privKey.Serialize(),
		PublicShares: map[string][]byte{
			userIdentifier: userPubKeyBytes,
		},
		PublicKey:  verifyingKeyBytes,
		MinSigners: uint32(config.Threshold),
	}

	// Step 5: Generate user side of nonce.
	hidingPriv, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	bindingPriv, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	hidingPubBytes := hidingPriv.PubKey().SerializeCompressed()
	bindingPubBytes := bindingPriv.PubKey().SerializeCompressed()
	userNonceCommitment, err := objects.NewSigningCommitment(bindingPubBytes, hidingPubBytes)
	if err != nil {
		t.Fatal(err)
	}
	userNonce, err := objects.NewSigningNonce(bindingPriv.Serialize(), hidingPriv.Serialize())
	if err != nil {
		t.Fatal(err)
	}
	userNonceProto, err := userNonce.MarshalProto()
	if err != nil {
		t.Fatal(err)
	}
	userNonceCommitmentProto, err := userNonceCommitment.MarshalProto()
	if err != nil {
		t.Fatal(err)
	}

	// Step 6: Generate operator side of nonce.
	operatorNonceCommitments, err := helper.GetSigningCommitments(ctx, config, []uuid.UUID{operatorKeyShare.ID})
	if err != nil {
		t.Fatal(err)
	}
	operatorNonceCommitmentArray := common.MapOfArrayToArrayOfMap(operatorNonceCommitments)
	operatorCommitmentsProto := make(map[string]*pbcommon.SigningCommitment)
	for id, commitment := range operatorNonceCommitmentArray[0] {
		commitmentProto, err := commitment.MarshalProto()
		if err != nil {
			t.Fatal(err)
		}
		operatorCommitmentsProto[id] = commitmentProto
	}

	// Step 7: User signing
	conn, err := common.NewGRPCConnectionWithoutTLS(config.SignerAddress, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := pbfrost.NewFrostServiceClient(conn)
	userSigningJobs := make([]*pbfrost.FrostSigningJob, 0)
	userJobID := uuid.New().String()
	userSigningJobs = append(userSigningJobs, &pbfrost.FrostSigningJob{
		JobId:           userJobID,
		Message:         msgHash[:],
		KeyPackage:      &userKeyPackage,
		VerifyingKey:    verifyingKeyBytes,
		Nonce:           userNonceProto,
		Commitments:     operatorCommitmentsProto,
		UserCommitments: userNonceCommitmentProto,
	})
	userSignatures, err := client.SignFrost(ctx, &pbfrost.SignFrostRequest{
		SigningJobs: userSigningJobs,
		Role:        pbfrost.SigningRole_USER,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Step 8: Operator signing
	signingJobs := make([]*helper.SigningJobWithPregeneratedNonce, 0)
	signingJobs = append(signingJobs, &helper.SigningJobWithPregeneratedNonce{
		SigningJob: helper.SigningJob{
			JobID:             uuid.New().String(),
			SigningKeyshareID: operatorKeyShare.ID,
			Message:           msgHash[:],
			VerifyingKey:      verifyingKeyBytes,
			UserCommitment:    userNonceCommitment,
		},
		Round1Packages: operatorNonceCommitmentArray[0],
	})
	signingResult, err := helper.SignFrostWithPregeneratedNonce(ctx, config, signingJobs)
	if err != nil {
		t.Fatal(err)
	}

	// Step 9: Validate all signature shares
	// SE part
	for identifier, signature := range signingResult[0].SignatureShares {
		_, err = client.ValidateSignatureShare(ctx, &pbfrost.ValidateSignatureShareRequest{
			Identifier:      identifier,
			Role:            pbfrost.SigningRole_STATECHAIN,
			Message:         msgHash[:],
			SignatureShare:  signature,
			PublicShare:     signingResult[0].PublicKeys[identifier],
			VerifyingKey:    verifyingKeyBytes,
			Commitments:     operatorCommitmentsProto,
			UserCommitments: userNonceCommitmentProto,
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	// User part
	_, err = client.ValidateSignatureShare(ctx, &pbfrost.ValidateSignatureShareRequest{
		Role:            pbfrost.SigningRole_USER,
		Message:         msgHash[:],
		SignatureShare:  userSignatures.Results[userJobID].SignatureShare,
		PublicShare:     userPubKeyBytes,
		VerifyingKey:    verifyingKeyBytes,
		Commitments:     operatorCommitmentsProto,
		UserCommitments: userNonceCommitmentProto,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Step 10: Signature aggregation - The aggregation is successful only if the signature is valid.
	signatureShares := signingResult[0].SignatureShares
	publicKeys := signingResult[0].PublicKeys
	signatureResult, err := client.AggregateFrost(ctx, &pbfrost.AggregateFrostRequest{
		Message:            msgHash[:],
		SignatureShares:    signatureShares,
		PublicShares:       publicKeys,
		VerifyingKey:       verifyingKeyBytes,
		Commitments:        operatorCommitmentsProto,
		UserCommitments:    userNonceCommitmentProto,
		UserPublicKey:      userPubKeyBytes,
		UserSignatureShare: userSignatures.Results[userJobID].SignatureShare,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Step 11: Verify signature using go lib.
	sig, err := schnorr.ParseSignature(signatureResult.Signature)
	if err != nil {
		t.Fatal(err)
	}

	pubKey, err := btcec.ParsePubKey(verifyingKeyBytes)
	if err != nil {
		t.Fatal(err)
	}
	taprootKey := txscript.ComputeTaprootKeyNoScript(pubKey)

	verified := sig.Verify(msgHash[:], taprootKey)
	if !verified {
		t.Fatal("signature verification failed")
	}
}
