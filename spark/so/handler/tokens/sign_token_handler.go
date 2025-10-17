package tokens

import (
	"bytes"
	"cmp"
	"context"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/lightsparkdev/spark/common/keys"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/logging"
	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	tokeninternalpb "github.com/lightsparkdev/spark/proto/spark_token_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authz"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/tokenoutput"
	"github.com/lightsparkdev/spark/so/ent/tokentransaction"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
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
	idPubKey, err := keys.ParsePublicKey(req.GetIdentityPublicKey())
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedKey(err)
	}
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, idPubKey); err != nil {
		return nil, err
	}

	tokenProtoTokenTransaction, err := protoconverter.TokenProtoFromSparkTokenTransaction(req.FinalTokenTransaction)
	if err != nil {
		return nil, sparkerrors.InternalTypeConversionError(err)
	}
	ctx, span := GetTracer().Start(ctx, "SignTokenHandler.SignTokenTransaction", GetProtoTokenTransactionTraceAttributes(ctx, tokenProtoTokenTransaction))
	defer span.End()

	finalTokenTransactionHash, err := utils.HashTokenTransaction(tokenProtoTokenTransaction, false)
	if err != nil {
		return nil, tokens.FormatErrorWithTransactionProto("failed to hash final token transaction", tokenProtoTokenTransaction, err)
	}

	tokenTransaction, err := ent.FetchAndLockTokenTransactionData(ctx, tokenProtoTokenTransaction)
	if err != nil {
		return nil, err
	}

	internalSignTokenHandler := NewInternalSignTokenHandler(h.config)
	operatorSignature, err := internalSignTokenHandler.SignAndPersistTokenTransaction(ctx, tokenTransaction, finalTokenTransactionHash, req.OperatorSpecificSignatures)
	if err != nil {
		return nil, err
	}

	if tokenTransaction.Status == st.TokenTransactionStatusSigned {
		revocationKeyshares, err := h.getRevocationKeysharesForTokenTransaction(ctx, tokenTransaction)
		if err != nil {
			return nil, sparkerrors.InternalDatabaseReadError(tokens.FormatErrorWithTransactionEnt(tokens.ErrFailedToGetRevocationKeyshares, tokenTransaction, err))
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
			return nil, sparkerrors.InternalDatabaseReadError(tokens.FormatErrorWithTransactionEnt(tokens.ErrFailedToGetKeyshareForOutput, tokenTransaction, err))
		}
		index := output.SpentTransactionInputVout
		keyshares[index] = keyshare
		revocationKeyshares[index] = &sparkpb.KeyshareWithIndex{
			InputIndex: uint32(index),
			Keyshare:   keyshare.SecretShare.Serialize(),
		}

		// Validate that the keyshare's public key is as expected.
		withdrawRevocationCommitment, err := keys.ParsePublicKey(output.WithdrawRevocationCommitment)
		if err != nil {
			return nil, sparkerrors.InvalidArgumentMalformedKey(err)
		}
		if !keyshare.PublicKey.Equals(withdrawRevocationCommitment) {
			return nil, sparkerrors.InvalidArgumentPublicKeyMismatch(fmt.Errorf("keyshare public key %v does not match output revocation commitment %v", keyshare.PublicKey, withdrawRevocationCommitment))
		}
	}

	return &sparkpb.SignTokenTransactionResponse{
		SparkOperatorSignature: operatorSignature,
		RevocationKeyshares:    revocationKeyshares,
	}, nil
}

