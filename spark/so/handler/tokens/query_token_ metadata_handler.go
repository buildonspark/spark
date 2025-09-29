package tokens

import (
	"context"
	"fmt"

	"github.com/lightsparkdev/spark/common/keys"

	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/predicate"
	"github.com/lightsparkdev/spark/so/ent/tokencreate"
)

type QueryTokenMetadataHandler struct {
	config *so.Config
}

// NewQueryTokenMetadataHandler creates a new QueryTokenMetadataHandler.
func NewQueryTokenMetadataHandler(config *so.Config) *QueryTokenMetadataHandler {
	return &QueryTokenMetadataHandler{
		config: config,
	}
}

func (h *QueryTokenMetadataHandler) QueryTokenMetadata(ctx context.Context, req *tokenpb.QueryTokenMetadataRequest) (*tokenpb.QueryTokenMetadataResponse, error) {
	ctx, span := tracer.Start(ctx, "QueryTokenMetadataHandler.QueryTokenMetadata")
	defer span.End()
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	if len(req.TokenIdentifiers) == 0 && len(req.GetIssuerPublicKeys()) == 0 {
		return nil, fmt.Errorf("must provide at least one token identifier or issuer public key")
	}

	fields := []string{
		tokencreate.FieldIssuerPublicKey,
		tokencreate.FieldTokenName,
		tokencreate.FieldTokenTicker,
		tokencreate.FieldDecimals,
		tokencreate.FieldMaxSupply,
		tokencreate.FieldIsFreezable,
		tokencreate.FieldCreationEntityPublicKey,
		tokencreate.FieldNetwork,
	}

	var conditions []predicate.TokenCreate
	if len(req.TokenIdentifiers) > 0 {
		conditions = append(conditions, tokencreate.TokenIdentifierIn(req.TokenIdentifiers...))
	}

	issuerPubKeys, err := keys.ParsePublicKeys(req.GetIssuerPublicKeys())
	if err != nil {
		return nil, fmt.Errorf("failed to parse issuer public key: %w", err)
	}
	if len(issuerPubKeys) > 0 {
		conditions = append(conditions, tokencreate.IssuerPublicKeyIn(issuerPubKeys...))
	}

	tokenCreateEntities, err := db.TokenCreate.Query().
		Where(tokencreate.Or(conditions...)).
		Select(fields...).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to query token metadata: %w", err)
	}

	var tokenMetadataList []*tokenpb.TokenMetadata
	for _, tokenCreate := range tokenCreateEntities {
		tokenMetadata, err := tokenCreate.ToTokenMetadata()
		if err != nil {
			return nil, fmt.Errorf("failed to convert token create to token metadata: %w", err)
		}
		tokenMetadataList = append(tokenMetadataList, tokenMetadata.ToTokenMetadataProto())
	}

	return &tokenpb.QueryTokenMetadataResponse{TokenMetadata: tokenMetadataList}, nil
}
