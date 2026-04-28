package task

import (
	"context"
	"errors"
	"fmt"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/signingkeyshare"
	"github.com/lightsparkdev/spark/so/entephemeral"
	"github.com/lightsparkdev/spark/so/entephemeral/signingkeysharesecret"
)

const (
	purgeDanglingSigningKeyshareSecretsGracePeriod      = 10 * time.Minute
	purgeDanglingSigningKeyshareSecretsDefaultBatchSize = 1000
	// Bound how many aged rows a single purge invocation will scan before yielding
	// to the next scheduled run in active-heavy environments.
	purgeDanglingSigningKeyshareSecretsMaxScanMultiplier = 10
)

type purgeDanglingSigningKeyshareSecretsBatchResult struct {
	CandidateCount       int
	DeletedCount         int
	FoundFullDeleteBatch bool
	ScanLimitReached     bool
}

type purgeDanglingSigningKeyshareSecretsCollectionResult struct {
	CandidateCount       int
	FoundFullDeleteBatch bool
	ScanLimitReached     bool
}

func purgeDanglingSigningKeyshareSecretsBatch(
	ctx context.Context,
	cutoffID uuid.UUID,
	batchSize int,
) (purgeDanglingSigningKeyshareSecretsBatchResult, error) {
	mainDB, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return purgeDanglingSigningKeyshareSecretsBatchResult{}, fmt.Errorf("failed to get main db from context: %w", err)
	}

	ephemeralDB, err := entephemeral.GetDbFromContext(ctx)
	if err != nil {
		if errors.Is(err, entephemeral.ErrNoTransactionProvider) {
			return purgeDanglingSigningKeyshareSecretsBatchResult{}, nil
		}
		return purgeDanglingSigningKeyshareSecretsBatchResult{}, fmt.Errorf("failed to get or create current ephemeral db for request: %w", err)
	}

	secretIDsToDelete, collectionResult, err := collectDanglingSigningKeyshareSecretIDs(
		ctx,
		mainDB,
		ephemeralDB,
		cutoffID,
		batchSize,
	)
	if err != nil {
		return purgeDanglingSigningKeyshareSecretsBatchResult{}, err
	}

	result := purgeDanglingSigningKeyshareSecretsBatchResult{
		CandidateCount:       collectionResult.CandidateCount,
		FoundFullDeleteBatch: collectionResult.FoundFullDeleteBatch,
		ScanLimitReached:     collectionResult.ScanLimitReached,
	}
	if len(secretIDsToDelete) == 0 {
		return result, nil
	}

	deletedCount, err := ephemeralDB.SigningKeyshareSecret.Delete().
		Where(signingkeysharesecret.IDIn(secretIDsToDelete...)).
		Exec(ctx)
	if err != nil {
		return purgeDanglingSigningKeyshareSecretsBatchResult{}, fmt.Errorf("failed to delete dangling signing keyshare secrets: %w", err)
	}
	result.DeletedCount = deletedCount

	return result, nil
}

func collectDanglingSigningKeyshareSecretIDs(
	ctx context.Context,
	mainDB *ent.Client,
	ephemeralDB *entephemeral.Client,
	cutoffID uuid.UUID,
	batchSize int,
) ([]uuid.UUID, purgeDanglingSigningKeyshareSecretsCollectionResult, error) {
	maxCandidateScanCount := batchSize * purgeDanglingSigningKeyshareSecretsMaxScanMultiplier
	secretIDsToDelete := make([]uuid.UUID, 0, batchSize)
	candidateCount := 0
	var lastSeenID *uuid.UUID

	for len(secretIDsToDelete) < batchSize && candidateCount < maxCandidateScanCount {
		queryLimit := batchSize
		remainingScanCapacity := maxCandidateScanCount - candidateCount
		if remainingScanCapacity < queryLimit {
			queryLimit = remainingScanCapacity
		}

		query := ephemeralDB.SigningKeyshareSecret.Query().
			Where(signingkeysharesecret.IDLT(cutoffID)).
			Order(signingkeysharesecret.ByID(sql.OrderAsc())).
			Limit(queryLimit)
		if lastSeenID != nil {
			query = query.Where(signingkeysharesecret.IDGT(*lastSeenID))
		}

		candidates, err := query.
			Select(signingkeysharesecret.FieldID, signingkeysharesecret.FieldSigningKeyshareID, signingkeysharesecret.FieldVersion).
			All(ctx)
		if err != nil {
			return nil, purgeDanglingSigningKeyshareSecretsCollectionResult{}, fmt.Errorf("failed to query aged signing keyshare secrets: %w", err)
		}
		if len(candidates) == 0 {
			break
		}

		candidateCount += len(candidates)
		batchDanglingIDs, err := getDanglingSigningKeyshareSecretIDs(ctx, mainDB, candidates)
		if err != nil {
			return nil, purgeDanglingSigningKeyshareSecretsCollectionResult{
				CandidateCount: candidateCount,
			}, err
		}

		remainingDeleteCapacity := batchSize - len(secretIDsToDelete)
		if len(batchDanglingIDs) > remainingDeleteCapacity {
			batchDanglingIDs = batchDanglingIDs[:remainingDeleteCapacity]
		}
		secretIDsToDelete = append(secretIDsToDelete, batchDanglingIDs...)

		lastCandidateID := candidates[len(candidates)-1].ID
		lastSeenID = &lastCandidateID
		if len(candidates) < queryLimit {
			break
		}
	}

	result := purgeDanglingSigningKeyshareSecretsCollectionResult{
		CandidateCount:       candidateCount,
		FoundFullDeleteBatch: len(secretIDsToDelete) == batchSize,
	}
	result.ScanLimitReached = candidateCount >= maxCandidateScanCount && !result.FoundFullDeleteBatch
	return secretIDsToDelete, result, nil
}

func getDanglingSigningKeyshareSecretIDs(
	ctx context.Context,
	mainDB *ent.Client,
	candidates []*entephemeral.SigningKeyshareSecret,
) ([]uuid.UUID, error) {
	signingKeyshareIDSet := make(map[uuid.UUID]struct{}, len(candidates))
	for _, secret := range candidates {
		signingKeyshareIDSet[secret.SigningKeyshareID] = struct{}{}
	}
	signingKeyshareIDs := make([]uuid.UUID, 0, len(signingKeyshareIDSet))
	for signingKeyshareID := range signingKeyshareIDSet {
		signingKeyshareIDs = append(signingKeyshareIDs, signingKeyshareID)
	}

	mainSigningKeyshares, err := mainDB.SigningKeyshare.Query().
		Where(signingkeyshare.IDIn(signingKeyshareIDs...)).
		Select(signingkeyshare.FieldID, signingkeyshare.FieldSecretVersion).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to query signing keyshares by id: %w", err)
	}

	mainSigningKeysharesByID := make(map[uuid.UUID]*ent.SigningKeyshare, len(mainSigningKeyshares))
	for _, sk := range mainSigningKeyshares {
		mainSigningKeysharesByID[sk.ID] = sk
	}

	secretIDsToDelete := make([]uuid.UUID, 0, len(candidates))
	for _, candidate := range candidates {
		sk, ok := mainSigningKeysharesByID[candidate.SigningKeyshareID]
		if !ok || sk.SecretVersion == nil || *sk.SecretVersion != candidate.Version {
			secretIDsToDelete = append(secretIDsToDelete, candidate.ID)
		}
	}

	return secretIDsToDelete, nil
}
