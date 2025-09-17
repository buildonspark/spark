package tokens

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/lightsparkdev/spark/common/keys"
	"go.uber.org/zap"

	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/logging"
	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	tokeninternalpb "github.com/lightsparkdev/spark/proto/spark_token_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authz"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/predicate"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/tokenoutput"
	"github.com/lightsparkdev/spark/so/ent/tokentransaction"
	"github.com/lightsparkdev/spark/so/helper"
	"github.com/lightsparkdev/spark/so/protoconverter"
	"github.com/lightsparkdev/spark/so/tokens"
	"github.com/lightsparkdev/spark/so/utils"
)

const queryTokenOutputsWithPartialRevocationSecretSharesBatchSize = 50

var finalizedCommitTransactionResponse = &tokenpb.CommitTransactionResponse{
	CommitStatus: tokenpb.CommitStatus_COMMIT_FINALIZED,
}

type operatorSignaturesMap map[string][]byte

type SignTokenHandler struct {
	config *so.Config
}

// NewSignTokenHandler creates a new SignTokenHandler.
func NewSignTokenHandler(config *so.Config) *SignTokenHandler {
	return &SignTokenHandler{
		config: config,
	}
}

// SignTokenTransaction signs the token transaction with the operators private key.
// If it is a transfer it also fetches that operator's keyshare for each spent output and
// returns it to the wallet so it can finalize the transaction.
func (h *SignTokenHandler) SignTokenTransaction(
	ctx context.Context,
	req *sparkpb.SignTokenTransactionRequest,
) (*sparkpb.SignTokenTransactionResponse, error) {
	logger := logging.GetLoggerFromContext(ctx)

	idPubKey, err := keys.ParsePublicKey(req.GetIdentityPublicKey())
	if err != nil {
		return nil, fmt.Errorf("invalid identity public key: %w", err)
	}
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, idPubKey); err != nil {
		return nil, err
	}

	tokenProtoTokenTransaction, err := protoconverter.TokenProtoFromSparkTokenTransaction(req.FinalTokenTransaction)
	if err != nil {
		return nil, fmt.Errorf("failed to convert token transaction to spark token transaction: %w", err)
	}
	ctx, span := tracer.Start(ctx, "SignTokenHandler.SignTokenTransaction", getTokenTransactionAttributes(tokenProtoTokenTransaction))
	defer span.End()

	finalTokenTransactionHash, err := utils.HashTokenTransaction(tokenProtoTokenTransaction, false)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", tokens.ErrFailedToHashFinalTransaction, err)
	}

	tokenTransaction, err := ent.FetchAndLockTokenTransactionData(ctx, tokenProtoTokenTransaction)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", tokens.ErrFailedToFetchTransaction, logging.FormatProto("final_token_transaction", req.FinalTokenTransaction), err)
	}

	internalSignTokenHandler := NewInternalSignTokenHandler(h.config)
	operatorSignature, err := internalSignTokenHandler.SignAndPersistTokenTransaction(ctx, tokenTransaction, finalTokenTransactionHash, req.OperatorSpecificSignatures)
	if err != nil {
		return nil, err
	}

	if tokenTransaction.Status == st.TokenTransactionStatusSigned {
		revocationKeyshares, err := h.getRevocationKeysharesForTokenTransaction(ctx, tokenTransaction)
		if err != nil {
			return nil, tokens.FormatErrorWithTransactionEnt(tokens.ErrFailedToGetRevocationKeyshares, tokenTransaction, err)
		}
		return &sparkpb.SignTokenTransactionResponse{
			SparkOperatorSignature: operatorSignature,
			RevocationKeyshares:    revocationKeyshares,
		}, nil
	}

	keyshares := make([]*ent.SigningKeyshare, len(tokenTransaction.Edges.SpentOutput))
	revocationKeyshares := make([]*sparkpb.KeyshareWithIndex, len(tokenTransaction.Edges.SpentOutput))
	for _, output := range tokenTransaction.Edges.SpentOutput {
		keyshare, err := output.QueryRevocationKeyshare().Only(ctx)
		if err != nil {
			logger.Info("Failed to get keyshare for output", zap.Error(err))
			return nil, err
		}
		index := output.SpentTransactionInputVout
		keyshares[index] = keyshare
		revocationKeyshares[index] = &sparkpb.KeyshareWithIndex{
			InputIndex: uint32(index),
			Keyshare:   keyshare.SecretShare,
		}

		// Validate that the keyshare's public key is as expected.
		withdrawRevocationCommitment, err := keys.ParsePublicKey(output.WithdrawRevocationCommitment)
		if err != nil {
			return nil, fmt.Errorf("failed to parse withdraw revocation commitment: %w", err)
		}
		if !keyshare.PublicKey.Equals(withdrawRevocationCommitment) {
			return nil, fmt.Errorf(
				"keyshare public key %v does not match output revocation commitment %v",
				keyshare.PublicKey,
				withdrawRevocationCommitment,
			)
		}
	}

	return &sparkpb.SignTokenTransactionResponse{
		SparkOperatorSignature: operatorSignature,
		RevocationKeyshares:    revocationKeyshares,
	}, nil
}

