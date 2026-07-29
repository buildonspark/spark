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
		name           string
		err            error
		expectedResult string
	}{
		{
			name:           "success",
			err:            nil,
			expectedResult: lightningResultSuccess,
		},
		{
			name:           "context canceled",
			err:            context.Canceled,
			expectedResult: lightningResultCanceled,
		},
		{
			name:           "grpc canceled",
			err:            status.Error(codes.Canceled, "client canceled"),
			expectedResult: lightningResultCanceled,
		},
		{
			name:           "wrapped grpc canceled",
			err:            fmt.Errorf("flow failed: %w", status.Error(codes.Canceled, "client canceled")),
			expectedResult: lightningResultCanceled,
		},
		{
			name:           "context deadline exceeded",
			err:            context.DeadlineExceeded,
			expectedResult: lightningResultTimeout,
		},
		{
			name:           "wrapped grpc deadline exceeded",
			err:            fmt.Errorf("flow failed: %w", status.Error(codes.DeadlineExceeded, "deadline exceeded")),
			expectedResult: lightningResultTimeout,
		},
		{
			name:           "grpc unavailable",
			err:            status.Error(codes.Unavailable, "unavailable"),
			expectedResult: lightningResultUnavailable,
		},
		{
			name:           "wrapped grpc unavailable",
			err:            fmt.Errorf("flow failed: %w", status.Error(codes.Unavailable, "unavailable")),
			expectedResult: lightningResultUnavailable,
		},
		{
			name:           "generic error",
			err:            errors.New("boom"),
			expectedResult: lightningResultError,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.expectedResult, classifyLightningMetricResult(test.err))
		})
	}
}

func TestLightningTargetOperatorIndex(t *testing.T) {
	require.Equal(t, "0", lightningTargetOperatorIndex(so.IndexToIdentifier(0)))
	require.Equal(t, "41", lightningTargetOperatorIndex(so.IndexToIdentifier(41)))
	require.Equal(t, "unknown", lightningTargetOperatorIndex("0"))
	require.Equal(t, "unknown", lightningTargetOperatorIndex("operator1"))
}
