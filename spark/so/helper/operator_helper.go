package helper

import (
	"context"
	"log"
	"sync"

	"github.com/lightsparkdev/spark-go/so"
)

type TaskResult[V any] struct {
	OperatorIdentifier string
	Result             V
	Error              error
}

// ExecuteTaskWithAllOperators executes the given task with all operators.
// If includeSelf is true, the task will also be executed with the current operator.
// This will run goroutines for each operator and wait for all of them to complete before returning.
// It returns an error if any of the tasks fail.
func ExecuteTaskWithAllOperators[V any](ctx context.Context, config *so.Config, task func(ctx context.Context, operator *so.SigningOperator) (V, error), includeSelf bool) (map[string]V, error) {
	wg := sync.WaitGroup{}
	var results chan TaskResult[V]
	if includeSelf {
		results = make(chan TaskResult[V], len(config.SigningOperatorMap))
	} else {
		results = make(chan TaskResult[V], len(config.SigningOperatorMap)-1)
	}

	for _, operator := range config.SigningOperatorMap {
		if operator.Identifier == config.Identifier && !includeSelf {
			continue
		}

		wg.Add(1)
		go func(operator *so.SigningOperator) {
			defer wg.Done()
			result, err := task(ctx, operator)
			results <- TaskResult[V]{
				OperatorIdentifier: operator.Identifier,
				Result:             result,
				Error:              err,
			}
		}(operator)
	}

	wg.Wait()
	close(results)

	resultsMap := make(map[string]V)
	for result := range results {
		if result.Error != nil {
			return nil, result.Error
		}

		resultsMap[result.OperatorIdentifier] = result.Result
	}

	log.Printf("Successfully executed task with all operators")

	return resultsMap, nil
}
