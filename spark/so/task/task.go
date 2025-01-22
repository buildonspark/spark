package task

import (
	"time"

	"github.com/lightsparkdev/spark-go/so"
)

// Task is a task that is scheduled to run.
type Task struct {
	// Duration is the duration between each run of the task.
	Duration time.Duration
	// Task is the function that is run when the task is scheduled.
	Task func(*so.Config) error
}

// AllTasks returns all the tasks that are scheduled to run.
func AllTasks() []Task {
	return []Task{}
}
