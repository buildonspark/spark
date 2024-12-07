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

// ApplyAdditiveTweakToPublicKey applies a tweak to a public key.
// The result key is pubkey + tweak * G.
func ApplyAdditiveTweakToPublicKey(pubkey []byte, tweak []byte) ([]byte, error) {
	if len(pubkey) != 33 {
		return nil, fmt.Errorf("pubkey must be 33 bytes")
	}
	if len(tweak) != 32 {
		return nil, fmt.Errorf("tweak must be 32 bytes")
	}

	curve := secp256k1.S256()
	pub, err := secp256k1.ParsePubKey(pubkey)
	if err != nil {
		return nil, err
	}

	_, tweakPub := secp256k1.PrivKeyFromBytes(tweak)

	pub.X, pub.Y = curve.Add(pub.X, pub.Y, tweakPub.X, tweakPub.Y)

	return pub.SerializeCompressed(), nil
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

// SumOfPrivateKeys returns the sum of the given private keys modulo the order of the secp256k1 curve.
func SumOfPrivateKeys(keys [][]byte) (*big.Int, error) {
	sum := new(big.Int)
	N := secp256k1.S256().N
	for _, key := range keys {
		if len(key) != 32 {
			return nil, fmt.Errorf("private keys must be 32 bytes")
		}
		priv, _ := secp256k1.PrivKeyFromBytes(key)
		sum.Add(sum, priv.D)
		sum.Mod(sum, N)
	}
	return sum, nil
}

// LastKeyWithTarget tweaks the given keys so that the sum of the keys equals the target.
// This will return target - sum(keys).
func LastKeyWithTarget(target []byte, keys [][]byte) ([]byte, error) {
	if len(target) != 32 {
		return nil, fmt.Errorf("target must be 32 bytes")
	}
	targetInt := new(big.Int).SetBytes(target)
	sum, err := SumOfPrivateKeys(keys)
	if err != nil {
		return nil, err
	}
	diff := new(big.Int).Sub(targetInt, sum)
	diff.Mod(diff, secp256k1.S256().N)

	privKey, _ := secp256k1.PrivKeyFromBytes(diff.Bytes())
	return privKey.Serialize(), nil
}
