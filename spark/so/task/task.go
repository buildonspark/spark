package task

import (
	"context"
	"fmt"
	"time"

	"github.com/go-co-op/gocron/v2"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/logging"
	pbspark "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/schema"
	"github.com/lightsparkdev/spark/so/ent/transfer"
	"github.com/lightsparkdev/spark/so/ent/tree"
	"github.com/lightsparkdev/spark/so/ent/treenode"
	"github.com/lightsparkdev/spark/so/handler"
	"github.com/lightsparkdev/spark/so/lrc20"
)

// Task is a task that is scheduled to run.
type Task struct {
	// Name is the human-readable name of the task.
	Name string
	// Duration is the duration between each run of the task.
	Duration time.Duration
	// Whether to run the scheduled task in the hermetic test environment.
	RunInTestEnv bool
	// Task is the function that is run when the task is scheduled.
	Task func(context.Context, *so.Config, *ent.Client, *lrc20.Client) error
}

// AllTasks returns all the tasks that are scheduled to run.
func AllTasks() []Task {
	return []Task{
		{
			Name:         "dkg",
			Duration:     10 * time.Second,
			RunInTestEnv: false,
			Task: func(ctx context.Context, config *so.Config, db *ent.Client, _ *lrc20.Client) error {
				return ent.RunDKGIfNeeded(ctx, db, config)
			},
		},
		{
			Name:         "cancel_expired_transfers",
			Duration:     1 * time.Minute,
			RunInTestEnv: true,
			Task: func(ctx context.Context, config *so.Config, db *ent.Client, _ *lrc20.Client) error {
				return DBTransactionTask(ctx, config, db, nil, func(ctx context.Context, config *so.Config, _ *lrc20.Client) error {
					logger := logging.GetLoggerFromContext(ctx)
					h := handler.NewTransferHandler(config)

					query := db.Transfer.Query().Where(
						transfer.And(
							transfer.StatusIn(schema.TransferStatusSenderInitiated, schema.TransferStatusSenderKeyTweakPending),
							transfer.ExpiryTimeLT(time.Now()),
							transfer.ExpiryTimeNEQ(time.Unix(0, 0)),
						),
					)

					transfers, err := query.All(ctx)
					if err != nil {
						return err
					}

					for _, transfer := range transfers {
						_, err := h.CancelTransfer(ctx, &pbspark.CancelTransferRequest{
							SenderIdentityPublicKey: transfer.SenderIdentityPubkey,
							TransferId:              transfer.ID.String(),
						}, handler.CancelTransferIntentTask)
						if err != nil {
							logger.Error("failed to cancel transfer", "error", err)
						}
					}

					return nil
				})
			},
		},
		{
			Name:         "delete_stale_pending_trees",
			Duration:     1 * time.Hour,
			RunInTestEnv: false,
			Task: func(ctx context.Context, config *so.Config, db *ent.Client, lrc20Client *lrc20.Client) error {
				return DBTransactionTask(ctx, config, db, lrc20Client, func(ctx context.Context, _ *so.Config, _ *lrc20.Client) error {
					logger := logging.GetLoggerFromContext(ctx)
					tx := ent.GetDbFromContext(ctx)
					query := tx.Tree.Query().Where(
						tree.And(
							tree.StatusEQ(schema.TreeStatusPending),
							tree.CreateTimeLTE(time.Now().Add(-5*24*time.Hour)),
						),
					)

					trees, err := query.All(ctx)
					if err != nil {
						logger.Error("failed to query trees", "error", err)
						return err
					}

					if len(trees) == 0 {
						logger.Info("Found no stale trees.")
						return nil
					}

					treeIDs := make([]uuid.UUID, len(trees))
					for i, tree := range trees {
						treeIDs[i] = tree.ID
					}

					// Log the trees that will be deleted
					logger.Info(fmt.Sprintf("Deleting %d trees with pending status older than 5 days: %v", len(treeIDs), treeIDs))

					numDeleted, err := tx.TreeNode.Delete().Where(treenode.HasTreeWith(tree.IDIn(treeIDs...))).Exec(ctx)
					if err != nil {
						logger.Error("failed to delete tree nodes", "error", err)
						return err
					}
					logger.Info(fmt.Sprintf("Deleted %d tree nodes.", numDeleted))
					numDeleted, err = tx.Tree.Delete().Where(tree.IDIn(treeIDs...)).Exec(ctx)
					if err != nil {
						logger.Error("failed to delete trees", "error", err)
						return err
					}
					logger.Info(fmt.Sprintf("Deleted %d trees.", numDeleted))
					return nil
				})
			},
		},
		{
			Name:     "resume_send_transfer",
			Duration: 5 * time.Minute,
			Task: func(ctx context.Context, config *so.Config, db *ent.Client, lrc20Client *lrc20.Client) error {
				return DBTransactionTask(ctx, config, db, lrc20Client, func(ctx context.Context, _ *so.Config, _ *lrc20.Client) error {
					logger := logging.GetLoggerFromContext(ctx)
					h := handler.NewTransferHandler(config)

					query := db.Transfer.Query().Where(
						transfer.And(
							transfer.StatusEQ(schema.TransferStatusSenderInitiatedCoordinator),
						),
					).Limit(1000)

					transfers, err := query.All(ctx)
					if err != nil {
						return err
					}

					for _, transfer := range transfers {
						err := h.ResumeSendTransfer(ctx, transfer)
						if err != nil {
							logger.Error("failed to resume send transfer", "error", err)
						}
					}
					return nil
				})
			},
		},
	}
}

func (t *Task) Schedule(scheduler gocron.Scheduler, config *so.Config, db *ent.Client, lrc20Client *lrc20.Client) error {
	_, err := scheduler.NewJob(
		gocron.DurationJob(t.Duration),
		gocron.NewTask(t.createWrappedTask(), config, db, lrc20Client),
		gocron.WithName(t.Name),
	)
	if err != nil {
		return err
	}

	return nil
}

func (t *Task) createWrappedTask() func(context.Context, *so.Config, *ent.Client, *lrc20.Client) error {
	return func(ctx context.Context, config *so.Config, db *ent.Client, lrc20Client *lrc20.Client) error {
		logger := logging.GetLoggerFromContext(ctx).
			With("task.name", t.Name).
			With("task.id", uuid.New().String())

		ctx = logging.Inject(ctx, logger)

		err := t.Task(ctx, config, db, lrc20Client)
		if err != nil {
			logger.Error("Task failed!", "error", err)
		}

		return err
	}
}

func DBTransactionTask(
	ctx context.Context,
	config *so.Config,
	db *ent.Client,
	lrc20Client *lrc20.Client,
	task func(ctx context.Context, config *so.Config, lrc20Client *lrc20.Client) error,
) error {
	tx, err := db.Tx(ctx)
	if err != nil {
		return err
	}

	ctx = context.WithValue(ctx, ent.ContextKey(ent.TxKey), tx)

	err = task(ctx, config, lrc20Client)
	if err != nil {
		err = tx.Rollback()
		if err != nil {
			return err
		}
		return err
	}

	return tx.Commit()
}
