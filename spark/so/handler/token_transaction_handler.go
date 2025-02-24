package handler

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go/common"
	pblrc20 "github.com/lightsparkdev/spark-go/proto/lrc20"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	pbinternal "github.com/lightsparkdev/spark-go/proto/spark_internal"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/authz"
	"github.com/lightsparkdev/spark-go/so/ent"
	"github.com/lightsparkdev/spark-go/so/ent/schema"
	"github.com/lightsparkdev/spark-go/so/helper"
	"github.com/lightsparkdev/spark-go/so/utils"
	"google.golang.org/protobuf/types/known/emptypb"
)

// The TokenTransactionHandler is responsible for handling token transaction requests to spend and create leaves.
type TokenTransactionHandler struct {
	config authz.Config
	db     *ent.Client
}

// NewTokenTransactionHandler creates a new TokenTransactionHandler.
func NewTokenTransactionHandler(config authz.Config, db *ent.Client) *TokenTransactionHandler {
	return &TokenTransactionHandler{
		config: config,
		db:     db,
	}
}

// StartTokenTransaction verifies the token leaves, generates the keyshares for the token transaction, and returns metadata about the operators that possess the keyshares.
func (o TokenTransactionHandler) StartTokenTransaction(ctx context.Context, config *so.Config, req *pb.StartTokenTransactionRequest) (*pb.StartTokenTransactionResponse, error) {
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, o.config, req.IdentityPublicKey); err != nil {
		return nil, err
	}

	if err := utils.ValidatePartialTokenTransaction(req.PartialTokenTransaction, req.TokenTransactionSignatures, config.GetSigningOperatorList()); err != nil {
		return nil, err
	}

	// TODO: Add a call to the LRC20 node to verify the validity of the transaction payload.

	// Each created leaf requires a keyshare for revocation key generation.
	numRevocationKeysharesNeeded := len(req.PartialTokenTransaction.OutputLeaves)
	keyshares, err := ent.GetUnusedSigningKeyshares(ctx, o.db, config, numRevocationKeysharesNeeded)
	if err != nil {
		return nil, err
	}

	keyshareIDs := make([]uuid.UUID, len(keyshares))
	keyshareIDStrings := make([]string, len(keyshares))
	for i, keyshare := range keyshares {
		keyshareIDs[i] = keyshare.ID
		keyshareIDStrings[i] = keyshare.ID.String()
	}

	// Fill revocation public keys and withdrawal bond/locktime for each leaf.
	finalTokenTransaction := req.PartialTokenTransaction
	for i, leaf := range finalTokenTransaction.OutputLeaves {
		id, err := uuid.NewV7()
		if err != nil {
			return nil, err
		}
		idStr := id.String()
		leaf.Id = &idStr
		leaf.RevocationPublicKey = keyshares[i].PublicKey
	}

	// Save the token transaction object to lock in the revocation public keys for each created leaf within this transaction.
	// Note that atomicity here is very important to ensure that the unused keyshares queried above are not used by another operation.
	// This property should be help because the coordinator blocks on the other SO responses.
	allSelection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionAll}
	_, err = helper.ExecuteTaskWithAllOperators(ctx, config, &allSelection, func(ctx context.Context, operator *so.SigningOperator) (interface{}, error) {
		conn, err := common.NewGRPCConnectionWithCert(operator.Address, operator.CertPath)
		if err != nil {
			log.Printf("Failed to connect to operator for marking token transaction keyshare: %v", err)
			return nil, err
		}
		defer conn.Close()

		// Fill revocation public keys and withdrawal bond/locktime for each leaf.
		finalTokenTransaction := req.PartialTokenTransaction
		for i, leaf := range finalTokenTransaction.OutputLeaves {
			leafID := uuid.New().String()
			leaf.Id = &leafID
			leaf.RevocationPublicKey = keyshares[i].PublicKey
			// TODO: Support non-regtest configs by providing network as a field in the transaction.
			withdrawalBondSats := config.Lrc20Configs[common.Regtest.String()].WithdrawBondSats
			leaf.WithdrawBondSats = &withdrawalBondSats
			withdrawRelativeBlockLocktime := config.Lrc20Configs[common.Regtest.String()].WithdrawRelativeBlockLocktime
			leaf.WithdrawRelativeBlockLocktime = &withdrawRelativeBlockLocktime
		}

		client := pbinternal.NewSparkInternalServiceClient(conn)
		internalResp, err := client.StartTokenTransactionInternal(ctx, &pbinternal.StartTokenTransactionInternalRequest{
			KeyshareIds:                keyshareIDStrings,
			FinalTokenTransaction:      finalTokenTransaction,
			TokenTransactionSignatures: req.TokenTransactionSignatures,
		})
		if err != nil {
			log.Printf("Failed to execute start token transaction task with operator %s: %v", operator.Identifier, err)
			return nil, err
		}
		return internalResp, err
	})
	if err != nil {
		log.Printf("Failed to successfully execute start token transaction task with all operators: %v", err)
		return nil, err
	}

	operatorList, err := allSelection.OperatorList(config)
	if err != nil {
		log.Printf("Failed to get selection operator list: %v", err)
		return nil, err
	}
	operatorIdentifiers := make([]string, len(operatorList))
	for i, operator := range operatorList {
		operatorIdentifiers[i] = operator.Identifier
	}
	signingKeyshareInfo := &pb.SigningKeyshare{
		OwnerIdentifiers: operatorIdentifiers,
		// TODO: Unify threshold type (uint32 vs uint64) at all callsites between protos and config.
		Threshold: uint32(config.Threshold),
	}

	return &pb.StartTokenTransactionResponse{
		FinalTokenTransaction: finalTokenTransaction,
		KeyshareInfo:          signingKeyshareInfo,
	}, nil
}

