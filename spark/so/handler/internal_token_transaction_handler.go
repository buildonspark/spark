package handler

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"math/big"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/google/uuid"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	pbinternal "github.com/lightsparkdev/spark-go/proto/spark_internal"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent"
	"github.com/lightsparkdev/spark-go/so/ent/schema"
	"github.com/lightsparkdev/spark-go/so/ent/signingkeyshare"
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

//  1. Validates that the wallet requesting the token transaction has either a) ownership of the token for issuance or
//     b) ownership of the leaves to spend for transfers.
//  2. Associates keyshares across SO's for a particular signing job (which could authorize issuance or spending of a leaf).
func (h *InternalTokenTransactionHandler) SignTokenTransaction(ctx context.Context, req *pbinternal.SignTokenTransactionRequest) (*pbinternal.SignTokenTransactionResponse, error) {
	db := ent.GetDbFromContext(ctx)
	// Query revocation public keys for the provided keyshare IDs to validate the filled values.
	expectedRevocationPublicKeys := make([][]byte, len(req.OutputLeafRevocationKeyshareIds))
	for i, keyshareIDStr := range req.OutputLeafRevocationKeyshareIds {
		keyshareID, err := uuid.Parse(keyshareIDStr)
		if err != nil {
			log.Printf("Failed to parse revocation keyshare ID: %v", err)
			return nil, err
		}
		keyshare, err := db.SigningKeyshare.Query().Where(signingkeyshare.ID(keyshareID)).Only(ctx)
		if err != nil {
			log.Printf("Failed to get revocation keyshare: %v", err)
			return nil, err
		}
		expectedRevocationPublicKeys[i] = keyshare.PublicKey
	}
	err := utils.ValidateFinalTokenTransaction(req.TokenTransaction, req.TokenTransactionSignatures, h.config.GetSigningOperatorList(), expectedRevocationPublicKeys)
	if err != nil {
		log.Printf("Failed to validate final token transaction: %v", err)
		return nil, err
	}

	partialTokenTransactionHash, err := utils.HashTokenTransaction(req.TokenTransaction, true)
	if err != nil {
		log.Printf("Failed to hash partial token transaction: %v", err)
		return nil, err
	}
	finalTokenTransactionHash, err := utils.HashTokenTransaction(req.TokenTransaction, false)
	if err != nil {
		log.Printf("Failed to hash final token transaction: %v", err)
		return nil, err
	}
	tokenTransactionReceipt, err := db.TokenTransactionReceipt.Create().
		SetPartialTokenTransactionHash(partialTokenTransactionHash).
		SetFinalizedTokenTransactionHash(finalTokenTransactionHash).
		Save(ctx)
	if err != nil {
		log.Printf("Failed to create token transaction receipt: %v", err)
		return nil, err
	}

	outputLeaves := make([]*ent.TokenLeafCreate, 0, len(req.TokenTransaction.OutputLeaves))
	for leafIndex, outputLeaf := range req.TokenTransaction.OutputLeaves {
		revocationUUID, err := uuid.Parse(req.OutputLeafRevocationKeyshareIds[leafIndex])
		if err != nil {
			return nil, err
		}
		outputLeaves = append(
			outputLeaves,
			db.TokenLeaf.
				Create().
				SetStatus(schema.TokenLeafStatusCreatedSigned).
				SetOwnerPublicKey(outputLeaf.OwnerPublicKey).
				SetWithdrawalBondSats(outputLeaf.WithdrawalBondSats).
				SetWithdrawalLocktime(outputLeaf.WithdrawalLocktime).
				SetWithdrawalRevocationPublicKey(outputLeaf.RevocationPublicKey).
				SetTokenPublicKey(outputLeaf.TokenPublicKey).
				SetTokenAmount(outputLeaf.TokenAmount).
				SetLeafCreatedTransactionOuputVout(uint32(leafIndex)).
				SetRevocationKeyshareID(revocationUUID).
				SetLeafCreatedTokenTransactionReceiptID(tokenTransactionReceipt.ID).
				SetRevocationKeyshareID(revocationUUID),
		)
	}
	_, err = db.TokenLeaf.CreateBulk(outputLeaves...).Save(ctx)
	if err != nil {
		log.Printf("Failed to create token leaves: %v", err)
		return nil, err
	}

	if req.TokenTransaction.GetIssueInput() != nil {
		err = ValidateIssue(req.TokenTransaction, req.TokenTransactionSignatures)
		if err != nil {
			log.Printf("Failed to validate issuance signature for this transasction: %v", err)
			return nil, err
		}
	}

	if req.TokenTransaction.GetTransferInput() != nil {
		tokenLeafEntsToSpend, err := ent.FetchTokenLeavesFromLeavesToSpend(ctx, req.TokenTransaction.GetTransferInput().LeavesToSpend)
		if err != nil {
			log.Printf("Failed to fetch input leaves from previous transactions: %v", err)
			return nil, err
		}

		// Validate the transfer input with context from prior transactions.
		err = ValidateTransferUsingPreviousTransactionData(req.TokenTransaction, req.TokenTransactionSignatures, tokenLeafEntsToSpend)
		if err != nil {
			log.Printf("Failed to validate leaves for transfer: %v", err)
			return nil, err
		}

		inputLeavesUpdate := make([]*ent.TokenLeafUpdateOne, 0, len(req.TokenTransaction.GetTransferInput().LeavesToSpend))
		for leafIndex, leafToSpendEnt := range tokenLeafEntsToSpend {
			if err != nil {
				log.Printf("Ownership signature for leaf to spend was invalid: leaf_index=%d, owner_public_key=%x, partial_tx_hash=%x, err=%v",
					leafIndex,
					tokenLeafEntsToSpend[leafIndex].OwnerPublicKey,
					partialTokenTransactionHash, err)
				return nil, err
			}
			inputLeavesUpdate = append(
				inputLeavesUpdate,
				db.TokenLeaf.UpdateOne(leafToSpendEnt).
					SetStatus(schema.TokenLeafStatusSpentSigned).
					SetLeafSpentOwnershipSignature(req.TokenTransactionSignatures.GetOwnerSignatures()[leafIndex]).
					SetLeafSpentTokenTransactionReceiptID(tokenTransactionReceipt.ID),
			)
		}
		// Execute all the updates
		for _, update := range inputLeavesUpdate {
			if _, err := update.Save(ctx); err != nil {
				log.Printf("Failed to update spent leaf: %v", err)
				return nil, err
			}
		}
	}

	// Sign the token transaction hash with the operator identity private key.
	identityPrivateKey := secp256k1.PrivKeyFromBytes(h.config.IdentityPrivateKey)
	operatorSignature := ecdsa.Sign(identityPrivateKey, finalTokenTransactionHash)
	if err != nil {
		log.Printf("Failed to sign token transaction with operator key: %v", err)
		return nil, err
	}

	return &pbinternal.SignTokenTransactionResponse{
		OperatorSignature: operatorSignature.Serialize(),
	}, nil
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

	// TODO: Change to handle all leaf statuses appropriately
	for i, leafEnt := range leafToSpendEnts {
		if leafEnt.Status == schema.TokenLeafStatusSpentSigned {
			return fmt.Errorf("leaf %d has already been spent", i)
		}
	}

	return nil
}
