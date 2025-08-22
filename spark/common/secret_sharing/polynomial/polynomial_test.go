package polynomial

import (
	"testing"

	"github.com/lightsparkdev/spark/common/secret_sharing/curve"
	"github.com/stretchr/testify/assert"
)

// Test basic ScalarPolynomial creation and evaluation
func TestScalarPolynomialBasic(t *testing.T) {
	// Test constant polynomial (degree 0)
	secret := curve.ScalarFromInt(42)
	poly, err := NewScalarPolynomialSharing(secret, 0)
	if err != nil {
		t.Fatalf("Failed to create constant polynomial: %v", err)
	}

	if len(poly.Coefs) != 1 {
		t.Errorf("Expected 1 coefficient for degree 0, got %d", len(poly.Coefs))
	}

	if !poly.Coefs[0].Equals(secret) {
		t.Errorf("Constant term should equal secret")
	}

	// Test evaluation at various points
	testPoints := []uint32{0, 1, 5, 100}
	for _, x := range testPoints {
		xScalar := curve.ScalarFromInt(x)
		result := poly.Eval(xScalar)
		if !result.Equals(secret) {
			t.Errorf("Constant polynomial should return %s at x=%d, got %s",
				secret.String(), x, result.String())
		}
	}
}

// Test ScalarPolynomial with higher degrees
func TestScalarPolynomialDegrees(t *testing.T) {
	secret := curve.ScalarFromInt(123)

	// Test polynomials of various degrees
	degrees := []int{1, 2, 5, 10}

	for _, degree := range degrees {
		poly, err := NewScalarPolynomialSharing(secret, degree)
		if err != nil {
			t.Fatalf("Failed to create polynomial of degree %d: %v", degree, err)
		}

		if len(poly.Coefs) != degree+1 {
			t.Errorf("Expected %d coefficients for degree %d, got %d",
				degree+1, degree, len(poly.Coefs))
		}

		// Constant term should be the secret
		if !poly.Coefs[0].Equals(secret) {
			t.Errorf("Constant term should equal secret for degree %d", degree)
		}

		// Evaluation at x=0 should return the secret
		zero := curve.ScalarFromInt(0)
		result := poly.Eval(zero)
		if !result.Equals(secret) {
			t.Errorf("P(0) should equal secret for degree %d, got %s",
				degree, result.String())
		}
	}
}

// Test manual polynomial creation and evaluation
func TestManualScalarPolynomial(t *testing.T) {
	// Create polynomial: 5 + 3x + 2x^2
	coefs := []curve.Scalar{
		curve.ScalarFromInt(5), // constant term
		curve.ScalarFromInt(3), // x coefficient
		curve.ScalarFromInt(2), // x^2 coefficient
	}

	poly := &ScalarPolynomial{Coefs: coefs}

	// Test known evaluations
	testCases := []struct {
		x        uint32
		expected uint32
	}{
		{0, 5},  // 5 + 3*0 + 2*0^2 = 5
		{1, 10}, // 5 + 3*1 + 2*1^2 = 10
		{2, 19}, // 5 + 3*2 + 2*2^2 = 19
		{3, 32}, // 5 + 3*3 + 2*3^2 = 32
	}

	for _, tc := range testCases {
		x := curve.ScalarFromInt(tc.x)
		expected := curve.ScalarFromInt(tc.expected)
		result := poly.Eval(x)

		if !result.Equals(expected) {
			t.Errorf("P(%d) expected %d, got %s", tc.x, tc.expected, result.String())
		}
	}
}

// Test PointPolynomial creation and evaluation
func TestPointPolynomial(t *testing.T) {
	// Create polynomial with point coefficients: G*5 + G*3*x + G*2*x^2
	coefs := []curve.Point{
		curve.ScalarFromInt(5).Point(), // G*5
		curve.ScalarFromInt(3).Point(), // G*3
		curve.ScalarFromInt(2).Point(), // G*2
	}

	poly := &PointPolynomial{Coefs: coefs}

	// Test evaluation - should give same results as scalar version but as points
	testCases := []struct {
		x        uint32
		expected uint32
	}{
		{0, 5},
		{1, 10},
		{2, 19},
		{3, 32},
	}

	for _, tc := range testCases {
		x := curve.ScalarFromInt(tc.x)
		expectedPoint := curve.ScalarFromInt(tc.expected).Point()
		result := poly.Eval(x)

		assert.True(t, expectedPoint.Equals(result), "Point polynomial P(%d) gave incorrect result", tc.x)
	}
}

