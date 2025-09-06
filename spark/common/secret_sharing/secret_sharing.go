package secretsharing

import (
	"crypto/rand"
	"fmt"
	"math/big"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/lightsparkdev/spark/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
)

// Polynomial is a polynomial used for secret sharing.
type Polynomial struct {
	// FieldModulus is the field modulus of the polynomial.
	FieldModulus *big.Int
	// coefficients are the coefficients of the polynomial.
	coefficients []*big.Int
	// Proofs are the proofs of the polynomial.
	Proofs [][]byte
}

// LagrangeInterpolatable is an interface that can be used to interpolate a secret from a set of shares.
type LagrangeInterpolatable interface {
	// GetIndex returns the index of the share.
	GetIndex() *big.Int
	// GetShare returns the share of the secret.
	GetShare() *big.Int
	// GetFieldModulus returns the field modulus of the share.
	GetFieldModulus() *big.Int
	// GetThreshold returns the threshold of the secret.
	GetThreshold() int
}

// SecretShare is a share of a secret.
type SecretShare struct {
	// FieldModulus is the field modulus of the share.
	FieldModulus *big.Int
	// Threshold is the threshold of the secret.
	Threshold int
	// Index is the index of the share.
	Index *big.Int
	// Share is the share of the secret.
	Share *big.Int
}

// GetIndex returns the index of the share.
func (s *SecretShare) GetIndex() *big.Int {
	return s.Index
}

// GetFieldModulus returns the field modulus of the share.
func (s *SecretShare) GetFieldModulus() *big.Int {
	return s.FieldModulus
}

// GetShare returns the share of the secret.
func (s *SecretShare) GetShare() *big.Int {
	return s.Share
}

// IntToScalar converts a big.Int to a secp256k1 ModNScalar.
func IntToScalar(value *big.Int) *secp256k1.ModNScalar {
	var scalar secp256k1.ModNScalar
	scalar.SetByteSlice(value.Bytes())
	return &scalar
}

// GetThreshold returns the threshold of the secret.
func (s *SecretShare) GetThreshold() int {
	return s.Threshold
}

// VerifiableSecretShare is a share of a secret with proofs.
type VerifiableSecretShare struct {
	SecretShare
	Proofs [][]byte
}

// MarshalProto marshals the VerifiableSecretShare to a protobuf message.
func (v *VerifiableSecretShare) MarshalProto() *pb.SecretShare {
	secretShare := make([]byte, 32)
	v.Share.FillBytes(secretShare)

	return &pb.SecretShare{
		SecretShare: secretShare,
		Proofs:      v.Proofs,
	}
}

// Evaluate evaluates the polynomial at a given point.
func (p *Polynomial) Evaluate(x *big.Int) *big.Int {
	result := big.NewInt(0)
	for i, coeff := range p.coefficients {
		xPow := new(big.Int).Exp(x, big.NewInt(int64(i)), p.FieldModulus)
		xPow.Mul(xPow, coeff)
		result.Add(result, xPow)
		result.Mod(result, p.FieldModulus)
	}
	return result
}

// fieldDiv divides two numbers in a given field modulus.
func fieldDiv(numerator *big.Int, denominator *big.Int, fieldModulus *big.Int) (*big.Int, error) {
	if denominator.Sign() == 0 {
		return nil, fmt.Errorf("division by zero")
	}

	inverse := new(big.Int).ModInverse(denominator, fieldModulus)
	inverse.Mul(inverse, numerator)
	return inverse.Mod(inverse, fieldModulus), nil
}

// ComputeLagrangeCoefficients computes the Lagrange coefficient for a given index and a set of points.
func ComputeLagrangeCoefficients[T LagrangeInterpolatable](index *big.Int, points []T) (*big.Int, error) {
	numerator := big.NewInt(1)
	denominator := big.NewInt(1)
	fieldModulus := points[0].GetFieldModulus()
	for _, point := range points {
		if point.GetIndex().Cmp(index) == 0 {
			continue
		}
		numerator.Mul(numerator, point.GetIndex())
		value := new(big.Int).Sub(point.GetIndex(), index)
		denominator.Mul(denominator, value)
	}

	return fieldDiv(numerator, denominator, fieldModulus)
}

