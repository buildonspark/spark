package ent

import (
	"context"
	"encoding/hex"
	"fmt"
	"log"

	"github.com/google/uuid"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent/schema"
	"github.com/lightsparkdev/spark-go/so/ent/tokenleaf"
	"github.com/lightsparkdev/spark-go/so/ent/tokentransactionreceipt"
	"github.com/lightsparkdev/spark-go/so/utils"
)

func GetReceiptMapFromList(receipts []*TokenTransactionReceipt) (map[string]*TokenTransactionReceipt, error) {
	receiptMap := make(map[string]*TokenTransactionReceipt)
	for _, r := range receipts {
		if len(r.FinalizedTokenTransactionHash) > 0 {
			key := hex.EncodeToString(r.FinalizedTokenTransactionHash)
			receiptMap[key] = r
		}
	}
	return receiptMap, nil
}

func CreateStartedTransactionEntities(
	ctx context.Context,
	tokenTransaction *pb.TokenTransaction,
	tokenTransactionSignatures *pb.TokenTransactionSignatures,
	leafRevocationKeyshareIDs []string,
	leafToSpendEnts []*TokenLeaf,
) (*TokenTransactionReceipt, error) {
	partialTokenTransactionHash, err := utils.HashTokenTransaction(tokenTransaction, true)
	if err != nil {
		log.Printf("Failed to hash partial token transaction: %v", err)
		return nil, err
	}
	finalTokenTransactionHash, err := utils.HashTokenTransaction(tokenTransaction, false)
	if err != nil {
		log.Printf("Failed to hash final token transaction: %v", err)
		return nil, err
	}

	db := GetDbFromContext(ctx)
	var tokenMintEnt *TokenMint
	if tokenTransaction.GetMintInput() != nil {
		tokenMintEnt, err = db.TokenMint.Create().
			SetIssuerPublicKey(tokenTransaction.GetMintInput().GetIssuerPublicKey()).
			SetIssuerSignature(tokenTransactionSignatures.GetOwnerSignatures()[0]).
			SetWalletProvidedTimestamp(tokenTransaction.GetMintInput().GetIssuerProvidedTimestamp()).
			Save(ctx)
		if err != nil {
			log.Printf("Failed to create token mint ent: %v", err)
			return nil, err
		}
	}

	txReceiptUpdate := db.TokenTransactionReceipt.Create().
		SetPartialTokenTransactionHash(partialTokenTransactionHash).
		SetFinalizedTokenTransactionHash(finalTokenTransactionHash).
		SetStatus(schema.TokenTransactionStatusStarted)
	if tokenMintEnt != nil {
		txReceiptUpdate.SetMintID(tokenMintEnt.ID)
	}
	tokenTransactionReceipt, err := txReceiptUpdate.Save(ctx)
	if err != nil {
		log.Printf("Failed to create token transaction receipt: %v", err)
		return nil, err
	}

	if tokenTransaction.GetTransferInput() != nil {
		ownershipSignatures := tokenTransactionSignatures.GetOwnerSignatures()
		if len(ownershipSignatures) != len(leafToSpendEnts) {
			return nil, fmt.Errorf(
				"number of signatures %d doesn't match number of leaves to spend %d",
				len(ownershipSignatures),
				len(leafToSpendEnts),
			)
		}

		for leafIndex, leafToSpendEnt := range leafToSpendEnts {
			_, err = db.TokenLeaf.UpdateOne(leafToSpendEnt).
				SetStatus(schema.TokenLeafStatusSpentStarted).
				SetLeafSpentTokenTransactionReceiptID(tokenTransactionReceipt.ID).
				SetLeafSpentOwnershipSignature(ownershipSignatures[leafIndex]).
				SetLeafSpentTransactionInputVout(int32(leafIndex)).
				Save(ctx)
			if err != nil {
				log.Printf("Failed to update leaf to spent: %v", err)
				return nil, err
			}
		}
	}

	outputLeaves := make([]*TokenLeafCreate, 0, len(tokenTransaction.OutputLeaves))
	for leafIndex, outputLeaf := range tokenTransaction.OutputLeaves {
		revocationUUID, err := uuid.Parse(leafRevocationKeyshareIDs[leafIndex])
		if err != nil {
			return nil, err
		}
		leafUUID, err := uuid.Parse(*outputLeaf.Id)
		if err != nil {
			return nil, err
		}
		outputLeaves = append(
			outputLeaves,
			db.TokenLeaf.
				Create().
				// TODO: Consider whether the coordinator instead of the wallet should define this ID.
				SetID(leafUUID).
				SetStatus(schema.TokenLeafStatusCreatedStarted).
				SetOwnerPublicKey(outputLeaf.OwnerPublicKey).
				SetWithdrawBondSats(*outputLeaf.WithdrawBondSats).
				SetWithdrawRelativeBlockLocktime(*outputLeaf.WithdrawRelativeBlockLocktime).
				SetWithdrawRevocationPublicKey(outputLeaf.RevocationPublicKey).
				SetTokenPublicKey(outputLeaf.TokenPublicKey).
				SetTokenAmount(outputLeaf.TokenAmount).
				SetLeafCreatedTransactionOutputVout(int32(leafIndex)).
				SetRevocationKeyshareID(revocationUUID).
				SetLeafCreatedTokenTransactionReceiptID(tokenTransactionReceipt.ID),
		)
	}
	_, err = db.TokenLeaf.CreateBulk(outputLeaves...).Save(ctx)
	if err != nil {
		log.Printf("Failed to create token leaves: %v", err)
		return nil, err
	}
	return tokenTransactionReceipt, nil
}

