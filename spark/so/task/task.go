package task

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"go.uber.org/zap"

	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/handler/tokens"
	"github.com/lightsparkdev/spark/so/helper"
	"github.com/lightsparkdev/spark/so/knobs"

	"entgo.io/ent/dialect/sql"
	"github.com/go-co-op/gocron/v2"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/logging"
	pbspark "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	tokeninternalpb "github.com/lightsparkdev/spark/proto/spark_token_internal"
	"github.com/lightsparkdev/spark/so"
	sodkg "github.com/lightsparkdev/spark/so/dkg"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/gossip"
	"github.com/lightsparkdev/spark/so/ent/pendingsendtransfer"
	"github.com/lightsparkdev/spark/so/ent/preimagerequest"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/signingkeyshare"
	"github.com/lightsparkdev/spark/so/ent/tokentransaction"
	"github.com/lightsparkdev/spark/so/ent/transfer"
	"github.com/lightsparkdev/spark/so/ent/tree"
	"github.com/lightsparkdev/spark/so/ent/treenode"
	"github.com/lightsparkdev/spark/so/ent/utxoswap"
	"github.com/lightsparkdev/spark/so/handler"
	tokenslogging "github.com/lightsparkdev/spark/so/tokens"
)

var (
	confirmPendingDKGKeysCutoffAge  = 15 * time.Minute
	defaultTaskTimeout              = 1 * time.Minute
	dkgTaskTimeout                  = 3 * time.Minute
	deleteStaleTreeNodesTaskTimeout = 10 * time.Minute
)

// Task contains common fields for all task types.
type Task func(context.Context, *so.Config) error

// BaseTaskSpec is a task that is scheduled to run.
type BaseTaskSpec struct { //nolint:revive
	// Name is the human-readable name of the task.
	Name string
	// Timeout is the maximum time the task is allowed to run before it will be cancelled.
	Timeout *time.Duration
	// Whether to run the task in the hermetic test environment.
	RunInTestEnv bool
	// If true, the task will not run
	Disabled bool
	// Task is the function that is run when the task is scheduled.
	Task func(context.Context, *so.Config, knobs.Knobs) error
}

// ScheduledTaskSpec is a task that runs on a schedule.
type ScheduledTaskSpec struct {
	BaseTaskSpec
	// ExecutionInterval is the interval between each run of the task.
	ExecutionInterval time.Duration
}

// StartupTaskSpec is a task that runs once at startup.
type StartupTaskSpec struct {
	BaseTaskSpec
	// RetryInterval is the interval between retries for startup tasks. If nil, no retries are performed.
	// Retries may be necessary if a startup task is dependent on other asynchronous setup, such as internal
	// GRPCs to other operators that may not be ready immediately upon the startup of this operator.
	RetryInterval *time.Duration
}

