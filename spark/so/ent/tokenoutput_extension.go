package ent

import (
	"context"
	"fmt"
	"math/big"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so/ent/predicate"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/tokenoutput"
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
		WithWithdrawal().
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
	OwnerPublicKeys  []keys.Public
	IssuerPublicKeys []keys.Public
	TokenIdentifiers [][]byte
	Network          btcnetwork.Network
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

	ownedStatusPredicate := tokenoutput.StatusIn(
		st.TokenOutputStatusCreatedFinalized,
		st.TokenOutputStatusSpentStarted,
		st.TokenOutputStatusSpentSigned,
	)

	query := db.TokenOutput.
		Query().
		Where(
			tokenoutput.OwnerPublicKeyIn(params.OwnerPublicKeys...),
			ownedStatusPredicate,
			tokenoutput.Not(tokenoutput.HasWithdrawal()),
		).
		Where(tokenoutput.NetworkEQ(params.Network)).
		WithOutputSpentTokenTransaction()

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

// OwnedTokenOutputResult contains the results from GetOwnedTokenOutputRefs.
type OwnedTokenOutputResult struct {
	OutputRefs  []*tokenpb.TokenOutputRef
	TotalAmount *big.Int
}

// GetOwnedTokenOutputRefs returns token output references (outpoints) and total amount for owned outputs.
// Outpoints (transaction hash + vout) are deterministic and consistent across all SOs.
func GetOwnedTokenOutputRefs(ctx context.Context, ownerPublicKeys []keys.Public, tokenIdentifier []byte, network btcnetwork.Network) (*OwnedTokenOutputResult, error) {
	outputs, err := GetOwnedTokenOutputs(ctx, GetOwnedTokenOutputsParams{
		OwnerPublicKeys:  ownerPublicKeys,
		TokenIdentifiers: [][]byte{tokenIdentifier},
		Network:          network,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query owned output refs: %w", err)
	}

	outputRefs := make([]*tokenpb.TokenOutputRef, len(outputs))
	totalAmount := new(big.Int)
	for i, output := range outputs {
		outputRefs[i] = &tokenpb.TokenOutputRef{
			TransactionHash: output.CreatedTransactionFinalizedHash,
			Vout:            uint32(output.CreatedTransactionOutputVout),
		}
		amount := new(big.Int).SetBytes(output.TokenAmount)
		totalAmount.Add(totalAmount, amount)
	}

	return &OwnedTokenOutputResult{
		OutputRefs:  outputRefs,
		TotalAmount: totalAmount,
	}, nil
}
