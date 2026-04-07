package handler

import (
	"testing"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/google/uuid"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	pb "github.com/lightsparkdev/spark/proto/spark"
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

func TestHandleCancelTransferGossipMessage_InvalidTransferID_ReturnsError(t *testing.T) {
	config := sparktesting.TestConfig(t)
	ctx := t.Context()

	handler := NewGossipHandler(config)

	cancelTransfer := &pbgossip.GossipMessageCancelTransfer{
		TransferId: "not-a-valid-uuid",
	}

	err := handler.handleCancelTransferGossipMessage(ctx, cancelTransfer)

	require.Error(t, err, "cancelling with a malformed transfer ID should return an error")
}

func TestHandleRollbackTransfer_NonExistentTransfer_Succeeds(t *testing.T) {
	config := sparktesting.TestConfig(t)
	ctx, _ := db.ConnectToTestPostgres(t)

	handler := NewGossipHandler(config)

	nonExistentTransferID := uuid.New()
	rollbackTransfer := &pbgossip.GossipMessageRollbackTransfer{
		TransferId: nonExistentTransferID.String(),
	}

	err := handler.handleRollbackTransfer(ctx, rollbackTransfer)

	require.NoError(t, err, "rolling back a non-existent transfer should succeed")
}

func TestHandleRollbackTransfer_InvalidTransferID_ReturnsError(t *testing.T) {
	config := sparktesting.TestConfig(t)
	ctx := t.Context()

	handler := NewGossipHandler(config)

	rollbackTransfer := &pbgossip.GossipMessageRollbackTransfer{
		TransferId: "not-a-valid-uuid",
	}

	err := handler.handleRollbackTransfer(ctx, rollbackTransfer)

	require.Error(t, err, "rolling back with a malformed transfer ID should return an error")
}

func TestHandleSettleSenderKeyTweakGossipMessage_InvalidTransferID_ReturnsError(t *testing.T) {
	config := sparktesting.TestConfig(t)
	ctx := t.Context()

	handler := NewGossipHandler(config)

	settleSenderKeyTweak := &pbgossip.GossipMessageSettleSenderKeyTweak{
		TransferId: "not-a-valid-uuid",
	}

	err := handler.handleSettleSenderKeyTweakGossipMessage(ctx, settleSenderKeyTweak)

	require.Error(t, err, "settling sender key tweak with a malformed transfer ID should return an error")
}

func TestHandleRollbackUtxoSwapGossipMessage_NonExistentUtxo_Succeeds(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	handler := NewGossipHandler(cfg)

	nonExistentTxid := chainhash.DoubleHashB([]byte("nonexistent_txid_for_gossip_test"))
	rollbackRequest, err := GenerateRollbackStaticDepositUtxoSwapForUtxoRequest(ctx, cfg, &pb.UTXO{
		Txid:    nonExistentTxid,
		Vout:    0,
		Network: pb.Network_REGTEST,
	})
	require.NoError(t, err)

	gossipMsg := &pbgossip.GossipMessageRollbackUtxoSwap{
		OnChainUtxo:          rollbackRequest.OnChainUtxo,
		Signature:            rollbackRequest.Signature,
		CoordinatorPublicKey: rollbackRequest.CoordinatorPublicKey,
	}

	err = handler.handleRollbackUtxoSwapGossipMessage(ctx, gossipMsg)
	require.NoError(t, err, "rolling back a non-existent UTXO should succeed")
}
