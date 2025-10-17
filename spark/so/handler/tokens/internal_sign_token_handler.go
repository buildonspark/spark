package tokens

import (
	"context"
	stderrors "errors"
	"fmt"
	"math/big"
	"strconv"

	"github.com/lightsparkdev/spark/common/keys"
	"go.uber.org/zap"

	"entgo.io/ent/dialect/sql"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/lightsparkdev/spark/common/logging"
	secretsharing "github.com/lightsparkdev/spark/common/secret_sharing"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbtkinternal "github.com/lightsparkdev/spark/proto/spark_token_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/signingkeyshare"
	"github.com/lightsparkdev/spark/so/ent/tokenoutput"
	"github.com/lightsparkdev/spark/so/ent/tokenpartialrevocationsecretshare"
	"github.com/lightsparkdev/spark/so/ent/tokentransaction"
	"github.com/lightsparkdev/spark/so/ent/tokentransactionpeersignature"
	soerrors "github.com/lightsparkdev/spark/so/errors"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/tokens"
	"github.com/lightsparkdev/spark/so/utils"
)

type InternalSignTokenHandler struct {
	config *so.Config
}

// NewInternalSignTokenHandler creates a new InternalSignTokenHandler.
func NewInternalSignTokenHandler(config *so.Config) *InternalSignTokenHandler {
	return &InternalSignTokenHandler{
		config: config,
	}
}

// getRequiredParticipatingOperatorsCount returns the number of operators required to
// sign/reveal to consider a transaction valid. By default, signatures from all operators are
// required. If the Token.RequireThresholdOperators flag is enabled, we fall back
// to the configured threshold value instead.
func (h *InternalSignTokenHandler) getRequiredParticipatingOperatorsCount() int {
	if h.config.Token.RequireThresholdOperators {
		return int(h.config.Threshold)
	}
	return len(h.config.SigningOperatorMap)
}

// SignAndPersistTokenTransaction performs the core logic for signing a token transaction from coordination.
// It validates the transaction, input signatures, signs the hash, updates the DB, and returns the signature bytes.
func (h *InternalSignTokenHandler) SignAndPersistTokenTransaction(
	ctx context.Context,
	tokenTransaction *ent.TokenTransaction,
	finalTokenTransactionHash []byte,
	operatorSpecificSignatures []*pb.OperatorSpecificOwnerSignature,
) ([]byte, error) {
	ctx, span := GetTracer().Start(ctx, "InternalSignTokenHandler.SignAndPersistTokenTransaction", GetEntTokenTransactionTraceAttributes(ctx, tokenTransaction))
	defer span.End()
	ctx, _ = logging.WithAttrs(ctx, tokens.GetEntTokenTransactionZapAttrs(ctx, tokenTransaction)...)

	if tokenTransaction.Status == st.TokenTransactionStatusSigned {
		// Return stored signature for sign requests if already signed.
		signature, err := h.regenerateOperatorSignatureForDuplicateRequest(ctx, h.config, tokenTransaction, finalTokenTransactionHash)
		if err != nil {
			return nil, err
		}
		return signature, nil
	}

	if err := validateTokenTransactionForSigning(ctx, h.config, tokenTransaction); err != nil {
		return nil, tokens.FormatErrorWithTransactionEnt(err.Error(), tokenTransaction, err)
	}

	if err := validateOperatorSpecificSignatures(h.config.IdentityPublicKey(), operatorSpecificSignatures, tokenTransaction); err != nil {
		return nil, err
	}

	operatorSignature := ecdsa.Sign(h.config.IdentityPrivateKey.ToBTCEC(), finalTokenTransactionHash)

	// Order the signatures according to their index before updating the DB.
	operatorSpecificSignatureMap := make(map[int][]byte, len(operatorSpecificSignatures))
	for _, sig := range operatorSpecificSignatures {
		inputIndex := int(sig.OwnerSignature.InputIndex)
		operatorSpecificSignatureMap[inputIndex] = sig.OwnerSignature.Signature
	}
	operatorSpecificSignaturesArr := make([][]byte, len(operatorSpecificSignatureMap))
	for i := 0; i < len(operatorSpecificSignatureMap); i++ {
		operatorSpecificSignaturesArr[i] = operatorSpecificSignatureMap[i]
	}
	if err := ent.UpdateSignedTransaction(ctx, tokenTransaction, operatorSpecificSignaturesArr, operatorSignature.Serialize()); err != nil {
		return nil, tokens.FormatErrorWithTransactionEnt("failed to update outputs after signing", tokenTransaction, err)
	}

	return operatorSignature.Serialize(), nil
}

