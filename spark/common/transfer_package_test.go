package common

import (
	"crypto/sha256"
	"testing"

	"github.com/google/uuid"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/stretchr/testify/require"
)

func TestGetTransferPackageSigningPayload(t *testing.T) {
	transferID := uuid.New()

	mapToSign := map[string][]byte{
		"0000000000000000000000000000000000000000000000000000000000000002": {0x02},
		"0000000000000000000000000000000000000000000000000000000000000001": {0x01},
		"0000000000000000000000000000000000000000000000000000000000000003": {0x03},
	}
	transferPackage := &pb.TransferPackage{
		KeyTweakPackage: mapToSign,
	}

	payload := GetTransferPackageSigningPayload(transferID, transferPackage)

	hasher := sha256.New()
	hasher.Write(transferID[:])
	hasher.Write([]byte("0000000000000000000000000000000000000000000000000000000000000001:"))
	hasher.Write([]byte{0x01})
	hasher.Write([]byte(";"))
	hasher.Write([]byte("0000000000000000000000000000000000000000000000000000000000000002:"))
	hasher.Write([]byte{0x02})
	hasher.Write([]byte(";"))
	hasher.Write([]byte("0000000000000000000000000000000000000000000000000000000000000003:"))
	hasher.Write([]byte{0x03})
	hasher.Write([]byte(";"))
	require.Equal(t, hasher.Sum(nil), payload)
}
