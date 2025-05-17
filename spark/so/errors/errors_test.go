package errors_test

import (
	"context"
	"testing"

	"github.com/lightsparkdev/spark/so/errors"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestInternalErrorDetailMask(t *testing.T) {
	msg := "message with sensitive data"
	grpcErr := status.Errorf(codes.Internal, msg)
	handler := func(_ context.Context, _ any) (any, error) {
		return nil, grpcErr
	}
	_, err := errors.ErrorInterceptor()(context.Background(), nil, nil, handler)
	require.NotContains(t, err.Error(), msg)
}
