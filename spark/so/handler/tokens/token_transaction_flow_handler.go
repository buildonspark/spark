package tokens

import (
	"bytes"
	"context"
	"fmt"
	"slices"

	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/logging"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/consensus"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/tokentransaction"
	"github.com/lightsparkdev/spark/so/ent/tokentransactionpeersignature"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/utils"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ---------------------------------------------------------------------------
// TokenTransactionFlowHandler — participant side (Prepare/Commit/Rollback)
// ---------------------------------------------------------------------------

// TokenTransactionFlowHandler implements consensus.FlowHandler for
// CONSENSUS_OPERATION_TYPE_TOKEN_TRANSACTION. The transaction type is
// discriminated by the token_inputs oneof on the prepared transaction; CREATE
// is the first (and currently only) branch implemented — Prepare fails closed
// on mint/transfer, so a coordinator cannot drive an unimplemented branch.
//
// Prepare reuses the exact peer signing leg the legacy phase-2 broadcast runs
// (SignTokenTransaction: validateAndLockForCommit, entity persistence directly
// in SIGNED, ECDSA identity-key signature over the final hash) and returns the
// signature as the prepare result. The coordinator verifies every signature
// and threshold in BuildCommitPayload and gossips the aggregated set as the
// commit payload, replacing the legacy empty-share
// exchange_revocation_secrets_shares finalize fanout and the 30s retry cron:
// a lost commit is recovered by gossip retry + the FlowExecution reconciler.
type TokenTransactionFlowHandler struct {
	config *so.Config
}

var (
	_ consensus.FlowHandler             = (*TokenTransactionFlowHandler)(nil)
	_ consensus.PrepareBoundFlowHandler = (*TokenTransactionFlowHandler)(nil)
)

func NewTokenTransactionFlowHandler(config *so.Config) *TokenTransactionFlowHandler {
	return &TokenTransactionFlowHandler{config: config}
}

// Prepare runs on every SO. It re-runs the full independent validation the
// legacy sign fanout performs (final-transaction structure, creation entity
// public key against this SO's own DKG key, issuer create signature over the
// partial hash, token-identifier uniqueness), persists the TokenTransaction +
// TokenCreate rows in SIGNED state, and returns this SO's ECDSA signature over
// the final transaction hash.
//
// The coordinator public key recorded on the row is derived from the engine
// (ConsensusPrepare's coordinator_index resolved against the operator config,
// attached to ctx by DispatchPrepare) rather than a self-declared payload
// field, so it is always a real signing operator's key.
//
// Prepare fails closed on a pre-existing transaction row for the same final
// hash instead of adopting it (SignTokenTransaction's cached-signature
// idempotency is deliberately not invoked in this flow; the read check below
// is a fast path and the entity insert's unique constraints are the
// authority, so even a row committing concurrently is refused rather than
// adopted). An identical retried create hashes to the same value by
// construction, so adopting a leftover SIGNED row from an earlier aborted
// attempt would alias two flow executions onto one domain row — and the
// earlier attempt's still-pending rollback (which resolves its target by
// final hash and passes the per-flow fences for its own row) would then
// delete the row the newer, committed attempt depends on. Refusing the row
// keeps rollback deletion safe: a rollback can only ever delete rows its own
// attempt created. The retry that raced the rollback aborts cleanly and
// succeeds once the rollback lands (bounded by gossip retry); legitimate
// client replays are absorbed earlier by the entrypoint's partial-hash
// idempotency.
func (h *TokenTransactionFlowHandler) Prepare(ctx context.Context, op proto.Message) (proto.Message, error) {
	prepareReq, ok := op.(*pbinternal.TokenTransactionPrepareRequest)
	if !ok {
		return nil, fmt.Errorf("unexpected operation type %T for token transaction prepare", op)
	}
	finalTokenTx := prepareReq.GetFinalTokenTransaction()
	if finalTokenTx == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("final_token_transaction is required"))
	}
	// InferTokenTransactionType does not error on an empty token_inputs oneof
	// (it infers TRANSFER); the create-only check below is what fails closed
	// on such payloads.
	txType, err := utils.InferTokenTransactionType(finalTokenTx)
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("failed to infer token transaction type: %w", err))
	}
	if txType != utils.TokenTransactionTypeCreate {
		return nil, sparkerrors.UnimplementedMethodDisabled(fmt.Errorf("consensus token transaction prepare supports only %s, got %s", utils.TokenTransactionTypeCreate, txType))
	}

	finalTokenTxHash, err := hashPreparedTokenTransaction(prepareReq)
	if err != nil {
		return nil, err
	}
	existingTx, err := ent.FetchExistingTokenTransaction(ctx, finalTokenTxHash)
	if err != nil && !ent.IsNotFound(err) {
		return nil, err
	}
	if existingTx != nil {
		return nil, sparkerrors.AlreadyExistsDuplicateOperation(fmt.Errorf(
			"consensus token transaction prepare: transaction %x already exists in status %s; refusing to adopt a row this flow did not create",
			finalTokenTxHash, existingTx.Status))
	}

	// Clone before signing: the sign leg applies ExecuteBefore to the
	// transaction, and the coordinator's self-Prepare passes the flow's own
	// message rather than a freshly-unmarshalled RPC payload.
	signTx, ok := proto.Clone(finalTokenTx).(*tokenpb.TokenTransaction)
	if !ok {
		return nil, fmt.Errorf("failed to clone token transaction for signing")
	}
	signTx.ExecuteBefore = prepareReq.GetExecuteBefore()

	// Drive SignTokenTransaction's signing leg directly rather than through
	// the RPC handler, whose adopt-if-SIGNED idempotency branch must stay
	// unreachable in this flow: the read check above cannot exclude a
	// same-hash row committing in between (read-committed TOCTOU), so the
	// entity insert below — backed by the unique token_identifier and final
	// hash constraints — is the authority on duplicates and fails closed with
	// AlreadyExists instead of silently adopting a row another attempt owns.
	prepareHandler := NewInternalPrepareTokenHandler(h.config)
	coordinatorPubKey, isCoordinator, coordinatorIndex, err := prepareHandler.validateInternalCoordinatorPublicKey(ctx, h.coordinatorIdentityForTokenTransaction(ctx).Serialize())
	if err != nil {
		return nil, err
	}
	inputTtxos, err := prepareHandler.validateAndLockForCommit(ctx, signTx, nil, prepareReq.GetTokenTransactionSignatures(), isCoordinator, coordinatorIndex)
	if err != nil {
		return nil, err
	}
	signature, err := NewSignTokenTransactionHandler(h.config).createSignedTokenTransactionEntitiesAndSign(
		ctx,
		signTx,
		finalTokenTxHash,
		prepareReq.GetTokenTransactionSignatures(),
		nil,
		inputTtxos,
		coordinatorPubKey,
	)
	if err != nil {
		return nil, err
	}
	return &pbinternal.TokenTransactionPrepareResponse{SparkOperatorSignature: signature}, nil
}

