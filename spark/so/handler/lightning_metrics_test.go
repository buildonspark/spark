package handler

import (
	"context"
	"errors"
	"fmt"
	"testing"

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
