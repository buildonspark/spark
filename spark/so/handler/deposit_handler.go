package handler

import (
	"bytes"
	"context"
	"encoding/hex"
	errs "errors"
	"fmt"
	"strings"

	"entgo.io/ent/dialect/sql"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common"
	bitcointransaction "github.com/lightsparkdev/spark/common/bitcoin_transaction"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/logging"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authn"
	"github.com/lightsparkdev/spark/so/authz"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/blockheight"
	"github.com/lightsparkdev/spark/so/ent/depositaddress"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/tree"
	"github.com/lightsparkdev/spark/so/ent/treenode"
	entutxo "github.com/lightsparkdev/spark/so/ent/utxo"
	"github.com/lightsparkdev/spark/so/ent/utxoswap"
	"github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/frost"
	"github.com/lightsparkdev/spark/so/helper"
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/lightsparkdev/spark/so/utils"
)

const DefaultDepositConfirmationThreshold = uint(3)

// DefaultMaxUnusedDepositAddresses is the default maximum number of unused non-static deposit
// addresses a user can have per network. This prevents DoS attacks where users repeatedly
// generate addresses without depositing, exhausting the available signing keyshares.
// This value can be overridden via the KnobMaxUnusedDepositAddresses knob.
const DefaultMaxUnusedDepositAddresses = 64

var ErrInvalidNetwork = errs.New("invalid network")

// The DepositHandler is responsible for handling deposit related requests.
type DepositHandler struct {
	config *so.Config
}

// NewDepositHandler creates a new DepositHandler.
func NewDepositHandler(config *so.Config) *DepositHandler {
	return &DepositHandler{
		config: config,
	}
}

// validateIdentity parses and validates the identity public key from a request.
func validateIdentity(ctx context.Context, config *so.Config, identityPublicKey []byte) (keys.Public, error) {
	reqIDPubKey, err := keys.ParsePublicKey(identityPublicKey)
	if err != nil {
		return keys.Public{}, fmt.Errorf("invalid identity public key: %w", err)
	}
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, config, reqIDPubKey); err != nil {
		return keys.Public{}, err
	}
	return reqIDPubKey, nil
}

// GenerateDepositAddress generates a deposit address for the given public key.
// The address string is generated using provided network field in the request.
func (o *DepositHandler) GenerateDepositAddress(ctx context.Context, config *so.Config, req *pb.GenerateDepositAddressRequest) (*pb.GenerateDepositAddressResponse, error) {
	ctx, span := tracer.Start(ctx, "DepositHandler.GenerateDepositAddress")
	defer span.End()

	if req.GetIsStatic() {
		res, err := o.GenerateStaticDepositAddress(ctx, config, &pb.GenerateStaticDepositAddressRequest{
			IdentityPublicKey: req.IdentityPublicKey,
			SigningPublicKey:  req.SigningPublicKey,
			Network:           req.Network,
		})
		if err != nil {
			return nil, err
		}
		return &pb.GenerateDepositAddressResponse{
			DepositAddress: res.DepositAddress,
		}, nil
	}

	logger := logging.GetLoggerFromContext(ctx)
	network, err := btcnetwork.FromProtoNetwork(req.Network)
	if err != nil {
		return nil, err
	}
	if !config.IsNetworkSupported(network) {
		return nil, fmt.Errorf("network not supported")
	}

	reqIDPubKey, err := keys.ParsePublicKey(req.IdentityPublicKey)
	if err != nil {
		return nil, fmt.Errorf("invalid identity public key: %w", err)
	}
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, o.config, reqIDPubKey); err != nil {
		return nil, err
	}
	reqSigningPubKey, err := keys.ParsePublicKey(req.SigningPublicKey)
	if err != nil {
		return nil, fmt.Errorf("invalid signing public key: %w", err)
	}

	logger.Sugar().Infof("Generating deposit address for public key %s (signing %s)", reqIDPubKey, reqSigningPubKey)

	// Check if user already has too many unused non-static deposit addresses for this network.
	// An "unused" address is one that has no tree created yet (no deposit confirmed).
	// This prevents DoS attacks where users repeatedly generate addresses without depositing,
	// exhausting the available signing keyshares.
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get db: %w", err)
	}
	// Approximate count; will not include concurrent requests
	// Considered low risk so not making use of locking
	unusedCount, err := db.DepositAddress.Query().
		Where(
			depositaddress.OwnerIdentityPubkey(reqIDPubKey),
			depositaddress.IsStatic(false),
			depositaddress.NetworkEQ(network),
			depositaddress.Not(depositaddress.HasTree()),
		).
		Count(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to count existing deposit addresses: %w", err)
	}
	maxUnusedAddresses := int(knobs.GetKnobsService(ctx).GetValue(knobs.KnobMaxUnusedDepositAddresses, DefaultMaxUnusedDepositAddresses))
	if unusedCount >= maxUnusedAddresses {
		return nil, status.Errorf(codes.ResourceExhausted,
			"user already has %d unused deposit addresses for this network (maximum %d); please use an existing address or wait for a deposit to be confirmed",
			unusedCount, maxUnusedAddresses)
	}

	keyshares, err := ent.GetUnusedSigningKeyshares(ctx, config, 1)
	if err != nil {
		return nil, err
	}

	if len(keyshares) == 0 {
		return nil, fmt.Errorf("no keyshares available")
	}

	keyshare := keyshares[0]

	selection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}
	_, err = helper.ExecuteTaskWithAllOperators(ctx, config, &selection, func(ctx context.Context, operator *so.SigningOperator) (any, error) {
		conn, err := operator.NewOperatorGRPCConnection()
		if err != nil {
			return nil, err
		}
		defer conn.Close()

		client := pbinternal.NewSparkInternalServiceClient(conn)
		_, err = client.MarkKeysharesAsUsed(ctx, &pbinternal.MarkKeysharesAsUsedRequest{KeyshareId: []string{keyshare.ID.String()}})
		return nil, err
	})
	if err != nil {
		return nil, err
	}

	combinedPublicKey := keyshare.PublicKey.Add(reqSigningPubKey)
	depositAddress, err := common.P2TRAddressFromPublicKey(combinedPublicKey, network)
	if err != nil {
		return nil, err
	}

	// Get a fresh db handle since GetUnusedSigningKeyshares commits the transaction
	db, err = ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get db: %w", err)
	}

	depositAddressMutator := db.DepositAddress.Create().
		SetSigningKeyshareID(keyshare.ID).
		SetOwnerIdentityPubkey(reqIDPubKey).
		SetOwnerSigningPubkey(reqSigningPubKey).
		SetNetwork(network).
		SetAddress(depositAddress)
	// Confirmation height is not set since nothing has been confirmed yet.

	if req.LeafId != nil {
		leafID, err := uuid.Parse(req.GetLeafId())
		if err != nil {
			return nil, err
		}
		depositAddressMutator.SetNodeID(leafID)
	}

	if _, err := depositAddressMutator.Save(ctx); err != nil {
		return nil, fmt.Errorf("failed to save deposit address: %w", err)
	}

	response, err := helper.ExecuteTaskWithAllOperators(ctx, config, &selection, func(ctx context.Context, operator *so.SigningOperator) ([]byte, error) {
		conn, err := operator.NewOperatorGRPCConnection()
		if err != nil {
			return nil, err
		}
		defer conn.Close()

		client := pbinternal.NewSparkInternalServiceClient(conn)
		response, err := client.MarkKeyshareForDepositAddress(ctx, &pbinternal.MarkKeyshareForDepositAddressRequest{
			KeyshareId:             keyshare.ID.String(),
			Address:                depositAddress,
			OwnerIdentityPublicKey: reqIDPubKey.Serialize(),
			OwnerSigningPublicKey:  reqSigningPubKey.Serialize(),
			IsStatic:               req.IsStatic,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to mark keyshare for deposit address: %w", err)
		}
		return response.AddressSignature, nil
	})
	if err != nil {
		return nil, err
	}

	msg := common.ProofOfPossessionMessageHashForDepositAddress(reqIDPubKey, keyshare.PublicKey, []byte(depositAddress))
	proofOfPossessionSignature, err := helper.GenerateProofOfPossessionSignatures(ctx, config, [][]byte{msg}, []*ent.SigningKeyshare{keyshare})
	if err != nil {
		return nil, err
	}
	return &pb.GenerateDepositAddressResponse{
		DepositAddress: &pb.Address{
			Address:      depositAddress,
			VerifyingKey: combinedPublicKey.Serialize(),
			DepositAddressProof: &pb.DepositAddressProof{
				AddressSignatures:          response,
				ProofOfPossessionSignature: proofOfPossessionSignature[0],
			},
			IsStatic: req.GetIsStatic(),
		},
	}, nil
}

