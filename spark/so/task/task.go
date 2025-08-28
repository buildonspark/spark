package task

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"time"

	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common/keys"

	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/handler/signing_handler"
	"github.com/lightsparkdev/spark/so/handler/tokens"
	"github.com/lightsparkdev/spark/so/helper"
	"github.com/lightsparkdev/spark/so/objects"
	"google.golang.org/protobuf/types/known/timestamppb"

	"entgo.io/ent/dialect/sql"
	"github.com/go-co-op/gocron/v2"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/logging"
	pbspark "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	tokeninternalpb "github.com/lightsparkdev/spark/proto/spark_token_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/gossip"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/signingcommitment"
	"github.com/lightsparkdev/spark/so/ent/tokenoutput"
	"github.com/lightsparkdev/spark/so/ent/tokentransaction"
	"github.com/lightsparkdev/spark/so/ent/transfer"
	"github.com/lightsparkdev/spark/so/ent/tree"
	"github.com/lightsparkdev/spark/so/ent/treenode"
	"github.com/lightsparkdev/spark/so/ent/utxoswap"
	"github.com/lightsparkdev/spark/so/handler"
)

var (
	defaultTaskTimeout              = 1 * time.Minute
	dkgTaskTimeout                  = 3 * time.Minute
	deleteStaleTreeNodesTaskTimeout = 10 * time.Minute
)

// BaseTask contains common fields for all task types.

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
	Task func(context.Context, *so.Config) error
}

// ScheduledTask is a task that runs on a schedule.
type ScheduledTaskSpec struct {
	BaseTaskSpec
	// ExecutionInterval is the interval between each run of the task.
	ExecutionInterval time.Duration
}

