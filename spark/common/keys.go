package common

import (
	"fmt"
	"math/big"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
)

// PublicKeyFromInts creates a secp256k1 public key from x and y big integers.
func PublicKeyFromInts(x, y *big.Int) *secp256k1.PublicKey {
	xFieldVal := secp256k1.FieldVal{}
	xFieldVal.SetByteSlice(x.Bytes())
	yFieldVal := secp256k1.FieldVal{}
	yFieldVal.SetByteSlice(y.Bytes())

	// Normalize both values, because the cost is low, and having non-normalized values will break everything.
	// See the comments on FieldVal for more information.
	xFieldVal.Normalize()
	yFieldVal.Normalize()
	return secp256k1.NewPublicKey(&xFieldVal, &yFieldVal)
}

// AddPublicKeysRaw adds two secp256k1 public keys using group addition.
// Deprecated: Use [github.com/lightsparkdev/spark/common/keys.Public.Add]
func AddPublicKeysRaw(a, b *secp256k1.PublicKey) *secp256k1.PublicKey {
	curve := secp256k1.S256()
	sumX, sumY := curve.Add(a.X(), a.Y(), b.X(), b.Y())
	return PublicKeyFromInts(sumX, sumY)
}

// AddPublicKeys adds two secp256k1 public keys using group addition.
// The input public keys must be 33 bytes.
// The result is a 33 byte compressed secp256k1 public key.
// Deprecated: Use [github.com/lightsparkdev/spark/common/keys.Public.Add]
func AddPublicKeys(a, b []byte) ([]byte, error) {
	if len(a) != 33 || len(b) != 33 {
		return nil, fmt.Errorf("pubkeys must be 33 bytes")
	}

	pubkeyA, err := secp256k1.ParsePubKey(a)
	if err != nil {
		return nil, err
	}
	pubkeyB, err := secp256k1.ParsePubKey(b)
	if err != nil {
		return nil, err
	}

	sum := AddPublicKeysRaw(pubkeyA, pubkeyB)

	return sum.SerializeCompressed(), nil
}

// AddPublicKeysList adds a list of secp256k1 public keys using group addition.
// The input public keys must be 33 bytes.
// The result is a 33 byte compressed secp256k1 public key.
// Deprecated: Use [github.com/lightsparkdev/spark/common/keys.SumPublicKeys]
func AddPublicKeysList(keys [][]byte) ([]byte, error) {
	if len(keys) == 0 {
		return nil, fmt.Errorf("no keys to add")
	}

	if len(keys) == 1 {
		return keys[0], nil
	}

	sum, err := AddPublicKeys(keys[0], keys[1])
	if err != nil {
		return nil, err
	}

	for _, key := range keys[2:] {
		sum, err = AddPublicKeys(sum, key)
		if err != nil {
			return nil, err
		}
	}

	return sum, nil
}

// ApplyAdditiveTweakToPublicKey applies a tweak to a public key.
// The result key is pubkey + tweak * G.
// Deprecated: Use [github.com/lightsparkdev/spark/common/keys.Public.AddTweak]
func ApplyAdditiveTweakToPublicKey(pubkey []byte, tweak []byte) ([]byte, error) {
	if len(pubkey) != 33 {
		return nil, fmt.Errorf("pubkey must be 33 bytes")
	}
	if len(tweak) != 32 {
		return nil, fmt.Errorf("tweak must be 32 bytes")
	}

	pub, err := secp256k1.ParsePubKey(pubkey)
	if err != nil {
		return nil, err
	}

	tweakPub := secp256k1.PrivKeyFromBytes(tweak).PubKey()

	pub = AddPublicKeysRaw(pub, tweakPub)

	return pub.SerializeCompressed(), nil
}

