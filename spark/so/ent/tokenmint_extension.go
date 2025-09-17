package ent

import (
	"context"
	"fmt"
	"math/big"

	"github.com/lightsparkdev/spark/common/keys"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/tokenmint"
	"github.com/lightsparkdev/spark/so/ent/tokentransaction"
)

// FetchSignedMintsForTokenWithOutputs fetches all token_mint entities for a specific token
// that have associated token_transaction entities with SIGNED status.
// It queries based on either token_identifier or issuer_public_key depending on what's specified in the token transaction.
// TODO DL-155: Optimize this query to reduce data fetch requirements.
func FetchSignedMintsForTokenWithOutputs(ctx context.Context, tokenTransaction *tokenpb.TokenTransaction) ([]*TokenMint, error) {
	db, err := GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}

	mintInput := tokenTransaction.GetMintInput()
	if mintInput == nil {
		return nil, fmt.Errorf("token transaction must have mint input")
	}

	query := db.TokenMint.Query().
		Where(tokenmint.HasTokenTransactionWith(
			tokentransaction.StatusEQ(st.TokenTransactionStatusSigned),
		)).
		WithTokenTransaction(func(q *TokenTransactionQuery) {
			// Needed to compute token amount in each mint.
			q.WithCreatedOutput()
		})

	// Query based on token identifier if present, otherwise use issuer public key
	if mintInput.GetTokenIdentifier() != nil {
		query = query.Where(tokenmint.TokenIdentifierEQ(mintInput.GetTokenIdentifier()))
	} else {
		issuerPubKey, err := keys.ParsePublicKey(mintInput.GetIssuerPublicKey())
		if err != nil {
			return nil, fmt.Errorf("failed to parse issuer public key: %w", err)
		}
		query = query.Where(tokenmint.IssuerPublicKeyEQ(issuerPubKey))
	}

	mints, err := query.All(ctx)
	if err != nil {
		return nil, err
	}

	return mints, nil
}

// CalculateCurrentMintedSupply calculates the total amount that has been minted for a token
// by summing all amounts from signed mint transactions.
func CalculateCurrentMintedSupply(ctx context.Context, tokenTransaction *tokenpb.TokenTransaction) (*big.Int, error) {
	signedMints, err := FetchSignedMintsForTokenWithOutputs(ctx, tokenTransaction)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch signed mints: %w", err)
	}

	totalMinted := new(big.Int)
	for _, mint := range signedMints {
		for _, tx := range mint.Edges.TokenTransaction {
			// Add the amounts from outputs of each signed mint transaction
			for _, output := range tx.Edges.CreatedOutput {
				amount := new(big.Int).SetBytes(output.TokenAmount)
				totalMinted.Add(totalMinted, amount)
			}
		}
	}

	return totalMinted, nil
}