func (h *SignTokenHandler) CommitTransaction(ctx context.Context, req *tokenpb.CommitTransactionRequest) (*tokenpb.CommitTransactionResponse, error) {
	ctx, span := tracer.Start(ctx, "SignTokenHandler.CommitTransaction", getTokenTransactionAttributes(req.FinalTokenTransaction))
	defer span.End()
	ownerIDPubKey, err := keys.ParsePublicKey(req.GetOwnerIdentityPublicKey())
	if err != nil {
		return nil, fmt.Errorf("invalid identity public key: %w", err)
	}
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, ownerIDPubKey); err != nil {
		return nil, fmt.Errorf("identity public key authentication failed: %w", err)
	}

	calculatedHash, err := utils.HashTokenTransaction(req.FinalTokenTransaction, false)
	ctx, logger := logging.WithAttrs(ctx, tokens.GetFinalizedTokenTransactionAttrs(calculatedHash)...)
	if err != nil {
		return nil, fmt.Errorf("failed to hash final token transaction: %w", err)
	}
	if !bytes.Equal(calculatedHash, req.FinalTokenTransactionHash) {
		return nil, fmt.Errorf("transaction hash mismatch: expected %x, got %x", calculatedHash, req.FinalTokenTransactionHash)
	}

	tokenTransaction, err := ent.FetchAndLockTokenTransactionData(ctx, req.FinalTokenTransaction)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch transaction: %w", err)
	}

	inferredTxType, err := tokenTransaction.InferTokenTransactionTypeEnt()
	if err != nil {
		return nil, fmt.Errorf("failed to infer token transaction type: %w", err)
	}

	// Check if we should return early without further processing
	if response, err := h.checkShouldReturnEarlyWithoutProcessing(ctx, tokenTransaction, inferredTxType); response != nil || err != nil {
		return response, err
	}

	if err := validateTokenTransactionForSigning(h.config, tokenTransaction); err != nil {
		return nil, tokens.FormatErrorWithTransactionEnt(err.Error(), tokenTransaction, err)
	}

	allOperators := helper.OperatorSelection{Option: helper.OperatorSelectionOptionAll}
	internalSignatures, err := helper.ExecuteTaskWithAllOperators(ctx, h.config, &allOperators,
		func(ctx context.Context, operator *so.SigningOperator) (*tokeninternalpb.SignTokenTransactionFromCoordinationResponse, error) {
			var foundOperatorSignatures *tokenpb.InputTtxoSignaturesPerOperator
			for _, operatorSignatures := range req.InputTtxoSignaturesPerOperator {
				signaturesPubKey, err := keys.ParsePublicKey(operatorSignatures.OperatorIdentityPublicKey)
				if err != nil {
					return nil, fmt.Errorf("failed to parse signatures operator ID public key: %w", err)
				}
				if signaturesPubKey.Equals(operator.IdentityPublicKey) {
					foundOperatorSignatures = operatorSignatures
					break
				}
			}
			if foundOperatorSignatures == nil {
				return nil, fmt.Errorf("no signatures found for operator %s: %w", operator.Identifier, err)
			}

			if operator.Identifier == h.config.Identifier {
				return h.localSignAndCommitTransaction(ctx, foundOperatorSignatures, req.FinalTokenTransactionHash, tokenTransaction)
			}

			conn, err := operator.NewOperatorGRPCConnection()
			if err != nil {
				return nil, fmt.Errorf("failed to connect to operator %s: %w", operator.Identifier, err)
			}
			defer conn.Close()
			client := tokeninternalpb.NewSparkTokenInternalServiceClient(conn)
			return client.SignTokenTransactionFromCoordination(ctx, &tokeninternalpb.SignTokenTransactionFromCoordinationRequest{
				FinalTokenTransaction:          req.FinalTokenTransaction,
				FinalTokenTransactionHash:      req.FinalTokenTransactionHash,
				InputTtxoSignaturesPerOperator: foundOperatorSignatures,
				OwnerIdentityPublicKey:         req.OwnerIdentityPublicKey,
			})
		},
	)
	if err != nil {
		return nil, tokens.FormatErrorWithTransactionEnt("failed to get signatures from operators", tokenTransaction, err)
	}

	signatures := make(operatorSignaturesMap)
	for operatorID, sig := range internalSignatures {
		signatures[operatorID] = sig.SparkOperatorSignature
	}

	internalSignTokenHandler := NewInternalSignTokenHandler(h.config)
	if err := internalSignTokenHandler.validateSignaturesPackageAndPersistPeerSignatures(ctx, signatures, tokenTransaction); err != nil {
		return nil, tokens.FormatErrorWithTransactionEnt("failed to validate signature package and persist peer signatures", tokenTransaction, err)
	}

	logger.Info("Successfully signed and persisted token transaction")

	switch inferredTxType {
	case utils.TokenTransactionTypeCreate, utils.TokenTransactionTypeMint:
		// We validated the signatures package above, so we know that it is finalized.
		return finalizedCommitTransactionResponse, nil
	case utils.TokenTransactionTypeTransfer:
		if response, err := h.ExchangeRevocationSecretsAndFinalizeIfPossible(ctx, req.FinalTokenTransaction, internalSignatures, req.FinalTokenTransactionHash); err != nil {
			return nil, tokens.FormatErrorWithTransactionEnt("failed to exchange revocation secret shares and finalize if possible", tokenTransaction, err)
		} else {
			return response, nil
		}
	default:
		return nil, fmt.Errorf("token transaction type not supported: %s", inferredTxType)
	}
}

