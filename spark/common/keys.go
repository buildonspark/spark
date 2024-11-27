package common

import (
	"fmt"
	"math/big"

	"github.com/decred/dcrd/dcrec/secp256k1/v2"
)

// AddPublicKeys adds two secp256k1 public keys using group addition.
// The input public keys must be 33 bytes.
// The result is a 33 byte compressed secp256k1 public key.
func AddPublicKeys(a, b []byte) ([]byte, error) {
	if len(a) != 33 || len(b) != 33 {
		return nil, fmt.Errorf("pubkeys must be 33 bytes")
	}

	curve := secp256k1.S256()
	pubkeyA, err := secp256k1.ParsePubKey(a)
	if err != nil {
		return nil, err
	}
	pubkeyB, err := secp256k1.ParsePubKey(b)
	if err != nil {
		return nil, err
	}

	sum := new(secp256k1.PublicKey)
	sum.X, sum.Y = curve.Add(pubkeyA.X, pubkeyA.Y, pubkeyB.X, pubkeyB.Y)

	return sum.SerializeCompressed(), nil
}

// AddPrivateKeys adds two secp256k1 private keys using field addition.
// The input private keys must be 32 bytes.
// The result is a 32 byte private key.
func AddPrivateKeys(a, b []byte) ([]byte, error) {
	if len(a) != 32 || len(b) != 32 {
		return nil, fmt.Errorf("private keys must be 32 bytes")
	}

	privA, _ := secp256k1.PrivKeyFromBytes(a)
	privB, _ := secp256k1.PrivKeyFromBytes(b)

	N := secp256k1.S256().N

	sum := new(big.Int).Add(privA.D, privB.D)
	sum.Mod(sum, N)

	privKey, _ := secp256k1.PrivKeyFromBytes(sum.Bytes())

	return privKey.Serialize(), nil
}
