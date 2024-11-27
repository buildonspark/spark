package grpctest

import (
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1"
	"github.com/lightsparkdev/spark-go/common"
	pb "github.com/lightsparkdev/spark-go/proto"
	"github.com/lightsparkdev/spark-go/so/ent_utils"
	"github.com/lightsparkdev/spark-go/so/helper"
	"github.com/lightsparkdev/spark-go/so/objects"
	"github.com/lightsparkdev/spark-go/test_util"
)

// TestFrostSign tests the frost signing process.
// It mimics both the user and signing coordinator side of the frost signing process.
// Since the FROST signer is a stateless signer except for DKG, it is reused for both the user and the operator.
func TestFrostSign(t *testing.T) {
	// Step 1: Setup config
	config, err := test_util.TestConfig()
	if err != nil {
		t.Fatal(err)
	}

	ctx, err := test_util.TestContext(config)
	if err != nil {
		t.Fatal(err)
	}

	msg := []byte("hello")

	// Step 2: Get operator key share
	operatorKeyShares, err := ent_utils.GetUnusedSigningKeyshares(ctx, config, 1)
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
	userKeyPackage := pb.KeyPackage{
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
	signingResult, err := helper.SignFrost(ctx, config, operatorKeyShare.ID, msg, verifyingKeyBytes, *userNonceCommitment)
	if err != nil {
		t.Fatal(err)
	}
	operatorCommitments := signingResult.SigningCommitments
	operatorCommitmentsProto := make(map[string]*pb.SigningCommitment)
	for id, commitment := range operatorCommitments {
		commitmentProto, err := commitment.MarshalProto()
		if err != nil {
			t.Fatal(err)
		}
		operatorCommitmentsProto[id] = commitmentProto
	}

	// Step 7: User signing
	conn, err := common.NewGRPCConnection(config.SignerAddress)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := pb.NewFrostServiceClient(conn)
	userSignatures, err := client.SignFrost(ctx, &pb.SignFrostRequest{
		Message:         msg,
		KeyPackage:      &userKeyPackage,
		VerifyingKey:    verifyingKeyBytes,
		Nonce:           userNonceProto,
		Commitments:     operatorCommitmentsProto,
		UserCommitments: userNonceCommitmentProto,
		Role:            pb.SigningRole_USER,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Step 8: Signature aggregation - The aggregation is successful only if the signature is valid.
	signatureShares := signingResult.SignatureShares
	signatureShares[userIdentifier] = userSignatures.SignatureShare
	_, err = client.AggregateFrost(ctx, &pb.AggregateFrostRequest{
		Message:         msg,
		SignatureShares: signatureShares,
		VerifyingKey:    verifyingKeyBytes,
		Commitments:     operatorCommitmentsProto,
		UserCommitments: userNonceCommitmentProto,
	})
	if err != nil {
		t.Fatal(err)
	}
}