func (h *SignTokenHandler) ExchangeRevocationSecretsAndFinalizeIfPossible(ctx context.Context, tokenTransactionProto *tokenpb.TokenTransaction, internalSignatures map[string]*tokeninternalpb.SignTokenTransactionFromCoordinationResponse, tokenTransactionHash []byte) (*tokenpb.CommitTransactionResponse, error) {
	ctx, logger := logging.WithAttrs(ctx, tokens.GetFinalizedTokenTransactionAttrs(tokenTransactionHash)...)
	ctx, span := tracer.Start(ctx, "SignTokenHandler.ExchangeRevocationSecretsAndFinalizeIfPossible", getTokenTransactionAttributes(tokenTransactionProto))
	defer span.End()
	response, err := h.exchangeRevocationSecretShares(ctx, internalSignatures, tokenTransactionProto, tokenTransactionHash)
	if err != nil {
		return nil, fmt.Errorf("coordinator failed to exchange revocation secret shares with all other operators for token txHash: %x: %w", tokenTransactionHash, err)
	}

	// Collect the secret shares from all operators.
	var operatorShares []*tokeninternalpb.OperatorRevocationShares
	for _, exchangeResponse := range response {
		if exchangeResponse == nil {
			return nil, fmt.Errorf("nil exchange response received from operator for token txHash: %x", tokenTransactionHash)
		}
		operatorShares = append(operatorShares, exchangeResponse.ReceivedOperatorShares...)
	}
	inputOperatorShareMap, err := buildInputOperatorShareMap(operatorShares)
	if err != nil {
		return nil, fmt.Errorf("failed to build input operator share map for token txHash: %x: %w", tokenTransactionHash, err)
	}
	logger.Sugar().Infof("Length of inputOperatorShareMap: %d", len(inputOperatorShareMap))
	// Persist the secret shares from all operators.
	internalHandler := NewInternalSignTokenHandler(h.config)
	finalized, err := internalHandler.persistPartialRevocationSecretShares(ctx, inputOperatorShareMap, tokenTransactionHash)
	if err != nil {
		return nil, tokens.FormatErrorWithTransactionProto("failed to persist partial revocation secret shares", tokenTransactionProto, err)
	}

	if finalized {
		_, err := h.exchangeRevocationSecretShares(ctx, internalSignatures, tokenTransactionProto, tokenTransactionHash)
		if err != nil {
			return nil, tokens.FormatErrorWithTransactionProto("failed to exchange revocation secret shares after finalization", tokenTransactionProto, err)
		}
		return finalizedCommitTransactionResponse, nil

	} else {
		// Refetch the token transaction to pick up newly committed partial revocation secret shares
		refetchedTokenTransaction, err := ent.FetchAndLockTokenTransactionDataByHash(ctx, tokenTransactionHash)
		if err != nil {
			return nil, fmt.Errorf("failed to refetch token transaction data: %w", err)
		}

		commitProgress, err := h.getRevealCommitProgress(ctx, refetchedTokenTransaction)
		if err != nil {
			return nil, fmt.Errorf("failed to get reveal commit progress: %w", err)
		}
		return &tokenpb.CommitTransactionResponse{
			CommitStatus:   tokenpb.CommitStatus_COMMIT_PROCESSING,
			CommitProgress: commitProgress,
		}, nil
	}
}

