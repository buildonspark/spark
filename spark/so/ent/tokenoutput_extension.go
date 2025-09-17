package ent

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"

	"github.com/lightsparkdev/spark/so/ent/predicate"

	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"

	"github.com/lightsparkdev/spark/common"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/tokenoutput"
	"github.com/lightsparkdev/spark/so/ent/tokentransaction"
)

// FetchAndLockTokenInputs fetches the transaction whose token transaction hashes
// match the PrevTokenTransactionHash of each output, then loads the created outputs for those transactions,
// and finally maps each input to the created output in the DB.
// Return the TTXOs in the same order they were specified in the input object.
func FetchAndLockTokenInputs(ctx context.Context, outputsToSpend []*tokenpb.TokenOutputToSpend) ([]*TokenOutput, error) {
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

	db, err := GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}

	transactions, err := db.TokenTransaction.Query().
		Where(tokentransaction.FinalizedTokenTransactionHashIn(distinctTxHashes...)).
		WithCreatedOutput().
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

	lockedOutputs, err := db.TokenOutput.Query().
		Where(tokenoutput.IDIn(outputIDs...)).
		WithOutputSpentTokenTransaction(
			func(q *TokenTransactionQuery) {
				q.ForUpdate()
			},
		).
		ForUpdate().
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to lock outputs for update: %w", err)
	}

	if len(lockedOutputs) != len(outputToSpendEnts) {
		return nil, fmt.Errorf("failed to lock all outputs: expected %d, got %d", len(outputToSpendEnts), len(lockedOutputs))
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

		if err := validateTokenOutputIntegrity(output, lockedOutput); err != nil {
			return nil, err
		}

		// Replace unlocked outputs with locked outputs.
		outputToSpendEnts[i] = lockedOutput
	}

	return outputToSpendEnts, nil
}

// validateTokenOutputIntegrity validates that no critical fields changed between the original fetch and the locked version.
func validateTokenOutputIntegrity(original, locked *TokenOutput) error {
	if locked.Status != original.Status {
		return fmt.Errorf("output status changed between fetching and locking prior to spending for ID %s (original: %v, locked: %v)", original.ID, original.Status, locked.Status)
	}

	originalSpentTx := original.Edges.OutputSpentTokenTransaction
	lockedSpentTx := locked.Edges.OutputSpentTokenTransaction

	if originalSpentTx != nil && lockedSpentTx != nil {
		if !bytes.Equal(originalSpentTx.FinalizedTokenTransactionHash, lockedSpentTx.FinalizedTokenTransactionHash) {
			return fmt.Errorf("output assigned to different transaction hash between fetching and locking for ID %s", original.ID)
		}
	}

	return nil
}

// GetOwnedTokenOutputsParams holds the parameters for GetOwnedTokenOutputs
type GetOwnedTokenOutputsParams struct {
	OwnerPublicKeys            []keys.Public
	IssuerPublicKeys           []keys.Public
	TokenIdentifiers           [][]byte
	IncludeExpiredTransactions bool
	Network                    common.Network
	// Pagination parameters.
	// For forward pagination: If AfterID is provided, results will include items with ID greater than AfterID.
	// For backward pagination: If BeforeID is provided, results will include items with ID less than BeforeID.
	// AfterID and BeforeID are mutually exclusive.
	// Limit controls the maximum number of items returned. If zero, defaults to 500 for legacy behavior.
	AfterID  *uuid.UUID
	BeforeID *uuid.UUID
	Limit    int
}

func GetOwnedTokenOutputs(ctx context.Context, params GetOwnedTokenOutputsParams) ([]*TokenOutput, error) {
	// Validate pagination parameters
	if params.AfterID != nil && params.BeforeID != nil {
		return nil, fmt.Errorf("AfterID and BeforeID are mutually exclusive")
	}

	schemaNetwork, err := common.SchemaNetworkFromNetwork(params.Network)
	if err != nil {
		return nil, fmt.Errorf("failed to convert proto network to schema network: %w", err)
	}

	db, err := GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}

	var statusPredicate predicate.TokenOutput

	ownedStatusPredicate := tokenoutput.StatusIn(
		st.TokenOutputStatusCreatedFinalized,
		st.TokenOutputStatusSpentStarted,
	)

	if params.IncludeExpiredTransactions {
		// Additionally include outputs whose spending transaction has been signed but has
		// expired. (SPENT_SIGNED + expired TX)
		statusPredicate = tokenoutput.Or(
			ownedStatusPredicate,
			tokenoutput.And(
				tokenoutput.StatusEQ(st.TokenOutputStatusSpentSigned),
				tokenoutput.HasOutputSpentTokenTransactionWith(
					tokentransaction.And(
						tokentransaction.ExpiryTimeLT(time.Now()),
						tokentransaction.StatusIn(st.TokenTransactionStatusStarted, st.TokenTransactionStatusSigned),
					),
				),
			),
		)
	} else {
		statusPredicate = ownedStatusPredicate
	}

	query := db.TokenOutput.
		Query().
		Where(
			// Order matters here to leverage the index.
			tokenoutput.OwnerPublicKeyIn(params.OwnerPublicKeys...),
			// A output is 'owned' as long as it has been fully created and a spending transaction
			// has not yet been signed by this SO (if a transaction with it has been started
			// and not yet signed it is still considered owned).
			statusPredicate,
			tokenoutput.ConfirmedWithdrawBlockHashIsNil(),
		).
		Where(tokenoutput.NetworkEQ(schemaNetwork))
	if len(params.IssuerPublicKeys) > 0 {
		query = query.Where(tokenoutput.TokenPublicKeyIn(params.IssuerPublicKeys...))
	}
	if len(params.TokenIdentifiers) > 0 {
		query = query.Where(tokenoutput.TokenIdentifierIn(params.TokenIdentifiers...))
	}

	// Check for unsupported backward pagination
	if params.BeforeID != nil {
		return nil, fmt.Errorf("backward pagination with 'before' cursor is not currently supported")
	}

	// Forward pagination: standard ascending order
	query = query.Order(tokenoutput.ByID())
	if params.AfterID != nil {
		query = query.Where(tokenoutput.IDGT(*params.AfterID))
	}

	outputs, err := query.Limit(params.Limit).WithOutputCreatedTokenTransaction().All(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to query owned outputs: %w", err)
	}

	return outputs, nil
}

func GetOwnedTokenOutputStats(ctx context.Context, ownerPublicKeys []keys.Public, tokenIdentifier []byte, network common.Network) ([]string, *big.Int, error) {
	outputs, err := GetOwnedTokenOutputs(ctx, GetOwnedTokenOutputsParams{
		OwnerPublicKeys:            ownerPublicKeys,
		TokenIdentifiers:           [][]byte{tokenIdentifier},
		IncludeExpiredTransactions: false,
		Network:                    network,
	})
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