// AllScheduledTasks returns all the tasks that are scheduled to run.
func AllScheduledTasks() []ScheduledTaskSpec {
	return []ScheduledTaskSpec{
		{
			ExecutionInterval: 10 * time.Second,
			BaseTaskSpec: BaseTaskSpec{
				Name:         "dkg",
				Timeout:      &dkgTaskTimeout,
				RunInTestEnv: true,
				Task: func(ctx context.Context, config *so.Config, knobsService knobs.Knobs) error {
					return ent.RunDKGIfNeeded(ctx, config)
				},
			},
		},
		{
			ExecutionInterval: 5 * time.Minute,
			BaseTaskSpec: BaseTaskSpec{
				Name:         "confirm_pending_dkg_keys",
				RunInTestEnv: true,
				Task: func(ctx context.Context, config *so.Config, knobsService knobs.Knobs) error {
					logger := logging.GetLoggerFromContext(ctx)
					cutoff := time.Now().Add(-confirmPendingDKGKeysCutoffAge)

					// Find local coordinator PENDING keyshares and attempt confirmation fanout
					tx, err := ent.GetDbFromContext(ctx)
					if err != nil {
						return fmt.Errorf("failed to get or create current tx for request: %w", err)
					}
					if abandonedCount, err := tx.SigningKeyshare.Update().
						Where(
							signingkeyshare.StatusEQ(st.KeyshareStatusPending),
							signingkeyshare.CoordinatorIndexEQ(config.Index),
							signingkeyshare.CreateTimeLT(cutoff),
						).
						SetStatus(st.KeyshareStatusAbandoned).
						Save(ctx); err != nil {
						return err
					} else if abandonedCount > 0 {
						logger.Sugar().Errorf("Abandoned %d stale pending DKG keyshares (older than %s)", abandonedCount, cutoff)
					}

					// Attempt to confirm keys with all operators.
					// Use best-effort mode: if some keys are missing on some operators
					// (e.g., due to transaction rollback), we still mark the keys that
					// ARE available across all operators as AVAILABLE.
					// Paginate at the DB level to avoid building a huge in-memory list.
					const chunkSize = 1000
					var lastID uuid.UUID
					for {
						query := tx.SigningKeyshare.Query().
							Where(
								signingkeyshare.StatusEQ(st.KeyshareStatusPending),
								signingkeyshare.CoordinatorIndexEQ(config.Index),
								signingkeyshare.CreateTimeGTE(cutoff),
							)
						if lastID != uuid.Nil {
							query = query.Where(signingkeyshare.IDGT(lastID))
						}
						rows, err := query.
							Order(
								signingkeyshare.ByID(sql.OrderAsc()),
							).
							Limit(chunkSize).
							All(ctx)
						if err != nil {
							return err
						}
						if len(rows) == 0 {
							return nil
						}

						keyIDs := make([]uuid.UUID, 0, len(rows))
						for _, r := range rows {
							keyIDs = append(keyIDs, r.ID)
						}

						batchID := uuid.New()
						logger.Sugar().Warnf("Confirming batch of %d pending DKG keys (batch_id: %s)", len(keyIDs), batchID)
						if err := sodkg.ConfirmAndMarkAvailableKeys(ctx, config, keyIDs, batchID); err != nil {
							// Log error but continue with next batch
							logger.With(zap.Error(err)).Sugar().Warnf("Failed to confirm batch of %d keys (batch_id: %s)", len(keyIDs), batchID)
						}

						last := rows[len(rows)-1]
						lastID = last.ID
					}
				},
			},
		},
		{
			ExecutionInterval: 1 * time.Minute,
			BaseTaskSpec: BaseTaskSpec{
				Name:         "cancel_expired_transfers",
				RunInTestEnv: true,
				Task: func(ctx context.Context, config *so.Config, knobsService knobs.Knobs) error {
					logger := logging.GetLoggerFromContext(ctx)
					h := handler.NewTransferHandler(config)

					tx, err := ent.GetDbFromContext(ctx)
					if err != nil {
						return fmt.Errorf("failed to get or create current tx for request: %w", err)
					}
					query := tx.Transfer.Query().Where(
						transfer.Or(
							transfer.And(
								transfer.StatusEQ(st.TransferStatusSenderInitiated),
								transfer.ExpiryTimeLT(time.Now()),
								transfer.ExpiryTimeNEQ(time.Unix(0, 0)),
							),
							transfer.And(
								transfer.StatusEQ(st.TransferStatusSenderKeyTweakPending),
								transfer.TypeEQ(st.TransferTypePreimageSwap),
								transfer.ExpiryTimeLT(time.Now()),
								transfer.ExpiryTimeNEQ(time.Unix(0, 0)),
							),
						))

					transfers, err := query.All(ctx)
					if err != nil {
						return err
					}

					for _, dbTransfer := range transfers {
						logger.Sugar().Infof("Cancelling transfer %s", dbTransfer.ID)
						err := h.CancelTransferInternal(ctx, dbTransfer.ID.String())
						if err != nil {
							logger.With(zap.Error(err)).Sugar().Errorf("failed to cancel transfer %s", dbTransfer.ID)
						}
					}

					return nil
				},
			},
		},
		{
			ExecutionInterval: 1 * time.Hour,
			BaseTaskSpec: BaseTaskSpec{
				Name:         "delete_stale_pending_trees",
				Timeout:      &deleteStaleTreeNodesTaskTimeout,
				RunInTestEnv: false,
				// TODO(LIG-7896): This task keeps on getting stuck on
				// very large trees. Disabling for now as we investigate
				Disabled: true,
				Task: func(ctx context.Context, _ *so.Config, knobsService knobs.Knobs) error {
					logger := logging.GetLoggerFromContext(ctx)
					tx, err := ent.GetDbFromContext(ctx)
					if err != nil {
						return fmt.Errorf("failed to get or create current tx for request: %w", err)
					}

					// Find tree nodes that are:
					// 1. Older than 5 days
					// 2. Have status "CREATING"
					// 3. Belong to trees with status "PENDING"
					query := tx.TreeNode.Query().Where(
						treenode.StatusEQ(st.TreeNodeStatusCreating),
						treenode.CreateTimeLTE(time.Now().Add(-5*24*time.Hour)),
						treenode.HasTreeWith(tree.StatusEQ(st.TreeStatusPending)),
					).WithTree()

					treeNodes, err := query.All(ctx)
					if err != nil {
						logger.Error("Failed to query tree nodes", zap.Error(err))
						return err
					}

					if len(treeNodes) == 0 {
						logger.Info("Found no stale tree nodes.")
						return nil
					}

					treeToTreeNodes := make(map[uuid.UUID][]uuid.UUID)
					for _, node := range treeNodes {
						treeID := node.Edges.Tree.ID
						treeToTreeNodes[treeID] = append(treeToTreeNodes[treeID], node.ID)
					}

					for treeID, treeNodeIDs := range treeToTreeNodes {
						logger.Info(fmt.Sprintf("Deleting stale tree %s along with associated tree nodes (%d in total).", treeID, len(treeNodeIDs)))

						numDeleted, err := tx.TreeNode.Delete().Where(treenode.IDIn(treeNodeIDs...)).Exec(ctx)
						if err != nil {
							logger.With(zap.Error(err)).Sugar().Errorf("Failed to delete tree nodes for tree %s", treeID)
							return err
						}

						logger.Info(fmt.Sprintf("Deleted %d tree nodes.", numDeleted))

						// Delete the associated trees
						_, err = tx.Tree.Delete().Where(tree.IDEQ(treeID)).Exec(ctx)
						if err != nil {
							logger.With(zap.Error(err)).Sugar().Errorf("Failed to delete tree %s", treeID)
							return err
						}

						logger.Sugar().Infof("Deleted tree %s", treeID)
					}

					return nil
				},
			},
		},
		{
			ExecutionInterval: 1 * time.Minute,
			BaseTaskSpec: BaseTaskSpec{
				Name: "resume_send_transfer",
				Task: func(ctx context.Context, config *so.Config, knobsService knobs.Knobs) error {
					logger := logging.GetLoggerFromContext(ctx)
					h := handler.NewTransferHandler(config)

					tx, err := ent.GetDbFromContext(ctx)
					if err != nil {
						return fmt.Errorf("failed to get or create current tx for request: %w", err)
					}
					resumeSendTransferLimit := knobsService.GetValue(knobs.KnobResumeSendTransferLimit, 100)
					query := tx.Transfer.Query().Where(
						transfer.StatusEQ(st.TransferStatusSenderInitiatedCoordinator),
						transfer.TypeNEQ(st.TransferTypeCooperativeExit),
					).Limit(int(resumeSendTransferLimit))

					transfers, err := query.All(ctx)
					if err != nil {
						return err
					}

					for _, dbTransfer := range transfers {
						if dbTransfer.Type == st.TransferTypePreimageSwap {
							preimageRequest, err := tx.PreimageRequest.Query().Where(preimagerequest.HasTransfersWith(transfer.IDEQ(dbTransfer.ID))).Only(ctx)
							if err != nil {
								logger.Error("Failed to get preimage request for transfer", zap.Error(err))
								continue
							}
							if preimageRequest.Status != st.PreimageRequestStatusPreimageShared {
								continue
							}
						}
						err := h.ResumeSendTransfer(ctx, dbTransfer)
						if err != nil {
							logger.Error("Failed to resume send transfer", zap.Error(err))
						}
					}
					return nil
				},
			},
		},
		{
			ExecutionInterval: 5 * time.Minute,
			BaseTaskSpec: BaseTaskSpec{
				Name:         "finalize_revealed_token_transactions",
				RunInTestEnv: true,
				Task: func(ctx context.Context, config *so.Config, knobsService knobs.Knobs) error {
					logger := logging.GetLoggerFromContext(ctx)
					logger.Info("[cron] Finalizing revealed token transactions")
					dbTX, err := ent.GetDbFromContext(ctx)
					if err != nil {
						return fmt.Errorf("failed to get or create current tx for request: %w", err)
					}
					tokenTransactions, err := dbTX.TokenTransaction.Query().
						Where(
							tokentransaction.Or(
								tokentransaction.StatusEQ(st.TokenTransactionStatusRevealed),
							),
							tokentransaction.UpdateTimeLT(
								time.Now().Add(-5*time.Minute).UTC(),
							),
							tokentransaction.HasSpentOutput(),
						).
						WithPeerSignatures().
						WithSpentOutput(func(q *ent.TokenOutputQuery) {
							q.WithOutputCreatedTokenTransaction()
							q.WithTokenPartialRevocationSecretShares()
							q.WithRevocationKeyshare()
							q.ForUpdate()
						}).
						WithCreatedOutput(func(q *ent.TokenOutputQuery) {
							q.ForUpdate()
						}).
						ForUpdate().
						All(ctx)
					if err != nil {
						return err
					}
					logger.Info(fmt.Sprintf("[cron] Found %d token transactions to finalize", len(tokenTransactions)))
					internalSignTokenHandler := tokens.NewInternalSignTokenHandler(config)
					for _, tokenTransaction := range tokenTransactions {
						ctx, logger = logging.WithAttrs(ctx, tokenslogging.GetEntTokenTransactionZapAttrs(ctx, tokenTransaction)...)

						signaturesPackage := make(map[string]*tokeninternalpb.SignTokenTransactionFromCoordinationResponse)
						finalized, err := internalSignTokenHandler.RecoverFullRevocationSecretsAndFinalize(ctx, tokenTransaction)
						if err != nil {
							logger.Error("failed to recover full revocation secrets and finalize token transaction",
								zap.Error(err),
							)
							continue
						}
						if finalized {
							logger.Info("Successfully finalized token transaction")
							continue
						}
						if tokenTransaction.Edges.PeerSignatures != nil {
							for _, signature := range tokenTransaction.Edges.PeerSignatures {
								identifier := config.GetOperatorIdentifierFromIdentityPublicKey(signature.OperatorIdentityPublicKey)
								signaturesPackage[identifier] = &tokeninternalpb.SignTokenTransactionFromCoordinationResponse{
									SparkOperatorSignature: signature.Signature,
								}
							}
						}
						if tokenTransaction.OperatorSignature != nil {
							signaturesPackage[config.Identifier] = &tokeninternalpb.SignTokenTransactionFromCoordinationResponse{
								SparkOperatorSignature: tokenTransaction.OperatorSignature,
							}
						}

						tokenPb, err := tokenTransaction.MarshalProto(ctx, config)
						if err != nil {
							return fmt.Errorf("failed to marshal token transaction: %w", err)
						}

						logger.Sugar().Infof("[cron] Finalizing token transaction with operators %+q (signatures: %d)", slices.Collect(maps.Keys(signaturesPackage)), len(signaturesPackage))
						signTokenHandler := tokens.NewSignTokenHandler(config)
						commitTransactionResponse, err := signTokenHandler.ExchangeRevocationSecretsAndFinalizeIfPossible(ctx, tokenPb, signaturesPackage, tokenTransaction.FinalizedTokenTransactionHash)
						if err != nil {
							return fmt.Errorf("cron job failed to exchange revocation secrets and finalize if possible: %w", err)
						} else {
							logger.Sugar().
								Infof("Successfully exchanged revocation secrets and finalized if possible for token tx. Commit response: %v", commitTransactionResponse)
						}
					}
					return nil
				},
			},
		},
		{
			ExecutionInterval: 45 * time.Second,
			BaseTaskSpec: BaseTaskSpec{
				Name:         "send_gossip",
				RunInTestEnv: true,
				Task: func(ctx context.Context, config *so.Config, knobsService knobs.Knobs) error {
					logger := logging.GetLoggerFromContext(ctx)
					gossipHandler := handler.NewSendGossipHandler(config)
					tx, err := ent.GetDbFromContext(ctx)
					if err != nil {
						return fmt.Errorf("failed to get or create current tx for request: %w", err)
					}
					gossipLimit := knobsService.GetValue(knobs.KnobGossipLimit, 50)
					query := tx.Gossip.Query().Where(gossip.StatusEQ(st.GossipStatusPending)).Limit(int(gossipLimit))
					gossips, err := query.ForUpdate().All(ctx)
					if err != nil {
						return err
					}

					for _, gossipMsg := range gossips {
						_, err := gossipHandler.SendGossipMessage(ctx, gossipMsg)
						if err != nil {
							logger.Error("Failed to send gossip", zap.Error(err))
						}
					}
					return nil
				},
			},
		},
		{
			ExecutionInterval: 1 * time.Minute,
			BaseTaskSpec: BaseTaskSpec{
				Name:         "complete_utxo_swap",
				RunInTestEnv: true,
				Task: func(ctx context.Context, config *so.Config, knobsService knobs.Knobs) error {
					logger := logging.GetLoggerFromContext(ctx)
					tx, err := ent.GetDbFromContext(ctx)
					if err != nil {
						return fmt.Errorf("failed to get or create current tx for request: %w", err)
					}

					query := tx.UtxoSwap.Query().
						Where(utxoswap.StatusEQ(st.UtxoSwapStatusCreated)).
						Where(utxoswap.CoordinatorIdentityPublicKeyEQ(config.IdentityPublicKey())).
						Order(utxoswap.ByCreateTime(sql.OrderDesc())).
						Limit(100)

					utxoSwaps, err := query.All(ctx)
					if err != nil {
						return err
					}

					for _, utxoSwap := range utxoSwaps {
						dbTransfer, err := utxoSwap.QueryTransfer().Only(ctx)
						if err != nil && !ent.IsNotFound(err) {
							logger.Error("Failed to get transfer for a utxo swap", zap.Error(err))
							continue
						}
						if dbTransfer == nil && utxoSwap.RequestType != st.UtxoSwapRequestTypeRefund {
							logger.Sugar().Debugf("No transfer found for a non-refund utxo swap %s", utxoSwap.ID)
							continue
						}
						if utxoSwap.RequestType == st.UtxoSwapRequestTypeRefund || dbTransfer.Status == st.TransferStatusCompleted {
							logger.Sugar().Debugf("Marking utxo swap %s as completed", utxoSwap.ID)

							utxo, err := utxoSwap.QueryUtxo().Only(ctx)
							if err != nil {
								return fmt.Errorf("unable to get utxo: %w", err)
							}
							protoNetwork, err := common.ProtoNetworkFromSchemaNetwork(utxo.Network)
							if err != nil {
								return fmt.Errorf("unable to get proto network: %w", err)
							}
							protoUtxo := &pbspark.UTXO{
								Txid:    utxo.Txid,
								Vout:    utxo.Vout,
								Network: protoNetwork,
							}

							completedUtxoSwapRequest, err := handler.CreateCompleteSwapForUtxoRequest(config, protoUtxo)
							if err != nil {
								logger.Warn("Failed to get complete swap for utxo request, cron task to retry", zap.Error(err))
							} else {
								h := handler.NewInternalDepositHandler(config)
								if err := h.CompleteSwapForAllOperators(ctx, config, completedUtxoSwapRequest); err != nil {
									logger.Warn("Failed to mark a utxo swap as completed in all operators, cron task to retry", zap.Error(err))
								}
							}
						}
					}
					return nil
				},
			},
		},
		{
			ExecutionInterval: 1 * time.Minute,
			BaseTaskSpec: BaseTaskSpec{
				Name:         "monitor_pending_send_transfers",
				RunInTestEnv: true,
				Task: func(ctx context.Context, config *so.Config, knobsService knobs.Knobs) error {
					logger := logging.GetLoggerFromContext(ctx)
					tx, err := ent.GetDbFromContext(ctx)
					if err != nil {
						return fmt.Errorf("failed to get or create current tx for request: %w", err)
					}
					pendingSendTransfers, err := tx.PendingSendTransfer.Query().Where(
						pendingsendtransfer.StatusEQ(st.PendingSendTransferStatusPending),
						pendingsendtransfer.UpdateTimeLT(time.Now().Add(-3*time.Minute)),
					).Limit(100).ForUpdate().All(ctx)
					if err != nil {
						return err
					}
					for _, pendingSendTransfer := range pendingSendTransfers {
						logger.Sugar().Infof("Pending send transfer %s is still pending", pendingSendTransfer.ID)
						transfer, err := tx.Transfer.Query().Where(transfer.IDEQ(pendingSendTransfer.TransferID)).Only(ctx)
						if err != nil && !ent.IsNotFound(err) {
							logger.Sugar().Errorw("failed to get transfer", zap.Error(err))
							continue
						}
						shouldCancel := ent.IsNotFound(err) || transfer.Status == st.TransferStatusReturned
						if shouldCancel {
							logger.Sugar().Infof("Cancelling transfer %s", pendingSendTransfer.TransferID)
							transferHandler := handler.NewTransferHandler(config)
							err := transferHandler.CreateCancelTransferGossipMessage(ctx, pendingSendTransfer.TransferID.String())
							if err != nil {
								logger.Sugar().Errorw("failed to cancel transfer", zap.Error(err))
							} else {
								logger.Sugar().Infof("Successfully cancelled transfer %s", pendingSendTransfer.TransferID)
								_, err = pendingSendTransfer.Update().SetStatus(st.PendingSendTransferStatusFinished).Save(ctx)
								if err != nil {
									logger.Sugar().Errorw("failed to update pending send transfer", zap.Error(err))
								}
							}
						} else {
							logger.Sugar().Infof("Transfer %s is not ready to be cancelled", pendingSendTransfer.TransferID)
							_, err = pendingSendTransfer.Update().SetStatus(st.PendingSendTransferStatusFinished).Save(ctx)
							if err != nil {
								logger.Sugar().Errorw("failed to update pending send transfer", zap.Error(err))
							}
						}
					}
					return nil
				},
			},
		},
	}
}