// checkShouldReturnEarlyWithoutProcessing determines if the transaction should return early based on the signatures
// and/or revocation keyshares already retrieved by this SO (which may have happened if this is a duplicate call or retry).
func (h *SignTokenHandler) checkShouldReturnEarlyWithoutProcessing(
	ctx context.Context,
	tokenTransaction *ent.TokenTransaction,
	inferredTxType utils.TokenTransactionType,
) (*tokenpb.CommitTransactionResponse, error) {
	switch inferredTxType {
	case utils.TokenTransactionTypeCreate, utils.TokenTransactionTypeMint:
		// If this SO has all signatures for a create or mint, the transaction is final and fully committed.
		// Otherwise continue because this SO is in STARTED or SIGNED and needs more signatures.
		if tokenTransaction.Status == st.TokenTransactionStatusSigned {
			commitProgress, err := h.getSignedCommitProgress(ctx, tokenTransaction)
			if err != nil {
				return nil, fmt.Errorf("failed to get create/mint signed commit progress: %w", err)
			}
			if len(commitProgress.UncommittedOperatorPublicKeys) == 0 {
				return finalizedCommitTransactionResponse, nil
			}
		}
	case utils.TokenTransactionTypeTransfer:
		if tokenTransaction.Status == st.TokenTransactionStatusFinalized {
			return finalizedCommitTransactionResponse, nil
		}
		if tokenTransaction.Status == st.TokenTransactionStatusRevealed {
			// If this SO is in revealed, the user is no longer responsible for any further actions.
			// If an SO is stuck in revealed, an internal cronjob is responsible for finalizing the transaction.
			commitProgress, err := h.getRevealCommitProgress(ctx, tokenTransaction)
			if err != nil {
				return nil, fmt.Errorf("failed to get transfer reveal commit progress: %w", err)
			}
			return &tokenpb.CommitTransactionResponse{
				CommitStatus:   tokenpb.CommitStatus_COMMIT_PROCESSING,
				CommitProgress: commitProgress,
			}, nil
		}
	default:
		return nil, fmt.Errorf("token transaction type not supported: %s", inferredTxType)
	}
	return nil, nil
}