// GenerateStaticDepositAddress generates or retrieves a static deposit address for a user's identity and signing public key.
//
// This method provides a deterministic way for users to obtain a permanent Bitcoin deposit address
// that remains valid across multiple deposits. Unlike regular deposit addresses, static addresses
// are reusable and tied to a specific identity-network combination.
//
// The method coordinates getting a static deposit address for a user in a distributed way:
// 1. First checks if a default static address already exists for the identity-network pair
// 2. If found, verifies that all operators have the necessary cryptographic proofs of possession
// 3. If not found, generates a new default static address using distributed key generation
// 4. Coordinates with all other operators to mark keyshares as used and generate proofs
//
// Parameters:
//   - SigningPublicKey: User's 33-byte secp256k1 public key for address generation
//   - IdentityPublicKey: User's 33-byte identity key for authentication
//   - Network: Target Bitcoin network (mainnet, testnet, regtest)
//
// Returns:
//   - Address: P2TR Bitcoin address string
//   - VerifyingKey: Combined public key (user + operator keyshare)
//   - DepositAddressProof: Cryptographic proofs including:
//   - AddressSignatures: Map of operator ID -> signature proving address validity
//   - ProofOfPossessionSignature: Proof that the operator possesses the key fragment
func (o *DepositHandler) GenerateStaticDepositAddress(ctx context.Context, config *so.Config, req *pb.GenerateStaticDepositAddressRequest) (*pb.GenerateStaticDepositAddressResponse, error) {
	ctx, span := tracer.Start(ctx, "DepositHandler.GenerateStaticDepositAddress")
	defer span.End()

	network, err := btcnetwork.FromProtoNetwork(req.Network)
	if err != nil {
		return nil, err
	}
	if !config.IsNetworkSupported(network) {
		return nil, fmt.Errorf("network not supported")
	}
	idPubKey, err := keys.ParsePublicKey(req.GetIdentityPublicKey())
	if err != nil {
		return nil, fmt.Errorf("failed to parse identity public key: %w", err)
	}
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, config, idPubKey); err != nil {
		return nil, err
	}

	logger := logging.GetLoggerFromContext(ctx)
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx: %w", err)
	}

	depositAddress, err := db.DepositAddress.Query().
		Where(
			depositaddress.OwnerIdentityPubkey(idPubKey),
			depositaddress.IsStatic(true),
			depositaddress.IsDefault(true),
			depositaddress.NetworkEQ(network),
		).
		Only(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return nil, fmt.Errorf("failed to query static deposit address for user id %s: %w", idPubKey.Serialize(), err)
	}

	// If a default static deposit address already exists, return it.
	if depositAddress != nil {
		// Get local keyshare for the deposit address.
		keyshare, err := depositAddress.QuerySigningKeyshare().Only(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get keyshare for static deposit address id %s: %w", depositAddress.ID, err)
		}

		addressSignatures, proofOfPossessionSignature, err := generateStaticDepositAddressProofs(ctx, config, keyshare, depositAddress)
		if err != nil {
			return nil, fmt.Errorf("failed to generate static deposit address proofs for static deposit address id %s: %w", depositAddress.ID, err)
		}
		if addressSignatures == nil {
			return nil, fmt.Errorf("static deposit address id %s does not have proofs on all operators", depositAddress.ID)
		}

		// Check if the proofs are already cached.
		verifyingKey := keyshare.PublicKey.Add(depositAddress.OwnerSigningPubkey)

		// Return the whole deposit address data.
		logger.Sugar().Infof("Static deposit address %s already exists with ID %s", depositAddress.Address, depositAddress.ID)
		return &pb.GenerateStaticDepositAddressResponse{
			DepositAddress: &pb.Address{
				Address:      depositAddress.Address,
				VerifyingKey: verifyingKey.Serialize(),
				DepositAddressProof: &pb.DepositAddressProof{
					AddressSignatures:          addressSignatures,
					ProofOfPossessionSignature: proofOfPossessionSignature,
				},
				IsStatic: true,
			},
		}, nil
	}

	reqSigningPubKey, err := keys.ParsePublicKey(req.GetSigningPublicKey())
	if err != nil {
		return nil, fmt.Errorf("failed to parse signing public key: %w", err)
	}

	depositAddressInfo, err := createStaticDepositAddress(ctx, config, network, idPubKey, reqSigningPubKey)
	if err != nil {
		return nil, err
	}
	return &pb.GenerateStaticDepositAddressResponse{
		DepositAddress: depositAddressInfo,
	}, nil
}

// Create a static deposit address in the database generating all the necessary proofs and return them as a protobuf message ready to return to the user
func createStaticDepositAddress(ctx context.Context, config *so.Config, network btcnetwork.Network, identityPublicKey keys.Public, signingPublicKey keys.Public) (*pb.Address, error) {
	logger := logging.GetLoggerFromContext(ctx)

	if identityPublicKey.IsZero() || signingPublicKey.IsZero() {
		return nil, fmt.Errorf("both identity key and signing key must be provided")
	}

	logger.Sugar().Infof("Generating static deposit address for public key %s (signing %x)", identityPublicKey, signingPublicKey)

	// Note that this method will COMMIT or ROLLBACK the DB transaction.
	keyshares, err := ent.GetUnusedSigningKeyshares(ctx, config, 1)
	if err != nil {
		return nil, fmt.Errorf("failed to get unused keyshares: %w", err)
	}
	if len(keyshares) == 0 {
		return nil, fmt.Errorf("no keyshares available")
	}
	keyshare := keyshares[0]

	verifyingKey := keyshare.PublicKey.Add(signingPublicKey)

	selection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}
	_, err = helper.ExecuteTaskWithAllOperators(ctx, config, &selection, func(ctx context.Context, operator *so.SigningOperator) (any, error) {
		conn, err := operator.NewOperatorGRPCConnection()
		if err != nil {
			return nil, err
		}
		defer conn.Close()

		client := pbinternal.NewSparkInternalServiceClient(conn)
		_, err = client.MarkKeysharesAsUsed(ctx, &pbinternal.MarkKeysharesAsUsedRequest{KeyshareId: []string{keyshare.ID.String()}})
		return nil, err
	})
	if err != nil {
		return nil, fmt.Errorf("failed to mark keyshares as used: %w", err)
	}

	combinedPublicKey := keyshare.PublicKey.Add(signingPublicKey)
	depositAddressString, err := common.P2TRAddressFromPublicKey(combinedPublicKey, network)
	if err != nil {
		return nil, err
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx: %w", err)
	}

	depositAddressMutator := db.DepositAddress.Create().
		SetSigningKeyshareID(keyshare.ID).
		SetOwnerIdentityPubkey(identityPublicKey).
		SetOwnerSigningPubkey(signingPublicKey).
		SetNetwork(network).
		SetAddress(depositAddressString).
		SetIsDefault(true).
		SetIsStatic(true)

	depositAddressRecord, err := depositAddressMutator.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to save deposit address: %w", err)
	}

	isStatic := true

	addressSignatures, err := helper.ExecuteTaskWithAllOperators(ctx, config, &selection, func(ctx context.Context, operator *so.SigningOperator) ([]byte, error) {
		conn, err := operator.NewOperatorGRPCConnection()
		if err != nil {
			return nil, err
		}
		defer conn.Close()

		client := pbinternal.NewSparkInternalServiceClient(conn)
		response, err := client.MarkKeyshareForDepositAddress(ctx, &pbinternal.MarkKeyshareForDepositAddressRequest{
			KeyshareId:             keyshare.ID.String(),
			Address:                depositAddressString,
			OwnerIdentityPublicKey: identityPublicKey.Serialize(),
			OwnerSigningPublicKey:  signingPublicKey.Serialize(),
			IsStatic:               &isStatic,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to mark keyshare for deposit address: %w", err)
		}
		return response.AddressSignature, nil
	})
	if err != nil {
		return nil, err
	}

	msg := common.ProofOfPossessionMessageHashForDepositAddress(identityPublicKey, keyshare.PublicKey, []byte(depositAddressString))
	proofOfPossessionSignatures, err := helper.GenerateProofOfPossessionSignatures(ctx, config, [][]byte{msg}, []*ent.SigningKeyshare{keyshare})
	if err != nil {
		return nil, err
	}
	if len(proofOfPossessionSignatures) == 0 {
		return nil, fmt.Errorf("unable to generate proof of possession signature for a deposit address: 0 signatures")
	}
	proofOfPossessionSignature := proofOfPossessionSignatures[0]

	internalHandler := NewInternalDepositHandler(config)
	selfProofs, err := internalHandler.GenerateStaticDepositAddressProofs(ctx, &pbinternal.GenerateStaticDepositAddressProofsRequest{
		KeyshareId:             keyshare.ID.String(),
		Address:                depositAddressString,
		OwnerIdentityPublicKey: identityPublicKey.Serialize(),
	})
	if err != nil {
		return nil, err
	}
	addressSignatures[config.Identifier] = selfProofs.AddressSignature

	// Cache the proofs in the database.
	db, err = ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}
	_, err = db.DepositAddress.Update().
		Where(depositaddress.ID(depositAddressRecord.ID)).
		SetAddressSignatures(addressSignatures).
		SetPossessionSignature(proofOfPossessionSignatures[0]).
		Save(ctx)
	if err != nil {
		logger.With(zap.Error(err)).
			Sugar().
			Errorf(
				"Failed to cache proofs for static deposit address %s (%s)",
				depositAddressRecord.ID,
				depositAddressString,
			)
	}
	return &pb.Address{
		Address:      depositAddressString,
		VerifyingKey: verifyingKey.Serialize(),
		DepositAddressProof: &pb.DepositAddressProof{
			AddressSignatures:          addressSignatures,
			ProofOfPossessionSignature: proofOfPossessionSignature,
		},
		IsStatic: true,
	}, nil
}

