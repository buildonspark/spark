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
)

func purgeDanglingSigningKeyshareSecretsBatch(
	ctx context.Context,
	cutoffID uuid.UUID,
	batchSize int,
) (candidateCount int, deletedCount int, err error) {
	mainDB, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to get main db from context: %w", err)
	}

	ephemeralDB, err := entephemeral.GetDbFromContext(ctx)
	if err != nil {
		if errors.Is(err, entephemeral.ErrNoTransactionProvider) {
			return 0, 0, nil
		}
		return 0, 0, fmt.Errorf("failed to get or create current ephemeral db for request: %w", err)
	}

	candidates, err := ephemeralDB.SigningKeyshareSecret.Query().
		Where(signingkeysharesecret.IDLT(cutoffID)).
		Order(signingkeysharesecret.ByID(sql.OrderAsc())).
		Limit(batchSize).
		Select(signingkeysharesecret.FieldID, signingkeysharesecret.FieldSigningKeyshareID, signingkeysharesecret.FieldVersion).
		All(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to query aged signing keyshare secrets: %w", err)
	}
	if len(candidates) == 0 {
		return 0, 0, nil
	}

	signingKeyshareIDSet := make(map[uuid.UUID]struct{}, len(candidates))
	for _, secret := range candidates {
		signingKeyshareIDSet[secret.SigningKeyshareID] = struct{}{}
	}
	signingKeyshareIDs := make([]uuid.UUID, 0, len(signingKeyshareIDSet))
	for signingKeyshareID := range signingKeyshareIDSet {
		signingKeyshareIDs = append(signingKeyshareIDs, signingKeyshareID)
	}

	secretIDsToDelete, err := getDanglingSigningKeyshareSecretIDs(ctx, mainDB, candidates, signingKeyshareIDs)
	if err != nil {
		return len(candidates), 0, err
	}
	if len(secretIDsToDelete) == 0 {
		return len(candidates), 0, nil
	}

	deletedCount, err = ephemeralDB.SigningKeyshareSecret.Delete().
		Where(signingkeysharesecret.IDIn(secretIDsToDelete...)).
		Exec(ctx)
	if err != nil {
		return len(candidates), 0, fmt.Errorf("failed to delete dangling signing keyshare secrets: %w", err)
	}

	return len(candidates), deletedCount, nil
}

func getDanglingSigningKeyshareSecretIDs(
	ctx context.Context,
	mainDB *ent.Client,
	candidates []*entephemeral.SigningKeyshareSecret,
	signingKeyshareIDs []uuid.UUID,
) ([]uuid.UUID, error) {
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
