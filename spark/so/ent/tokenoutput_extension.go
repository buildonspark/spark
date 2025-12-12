package ent

import (
	"context"
	"fmt"
	"math/big"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so/ent/predicate"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/tokenoutput"
	"github.com/lightsparkdev/spark/so/ent/tokentransaction"
)

// FetchAndLockTokenInputs fetches token outputs by their (tx_hash, vout) identifiers and locks them for update.
// Returns the outputs in the same order they were specified in the input.
func FetchAndLockTokenInputs(ctx context.Context, outputsToSpend []*tokenpb.TokenOutputToSpend) ([]*TokenOutput, error) {
	db, err := GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}

	// Build predicates for each (tx_hash, vout) pair using the denormalized field
	predicates := make([]predicate.TokenOutput, 0, len(outputsToSpend))
	for _, output := range outputsToSpend {
		if output.PrevTokenTransactionHash == nil {
			return nil, fmt.Errorf("prev token transaction hash is nil")
		}
		predicates = append(predicates, tokenoutput.And(
			tokenoutput.CreatedTransactionFinalizedHash(output.PrevTokenTransactionHash),
			tokenoutput.CreatedTransactionOutputVout(int32(output.PrevTokenTransactionVout)),
		))
	}

	// Query all outputs matching any of the (tx_hash, vout) pairs and lock them
	lockedOutputs, err := db.TokenOutput.Query().
		Where(tokenoutput.Or(predicates...)).
		WithOutputSpentTokenTransaction(func(q *TokenTransactionQuery) {
			q.ForUpdate()
		}).
		ForUpdate().
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch and lock outputs: %w", err)
	}

	// Build a map for quick lookup by (tx_hash, vout)
	type outputKey struct {
		txHash string
		vout   int32
	}
	outputMap := make(map[outputKey]*TokenOutput, len(lockedOutputs))
	for _, output := range lockedOutputs {
		key := outputKey{
			txHash: string(output.CreatedTransactionFinalizedHash),
			vout:   output.CreatedTransactionOutputVout,
		}
		outputMap[key] = output
	}

	// Return outputs in the same order as the input
	result := make([]*TokenOutput, len(outputsToSpend))
	for i, output := range outputsToSpend {
		key := outputKey{
			txHash: string(output.PrevTokenTransactionHash),
			vout:   int32(output.PrevTokenTransactionVout),
		}
		lockedOutput, ok := outputMap[key]
		if !ok {
			return nil, fmt.Errorf("no output found for prev tx hash %x and vout %d",
				output.PrevTokenTransactionHash,
				output.PrevTokenTransactionVout)
		}
		result[i] = lockedOutput
	}

	return result, nil
}

// GetOwnedTokenOutputsParams holds the parameters for GetOwnedTokenOutputs
type GetOwnedTokenOutputsParams struct {
	OwnerPublicKeys            []keys.Public
	IssuerPublicKeys           []keys.Public
	TokenIdentifiers           [][]byte
	IncludeExpiredTransactions bool
	Network                    btcnetwork.Network
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
		Where(tokenoutput.NetworkEQ(params.Network))
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

func GetOwnedTokenOutputStats(ctx context.Context, ownerPublicKeys []keys.Public, tokenIdentifier []byte, network btcnetwork.Network) ([]string, *big.Int, error) {
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
