package helper

import (
	"context"
	"log"
	"sync"

	"github.com/lightsparkdev/spark-go/so"
)

// ExecuteTaskWithAllOtherOperators executes the given task with all operators except the one specified.
// This will run goroutines for each operator and wait for all of them to complete before returning.
// It returns an error if any of the tasks fail.
func ExecuteTaskWithAllOtherOperators(ctx context.Context, config *so.Config, task func(ctx context.Context, operator *so.SigningOperator) error) error {
	wg := sync.WaitGroup{}
	results := make(chan error, len(config.SigningOperatorMap)-1)

	for _, operator := range config.SigningOperatorMap {
		if operator.Identifier == config.Identifier {
			continue
		}

		wg.Add(1)
		go func(operator *so.SigningOperator) {
			defer wg.Done()
			results <- task(ctx, operator)
		}(operator)
	}

	wg.Wait()
	close(results)

	for result := range results {
		if result != nil {
			return result
		}
	}

	log.Printf("Successfully executed task with all operators")

	return nil
}
