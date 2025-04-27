package grpctest

import (
	"context"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	pb "github.com/lightsparkdev/spark/proto/spark"
	testutil "github.com/lightsparkdev/spark/test_util"
	"github.com/lightsparkdev/spark/wallet"
	"github.com/stretchr/testify/require"
)

func skipConnectedEvent(t *testing.T, stream pb.SparkService_SubscribeToEventsClient) {
	event, err := stream.Recv()
	require.NoError(t, err)
	require.NotNil(t, event.GetConnected())
}

func TestEventHandlerTransferNotification(t *testing.T) {
	senderConfig, err := testutil.TestWalletConfig()
	require.NoError(t, err)

	receiverConfig, err := testutil.TestWalletConfig()
	require.NoError(t, err)

	stream, err := wallet.SubscribeToEvents(context.Background(), receiverConfig)
	require.NoError(t, err)

	numTransfers := 5
	events := make(chan *pb.SubscribeToEventsResponse, numTransfers)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		skipConnectedEvent(t, stream)
		for {
			select {
			case <-ctx.Done():
				return
			default:
				event, err := stream.Recv()
				if err != nil {
					return
				}
				events <- event
			}
		}
	}()

	var expectedNodeIDs []string
	for range numTransfers {
		leafPrivKey, err := secp256k1.GeneratePrivateKey()
		require.NoError(t, err, "failed to create node signing private key")

		rootNode, err := testutil.CreateNewTree(senderConfig, faucet, leafPrivKey, 100_000)
		require.NoError(t, err, "failed to create new tree")
		expectedNodeIDs = append(expectedNodeIDs, rootNode.Id)

		newLeafPrivKey, err := secp256k1.GeneratePrivateKey()
		require.NoError(t, err, "failed to create new node signing private key")

		transferNode := wallet.LeafKeyTweak{
			Leaf:              rootNode,
			SigningPrivKey:    leafPrivKey.Serialize(),
			NewSigningPrivKey: newLeafPrivKey.Serialize(),
		}
		leavesToTransfer := [1]wallet.LeafKeyTweak{transferNode}

		_, err = wallet.SendTransfer(
			context.Background(),
			senderConfig,
			leavesToTransfer[:],
			receiverConfig.IdentityPublicKey(),
			time.Now().Add(10*time.Minute),
		)
		require.NoError(t, err)
	}

	receivedEvents := 0
	receivedNodeIDs := make(map[string]bool)

	for receivedEvents < numTransfers {
		select {
		case event := <-events:
			require.NotNil(t, event)
			require.NotNil(t, event.GetTransfer())
			transfer := event.GetTransfer().Transfer
			require.NotNil(t, transfer)
			require.Equal(t, 1, len(transfer.Leaves))

			nodeID := transfer.Leaves[0].Leaf.Id
			require.Contains(t, expectedNodeIDs, nodeID)
			require.False(t, receivedNodeIDs[nodeID], "Received duplicate event")
			receivedNodeIDs[nodeID] = true
			receivedEvents++

		case <-time.After(10 * time.Second):
			require.Fail(t, "timed out waiting for events")
		}
	}
}

func TestEventHandlerDepositNotification(t *testing.T) {
	config, err := testutil.TestWalletConfig()
	require.NoError(t, err)

	stream, err := wallet.SubscribeToEvents(context.Background(), config)
	require.NoError(t, err)

	events := make(chan *pb.SubscribeToEventsResponse, 1)
	go func() {
		skipConnectedEvent(t, stream)

		for {
			event, err := stream.Recv()
			require.NoError(t, err)
			events <- event
		}
	}()

	leafPrivKey, err := secp256k1.GeneratePrivateKey()
	require.NoError(t, err, "failed to create node signing private key")

	rootNode, err := testutil.CreateNewTree(config, faucet, leafPrivKey, 100_000)
	require.NoError(t, err, "failed to create new tree")

	select {
	case event := <-events:
		require.NotNil(t, event)
		require.NotNil(t, event.GetDeposit())
		require.Equal(t, rootNode.Id, event.GetDeposit().Deposit.Id)
	case <-time.After(5 * time.Second):
		require.Fail(t, "no event received")
	}
}

func TestMultipleSubscriptions(t *testing.T) {
	senderConfig, err := testutil.TestWalletConfig()
	require.NoError(t, err)

	receiverConfig, err := testutil.TestWalletConfig()
	require.NoError(t, err)

	stream1, err := wallet.SubscribeToEvents(context.Background(), receiverConfig)
	require.NoError(t, err)

	events1 := make(chan *pb.SubscribeToEventsResponse, 1)
	go func() {
		skipConnectedEvent(t, stream1)

		for {
			event, err := stream1.Recv()
			if err != nil {
				return
			}
			events1 <- event
		}
	}()

	stream2, err := wallet.SubscribeToEvents(context.Background(), receiverConfig)
	require.NoError(t, err)

	events2 := make(chan *pb.SubscribeToEventsResponse, 1)
	go func() {
		skipConnectedEvent(t, stream2)

		for {
			event, err := stream2.Recv()
			if err != nil {
				return
			}
			events2 <- event
		}
	}()

	leafPrivKey, err := secp256k1.GeneratePrivateKey()
	require.NoError(t, err)
	rootNode, err := testutil.CreateNewTree(senderConfig, faucet, leafPrivKey, 100_000)
	require.NoError(t, err)

	newLeafPrivKey, err := secp256k1.GeneratePrivateKey()
	require.NoError(t, err)
	transferNode := wallet.LeafKeyTweak{
		Leaf:              rootNode,
		SigningPrivKey:    leafPrivKey.Serialize(),
		NewSigningPrivKey: newLeafPrivKey.Serialize(),
	}
	leavesToTransfer := [1]wallet.LeafKeyTweak{transferNode}

	_, err = wallet.SendTransfer(
		context.Background(),
		senderConfig,
		leavesToTransfer[:],
		receiverConfig.IdentityPublicKey(),
		time.Now().Add(10*time.Minute),
	)
	require.NoError(t, err)

	select {
	case <-events1:
		t.Fatal("stream1 should not receive any events")
	case event := <-events2:
		require.NotNil(t, event)
		require.NotNil(t, event.GetTransfer())
		require.Equal(t, rootNode.Id, event.GetTransfer().Transfer.Leaves[0].Leaf.Id)
	case <-time.After(5 * time.Second):
		t.Fatal("no event received on stream2")
	}
}
