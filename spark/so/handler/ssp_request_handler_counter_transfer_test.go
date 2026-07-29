//go:build lightspark

package handler

import (
	"testing"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbssp "github.com/lightsparkdev/spark/proto/spark_ssp_internal"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/require"
)

func TestInitiateCounterTransferRejectsMalformedRequestWithoutPanic(t *testing.T) {
	handler := NewSspRequestHandler(sparktesting.TestConfig(t))

	validAdaptorKeys := &pb.AdaptorPublicKeyPackage{
		AdaptorPublicKey: keys.GeneratePrivateKey().Public().Serialize(),
	}
	validTransfer := &pb.StartTransferRequest{
		TransferId:      uuid.NewString(),
		TransferPackage: &pb.TransferPackage{},
	}
	primaryTransferID := uuid.NewString()

	tests := []struct {
		name        string
		req         *pbssp.CounterTransferRequest
		expectedErr string
	}{
		{
			name:        "nil request",
			req:         nil,
			expectedErr: "request is required",
		},
		{
			name:        "missing transfer",
			req:         &pbssp.CounterTransferRequest{},
			expectedErr: "transfer is required",
		},
		{
			name: "missing adaptor keys",
			req: &pbssp.CounterTransferRequest{
				Transfer:          validTransfer,
				PrimaryTransferId: primaryTransferID,
			},
			expectedErr: "adaptor_public_keys is required",
		},
		{
			name: "missing transfer package",
			req: &pbssp.CounterTransferRequest{
				Transfer: &pb.StartTransferRequest{
					TransferId: uuid.NewString(),
				},
				AdaptorPublicKeys: validAdaptorKeys,
				PrimaryTransferId: primaryTransferID,
			},
			expectedErr: "transfer_package is required",
		},
		{
			name: "direct leaves provided",
			req: &pbssp.CounterTransferRequest{
				Transfer: &pb.StartTransferRequest{
					TransferId: uuid.NewString(),
					TransferPackage: &pb.TransferPackage{
						LeavesToSend: []*pb.UserSignedTxSigningJob{
							{LeafId: uuid.NewString()},
						},
						DirectLeavesToSend: []*pb.UserSignedTxSigningJob{
							{LeafId: uuid.NewString()},
						},
					},
				},
				AdaptorPublicKeys: validAdaptorKeys,
				PrimaryTransferId: primaryTransferID,
			},
			expectedErr: "direct transactions should not be provided",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var err error
			require.NotPanics(t, func() {
				_, err = handler.InitiateCounterTransfer(t.Context(), tt.req)
			})
			require.Error(t, err)
			require.ErrorContains(t, err, tt.expectedErr)
		})
	}
}
