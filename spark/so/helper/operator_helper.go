package helper

import (
	"context"
	"errors"
	"log"
	"math/big"
	"sync"

	"crypto/rand"

	"github.com/lightsparkdev/spark-go/so"
)

type OperatorSelectionOption int

const (
	OperatorSelectionOptionAll OperatorSelectionOption = iota
	OperatorSelectionOptionExcludeSelf
	OperatorSelectionOptionThreshold
)

type OperatorSelection struct {
	Option    OperatorSelectionOption
	Threshold int

	operatorList *[]*so.SigningOperator
}

func (o OperatorSelection) OperatorCount(config *so.Config) int {
	switch o.Option {
	case OperatorSelectionOptionAll:
		return len(config.SigningOperatorMap)
	case OperatorSelectionOptionExcludeSelf:
		return len(config.SigningOperatorMap) - 1
	case OperatorSelectionOptionThreshold:
		return o.Threshold
	}

	return 0
}

func (o *OperatorSelection) OperatorList(config *so.Config) ([]*so.SigningOperator, error) {
	if o.operatorList != nil {
		return *o.operatorList, nil
	}

	switch o.Option {
	case OperatorSelectionOptionAll:
		operators := make([]*so.SigningOperator, 0, len(config.SigningOperatorMap))
		for _, operator := range config.SigningOperatorMap {
			operators = append(operators, operator)
		}
		o.operatorList = &operators
	case OperatorSelectionOptionExcludeSelf:
		operators := make([]*so.SigningOperator, 0, len(config.SigningOperatorMap)-1)
		for _, operator := range config.SigningOperatorMap {
			if operator.Identifier != config.Identifier {
				operators = append(operators, operator)
			}
		}
		o.operatorList = &operators
	case OperatorSelectionOptionThreshold:
		operators := make([]*so.SigningOperator, 0, o.Threshold)
		// Create a random array of indices
		indices := make([]string, 0)
		for key, _ := range config.SigningOperatorMap {
			indices = append(indices, key)
		}
		// Fisher-Yates shuffle
		for i := len(indices) - 1; i > 0; i-- {
			j, err := rand.Int(rand.Reader, big.NewInt(int64(i)))
			if err != nil {
				return nil, err
			}
			indices[i], indices[j.Int64()] = indices[j.Int64()], indices[i]
		}
		// Take first Threshold elements
		indices = indices[:o.Threshold]
		for _, index := range indices {
			operators = append(operators, config.SigningOperatorMap[index])
		}
		o.operatorList = &operators
	}

	if o.operatorList == nil {
		return nil, errors.New("invalid operator selection option")
	}

	return *o.operatorList, nil
}

type TaskResult[V any] struct {
	OperatorIdentifier string
	Result             V
	Error              error
}

// ExecuteTaskWithAllOperators executes the given task with all operators.
// If includeSelf is true, the task will also be executed with the current operator.
// This will run goroutines for each operator and wait for all of them to complete before returning.
// It returns an error if any of the tasks fail.
func ExecuteTaskWithAllOperators[V any](ctx context.Context, config *so.Config, selection *OperatorSelection, task func(ctx context.Context, operator *so.SigningOperator) (V, error)) (map[string]V, error) {
	wg := sync.WaitGroup{}
	results := make(chan TaskResult[V], selection.OperatorCount(config))

	operators, err := selection.OperatorList(config)
	if err != nil {
		return nil, err
	}

	for _, operator := range operators {
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
