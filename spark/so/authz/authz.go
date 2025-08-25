package authz

import (
	"context"
	"fmt"
	"github.com/lightsparkdev/spark/common/keys"

	"github.com/lightsparkdev/spark/so/authn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Config defines the base configuration interface for authorization
type Config interface {
	// IsAuthzEnforced returns whether authorization is enforced
	IsAuthzEnforced() bool
}

// Error represents authorization errors
type Error struct {
	Code    ErrorCode
	Message string
	Cause   error
}

func (e *Error) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.Cause)
	}
	return e.Message
}

type ErrorCode int

const (
	ErrorCodeNoSession ErrorCode = iota
	ErrorCodeIdentityMismatch
)

// ToGRPCError converts the auth error to an appropriate gRPC error
func (e *Error) ToGRPCError() error {
	var code codes.Code
	switch e.Code {
	case ErrorCodeNoSession:
		code = codes.Unauthenticated
	case ErrorCodeIdentityMismatch:
		code = codes.PermissionDenied
	default:
		code = codes.Internal
	}
	return status.Error(code, e.Error())
}

// EnforceSessionIdentityPublicKeyMatches checks if the request's identity public key matches the current session.
// Returns an error if authorization fails or is required but not present.
func EnforceSessionIdentityPublicKeyMatches(ctx context.Context, config Config, identityPublicKey keys.Public) error {
	if !config.IsAuthzEnforced() {
		return nil
	}

	session, err := authn.GetSessionFromContext(ctx)
	if err != nil {
		return &Error{
			Code:    ErrorCodeNoSession,
			Message: "no valid session found",
			Cause:   err,
		}
	}

	if !session.IdentityPublicKey().Equals(identityPublicKey) {
		return &Error{
			Code:    ErrorCodeIdentityMismatch,
			Message: "session identity does not match request identity",
		}
	}

	return nil
}
