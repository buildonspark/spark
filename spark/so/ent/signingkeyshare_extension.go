package ent

import (
	"bytes"
	"context"
	"fmt"
	"log"

	"github.com/decred/dcrd/dcrec/secp256k1"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go"
	"github.com/lightsparkdev/spark-go/common"
	pbdkg "github.com/lightsparkdev/spark-go/proto/dkg"
	pbfrost "github.com/lightsparkdev/spark-go/proto/frost"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent/schema"
	"github.com/lightsparkdev/spark-go/so/ent/signingkeyshare"
)

// TweakKeyShare tweaks the given keyshare with the given tweak, updates the keyshare in the database and returns the updated keyshare.
func (keyshare *SigningKeyshare) TweakKeyShare(ctx context.Context, shareTweak []byte, pubkeyTweak []byte, pubkeySharesTweak map[string][]byte) (*SigningKeyshare, error) {
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
		Threshold:        keyshare.MinSigners,
	}
}

// GetUnusedSigningKeyshares returns the available keyshares for the given coordinator index.
func GetUnusedSigningKeyshares(ctx context.Context, dbClient *Client, config *so.Config, keyshareCount int) ([]*SigningKeyshare, error) {
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
		return nil, tx.Rollback()
	}

	if len(signingKeyshares) < keyshareCount {
		go func() {
			err := RunDKG(context.Background(), config)
			if err != nil {
				log.Printf("Error running DKG: %v", err)
			}
		}()
		return nil, tx.Rollback()
	}

	for _, keyshare := range signingKeyshares {
		_, err := keyshare.Update().
			SetStatus(schema.KeyshareStatusInUse).
			Save(ctx)
		if err != nil {
			return nil, tx.Rollback()
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
func MarkSigningKeysharesAsUsed(ctx context.Context, config *so.Config, ids []uuid.UUID) (map[uuid.UUID]*SigningKeyshare, error) {
	db := GetDbFromContext(ctx)
	log.Printf("Marking keyshares as used: %v", ids)

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

	remainingKeyshares, err := db.SigningKeyshare.Query().Where(
		signingkeyshare.StatusEQ(schema.KeyshareStatusAvailable),
		signingkeyshare.CoordinatorIndexEQ(config.Index),
	).Count(context.Background())
	if err != nil {
		return nil, err
	}
	log.Printf("Remaining keyshares: %v", remainingKeyshares)
	if uint64(remainingKeyshares) < spark.DKGKeyThreshold && (config.DKGLimitOverride == 0 || uint64(remainingKeyshares) < config.DKGLimitOverride) {
		go func() {
			err := RunDKG(context.Background(), config)
			if err != nil {
				log.Printf("Error running DKG: %v", err)
			}
		}()
	}

	// Return the keyshares that were marked as used in case the caller wants to make use of them.
	return keysharesMap, nil
}

// GetKeyPackage returns the key package for the given keyshare ID.
func GetKeyPackage(ctx context.Context, config *so.Config, keyshareID uuid.UUID) (*pbfrost.KeyPackage, error) {
	keyshare, err := GetDbFromContext(ctx).SigningKeyshare.Get(ctx, keyshareID)
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
			MinSigners:   keyshare.MinSigners,
		}
	}

	return keyPackages, nil
}

// GetKeyPackagesArray returns the keyshares for the given keyshare IDs.
// The order of the keyshares in the result is the same as the order of the keyshare IDs.
func GetKeyPackagesArray(ctx context.Context, keyshareIDs []uuid.UUID) ([]*SigningKeyshare, error) {
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
func CalculateAndStoreLastKey(ctx context.Context, config *so.Config, target *SigningKeyshare, keyshares []*SigningKeyshare, id uuid.UUID) (*SigningKeyshare, error) {
	if len(keyshares) == 0 {
		return target, nil
	}

	log.Printf("Calculating last key for keyshares: %v", keyshares)
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
	if bytes.Compare(verifyLastKey, target.SecretShare) != 0 {
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
	if bytes.Compare(verifyVerifyingKey, target.PublicKey) != 0 {
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
func AggregateKeyshares(ctx context.Context, config *so.Config, keyshares []*SigningKeyshare, updateKeyshareID uuid.UUID) (*SigningKeyshare, error) {
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
func RunDKGIfNeeded(db *Client, config *so.Config) error {
	count, err := db.SigningKeyshare.Query().Where(
		signingkeyshare.StatusEQ(schema.KeyshareStatusAvailable),
		signingkeyshare.CoordinatorIndexEQ(config.Index),
		signingkeyshare.IDGT(uuid.MustParse("01954639-8d50-7e47-b3f0-ddb307fab7c2")),
	).Count(context.Background())
	if err != nil {
		return err
	}
	if config.DKGLimitOverride > 0 && uint64(count) >= config.DKGLimitOverride {
		return nil
	}
	if uint64(count) >= spark.DKGKeyThreshold {
		return nil
	}

	return RunDKG(context.Background(), config)
}

func RunDKG(ctx context.Context, config *so.Config) error {
	connection, err := common.NewGRPCConnectionWithCert(config.DKGCoordinatorAddress, config.SigningOperatorMap[config.Identifier].CertPath)
	if err != nil {
		log.Printf("Failed to create connection to DKG coordinator: %v, cert path: %v", err, config.SigningOperatorMap[config.Identifier].CertPath)
		return err
	}
	defer connection.Close()
	client := pbdkg.NewDKGServiceClient(connection)

	_, err = client.StartDkg(ctx, &pbdkg.StartDkgRequest{
		Count: spark.DKGKeyCount,
	})
	if err != nil {
		log.Printf("Failed to start DKG: %v", err)
		return err
	}

	return nil
}