// SubtractPublicKeys subtracts two secp256k1 public keys using group subtraction.
// The input public keys must be 33 bytes.
// The result is a 33 byte compressed secp256k1 public key.
// Deprecated: Use [github.com/lightsparkdev/spark/common/keys.Public.Sub]
func SubtractPublicKeys(a, b []byte) ([]byte, error) {
	if len(a) != 33 || len(b) != 33 {
		return nil, fmt.Errorf("pubkeys must be 33 bytes")
	}

	pubkeyA, err := secp256k1.ParsePubKey(a)
	if err != nil {
		return nil, err
	}
	pubkeyB, err := secp256k1.ParsePubKey(b)
	if err != nil {
		return nil, err
	}

	negBY := new(big.Int).Sub(secp256k1.S256().P, pubkeyB.Y())
	pubkeyB = PublicKeyFromInts(pubkeyB.X(), negBY)

	pubkeyA = AddPublicKeysRaw(pubkeyA, pubkeyB)
	return pubkeyA.SerializeCompressed(), nil
}

// PrivateKeyFromBytes creates a secp256k1 private key from a byte slice. The bytes slice must be
// 32 bytes.
// Deprecated: Use [github.com/lightsparkdev/spark/common/keys.ParsePrivateKey]
func PrivateKeyFromBytes(privKeyBytes []byte) (*secp256k1.PrivateKey, error) {
	if len(privKeyBytes) != 32 {
		return nil, fmt.Errorf("private key must be 32 bytes")
	}

	return secp256k1.PrivKeyFromBytes(privKeyBytes), nil
}

// PrivateKeyFromBigInt creates a secp256k1 private key from a big integer.
func PrivateKeyFromBigInt(privKeyInt *big.Int) (*secp256k1.PrivateKey, error) {
	if privKeyInt.BitLen() > 256 {
		return nil, fmt.Errorf("private key cannot be represented by an Int larger than 32 bytes")
	}

	bytes := make([]byte, 32)
	privKeyInt.FillBytes(bytes)
	return secp256k1.PrivKeyFromBytes(bytes), nil
}

// AddPrivateKeysRaw adds two secp256k1 private keys using field addition.
// Deprecated: Use [github.com/lightsparkdev/spark/common/keys.Private.Add]
func AddPrivateKeysRaw(a, b *secp256k1.PrivateKey) *secp256k1.PrivateKey {
	curve := secp256k1.S256()
	aInt := new(big.Int).SetBytes(a.Serialize())
	bInt := new(big.Int).SetBytes(b.Serialize())
	sum := new(big.Int).Add(aInt, bInt)
	sum.Mod(sum, curve.N)
	return secp256k1.PrivKeyFromBytes(sum.Bytes())
}

// AddPrivateKeys adds two secp256k1 private keys using field addition.
// The input private keys must be 32 bytes.
// The result is a 32 byte private key.
// Deprecated: Use [github.com/lightsparkdev/spark/common/keys.Private.Add]
func AddPrivateKeys(a, b []byte) ([]byte, error) {
	if len(a) != 32 || len(b) != 32 {
		return nil, fmt.Errorf("private keys must be 32 bytes")
	}

	privA := secp256k1.PrivKeyFromBytes(a)
	privB := secp256k1.PrivKeyFromBytes(b)

	privKey := AddPrivateKeysRaw(privA, privB)

	return privKey.Serialize(), nil
}

// SubtractPrivateKeys subtracts two secp256k1 private keys using field subtraction.
// The input private keys must be 32 bytes.
// The result is a 32 byte private key.
// Deprecated: Use [github.com/lightsparkdev/spark/common/keys.Private.Sub]
func SubtractPrivateKeys(a, b []byte) ([]byte, error) {
	if len(a) != 32 || len(b) != 32 {
		return nil, fmt.Errorf("private keys must be 32 bytes")
	}

	privA := secp256k1.PrivKeyFromBytes(a)
	privB := secp256k1.PrivKeyFromBytes(b)

	N := secp256k1.S256().N

	privAInt := new(big.Int).SetBytes(privA.Serialize())
	privBInt := new(big.Int).SetBytes(privB.Serialize())
	negB := new(big.Int).Sub(N, privBInt)
	sum := new(big.Int).Add(privAInt, negB)
	sum.Mod(sum, N)

	privKey := secp256k1.PrivKeyFromBytes(sum.Bytes())

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
		priv := secp256k1.PrivKeyFromBytes(key)
		privInt := new(big.Int).SetBytes(priv.Serialize())
		sum.Add(sum, privInt)
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

	privKey := secp256k1.PrivKeyFromBytes(diff.Bytes())
	return privKey.Serialize(), nil
}
