package handler

import (
	"context"

	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	"github.com/lightsparkdev/spark/so/handler/tokens"
)

func (h *GossipHandler) handleRevokeTokenAllowanceGossipMessage(ctx context.Context, msg *pbgossip.GossipMessageRevokeTokenAllowance) error {
	return tokens.ApplyRevokeTokenAllowanceGossip(ctx, h.config, msg)
}
