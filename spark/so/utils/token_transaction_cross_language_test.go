package utils

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

type tokenTxCrossLangFile struct {
	Description string                     `json:"description"`
	TestCases   []tokenTxCrossLangTestCase `json:"testCases"`
}

type tokenTxCrossLangTestCase struct {
	Name             string          `json:"name"`
	Description      string          `json:"description"`
	ExpectedHashHex  string          `json:"expectedHash"`
	TokenTransaction json.RawMessage `json:"tokenTransaction"`
}

func TestTokenTransactionV3CrossLanguageJSONCases(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	jsonPath := filepath.Join(wd, "..", "..", "testdata", "token_transaction_v3_hash_cases.json")

	data, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatalf("read json cases: %v", err)
	}

	var file tokenTxCrossLangFile
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatalf("unmarshal json: %v", err)
	}

	for _, tc := range file.TestCases {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			var msg tokenpb.TokenTransaction
			if err := protojson.Unmarshal(tc.TokenTransaction, &msg); err != nil {
				t.Fatalf("protojson unmarshal TokenTransaction: %v", err)
			}

			isPartial := strings.Contains(tc.Name, "partial")
			got, err := HashTokenTransactionV3(&msg, isPartial)
			if err != nil {
				t.Fatalf("hash TokenTransaction: %v", err)
			}

			gotHex := hex.EncodeToString(got)

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
