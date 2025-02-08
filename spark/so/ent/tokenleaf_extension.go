package ent

import (
	"context"
	"encoding/hex"
	"fmt"

	pb "github.com/lightsparkdev/spark-go/proto/spark"
	"github.com/lightsparkdev/spark-go/so/ent/tokentransactionreceipt"
)

// FetchTokenLeavesFromLeavesToSpend fetches the transaction receipts whose token transaction hashes
// match the PrevTokenTransactionHash of each leafToSpend, then loads the created leaves for those receipts,
// and finally maps each input leaf to the created leaf using PrevTokenTransactionLeafVout.
// Return the leaves in the same order they were specified in the input leaf object.
func FetchTokenLeavesFromLeavesToSpend(ctx context.Context, leavesToSpend []*pb.TokenLeafToSpend) ([]*TokenLeaf, error) {
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
	receipts, err := GetDbFromContext(ctx).TokenTransactionReceipt.Query().
		Where(tokentransactionreceipt.FinalizedTokenTransactionHashIn(distinctTxHashes...)).
		WithCreatedLeaf().
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch matching transaction receipt and leaves: %w", err)
	}

	receiptMap, err := GetReceiptMapFromList(receipts)
	if err != nil {
		return nil, fmt.Errorf("failed to create receipt map: %w", err)
	}

	// For each leafToSpend, find a matching created leaf based on its prev transaction and prev vout fields.
	leafToSpendEnts := make([]*TokenLeaf, len(leavesToSpend))
	for i, leaf := range leavesToSpend {
		hashKey := hex.EncodeToString(leaf.PrevTokenTransactionHash)
		receipt, ok := receiptMap[hashKey]
		if !ok {
			return nil, fmt.Errorf("no receipt found for prev tx hash %x", leaf.PrevTokenTransactionHash)
		}

		var foundLeaf *TokenLeaf
		for _, createdLeaf := range receipt.Edges.CreatedLeaf {
			if createdLeaf.LeafCreatedTransactionOuputVout == leaf.PrevTokenTransactionLeafVout {
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
