package common

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"google.golang.org/protobuf/encoding/protojson"
)

type tokenInvoiceSigningHashFile struct {
	Description string                        `json:"description"`
	TestCases   []tokenInvoiceSigningHashCase `json:"testCases"`
}

type tokenInvoiceSigningHashCase struct {
	Name               string          `json:"name"`
	Description        string          `json:"description"`
	Network            string          `json:"network"`
	ReceiverPublicKey  string          `json:"receiverPublicKey"`
	ExpectedHashHex    string          `json:"expectedHash"`
	SparkInvoiceFields json.RawMessage `json:"sparkInvoiceFields"`
}

func TestTokenInvoiceSigningHashJSONCases(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	jsonPath := filepath.Join(wd, "..", "testdata", "token_invoice_signing_hash_cases.json")

	data, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatalf("read json cases: %v", err)
	}

	var file tokenInvoiceSigningHashFile
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatalf("unmarshal json: %v", err)
	}

	for _, tc := range file.TestCases {
		t.Run(tc.Name, func(t *testing.T) {
			var msg pb.SparkInvoiceFields
			if err := protojson.Unmarshal(tc.SparkInvoiceFields, &msg); err != nil {
				t.Fatalf("protojson unmarshal SparkInvoiceFields: %v", err)
			}

			receiverPublicKeyBytes, err := base64.StdEncoding.DecodeString(tc.ReceiverPublicKey)
			if err != nil {
				t.Fatalf("decode receiver public key: %v", err)
			}
			receiverPublicKey, err := keys.ParsePublicKey(receiverPublicKeyBytes)
			if err != nil {
				t.Fatalf("parse receiver public key: %v", err)
			}

			network, err := btcnetwork.FromString(tc.Network)
			if err != nil {
				t.Fatalf("parse network: %v", err)
			}

			got, err := HashSparkInvoiceFields(&msg, network, receiverPublicKey)
			if err != nil {
				t.Fatalf("hash token invoice signing payload: %v", err)
			}

			gotHex := hex.EncodeToString(got)
			if !strings.EqualFold(tc.ExpectedHashHex, gotHex) {
				t.Fatalf("hash mismatch: expected=%s got=%s", tc.ExpectedHashHex, gotHex)
			}
		})
	}
}
