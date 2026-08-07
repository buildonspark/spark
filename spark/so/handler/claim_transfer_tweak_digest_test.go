package handler

import (
	"crypto/sha256"
	"encoding/binary"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/anypb"
)

func writeExpectedClaimDigestField(h interface{ Write([]byte) (int, error) }, value []byte) {
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(value)))
	_, _ = h.Write(length[:])
	_, _ = h.Write(value)
}

func TestClaimPostTweakKeyshareDigest(t *testing.T) {
	basePublicKey := keys.GeneratePrivateKey().Public()
	publicKeyTweak := keys.GeneratePrivateKey().Public()
	baseShares := map[string]keys.Public{
		"operator-b": keys.GeneratePrivateKey().Public(),
		"operator-a": keys.GeneratePrivateKey().Public(),
	}
	shareTweaks := map[string]keys.Public{
		"operator-a": keys.GeneratePrivateKey().Public(),
		"operator-b": keys.GeneratePrivateKey().Public(),
	}
	keyshare := &ent.SigningKeyshare{PublicKey: basePublicKey, PublicShares: baseShares}
	tweak := &pb.ClaimLeafKeyTweak{
		SecretShareTweak: &pb.SecretShare{Proofs: [][]byte{publicKeyTweak.Serialize()}},
		PubkeySharesTweak: map[string][]byte{
			"operator-a": shareTweaks["operator-a"].Serialize(),
			"operator-b": shareTweaks["operator-b"].Serialize(),
		},
	}

	digest, err := claimPostTweakKeyshareDigest(keyshare, tweak)
	require.NoError(t, err)

	h := sha256.New()
	writeExpectedClaimDigestField(h, []byte("spark.claim.post-tweak-keyshare.v1"))
	writeExpectedClaimDigestField(h, basePublicKey.Add(publicKeyTweak).Serialize())
	operatorIDs := []string{"operator-a", "operator-b"}
	slices.Sort(operatorIDs)
	var count [4]byte
	binary.BigEndian.PutUint32(count[:], uint32(len(operatorIDs)))
	_, _ = h.Write(count[:])
	for _, operatorID := range operatorIDs {
		writeExpectedClaimDigestField(h, []byte(operatorID))
		writeExpectedClaimDigestField(h, baseShares[operatorID].Add(shareTweaks[operatorID]).Serialize())
	}
	require.Equal(t, h.Sum(nil), digest)
}

// These tests exercise digest unanimity directly because its enclosing
// boundary requires a live multi-operator FROST round. The check prevents
// divergent VSS polynomials from corrupting threshold signing keyshares.

var digestTestTransferID = uuid.MustParse("019ebba9-2a03-77e7-9fa6-05a7d3be388c")

func digestReport(applied bool, digests map[string][]byte) *pbinternal.ClaimTransferPrepareResponse {
	return digestReportWithState(applied, digests, digests)
}

func digestReportWithState(applied bool, proofDigests, keyshareDigests map[string][]byte) *pbinternal.ClaimTransferPrepareResponse {
	resp := &pbinternal.ClaimTransferPrepareResponse{TweaksAlreadyApplied: applied}
	leafIDs := make(map[string]struct{}, len(proofDigests)+len(keyshareDigests))
	for leafID := range proofDigests {
		leafIDs[leafID] = struct{}{}
	}
	for leafID := range keyshareDigests {
		leafIDs[leafID] = struct{}{}
	}
	for leafID := range leafIDs {
		resp.LeafTweakDigests = append(resp.LeafTweakDigests, &pbinternal.ClaimLeafTweakDigest{
			LeafId:                leafID,
			ProofsHash:            proofDigests[leafID],
			PostTweakKeyshareHash: keyshareDigests[leafID],
		})
	}
	return resp
}