func (h *SignTokenHandler) CommitTransaction(ctx context.Context, req *tokenpb.CommitTransactionRequest) (*tokenpb.CommitTransactionResponse, error) {
	ctx, span := GetTracer().Start(ctx, "SignTokenHandler.CommitTransaction", GetProtoTokenTransactionTraceAttributes(ctx, req.FinalTokenTransaction))
	defer span.End()
	ownerIDPubKey, err := keys.ParsePublicKey(req.GetOwnerIdentityPublicKey())
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedKey(err)
	}
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, ownerIDPubKey); err != nil {
		return nil, err
	}

	calculatedHash, err := utils.HashTokenTransaction(req.FinalTokenTransaction, false)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(calculatedHash, req.FinalTokenTransactionHash) {
		return nil, sparkerrors.FailedPreconditionHashMismatch(fmt.Errorf("transaction hash mismatch: expected %x, got %x", calculatedHash, req.FinalTokenTransactionHash))
	}

	tokenTransaction, err := ent.FetchTokenTransactionDataByHashForRead(ctx, req.FinalTokenTransactionHash)
	if err != nil {
		return nil, err
	}

	inferredTxType := tokenTransaction.InferTokenTransactionTypeEnt()
	// Check if we should return early without further processing
	if response, err := h.checkShouldReturnEarlyWithoutProcessing(ctx, tokenTransaction, inferredTxType); response != nil || err != nil {
		return response, err
	}

	if err := validateTokenTransactionForSigning(ctx, h.config, tokenTransaction); err != nil {
		return nil, err
	}

	inputSignaturesByOperatorHex := make(map[string]*tokenpb.InputTtxoSignaturesPerOperator, len(req.InputTtxoSignaturesPerOperator))
	for _, opSigs := range req.InputTtxoSignaturesPerOperator {
		if opSigs == nil || len(opSigs.OperatorIdentityPublicKey) == 0 {
			continue
		}
		inputSignaturesByOperatorHex[hex.EncodeToString(opSigs.OperatorIdentityPublicKey)] = opSigs
	}
	selfHex := h.config.IdentityPublicKey().ToHex()
	selfSignatures, ok := inputSignaturesByOperatorHex[selfHex]
	if !ok {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("no signatures found for local operator %s", h.config.Identifier))
	}

	excludeSelf := helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}
	internalSignatures, err := helper.ExecuteTaskWithAllOperators(ctx, h.config, &excludeSelf,
		func(ctx context.Context, operator *so.SigningOperator) (*tokeninternalpb.SignTokenTransactionFromCoordinationResponse, error) {
			opHex := operator.IdentityPublicKey.ToHex()
			foundOperatorSignatures := inputSignaturesByOperatorHex[opHex]
			if foundOperatorSignatures == nil {
				return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("no signatures found for operator %s", operator.Identifier))
			}
			conn, err := operator.NewOperatorGRPCConnection()
			if err != nil {
				return nil, sparkerrors.UnavailableExternalOperator(fmt.Errorf("failed to connect to operator %s: %w", operator.Identifier, err))
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
		return nil, sparkerrors.WrapErrorWithReasonPrefix(tokens.FormatErrorWithTransactionEnt("failed to get signatures from operators", tokenTransaction, err),
			sparkerrors.ErrorReasonPrefixFailedWithExternalCoordinator)
	}

	lockedTokenTransaction, err := ent.FetchAndLockTokenTransactionData(ctx, req.FinalTokenTransaction)
	if err != nil {
		return nil, err
	}
	if err := validateTokenTransactionForSigning(ctx, h.config, lockedTokenTransaction); err != nil {
		return nil, err
	}
	localResp, err := h.localSignAndCommitTransaction(ctx, selfSignatures, req.FinalTokenTransactionHash, lockedTokenTransaction)
	if err != nil {
		return nil, err
	}

	signatures := make(operatorSignaturesMap, len(internalSignatures))
	signatures[h.config.Identifier] = localResp.SparkOperatorSignature
	for operatorID, sig := range internalSignatures {
		signatures[operatorID] = sig.SparkOperatorSignature
	}
	internalSignTokenHandler := NewInternalSignTokenHandler(h.config)
	if err := internalSignTokenHandler.validateSignaturesPackageAndPersistPeerSignatures(ctx, signatures, lockedTokenTransaction); err != nil {
		return nil, err
	}

	switch inferredTxType {
	case utils.TokenTransactionTypeCreate, utils.TokenTransactionTypeMint:
		// We validated the signatures package above, so we know that it is finalized.
		return finalizedCommitTransactionResponse, nil
	case utils.TokenTransactionTypeTransfer:
		// Include the coordinator's own signature when exchanging shares so peers validate against all operators
		allOperatorSignatures := make(map[string]*tokeninternalpb.SignTokenTransactionFromCoordinationResponse, len(internalSignatures)+1)
		for k, v := range internalSignatures {
			allOperatorSignatures[k] = v
		}
		allOperatorSignatures[h.config.Identifier] = localResp
		if response, err := h.ExchangeRevocationSecretsAndFinalizeIfPossible(ctx, req.FinalTokenTransaction, allOperatorSignatures, req.FinalTokenTransactionHash); err != nil {
			return nil, err
		} else {
			return response, nil
		}
	default:
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("token transaction type not supported: %s", inferredTxType))
	}
}

