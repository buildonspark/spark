package protohash

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pb "github.com/lightsparkdev/spark/proto/spark"
	"google.golang.org/protobuf/encoding/protojson"
)

type crossLangFile struct {
	Description string              `json:"description"`
	TestCases   []crossLangTestCase `json:"testCases"`
}

type crossLangTestCase struct {
	Name               string          `json:"name"`
	Description        string          `json:"description"`
	ExpectedHashHex    string          `json:"expectedHash"`
	SparkInvoiceFields json.RawMessage `json:"sparkInvoiceFields"`
}

func TestSparkInvoiceFieldsJSONCases(t *testing.T) {
	// Resolve path to testdata JSON from the protohash package directory.
	// Layout: spark/spark/common/protohash/... and JSON at spark/spark/testdata/...
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	jsonPath := filepath.Join(wd, "..", "..", "testdata", "cross_language_hash_cases_proto.json")

	data, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatalf("read json cases: %v", err)
	}

	var file crossLangFile
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatalf("unmarshal json: %v", err)
	}

	for _, tc := range file.TestCases {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			var msg pb.SparkInvoiceFields
			if err := protojson.Unmarshal(tc.SparkInvoiceFields, &msg); err != nil {
				t.Fatalf("protojson unmarshal SparkInvoiceFields: %v", err)
			}

			got, err := Hash(&msg)
			if err != nil {
				t.Fatalf("hash SparkInvoiceFields: %v", err)
			}

			gotHex := hex.EncodeToString(got)

			// If expected is missing or TBD, print computed to help update fixtures
			if tc.ExpectedHashHex == "" || strings.EqualFold(tc.ExpectedHashHex, "TBD") {
				t.Logf("COMPUTED_HASH %s: %s", tc.Name, gotHex)
				return
			}

			if !strings.EqualFold(tc.ExpectedHashHex, gotHex) {
				t.Fatalf("hash mismatch: expected=%s got=%s", tc.ExpectedHashHex, gotHex)
			}
		})
	}
}
