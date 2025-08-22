package secretsharing

import (
	"maps"
	"slices"
	"testing"

	"github.com/lightsparkdev/spark/common/secret_sharing/curve"
	"github.com/lightsparkdev/spark/common/secret_sharing/polynomial"
	"github.com/stretchr/testify/require"
)

func scalarPointerFromInt(n uint32) *curve.Scalar {
	s := curve.ScalarFromInt(n)
	return &s
}

// TestIssueProtocolFull tests the full issue protocol flow
// from setup through all rounds to final share generation and verification
func TestIssueProtocolFull(t *testing.T) {
	// === Setup ===

	threshold := 3

	establishedShareArgs := map[PartyIndex]*curve.Scalar{
		"0": scalarPointerFromInt(1),
		"1": scalarPointerFromInt(2),
		"2": scalarPointerFromInt(3),
		"3": scalarPointerFromInt(4),
		"4": scalarPointerFromInt(5),
	}

	issuePartyIndex := PartyIndex("8")
	issueShareArg := curve.ScalarFromInt(9)

	// Create a secret and a verifiable secret sharing of it
	secret := curve.ScalarFromInt(12345)

	sharingPoly, err := polynomial.NewScalarPolynomialSharing(secret, threshold-1)
	if err != nil {
		t.Fatalf("Failed to create sharing polynomial: %v", err)
	}

	pubSharingPoly := sharingPoly.ToPointPolynomial()

	establishedShares := make(map[PartyIndex]*curve.Scalar)
	for partyIdx, shareArg := range establishedShareArgs {
		share := sharingPoly.Eval(*shareArg)
		establishedShares[partyIdx] = &share
	}

	pubSharesInterpolatingPoly := polynomial.NewInterpolatingPointPolynomialFromPolynomial(pubSharingPoly)

	// === Request ===

	req := IssueRequest{
		IssueIndex: issuePartyIndex,
		BigI:       slices.Collect(maps.Keys(establishedShareArgs)),
	}

	shareArgs := maps.Clone(establishedShareArgs)
	shareArgs[issuePartyIndex] = &issueShareArg

	// Create the common configuration for all parties
	config := IssueConfig{
		IssueRequest: req,
		Sid:          []byte("test-issue-session"),
		T:            threshold,
		Alphas:       shareArgs,
	}

	// === Round 1 ===

	var allRound1Messages []Message[IssuePayload1]
	senders := make(map[PartyIndex]*IssueSender)

	// Each established party acts as a sender
	for _, senderIdx := range req.BigI {
		sender := &IssueSender{
			Config:   config,
			SmallI:   senderIdx,
			SIScalar: establishedShares[senderIdx],
			MathcalB: pubSharesInterpolatingPoly, // *pubSharingPoly,
		}
		sender.AssumeAllPartiesHaveSameCoefficients()
		senders[senderIdx] = sender

		// Execute Round 1
		messages, err := sender.Round1()
		if err != nil {
			t.Fatalf("Round 1 failed for party %s: %v", senderIdx, err)
		}

		// TODO: Marshal then later unmarshal messages to test more.
		allRound1Messages = append(allRound1Messages, messages...)
	}

	// Organize round 1 messages by recipient
	messagesByRecipient := make(map[PartyIndex]map[PartyIndex]IssuePayload1)
	for _, msg := range allRound1Messages {
		if messagesByRecipient[msg.To] == nil {
			messagesByRecipient[msg.To] = make(map[PartyIndex]IssuePayload1)
		}
		messagesByRecipient[msg.To][msg.From] = msg.Payload
	}

	// === Round 2 ===

	var allRound2Messages []Message[IssuePayload2]

	for _, senderIdx := range req.BigI {
		sender := senders[senderIdx]

		// Get the subshares this party received from others
		receivedPayloads := messagesByRecipient[senderIdx]

		// Execute Round 2
		message, err := sender.Round2(receivedPayloads)
		if err != nil {
			t.Fatalf("Round 2 failed for party %s: %v", senderIdx, err)
		}

		allRound2Messages = append(allRound2Messages, message)
	}

	// Organize Round 2 messages for the receiver
	round2PayloadsByParty := make(map[PartyIndex]IssuePayload2)
	for _, msg := range allRound2Messages {
		if msg.To != req.IssueIndex {
			t.Fatalf("Round 2 message sent to wrong party: expected %s, got %s", req.IssueIndex, msg.To)
		}
		round2PayloadsByParty[msg.From] = msg.Payload
	}

	// === Round 3 ===

	// Create receiver for the new party
	receiver := &IssueReceiver{
		Config: config,
	}

	// Execute Round 3
	finalResult, err := receiver.Round3(round2PayloadsByParty)
	if err != nil {
		t.Fatalf("Final round failed: %v", err)
	}

	// === Verification ===

	sIssue, err := curve.ParseScalar(finalResult.SIssue)
	require.NoError(t, err)

	mathcalB := finalResult.MathcalB.Decode()

	// Verify that the issued share lies on the sharing polynomial
	expectedShare := sharingPoly.Eval(issueShareArg)
	if !sIssue.Equals(expectedShare) {
		t.Errorf("Issued share is incorrect")
		t.Errorf("Expected: %s", expectedShare.String())
		t.Errorf("Got: %s", sIssue.String())
	}

	// Verify that the polynomial commitment is correct
	if !mathcalB.Equal(&pubSharesInterpolatingPoly) {
		t.Errorf("Polynomial commitment in result doesn't match original")
	}

	// Verify that the new share can help reconstruct the secret
	var interpolationEvals []polynomial.ScalarEval
	for _, partyIdx := range req.BigI[:threshold-1] {
		interpolationEvals = append(interpolationEvals, polynomial.ScalarEval{
			X: *establishedShareArgs[partyIdx],
			Y: *establishedShares[partyIdx],
		})
	}
	interpolationEvals = append(interpolationEvals, polynomial.ScalarEval{
		X: issueShareArg,
		Y: sIssue,
	})

	reconstructedSecret := polynomial.ReconstructScalar(interpolationEvals)

	if !reconstructedSecret.Equals(secret) {
		t.Errorf("Reconstruction with new share failed")
		t.Errorf("Expected secret: %s", secret.String())
		t.Errorf("Reconstructed: %s", reconstructedSecret.String())
	}

	t.Logf("Issue protocol completed successfully")
	t.Logf("New party %s received valid share: %s", req.IssueIndex, sIssue.String())
}
