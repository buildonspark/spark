package handler

import (
	"context"
	"encoding/hex"
	"testing"

	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authn"
	"github.com/lightsparkdev/spark/so/db"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestQueryTransfers_SSP_WithReceiverFilter(t *testing.T) {
	// Test that SSP can query transfers by receiver without authorization check
	ctx, cfg := createTestContextForTransferQuery(t)

	// Create receiver identity key
	receiverIDPubKey := keys.GeneratePrivateKey().Public()

	// Create a transfer filter with receiver identity
	filter := &pb.TransferFilter{
		Participant: &pb.TransferFilter_ReceiverIdentityPublicKey{
			ReceiverIdentityPublicKey: receiverIDPubKey.Serialize(),
		},
	}

	// Call queryTransfers with isSSP=true, isPending=false
	handler := NewTransferHandler(cfg)
	resp, err := handler.queryTransfers(ctx, filter, false, true)

	// Should not error - SSP bypasses authz check
	require.NoError(t, err, "SSP should be able to query transfers without auth")
	assert.NotNil(t, resp, "Response should not be nil")
}

func TestQueryTransfers_SSP_WithSenderFilter(t *testing.T) {
	// Test that SSP can query transfers by sender without authorization check
	ctx, cfg := createTestContextForTransferQuery(t)

	// Create sender identity key
	senderIDPubKey := keys.GeneratePrivateKey().Public()

	// Create a transfer filter with sender identity
	filter := &pb.TransferFilter{
		Participant: &pb.TransferFilter_SenderIdentityPublicKey{
			SenderIdentityPublicKey: senderIDPubKey.Serialize(),
		},
	}

	// Call queryTransfers with isSSP=true, isPending=false
	handler := NewTransferHandler(cfg)
	resp, err := handler.queryTransfers(ctx, filter, false, true)

	// Should not error - SSP bypasses authz check
	require.NoError(t, err, "SSP should be able to query transfers without auth")
	assert.NotNil(t, resp, "Response should not be nil")
}

func TestQueryTransfers_NotSSP_RequiresAuthz(t *testing.T) {
	// Test that non-SSP queries require authentication and match participant
	ctx, cfg := createTestContextForTransferQuery(t)

	// Create identity keys
	receiverIDPubKey := keys.GeneratePrivateKey().Public()

	// Inject session for the receiver
	ctx = authn.InjectSessionForTests(ctx, hex.EncodeToString(receiverIDPubKey.Serialize()), 9999999999)

	// Create a transfer filter with receiver identity
	filter := &pb.TransferFilter{
		Participant: &pb.TransferFilter_ReceiverIdentityPublicKey{
			ReceiverIdentityPublicKey: receiverIDPubKey.Serialize(),
		},
	}

	// Call queryTransfers with isPending=false, isSSP=false
	handler := NewTransferHandler(cfg)
	resp, err := handler.queryTransfers(ctx, filter, false, false)

	// Should not error - session matches receiver
	require.NoError(t, err, "Should be able to query transfers when session matches participant")
	assert.NotNil(t, resp, "Response should not be nil")
}

func TestQueryTransfers_NotSSP_RequiresAuthz_Mismatch(t *testing.T) {
	// Test that non-SSP queries fail when session doesn't match participant
	ctx, cfg := createTestContextForTransferQuery(t)

	// Create identity keys
	receiverIDPubKey := keys.GeneratePrivateKey().Public()
	differentIDPubKey := keys.GeneratePrivateKey().Public()

	// Inject session for a different identity
	ctx = authn.InjectSessionForTests(ctx, hex.EncodeToString(differentIDPubKey.Serialize()), 9999999999)

	// Create a transfer filter with receiver identity
	filter := &pb.TransferFilter{
		Participant: &pb.TransferFilter_ReceiverIdentityPublicKey{
			ReceiverIdentityPublicKey: receiverIDPubKey.Serialize(),
		},
	}

	// Call queryTransfers with isPending=false, isSSP=false
	handler := NewTransferHandler(cfg)
	_, err := handler.queryTransfers(ctx, filter, false, false)

	// Should error - session doesn't match receiver
	require.Error(t, err, "Should fail when session doesn't match participant")
}

func TestQueryTransfers_NotSSP_NoSession(t *testing.T) {
	// Test that non-SSP queries fail when there's no session
	ctx, cfg := createTestContextForTransferQuery(t)

	// Create identity keys
	receiverIDPubKey := keys.GeneratePrivateKey().Public()

	// Don't inject any session

	// Create a transfer filter with receiver identity
	filter := &pb.TransferFilter{
		Participant: &pb.TransferFilter_ReceiverIdentityPublicKey{
			ReceiverIdentityPublicKey: receiverIDPubKey.Serialize(),
		},
	}

	// Call queryTransfers with isPending=false, isSSP=false
	handler := NewTransferHandler(cfg)
	_, err := handler.queryTransfers(ctx, filter, false, false)

	// Should error - no session
	require.Error(t, err, "Should fail when there's no session")
}

// Helper function to create test context with authz enabled
func createTestContextForTransferQuery(t *testing.T) (context.Context, *so.Config) {
	ctx, _ := db.NewTestSQLiteContext(t)
	cfg := sparktesting.TestConfig(t)
	cfg.AuthzEnforced = true // Enable authz enforcement for these tests
	return ctx, cfg
}
