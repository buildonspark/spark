package secretsharing_test

import (
	"crypto/rand"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	secretsharing "github.com/lightsparkdev/spark/common/secret_sharing"
)

func TestSecretSharing(t *testing.T) {
	fieldModulus := secp256k1.S256().N
	secret, err := rand.Int(rand.Reader, fieldModulus)
	if err != nil {
		t.Fatal(err)
	}
	threshold := 3
	numberOfShares := 5

	shares, err := secretsharing.SplitSecretWithProofs(secret, fieldModulus, threshold, numberOfShares)
	if err != nil {
		t.Fatal(err)
	}

	for _, share := range shares {
		err := secretsharing.ValidateShare(share)
		if err != nil {
			t.Fatal(err)
		}
	}

	recoveredSecret, err := secretsharing.RecoverSecret(shares[:threshold])
	if err != nil {
		t.Fatal(err)
	}

	if secret.Cmp(recoveredSecret) != 0 {
		t.Fatalf("secret %s does not match recovered secret %s", secret.String(), recoveredSecret.String())
	}
}
