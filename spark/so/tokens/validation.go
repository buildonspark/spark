package tokens

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"entgo.io/ent/dialect"
	esql "entgo.io/ent/dialect/sql"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/common/uint128"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/tokencreate"
	"github.com/lightsparkdev/spark/so/ent/tokenoutput"
	"github.com/lightsparkdev/spark/so/ent/tokentransaction"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/knobs"
)

// ValidateMintDoesNotExceedMaxSupply validates that a mint transaction doesn't exceed the token's max supply.
// This validation is shared between the prepare and sign handlers.
func ValidateMintDoesNotExceedMaxSupply(ctx context.Context, tokenTransaction *tokenpb.TokenTransaction) error {
	mintAmount := new(big.Int)
	for _, output := range tokenTransaction.GetTokenOutputs() {
		amount := new(big.Int).SetBytes(output.GetTokenAmount())
		mintAmount.Add(mintAmount, amount)
	}

	// Extract token identification from proto transaction
	var tokenIdentifier []byte
	var issuerPublicKey keys.Public

	if tokenTransaction.GetMintInput() != nil {
		tokenIdentifier = tokenTransaction.GetMintInput().GetTokenIdentifier()
		mintPublicKey, err := keys.ParsePublicKey(tokenTransaction.GetMintInput().GetIssuerPublicKey())
		if err != nil {
			return sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("failed to get issuer public key: %w", err))
		}
		issuerPublicKey = mintPublicKey
	} else if len(tokenTransaction.GetTokenOutputs()) > 0 {
		output := tokenTransaction.GetTokenOutputs()[0]
		tokenIdentifier = output.GetTokenIdentifier()
		tokenPublicKey, err := keys.ParsePublicKey(output.GetTokenPublicKey())
		if err != nil {
			return sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("failed to get token public key: %w", err))
		}
		issuerPublicKey = tokenPublicKey
	}

	commonNetwork, err := common.NetworkFromProtoNetwork(tokenTransaction.Network)
	if err != nil {
		return err
	}
	schemaNetwork, err := common.SchemaNetworkFromNetwork(commonNetwork)
	if err != nil {
		return err
	}

	return validateMintAgainstMaxSupplyCore(ctx, mintAmount, tokenIdentifier, issuerPublicKey, schemaNetwork)
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
	issuerPublicKey := tokenTransaction.Edges.Mint.IssuerPublicKey

	if len(tokenTransaction.Edges.CreatedOutput) == 0 {
		return sparkerrors.InternalDatabaseMissingEdge(fmt.Errorf("cannot determine network for mint transaction because no outputs were found"))
	}
	network := tokenTransaction.Edges.CreatedOutput[0].Network

	return validateMintAgainstMaxSupplyCore(ctx, mintAmount, tokenIdentifier, issuerPublicKey, network)
}

