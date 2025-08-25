package tokens

import (
	"context"
	"fmt"
	"github.com/lightsparkdev/spark/common/keys"

	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authz"
	"github.com/lightsparkdev/spark/so/tokens"
	"google.golang.org/protobuf/types/known/emptypb"
)

type FinalizeTokenHandler struct {
	config *so.Config
}

// NewFinalizeTokenHandler creates a new FinalizeTransactionHandler.
func NewFinalizeTokenHandler(config *so.Config) *FinalizeTokenHandler {
	return &FinalizeTokenHandler{
		config: config,
	}
}

// FinalizeTokenTransaction takes the revocation private keys for spent outputs and updates their status to finalized.
func (h *FinalizeTokenHandler) FinalizeTokenTransaction(ctx context.Context, req *sparkpb.FinalizeTokenTransactionRequest) (*emptypb.Empty, error) {
	pubKey, err := keys.ParsePublicKey(req.IdentityPublicKey)
	if err != nil {
		return nil, fmt.Errorf("invalid identity public key: %w", err)
	}
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, pubKey); err != nil {
		return nil, fmt.Errorf("%s: %w", tokens.ErrIdentityPublicKeyAuthFailed, err)
	}

	internalFinalizeHandler := NewInternalFinalizeTokenHandler(h.config)
	return internalFinalizeHandler.FinalizeTokenTransactionInternal(ctx, req)
}
