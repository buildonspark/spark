package errors_test

import (
	"context"
	"testing"

	"github.com/lightsparkdev/spark/so/errors"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const msg = "message with sensitive data"

var (
	grpcErr = status.Errorf(codes.Internal, msg)
	handler = func(_ context.Context, _ any) (any, error) {
		return nil, grpcErr
	}
)

func TestInternalErrorDetailMask(t *testing.T) {
	serverInfo := &grpc.UnaryServerInfo{FullMethod: "/spark.SparkService/SomeMethod"}
	_, err := errors.ErrorInterceptor(false)(context.Background(), nil, serverInfo, handler)
	require.NotContains(t, err.Error(), msg)
}

func TestInternalErrorDetailDoNotMask(t *testing.T) {
	serverInfo := &grpc.UnaryServerInfo{FullMethod: "/spark.SparkService/SomeMethod"}
	_, err := errors.ErrorInterceptor(true)(context.Background(), nil, serverInfo, handler)
	require.Contains(t, err.Error(), msg)
}

func TestInternalErrorDetailForInternalService(t *testing.T) {
	serverInfo := &grpc.UnaryServerInfo{FullMethod: "/spark_internal.SparkInternalService/SomeMethod"}
	_, err := errors.ErrorInterceptor(false)(context.Background(), nil, serverInfo, handler)
	require.Contains(t, err.Error(), msg)
}