func AllStartupTasks() []StartupTaskSpec {
	entityDkgTaskTimeout := 5 * time.Minute
	entityDkgRetryInterval := 10 * time.Second

	return []StartupTaskSpec{
		{
			RetryInterval: &entityDkgRetryInterval,
			BaseTaskSpec: BaseTaskSpec{
				Name:         "maybe_reserve_entity_dkg",
				RunInTestEnv: true,
				Timeout:      &entityDkgTaskTimeout,
				Task: func(ctx context.Context, config *so.Config, knobsService knobs.Knobs) error {
					logger := logging.GetLoggerFromContext(ctx)
					tx, err := ent.GetDbFromContext(ctx)
					if err != nil {
						return fmt.Errorf("failed to get or create current tx for request: %w", err)
					}
					if config.Index != 0 {
						logger.Info("Not the first operator, skipping entity DKG reservation task")
						return nil
					}

					// Try to find existing entity DKG key
					entityDkgKey, err := tx.EntityDkgKey.Query().
						WithSigningKeyshare().
						Only(ctx)

					var keyshare *ent.SigningKeyshare
					if err != nil {
						if !ent.IsNotFound(err) {
							return fmt.Errorf("failed to query for entity DKG key: %w", err)
						}
						// No existing entity DKG key found, create a new one
						entityDkgKey, err = ent.CreateEntityDkgKeyWithUnusedSigningKeyshare(ctx, config)
						if err != nil {
							return fmt.Errorf("failed to create entity DKG key with unused signing keyshare: %w", err)
						}
						tx, err = ent.GetDbFromContext(ctx)
						if err != nil {
							return fmt.Errorf("failed to get database connection: %w", err)
						}
						entityDkgKey, err = tx.EntityDkgKey.Query().WithSigningKeyshare().Only(ctx)
						if err != nil {
							return fmt.Errorf("failed to re-load entity DKG key with signing keyshare: %w", err)
						}
					}
					keyshare, err = entityDkgKey.Edges.SigningKeyshareOrErr()
					if err != nil {
						return fmt.Errorf("failed to get signing keyshare from entity DKG key: %w", err)
					}
					logger.Sugar().Infof("Found available signing keyshare %s, proceeding with reservation on other SOs", keyshare.ID)
					selection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}
					_, err = helper.ExecuteTaskWithAllOperators(ctx, config, &selection, func(ctx context.Context, operator *so.SigningOperator) (any, error) {
						conn, err := operator.NewOperatorGRPCConnection()
						if err != nil {
							return nil, err
						}
						defer conn.Close()

						client := pbinternal.NewSparkInternalServiceClient(conn)
						_, err = client.ReserveEntityDkgKey(ctx, &pbinternal.ReserveEntityDkgKeyRequest{KeyshareId: keyshare.ID.String()})
						return nil, err
					})
					if err != nil {
						return fmt.Errorf("failed to reserve entity DKG key with operators. This is likely due to not all SOs being ready yet. Will retry in %s: %w", entityDkgRetryInterval, err)
					}

					logger.Sugar().Infof("Successfully verified reserved entity DKG key %s in all operators", keyshare.ID)
					return nil
				},
			},
		},
	}
}

