package tokens_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/utils"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTokenTransactionStreamNotification(t *testing.T) {
	if !broadcastTokenTestsUsePhase2 {
		t.Skip("Skipping stream notification test for non-Phase2 modes: only Phase2 finalizes the token transaction on the coordinator synchronously")
	}

	issuerPrivKey := keys.GeneratePrivateKey()

	// Create native spark token.
	params := sparkTokenCreationTestParams{
		issuerPrivateKey: issuerPrivKey,
		name:             "StreamTest",
		ticker:           "STR",
		maxSupply:        testTokenMaxSupply,
	}
	err := createNativeToken(t, params)
	require.NoError(t, err, "failed to create native token")
	tokenIdentifier := verifyNativeToken(t, params)

	config := wallet.NewTestWalletConfigWithIdentityKey(t, issuerPrivKey)

	// Subscribe to events before minting.
	authToken, err := wallet.AuthenticateWithServer(t.Context(), config)
	require.NoError(t, err, "failed to authenticate")
	streamCtx := wallet.ContextWithToken(t.Context(), authToken)

	stream, err := wallet.SubscribeToEvents(streamCtx, config)
	require.NoError(t, err)

	events := make(chan *pb.SubscribeToEventsResponse, 5)
	go func() {
		for {
			event, err := stream.Recv()
			if err != nil {
				return
			}
			events <- event
		}
	}()

	// Wait for connected event.
	select {
	case ev := <-events:
		require.NotNil(t, ev.GetConnected(), "expected connected event")
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for connected event")
	}

	// Mint tokens (creates a finalized token transaction).
	// Use MintToSelf so the output owner matches the subscriber's identity key,
	// otherwise the event handler filters out the notification.
	mintTx, _, err := createTestTokenMintTransactionTokenPbWithParams(t, config, tokenTransactionParams{
		TokenIdentityPubKey: issuerPrivKey.Public(),
		TokenIdentifier:     tokenIdentifier,
		NumOutputs:          2,
		OutputAmounts:       []uint64{uint64(testIssueOutput1Amount), uint64(testIssueOutput2Amount)},
		MintToSelf:          true,
	})
	require.NoError(t, err)

	finalMintTx, err := broadcastTokenTransaction(t, t.Context(), config, mintTx, []keys.Private{issuerPrivKey})
	require.NoError(t, err, "failed to broadcast mint transaction")

	expectedHash, err := utils.HashTokenTransaction(finalMintTx, false)
	require.NoError(t, err)

	// Wait for token transaction event.
	select {
	case ev := <-events:
		tokenEvent := ev.GetTokenTransaction()
		require.NotNil(t, tokenEvent, "expected token_transaction event, got: %v", ev)
		assert.Equal(t, expectedHash, tokenEvent.TokenTransactionHash)
		require.NotEmpty(t, tokenEvent.TokenIdentifiers, "expected token identifiers")

		found := false
		for _, id := range tokenEvent.TokenIdentifiers {
			if bytes.Equal(id, tokenIdentifier) {
				found = true
				break
			}
		}
		assert.True(t, found, "expected token identifier %x in event identifiers", tokenIdentifier)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for token transaction event")
	}
}