// SignTokenTransaction signs the token transaction with the operators private key.
// If it is a transfer it also fetches this operators keyshare for each spent leaf and
// returns it to the wallet so it can finalize the transaction.
func (o TokenTransactionHandler) SignTokenTransaction(
	ctx context.Context,
	config *so.Config,
	req *pb.SignTokenTransactionRequest,
) (*pb.SignTokenTransactionResponse, error) {
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, o.config, req.IdentityPublicKey); err != nil {
		return nil, err
	}

	// Validate each leaf signature in the request. Each signed payload consists of
	//   payload.final_token_transaction_hash
	//   payload.operator_identity_public_key
	// This verifies that this request for this transaction (and release of the revocation
	// keyshare) came from the leaf owner and was intended for this specific SO.
	for _, leafSig := range req.OperatorSpecificSignatures {
		payloadHash, err := utils.HashOperatorSpecificTokenTransactionSignablePayload(leafSig.Payload)
		if err != nil {
			return nil, fmt.Errorf("failed to hash revocation keyshares payload: %w", err)
		}

		if err := utils.ValidateOwnershipSignature(
			leafSig.OwnerSignature,
			payloadHash,
			leafSig.OwnerPublicKey,
		); err != nil {
			return nil, fmt.Errorf("invalid owner signature for leaf: %w", err)
		}
	}
	finalTokenTransactionHash, err := utils.HashTokenTransaction(req.FinalTokenTransaction, false)
	if err != nil {
		log.Printf("Failed to hash final token transaction: %v", err)
		return nil, err
	}
	tokenTransactionReceipt, err := ent.FetchTokenTransactionData(ctx, req.FinalTokenTransaction)
	if err != nil {
		log.Printf("Sign request for token transaction did not map to a previously started transaction: %v", err)
		return nil, err
	}

	var invalidLeaves []string
	for i, leaf := range tokenTransactionReceipt.Edges.CreatedLeaf {
		if leaf.Status != schema.TokenLeafStatusCreatedStarted {
			invalidLeaves = append(invalidLeaves, fmt.Sprintf("output leaf %d has invalid status %s, expected CREATED_STARTED", i, leaf.Status))
		}
	}
	if len(invalidLeaves) > 0 {
		return nil, fmt.Errorf("found invalid output leaves: %s", strings.Join(invalidLeaves, "; "))
	}
	if len(tokenTransactionReceipt.Edges.SpentLeaf) > 0 {
		ownerPublicKeys := make([][]byte, len(tokenTransactionReceipt.Edges.SpentLeaf))
		// Assumes that all token public keys are the same as the first leaf. This is asserted when validating
		// in the StartTokenTransaction() step.
		tokenPublicKey := tokenTransactionReceipt.Edges.SpentLeaf[0].TokenPublicKey
		var invalidLeaves []string
		for i, leaf := range tokenTransactionReceipt.Edges.SpentLeaf {
			ownerPublicKeys[i] = leaf.OwnerPublicKey

			if leaf.Status != schema.TokenLeafStatusSpentStarted {
				invalidLeaves = append(invalidLeaves, fmt.Sprintf("input leaf %x has invalid status %s, expected SPENT_STARTED",
					leaf.ID, leaf.Status))
			}
		}
		if len(invalidLeaves) > 0 {
			return nil, fmt.Errorf("found invalid input leaves: %s", strings.Join(invalidLeaves, "; "))
		}
		// Bulk query all input leaf ids to ensure none of them are frozen.
		activeFreezes, err := ent.GetActiveFreezes(ctx, ownerPublicKeys, tokenPublicKey)
		if err != nil {
			log.Printf("Failed to query token freeze status: %v", err)
			return nil, err
		}

		if len(activeFreezes) > 0 {
			for _, freeze := range activeFreezes {
				log.Printf("Found active freeze - owner: %x, token: %x, freeze timestamp: %d",
					freeze.OwnerPublicKey,
					freeze.TokenPublicKey,
					freeze.WalletProvidedFreezeTimestamp)
			}
			return nil, fmt.Errorf("at least one input leaf is frozen. Cannot proceed with transaction")
		}
	}

	// Sign the token transaction hash with the operator identity private key.
	identityPrivateKey := secp256k1.PrivKeyFromBytes(config.IdentityPrivateKey)
	operatorSignature := ecdsa.Sign(identityPrivateKey, finalTokenTransactionHash)

	operatorSpecificSignature := make([][]byte, len(req.OperatorSpecificSignatures))
	for i, sig := range req.OperatorSpecificSignatures {
		operatorSpecificSignature[i] = sig.OwnerSignature
	}
	err = ent.UpdateSignedTransactionLeaves(ctx, tokenTransactionReceipt, operatorSpecificSignature, operatorSignature.Serialize())
	if err != nil {
		log.Printf("Failed to update leaves after signing: %v", err)
		return nil, err
	}

	err = o.SendTransactionToLRC20Node(
		ctx,
		config,
		req.FinalTokenTransaction,
		operatorSignature.Serialize(),
		// Revocation keys not available until finalize step.
		[][]byte{})
	if err != nil {
		log.Printf("Failed to send transaction to LRC20 node: %v", err)
		return nil, err
	}

	keyshares := make([]*ent.SigningKeyshare, len(tokenTransactionReceipt.Edges.SpentLeaf))
	for _, leaf := range tokenTransactionReceipt.Edges.SpentLeaf {
		keyshare, err := leaf.QueryRevocationKeyshare().Only(ctx)
		if err != nil {
			log.Printf("Failed to get keyshare for leaf: %v", err)
			return nil, err
		}
		// Use the vout index to order the keyshares
		keyshares[leaf.LeafSpentTransactionInputVout] = keyshare

		// Validate that the keyshare's public key is as expected.
		if !bytes.Equal(keyshare.PublicKey, leaf.WithdrawRevocationPublicKey) {
			return nil, fmt.Errorf(
				"keyshare public key %x does not match leaf revocation public key %x",
				keyshare.PublicKey,
				leaf.WithdrawRevocationPublicKey,
			)
		}
	}

	revocationKeyshares := make([][]byte, len(keyshares))
	for i, keyshare := range keyshares {
		revocationKeyshares[i] = keyshare.SecretShare
	}

	return &pb.SignTokenTransactionResponse{
		SparkOperatorSignature:              operatorSignature.Serialize(),
		TokenTransactionRevocationKeyshares: revocationKeyshares,
	}, nil
}