func TestValidateClaimTweakDigestUnanimity(t *testing.T) {
	leafA := "11111111-1111-1111-1111-111111111111"
	leafB := "22222222-2222-2222-2222-222222222222"
	hashX := []byte{0x01, 0x02}
	hashY := []byte{0x03, 0x04}

	tests := []struct {
		name        string
		reports     map[string]*pbinternal.ClaimTransferPrepareResponse
		expectedErr string
	}{
		{
			name: "unanimous digests pass",
			reports: map[string]*pbinternal.ClaimTransferPrepareResponse{
				"0001": digestReport(false, map[string][]byte{leafA: hashX, leafB: hashY}),
				"0002": digestReport(false, map[string][]byte{leafA: hashX, leafB: hashY}),
			},
		},
		{
			name: "all staged matching proofs and divergent post-tweak keyshares fail",
			reports: map[string]*pbinternal.ClaimTransferPrepareResponse{
				"0001": digestReportWithState(false, map[string][]byte{leafA: hashX}, map[string][]byte{leafA: hashX}),
				"0002": digestReportWithState(false, map[string][]byte{leafA: hashX}, map[string][]byte{leafA: hashY}),
			},
			expectedErr: "post-tweak keyshare digest mismatch",
		},
		{
			name: "all staged without post-tweak keyshare evidence fails",
			reports: map[string]*pbinternal.ClaimTransferPrepareResponse{
				"0001": digestReportWithState(false, map[string][]byte{leafA: hashX}, nil),
				"0002": digestReportWithState(false, map[string][]byte{leafA: hashX}, nil),
			},
			expectedErr: "no post-tweak keyshare digest",
		},
		{
			name: "divergent digest for one leaf fails",
			reports: map[string]*pbinternal.ClaimTransferPrepareResponse{
				"0001": digestReport(false, map[string][]byte{leafA: hashX}),
				"0002": digestReport(false, map[string][]byte{leafA: hashY}),
			},
			expectedErr: "tweak digest",
		},
		{
			name: "reporter missing a leaf another reports fails",
			reports: map[string]*pbinternal.ClaimTransferPrepareResponse{
				"0001": digestReport(false, map[string][]byte{leafA: hashX, leafB: hashY}),
				"0002": digestReport(false, map[string][]byte{leafA: hashX}),
			},
			expectedErr: "tweak digest",
		},
		{
			name: "all applied X passes",
			reports: map[string]*pbinternal.ClaimTransferPrepareResponse{
				"0001": digestReportWithState(true, nil, map[string][]byte{leafA: hashX}),
				"0002": digestReportWithState(true, nil, map[string][]byte{leafA: hashX}),
			},
		},
		{
			name: "all applied X and Y fails",
			reports: map[string]*pbinternal.ClaimTransferPrepareResponse{
				"0001": digestReportWithState(true, nil, map[string][]byte{leafA: hashX}),
				"0002": digestReportWithState(true, nil, map[string][]byte{leafA: hashY}),
			},
			expectedErr: "post-tweak keyshare digest mismatch",
		},
		{
			name: "all applied without post-tweak keyshare evidence fails",
			reports: map[string]*pbinternal.ClaimTransferPrepareResponse{
				"0001": digestReport(true, nil),
				"0002": digestReport(true, nil),
			},
			expectedErr: "no post-tweak keyshare digests",
		},
		{
			name: "applied X mixed with staged X passes",
			reports: map[string]*pbinternal.ClaimTransferPrepareResponse{
				"0001": digestReportWithState(true, nil, map[string][]byte{leafA: hashX}),
				"0002": digestReportWithState(false, map[string][]byte{leafA: hashX}, map[string][]byte{leafA: hashX}),
			},
		},
		{
			name: "applied X mixed with staged Y fails",
			reports: map[string]*pbinternal.ClaimTransferPrepareResponse{
				"0001": digestReportWithState(true, nil, map[string][]byte{leafA: hashX}),
				"0002": digestReportWithState(false, map[string][]byte{leafA: hashY}, map[string][]byte{leafA: hashY}),
			},
			expectedErr: "post-tweak keyshare digest mismatch",
		},
		{
			name: "mixed applied and staged without post-tweak keyshare evidence fails",
			reports: map[string]*pbinternal.ClaimTransferPrepareResponse{
				"0001": digestReport(true, nil),
				"0002": digestReport(false, map[string][]byte{leafA: hashX}),
			},
			expectedErr: "no post-tweak keyshare digest",
		},
		{
			name:    "no reporters (all old binaries) passes",
			reports: map[string]*pbinternal.ClaimTransferPrepareResponse{},
		},
		{
			name: "staging reporter with zero digests fails",
			reports: map[string]*pbinternal.ClaimTransferPrepareResponse{
				"0001": digestReport(false, nil),
				"0002": digestReport(false, nil),
			},
			expectedErr: "no leaf tweak digests",
		},
		{
			name: "single reporter passes",
			reports: map[string]*pbinternal.ClaimTransferPrepareResponse{
				"0001": digestReport(false, map[string][]byte{leafA: hashX}),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateClaimTweakDigestUnanimity(digestTestTransferID, tc.reports)
			if tc.expectedErr == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.expectedErr)
			}
		})
	}
}

