package tokens

import (
	"context"
	"encoding/base64"
	"fmt"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/logging"
	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/helper"
	"github.com/lightsparkdev/spark/so/protoconverter"
	"github.com/lightsparkdev/spark/so/tokens"
	"go.uber.org/zap"
)

const (
	DefaultTokenOutputPageSize = 500
	MaxTokenOutputPageSize     = 500
)

type QueryTokenOutputsHandler struct {
	config                     *so.Config
	includeExpiredTransactions bool
}

// NewQueryTokenOutputsHandler creates a new QueryTokenOutputsHandler.
func NewQueryTokenOutputsHandler(config *so.Config) *QueryTokenOutputsHandler {
	return &QueryTokenOutputsHandler{
		config:                     config,
		includeExpiredTransactions: false,
	}
}

func NewQueryTokenOutputsHandlerWithExpiredTransactions(config *so.Config) *QueryTokenOutputsHandler {
	return &QueryTokenOutputsHandler{
		config:                     config,
		includeExpiredTransactions: true,
	}
}

func (h *QueryTokenOutputsHandler) QueryTokenOutputs(
	ctx context.Context,
	req *sparkpb.QueryTokenOutputsRequest,
) (*sparkpb.QueryTokenOutputsResponse, error) {
	ctx, span := tracer.Start(ctx, "QueryTokenHandler.QueryTokenOutputs")
	defer span.End()
	// Convert sparkpb request to tokenpb request
	tokenReq := protoconverter.TokenProtoQueryTokenOutputsRequestFromSpark(req)

	// Call internal method with tokenpb
	tokenResp, err := h.queryTokenOutputsInternal(ctx, tokenReq)
	if err != nil {
		return nil, err
	}

	// Convert tokenpb response back to sparkpb response
	return protoconverter.SparkQueryTokenOutputsResponseFromTokenProto(tokenResp), nil
}