// StartupTask is a task that runs once at startup.
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
				Task: func(ctx context.Context, config *so.Config) error {
					return ent.RunDKGIfNeeded(ctx, config)
				},
			},
		},
		{
			ExecutionInterval: 1 * time.Minute,
			BaseTaskSpec: BaseTaskSpec{
				Name:         "generate_signing_commitments",
				RunInTestEnv: false,
				Task: func(ctx context.Context, config *so.Config) error {
					db, err := ent.GetDbFromContext(ctx)
					if err != nil {
						return fmt.Errorf("failed to get or create current tx for request: %w", err)
					}

					logger := logging.GetLoggerFromContext(ctx)
					entCommitments := make([]*ent.SigningCommitmentCreate, 0)
					for _, operator := range config.SigningOperatorMap {
						count, err := db.SigningCommitment.Query().Where(
							signingcommitment.OperatorIndexEQ(uint(operator.ID)),
							signingcommitment.StatusEQ(st.SigningCommitmentStatusAvailable),
						).Count(ctx)
						if err != nil {
							logger.Error("failed to query signing commitments for operator", "operator", operator.ID, "error", err)
							continue
						}

						if count < spark.SigningCommitmentReserve {
							var resp *pbinternal.FrostRound1Response
							if operator.ID == config.Index {
								signingHandler := signing_handler.NewFrostSigningHandler(config)
								resp, err = signingHandler.GenerateRandomNonces(ctx, spark.SigningCommitmentBatchSize)
								if err != nil {
									return err
								}
							}

							conn, err := operator.NewOperatorGRPCConnection()
							if err != nil {
								return err
							}

							client := pbinternal.NewSparkInternalServiceClient(conn)
							resp, err = client.FrostRound1(ctx, &pbinternal.FrostRound1Request{
								RandomNonceCount: spark.SigningCommitmentBatchSize,
							})
							if err != nil {
								logger.Error("failed to generate signing commitments for operator", "operator", operator.ID, "error", err)
								continue
							}

							for _, pbCommitment := range resp.SigningCommitments {
								commitments := objects.SigningCommitment{}
								err := commitments.UnmarshalProto(pbCommitment)
								if err != nil {
									return err
								}

								commitmentBinary := commitments.MarshalBinary()

								entCommitments = append(
									entCommitments,
									db.SigningCommitment.Create().
										SetOperatorIndex(uint(operator.ID)).
										SetStatus(st.SigningCommitmentStatusAvailable).
										SetNonceCommitment(commitmentBinary),
								)
							}
						}
					}

					if err := db.SigningCommitment.CreateBulk(entCommitments...).Exec(ctx); err != nil {
						return err
					}

					return nil
				},
			},
		},
		{
			ExecutionInterval: 1 * time.Minute,
			BaseTaskSpec: BaseTaskSpec{
				Name:         "cancel_expired_transfers",
				RunInTestEnv: true,
				Task: func(ctx context.Context, config *so.Config) error {
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
								transfer.TypeEQ(st.TransferTypeCooperativeExit),
								transfer.ExpiryTimeLT(time.Now()),
								transfer.ExpiryTimeNEQ(time.Unix(0, 0)),
							),
							transfer.And(
								transfer.StatusEQ(st.TransferStatusSenderKeyTweakPending),
								transfer.TypeEQ(st.TransferTypePreimageSwap),
								transfer.ExpiryTimeLT(time.Now().Add(-24*time.Hour*14)),
								transfer.ExpiryTimeNEQ(time.Unix(0, 0)),
							),
						))

					transfers, err := query.All(ctx)
					if err != nil {
						return err
					}

					for _, dbTransfer := range transfers {
						logger.Info("Cancelling transfer", "transfer_id", dbTransfer.ID)
						err := h.CancelTransferInternal(ctx, dbTransfer.ID.String())
						if err != nil {
							logger.Error("failed to cancel transfer", "error", err)
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
				Task: func(ctx context.Context, _ *so.Config) error {
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
						treenode.And(
							treenode.StatusEQ(st.TreeNodeStatusCreating),
							treenode.CreateTimeLTE(time.Now().Add(-5*24*time.Hour)),
							treenode.HasTreeWith(tree.StatusEQ(st.TreeStatusPending)),
						),
					).WithTree()

					treeNodes, err := query.All(ctx)
					if err != nil {
						logger.Error("failed to query tree nodes", "error", err)
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
							logger.Error("failed to delete tree nodes", "tree_id", treeID, "error", err)
							return err
						}

						logger.Info(fmt.Sprintf("Deleted %d tree nodes.", numDeleted))

						// Delete the associated trees
						_, err = tx.Tree.Delete().Where(tree.IDEQ(treeID)).Exec(ctx)
						if err != nil {
							logger.Error("failed to delete tree", "tree_id", treeID, "error", err)
							return err
						}

						logger.Info(fmt.Sprintf("Deleted tree %s.", treeID))
					}

					return nil
				},
			},
		},
		{
			ExecutionInterval: 5 * time.Minute,
			BaseTaskSpec: BaseTaskSpec{
				Name: "resume_send_transfer",
				Task: func(ctx context.Context, config *so.Config) error {
					logger := logging.GetLoggerFromContext(ctx)
					h := handler.NewTransferHandler(config)

					tx, err := ent.GetDbFromContext(ctx)
					if err != nil {
						return fmt.Errorf("failed to get or create current tx for request: %w", err)
					}
					query := tx.Transfer.Query().Where(
						transfer.And(
							transfer.StatusEQ(st.TransferStatusSenderInitiatedCoordinator),
							transfer.TypeNotIn(st.TransferTypeCooperativeExit, st.TransferTypePreimageSwap),
						),
					).Limit(1000)

					transfers, err := query.All(ctx)
					if err != nil {
						return err
					}

					for _, dbTransfer := range transfers {
						err := h.ResumeSendTransfer(ctx, dbTransfer)
						if err != nil {
							logger.Error("failed to resume send transfer", "error", err)
						}
					}
					return nil
				},
			},
		},
		{
			ExecutionInterval: 10 * time.Minute,
			BaseTaskSpec: BaseTaskSpec{
				Name:         "finalize_revealed_token_transactions",
				RunInTestEnv: true,
				Task: func(ctx context.Context, config *so.Config) error {
					logger := logging.GetLoggerFromContext(ctx)
					logger.Info("[cron] Finalizing revealed token transactions")
					db, err := ent.GetDbFromContext(ctx)
					if err != nil {
						return fmt.Errorf("failed to get or create current tx for request: %w", err)
					}
					stuckTx1, err := hex.DecodeString("637c8251c4f6748c3fc70880f7e6e6fa607c563fae8e21866946f49400d0d5d5")
					if err != nil {
						return fmt.Errorf("invalid hex for finalized token transaction hash A: %w", err)
					}
					stuckTx2, err := hex.DecodeString("7941b215536c63e4a0f8c1ad0ea5770d614e959b2f7adb647bfc8c4e78ad1dc4")
					if err != nil {
						return fmt.Errorf("invalid hex for finalized token transaction hash B: %w", err)
					}

					tokenTransactions, err := db.TokenTransaction.Query().
						Where(
							tokentransaction.Or(
								tokentransaction.StatusEQ(st.TokenTransactionStatusRevealed),
								// Temporary condition to resolve stuck transactions created due to theshold bug.
								// TODO(CNT-444): Remove this condition once the threshold bug is resolved.
								tokentransaction.And(
									tokentransaction.FinalizedTokenTransactionHashIn(stuckTx1, stuckTx2),
									tokentransaction.StatusNotIn(st.TokenTransactionStatusFinalized),
								),
							),
						).
						WithPeerSignatures().
						WithSpentOutput(func(q *ent.TokenOutputQuery) {
							q.WithOutputCreatedTokenTransaction()
						}).
						WithCreatedOutput().
						All(ctx)
					if err != nil {
						return err
					}
					logger.Info(fmt.Sprintf("[cron] Found %d token transactions to finalize", len(tokenTransactions)))
					for _, tokenTransaction := range tokenTransactions {
						var spentOutputs []*tokenpb.TokenOutputToSpend
						var createdOutputs []*tokenpb.TokenOutput
						signaturesPackage := make(map[string]*tokeninternalpb.SignTokenTransactionFromCoordinationResponse)

						if tokenTransaction.Edges.SpentOutput != nil {
							for _, spentOutput := range tokenTransaction.Edges.SpentOutput {
								spentOutputs = append(spentOutputs, &tokenpb.TokenOutputToSpend{
									PrevTokenTransactionHash: spentOutput.Edges.OutputCreatedTokenTransaction.FinalizedTokenTransactionHash,
									PrevTokenTransactionVout: uint32(spentOutput.CreatedTransactionOutputVout),
								})
							}
						}
						if tokenTransaction.Edges.CreatedOutput != nil {
							for _, createdOutput := range tokenTransaction.Edges.CreatedOutput {
								idStr := createdOutput.ID.String()
								createdOutputs = append(createdOutputs, &tokenpb.TokenOutput{
									Id:                            &idStr,
									OwnerPublicKey:                createdOutput.OwnerPublicKey,
									RevocationCommitment:          createdOutput.WithdrawRevocationCommitment,
									WithdrawBondSats:              &createdOutput.WithdrawBondSats,
									WithdrawRelativeBlockLocktime: &createdOutput.WithdrawRelativeBlockLocktime,
									TokenPublicKey:                createdOutput.TokenPublicKey,
									TokenIdentifier:               createdOutput.TokenIdentifier,
									TokenAmount:                   createdOutput.TokenAmount,
								})
							}
						}

						var protoNetwork pbspark.Network
						if len(tokenTransaction.Edges.CreatedOutput) > 0 {
							protoNetwork, err = common.ProtoNetworkFromSchemaNetwork(tokenTransaction.Edges.CreatedOutput[0].Network)
							if err != nil {
								return fmt.Errorf("unable to get proto network: %w", err)
							}
						} else {
							return fmt.Errorf("no created outputs found for token transaction: %s", tokenTransaction.ID)
						}

						if tokenTransaction.Edges.PeerSignatures != nil {
							for _, signature := range tokenTransaction.Edges.PeerSignatures {
								pubKey, err := keys.ParsePublicKey(signature.OperatorIdentityPublicKey)
								if err != nil {
									return fmt.Errorf("unable to parse public key: %w", err)
								}
								identifier := config.GetOperatorIdentifierFromIdentityPublicKey(pubKey)
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

						tokenPb := &tokenpb.TokenTransaction{
							Version: uint32(tokenTransaction.Version),
							TokenInputs: &tokenpb.TokenTransaction_TransferInput{
								TransferInput: &tokenpb.TokenTransferInput{
									OutputsToSpend: spentOutputs,
								},
							},
							TokenOutputs: createdOutputs,
							ExpiryTime:   timestamppb.New(tokenTransaction.ExpiryTime),
							Network:      protoNetwork,
						}
						logger.Info("[cron] Finalizing token transaction",
							"num_signatures", len(signaturesPackage),
							"operator_ids", slices.Collect(maps.Keys(signaturesPackage)),
							"transaction_hash", hex.EncodeToString(tokenTransaction.FinalizedTokenTransactionHash))
						signTokenHandler := tokens.NewSignTokenHandler(config)
						commitTransactionResponse, err := signTokenHandler.ExchangeRevocationSecretsAndFinalizeIfPossible(ctx, tokenPb, signaturesPackage, tokenTransaction.FinalizedTokenTransactionHash)
						if err != nil {
							return fmt.Errorf("cron job failed to exchange revocation secrets and finalize if possible for token txHash: %x: %w", tokenTransaction.FinalizedTokenTransactionHash, err)
						} else {
							logger.Info(fmt.Sprintf("Successfully exchanged revocation secrets and finalized if possible for token txHash: %x. Commit response: %v", tokenTransaction.FinalizedTokenTransactionHash, commitTransactionResponse))
						}
					}
					return nil
				},
			},
		},
		{
			ExecutionInterval: 5 * time.Minute,
			BaseTaskSpec: BaseTaskSpec{
				Name:         "send_gossip",
				RunInTestEnv: true,
				Task: func(ctx context.Context, config *so.Config) error {
					logger := logging.GetLoggerFromContext(ctx)
					gossipHandler := handler.NewSendGossipHandler(config)
					tx, err := ent.GetDbFromContext(ctx)
					if err != nil {
						return fmt.Errorf("failed to get or create current tx for request: %w", err)
					}
					query := tx.Gossip.Query().Where(gossip.StatusEQ(st.GossipStatusPending)).Limit(1000)
					gossips, err := query.ForUpdate().All(ctx)
					if err != nil {
						return err
					}

					for _, gossipMsg := range gossips {
						_, err := gossipHandler.SendGossipMessage(ctx, gossipMsg)
						if err != nil {
							logger.Error("failed to send gossip", "error", err)
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
				Task: func(ctx context.Context, config *so.Config) error {
					logger := logging.GetLoggerFromContext(ctx)
					tx, err := ent.GetDbFromContext(ctx)
					if err != nil {
						return fmt.Errorf("failed to get or create current tx for request: %w", err)
					}

					query := tx.UtxoSwap.Query().
						Where(utxoswap.StatusEQ(st.UtxoSwapStatusCreated)).
						Where(utxoswap.CoordinatorIdentityPublicKeyEQ(config.IdentityPublicKey().Serialize())).
						Order(utxoswap.ByCreateTime(sql.OrderDesc())).
						Limit(100)

					utxoSwaps, err := query.All(ctx)
					if err != nil {
						return err
					}

					for _, utxoSwap := range utxoSwaps {
						dbTransfer, err := utxoSwap.QueryTransfer().Only(ctx)
						if err != nil && !ent.IsNotFound(err) {
							logger.Error("failed to get transfer for a utxo swap", "error", err)
							continue
						}
						if dbTransfer == nil && utxoSwap.RequestType != st.UtxoSwapRequestTypeRefund {
							logger.Debug("No transfer found for a non-refund utxo swap", "utxo_swap_id", utxoSwap.ID)
							continue
						}
						if utxoSwap.RequestType == st.UtxoSwapRequestTypeRefund || dbTransfer.Status == st.TransferStatusCompleted {
							logger.Debug("Marking utxo swap as completed", "utxo_swap_id", utxoSwap.ID)

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
								logger.Warn("Failed to get complete swap for utxo request, cron task to retry", "error", err)
							} else {
								h := handler.NewInternalDepositHandler(config)
								if err := h.CompleteSwapForAllOperators(ctx, config, completedUtxoSwapRequest); err != nil {
									logger.Warn("Failed to mark a utxo swap as completed in all operators, cron task to retry", "error", err)
								}
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
	backfillTokenOutputInterval := 10 * time.Minute

	return []StartupTaskSpec{
		{
			RetryInterval: &entityDkgRetryInterval,
			BaseTaskSpec: BaseTaskSpec{
				Name:         "maybe_reserve_entity_dkg",
				RunInTestEnv: true,
				Timeout:      &entityDkgTaskTimeout,
				Task: func(ctx context.Context, config *so.Config) error {
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
						keyshares, err := ent.GetUnusedSigningKeysharesTx(ctx, tx, config, 1)
						if err != nil {
							return fmt.Errorf("failed to get unused signing keyshares: %w", err)
						}
						if len(keyshares) == 0 {
							return fmt.Errorf("no signing keyshares available yet")
						}

						keyshare = keyshares[0]
						_, err = tx.EntityDkgKey.Create().
							SetSigningKeyshareID(keyshare.ID).
							Save(ctx)
						if err != nil {
							return fmt.Errorf("failed to create entity DKG key: %w", err)
						}

						// Commit the existing transaction before making the operator calls to ensure it is not rolled back if they fail
						// due to the SOs still starting up.
						if err = tx.Commit(); err != nil {
							return fmt.Errorf("failed to commit entity DKG key: %w", err)
						}
					} else {
						// Existing entity DKG key found, get its signing keyshare
						keyshare, err = entityDkgKey.Edges.SigningKeyshareOrErr()
						if err != nil {
							return fmt.Errorf("failed to get signing keyshare from entity DKG key: %w", err)
						}
					}
					logger.Info("Found available signing keyshare, proceeding with reservation on other SOs", "keyshare_id", keyshare.ID)
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

					logger.Info("Successfully verified reserved entity DKG key in all operators", "keyshare_id", keyshare.ID)
					return nil
				},
			},
		},
		{
			RetryInterval: &backfillTokenOutputInterval,
			BaseTaskSpec: BaseTaskSpec{
				Name:         "backfill_spent_token_transaction_history",
				RunInTestEnv: true,
				Task: func(ctx context.Context, config *so.Config) error {
					logger := logging.GetLoggerFromContext(ctx)

					if !config.Token.EnableBackfillSpentTokenTransactionHistoryTask {
						logger.Info("Backfill spent token transaction history is disabled, skipping")
						return nil
					}

					tx, err := ent.GetDbFromContext(ctx)
					if err != nil {
						return fmt.Errorf("failed to get or create current tx for request: %w", err)
					}

					const batchSize = 1000
					var processed int
					start := time.Now()

					logger.Info("Starting backfill of spent token transaction history")

					for {
						// Get next batch of outputs that have spent relationships but haven't been backfilled to M2M
						outputs, err := tx.TokenOutput.Query().
							Where(
								tokenoutput.HasOutputSpentTokenTransaction(),                          // Has current spent relationship
								tokenoutput.Not(tokenoutput.HasOutputSpentStartedTokenTransactions()), // Not yet backfilled to M2M
							).
							WithOutputSpentTokenTransaction().
							Order(ent.Asc(tokenoutput.FieldID)).
							Limit(batchSize).
							All(ctx)

						if err != nil {
							return fmt.Errorf("failed to fetch outputs for backfill: %w", err)
						}

						if len(outputs) == 0 {
							break // No more outputs to process
						}

						// Process batch
						for _, output := range outputs {
							if output.Edges.OutputSpentTokenTransaction != nil {
								_, err := tx.TokenOutput.UpdateOne(output).
									AddOutputSpentStartedTokenTransactions(output.Edges.OutputSpentTokenTransaction).
									Save(ctx)
								if err != nil {
									return fmt.Errorf("failed to backfill output %s: %w", output.ID, err)
								}
							}
						}

						processed += len(outputs)

						// Progress logging every 10k records
						if processed%10000 == 0 {
							elapsed := time.Since(start)
							rate := float64(processed) / elapsed.Seconds()
							logger.Info("Backfill progress",
								"processed", processed,
								"rate_per_sec", int(rate),
								"elapsed", elapsed.Round(time.Second),
							)
						}

						// Small pause to be nice to the database
						time.Sleep(50 * time.Millisecond)
					}

					elapsed := time.Since(start)
					logger.Info("Backfill completed",
						"total_processed", processed,
						"duration", elapsed.Round(time.Second),
						"rate_per_sec", int(float64(processed)/elapsed.Seconds()),
					)

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

func (t *BaseTaskSpec) RunOnce(config *so.Config, dbClient *ent.Client) error {
	ctx := context.Background()

	wrappedTask := t.chainMiddleware(
		LogMiddleware(),
		DatabaseMiddleware(db.NewDefaultSessionFactory(dbClient), config.Database.NewTxTimeout),
		TimeoutMiddleware(),
		PanicRecoveryMiddleware(),
	)

	return wrappedTask.Task(ctx, config)
}

func (t *ScheduledTaskSpec) Schedule(scheduler gocron.Scheduler, config *so.Config, dbClient *ent.Client) error {
	wrappedTask := t.chainMiddleware(
		LogMiddleware(),
		DatabaseMiddleware(db.NewDefaultSessionFactory(dbClient), config.Database.NewTxTimeout),
		TimeoutMiddleware(),
		PanicRecoveryMiddleware(),
	)

	_, err := scheduler.NewJob(
		gocron.DurationJob(t.ExecutionInterval),
		gocron.NewTask(wrappedTask.Task, config),
		gocron.WithName(t.Name),
	)
	if err != nil {
		return err
	}

	return nil
}

// Wrap the task with the given middleware. This returns a new BaseTaskSpec whose Task function
// is wrapped with the provided middleware. The original task's fields are preserved.
func (t *BaseTaskSpec) wrapMiddleware(middleware TaskMiddleware) *BaseTaskSpec {
	return &BaseTaskSpec{
		Name:         t.Name,
		Timeout:      t.Timeout,
		RunInTestEnv: t.RunInTestEnv,
		Task: func(ctx context.Context, config *so.Config) error {
			return middleware(ctx, config, t)
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
		innerTask, i := currTask, i
		currTask = innerTask.wrapMiddleware(middlewares[i])
	}

	return currTask
}

// RunStartupTasks runs startup tasks with optional retry logic.
// Any task with a non-nil RetryInterval will be retried in the background on failure.
func RunStartupTasks(config *so.Config, db *ent.Client, runningLocally bool) error {
	slog.Info("Running startup tasks...")

	for _, task := range AllStartupTasks() {
		if !runningLocally || task.RunInTestEnv {
			if task.RetryInterval != nil {
				go func(task StartupTaskSpec) {
					retryInterval := *task.RetryInterval

					for {
						err := task.RunOnce(config, db)
						if err == nil {
							break
						}

						if errors.Is(err, errTaskTimeout) {
							break
						}

						slog.Warn(fmt.Sprintf("Startup task failed, retrying in %s", retryInterval), "task.name", task.Name, "error", err)
						time.Sleep(retryInterval)
					}
				}(task)
			} else {
				task.RunOnce(config, db) // nolint: errcheck
			}
		}
	}
	slog.Info("All startup tasks completed")
	return nil
}
