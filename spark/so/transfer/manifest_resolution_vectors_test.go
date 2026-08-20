package transfer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/proto/spark"
	"github.com/stretchr/testify/require"
)

type resolutionAmount struct {
	Sats *uint64 `json:"sats"`
	Bps  *uint32 `json:"bps"`
}

type resolutionCase struct {
	Name          string             `json:"name"`
	Gross         uint64             `json:"gross"`
	Edges         []resolutionAmount `json:"edges"`
	Resolved      []uint64           `json:"resolved"`
	Allowed       []uint64           `json:"allowed"`
	ExpectedError string             `json:"expectedError"`
}

type resolutionCases struct {
	TestCases    []resolutionCase `json:"testCases"`
	InvalidCases []resolutionCase `json:"invalidCases"`
}

func loadResolutionCases(t *testing.T) resolutionCases {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)
	raw, err := os.ReadFile(filepath.Join(wd, "..", "..", "testdata", "transfer_manifest_resolution_cases.json"))
	require.NoError(t, err)
	var cases resolutionCases
	require.NoError(t, json.Unmarshal(raw, &cases))
	require.NotEmpty(t, cases.TestCases)
	require.NotEmpty(t, cases.InvalidCases)
	return cases
}

// Distinct receivers per edge, so each resolves under its own key.
func edgesFrom(t *testing.T, amounts []resolutionAmount) ([]*spark.ManifestEdge, []keys.Public) {
	t.Helper()
	sender := keys.GeneratePrivateKey().Public()
	edges := make([]*spark.ManifestEdge, 0, len(amounts))
	receivers := make([]keys.Public, 0, len(amounts))
	for _, amount := range amounts {
		receiver := keys.GeneratePrivateKey().Public()
		receivers = append(receivers, receiver)
		built := &spark.ManifestEdge{
			SenderIdentityPublicKey:   sender.Serialize(),
			ReceiverIdentityPublicKey: receiver.Serialize(),
		}
		if (amount.Sats != nil) == (amount.Bps != nil) {
			t.Fatalf("vector edge must declare exactly one of sats or bps")
		}
		if amount.Sats != nil {
			built.Amount = satsOf(*amount.Sats)
		} else {
			built.Amount = bpsOf(*amount.Bps)
		}
		edges = append(edges, built)
	}
	return edges, receivers
}

func TestDeclaredEdgeTotalsMatchesSharedResolutionCases(t *testing.T) {
	for _, testCase := range loadResolutionCases(t).TestCases {
		t.Run(testCase.Name, func(t *testing.T) {
			require.NotEmpty(t, testCase.Edges, "vector declares no edges")
			require.Len(t, testCase.Resolved, len(testCase.Edges), "vector states a resolved amount per edge")
			require.Len(t, testCase.Allowed, len(testCase.Edges), "vector states an allowed bound per edge")
			edges, receivers := edgesFrom(t, testCase.Edges)
			parsed, err := declaredEdgeKeys(edges)
			require.NoError(t, err)
			totals, err := declaredEdgeTotals(edges, parsed, testCase.Gross)
			require.NoError(t, err)
			require.Len(t, totals, len(receivers))

			sender, err := keys.ParsePublicKey(edges[0].GetSenderIdentityPublicKey())
			require.NoError(t, err)
			for i, receiver := range receivers {
				resolved, ok := totals[manifestEdgeKey{sender: sender, receiver: receiver}]
				require.True(t, ok, "edges[%d] resolved to no entry", i)
				require.Equal(t, testCase.Resolved[i], resolved.sats, "edges[%d] resolved", i)
				require.Equal(t, testCase.Allowed[i], resolved.allowed, "edges[%d] allowed", i)
			}
		})
	}
}

func TestDeclaredEdgeTotalsRefusesSharedInvalidCases(t *testing.T) {
	for _, testCase := range loadResolutionCases(t).InvalidCases {
		t.Run(testCase.Name, func(t *testing.T) {
			require.NotEmpty(t, testCase.ExpectedError, "vector states the refusal it expects")
			edges, _ := edgesFrom(t, testCase.Edges)
			parsed, err := declaredEdgeKeys(edges)
			require.NoError(t, err)
			_, err = declaredEdgeTotals(edges, parsed, testCase.Gross)
			require.ErrorContains(t, err, testCase.ExpectedError)
		})
	}
}

// The vector file is the contract's source of truth, so a malformed entry must fail loudly rather
// than silently reduce what the suite covers.
func TestResolutionVectorGuardsRejectMalformedCases(t *testing.T) {
	sats := uint64(100)
	bps := uint32(6000)
	for name, amounts := range map[string][]resolutionAmount{
		"edge declaring both denominations": {{Sats: &sats, Bps: &bps}},
		"edge declaring neither":            {{}},
	} {
		t.Run(name, func(t *testing.T) {
			fake := &testing.T{}
			done := make(chan struct{})
			go func() {
				defer close(done)
				edgesFrom(fake, amounts)
			}()
			<-done
			require.True(t, fake.Failed(), "a malformed vector edge must fail its case")
		})
	}
}
