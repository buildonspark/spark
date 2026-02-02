package handler

import (
	"testing"

	"github.com/google/uuid"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	"github.com/lightsparkdev/spark/so/db"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/require"
)

func TestHandleCancelTransferGossipMessage_NonExistentTransfer_Succeeds(t *testing.T) {
	config := sparktesting.TestConfig(t)
	ctx, _ := db.ConnectToTestPostgres(t)

	handler := NewGossipHandler(config)

	nonExistentTransferID := uuid.New()
	cancelTransfer := &pbgossip.GossipMessageCancelTransfer{
		TransferId: nonExistentTransferID.String(),
	}

	err := handler.handleCancelTransferGossipMessage(ctx, cancelTransfer)

	require.NoError(t, err, "cancelling a non-existent transfer should succeed")
}