// coordinatorIdentityForTokenTransaction returns the identity public key
// recorded as the coordinator on this SO's TokenTransaction row. On a
// participant it comes from ctx, where DispatchPrepare attached it after
// resolving the engine's coordinator_index against the operator config —
// DispatchPrepare fails closed on an unresolvable index and rejects an index
// naming the receiving SO, so a missing ctx value here can only mean the
// coordinator's own self-Prepare (the engine calls Prepare directly, no
// ConsensusPrepare RPC), where falling back to this SO's own identity key is
// correct by definition.
func (h *TokenTransactionFlowHandler) coordinatorIdentityForTokenTransaction(ctx context.Context) keys.Public {
	if coordinatorPubKey, ok := consensus.CoordinatorIdentityFromContext(ctx); ok {
		return coordinatorPubKey
	}
	return h.config.IdentityPublicKey()
}

// Commit persists the coordinator-aggregated peer signatures and finalizes the
// transaction (SIGNED -> FINALIZED). Idempotent against gossip redelivery: the
// peer-signature insert is an upsert (DoNothing on conflict) and an
// already-FINALIZED transaction is a no-op. A missing row is a hard error
// (deliberately asymmetric with Rollback's no-op) so redelivery keeps retrying
// instead of silently dropping a committed flow; if the row is genuinely gone
// the participant FlowExecution row stays IN_FLIGHT and surfaces via the
// flow_execution reconcile task.
func (h *TokenTransactionFlowHandler) Commit(ctx context.Context, op proto.Message) error {
	req, ok := op.(*pbinternal.TokenTransactionCommitRequest)
	if !ok {
		return fmt.Errorf("unexpected operation type %T for token transaction commit", op)
	}
	_, err := h.applyTokenTransactionCommit(ctx, req)
	return err
}

