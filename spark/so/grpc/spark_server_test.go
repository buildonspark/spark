package grpc

import (
	"testing"

	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func grpcErrorReason(t *testing.T, err error) string {
	t.Helper()
	for _, d := range status.Convert(err).Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok {
			return info.GetReason()
		}
	}
	return ""
}

// The reason is the contract, not just the code: it is what lets a client tell a method that is
// coming from one that is gone, and the endpoint is published as a target to generate against.
func TestSparkServerInitiatePreimageSwapV4IsFeatureIncomplete(t *testing.T) {
	server := NewSparkServer(nil, nil)
	sender := keys.GeneratePrivateKey().Public()
	receiver := keys.GeneratePrivateKey().Public()
	const transferID = "11111111-1111-1111-1111-111111111111"

	req := &pb.InitiatePreimageSwapV4Request{
		PaymentHash:                   make([]byte, 32),
		CounterpartyIdentityPublicKey: receiver.Serialize(),
		TransferV3Request: &pb.StartTransferV3Request{
			TransferId: transferID,
			SenderPackages: []*pb.SenderTransferPackage{{
				OwnerIdentityPublicKey:     sender.Serialize(),
				TransferPackage:            &pb.TransferPackage{},
				ReceiverIdentityPublicKeys: map[string][]byte{"leaf": receiver.Serialize()},
			}},
		},
	}
	require.NoError(t, req.Validate(), "the request must clear the interceptor to reach the method")

	resp, err := server.InitiatePreimageSwapV4(t.Context(), req)

	require.Nil(t, resp)
	require.Error(t, err)
	assert.Equal(t, codes.Unimplemented, status.Code(err))
	assert.Equal(t, sparkerrors.ReasonUnimplementedFeatureIncomplete, grpcErrorReason(t, err))
}

// The retired versions answer METHOD_DISABLED, so a client can distinguish them from v4.
// InitiatePreimageSwapRequest carries no validate.rules, so an empty request does reach them.
func TestSparkServerRetiredPreimageSwapVersionsAreMethodDisabled(t *testing.T) {
	server := NewSparkServer(nil, nil)

	for name, call := range map[string]func() (*pb.InitiatePreimageSwapResponse, error){
		"v1": func() (*pb.InitiatePreimageSwapResponse, error) {
			return server.InitiatePreimageSwap(t.Context(), &pb.InitiatePreimageSwapRequest{})
		},
		"v2": func() (*pb.InitiatePreimageSwapResponse, error) {
			return server.InitiatePreimageSwapV2(t.Context(), &pb.InitiatePreimageSwapRequest{})
		},
	} {
		t.Run(name, func(t *testing.T) {
			resp, err := call()

			require.Nil(t, resp)
			require.Error(t, err)
			assert.Equal(t, codes.Unimplemented, status.Code(err))
			assert.NotEqual(t, sparkerrors.ReasonUnimplementedFeatureIncomplete, grpcErrorReason(t, err))
		})
	}
}
