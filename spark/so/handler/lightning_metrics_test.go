package handler

import (
	"context"
	"errors"
	"fmt"
	"testing"

	pbspark "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestClassifyLightningMetricResult(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "success",
			err:  nil,
			want: lightningResultSuccess,
		},
		{
			name: "context canceled",
			err:  context.Canceled,
			want: lightningResultCanceled,
		},
		{
			name: "grpc canceled",
			err:  status.Error(codes.Canceled, "client canceled"),
			want: lightningResultCanceled,
		},
		{
			name: "wrapped grpc canceled",
			err:  fmt.Errorf("flow failed: %w", status.Error(codes.Canceled, "client canceled")),
			want: lightningResultCanceled,
		},
		{
			name: "context deadline exceeded",
			err:  context.DeadlineExceeded,
			want: lightningResultTimeout,
		},
		{
			name: "wrapped grpc deadline exceeded",
			err:  fmt.Errorf("flow failed: %w", status.Error(codes.DeadlineExceeded, "deadline exceeded")),
			want: lightningResultTimeout,
		},
		{
			name: "grpc unavailable",
			err:  status.Error(codes.Unavailable, "unavailable"),
			want: lightningResultUnavailable,
		},
		{
			name: "wrapped grpc unavailable",
			err:  fmt.Errorf("flow failed: %w", status.Error(codes.Unavailable, "unavailable")),
			want: lightningResultUnavailable,
		},
		{
			name: "generic error",
			err:  errors.New("boom"),
			want: lightningResultError,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, classifyLightningMetricResult(test.err))
		})
	}
}

func TestLightningTargetOperatorIndex(t *testing.T) {
	require.Equal(t, "0", lightningTargetOperatorIndex(so.IndexToIdentifier(0)))
	require.Equal(t, "41", lightningTargetOperatorIndex(so.IndexToIdentifier(41)))
	require.Equal(t, "unknown", lightningTargetOperatorIndex("0"))
	require.Equal(t, "unknown", lightningTargetOperatorIndex("operator1"))
}

func TestPreimageSwapShape(t *testing.T) {
	tests := []struct {
		name string
		req  *pbspark.InitiatePreimageSwapRequest
		want string
	}{
		{
			name: "transfer only",
			req:  &pbspark.InitiatePreimageSwapRequest{Transfer: &pbspark.StartUserSignedTransferRequest{}},
			want: preimageSwapShapeTransferOnly,
		},
		{
			name: "transfer_request only",
			req:  &pbspark.InitiatePreimageSwapRequest{TransferRequest: &pbspark.StartTransferRequest{}},
			want: preimageSwapShapeTransferRequestOnly,
		},
		{
			name: "both",
			req: &pbspark.InitiatePreimageSwapRequest{
				Transfer:        &pbspark.StartUserSignedTransferRequest{},
				TransferRequest: &pbspark.StartTransferRequest{},
			},
			want: preimageSwapShapeBoth,
		},
		{
			name: "neither",
			req:  &pbspark.InitiatePreimageSwapRequest{},
			want: preimageSwapShapeNeither,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, preimageSwapShape(tt.req))
		})
	}
}

func TestPreimageSwapReason(t *testing.T) {
	require.Equal(t, "send", preimageSwapReason(pbspark.InitiatePreimageSwapRequest_REASON_SEND))
	require.Equal(t, "receive", preimageSwapReason(pbspark.InitiatePreimageSwapRequest_REASON_RECEIVE))
	require.Equal(t, "unknown", preimageSwapReason(pbspark.InitiatePreimageSwapRequest_Reason(999999)))
}
