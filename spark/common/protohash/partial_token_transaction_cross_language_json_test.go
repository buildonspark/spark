package protohash

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"google.golang.org/protobuf/encoding/protojson"
)

type partialTokenTxCrossLangFile struct {
	Description string                            `json:"description"`
	TestCases   []partialTokenTxCrossLangTestCase `json:"testCases"`
}

type partialTokenTxCrossLangTestCase struct {
	Name                    string          `json:"name"`
	Description             string          `json:"description"`
	ExpectedHashHex         string          `json:"expectedHash"`
	PartialTokenTransaction json.RawMessage `json:"partialTokenTransaction"`
}

func TestPartialTokenTransactionJSONCases(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	jsonPath := filepath.Join(wd, "..", "..", "testdata", "partial_token_transaction_hash_cases.json")

	data, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatalf("read json cases: %v", err)
	}

	var file partialTokenTxCrossLangFile
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatalf("unmarshal json: %v", err)
	}

	for _, tc := range file.TestCases {
		t.Run(tc.Name, func(t *testing.T) {
			var msg tokenpb.PartialTokenTransaction
			if err := protojson.Unmarshal(tc.PartialTokenTransaction, &msg); err != nil {
				t.Fatalf("protojson unmarshal PartialTokenTransaction: %v", err)
			}

			got, err := Hash(&msg)
			if err != nil {
				t.Fatalf("hash PartialTokenTransaction: %v", err)
			}
			gotHex := hex.EncodeToString(got)

			if tc.ExpectedHashHex == "" {
				t.Logf("COMPUTED_PARTIAL_CASE %s: hash=%s", tc.Name, gotHex)
				return
			}

			if !strings.EqualFold(tc.ExpectedHashHex, gotHex) {
				t.Fatalf("hash mismatch: expected=%s got=%s", tc.ExpectedHashHex, gotHex)
			}
		})
	}
}
