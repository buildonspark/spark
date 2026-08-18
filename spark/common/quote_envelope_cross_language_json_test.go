package common

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type quoteEnvelopeCaseFile struct {
	Description         string                      `json:"description"`
	Reasons             map[string]uint64           `json:"reasons"`
	Roles               map[string]uint64           `json:"roles"`
	TestCases           []quoteEnvelopeCase         `json:"testCases"`
	InvalidCases        []quoteEnvelopeInvalidCase  `json:"invalidCases"`
	DistinctDigestPairs []quoteEnvelopeDistinctPair `json:"distinctDigestPairs"`
	TargetCases         []quoteEnvelopeTargetCase   `json:"targetCases"`
}

type quoteEnvelopeTargetCase struct {
	Name              string   `json:"name"`
	Description       string   `json:"description"`
	Call              string   `json:"call"`
	Ports             []string `json:"ports"`
	PaymentHashHex    string   `json:"paymentHash"`
	ExpectedDigestHex string   `json:"expectedDigest"`
}

type quoteEnvelopeCase struct {
	Name              string `json:"name"`
	Description       string `json:"description"`
	Network           uint64 `json:"network"`
	ManifestHashHex   string `json:"manifestHash"`
	Reason            uint64 `json:"reason"`
	Role              uint64 `json:"role"`
	PaymentHashHex    string `json:"paymentHash"`
	TargetHex         string `json:"target"`
	ExpectedDigestHex string `json:"expectedDigest"`
}

type quoteEnvelopeInvalidCase struct {
	Name            string `json:"name"`
	Description     string `json:"description"`
	Call            string `json:"call"`
	Network         uint64 `json:"network"`
	ManifestHashHex string `json:"manifestHash"`
	Reason          uint64 `json:"reason"`
	Role            uint64 `json:"role"`
	PaymentHashHex  string `json:"paymentHash"`
	TargetHex       string `json:"target"`
	ExpectedError   string `json:"expectedError"`
}

// A pair of cases differing in exactly one enum component. Equal digests would mean that
// component separates no domains at all.
type quoteEnvelopeDistinctPair struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	CaseA       string `json:"a"`
	CaseB       string `json:"b"`
}

func decodeQuoteEnvelopeHex(t *testing.T, field string, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatalf("decode %s %q: %v", field, value, err)
	}
	return decoded
}

func requireRefusal(t *testing.T, tc quoteEnvelopeInvalidCase, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s to be refused", tc.Name)
	}
	if tc.ExpectedError == "" {
		t.Fatalf("%s has no expectedError in the fixture", tc.Name)
	}
	if !strings.Contains(err.Error(), tc.ExpectedError) {
		t.Fatalf("%s: expected=%q got=%q", tc.Name, tc.ExpectedError, err.Error())
	}
}

func loadQuoteEnvelopeCases(t *testing.T) quoteEnvelopeCaseFile {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(wd, "..", "testdata", "quote_envelope_cases.json"))
	if err != nil {
		t.Fatalf("read json cases: %v", err)
	}
	var file quoteEnvelopeCaseFile
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatalf("unmarshal json: %v", err)
	}
	return file
}

