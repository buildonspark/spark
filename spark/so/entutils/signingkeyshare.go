package entutils

import (
	"context"
	"fmt"
	"math/big"

	"github.com/decred/dcrd/dcrec/secp256k1"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go/common"
	pbfrost "github.com/lightsparkdev/spark-go/proto/frost"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/dkg"
	"github.com/lightsparkdev/spark-go/so/ent"
	"github.com/lightsparkdev/spark-go/so/ent/schema"
	"github.com/lightsparkdev/spark-go/so/ent/signingkeyshare"
)

// GetUnusedSigningKeyshares returns the available keyshares for the given coordinator index.
func GetUnusedSigningKeyshares(ctx context.Context, config *so.Config, keyshareCount int) ([]*ent.SigningKeyshare, error) {
	signingKeyshares, err := common.GetDbFromContext(ctx).SigningKeyshare.Query().Where(
		signingkeyshare.StatusEQ(schema.KeyshareStatusAvailable),
		signingkeyshare.CoordinatorIndexEQ(config.Index),
	).Limit(keyshareCount).All(ctx)
	if err != nil {
		return nil, err
	}

	if len(signingKeyshares) < keyshareCount {
		return nil, fmt.Errorf("not enough keyshares available")
	}

	return signingKeyshares, nil
}

// MarkSigningKeysharesAsUsed marks the given keyshares as used. If any of the keyshares are not
// found or not available, it returns an error.
func MarkSigningKeysharesAsUsed(ctx context.Context, config *so.Config, ids []uuid.UUID) error {
	db := common.GetDbFromContext(ctx)

	signingKeyshares, err := db.SigningKeyshare.
		Query().
		Where(
			signingkeyshare.IDIn(ids...),
			signingkeyshare.StatusEQ(schema.KeyshareStatusAvailable),
		).
		All(ctx)
	if err != nil {
		return err
	}

	if len(signingKeyshares) != len(ids) {
		return fmt.Errorf("some keyshares are not available in ", ids)
	}

	_, err = db.SigningKeyshare.
		Update().
		Where(signingkeyshare.IDIn(ids...)).
		SetStatus(schema.KeyshareStatusInUse).
		Save(ctx)
	if err != nil {
		return err
	}

	// Check if we need to generate more keyshares after marking some as used
	go dkg.RunDKGIfNeeded(db, config)

	return nil
}

// GetKeyPackage returns the key package for the given keyshare ID.
func GetKeyPackage(ctx context.Context, config *so.Config, keyshareID uuid.UUID) (*pbfrost.KeyPackage, error) {
	keyshare, err := common.GetDbFromContext(ctx).SigningKeyshare.Get(ctx, keyshareID)
	if err != nil {
		return nil, err
	}

	keyPackage := &pbfrost.KeyPackage{
		Identifier:   config.Identifier,
		SecretShare:  keyshare.SecretShare,
		PublicShares: keyshare.PublicShares,
		PublicKey:    keyshare.PublicKey,
		MinSigners:   keyshare.MinSigners,
	}

	return keyPackage, nil
}

// GetKeyPackages returns the key packages for the given keyshare IDs.
func GetKeyPackages(ctx context.Context, config *so.Config, keyshareIDs []uuid.UUID) (map[uuid.UUID]*pbfrost.KeyPackage, error) {
	keyshares, err := common.GetDbFromContext(ctx).SigningKeyshare.Query().Where(
		signingkeyshare.IDIn(keyshareIDs...),
	).All(ctx)
	if err != nil {
		return nil, err
	}

	keyPackages := make(map[uuid.UUID]*pbfrost.KeyPackage, len(keyshares))
	for _, keyshare := range keyshares {
		keyPackages[keyshare.ID] = &pbfrost.KeyPackage{
			Identifier:   config.Identifier,
			SecretShare:  keyshare.SecretShare,
			PublicShares: keyshare.PublicShares,
			PublicKey:    keyshare.PublicKey,
			MinSigners:   keyshare.MinSigners,
		}
	}

	return keyPackages, nil
}

// GetKeyPackagesArray returns the keyshares for the given keyshare IDs.
// The order of the keyshares in the result is the same as the order of the keyshare IDs.
func GetKeyPackagesArray(ctx context.Context, keyshareIDs []uuid.UUID) ([]*ent.SigningKeyshare, error) {
	keyshares, err := common.GetDbFromContext(ctx).SigningKeyshare.Query().Where(
		signingkeyshare.IDIn(keyshareIDs...),
	).All(ctx)
	if err != nil {
		return nil, err
	}

	keysharesMap := make(map[uuid.UUID]*ent.SigningKeyshare, len(keyshares))
	for _, keyshare := range keyshares {
		keysharesMap[keyshare.ID] = keyshare
	}

	result := make([]*ent.SigningKeyshare, len(keyshareIDs))
	for i, id := range keyshareIDs {
		result[i] = keysharesMap[id]
	}

	return result, nil
}

// CalculateAndStoreLastKey calculates the last key from the given keyshares and stores it in the database.
// The target = sum(keyshares) + last_key
func CalculateAndStoreLastKey(ctx context.Context, config *so.Config, target *ent.SigningKeyshare, keyshares []*ent.SigningKeyshare, id uuid.UUID) (*ent.SigningKeyshare, error) {
	privateShares := make([][]byte, len(keyshares))
	for i, keyshare := range keyshares {
		privateShares[i] = keyshare.SecretShare
	}

	sum, err := common.SumOfPrivateKeys(privateShares)
	if err != nil {
		return nil, err
	}

	tweak := new(big.Int).Neg(sum)
	tweakPriv, _ := secp256k1.PrivKeyFromBytes(tweak.Bytes())
	tweakBytes := tweakPriv.Serialize()

	lastSecretShare, err := common.AddPrivateKeys(target.SecretShare, tweakBytes)
	if err != nil {
		return nil, err
	}

	verifyingKey, err := common.ApplyAdditiveTweakToPublicKey(target.PublicKey, tweakBytes)
	if err != nil {
		return nil, err
	}

	publicShares := make(map[string][]byte)
	for i, publicShare := range target.PublicShares {
		newShare, err := common.ApplyAdditiveTweakToPublicKey(publicShare, tweakBytes)
		if err != nil {
			return nil, err
		}
		publicShares[i] = newShare
	}

	db := common.GetDbFromContext(ctx)
	lastKey, err := db.SigningKeyshare.Create().
		SetID(id).
		SetSecretShare(lastSecretShare).
		SetPublicShares(publicShares).
		SetPublicKey(verifyingKey).
		SetStatus(schema.KeyshareStatusInUse).
		SetCoordinatorIndex(0).
		SetMinSigners(target.MinSigners).
		Save(ctx)
	if err != nil {
		return nil, err
	}

	return lastKey, nil
}
