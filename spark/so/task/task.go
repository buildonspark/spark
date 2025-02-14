package task

import (
	"context"
	"log"
	"time"

	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent"
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
				log.Printf("Running DKG if needed")
				tx, err := db.Tx(context.Background())
				if err != nil {
					log.Printf("Failed to create transaction: %v", err)
					return err
				}
				return ent.RunDKGIfNeeded(tx, config)
			},
		},
	}
}
