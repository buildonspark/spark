package ent

import (
	"bytes"
	"context"
	"fmt"

	"github.com/decred/dcrd/dcrec/secp256k1"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/logging"
	pbdkg "github.com/lightsparkdev/spark/proto/dkg"
	pbfrost "github.com/lightsparkdev/spark/proto/frost"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent/schema"
	"github.com/lightsparkdev/spark/so/ent/signingkeyshare"
)

// TweakKeyShare tweaks the given keyshare with the given tweak, updates the keyshare in the database and returns the updated keyshare.
func (keyshare *SigningKeyshare) TweakKeyShare(ctx context.Context, shareTweak []byte, pubkeyTweak []byte, pubkeySharesTweak map[string][]byte) (*SigningKeyshare, error) {
	ctx, span := tracer.Start(ctx, "SigningKeyshare.TweakKeyShare")
	defer span.End()

	tweakPriv, _ := secp256k1.PrivKeyFromBytes(shareTweak)
	tweakBytes := tweakPriv.Serialize()

	newSecretShare, err := common.AddPrivateKeys(keyshare.SecretShare, tweakBytes)
	if err != nil {
		return nil, err
	}

	newPublicKey, err := common.AddPublicKeys(keyshare.PublicKey, pubkeyTweak)
	if err != nil {
		return nil, err
	}

	newPublicShares := make(map[string][]byte)
	for i, publicShare := range keyshare.PublicShares {
		newPublicShares[i], err = common.AddPublicKeys(publicShare, pubkeySharesTweak[i])
		if err != nil {
			return nil, err
		}
	}

	return keyshare.Update().
		SetSecretShare(newSecretShare).
		SetPublicKey(newPublicKey).
		SetPublicShares(newPublicShares).
		Save(ctx)
}

// MarshalProto converts a SigningKeyshare to a spark protobuf SigningKeyshare.
func (keyshare *SigningKeyshare) MarshalProto() *pb.SigningKeyshare {
	ownerIdentifiers := make([]string, 0)
	for identifier := range keyshare.PublicShares {
		ownerIdentifiers = append(ownerIdentifiers, identifier)
	}

	return &pb.SigningKeyshare{
		OwnerIdentifiers: ownerIdentifiers,
		Threshold:        uint32(keyshare.MinSigners),
	}
}

// GetUnusedSigningKeyshares returns the available keyshares for the given coordinator index.
func GetUnusedSigningKeyshares(ctx context.Context, dbClient *Client, config *so.Config, keyshareCount int) ([]*SigningKeyshare, error) {
	ctx, span := tracer.Start(ctx, "SigningKeyshare.GetUnusedSigningKeyshares")
	defer span.End()

	logger := logging.GetLoggerFromContext(ctx)

	tx, err := dbClient.Tx(ctx)
	if err != nil {
		return nil, err
	}

	signingKeyshares, err := tx.SigningKeyshare.Query().Where(
		signingkeyshare.StatusEQ(schema.KeyshareStatusAvailable),
		signingkeyshare.CoordinatorIndexEQ(config.Index),
		signingkeyshare.IDGT(uuid.MustParse("01954639-8d50-7e47-b3f0-ddb307fab7c2")),
	).
		Limit(keyshareCount).
		ForUpdate().
		All(ctx)
	if err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			logger.Error("Failed to rollback transaction", "error", rollbackErr)
		}

		return nil, fmt.Errorf("failed to get signing keyshares: %w", err)
	}

	if len(signingKeyshares) < keyshareCount {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			logger.Error("Failed to rollback transaction", "error", rollbackErr)
		}

		return nil, fmt.Errorf("not enough signing keyshares available (needed %d, got %d)", keyshareCount, len(signingKeyshares))
	}

	for _, keyshare := range signingKeyshares {
		_, err := keyshare.Update().
			SetStatus(schema.KeyshareStatusInUse).
			Save(ctx)
		if err != nil {
			if rollbackErr := tx.Rollback(); rollbackErr != nil {
				logger.Error("Failed to rollback transaction", "error", rollbackErr)
			}

			return nil, fmt.Errorf("failed to update signing keyshare status: %w", err)
		}
	}

	err = tx.Commit()
	if err != nil {
		return nil, err
	}

	return signingKeyshares, nil
}