// Test Lagrange interpolation for scalars
func TestScalarLagrangeInterpolation(t *testing.T) {
	// Test with known polynomial: 7 + 4x + 2x^2
	secret := curve.ScalarFromInt(7)
	coefs := []curve.Scalar{
		secret,
		curve.ScalarFromInt(4),
		curve.ScalarFromInt(2),
	}
	poly := &ScalarPolynomial{Coefs: coefs}

	// Generate evaluation points
	points := []ScalarEval{
		{X: curve.ScalarFromInt(1), Y: poly.Eval(curve.ScalarFromInt(1))}, // P(1) = 13
		{X: curve.ScalarFromInt(2), Y: poly.Eval(curve.ScalarFromInt(2))}, // P(2) = 23
		{X: curve.ScalarFromInt(3), Y: poly.Eval(curve.ScalarFromInt(3))}, // P(3) = 37
	}

	// Reconstruct at x=0 (should give us the secret)
	reconstructed := ReconstructScalar(points)

	if !reconstructed.Equals(secret) {
		t.Errorf("Scalar Lagrange interpolation failed")
		t.Errorf("Expected: %s", secret.String())
		t.Errorf("Got: %s", reconstructed.String())
	}
}

// Test Lagrange interpolation for points
func TestPointLagrangeInterpolation(t *testing.T) {
	// Test with known polynomial over points: G*7 + G*4*x + G*2*x^2
	secret := curve.ScalarFromInt(7)
	secretPoint := (secret).Point()

	coefs := []curve.Point{
		secretPoint,
		curve.ScalarFromInt(4).Point(),
		curve.ScalarFromInt(2).Point(),
	}
	poly := &PointPolynomial{Coefs: coefs}

	// Generate evaluation points
	points := []PointEval{
		{X: curve.ScalarFromInt(1), Y: poly.Eval(curve.ScalarFromInt(1))},
		{X: curve.ScalarFromInt(2), Y: poly.Eval(curve.ScalarFromInt(2))},
		{X: curve.ScalarFromInt(3), Y: poly.Eval(curve.ScalarFromInt(3))},
	}

	// Reconstruct at x=0 (should give us the secret point)
	reconstructed := ReconstructPoint(points)

	assert.True(t, secretPoint.Equals(reconstructed), "Point Lagrange interpolation failed to reconstruct secret point")
}

// Test threshold secret sharing scenario
func TestThresholdSecretSharing(t *testing.T) {
	secret := curve.ScalarFromInt(999)
	threshold := 3
	numShares := 5

	// Create polynomial for (3,5) threshold scheme
	poly, err := NewScalarPolynomialSharing(secret, threshold-1)
	if err != nil {
		t.Fatalf("Failed to create polynomial: %v", err)
	}

	// Generate shares
	shares := make([]ScalarEval, numShares)
	for i := range numShares {
		x := curve.ScalarFromInt(uint32(i + 1)) // Party IDs 1,2,3,4,5
		y := poly.Eval(x)
		shares[i] = ScalarEval{X: x, Y: y}
	}

	// Test that any 3 shares can reconstruct the secret
	for i := 0; i <= numShares-threshold; i++ {
		subset := shares[i : i+threshold]
		reconstructed := ReconstructScalar(subset)

		if !reconstructed.Equals(secret) {
			t.Errorf("Failed to reconstruct secret with shares %d-%d",
				i+1, i+threshold)
		}
	}

	// Test that 2 shares cannot reconstruct (should give wrong result)
	twoShares := shares[0:2]
	wrongReconstruction := ReconstructScalar(twoShares)

	if wrongReconstruction.Equals(secret) {
		t.Errorf("Two shares should not be able to reconstruct the secret")
	}
}

func TestScalarPolynomialEmpty(t *testing.T) {
	emptyPoly := &ScalarPolynomial{Coefs: []curve.Scalar{}}
	assert.Equal(t, curve.ScalarFromInt(0), emptyPoly.Eval(curve.ScalarFromInt(5)))
}

func TestPointPolynomialEmpty(t *testing.T) {
	emptyPoly := &PointPolynomial{Coefs: []curve.Point{}}

	eval := emptyPoly.Eval(curve.ScalarFromInt(5))
	assert.True(t, curve.IdentityPoint().Equals(eval), "empty point polynomial should evaluate to zero")
}

