package tokens

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	tokeninternalpb "github.com/lightsparkdev/spark/proto/spark_token_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authz"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/helper"
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/lightsparkdev/spark/so/protoconverter"
	"github.com/lightsparkdev/spark/so/tokens"
	"github.com/lightsparkdev/spark/so/utils"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type BroadcastTokenHandler struct {
	config                   *so.Config
	startTokenHandler        *StartTokenTransactionHandler
	signTokenHandler         *SignTokenHandler
	internalBroadcastHandler *InternalBroadcastTokenHandler
}

// NewBroadcastTokenHandler creates a new BroadcastTokenHandler.
func NewBroadcastTokenHandler(config *so.Config) *BroadcastTokenHandler {
	return &BroadcastTokenHandler{
		config:                   config,
		startTokenHandler:        NewStartTokenTransactionHandler(config),
		signTokenHandler:         NewSignTokenHandler(config),
		internalBroadcastHandler: NewInternalBroadcastTokenHandler(config),
	}
}

// BroadcastTokenTransaction combines start and commit into a single call for simplified transaction flows.
func (h *BroadcastTokenHandler) BroadcastTokenTransaction(
	ctx context.Context,
	req *tokenpb.BroadcastTransactionRequest,
) (*tokenpb.BroadcastTransactionResponse, error) {
	knobService := knobs.GetKnobsService(ctx)
	if knobService != nil && !knobService.RolloutRandom(knobs.KnobTokenTransactionV3Enabled, 0) {
		return nil, status.Error(codes.Unimplemented, "BroadcastTokenTransaction is not enabled")
	}

	partial := req.GetPartialTokenTransaction()
	if partial == nil {
		return nil, status.Error(codes.InvalidArgument, "partial token transaction is required")
	}
	if partial.GetVersion() < 3 {
		return nil, sparkerrors.InvalidArgumentMalformedField(
			fmt.Errorf("broadcast transaction requires version 3+ partial token transaction, got %d", partial.GetVersion()),
		)
	}

	if knobService != nil && knobService.RolloutRandom(knobs.KnobTokenTransactionV3Phase2Enabled, 0) {
		return h.broadcastTokenTransactionPhase2(ctx, req)
	}
	return h.broadcastTokenTransactionPhase1(ctx, req)
}

// broadcastTokenTransactionPhase1 uses the existing two-step flow: StartTokenTransaction prepares
// the transaction across all operators, then CommitTransaction finalizes it. This is the legacy
// approach that requires both steps to succeed atomically.
func (h *BroadcastTokenHandler) broadcastTokenTransactionPhase1(
	ctx context.Context,
	req *tokenpb.BroadcastTransactionRequest,
) (*tokenpb.BroadcastTransactionResponse, error) {
	startReq, err := protoconverter.ConvertBroadcastToStart(req)
	if err != nil {
		return nil, fmt.Errorf("failed to convert broadcast request to start request: %w", err)
	}

	startResponse, err := h.startTokenHandler.StartTokenTransaction(ctx, startReq)
	if err != nil {
		return nil, fmt.Errorf("failed to start transaction: %w", err)
	}

	// Persist the Start operation right away so later failures don't roll back the prepared state.
	if err := ent.DbCommit(ctx); err != nil {
		return nil, fmt.Errorf("failed to commit start transaction: %w", err)
	}

	finalTx := startResponse.GetFinalTokenTransaction()
	finalTxHash, err := utils.HashTokenTransaction(finalTx, false)
	if err != nil {
		return nil, fmt.Errorf("failed to hash final token transaction: %w", err)
	}

	commitReq := &tokenpb.CommitTransactionRequest{
		FinalTokenTransaction:          finalTx,
		FinalTokenTransactionHash:      finalTxHash,
		InputTtxoSignaturesPerOperator: nil,
		OwnerIdentityPublicKey:         req.GetIdentityPublicKey(),
	}

	commitResponse, err := h.signTokenHandler.CommitTransaction(ctx, commitReq)
	if err != nil {
		return nil, fmt.Errorf("failed to commit transaction: %w", err)
	}

	finalForResponse, err := protoconverter.ConvertV2TxShapeToFinal(finalTx)
	if err != nil {
		return nil, fmt.Errorf("failed to convert final transaction for response: %w", err)
	}

	return &tokenpb.BroadcastTransactionResponse{
		FinalTokenTransaction: finalForResponse,
		CommitStatus:          commitResponse.GetCommitStatus(),
		CommitProgress:        commitResponse.GetCommitProgress(),
		TokenIdentifier:       commitResponse.GetTokenIdentifier(),
	}, nil
}