func (h *SignTokenHandler) exchangeRevocationSecretShares(ctx context.Context, allOperatorSignaturesResponse map[string]*tokeninternalpb.SignTokenTransactionFromCoordinationResponse, tokenTransaction *tokenpb.TokenTransaction, tokenTransactionHash []byte) (map[string]*tokeninternalpb.ExchangeRevocationSecretsSharesResponse, error) {
	ctx, span := tracer.Start(ctx, "SignTokenHandler.exchangeRevocationSecretShares", getTokenTransactionAttributes(tokenTransaction))
	defer span.End()
	// prepare the operator signatures package
	allOperatorSignaturesPackage := make([]*tokeninternalpb.OperatorTransactionSignature, 0, len(allOperatorSignaturesResponse))
	for identifier, sig := range allOperatorSignaturesResponse {
		allOperatorSignaturesPackage = append(allOperatorSignaturesPackage, &tokeninternalpb.OperatorTransactionSignature{
			OperatorIdentityPublicKey: h.config.SigningOperatorMap[identifier].IdentityPublicKey.Serialize(),
			Signature:                 sig.SparkOperatorSignature,
		})
	}

	revocationSecretShares, err := h.prepareRevocationSecretSharesForExchange(ctx, tokenTransaction)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare coordinator revocation secret shares for exchange: %w for token txHash: %x", err, tokenTransactionHash)
	}

	// We are about to reveal our revocation secrets. Mark as revealed, then reveal.
	tx, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}
	if _, err := tx.TokenTransaction.Update().
		Where(
			tokentransaction.StatusNEQ(st.TokenTransactionStatusFinalized),
			tokentransaction.FinalizedTokenTransactionHashEQ(tokenTransactionHash),
		).
		SetStatus(st.TokenTransactionStatusRevealed).
		Save(ctx); err != nil {
		return nil, fmt.Errorf("failed to update token transaction status to Revealed: %w for token txHash: %x", err, tokenTransactionHash)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit and replace transaction after setting status to revealed: %w for token txHash: %x", err, tokenTransactionHash)
	}

	// exchange the revocation secret shares with all other operators
	opSelection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}
	response, errorExchangingWithAllOperators := helper.ExecuteTaskWithAllOperators(ctx, h.config, &opSelection, func(ctx context.Context, operator *so.SigningOperator) (*tokeninternalpb.ExchangeRevocationSecretsSharesResponse, error) {
		conn, err := operator.NewOperatorGRPCConnection()
		if err != nil {
			return nil, fmt.Errorf("failed to connect to operator %s: %w for token txHash: %x", operator.Identifier, err, tokenTransactionHash)
		}
		defer conn.Close()
		client := tokeninternalpb.NewSparkTokenInternalServiceClient(conn)
		return client.ExchangeRevocationSecretsShares(ctx, &tokeninternalpb.ExchangeRevocationSecretsSharesRequest{
			FinalTokenTransaction:         tokenTransaction,
			FinalTokenTransactionHash:     tokenTransactionHash,
			OperatorTransactionSignatures: allOperatorSignaturesPackage,
			OperatorShares:                revocationSecretShares,
			OperatorIdentityPublicKey:     h.config.IdentityPublicKey().Serialize(),
		})
	})
	// If there was an error exchanging with all operators, we will roll back to the revealed status.
	if errorExchangingWithAllOperators != nil {
		return nil, fmt.Errorf("1 failed to exchange revocation secret shares: %w for token txHash: %x", errorExchangingWithAllOperators, tokenTransactionHash)
	}

	return response, nil
}

func (h *SignTokenHandler) prepareRevocationSecretSharesForExchange(ctx context.Context, tokenTransaction *tokenpb.TokenTransaction) ([]*tokeninternalpb.OperatorRevocationShares, error) {
	ctx, span := tracer.Start(ctx, "SignTokenHandler.prepareRevocationSecretSharesForExchange", getTokenTransactionAttributes(tokenTransaction))
	defer span.End()
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	outputsToSpend := tokenTransaction.GetTransferInput().GetOutputsToSpend()

	voutsByPrevHash := make(map[string][]int32)
	hashBytesByKey := make(map[string][]byte)
	for _, outputToSpend := range outputsToSpend {
		if outputToSpend == nil {
			continue
		}
		hashBytes := outputToSpend.GetPrevTokenTransactionHash()
		key := string(hashBytes)
		hashBytesByKey[key] = hashBytes
		vout := int32(outputToSpend.GetPrevTokenTransactionVout())
		// Deduplicate vouts per hash to keep predicates minimal
		existing := voutsByPrevHash[key]
		seen := false
		for _, existingVout := range existing {
			if existingVout == vout {
				seen = true
				break
			}
		}
		if !seen {
			voutsByPrevHash[key] = append(existing, vout)
		}
	}

	var matchOutputsToSpendPredicates []predicate.TokenOutput
	for prevHash, vouts := range voutsByPrevHash {
		hash := hashBytesByKey[prevHash]
		matchOutputsToSpendPredicates = append(matchOutputsToSpendPredicates,
			tokenoutput.And(
				tokenoutput.HasOutputCreatedTokenTransactionWith(
					tokentransaction.FinalizedTokenTransactionHashEQ(hash),
				),
				tokenoutput.CreatedTransactionOutputVoutIn(vouts...),
			),
		)
	}

	const batchSize = queryTokenOutputsWithPartialRevocationSecretSharesBatchSize
	var outputsWithKeyShares []*ent.TokenOutput

	for i := 0; i < len(matchOutputsToSpendPredicates); i += batchSize {
		end := i + batchSize
		if end > len(matchOutputsToSpendPredicates) {
			end = len(matchOutputsToSpendPredicates)
		}
		batchPredicates := matchOutputsToSpendPredicates[i:end]
		batchOutputs, err := db.TokenOutput.Query().
			Where(tokenoutput.Or(batchPredicates...)).
			WithRevocationKeyshare().
			WithTokenPartialRevocationSecretShares().
			All(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to query TokenOutputs batch (%d-%d): %w", i, end-1, err)
		}

		outputsWithKeyShares = append(outputsWithKeyShares, batchOutputs...)
	}

	sharesToReturnMap := make(map[keys.Public]*tokeninternalpb.OperatorRevocationShares)

	allOperatorPubkeys := make([]keys.Public, 0, len(h.config.SigningOperatorMap))
	for _, operator := range h.config.SigningOperatorMap {
		allOperatorPubkeys = append(allOperatorPubkeys, operator.IdentityPublicKey)
	}

	for _, identityPubkey := range allOperatorPubkeys {
		sharesToReturnMap[identityPubkey] = &tokeninternalpb.OperatorRevocationShares{
			OperatorIdentityPublicKey: identityPubkey.Serialize(),
			Shares:                    make([]*tokeninternalpb.RevocationSecretShare, 0, len(tokenTransaction.GetTransferInput().GetOutputsToSpend())),
		}
	}

	for _, outputWithKeyShare := range outputsWithKeyShares {
		if keyshare := outputWithKeyShare.Edges.RevocationKeyshare; keyshare != nil {
			if operatorShares, exists := sharesToReturnMap[h.config.IdentityPublicKey()]; exists {
				operatorShares.Shares = append(operatorShares.Shares, &tokeninternalpb.RevocationSecretShare{
					InputTtxoId: outputWithKeyShare.ID.String(),
					SecretShare: keyshare.SecretShare,
				})
			}
		}
		if outputWithKeyShare.Edges.TokenPartialRevocationSecretShares != nil {
			for _, partialShare := range outputWithKeyShare.Edges.TokenPartialRevocationSecretShares {
				if operatorShares, exists := sharesToReturnMap[partialShare.OperatorIdentityPublicKey]; exists {
					operatorShares.Shares = append(operatorShares.Shares, &tokeninternalpb.RevocationSecretShare{
						InputTtxoId: outputWithKeyShare.ID.String(),
						SecretShare: partialShare.SecretShare,
					})
				}
			}
		}
	}

	operatorRevocationShares := make([]*tokeninternalpb.OperatorRevocationShares, 0, len(sharesToReturnMap))
	for _, operatorShares := range sharesToReturnMap {
		operatorRevocationShares = append(operatorRevocationShares, operatorShares)
	}

	return operatorRevocationShares, nil
}