func TestScalarPolynomialSingeton(t *testing.T) {
	singleton := []ScalarEval{
		{X: curve.ScalarFromInt(5), Y: curve.ScalarFromInt(42)},
	}

	assert.Equal(t, curve.ScalarFromInt(42), ReconstructScalar(singleton), "single eval interpolation failed")
}

func TestPointPolynomialSingleton(t *testing.T) {
	height := curve.ScalarFromInt(42).Point()
	singleton := []PointEval{
		{X: curve.ScalarFromInt(5), Y: height},
	}

	assert.True(t, height.Equals(ReconstructPoint(singleton)), "single eval interpolation failed")
}

// Test Lagrange basis function
func TestLagrangeBasis(t *testing.T) {
	// Test with known x values
	xs := []curve.Scalar{
		curve.ScalarFromInt(1),
		curve.ScalarFromInt(2),
		curve.ScalarFromInt(3),
	}

	// L_0(0) = (0-2)(0-3) / ((1-2)(1-3)) = 6/2 = 3
	basis0 := LagrangeBasisAt(xs, 0, curve.ScalarFromInt(0))
	expected0 := curve.ScalarFromInt(3)
	assert.True(t, expected0.Equals(basis0), "L_0(0) expected 3, got %s", basis0.String())

	// L_1(0) = (0-1)(0-3) / ((2-1)(2-3)) = 3/(-1) = -3
	basis1 := LagrangeBasisAt(xs, 1, curve.ScalarFromInt(0))
	expected1 := curve.ScalarFromInt(3).Neg()
	assert.True(t, expected1.Equals(basis1), "L_1(0) expected -3, got %s", basis1.String())

	// L_2(0) = (0-1)(0-2) / ((3-1)(3-2)) = 2/2 = 1
	basis2 := LagrangeBasisAt(xs, 2, curve.ScalarFromInt(0))
	expected2 := curve.ScalarFromInt(1)
	assert.True(t, expected2.Equals(basis2), "L_2(0) expected 1, got %s", basis2.String())
}

// Test consistency between scalar and point polynomials
func TestScalarPointConsistency(t *testing.T) {
	// Create matching scalar and point polynomials
	scalarCoefs := []curve.Scalar{
		curve.ScalarFromInt(10),
		curve.ScalarFromInt(20),
		curve.ScalarFromInt(30),
	}

	pointCoefs := make([]curve.Point, len(scalarCoefs))
	for i, coef := range scalarCoefs {
		pointCoefs[i] = (coef).Point()
	}

	scalarPoly := &ScalarPolynomial{Coefs: scalarCoefs}
	pointPoly := &PointPolynomial{Coefs: pointCoefs}

	// Test that evaluations are consistent
	testPoints := []uint32{0, 1, 2, 5, 10}

	for _, xVal := range testPoints {
		x := curve.ScalarFromInt(xVal)

		scalarResult := scalarPoly.Eval(x)
		pointResult := pointPoly.Eval(x)
		expectedPoint := (scalarResult).Point()

		assert.True(t, expectedPoint.Equals(pointResult), "Inconsistency at x=%d between scalar and point polynomial", xVal)
	}
}

// Test InterpolatingPointPolynomial construction from evaluations
func TestInterpolatingPointPolynomialFromEvals(t *testing.T) {
	// Create some test evaluation points
	evals := []PointEval{
		{X: curve.ScalarFromInt(1), Y: curve.ScalarFromInt(10).Point()},
		{X: curve.ScalarFromInt(2), Y: curve.ScalarFromInt(23).Point()},
		{X: curve.ScalarFromInt(3), Y: curve.ScalarFromInt(40).Point()},
	}

	poly := NewInterpolatingPointPolynomial(evals)

	// Test that the polynomial evaluates correctly at the original points
	for i, eval := range evals {
		result := poly.Eval(eval.X)
		assert.True(t, eval.Y.Equals(result), "Interpolating polynomial doesn't match evaluation point %d", i)
	}

	// Test evaluation at a new point
	zero := curve.ScalarFromInt(0)
	secretPoint := poly.Eval(zero)

	// Verify using direct interpolation
	expectedSecret := InterpolatePoint(evals, zero)
	assert.True(t, expectedSecret.Equals(secretPoint), "Interpolating polynomial gives different result than direct interpolation")
}

