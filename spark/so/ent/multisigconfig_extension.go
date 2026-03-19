package ent

import (
	"bytes"
	"context"
	"fmt"
	"sort"

	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/multisig"
	pb "github.com/lightsparkdev/spark/proto/multisig"
	"github.com/lightsparkdev/spark/so/ent/multisigconfig"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
)

// GetOrCreateMultisigConfig looks up a MultisigConfig by its computed identifier,
// creating it with all members if it doesn't exist. The proto config must be
// normalized (keys sorted lexicographically) before calling this function.
// Callers should ensure this runs within a database transaction for atomicity.
func GetOrCreateMultisigConfig(ctx context.Context, client *Client, protoConfig *pb.MultisigConfig) (*MultisigConfig, error) {
	identifier, err := multisig.ValidateAndComputeMultisigIdentifier(protoConfig)
	if err != nil {
		return nil, fmt.Errorf("invalid multisig config: %w", err)
	}

	existing, err := client.MultisigConfig.Query().
		Where(multisigconfig.MultisigIdentifier(identifier)).
		WithMembers().
		Only(ctx)
	if err == nil {
		return existing, nil
	}
	if !IsNotFound(err) {
		return nil, sparkerrors.InternalDatabaseReadError(fmt.Errorf("failed to query multisig config: %w", err))
	}

	config, err := client.MultisigConfig.Create().
		SetMultisigIdentifier(identifier).
		SetNumSignersThreshold(protoConfig.Threshold).
		SetNumSignersTotal(uint32(len(protoConfig.PublicKeys))).
		Save(ctx)
	if err != nil {
		// Concurrent insert with same identifier: retry the lookup.
		if IsConstraintError(err) {
			existing, queryErr := client.MultisigConfig.Query().
				Where(multisigconfig.MultisigIdentifier(identifier)).
				WithMembers().
				Only(ctx)
			if queryErr == nil {
				return existing, nil
			}
			return nil, sparkerrors.InternalDatabaseWriteError(
				fmt.Errorf("failed to create multisig config (retry query also failed: %w): %w", queryErr, err))
		}
		return nil, sparkerrors.InternalDatabaseWriteError(fmt.Errorf("failed to create multisig config: %w", err))
	}

	for _, pkBytes := range protoConfig.PublicKeys {
		pk, err := keys.ParsePublicKey(pkBytes)
		if err != nil {
			return nil, sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("failed to parse public key: %w", err))
		}
		_, err = client.MultisigMember.Create().
			SetPublicKey(pk).
			SetConfig(config).
			Save(ctx)
		if err != nil {
			return nil, sparkerrors.InternalDatabaseWriteError(fmt.Errorf("failed to create multisig member: %w", err))
		}
	}

	result, err := client.MultisigConfig.Query().
		Where(multisigconfig.ID(config.ID)).
		WithMembers().
		Only(ctx)
	if err != nil {
		return nil, sparkerrors.InternalDatabaseReadError(fmt.Errorf("failed to re-fetch multisig config after creation: %w", err))
	}
	return result, nil
}

// ToProtoConfig converts the Ent MultisigConfig entity to a proto MultisigConfig.
// The Members edge must be loaded before calling this method.
// Keys are sorted lexicographically to match the canonical ordering
// expected by ValidateAndComputeMultisigIdentifier.
func (mc *MultisigConfig) ToProtoConfig() *pb.MultisigConfig {
	publicKeys := make([][]byte, len(mc.Edges.Members))
	for i, member := range mc.Edges.Members {
		publicKeys[i] = member.PublicKey.Serialize()
	}
	sort.Slice(publicKeys, func(i, j int) bool {
		return bytes.Compare(publicKeys[i], publicKeys[j]) < 0
	})
	return &pb.MultisigConfig{
		Version:    0,
		Threshold:  mc.NumSignersThreshold,
		PublicKeys: publicKeys,
	}
}
