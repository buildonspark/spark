package grpc

import (
	"testing"

	"github.com/lightsparkdev/spark/common/keys"
	pbspark "github.com/lightsparkdev/spark/proto/spark"
	pb "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/stretchr/testify/require"
)

func TestSparkInternalServerMarkKeysharesAsUsedRejectsEmptyRequest(t *testing.T) {
	server := NewSparkInternalServer(nil)

	resp, err := server.MarkKeysharesAsUsed(t.Context(), &pb.MarkKeysharesAsUsedRequest{})

	require.Nil(t, resp)
	require.ErrorContains(t, err, "keyshare ids must not be empty")
}

func TestSparkInternalServerInitiatePreimageSwapRejectsNilRequests(t *testing.T) {
	server := NewSparkInternalServer(nil)

	tests := []struct {
		name        string
		call        func() error
		expectedErr string
	}{
		{
			name: "legacy nil request",
			call: func() error {
				_, err := server.InitiatePreimageSwap(t.Context(), nil)
				return err
			},
			expectedErr: "request is required",
		},
		{
			name: "v2 nil wrapper",
			call: func() error {
				_, err := server.InitiatePreimageSwapV2(t.Context(), nil)
				return err
			},
			expectedErr: "request is required",
		},
		{
			name: "v2 nil inner request",
			call: func() error {
				_, err := server.InitiatePreimageSwapV2(t.Context(), &pb.InitiatePreimageSwapRequest{})
				return err
			},
			expectedErr: "request is required",
		},
		{
			name: "v2 nil inner transfer",
			call: func() error {
				receiverIdentityPubKey := keys.GeneratePrivateKey().Public()
				_, err := server.InitiatePreimageSwapV2(t.Context(), &pb.InitiatePreimageSwapRequest{
					Request: &pbspark.InitiatePreimageSwapRequest{
						ReceiverIdentityPublicKey: receiverIdentityPubKey.Serialize(),
					},
				})
				return err
			},
			expectedErr: "transfer_request is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NotPanics(t, func() {
				require.ErrorContains(t, tt.call(), tt.expectedErr)
			})
		})
	}
}