// applyTokenTransactionCommit applies the commit work on a single SO: verify
// every operator signature in the payload against the configured operator
// identity keys plus the required participation threshold, persist the peer
// signatures, and finalize the transaction. Shared by participant Commit and
// coordinator BuildCommitPayload so both sides apply identical state. Returns
// the loaded transaction ent so BuildCommitPayload can build the RPC response
// without refetching.
func (h *TokenTransactionFlowHandler) applyTokenTransactionCommit(ctx context.Context, req *pbinternal.TokenTransactionCommitRequest) (*ent.TokenTransaction, error) {
	signatures, err := h.parseOperatorSignatures(req.GetOperatorTransactionSignatures())
	if err != nil {
		return nil, err
	}
	tokenTxEnt, err := ent.FetchAndLockTokenTransactionDataByHash(ctx, req.GetFinalTokenTransactionHash())
	if err != nil {
		return nil, err
	}
	if tokenTxEnt.Edges.Create == nil {
		return nil, sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("consensus token transaction commit for %x: transaction is not a CREATE", req.GetFinalTokenTransactionHash()))
	}
	if err := NewInternalSignTokenHandler(h.config).validateAndPersistPeerSignatures(ctx, signatures, tokenTxEnt); err != nil {
		return nil, err
	}
	if err := NewInternalFinalizeTokenHandler(h.config).FinalizeMintOrCreateTransaction(ctx, tokenTxEnt); err != nil {
		return nil, err
	}
	return tokenTxEnt, nil
}

// parseOperatorSignatures converts the wire signature set into the
// operator-identifier-keyed map the shared verification/persistence helpers
// take. Fails closed on unknown operator keys and duplicates so a malformed
// commit payload can never dilute the threshold check.
func (h *TokenTransactionFlowHandler) parseOperatorSignatures(sigs []*pbinternal.TokenTransactionOperatorSignature) (operatorSignaturesMap, error) {
	signatures := make(operatorSignaturesMap, len(sigs))
	for _, sig := range sigs {
		pubKey, err := keys.ParsePublicKey(sig.GetOperatorIdentityPublicKey())
		if err != nil {
			return nil, sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("failed to parse operator identity public key in commit payload: %w", err))
		}
		identifier := h.config.GetOperatorIdentifierFromIdentityPublicKey(pubKey)
		if identifier == "" {
			return nil, sparkerrors.InvalidArgumentPublicKeyMismatch(fmt.Errorf("operator signature public key %x is not a configured signing operator", sig.GetOperatorIdentityPublicKey()))
		}
		if _, exists := signatures[identifier]; exists {
			return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("duplicate operator signature for operator %s", identifier))
		}
		signatures[identifier] = sig.GetSignature()
	}
	return signatures, nil
}