// broadcastTokenTransactionPhase2 handles signing in a single coordinated operation. The coordinator
// signs and commits locally first, then fans out to other operators. This enables retry logic for
// failed cross-operator communication since the coordinator's state is already persisted.
func (h *BroadcastTokenHandler) broadcastTokenTransactionPhase2(
	ctx context.Context,
	req *tokenpb.BroadcastTransactionRequest,
) (*tokenpb.BroadcastTransactionResponse, error) {
	idPubKey, err := keys.ParsePublicKey(req.GetIdentityPublicKey())
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("invalid identity public key: %w", err))
	}
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, idPubKey); err != nil {
		return nil, err
	}

	partial := req.GetPartialTokenTransaction()
	metadata := partial.GetTokenTransactionMetadata()
	if metadata == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("token transaction metadata is required"))
	}

	// Validate the partial transaction up-front (mirrors StartTokenTransaction).
	// Convert to V2 shape to allow us to share all of the same validation logic with StartTokenTransaction.
	// TODO(SPARK-334): After the switch to require V3+ transactions, stop converting to V2 shape and just use the partial directly for all logic.
	partialTxV2Shape, err := protoconverter.ConvertPartialToV2TxShape(partial)
	if err != nil {
		return nil, err
	}

	network, err := btcnetwork.FromProtoNetwork(metadata.Network)
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("failed to get network from proto network: %w", err))
	}
	expectedBondSats := h.config.Lrc20Configs[strings.ToLower(network.String())].WithdrawBondSats
	expectedRelativeBlockLocktime := h.config.Lrc20Configs[strings.ToLower(network.String())].WithdrawRelativeBlockLocktime
	if err := utils.ValidatePartialTokenTransaction(
		ctx,
		partialTxV2Shape,
		req.GetTokenTransactionOwnerSignatures(),
		h.config.GetSigningOperatorList(),
		h.config.SupportedNetworks,
		expectedBondSats,
		expectedRelativeBlockLocktime,
	); err != nil {
		return nil, err
	}

	partialHash, err := utils.HashTokenTransaction(partialTxV2Shape, true)
	if err != nil {
		return nil, fmt.Errorf("failed to hash partial token transaction: %w", err)
	}
	existingPartialTx, err := ent.FetchPartialTokenTransactionData(ctx, partialHash)
	if err != nil && !ent.IsNotFound(err) {
		return nil, fmt.Errorf("failed to fetch partial token transaction: %w", err)
	}
	if existingPartialTx != nil {
		return h.handleExistingTransaction(ctx, existingPartialTx)
	}

	if err := preemptOrRejectTransactions(ctx, partialTxV2Shape); err != nil {
		return nil, err
	}

	finalTx, keyshareIDs, err := h.constructFinalTokenTransaction(
		ctx,
		partial,
	)
	if err != nil {
		return nil, err
	}

	legacyTokenTx, err := protoconverter.ConvertFinalToV2TxShape(finalTx)
	if err != nil {
		return nil, err
	}

	// Sign and save the transaction on the coordinator *before* signing with other operators.
	// This allows the coordinator to re-attempt failed signing requests to non-coordinators in the event of a failure (up until expiration time).
	localResp, err := h.internalBroadcastHandler.BroadcastTokenTransactionInternal(ctx, &tokeninternalpb.BroadcastTransactionInternalRequest{
		KeyshareIds:                keyshareIDs,
		FinalTokenTransaction:      legacyTokenTx,
		TokenTransactionSignatures: req.GetTokenTransactionOwnerSignatures(),
		CoordinatorPublicKey:       h.config.IdentityPublicKey().Serialize(),
	})
	if err != nil {
		return nil, err
	}
	if err := ent.DbCommit(ctx); err != nil {
		return nil, fmt.Errorf("failed to commit token transaction broadcast: %w", err)
	}

	signatures := make(operatorSignaturesMap)
	signatures[h.config.Identifier] = localResp.SparkOperatorSignature

	excludeSelf := helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}
	internalSignatures, err := helper.ExecuteTaskWithAllOperators(ctx, h.config, &excludeSelf,
		func(ctx context.Context, operator *so.SigningOperator) (*tokeninternalpb.BroadcastTransactionInternalResponse, error) {
			conn, err := operator.NewOperatorGRPCConnection()
			if err != nil {
				return nil, err
			}
			defer conn.Close()

			client := tokeninternalpb.NewSparkTokenInternalServiceClient(conn)
			return client.BroadcastTokenTransactionInternal(ctx, &tokeninternalpb.BroadcastTransactionInternalRequest{
				KeyshareIds:                keyshareIDs,
				FinalTokenTransaction:      legacyTokenTx,
				TokenTransactionSignatures: req.GetTokenTransactionOwnerSignatures(),
				CoordinatorPublicKey:       h.config.IdentityPublicKey().Serialize(),
			})
		},
	)
	if err != nil {
		return nil, sparkerrors.WrapErrorWithReasonPrefix(err, sparkerrors.ErrorReasonPrefixFailedWithExternalCoordinator)
	}

	for opID, resp := range internalSignatures {
		signatures[opID] = resp.SparkOperatorSignature
	}

	finalTxHash, err := utils.HashTokenTransaction(legacyTokenTx, false)
	if err != nil {
		return nil, fmt.Errorf("failed to hash final token transaction: %w", err)
	}

	tokenTxEnt, err := ent.FetchAndLockTokenTransactionDataByHash(ctx, finalTxHash)
	if err != nil {
		return nil, err
	}

	internalSignHandler := NewInternalSignTokenHandler(h.config)
	if err := internalSignHandler.validateSignaturesPackageAndPersistPeerSignatures(ctx, signatures, tokenTxEnt); err != nil {
		return nil, err
	}

	txType, err := utils.InferTokenTransactionType(legacyTokenTx)
	if err != nil {
		return nil, err
	}

	switch txType {
	case utils.TokenTransactionTypeCreate:
		tokenMetadata, err := common.NewTokenMetadataFromCreateInput(legacyTokenTx.GetCreateInput(), legacyTokenTx.GetNetwork())
		if err != nil {
			return nil, sparkerrors.InternalObjectMalformedField(fmt.Errorf("failed to create token metadata: %w", err))
		}
		tokenIdentifier, err := tokenMetadata.ComputeTokenIdentifier()
		if err != nil {
			return nil, sparkerrors.InternalObjectMalformedField(fmt.Errorf("failed to compute token identifier: %w", err))
		}
		return &tokenpb.BroadcastTransactionResponse{
			FinalTokenTransaction: finalTx,
			CommitStatus:          tokenpb.CommitStatus_COMMIT_FINALIZED,
			CommitProgress:        nil,
			TokenIdentifier:       tokenIdentifier,
		}, nil
	case utils.TokenTransactionTypeMint:
		return &tokenpb.BroadcastTransactionResponse{
			FinalTokenTransaction: finalTx,
			CommitStatus:          tokenpb.CommitStatus_COMMIT_FINALIZED,
			CommitProgress:        nil,
			TokenIdentifier:       finalTx.GetMintInput().GetTokenIdentifier(),
		}, nil
	case utils.TokenTransactionTypeTransfer:
		mappedSigs := make(map[string]*tokeninternalpb.SignTokenTransactionFromCoordinationResponse, len(signatures))
		for k, v := range signatures {
			mappedSigs[k] = &tokeninternalpb.SignTokenTransactionFromCoordinationResponse{SparkOperatorSignature: v}
		}
		commitResp, err := h.signTokenHandler.ExchangeRevocationSecretsAndFinalizeIfPossible(ctx, legacyTokenTx, mappedSigs, finalTxHash)
		if err != nil {
			return nil, err
		}
		return &tokenpb.BroadcastTransactionResponse{
			FinalTokenTransaction: finalTx,
			CommitStatus:          commitResp.GetCommitStatus(),
			CommitProgress:        commitResp.GetCommitProgress(),
			TokenIdentifier:       commitResp.GetTokenIdentifier(),
		}, nil
	default:
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("token transaction type not supported: %s", txType))
	}
}