// regenerateOperatorSignatureForDuplicateRequest handles the case where a transaction has already been signed.
// This allows for simpler wallet SDK logic such that if a Sign() call to one of the SOs failed,
// the wallet SDK can retry with all SOs and get successful responses.
func (h *InternalSignTokenHandler) regenerateOperatorSignatureForDuplicateRequest(
	ctx context.Context,
	config *so.Config,
	tokenTransaction *ent.TokenTransaction,
	finalTokenTransactionHash []byte,
) ([]byte, error) {
	_, logger := logging.WithAttrs(ctx, tokens.GetEntTokenTransactionZapAttrs(ctx, tokenTransaction)...)
	logger.Debug("Regenerating response for a duplicate SignTokenTransaction() Call")

	var invalidOutputs []error
	isMint := tokenTransaction.Edges.Mint != nil
	expectedCreatedOutputStatus := st.TokenOutputStatusCreatedSigned
	if isMint {
		expectedCreatedOutputStatus = st.TokenOutputStatusCreatedFinalized
	}

	invalidOutputs = validateOutputStatuses(tokenTransaction.Edges.CreatedOutput, expectedCreatedOutputStatus)
	if len(tokenTransaction.Edges.SpentOutput) > 0 {
		invalidOutputs = append(invalidOutputs, validateInputStatuses(tokenTransaction.Edges.SpentOutput, st.TokenOutputStatusSpentSigned)...)
	}
	if len(invalidOutputs) > 0 {
		return nil, tokens.FormatErrorWithTransactionEnt(
			tokens.ErrInvalidOutputs,
			tokenTransaction,
			stderrors.Join(invalidOutputs...),
		)
	}

	if err := utils.ValidateOwnershipSignature(tokenTransaction.OperatorSignature, finalTokenTransactionHash, config.IdentityPublicKey()); err != nil {
		return nil, tokens.FormatErrorWithTransactionEnt(tokens.ErrStoredOperatorSignatureInvalid, tokenTransaction, err)
	}

	logger.Debug("Returning stored signature in response to repeat Sign() call")
	return tokenTransaction.OperatorSignature, nil
}

// === Revocation Secret Exchange ===
type ShareKey struct {
	TokenOutputID             uuid.UUID
	OperatorIdentityPublicKey keys.Public
}
type ShareValue struct {
	SecretShare               keys.Private
	OperatorIdentityPublicKey keys.Public
}

type operatorSharesMap map[keys.Public][]*pbtkinternal.RevocationSecretShare

func (h *InternalSignTokenHandler) ExchangeRevocationSecretsShares(ctx context.Context, req *pbtkinternal.ExchangeRevocationSecretsSharesRequest) (*pbtkinternal.ExchangeRevocationSecretsSharesResponse, error) {
	ctx, span := GetTracer().Start(ctx, "InternalSignTokenHandler.ExchangeRevocationSecretsShares")
	defer span.End()
	ctx, logger := logging.WithAttrs(ctx, tokens.GetProtoTokenTransactionZapAttrs(ctx, req.FinalTokenTransaction)...)

	if len(req.OperatorShares) == 0 {
		return nil, fmt.Errorf("no operator shares provided in request")
	}
	reqPubKey, err := keys.ParsePublicKey(req.OperatorIdentityPublicKey)
	if err != nil {
		return nil, fmt.Errorf("unable to parse request operator identity public key: %w", err)
	}
	reqOperatorIdentifier := h.config.GetOperatorIdentifierFromIdentityPublicKey(reqPubKey)
	logger.Sugar().Infof("exchanging revocation secret shares with operator %d", reqOperatorIdentifier)

	// Verify the incoming operator signatures package
	operatorSignatures := make(operatorSignaturesMap)
	for _, sig := range req.OperatorTransactionSignatures {
		sigOperatorIdentityPublicKey, err := keys.ParsePublicKey(sig.OperatorIdentityPublicKey)
		if err != nil {
			return nil, fmt.Errorf("failed to parse signature operator identity public key: %w", err)
		}
		identifier := h.config.GetOperatorIdentifierFromIdentityPublicKey(sigOperatorIdentityPublicKey)
		operatorSignatures[identifier] = sig.GetSignature()
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}
	tokenTransaction, err := db.TokenTransaction.Query().
		Where(tokentransaction.FinalizedTokenTransactionHashEQ(req.FinalTokenTransactionHash)).
		WithSpentOutput().
		WithCreatedOutput().
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load token transaction with txHash (%x) in ExchangeRevocationSecretsShares: %w", req.FinalTokenTransactionHash, err)
	}
	if err := h.validateSignaturesPackageAndPersistPeerSignatures(ctx, operatorSignatures, tokenTransaction); err != nil {
		return nil, tokens.FormatErrorWithTransactionEnt("failed to validate signature package and persist peer signatures", tokenTransaction, err)
	}
	if tokenTransaction.Status == st.TokenTransactionStatusStarted {
		lockedTx, lockErr := ent.FetchAndLockTokenTransactionDataByHash(ctx, req.FinalTokenTransactionHash)
		if lockErr != nil {
			return nil, tokens.FormatErrorWithTransactionEnt("failed to refetch transaction with lock", tokenTransaction, lockErr)
		}
		if err := validateTokenTransactionForSigning(ctx, h.config, lockedTx); err != nil {
			return nil, tokens.FormatErrorWithTransactionEnt(err.Error(), lockedTx, err)
		}
		err = h.validateAndSignTransactionWithProvidedOwnSignature(ctx, lockedTx, operatorSignatures[h.config.Identifier])
		if err != nil {
			return nil, err
		}
	}

	inputOperatorShareMap, err := buildInputOperatorShareMap(req.OperatorShares)
	if err != nil {
		return nil, tokens.FormatErrorWithTransactionEnt("failed to build input operator share map", tokenTransaction, err)
	}
	finalized, err := h.persistPartialRevocationSecretShares(ctx, inputOperatorShareMap, req.FinalTokenTransactionHash)
	if err != nil {
		return nil, tokens.FormatErrorWithTransactionEnt("failed to persist partial revocation secret shares", tokenTransaction, err)
	}

	response, err := h.prepareResponseForExchangeRevocationSecretsShare(ctx, inputOperatorShareMap)
	if err != nil {
		return nil, tokens.FormatErrorWithTransactionEnt("failed to prepare response for exchange revocation secrets share", tokenTransaction, err)
	}

	// No actions take place after this point so we don't have to worry about commiting the revealed status.
	// It is possible for us to finalize in the exchange step above.
	// If that happens, the status will go directly from Signed to Finalized.
	if !finalized &&
		tokenTransaction.Status != st.TokenTransactionStatusRevealed &&
		tokenTransaction.Status != st.TokenTransactionStatusFinalized {
		_, err = tokenTransaction.Update().
			Where(
				tokentransaction.IDEQ(tokenTransaction.ID),
				tokentransaction.StatusNotIn(
					st.TokenTransactionStatusFinalized,
					st.TokenTransactionStatusRevealed,
				),
			).
			SetStatus(st.TokenTransactionStatusRevealed).
			Save(ctx)
		if ent.IsNotFound(err) {
			// We know the row exists, but it's either Finalized or Revealed. Ignore.
			err = nil
		}
		if err != nil {
			return nil, tokens.FormatErrorWithTransactionEnt("failed to update token transaction status", tokenTransaction, err)
		}
	}
	return response, nil
}