// Rollback deletes the TokenTransaction + TokenCreate rows this SO wrote in
// Prepare. Deletion (not a cancelled status) is required for retryability:
// token_create.token_identifier and
// token_transaction.finalized_token_transaction_hash are unconditionally
// unique, so a kept row — cancelled or not — would block the issuer's retry of
// the same create forever. Accepts both the canonical rollback payload and the
// prepare op echoed by the participant reconciler's presumed-abort path.
//
// Idempotent: a never-created or already-deleted transaction is a no-op, and a
// FINALIZED transaction absorbs a stray/redelivered rollback as a no-op (in
// correct 2PC a rollback never lands post-commit; classifyConsensusOp's
// terminal-row fence skips redelivery for terminal participant rows).
//
// If anything already references the TokenCreate row (a mint racing the
// aborted create in the SIGNED->rollback window, a freeze, an L1 announce),
// deletion would break referential integrity — the rows are instead marked
// SIGNED_CANCELLED and an error is logged for operator attention. Because
// every SO must pass the duplicate-identifier check for a create to succeed,
// a kept row blocks re-creation of that token fleet-wide (not just on this
// SO) until manual cleanup.
func (h *TokenTransactionFlowHandler) Rollback(ctx context.Context, op proto.Message) error {
	finalTokenTransactionHash, err := finalTokenTransactionHashFromOp(op)
	if err != nil {
		return err
	}
	logger := logging.GetLoggerFromContext(ctx)

	tokenTxEnt, err := ent.FetchAndLockTokenTransactionDataByHash(ctx, finalTokenTransactionHash)
	if err != nil {
		if ent.IsNotFound(err) {
			// Prepare never persisted on this SO, or a prior rollback already
			// deleted the rows.
			logger.Sugar().Infof("token transaction 2pc rollback: no transaction for hash %x, no-op", finalTokenTransactionHash)
			return nil
		}
		return err
	}

	switch tokenTxEnt.Status {
	case st.TokenTransactionStatusFinalized:
		logger.Sugar().Infof("token transaction 2pc rollback: transaction %s already FINALIZED, no-op", tokenTxEnt.ID)
		return nil
	case st.TokenTransactionStatusStartedCancelled, st.TokenTransactionStatusSignedCancelled:
		logger.Sugar().Infof("token transaction 2pc rollback: transaction %s already cancelled, no-op", tokenTxEnt.ID)
		return nil
	case st.TokenTransactionStatusSigned:
		// The one state this flow's Prepare leaves behind; fall through.
	default:
		return sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("token transaction 2pc rollback: transaction %s in unexpected status %s", tokenTxEnt.ID, tokenTxEnt.Status))
	}

	if tokenTxEnt.Edges.Create == nil {
		return sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("token transaction 2pc rollback: transaction %s is not a CREATE", tokenTxEnt.ID))
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return err
	}

	// Lock the TokenCreate row before the reference check so the check and the
	// delete below are atomic against a concurrent mint: mint validation takes
	// the same lock (ValidateMintDoesNotExceedMaxSupply's ForUpdate), and a
	// mint can legitimately reference a SIGNED-not-yet-FINALIZED create, so an
	// unlocked check could pass just before a racing mint inserts a
	// referencing output — turning the delete into a raw FK violation instead
	// of the graceful cancel branch.
	tokenCreateEnt, err := ent.GetTokenCreateByIdentifierForUpdate(ctx, tokenTxEnt.Edges.Create.TokenIdentifier)
	if err != nil {
		return sparkerrors.InternalDatabaseReadError(fmt.Errorf("failed to lock token create for rollback of transaction %s: %w", tokenTxEnt.ID, err))
	}

	referenced, err := tokenCreateHasExternalReferences(ctx, tokenCreateEnt)
	if err != nil {
		return err
	}
	if referenced {
		logger.Sugar().Errorf(
			"token transaction 2pc rollback: token create %s (identifier %x) is already referenced by other entities; marking transaction %s SIGNED_CANCELLED instead of deleting — the kept row blocks re-creating this token fleet-wide pending manual cleanup",
			tokenCreateEnt.ID, tokenCreateEnt.TokenIdentifier, tokenTxEnt.ID)
		if _, err := db.TokenTransaction.UpdateOne(tokenTxEnt).SetStatus(st.TokenTransactionStatusSignedCancelled).Save(ctx); err != nil {
			return sparkerrors.InternalDatabaseWriteError(fmt.Errorf("failed to mark token transaction %s cancelled during rollback: %w", tokenTxEnt.ID, err))
		}
		return nil
	}

	// Peer signature rows exist pre-commit only if a concurrent legacy attempt
	// for the same transaction progressed on this SO; clear them so the
	// transaction row can be deleted.
	if _, err := db.TokenTransactionPeerSignature.Delete().
		Where(tokentransactionpeersignature.HasTokenTransactionWith(tokentransaction.IDEQ(tokenTxEnt.ID))).
		Exec(ctx); err != nil {
		return sparkerrors.InternalDatabaseWriteError(fmt.Errorf("failed to delete peer signatures during rollback of token transaction %s: %w", tokenTxEnt.ID, err))
	}
	if err := db.TokenTransaction.DeleteOne(tokenTxEnt).Exec(ctx); err != nil {
		return sparkerrors.InternalDatabaseWriteError(fmt.Errorf("failed to delete token transaction %s during rollback: %w", tokenTxEnt.ID, err))
	}
	if err := db.TokenCreate.DeleteOne(tokenCreateEnt).Exec(ctx); err != nil {
		return sparkerrors.InternalDatabaseWriteError(fmt.Errorf("failed to delete token create %s during rollback: %w", tokenCreateEnt.ID, err))
	}
	logger.Sugar().Infof("token transaction 2pc rollback: deleted transaction %s and token create %s (identifier %x)", tokenTxEnt.ID, tokenCreateEnt.ID, tokenCreateEnt.TokenIdentifier)
	return nil
}

