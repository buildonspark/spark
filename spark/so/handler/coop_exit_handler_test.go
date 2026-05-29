package handler

import (
	"testing"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestCooperativeExitV2RejectsMalformedRequestShape(t *testing.T) {
	handler := NewCooperativeExitHandler(&so.Config{})
	ownerKey := keys.GeneratePrivateKey().Public().Serialize()
	receiverKey := keys.GeneratePrivateKey().Public().Serialize()

	baseTransfer := func(leaves []*pb.LeafRefundTxSigningJob) *pb.StartTransferRequest {
		return &pb.StartTransferRequest{
			OwnerIdentityPublicKey:    ownerKey,
			ReceiverIdentityPublicKey: receiverKey,
			TransferId:                uuid.New().String(),
			ExpiryTime:                timestamppb.Now(),
			LeavesToSend:              leaves,
		}
	}

	tests := []struct {
		name string
		req  *pb.CooperativeExitRequest
	}{
		{
			name: "nil request",
			req:  nil,
		},
		{
			name: "nil transfer",
			req:  &pb.CooperativeExitRequest{},
		},
		{
			name: "empty leaves",
			req: &pb.CooperativeExitRequest{
				Transfer: baseTransfer(nil),
			},
		},
		{
			name: "nil leaf job",
			req: &pb.CooperativeExitRequest{
				Transfer: baseTransfer([]*pb.LeafRefundTxSigningJob{nil}),
			},
		},
		{
			name: "missing refund tx signing job",
			req: &pb.CooperativeExitRequest{
				Transfer: baseTransfer([]*pb.LeafRefundTxSigningJob{{
					LeafId:                           uuid.New().String(),
					DirectFromCpfpRefundTxSigningJob: &pb.SigningJob{},
				}}),
			},
		},
		{
			name: "missing direct from cpfp refund tx signing job",
			req: &pb.CooperativeExitRequest{
				Transfer: baseTransfer([]*pb.LeafRefundTxSigningJob{{
					LeafId:             uuid.New().String(),
					RefundTxSigningJob: &pb.SigningJob{},
				}}),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := handler.CooperativeExitV2(t.Context(), tt.req)
			require.Nil(t, resp)
			require.Equal(t, codes.InvalidArgument, status.Code(err))
		})
	}
}

func TestCooperativeExitV2RejectsMalformedExitTxidBeforePendingSend(t *testing.T) {
	handler := NewCooperativeExitHandler(&so.Config{})
	ownerKey := keys.GeneratePrivateKey().Public().Serialize()
	receiverKey := keys.GeneratePrivateKey().Public().Serialize()

	buildRequest := func(exitTxid []byte) *pb.CooperativeExitRequest {
		return &pb.CooperativeExitRequest{
			Transfer: &pb.StartTransferRequest{
				OwnerIdentityPublicKey:    ownerKey,
				ReceiverIdentityPublicKey: receiverKey,
				TransferId:                uuid.New().String(),
				ExpiryTime:                timestamppb.Now(),
				LeavesToSend: []*pb.LeafRefundTxSigningJob{{
					LeafId:                           uuid.New().String(),
					RefundTxSigningJob:               &pb.SigningJob{RawTx: []byte{0x01}},
					DirectFromCpfpRefundTxSigningJob: &pb.SigningJob{RawTx: []byte{0x02}},
				}},
			},
			ExitId:   uuid.New().String(),
			ExitTxid: exitTxid,
		}
	}

	for _, tc := range []struct {
		name     string
		exitTxid []byte
	}{
		{name: "empty", exitTxid: nil},
		{name: "too_short", exitTxid: make([]byte, 31)},
		{name: "too_long", exitTxid: make([]byte, 33)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := handler.CooperativeExitV2(t.Context(), buildRequest(tc.exitTxid))
			require.Nil(t, resp)
			require.Equal(t, codes.InvalidArgument, status.Code(err))
			require.Contains(t, err.Error(), "exit_txid")
		})
	}
}
