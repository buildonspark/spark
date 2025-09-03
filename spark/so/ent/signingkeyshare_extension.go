package ent

import (
	"bytes"
	"context"
	"fmt"

	"github.com/lightsparkdev/spark/common/keys"

	"entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/logging"
	pbdkg "github.com/lightsparkdev/spark/proto/dkg"
	pbfrost "github.com/lightsparkdev/spark/proto/frost"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/signingkeyshare"
	"github.com/lightsparkdev/spark/so/knobs"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// DefaultMinAvailableKeys is the minimum number of DKG keys that should be available at all times.
// If the number of available keys drops below this threshold, DKG will be triggered to generate new
// keys.
var DefaultMinAvailableKeys = 100_000

// TweakKeyShare tweaks the given keyshare with the given tweak, updates the keyshare in the database and returns the updated keyshare.
func (sk *SigningKeyshare) TweakKeyShare(ctx context.Context, shareTweak keys.Private, pubKeyTweak keys.Public, pubKeySharesTweak map[string]keys.Public) (*SigningKeyshare, error) {
	ctx, span := tracer.Start(ctx, "SigningKeyshare.TweakKeyShare")
	defer span.End()

	secretShare, err := keys.ParsePrivateKey(sk.SecretShare)
	if err != nil {
		return nil, fmt.Errorf("unable to parse secret share: %w", err)
	}
	newSecretShare := secretShare.Add(shareTweak)

	pubKey, err := keys.ParsePublicKey(sk.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("unable to parse public key: %w", err)
	}
	newPubKey := pubKey.Add(pubKeyTweak)

	newPublicShares := make(map[string]keys.Public)
	for id, publicShareBytes := range sk.PublicShares {
		pubShare, err := keys.ParsePublicKey(publicShareBytes)
		if err != nil {
			return nil, fmt.Errorf("unable to parse public share %s: %w", id, err)
		}
		newPublicShares[id] = pubShare.Add(pubKeySharesTweak[id])
	}

	return sk.Update().
		SetSecretShare(newSecretShare.Serialize()).
		SetPublicKey(newPubKey.Serialize()).
		SetPublicShares(keys.ToBytesMap(newPublicShares)).
		Save(ctx)
}

// MarshalProto converts a SigningKeyshare to a spark protobuf SigningKeyshare.
func (sk *SigningKeyshare) MarshalProto() *pb.SigningKeyshare {
	var ownerIdentifiers []string
	for identifier := range sk.PublicShares {
		ownerIdentifiers = append(ownerIdentifiers, identifier)
	}

	return &pb.SigningKeyshare{
		OwnerIdentifiers: ownerIdentifiers,
		Threshold:        uint32(sk.MinSigners),
		PublicKey:        sk.PublicKey,
		PublicShares:     sk.PublicShares,
		UpdatedTime:      timestamppb.New(sk.UpdateTime),
	}
}

// GetUnusedSigningKeyshares returns the available keyshares for the given coordinator index.
func GetUnusedSigningKeyshares(ctx context.Context, config *so.Config, keyshareCount int) ([]*SigningKeyshare, error) {
	ctx, span := tracer.Start(ctx, "SigningKeyshare.GetUnusedSigningKeyshares")
	defer span.End()

	logger := logging.GetLoggerFromContext(ctx)

	tx, err := GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}

	signingKeyshares, err := GetUnusedSigningKeysharesTx(ctx, tx, config, keyshareCount)
	if err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			logger.Error("Failed to rollback transaction", "error", rollbackErr)
		}
		return nil, err
	}

	err = tx.Commit()
	if err != nil {
		return nil, err
	}

	return signingKeyshares, nil
}