// MarkSigningKeysharesAsUsed marks the given keyshares as used. If any of the keyshares are not
// found or not available, it returns an error.
func MarkSigningKeysharesAsUsed(ctx context.Context, _ *so.Config, ids []uuid.UUID) (map[uuid.UUID]*SigningKeyshare, error) {
	ctx, span := tracer.Start(ctx, "SigningKeyshare.MarkSigningKeysharesAsUsed")
	defer span.End()

	logger := logging.GetLoggerFromContext(ctx)
	db := GetDbFromContext(ctx)
	logger.Info("Marking keyshares as used: %v", "keyshare_ids", ids)

	keysharesMap, err := GetSigningKeysharesMap(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("failed to check for existing in use keyshares: %w", err)
	}
	if len(keysharesMap) != len(ids) {
		return nil, fmt.Errorf("some shares are already in use: %d", len(ids)-len(keysharesMap))
	}

	// If these keyshares are not already in use, proceed with the update.
	count, err := db.SigningKeyshare.
		Update().
		Where(signingkeyshare.IDIn(ids...)).
		SetStatus(schema.KeyshareStatusInUse).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to update keyshares to in use: %w", err)
	}

	if count != len(ids) {
		return nil, fmt.Errorf("some keyshares are not available in %v", ids)
	}

	// Return the keyshares that were marked as used in case the caller wants to make use of them.
	return keysharesMap, nil
}

// GetKeyPackage returns the key package for the given keyshare ID.
func GetKeyPackage(ctx context.Context, config *so.Config, keyshareID uuid.UUID) (*pbfrost.KeyPackage, error) {
	ctx, span := tracer.Start(ctx, "SigningKeyshare.GetKeyPackage")
	defer span.End()

	keyshare, err := GetDbFromContext(ctx).SigningKeyshare.Get(ctx, keyshareID)
	if err != nil {
		return nil, err
	}

	keyPackage := &pbfrost.KeyPackage{
		Identifier:   config.Identifier,
		SecretShare:  keyshare.SecretShare,
		PublicShares: keyshare.PublicShares,
		PublicKey:    keyshare.PublicKey,
		MinSigners:   uint32(keyshare.MinSigners),
	}

	return keyPackage, nil
}

// GetKeyPackages returns the key packages for the given keyshare IDs.
func GetKeyPackages(ctx context.Context, config *so.Config, keyshareIDs []uuid.UUID) (map[uuid.UUID]*pbfrost.KeyPackage, error) {
	ctx, span := tracer.Start(ctx, "SigningKeyshare.GetKeyPackages")
	defer span.End()

	keyshares, err := GetDbFromContext(ctx).SigningKeyshare.Query().Where(
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
			MinSigners:   uint32(keyshare.MinSigners),
		}
	}

	return keyPackages, nil
}

// GetKeyPackagesArray returns the keyshares for the given keyshare IDs.
// The order of the keyshares in the result is the same as the order of the keyshare IDs.
func GetKeyPackagesArray(ctx context.Context, keyshareIDs []uuid.UUID) ([]*SigningKeyshare, error) {
	ctx, span := tracer.Start(ctx, "SigningKeyshare.GetKeyPackagesArray")
	defer span.End()

	keysharesMap, err := GetSigningKeysharesMap(ctx, keyshareIDs)
	if err != nil {
		return nil, err
	}

	result := make([]*SigningKeyshare, len(keyshareIDs))
	for i, id := range keyshareIDs {
		result[i] = keysharesMap[id]
	}

	return result, nil
}

// GetSigningKeysharesMap returns the keyshares for the given keyshare IDs.
// The order of the keyshares in the result is the same as the order of the keyshare IDs.
func GetSigningKeysharesMap(ctx context.Context, keyshareIDs []uuid.UUID) (map[uuid.UUID]*SigningKeyshare, error) {
	ctx, span := tracer.Start(ctx, "SigningKeyshare.GetSigningKeysharesMap")
	defer span.End()

	keyshares, err := GetDbFromContext(ctx).SigningKeyshare.Query().Where(
		signingkeyshare.IDIn(keyshareIDs...),
	).All(ctx)
	if err != nil {
		return nil, err
	}

	keysharesMap := make(map[uuid.UUID]*SigningKeyshare, len(keyshares))
	for _, keyshare := range keyshares {
		keysharesMap[keyshare.ID] = keyshare
	}

	return keysharesMap, nil
}

func sumOfSigningKeyshares(keyshares []*SigningKeyshare) (*SigningKeyshare, error) {
	resultKeyshares := *keyshares[0]
	for i, keyshare := range keyshares {
		if i == 0 {
			continue
		}
		sum, err := common.AddPrivateKeys(resultKeyshares.SecretShare, keyshare.SecretShare)
		if err != nil {
			return nil, err
		}
		resultKeyshares.SecretShare = sum

		verifySum, err := common.AddPublicKeys(resultKeyshares.PublicKey, keyshare.PublicKey)
		if err != nil {
			return nil, err
		}
		resultKeyshares.PublicKey = verifySum

		for i, publicShare := range resultKeyshares.PublicShares {
			newShare, err := common.AddPublicKeys(publicShare, keyshare.PublicShares[i])
			if err != nil {
				return nil, err
			}
			resultKeyshares.PublicShares[i] = newShare
		}
	}

	return &resultKeyshares, nil
}

