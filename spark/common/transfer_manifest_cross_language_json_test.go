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
)

type transferManifestHashFile struct {
	Description  string                        `json:"description"`
	TestCases    []transferManifestHashCase    `json:"testCases"`
	InvalidCases []transferManifestInvalidCase `json:"invalidCases"`
}

type transferManifestInvalidCase struct {
	Name             string          `json:"name"`
	Description      string          `json:"description"`
	TransferManifest json.RawMessage `json:"transferManifest"`
}

type transferManifestHashCase struct {
	Name             string          `json:"name"`
	Description      string          `json:"description"`
	ExpectedHashHex  string          `json:"expectedHash"`
	TransferManifest json.RawMessage `json:"transferManifest"`
}

func TestTransferManifestHashJSONCases(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	jsonPath := filepath.Join(wd, "..", "testdata", "transfer_manifest_hash_cases.json")

	data, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatalf("read json cases: %v", err)
	}

	var file transferManifestHashFile
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatalf("unmarshal json: %v", err)
	}

	for _, tc := range file.TestCases {
		t.Run(tc.Name, func(t *testing.T) {
			var msg pb.TransferManifest
			if err := protojson.Unmarshal(tc.TransferManifest, &msg); err != nil {
				t.Fatalf("protojson unmarshal TransferManifest: %v", err)
			}

			// A wire rule stricter than these agreed-buildable shapes would make a
			// legitimate manifest unsendable while the hashing assertions stayed green.
			if err := msg.Validate(); err != nil {
				t.Fatalf("proto validation rejected a valid fixture: %v", err)
			}

			got, err := HashTransferManifest(&msg)
			if err != nil {
				t.Fatalf("hash transfer manifest: %v", err)
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

	for _, tc := range file.InvalidCases {
		t.Run("invalid_"+tc.Name, func(t *testing.T) {
			var msg pb.TransferManifest
			if err := protojson.Unmarshal(tc.TransferManifest, &msg); err != nil {
				t.Fatalf("protojson unmarshal TransferManifest: %v", err)
			}

			if _, err := HashTransferManifest(&msg); err == nil {
				t.Fatalf("expected validation to reject %s, but it hashed successfully", tc.Name)
			}
		})
	}
}
