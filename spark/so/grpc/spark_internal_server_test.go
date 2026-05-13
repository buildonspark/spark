package grpc

import (
	"testing"

	pb "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/stretchr/testify/require"
)

func TestSparkInternalServerMarkKeysharesAsUsedRejectsEmptyRequest(t *testing.T) {
	server := NewSparkInternalServer(nil)

	resp, err := server.MarkKeysharesAsUsed(t.Context(), &pb.MarkKeysharesAsUsedRequest{})

	require.Nil(t, resp)
	require.ErrorContains(t, err, "keyshare ids must not be empty")
}
