package ent_utils

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	pb "github.com/lightsparkdev/spark-go/proto"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/dkg"
	"github.com/lightsparkdev/spark-go/so/ent"
	"github.com/lightsparkdev/spark-go/so/ent/schema"
	"github.com/lightsparkdev/spark-go/so/ent/signingkeyshare"
)

// GetUnusedSigningKeyshares returns the available keyshares for the given coordinator index.
func GetUnusedSigningKeyshares(ctx context.Context, config *so.Config, keyshareCount int) ([]*ent.SigningKeyshare, error) {
	db, err := ent.Open(config.DatabaseDriver(), config.DatabasePath)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	signingKeyshares, err := db.SigningKeyshare.Query().Where(
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
	db, err := ent.Open(config.DatabaseDriver(), config.DatabasePath)
	if err != nil {
		return err
	}
	defer db.Close()

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
		return fmt.Errorf("some keyshares are not available")
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
	go dkg.RunDKGIfNeeded(config)

	return nil
}

func GetKeyPackage(ctx context.Context, config *so.Config, keyshareID uuid.UUID) (*pb.KeyPackage, error) {
	db, err := ent.Open(config.DatabaseDriver(), config.DatabasePath)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	keyshare, err := db.SigningKeyshare.Get(ctx, keyshareID)
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
