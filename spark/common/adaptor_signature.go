package common

import (
	"fmt"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
)

// GenerateAdaptorFromSignature generates creates a hidden value and the adaptor signature for a given signature.s
func GenerateAdaptorFromSignature(signature []byte) ([]byte, []byte, error) {
	adaptorPrivateKey, err := btcec.NewPrivateKey()
	if err != nil {
		return nil, nil, err
	}

	sig, err := parseSignature(signature)
	if err != nil {
		return nil, nil, err
	}

	// Calculate sig.s - adaptorPrivateKey
	t := adaptorPrivateKey.Key
	t.Negate()
	newS := sig.s
	newS.Add(&t)

	newSig := newSignature(&sig.r, &newS)

	return newSig.serialize(), adaptorPrivateKey.Serialize(), nil
}

func GenerateSignatureFromExistingAdaptor(signature []byte, adaptorPrivateKeyBytes []byte) ([]byte, error) {
	adaptorPrivateKey, _ := btcec.PrivKeyFromBytes(adaptorPrivateKeyBytes)

	sig, err := parseSignature(signature)
	if err != nil {
		return nil, err
	}

	t := adaptorPrivateKey.Key
	t.Negate()
	newS := sig.s
	newS.Add(&t)

	newSig := newSignature(&sig.r, &newS)

	return newSig.serialize(), nil
}

// ValidateOutboundAdaptorSignature validates a adaptor signature from creator of the adaptor.
func ValidateOutboundAdaptorSignature(pubkey *btcec.PublicKey, hash []byte, signature []byte, adaptorPubkey []byte) error {
	sig, err := parseSignature(signature)
	if err != nil {
		return err
	}

	pubkeyBytes := schnorr.SerializePubKey(pubkey)

	return schnorrVerifyWithAdaptor(sig, hash, pubkeyBytes, adaptorPubkey, false)
}

// ApplyAdaptorToSignature applies an adaptor to a signature.
func ApplyAdaptorToSignature(pubkey *btcec.PublicKey, hash []byte, signature []byte, adaptorPrivateKeyBytes []byte) ([]byte, error) {
	sig, err := parseSignature(signature)
	if err != nil {
		return nil, err
	}

	adaptorPrivateKey, _ := btcec.PrivKeyFromBytes(adaptorPrivateKeyBytes)

	t := adaptorPrivateKey.Key
	newS := sig.s
	newS.Add(&t)

	newSig := schnorr.NewSignature(&sig.r, &newS)

	if newSig.Verify(hash, pubkey) {
		return newSig.Serialize(), nil
	}

	t.Negate()
	newS = sig.s
	newS.Add(&t)

	newSig = schnorr.NewSignature(&sig.r, &newS)
	if !newSig.Verify(hash, pubkey) {
		return nil, fmt.Errorf("cannot apply adaptor to signature")
	}

	return newSig.Serialize(), nil
}

// Signature is a type representing a Schnorr signature.
type signature struct {
	r btcec.FieldVal
	s btcec.ModNScalar
}

// NewSignature instantiates a new signature given some r and s values.
func newSignature(r *btcec.FieldVal, s *btcec.ModNScalar) *signature {
	var sig signature
	sig.r.Set(r).Normalize()
	sig.s.Set(s)
	return &sig
}

// Serialize returns the Schnorr signature in the more strict format.
//
// The signatures are encoded as
//
//	sig[0:32]  x coordinate of the point R, encoded as a big-endian uint256
//	sig[32:64] s, encoded also as big-endian uint256
func (sig signature) serialize() []byte {
	// Total length of returned signature is the length of r and s.
	var b [64]byte
	sig.r.PutBytesUnchecked(b[0:32])
	sig.s.PutBytesUnchecked(b[32:64])
	return b[:]
}

// ParseSignature parses a signature according to the BIP-340 specification and
// enforces the following additional restrictions specific to secp256k1:
//
// - The r component must be in the valid range for secp256k1 field elements
// - The s component must be in the valid range for secp256k1 scalars
func parseSignature(sig []byte) (*signature, error) {
	// The signature must be the correct length.
	sigLen := len(sig)
	if sigLen < 64 {
		return nil, fmt.Errorf("malformed signature: too short: %d < %d", sigLen,
			64)
	}
	if sigLen > 64 {
		return nil, fmt.Errorf("malformed signature: too long: %d > %d", sigLen,
			64)
	}

	// The signature is validly encoded at this point, however, enforce
	// additional restrictions to ensure r is in the range [0, p-1], and s is in
	// the range [0, n-1] since valid Schnorr signatures are required to be in
	// that range per spec.
	var r btcec.FieldVal
	if overflow := r.SetByteSlice(sig[0:32]); overflow {
		return nil, fmt.Errorf("invalid signature: r >= field prime")
	}
	var s btcec.ModNScalar
	if overflow := s.SetByteSlice(sig[32:64]); overflow {
		return nil, fmt.Errorf("invalid signature: s >= group order")
	}

	// Return the signature.
	return newSignature(&r, &s), nil
}

