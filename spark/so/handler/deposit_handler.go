package handler

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"strings"

	"entgo.io/ent/dialect/sql"
	"github.com/lightsparkdev/spark/common/keys"

	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/logging"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authz"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/blockheight"
	"github.com/lightsparkdev/spark/so/ent/depositaddress"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/tree"
	"github.com/lightsparkdev/spark/so/ent/treenode"
	"github.com/lightsparkdev/spark/so/ent/utxo"
	"github.com/lightsparkdev/spark/so/ent/utxoswap"
	"github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/helper"
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/lightsparkdev/spark/so/objects"
	"github.com/lightsparkdev/spark/so/staticdeposit"
	"github.com/lightsparkdev/spark/so/utils"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const DefaultDepositConfirmationThreshold = uint(3)

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

// GenerateDepositAddress generates a deposit address for the given public key.
// The address string is generated using provided network field in the request.
func (o *DepositHandler) GenerateDepositAddress(ctx context.Context, config *so.Config, req *pb.GenerateDepositAddressRequest) (*pb.GenerateDepositAddressResponse, error) {
	ctx, span := tracer.Start(ctx, "DepositHandler.GenerateDepositAddress")
	defer span.End()

	if req.GetIsStatic() && knobs.GetKnobsService(ctx).GetValue(knobs.KnobSoGenerateStaticDepositAddressV2, 0) > 0 {
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
	network, err := common.NetworkFromProtoNetwork(req.Network)
	if err != nil {
		return nil, err
	}
	schemaNetwork, err := common.SchemaNetworkFromNetwork(network)
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

	// TODO(LIG-8000): remove when we have a way to support multiple static deposit addresses per (identity, network).
	if req.GetIsStatic() {
		db, err := ent.GetDbFromContext(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
		}
		depositAddresses, err := db.DepositAddress.Query().
			Where(
				depositaddress.OwnerIdentityPubkey(reqIDPubKey),
				depositaddress.IsStatic(true),
			).
			All(ctx)
		if err != nil {
			return nil, err
		}
		// Find if there is already a static deposit address for this identity and network.
		for _, depositAddress := range depositAddresses {
			if utils.IsBitcoinAddressForNetwork(depositAddress.Address, network) {
				return nil, fmt.Errorf("static deposit address already exists: %s", depositAddress.Address)
			}
		}
	}

	logger.Info("Generating deposit address for public key", "public_key", reqSigningPubKey, "identity_public_key", reqIDPubKey)
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

	keysharePubKey, err := keys.ParsePublicKey(keyshare.PublicKey)
	if err != nil {
		return nil, err
	}
	combinedPublicKey := keysharePubKey.Add(reqSigningPubKey)
	depositAddress, err := common.P2TRAddressFromPublicKey(combinedPublicKey, network)
	if err != nil {
		return nil, err
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx: %w", err)
	}

	depositAddressMutator := db.DepositAddress.Create().
		SetSigningKeyshareID(keyshare.ID).
		SetOwnerIdentityPubkey(reqIDPubKey).
		SetOwnerSigningPubkey(reqSigningPubKey).
		SetNetwork(schemaNetwork).
		SetAddress(depositAddress)
	// Confirmation height is not set since nothing has been confirmed yet.

	if req.GetIsStatic() {
		depositAddressMutator.SetIsStatic(true)
		depositAddressMutator.SetIsDefault(true)
	} else if req.LeafId != nil {
		// Static deposit addresses are not allowed to have a leaf ID
		// because it would be meaningless.
		leafID, err := uuid.Parse(req.GetLeafId())
		if err != nil {
			return nil, err
		}
		depositAddressMutator.SetNodeID(leafID)
	}

	_, err = depositAddressMutator.Save(ctx)
	if err != nil {
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

	verifyingKey := keysharePubKey.Add(reqSigningPubKey)

	msg := common.ProofOfPossessionMessageHashForDepositAddress(reqIDPubKey.Serialize(), keysharePubKey.Serialize(), []byte(depositAddress))
	proofOfPossessionSignature, err := helper.GenerateProofOfPossessionSignatures(ctx, config, [][]byte{msg}, []*ent.SigningKeyshare{keyshare})
	if err != nil {
		return nil, err
	}
	return &pb.GenerateDepositAddressResponse{
		DepositAddress: &pb.Address{
			Address:      depositAddress,
			VerifyingKey: verifyingKey.Serialize(),
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

	network, err := common.NetworkFromProtoNetwork(req.Network)
	if err != nil {
		return nil, err
	}
	schemaNetwork, err := common.SchemaNetworkFromNetwork(network)
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

	// TODO(LIG-8000): remove when we have a way to support multiple static deposit addresses per (identity, network).
	depositAddress, err := db.DepositAddress.Query().
		Where(
			depositaddress.OwnerIdentityPubkey(idPubKey),
			depositaddress.IsStatic(true),
			depositaddress.IsDefault(true),
			depositaddress.NetworkEQ(schemaNetwork),
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

		keysharePubKey, err := keys.ParsePublicKey(keyshare.PublicKey)
		if err != nil {
			return nil, fmt.Errorf("failed to parse keyshare public key: %w", err)
		}
		verifyingKey := keysharePubKey.Add(depositAddress.OwnerSigningPubkey)

		// Return the whole deposit address data.
		logger.Info("Static deposit address already exists", "id", depositAddress.ID, "address", depositAddress.Address)
		return &pb.GenerateStaticDepositAddressResponse{
			DepositAddress: &pb.Address{
				Address:      depositAddress.Address,
				VerifyingKey: verifyingKey.Serialize(),
				DepositAddressProof: &pb.DepositAddressProof{
					AddressSignatures:          addressSignatures,
					ProofOfPossessionSignature: proofOfPossessionSignature,
				},
			},
		}, nil
	}

	reqSigningPubKey, err := keys.ParsePublicKey(req.GetSigningPublicKey())
	if err != nil {
		return nil, fmt.Errorf("failed to parse signing public key: %w", err)
	}
	// If no default static deposit address exists, generate a new one.
	logger.Info("Generating static deposit address for public key", "public_key", hex.EncodeToString(req.SigningPublicKey), "identity_public_key", idPubKey)

	// Note that this method will COMMIT or ROLLBACK the DB transaction.
	keyshares, err := ent.GetUnusedSigningKeyshares(ctx, config, 1)
	if err != nil {
		return nil, fmt.Errorf("failed to get unused keyshares: %w", err)
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

	keysharePubKey, err := keys.ParsePublicKey(keyshare.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("failed to parse keyshare public key: %w", err)
	}
	combinedPublicKey := keysharePubKey.Add(reqSigningPubKey)
	depositAddressString, err := common.P2TRAddressFromPublicKey(combinedPublicKey, network)
	if err != nil {
		return nil, err
	}

	db, err = ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx: %w", err)
	}

	depositAddressMutator := db.DepositAddress.Create().
		SetSigningKeyshareID(keyshare.ID).
		SetOwnerIdentityPubkey(idPubKey).
		SetOwnerSigningPubkey(reqSigningPubKey).
		SetNetwork(schemaNetwork).
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
			OwnerIdentityPublicKey: idPubKey.Serialize(),
			OwnerSigningPublicKey:  reqSigningPubKey.Serialize(),
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

	verifyingKey := keysharePubKey.Add(reqSigningPubKey)

	msg := common.ProofOfPossessionMessageHashForDepositAddress(idPubKey.Serialize(), keyshare.PublicKey, []byte(depositAddressString))
	proofOfPossessionSignatures, err := helper.GenerateProofOfPossessionSignatures(ctx, config, [][]byte{msg}, []*ent.SigningKeyshare{keyshare})
	if err != nil {
		return nil, err
	}

	internalHandler := NewInternalDepositHandler(config)
	selfProofs, err := internalHandler.GenerateStaticDepositAddressProofs(ctx, &pbinternal.GenerateStaticDepositAddressProofsRequest{
		KeyshareId:             keyshare.ID.String(),
		Address:                depositAddressString,
		OwnerIdentityPublicKey: req.IdentityPublicKey,
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
		logger.Error("failed to cache proofs for static deposit address", "error", err, "id", depositAddressRecord.ID, "address", depositAddress)
	}

	return &pb.GenerateStaticDepositAddressResponse{
		DepositAddress: &pb.Address{
			Address:      depositAddressString,
			VerifyingKey: verifyingKey.Serialize(),
			DepositAddressProof: &pb.DepositAddressProof{
				AddressSignatures:          addressSignatures,
				ProofOfPossessionSignature: proofOfPossessionSignatures[0],
			},
		},
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
		logger.Error("static deposit address does not have proofs on some or all operators", "id", depositAddress.ID, "address", depositAddress.Address, "error", err)
		return nil, nil, nil
	}

	addressSignatures := make(map[string][]byte)
	for id, response := range responses {
		addressSignatures[id] = response.AddressSignature
	}
	addressSignatures[config.Identifier] = selfProofs.AddressSignature

	msg := common.ProofOfPossessionMessageHashForDepositAddress(depositAddress.OwnerIdentityPubkey.Serialize(), keyshare.PublicKey, []byte(depositAddress.Address))
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
		logger.Error("failed to cache proofs for static deposit address", "error", err, "id", depositAddress.ID, "address", depositAddress.Address)
	}
	return addressSignatures, proofOfPossessionSignatures[0], nil
}

func (o *DepositHandler) StartTreeCreation(ctx context.Context, config *so.Config, req *pb.StartTreeCreationRequest) (*pb.StartTreeCreationResponse, error) {
	ctx, span := tracer.Start(ctx, "DepositHandler.StartTreeCreation")
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
		return nil, fmt.Errorf("failed to get on-chain tx for request %s: %w", logging.FormatProto("start_tree_creation_request", req), err)
	}
	if len(onChainTx.TxOut) <= int(req.OnChainUtxo.Vout) {
		return nil, fmt.Errorf("utxo index out of bounds for request %s", logging.FormatProto("start_tree_creation_request", req))
	}

	// Verify that the on chain utxo is paid to the registered deposit address
	if len(onChainTx.TxOut) <= int(req.OnChainUtxo.Vout) {
		return nil, fmt.Errorf("utxo index out of bounds for request %s", logging.FormatProto("start_tree_creation_request", req))
	}
	onChainOutput := onChainTx.TxOut[req.OnChainUtxo.Vout]
	network, err := common.NetworkFromProtoNetwork(req.OnChainUtxo.Network)
	if err != nil {
		return nil, fmt.Errorf("failed to get network for request %s: %w", logging.FormatProto("start_tree_creation_request", req), err)
	}
	if !config.IsNetworkSupported(network) {
		return nil, fmt.Errorf("network not supported for request %s", logging.FormatProto("start_tree_creation_request", req))
	}
	utxoAddress, err := common.P2TRAddressFromPkScript(onChainOutput.PkScript, network)
	if err != nil {
		return nil, fmt.Errorf("failed to get P2TR address from pk script for request %s: %w", logging.FormatProto("start_tree_creation_request", req), err)
	}
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	depositAddress, err := db.DepositAddress.Query().Where(depositaddress.Address(*utxoAddress)).First(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to query deposit address for request %s: %w", logging.FormatProto("start_tree_creation_request", req), err)
	}
	if !depositAddress.OwnerIdentityPubkey.Equals(reqIDPubKey) {
		return nil, fmt.Errorf("deposit address not found for address: %s", *utxoAddress)
	}
	rootSigningPubKey, err := keys.ParsePublicKey(req.GetRootTxSigningJob().GetSigningPublicKey())
	if err != nil {
		return nil, fmt.Errorf("invalid root signing public key: %w", err)
	}
	refundSigningPubKey, err := keys.ParsePublicKey(req.GetRefundTxSigningJob().GetSigningPublicKey())
	if err != nil {
		return nil, fmt.Errorf("invalid root signing public key: %w", err)
	}

	if !depositAddress.OwnerSigningPubkey.Equals(rootSigningPubKey) || !depositAddress.OwnerSigningPubkey.Equals(refundSigningPubKey) {
		return nil, fmt.Errorf("unexpected signing public key")
	}

	txConfirmed := depositAddress.ConfirmationHeight != 0

	if txConfirmed && depositAddress.ConfirmationTxid != "" {
		onChainTxid := onChainTx.TxHash().String()
		if onChainTxid != depositAddress.ConfirmationTxid {
			return nil, fmt.Errorf("transaction ID does not match confirmed transaction ID")
		}
	}

	// Verify the root transactions
	cpfpRootTx, err := common.TxFromRawTxBytes(req.RootTxSigningJob.RawTx)
	if err != nil {
		return nil, err
	}
	if len(cpfpRootTx.TxOut) <= 0 {
		return nil, fmt.Errorf("vout out of bounds, root tx has no outputs")
	}
	err = o.verifyRootTransaction(cpfpRootTx, onChainTx, req.OnChainUtxo.Vout)
	if err != nil {
		return nil, err
	}
	cpfpRootTxSigHash, err := common.SigHashFromTx(cpfpRootTx, 0, onChainOutput)
	if err != nil {
		return nil, err
	}

	// Verify the refund transactions
	cpfpRefundTx, err := common.TxFromRawTxBytes(req.RefundTxSigningJob.RawTx)
	if err != nil {
		return nil, err
	}
	err = o.verifyRefundTransaction(cpfpRootTx, cpfpRefundTx)
	if err != nil {
		return nil, err
	}

	// Sign the root and refund transactions
	signingKeyShare, err := depositAddress.QuerySigningKeyshare().Only(ctx)
	if err != nil {
		return nil, err
	}
	signingKeySharePublicKey, err := keys.ParsePublicKey(signingKeyShare.PublicKey)
	if err != nil {
		return nil, err
	}
	verifyingKey := signingKeySharePublicKey.Add(depositAddress.OwnerSigningPubkey)

	userCpfpRootTxNonceCommitment, err := objects.NewSigningCommitment(req.RootTxSigningJob.SigningNonceCommitment.Binding, req.RootTxSigningJob.SigningNonceCommitment.Hiding)
	if err != nil {
		return nil, err
	}

	cpfpRefundTxSigHash, err := common.SigHashFromTx(cpfpRefundTx, 0, cpfpRootTx.TxOut[0])
	if err != nil {
		return nil, err
	}

	userCpfpRefundTxNonceCommitment, err := objects.NewSigningCommitment(req.RefundTxSigningJob.SigningNonceCommitment.Binding, req.RefundTxSigningJob.SigningNonceCommitment.Hiding)
	if err != nil {
		return nil, err
	}
	signingJobs := []*helper.SigningJob{
		{
			JobID:             uuid.New().String(),
			SigningKeyshareID: signingKeyShare.ID,
			Message:           cpfpRootTxSigHash,
			VerifyingKey:      &verifyingKey,
			UserCommitment:    userCpfpRootTxNonceCommitment,
		},
		{
			JobID:             uuid.New().String(),
			SigningKeyshareID: signingKeyShare.ID,
			Message:           cpfpRefundTxSigHash,
			VerifyingKey:      &verifyingKey,
			UserCommitment:    userCpfpRefundTxNonceCommitment,
		},
	}

	directRootTxSigningJob := req.GetDirectRootTxSigningJob()
	directRefundTxSigningJob := req.GetDirectRefundTxSigningJob()
	directFromCpfpRefundTxSigningJob := req.GetDirectFromCpfpRefundTxSigningJob()
	if directRootTxSigningJob != nil && directRefundTxSigningJob != nil && directFromCpfpRefundTxSigningJob != nil {
		directRootTx, err := common.TxFromRawTxBytes(directRootTxSigningJob.RawTx)
		if err != nil {
			return nil, err
		}
		err = o.verifyRootTransaction(directRootTx, onChainTx, req.OnChainUtxo.Vout)
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
		err = o.verifyRefundTransaction(directRootTx, directRefundTx)
		if err != nil {
			return nil, err
		}

		directFromCpfpRefundTx, err := common.TxFromRawTxBytes(req.DirectFromCpfpRefundTxSigningJob.RawTx)
		if err != nil {
			return nil, err
		}
		err = o.verifyRefundTransaction(cpfpRootTx, directFromCpfpRefundTx)
		if err != nil {
			return nil, err
		}
		directRefundTxSigHash, err := common.SigHashFromTx(directRefundTx, 0, directRootTx.TxOut[0])
		if err != nil {
			return nil, err
		}
		directFromCpfpRefundTxSigHash, err := common.SigHashFromTx(directFromCpfpRefundTx, 0, cpfpRootTx.TxOut[0])
		if err != nil {
			return nil, err
		}
		userDirectRootTxNonceCommitment, err := objects.NewSigningCommitment(req.DirectRootTxSigningJob.SigningNonceCommitment.Binding, req.DirectRootTxSigningJob.SigningNonceCommitment.Hiding)
		if err != nil {
			return nil, err
		}
		userDirectRefundTxNonceCommitment, err := objects.NewSigningCommitment(req.DirectRefundTxSigningJob.SigningNonceCommitment.Binding, req.DirectRefundTxSigningJob.SigningNonceCommitment.Hiding)
		if err != nil {
			return nil, err
		}
		userDirectFromCpfpRefundTxNonceCommitment, err := objects.NewSigningCommitment(req.DirectFromCpfpRefundTxSigningJob.SigningNonceCommitment.Binding, req.DirectFromCpfpRefundTxSigningJob.SigningNonceCommitment.Hiding)
		if err != nil {
			return nil, err
		}
		signingJobs = append(
			signingJobs,
			&helper.SigningJob{
				JobID:             uuid.New().String(),
				SigningKeyshareID: signingKeyShare.ID,
				Message:           directRootTxSigHash,
				VerifyingKey:      &verifyingKey,
				UserCommitment:    userDirectRootTxNonceCommitment,
			},
			&helper.SigningJob{
				JobID:             uuid.New().String(),
				SigningKeyshareID: signingKeyShare.ID,
				Message:           directRefundTxSigHash,
				VerifyingKey:      &verifyingKey,
				UserCommitment:    userDirectRefundTxNonceCommitment,
			},
			&helper.SigningJob{
				JobID:             uuid.New().String(),
				SigningKeyshareID: signingKeyShare.ID,
				Message:           directFromCpfpRefundTxSigHash,
				VerifyingKey:      &verifyingKey,
				UserCommitment:    userDirectFromCpfpRefundTxNonceCommitment,
			},
		)
	} else if directRootTxSigningJob != nil || directRefundTxSigningJob != nil || directFromCpfpRefundTxSigningJob != nil {
		return nil, fmt.Errorf("direct root tx signing job, direct refund tx signing job, and direct from cpfp refund tx signing job must all be provided or none of them")
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
	if req.GetDirectRootTxSigningJob() != nil && req.GetDirectRefundTxSigningJob() != nil && req.GetDirectFromCpfpRefundTxSigningJob() != nil {
		// First 2 signing results are always for cpfpNodeTx and cpfpRefundTx.
		// If all three direct jobs (root, refund, fromCpfpRefund) are present,
		// they produce 3 additional signing results (indexes 2, 3, 4), so the total must be at least 5.
		if len(signingResults) < 5 {
			return nil, fmt.Errorf("expected at least 5 signing results, got %d", len(signingResults))
		}
		directNodeTxSigningResult, err = signingResults[2].MarshalProto()
		if err != nil {
			return nil, err
		}
		directRefundTxSigningResult, err = signingResults[3].MarshalProto()
		if err != nil {
			return nil, err
		}
		directFromCpfpRefundTxSigningResult, err = signingResults[4].MarshalProto()
		if err != nil {
			return nil, err
		}
	}

	// Create the tree
	schemaNetwork, err := common.SchemaNetworkFromNetwork(network)
	if err != nil {
		return nil, err
	}
	txid := onChainTx.TxHash()
	treeMutator := db.Tree.
		Create().
		SetOwnerIdentityPubkey(depositAddress.OwnerIdentityPubkey.Serialize()).
		SetNetwork(schemaNetwork).
		SetBaseTxid(txid[:]).
		SetVout(int16(req.OnChainUtxo.Vout))
	if txConfirmed {
		treeMutator.SetStatus(st.TreeStatusAvailable)
	} else {
		treeMutator.SetStatus(st.TreeStatusPending)
	}
	entTree, err := treeMutator.Save(ctx)
	if err != nil {
		return nil, err
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
	root, err := db.TreeNode.
		Create().
		SetTree(entTree).
		SetStatus(st.TreeNodeStatusCreating).
		SetOwnerIdentityPubkey(depositAddress.OwnerIdentityPubkey.Serialize()).
		SetOwnerSigningPubkey(depositAddress.OwnerSigningPubkey.Serialize()).
		SetValue(uint64(onChainOutput.Value)).
		SetVerifyingPubkey(verifyingKey.Serialize()).
		SetSigningKeyshare(signingKeyShare).
		SetRawTx(req.RootTxSigningJob.RawTx).
		SetRawRefundTx(req.RefundTxSigningJob.RawTx).
		SetDirectTx(directTx).
		SetDirectRefundTx(directRefundTx).
		SetDirectFromCpfpRefundTx(directFromCpfpRefundTx).
		SetVout(int16(req.OnChainUtxo.Vout)).
		Save(ctx)
	if err != nil {
		return nil, err
	}
	entTree, err = entTree.Update().SetRoot(root).Save(ctx)
	if err != nil {
		return nil, err
	}

	return &pb.StartTreeCreationResponse{
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

	network, err := common.NetworkFromProtoNetwork(req.OnChainUtxo.Network)
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

	depositAddress, err := db.DepositAddress.Query().Where(depositaddress.Address(*utxoAddress)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			err = errors.NotFoundErrorf("The requested deposit address could not be found.")
		}
		if ent.IsNotSingular(err) {
			return nil, fmt.Errorf("multiple deposit addresses found for address: %s", *utxoAddress)
		}
		return nil, err
	}
	if !depositAddress.OwnerIdentityPubkey.Equals(reqIDPubKey) {
		return nil, fmt.Errorf("deposit address not found for address: %s", *utxoAddress)
	}
	rootSigningPubKey, err := keys.ParsePublicKey(req.GetRootTxSigningJob().GetSigningPublicKey())
	if err != nil {
		return nil, fmt.Errorf("invalid root signing public key: %w", err)
	}
	refundSigningPubKey, err := keys.ParsePublicKey(req.GetRefundTxSigningJob().GetSigningPublicKey())
	if err != nil {
		return nil, fmt.Errorf("invalid root signing public key: %w", err)
	}
	if !depositAddress.OwnerSigningPubkey.Equals(rootSigningPubKey) || !depositAddress.OwnerSigningPubkey.Equals(refundSigningPubKey) {
		return nil, fmt.Errorf("unexpected signing public key")
	}

	txConfirmed := depositAddress.ConfirmationHeight != 0

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
	err = o.verifyRootTransaction(cpfpRootTx, onChainTx, req.OnChainUtxo.Vout)
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
	signingKeySharePublicKey, err := keys.ParsePublicKey(signingKeyShare.PublicKey)
	if err != nil {
		return nil, err
	}
	verifyingKey := signingKeySharePublicKey.Add(depositAddress.OwnerSigningPubkey)

	userCpfpRootTxNonceCommitment, err := objects.NewSigningCommitment(req.RootTxSigningJob.SigningNonceCommitment.Binding, req.RootTxSigningJob.SigningNonceCommitment.Hiding)
	if err != nil {
		return nil, err
	}
	userCpfpRefundTxNonceCommitment, err := objects.NewSigningCommitment(req.RefundTxSigningJob.SigningNonceCommitment.Binding, req.RefundTxSigningJob.SigningNonceCommitment.Hiding)
	if err != nil {
		return nil, err
	}

	signingJobs := []*helper.SigningJob{
		{
			JobID:             uuid.New().String(),
			SigningKeyshareID: signingKeyShare.ID,
			Message:           cpfpRootTxSigHash,
			VerifyingKey:      &verifyingKey,
			UserCommitment:    userCpfpRootTxNonceCommitment,
		},
		{
			JobID:             uuid.New().String(),
			SigningKeyshareID: signingKeyShare.ID,
			Message:           cpfpRefundTxSigHash,
			VerifyingKey:      &verifyingKey,
			UserCommitment:    userCpfpRefundTxNonceCommitment,
		},
	}

	// New flow
	directRootTxSigningJob := req.GetDirectRootTxSigningJob()
	directRefundTxSigningJob := req.GetDirectRefundTxSigningJob()
	directFromCpfpRefundTxSigningJob := req.GetDirectFromCpfpRefundTxSigningJob()

	if directRootTxSigningJob != nil && directRefundTxSigningJob != nil && directFromCpfpRefundTxSigningJob != nil {

		directRootTx, err := common.TxFromRawTxBytes(req.DirectRootTxSigningJob.RawTx)
		if err != nil {
			return nil, err
		}
		err = o.verifyRootTransaction(directRootTx, onChainTx, req.OnChainUtxo.Vout)
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
		directFromCpfpRefundTx, err := common.TxFromRawTxBytes(req.DirectFromCpfpRefundTxSigningJob.RawTx)
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
		err = o.verifyRefundTransaction(cpfpRootTx, directFromCpfpRefundTx)
		if err != nil {
			return nil, err
		}
		if len(cpfpRootTx.TxOut) <= 0 {
			return nil, fmt.Errorf("vout out of bounds, root tx has no outputs")
		}
		directRefundTxSigHash, err := common.SigHashFromTx(directRefundTx, 0, directRootTx.TxOut[0])
		if err != nil {
			return nil, err
		}
		directFromCpfpRefundTxSigHash, err := common.SigHashFromTx(directFromCpfpRefundTx, 0, cpfpRootTx.TxOut[0])
		if err != nil {
			return nil, err
		}

		userDirectRootTxNonceCommitment, err := objects.NewSigningCommitment(req.DirectRootTxSigningJob.SigningNonceCommitment.Binding, req.DirectRootTxSigningJob.SigningNonceCommitment.Hiding)
		if err != nil {
			return nil, err
		}
		userDirectRefundTxNonceCommitment, err := objects.NewSigningCommitment(req.DirectRefundTxSigningJob.SigningNonceCommitment.Binding, req.DirectRefundTxSigningJob.SigningNonceCommitment.Hiding)
		if err != nil {
			return nil, err
		}
		userDirectFromCpfpRefundTxNonceCommitment, err := objects.NewSigningCommitment(req.DirectFromCpfpRefundTxSigningJob.SigningNonceCommitment.Binding, req.DirectFromCpfpRefundTxSigningJob.SigningNonceCommitment.Hiding)
		if err != nil {
			return nil, err
		}
		signingJobs = append(
			signingJobs,
			&helper.SigningJob{
				JobID:             uuid.New().String(),
				SigningKeyshareID: signingKeyShare.ID,
				Message:           directRootTxSigHash,
				VerifyingKey:      &verifyingKey,
				UserCommitment:    userDirectRootTxNonceCommitment,
			},
			&helper.SigningJob{
				JobID:             uuid.New().String(),
				SigningKeyshareID: signingKeyShare.ID,
				Message:           directRefundTxSigHash,
				VerifyingKey:      &verifyingKey,
				UserCommitment:    userDirectRefundTxNonceCommitment,
			},
			&helper.SigningJob{
				JobID:             uuid.New().String(),
				SigningKeyshareID: signingKeyShare.ID,
				Message:           directFromCpfpRefundTxSigHash,
				VerifyingKey:      &verifyingKey,
				UserCommitment:    userDirectFromCpfpRefundTxNonceCommitment,
			},
		)
	} else if directRootTxSigningJob != nil || directRefundTxSigningJob != nil || directFromCpfpRefundTxSigningJob != nil {
		return nil, fmt.Errorf("direct root tx signing job, direct refund tx signing job, and direct from cpfp refund tx signing job must all be provided or none of them")
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
	if len(signingResults) > 4 {
		directNodeTxSigningResult, err = signingResults[2].MarshalProto()
		if err != nil {
			return nil, err
		}
		directRefundTxSigningResult, err = signingResults[3].MarshalProto()
		if err != nil {
			return nil, err
		}
		directFromCpfpRefundTxSigningResult, err = signingResults[4].MarshalProto()
		if err != nil {
			return nil, err
		}
	}
	// Create the tree
	schemaNetwork, err := common.SchemaNetworkFromNetwork(network)
	if err != nil {
		return nil, err
	}
	txid := onChainTx.TxHash()

	// Check if a tree already exists for this deposit
	existingTree, err := db.Tree.Query().
		Where(tree.BaseTxid(txid[:])).
		Where(tree.Vout(int16(req.OnChainUtxo.Vout))).
		First(ctx)

	if err != nil && !ent.IsNotFound(err) {
		return nil, fmt.Errorf("failed to query for existing tree: %w", err)
	}

	logger := logging.GetLoggerFromContext(ctx)
	logger.Info("existingTree", "existingTree", existingTree)
	var entTree *ent.Tree
	if existingTree != nil {
		// Tree already exists, use the existing one
		entTree = existingTree
		logger.Info("Tree already exists", "treeId", existingTree.ID, "depositAddress", depositAddress.ID, "txid", txid)
	} else {
		// Create new tree
		treeMutator := db.Tree.
			Create().
			SetOwnerIdentityPubkey(depositAddress.OwnerIdentityPubkey.Serialize()).
			SetNetwork(schemaNetwork).
			SetBaseTxid(txid[:]).
			SetVout(int16(req.OnChainUtxo.Vout))

		if txConfirmed {
			treeMutator.SetStatus(st.TreeStatusAvailable)
		} else {
			treeMutator.SetStatus(st.TreeStatusPending)
		}
		entTree, err = treeMutator.Save(ctx)
		if err != nil {
			if ent.IsConstraintError(err) {
				return nil, errors.AlreadyExistsErrorf("tree already exists: %v", err)
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
		Where(treenode.OwnerIdentityPubkey(depositAddress.OwnerIdentityPubkey.Serialize())).
		Where(treenode.OwnerSigningPubkey(depositAddress.OwnerSigningPubkey.Serialize())).
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
			return nil, errors.FailedPreconditionErrorf("Expected tree node %s to be in creating status; got %s.", existingRoot.ID, existingRoot.Status)
		}
		logger.Info("Tree node already exists, updating with new transaction data", "treeNodeId", existingRoot.ID, "depositAddress", depositAddress.ID, "txid", txid)
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
			SetStatus(st.TreeNodeStatusCreating).
			SetOwnerIdentityPubkey(depositAddress.OwnerIdentityPubkey.Serialize()).
			SetOwnerSigningPubkey(depositAddress.OwnerSigningPubkey.Serialize()).
			SetValue(uint64(onChainOutput.Value)).
			SetVerifyingPubkey(verifyingKey.Serialize()).
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

func (o *DepositHandler) verifyRootTransaction(rootTx *wire.MsgTx, onChainTx *wire.MsgTx, onChainVout uint32) error {
	if len(rootTx.TxIn) <= 0 || len(rootTx.TxOut) <= 0 {
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
	if rootTx.TxOut[0].Value > onChainTx.TxOut[onChainVout].Value {
		return fmt.Errorf("root transaction has wrong value: root tx value %d > on-chain tx value %d", rootTx.TxOut[0].Value, onChainTx.TxOut[onChainVout].Value)
	}

	return nil
}

func (o *DepositHandler) verifyRefundTransaction(tx *wire.MsgTx, refundTx *wire.MsgTx) error {
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

// InitiateUtxoSwap initiates a UTXO swap operation, allowing a User to swap their on-chain UTXOs for Spark funds.
// Deprecated: Use InitiateStaticDepositUtxoSwap instead.
func (o *DepositHandler) InitiateUtxoSwap(ctx context.Context, config *so.Config, req *pb.InitiateUtxoSwapRequest) (*pb.InitiateUtxoSwapResponse, error) {
	reqTransferOwnerIDPubKey, err := keys.ParsePublicKey(req.Transfer.OwnerIdentityPublicKey)
	if err != nil {
		return nil, fmt.Errorf("invalid identity public key: %w", err)
	}
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, config, reqTransferOwnerIDPubKey); err != nil {
		return nil, err
	}
	ctx, span := tracer.Start(ctx, "DepositHandler.InitiateUtxoSwap", trace.WithAttributes(
		transferTypeKey.String(string(req.RequestType)),
	))
	defer span.End()

	logger := logging.GetLoggerFromContext(ctx)
	logger.Info("Start InitiateUtxoSwap request for on-chain utxo", "txid", hex.EncodeToString(req.OnChainUtxo.Txid), "vout", req.OnChainUtxo.Vout, "coordinator", config.Identifier)

	// Check if the swap is already completed for the caller
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	schemaNetwork, err := common.SchemaNetworkFromProtoNetwork(req.OnChainUtxo.Network)
	if err != nil {
		return nil, err
	}

	onChainUtxoTxId, err := NewValidatedTxID(req.OnChainUtxo.Txid)
	if err != nil {
		return nil, fmt.Errorf("failed to validate on-chain UTXO txid: %w", err)
	}
	targetUtxo, err := VerifiedTargetUtxo(ctx, config, db, schemaNetwork, onChainUtxoTxId, req.OnChainUtxo.Vout)
	if err != nil {
		return nil, err
	}

	utxoSwap, err := staticdeposit.GetRegisteredUtxoSwapForUtxo(ctx, db, targetUtxo)
	if err != nil {
		return nil, fmt.Errorf("unable to check if utxo swap is already completed: %w", err)
	}
	if utxoSwap != nil {
		// If the swap is completed and owned by the caller,
		// idempotently return the result.
		if utxoSwap.Status == st.UtxoSwapStatusCompleted {
			spendTxSigningResult := &pb.SigningResult{}
			err := proto.Unmarshal(utxoSwap.SpendTxSigningResult, spendTxSigningResult)
			if err != nil {
				return nil, fmt.Errorf("failed to unmarshal spend tx signing result: %w", err)
			}
			depositAddress, err := targetUtxo.QueryDepositAddress().Only(ctx)
			if err != nil {
				return nil, fmt.Errorf("failed to get deposit address: %w", err)
			}
			signingKeyShare, err := depositAddress.QuerySigningKeyshare().Only(ctx)
			if err != nil {
				return nil, fmt.Errorf("failed to get signing keyshare: %w", err)
			}
			signingKeySharePubKey, err := keys.ParsePublicKey(signingKeyShare.PublicKey)
			if err != nil {
				return nil, fmt.Errorf("failed to parse signing key share public key: %w", err)
			}
			verifyingKey := signingKeySharePubKey.Add(depositAddress.OwnerSigningPubkey)
			transferProto := &pb.Transfer{}
			if utxoSwap.RequestType != st.UtxoSwapRequestTypeRefund {
				transfer, err := utxoSwap.QueryTransfer().Only(ctx)
				if err != nil {
					return nil, fmt.Errorf("failed to get transfer: %w", err)
				}
				transferProto, err = transfer.MarshalProto(ctx)
				if err != nil {
					return nil, fmt.Errorf("failed to marshal transfer: %w", err)
				}
			}
			nodeIDStr := depositAddress.NodeID.String()
			return &pb.InitiateUtxoSwapResponse{
				SpendTxSigningResult: spendTxSigningResult,
				Transfer:             transferProto,
				DepositAddress: &pb.DepositAddressQueryResult{
					DepositAddress:       depositAddress.Address,
					UserSigningPublicKey: depositAddress.OwnerSigningPubkey.Serialize(),
					VerifyingPublicKey:   verifyingKey.Serialize(),
					LeafId:               &nodeIDStr,
				},
			}, nil
		}
		return nil, fmt.Errorf("utxo swap is already registered")
	}

	// **********************************************************************************************
	// Create a swap record in all SEs so they can not be called concurrently to spend the same utxo.
	// This will validate the swap request and store it in the database with status CREATED,
	// blocking any other swap requests. If this step fails, the caller will receive an error and
	// the swap will be cancelled.
	// **********************************************************************************************
	internalDepositHandler := NewInternalDepositHandler(config)

	// Sign a statement that this utxo swap is created by this coordinator.
	// SOs will use it to mark the utxo swap as owned by this coordinator.
	// This will allow the coordinator to cancel the swap if needed.
	createdUtxoSwapRequest, err := CreateCreateSwapForUtxoRequest(config, req)
	if err != nil {
		logger.Warn("Failed to get create utxo swap request, cron task to retry", "error", err)
	} else {
		if err := internalDepositHandler.CreateSwapForAllOperators(ctx, config, createdUtxoSwapRequest); err != nil {
			originalErr := err
			logger.Info("Failed to successfully execute create utxo swap task with all operators, rolling back", "error", originalErr, "txid", hex.EncodeToString(req.OnChainUtxo.Txid), "vout", req.OnChainUtxo.Vout)

			if err := internalDepositHandler.RollbackSwapForAllOperators(ctx, config, createdUtxoSwapRequest); err != nil {
				logger.Error("Failed to rollback utxo swap", "error", err, "txid", hex.EncodeToString(req.OnChainUtxo.Txid), "vout", req.OnChainUtxo.Vout)
			}

			logger.Error("UTXO swap rollback completed", "txid", hex.EncodeToString(req.OnChainUtxo.Txid), "vout", req.OnChainUtxo.Vout)
			return nil, errors.WrapErrorWithMessage(originalErr, "failed to successfully execute create utxo swap task with all operators")
		}
	}
	logger.Info("Created utxo swap", "txid", hex.EncodeToString(req.OnChainUtxo.Txid), "vout", req.OnChainUtxo.Vout)

	utxoSwap, err = staticdeposit.GetRegisteredUtxoSwapForUtxo(ctx, db, targetUtxo)
	if err != nil || utxoSwap == nil {
		return nil, fmt.Errorf("unable to get utxo swap: %w", err)
	}

	// **********************************************************************************************
	// Initiate a transfer to the user. This step is 2-phase and will be rolled
	// back if the first phase fails or retried otherwise.
	// **********************************************************************************************
	var transfer *pb.Transfer
	if req.RequestType != pb.UtxoSwapRequestType_Refund {
		transferHandler := NewTransferHandler(config)
		transferResponse, err := transferHandler.startTransferInternal(
			ctx,
			req.Transfer,
			st.TransferTypeUtxoSwap,
			keys.Public{},
			keys.Public{},
			keys.Public{},
			false,
		)
		if err != nil {
			if err := internalDepositHandler.RollbackSwapForAllOperators(ctx, config, createdUtxoSwapRequest); err != nil {
				logger.Error("Failed to rollback utxo swap", "error", err)
			}
			return nil, fmt.Errorf("failed to create transfer: %w", err)
		}
		transfer = transferResponse.Transfer
		if transfer == nil {
			return nil, fmt.Errorf("create utxo swap task with operator %s returned nil transfer", config.Identifier)
		}

		db, err = ent.GetDbFromContext(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to get db: %w", err)
		}

		utxoSwap, err = db.UtxoSwap.Get(ctx, utxoSwap.ID)
		if err != nil {
			return nil, fmt.Errorf("unable to get utxo swap: %w", err)
		}

		// The transfer is created, update the utxo swap with the transfer.
		entTransfer, err := db.Transfer.Get(ctx, utxoSwap.RequestedTransferID)
		if err != nil {
			return nil, fmt.Errorf("unable to get transfer from utxo swap: %w", err)
		}
		if entTransfer != nil {
			_, err := utxoSwap.Update().SetTransfer(entTransfer).Save(ctx)
			if err != nil {
				return nil, fmt.Errorf("unable to set transfer for utxo swap: %w", err)
			}
		}

		logger.Info("UTXO swap transfer created", "transfer", transfer, "txid", hex.EncodeToString(req.OnChainUtxo.Txid), "vout", req.OnChainUtxo.Vout)
	}

	// **********************************************************************************************
	// Mark the utxo swap as completed.
	// At this point the swap is considered successful. We will not return an error if this step fails.
	// The user can retry calling this API to get the signed spend transaction.
	// **********************************************************************************************
	completedUtxoSwapRequest, err := CreateCompleteSwapForUtxoRequest(config, req.OnChainUtxo)
	if err != nil {
		logger.Warn("Failed to get complete swap for utxo request, cron task to retry", "error", err)
	} else {
		if err := internalDepositHandler.CompleteSwapForAllOperators(ctx, config, completedUtxoSwapRequest); err != nil {
			logger.Warn("Failed to mark a utxo swap as completed in all operators, cron task to retry", "error", err)
		}
	}

	// **********************************************************************************************
	// Signing the spend transaction.
	// **********************************************************************************************
	spendTxSigningResult, depositAddressQueryResult, err := GetSpendTxSigningResult(ctx, config, req.OnChainUtxo, req.SpendTxSigningJob)
	if err != nil {
		logger.Warn("failed to get spend tx signing result", "error", err)
	}
	spendTxSigningResultBytes, err := proto.Marshal(spendTxSigningResult)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal spend tx signing result: %w", err)
	}

	_, err = db.UtxoSwap.UpdateOne(utxoSwap).SetSpendTxSigningResult(spendTxSigningResultBytes).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to update utxo swap: %w", err)
	}

	return &pb.InitiateUtxoSwapResponse{
		SpendTxSigningResult: spendTxSigningResult,
		Transfer:             transfer,
		DepositAddress:       depositAddressQueryResult,
	}, nil
}

// validatedTxID is a 32-byte Bitcoin transaction ID (txid) that has passed basic format checks.
// "Validated" means only the length is verified; no cryptographic or blockchain existence checks are performed.
type validatedTxID [32]byte

// NewValidatedTxID returns a validatedTxID if b is exactly 32 bytes long.
func NewValidatedTxID(b []byte) (validatedTxID, error) {
	if len(b) != 32 {
		return validatedTxID{}, fmt.Errorf("invalid txid length: got %d, want 32", len(b))
	}
	return validatedTxID(b), nil
}

// Verifies that an UTXO is confirmed on the blockchain and has sufficient confirmations.
func VerifiedTargetUtxo(ctx context.Context, config *so.Config, db *ent.Tx, schemaNetwork st.Network, txid validatedTxID, vout uint32) (*ent.Utxo, error) {
	blockHeight, err := db.BlockHeight.Query().Where(
		blockheight.NetworkEQ(schemaNetwork),
	).Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to find block height: %w", err)
	}
	targetUtxo, err := db.Utxo.Query().
		Where(utxo.NetworkEQ(schemaNetwork)).
		Where(utxo.Txid(txid[:])).
		Where(utxo.Vout(vout)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, errors.FailedPreconditionErrorf("utxo not found: txid: %s vout: %d", hex.EncodeToString(txid[:]), vout)
		}
		return nil, fmt.Errorf("failed to get target utxo: %w", err)
	}

	threshold := DefaultDepositConfirmationThreshold
	if bitcoinConfig, ok := config.BitcoindConfigs[strings.ToLower(string(schemaNetwork))]; ok {
		threshold = bitcoinConfig.DepositConfirmationThreshold
	}
	if blockHeight.Height-targetUtxo.BlockHeight+1 < int64(threshold) {
		return nil, errors.FailedPreconditionErrorf("deposit tx doesn't have enough confirmations: confirmation height: %d current block height: %d", targetUtxo.BlockHeight, blockHeight.Height)
	}
	return targetUtxo, nil
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
func getSpendTxSigningResult(ctx context.Context, config *so.Config, depositAddress *ent.DepositAddress, targetUtxo *ent.Utxo, spendTxRaw []byte, userSpendTxNonceCommitment *objects.SigningCommitment) (keys.Public, *pb.SigningResult, error) {
	signingKeyShare, err := depositAddress.QuerySigningKeyshare().Only(ctx)
	if err != nil {
		return keys.Public{}, nil, fmt.Errorf("failed to get signing keyshare: %w", err)
	}
	signingKeySharePublicKey, err := keys.ParsePublicKey(signingKeyShare.PublicKey)
	if err != nil {
		return keys.Public{}, nil, err
	}
	verifyingKey := signingKeySharePublicKey.Add(depositAddress.OwnerSigningPubkey)
	spendTxSigHash, _, err := GetTxSigningInfo(ctx, targetUtxo, spendTxRaw)
	if err != nil {
		return keys.Public{}, nil, fmt.Errorf("failed to get spend tx sig hash: %w", err)
	}

	signingJobs := []*helper.SigningJob{{
		JobID:             uuid.New().String(),
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
	logger.Debug("spendTxSigHash", "spendTxSigHash", hex.EncodeToString(spendTxSigHash))
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
	schemaNetwork, err := common.SchemaNetworkFromProtoNetwork(utxo.Network)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get schema network: %w", err)
	}

	targetUtxoTxId, err := NewValidatedTxID(utxo.Txid)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to validate UTXO txid: %w", err)
	}
	targetUtxo, err := VerifiedTargetUtxo(ctx, config, db, schemaNetwork, targetUtxoTxId, utxo.Vout)
	if err != nil {
		return nil, nil, err
	}
	depositAddress, err := targetUtxo.QueryDepositAddress().Only(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get deposit address: %w", err)
	}

	// Recover the signature for the utxo spend
	// Execute signing jobs with all operators and create a refund transaction
	userRootTxNonceCommitment, err := objects.NewSigningCommitment(spendTxSigningJob.SigningNonceCommitment.Binding, spendTxSigningJob.SigningNonceCommitment.Hiding)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create signing commitment: %w", err)
	}
	verifyingKey, spendTxSigningResult, err := getSpendTxSigningResult(ctx, config, depositAddress, targetUtxo, spendTxSigningJob.RawTx, userRootTxNonceCommitment)
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

	network, err := common.DetermineNetwork(req.Network)
	if err != nil {
		return nil, fmt.Errorf("failed to get schema network: %w", err)
	}

	schemaNetwork, err := common.SchemaNetworkFromProtoNetwork(req.Network)
	if err != nil {
		return nil, fmt.Errorf("failed to get schema network: %w", err)
	}

	if !utils.IsBitcoinAddressForNetwork(req.Address, *network) {
		return nil, fmt.Errorf("deposit address is not aligned with the requested network")
	}

	currentBlockHeight, err := db.BlockHeight.Query().Where(blockheight.NetworkEQ(schemaNetwork)).Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get current block height: %w", err)
	}

	threshold := DefaultDepositConfirmationThreshold
	if bitcoinConfig, ok := o.config.BitcoindConfigs[strings.ToLower(string(schemaNetwork))]; ok {
		threshold = bitcoinConfig.DepositConfirmationThreshold
	}

	var utxosResult []*pb.UTXO
	if depositAddress.IsStatic {
		if req.Limit > 100 || req.Limit <= 0 {
			req.Limit = 100
		}
		query := depositAddress.QueryUtxo().
			Where(utxo.BlockHeightLTE(currentBlockHeight.Height - int64(threshold))).
			Offset(int(req.Offset)).
			Limit(int(req.Limit)).
			Order(utxo.ByBlockHeight(sql.OrderDesc()))
		if req.ExcludeClaimed {
			query = query.Where(func(s *sql.Selector) {
				// Exclude UTXOs that have non-cancelled UTXO swaps
				subquery := sql.Select(utxoswap.UtxoColumn).
					From(sql.Table(utxoswap.Table)).
					Where(sql.NEQ(utxoswap.FieldStatus, string(st.UtxoSwapStatusCancelled)))
				s.Where(sql.NotIn(s.C(utxo.FieldID), subquery))
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