// FinalizeTokenTransaction takes the revocation private keys for spent leaves and updates their status to finalized.
func (o TokenTransactionHandler) FinalizeTokenTransaction(
	ctx context.Context,
	config *so.Config,
	req *pb.FinalizeTokenTransactionRequest,
) (*emptypb.Empty, error) {
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, o.config, req.IdentityPublicKey); err != nil {
		return nil, err
	}

	tokenTransactionReceipt, err := ent.FetchTokenTransactionData(ctx, req.FinalTokenTransaction)
	if err != nil {
		log.Printf("Failed to fetch matching transaction receipt: %v", err)
		return nil, fmt.Errorf("failed to fetch transaction receipt: %w", err)
	}

	// Extract revocation public keys from spent leaves
	revocationPublicKeys := make([][]byte, len(tokenTransactionReceipt.Edges.SpentLeaf))
	if (len(tokenTransactionReceipt.Edges.SpentLeaf)) != len(req.LeafToSpendRevocationKeys) {
		return nil, fmt.Errorf(
			"number of revocation keys (%d) does not match number of spent leaves (%d)",
			len(req.LeafToSpendRevocationKeys),
			len(tokenTransactionReceipt.Edges.SpentLeaf),
		)
	}
	for _, leaf := range tokenTransactionReceipt.Edges.SpentLeaf {
		revocationPublicKeys[leaf.LeafSpentTransactionInputVout] = leaf.WithdrawRevocationPublicKey
	}
	err = utils.ValidateRevocationKeys(req.LeafToSpendRevocationKeys, revocationPublicKeys)
	if err != nil {
		return nil, err
	}

	err = o.SendTransactionToLRC20Node(
		ctx,
		config,
		req.FinalTokenTransaction,
		// TODO: Consider removing this because it was already provided in the Sign() step.
		tokenTransactionReceipt.OperatorSignature,
		req.LeafToSpendRevocationKeys)
	if err != nil {
		log.Printf("Failed to send transaction to LRC20 node: %v", err)
		return nil, err
	}

	err = ent.UpdateFinalizedTransactionLeaves(ctx, tokenTransactionReceipt, req.LeafToSpendRevocationKeys)
	if err != nil {
		log.Printf("Failed to update leaves after finalizing: %v", err)
		return nil, err
	}

	return &emptypb.Empty{}, nil
}