// tokenCreateHasExternalReferences reports whether any entity outside this
// create transaction references the TokenCreate row (mint outputs, freezes,
// an L1 announce). Such references make deletion unsafe.
func tokenCreateHasExternalReferences(ctx context.Context, tokenCreateEnt *ent.TokenCreate) (bool, error) {
	if exists, err := tokenCreateEnt.QueryTokenOutput().Exist(ctx); err != nil || exists {
		return exists, err
	}
	if exists, err := tokenCreateEnt.QueryTokenFreeze().Exist(ctx); err != nil || exists {
		return exists, err
	}
	return tokenCreateEnt.QueryL1TokenCreate().Exist(ctx)
}

// finalTokenTransactionHashFromOp resolves the final transaction hash from a
// rollback decision payload. The canonical rollback carries the hash directly;
// the reconciler's presumed-abort path echoes the persisted prepare op, whose
// hash is re-derived exactly as Prepare derived it (execute_before is set on
// the transaction before hashing).
func finalTokenTransactionHashFromOp(op proto.Message) ([]byte, error) {
	switch r := op.(type) {
	case *pbinternal.TokenTransactionRollbackRequest:
		if len(r.GetFinalTokenTransactionHash()) == 0 {
			return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("final_token_transaction_hash is required for token transaction rollback"))
		}
		return r.GetFinalTokenTransactionHash(), nil
	case *pbinternal.TokenTransactionPrepareRequest:
		return hashPreparedTokenTransaction(r)
	default:
		return nil, fmt.Errorf("unexpected operation type %T for token transaction rollback", op)
	}
}

// hashPreparedTokenTransaction computes the final transaction hash of a
// prepare op the same way Prepare does: execute_before is applied to the
// transaction before hashing. The input message is cloned so callers holding
// the persisted prepare payload never observe a mutation.
func hashPreparedTokenTransaction(prepareReq *pbinternal.TokenTransactionPrepareRequest) ([]byte, error) {
	if prepareReq.GetFinalTokenTransaction() == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("prepared token transaction op has no final_token_transaction"))
	}
	finalTx, ok := proto.Clone(prepareReq.GetFinalTokenTransaction()).(*tokenpb.TokenTransaction)
	if !ok {
		return nil, fmt.Errorf("failed to clone prepared token transaction")
	}
	finalTx.ExecuteBefore = prepareReq.GetExecuteBefore()
	hash, err := utils.HashTokenTransaction(finalTx, false)
	if err != nil {
		return nil, fmt.Errorf("failed to hash prepared token transaction: %w", err)
	}
	return hash, nil
}