func (h *SignTokenHandler) localSignAndCommitTransaction(
	ctx context.Context,
	foundOperatorSignatures *tokenpb.InputTtxoSignaturesPerOperator,
	finalTokenTransactionHash []byte,
	tokenTransaction *ent.TokenTransaction,
) (*tokeninternalpb.SignTokenTransactionFromCoordinationResponse, error) {
	ctx, span := tracer.Start(ctx, "SignTokenHandler.localSignAndCommitTransaction", getTokenTransactionAttributesFromEnt(ctx, tokenTransaction, h.config))
	defer span.End()
	operatorSpecificSignatures := convertTokenProtoSignaturesToOperatorSpecific(
		foundOperatorSignatures.TtxoSignatures,
		finalTokenTransactionHash,
		h.config.IdentityPublicKey(),
	)
	internalSignTokenHandler := NewInternalSignTokenHandler(h.config)
	sigBytes, err := internalSignTokenHandler.SignAndPersistTokenTransaction(ctx, tokenTransaction, finalTokenTransactionHash, operatorSpecificSignatures)
	if err != nil {
		return nil, err
	}
	return &tokeninternalpb.SignTokenTransactionFromCoordinationResponse{
		SparkOperatorSignature: sigBytes,
	}, nil
}

// getRevocationKeysharesForTokenTransaction retrieves the revocation keyshares for a token transaction
func (h *SignTokenHandler) getRevocationKeysharesForTokenTransaction(ctx context.Context, tokenTransaction *ent.TokenTransaction) ([]*sparkpb.KeyshareWithIndex, error) {
	spentOutputs := tokenTransaction.Edges.SpentOutput
	revocationKeyshares := make([]*sparkpb.KeyshareWithIndex, len(spentOutputs))
	for i, output := range spentOutputs {
		keyshare, err := output.QueryRevocationKeyshare().Only(ctx)
		if err != nil {
			return nil, tokens.FormatErrorWithTransactionEnt(tokens.ErrFailedToGetKeyshareForOutput, tokenTransaction, err)
		}
		// Validate that the keyshare's public key is as expected.
		withdrawRevocationCommitment, err := keys.ParsePublicKey(output.WithdrawRevocationCommitment)
		if err != nil {
			return nil, fmt.Errorf("failed to parse withdraw revocation commitment: %w", err)
		}
		if !keyshare.PublicKey.Equals(withdrawRevocationCommitment) {
			return nil, tokens.FormatErrorWithTransactionEnt(
				fmt.Sprintf("%s: %v does not match %v", tokens.ErrRevocationKeyMismatch, keyshare.PublicKey, output.WithdrawRevocationCommitment),
				tokenTransaction, nil)
		}

		revocationKeyshares[i] = &sparkpb.KeyshareWithIndex{
			InputIndex: uint32(output.SpentTransactionInputVout),
			Keyshare:   keyshare.SecretShare,
		}
	}
	// Sort spent output keyshares by their index to ensure a consistent response
	slices.SortFunc(revocationKeyshares, func(a, b *sparkpb.KeyshareWithIndex) int {
		return cmp.Compare(a.InputIndex, b.InputIndex)
	})

	return revocationKeyshares, nil
}

