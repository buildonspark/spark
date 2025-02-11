package handler

import (
	"bytes"
	"context"
	"fmt"
	"math/big"

	pb "github.com/lightsparkdev/spark-go/proto/spark"
	pbinternal "github.com/lightsparkdev/spark-go/proto/spark_internal"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent"
	"github.com/lightsparkdev/spark-go/so/ent/schema"
	"github.com/lightsparkdev/spark-go/so/utils"
)

// InternalTokenTransactionHandler is the deposit handler for so internal
type InternalTokenTransactionHandler struct {
	config *so.Config
}

// NewInternalTokenTransactionHandler creates a new InternalTokenTransactionHandler.
func NewInternalTokenTransactionHandler(config *so.Config) *InternalTokenTransactionHandler {
	return &InternalTokenTransactionHandler{config: config}
}

func (h *InternalTokenTransactionHandler) StartTokenTransactionInternal(ctx context.Context, config *so.Config, req *pbinternal.StartTokenTransactionInternalRequest) error {
	// TODO: Validate that the keyshare UUIDs match keyshares reserved specifically for
	// this transaction. This is to prevent a malicious coordinator from swapping out keyshares.

	err := utils.ValidateFinalTokenTransaction(req.FinalTokenTransaction, req.TokenTransactionSignatures, config.GetSigningOperatorList())
	if err != nil {
		return fmt.Errorf("invalid final token transaction: %w", err)
	}

	// Validate the token transaction.
	if req.FinalTokenTransaction.GetIssueInput() != nil {
		err = ValidateIssue(req.FinalTokenTransaction, req.TokenTransactionSignatures)
		if err != nil {
			return fmt.Errorf("invalid token transaction: %w", err)
		}
	}
	var leafToSpendEnts []*ent.TokenLeaf
	if req.FinalTokenTransaction.GetTransferInput() != nil {
		// Get the leaves to spend from the database.
		leafToSpendEnts, err = ent.FetchInputLeaves(ctx, req.FinalTokenTransaction.GetTransferInput().GetLeavesToSpend())
		if err != nil {
			return fmt.Errorf("failed to fetch leaves to spend: %w", err)
		}
		if len(leafToSpendEnts) != len(req.FinalTokenTransaction.GetTransferInput().GetLeavesToSpend()) {
			return fmt.Errorf("failed to fetch all leaves to spend: got %d leaves, expected %d", len(leafToSpendEnts), len(req.FinalTokenTransaction.GetTransferInput().GetLeavesToSpend()))
		}

		err = ValidateTransferUsingPreviousTransactionData(req.FinalTokenTransaction, req.TokenTransactionSignatures, leafToSpendEnts)
		if err != nil {
			return fmt.Errorf("error validating transfer using previous leaf data: %w", err)
		}
	}

	// Save the token transaction receipt, created leaf ents, and update the leaves to spend.
	tokenTransactionReceipt, err := ent.SaveTokenTransactionReceiptAndLeafEnts(ctx, req.FinalTokenTransaction, req.KeyshareIds)
	if err != nil {
		return fmt.Errorf("failed to save token transaction receipt and leaf ents: %w", err)
	}

	if leafToSpendEnts != nil {
		err = ent.MarkLeavesAsSpent(ctx, leafToSpendEnts, req.TokenTransactionSignatures.GetOwnerSignatures(), tokenTransactionReceipt)
		if err != nil {
			return fmt.Errorf("failed to update token leaves to spend: %w", err)
		}
	}

	return nil
}

func ValidateIssue(
	tokenTransaction *pb.TokenTransaction,
	tokenTransactionSignatures *pb.TokenTransactionSignatures,
) error {
	partialTokenTransactionHash, err := utils.HashTokenTransaction(tokenTransaction, true)
	if err != nil {
		return fmt.Errorf("failed to hash token transaction: %w", err)
	}

	err = utils.ValidateOwnershipSignature(tokenTransactionSignatures.GetOwnerSignatures()[0], partialTokenTransactionHash, tokenTransaction.GetIssueInput().GetIssuerPublicKey())
	if err != nil {
		return fmt.Errorf("invalid issuer signature: %w", err)
	}

	return nil
}

func ValidateTransferUsingPreviousTransactionData(
	tokenTransaction *pb.TokenTransaction,
	tokenTransactionSignatures *pb.TokenTransactionSignatures,
	leafToSpendEnts []*ent.TokenLeaf,
) error {
	// Validate that the correct number of signatures were provided
	if len(tokenTransactionSignatures.GetOwnerSignatures()) != len(leafToSpendEnts) {
		return fmt.Errorf("number of signatures must match number of ownership public keys")
	}

	// Validate that all token public keys in leaves to spend match the output leaves.
	// Ok to just check against the first output because output token public key uniformity
	// is checked in the main ValidateTokenTransaction() call.
	expectedTokenPubKey := tokenTransaction.OutputLeaves[0].GetTokenPublicKey()
	if expectedTokenPubKey == nil {
		return fmt.Errorf("token public key cannot be nil in output leaves")
	}
	for i, leafEnt := range leafToSpendEnts {
		if !bytes.Equal(leafEnt.TokenPublicKey, expectedTokenPubKey) {
			return fmt.Errorf("token public key mismatch for leaf %d - input leaves must be for the same token public key as the output", i)
		}
	}

	// Validate token conservation in inputs + outputs.
	totalInputAmount := new(big.Int)
	for _, leafEnt := range leafToSpendEnts {
		inputAmount := new(big.Int).SetBytes(leafEnt.TokenAmount)
		totalInputAmount.Add(totalInputAmount, inputAmount)
	}
	totalOutputAmount := new(big.Int)
	for _, outputLeaf := range tokenTransaction.OutputLeaves {
		outputAmount := new(big.Int).SetBytes(outputLeaf.GetTokenAmount())
		totalOutputAmount.Add(totalOutputAmount, outputAmount)
	}
	if totalInputAmount.Cmp(totalOutputAmount) != 0 {
		return fmt.Errorf("total input amount %s does not match total output amount %s", totalInputAmount.String(), totalOutputAmount.String())
	}

	// Validate that the ownership signatures match the ownership public keys in the leaves to spend.
	partialTokenTransactionHash, err := utils.HashTokenTransaction(tokenTransaction, true)
	if err != nil {
		return fmt.Errorf("failed to hash token transaction: %w", err)
	}

	for i, ownershipSignature := range tokenTransactionSignatures.GetOwnerSignatures() {
		if ownershipSignature == nil {
			return fmt.Errorf("ownership signature cannot be nil")
		}

		err = utils.ValidateOwnershipSignature(ownershipSignature, partialTokenTransactionHash, leafToSpendEnts[i].OwnerPublicKey)
		if err != nil {
			return fmt.Errorf("invalid ownership signature for leaf %d: %w", i, err)
		}
	}

	for i, leafEnt := range leafToSpendEnts {
		if leafEnt.Status != schema.TokenLeafStatusCreatedFinalized {
			return fmt.Errorf("leaf %d either has already been spent or it is too early to be spent. It has status: %s", i, leafEnt.Status)
		}
	}

	return nil
}