func (t *BaseTaskSpec) getTimeout() time.Duration {
	if t.Timeout != nil {
		return *t.Timeout
	}
	return defaultTaskTimeout
}

func (t *BaseTaskSpec) RunOnce(ctx context.Context, config *so.Config, dbClient *ent.Client, knobsService knobs.Knobs) error {
	wrappedTask := t.chainMiddleware(
		LogMiddleware(),
		DatabaseMiddleware(db.NewDefaultSessionFactory(dbClient, knobsService), config.Database.NewTxTimeout),
		TimeoutMiddleware(),
		PanicRecoveryMiddleware(),
	)

	return wrappedTask.Task(ctx, config, knobsService)
}

func (t *ScheduledTaskSpec) Schedule(scheduler gocron.Scheduler, config *so.Config, dbClient *ent.Client, knobsService knobs.Knobs) error {
	wrappedTask := t.chainMiddleware(
		LogMiddleware(),
		DatabaseMiddleware(db.NewDefaultSessionFactory(dbClient, knobsService), config.Database.NewTxTimeout),
		TimeoutMiddleware(),
		PanicRecoveryMiddleware(),
	)

	_, err := scheduler.NewJob(
		gocron.DurationJob(t.ExecutionInterval),
		gocron.NewTask(wrappedTask.Task, config, knobsService),
		gocron.WithName(t.Name),
	)
	return err
}

