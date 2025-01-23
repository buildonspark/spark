package common

import (
	"bytes"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
)

func TestKeyAdditions(t *testing.T) {
	privA, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	pubA := privA.PubKey()

	privB, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	pubB := privB.PubKey()

	// Testing the public key of private key addition equals the public key addition
	privSum := AddPrivateKeysRaw(privA, privB)
	if err != nil {
		t.Fatal(err)
	}
	pubSum, err := AddPublicKeys(pubA.SerializeCompressed(), pubB.SerializeCompressed())
	if err != nil {
		t.Fatal(err)
	}

	target := privSum.PubKey()
	if !bytes.Equal(target.SerializeCompressed(), pubSum) {
		t.Fatal("public key of private key addition does not equal the public key addition")
	}
}

func TestSumOfPrivateKeys(t *testing.T) {
	keys := make([][]byte, 10)
	for i := 0; i < 10; i++ {
		key, err := secp256k1.GeneratePrivateKey()
		if err != nil {
			t.Fatal(err)
		}
		keys[i] = key.Serialize()
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
	target, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}

	keys := make([][]byte, 10)
	for i := 0; i < 10; i++ {
		privKey, err := secp256k1.GeneratePrivateKey()
		if err != nil {
			t.Fatal(err)
		}
		keys[i] = privKey.Serialize()
	}

	tweak, err := LastKeyWithTarget(target.Serialize(), keys)
	if err != nil {
		t.Fatal(err)
	}

	keys = append(keys, tweak)

	sum, err := SumOfPrivateKeys(keys)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(sum.Bytes(), target.Serialize()) {
		t.Fatal("private key tweak with target does not match")
	}
}

func TestApplyAdditiveTweakToPublicKey(t *testing.T) {
	privKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	pubKey := privKey.PubKey()

	tweak, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}

	newPriv, err := AddPrivateKeys(privKey.Serialize(), tweak.Serialize())
	if err != nil {
		t.Fatal(err)
	}
	target := secp256k1.PrivKeyFromBytes(newPriv).PubKey()

	newPubKey, err := ApplyAdditiveTweakToPublicKey(pubKey.SerializeCompressed(), tweak.Serialize())
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(newPubKey, target.SerializeCompressed()) {
		t.Fatal("apply additive tweak to public key does not match")
	}
}
