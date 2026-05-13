package tokens

import (
	"context"
	"fmt"

	"github.com/lightsparkdev/spark/common/keys"

	"entgo.io/ent/dialect/sql"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/predicate"
	"github.com/lightsparkdev/spark/so/ent/tokencreate"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
)

const MaxTokenMetadataFilterValues = MaxTokenOutputFilterValues

type QueryTokenMetadataHandler struct {
	config *so.Config
}

// NewQueryTokenMetadataHandler creates a new QueryTokenMetadataHandler.
func NewQueryTokenMetadataHandler(config *so.Config) *QueryTokenMetadataHandler {
	return &QueryTokenMetadataHandler{
		config: config,
	}
}

func validateQueryTokenMetadataRequest(req *tokenpb.QueryTokenMetadataRequest) error {
	if req == nil {
		return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("request is required"))
	}

	if len(req.TokenIdentifiers) == 0 && len(req.IssuerPublicKeys) == 0 {
		return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("must provide at least one token identifier or issuer public key"))
	}

	if len(req.TokenIdentifiers) > MaxTokenMetadataFilterValues {
		return sparkerrors.InvalidArgumentOutOfRange(
			fmt.Errorf("too many token identifiers in filter: got %d, max %d", len(req.TokenIdentifiers), MaxTokenMetadataFilterValues),
		)
	}

	if len(req.IssuerPublicKeys) > MaxTokenMetadataFilterValues {
		return sparkerrors.InvalidArgumentOutOfRange(
			fmt.Errorf("too many issuer public keys in filter: got %d, max %d", len(req.IssuerPublicKeys), MaxTokenMetadataFilterValues),
		)
	}

	return nil
}

func (h *QueryTokenMetadataHandler) QueryTokenMetadata(ctx context.Context, req *tokenpb.QueryTokenMetadataRequest) (*tokenpb.QueryTokenMetadataResponse, error) {
	ctx, span := GetTracer().Start(ctx, "QueryTokenMetadataHandler.QueryTokenMetadata")
	defer span.End()

	if err := validateQueryTokenMetadataRequest(req); err != nil {
		return nil, err
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
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
		tokencreate.FieldExtraMetadata,
	}

	var conditions []predicate.TokenCreate
	if len(req.TokenIdentifiers) > 0 {
		conditions = append(conditions, tokencreate.TokenIdentifierIn(req.TokenIdentifiers...))
	}

	issuerPubKeys, err := keys.ParsePublicKeys(req.IssuerPublicKeys)
	if err != nil {
		return nil, fmt.Errorf("failed to parse issuer public key: %w", err)
	}
	if len(issuerPubKeys) > 0 {
		conditions = append(conditions, tokencreate.IssuerPublicKeyIn(issuerPubKeys...))
	}

	tokenCreateEntities, err := db.TokenCreate.Query().
		Where(tokencreate.Or(conditions...)).
		Select(fields...).
		Order(tokencreate.ByCreateTime(sql.OrderAsc())). // Return the oldest first to support legacy sdks that only supported one token per issuer
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
