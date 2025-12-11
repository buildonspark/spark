package tokens

import (
	"bytes"
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

	if err := validateTokenTransactionForSigning(ctx, h.config, tokenTransaction, req.FinalTokenTransaction); err != nil {
		return nil, err
	}

	requireInputSignatures := req.GetFinalTokenTransaction().GetVersion() < 3
	inputSignaturesByOperatorHex := make(map[string]*tokenpb.InputTtxoSignaturesPerOperator, len(req.InputTtxoSignaturesPerOperator))
	for _, opSigs := range req.InputTtxoSignaturesPerOperator {
		if opSigs == nil || len(opSigs.OperatorIdentityPublicKey) == 0 {
			continue
		}
		inputSignaturesByOperatorHex[hex.EncodeToString(opSigs.OperatorIdentityPublicKey)] = opSigs
	}
	selfHex := h.config.IdentityPublicKey().ToHex()
	operatorSpecificSignatures := inputSignaturesByOperatorHex[selfHex]
	if operatorSpecificSignatures == nil && requireInputSignatures {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("no signatures found for local operator %s", h.config.Identifier))
	}

	excludeSelf := helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}
	internalSignatures, err := helper.ExecuteTaskWithAllOperators(ctx, h.config, &excludeSelf,
		func(ctx context.Context, operator *so.SigningOperator) (*tokeninternalpb.SignTokenTransactionFromCoordinationResponse, error) {
			opHex := operator.IdentityPublicKey.ToHex()
			foundOperatorSignatures := inputSignaturesByOperatorHex[opHex]
			if requireInputSignatures && foundOperatorSignatures == nil {
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
	if err := validateTokenTransactionForSigning(ctx, h.config, lockedTokenTransaction, req.FinalTokenTransaction); err != nil {
		return nil, err
	}
	localResp, err := h.localSignAndCommitTransaction(
		ctx,
		operatorSpecificSignatures,
		req.FinalTokenTransactionHash,
		lockedTokenTransaction,
		req.FinalTokenTransaction,
	)
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
	case utils.TokenTransactionTypeCreate:
		// We validated the signatures package above, so we know that it is finalized.
		tokenCreate, err := lockedTokenTransaction.Edges.CreateOrErr()
		if err != nil {
			return nil, sparkerrors.InternalDatabaseReadError(fmt.Errorf("token create edge not eager loaded or not found: %w", err))
		}
		finalizedCommitTransactionResponse.TokenIdentifier = tokenCreate.TokenIdentifier
		return finalizedCommitTransactionResponse, nil
	case utils.TokenTransactionTypeMint:
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
	logger.Sugar().Infof("Length of inputOperatorShareMap built from first exchange response: %d", len(inputOperatorShareMap))
	// Persist the secret shares from all operators.
	internalHandler := NewInternalSignTokenHandler(h.config)
	finalized, err := internalHandler.persistPartialRevocationSecretShares(ctx, inputOperatorShareMap, tokenTransactionHash)
	if err != nil {
		return nil, tokens.FormatErrorWithTransactionProto("failed to persist partial revocation secret shares", tokenTransactionProto, err)
	}

	if finalized {
		logger.Sugar().Infof("Operator %s has finalized token transaction %s, exchanging full revocation secret shares with all operators", h.config.Identifier, hex.EncodeToString(tokenTransactionHash))
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

func (h *SignTokenHandler) TryFinalizeRevealedTokenTransaction(ctx context.Context, tokenTransaction *ent.TokenTransaction) error {
	logger := logging.GetLoggerFromContext(ctx)

	if tokenTransaction.Status != st.TokenTransactionStatusRevealed {
		return sparkerrors.FailedPreconditionInvalidState(fmt.Errorf(
			"failed to finalize revealed token transaction: must be %s (was: %s), txHash: %x, ", st.TokenTransactionStatusRevealed, tokenTransaction.Status, tokenTransaction.FinalizedTokenTransactionHash))
	}

	// attempt to internally finalize the transaciton
	internalSignTokenHandler := NewInternalSignTokenHandler(h.config)
	finalized, err := internalSignTokenHandler.RecoverFullRevocationSecretsAndFinalize(ctx, tokenTransaction)
	if err != nil {
		return fmt.Errorf("failed to internally recover full revocation secrets and finalize token transaction: %w", err)
	}
	if finalized {
		logger.Sugar().Infof("Successfully finalized token transaction %s", tokenTransaction.ID)
		return nil
	}

	// if the transaction was not finalized internally, attempt to finalize with all operators
	signaturesPackage := make(map[string]*tokeninternalpb.SignTokenTransactionFromCoordinationResponse)
	if tokenTransaction.Edges.PeerSignatures != nil {
		for _, signature := range tokenTransaction.Edges.PeerSignatures {
			identifier := h.config.GetOperatorIdentifierFromIdentityPublicKey(signature.OperatorIdentityPublicKey)
			signaturesPackage[identifier] = &tokeninternalpb.SignTokenTransactionFromCoordinationResponse{
				SparkOperatorSignature: signature.Signature,
			}
		}
	}
	if tokenTransaction.OperatorSignature != nil {
		signaturesPackage[h.config.Identifier] = &tokeninternalpb.SignTokenTransactionFromCoordinationResponse{
			SparkOperatorSignature: tokenTransaction.OperatorSignature,
		}
	}

	tokenPb, err := tokenTransaction.MarshalProto(ctx, h.config)
	if err != nil {
		return sparkerrors.InternalDatabaseTransactionLifecycleError(fmt.Errorf("failed to marshal parent transaction: %w", err))
	}
	logger.Sugar().Infof("Exchanging revocation secrets and finalizing if possible for token transaction %s with txHash: %x", tokenTransaction.ID, tokenTransaction.FinalizedTokenTransactionHash)

	_, err = h.ExchangeRevocationSecretsAndFinalizeIfPossible(ctx, tokenPb, signaturesPackage, tokenTransaction.FinalizedTokenTransactionHash)
	if err != nil {
		return fmt.Errorf("failed to exchange revocation secrets and finalize %s: %w", tokenTransaction.ID, err)
	}

	return nil
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
	logger := logging.GetLoggerFromContext(ctx)
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
	entTx, err := ent.GetTxFromContext(ctx)
	if err != nil {
		return nil, sparkerrors.InternalDatabaseTransactionLifecycleError(fmt.Errorf("failed to get or create current tx for request: %w", err))
	}
	tx := entTx.Client()
	if _, err := tx.TokenTransaction.Update().
		Where(
			tokentransaction.StatusNEQ(st.TokenTransactionStatusFinalized),
			tokentransaction.FinalizedTokenTransactionHashEQ(tokenTransactionHash),
		).
		SetStatus(st.TokenTransactionStatusRevealed).
		Save(ctx); err != nil {
		return nil, sparkerrors.InternalDatabaseWriteError(fmt.Errorf("failed to update token transaction status to Revealed: %w for token txHash: %x", err, tokenTransactionHash))
	}
	if err := entTx.Commit(); err != nil {
		return nil, sparkerrors.InternalDatabaseTransactionLifecycleError(fmt.Errorf("failed to commit and replace transaction after setting status to revealed: %w for token txHash: %x", err, tokenTransactionHash))
	}

	outputsToSpend, err := h.getOutputsToSpendForExchange(ctx, tokenTransactionHash)
	if err != nil {
		return nil, sparkerrors.InternalDatabaseReadError(fmt.Errorf("failed to get outputs to spend for exchange: %w for token txHash: %x", err, tokenTransactionHash))
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

		logger.Sugar().Infof("Operator %s is exchanging revocation secret shares with operator %s for token txHash: %s", h.config.Identifier, operator.Identifier, hex.EncodeToString(tokenTransactionHash))
		return client.ExchangeRevocationSecretsShares(ctx, &tokeninternalpb.ExchangeRevocationSecretsSharesRequest{
			FinalTokenTransaction:         tokenTransaction,
			FinalTokenTransactionHash:     tokenTransactionHash,
			OperatorTransactionSignatures: allOperatorSignaturesPackage,
			OperatorShares:                revocationSecretShares,
			OperatorIdentityPublicKey:     h.config.IdentityPublicKey().Serialize(),
			OutputsToSpend:                outputsToSpend,
		})
	})

	for identifier, resp := range response {
		for _, operatorShares := range resp.ReceivedOperatorShares {
			reqPubKey, err := keys.ParsePublicKey(operatorShares.OperatorIdentityPublicKey)
			if err != nil {
				return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("unable to parse request operator identity public key: %w", err))
			}
			reqOperatorIdentifier := h.config.GetOperatorIdentifierFromIdentityPublicKey(reqPubKey)
			logger.Sugar().Infof("Operator %s received from operator %s, %d secret shares originating from operator %s for token txHash: %s",
				h.config.Identifier,
				identifier,
				len(operatorShares.Shares),
				reqOperatorIdentifier,
				hex.EncodeToString(tokenTransactionHash),
			)
		}
	}

	// If there was an error exchanging with all operators, we will roll back to the revealed status.
	if errorExchangingWithAllOperators != nil {
		return nil, sparkerrors.WrapErrorWithMessage(errorExchangingWithAllOperators, fmt.Sprintf("failed to exchange revocation secret shares for token txHash: %x", tokenTransactionHash))
	}

	return response, nil
}

func (h *SignTokenHandler) getOutputsToSpendForExchange(ctx context.Context, tokenTransactionHash []byte) ([]*tokeninternalpb.OutputToSpend, error) {
	revealedTokenTransaction, err := ent.FetchTokenTransactionDataByHashForRead(ctx, tokenTransactionHash)
	if err != nil {
		return nil, sparkerrors.InternalDatabaseReadError(fmt.Errorf("failed to fetch token transaction after setting status to revealed: %w for token txHash: %x", err, tokenTransactionHash))
	}
	outputsToSpend := make([]*tokeninternalpb.OutputToSpend, 0, len(revealedTokenTransaction.Edges.SpentOutput))
	for _, outputToSpend := range revealedTokenTransaction.Edges.SpentOutput {
		if outputToSpend == nil {
			continue
		}
		outputsToSpend = append(outputsToSpend, &tokeninternalpb.OutputToSpend{
			CreatedTokenTransactionHash: outputToSpend.Edges.OutputCreatedTokenTransaction.FinalizedTokenTransactionHash,
			CreatedTokenTransactionVout: uint32(outputToSpend.CreatedTransactionOutputVout),
			SpentTokenTransactionVout:   uint32(outputToSpend.SpentTransactionInputVout),
			SpentOwnershipSignature:     outputToSpend.SpentOwnershipSignature,
		})
	}
	return outputsToSpend, nil
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
	finalTokenTransaction *tokenpb.TokenTransaction,
) (*tokeninternalpb.SignTokenTransactionFromCoordinationResponse, error) {
	ctx, span := GetTracer().Start(ctx, "SignTokenHandler.localSignAndCommitTransaction", GetEntTokenTransactionTraceAttributes(ctx, tokenTransaction))
	defer span.End()
	internalSignTokenHandler := NewInternalSignTokenHandler(h.config)
	var ttxoSignatures []*tokenpb.SignatureWithIndex
	if foundOperatorSignatures != nil {
		ttxoSignatures = foundOperatorSignatures.TtxoSignatures
	}
	sigBytes, err := internalSignTokenHandler.SignAndPersistTokenTransaction(
		ctx,
		tokenTransaction,
		finalTokenTransaction,
		finalTokenTransactionHash,
		ttxoSignatures,
	)
	if err != nil {
		return nil, err
	}
	return &tokeninternalpb.SignTokenTransactionFromCoordinationResponse{
		SparkOperatorSignature: sigBytes,
	}, nil
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
