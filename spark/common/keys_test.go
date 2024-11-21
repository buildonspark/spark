package common

import (
	"bytes"
	"crypto/rand"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1"
)

func TestKeyAdditions(t *testing.T) {
	privABytes, _, _, err := secp256k1.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, pubA := secp256k1.PrivKeyFromBytes(privABytes)

	privBBytes, _, _, err := secp256k1.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, pubB := secp256k1.PrivKeyFromBytes(privBBytes)

	// Testing the public key of private key addition equals the public key addition
	privSum, err := AddPrivateKeys(privABytes, privBBytes)
	if err != nil {
		t.Fatal(err)
	}
	pubSum, err := AddPublicKeys(pubA.SerializeCompressed(), pubB.SerializeCompressed())
	if err != nil {
		t.Fatal(err)
	}

	_, target := secp256k1.PrivKeyFromBytes(privSum)
	if !bytes.Equal(target.SerializeCompressed(), pubSum) {
		t.Fatal("public key of private key addition does not equal the public key addition")
	}
}