func generateStaticDepositAddressProofs(ctx context.Context, config *so.Config, keyshare *ent.SigningKeyshare, depositAddress *ent.DepositAddress) (map[string][]byte, []byte, error) {
	// If the proofs are already cached, return them.
	if depositAddress.AddressSignatures != nil && depositAddress.PossessionSignature != nil {
		return depositAddress.AddressSignatures, depositAddress.PossessionSignature, nil
	}

	logger := logging.GetLoggerFromContext(ctx)

	internalHandler := NewInternalDepositHandler(config)
	selfProofs, err := internalHandler.GenerateStaticDepositAddressProofs(ctx, &pbinternal.GenerateStaticDepositAddressProofsRequest{
		KeyshareId:             keyshare.ID.String(),
		Address:                depositAddress.Address,
		OwnerIdentityPublicKey: depositAddress.OwnerIdentityPubkey.Serialize(),
	})
	if err != nil {
		return nil, nil, err
	}

	// Get proofs from other operators.
	selection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}
	responses, err := helper.ExecuteTaskWithAllOperators(ctx, config, &selection, func(ctx context.Context, operator *so.SigningOperator) (*pbinternal.GenerateStaticDepositAddressProofsResponse, error) {
		conn, err := operator.NewOperatorGRPCConnection()
		if err != nil {
			return nil, fmt.Errorf("failed to get operator grpc connection: %w", err)
		}
		defer conn.Close()

		client := pbinternal.NewSparkInternalServiceClient(conn)
		response, err := client.GenerateStaticDepositAddressProofs(ctx, &pbinternal.GenerateStaticDepositAddressProofsRequest{
			KeyshareId:             keyshare.ID.String(),
			Address:                depositAddress.Address,
			OwnerIdentityPublicKey: depositAddress.OwnerIdentityPubkey.Serialize(),
		})
		if err != nil {
			return nil, fmt.Errorf("failed to generate static deposit address proofs: %w", err)
		}
		return response, nil
	})
	// If internal error, return it.
	if err != nil && status.Code(err) != codes.NotFound {
		return nil, nil, fmt.Errorf("failed to generate static deposit address proofs: %w", err)
	}
	// If not found, continue with another address.
	if err != nil && status.Code(err) == codes.NotFound {
		logger.With(zap.Error(err)).
			Sugar().
			Errorf(
				"Static deposit address %s (%s) does not have proofs on some or all operators",
				depositAddress.ID,
				depositAddress.Address,
			)
		return nil, nil, nil
	}

	addressSignatures := make(map[string][]byte)
	for id, response := range responses {
		addressSignatures[id] = response.AddressSignature
	}
	addressSignatures[config.Identifier] = selfProofs.AddressSignature

	msg := common.ProofOfPossessionMessageHashForDepositAddress(depositAddress.OwnerIdentityPubkey, keyshare.PublicKey, []byte(depositAddress.Address))
	proofOfPossessionSignatures, err := helper.GenerateProofOfPossessionSignatures(ctx, config, [][]byte{msg}, []*ent.SigningKeyshare{keyshare})
	if err != nil {
		return nil, nil, err
	}

	// Cache the proofs in the database.
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}
	_, err = db.DepositAddress.Update().
		Where(depositaddress.ID(depositAddress.ID)).
		SetAddressSignatures(addressSignatures).
		SetPossessionSignature(proofOfPossessionSignatures[0]).
		Save(ctx)
	if err != nil {
		logger.With(zap.Error(err)).
			Sugar().
			Errorf(
				"Failed to cache proofs for static deposit address %s (%s)",
				depositAddress.ID,
				depositAddress.Address,
			)
	}
	return addressSignatures, proofOfPossessionSignatures[0], nil
}

// Archives the current default Static Deposit Address and generates a new one
// for a user. This method is useful when users want to obtain a new static deposit
// address for privacy or security reasons.
//
// The method performs the following steps:
//  1. Queries for the existing default static deposit address
//  2. If no default address exists, returns an error
//  3. Archives the existing default address (sets is_default = false)
//  4. Sends a gossip message to other SOs commanding them to archive that
//     specific address using a signed statement (idempotent handler).
//  5. Generates a new default static deposit address using the same logic
//     as GenerateStaticDepositAddress (involves sending another gossip via
//     MarkKeyshareForDepositAddress)
//  6. Returns both the new and archived addresses in the response
//
// Parameters:
//   - SigningPublicKey: User's 33-byte secp256k1 public key for address generation
//   - IdentityPublicKey: User's 33-byte identity key for authentication
//   - Network: Target Bitcoin network (mainnet, testnet, regtest)
//
// Returns:
//   - NewDepositAddress: The newly generated default static deposit address
//   - ArchivedDepositAddress: The archived (previous default) static deposit address
func (o *DepositHandler) RotateStaticDepositAddress(ctx context.Context, config *so.Config, req *pb.RotateStaticDepositAddressRequest) (*pb.RotateStaticDepositAddressResponse, error) {
	ctx, span := tracer.Start(ctx, "DepositHandler.RotateStaticDepositAddress")
	defer span.End()

	network, err := btcnetwork.FromProtoNetwork(req.Network)
	if err != nil {
		return nil, err
	}
	if !config.IsNetworkSupported(network) {
		return nil, fmt.Errorf("network not supported")
	}
	// Get the session from context
	session, err := authn.GetSessionFromContext(ctx)
	if err != nil {
		return nil, err
	}
	// Get the identity public key from the session
	idPubKey := session.IdentityPublicKey() // Returns keys.Public

	logger := logging.GetLoggerFromContext(ctx)
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx: %w", err)
	}

	// Query for the existing default static deposit address
	existingDefaultAddress, err := db.DepositAddress.Query().
		Where(
			depositaddress.OwnerIdentityPubkey(idPubKey),
			depositaddress.IsStatic(true),
			depositaddress.IsDefault(true),
			depositaddress.NetworkEQ(network),
		).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, errors.NotFoundMissingEntity(fmt.Errorf("no default static deposit address found for user; generate one first using generate_static_deposit_address"))
		}
		return nil, fmt.Errorf("failed to query static deposit address for user id %s: %w", idPubKey.Serialize(), err)
	}

	// Get keyshare for the existing address to construct the archived address response
	existingKeyshare, err := existingDefaultAddress.QuerySigningKeyshare().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get keyshare for existing static deposit address id %s: %w", existingDefaultAddress.ID, err)
	}

	// Generate proofs for the existing address to include in the archived address response
	existingAddressSignatures, existingProofOfPossession, err := generateStaticDepositAddressProofs(ctx, config, existingKeyshare, existingDefaultAddress)
	if err != nil {
		return nil, fmt.Errorf("failed to generate proofs for existing static deposit address id %s: %w", existingDefaultAddress.ID, err)
	}
	if existingAddressSignatures == nil {
		return nil, fmt.Errorf("existing static deposit address id %s does not have proofs on all operators", existingDefaultAddress.ID)
	}

	existingVerifyingKey := existingKeyshare.PublicKey.Add(existingDefaultAddress.OwnerSigningPubkey)

	// Archive the existing default address by setting is_default to false
	_, err = db.DepositAddress.Update().
		Where(depositaddress.ID(existingDefaultAddress.ID)).
		SetIsDefault(false).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to archive existing default static deposit address: %w", err)
	}

	logger.Sugar().Infof("Archived static deposit address %s with ID %s", existingDefaultAddress.Address, existingDefaultAddress.ID)

	// Create statement and sign it with coordinator's identity key to prove authorization
	messageHash, err := CreateArchiveStaticDepositAddressStatement(idPubKey, network, existingDefaultAddress.Address)
	if err != nil {
		return nil, fmt.Errorf("failed to create archive statement: %w", err)
	}
	signature := ecdsa.Sign(config.IdentityPrivateKey.ToBTCEC(), messageHash)

	// Send gossip message to archive the address on all other SOs
	selection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}
	participants, err := selection.OperatorIdentifierList(config)
	if err != nil {
		return nil, fmt.Errorf("unable to get operator list: %w", err)
	}

	// Broadcast gossip to other SOs to archive a specific deposit address.
	// This call is asynchronous ensuring eventual consistency.
	// The signature prevents rogue SOs from archiving addresses without user authorization.
	sendGossipHandler := NewSendGossipHandler(config)
	_, err = sendGossipHandler.CreateAndSendGossipMessage(ctx, &pbgossip.GossipMessage{
		Message: &pbgossip.GossipMessage_ArchiveStaticDepositAddress{
			ArchiveStaticDepositAddress: &pbgossip.GossipMessageArchiveStaticDepositAddress{
				OwnerIdentityPublicKey: idPubKey.Serialize(),
				Network:                req.Network,
				Address:                existingDefaultAddress.Address,
				Signature:              signature.Serialize(),
				CoordinatorPublicKey:   config.IdentityPublicKey().Serialize(),
			},
		},
	}, participants)
	if err != nil {
		return nil, fmt.Errorf("failed to send gossip message to archive static deposit address: %w", err)
	}

	reqSigningPubKey, err := keys.ParsePublicKey(req.GetSigningPublicKey())
	if err != nil {
		return nil, fmt.Errorf("failed to parse signing public key: %w", err)
	}

	depositAddressInfo, err := createStaticDepositAddress(ctx, config, network, idPubKey, reqSigningPubKey)
	if err != nil {
		return nil, err
	}

	logger.Sugar().Infof("Successfully rotated static deposit address. New address: %s, Archived address: %s", depositAddressInfo.Address, existingDefaultAddress.Address)

	return &pb.RotateStaticDepositAddressResponse{
		NewDepositAddress: depositAddressInfo,
		ArchivedDepositAddress: &pb.Address{
			Address:      existingDefaultAddress.Address,
			VerifyingKey: existingVerifyingKey.Serialize(),
			DepositAddressProof: &pb.DepositAddressProof{
				AddressSignatures:          existingAddressSignatures,
				ProofOfPossessionSignature: existingProofOfPossession,
			},
			IsStatic: true,
		},
	}, nil
}

