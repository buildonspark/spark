package ent

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"

	"github.com/google/uuid"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/ent/schema"
	"github.com/lightsparkdev/spark/so/ent/tokenoutput"
	"github.com/lightsparkdev/spark/so/ent/tokentransaction"
)

// FetchTokenInputs fetches the transaction whose token transaction hashes
// match the PrevTokenTransactionHash of each output, then loads the created outputs for those transactions,
// and finally maps each input to the created output in the DB.
// Return the TTXOs in the same order they were specified in the input object.
func FetchAndLockTokenInputs(ctx context.Context, outputsToSpend []*pb.TokenOutputToSpend) ([]*TokenOutput, error) {
	// Gather all distinct prev transaction hashes
	var distinctTxHashes [][]byte
	txHashMap := make(map[string]bool)
	for _, output := range outputsToSpend {
		if output.PrevTokenTransactionHash != nil {
			txHashMap[string(output.PrevTokenTransactionHash)] = true
		}
	}
	for hashStr := range txHashMap {
		distinctTxHashes = append(distinctTxHashes, []byte(hashStr))
	}

	// Query for transactions whose finalized hash matches any of the prev tx hashes
	transactions, err := GetDbFromContext(ctx).TokenTransaction.Query().
		Where(tokentransaction.FinalizedTokenTransactionHashIn(distinctTxHashes...)).
		WithCreatedOutput().
		ForUpdate().
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch matching transaction and outputs: %w", err)
	}

	transaction, err := GetTokenTransactionMapFromList(transactions)
	if err != nil {
		return nil, fmt.Errorf("failed to create transaction map: %w", err)
	}

	// For each outputToSpend, find a matching created output based on its prev transaction and prev vout fields.
	outputToSpendEnts := make([]*TokenOutput, len(outputsToSpend))
	for i, output := range outputsToSpend {
		hashKey := hex.EncodeToString(output.PrevTokenTransactionHash)
		transaction, ok := transaction[hashKey]
		if !ok {
			return nil, fmt.Errorf("no transaction found for prev tx hash %x", output.PrevTokenTransactionHash)
		}

		var foundOutput *TokenOutput
		for _, createdOutput := range transaction.Edges.CreatedOutput {
			if createdOutput.CreatedTransactionOutputVout == int32(output.PrevTokenTransactionVout) {
				foundOutput = createdOutput
				break
			}
		}
		if foundOutput == nil {
			return nil, fmt.Errorf("no created output found for prev tx hash %x and vout %d",
				output.PrevTokenTransactionHash,
				output.PrevTokenTransactionVout)
		}

		outputToSpendEnts[i] = foundOutput
	}

	outputIDs := make([]uuid.UUID, len(outputToSpendEnts))
	for i, output := range outputToSpendEnts {
		outputIDs[i] = output.ID
	}

	// Lock the outputs for update to prevent concurrent spending.  This refetch is necessary because
	// the above query on the token transactions table is not capable of locking the outputs during the join
	// conducted in the initial query via `WithCreatedOutput()`.
	lockedOutputs, err := GetDbFromContext(ctx).TokenOutput.Query().
		Where(tokenoutput.IDIn(outputIDs...)).
		ForUpdate().
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to lock outputs for update: %w", err)
	}

	lockedOutputMap := make(map[uuid.UUID]*TokenOutput)
	for _, output := range lockedOutputs {
		lockedOutputMap[output.ID] = output
	}

	for i, output := range outputToSpendEnts {
		lockedOutput, ok := lockedOutputMap[output.ID]
		if !ok {
			return nil, fmt.Errorf("unable to lock output prior to spending for ID %s", output.ID)
		}

		if lockedOutput.Status != output.Status {
			return nil, fmt.Errorf("output state changed between fetching and locking prior to spending for ID %s", output.ID)
		}

		// Replace unlocked outputs with locked outputs.
		outputToSpendEnts[i] = lockedOutput
	}

	return outputToSpendEnts, nil
}

func GetOwnedTokenOutputs(ctx context.Context, ownerPublicKeys [][]byte, tokenPublicKeys [][]byte) ([]*TokenOutput, error) {
	query := GetDbFromContext(ctx).TokenOutput.
		Query().
		Where(
			// Order matters here to leverage the index.
			tokenoutput.OwnerPublicKeyIn(ownerPublicKeys...),
			// A output is 'owned' as long as it has been fully created and a spending transaction
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

	outputs, err := query.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to query owned outputs: %w", err)
	}

	return outputs, nil
}

func GetOwnedTokenOutputStats(ctx context.Context, ownerPublicKeys [][]byte, tokenPublicKey []byte) ([]string, *big.Int, error) {
	outputs, err := GetOwnedTokenOutputs(ctx, ownerPublicKeys, [][]byte{tokenPublicKey})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to query owned output stats: %w", err)
	}

	// Collect output IDs and token amounts
	outputIDs := make([]string, len(outputs))
	totalAmount := new(big.Int)
	for i, output := range outputs {
		outputIDs[i] = output.ID.String()
		amount := new(big.Int).SetBytes(output.TokenAmount)
		totalAmount.Add(totalAmount, amount)
	}

	return outputIDs, totalAmount, nil
}