// UpdateSignedTransaction updates the status and ownership signatures of the input + output leaves
// and the issuer signature (if applicable).
func UpdateSignedTransaction(
	ctx context.Context,
	tokenTransactionReceipt *TokenTransactionReceipt,
	operatorSpecificOwnershipSignatures [][]byte,
	operatorSignature []byte,
) error {
	// Update the token transaction receipt with the operator signature and new status
	_, err := GetDbFromContext(ctx).TokenTransactionReceipt.UpdateOne(tokenTransactionReceipt).
		SetOperatorSignature(operatorSignature).
		SetStatus(schema.TokenTransactionStatusSigned).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to update token transaction receipt with operator signature and status: %w", err)
	}

	newInputLeafStatus := schema.TokenLeafStatusSpentSigned
	newOutputLeafStatus := schema.TokenLeafStatusCreatedSigned
	if tokenTransactionReceipt.Edges.Mint != nil {
		// If this is a mint, update status straight to finalized because a follow up Finalize() call
		// is not necessary for mint.
		newInputLeafStatus = schema.TokenLeafStatusSpentFinalized
		newOutputLeafStatus = schema.TokenLeafStatusCreatedFinalized
		if len(operatorSpecificOwnershipSignatures) != 1 {
			return fmt.Errorf(
				"expected 1 ownership signature for mint, got %d",
				len(operatorSpecificOwnershipSignatures),
			)
		}

		_, err := GetDbFromContext(ctx).TokenMint.UpdateOne(tokenTransactionReceipt.Edges.Mint).
			SetOperatorSpecificIssuerSignature(operatorSpecificOwnershipSignatures[0]).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("failed to update mint with signature: %w", err)
		}
	}

	// Update input leaves.
	if tokenTransactionReceipt.Edges.SpentLeaf != nil {
		for _, leafToSpendEnt := range tokenTransactionReceipt.Edges.SpentLeaf {
			spentLeaves := tokenTransactionReceipt.Edges.SpentLeaf
			if len(spentLeaves) == 0 {
				return fmt.Errorf("no spent leaves found for transaction. cannot finalize")
			}

			// Validate that we have the right number of revocation keys
			if len(operatorSpecificOwnershipSignatures) != len(spentLeaves) {
				return fmt.Errorf(
					"number of operator specific ownership signatures (%d) does not match number of spent leaves (%d)",
					len(operatorSpecificOwnershipSignatures),
					len(spentLeaves),
				)
			}

			inputLeafIndex := leafToSpendEnt.LeafSpentTransactionInputVout
			_, err := GetDbFromContext(ctx).TokenLeaf.UpdateOne(leafToSpendEnt).
				SetStatus(newInputLeafStatus).
				SetLeafSpentOperatorSpecificOwnershipSignature(operatorSpecificOwnershipSignatures[inputLeafIndex]).
				Save(ctx)
			if err != nil {
				return fmt.Errorf("failed to update spent leaf to signed: %w", err)
			}
		}
	}

	// Update output leaves.
	leafIDs := make([]uuid.UUID, len(tokenTransactionReceipt.Edges.CreatedLeaf))
	for i, leaf := range tokenTransactionReceipt.Edges.CreatedLeaf {
		leafIDs[i] = leaf.ID
	}
	_, err = GetDbFromContext(ctx).TokenLeaf.Update().
		Where(tokenleaf.IDIn(leafIDs...)).
		SetStatus(newOutputLeafStatus).
		Save(ctx)
	if err != nil {
		log.Printf("Failed to bulk update leaf status to signed: %v", err)
		return err
	}

	return nil
}