// GetUnusedSigningKeysharesTx runs inside an existing *ent.Tx.
// Caller is responsible for committing/rolling-back the tx.
func GetUnusedSigningKeysharesTx(
	ctx context.Context,
	tx *Tx,
	cfg *so.Config,
	keyshareCount int,
) ([]*SigningKeyshare, error) {
	ctx, span := tracer.Start(ctx, "SigningKeyshare.GetUnusedSigningKeysharesTx")
	defer span.End()

	if keyshareCount <= 0 {
		return nil, fmt.Errorf("keyshare count must be greater than 0")
	}

	// Prevent keyshare exhaustion attacks by limiting maximum request size
	maxKeysharesPerRequest := int(knobs.GetKnobsService(ctx).GetValue(knobs.KnobSoMaxKeysharesPerRequest, 1000))
	if keyshareCount > maxKeysharesPerRequest {
		return nil, fmt.Errorf("keyshare request too large: requested %d, maximum allowed %d", keyshareCount, maxKeysharesPerRequest)
	}

	// Setting these parameters to optimize the performance of the query below.

	// nolint:forbidigo
	_, err := tx.ExecContext(ctx, `
		SET LOCAL seq_page_cost = 10.0;
		SET LOCAL random_page_cost = 1.0;
	`)
	if err != nil {
		return nil, err
	}

	var updatedKeyshares []*SigningKeyshare

	// We use a custom a custom query here to select and update the keyshares in a single query, while
	// skipping locked rows to avoid contention.

	// nolint:forbidigo
	rows, err := tx.QueryContext(ctx, `
		WITH selected_ids AS (
			SELECT id FROM signing_keyshares
			WHERE status = 'AVAILABLE' AND coordinator_index = $1
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		)
		UPDATE signing_keyshares
		SET status = 'IN_USE', update_time = NOW()
		FROM selected_ids
		WHERE signing_keyshares.id = selected_ids.id
		RETURNING signing_keyshares.*
	`, []any{cfg.Index, keyshareCount}...)
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			// If ScanSlice already returned an error, we don't want to overwrite it,
			// so just log the close error.
			logging.GetLoggerFromContext(ctx).Error("failed to close rows", "error", cerr)
			span.RecordError(cerr)
		}
	}()

	if err := sql.ScanSlice(rows, &updatedKeyshares); err != nil {
		return nil, err
	}

	if len(updatedKeyshares) < keyshareCount {
		return nil, fmt.Errorf("not enough signing keyshares available (needed %d, got %d)", keyshareCount, len(updatedKeyshares))
	}

	return updatedKeyshares, nil
}

// MarkSigningKeysharesAsUsed marks the given keyshares as used. If any of the keyshares are not
// found or not available, it returns an error.
func MarkSigningKeysharesAsUsed(ctx context.Context, _ *so.Config, ids []uuid.UUID) (map[uuid.UUID]*SigningKeyshare, error) {
	ctx, span := tracer.Start(ctx, "SigningKeyshare.MarkSigningKeysharesAsUsed")
	defer span.End()

	logger := logging.GetLoggerFromContext(ctx)
	db, err := GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}
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
		SetStatus(st.KeyshareStatusInUse).
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

	db, err := GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}

	keyshare, err := db.SigningKeyshare.Get(ctx, keyshareID)
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

	db, err := GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}

	keyshares, err := db.SigningKeyshare.Query().Where(
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

	db, err := GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}

	keyshares, err := db.SigningKeyshare.Query().Where(
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

	db, err := GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}

	lastKey, err := db.SigningKeyshare.Create().
		SetID(id).
		SetSecretShare(lastSecretShare).
		SetPublicShares(publicShares).
		SetPublicKey(verifyingKey).
		SetStatus(st.KeyshareStatusInUse).
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

	db, err := GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}

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
func RunDKGIfNeeded(ctx context.Context, config *so.Config) error {
	ctx, span := tracer.Start(ctx, "SigningKeyshare.RunDKGIfNeeded")
	defer span.End()

	db, err := GetDbFromContext(ctx)
	if err != nil {
		return err
	}

	count, err := db.SigningKeyshare.Query().Where(
		signingkeyshare.StatusEQ(st.KeyshareStatusAvailable),
		signingkeyshare.CoordinatorIndexEQ(config.Index),
		signingkeyshare.IDGT(uuid.MustParse("01954639-8d50-7e47-b3f0-ddb307fab7c2")),
	).Count(ctx)
	if err != nil {
		return err
	}

	minAvailableKeys := DefaultMinAvailableKeys
	if config.DKGConfig.MinAvailableKeys != nil && *config.DKGConfig.MinAvailableKeys > 0 {
		minAvailableKeys = *config.DKGConfig.MinAvailableKeys
	}

	if count >= minAvailableKeys {
		return nil
	}

	return RunDKG(ctx, config)
}

func RunDKG(ctx context.Context, config *so.Config) error {
	ctx, span := tracer.Start(ctx, "SigningKeyshare.RunDKG")
	defer span.End()

	logger := logging.GetLoggerFromContext(ctx)

	connection, err := config.SigningOperatorMap[config.Identifier].NewOperatorGRPCConnectionForDKG()
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
