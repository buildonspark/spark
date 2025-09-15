package grpc

import (
	"context"
	"errors"
	"fmt"

	"github.com/lightsparkdev/spark/common/logging"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const (
	// MaxRequestSize is the maximum allowed size for incoming requests in bytes
	MaxRequestSize = 10 * 1024 * 1024 // 10MB

	// MaxArrayLength is the maximum allowed length for arrays/slices in requests
	MaxArrayLength = 1000
)

// ValidationError represents a structured validation error
type ValidationError struct {
	Field      string
	Value      any
	Constraint string
	Message    string
	Method     string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("validation failed for field %s: %s (constraint: %s)", e.Field, e.Message, e.Constraint)
}

// validateRequestSize checks if the request size is within allowed limits
func validateRequestSize(req any, method string) error {
	if msg, ok := req.(proto.Message); ok {
		size := proto.Size(msg)
		if size > MaxRequestSize {
			return &ValidationError{
				Field:      "request_size",
				Value:      size,
				Constraint: fmt.Sprintf("must be <= %d bytes", MaxRequestSize),
				Message:    "request too large",
				Method:     method,
			}
		}
	}
	return nil
}

// validateArrayLengths recursively checks array lengths in the request
func validateArrayLengths(req any, method string) error {
	if msg, ok := req.(proto.Message); ok {
		// Use reflection to check array lengths
		// This is a simplified version - in practice you'd want to recursively check all fields
		if protoMsg, ok := msg.(interface{ GetArrayFields() []any }); ok {
			for _, arr := range protoMsg.GetArrayFields() {
				if anyArr, ok := arr.([]any); ok && len(anyArr) > MaxArrayLength {
					return &ValidationError{
						Field:      "array_length",
						Value:      len(anyArr),
						Constraint: fmt.Sprintf("must be <= %d items", MaxArrayLength),
						Message:    "array too long",
						Method:     method,
					}
				}
			}
		}
	}
	return nil
}

func ValidationInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
		logger := logging.GetLoggerFromContext(ctx)

		// Check request size
		if err := validateRequestSize(req, info.FullMethod); err != nil {
			var valErr *ValidationError
			if errors.As(err, &valErr) {
				logger.With(zap.Error(err)).Sugar().Warnf("Request size validation failed (size: %d)", valErr.Value)
			}
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}

		// Check array lengths
		if err := validateArrayLengths(req, info.FullMethod); err != nil {
			var valErr *ValidationError
			if errors.As(err, &valErr) {
				logger.With(zap.Error(err)).Sugar().Warnf("Array length validation failed (length: %d)", valErr.Value)
			}
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}

		// Validate the request proto if it implements Validate()
		if v, ok := req.(interface{ Validate() error }); ok {
			if err := v.Validate(); err != nil {
				logger.Warn("Proto validation failed", zap.Error(err))
				return nil, status.Errorf(codes.InvalidArgument, "invalid request: %v", err)
			}
		}

		// Pass the request on down the chain
		resp, err = handler(ctx, req)
		if err != nil {
			return nil, err
		}

		// Validate the response proto if it implements Validate()
		if resp != nil {
			if v, ok := resp.(interface{ Validate() error }); ok {
				if err := v.Validate(); err != nil {
					logger.Error("Response validation failed", zap.Error(err))
					return nil, status.Errorf(codes.Internal, "invalid response: %v", err)
				}
			}
		}

		return resp, nil
	}
}

type validatingServerStream struct {
	grpc.ServerStream
	method string
	logger *zap.Logger
}

func StreamValidationInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		logger := logging.GetLoggerFromContext(ss.Context())

		// Create a wrapping ServerStream that intercepts SendMsg
		wrappedStream := &validatingServerStream{
			ServerStream: ss,
			method:       info.FullMethod,
			logger:       logger,
		}

		// Handle the stream
		return handler(srv, wrappedStream)
	}
}

func (s *validatingServerStream) SendMsg(m any) error {
	// Validate outgoing message if it implements Validate()
	if m != nil {
		if v, ok := m.(interface{ Validate() error }); ok {
			if err := v.Validate(); err != nil {
				s.logger.Error("Stream response validation failed", zap.Error(err))
				return status.Errorf(codes.Internal, "invalid response: %v", err)
			}
		}
	}

	return s.ServerStream.SendMsg(m)
}
