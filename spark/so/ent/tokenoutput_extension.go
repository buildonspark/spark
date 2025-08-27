package ent

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"time"

	"github.com/lightsparkdev/spark/common/keys"

	"github.com/lightsparkdev/spark/so/ent/predicate"

	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"

	"github.com/google/uuid"
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

	// Query for transactions whose finalized hash matches any of the prev tx hashes
	db, err := GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}

	transactions, err := db.TokenTransaction.Query().
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
	lockedOutputs, err := db.TokenOutput.Query().
		Where(tokenoutput.IDIn(outputIDs...)).
		WithOutputSpentTokenTransaction().
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

// GetOwnedTokenOutputsParams holds the parameters for GetOwnedTokenOutputs
type GetOwnedTokenOutputsParams struct {
	OwnerPublicKeys            []keys.Public
	IssuerPublicKeys           []keys.Public
	TokenIdentifiers           [][]byte
	IncludeExpiredTransactions bool
	Network                    common.Network
}

func GetOwnedTokenOutputs(ctx context.Context, params GetOwnedTokenOutputsParams) ([]*TokenOutput, error) {
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

	ownerPubKeyBytes := make([][]byte, len(params.OwnerPublicKeys))
	for i, pk := range params.OwnerPublicKeys {
		ownerPubKeyBytes[i] = pk.Serialize()
	}
	query := db.TokenOutput.
		Query().
		Where(
			// Order matters here to leverage the index.
			tokenoutput.OwnerPublicKeyIn(ownerPubKeyBytes...),
			// A output is 'owned' as long as it has been fully created and a spending transaction
			// has not yet been signed by this SO (if a transaction with it has been started
			// and not yet signed it is still considered owned).
			statusPredicate,
			tokenoutput.ConfirmedWithdrawBlockHashIsNil(),
		).
		Where(tokenoutput.NetworkEQ(schemaNetwork))
	if len(params.IssuerPublicKeys) > 0 {
		issuerPubKeyBytes := make([][]byte, len(params.IssuerPublicKeys))
		for i, pk := range params.IssuerPublicKeys {
			issuerPubKeyBytes[i] = pk.Serialize()
		}
		query = query.Where(tokenoutput.TokenPublicKeyIn(issuerPubKeyBytes...))
	}
	if len(params.TokenIdentifiers) > 0 {
		query = query.Where(tokenoutput.TokenIdentifierIn(params.TokenIdentifiers...))
	}

	outputs, err := query.WithOutputCreatedTokenTransaction().All(ctx)
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
