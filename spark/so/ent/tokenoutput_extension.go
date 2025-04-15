package ent

import (
	"context"
	"encoding/hex"
	"fmt"
	"log"
	"math/big"

	"github.com/google/uuid"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	"github.com/lightsparkdev/spark-go/so/ent/schema"
	"github.com/lightsparkdev/spark-go/so/ent/tokenoutput"
	"github.com/lightsparkdev/spark-go/so/ent/tokentransaction"
)

// FetchInputLeaves fetches the transaction receipts whose token transaction hashes
// match the PrevTokenTransactionHash of each leafToSpend, then loads the created leaves for those receipts,
// and finally maps each input leaf to the created leaf using PrevTokenTransactionLeafVout.
// Return the leaves in the same order they were specified in the input leaf object.
func FetchInputLeaves(ctx context.Context, leavesToSpend []*pb.TokenLeafToSpend) ([]*TokenOutput, error) {
	// Gather all distinct prev transaction hashes
	var distinctTxHashes [][]byte
	txHashMap := make(map[string]bool)
	for _, leaf := range leavesToSpend {
		if leaf.PrevTokenTransactionHash != nil {
			txHashMap[string(leaf.PrevTokenTransactionHash)] = true
		}
	}
	for hashStr := range txHashMap {
		distinctTxHashes = append(distinctTxHashes, []byte(hashStr))
	}

	// Query for receipts whose finalized hash matches any of the prev tx hashes
	receipts, err := GetDbFromContext(ctx).TokenTransaction.Query().
		Where(tokentransaction.FinalizedTokenTransactionHashIn(distinctTxHashes...)).
		WithCreatedOutput().
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch matching transaction receipt and leaves: %w", err)
	}

	receiptMap, err := GetReceiptMapFromList(receipts)
	if err != nil {
		return nil, fmt.Errorf("failed to create receipt map: %w", err)
	}

	// For each leafToSpend, find a matching created leaf based on its prev transaction and prev vout fields.
	leafToSpendEnts := make([]*TokenOutput, len(leavesToSpend))
	for i, leaf := range leavesToSpend {
		hashKey := hex.EncodeToString(leaf.PrevTokenTransactionHash)
		receipt, ok := receiptMap[hashKey]
		if !ok {
			return nil, fmt.Errorf("no receipt found for prev tx hash %x", leaf.PrevTokenTransactionHash)
		}

		var foundLeaf *TokenOutput
		for _, createdLeaf := range receipt.Edges.CreatedOutput {
			if createdLeaf.CreatedTransactionOutputVout == int32(leaf.PrevTokenTransactionLeafVout) {
				foundLeaf = createdLeaf
				break
			}
		}
		if foundLeaf == nil {
			return nil, fmt.Errorf("no created leaf found for prev tx hash %x and vout %d",
				leaf.PrevTokenTransactionHash,
				leaf.PrevTokenTransactionLeafVout)
		}

		leafToSpendEnts[i] = foundLeaf
	}

	return leafToSpendEnts, nil
}

// UpdateTokenLeavesToSpend updates the status of the leaves to be spent to spent unsigned which means the owner has provided
// a valid signature but the operator has not yet signed off on the transaction.
func MarkLeavesAsSpent(ctx context.Context, leafToSpendEnts []*TokenOutput, leafSpentOwnershipSignatures [][]byte, leafSpentTokenTransaction *TokenTransaction) error {
	for leafIndex, leafToSpendEnt := range leafToSpendEnts {
		_, err := GetDbFromContext(ctx).TokenOutput.UpdateOne(leafToSpendEnt).
			SetStatus(schema.TokenOutputStatusSpentStarted).
			SetSpentOwnershipSignature(leafSpentOwnershipSignatures[leafIndex]).
			SetOutputSpentTokenTransactionID(leafSpentTokenTransaction.ID).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("failed to update spent leaf: %w", err)
		}
	}
	return nil
}

func UpdateLeafStatuses(ctx context.Context, leafEnts []*TokenOutput, status schema.TokenOutputStatus) error {
	leafIDs := make([]uuid.UUID, len(leafEnts))
	for i, leaf := range leafEnts {
		leafIDs[i] = leaf.ID
	}
	_, err := GetDbFromContext(ctx).TokenOutput.Update().
		Where(tokenoutput.IDIn(leafIDs...)).
		SetStatus(status).
		Save(ctx)
	if err != nil {
		log.Printf("Failed to bulk update leaf status: %v", err)
		return err
	}

	return nil
}

func GetOwnedLeaves(ctx context.Context, ownerPublicKeys [][]byte, tokenPublicKeys [][]byte) ([]*TokenOutput, error) {
	query := GetDbFromContext(ctx).TokenOutput.
		Query().
		Where(
			// Order matters here to leverage the index.
			tokenoutput.OwnerPublicKeyIn(ownerPublicKeys...),
			// A leaf is 'owned' as long as it has been fully created and a spending transaction
			// has not yet been signed by this SO (if a transaction with it has been started
			// and not yet signed it is still considered owned).
			tokenoutput.StatusIn(
				schema.TokenOutputStatusCreatedFinalized,
				schema.TokenOutputStatusSpentStarted,
			),
			tokenoutput.ConfirmedWithdrawBlockHashIsNil(),
		)
	// Only filter by tokenPublicKey if it's provided.
	if len(tokenPublicKeys) > 0 {
		query = query.Where(tokenoutput.TokenPublicKeyIn(tokenPublicKeys...))
	}
	query = query.
		WithOutputCreatedTokenTransaction()

	leaves, err := query.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to query owned leaves: %w", err)
	}

	return leaves, nil
}

func GetOwnedLeafTokenStats(ctx context.Context, ownerPublicKeys [][]byte, tokenPublicKey []byte) ([]string, *big.Int, error) {
	leaves, err := GetOwnedLeaves(ctx, ownerPublicKeys, [][]byte{tokenPublicKey})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to query owned leaf stats: %w", err)
	}

	// Collect leaf IDs and token amounts
	leafIDs := make([]string, len(leaves))
	totalAmount := new(big.Int)
	for i, leaf := range leaves {
		leafIDs[i] = leaf.ID.String()
		amount := new(big.Int).SetBytes(leaf.TokenAmount)
		totalAmount.Add(totalAmount, amount)
	}

	return leafIDs, totalAmount, nil
}
