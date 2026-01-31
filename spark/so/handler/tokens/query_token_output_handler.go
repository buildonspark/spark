package tokens

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/tokens"
)

const (
	DefaultTokenOutputPageSize = 500
	MaxTokenOutputPageSize     = 500
	MaxTokenOutputFilterValues = 500
)

func validateQueryTokenOutputsRequest(req *tokenpb.QueryTokenOutputsRequest) error {
	if len(req.OwnerPublicKeys) > MaxTokenOutputFilterValues {
		return errors.InvalidArgumentOutOfRange(
			fmt.Errorf("too many owner public keys in filter: got %d, max %d", len(req.OwnerPublicKeys), MaxTokenOutputFilterValues),
		)
	}

	if len(req.IssuerPublicKeys) > MaxTokenOutputFilterValues {
		return errors.InvalidArgumentOutOfRange(
			fmt.Errorf("too many issuer public keys in filter: got %d, max %d", len(req.IssuerPublicKeys), MaxTokenOutputFilterValues),
		)
	}

	if len(req.TokenIdentifiers) > MaxTokenOutputFilterValues {
		return errors.InvalidArgumentOutOfRange(
			fmt.Errorf("too many token identifiers in filter: got %d, max %d", len(req.TokenIdentifiers), MaxTokenOutputFilterValues),
		)
	}

	return nil
}

type QueryTokenOutputsHandler struct {
	config *so.Config
}

func NewQueryTokenOutputsHandler(config *so.Config) *QueryTokenOutputsHandler {
	return &QueryTokenOutputsHandler{
		config: config,
	}
}

