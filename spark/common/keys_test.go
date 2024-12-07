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

func TestSumOfPrivateKeys(t *testing.T) {
	keys := make([][]byte, 10)
	for i := 0; i < 10; i++ {
		key, _, _, err := secp256k1.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		keys[i] = key
	}
	sum, err := SumOfPrivateKeys(keys)
	if err != nil {
		t.Fatal(err)
	}
	sumBytes := sum.Bytes()

	sum2 := keys[0]
	for i := 1; i < len(keys); i++ {
		sum2, _ = AddPrivateKeys(sum2, keys[i])
	}

	if !bytes.Equal(sumBytes, sum2) {
		t.Fatal("sum of private keys does not match")
	}
}

func TestPrivateKeyTweakWithTarget(t *testing.T) {
	target, _, _, err := secp256k1.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	keys := make([][]byte, 10)
	for i := 0; i < 10; i++ {
		key, _, _, err := secp256k1.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		privKey, _ := secp256k1.PrivKeyFromBytes(key)
		keys[i] = privKey.Serialize()
	}

	tweak, err := LastKeyWithTarget(target, keys)
	if err != nil {
		t.Fatal(err)
	}

	keys = append(keys, tweak)

	sum, err := SumOfPrivateKeys(keys)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(sum.Bytes(), target) {
		t.Fatal("private key tweak with target does not match")
	}
}

func TestApplyAdditiveTweakToPublicKey(t *testing.T) {
	privKey, _, _, err := secp256k1.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, pubKey := secp256k1.PrivKeyFromBytes(privKey)

	tweak, _, _, err := secp256k1.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	newPriv, err := AddPrivateKeys(privKey, tweak)
	if err != nil {
		t.Fatal(err)
	}
	_, target := secp256k1.PrivKeyFromBytes(newPriv)

	newPubKey, err := ApplyAdditiveTweakToPublicKey(pubKey.SerializeCompressed(), tweak)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(newPubKey, target.SerializeCompressed()) {
		t.Fatal("apply additive tweak to public key does not match")
	}
}