// validateMintAgainstMaxSupplyCore contains the core validation logic that both proto and Ent versions can use.
func validateMintAgainstMaxSupplyCore(ctx context.Context, mintAmount *big.Int, tokenIdentifier []byte, issuerPublicKey keys.Public, network st.Network) error {
	logger := logging.GetLoggerFromContext(ctx)
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return sparkerrors.InternalDatabaseReadError(fmt.Errorf("failed to get or create current tx for request: %w", err))
	}

	// Get token metadata
	var tokenCreate *ent.TokenCreate
	var identifierInfo string
	if tokenIdentifier != nil {
		tokenCreate, err = db.TokenCreate.Query().
			Where(tokencreate.TokenIdentifierEQ(tokenIdentifier)).
			ForUpdate().
			First(ctx)
		identifierInfo = fmt.Sprintf("token identifier: %x", tokenIdentifier)
	} else if !issuerPublicKey.IsZero() {
		tokenCreate, err = db.TokenCreate.Query().
			Where(
				tokencreate.IssuerPublicKeyEQ(issuerPublicKey),
				tokencreate.NetworkEQ(network),
			).
			ForUpdate().
			First(ctx)
		identifierInfo = fmt.Sprintf("issuer public key: %v", issuerPublicKey)
	} else {
		return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("no token identifier or issuer public key provided"))
	}
	if ent.IsNotFound(err) {
		logger.Sugar().Infof("Token metadata not found - minting not allowed for %s", identifierInfo)
		return sparkerrors.NotFoundMissingEntity(fmt.Errorf("minting not allowed because a created token was not found for %s", identifierInfo))
	}
	if err != nil {
		return sparkerrors.InternalDatabaseReadError(fmt.Errorf("failed to get token metadata for %s: %w", identifierInfo, err))
	}

	maxSupply := new(big.Int).SetBytes(tokenCreate.MaxSupply)
	if maxSupply.Cmp(big.NewInt(0)) == 0 {
		// Max supply of 0 means infinite supply.
		return nil
	}

	// Calculate current supply
	var currentSupply *big.Int
	if tokenIdentifier != nil {
		currentSupply, err = calculateCurrentSupplyByTokenIdentifier(ctx, tokenIdentifier)
	} else {
		currentSupply, err = calculateCurrentSupplyByIssuerKey(ctx, issuerPublicKey)
	}
	if err != nil {
		return sparkerrors.WrapErrorWithMessage(err, "failed to calculate current minted supply")
	}

	// Validate against max supply
	newTotalSupply := new(big.Int).Add(currentSupply, mintAmount)
	if newTotalSupply.Cmp(maxSupply) > 0 {
		return sparkerrors.InvalidArgumentOutOfRange(fmt.Errorf("mint would exceed max supply: total supply after mint (%s) would exceed max supply (%s)",
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

// calculateCurrentSupplyByIssuerKey calculates the current minted supply for a token by issuer public key.
func calculateCurrentSupplyByIssuerKey(ctx context.Context, issuerPublicKey keys.Public) (*big.Int, error) {
	return calculateCurrentSupply(ctx, func(q *ent.TokenOutputQuery) *ent.TokenOutputQuery {
		return q.Where(tokenoutput.TokenPublicKeyEQ(issuerPublicKey))
	})
}

// calculateCurrentSupply is a helper function that executes the common query logic.
func calculateCurrentSupply(ctx context.Context, whereClause func(*ent.TokenOutputQuery) *ent.TokenOutputQuery) (*big.Int, error) {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, sparkerrors.InternalDatabaseReadError(fmt.Errorf("failed to get or create current tx for request: %w", err))
	}

	knobsService := knobs.GetKnobsService(ctx)
	useNumericAmountForCurrentSupply := knobsService.GetValue(knobs.KnobUseNumericAmountForCurrentTokenSupply, 0)

	totalMinted := new(big.Int)
	if useNumericAmountForCurrentSupply == 1 {
		var (
			rows []struct {
				Sum string `json:"sum_amount"` // match AS alias in Modify
			}
			qErr error
		)
		baseQuery := whereClause(db.TokenOutput.Query()).
			Where(tokenoutput.HasOutputCreatedTokenTransactionWith(
				tokentransaction.StatusEQ(st.TokenTransactionStatusSigned),
				tokentransaction.HasMint(),
			))
		err := baseQuery.Modify(func(s *esql.Selector) {
			switch s.Dialect() {
			case dialect.Postgres:
				// Postgres: SUM(amount) on NUMERIC is natively supported and efficient.
				// CAST(SUM... AS TEXT) returns unambiguous decimal string representation of the sum, which our Uint128 scanner can parse
				s.SelectExpr(esql.Expr("CAST(COALESCE(SUM(amount), 0) AS TEXT) AS sum_amount")).Limit(1)
			case dialect.SQLite:
				// SQLite: amount is stored as TEXT for precision; CAST(amount AS NUMERIC) forces numeric
				// arithmetic for SUM; CAST(SUM... AS TEXT) returns unambiguous decimal string representation
				// of the sum so we avoid float64 from the driver
				s.SelectExpr(esql.Expr("CAST(COALESCE(SUM(CAST(amount AS NUMERIC)), 0) AS TEXT) AS sum_amount")).Limit(1)
			default:
				qErr = fmt.Errorf("unsupported dialect: %s", s.Dialect())
			}
		}).Scan(ctx, &rows)
		if err = errors.Join(err, qErr); err != nil {
			return nil, sparkerrors.InternalDatabaseReadError(fmt.Errorf("failed to fetch signed mint outputs: %w", err))
		}
		total := uint128.New()
		if len(rows) > 0 {
			if err := total.Scan(rows[0].Sum); err != nil {
				return nil, err
			}
		}
		totalMinted.Set(total.BigInt())
	} else {
		outputs, err := whereClause(db.TokenOutput.Query()).
			Where(tokenoutput.HasOutputCreatedTokenTransactionWith(
				tokentransaction.StatusEQ(st.TokenTransactionStatusSigned),
				tokentransaction.HasMint(),
			)).
			Select(tokenoutput.FieldTokenAmount).
			All(ctx)
		if err != nil {
			return nil, sparkerrors.InternalDatabaseReadError(fmt.Errorf("failed to fetch signed mint outputs: %w", err))
		}

		for _, out := range outputs {
			amount := new(big.Int).SetBytes(out.TokenAmount)
			totalMinted.Add(totalMinted, amount)
		}
	}
	return totalMinted, nil
}
