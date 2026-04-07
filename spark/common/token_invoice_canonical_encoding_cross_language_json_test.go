package common

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pb "github.com/lightsparkdev/spark/proto/spark"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type tokenInvoiceCanonicalEncodingFile struct {
	Description string                              `json:"description"`
	TestCases   []tokenInvoiceCanonicalEncodingCase `json:"testCases"`
}

type tokenInvoiceCanonicalEncodingCase struct {
	Name                      string          `json:"name"`
	ExpectedCanonicalEncoding string          `json:"expectedCanonicalEncoding"`
	SparkInvoiceFields        json.RawMessage `json:"sparkInvoiceFields"`
}

func TestTokenInvoiceCanonicalEncodingJSONCases(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	jsonPath := filepath.Join(wd, "..", "testdata", "token_invoice_canonical_encoding_cases.json")

	data, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatalf("read json cases: %v", err)
	}

	var file tokenInvoiceCanonicalEncodingFile
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatalf("unmarshal json: %v", err)
	}

	for _, tc := range file.TestCases {
		t.Run(tc.Name, func(t *testing.T) {
			var msg pb.SparkInvoiceFields
			if err := protojson.Unmarshal(tc.SparkInvoiceFields, &msg); err != nil {
				t.Fatalf("protojson unmarshal SparkInvoiceFields: %v", err)
			}

			got, err := proto.MarshalOptions{Deterministic: true}.Marshal(&msg)
			if err != nil {
				t.Fatalf("marshal SparkInvoiceFields: %v", err)
			}

			gotHex := hex.EncodeToString(got)
			if !strings.EqualFold(tc.ExpectedCanonicalEncoding, gotHex) {
				t.Fatalf(
					"canonical encoding mismatch: expected=%s got=%s",
					tc.ExpectedCanonicalEncoding,
					gotHex,
				)
			}
		})
	}
}