// queryTokenOutputsInternal is the internal implementation using tokenpb protos
func (h *QueryTokenOutputsHandler) queryTokenOutputsInternal(
	ctx context.Context,
	req *tokenpb.QueryTokenOutputsRequest,
) (*tokenpb.QueryTokenOutputsResponse, error) {
	ctx, span := tracer.Start(ctx, "QueryTokenHandler.queryTokenOutputsInternal")
	defer span.End()
	logger := logging.GetLoggerFromContext(ctx)

	// Convert tokenpb request to sparkpb request for internal service calls
	// This is necessary because the internal services still use sparkpb
	sparkReq := &sparkpb.QueryTokenOutputsRequest{
		OwnerPublicKeys:  req.OwnerPublicKeys,
		TokenPublicKeys:  req.IssuerPublicKeys, // Field name change: IssuerPublicKeys -> TokenPublicKeys
		TokenIdentifiers: req.TokenIdentifiers,
		Network:          req.Network,
	}

	allSelection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionAll}
	responses, err := helper.ExecuteTaskWithAllOperators(ctx, h.config, &allSelection,
		func(ctx context.Context, operator *so.SigningOperator) (map[string]*sparkpb.OutputWithPreviousTransactionData, error) {
			var availableOutputs *sparkpb.QueryTokenOutputsResponse
			var err error

			if operator.Identifier == h.config.Identifier {
				availableOutputs, err = h.QueryTokenOutputsSpark(ctx, sparkReq)
				if err != nil {
					return nil, fmt.Errorf("failed to query token outputs from operator %s: %w", operator.Identifier, err)
				}
			} else {
				conn, err := operator.NewOperatorGRPCConnection()
				if err != nil {
					return nil, fmt.Errorf("failed to connect to operator %s: %w", operator.Identifier, err)
				}
				defer conn.Close()

				client := pbinternal.NewSparkInternalServiceClient(conn)
				availableOutputs, err = client.QueryTokenOutputsInternal(ctx, sparkReq)
				if err != nil {
					return nil, fmt.Errorf("failed to query token outputs from operator %s: %w", operator.Identifier, err)
				}
			}

			spendableOutputMap := make(map[string]*sparkpb.OutputWithPreviousTransactionData)
			for _, output := range availableOutputs.OutputsWithPreviousTransactionData {
				spendableOutputMap[output.GetOutput().GetId()] = output
			}
			return spendableOutputMap, nil
		},
	)
	if err != nil {
		logger.Info("failed to query token outputs from operators", zap.Error(err))
		return nil, fmt.Errorf("failed to query token outputs from operators: %w", err)
	}

	// Only return token outputs to the wallet that ALL SOs agree are spendable.
	//
	// If a TTXO is partially signed, the spending transaction will be cancelled once it expires to return the TTXO to the wallet.
	var spendableOutputs []*sparkpb.OutputWithPreviousTransactionData
	countSpendableOperatorsForOutputID := make(map[string]int)

	requiredSpendableOperators := len(h.config.GetSigningOperatorList())
	for _, spendableOutputMap := range responses {
		for outputID, spendableOutput := range spendableOutputMap {
			countSpendableOperatorsForOutputID[outputID]++
			if countSpendableOperatorsForOutputID[outputID] == requiredSpendableOperators {
				spendableOutputs = append(spendableOutputs, spendableOutput)
			}
		}
	}

	for outputID, countSpendableOperators := range countSpendableOperatorsForOutputID {
		if countSpendableOperators < requiredSpendableOperators {
			logger.Sugar().Warnf(
				"Token output %s not spendable in all operators (count %d, required %d)",
				outputID,
				countSpendableOperators,
				requiredSpendableOperators,
			)
		}
	}

	// Convert sparkpb response to tokenpb response
	tokenOutputs := make([]*tokenpb.OutputWithPreviousTransactionData, len(spendableOutputs))
	for i, sparkOutput := range spendableOutputs {
		tokenOutputs[i] = &tokenpb.OutputWithPreviousTransactionData{
			Output: &tokenpb.TokenOutput{
				Id:                            sparkOutput.Output.Id,
				OwnerPublicKey:                sparkOutput.Output.OwnerPublicKey,
				RevocationCommitment:          sparkOutput.Output.RevocationCommitment,
				WithdrawBondSats:              sparkOutput.Output.WithdrawBondSats,
				WithdrawRelativeBlockLocktime: sparkOutput.Output.WithdrawRelativeBlockLocktime,
				TokenPublicKey:                sparkOutput.Output.TokenPublicKey,
				TokenAmount:                   sparkOutput.Output.TokenAmount,
				TokenIdentifier:               sparkOutput.Output.TokenIdentifier,
			},
			PreviousTransactionHash: sparkOutput.PreviousTransactionHash,
			PreviousTransactionVout: sparkOutput.PreviousTransactionVout,
		}
	}

	return &tokenpb.QueryTokenOutputsResponse{
		OutputsWithPreviousTransactionData: tokenOutputs,
	}, nil
}

func (h *QueryTokenOutputsHandler) QueryTokenOutputsSpark(ctx context.Context, req *sparkpb.QueryTokenOutputsRequest) (*sparkpb.QueryTokenOutputsResponse, error) {
	tokenReq := protoconverter.TokenProtoQueryTokenOutputsRequestFromSpark(req)

	tokenResp, err := h.QueryTokenOutputsToken(ctx, tokenReq)
	if err != nil {
		return nil, err
	}

	return protoconverter.SparkQueryTokenOutputsResponseFromTokenProto(tokenResp), nil
}

// QueryTokenOutputsToken is the native tokenpb endpoint for SparkTokenService.
// This provides the same functionality as the legacy QueryTokenOutputs but uses
// tokenpb protocol directly for better performance and cleaner API design.
func (h *QueryTokenOutputsHandler) QueryTokenOutputsToken(ctx context.Context, req *tokenpb.QueryTokenOutputsRequest) (*tokenpb.QueryTokenOutputsResponse, error) {
	network, err := common.DetermineNetwork(req.GetNetwork())
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
	if pageRequest != nil && pageRequest.GetPageSize() > 0 {
		limit = int(pageRequest.GetPageSize())
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
		OwnerPublicKeys:            ownerPubKeys,
		IssuerPublicKeys:           issuerPubKeys,
		TokenIdentifiers:           tokenIdentifiers,
		IncludeExpiredTransactions: true,
		Network:                    *network,
		AfterID:                    afterID,
		BeforeID:                   beforeID,
		Limit:                      queryLimit,
	})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", tokens.ErrFailedToGetOwnedOutputStats, err)
	}
	var ownedTokenOutputs []*tokenpb.OutputWithPreviousTransactionData
	for i, output := range outputs {
		if i >= limit {
			break
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
