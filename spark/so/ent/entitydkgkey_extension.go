package ent

import (
	"context"
	"fmt"

	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so"
)

// GetEntityDkgKeyPublicKey fetches the entity DKG key and returns its associated public key.
// Returns an error if no entity DKG key is found, if there are multiple entity DKG keys,
// or if the SigningKeyshare is not loaded.
func GetEntityDkgKeyPublicKey(ctx context.Context, db *Client) (keys.Public, error) {
	entityDkgKey, err := db.EntityDkgKey.Query().
		WithSigningKeyshare().
		Only(ctx)
	if err != nil {
		if IsNotFound(err) {
			return keys.Public{}, fmt.Errorf("entity DKG key not found")
		}
		if IsNotSingular(err) {
			return keys.Public{}, fmt.Errorf("multiple entity DKG keys found, expected exactly one")
		}
		return keys.Public{}, fmt.Errorf("failed to query entity DKG key: %w", err)
	}

	signingKeyshare, err := entityDkgKey.Edges.SigningKeyshareOrErr()
	if err != nil {
		return keys.Public{}, fmt.Errorf("failed to get signing keyshare from entity DKG key: %w", err)
	}

	return signingKeyshare.PublicKey, nil
}

// CreateEntityDkgKeyWithUnusedSigningKeyshare creates a new entity DKG key using an unused signing keyshare.
// Returns the entity DKG key or an error if there's an error creating the entity DKG key or reserving the keyshare.
func CreateEntityDkgKeyWithUnusedSigningKeyshare(ctx context.Context, config *so.Config) (*EntityDkgKey, error) {
	tx, err := GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get database connection: %w", err)
	}

	keyshares, err := getUnusedSigningKeysharesTx(ctx, tx, config, 1)
	if err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			return nil, fmt.Errorf("failed to rollback transaction after error: %w (original error: %w)", rollbackErr, err)
		}
		return nil, fmt.Errorf("failed to get unused signing keyshares: %w", err)
	}

	if len(keyshares) == 0 {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			return nil, fmt.Errorf("failed to rollback transaction: %w", rollbackErr)
		}
		return nil, fmt.Errorf("no signing keyshares available yet")
	}

	keyshare := keyshares[0]

	// Create the entity DKG key
	entityDkgKey, err := tx.EntityDkgKey.Create().
		SetSigningKeyshare(keyshare).
		Save(ctx)
	if err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			return nil, fmt.Errorf("failed to rollback transaction after error: %w (original error: %w)", rollbackErr, err)
		}
		return nil, fmt.Errorf("failed to create entity DKG key: %w", err)
	}

	err = tx.Commit()
	if err != nil {
		return nil, fmt.Errorf("failed to commit transaction: %w", err)
	}

	return entityDkgKey, nil
}