// StartDepositTreeCreation verifies the on chain utxo, and then verifies and signs the offchain root and refund transactions.
func (o *DepositHandler) StartDepositTreeCreation(ctx context.Context, config *so.Config, req *pb.StartDepositTreeCreationRequest) (*pb.StartDepositTreeCreationResponse, error) {
	ctx, span := tracer.Start(ctx, "DepositHandler.StartDepositTreeCreation")
	defer span.End()
	reqIDPubKey, err := keys.ParsePublicKey(req.IdentityPublicKey)
	if err != nil {
		return nil, fmt.Errorf("invalid identity public key: %w", err)
	}
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, o.config, reqIDPubKey); err != nil {
		return nil, err
	}
	// Get the on chain tx
	onChainTx, err := common.TxFromRawTxBytes(req.OnChainUtxo.RawTx)
	if err != nil {
		return nil, err
	}

	// Verify that the on chain utxo is paid to the registered deposit address
	if len(onChainTx.TxOut) <= int(req.OnChainUtxo.Vout) {
		return nil, fmt.Errorf("utxo index out of bounds")
	}
	onChainOutput := onChainTx.TxOut[req.OnChainUtxo.Vout]
	network, err := btcnetwork.FromProtoNetwork(req.OnChainUtxo.Network)
	if err != nil {
		return nil, err
	}
	if !config.IsNetworkSupported(network) {
		return nil, fmt.Errorf("network not supported")
	}
	utxoAddress, err := common.P2TRAddressFromPkScript(onChainOutput.PkScript, network)
	if err != nil {
		return nil, err
	}
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	depositAddress, err := db.DepositAddress.Query().Where(depositaddress.Address(*utxoAddress)).Where(depositaddress.IsStatic(false)).WithTree().ForUpdate().Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			err = errors.NotFoundMissingEntity(fmt.Errorf("the requested deposit address could not be found for address: %s", *utxoAddress))
		}
		if ent.IsNotSingular(err) {
			return nil, fmt.Errorf("multiple deposit addresses found for address: %s", *utxoAddress)
		}
		return nil, err
	}
	if !depositAddress.OwnerIdentityPubkey.Equals(reqIDPubKey) {
		return nil, fmt.Errorf("requested public key does not match public key found for address: %s", *utxoAddress)
	}
	rootSigningPubKey, err := keys.ParsePublicKey(req.GetRootTxSigningJob().GetSigningPublicKey())
	if err != nil {
		return nil, fmt.Errorf("invalid root tx signing public key: %w", err)
	}
	refundSigningPubKey, err := keys.ParsePublicKey(req.GetRefundTxSigningJob().GetSigningPublicKey())
	if err != nil {
		return nil, fmt.Errorf("invalid refund tx signing public key: %w", err)
	}
	if !depositAddress.OwnerSigningPubkey.Equals(rootSigningPubKey) || !depositAddress.OwnerSigningPubkey.Equals(refundSigningPubKey) {
		return nil, fmt.Errorf("unexpected signing public key")
	}

	txConfirmed := !depositAddress.AvailabilityConfirmedAt.IsZero()

	if txConfirmed && depositAddress.ConfirmationTxid != "" {
		onChainTxid := onChainTx.TxHash().String()
		if onChainTxid != depositAddress.ConfirmationTxid {
			return nil, fmt.Errorf("transaction ID does not match confirmed transaction ID")
		}
	}

	// Existing flow
	cpfpRootTx, err := common.TxFromRawTxBytes(req.RootTxSigningJob.RawTx)
	if err != nil {
		return nil, err
	}
	err = o.verifyRootTransaction(cpfpRootTx, onChainTx, req.OnChainUtxo.Vout, false)
	if err != nil {
		return nil, err
	}

	cpfpRefundTx, err := common.TxFromRawTxBytes(req.RefundTxSigningJob.RawTx)
	if err != nil {
		return nil, err
	}

	cpfpRootTxSigHash, err := common.SigHashFromTx(cpfpRootTx, 0, onChainOutput)
	if err != nil {
		return nil, err
	}

	cpfpRefundTxSigHash, err := common.SigHashFromTx(cpfpRefundTx, 0, cpfpRootTx.TxOut[0])
	if err != nil {
		return nil, err
	}

	// Sign the root and refund transactions
	signingKeyShare, err := depositAddress.QuerySigningKeyshare().Only(ctx)
	if err != nil {
		return nil, err
	}
	verifyingKey := signingKeyShare.PublicKey.Add(depositAddress.OwnerSigningPubkey)

	userCpfpRootTxNonceCommitment := frost.SigningCommitment{}
	if err := userCpfpRootTxNonceCommitment.UnmarshalProto(req.GetRootTxSigningJob().GetSigningNonceCommitment()); err != nil {
		return nil, err
	}
	userCpfpRefundTxNonceCommitment := frost.SigningCommitment{}
	if err := userCpfpRefundTxNonceCommitment.UnmarshalProto(req.GetRefundTxSigningJob().GetSigningNonceCommitment()); err != nil {
		return nil, err
	}

	signingJobs := []*helper.SigningJob{
		{
			JobID:             uuid.New(),
			SigningKeyshareID: signingKeyShare.ID,
			Message:           cpfpRootTxSigHash,
			VerifyingKey:      &verifyingKey,
			UserCommitment:    &userCpfpRootTxNonceCommitment,
		},
		{
			JobID:             uuid.New(),
			SigningKeyshareID: signingKeyShare.ID,
			Message:           cpfpRefundTxSigHash,
			VerifyingKey:      &verifyingKey,
			UserCommitment:    &userCpfpRefundTxNonceCommitment,
		},
	}

	// New flow
	directRootTxSigningJob := req.GetDirectRootTxSigningJob()
	directRefundTxSigningJob := req.GetDirectRefundTxSigningJob()

	if directRootTxSigningJob != nil || directRefundTxSigningJob != nil {
		networkString := network.String()
		if knobs.GetKnobsService(ctx).GetValueTarget(knobs.KnobEnforceNoDirectTransactionsFromDepositTx, &networkString, 0) > 0 {
			return nil, errors.InvalidArgumentInvalidVersion(fmt.Errorf("direct root tx signing job and direct refund tx signing job are deprecated, please upgrade to the latest SDK version"))
		}
	}

	directFromCpfpRefundTxSigningJob := req.GetDirectFromCpfpRefundTxSigningJob()

	if directFromCpfpRefundTxSigningJob == nil {
		networkString := network.String()
		if knobs.GetKnobsService(ctx).GetValueTarget(knobs.KnobRequireDirectFromCPFPRefund, &networkString, 0) > 0 {
			return nil, fmt.Errorf("DirectFromCpfpRefundTxSigningJob is required. Please upgrade to the latest SDK version")
		}
	} else {
		directFromCpfpRefundTx, err := common.TxFromRawTxBytes(directFromCpfpRefundTxSigningJob.GetRawTx())
		if err != nil {
			return nil, err
		}
		if err := o.verifyRefundTransaction(cpfpRootTx, directFromCpfpRefundTx); err != nil {
			return nil, err
		}
		if len(cpfpRootTx.TxOut) == 0 {
			return nil, fmt.Errorf("vout out of bounds, root tx has no outputs")
		}
		directFromCpfpRefundTxSigHash, err := common.SigHashFromTx(directFromCpfpRefundTx, 0, cpfpRootTx.TxOut[0])
		if err != nil {
			return nil, err
		}

		userDirectFromCpfpRefundTxNonceCommitment := frost.SigningCommitment{}
		if err := userDirectFromCpfpRefundTxNonceCommitment.UnmarshalProto(directFromCpfpRefundTxSigningJob.GetSigningNonceCommitment()); err != nil {
			return nil, err
		}
		signingJobs = append(
			signingJobs,
			&helper.SigningJob{
				JobID:             uuid.New(),
				SigningKeyshareID: signingKeyShare.ID,
				Message:           directFromCpfpRefundTxSigHash,
				VerifyingKey:      &verifyingKey,
				UserCommitment:    &userDirectFromCpfpRefundTxNonceCommitment,
			},
		)
	}

	// Process direct root and refund txs if both are provided
	if directRootTxSigningJob != nil && directRefundTxSigningJob != nil {
		directRootTx, err := common.TxFromRawTxBytes(req.DirectRootTxSigningJob.RawTx)
		if err != nil {
			return nil, err
		}
		err = o.verifyRootTransaction(directRootTx, onChainTx, req.OnChainUtxo.Vout, true)
		if err != nil {
			return nil, err
		}
		directRootTxSigHash, err := common.SigHashFromTx(directRootTx, 0, onChainOutput)
		if err != nil {
			return nil, err
		}
		directRefundTx, err := common.TxFromRawTxBytes(req.DirectRefundTxSigningJob.RawTx)
		if err != nil {
			return nil, err
		}
		err = o.verifyRefundTransaction(cpfpRootTx, cpfpRefundTx)
		if err != nil {
			return nil, err
		}
		err = o.verifyRefundTransaction(directRootTx, directRefundTx)
		if err != nil {
			return nil, err
		}
		if len(cpfpRootTx.TxOut) == 0 {
			return nil, fmt.Errorf("vout out of bounds, root tx has no outputs")
		}
		if len(directRootTx.TxOut) == 0 {
			return nil, fmt.Errorf("vout out of bounds, direct root tx has no outputs")
		}
		directRefundTxSigHash, err := common.SigHashFromTx(directRefundTx, 0, directRootTx.TxOut[0])
		if err != nil {
			return nil, err
		}
		userDirectRootTxNonceCommitment := frost.SigningCommitment{}
		if err := userDirectRootTxNonceCommitment.UnmarshalProto(directRootTxSigningJob.GetSigningNonceCommitment()); err != nil {
			return nil, err
		}
		userDirectRefundTxNonceCommitment := frost.SigningCommitment{}
		if err := userDirectRefundTxNonceCommitment.UnmarshalProto(directRefundTxSigningJob.GetSigningNonceCommitment()); err != nil {
			return nil, err
		}
		signingJobs = append(
			signingJobs,
			&helper.SigningJob{
				JobID:             uuid.New(),
				SigningKeyshareID: signingKeyShare.ID,
				Message:           directRootTxSigHash,
				VerifyingKey:      &verifyingKey,
				UserCommitment:    &userDirectRootTxNonceCommitment,
			},
			&helper.SigningJob{
				JobID:             uuid.New(),
				SigningKeyshareID: signingKeyShare.ID,
				Message:           directRefundTxSigHash,
				VerifyingKey:      &verifyingKey,
				UserCommitment:    &userDirectRefundTxNonceCommitment,
			},
		)
	} else if directRootTxSigningJob != nil || directRefundTxSigningJob != nil {
		return nil, fmt.Errorf("direct root tx signing job and direct refund tx signing job must both be provided or neither of them")
	}

	networkString := network.String()
	if knobs.GetKnobsService(ctx).GetValueTarget(knobs.KnobEnableDepositFlowValidation, &networkString, 0) > 0 {
		combinedPublicKey := signingKeyShare.PublicKey.Add(depositAddress.OwnerSigningPubkey)

		var directRootTxRaw, directRefundTxRaw []byte
		if req.DirectRootTxSigningJob != nil {
			directRootTxRaw = req.DirectRootTxSigningJob.RawTx
		}
		if req.DirectRefundTxSigningJob != nil {
			directRefundTxRaw = req.DirectRefundTxSigningJob.RawTx
		}
		var directFromCpfpRefundTxRaw []byte
		if req.DirectFromCpfpRefundTxSigningJob != nil {
			directFromCpfpRefundTxRaw = req.DirectFromCpfpRefundTxSigningJob.RawTx
		}

		err = validateBitcoinTransactions(
			ctx,
			req.OnChainUtxo.RawTx,
			req.OnChainUtxo.Vout,
			req.RootTxSigningJob.RawTx,
			req.RefundTxSigningJob.RawTx,
			directFromCpfpRefundTxRaw,
			directRootTxRaw,
			directRefundTxRaw,
			combinedPublicKey,
			depositAddress.OwnerSigningPubkey,
			networkString,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to validate transaction in tree creation request: %w", err)
		}
	}
	signingResults, err := helper.SignFrost(ctx, config, signingJobs)
	if err != nil {
		return nil, err
	}
	if len(signingResults) < 2 {
		return nil, fmt.Errorf("expected at least 2 signing results, got %d", len(signingResults))
	}

	cpfpNodeTxSigningResult, err := signingResults[0].MarshalProto()
	if err != nil {
		return nil, err
	}
	cpfpRefundTxSigningResult, err := signingResults[1].MarshalProto()
	if err != nil {
		return nil, err
	}
	var directNodeTxSigningResult, directRefundTxSigningResult, directFromCpfpRefundTxSigningResult *pb.SigningResult
	resultIndex := 2
	if directFromCpfpRefundTxSigningJob != nil {
		directFromCpfpRefundTxSigningResult, err = signingResults[resultIndex].MarshalProto()
		if err != nil {
			return nil, err
		}
		resultIndex++
	}
	if directRootTxSigningJob != nil && directRefundTxSigningJob != nil {
		directNodeTxSigningResult, err = signingResults[resultIndex].MarshalProto()
		if err != nil {
			return nil, err
		}
		directRefundTxSigningResult, err = signingResults[resultIndex+1].MarshalProto()
		if err != nil {
			return nil, err
		}
	}
	// Create the tree
	txid := onChainTx.TxHash()

	// Check if a tree already exists for this deposit
	existingTree, err := db.Tree.Query().
		Where(tree.BaseTxid(st.NewTxID(txid))).
		Where(tree.Vout(int16(req.OnChainUtxo.Vout))).
		First(ctx)

	if err != nil && !ent.IsNotFound(err) {
		return nil, fmt.Errorf("failed to query for existing tree: %w", err)
	}

	logger := logging.GetLoggerFromContext(ctx)

	var entTree *ent.Tree
	if existingTree != nil {
		// Tree already exists, use the existing one
		entTree = existingTree
		logger.Sugar().Infof("Found existing tree %s for txid %s", existingTree.ID, txid)
	} else {
		if depositAddress.Edges.Tree != nil {
			return nil, errors.AlreadyExistsDuplicateOperation(fmt.Errorf("deposit address already has a tree"))
		}
		// Create new tree
		treeMutator := db.Tree.
			Create().
			SetOwnerIdentityPubkey(depositAddress.OwnerIdentityPubkey).
			SetNetwork(network).
			SetBaseTxid(st.NewTxID(txid)).
			SetVout(int16(req.OnChainUtxo.Vout)).
			SetDepositAddress(depositAddress)

		if txConfirmed {
			treeMutator.SetStatus(st.TreeStatusAvailable)
		} else {
			treeMutator.SetStatus(st.TreeStatusPending)
		}
		entTree, err = treeMutator.Save(ctx)
		if err != nil {
			if ent.IsConstraintError(err) {
				return nil, errors.AlreadyExistsDuplicateOperation(fmt.Errorf("tree already exists: %w", err))
			}
			return nil, err
		}
	}
	var directTx []byte
	if req.DirectRootTxSigningJob != nil {
		directTx = req.DirectRootTxSigningJob.RawTx
	}
	var directRefundTx []byte
	if req.DirectRefundTxSigningJob != nil {
		directRefundTx = req.DirectRefundTxSigningJob.RawTx
	}
	var directFromCpfpRefundTx []byte
	if req.DirectFromCpfpRefundTxSigningJob != nil {
		directFromCpfpRefundTx = req.DirectFromCpfpRefundTxSigningJob.RawTx
	}
	// Check if a tree node already exists for this deposit
	existingRoot, err := db.TreeNode.Query().
		Where(treenode.OwnerIdentityPubkey(depositAddress.OwnerIdentityPubkey)).
		Where(treenode.OwnerSigningPubkey(depositAddress.OwnerSigningPubkey)).
		Where(treenode.Value(uint64(onChainOutput.Value))).
		Where(treenode.Vout(int16(req.OnChainUtxo.Vout))).
		ForUpdate().
		Only(ctx)

	if err != nil && !ent.IsNotFound(err) {
		return nil, fmt.Errorf("failed to query for existing tree node: %w", err)
	}

	var root *ent.TreeNode
	if existingRoot != nil {
		if existingRoot.Status != st.TreeNodeStatusCreating {
			return nil, errors.FailedPreconditionInvalidState(fmt.Errorf("expected tree node %s to be in creating status; got %s", existingRoot.ID, existingRoot.Status))
		}
		logger.Sugar().Infof(
			"Tree node %s already exists (deposit address %s), updating with new txid %s",
			existingRoot.ID,
			depositAddress.ID,
			txid,
		)
		// Tree node already exists, update it with new transaction data
		root, err = existingRoot.Update().
			SetRawTx(req.RootTxSigningJob.RawTx).
			SetRawRefundTx(req.RefundTxSigningJob.RawTx).
			SetDirectTx(directTx).
			SetDirectRefundTx(directRefundTx).
			SetDirectFromCpfpRefundTx(directFromCpfpRefundTx).
			Save(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to update existing tree node: %w", err)
		}
	} else {
		// Create new tree node
		treeNodeMutator := db.TreeNode.
			Create().
			SetTree(entTree).
			SetNetwork(entTree.Network).
			SetStatus(st.TreeNodeStatusCreating).
			SetOwnerIdentityPubkey(depositAddress.OwnerIdentityPubkey).
			SetOwnerSigningPubkey(depositAddress.OwnerSigningPubkey).
			SetValue(uint64(onChainOutput.Value)).
			SetVerifyingPubkey(verifyingKey).
			SetSigningKeyshare(signingKeyShare).
			SetRawTx(req.RootTxSigningJob.RawTx).
			SetRawRefundTx(req.RefundTxSigningJob.RawTx).
			SetDirectTx(directTx).
			SetDirectRefundTx(directRefundTx).
			SetDirectFromCpfpRefundTx(directFromCpfpRefundTx).
			SetVout(int16(req.OnChainUtxo.Vout))

		if depositAddress.NodeID != uuid.Nil {
			treeNodeMutator.SetID(depositAddress.NodeID)
		}

		root, err = treeNodeMutator.Save(ctx)
		if err != nil {
			return nil, err
		}
	}
	entTree, err = entTree.Update().SetRoot(root).Save(ctx)
	if err != nil {
		return nil, err
	}

	return &pb.StartDepositTreeCreationResponse{
		TreeId: entTree.ID.String(),
		RootNodeSignatureShares: &pb.NodeSignatureShares{
			NodeId:                              root.ID.String(),
			NodeTxSigningResult:                 cpfpNodeTxSigningResult,
			RefundTxSigningResult:               cpfpRefundTxSigningResult,
			VerifyingKey:                        verifyingKey.Serialize(),
			DirectNodeTxSigningResult:           directNodeTxSigningResult,
			DirectRefundTxSigningResult:         directRefundTxSigningResult,
			DirectFromCpfpRefundTxSigningResult: directFromCpfpRefundTxSigningResult,
		},
	}, nil
}