func validateTokenTransactionForSigning(config *so.Config, tokenTransactionEnt *ent.TokenTransaction) error {
	if tokenTransactionEnt.Status != st.TokenTransactionStatusStarted &&
		tokenTransactionEnt.Status != st.TokenTransactionStatusSigned {
		return fmt.Errorf("signing failed because transaction is not in correct state, expected %s or %s, current status: %s", st.TokenTransactionStatusStarted, st.TokenTransactionStatusSigned, tokenTransactionEnt.Status)
	}

	// Get the network-specific transaction expiry duration
	schemaNetwork, err := tokenTransactionEnt.GetNetworkFromEdges()
	if err != nil {
		return fmt.Errorf("failed to get network from edges: %w", err)
	}
	network, err := common.NetworkFromSchemaNetwork(schemaNetwork)
	if err != nil {
		return fmt.Errorf("failed to get network from schema network: %w", err)
	}
	transactionV0ExpiryDuration := config.Lrc20Configs[network.String()].TransactionExpiryDuration

	if err := tokenTransactionEnt.ValidateNotExpired(transactionV0ExpiryDuration); err != nil {
		return err
	}
	return nil
}

// verifyOperatorSignatures verifies the signatures from each operator for a token transaction.
func verifyOperatorSignatures(
	signatures map[string][]byte,
	operatorMap map[string]*so.SigningOperator,
	finalTokenTransactionHash []byte,
) error {
	var errors []string
	for operatorID, sigBytes := range signatures {
		operator, ok := operatorMap[operatorID]
		if !ok {
			return fmt.Errorf("operator %s not found in operator map", operatorID)
		}
		if err := verifyOperatorSignature(sigBytes, operator, finalTokenTransactionHash); err != nil {
			errors = append(errors, err.Error())
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("signature verification failed: %s", strings.Join(errors, "; "))
	}

	return nil
}

func verifyOperatorSignature(sigBytes []byte, operator *so.SigningOperator, finalTokenTransactionHash []byte) error {
	pubKey := operator.IdentityPublicKey
	if err := common.VerifyECDSASignature(pubKey, sigBytes, finalTokenTransactionHash); err != nil {
		return fmt.Errorf("failed to verify operator signature for operator %s: %w", operator.Identifier, err)
	}
	return nil
}

func (h *SignTokenHandler) getSignedCommitProgress(_ context.Context, tt *ent.TokenTransaction) (*tokenpb.CommitProgress, error) {
	peerSigs := tt.Edges.PeerSignatures
	if peerSigs == nil {
		return nil, fmt.Errorf("no peer signatures")
	}

	seen := map[keys.Public]struct{}{}
	for _, ps := range peerSigs {
		operatorPublicKey, err := keys.ParsePublicKey(ps.OperatorIdentityPublicKey)
		if err != nil {
			return nil, err
		}
		seen[operatorPublicKey] = struct{}{}
	}

	self := h.config.IdentityPublicKey()
	seen[self] = struct{}{}

	var committed, uncommitted [][]byte
	for _, operator := range h.config.SigningOperatorMap {
		operatorPublicKey := operator.IdentityPublicKey
		if _, ok := seen[operatorPublicKey]; ok {
			committed = append(committed, operatorPublicKey.Serialize())
		} else {
			uncommitted = append(uncommitted, operatorPublicKey.Serialize())
		}
	}

	return &tokenpb.CommitProgress{
		CommittedOperatorPublicKeys:   committed,
		UncommittedOperatorPublicKeys: uncommitted,
	}, nil
}

// getRevealCommitProgress determines which operators have provided their secret shares to this SO for the transaction.
func (h *SignTokenHandler) getRevealCommitProgress(ctx context.Context, tokenTransaction *ent.TokenTransaction) (*tokenpb.CommitProgress, error) {
	// Get all known operator public keys
	allOperatorPubKeys := make([]keys.Public, 0, len(h.config.SigningOperatorMap))
	for _, operator := range h.config.SigningOperatorMap {
		allOperatorPubKeys = append(allOperatorPubKeys, operator.IdentityPublicKey)
	}

	// Determine which operators have provided their secret shares for each output
	operatorSharesPerOutput := make(map[int]map[keys.Public]struct{}) // output_index -> operator_key -> has_share
	coordinatorKey := h.config.IdentityPublicKey()

	outputsToCheck := tokenTransaction.Edges.SpentOutput
	if len(outputsToCheck) == 0 {
		return nil, fmt.Errorf("no spent outputs found for transfer token transaction %x", tokenTransaction.FinalizedTokenTransactionHash)
	}

	for i := range outputsToCheck {
		operatorSharesPerOutput[i] = make(map[keys.Public]struct{})
	}

	for i, output := range outputsToCheck {
		logger := logging.GetLoggerFromContext(ctx)
		logger.Sugar().Infof("Checking output %d for revocation keyshare (has keyshare: %t)", i, output.Edges.RevocationKeyshare != nil)

		if output.Edges.RevocationKeyshare != nil {
			logger.Sugar().Infof("Found revocation keyshare, marking coordinator %s as revealed for output %d", coordinatorKey.ToHex(), i)
			operatorSharesPerOutput[i][coordinatorKey] = struct{}{}
		}
		if output.Edges.TokenPartialRevocationSecretShares != nil {
			for _, partialShare := range output.Edges.TokenPartialRevocationSecretShares {
				operatorSharesPerOutput[i][partialShare.OperatorIdentityPublicKey] = struct{}{}
			}
		}
	}

	operatorsWithAllShares := make(map[keys.Public]struct{})
	for _, operatorKey := range allOperatorPubKeys {
		hasAllShares := true
		for i := range outputsToCheck {
			if _, exists := operatorSharesPerOutput[i][operatorKey]; !exists {
				hasAllShares = false
				break
			}
		}
		if hasAllShares {
			operatorsWithAllShares[operatorKey] = struct{}{}
		}
	}

	var committedOperatorPublicKeys [][]byte
	var uncommittedOperatorPublicKeys [][]byte
	for _, operatorKey := range allOperatorPubKeys {
		if _, hasAllShares := operatorsWithAllShares[operatorKey]; hasAllShares {
			committedOperatorPublicKeys = append(committedOperatorPublicKeys, operatorKey.Serialize())
		} else {
			uncommittedOperatorPublicKeys = append(uncommittedOperatorPublicKeys, operatorKey.Serialize())
		}
	}

	return &tokenpb.CommitProgress{
		CommittedOperatorPublicKeys:   committedOperatorPublicKeys,
		UncommittedOperatorPublicKeys: uncommittedOperatorPublicKeys,
	}, nil
}

// convertTokenProtoSignaturesToOperatorSpecific converts token proto signatures to OperatorSpecificOwnerSignature format
func convertTokenProtoSignaturesToOperatorSpecific(
	ttxoSignatures []*tokenpb.SignatureWithIndex,
	finalTokenTransactionHash []byte,
	operatorIdentityPublicKey keys.Public,
) []*sparkpb.OperatorSpecificOwnerSignature {
	operatorSpecificSignatures := make([]*sparkpb.OperatorSpecificOwnerSignature, 0, len(ttxoSignatures))
	for _, operatorSignatures := range ttxoSignatures {
		operatorSpecificSignatures = append(operatorSpecificSignatures, &sparkpb.OperatorSpecificOwnerSignature{
			OwnerSignature: protoconverter.SparkSignatureWithIndexFromTokenProto(operatorSignatures),
			Payload: &sparkpb.OperatorSpecificTokenTransactionSignablePayload{
				FinalTokenTransactionHash: finalTokenTransactionHash,
				OperatorIdentityPublicKey: operatorIdentityPublicKey.Serialize(),
			},
		})
	}
	return operatorSpecificSignatures
}
