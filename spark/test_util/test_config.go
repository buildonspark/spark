package test_util

import (
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/lightsparkdev/spark-go/so"
)

func findLatestRun(dirPath string) (int, error) {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return 0, fmt.Errorf("failed to read directory: %w", err)
	}

	maxNum := -1
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		name := entry.Name()
		// Check if name matches pattern "run_x"
		if !strings.HasPrefix(name, "run_") {
			continue
		}

		// Extract number after "run_"
		numStr := strings.TrimPrefix(name, "run_")
		num, err := strconv.Atoi(numStr)
		if err != nil {
			// Skip if number parsing fails
			continue
		}

		if num > maxNum {
			maxNum = num
		}
	}

	if maxNum == -1 {
		return 0, fmt.Errorf("no run_x folders found")
	}

	return maxNum, nil
}

func TestConfig() (*so.Config, error) {
	identityPrivateKeyBytes, err := hex.DecodeString("5eaae81bcf1fd43fbb92432b82dbafc8273bb3287b42cb4cf3c851fcee2212a5")
	if err != nil {
		return nil, err
	}
	pubkeys := []string{
		"0322ca18fc489ae25418a0e768273c2c61cabb823edfb14feb891e9bec62016510",
		"0341727a6c41b168f07eb50865ab8c397a53c7eef628ac1020956b705e43b6cb27",
		"0305ab8d485cc752394de4981f8a5ae004f2becfea6f432c9a59d5022d8764f0a6",
		"0352aef4d49439dedd798ac4aef1e7ebef95f569545b647a25338398c1247ffdea",
		"02c05c88cc8fc181b1ba30006df6a4b0597de6490e24514fbdd0266d2b9cd3d0ba",
	}

	pubkeyBytesArray := make([][]byte, len(pubkeys))
	for i, pubkey := range pubkeys {
		pubkeyBytes, err := hex.DecodeString(pubkey)
		if err != nil {
			return nil, err
		}
		pubkeyBytesArray[i] = pubkeyBytes
	}

	latestRun, err := findLatestRun("../../../_data")
	if err != nil {
		return nil, err
	}

	config := so.Config{
		Identifier:         "0000000000000000000000000000000000000000000000000000000000000001",
		IdentityPrivateKey: identityPrivateKeyBytes,
		SigningOperatorMap: map[string]*so.SigningOperator{
			"0000000000000000000000000000000000000000000000000000000000000001": &so.SigningOperator{
				Identifier:        "0000000000000000000000000000000000000000000000000000000000000001",
				Address:           "localhost:8535",
				IdentityPublicKey: pubkeyBytesArray[0],
			},
			"0000000000000000000000000000000000000000000000000000000000000002": &so.SigningOperator{
				Identifier:        "0000000000000000000000000000000000000000000000000000000000000002",
				Address:           "localhost:8536",
				IdentityPublicKey: pubkeyBytesArray[1],
			},
			"0000000000000000000000000000000000000000000000000000000000000003": &so.SigningOperator{
				Identifier:        "0000000000000000000000000000000000000000000000000000000000000003",
				Address:           "localhost:8537",
				IdentityPublicKey: pubkeyBytesArray[2],
			},
			"0000000000000000000000000000000000000000000000000000000000000004": &so.SigningOperator{
				Identifier:        "0000000000000000000000000000000000000000000000000000000000000004",
				Address:           "localhost:8538",
				IdentityPublicKey: pubkeyBytesArray[3],
			},
			"0000000000000000000000000000000000000000000000000000000000000005": &so.SigningOperator{
				Identifier:        "0000000000000000000000000000000000000000000000000000000000000005",
				Address:           "localhost:8539",
				IdentityPublicKey: pubkeyBytesArray[4],
			},
		},
		Threshold:     3,
		SignerAddress: "unix:///tmp/frost_0.sock",
		DatabasePath:  fmt.Sprintf("../../../_data/run_%d/db/operator_0.sqlite", latestRun),
	}
	return &config, nil
}