func (h *SignTokenHandler) ExchangeRevocationSecretsAndFinalizeIfPossible(ctx context.Context, tokenTransactionProto *tokenpb.TokenTransaction, allOperatorSignatures map[string]*tokeninternalpb.SignTokenTransactionFromCoordinationResponse, tokenTransactionHash []byte) (*tokenpb.CommitTransactionResponse, error) {
	ctx, span := GetTracer().Start(ctx, "SignTokenHandler.ExchangeRevocationSecretsAndFinalizeIfPossible", GetProtoTokenTransactionTraceAttributes(ctx, tokenTransactionProto))
	defer span.End()
	logger := logging.GetLoggerFromContext(ctx)
	response, err := h.exchangeRevocationSecretShares(ctx, allOperatorSignatures, tokenTransactionProto, tokenTransactionHash)
	if err != nil {
		return nil, tokens.FormatErrorWithTransactionProto("coordinator failed to exchange revocation secret shares with all other operators", tokenTransactionProto, err)
	}

	// Collect the secret shares from all operators.
	var operatorShares []*tokeninternalpb.OperatorRevocationShares
	for _, exchangeResponse := range response {
		if exchangeResponse == nil {
			return nil, tokens.FormatErrorWithTransactionProto("nil exchange response received from operator", tokenTransactionProto, sparkerrors.InternalInvalidOperatorResponse(err))
		}
		operatorShares = append(operatorShares, exchangeResponse.ReceivedOperatorShares...)
	}
	inputOperatorShareMap, err := buildInputOperatorShareMap(operatorShares)
	if err != nil {
		return nil, tokens.FormatErrorWithTransactionProto("failed to build input operator share map", tokenTransactionProto, err)
	}
	logger.Sugar().Infof("Length of inputOperatorShareMap: %d", len(inputOperatorShareMap))
	// Persist the secret shares from all operators.
	internalHandler := NewInternalSignTokenHandler(h.config)
	finalized, err := internalHandler.persistPartialRevocationSecretShares(ctx, inputOperatorShareMap, tokenTransactionHash)
	if err != nil {
		return nil, tokens.FormatErrorWithTransactionProto("failed to persist partial revocation secret shares", tokenTransactionProto, err)
	}

	if finalized {
		_, err := h.exchangeRevocationSecretShares(ctx, allOperatorSignatures, tokenTransactionProto, tokenTransactionHash)
		if err != nil {
			return nil, tokens.FormatErrorWithTransactionProto("failed to exchange revocation secret shares after finalization", tokenTransactionProto, err)
		}
		return finalizedCommitTransactionResponse, nil

	} else {
		// Refetch the token transaction (for-read) to pick up newly committed partial revocation secret shares
		refetchedTokenTransaction, err := ent.FetchTokenTransactionDataByHashForRead(ctx, tokenTransactionHash)
		if err != nil {
			return nil, tokens.FormatErrorWithTransactionProto("failed to fetch token transaction after finalization", tokenTransactionProto, err)
		}

		commitProgress, err := h.getRevealCommitProgress(ctx, refetchedTokenTransaction)
		if err != nil {
			return nil, tokens.FormatErrorWithTransactionProto("failed to get reveal commit progress", tokenTransactionProto, err)
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
			commitProgress, err := h.getSignedCommitProgress(tokenTransaction)
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
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("token transaction type not supported: %s", inferredTxType))
	}
	return nil, nil
}

