package secretsharing

// This file implements Protocol 3 (Π_add) of
// "Feldman's Verifiable Secret Sharing for a Dishonest Majority".
//
// However, this implementation uses a set of public shares of the secret sharing polynomial
// instead of the set of secret sharing polynomial coefficient commitments.
// This is because, in practice, the public share are typically available while the commitments may not be.
// The two formulations are mathematically equivalent since each set can be reconstructed from the other.
//
// In principle, this implementation can be made to exactly follow the reference
// and require a user with only public shares to reconstruct the commitments.
//
// For completeness, here is a sketch of an algorithm to reconstruct the coefficients of a polynomial F:
//
// 1. while F is not the zero polynomial
// 2.   yield F(0)
// 3.   set F := G where G(x) = (F(x) - F(0)) / x
//
// Note that these operations work even when F is defined by interpolation over a set of evaluations.

// NOTE: Messages must be sent securely from sender to receiver, end to end.
// This is left to the handler that runs the protocol since it has enough information to know how.
// Point-to-point secure transport, such as TLS, may not suffice
// since it allows the protocol coordinator to become a man in the middle.

import (
	"fmt"

	"github.com/lightsparkdev/spark/common/secret_sharing/curve"
	"github.com/lightsparkdev/spark/common/secret_sharing/polynomial"
)

type PartyIndex = string

// IssueRequest is the parameters a caller provides.
type IssueRequest struct {
	IssueIndex PartyIndex   // party index being issued a share
	BigI       []PartyIndex // party index of each sending participant
}

// IssueConfig is what all parties know.
type IssueConfig struct {
	IssueRequest
	Sid    []byte                       // session ID
	T      int                          // secret sharing threshold
	Alphas map[PartyIndex]*curve.Scalar // input, of each participant, to the sharing polynomial
}

// IssueSender is what one sending party knows.
type IssueSender struct {
	Config          IssueConfig
	SmallI          PartyIndex
	SIScalar        *curve.Scalar
	MathcalB        polynomial.InterpolatingPointPolynomial // NOTE: Would be a `PointPolynomial` if following the described protocol.
	allSameMathcalB bool
}

func NewIssueSender(config IssueConfig, smallI PartyIndex, sIScalar *curve.Scalar, mathcalB polynomial.InterpolatingPointPolynomial) (*IssueSender, error) {
	degree := mathcalB.Degree() - 1
	expectedDegree := config.T - 1
	if degree != expectedDegree {
		return nil, fmt.Errorf("public sharing polynomial has the wrong degree: expected %d, is %d", expectedDegree, degree)
	}

	sender := IssueSender{
		Config:          config,
		SmallI:          smallI,
		SIScalar:        sIScalar,
		MathcalB:        mathcalB,
		allSameMathcalB: false,
	}

	return &sender, nil
}

// IssueReceiver is what the receiving party knows.
type IssueReceiver struct {
	Config IssueConfig
}

// Message adds information to route a payload.
type Message[T any] struct {
	From    PartyIndex `json:"from"`
	To      PartyIndex `json:"to"`
	Payload T          `json:"payload"`
}

// IssuePayload1 is the data from round 1 for other parties.
// It must be sent securely to its recipient.
type IssuePayload1 struct {
	Sid    []byte       `json:"sid"`
	SArrow curve.Scalar `json:"sArrow"`
}

// IssuePayload2 is the data from round 2 for other parties.
// It must be sent securely to its recipient.
type IssuePayload2 struct {
	MathcalB polynomial.InterpolatingPointPolynomialBytes `json:"mathcalB"` // NOTE: Would be a `PointPolynomialBytes` if following the described protocol.
	SIIssue  curve.Scalar                                 `json:"sIIssue"`
}

// IssuePayload3 is the final result of round 3.
type IssuePayload3 struct {
	MathcalB polynomial.InterpolatingPointPolynomialBytes `json:"mathcalB"` // NOTE: Would be a `PointPolynomialBytes` if following the described protocol.
	SIssue   curve.Scalar                                 `json:"sIssue"`
}

func newIssueError(round int, err error) error {
	return fmt.Errorf("issue protocol error: round %d: %w", round, err)
}

// AssumeAllPartiesHaveSameCoefficients configures the protocol to assume
// that all parties participating in the protocol
// have the same secret sharing polynomial coefficients in the exponent.
// If that does not hold, then the protocol is not secure,
// and this function should not be called.
func (p *IssueSender) AssumeAllPartiesHaveSameCoefficients() {
	p.allSameMathcalB = true
}

// checkAssumptions checks that the protocol assumptions hold.
func (p IssueSender) checkAssumptions() error {
	// The protocol assumes that all parties have the same mathcal{B}
	// and that s_i is the correct share as defined by mathcal{B}.
	// If this may not be the case, then parties need to begin by echo-broadcasting B
	// (to ensure that all honest parties hold the same vector),
	// and each party needs to locally verify that
	// s_i · G = sum_{k = 0}^{t - 1} (α_i)^k · B_k
	// where mathcal{B} = (B_0, . . . , B_{t − 1}).

	if !p.allSameMathcalB {
		return fmt.Errorf("all parties must have identical secret sharing polynomial coefficients")
	}

	lhs := p.SIScalar.Point()

	alphaI := p.Config.Alphas[p.SmallI]
	rhs := p.MathcalB.Eval(*alphaI)

	if !lhs.Equals(rhs) {
		return fmt.Errorf("party %s's secret share does not match the sharing polynomial", p.SmallI)
	}

	return nil
}

