package ent

import (
	"context"
	"encoding/hex"
	"fmt"
	"log"

	"github.com/google/uuid"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
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
			Save(ctx)
		if err != nil {
			log.Printf("Failed to create token mint ent: %v", err)
			return nil, err
		}
	}

	txReceiptUpdate := db.TokenTransactionReceipt.Create().
		SetPartialTokenTransactionHash(partialTokenTransactionHash).
		SetFinalizedTokenTransactionHash(finalTokenTransactionHash)
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
				SetLeafSpentTransactionInputVout(uint32(leafIndex)).
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
				SetWithdrawalBondSats(outputLeaf.WithdrawalBondSats).
				SetWithdrawalLocktime(outputLeaf.WithdrawalLocktime).
				SetWithdrawalRevocationPublicKey(outputLeaf.RevocationPublicKey).
				SetTokenPublicKey(outputLeaf.TokenPublicKey).
				SetTokenAmount(outputLeaf.TokenAmount).
				SetLeafCreatedTransactionOutputVout(uint32(leafIndex)).
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

// UpdateSignedTransactionLeaves updates the status and ownership signatures of the input + output leaves
// and the issuer signature (if applicable).
func UpdateSignedTransactionLeaves(
	ctx context.Context,
	tokenTransactionReceipt *TokenTransactionReceipt,
	operatorSpecificOwnershipSignatures [][]byte,
	operatorSignature []byte,
) error {
	// Update the token transaction receipt with the operator signature
	_, err := GetDbFromContext(ctx).TokenTransactionReceipt.UpdateOne(tokenTransactionReceipt).
		SetOperatorSignature(operatorSignature).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to update token transaction receipt with operator signature: %w", err)
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

// UpdateFinalizedTransactionInputs updates the status and ownership signatures of the finalized input + output
// leaves.
func UpdateFinalizedTransactionLeaves(
	ctx context.Context,
	tokenTransactionReceipt *TokenTransactionReceipt,
	revocationKeys [][]byte,
) error {
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
	_, err := GetDbFromContext(ctx).TokenLeaf.Update().
		Where(tokenleaf.IDIn(leafIDs...)).
		SetStatus(schema.TokenLeafStatusCreatedFinalized).
		Save(ctx)
	if err != nil {
		log.Printf("Failed to bulk update leaf status to signed: %v", err)
		return err
	}
	return nil
}

// FetchTokenTransactionReceipt refetches the receipt with all its relations.
func FetchTokenTransactionData(ctx context.Context, finalTokenTransactionHash []byte) (*TokenTransactionReceipt, error) {
	return GetDbFromContext(ctx).TokenTransactionReceipt.Query().
		Where(tokentransactionreceipt.FinalizedTokenTransactionHash(finalTokenTransactionHash)).
		WithCreatedLeaf().
		WithSpentLeaf().
		WithMint().
		Only(ctx)
}