func (o TokenTransactionHandler) SendTransactionToLRC20Node(
	ctx context.Context,
	config *so.Config,
	finalTokenTransaction *pb.TokenTransaction,
	operatorSignature []byte,
	revocationKeys [][]byte,
) error {
	network := common.Regtest.String() // TODO: Get network from transaction
	if lrc20Config, ok := config.Lrc20Configs[network]; ok && lrc20Config.DisableRpcs {
		log.Printf("Skipping LRC20 node call due to DisableRpcs flag")
		return nil
	}

	leavesToSpendData := make([]*pblrc20.SparkSignatureLeafData, len(revocationKeys))
	for i, revocationKey := range revocationKeys {
		leavesToSpendData[i] = &pblrc20.SparkSignatureLeafData{
			SpentLeafIndex: uint32(i),
			// Revocation will be nil if we are sending the transaction at the Sign() step.
			// It will be filled when sending a transaction in the Finalize() step.
			RevocationPrivateKey: revocationKey,
		}
	}

	signatureData := &pblrc20.SparkSignatureData{
		SparkOperatorSignature:         operatorSignature,
		SparkOperatorIdentityPublicKey: config.IdentityPublicKey(),
		FinalTokenTransaction:          finalTokenTransaction,
		LeavesToSpendData:              leavesToSpendData,
	}

	conn, err := helper.ConnectToLrc20Node(config)
	if err != nil {
		return fmt.Errorf("failed to connect to LRC20 node: %w", err)
	}
	defer conn.Close()
	lrc20Client := pblrc20.NewSparkServiceClient(conn)

	_, err = lrc20Client.SendSparkSignature(ctx, &pblrc20.SendSparkSignatureRequest{
		SignatureData: signatureData,
	})
	if err != nil {
		return err
	}

	return nil
}