func (h *InternalSignTokenHandler) validateAndSignTransactionWithProvidedOwnSignature(ctx context.Context, tokenTransaction *ent.TokenTransaction, ownSignature []byte) error {
	logger := logging.GetLoggerFromContext(ctx)
	logger.Error("Updating token transaction status to signed from peer operator's signature. This should not happen unless the operator did not successfully commit after signing.")

	if err := verifyOperatorSignature(
		ownSignature,
		h.config.SigningOperatorMap[h.config.Identifier],
		tokenTransaction.FinalizedTokenTransactionHash); err != nil {
		return tokens.FormatErrorWithTransactionEnt("failed to verify own operator signature", tokenTransaction, err)
	}

	if err := ent.UpdateSignedTransferTransactionWithoutOperatorSpecificOwnershipSignatures(ctx, tokenTransaction, ownSignature); err != nil {
		return tokens.FormatErrorWithTransactionEnt("failed to update token transaction status to signed", tokenTransaction, err)
	}
	return nil
}

func (h *InternalSignTokenHandler) prepareResponseForExchangeRevocationSecretsShare(ctx context.Context, inputOperatorShareMap map[ShareKey]ShareValue) (*pbtkinternal.ExchangeRevocationSecretsSharesResponse, error) {
	operatorSharesMap, err := h.getSecretSharesNotInInput(ctx, inputOperatorShareMap)
	if err != nil {
		return nil, fmt.Errorf("failed to get token outputs with shares: %w", err)
	}
	secretSharesToReturn := make([]*pbtkinternal.OperatorRevocationShares, 0, len(operatorSharesMap))
	for operatorIdentity, shares := range operatorSharesMap {
		secretSharesToReturn = append(secretSharesToReturn, &pbtkinternal.OperatorRevocationShares{
			OperatorIdentityPublicKey: operatorIdentity.Serialize(),
			Shares:                    shares,
		})
	}

	return &pbtkinternal.ExchangeRevocationSecretsSharesResponse{
		ReceivedOperatorShares: secretSharesToReturn,
	}, nil
}