func (h *BroadcastTokenHandler) constructFinalTokenTransaction(
	ctx context.Context,
	partial *tokenpb.PartialTokenTransaction,
) (*tokenpb.FinalTokenTransaction, []string, error) {
	metadata := partial.GetTokenTransactionMetadata()
	if metadata == nil {
		return nil, nil, sparkerrors.InternalObjectMissingField(fmt.Errorf("token transaction metadata is required"))
	}

	final := &tokenpb.FinalTokenTransaction{
		Version:                  partial.Version,
		TokenTransactionMetadata: metadata,
	}

	if mint := partial.GetMintInput(); mint != nil {
		final.TokenInputs = &tokenpb.FinalTokenTransaction_MintInput{MintInput: mint}
	} else if transfer := partial.GetTransferInput(); transfer != nil {
		final.TokenInputs = &tokenpb.FinalTokenTransaction_TransferInput{TransferInput: transfer}
	} else if create := partial.GetCreateInput(); create != nil {
		final.TokenInputs = &tokenpb.FinalTokenTransaction_CreateInput{CreateInput: create}
	}

	numOutputs := 0
	if partial.PartialTokenOutputs != nil {
		numOutputs = len(partial.PartialTokenOutputs)
		final.FinalTokenOutputs = make([]*tokenpb.FinalTokenOutput, numOutputs)
		for i, pOut := range partial.PartialTokenOutputs {
			// Copy to avoid mutating the request proto.
			pOutCopy := &tokenpb.PartialTokenOutput{
				OwnerPublicKey:                pOut.OwnerPublicKey,
				WithdrawBondSats:              pOut.WithdrawBondSats,
				WithdrawRelativeBlockLocktime: pOut.WithdrawRelativeBlockLocktime,
				TokenIdentifier:               pOut.TokenIdentifier,
				TokenAmount:                   pOut.TokenAmount,
			}
			final.FinalTokenOutputs[i] = &tokenpb.FinalTokenOutput{
				PartialTokenOutput: pOutCopy,
			}
		}
	}

	keyshareIDStrings := make([]string, numOutputs)

	inputType, err := utils.InferTokenTransactionType(final)
	if err != nil {
		return nil, nil, err
	}

	switch inputType {
	case utils.TokenTransactionTypeCreate:
		db, err := ent.GetDbFromContext(ctx)
		if err != nil {
			return nil, nil, sparkerrors.InternalDatabaseReadError(fmt.Errorf("failed to get database from context: %w", err))
		}
		creationEntityPubKey, err := ent.GetEntityDkgKeyPublicKey(ctx, db)
		if err != nil {
			return nil, nil, err
		}
		final.GetCreateInput().CreationEntityPublicKey = creationEntityPubKey.Serialize()
	case utils.TokenTransactionTypeMint, utils.TokenTransactionTypeTransfer:
		keyshares, err := ent.GetUnusedSigningKeyshares(ctx, h.config, numOutputs)
		if err != nil {
			return nil, nil, sparkerrors.InternalKeyshareError(fmt.Errorf("failed to get unused keyshares: %w", err))
		}
		if len(keyshares) < numOutputs {
			return nil, nil, sparkerrors.InternalKeyshareError(fmt.Errorf("%s: %d needed, %d available", tokens.ErrNotEnoughUnusedKeyshares, numOutputs, len(keyshares)))
		}

		network, err := btcnetwork.FromProtoNetwork(final.TokenTransactionMetadata.Network)
		if err != nil {
			return nil, nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("failed to parse network: %w", err))
		}

		lrc20Config := h.config.Lrc20Configs[strings.ToLower(network.String())]

		for i, output := range final.FinalTokenOutputs {
			keyshareIDStrings[i] = keyshares[i].ID.String()
			output.RevocationCommitment = keyshares[i].PublicKey.Serialize()
			if output.PartialTokenOutput != nil {
				output.PartialTokenOutput.WithdrawBondSats = lrc20Config.WithdrawBondSats
				output.PartialTokenOutput.WithdrawRelativeBlockLocktime = lrc20Config.WithdrawRelativeBlockLocktime
			}
		}
	default:
		return nil, nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("unsupported transaction type"))
	}

	return final, keyshareIDStrings, nil
}