// Round1 is round 1 of the protocol to issue a secret share.
func (p IssueSender) Round1() ([]Message[IssuePayload1], error) {
	if err := p.checkAssumptions(); err != nil {
		return nil, newIssueError(1, fmt.Errorf("assumption violated: %w", err))
	}

	// Each party P_i subshares its share:

	// (a) P_i chooses a random polynomial s_i(x) of degree (t - 1) such that s_i(0) = s_i
	sIPoly, err := polynomial.NewScalarPolynomialSharing(*p.SIScalar, p.Config.T-1)
	if err != nil {
		return nil, err
	}

	// (b) For every j ∈ I, P_i computes s_{i→j} = s_i(α_j)
	// (c) For every j ∈ I, P_i sends (sid, s_{i→j}) to party P_j
	var outMessages []Message[IssuePayload1]
	for _, j := range p.Config.BigI {
		alphaJ := p.Config.Alphas[j]
		sArrow := sIPoly.Eval(*alphaJ)

		payload := IssuePayload1{
			Sid:    p.Config.Sid,
			SArrow: sArrow,
		}
		message := Message[IssuePayload1]{
			From:    p.SmallI,
			To:      j,
			Payload: payload,
		}
		outMessages = append(outMessages, message)
	}

	return outMessages, nil
}

func (c IssueConfig) lagrangeBasisAt(i int, x curve.Scalar) *curve.Scalar {
	// Let α_1, ... , α_n be distinct field elements.
	// We denote the Lagrange basis polynomials with respect to a set I ⊆ [n] by { L_i^I(x) }_{i ∈ I}
	// where L_i^I(x) = prod_{j ∈ I\{i}} (x − α_j) / (α_i − α_j).

	// TODO: Optimize by computing in a constructor
	var xs []curve.Scalar
	for _, j := range c.BigI {
		xs = append(xs, *c.Alphas[j])
	}

	l := polynomial.LagrangeBasisAt(xs, i, x)
	return &l
}

// Round2 is round 2 of the protocol to issue a secret share.
func (p IssueSender) Round2(payloadFrom map[PartyIndex]IssuePayload1) (Message[IssuePayload2], error) {
	// TODO: Make sure received (exactly) one message from each party in p.Config.big_i

	// P_i generates the new party's subshare:
	// upon receiving (sid, s_{j→i}) from t − 1 parties P_j, do the following

	// (a) P_i computes s_i^{n + 1} = sum_{j ∈ I} s_{j→i} · L_j^I(α_{n + 1})

	sIIssue := curve.ScalarFromInt(0)

	alphaIssue := p.Config.Alphas[p.Config.IssueIndex]

	for idx, j := range p.Config.BigI {
		lagrangeCoeff := p.Config.lagrangeBasisAt(idx, *alphaIssue)

		sArrow := payloadFrom[j].SArrow
		term := sArrow.Mul(*lagrangeCoeff)

		sIIssue.SetAdd(&term)
	}

	// (b) P_i sends (B, s_i^{n + 1}) to P_{n + 1}
	// (If P_{n + 1} is not online, then this message is encrypted under P_{n + 1}'s public key,
	// and signed with P_i's private signing key, using secure signcryption.)
	outPayload := IssuePayload2{
		MathcalB: p.MathcalB.Encode(),
		SIIssue:  sIIssue,
	}
	outMessage := Message[IssuePayload2]{
		From:    p.SmallI,
		To:      p.Config.IssueIndex,
		Payload: outPayload,
	}

	return outMessage, nil
}

// Round3 is round 3 of the protocol to issue a secret share.
func (p IssueReceiver) Round3(payloadFrom map[PartyIndex]IssuePayload2) (*IssuePayload3, error) {
	// TODO: Make sure received (exactly) one message from each party in p.Config.big_i

	// P_{n + 1} prepares its output:
	// upon receiving t values (mathcal{B}, s_i^{n + 1}) from parties I, do the following

	// (a) P_{n + 1} verifies that all mathcal{B} values are the same from all parties,
	// and aborts if not

	var mathcalB *polynomial.InterpolatingPointPolynomial // NOTE: Would be a `PointPolynomial` if following the described protocol.

	for _, j := range p.Config.BigI {
		q := payloadFrom[j].MathcalB.Decode()
		if mathcalB == nil {
			mathcalB = q
			continue
		}

		if !mathcalB.Equal(q) {
			return nil, newIssueError(3, fmt.Errorf("abort: inconsistent polynomial commitments received"))
		}
	}

	// (b) P_{n + 1} computes s_{n + 1} = sum_{i ∈ I} s_i^{n + 1} · L_i^I(0)

	sIssue := curve.ScalarFromInt(0)
	for idx, i := range p.Config.BigI {
		term := p.Config.lagrangeBasisAt(idx, curve.ScalarFromInt(0))

		sIIssue := payloadFrom[i].SIIssue
		term.SetMul(&sIIssue)

		sIssue.SetAdd(term)
	}

	// (c) P_{n + 1} verifies that s_{n + 1} · G = sum_{k = 0}^{t - 1} (α_{n + 1})^k · B_k,
	// where mathcal{B} = (B_0, . . . , B_{t − 1}), and aborts if not
	lhs := sIssue.Point()

	alphaIssue := p.Config.Alphas[p.Config.IssueIndex]
	rhs := mathcalB.Eval(*alphaIssue)

	if !lhs.Equals(rhs) {
		return nil, newIssueError(3, fmt.Errorf("abort: issued share is not correct"))
	}

	// (d) P_{n + 1} outputs (B, s_{n + 1})
	outPayload := IssuePayload3{
		MathcalB: mathcalB.Encode(),
		SIssue:   sIssue,
	}

	return &outPayload, nil
}