func (h *InternalSignTokenHandler) getSecretSharesNotInInput(ctx context.Context, inputOperatorShareMap map[ShareKey]ShareValue) (operatorSharesMap, error) {
	if len(inputOperatorShareMap) == 0 {
		return nil, fmt.Errorf("no input operator shares provided")
	}
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}
	thisOperatorIdentityPubkey := h.config.IdentityPublicKey()

	uniqueTokenOutputIDs := make([]uuid.UUID, 0, len(inputOperatorShareMap))
	seen := make(map[uuid.UUID]bool)
	for shareKey := range inputOperatorShareMap {
		if !seen[shareKey.TokenOutputID] {
			uniqueTokenOutputIDs = append(uniqueTokenOutputIDs, shareKey.TokenOutputID)
			seen[shareKey.TokenOutputID] = true
		}
	}

	const batchSize = queryTokenOutputsWithPartialRevocationSecretSharesBatchSize
	var outputsWithKeyShares []*ent.TokenOutput

	for i := 0; i < len(uniqueTokenOutputIDs); i += batchSize {
		end := i + batchSize
		if end > len(uniqueTokenOutputIDs) {
			end = len(uniqueTokenOutputIDs)
		}

		batchOutputIDs := uniqueTokenOutputIDs[i:end]

		var excludeKeyshareTokenOutputIDs []any
		for shareKey := range inputOperatorShareMap {
			for _, outputID := range batchOutputIDs {
				if shareKey.TokenOutputID == outputID {
					if shareKey.OperatorIdentityPublicKey.Equals(thisOperatorIdentityPubkey) {
						excludeKeyshareTokenOutputIDs = append(excludeKeyshareTokenOutputIDs, shareKey.TokenOutputID)
					}
					break
				}
			}
		}
		batchOutputs, err := db.TokenOutput.Query().Where(tokenoutput.IDIn(batchOutputIDs...)).
			WithRevocationKeyshare(func(q *ent.SigningKeyshareQuery) {
				if len(excludeKeyshareTokenOutputIDs) > 0 {
					q.Where(func(s *sql.Selector) {
						subquery := sql.Select(tokenoutput.RevocationKeyshareColumn).
							From(sql.Table(tokenoutput.Table)).
							Where(sql.In(tokenoutput.FieldID, excludeKeyshareTokenOutputIDs...))
						s.Where(sql.NotIn(signingkeyshare.FieldID, subquery))
					})
				}
			}).
			All(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get token outputs with shares batch %d-%d: %w", i, end-1, err)
		}

		partialSharesByOutput, err := h.getPartialRevocationSecretShares(ctx, db, batchOutputIDs, inputOperatorShareMap)
		if err != nil {
			return nil, fmt.Errorf("failed to get partial shares batch %d-%d: %w", i, end-1, err)
		}

		// Attach partial shares to outputs
		for _, output := range batchOutputs {
			output.Edges.TokenPartialRevocationSecretShares = partialSharesByOutput[output.ID]
		}

		outputsWithKeyShares = append(outputsWithKeyShares, batchOutputs...)
	}

	operatorShares, err := h.buildOperatorPubkeyToRevocationSecretShareMap(outputsWithKeyShares)
	if err != nil {
		return nil, fmt.Errorf("failed to build operator pubkey to revocation secret share map: %w", err)
	}
	return operatorShares, nil
}

// getPartialRevocationSecretShares uses raw SQL for efficient exclusion
func (h *InternalSignTokenHandler) getPartialRevocationSecretShares(
	ctx context.Context,
	db *ent.Tx,
	batchOutputIDs []uuid.UUID,
	inputOperatorShareMap map[ShareKey]ShareValue,
) (map[uuid.UUID][]*ent.TokenPartialRevocationSecretShare, error) {
	ctx, span := GetTracer().Start(ctx, "InternalSignTokenHandler.getPartialRevocationSecretShares")
	defer span.End()

	// Build exclusion arrays for UNNEST
	var excludeOutputIDs []uuid.UUID
	var excludeOperatorKeys [][]byte
	for shareKey, shareValue := range inputOperatorShareMap {
		for _, outputID := range batchOutputIDs {
			if shareKey.TokenOutputID == outputID {
				excludeOutputIDs = append(excludeOutputIDs, shareKey.TokenOutputID)
				excludeOperatorKeys = append(excludeOperatorKeys, shareValue.OperatorIdentityPublicKey.Serialize())
				break
			}
		}
	}

	query := `
		SELECT tprss.id, 
		       tprss.create_time, 
		       tprss.update_time, 
		       tprss.operator_identity_public_key, 
		       tprss.secret_share, 
		       tprss.token_output_token_partial_revocation_secret_shares
		FROM token_partial_revocation_secret_shares tprss
		WHERE tprss.token_output_token_partial_revocation_secret_shares = ANY($1)
	`

	args := []any{pq.Array(batchOutputIDs)}

	if len(excludeOutputIDs) > 0 {
		// Use CTE with LEFT JOIN for efficient exclusion
		query = `
			WITH excluded_pairs AS (
			    SELECT 
			        excluded_pairs.token_id,
			        excluded_pairs.operator_key
			    FROM UNNEST($2::uuid[], $3::bytea[]) AS excluded_pairs(token_id, operator_key)
			)
			SELECT tprss.id, 
			       tprss.create_time, 
			       tprss.update_time, 
			       tprss.operator_identity_public_key, 
			       tprss.secret_share, 
			       tprss.token_output_token_partial_revocation_secret_shares
			FROM token_partial_revocation_secret_shares tprss
			LEFT JOIN excluded_pairs ep ON (
			    tprss.token_output_token_partial_revocation_secret_shares = ep.token_id
			    AND tprss.operator_identity_public_key = ep.operator_key
			)
			WHERE ep.token_id IS NULL
			  AND tprss.token_output_token_partial_revocation_secret_shares = ANY($1)
		`
		args = append(args, pq.Array(excludeOutputIDs), pq.Array(excludeOperatorKeys))
	}

	// nolint:forbidigo
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to execute optimized partial shares query: %w", err)
	}

	defer func() {
		if cerr := rows.Close(); cerr != nil {
			logging.GetLoggerFromContext(ctx).Error("failed to close rows", zap.Error(cerr))
			span.RecordError(cerr)
		}
	}()

	// Scan results into a map keyed by token output ID
	sharesByOutput := make(map[uuid.UUID][]*ent.TokenPartialRevocationSecretShare)
	for rows.Next() {
		share := &ent.TokenPartialRevocationSecretShare{}
		var operatorKeyBytes []byte
		var tokenOutputID uuid.UUID
		if err := rows.Scan(
			&share.ID,
			&share.CreateTime,
			&share.UpdateTime,
			&operatorKeyBytes,
			&share.SecretShare,
			&tokenOutputID,
		); err != nil {
			return nil, fmt.Errorf("failed to scan partial share: %w", err)
		}

		operatorKey, err := keys.ParsePublicKey(operatorKeyBytes)
		if err != nil {
			return nil, fmt.Errorf("failed to parse operator identity public key: %w", err)
		}
		share.OperatorIdentityPublicKey = operatorKey

		sharesByOutput[tokenOutputID] = append(sharesByOutput[tokenOutputID], share)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating partial shares: %w", err)
	}

	return sharesByOutput, nil
}