// UpdateFinalizedTransaction updates the status and ownership signatures of the finalized input + output leaves.
func UpdateFinalizedTransaction(
	ctx context.Context,
	tokenTransactionReceipt *TokenTransactionReceipt,
	revocationKeys [][]byte,
) error {
	// Update the token transaction receipt with the operator signature and new status
	_, err := GetDbFromContext(ctx).TokenTransactionReceipt.UpdateOne(tokenTransactionReceipt).
		SetStatus(schema.TokenTransactionStatusFinalized).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to update token transaction receipt with finalized status: %w", err)
	}

	spentLeaves := tokenTransactionReceipt.Edges.SpentLeaf
	if len(spentLeaves) == 0 {
		return fmt.Errorf("no spent leaves found for transaction. cannot finalize")
	}
	if len(revocationKeys) != len(spentLeaves) {
		return fmt.Errorf(
			"number of revocation keys (%d) does not match number of spent leaves (%d)",
			len(revocationKeys),
			len(spentLeaves),
		)
	}
	// Update input leaves.
	for _, leafToSpendEnt := range tokenTransactionReceipt.Edges.SpentLeaf {
		inputLeafIndex := leafToSpendEnt.LeafSpentTransactionInputVout
		_, err := GetDbFromContext(ctx).TokenLeaf.UpdateOne(leafToSpendEnt).
			SetStatus(schema.TokenLeafStatusSpentFinalized).
			SetLeafSpentRevocationPrivateKey(revocationKeys[inputLeafIndex]).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("failed to update spent leaf to signed: %w", err)
		}
	}

	// Update output leaves.
	leafIDs := make([]uuid.UUID, len(tokenTransactionReceipt.Edges.CreatedLeaf))
	for i, leaf := range tokenTransactionReceipt.Edges.CreatedLeaf {
		leafIDs[i] = leaf.ID
	}
	_, err = GetDbFromContext(ctx).TokenLeaf.Update().
		Where(tokenleaf.IDIn(leafIDs...)).
		SetStatus(schema.TokenLeafStatusCreatedFinalized).
		Save(ctx)
	if err != nil {
		log.Printf("Failed to bulk update leaf status to signed: %v", err)
		return err
	}
	return nil
}

// UpdateCancelledTransaction updates the status and ownership signatures input + output leaves in response to a cancelled transaction.
func UpdateCancelledTransaction(
	ctx context.Context,
	tokenTransactionReceipt *TokenTransactionReceipt,
) error {
	// Update the token transaction receipt with the operator signature and new status
	_, err := GetDbFromContext(ctx).TokenTransactionReceipt.UpdateOne(tokenTransactionReceipt).
		SetStatus(schema.TokenTransactionStatus(schema.TokenTransactionStatusSignedCancelled)).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to update token transaction receipt with finalized status: %w", err)
	}

	// Change input leaf statuses back to CREATED_FINALIZED to re-enable spending.
	spentLeaves := tokenTransactionReceipt.Edges.SpentLeaf
	for _, leafToSpendEnt := range spentLeaves {
		if leafToSpendEnt.Status != schema.TokenLeafStatusSpentSigned {
			return fmt.Errorf("spent leaf ID %s has status %s, expected %s",
				leafToSpendEnt.ID.String(),
				leafToSpendEnt.Status,
				schema.TokenLeafStatusSpentSigned)
		}
		_, err := GetDbFromContext(ctx).TokenLeaf.UpdateOne(leafToSpendEnt).
			SetStatus(schema.TokenLeafStatusCreatedFinalized).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("failed to cancel transaction and update spent leaf back to CREATED_FINALIZED: %w", err)
		}
	}

	// Change output leaf statuses to SIGNED_CANCELLED to invalidate them.
	leafIDs := make([]uuid.UUID, len(tokenTransactionReceipt.Edges.CreatedLeaf))
	for i, leaf := range tokenTransactionReceipt.Edges.CreatedLeaf {
		leafIDs[i] = leaf.ID
		// Verify leaf is in the expected state
		if leaf.Status != schema.TokenLeafStatusCreatedSigned {
			return fmt.Errorf("created leaf ID %s has status %s, expected %s",
				leaf.ID.String(),
				leaf.Status,
				schema.TokenLeafStatusCreatedSigned)
		}
	}
	_, err = GetDbFromContext(ctx).TokenLeaf.Update().
		Where(tokenleaf.IDIn(leafIDs...)).
		SetStatus(schema.TokenLeafStatusCreatedSignedCancelled).
		Save(ctx)
	if err != nil {
		log.Printf("Failed to bulk update leaf status to signed: %v", err)
		return err
	}
	return nil
}

