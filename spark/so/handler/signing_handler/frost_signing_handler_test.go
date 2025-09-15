package signing_handler

import (
	"testing"

	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFrostSigningHandler_GenerateRandomNonces(t *testing.T) {
	tests := []struct {
		name        string
		count       uint32
		expectError bool
	}{
		{
			name:        "Generate single nonce",
			count:       1,
			expectError: false,
		},
		{
			name:        "Generate multiple nonces",
			count:       5,
			expectError: false,
		},
		{
			name:        "Generate zero nonces",
			count:       0,
			expectError: false,
		},
		{
			name:        "Generate large number of nonces",
			count:       10,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, _ := db.NewTestSQLiteContext(t)

			config := &so.Config{FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{}}
			handler := NewFrostSigningHandler(config)

			resp, err := handler.GenerateRandomNonces(ctx, tt.count)

			if tt.expectError {
				require.Error(t, err)
				assert.Nil(t, resp)
				return
			}

			// Verify response
			require.NoError(t, err)
			assert.NotNil(t, resp)
			assert.Len(t, resp.SigningCommitments, int(tt.count))

			// Verify each commitment
			for i, commitment := range resp.SigningCommitments {
				assert.NotNil(t, commitment, "Commitment %d should not be nil", i)
				assert.NotEmpty(t, commitment.Binding, "Commitment %d binding should not be empty", i)
				assert.NotEmpty(t, commitment.Hiding, "Commitment %d hiding should not be empty", i)
				assert.Len(t, commitment.Binding, 33, "Commitment %d binding should be 33 bytes (compressed public key)", i)
				assert.Len(t, commitment.Hiding, 33, "Commitment %d hiding should be 33 bytes (compressed public key)", i)
			}

			// Verify that nonces were stored in database
			dbTx, err := ent.GetDbFromContext(ctx)
			require.NoError(t, err)

			nonces, err := dbTx.SigningNonce.Query().All(ctx)
			require.NoError(t, err)
			assert.Len(t, nonces, int(tt.count), "Expected %d nonces in database", tt.count)

			// Verify that each nonce has a corresponding commitment
			for _, nonce := range nonces {
				assert.NotEmpty(t, nonce.Nonce, "Nonce should not be empty")
				assert.NotEmpty(t, nonce.NonceCommitment, "Nonce commitment should not be empty")
				assert.Len(t, nonce.Nonce, 64, "Nonce should be 64 bytes (32 binding + 32 hiding)")
			}
		})
	}
}

func TestFrostSigningHandler_GenerateRandomNonces_UniqueCommitments(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)

	config := &so.Config{FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{}}
	handler := NewFrostSigningHandler(config)

	// Generate multiple nonces
	count := uint32(10)
	resp, err := handler.GenerateRandomNonces(ctx, count)
	require.NoError(t, err)
	assert.Len(t, resp.SigningCommitments, int(count))

	// Verify that all commitments are unique
	commitmentMap := make(map[string]bool)
	for i, commitment := range resp.SigningCommitments {
		// Create a unique key for each commitment by combining binding and hiding
		key := string(commitment.Binding) + string(commitment.Hiding)
		assert.False(t, commitmentMap[key], "Commitment %d should be unique", i)
		commitmentMap[key] = true
	}

	// Verify that we have exactly the expected number of unique commitments
	assert.Len(t, commitmentMap, int(count), "Should have exactly %d unique commitments", count)
}

func TestFrostSigningHandler_NewFrostSigningHandler(t *testing.T) {
	config := &so.Config{FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{}}
	handler := NewFrostSigningHandler(config)

	assert.NotNil(t, handler)
	assert.Equal(t, config, handler.config)
}

func TestFrostSigningHandler_GenerateRandomNonces_DatabaseError(t *testing.T) {
	// Test with a context that doesn't have a database connection
	ctx := t.Context()
	config := &so.Config{FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{}}
	handler := NewFrostSigningHandler(config)

	// This should fail because there's no database context
	resp, err := handler.GenerateRandomNonces(ctx, 1)
	require.Error(t, err)
	assert.Nil(t, resp)
}