func TestQuoteEnvelopeJSONCases(t *testing.T) {
	file := loadQuoteEnvelopeCases(t)

	t.Run("enum_numbering", func(t *testing.T) {
		expectedReasons := map[string]uint64{
			"RECEIVE":        uint64(QuoteReasonReceive),
			"SEND":           uint64(QuoteReasonSend),
			"COOP_EXIT":      uint64(QuoteReasonCoopExit),
			"STATIC_DEPOSIT": uint64(QuoteReasonStaticDeposit),
		}
		if len(file.Reasons) != len(expectedReasons) {
			t.Fatalf("fixture names %d reasons, Go defines %d", len(file.Reasons), len(expectedReasons))
		}
		for name, value := range expectedReasons {
			if file.Reasons[name] != value {
				t.Fatalf("reason %s: fixture=%d Go=%d", name, file.Reasons[name], value)
			}
		}

		expectedRoles := map[string]uint64{
			"ISSUER":   uint64(QuoteRoleIssuer),
			"ATTESTOR": uint64(QuoteRoleAttestor),
		}
		if len(file.Roles) != len(expectedRoles) {
			t.Fatalf("fixture names %d roles, Go defines %d", len(file.Roles), len(expectedRoles))
		}
		for name, value := range expectedRoles {
			if file.Roles[name] != value {
				t.Fatalf("role %s: fixture=%d Go=%d", name, file.Roles[name], value)
			}
		}
	})

	if len(file.TestCases) == 0 || len(file.InvalidCases) == 0 || len(file.DistinctDigestPairs) == 0 {
		t.Fatalf("fixture asserts nothing: %d cases, %d invalid, %d pairs",
			len(file.TestCases), len(file.InvalidCases), len(file.DistinctDigestPairs))
	}

	digestsByCase := make(map[string][]byte, len(file.TestCases))

	for _, tc := range file.TestCases {
		t.Run(tc.Name, func(t *testing.T) {
			manifestHash := decodeQuoteEnvelopeHex(t, "manifestHash", tc.ManifestHashHex)

			target := decodeQuoteEnvelopeHex(t, "target", tc.TargetHex)
			if tc.PaymentHashHex != "" {
				paymentHash := decodeQuoteEnvelopeHex(t, "paymentHash", tc.PaymentHashHex)
				derivedTarget, err := ReceiveAttestorTarget(paymentHash)
				if err != nil {
					t.Fatalf("receive attestor target: %v", err)
				}
				if !bytes.Equal(derivedTarget, target) {
					t.Fatalf("target mismatch: expected=%s got=%x", tc.TargetHex, derivedTarget)
				}
			}

			digest, err := quoteEnvelopeDigest(tc.Network, manifestHash, QuoteReason(tc.Reason), QuoteRole(tc.Role), target)
			if err != nil {
				t.Fatalf("quote envelope digest: %v", err)
			}
			digestHex := hex.EncodeToString(digest)

			if !strings.EqualFold(tc.ExpectedDigestHex, digestHex) {
				t.Fatalf("digest mismatch: expected=%s got=%s", tc.ExpectedDigestHex, digestHex)
			}
			digestsByCase[tc.Name] = digest
		})
	}

	for _, pair := range file.DistinctDigestPairs {
		t.Run("distinct_"+pair.Name, func(t *testing.T) {
			digestA, hasA := digestsByCase[pair.CaseA]
			digestB, hasB := digestsByCase[pair.CaseB]
			if !hasA || !hasB {
				t.Fatalf("distinctDigestPairs names a case with no pinned digest: %q / %q", pair.CaseA, pair.CaseB)
			}
			if bytes.Equal(digestA, digestB) {
				t.Fatalf("%s and %s hash identically: %x", pair.CaseA, pair.CaseB, digestA)
			}
		})
	}

	for _, tc := range file.InvalidCases {
		t.Run("invalid_"+tc.Name, func(t *testing.T) {
			switch tc.Call {
			case "receiveAttestationTarget":
				paymentHash := decodeQuoteEnvelopeHex(t, "paymentHash", tc.PaymentHashHex)
				_, err := ReceiveAttestorTarget(paymentHash)
				requireRefusal(t, tc, err)
			case "quoteEnvelopeDigest":
				manifestHash := decodeQuoteEnvelopeHex(t, "manifestHash", tc.ManifestHashHex)
				target := decodeQuoteEnvelopeHex(t, "target", tc.TargetHex)
				_, err := quoteEnvelopeDigest(tc.Network, manifestHash, QuoteReason(tc.Reason), QuoteRole(tc.Role), target)
				requireRefusal(t, tc, err)
			default:
				t.Fatalf("case names an unknown call %q", tc.Call)
			}
		})
	}
}

// The fixture pins four target derivations; the operators verify only the attestor's, so this port
// implements only that one. Asserting the `ports` list rather than skipping unknown calls is what
// stops a target added to Go later from going unasserted.
func TestQuoteEnvelopeTargetCases(t *testing.T) {
	file := loadQuoteEnvelopeCases(t)
	if len(file.TargetCases) == 0 {
		t.Fatal("fixture carries no target cases")
	}

	asserted := 0
	for _, testCase := range file.TargetCases {
		if !slices.Contains(testCase.Ports, "go") {
			continue
		}
		t.Run(testCase.Name, func(t *testing.T) {
			switch testCase.Call {
			case "receiveAttestorTarget":
				target, err := ReceiveAttestorTarget(decodeQuoteEnvelopeHex(t, "paymentHash", testCase.PaymentHashHex))
				if err != nil {
					t.Fatalf("%s: %v", testCase.Name, err)
				}
				if digest := hex.EncodeToString(target); digest != testCase.ExpectedDigestHex {
					t.Fatalf("%s: digest %s, expected %s", testCase.Name, digest, testCase.ExpectedDigestHex)
				}
			default:
				t.Fatalf("case lists the go port but this port implements no %s", testCase.Call)
			}
		})
		asserted++
	}
	if asserted == 0 {
		t.Fatal("no target case names the go port")
	}
}