// handleExistingTransaction handles the case where a transaction with the same partial hash already exists.
// It returns the current commit status and progress for the transaction.
func (h *BroadcastTokenHandler) handleExistingTransaction(
	ctx context.Context,
	existingTx *ent.TokenTransaction,
) (*tokenpb.BroadcastTransactionResponse, error) {
	txProto, err := existingTx.MarshalProto(ctx, h.config)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal existing transaction: %w", err)
	}
	finalTx, err := protoconverter.ConvertV2TxShapeToFinal(txProto)
	if err != nil {
		return nil, fmt.Errorf("failed to convert transaction to final shape: %w", err)
	}

	if existingTx.Status == st.TokenTransactionStatusFinalized {
		return &tokenpb.BroadcastTransactionResponse{
			FinalTokenTransaction: finalTx,
			CommitStatus:          tokenpb.CommitStatus_COMMIT_FINALIZED,
			CommitProgress:        nil,
		}, nil
	}

	// Check if the transaction has expired (and not finalized).
	// so clients should check transaction status before creating a replacement.
	// Note: In rare cases the expired transaction might still be in the process of finalizing if
	// enough operators have signed, but not yet revealed. In this case if the client generates a new transaction
	// with the same outputs that broadcast will fail.
	if !existingTx.ExpiryTime.IsZero() && time.Now().After(existingTx.ExpiryTime) {
		return nil, sparkerrors.AlreadyExistsExpiredTransaction(
			fmt.Errorf("transaction already broadcasted and has expired at %s; please generate a new transaction to retry", existingTx.ExpiryTime),
		)
	}

	// For transfers, track reveal progress (secret share exchange) since that's the final commitment step.
	// For mint/create, track signing progress since there's no reveal step.
	txType, err := utils.InferTokenTransactionType(txProto)
	if err != nil {
		return nil, fmt.Errorf("failed to infer transaction type: %w", err)
	}

	var commitProgress *tokenpb.CommitProgress
	if txType == utils.TokenTransactionTypeTransfer {
		commitProgress, err = BuildRevealCommitProgress(existingTx, h.config)
		if err != nil {
			return nil, fmt.Errorf("failed to build reveal commit progress: %w", err)
		}
	} else {
		commitProgress, err = BuildSignedCommitProgress(existingTx, h.config)
		if err != nil {
			return nil, fmt.Errorf("failed to build signed commit progress: %w", err)
		}
	}

	return &tokenpb.BroadcastTransactionResponse{
		FinalTokenTransaction: finalTx,
		CommitStatus:          tokenpb.CommitStatus_COMMIT_PROCESSING,
		CommitProgress:        commitProgress,
	}, nil
}
