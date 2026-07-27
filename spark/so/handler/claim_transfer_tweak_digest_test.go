package handler

import (
	"testing"

	"github.com/google/uuid"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/anypb"
)

// These tests target the unexported digest-unanimity helpers directly rather
// than the BuildCommitPayload boundary: the check is security-sensitive
// (divergent VSS polynomials silently corrupt a leaf's signing keyshare across
// SOs) and the enclosing boundary needs a live FROST signer plus a full
// multi-SO claim, which only the minikube integration suite can drive
// (TestClaimTransferV2_FreshPolynomialHealsPeerLockedAtRKL). The pre-apply /
// adopt-fresh behavior is covered at the FlowHandler boundary in
// claim_transfer_status_test.go.

var digestTestTransferID = uuid.MustParse("019ebba9-2a03-77e7-9fa6-05a7d3be388c")

func digestReport(applied bool, digests map[string][]byte) *pbinternal.ClaimTransferPrepareResponse {
	resp := &pbinternal.ClaimTransferPrepareResponse{TweaksAlreadyApplied: applied}
	for leafID, hash := range digests {
		resp.LeafTweakDigests = append(resp.LeafTweakDigests, &pbinternal.ClaimLeafTweakDigest{
			LeafId:     leafID,
			ProofsHash: hash,
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
		name    string
		reports map[string]*pbinternal.ClaimTransferPrepareResponse
		wantErr string
	}{
		{
			name: "unanimous digests pass",
			reports: map[string]*pbinternal.ClaimTransferPrepareResponse{
				"0001": digestReport(false, map[string][]byte{leafA: hashX, leafB: hashY}),
				"0002": digestReport(false, map[string][]byte{leafA: hashX, leafB: hashY}),
			},
		},
		{
			name: "divergent digest for one leaf fails",
			reports: map[string]*pbinternal.ClaimTransferPrepareResponse{
				"0001": digestReport(false, map[string][]byte{leafA: hashX}),
				"0002": digestReport(false, map[string][]byte{leafA: hashY}),
			},
			wantErr: "tweak digest",
		},
		{
			name: "reporter missing a leaf another reports fails",
			reports: map[string]*pbinternal.ClaimTransferPrepareResponse{
				"0001": digestReport(false, map[string][]byte{leafA: hashX, leafB: hashY}),
				"0002": digestReport(false, map[string][]byte{leafA: hashX}),
			},
			wantErr: "tweak digest",
		},
		{
			name: "all applied pass",
			reports: map[string]*pbinternal.ClaimTransferPrepareResponse{
				"0001": digestReport(true, nil),
				"0002": digestReport(true, nil),
			},
		},
		{
			name: "applied mixed with pre-apply fails",
			reports: map[string]*pbinternal.ClaimTransferPrepareResponse{
				"0001": digestReport(true, nil),
				"0002": digestReport(false, map[string][]byte{leafA: hashX}),
			},
			wantErr: "already applied",
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
			wantErr: "no leaf tweak digests",
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
			if tc.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
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