// generatePolynomialForSecretSharing generates a polynomial for secret sharing.
func generatePolynomialForSecretSharing(fieldModulus *big.Int, secret *big.Int, degree int) (*Polynomial, error) {
	coefficients := make([]*big.Int, degree)
	proofs := make([][]byte, degree)

	coefficients[0] = secret
	var secretScalar secp256k1.ModNScalar
	secretScalar.SetByteSlice(secret.Bytes())
	proofs[0] = secp256k1.NewPrivateKey(&secretScalar).PubKey().SerializeCompressed()
	for i := 1; i < degree; i++ {
		randomInt, err := rand.Int(rand.Reader, fieldModulus)
		if err != nil {
			return nil, err
		}
		coefficients[i] = randomInt
		var coefScalar secp256k1.ModNScalar
		coefScalar.SetByteSlice(randomInt.Bytes())
		proofs[i] = secp256k1.NewPrivateKey(&coefScalar).PubKey().SerializeCompressed()
	}
	return &Polynomial{
		FieldModulus: fieldModulus,
		coefficients: coefficients,
		Proofs:       proofs,
	}, nil
}

// SplitSecret splits a secret into a set of shares.
func SplitSecret(secret *big.Int, fieldModulus *big.Int, threshold int, numberOfShares int) ([]*SecretShare, error) {
	polynomial, err := generatePolynomialForSecretSharing(fieldModulus, secret, threshold)
	if err != nil {
		return nil, err
	}

	shares := make([]*SecretShare, 0)
	for i := 1; i <= numberOfShares; i++ {
		share := polynomial.Evaluate(big.NewInt(int64(i)))
		shares = append(shares, &SecretShare{
			FieldModulus: fieldModulus,
			Threshold:    threshold,
			Index:        big.NewInt(int64(i)),
			Share:        share,
		})
	}
	return shares, nil
}

// SplitSecretWithProofs splits a secret into a set of shares with proofs.
func SplitSecretWithProofs(secret *big.Int, fieldModulus *big.Int, threshold int, numberOfShares int) ([]*VerifiableSecretShare, error) {
	polynomial, err := generatePolynomialForSecretSharing(fieldModulus, secret, threshold)
	if err != nil {
		return nil, err
	}

	shares := make([]*VerifiableSecretShare, 0)
	for i := 1; i <= numberOfShares; i++ {
		share := polynomial.Evaluate(big.NewInt(int64(i)))
		shares = append(shares, &VerifiableSecretShare{
			SecretShare: SecretShare{
				FieldModulus: fieldModulus,
				Threshold:    threshold,
				Index:        big.NewInt(int64(i)),
				Share:        share,
			},
			Proofs: polynomial.Proofs,
		})
	}
	return shares, nil
}

// RecoverSecret recovers a secret from a set of shares.
func RecoverSecret[T LagrangeInterpolatable](shares []T) (*big.Int, error) {
	if len(shares) < shares[0].GetThreshold() {
		return nil, fmt.Errorf("not enough shares to recover secret")
	}

	result := big.NewInt(0)
	for _, share := range shares {
		coeff, err := ComputeLagrangeCoefficients(share.GetIndex(), shares)
		if err != nil {
			return nil, err
		}
		item := new(big.Int).Mul(share.GetShare(), coeff)
		item.Mod(item, shares[0].GetFieldModulus())
		result.Add(result, item)
		result.Mod(result, shares[0].GetFieldModulus())
	}

	return result, nil
}

// ValidateShare validates a share of a secret.
func ValidateShare(share *VerifiableSecretShare) error {
	targetPubkey := secp256k1.NewPrivateKey(IntToScalar(share.Share)).PubKey()
	rawPubkey := share.Proofs[0]
	if len(rawPubkey) != 33 {
		return fmt.Errorf("pubkeys must be 33 bytes")
	}
	resultPubkey, err := secp256k1.ParsePubKey(share.Proofs[0])
	if err != nil {
		return err
	}
	for i, proof := range share.Proofs {
		if i == 0 {
			continue
		}
		pubkey, err := secp256k1.ParsePubKey(proof)
		if err != nil {
			return err
		}

		value := new(big.Int).Exp(share.Index, big.NewInt(int64(i)), share.FieldModulus)
		curve := secp256k1.S256()
		resX, resY := curve.ScalarMult(pubkey.X(), pubkey.Y(), value.Bytes())
		resPubkey := common.PublicKeyFromInts(resX, resY)

		resultPubkey = common.AddPublicKeysRaw(resultPubkey, resPubkey)
	}

	if resultPubkey.X().Cmp(targetPubkey.X()) != 0 || resultPubkey.Y().Cmp(targetPubkey.Y()) != 0 {
		return fmt.Errorf("share is not valid")
	}

	return nil
}
