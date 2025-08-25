package polynomial

import (
	"fmt"

	"github.com/lightsparkdev/spark/common/secret_sharing/curve"
)

// polynomialEval is the evaluation of a polynomial.
type polynomialEval[Codomain any] struct {
	X curve.Scalar
	Y Codomain
}

// ScalarEval is the evaluation of a polynomial that outputs scalars.
type ScalarEval = polynomialEval[curve.Scalar]

// PointEval is the evaluation of a polynomial that outputs curve points.
type PointEval = polynomialEval[curve.Point]

func xCoords[T any](evals []polynomialEval[T]) []curve.Scalar {
	xs := make([]curve.Scalar, len(evals))
	for i, eval := range evals {
		xs[i] = eval.X
	}
	return xs
}

// LagrangeBasisAt computes the Lagrange basis polynomial for index i evaluated at x:
// prod_{j≠i} (x - xs[j]) / (xs[i] - xs[j]).
//
// It returns an error if two passed xs are the same.
func LagrangeBasisAt(xs []curve.Scalar, i int, x curve.Scalar) (curve.Scalar, error) {
	numProd := curve.ScalarFromInt(1)
	denProd := curve.ScalarFromInt(1)

	for j, xj := range xs {
		if i == j {
			continue
		}

		// (x - x_j) / (x_i - x_j)
		num := x.Sub(xj)
		den := xs[i].Sub(xj)

		numProd.SetMul(&num)
		denProd.SetMul(&den)
	}

	denProdInv, err := denProd.InvNonConst()
	if err != nil {
		return curve.Scalar{}, fmt.Errorf("Lagrange basis polynomial is not defined when two nodes are equal")
	}

	return numProd.Mul(denProdInv), nil
}

// InterpolateScalar returns P(x) for given x where P is the unique polynomial
// of least degree that passes through the given evaluations.
//
// It returns an error if two passed evals have the same x coordinate.
func InterpolateScalar(evals []ScalarEval, x curve.Scalar) (curve.Scalar, error) {
	sum := curve.ScalarFromInt(0)

	xs := xCoords(evals)

	// P(x) = sum_i y_i * L_i(x)
	for i, eval := range evals {
		lagrangeI, err := LagrangeBasisAt(xs, i, x)
		if err != nil {
			return curve.Scalar{}, fmt.Errorf("failed to interpolate: %w", err)
		}

		// y_i * L_i(x)
		yI := eval.Y
		term := yI.Mul(lagrangeI)

		sum.SetAdd(&term)
	}

	return sum, nil
}

// InterpolatePoint returns P(x) for given x where P is the unique polynomial
// of least degree that passes through the given evaluations.
//
// It returns an error if two passed evals have the same x coordinate.
func InterpolatePoint(evals []PointEval, x curve.Scalar) (curve.Point, error) {
	sum := curve.ScalarFromInt(0).Point()

	xs := xCoords(evals)

	// P(x) = sum_i y_i * L_i(x)
	for i, eval := range evals {
		lagrangeI, err := LagrangeBasisAt(xs, i, x)
		if err != nil {
			return curve.Point{}, fmt.Errorf("failed to interpolate: %w", err)
		}

		// y_i * L_i(x)
		term := eval.Y
		term.SetScalarMul(&lagrangeI)

		sum.SetAdd(&term)
	}

	return sum, nil
}

// ReconstructScalar returns P(0) where P is the unique polynomial
// of least degree that passes through the given evaluation points.
//
// It returns an error if two passed evals have the same x coordinate.
func ReconstructScalar(evals []ScalarEval) (curve.Scalar, error) {
	return InterpolateScalar(evals, curve.ScalarFromInt(0))
}

// ReconstructPoint returns P(0) where P is the unique polynomial
// of least degree that passes through the given evaluation points.
//
// It returns an error if two passed evals have the same x coordinate.
func ReconstructPoint(evals []PointEval) (curve.Point, error) {
	return InterpolatePoint(evals, curve.ScalarFromInt(0))
}

// InterpolatingPointPolynomial is the unique polynomial of least degree
// that has the given evaluations.
type InterpolatingPointPolynomial struct {
	standardEvals []PointEval
}

// InterpolatingPointPolynomialBytes holds an encoding of a polynomial.
type InterpolatingPointPolynomialBytes []byte

func standardInterpolatingX(index int) curve.Scalar {
	return curve.ScalarFromInt(uint32(index + 1))
}

// Encode returns the encoding of a polynomial.
func (p *InterpolatingPointPolynomial) Encode() InterpolatingPointPolynomialBytes {
	var encoding []byte
	for _, eval := range p.standardEvals {
		yEncoding := eval.Y.Serialize()
		encoding = append(encoding, yEncoding[:]...)
	}
	return encoding
}

// Decode returns a polynomial decoded from an encoding.
func (pb InterpolatingPointPolynomialBytes) Decode() *InterpolatingPointPolynomial {
	evalsCount := len(pb) / curve.PointBytesLen
	standardEvals := make([]PointEval, evalsCount)

	for i := range standardEvals {
		yBytes := pb[i*curve.PointBytesLen : (i+1)*curve.PointBytesLen]
		y, err := curve.ParsePoint(yBytes)
		if err != nil {
			// TODO: return an error and adjust the caller to expect it
			panic("failed to parse")
		}

		standardEvals[i] = PointEval{
			X: standardInterpolatingX(i),
			Y: y,
		}
	}

	return &InterpolatingPointPolynomial{
		standardEvals: standardEvals,
	}
}