func (h *InternalSignTokenHandler) buildOperatorPubkeyToRevocationSecretShareMap(tokenOutputs []*ent.TokenOutput) (operatorSharesMap, error) {
	operatorShares := make(operatorSharesMap)
	for _, to := range tokenOutputs {
		if share := to.Edges.RevocationKeyshare; share != nil {
			operatorIdentityPubkey := h.config.IdentityPublicKey()
			operatorShares[operatorIdentityPubkey] = append(
				operatorShares[operatorIdentityPubkey],
				&pbtkinternal.RevocationSecretShare{
					InputTtxoId: to.ID.String(),
					SecretShare: share.SecretShare.Serialize(),
				},
			)
		}
		for _, partialShare := range to.Edges.TokenPartialRevocationSecretShares {
			idPubKey := partialShare.OperatorIdentityPublicKey
			operatorShares[idPubKey] = append(
				operatorShares[idPubKey],
				&pbtkinternal.RevocationSecretShare{
					InputTtxoId: to.ID.String(),
					SecretShare: partialShare.SecretShare.Serialize(),
				},
			)
		}
	}
	return operatorShares, nil
}

func (h *InternalSignTokenHandler) persistPartialRevocationSecretShares(
	ctx context.Context,
	inputOperatorShareMap map[ShareKey]ShareValue,
	transactionHash []byte,
) (finalized bool, err error) {
	if len(inputOperatorShareMap) == 0 {
		return false, nil
	}
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	inputTokenOutputIDMap := make(map[uuid.UUID]struct{}, len(inputOperatorShareMap))
	for k := range inputOperatorShareMap {
		inputTokenOutputIDMap[k.TokenOutputID] = struct{}{}
	}
	uniqueInputTokenOutputIDs := make([]uuid.UUID, 0, len(inputTokenOutputIDMap))
	for id := range inputTokenOutputIDMap {
		uniqueInputTokenOutputIDs = append(uniqueInputTokenOutputIDs, id)
	}

	tx, err := db.TokenTransaction.
		Query().
		Where(tokentransaction.FinalizedTokenTransactionHash(transactionHash)).
		WithSpentOutput(func(q *ent.TokenOutputQuery) {
			q.WithRevocationKeyshare()
		}).
		Only(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to load token transaction with txHash in persistPartialRevocationSecretShares: %x: %w", transactionHash, err)
	}

	err = validateInputTokenOutputsMatchSpentTokenOutputs(uniqueInputTokenOutputIDs, tx.Edges.SpentOutput)
	if err != nil {
		return false, tokens.FormatErrorWithTransactionEnt("input token outputs do not match spent token outputs", tx, err)
	}
	revocationKeyshares := make(map[uuid.UUID]*ent.SigningKeyshare)
	for _, spentOutput := range tx.Edges.SpentOutput {
		if revocationKeyshare := spentOutput.Edges.RevocationKeyshare; revocationKeyshare != nil {
			revocationKeyshares[spentOutput.ID] = revocationKeyshare
		}
	}

	var newShares []*ent.TokenPartialRevocationSecretShareCreate
	for sk, sv := range inputOperatorShareMap {
		if sv.OperatorIdentityPublicKey == (keys.Public{}) {
			return false, fmt.Errorf("nil operator identity public key bytes found in input operator share map")
		}
		if sv.SecretShare.IsZero() {
			return false, fmt.Errorf("zero secret share found in input operator share map")
		}
		// Do not write shares that belong to this server to the TokenPartialRevocationSecretShare table.
		if sv.OperatorIdentityPublicKey.Equals(h.config.IdentityPublicKey()) {
			continue
		}
		newShares = append(newShares, db.TokenPartialRevocationSecretShare.Create().
			SetOperatorIdentityPublicKey(sv.OperatorIdentityPublicKey).
			SetSecretShare(sv.SecretShare).
			SetTokenOutputID(sk.TokenOutputID))
	}

	if len(newShares) > 0 {
		// Insert the new secret shares: if an operator already has a secret share from a specific
		// peer operator (same operator identity pubkey + token-output edge), ignore the conflict and move on.
		err := db.TokenPartialRevocationSecretShare.
			CreateBulk(newShares...).
			OnConflictColumns(
				tokenpartialrevocationsecretshare.FieldOperatorIdentityPublicKey,
				tokenpartialrevocationsecretshare.TokenOutputColumn,
			).
			DoNothing().
			Exec(ctx)
		if err != nil {
			return false, tokens.FormatErrorWithTransactionEnt("failed to save new secret shares", tx, soerrors.InternalDatabaseWriteError(err))
		}
	}
	finalized, err = h.recoverFullRevocationSecretsAndFinalize(ctx, transactionHash)
	if err != nil {
		return false, fmt.Errorf("failed to finalize token transaction: %w", err)
	}
	return finalized, nil
}