func TestUnanimousClaimTweakDigests(t *testing.T) {
	leafA := "11111111-1111-1111-1111-111111111111"
	hashX := []byte{0x01, 0x02}

	t.Run("returns first staging reporter's digests in deterministic order", func(t *testing.T) {
		digests := unanimousClaimTweakDigests(map[string]*pbinternal.ClaimTransferPrepareResponse{
			"0002": digestReport(false, map[string][]byte{leafA: hashX}),
			"0001": digestReport(true, nil), // applied — must be skipped even though it sorts first
		})
		require.Len(t, digests, 1)
		assert.Equal(t, leafA, digests[0].GetLeafId())
		assert.Equal(t, hashX, digests[0].GetProofsHash())
	})

	t.Run("all applied binds nothing", func(t *testing.T) {
		assert.Empty(t, unanimousClaimTweakDigests(map[string]*pbinternal.ClaimTransferPrepareResponse{
			"0001": digestReport(true, nil),
			"0002": digestReport(true, nil),
		}))
	})

	t.Run("no reporters binds nothing", func(t *testing.T) {
		assert.Empty(t, unanimousClaimTweakDigests(map[string]*pbinternal.ClaimTransferPrepareResponse{}))
	})
}

func TestValidateClaimTweakDigestReporters(t *testing.T) {
	participants := []string{"0001", "0002", "0003"}
	full := map[string]*pbinternal.ClaimTransferPrepareResponse{
		"0001": digestReport(false, map[string][]byte{"leaf": {0x01}}),
		"0002": digestReport(false, map[string][]byte{"leaf": {0x01}}),
		"0003": digestReport(false, map[string][]byte{"leaf": {0x01}}),
	}
	require.NoError(t, validateClaimTweakDigestReporters(digestTestTransferID, participants, full))

	missing := map[string]*pbinternal.ClaimTransferPrepareResponse{
		"0001": digestReport(false, map[string][]byte{"leaf": {0x01}}),
		"0003": digestReport(false, map[string][]byte{"leaf": {0x01}}),
	}
	err := validateClaimTweakDigestReporters(digestTestTransferID, participants, missing)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "0002")
	assert.Contains(t, err.Error(), "did not report")
}

func TestClaimTransferBuildCommitAcceptsUnanimousTweakDigestReports(t *testing.T) {
	leafID := "11111111-1111-1111-1111-111111111111"
	report1, err := anypb.New(digestReport(false, map[string][]byte{leafID: {0x01}}))
	require.NoError(t, err)
	report2, err := anypb.New(digestReport(false, map[string][]byte{leafID: {0x01}}))
	require.NoError(t, err)

	flow := &claimTransferCoordinatorFlow{
		ClaimTransferFlowHandler: NewClaimTransferFlowHandler(&so.Config{
			SigningOperatorMap: map[string]*so.SigningOperator{
				"0001": nil,
				"0002": nil,
			},
		}),
		parsed: parsedClaimTransferRequest{transferID: digestTestTransferID},
	}

	_, err = flow.BuildCommitPayload(t.Context(), map[string]*anypb.Any{
		"0001": report1,
		"0002": report2,
	})
	require.ErrorContains(t, err, "unable to apply receiver key tweaks during coordinator commit")
}