func (h *SignTokenHandler) exchangeRevocationSecretShares(ctx context.Context, allOperatorSignaturesResponse map[string]*tokeninternalpb.SignTokenTransactionFromCoordinationResponse, tokenTransaction *tokenpb.TokenTransaction, tokenTransactionHash []byte) (map[string]*tokeninternalpb.ExchangeRevocationSecretsSharesResponse, error) {
	ctx, span := GetTracer().Start(ctx, "SignTokenHandler.exchangeRevocationSecretShares", GetProtoTokenTransactionTraceAttributes(ctx, tokenTransaction))
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
		return nil, fmt.Errorf("failed to prepare coordinator revocation secret shares for exchange: %w", err)
	}

	// We are about to reveal our revocation secrets. Mark as revealed, then reveal.
	tx, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, sparkerrors.InternalDatabaseTransactionLifecycleError(fmt.Errorf("failed to get or create current tx for request: %w", err))
	}
	if _, err := tx.TokenTransaction.Update().
		Where(
			tokentransaction.StatusNEQ(st.TokenTransactionStatusFinalized),
			tokentransaction.FinalizedTokenTransactionHashEQ(tokenTransactionHash),
		).
		SetStatus(st.TokenTransactionStatusRevealed).
		Save(ctx); err != nil {
		return nil, sparkerrors.InternalDatabaseWriteError(fmt.Errorf("failed to update token transaction status to Revealed: %w for token txHash: %x", err, tokenTransactionHash))
	}
	if err := tx.Commit(); err != nil {
		return nil, sparkerrors.InternalDatabaseTransactionLifecycleError(fmt.Errorf("failed to commit and replace transaction after setting status to revealed: %w for token txHash: %x", err, tokenTransactionHash))
	}

	// exchange the revocation secret shares with all other operators
	opSelection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}
	response, errorExchangingWithAllOperators := helper.ExecuteTaskWithAllOperators(ctx, h.config, &opSelection, func(ctx context.Context, operator *so.SigningOperator) (*tokeninternalpb.ExchangeRevocationSecretsSharesResponse, error) {
		conn, err := operator.NewOperatorGRPCConnection()
		if err != nil {
			return nil, sparkerrors.UnavailableExternalOperator(fmt.Errorf("failed to connect to operator %s: %w for token txHash: %x", operator.Identifier, err, tokenTransactionHash))
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
		return nil, sparkerrors.WrapErrorWithMessage(errorExchangingWithAllOperators, fmt.Sprintf("failed to exchange revocation secret shares for token txHash: %x", tokenTransactionHash))
	}

	return response, nil
}

func (h *SignTokenHandler) prepareRevocationSecretSharesForExchange(ctx context.Context, tokenTransaction *tokenpb.TokenTransaction) ([]*tokeninternalpb.OperatorRevocationShares, error) {
	ctx, span := GetTracer().Start(ctx, "SignTokenHandler.prepareRevocationSecretSharesForExchange", GetProtoTokenTransactionTraceAttributes(ctx, tokenTransaction))
	defer span.End()
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, sparkerrors.InternalDatabaseTransactionLifecycleError(fmt.Errorf("failed to get or create current tx for request: %w", err))
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

	// Get all distinct transaction hashes for batch lookup
	var distinctTxHashes [][]byte
	for _, hashBytes := range hashBytesByKey {
		distinctTxHashes = append(distinctTxHashes, hashBytes)
	}

	transactions, err := db.TokenTransaction.Query().
		Where(tokentransaction.FinalizedTokenTransactionHashIn(distinctTxHashes...)).
		WithCreatedOutput().
		All(ctx)
	if err != nil {
		return nil, sparkerrors.InternalDatabaseReadError(fmt.Errorf("failed to fetch matching transactions and outputs: %w", err))
	}

	transactionMap := make(map[string]*ent.TokenTransaction)
	for _, tx := range transactions {
		hashKey := string(tx.FinalizedTokenTransactionHash)
		transactionMap[hashKey] = tx
	}

	var outputIDs []uuid.UUID
	for prevHash, vouts := range voutsByPrevHash {
		transaction, ok := transactionMap[prevHash]
		if !ok {
			return nil, sparkerrors.NotFoundMissingEntity(fmt.Errorf("no transaction found for prev tx hash %x", hashBytesByKey[prevHash]))
		}

		// Find matching outputs by vout
		for _, createdOutput := range transaction.Edges.CreatedOutput {
			for _, vout := range vouts {
				if createdOutput.CreatedTransactionOutputVout == vout {
					outputIDs = append(outputIDs, createdOutput.ID)
					break
				}
			}
		}
	}

	outputsWithKeyShares, err := db.TokenOutput.Query().
		Where(tokenoutput.IDIn(outputIDs...)).
		WithRevocationKeyshare().
		WithTokenPartialRevocationSecretShares().
		All(ctx)
	if err != nil {
		return nil, sparkerrors.InternalDatabaseReadError(fmt.Errorf("failed to query TokenOutputs with key shares: %w", err))
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
					SecretShare: keyshare.SecretShare.Serialize(),
				})
			}
		}
		if outputWithKeyShare.Edges.TokenPartialRevocationSecretShares != nil {
			for _, partialShare := range outputWithKeyShare.Edges.TokenPartialRevocationSecretShares {
				if operatorShares, exists := sharesToReturnMap[partialShare.OperatorIdentityPublicKey]; exists {
					operatorShares.Shares = append(operatorShares.Shares, &tokeninternalpb.RevocationSecretShare{
						InputTtxoId: outputWithKeyShare.ID.String(),
						SecretShare: partialShare.SecretShare.Serialize(),
					})
				}
			}
		}
	}

	return slices.Collect(maps.Values(sharesToReturnMap)), nil
}

