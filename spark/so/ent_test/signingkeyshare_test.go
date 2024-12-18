package ent_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"math/big"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go/common"
	secretsharing "github.com/lightsparkdev/spark-go/common/secret_sharing"
	"github.com/lightsparkdev/spark-go/so/ent"
	"github.com/lightsparkdev/spark-go/so/ent/enttest"
	"github.com/lightsparkdev/spark-go/so/ent/schema"
	testutil "github.com/lightsparkdev/spark-go/test_util"
	_ "github.com/mattn/go-sqlite3"
)

func TestSigningKeyshare(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&_fk=1")
	defer client.Close()

	ctx := context.Background()

	secretShare := make([]byte, 32)
	rand.Read(secretShare)

	publicShares := make(map[string][]uint8)
	for i := 0; i < 3; i++ {
		identifier := make([]byte, 16)
		rand.Read(identifier)
		publicShare := make([]byte, 32)
		rand.Read(publicShare)
		publicShares[hex.EncodeToString(identifier)] = publicShare
	}

	publicKey := make([]byte, 64)
	rand.Read(publicKey)

	keyshare := client.SigningKeyshare.
		Create().
		SetID(uuid.UUID{}).
		SetStatus(schema.KeyshareStatusAvailable).
		SetMinSigners(5).
		SetSecretShare(secretShare).
		SetPublicShares(publicShares).
		SetPublicKey(publicKey).
		SaveX(ctx)
	if !bytes.Equal(keyshare.SecretShare, secretShare) {
		t.Errorf("secret share not equal")
	}
	if !bytes.Equal(keyshare.PublicKey, publicKey) {
		t.Errorf("public key not equal")
	}
	if len(keyshare.PublicShares) != len(publicShares) {
		t.Errorf("public shares not equal")
	}
	for identifier, expectedPublicShare := range publicShares {
		publicShare, ok := keyshare.PublicShares[identifier]
		if !ok || !bytes.Equal(publicShare, expectedPublicShare) {
			t.Errorf("public shares not equal")
		}
	}
}

func TestKeyTweak(t *testing.T) {
	config, _ := testutil.TestConfig()
	ctx, _ := testutil.TestContext(config)

	operatorKeyShares, _ := ent.GetUnusedSigningKeyshares(ctx, config, 1)
	operatorKeyShare := operatorKeyShares[0]

	oldPrivKey, _ := secp256k1.GeneratePrivateKey()
	oldVerifyingKey, _ := common.AddPublicKeys(operatorKeyShare.PublicKey, oldPrivKey.PubKey().SerializeCompressed())

	newPrivKey, _ := secp256k1.GeneratePrivateKey()
	privKeyTweak, _ := common.SubtractPrivateKeys(oldPrivKey.Serialize(), newPrivKey.Serialize())

	signingOperators, _ := testutil.GetAllSigningOperators()
	tweakShares, _ := secretsharing.SplitSecretWithProofs(
		new(big.Int).SetBytes(privKeyTweak),
		secp256k1.S256().N,
		int(config.Threshold),
		len(signingOperators),
	)

	pubkeySharesTweak := make(map[string][]byte)
	for identifier, operator := range signingOperators {
		share := findShare(tweakShares, operator.ID)
		pubkeyTweak := secp256k1.NewPrivateKey(share.Share).PubKey()
		pubkeySharesTweak[identifier] = pubkeyTweak.SerializeCompressed()
	}

	operatorTweak := findShare(tweakShares, config.Index)
	operatorKeyShare, _ = operatorKeyShare.TweakKeyShare(
		ctx,
		operatorTweak.SecretShare.Share.Bytes(),
		operatorTweak.Proofs[0],
		pubkeySharesTweak,
	)
	newVerifyingKey, _ := common.AddPublicKeys(operatorKeyShare.PublicKey, newPrivKey.PubKey().SerializeCompressed())
	if !bytes.Equal(oldVerifyingKey, newVerifyingKey) {
		t.Fatalf("verifying key mismatch")
	}
}

func findShare(shares []*secretsharing.VerifiableSecretShare, operatorID uint64) *secretsharing.VerifiableSecretShare {
	targetShareIndex := big.NewInt(int64(operatorID + 1))
	for _, s := range shares {
		if s.SecretShare.Index.Cmp(targetShareIndex) == 0 {
			return s
		}
	}
	return nil
}