func NewInterpolatingPointPolynomial(evals []PointEval) InterpolatingPointPolynomial {
	standardEvals := make([]PointEval, len(evals))

	for i := range standardEvals {
		standardX := standardInterpolatingX(i)

		// Since the standard xs are all different, interpolating does not return an error.
		standardY, _ := InterpolatePoint(evals, standardX)

		standardEvals[i] = PointEval{
			X: standardX,
			Y: standardY,
		}
	}

	return InterpolatingPointPolynomial{
		standardEvals: standardEvals,
	}
}

func NewInterpolatingPointPolynomialFromPolynomial(poly *PointPolynomial) InterpolatingPointPolynomial {
	standardEvals := make([]PointEval, len(poly.Coefs))

	for i := range standardEvals {
		standardX := standardInterpolatingX(i)
		standardY := poly.Eval(standardX)
		standardEvals[i] = PointEval{
			X: standardX,
			Y: standardY,
		}
	}

	return InterpolatingPointPolynomial{
		standardEvals: standardEvals,
	}
}
func (p *InterpolatingPointPolynomial) Degree() int {
	return len(p.standardEvals)
}

// Eval evaluates the polynomial at x.
func (p *InterpolatingPointPolynomial) Eval(x curve.Scalar) curve.Point {
	// Since the standard xs are all different, interpolating does not return an error.
	value, _ := InterpolatePoint(p.standardEvals, x)

	return value
}

func (p *InterpolatingPointPolynomial) Equal(q *InterpolatingPointPolynomial) bool {
	if len(p.standardEvals) != len(q.standardEvals) {
		return false
	}

	for i := range p.standardEvals {
		pY := p.standardEvals[i].Y
		qY := q.standardEvals[i].Y

		if !pY.Equals(qY) {
			return false
		}
	}

	return true
}

// ScalarPolynomial represents a polynomial with scalar coefficients.
type ScalarPolynomial struct {
	Coefs []curve.Scalar
}

// PointPolynomial represents a polynomial with curve point coefficients.
type PointPolynomial struct {
	Coefs []curve.Point
}

// PointPolynomialBytes holds an encoding of a polynomial.
type PointPolynomialBytes []byte

// Encode returns the encoding of a polynomial.
func (p *PointPolynomial) Encode() PointPolynomialBytes {
	var encoding []byte
	for _, coef := range p.Coefs {
		coefEncoding := coef.Serialize()
		encoding = append(encoding, coefEncoding[:]...)
	}
	return encoding
}

// Decode returns a polynomial decoded from an encoding.
func (pb PointPolynomialBytes) Decode() *PointPolynomial {
	coefCount := len(pb) / curve.PointBytesLen
	coefs := make([]curve.Point, coefCount)

	for i := range coefs {
		coefBytes := pb[i*curve.PointBytesLen : (i+1)*curve.PointBytesLen]
		coef, err := curve.ParsePoint(coefBytes)
		if err != nil {
			// TODO: return an error and adjust the caller to expect it
			panic("failed to parse")
		}

		coefs[i] = coef
	}

	return &PointPolynomial{
		Coefs: coefs,
	}
}

// NewScalarPolynomialSharing returns a polynomial with random scalar coefficients
// and the passed secret as constant term.
//
// It returns an error if the internal random generation does.
func NewScalarPolynomialSharing(secret curve.Scalar, degree int) (*ScalarPolynomial, error) {
	coefs := make([]curve.Scalar, degree+1)

	for i := range coefs {
		coef, err := curve.GenerateScalar()
		if err != nil {
			return nil, err
		}

		coefs[i] = coef
	}

	coefs[0] = secret

	poly := ScalarPolynomial{Coefs: coefs}
	return &poly, nil
}

// ToPointPolynomial creates a polynomial whose coefficients are the original scalars times the base point.
func (p *ScalarPolynomial) ToPointPolynomial() *PointPolynomial {
	pointCoefs := make([]curve.Point, len(p.Coefs))

	for i, coef := range p.Coefs {
		pointCoefs[i] = coef.Point()
	}

	return &PointPolynomial{Coefs: pointCoefs}
}

// Eval evaluates the polynomial at x.
func (p *ScalarPolynomial) Eval(x curve.Scalar) curve.Scalar {
	if len(p.Coefs) == 0 {
		return curve.ScalarFromInt(0)
	}

	// Use Horner's method: a + b x + c x^2 + d x^3 = ((d x + c) x + b) x + a.
	result := p.Coefs[len(p.Coefs)-1]

	for i := len(p.Coefs) - 2; i >= 0; i-- {
		result.SetMul(&x)
		result.SetAdd(&p.Coefs[i])
	}

	return result
}

// Eval evaluates the polynomial at x.
func (p *PointPolynomial) Eval(x curve.Scalar) curve.Point {
	if len(p.Coefs) == 0 {
		return curve.IdentityPoint()
	}

	// Use Horner's method: a + b x + c x^2 + d x^3 = ((d x + c) x + b) x + a.
	result := p.Coefs[len(p.Coefs)-1]

	for i := len(p.Coefs) - 2; i >= 0; i-- {
		result.SetScalarMul(&x)
		result.SetAdd(&p.Coefs[i])
	}

	return result
}

func (p *PointPolynomial) Equal(q *PointPolynomial) bool {
	if len(p.Coefs) != len(q.Coefs) {
		return false
	}

	for i := range p.Coefs {
		if !p.Coefs[i].Equals(q.Coefs[i]) {
			return false
		}
	}

	return true
}