func (h *SignTokenHandler) localSignAndCommitTransaction(
	ctx context.Context,
	foundOperatorSignatures *tokenpb.InputTtxoSignaturesPerOperator,
	finalTokenTransactionHash []byte,
	tokenTransaction *ent.TokenTransaction,
) (*tokeninternalpb.SignTokenTransactionFromCoordinationResponse, error) {
	ctx, span := GetTracer().Start(ctx, "SignTokenHandler.localSignAndCommitTransaction", GetEntTokenTransactionTraceAttributes(ctx, tokenTransaction))
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
			return nil, sparkerrors.InternalDatabaseReadError(tokens.FormatErrorWithTransactionEnt(tokens.ErrFailedToGetKeyshareForOutput, tokenTransaction, err))
		}
		// Validate that the keyshare's public key is as expected.
		withdrawRevocationCommitment, err := keys.ParsePublicKey(output.WithdrawRevocationCommitment)
		if err != nil {
			return nil, sparkerrors.InternalObjectMalformedField(fmt.Errorf("failed to parse withdraw revocation commitment: %w", err))
		}
		if !keyshare.PublicKey.Equals(withdrawRevocationCommitment) {
			return nil, sparkerrors.InternalKeyshareError(tokens.FormatErrorWithTransactionEnt(
				fmt.Sprintf("%s: %v does not match %v", tokens.ErrRevocationKeyMismatch, keyshare.PublicKey, output.WithdrawRevocationCommitment),
				tokenTransaction, nil))
		}

		revocationKeyshares[i] = &sparkpb.KeyshareWithIndex{
			InputIndex: uint32(output.SpentTransactionInputVout),
			Keyshare:   keyshare.SecretShare.Serialize(),
		}
	}
	// Sort spent output keyshares by their index to ensure a consistent response
	slices.SortFunc(revocationKeyshares, func(a, b *sparkpb.KeyshareWithIndex) int {
		return cmp.Compare(a.InputIndex, b.InputIndex)
	})

	return revocationKeyshares, nil
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
			return sparkerrors.InternalObjectMalformedField(fmt.Errorf("operator %s not found in operator map", operatorID))
		}
		if err := verifyOperatorSignature(sigBytes, operator, finalTokenTransactionHash); err != nil {
			errors = append(errors, err.Error())
		}
	}

	if len(errors) > 0 {
		return sparkerrors.FailedPreconditionBadSignature(fmt.Errorf("signature verification failed: %s", strings.Join(errors, "; ")))
	}

	return nil
}

func verifyOperatorSignature(sigBytes []byte, operator *so.SigningOperator, finalTokenTransactionHash []byte) error {
	pubKey := operator.IdentityPublicKey
	if err := common.VerifyECDSASignature(pubKey, sigBytes, finalTokenTransactionHash); err != nil {
		return sparkerrors.FailedPreconditionBadSignature(fmt.Errorf("failed to verify operator signature for operator %s: %w", operator.Identifier, err))
	}
	return nil
}

func (h *SignTokenHandler) getSignedCommitProgress(tt *ent.TokenTransaction) (*tokenpb.CommitProgress, error) {
	peerSigs := tt.Edges.PeerSignatures
	if peerSigs == nil {
		return nil, sparkerrors.InternalDatabaseMissingEdge(fmt.Errorf("no peer signatures"))
	}

	seen := map[keys.Public]struct{}{}
	for _, ps := range peerSigs {
		seen[ps.OperatorIdentityPublicKey] = struct{}{}
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
		return nil, sparkerrors.InternalDatabaseMissingEdge(fmt.Errorf("no spent outputs found for transfer token transaction %x", tokenTransaction.FinalizedTokenTransactionHash))
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
