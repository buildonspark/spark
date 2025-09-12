package helper

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetSigningCommitmentsFromContext_MapUpdate(t *testing.T) {
	initialCommitments := map[uint][]*ent.SigningCommitment{
		1: {
			{ID: uuid.New()},
			{ID: uuid.New()},
			{ID: uuid.New()},
		},
		2: {
			{ID: uuid.New()},
			{ID: uuid.New()},
		},
	}

	ctx := context.WithValue(t.Context(), signingCommitmentsKey, initialCommitments)

	// Test getting commitments and verify map is updated
	commitments, err := GetSigningCommitmentsFromContext(ctx, 2, 1)

	require.NoError(t, err)
	assert.Len(t, commitments, 2)

	// Verify the map was updated (only 1 commitment should remain)
	updatedMap, ok := ctx.Value(signingCommitmentsKey).(map[uint][]*ent.SigningCommitment)
	require.True(t, ok)
	remainingCommitments := updatedMap[1]
	assert.Len(t, remainingCommitments, 1)

	// Verify operator 2 was not affected
	operator2Commitments := updatedMap[2]
	assert.Len(t, operator2Commitments, 2)
}

func TestGetSigningCommitmentsFromContext_NotEnoughCommitments(t *testing.T) {
	initialCommitments := map[uint][]*ent.SigningCommitment{
		1: {{ID: uuid.New()}},
	}

	ctx := context.WithValue(t.Context(), signingCommitmentsKey, initialCommitments)

	// Test getting more commitments than available
	commitments, err := GetSigningCommitmentsFromContext(ctx, 2, 1)

	require.ErrorContains(t, err, "not enough signing commitments")
	assert.Empty(t, commitments)
}

func TestGetSigningCommitmentsFromContext_OperatorNotFound(t *testing.T) {
	initialCommitments := map[uint][]*ent.SigningCommitment{
		1: {{ID: uuid.New()}},
	}

	ctx := context.WithValue(t.Context(), signingCommitmentsKey, initialCommitments)

	// Test getting commitments for operator that doesn't exist
	commitments, err := GetSigningCommitmentsFromContext(ctx, 1, 999)

	require.ErrorContains(t, err, "not enough signing commitments")
	assert.Nil(t, commitments)
}
