package tokens

import (
	"context"
	"fmt"
	"math/big"

	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"

	"github.com/lightsparkdev/spark/common/logging"
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

	// Extract token identification from proto transaction
	var tokenIdentifier []byte
	var issuerPublicKey keys.Public

	if tokenTransaction.GetMintInput() != nil {
		tokenIdentifier = tokenTransaction.GetMintInput().GetTokenIdentifier()
		mintPublicKey, err := keys.ParsePublicKey(tokenTransaction.GetMintInput().GetIssuerPublicKey())
		if err != nil {
			return sparkerrors.InvalidUserInputErrorf("failed to get issuer public key: %w", err)
		}
		issuerPublicKey = mintPublicKey
	} else if len(tokenTransaction.GetTokenOutputs()) > 0 {
		output := tokenTransaction.GetTokenOutputs()[0]
		tokenIdentifier = output.GetTokenIdentifier()
		tokenPublicKey, err := keys.ParsePublicKey(output.GetTokenPublicKey())
		if err != nil {
			return sparkerrors.InvalidUserInputErrorf("failed to get token public key: %w", err)
		}
		issuerPublicKey = tokenPublicKey
	}

	commonNetwork, err := common.NetworkFromProtoNetwork(tokenTransaction.Network)
	if err != nil {
		return sparkerrors.InternalErrorf("failed to get network from proto: %w", err)
	}
	schemaNetwork, err := common.SchemaNetworkFromNetwork(commonNetwork)
	if err != nil {
		return sparkerrors.InternalErrorf("failed to get schema network: %w", err)
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
		return sparkerrors.NotFoundErrorf("cannot verify max supply for mint transaction because no mint input was found")
	}
	tokenIdentifier := tokenTransaction.Edges.Mint.TokenIdentifier
	issuerPublicKey, err := keys.ParsePublicKey(tokenTransaction.Edges.Mint.IssuerPublicKey)
	if err != nil {
		return sparkerrors.NotFoundErrorf("failed to get issuer public key: %w", err)
	}

	if len(tokenTransaction.Edges.CreatedOutput) == 0 {
		return sparkerrors.NotFoundErrorf("cannot determine network for mint transaction because no outputs were found")
	}
	network := tokenTransaction.Edges.CreatedOutput[0].Network

	return validateMintAgainstMaxSupplyCore(ctx, mintAmount, tokenIdentifier, issuerPublicKey, network)
}

// validateMintAgainstMaxSupplyCore contains the core validation logic that both proto and Ent versions can use.
func validateMintAgainstMaxSupplyCore(ctx context.Context, mintAmount *big.Int, tokenIdentifier []byte, issuerPublicKey keys.Public, network st.Network) error {
	logger := logging.GetLoggerFromContext(ctx)
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return sparkerrors.InternalErrorf("failed to get or create current tx for request: %w", err)
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
			Where(tokencreate.IssuerPublicKeyEQ(issuerPublicKey.Serialize()), tokencreate.NetworkEQ(network)).
			ForUpdate().
			First(ctx)
		identifierInfo = fmt.Sprintf("issuer public key: %v", issuerPublicKey)
	} else {
		return sparkerrors.InvalidUserInputErrorf("no token identifier or issuer public key provided")
	}
	if ent.IsNotFound(err) {
		logger.Sugar().Infof("Token metadata not found - minting not allowed for %s", identifierInfo)
		return sparkerrors.NotFoundErrorf("minting not allowed because a created token was not found for %s", identifierInfo)
	}
	if err != nil {
		return sparkerrors.InternalErrorf("failed to get token metadata for %s: %w", identifierInfo, err)
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
		return sparkerrors.InternalErrorf("failed to calculate current minted supply: %w", err)
	}

	// Validate against max supply
	newTotalSupply := new(big.Int).Add(currentSupply, mintAmount)
	if newTotalSupply.Cmp(maxSupply) > 0 {
		return sparkerrors.FailedPreconditionErrorf("mint would exceed max supply: total supply after mint (%s) would exceed max supply (%s)",
			newTotalSupply.String(), maxSupply.String())
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
		return q.Where(tokenoutput.TokenPublicKeyEQ(issuerPublicKey.Serialize()))
	})
}

// calculateCurrentSupply is a helper function that executes the common query logic.
func calculateCurrentSupply(ctx context.Context, whereClause func(*ent.TokenOutputQuery) *ent.TokenOutputQuery) (*big.Int, error) {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	outputs, err := whereClause(db.TokenOutput.Query()).
		Where(tokenoutput.HasOutputCreatedTokenTransactionWith(
			tokentransaction.StatusEQ(st.TokenTransactionStatusSigned),
			tokentransaction.HasMint(),
		)).
		All(ctx)
	if err != nil {
		return nil, sparkerrors.InternalErrorf("failed to fetch signed mint outputs: %w", err)
	}

	totalMinted := new(big.Int)
	for _, out := range outputs {
		amount := new(big.Int).SetBytes(out.TokenAmount)
		totalMinted.Add(totalMinted, amount)
	}
	return totalMinted, nil
}