func (h *InternalSignTokenHandler) recoverFullRevocationSecretsAndFinalize(ctx context.Context, tokenTransactionHash []byte) (finalized bool, err error) {
	ctx, span := GetTracer().Start(ctx, "InternalSignTokenHandler.recoverFullRevocationSecretsAndFinalize")
	defer span.End()
	logger := logging.GetLoggerFromContext(ctx)
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	tokenTransaction, err := db.TokenTransaction.Query().
		Where(tokentransaction.FinalizedTokenTransactionHashEQ(tokenTransactionHash),
			tokentransaction.StatusIn(
				st.TokenTransactionStatusStarted,
				st.TokenTransactionStatusSigned,
				st.TokenTransactionStatusRevealed,
				st.TokenTransactionStatusFinalized,
			)).
		WithSpentOutput().
		Only(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to load token transaction with txHash in recoverFullRevocationSecretsAndFinalize: %x: %w", tokenTransactionHash, err)
	}
	// Token transaction is already finalized, so we can return early.
	if tokenTransaction.Status == st.TokenTransactionStatusFinalized {
		return true, nil
	}
	if len(tokenTransaction.Edges.SpentOutput) == 0 {
		return false, fmt.Errorf("transaction %x has no spent outputs loaded", tokenTransactionHash)
	}

	outputIDs := make([]uuid.UUID, len(tokenTransaction.Edges.SpentOutput))
	for i, output := range tokenTransaction.Edges.SpentOutput {
		outputIDs[i] = output.ID
	}

	const batchSize = queryTokenOutputsWithPartialRevocationSecretSharesBatchSize
	outputsWithShares := make(map[uuid.UUID]*ent.TokenOutput)

	for i := 0; i < len(outputIDs); i += batchSize {
		end := i + batchSize
		if end > len(outputIDs) {
			end = len(outputIDs)
		}

		batchOutputIDs := outputIDs[i:end]
		batchOutputs, err := db.TokenOutput.Query().
			Where(tokenoutput.IDIn(batchOutputIDs...)).
			WithTokenPartialRevocationSecretShares().
			WithRevocationKeyshare().
			All(ctx)
		if err != nil {
			return false, tokens.FormatErrorWithTransactionEnt(fmt.Sprintf("failed to load shares for outputs batch (%d-%d)", i, end-1), tokenTransaction, soerrors.InternalDatabaseReadError(err))
		}

		for _, output := range batchOutputs {
			outputsWithShares[output.ID] = output
			shares := 0
			if output.Edges.TokenPartialRevocationSecretShares != nil {
				shares = len(output.Edges.TokenPartialRevocationSecretShares)
			}
			logger.Info(fmt.Sprintf("output: %s, has %d revocation keyshares", output.ID, shares))
		}
	}

	// Replace the spent outputs with the ones that have shares loaded
	for i, spentOutput := range tokenTransaction.Edges.SpentOutput {
		if outputWithShares, exists := outputsWithShares[spentOutput.ID]; exists {
			tokenTransaction.Edges.SpentOutput[i] = outputWithShares
		}
	}

	return h.RecoverFullRevocationSecretsAndFinalize(ctx, tokenTransaction)
}

func (h *InternalSignTokenHandler) RecoverFullRevocationSecretsAndFinalize(ctx context.Context, tokenTransaction *ent.TokenTransaction) (finalized bool, err error) {
	if canRecover, err := h.canRecoverAndFinalizeTransaction(tokenTransaction); err != nil {
		return false, tokens.FormatErrorWithTransactionEnt("failed to check if can recover and finalize transaction", tokenTransaction, err)
	} else if !canRecover {
		return false, nil
	}

	outputRecoveredSecrets, outputToSpendRevocationCommitments, err := h.recoverFullRevocationSecrets(tokenTransaction)
	if err != nil {
		return false, tokens.FormatErrorWithTransactionEnt("failed to recover full revocation secrets", tokenTransaction, err)
	}

	recoveredSecretsToValidate := make([]keys.Private, len(outputRecoveredSecrets))
	for i, secret := range outputRecoveredSecrets {
		recoveredSecretsToValidate[i] = secret.RevocationSecret
	}
	if err := utils.ValidateRevocationKeys(recoveredSecretsToValidate, outputToSpendRevocationCommitments); err != nil {
		return false, tokens.FormatErrorWithTransactionEnt("invalid revocation keys found", tokenTransaction, err)
	}

	internalFinalizeHandler := NewInternalFinalizeTokenHandler(h.config)
	err = internalFinalizeHandler.FinalizeCoordinatedTokenTransactionInternal(ctx, tokenTransaction.FinalizedTokenTransactionHash, outputRecoveredSecrets)
	if err != nil {
		return false, tokens.FormatErrorWithTransactionEnt("failed to finalize token transaction", tokenTransaction, err)
	}
	return true, nil
}