// ValidateDecisionAgainstPrepare implements consensus.PrepareBoundFlowHandler:
// commit and rollback resolve their target row purely from the
// final_token_transaction_hash carried in the decision payload, so bind that
// hash to the transaction this SO actually prepared. Also requires a commit to
// carry a non-empty signature set — a commit with no operator signatures could
// otherwise reach the handler and fail there repeatedly instead of being
// fenced loudly here.
func (h *TokenTransactionFlowHandler) ValidateDecisionAgainstPrepare(prepareOp proto.Message, decisionOp proto.Message) error {
	prepareReq, ok := prepareOp.(*pbinternal.TokenTransactionPrepareRequest)
	if !ok {
		return fmt.Errorf("unexpected prepare op type %T for token transaction decision validation", prepareOp)
	}
	preparedHash, err := hashPreparedTokenTransaction(prepareReq)
	if err != nil {
		return err
	}

	var decisionHash []byte
	switch d := decisionOp.(type) {
	case *pbinternal.TokenTransactionCommitRequest:
		if len(d.GetOperatorTransactionSignatures()) == 0 {
			return fmt.Errorf("token transaction commit for %x carries no operator signatures", preparedHash)
		}
		if len(d.GetFinalTokenTransactionHash()) == 0 {
			return fmt.Errorf("token transaction commit carries no final_token_transaction_hash")
		}
		decisionHash = d.GetFinalTokenTransactionHash()
	case *pbinternal.TokenTransactionRollbackRequest:
		if len(d.GetFinalTokenTransactionHash()) == 0 {
			return fmt.Errorf("token transaction rollback carries no final_token_transaction_hash")
		}
		decisionHash = d.GetFinalTokenTransactionHash()
	case *pbinternal.TokenTransactionPrepareRequest:
		// Presumed-abort rollback: the reconciler echoes the persisted prepare
		// op as the decision.
		decisionHash, err = hashPreparedTokenTransaction(d)
		if err != nil {
			return err
		}
	default:
		return fmt.Errorf("unexpected decision op type %T for token transaction decision validation", decisionOp)
	}

	if !bytes.Equal(decisionHash, preparedHash) {
		return fmt.Errorf("token transaction decision hash %x does not match prepared transaction hash %x", decisionHash, preparedHash)
	}
	return nil
}

// ---------------------------------------------------------------------------
// tokenTransactionCoordinatorFlow — coordinator side
// ---------------------------------------------------------------------------

type tokenTransactionCoordinatorFlow struct {
	*TokenTransactionFlowHandler

	// finalTokenTransaction is the SO-constructed final transaction in the
	// legacy V2 wire shape (the shape the internal signing leg takes), with
	// execute_before carried separately exactly like the legacy fanout.
	finalTokenTransaction     *tokenpb.TokenTransaction
	finalTokenTransactionHash []byte
	ownerSignatures           []*tokenpb.SignatureWithIndex
	executeBefore             *timestamppb.Timestamp

	// finalTokenTransactionForResponse is the V3 Final shape echoed back to
	// the client in the RPC response.
	finalTokenTransactionForResponse *tokenpb.FinalTokenTransaction

	// response is populated in BuildCommitPayload for the entrypoint to return.
	response *tokenpb.BroadcastTransactionResponse
}

var _ consensus.CoordinatorFlow = (*tokenTransactionCoordinatorFlow)(nil)

// newTokenTransactionCoordinatorFlow builds the coordinator flow for a create
// transaction. finalTokenTransactionHash must be the hash of
// finalTokenTransaction with execute_before applied (the entrypoint computes
// it once and every stage reuses it).
func newTokenTransactionCoordinatorFlow(
	config *so.Config,
	finalTokenTransaction *tokenpb.TokenTransaction,
	finalTokenTransactionHash []byte,
	ownerSignatures []*tokenpb.SignatureWithIndex,
	executeBefore *timestamppb.Timestamp,
	finalTokenTransactionForResponse *tokenpb.FinalTokenTransaction,
) *tokenTransactionCoordinatorFlow {
	return &tokenTransactionCoordinatorFlow{
		TokenTransactionFlowHandler:      NewTokenTransactionFlowHandler(config),
		finalTokenTransaction:            finalTokenTransaction,
		finalTokenTransactionHash:        finalTokenTransactionHash,
		ownerSignatures:                  ownerSignatures,
		executeBefore:                    executeBefore,
		finalTokenTransactionForResponse: finalTokenTransactionForResponse,
	}
}