// CalculateAndStoreLastKey calculates the last key from the given keyshares and stores it in the database.
// The target = sum(keyshares) + last_key
func CalculateAndStoreLastKey(ctx context.Context, _ *so.Config, target *SigningKeyshare, keyshares []*SigningKeyshare, id uuid.UUID) (*SigningKeyshare, error) {
	ctx, span := tracer.Start(ctx, "SigningKeyshare.CalculateAndStoreLastKey")
	defer span.End()

	logger := logging.GetLoggerFromContext(ctx)

	if len(keyshares) == 0 {
		return target, nil
	}

	keyshareIDs := make([]uuid.UUID, len(keyshares))
	for i, keyshare := range keyshares {
		keyshareIDs[i] = keyshare.ID
	}

	logger.Info("Calculating last key for keyshares", "keyshare_ids", keyshareIDs)
	sumKeyshare, err := sumOfSigningKeyshares(keyshares)
	if err != nil {
		return nil, err
	}

	lastSecretShare, err := common.SubtractPrivateKeys(target.SecretShare, sumKeyshare.SecretShare)
	if err != nil {
		return nil, err
	}
	verifyLastKey, err := common.AddPrivateKeys(sumKeyshare.SecretShare, lastSecretShare)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(verifyLastKey, target.SecretShare) {
		return nil, fmt.Errorf("last key verification failed")
	}

	verifyingKey, err := common.SubtractPublicKeys(target.PublicKey, sumKeyshare.PublicKey)
	if err != nil {
		return nil, err
	}

	verifyVerifyingKey, err := common.AddPublicKeys(keyshares[0].PublicKey, verifyingKey)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(verifyVerifyingKey, target.PublicKey) {
		return nil, fmt.Errorf("verifying key verification failed")
	}

	publicShares := make(map[string][]byte)
	for i, publicShare := range target.PublicShares {
		newShare, err := common.SubtractPublicKeys(publicShare, sumKeyshare.PublicShares[i])
		if err != nil {
			return nil, err
		}
		publicShares[i] = newShare
	}

	db := GetDbFromContext(ctx)
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

// AggregateKeyshares aggregates the given keyshares and updates the keyshare in the database.
func AggregateKeyshares(ctx context.Context, _ *so.Config, keyshares []*SigningKeyshare, updateKeyshareID uuid.UUID) (*SigningKeyshare, error) {
	ctx, span := tracer.Start(ctx, "SigningKeyshare.AggregateKeyshares")
	defer span.End()

	sumKeyshare, err := sumOfSigningKeyshares(keyshares)
	if err != nil {
		return nil, err
	}

	db := GetDbFromContext(ctx)
	updateKeyshare, err := db.SigningKeyshare.UpdateOneID(updateKeyshareID).
		SetSecretShare(sumKeyshare.SecretShare).
		SetPublicKey(sumKeyshare.PublicKey).
		SetPublicShares(sumKeyshare.PublicShares).
		Save(ctx)
	if err != nil {
		return nil, err
	}

	return updateKeyshare, nil
}

// RunDKGIfNeeded checks if the keyshare count is below the threshold and runs DKG if needed.
func RunDKGIfNeeded(ctx context.Context, db *Client, config *so.Config) error {
	ctx, span := tracer.Start(ctx, "SigningKeyshare.RunDKGIfNeeded")
	defer span.End()

	count, err := db.SigningKeyshare.Query().Where(
		signingkeyshare.StatusEQ(schema.KeyshareStatusAvailable),
		signingkeyshare.CoordinatorIndexEQ(config.Index),
		signingkeyshare.IDGT(uuid.MustParse("01954639-8d50-7e47-b3f0-ddb307fab7c2")),
	).Count(ctx)
	if err != nil {
		return err
	}
	if config.DKGLimitOverride > 0 && uint64(count) >= config.DKGLimitOverride {
		return nil
	}
	if uint64(count) >= spark.DKGKeyThreshold {
		return nil
	}

	return RunDKG(ctx, config)
}

func RunDKG(ctx context.Context, config *so.Config) error {
	ctx, span := tracer.Start(ctx, "SigningKeyshare.RunDKG")
	defer span.End()

	logger := logging.GetLoggerFromContext(ctx)

	connection, err := common.NewGRPCConnection(
		config.DKGCoordinatorAddress,
		config.SigningOperatorMap[config.Identifier].CertPath,
		nil,
	)
	if err != nil {
		logger.Error("Failed to create connection to DKG coordinator", "error", err)
		return err
	}
	defer connection.Close()
	client := pbdkg.NewDKGServiceClient(connection)

	_, err = client.StartDkg(ctx, &pbdkg.StartDkgRequest{
		Count: spark.DKGKeyCount,
	})
	if err != nil {
		logger.Error("Failed to start DKG", "error", err)
		return err
	}

	return nil
}