func (h *InternalSignTokenHandler) canRecoverAndFinalizeTransaction(tokenTransaction *ent.TokenTransaction) (canRecoverAndFinalize bool, err error) {
	minCountOutputPartialRevocationSecretSharesForAllOutputs := len(h.config.SigningOperatorMap)
	for _, spentOutput := range tokenTransaction.Edges.SpentOutput {
		if spentOutput.Edges.RevocationKeyshare == nil {
			return false, tokens.FormatErrorWithTransactionEnt(
				"missing revocation key-share on output", tokenTransaction, soerrors.InternalDatabaseMissingEdge(nil))
		}
		if spentOutput.Edges.RevocationKeyshare.SecretShare.IsZero() {
			return false, tokens.FormatErrorWithTransactionEnt(
				"nil revocation secret share on output", tokenTransaction, soerrors.InternalObjectMissingField(nil))
		}
		minCountOutputPartialRevocationSecretSharesForAllOutputs = min(
			minCountOutputPartialRevocationSecretSharesForAllOutputs,
			len(spentOutput.Edges.TokenPartialRevocationSecretShares),
		)
	}
	requiredOperators := h.getRequiredParticipatingOperatorsCount()
	// min count of partial revocation secret shares + this server's share must be >= threshold, for all outputs
	if minCountOutputPartialRevocationSecretSharesForAllOutputs+1 >= requiredOperators {
		return true, nil
	}
	return false, nil
}

func (h *InternalSignTokenHandler) recoverFullRevocationSecrets(tokenTransaction *ent.TokenTransaction) (outputRecoveredSecrets []*ent.RecoveredRevocationSecret, outputToSpendRevocationCommitments []keys.Public, err error) {
	outputRecoveredSecrets = make([]*ent.RecoveredRevocationSecret, 0, len(tokenTransaction.Edges.SpentOutput))
	outputToSpendRevocationCommitments = make([]keys.Public, 0, len(tokenTransaction.Edges.SpentOutput))

	for _, output := range tokenTransaction.Edges.SpentOutput {
		commitment, err := keys.ParsePublicKey(output.WithdrawRevocationCommitment)
		if err != nil {
			return nil, nil, err
		}
		if output.Edges.RevocationKeyshare == nil {
			return nil, nil, soerrors.InternalDatabaseMissingEdge(fmt.Errorf("missing revocation key-share edge on output"))
		}
		if output.Edges.RevocationKeyshare.SecretShare.IsZero() {
			return nil, nil, soerrors.InternalObjectMissingField(fmt.Errorf("nil revocation secret share on output"))
		}
		outputToSpendRevocationCommitments = append(outputToSpendRevocationCommitments, commitment)
		outputShares := make([]*secretsharing.SecretShare, 0, len(output.Edges.TokenPartialRevocationSecretShares)+1)
		for _, share := range output.Edges.TokenPartialRevocationSecretShares {
			operatorIndex, err := strconv.ParseInt(h.config.GetOperatorIdentifierFromIdentityPublicKey(share.OperatorIdentityPublicKey), 10, 64)
			if err != nil {
				return nil, nil, soerrors.InternalObjectMalformedField(fmt.Errorf("failed to parse operator index: %w", err))
			}
			outputShares = append(outputShares, &secretsharing.SecretShare{
				FieldModulus: secp256k1.S256().N,
				Threshold:    int(h.config.Threshold),
				Index:        big.NewInt(operatorIndex),
				Share:        new(big.Int).SetBytes(share.SecretShare.Serialize()),
			})
		}
		coordinatorIndex, err := strconv.ParseInt(h.config.GetOperatorIdentifierFromIdentityPublicKey(h.config.IdentityPublicKey()), 10, 64)
		if err != nil {
			return nil, nil, soerrors.InternalObjectMalformedField(fmt.Errorf("failed to parse coordinator index: %w", err))
		}
		outputShares = append(outputShares, &secretsharing.SecretShare{
			FieldModulus: secp256k1.S256().N,
			Threshold:    int(h.config.Threshold),
			Index:        big.NewInt(coordinatorIndex),
			Share:        new(big.Int).SetBytes(output.Edges.RevocationKeyshare.SecretShare.Serialize()),
		})
		recoveredSecret, err := secretsharing.RecoverSecret(outputShares)
		if err != nil {
			return nil, nil, soerrors.InternalKeyshareError(fmt.Errorf("failed to recover secret: %w", err))
		}
		privKey, err := keys.PrivateKeyFromBigInt(recoveredSecret)
		if err != nil {
			return nil, nil, soerrors.InternalKeyshareError(fmt.Errorf("failed to convert recovered keyshare to private key: %w", err))
		}
		outputRecoveredSecrets = append(outputRecoveredSecrets, &ent.RecoveredRevocationSecret{
			OutputIndex:      uint32(output.SpentTransactionInputVout),
			RevocationSecret: privKey,
		})
	}
	return outputRecoveredSecrets, outputToSpendRevocationCommitments, nil
}