func (o *DepositHandler) verifyRootTransaction(rootTx *wire.MsgTx, onChainTx *wire.MsgTx, onChainVout uint32, isDirect bool) error {
	if err := common.ValidateBitcoinTxVersion(rootTx); err != nil {
		return fmt.Errorf("root tx version validation failed: %w", err)
	}

	if len(rootTx.TxIn) == 0 || len(rootTx.TxOut) == 0 {
		return fmt.Errorf("root transaction should have at least 1 input and 1 output")
	}

	if len(onChainTx.TxOut) <= int(onChainVout) {
		return fmt.Errorf("vout out of bounds")
	}

	// Check root transaction input
	if rootTx.TxIn[0].PreviousOutPoint.Index != onChainVout || rootTx.TxIn[0].PreviousOutPoint.Hash != onChainTx.TxHash() {
		return fmt.Errorf("root transaction must use the on chain utxo as input")
	}

	// Check root transaction output address
	if !bytes.Equal(rootTx.TxOut[0].PkScript, onChainTx.TxOut[onChainVout].PkScript) {
		return fmt.Errorf("root transaction must pay to the same deposit address")
	}

	// Check root transaction amount
	onChainValue := onChainTx.TxOut[onChainVout].Value
	if isDirect {
		onChainValue = common.MaybeApplyFee(onChainValue)
	}
	if rootTx.TxOut[0].Value != onChainValue {
		return fmt.Errorf("root transaction has wrong value: root tx value %d != on-chain tx value %d", rootTx.TxOut[0].Value, onChainTx.TxOut[onChainVout].Value)
	}
	return nil
}