// Test InterpolatingPointPolynomial construction from PointPolynomial
func TestInterpolatingPointPolynomialFromPolynomial(t *testing.T) {
	// Create a specific polynomial: (5 + 3 x + 2 x^2) G
	originalPoly := &PointPolynomial{
		Coefs: []curve.Point{
			curve.ScalarFromInt(5).Point(),
			curve.ScalarFromInt(3).Point(),
			curve.ScalarFromInt(2).Point(),
		},
	}

	// Create InterpolatingPointPolynomial from it
	interpPoly := NewInterpolatingPointPolynomialFromPolynomial(originalPoly)

	// Test that both polynomials evaluate to the same result at various points
	testPoints := []uint32{0, 1, 2, 4, 10}
	for _, xVal := range testPoints {
		x := curve.ScalarFromInt(xVal)
		originalResult := originalPoly.Eval(x)
		interpResult := interpPoly.Eval(x)

		assert.True(t, originalResult.Equals(interpResult), "InterpolatingPointPolynomial differs from original at x=%d", xVal)
	}
}

// Test InterpolatingPointPolynomial encoding and decoding
func TestInterpolatingPointPolynomialEncodeDecode(t *testing.T) {
	// Create test polynomial
	evals := []PointEval{
		{X: curve.ScalarFromInt(1), Y: curve.ScalarFromInt(17).Point()},
		{X: curve.ScalarFromInt(2), Y: curve.ScalarFromInt(31).Point()},
		{X: curve.ScalarFromInt(3), Y: curve.ScalarFromInt(49).Point()},
		{X: curve.ScalarFromInt(4), Y: curve.ScalarFromInt(71).Point()},
	}

	originalPoly := NewInterpolatingPointPolynomial(evals)

	// Test encoding
	encoded := originalPoly.Encode()
	expectedLen := len(evals) * curve.PointBytesLen
	if len(encoded) != expectedLen {
		t.Errorf("Encoded length expected %d, got %d", expectedLen, len(encoded))
	}

	// Test decoding
	decodedPoly := encoded.Decode()

	// Test that original and decoded polynomials are equal
	if !originalPoly.Equal(decodedPoly) {
		t.Errorf("Decoded polynomial doesn't equal original")
	}

	// Test that they evaluate to the same results
	testPoints := []uint32{0, 1, 2, 5, 7}
	for _, xVal := range testPoints {
		x := curve.ScalarFromInt(xVal)
		originalResult := originalPoly.Eval(x)
		decodedResult := decodedPoly.Eval(x)

		assert.True(t, originalResult.Equals(decodedResult), "Decoded polynomial gives different result at x=%d", xVal)
	}
}

// Test InterpolatingPointPolynomial equality
func TestInterpolatingPointPolynomialEquality(t *testing.T) {
	// Create two identical polynomials
	evals := []PointEval{
		{X: curve.ScalarFromInt(1), Y: curve.ScalarFromInt(100).Point()},
		{X: curve.ScalarFromInt(2), Y: curve.ScalarFromInt(200).Point()},
	}

	poly1 := NewInterpolatingPointPolynomial(evals)
	poly2 := NewInterpolatingPointPolynomial(evals)

	// Test equality
	if !poly1.Equal(&poly2) {
		t.Errorf("Identical polynomials should be equal")
	}

	// Test reflexivity
	if !poly1.Equal(&poly1) {
		t.Errorf("Polynomial should be equal to itself")
	}

	// Create different polynomial
	differentEvals := []PointEval{
		{X: curve.ScalarFromInt(1), Y: curve.ScalarFromInt(101).Point()}, // Different value
		{X: curve.ScalarFromInt(2), Y: curve.ScalarFromInt(200).Point()},
	}

	poly3 := NewInterpolatingPointPolynomial(differentEvals)

	// Test inequality
	if poly1.Equal(&poly3) {
		t.Errorf("Different polynomials should not be equal")
	}

	// Test different lengths
	shorterEvals := []PointEval{
		{X: curve.ScalarFromInt(1), Y: curve.ScalarFromInt(100).Point()},
	}

	poly4 := NewInterpolatingPointPolynomial(shorterEvals)

	if poly1.Equal(&poly4) {
		t.Errorf("Polynomials with different lengths should not be equal")
	}
}

