package ent_test

import (
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent/multisigconfig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	stop := db.StartPostgresServer()
	defer stop()

	os.Exit(m.Run())
}

func TestMultisigMemberCreation_ConcurrentLimitEnforcement(t *testing.T) {
	ctx, dbCtx := db.ConnectToTestPostgres(t)

	// Create a multisig config with num_members = 3
	const numMembers = 3
	multisigID := []byte("test_multisig_id_12345678901234567890")

	config, err := dbCtx.Client.MultisigConfig.Create().
		SetMultisigIdentifier(multisigID).
		SetNumSignersThreshold(2).
		SetNumSignersTotal(numMembers).
		Save(ctx)
	require.NoError(t, err)

	// Try to create 5 members concurrently (should only allow 3)
	const numAttempts = 5
	var wg sync.WaitGroup
	errors := make([]error, numAttempts)
	var successCount atomic.Int32

	// Generate unique keys for each attempt
	publicKeys := make([]keys.Public, numAttempts)
	for i := range numAttempts {
		publicKeys[i] = keys.GeneratePrivateKey().Public()
	}

	// Launch concurrent member creation attempts
	for i := range numAttempts {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			// Each goroutine gets its own transaction context to simulate
			// concurrent requests from different gRPC handlers
			txCtx := t.Context()
			tx, err := dbCtx.Client.Tx(txCtx)
			if err != nil {
				errors[idx] = fmt.Errorf("failed to start transaction: %w", err)
				return
			}
			defer func() {
				_ = tx.Rollback() // Rollback error is expected on successful commit
			}()

			_, err = tx.MultisigMember.Create().
				SetPublicKey(publicKeys[idx]).
				SetConfig(config).
				Save(txCtx)

			if err != nil {
				errors[idx] = err
				return
			}

			// Commit the transaction
			if err := tx.Commit(); err != nil {
				errors[idx] = fmt.Errorf("failed to commit: %w", err)
				return
			}

			successCount.Add(1)
		}(i)
	}

	wg.Wait()

	// Verify that exactly num_members succeeded
	assert.Equal(t, int32(numMembers), successCount.Load(),
		"Expected exactly %d members to be created", numMembers)

	// Verify that the remaining attempts failed with the correct error
	failureCount := 0
	for i, err := range errors {
		if err != nil {
			failureCount++
			assert.Contains(t, err.Error(), "maximum number of members",
				"Attempt %d failed with unexpected error: %v", i, err)
		}
	}
	assert.Equal(t, numAttempts-numMembers, failureCount,
		"Expected %d attempts to fail", numAttempts-numMembers)

	// Verify the final count in the database
	finalCount, err := dbCtx.Client.MultisigConfig.Query().
		Where(multisigconfig.ID(config.ID)).
		QueryMembers().
		Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, numMembers, finalCount,
		"Database should contain exactly %d members", numMembers)
}

func TestMultisigMemberCreation_DuplicatePublicKey(t *testing.T) {
	ctx, dbCtx := db.ConnectToTestPostgres(t)

	// Create a multisig config
	multisigID := []byte("test_multisig_id_duplicate_key_test")
	config, err := dbCtx.Client.MultisigConfig.Create().
		SetMultisigIdentifier(multisigID).
		SetNumSignersThreshold(2).
		SetNumSignersTotal(3).
		Save(ctx)
	require.NoError(t, err)

	// Create a member with a specific public key
	publicKey := keys.GeneratePrivateKey().Public()
	_, err = dbCtx.Client.MultisigMember.Create().
		SetPublicKey(publicKey).
		SetConfig(config).
		Save(ctx)
	require.NoError(t, err)

	// Try to create another member with the same public key
	_, err = dbCtx.Client.MultisigMember.Create().
		SetPublicKey(publicKey).
		SetConfig(config).
		Save(ctx)

	// Should fail due to unique constraint on (public_key, config)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unique",
		"Expected unique constraint violation")
}

func TestMultisigMemberCreation_Sequential(t *testing.T) {
	ctx, dbCtx := db.ConnectToTestPostgres(t)

	// Create a multisig config with num_members = 3
	const numMembers = 3
	multisigID := []byte("test_multisig_id_sequential_test")
	config, err := dbCtx.Client.MultisigConfig.Create().
		SetMultisigIdentifier(multisigID).
		SetNumSignersThreshold(2).
		SetNumSignersTotal(numMembers).
		Save(ctx)
	require.NoError(t, err)

	// Create members sequentially up to the limit
	for i := range numMembers {
		publicKey := keys.GeneratePrivateKey().Public()
		_, err := dbCtx.Client.MultisigMember.Create().
			SetPublicKey(publicKey).
			SetConfig(config).
			Save(ctx)
		require.NoError(t, err, "Member %d creation should succeed", i)
	}

	// Try to create one more member (should fail)
	publicKey := keys.GeneratePrivateKey().Public()
	_, err = dbCtx.Client.MultisigMember.Create().
		SetPublicKey(publicKey).
		SetConfig(config).
		Save(ctx)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "maximum number of members",
		"Expected error about maximum members")

	// Verify final count
	finalCount, err := config.QueryMembers().Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, numMembers, finalCount)
}

func TestMultisigConfigCreation_ThresholdValidation(t *testing.T) {
	ctx, dbCtx := db.ConnectToTestPostgres(t)

	tests := []struct {
		name      string
		threshold uint32
		total     uint32
		shouldErr bool
		errSubstr string
	}{
		{
			name:      "threshold equals total (valid)",
			threshold: 3,
			total:     3,
			shouldErr: false,
		},
		{
			name:      "threshold less than total (valid)",
			threshold: 2,
			total:     3,
			shouldErr: false,
		},
		{
			name:      "threshold exceeds total (invalid)",
			threshold: 4,
			total:     3,
			shouldErr: true,
			errSubstr: "cannot exceed",
		},
		{
			name:      "threshold zero (invalid - caught by Min validator)",
			threshold: 0,
			total:     3,
			shouldErr: true,
			errSubstr: "value out of range",
		},
		{
			name:      "total zero (invalid - caught by Min validator)",
			threshold: 2,
			total:     0,
			shouldErr: true,
			errSubstr: "cannot exceed",
		},
		{
			name:      "total one (invalid - caught by Min validator)",
			threshold: 1,
			total:     1,
			shouldErr: true,
			errSubstr: "value out of range",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			multisigID := fmt.Appendf(nil, "test_multisig_id_%s", tc.name)
			_, err := dbCtx.Client.MultisigConfig.Create().
				SetMultisigIdentifier(multisigID).
				SetNumSignersThreshold(tc.threshold).
				SetNumSignersTotal(tc.total).
				Save(ctx)

			if tc.shouldErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.errSubstr,
					"Expected error containing '%s', got: %v", tc.errSubstr, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