func buildInputOperatorShareMap(operatorShares []*pbtkinternal.OperatorRevocationShares) (map[ShareKey]ShareValue, error) {
	inputOperatorShares := make(map[ShareKey]ShareValue)
	for _, operatorShare := range operatorShares {
		if operatorShare == nil {
			return nil, sparkerrors.InternalInvalidOperatorResponse(fmt.Errorf("nil operator share found in buildInputOperatorShareMap"))
		}
		for _, share := range operatorShare.Shares {
			if share == nil {
				return nil, sparkerrors.InternalInvalidOperatorResponse(fmt.Errorf("nil share found on operator share in buildInputOperatorShareMap"))
			}
			tokenOutputID, err := uuid.Parse(share.GetInputTtxoId())
			if err != nil {
				return nil, sparkerrors.InternalInvalidOperatorResponse(fmt.Errorf("failed to parse token output id: %w", err))
			}
			opIDPubKey, err := keys.ParsePublicKey(operatorShare.OperatorIdentityPublicKey)
			if err != nil {
				return nil, sparkerrors.InternalInvalidOperatorResponse(fmt.Errorf("failed to parse operator identity public key: %w", err))
			}
			secretShare, err := keys.ParsePrivateKey(share.SecretShare)
			if err != nil {
				return nil, sparkerrors.InternalInvalidOperatorResponse(fmt.Errorf("failed to parse secret share: %w", err))
			}
			inputOperatorShares[ShareKey{
				TokenOutputID:             tokenOutputID,
				OperatorIdentityPublicKey: opIDPubKey,
			}] = ShareValue{
				SecretShare:               secretShare,
				OperatorIdentityPublicKey: opIDPubKey,
			}
		}
	}
	return inputOperatorShares, nil
}

func (h *InternalSignTokenHandler) validateSignaturesPackageAndPersistPeerSignatures(
	ctx context.Context,
	signatures operatorSignaturesMap,
	tokenTransaction *ent.TokenTransaction,
) error {
	expectedSignatures := h.getRequiredParticipatingOperatorsCount()
	if len(signatures) < expectedSignatures {
		return tokens.FormatErrorWithTransactionEnt("less than required operators have signed this transaction", tokenTransaction, sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("expected %d signatures, got %d", expectedSignatures, len(signatures))))
	}

	if err := verifyOperatorSignatures(signatures, h.config.SigningOperatorMap, tokenTransaction.FinalizedTokenTransactionHash); err != nil {
		return tokens.FormatErrorWithTransactionEnt("failed to verify operator signatures", tokenTransaction, err)
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to get or create current tx for request: %w", err)
	}
	peerSignatures := make([]*ent.TokenTransactionPeerSignatureCreate, 0, len(h.config.SigningOperatorMap)-1)
	for identifier, sig := range signatures {
		// DO NOT WRITE this operator's signature to the peer signatures table
		if identifier != h.config.Identifier {
			operatorIdentityPubKey := h.config.SigningOperatorMap[identifier].IdentityPublicKey
			peerSignatures = append(peerSignatures, db.TokenTransactionPeerSignature.Create().
				SetTokenTransactionID(tokenTransaction.ID).
				SetOperatorIdentityPublicKey(operatorIdentityPubKey).
				SetSignature(sig))
		}
	}

	if len(peerSignatures) > 0 {
		// Insert the new peer signature: if an operator already has a signature from a specific
		// peer operator (same operator identity pubkey + token-transaction edge), ignore the conflict and move on.
		err := db.TokenTransactionPeerSignature.
			CreateBulk(peerSignatures...).
			OnConflictColumns(
				tokentransactionpeersignature.FieldOperatorIdentityPublicKey,
				tokentransactionpeersignature.TokenTransactionColumn,
			).
			DoNothing().
			Exec(ctx)
		if err != nil {
			return tokens.FormatErrorWithTransactionEnt("failed to bulk create peer signatures", tokenTransaction, err)
		}
	}
	return nil
}

func validateInputTokenOutputsMatchSpentTokenOutputs(tokenOutputIDs []uuid.UUID, spentOutputs []*ent.TokenOutput) error {
	spentOutputMap := make(map[uuid.UUID]*ent.TokenOutput)
	for _, spentOutput := range spentOutputs {
		spentOutputMap[spentOutput.ID] = &ent.TokenOutput{}
	}
	if len(spentOutputMap) != len(tokenOutputIDs) {
		return fmt.Errorf("length of spent token outputs does not match length of token output ids: num spent output in DB (%d) != num input token output ids (%d)", len(spentOutputMap), len(tokenOutputIDs))
	}
	for _, tokenOutputID := range tokenOutputIDs {
		if _, ok := spentOutputMap[tokenOutputID]; !ok {
			return fmt.Errorf("input token output id: %s not spent in transaction", tokenOutputID)
		}
	}
	return nil
}