func (o *DepositHandler) verifyRefundTransaction(tx *wire.MsgTx, refundTx *wire.MsgTx) error {
	if err := common.ValidateBitcoinTxVersion(tx); err != nil {
		return fmt.Errorf("tx version validation failed: %w", err)
	}

	if err := common.ValidateBitcoinTxVersion(refundTx); err != nil {
		return fmt.Errorf("refund tx version validation failed: %w", err)
	}

	// Refund transaction should have the given tx as input
	previousTxid := tx.TxHash()
	for _, refundTxIn := range refundTx.TxIn {
		if refundTxIn.PreviousOutPoint.Hash == previousTxid && refundTxIn.PreviousOutPoint.Index == 0 {
			return nil
		}
	}

	return fmt.Errorf("refund transaction should have the node tx as input")
}

type UtxoSwapRequestType int

const (
	UtxoSwapRequestFixed UtxoSwapRequestType = iota
	UtxoSwapRequestMaxFee
)

type UtxoSwapStatementType int

const (
	UtxoSwapStatementTypeCreated UtxoSwapStatementType = iota
	UtxoSwapStatementTypeRollback
	UtxoSwapStatementTypeCompleted
)

func (s UtxoSwapStatementType) String() string {
	return [...]string{"Created", "Rollback", "Completed"}[s]
}

// Holds an UTXO that was verified by the validating function as confirmed on
// the blockchain. Can be used in functions that require a valid UTXO.
type VerifiedTargetUtxo struct {
	// DB record of the confirmed utxo stored by Chain Watcher
	inner *ent.Utxo
	// Cached transaction hash of the UTXO. Different from ent UTXO txid, which is
	// txid string stored as bytes.
	txid chainhash.Hash
}

func (u *VerifiedTargetUtxo) Hash() *chainhash.Hash {
	return &u.txid
}

func (u *VerifiedTargetUtxo) Vout() uint32 {
	return u.inner.Vout
}

// Verifies that an UTXO is confirmed on the blockchain and has sufficient confirmations.
func VerifiedTargetUtxoFromRequest(ctx context.Context, config *so.Config, db *ent.Client, network btcnetwork.Network, reqUtxo *pb.UTXO) (*VerifiedTargetUtxo, error) {
	if reqUtxo == nil {
		return nil, fmt.Errorf("requested UTXO is nil")
	}

	if len(reqUtxo.Txid) != chainhash.HashSize {
		return nil, fmt.Errorf("invalid txid length: expected %d bytes, got %d bytes", chainhash.HashSize, len(reqUtxo.Txid))
	}

	txidString := hex.EncodeToString(reqUtxo.Txid)
	reqUtxoTxid, err := chainhash.NewHashFromStr(txidString)
	if err != nil {
		return nil, fmt.Errorf("failed to parse on-chain txid: %w", err)
	}
	blockHeight, err := db.BlockHeight.Query().Where(
		blockheight.NetworkEQ(network),
	).Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to find block height: %w", err)
	}

	targetUtxo, err := db.Utxo.Query().
		Where(entutxo.NetworkEQ(network)).
		Where(entutxo.Txid(reqUtxo.Txid)).
		Where(entutxo.Vout(reqUtxo.Vout)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, errors.NotFoundMissingEntity(fmt.Errorf("utxo not found: txid: %s vout: %d", reqUtxoTxid.String(), reqUtxo.Vout))
		}
		return nil, fmt.Errorf("failed to get target utxo: %w", err)
	}

	threshold := DefaultDepositConfirmationThreshold
	if bitcoinConfig, ok := config.BitcoindConfigs[strings.ToLower(network.String())]; ok {
		threshold = bitcoinConfig.DepositConfirmationThreshold
	}
	if blockHeight.Height-targetUtxo.BlockHeight+1 < int64(threshold) {
		return nil, errors.FailedPreconditionInsufficientConfirmations(fmt.Errorf("deposit tx doesn't have enough confirmations: confirmation height: %d current block height: %d", targetUtxo.BlockHeight, blockHeight.Height))
	}
	return &VerifiedTargetUtxo{inner: targetUtxo, txid: *reqUtxoTxid}, nil
}