// Test InterpolatingPointPolynomial construction from evaluations
func TestInterpolatingPointPolynomialWellDefined(t *testing.T) {
	// Create two evaluations of one polynomial
	p := &ScalarPolynomial{Coefs: []curve.Scalar{
		curve.ScalarFromInt(5),
		curve.ScalarFromInt(3),
		curve.ScalarFromInt(2),
	}}
	sourcePoly := p.ToPointPolynomial()

	evals1 := []PointEval{
		{X: curve.ScalarFromInt(4), Y: sourcePoly.Eval(curve.ScalarFromInt(4))},
		{X: curve.ScalarFromInt(5), Y: sourcePoly.Eval(curve.ScalarFromInt(5))},
		{X: curve.ScalarFromInt(6), Y: sourcePoly.Eval(curve.ScalarFromInt(6))},
	}

	evals2 := []PointEval{
		{X: curve.ScalarFromInt(7), Y: sourcePoly.Eval(curve.ScalarFromInt(7))},
		{X: curve.ScalarFromInt(8), Y: sourcePoly.Eval(curve.ScalarFromInt(8))},
		{X: curve.ScalarFromInt(9), Y: sourcePoly.Eval(curve.ScalarFromInt(9))},
	}

	poly1 := NewInterpolatingPointPolynomial(evals1)
	poly2 := NewInterpolatingPointPolynomial(evals2)

	// poly1ed := poly1.Encode().Decode()
	// poly2ed := poly2.Encode().Decode()

	if !poly1.Equal(&poly2) {
		t.Errorf("Interpolating polynomial for two evals of the same polynomial don't match")
	}
}

// Test InterpolatingPointPolynomial edge cases
func TestInterpolatingPointPolynomialEdgeCases(t *testing.T) {
	// Test no evaluation points (zero polynomial)
	zeroPoly := NewInterpolatingPointPolynomial([]PointEval{})

	// Test single evaluation point (constant polynomial)
	singleEval := []PointEval{
		{X: curve.ScalarFromInt(5), Y: curve.ScalarFromInt(42).Point()},
	}

	constPoly := NewInterpolatingPointPolynomial(singleEval)

	// Should evaluate to the same point everywhere
	testPoints := []uint32{0, 1, 2, 10, 100}
	expectedZeroPoint := curve.ScalarFromInt(0).Point()
	expectedConstPoint := curve.ScalarFromInt(42).Point()

	for _, xVal := range testPoints {
		x := curve.ScalarFromInt(xVal)
		// result := constPoly.Eval(x)

		assert.True(t, expectedZeroPoint.Equals(zeroPoly.Eval(x)), "Constant polynomial should return same value at x=%d", xVal)

		assert.True(t, expectedConstPoint.Equals(constPoly.Eval(x)), "Constant polynomial should return same value at x=%d", xVal)
	}

	// Test encoding/decoding
	if !zeroPoly.Equal(zeroPoly.Encode().Decode()) {
		t.Errorf("Zero polynomial encode/decode failed")
	}

	if !constPoly.Equal(constPoly.Encode().Decode()) {
		t.Errorf("Single point polynomial encode/decode failed")
	}
}

// Test InterpolatingPointPolynomial consistency with direct interpolation
func TestInterpolatingPointPolynomialConsistency(t *testing.T) {
	// Create test evaluations
	evals := []PointEval{
		{X: curve.ScalarFromInt(1), Y: curve.ScalarFromInt(7).Point()},
		{X: curve.ScalarFromInt(3), Y: curve.ScalarFromInt(19).Point()},
		{X: curve.ScalarFromInt(5), Y: curve.ScalarFromInt(37).Point()},
		{X: curve.ScalarFromInt(7), Y: curve.ScalarFromInt(61).Point()},
	}

	poly := NewInterpolatingPointPolynomial(evals)

	// Test that interpolating polynomial gives same results as direct interpolation
	testPoints := []uint32{0, 2, 4, 6, 8, 10}
	for _, xVal := range testPoints {
		x := curve.ScalarFromInt(xVal)
		polyResult := poly.Eval(x)
		directResult := InterpolatePoint(evals, x)

		assert.True(t, directResult.Equals(polyResult), "InterpolatingPointPolynomial inconsistent with direct interpolation at x=%d", xVal)
	}
}
