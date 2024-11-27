package entutils

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go/common"
	pb "github.com/lightsparkdev/spark-go/proto"
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
func GetKeyPackage(ctx context.Context, config *so.Config, keyshareID uuid.UUID) (*pb.KeyPackage, error) {
	keyshare, err := common.GetDbFromContext(ctx).SigningKeyshare.Get(ctx, keyshareID)
	if err != nil {
		return nil, err
	}

	keyPackage := &pb.KeyPackage{
		Identifier:   config.Identifier,
		SecretShare:  keyshare.SecretShare,
		PublicShares: keyshare.PublicShares,
		PublicKey:    keyshare.PublicKey,
		MinSigners:   keyshare.MinSigners,
	}

	return keyPackage, nil
}

// GetKeyPackages returns the key packages for the given keyshare IDs.
func GetKeyPackages(ctx context.Context, config *so.Config, keyshareIDs []uuid.UUID) (map[uuid.UUID]*pb.KeyPackage, error) {
	keyshares, err := common.GetDbFromContext(ctx).SigningKeyshare.Query().Where(
		signingkeyshare.IDIn(keyshareIDs...),
	).All(ctx)
	if err != nil {
		return nil, err
	}

	keyPackages := make(map[uuid.UUID]*pb.KeyPackage, len(keyshares))
	for _, keyshare := range keyshares {
		keyPackages[keyshare.ID] = &pb.KeyPackage{
			Identifier:   config.Identifier,
			SecretShare:  keyshare.SecretShare,
			PublicShares: keyshare.PublicShares,
			PublicKey:    keyshare.PublicKey,
			MinSigners:   keyshare.MinSigners,
		}
	}

	return keyPackages, nil
}
