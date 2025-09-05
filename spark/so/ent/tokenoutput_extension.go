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

	"github.com/lightsparkdev/spark/common"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/tokenoutput"
	"github.com/lightsparkdev/spark/so/ent/tokentransaction"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
)

// FetchAndLockTokenInputs fetches the transaction whose token transaction hashes
// match the PrevTokenTransactionHash of each output, then loads the created outputs for those transactions,
// and finally maps each input to the created output in the DB.
// Return the TTXOs in the same order they were specified in the input object.
func FetchAndLockTokenInputs(ctx context.Context, outputsToSpend []*tokenpb.TokenOutputToSpend) ([]*TokenOutput, error) {
	db, err := GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}

	// Group inputs by prev transaction hash, collecting vouts per hash to leverage
	// CreatedTransactionOutputVoutIn(..) with the OutputCreatedTokenTransaction index.
	if len(outputsToSpend) == 0 {
		return []*TokenOutput{}, nil
	}

	voutsByHash := make(map[string][]int32)
	hashBytesByKey := make(map[string][]byte)
	for _, o := range outputsToSpend {
		if o == nil || o.PrevTokenTransactionHash == nil {
			return nil, sparkerrors.NotFoundErrorf("invalid output to spend: missing previous transaction hash")
		}
		key := string(o.PrevTokenTransactionHash)
		voutsByHash[key] = append(voutsByHash[key], int32(o.PrevTokenTransactionVout))
		if _, ok := hashBytesByKey[key]; !ok {
			hashBytesByKey[key] = o.PrevTokenTransactionHash
		}
	}

	groupedPredicates := make([]predicate.TokenOutput, 0, len(voutsByHash))
	for key, vouts := range voutsByHash {
		txHash := hashBytesByKey[key]
		groupedPredicates = append(groupedPredicates,
			tokenoutput.And(
				tokenoutput.CreatedTransactionOutputVoutIn(vouts...),
				tokenoutput.HasOutputCreatedTokenTransactionWith(
					tokentransaction.FinalizedTokenTransactionHash(txHash),
				),
			),
		)
	}

	// Query and lock the matching outputs. Also load the spent-transaction edge needed by downstream checks,
	// and the created-transaction edge so we can map results back to (hash,vout).
	lockedOutputs, err := db.TokenOutput.Query().
		Where(tokenoutput.Or(groupedPredicates...)).
		WithOutputCreatedTokenTransaction().
		WithOutputSpentTokenTransaction().
		ForUpdate().
		All(ctx)
	if err != nil {
		return nil, sparkerrors.InternalErrorf("failed to lock outputs for update: %w", err)
	}

	// Build index by (finalized_hash, vout)
	byHashVout := make(map[string]*TokenOutput, len(lockedOutputs))
	for _, out := range lockedOutputs {
		if out.Edges.OutputCreatedTokenTransaction == nil || len(out.Edges.OutputCreatedTokenTransaction.FinalizedTokenTransactionHash) == 0 {
			return nil, sparkerrors.NotFoundErrorf("locked output missing created transaction edge or hash: %s", out.ID)
		}
		key := fmt.Sprintf("%s:%d", hex.EncodeToString(out.Edges.OutputCreatedTokenTransaction.FinalizedTokenTransactionHash), out.CreatedTransactionOutputVout)
		byHashVout[key] = out
	}

	// Return outputs in the same order as inputs
	result := make([]*TokenOutput, len(outputsToSpend))
	for i, o := range outputsToSpend {
		key := fmt.Sprintf("%s:%d", hex.EncodeToString(o.PrevTokenTransactionHash), int32(o.PrevTokenTransactionVout))
		out, ok := byHashVout[key]
		if !ok {
			return nil, sparkerrors.NotFoundErrorf("no created output found for prev tx hash %x and vout %d", o.PrevTokenTransactionHash, o.PrevTokenTransactionVout)
		}
		result[i] = out
	}

	return result, nil
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

	// TODO: Remove limit once we have a way to paginate the results
	outputs, err := query.Limit(500).WithOutputCreatedTokenTransaction().All(ctx)
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
