package handler

import (
	"testing"

	"github.com/lightsparkdev/spark/so/consensus"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/require"
)

// TestTransferFlowsImplementPrepareBoundFlowHandler asserts that the four
// transfer-family consensus flows this stack binds implement
// PrepareBoundFlowHandler. It deliberately makes NO statement about any other
// consensus flow: whether flows like RENEW_LEAF, PROVIDE_PREIMAGE, or the
// static/instant-deposit flows need equivalent payload binding is a separate
// audit of pre-existing flows, out of scope for this transfer-binding stack.
func TestTransferFlowsImplementPrepareBoundFlowHandler(t *testing.T) {
	cfg := sparktesting.TestConfig(t)
	for name, h := range map[string]any{
		"send_transfer":          NewSendTransferFlowHandler(cfg),
		"claim_transfer":         NewClaimTransferFlowHandler(cfg),
		"coop_exit":              NewCoopExitFlowHandler(cfg),
		"initiate_preimage_swap": NewInitiatePreimageSwapFlowHandler(cfg),
	} {
		_, ok := h.(consensus.PrepareBoundFlowHandler)
		require.Truef(t, ok, "%s must implement PrepareBoundFlowHandler", name)
	}
}