func (h *QueryTokenOutputsHandler) QueryTokenOutputs(ctx context.Context, req *tokenpb.QueryTokenOutputsRequest) (*tokenpb.QueryTokenOutputsResponse, error) {
	ctx, span := GetTracer().Start(ctx, "QueryTokenOutputsHandler.QueryTokenOutputs")
	defer span.End()

	if err := validateQueryTokenOutputsRequest(req); err != nil {
		return nil, err
	}

	network, err := btcnetwork.FromProtoNetwork(req.GetNetwork())
	if err != nil {
		return nil, err
	}

	ownerPubKeys, err := keys.ParsePublicKeys(req.GetOwnerPublicKeys())
	if err != nil {
		return nil, errors.InvalidArgumentMalformedKey(fmt.Errorf("invalid owner public keys: %w", err))
	}
	issuerPubKeys, err := keys.ParsePublicKeys(req.GetIssuerPublicKeys())
	if err != nil {
		return nil, errors.InvalidArgumentMalformedKey(fmt.Errorf("invalid issuer public keys: %w", err))
	}
	tokenIdentifiers := req.GetTokenIdentifiers()
	if len(ownerPubKeys) == 0 && len(issuerPubKeys) == 0 && len(tokenIdentifiers) == 0 {
		return nil, errors.InvalidArgumentMissingField(fmt.Errorf("must specify owner public key, issuer public key, or token identifier"))
	}

	var afterID *uuid.UUID
	var beforeID *uuid.UUID

	pageRequest := req.GetPageRequest()
	var direction sparkpb.Direction
	var cursor string

	if pageRequest != nil {
		direction = pageRequest.GetDirection()
		cursor = pageRequest.GetCursor()
	}

	// Handle cursor based on direction
	if cursor != "" {
		cursorBytes, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil {
			cursorBytes, err = base64.URLEncoding.DecodeString(cursor)
			if err != nil {
				return nil, errors.InvalidArgumentMalformedField(fmt.Errorf("invalid cursor: %w", err))
			}
		}
		id, err := uuid.FromBytes(cursorBytes)
		if err != nil {
			return nil, errors.InvalidArgumentMalformedField(fmt.Errorf("invalid cursor: %w", err))
		}

		if direction == sparkpb.Direction_PREVIOUS {
			beforeID = &id
		} else {
			afterID = &id
		}
	}

	limit := DefaultTokenOutputPageSize
	if pageRequest != nil {
		if pageRequest.GetPageSize() > 0 {
			limit = int(pageRequest.GetPageSize())
		} else if pageRequest.GetUnsafePageSize() > 0 {
			limit = int(pageRequest.GetUnsafePageSize())
		}
	}
	if limit > MaxTokenOutputPageSize {
		limit = MaxTokenOutputPageSize
	}

	// Check for unsupported backward pagination
	if direction == sparkpb.Direction_PREVIOUS {
		return nil, errors.InvalidArgumentMalformedField(fmt.Errorf("backward pagination with 'previous' direction is not currently supported"))
	}

	queryLimit := limit + 1
	outputs, err := ent.GetOwnedTokenOutputs(ctx, ent.GetOwnedTokenOutputsParams{
		OwnerPublicKeys:  ownerPubKeys,
		IssuerPublicKeys: issuerPubKeys,
		TokenIdentifiers: tokenIdentifiers,
		Network:          network,
		AfterID:          afterID,
		BeforeID:         beforeID,
		Limit:            queryLimit,
	})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", tokens.ErrFailedToGetOwnedOutputStats, err)
	}
	var ownedTokenOutputs []*tokenpb.OutputWithPreviousTransactionData
	for i, output := range outputs {
		if i >= limit {
			break
		}
		status, err := mapTokenOutputStatusToProto(output)
		if err != nil {
			return nil, err
		}
		idStr := output.ID.String()
		ownedTokenOutputs = append(ownedTokenOutputs, &tokenpb.OutputWithPreviousTransactionData{
			Output: &tokenpb.TokenOutput{
				Id:                            &idStr,
				OwnerPublicKey:                output.OwnerPublicKey.Serialize(),
				RevocationCommitment:          output.WithdrawRevocationCommitment,
				WithdrawBondSats:              &output.WithdrawBondSats,
				WithdrawRelativeBlockLocktime: &output.WithdrawRelativeBlockLocktime,
				TokenPublicKey:                output.TokenPublicKey.Serialize(),
				TokenIdentifier:               output.TokenIdentifier,
				TokenAmount:                   output.TokenAmount,
				SeWithdrawalSignature:         output.SeWithdrawalSignature,
				Status:                        status,
			},
			PreviousTransactionHash: output.Edges.OutputCreatedTokenTransaction.FinalizedTokenTransactionHash,
			PreviousTransactionVout: uint32(output.CreatedTransactionOutputVout),
		})
	}
	pageResponse := &sparkpb.PageResponse{}

	hasMoreResults := len(outputs) > limit

	if afterID == nil {
		// No pagination: no previous page, check if there's a next page
		pageResponse.HasPreviousPage = false
		pageResponse.HasNextPage = hasMoreResults
	} else {
		// Forward pagination: we know there's a previous page, check if there's a next page
		pageResponse.HasPreviousPage = true
		pageResponse.HasNextPage = hasMoreResults
	}

	if len(ownedTokenOutputs) > 0 {
		// Set previous cursor (first item's ID) - for going backward from this page
		if first := ownedTokenOutputs[0]; first != nil && first.Output != nil && first.Output.Id != nil {
			if firstUUID, err := uuid.Parse(first.GetOutput().GetId()); err == nil {
				pageResponse.PreviousCursor = base64.RawURLEncoding.EncodeToString(firstUUID[:])
			}
		}

		// Set next cursor (last item's ID) - for going forward from this page
		if last := ownedTokenOutputs[len(ownedTokenOutputs)-1]; last != nil && last.Output != nil && last.Output.Id != nil {
			if lastUUID, err := uuid.Parse(last.GetOutput().GetId()); err == nil {
				pageResponse.NextCursor = base64.RawURLEncoding.EncodeToString(lastUUID[:])
			}
		}
	}

	return &tokenpb.QueryTokenOutputsResponse{
		OutputsWithPreviousTransactionData: ownedTokenOutputs,
		PageResponse:                       pageResponse,
	}, nil
}

func mapTokenOutputStatusToProto(output *ent.TokenOutput) (*tokenpb.TokenOutputStatus, error) {
	switch output.Status {
	case st.TokenOutputStatusCreatedFinalized, st.TokenOutputStatusSpentStarted:
		return tokenpb.TokenOutputStatus_TOKEN_OUTPUT_STATUS_AVAILABLE.Enum(), nil
	case st.TokenOutputStatusSpentSigned:
		if isOutputSpentTransactionExpired(output) {
			return tokenpb.TokenOutputStatus_TOKEN_OUTPUT_STATUS_AVAILABLE.Enum(), nil
		}
		return tokenpb.TokenOutputStatus_TOKEN_OUTPUT_STATUS_PENDING_OUTBOUND.Enum(), nil
	default:
		return nil, fmt.Errorf("unexpected token output status %q for output %s", output.Status, output.ID)
	}
}

func isOutputSpentTransactionExpired(output *ent.TokenOutput) bool {
	tx := output.Edges.OutputSpentTokenTransaction
	if tx == nil {
		return false
	}
	if err := tx.ValidateNotExpired(); err == nil {
		return false
	}
	return tx.Status == st.TokenTransactionStatusSigned || tx.Status == st.TokenTransactionStatusStarted
}