// FetchTokenTransactionData refetches the receipt with all its relations.
func FetchAndLockTokenTransactionData(ctx context.Context, finalTokenTransaction *pb.TokenTransaction) (*TokenTransactionReceipt, error) {
	finalTokenTransactionHash, err := utils.HashTokenTransaction(finalTokenTransaction, false)
	if err != nil {
		return nil, err
	}

	tokenTransctionReceipt, err := GetDbFromContext(ctx).TokenTransactionReceipt.Query().
		Where(tokentransactionreceipt.FinalizedTokenTransactionHash(finalTokenTransactionHash)).
		WithCreatedLeaf().
		WithSpentLeaf().
		WithMint().
		ForUpdate().
		Only(ctx)
	if err != nil {
		return nil, err
	}

	// Sanity check that inputs and outputs matching the expected length were found.
	if finalTokenTransaction.GetMintInput() != nil {
		if tokenTransctionReceipt.Edges.Mint == nil {
			return nil, fmt.Errorf("mint transaction must have a mint record, but none was found")
		}
	} else { // Transfer
		if len(finalTokenTransaction.GetTransferInput().LeavesToSpend) != len(tokenTransctionReceipt.Edges.SpentLeaf) {
			return nil, fmt.Errorf(
				"number of input leaves in transaction (%d) does not match number of spent leaves in receipt (%d)",
				len(finalTokenTransaction.GetTransferInput().LeavesToSpend),
				len(tokenTransctionReceipt.Edges.SpentLeaf),
			)
		}
	}
	if len(finalTokenTransaction.OutputLeaves) != len(tokenTransctionReceipt.Edges.CreatedLeaf) {
		return nil, fmt.Errorf(
			"number of output leaves in transaction (%d) does not match number of created leaves in receipt (%d)",
			len(finalTokenTransaction.OutputLeaves),
			len(tokenTransctionReceipt.Edges.CreatedLeaf),
		)
	}
	return tokenTransctionReceipt, nil
}

// MarshalProto converts a TokenTransactionReceipt to a spark protobuf TokenTransaction.
// This assumes the receipt already has all its relationships loaded.
func (r *TokenTransactionReceipt) MarshalProto(config *so.Config) (*pb.TokenTransaction, error) {
	// TODO: When adding support for adding/removing, we will need to save this per transaction rather than
	// pulling from the config.
	operatorPublicKeys := make([][]byte, 0, len(config.SigningOperatorMap))
	for _, operator := range config.SigningOperatorMap {
		operatorPublicKeys = append(operatorPublicKeys, operator.IdentityPublicKey)
	}

	// Create a new TokenTransaction
	tokenTransaction := &pb.TokenTransaction{
		OutputLeaves: make([]*pb.TokenLeafOutput, len(r.Edges.CreatedLeaf)),
		// Get all operator identity public keys from the config
		SparkOperatorIdentityPublicKeys: operatorPublicKeys,
	}

	// Set up output leaves
	for i, leaf := range r.Edges.CreatedLeaf {
		idStr := leaf.ID.String()
		tokenTransaction.OutputLeaves[i] = &pb.TokenLeafOutput{
			Id:                            &idStr,
			OwnerPublicKey:                leaf.OwnerPublicKey,
			RevocationPublicKey:           leaf.WithdrawRevocationPublicKey,
			WithdrawBondSats:              &leaf.WithdrawBondSats,
			WithdrawRelativeBlockLocktime: &leaf.WithdrawRelativeBlockLocktime,
			TokenPublicKey:                leaf.TokenPublicKey,
			TokenAmount:                   leaf.TokenAmount,
		}
	}

	// Determine if this is a mint or transfer transaction
	if r.Edges.Mint != nil {
		// This is a mint transaction
		tokenTransaction.TokenInput = &pb.TokenTransaction_MintInput{
			MintInput: &pb.MintInput{
				IssuerPublicKey:         r.Edges.Mint.IssuerPublicKey,
				IssuerProvidedTimestamp: r.Edges.Mint.WalletProvidedTimestamp,
			},
		}
	} else if len(r.Edges.SpentLeaf) > 0 {
		// This is a transfer transaction
		transferInput := &pb.TransferInput{
			LeavesToSpend: make([]*pb.TokenLeafToSpend, len(r.Edges.SpentLeaf)),
		}

		for i, leaf := range r.Edges.SpentLeaf {
			// Since we assume all relationships are loaded, we can directly access the created transaction receipt
			if leaf.Edges.LeafCreatedTokenTransactionReceipt == nil {
				return nil, fmt.Errorf("leaf created transaction receipt edge not loaded for leaf %s", leaf.ID)
			}

			transferInput.LeavesToSpend[i] = &pb.TokenLeafToSpend{
				PrevTokenTransactionHash:     leaf.Edges.LeafCreatedTokenTransactionReceipt.FinalizedTokenTransactionHash,
				PrevTokenTransactionLeafVout: uint32(leaf.LeafCreatedTransactionOutputVout),
			}
		}

		tokenTransaction.TokenInput = &pb.TokenTransaction_TransferInput{
			TransferInput: transferInput,
		}
	}

	return tokenTransaction, nil
}
