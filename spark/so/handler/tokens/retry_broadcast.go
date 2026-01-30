package tokens

import (
	"context"
	"fmt"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	spark "github.com/lightsparkdev/spark"
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

	ids, err := findTransactionIDsNeedingRetry(ctx, config)
	if err != nil {
		return err
	}

	if len(ids) == 0 {
		return nil
	}

	logger.Sugar().Infof("Found %d SIGNED token transactions that need broadcast retry", len(ids))

	broadcastHandler := NewBroadcastTokenHandler(config)
	var errs []error

	// Process each transaction one at a time to minimize lock duration
	for _, id := range ids {
		if err := retryTokenTransactionBroadcast(ctx, config, broadcastHandler, id); err != nil {
			logger.Error("Failed to retry token transaction broadcast",
				zap.String("token_transaction_id", id.String()),
				zap.Error(err))
			errs = append(errs, fmt.Errorf("failed to retry tx %s: %w", id, err))
		} else {
			logger.Info("Successfully retried token transaction broadcast",
				zap.String("token_transaction_id", id.String()))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("failed to retry %d/%d transactions", len(errs), len(ids))
	}
	return nil
}

// findTransactionIDsNeedingRetry queries for IDs of SIGNED transactions that need broadcast retry.
// It finds transactions where:
// 1. This SO is the coordinator
// 2. Have operator_signature set (coordinator finished its attempt)
// 3. Not expired
// 4. Version >= V3 (phase 2 transactions only)
// 5. Have insufficient peer signatures
// 6. Created within the max validity duration (older transactions are complete or expired)
func findTransactionIDsNeedingRetry(ctx context.Context, config *so.Config) ([]uuid.UUID, error) {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, sparkerrors.InternalDatabaseTransactionLifecycleError(fmt.Errorf("retry broadcast: failed to get database from context: %w", err))
	}

	now := time.Now()
	requiredOperators := config.TokenRequiredParticipatingOperatorsCount()

	var ids []uuid.UUID
	err = db.TokenTransaction.Query().
		Where(
			tokentransaction.StatusEQ(st.TokenTransactionStatusSigned),
			tokentransaction.CoordinatorPublicKeyEQ(config.IdentityPublicKey()),
			tokentransaction.OperatorSignatureNotNil(),
			tokentransaction.CreateTimeGT(now.Add(-spark.TokenMaxValidityDuration)),
			tokentransaction.Or(
				tokentransaction.ExpiryTimeGT(now),
				tokentransaction.ExpiryTimeIsNil(),
			),
			tokentransaction.VersionGTE(st.TokenTransactionVersionV3),
		).
		Modify(func(s *sql.Selector) {
			// Filter for transactions with insufficient peer signatures using a correlated subquery.
			// We need (peer_signature_count + 1) < requiredOperators, where +1 is our own signature.
			s.Where(sql.P(func(b *sql.Builder) {
				b.WriteString("(SELECT COUNT(*) FROM token_transaction_peer_signatures ps WHERE ps.token_transaction_peer_signatures = ")
				b.Ident(s.C(tokentransaction.FieldID))
				b.WriteString(") + 1 < ")
				b.WriteString(fmt.Sprintf("%d", requiredOperators))
			}))
			// Order by create_time to process oldest transactions first
			s.OrderBy(sql.Asc(s.C(tokentransaction.FieldCreateTime)))
		}).
		Limit(100).
		Select(tokentransaction.FieldID).
		Scan(ctx, &ids)
	if err != nil {
		return nil, sparkerrors.InternalDatabaseReadError(fmt.Errorf("retry broadcast: failed to query transaction IDs: %w", err))
	}

	return ids, nil
}

func retryTokenTransactionBroadcast(
	ctx context.Context,
	config *so.Config,
	broadcastHandler *BroadcastTokenHandler,
	id uuid.UUID,
) error {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return sparkerrors.InternalDatabaseTransactionLifecycleError(fmt.Errorf("retry broadcast: failed to get database from context: %w", err))
	}

	// Fetch and lock this specific transaction
	tokenTx, err := db.TokenTransaction.Query().
		Where(tokentransaction.IDEQ(id)).
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
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			// Transaction was locked by another process or no longer exists, skip it
			return nil
		}
		return sparkerrors.InternalDatabaseReadError(fmt.Errorf("retry broadcast: failed to fetch token transaction %s: %w", id, err))
	}

	// Check if the transaction expired between finding the ID and processing it.
	// This indicates the retry mechanism is too slow and should trigger an alert.
	if !tokenTx.ExpiryTime.IsZero() && time.Now().After(tokenTx.ExpiryTime) {
		return sparkerrors.InternalOperationTooSlow(
			fmt.Errorf("retry broadcast: transaction %s expired before retry could complete", id))
	}

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