func (f *tokenTransactionCoordinatorFlow) PrepareOp() proto.Message {
	return &pbinternal.TokenTransactionPrepareRequest{
		FinalTokenTransaction:      f.finalTokenTransaction,
		TokenTransactionSignatures: f.ownerSignatures,
		ExecuteBefore:              f.executeBefore,
	}
}

// BuildCommitPayload collects each SO's ECDSA signature from the prepare
// results, builds the commit payload, and applies the commit on the
// coordinator's own DB in the same request transaction (verify signatures +
// threshold, persist peer signatures, finalize) so the finalized state lands
// atomically with the engine's COMMITTED decision. Participants do the same
// work via FlowHandler.Commit after receiving the commit gossip. The
// signature verification itself runs inside applyTokenTransactionCommit
// (validateAndPersistPeerSignatures), identical to the legacy path.
func (f *tokenTransactionCoordinatorFlow) BuildCommitPayload(ctx context.Context, results map[string]*anypb.Any) (proto.Message, error) {
	operatorIDs := make([]string, 0, len(results))
	for operatorID := range results {
		operatorIDs = append(operatorIDs, operatorID)
	}
	slices.Sort(operatorIDs)

	operatorSigs := make([]*pbinternal.TokenTransactionOperatorSignature, 0, len(results))
	for _, operatorID := range operatorIDs {
		operator, ok := f.config.SigningOperatorMap[operatorID]
		if !ok {
			return nil, fmt.Errorf("unknown operator identifier %q in prepare results", operatorID)
		}
		result := results[operatorID]
		if result == nil {
			return nil, fmt.Errorf("operator %s returned no prepare result for token transaction %x", operatorID, f.finalTokenTransactionHash)
		}
		msg, err := result.UnmarshalNew()
		if err != nil {
			return nil, fmt.Errorf("failed to unmarshal prepare result from operator %s: %w", operatorID, err)
		}
		prepareResp, ok := msg.(*pbinternal.TokenTransactionPrepareResponse)
		if !ok {
			return nil, fmt.Errorf("unexpected prepare result type %T from operator %s", msg, operatorID)
		}
		if len(prepareResp.GetSparkOperatorSignature()) == 0 {
			return nil, fmt.Errorf("operator %s returned an empty signature for token transaction %x", operatorID, f.finalTokenTransactionHash)
		}
		operatorSigs = append(operatorSigs, &pbinternal.TokenTransactionOperatorSignature{
			OperatorIdentityPublicKey: operator.IdentityPublicKey.Serialize(),
			Signature:                 prepareResp.GetSparkOperatorSignature(),
		})
	}

	commitReq := &pbinternal.TokenTransactionCommitRequest{
		FinalTokenTransactionHash:     f.finalTokenTransactionHash,
		OperatorTransactionSignatures: operatorSigs,
	}
	tokenTxEnt, err := f.applyTokenTransactionCommit(ctx, commitReq)
	if err != nil {
		return nil, err
	}

	f.response = &tokenpb.BroadcastTransactionResponse{
		FinalTokenTransaction: f.finalTokenTransactionForResponse,
		CommitStatus:          tokenpb.CommitStatus_COMMIT_FINALIZED,
		TokenIdentifier:       tokenTxEnt.Edges.Create.TokenIdentifier,
	}
	return commitReq, nil
}

func (f *tokenTransactionCoordinatorFlow) RollbackPayload() proto.Message {
	return &pbinternal.TokenTransactionRollbackRequest{FinalTokenTransactionHash: f.finalTokenTransactionHash}
}