func (o TokenTransactionHandler) FreezeTokens(
	ctx context.Context,
	config *so.Config,
	req *pb.FreezeTokensRequest,
) (*pb.FreezeTokensResponse, error) {
	freezePayloadHash, err := utils.HashFreezeTokensPayload(req.FreezeTokensPayload)
	if err != nil {
		return nil, fmt.Errorf("failed to hash freeze tokens payload: %w", err)
	}

	if err := utils.ValidateOwnershipSignature(
		req.IssuerSignature,
		freezePayloadHash,
		req.FreezeTokensPayload.TokenPublicKey,
	); err != nil {
		return nil, fmt.Errorf("invalid issuer signature to freeze token public key %x: %w", req.FreezeTokensPayload.TokenPublicKey, err)
	}

	// Check for existing freeze.
	activeFreezes, err := ent.GetActiveFreezes(ctx, [][]byte{req.FreezeTokensPayload.OwnerPublicKey}, req.FreezeTokensPayload.TokenPublicKey)
	if err != nil {
		log.Printf("Failed to check for existing token freeze: %v", err)
		return nil, err
	}
	if req.FreezeTokensPayload.ShouldUnfreeze {
		if len(activeFreezes) == 0 {
			return nil, fmt.Errorf("no active freezes found to thaw")
		}
		if len(activeFreezes) > 1 {
			return nil, fmt.Errorf("multiple active freezes found for this owner and token which should not happen")
		}
		err = ent.ThawActiveFreeze(ctx, activeFreezes[0].ID, req.FreezeTokensPayload.IssuerProvidedTimestamp)
		if err != nil {
			log.Printf("Failed to update token freeze status to thawed: %v", err)
			return nil, err
		}
	} else { // Freeze
		if len(activeFreezes) > 0 {
			return nil, fmt.Errorf("tokens are already frozen for this owner and token")
		}
		err = ent.ActivateFreeze(ctx,
			req.FreezeTokensPayload.OwnerPublicKey,
			req.FreezeTokensPayload.TokenPublicKey,
			req.IssuerSignature,
			req.FreezeTokensPayload.IssuerProvidedTimestamp,
		)
		if err != nil {
			log.Printf("Failed to create token freeze entity: %v", err)
			return nil, err
		}
	}

	// Collect information about the frozen leaves.
	leafIDs, totalAmount, err := ent.GetOwnedLeafTokenStats(ctx, [][]byte{req.FreezeTokensPayload.OwnerPublicKey}, req.FreezeTokensPayload.TokenPublicKey)
	if err != nil {
		log.Printf("Failed to get impacted leaf stats: %v", err)
		return nil, err
	}

	err = o.FreezeTokensOnLRC20Node(ctx, config, req)
	if err != nil {
		log.Printf("Failed to freeze tokens on LRC20 node: %v", err)
		return nil, err
	}

	return &pb.FreezeTokensResponse{
		ImpactedLeafIds:     leafIDs,
		ImpactedTokenAmount: totalAmount.Bytes(),
	}, nil
}

func (o TokenTransactionHandler) FreezeTokensOnLRC20Node(
	ctx context.Context,
	config *so.Config,
	req *pb.FreezeTokensRequest,
) error {
	network := common.Regtest.String() // TODO: Get network from transaction
	if lrc20Config, ok := config.Lrc20Configs[network]; ok && lrc20Config.DisableRpcs {
		log.Printf("Skipping LRC20 node call due to DisableRpcs flag")
		return nil
	}

	conn, err := helper.ConnectToLrc20Node(config)
	if err != nil {
		return fmt.Errorf("failed to connect to LRC20 node: %w", err)
	}
	defer conn.Close()
	lrc20Client := pblrc20.NewSparkServiceClient(conn)

	_, err = lrc20Client.FreezeTokens(ctx, req)
	if err != nil {
		return err
	}

	return nil
}

func (o TokenTransactionHandler) GetOwnedTokenLeaves(
	ctx context.Context,
	req *pb.GetOwnedTokenLeavesRequest,
) (*pb.GetOwnedTokenLeavesResponse, error) {
	leaves, err := ent.GetOwnedLeaves(ctx, req.OwnerPublicKeys, req.TokenPublicKeys)
	if err != nil {
		log.Printf("Failed to get owned leaf stats: %v", err)
		return nil, err
	}

	leavesWithPrevTxData := make([]*pb.LeafWithPreviousTransactionData, len(leaves))
	for i, leaf := range leaves {
		idStr := leaf.ID.String()
		leavesWithPrevTxData[i] = &pb.LeafWithPreviousTransactionData{
			Leaf: &pb.TokenLeafOutput{
				Id:                            &idStr,
				OwnerPublicKey:                leaf.OwnerPublicKey,
				RevocationPublicKey:           leaf.WithdrawRevocationPublicKey,
				WithdrawBondSats:              &leaf.WithdrawBondSats,
				WithdrawRelativeBlockLocktime: &leaf.WithdrawRelativeBlockLocktime,
				TokenPublicKey:                leaf.TokenPublicKey,
				TokenAmount:                   leaf.TokenAmount,
			},
			PreviousTransactionHash: leaf.Edges.LeafCreatedTokenTransactionReceipt.FinalizedTokenTransactionHash,
			PreviousTransactionVout: leaf.LeafCreatedTransactionOutputVout,
		}
	}

	return &pb.GetOwnedTokenLeavesResponse{
		LeavesWithPreviousTransactionData: leavesWithPrevTxData,
	}, nil
}
