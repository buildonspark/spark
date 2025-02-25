package task

import (
	"context"
	"time"

	pbspark "github.com/lightsparkdev/spark-go/proto/spark"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent"
	"github.com/lightsparkdev/spark-go/so/ent/schema"
	"github.com/lightsparkdev/spark-go/so/ent/transfer"
	"github.com/lightsparkdev/spark-go/so/handler"
)

// Task is a task that is scheduled to run.
type Task struct {
	// Duration is the duration between each run of the task.
	Duration time.Duration
	// Task is the function that is run when the task is scheduled.
	Task func(*so.Config, *ent.Client) error
}

// AllTasks returns all the tasks that are scheduled to run.
func AllTasks() []Task {
	return []Task{
		{
			Duration: 10 * time.Second,
			Task: func(config *so.Config, db *ent.Client) error {
				return ent.RunDKGIfNeeded(db, config)
			},
		},
		{
			Duration: 10 * time.Minute,
			Task: func(config *so.Config, db *ent.Client) error {
				return DBTransactionTask(context.Background(), config, db, func(ctx context.Context, config *so.Config) error {
					handler := handler.NewTransferHandler(config)

					time := time.Now()
					query := db.Transfer.Query().Where(
						transfer.And(
							transfer.StatusEQ(schema.TransferStatusSenderInitiated),
							transfer.ExpiryTimeLT(time),
						),
					)

					transfers, err := query.All(ctx)
					if err != nil {
						return err
					}

					for _, transfer := range transfers {
						_, err := handler.CancelSendTransfer(ctx, &pbspark.CancelSendTransferRequest{
							SenderIdentityPublicKey: transfer.SenderIdentityPubkey,
							TransferId:              transfer.ID.String(),
						}, true)
						if err != nil {
							return err
						}
					}

					return nil
				})
			},
		},
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