// This is copied and modified with adaptor from the schnorrVerify function in the btcd library.
func schnorrVerifyWithAdaptor(sig *signature, hash []byte, pubKeyBytes []byte, adaptorPubkey []byte, inbound bool) error {
	// The algorithm for producing a BIP-340 signature is described in
	// README.md and is reproduced here for reference:
	//
	// 1. Fail if m is not 32 bytes
	// 2. P = lift_x(int(pk)).
	// 3. r = int(sig[0:32]); fail is r >= p.
	// 4. s = int(sig[32:64]); fail if s >= n.
	// 5. e = int(tagged_hash("BIP0340/challenge", bytes(r) || bytes(P) || M)) mod n.
	// 6. R = s*G - e*P
	// 7. Fail if is_infinite(R)
	// 8. Fail if not hash_even_y(R)
	// 9. Fail is x(R) != r.
	// 10. Return success iff not failure occured before reachign this
	// point.

	// Step 1.
	//
	// Fail if m is not 32 bytes
	if len(hash) != 32 {
		return fmt.Errorf("wrong size for message (got %v, want %v)",
			len(hash), 32)
	}

	// Step 2.
	//
	// P = lift_x(int(pk))
	//
	// Fail if P is not a point on the curve
	pubKey, err := schnorr.ParsePubKey(pubKeyBytes)
	if err != nil {
		return err
	}
	if !pubKey.IsOnCurve() {
		return fmt.Errorf("pubkey point is not on curve")
	}

	// Step 3.
	//
	// Fail if r >= p
	//
	// Note this is already handled by the fact r is a field element.

	// Step 4.
	//
	// Fail if s >= n
	//
	// Note this is already handled by the fact s is a mod n scalar.

	// Step 5.
	//
	// e = int(tagged_hash("BIP0340/challenge", bytes(r) || bytes(P) || M)) mod n.
	var rBytes [32]byte
	sig.r.PutBytesUnchecked(rBytes[:])
	pBytes := schnorr.SerializePubKey(pubKey)

	commitment := chainhash.TaggedHash(
		chainhash.TagBIP0340Challenge, rBytes[:], pBytes, hash,
	)

	var e btcec.ModNScalar
	if overflow := e.SetBytes((*[32]byte)(commitment)); overflow != 0 {
		return fmt.Errorf("hash of (r || P || m) too big")
	}

	// Negate e here so we can use AddNonConst below to subtract the s*G
	// point from e*P.
	e.Negate()

	// Step 6.
	//
	// R = s*G - e*P
	var P, R, sG, eP btcec.JacobianPoint
	pubKey.AsJacobian(&P)
	btcec.ScalarBaseMultNonConst(&sig.s, &sG)
	btcec.ScalarMultNonConst(&e, &P, &eP)
	btcec.AddNonConst(&sG, &eP, &R)

	// Step 6.5
	//
	// Add adaptorPubkey to R
	adaptorPub, err := btcec.ParsePubKey(adaptorPubkey)
	if err != nil {
		return err
	}
	var adaptorPubJacobian btcec.JacobianPoint
	adaptorPub.AsJacobian(&adaptorPubJacobian)
	var newR btcec.JacobianPoint
	btcec.AddNonConst(&R, &adaptorPubJacobian, &newR)

	// Step 7.
	//
	// Fail if R is the point at infinity
	if !inbound {
		if (newR.X.IsZero() && newR.Y.IsZero()) || newR.Z.IsZero() {
			return fmt.Errorf("calculated R point is the point at infinity")
		}
	}

	// Step 8.
	//
	// Fail if R.y is odd
	//
	// Note that R must be in affine coordinates for this check.
	newR.ToAffine()
	if newR.Y.IsOdd() {
		return fmt.Errorf("calculated R y-value is odd")
	}

	// Step 9.
	//
	// Verified if R.x == r
	//
	// Note that R must be in affine coordinates for this check.
	if !sig.r.Equals(&newR.X) {
		return fmt.Errorf("calculated R point was not given R")
	}

	// Step 10.
	//
	// Return success iff not failure occured before reachign this
	return nil
}
