package tokens

import (
	"context"
	"fmt"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/lightsparkdev/spark/common/logging"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/tokentransaction"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"go.uber.org/zap"
)

// RetryIncompleteSignatureBroadcasts finds SIGNED transactions where this SO is coordinator,
// has no/insufficient peer signatures, and re-attempts the broadcast fanout.
// This handles cases where the coordinator successfully signed but the fanout to other SOs failed.
func RetryIncompleteSignatureBroadcasts(ctx context.Context, config *so.Config) error {
	logger := logging.GetLoggerFromContext(ctx)
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return sparkerrors.InternalDatabaseTransactionLifecycleError(fmt.Errorf("retry broadcast: failed to get database from context: %w", err))
	}

	// Query for SIGNED transactions that:
	// 1. This SO is the coordinator
	// 2. Have operator_signature set (coordinator finished its attempt)
	// 3. Not expired
	// 4. Version >= V3 (phase 2 transactions only)
	now := time.Now()
	tokenTransactions, err := db.TokenTransaction.Query().
		Where(
			tokentransaction.StatusEQ(st.TokenTransactionStatusSigned),
			tokentransaction.CoordinatorPublicKeyEQ(config.IdentityPublicKey()),
			tokentransaction.OperatorSignatureNotNil(),
			tokentransaction.Or(
				tokentransaction.ExpiryTimeGT(now),
				tokentransaction.ExpiryTimeIsNil(),
			),
			tokentransaction.VersionGTE(st.TokenTransactionVersionV3),
		).
		WithPeerSignatures().
		WithCreatedOutput(func(q *ent.TokenOutputQuery) {
			q.WithRevocationKeyshare()
		}).
		WithSpentOutput(func(q *ent.TokenOutputQuery) {
			q.WithOutputCreatedTokenTransaction()
		}).
		WithMint().
		WithCreate().
		ForUpdate(sql.WithLockAction(sql.SkipLocked)).
		Limit(100).
		All(ctx)
	if err != nil {
		return sparkerrors.InternalDatabaseReadError(fmt.Errorf("retry broadcast: failed to query token transactions: %w", err))
	}

	if len(tokenTransactions) == 0 {
		return nil
	}

	// Filter to transactions that need retry (insufficient peer signatures)
	requiredOperators := getRequiredParticipatingOperatorsCount(config)
	var transactionsToRetry []*ent.TokenTransaction
	for _, tx := range tokenTransactions {
		// Count peer signatures (excludes this operator's signature which is in operator_signature field)
		peerSignatureCount := len(tx.Edges.PeerSignatures)
		// Need requiredOperators total signatures. We have 1 (ours) + peerSignatureCount.
		if peerSignatureCount+1 < requiredOperators {
			transactionsToRetry = append(transactionsToRetry, tx)
		}
	}

	if len(transactionsToRetry) == 0 {
		return nil
	}

	logger.Sugar().Infof("Found %d SIGNED token transactions that need broadcast retry", len(transactionsToRetry))

	broadcastHandler := NewBroadcastTokenHandler(config)
	var errs []error

	for _, tokenTx := range transactionsToRetry {
		if err := retryTokenTransactionBroadcast(ctx, config, broadcastHandler, tokenTx); err != nil {
			logger.Error("Failed to retry token transaction broadcast",
				zap.String("token_transaction_id", tokenTx.ID.String()),
				zap.Error(err))
			errs = append(errs, fmt.Errorf("failed to retry tx %s: %w", tokenTx.ID, err))
		} else {
			logger.Info("Successfully retried token transaction broadcast",
				zap.String("token_transaction_id", tokenTx.ID.String()))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("failed to retry %d/%d transactions", len(errs), len(transactionsToRetry))
	}
	return nil
}

func retryTokenTransactionBroadcast(
	ctx context.Context,
	config *so.Config,
	broadcastHandler *BroadcastTokenHandler,
	tokenTx *ent.TokenTransaction,
) error {
	// Marshal the token transaction to proto format
	legacyTokenTx, err := tokenTx.MarshalProto(ctx, config)
	if err != nil {
		return sparkerrors.InternalTypeConversionError(fmt.Errorf("retry broadcast: failed to marshal token transaction %s: %w", tokenTx.ID, err))
	}

	// Extract keyshare IDs from created outputs
	keyshareIDs := make([]string, 0, len(tokenTx.Edges.CreatedOutput))
	for _, output := range tokenTx.Edges.CreatedOutput {
		if output.Edges.RevocationKeyshare != nil {
			keyshareIDs = append(keyshareIDs, output.Edges.RevocationKeyshare.ID.String())
		}
	}

	// Extract owner signatures from spent outputs
	ownerSignatures := make([]*tokenpb.SignatureWithIndex, 0, len(tokenTx.Edges.SpentOutput))
	for _, output := range tokenTx.Edges.SpentOutput {
		if output.SpentOwnershipSignature != nil {
			ownerSignatures = append(ownerSignatures, &tokenpb.SignatureWithIndex{
				InputIndex: uint32(output.SpentTransactionInputVout),
				Signature:  output.SpentOwnershipSignature,
			})
		}
	}

	// Call FanoutBroadcastAndFinalize which is idempotent
	_, err = broadcastHandler.FanoutBroadcastAndFinalize(ctx, tokenTx, legacyTokenTx, keyshareIDs, ownerSignatures)
	return err
}

// getRequiredParticipatingOperatorsCount returns the number of operators required to
// sign/reveal to consider a transaction valid.
func getRequiredParticipatingOperatorsCount(config *so.Config) int {
	if config.Token.RequireThresholdOperators {
		return int(config.Threshold)
	}
	return len(config.SigningOperatorMap)
}
