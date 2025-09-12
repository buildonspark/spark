package ent

import (
	"context"
	"fmt"

	"github.com/lightsparkdev/spark/common/keys"
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
