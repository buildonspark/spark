package tokens

import (
	"bytes"
	"context"
	"fmt"

	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/lightsparkdev/spark/common/keys"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	tokeninternalpb "github.com/lightsparkdev/spark/proto/spark_token_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/lightsparkdev/spark/so/utils"
)

type InternalBroadcastTokenHandler struct {
	config         *so.Config
	prepareHandler *InternalPrepareTokenHandler
	signHandler    *InternalSignTokenHandler
}

func NewInternalBroadcastTokenHandler(config *so.Config) *InternalBroadcastTokenHandler {
	return &InternalBroadcastTokenHandler{
		config:         config,
		prepareHandler: NewInternalPrepareTokenHandler(config),
		signHandler:    NewInternalSignTokenHandler(config),
	}
}

func (h *InternalBroadcastTokenHandler) BroadcastTokenTransactionInternal(
	ctx context.Context,
	req *tokeninternalpb.BroadcastTransactionInternalRequest,
) (*tokeninternalpb.BroadcastTransactionInternalResponse, error) {
	ctx, span := GetTracer().Start(ctx, "InternalBroadcastTokenHandler.BroadcastTokenTransactionInternal")
	defer span.End()

	knobService := knobs.GetKnobsService(ctx)
	phase2Enabled := knobService != nil && knobService.RolloutRandom(knobs.KnobTokenTransactionV3Phase2Enabled, 0)
	if !phase2Enabled {
		return nil, sparkerrors.UnimplementedMethodDisabled(fmt.Errorf("broadcastTokenTransactionInternal flow is not enabled"))
	}

	finalTokenTX := req.GetFinalTokenTransaction()
	if finalTokenTX == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("final token transaction is required"))
	}

	hash, err := utils.HashTokenTransaction(finalTokenTX, false)
	if err != nil {
		return nil, fmt.Errorf("failed to hash final token transaction: %w", err)
	}

	// Idempotency support: if the transaction already exists just sign/persist using the existing logic.
	existingTx, err := ent.FetchExistingTokenTransaction(ctx, hash)
	if err != nil && !ent.IsNotFound(err) {
		return nil, fmt.Errorf("failed to check for existing token transaction: %w", err)
	}
	if existingTx != nil {
		if existingTx.Status == st.TokenTransactionStatusSigned {
			// Return stored signature for sign requests if already signed.
			signature, err := h.signHandler.regenerateOperatorSignatureForDuplicateRequest(ctx, h.config, existingTx, hash)
			if err != nil {
				return nil, err
			}
			return &tokeninternalpb.BroadcastTransactionInternalResponse{
				SparkOperatorSignature: signature,
			}, nil
		} else {
			return nil, sparkerrors.AlreadyExistsDuplicateOperation(fmt.Errorf("repeat sign attempt but the transaction is not in signed state %s", existingTx.Status))
		}
	}

	inputTtxos, err := h.prepareHandler.validateAndLockForCommit(
		ctx,
		finalTokenTX,
		req.KeyshareIds,
		req.TokenTransactionSignatures,
		req.CoordinatorPublicKey,
	)
	if err != nil {
		return nil, err
	}
	coordinatorPubKey, err := keys.ParsePublicKey(req.CoordinatorPublicKey)
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("failed to parse coordinator public key: %w", err))
	}

	sig, err := h.createSignedTokenTransactionEntitiesAndSign(
		ctx,
		finalTokenTX,
		hash,
		req.TokenTransactionSignatures,
		req.KeyshareIds,
		inputTtxos,
		coordinatorPubKey,
	)
	if err != nil {
		return nil, fmt.Errorf("sign phase failed: %w", err)
	}

	return &tokeninternalpb.BroadcastTransactionInternalResponse{
		SparkOperatorSignature: sig,
	}, nil
}

func (h *InternalBroadcastTokenHandler) createSignedTokenTransactionEntitiesAndSign(
	ctx context.Context,
	finalTokenTransaction *tokenpb.TokenTransaction,
	finalTokenTransactionHash []byte,
	ownerSignatures []*tokenpb.SignatureWithIndex,
	keyshareIDs []string,
	orderedOutputToSpendEnts []*ent.TokenOutput,
	coordinatorPublicKey keys.Public,
) ([]byte, error) {
	ctx, span := GetTracer().Start(ctx, "InternalBroadcastTokenHandler.CreateSignedTokenTransactionEntitiesAndSign", GetProtoTokenTransactionTraceAttributes(ctx, finalTokenTransaction))
	defer span.End()

	if finalTokenTransaction == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("final token transaction cannot be nil"))
	}
	if finalTokenTransaction.GetVersion() < 3 {
		return nil, sparkerrors.InvalidArgumentInvalidVersion(fmt.Errorf("broadcast requires version 3+ token transaction, got %d", finalTokenTransaction.GetVersion()))
	}

	calculatedHash, err := utils.HashTokenTransaction(finalTokenTransaction, false)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(calculatedHash, finalTokenTransactionHash) {
		return nil, sparkerrors.FailedPreconditionHashMismatch(fmt.Errorf("final transaction hash mismatch: expected %x, got %x", calculatedHash, finalTokenTransactionHash))
	}

	operatorSignature := ecdsa.Sign(h.config.IdentityPrivateKey.ToBTCEC(), finalTokenTransactionHash).Serialize()
	_, err = ent.CreateSignedTransactionEntities(
		ctx,
		finalTokenTransaction,
		ownerSignatures,
		keyshareIDs,
		orderedOutputToSpendEnts,
		coordinatorPublicKey,
		operatorSignature,
	)
	if err != nil {
		return nil, err
	}

	return operatorSignature, nil
}