func TestClaimTransferBuildCommitRejectsMissingTweakDigestReporter(t *testing.T) {
	report, err := anypb.New(digestReport(false, map[string][]byte{
		"11111111-1111-1111-1111-111111111111": {0x01},
	}))
	require.NoError(t, err)

	flow := &claimTransferCoordinatorFlow{
		ClaimTransferFlowHandler: NewClaimTransferFlowHandler(&so.Config{
			SigningOperatorMap: map[string]*so.SigningOperator{
				"0001": nil,
				"0002": nil,
			},
		}),
		parsed: parsedClaimTransferRequest{transferID: digestTestTransferID},
	}

	_, err = flow.BuildCommitPayload(t.Context(), map[string]*anypb.Any{
		"0001": report,
		"0002": nil,
	})
	require.Error(t, err)
	assert.ErrorContains(t, err, "SO 0002 did not report tweak digests")
}

func TestClaimTransferBuildCommitRejectsDivergentTweakDigests(t *testing.T) {
	leafID := "11111111-1111-1111-1111-111111111111"
	reportX, err := anypb.New(digestReport(false, map[string][]byte{leafID: {0x01}}))
	require.NoError(t, err)
	reportY, err := anypb.New(digestReport(false, map[string][]byte{leafID: {0x02}}))
	require.NoError(t, err)

	flow := &claimTransferCoordinatorFlow{
		ClaimTransferFlowHandler: NewClaimTransferFlowHandler(&so.Config{
			SigningOperatorMap: map[string]*so.SigningOperator{
				"0001": nil,
				"0002": nil,
			},
		}),
		parsed: parsedClaimTransferRequest{transferID: digestTestTransferID},
	}

	_, err = flow.BuildCommitPayload(t.Context(), map[string]*anypb.Any{
		"0001": reportX,
		"0002": reportY,
	})
	require.ErrorContains(t, err, "tweak digest mismatch")
}

func TestParseClaimPrepareResults(t *testing.T) {
	round2 := &pbinternal.FrostRound2Response{
		Results: map[string]*pbcommon.SigningResult{
			"job-1": {SignatureShare: []byte{0xAA}},
		},
	}
	wrappedAny, err := anypb.New(&pbinternal.ClaimTransferPrepareResponse{
		Round2: round2,
		LeafTweakDigests: []*pbinternal.ClaimLeafTweakDigest{
			{LeafId: "leaf-1", ProofsHash: []byte{0x01}},
		},
	})
	require.NoError(t, err)
	legacyAny, err := anypb.New(round2)
	require.NoError(t, err)
	nonSignerAny, err := anypb.New(&pbinternal.ClaimTransferPrepareResponse{
		LeafTweakDigests: []*pbinternal.ClaimLeafTweakDigest{
			{LeafId: "leaf-1", ProofsHash: []byte{0x01}},
		},
	})
	require.NoError(t, err)

	shares, reports, err := parseClaimPrepareResults(map[string]*anypb.Any{
		"0001": wrappedAny,   // new binary, in signing set
		"0002": legacyAny,    // old binary, in signing set
		"0003": nonSignerAny, // new binary, NOT in signing set — still reports digests
		"0004": nil,          // old binary, not in signing set
	})
	require.NoError(t, err)

	require.Contains(t, shares, "job-1")
	assert.Equal(t, []byte{0xAA}, shares["job-1"]["0001"], "wrapped round-2 shares must be collected")
	assert.Equal(t, []byte{0xAA}, shares["job-1"]["0002"], "legacy bare FrostRound2Response shares must be collected")
	assert.NotContains(t, shares["job-1"], "0003", "non-signer contributes no shares")

	require.Contains(t, reports, "0001")
	require.Contains(t, reports, "0003", "a reporting non-signer's digests must be visible to the unanimity check")
	assert.NotContains(t, reports, "0002", "old binaries do not report")
	assert.NotContains(t, reports, "0004")
}
