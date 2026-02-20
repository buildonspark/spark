package tokens

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"entgo.io/ent/dialect"
	esql "entgo.io/ent/dialect/sql"
	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/common/uint128"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/tokencreate"
	"github.com/lightsparkdev/spark/so/ent/tokenoutput"
	"github.com/lightsparkdev/spark/so/ent/tokentransaction"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
)

// ValidateMintDoesNotExceedMaxSupply validates that a mint transaction doesn't exceed the token's max supply.
// This validation is shared between the prepare and sign handlers.
func ValidateMintDoesNotExceedMaxSupply(ctx context.Context, tokenTransaction *tokenpb.TokenTransaction) error {
	mintAmount := new(big.Int)
	for _, output := range tokenTransaction.GetTokenOutputs() {
		amount := new(big.Int).SetBytes(output.GetTokenAmount())
		mintAmount.Add(mintAmount, amount)
	}

	var tokenIdentifier []byte
	if tokenTransaction.GetMintInput() != nil {
		tokenIdentifier = tokenTransaction.GetMintInput().GetTokenIdentifier()
	}
	if len(tokenIdentifier) == 0 {
		return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("token_identifier is required on MintInput for max supply validation"))
	}

	return validateMintAgainstMaxSupplyCore(ctx, mintAmount, tokenIdentifier)
}

// ValidateMintDoesNotExceedMaxSupplyEnt validates that a mint transaction doesn't exceed the token's max supply.
// This is a more efficient version that works with Ent entities directly without proto conversion.
func ValidateMintDoesNotExceedMaxSupplyEnt(ctx context.Context, tokenTransaction *ent.TokenTransaction) error {
	mintAmount := new(big.Int)
	for _, output := range tokenTransaction.Edges.CreatedOutput {
		amount := new(big.Int).SetBytes(output.TokenAmount)
		mintAmount.Add(mintAmount, amount)
	}

	if tokenTransaction.Edges.Mint == nil {
		return sparkerrors.InternalDatabaseMissingEdge(fmt.Errorf("cannot verify max supply for mint transaction because no mint input was found"))
	}
	tokenIdentifier := tokenTransaction.Edges.Mint.TokenIdentifier
	if len(tokenIdentifier) == 0 {
		return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("token_identifier is required on mint for max supply validation"))
	}

	return validateMintAgainstMaxSupplyCore(ctx, mintAmount, tokenIdentifier)
}

// validateMintAgainstMaxSupplyCore contains the core validation logic that both proto and Ent versions can use.
func validateMintAgainstMaxSupplyCore(ctx context.Context, mintAmount *big.Int, tokenIdentifier []byte) error {
	logger := logging.GetLoggerFromContext(ctx)
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return sparkerrors.InternalDatabaseReadError(fmt.Errorf("failed to get or create current tx for request: %w", err))
	}

	identifierInfo := fmt.Sprintf("token identifier: %x", tokenIdentifier)
	tokenCreate, err := db.TokenCreate.Query().
		Where(tokencreate.TokenIdentifierEQ(tokenIdentifier)).
		ForUpdate().
		First(ctx)
	if ent.IsNotFound(err) {
		logger.Info(fmt.Sprintf("Token metadata not found - minting not allowed for %s", identifierInfo))
		return sparkerrors.NotFoundMissingEntity(fmt.Errorf("minting not allowed because a created token was not found for %s", identifierInfo))
	}
	if err != nil {
		return sparkerrors.InternalDatabaseReadError(fmt.Errorf("failed to get token metadata for %s: %w", identifierInfo, err))
	}

	maxSupply := new(big.Int).SetBytes(tokenCreate.MaxSupply)
	if maxSupply.Cmp(big.NewInt(0)) == 0 {
		return nil
	}

	currentSupply, err := calculateCurrentSupplyByTokenIdentifier(ctx, tokenIdentifier)
	if err != nil {
		return sparkerrors.WrapErrorWithMessage(err, "failed to calculate current minted supply")
	}

	newTotalSupply := new(big.Int).Add(currentSupply, mintAmount)
	if newTotalSupply.Cmp(maxSupply) > 0 {
		return sparkerrors.FailedPreconditionTokenRulesViolation(fmt.Errorf("mint would exceed max supply: total supply after mint (%s) would exceed max supply (%s)",
			newTotalSupply.String(), maxSupply.String()))
	}

	return nil
}

// calculateCurrentSupplyByTokenIdentifier calculates the current minted supply for a token by token identifier.
func calculateCurrentSupplyByTokenIdentifier(ctx context.Context, tokenIdentifier []byte) (*big.Int, error) {
	return calculateCurrentSupply(ctx, func(q *ent.TokenOutputQuery) *ent.TokenOutputQuery {
		return q.Where(tokenoutput.TokenIdentifierEQ(tokenIdentifier))
	})
}

// calculateCurrentSupply is a helper function that executes the common query logic.
// It counts outputs from FINALIZED mint transactions and non-expired SIGNED mint transactions.
func calculateCurrentSupply(ctx context.Context, whereClause func(*ent.TokenOutputQuery) *ent.TokenOutputQuery) (*big.Int, error) {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, sparkerrors.InternalDatabaseReadError(fmt.Errorf("failed to get or create current tx for request: %w", err))
	}

	// Count outputs from both FINALIZED and SIGNED mint transactions.
	// We include SIGNED because v3 mint transactions may be stuck in SIGNED status
	// until a backfill task updates them to FINALIZED.
	// Expiry checks will be added in a future PR after backfill completes.
	mintTransactionPredicate := tokentransaction.And(
		tokentransaction.HasMint(),
		tokentransaction.Or(
			tokentransaction.StatusEQ(st.TokenTransactionStatusFinalized),
			tokentransaction.StatusEQ(st.TokenTransactionStatusSigned),
		),
	)

	var (
		rows []struct {
			Sum string `json:"sum_amount"`
		}
		qErr error
	)
	baseQuery := whereClause(db.TokenOutput.Query()).
		Where(tokenoutput.HasOutputCreatedTokenTransactionWith(mintTransactionPredicate))
	err = baseQuery.Modify(func(s *esql.Selector) {
		switch s.Dialect() {
		case dialect.Postgres:
			s.SelectExpr(esql.Expr("CAST(COALESCE(SUM(amount), 0) AS TEXT) AS sum_amount")).Limit(1)
		case dialect.SQLite:
			s.SelectExpr(esql.Expr("CAST(COALESCE(SUM(CAST(amount AS NUMERIC)), 0) AS TEXT) AS sum_amount")).Limit(1)
		default:
			qErr = fmt.Errorf("unsupported dialect: %s", s.Dialect())
		}
	}).Scan(ctx, &rows)
	if err = errors.Join(err, qErr); err != nil {
		return nil, sparkerrors.InternalDatabaseReadError(fmt.Errorf("failed to fetch mint outputs: %w", err))
	}
	total := uint128.New()
	if len(rows) > 0 {
		if err := total.Scan(rows[0].Sum); err != nil {
			return nil, err
		}
	}
	return total.BigInt(), nil
}