// A helper function to generate a FROST signature for a spend transaction. This
// function is used in the static deposit address flow to create a spending
// transaction for the SSP.
//
// Parameters:
//   - ctx: The context for the operation
//   - config: The service configuration containing network and operator settings
//   - depositAddress: The deposit address entity containing:
//   - targetUtxo: The target UTXO entity containing:
//   - spendTxRaw: The raw spend transaction bytes
//   - userSpendTxNonceCommitment: The user's nonce commitment for the spend tx signing job
//
// Returns:
//   - []byte: The verifying public key to verify the combined signature in frost aggregate.
//   - *pb.SigningResult: Signing result containing a partial FROST signature that can
//     be aggregated with other signatures.
//   - error if the operation fails.
func getSpendTxSigningResult(ctx context.Context, config *so.Config, depositAddress *ent.DepositAddress, targetUtxo *VerifiedTargetUtxo, spendTxRaw []byte, userSpendTxNonceCommitment *frost.SigningCommitment) (keys.Public, *pb.SigningResult, error) {
	signingKeyShare, err := depositAddress.QuerySigningKeyshare().Only(ctx)
	if err != nil {
		return keys.Public{}, nil, fmt.Errorf("failed to get signing keyshare: %w", err)
	}
	verifyingKey := signingKeyShare.PublicKey.Add(depositAddress.OwnerSigningPubkey)
	spendTxSigHash, _, err := GetTxSigningInfo(ctx, targetUtxo.inner, spendTxRaw)
	if err != nil {
		return keys.Public{}, nil, fmt.Errorf("failed to get spend tx sig hash: %w", err)
	}

	signingJobs := []*helper.SigningJob{{
		JobID:             uuid.New(),
		SigningKeyshareID: signingKeyShare.ID,
		Message:           spendTxSigHash,
		VerifyingKey:      &verifyingKey,
		UserCommitment:    userSpendTxNonceCommitment,
	}}
	signingResults, err := helper.SignFrost(ctx, config, signingJobs)
	if err != nil {
		return keys.Public{}, nil, fmt.Errorf("failed to sign spend tx: %w", err)
	}
	if len(signingResults) == 0 {
		return keys.Public{}, nil, fmt.Errorf("no signing results returned for spend tx")
	}

	spendTxSigningResult, err := signingResults[0].MarshalProto()
	if err != nil {
		return keys.Public{}, nil, fmt.Errorf("failed to marshal spend tx signing result: %w", err)
	}
	return verifyingKey, spendTxSigningResult, nil
}

func GetTxSigningInfo(ctx context.Context, targetUtxo *ent.Utxo, spendTxRaw []byte) ([]byte, uint64, error) {
	logger := logging.GetLoggerFromContext(ctx)

	onChainTxOut := wire.NewTxOut(int64(targetUtxo.Amount), targetUtxo.PkScript)
	spendTx, err := common.TxFromRawTxBytes(spendTxRaw)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to parse spend tx: %w", err)
	}

	spendTxSigHash, err := common.SigHashFromTx(spendTx, 0, onChainTxOut)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to get spend tx sig hash: %w", err)
	}

	const maxSats uint64 = 21_000_000 * 100_000_000

	var total uint64
	for i, o := range spendTx.TxOut {
		if o.Value < 0 {
			return nil, 0, fmt.Errorf("txout[%d]: negative value %d", i, o.Value)
		}
		v := uint64(o.Value)
		if v > maxSats {
			return nil, 0, fmt.Errorf("txout[%d]: value %d exceeds %d", i, v, maxSats)
		}
		if total > maxSats-v {
			return nil, 0, fmt.Errorf("total amount overflow: %d + %d", total, v)
		}
		total += v
	}

	if total > maxSats {
		return nil, 0, fmt.Errorf("total amount %d exceeds %d", total, maxSats)
	}
	logger.Sugar().Debugf("Retrieved %x as spend tx sighash", spendTxSigHash)
	return spendTxSigHash, total, nil
}

func GetSpendTxSigningResult(ctx context.Context, config *so.Config, utxo *pb.UTXO, spendTxSigningJob *pb.SigningJob) (*pb.SigningResult, *pb.DepositAddressQueryResult, error) {
	if spendTxSigningJob == nil || spendTxSigningJob.SigningNonceCommitment == nil || spendTxSigningJob.RawTx == nil {
		return nil, nil, fmt.Errorf("spend tx signing job is not valid")
	}
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}
	network, err := btcnetwork.FromProtoNetwork(utxo.GetNetwork())
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get schema network: %w", err)
	}

	targetUtxo, err := VerifiedTargetUtxoFromRequest(ctx, config, db, network, utxo)
	if err != nil {
		return nil, nil, err
	}
	depositAddress, err := targetUtxo.inner.QueryDepositAddress().Only(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get deposit address: %w", err)
	}

	// Recover the signature for the utxo spend
	// Execute signing jobs with all operators and create a refund transaction
	userRootTxNonceCommitment := frost.SigningCommitment{}
	if err := userRootTxNonceCommitment.UnmarshalProto(spendTxSigningJob.GetSigningNonceCommitment()); err != nil {
		return nil, nil, fmt.Errorf("failed to create signing commitment: %w", err)
	}
	verifyingKey, spendTxSigningResult, err := getSpendTxSigningResult(ctx, config, depositAddress, targetUtxo, spendTxSigningJob.RawTx, &userRootTxNonceCommitment)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get spend tx signing result: %w", err)
	}

	nodeIDStr := depositAddress.NodeID.String()
	return spendTxSigningResult, &pb.DepositAddressQueryResult{
		DepositAddress:       depositAddress.Address,
		UserSigningPublicKey: depositAddress.OwnerSigningPubkey.Serialize(),
		VerifyingPublicKey:   verifyingKey.Serialize(),
		LeafId:               &nodeIDStr,
	}, nil
}

func (o *DepositHandler) GetUtxosForAddress(ctx context.Context, req *pb.GetUtxosForAddressRequest) (*pb.GetUtxosForAddressResponse, error) {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}
	depositAddress, err := db.DepositAddress.Query().Where(depositaddress.Address(req.Address)).Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get deposit address: %w", err)
	}

	network, err := btcnetwork.FromProtoNetwork(req.Network)
	if err != nil {
		return nil, fmt.Errorf("failed to get schema network: %w", err)
	}

	if !utils.IsBitcoinAddressForNetwork(req.Address, network) {
		return nil, fmt.Errorf("deposit address is not aligned with the requested network")
	}

	currentBlockHeight, err := db.BlockHeight.Query().Where(blockheight.NetworkEQ(network)).Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get current block height: %w", err)
	}

	threshold := DefaultDepositConfirmationThreshold
	if bitcoinConfig, ok := o.config.BitcoindConfigs[strings.ToLower(network.String())]; ok {
		threshold = bitcoinConfig.DepositConfirmationThreshold
	}

	var utxosResult []*pb.UTXO
	if depositAddress.IsStatic {
		if req.Limit > 100 || req.Limit == 0 {
			req.Limit = 100
		}
		query := depositAddress.QueryUtxo().
			Where(entutxo.BlockHeightLTE(currentBlockHeight.Height - int64(threshold))).
			Offset(int(req.Offset)).
			Limit(int(req.Limit)).
			Order(entutxo.ByBlockHeight(sql.OrderDesc()))
		if req.ExcludeClaimed {
			query = query.Where(func(s *sql.Selector) {
				// Exclude UTXOs that have non-cancelled UTXO swaps
				subquery := sql.Select(utxoswap.UtxoColumn).
					From(sql.Table(utxoswap.Table)).
					Where(sql.NEQ(utxoswap.FieldStatus, string(st.UtxoSwapStatusCancelled)))
				s.Where(sql.NotIn(s.C(entutxo.FieldID), subquery))
			})
		}
		utxos, err := query.All(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get utxo: %w", err)
		}
		if len(utxos) == 0 {
			return &pb.GetUtxosForAddressResponse{
				Utxos: []*pb.UTXO{},
			}, nil
		}

		for _, utxo := range utxos {
			utxosResult = append(utxosResult, &pb.UTXO{
				Txid:    utxo.Txid,
				Vout:    utxo.Vout,
				Network: req.Network,
			})
		}
	} else if len(depositAddress.ConfirmationTxid) > 0 {
		txid, err := hex.DecodeString(depositAddress.ConfirmationTxid)
		if err != nil {
			return nil, fmt.Errorf("failed to decode confirmation txid: %w", err)
		}

		if depositAddress.ConfirmationHeight <= currentBlockHeight.Height-int64(threshold) {
			utxosResult = append(utxosResult, &pb.UTXO{
				Txid:    txid,
				Vout:    0,
				Network: req.Network,
			})
		}
	}

	return &pb.GetUtxosForAddressResponse{Utxos: utxosResult}, nil
}