// Wrap the task with the given middleware. This returns a new BaseTaskSpec whose Task function
// is wrapped with the provided middleware. The original task's fields are preserved.
func (t *BaseTaskSpec) wrapMiddleware(middleware TaskMiddleware) *BaseTaskSpec {
	return &BaseTaskSpec{
		Name:         t.Name,
		Timeout:      t.Timeout,
		RunInTestEnv: t.RunInTestEnv,
		Task: func(ctx context.Context, config *so.Config, knobsService knobs.Knobs) error {
			return middleware(ctx, config, t, knobsService)
		},
	}
}

// Wrap the task with the given middlewares chained together. The middlewares have their ordering
// preserved, so the first middelware in the slice will be the outermost, and the last middleware
// will be the innermost.
//
// +------- Middleware 1 -------+
// | +----- Middleware 2 -----+ |
// | | +--- Middleware 3 ---+ | |
// | | |                    | | |
// | | |   Task (t.Task)    | | |
// | | |                    | | |
// | | +--------------------+ | |
// | +------------------------+ |
// +----------------------------+
//
// Once the task has completed, the middlewares will be unwound in reverse order, so the last
// middleware will be the first to complete.
func (t *BaseTaskSpec) chainMiddleware(
	middlewares ...TaskMiddleware,
) *BaseTaskSpec {
	// Apply the middleware to the task so that the last middleware is the inner most.
	currTask := t

	for i := len(middlewares) - 1; i >= 0; i-- {
		innerTask := currTask
		currTask = innerTask.wrapMiddleware(middlewares[i])
	}

	return currTask
}

// RunStartupTasks runs startup tasks with optional retry logic.
// Any task with a non-nil RetryInterval will be retried in the background on failure.
func RunStartupTasks(ctx context.Context, config *so.Config, db *ent.Client, runningLocally bool, knobsService knobs.Knobs) error {
	logger := logging.GetLoggerFromContext(ctx)
	logger.Info("Running startup tasks...")

	for _, task := range AllStartupTasks() {
		if !runningLocally || task.RunInTestEnv {
			if task.RetryInterval != nil {
				go func(task StartupTaskSpec) {
					retryInterval := *task.RetryInterval

					for {
						err := task.RunOnce(ctx, config, db, knobsService)
						if err == nil {
							break
						}

						if errors.Is(err, errTaskTimeout) {
							break
						}

						logger.With(zap.String("task.name", task.Name), zap.Error(err)).Sugar().Warnf("Startup task failed, retrying in %s", retryInterval)
						time.Sleep(retryInterval)
					}
				}(task)
			} else {
				task.RunOnce(ctx, config, db, knobsService) // nolint: errcheck
			}
		}
	}
	logger.Info("All startup tasks completed")
	return nil
}
