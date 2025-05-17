package task

import (
	"context"
	"time"

	"github.com/go-co-op/gocron/v2"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/logging"
	pbspark "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/schema"
	"github.com/lightsparkdev/spark/so/ent/transfer"
	"github.com/lightsparkdev/spark/so/handler"
)

// Task is a task that is scheduled to run.
type Task struct {
	// Name is the human-readable name of the task.
	Name string
	// Duration is the duration between each run of the task.
	Duration time.Duration
	// Task is the function that is run when the task is scheduled.
	Task func(context.Context, *so.Config, *ent.Client) error
}

// AllTasks returns all the tasks that are scheduled to run.
func AllTasks() []Task {
	return []Task{
		{
			Name:     "dkg",
			Duration: 10 * time.Second,
			Task: func(ctx context.Context, config *so.Config, db *ent.Client) error {
				return ent.RunDKGIfNeeded(ctx, db, config)
			},
		},
		{
			Name:     "cancel_expired_transfers",
			Duration: 1 * time.Minute,
			Task: func(ctx context.Context, config *so.Config, db *ent.Client) error {
				return DBTransactionTask(ctx, config, db, func(ctx context.Context, config *so.Config) error {
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
	}
}

func (t *Task) Schedule(scheduler gocron.Scheduler, config *so.Config, db *ent.Client) error {
	_, err := scheduler.NewJob(
		gocron.DurationJob(t.Duration),
		gocron.NewTask(t.createWrappedTask(), config, db),
		gocron.WithName(t.Name),
	)
	if err != nil {
		return err
	}

	return nil
}

func (t *Task) createWrappedTask() func(context.Context, *so.Config, *ent.Client) error {
	return func(ctx context.Context, config *so.Config, db *ent.Client) error {
		logger := logging.GetLoggerFromContext(ctx).
			With("task.name", t.Name).
			With("task.id", uuid.New().String())

		ctx = logging.Inject(ctx, logger)

		err := t.Task(ctx, config, db)
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
	task func(ctx context.Context, config *so.Config) error,
) error {
	tx, err := db.Tx(ctx)
	if err != nil {
		return err
	}

	ctx = context.WithValue(ctx, ent.ContextKey(ent.TxKey), tx)

	err = task(ctx, config)
	if err != nil {
		err = tx.Rollback()
		if err != nil {
			return err
		}
		return err
	}

	return tx.Commit()
}