// validateBitcoinTransactions validates Bitcoin transactions in a deposit flow.
// Parameters:
//   - depositTx: Raw bytes of the on-chain deposit transaction
//   - vout: Output index in the deposit transaction
//   - cpfpRootTx: Raw bytes of the CPFP root transaction
//   - cpfpRefundTx: Raw bytes of the CPFP refund transaction
//   - directFromCpfpRefundTx: Optional raw bytes of direct-from-CPFP refund transaction
//   - directRootTx: Optional raw bytes of direct root transaction
//   - directRefundTx: Optional raw bytes of direct refund transaction
//   - rootDestPubkey: Public key for root transaction destination
//   - refundDestPubkey: Public key for refund transaction destination
//   - networkString: Network identifier string
func validateBitcoinTransactions(
	ctx context.Context,
	depositTx []byte,
	vout uint32,
	cpfpRootTx []byte,
	cpfpRefundTx []byte,
	directFromCpfpRefundTx []byte,
	directRootTx []byte,
	directRefundTx []byte,
	rootDestPubkey keys.Public,
	refundDestPubkey keys.Public,
	networkString string,
) error {
	// Validate cpfp root tx based on deposit tx
	err := bitcointransaction.VerifyTransactionWithSource(ctx, cpfpRootTx, depositTx, vout, 0, bitcointransaction.TxTypeNodeCPFP, rootDestPubkey, networkString)
	if err != nil {
		return fmt.Errorf("cpfp root transaction verification failed: %w", err)
	}

	// We add TimeLockInterval to ensure that expectedTx has locktime
	// set to InitialTimeLock
	cpfpTimelock := spark.InitialTimeLock + spark.TimeLockInterval
	// Validate cpfp refund tx based on cpfp root tx
	err = bitcointransaction.VerifyTransactionWithSource(ctx, cpfpRefundTx, cpfpRootTx, 0, cpfpTimelock, bitcointransaction.TxTypeRefundCPFP, refundDestPubkey, networkString)
	if err != nil {
		return fmt.Errorf("cpfp refund transaction verification failed: %w", err)
	}

	// Validate direct-from-cpfp refund tx based on cpfp root tx (If provided)
	if len(directFromCpfpRefundTx) > 0 {
		err = bitcointransaction.VerifyTransactionWithSource(ctx, directFromCpfpRefundTx, cpfpRootTx, 0, cpfpTimelock, bitcointransaction.TxTypeRefundDirectFromCPFP, refundDestPubkey, networkString)
		if err != nil {
			return fmt.Errorf("direct-from-cpfp refund transaction verification failed: %w", err)
		}
	}

	// Only validate direct tx if both are provided
	// Validate direct refund tx based on direct root tx
	if len(directRootTx) > 0 && len(directRefundTx) > 0 {
		err = bitcointransaction.VerifyTransactionWithSource(ctx, directRootTx, depositTx, vout, 0, bitcointransaction.TxTypeNodeDirect, rootDestPubkey, networkString)
		if err != nil {
			return fmt.Errorf("direct root transaction verification failed: %w", err)
		}

		err = bitcointransaction.VerifyTransactionWithSource(ctx, directRefundTx, directRootTx, 0, cpfpTimelock, bitcointransaction.TxTypeRefundDirect, refundDestPubkey, networkString)
		if err != nil {
			return fmt.Errorf("direct refund transaction verification failed: %w", err)
		}
	}
	return nil
}

// FinalizeDepositTreeCreation finalizes the tree creation for a deposit by aggregating
// user signature shares with SE signature shares to produce final signatures.
// This is part of the new deposit flow where:
// 1. Client calls get_signing_commitments to get SE commitments
// 2. Client signs locally to produce signature shares
// 3. Client calls this endpoint to have SE aggregate and finalize the tree
func (o *DepositHandler) FinalizeDepositTreeCreation(ctx context.Context, config *so.Config, req *pb.FinalizeDepositTreeCreationRequest) (*pb.FinalizeDepositTreeCreationResponse, error) {
	ctx, span := tracer.Start(ctx, "DepositHandler.FinalizeDepositTreeCreation")
	defer span.End()

	logger := logging.GetLoggerFromContext(ctx)

	// Validate request
	err := validateFinalizeDepositTreeCreationRequest(req)
	if err != nil {
		return nil, err
	}

	// Validate identity
	reqIDPubKey, err := validateIdentity(ctx, config, req.IdentityPublicKey)
	if err != nil {
		return nil, err
	}

	network, err := convertAndValidateProtoNetwork(config, req.OnChainUtxo.Network)
	if err != nil {
		return nil, fmt.Errorf("invalid network %s: %w", req.OnChainUtxo.Network, err)
	}

	// Step 1: Validate request and get deposit address
	depositAddress, onChainTx, onChainOutput, err := loadAndValidateDepositAddress(ctx, network, req, reqIDPubKey)
	if err != nil {
		return nil, err
	}

	// Check if tree already exists for this deposit address
	if depositAddress.Edges.Tree != nil {
		return nil, errors.AlreadyExistsDuplicateOperation(fmt.Errorf("tree already exists for deposit address %s", depositAddress.Address))
	}

	logger.Sugar().Infof("Finalizing deposit tree creation for address %s", depositAddress.Address)

	// Step 2: Prepare signing jobs with pregenerated nonces
	signingJobs, verifyingKey, err := o.prepareSigningJobs(req, depositAddress, onChainTx, onChainOutput)
	if err != nil {
		return nil, err
	}

	// Convert to SigningJobWithPregeneratedNonce using SE commitments from request
	signingJobsWithNonce, err := o.convertToSigningJobsWithPregeneratedNonce(signingJobs, req)
	if err != nil {
		return nil, fmt.Errorf("failed to convert signing jobs: %w", err)
	}

	// Step 3: SE signs all transactions using pregenerated commitments
	logger.Sugar().Infof("SE signing %d transactions for deposit using pregenerated nonces", len(signingJobsWithNonce))
	signingResults, err := helper.SignFrostWithPregeneratedNonce(ctx, config, signingJobsWithNonce)
	if err != nil {
		return nil, fmt.Errorf("failed to sign transactions: %w", err)
	}
	if len(signingResults) != len(signingJobs) {
		return nil, fmt.Errorf("expected %d signing results, got %d", len(signingJobs), len(signingResults))
	}
	for i, signingResult := range signingResults {
		if signingResult.JobID != signingJobs[i].JobID {
			return nil, fmt.Errorf("signing results do not match signing jobs (i=%d resultID=%s jobID=%s)", i, signingResult.JobID, signingJobs[i].JobID)
		}
	}

	// Step 4: Aggregate signatures (SE + user)
	rootSigningPubKey, err := keys.ParsePublicKey(req.RootTxSigningJob.SigningPublicKey)
	if err != nil {
		return nil, fmt.Errorf("failed to parse root signing key: %w", err)
	}
	signatures, err := o.aggregateSignatures(ctx, config, req, signingResults, verifyingKey, rootSigningPubKey)
	if err != nil {
		return nil, err
	}

	logger.Sugar().Infof("Successfully aggregated %d signatures", len(signatures))

	// Step 5: Apply signatures to transactions
	signedCpfpRootTx, signedCpfpRefundTx, signedDirectFromCpfpRefundTx, err := o.applySignaturesToTransactions(req, signatures)
	if err != nil {
		return nil, err
	}

	// Step 6: Create tree and node in database with signed transactions
	// Note: The tree is automatically linked to deposit address via SetDepositAddress() in createTreeAndNode
	createdTree, createdNode, err := o.createTreeAndNode(ctx, depositAddress, onChainTx, onChainOutput, req, network, verifyingKey, signedCpfpRootTx, signedCpfpRefundTx, signedDirectFromCpfpRefundTx)
	if err != nil {
		return nil, err
	}

	logger.Sugar().Infof("Successfully finalized deposit tree with root node %s", createdNode.ID)

	// Marshal the response BEFORE sending gossip (which commits the transaction)
	// MarshalSparkProto may need to load edges from the database
	pbNode, err := createdNode.MarshalSparkProto(ctx)
	if err != nil {
		return nil, err
	}

	// Step 7: Send gossip to other SOs
	// Note: CreateCommitAndSendGossipMessage will commit the transaction
	err = o.sendFinalizeNodeGossip(ctx, createdTree, createdNode)
	if err != nil {
		logger.With(zap.Error(err)).Sugar().Errorf(
			"failed to send gossip for new tree (%s) and node (%s)",
			createdTree.ID.String(), createdNode.ID.String())
		// Don't return error - gossip failure shouldn't fail the entire operation
		// The local SO will process the gossip through the normal retry mechanism
	}

	// Return response
	return &pb.FinalizeDepositTreeCreationResponse{
		RootNode: pbNode,
	}, nil
}

func convertAndValidateProtoNetwork(
	config *so.Config,
	protoNetwork pb.Network,
) (btcnetwork.Network, error) {
	network, err := btcnetwork.FromProtoNetwork(protoNetwork)
	if err != nil {
		return btcnetwork.Unspecified, err
	}
	if !config.IsNetworkSupported(network) {
		return btcnetwork.Unspecified, ErrInvalidNetwork
	}
	return network, nil
}
