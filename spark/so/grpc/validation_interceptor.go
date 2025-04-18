package grpc

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
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
	Value      interface{}
	Constraint string
	Message    string
	RequestID  string
	Method     string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("validation failed for field %s: %s (constraint: %s)", e.Field, e.Message, e.Constraint)
}

// validateRequestSize checks if the request size is within allowed limits
func validateRequestSize(req interface{}, requestID string, method string) error {
	if msg, ok := req.(proto.Message); ok {
		size := proto.Size(msg)
		if size > MaxRequestSize {
			return &ValidationError{
				Field:      "request_size",
				Value:      size,
				Constraint: fmt.Sprintf("must be <= %d bytes", MaxRequestSize),
				Message:    "request too large",
				RequestID:  requestID,
				Method:     method,
			}
		}
	}
	return nil
}

// validateArrayLengths recursively checks array lengths in the request
func validateArrayLengths(req interface{}, requestID string, method string) error {
	if msg, ok := req.(proto.Message); ok {
		// Use reflection to check array lengths
		// This is a simplified version - in practice you'd want to recursively check all fields
		if protoMsg, ok := msg.(interface{ GetArrayFields() []interface{} }); ok {
			for _, arr := range protoMsg.GetArrayFields() {
				if len(arr.([]interface{})) > MaxArrayLength {
					return &ValidationError{
						Field:      "array_length",
						Value:      len(arr.([]interface{})),
						Constraint: fmt.Sprintf("must be <= %d items", MaxArrayLength),
						Message:    "array too long",
						RequestID:  requestID,
						Method:     method,
					}
				}
			}
		}
	}
	return nil
}

func ValidationInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp interface{}, err error) {
		requestID := uuid.New().String()
		logger := slog.Default().With(
			"request_id", requestID,
			"method", info.FullMethod,
		)

		// Check request size
		if err := validateRequestSize(req, requestID, info.FullMethod); err != nil {
			logger.Warn("Request size validation failed",
				"error", err,
				"size", err.(*ValidationError).Value,
			)
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}

		// Check array lengths
		if err := validateArrayLengths(req, requestID, info.FullMethod); err != nil {
			logger.Warn("Array length validation failed",
				"error", err,
				"length", err.(*ValidationError).Value,
			)
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}

		// Validate the request proto if it implements Validate()
		if v, ok := req.(interface{ Validate() error }); ok {
			if err := v.Validate(); err != nil {
				logger.Warn("Proto validation failed", "error", err)
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
					logger.Error("Response validation failed", "error", err)
					return nil, status.Errorf(codes.Internal, "invalid response: %v", err)
				}
			}
		}

		return resp, nil
	}
}

type validatingServerStream struct {
	grpc.ServerStream
	requestID string
	method    string
	logger    *slog.Logger
}

func StreamValidationInterceptor() grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		requestID := uuid.New().String()
		logger := slog.Default().With(
			"request_id", requestID,
			"method", info.FullMethod,
		)

		// Create a wrapping ServerStream that intercepts SendMsg
		wrappedStream := &validatingServerStream{
			ServerStream: ss,
			requestID:    requestID,
			method:       info.FullMethod,
			logger:       logger,
		}

		// Handle the stream
		return handler(srv, wrappedStream)
	}
}

func (s *validatingServerStream) SendMsg(m interface{}) error {
	// Validate outgoing message if it implements Validate()
	if m != nil {
		if v, ok := m.(interface{ Validate() error }); ok {
			if err := v.Validate(); err != nil {
				s.logger.Error("Stream response validation failed", "error", err)
				return status.Errorf(codes.Internal, "invalid response: %v", err)
			}
		}
	}

	return s.ServerStream.SendMsg(m)
}
