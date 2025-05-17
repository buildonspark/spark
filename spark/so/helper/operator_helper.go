package helper

import (
	"context"
	"crypto/rand"
	"errors"
	"math/big"
	"sync"

	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so"
)

// OperatorSelectionOption is the option for selecting operators.
type OperatorSelectionOption int

const (
	// OperatorSelectionOptionAll selects all operators.
	OperatorSelectionOptionAll OperatorSelectionOption = iota
	// OperatorSelectionOptionExcludeSelf selects all operators except the current operator.
	OperatorSelectionOptionExcludeSelf
	// OperatorSelectionOptionThreshold selects a random subset of operators with the given threshold.
	OperatorSelectionOptionThreshold
	// OperatorSelectionOptionPreSelected selects a pre-selected list of operators.
	OperatorSelectionOptionPreSelected
)

// OperatorSelection is the selection of operators.
// It will return a list of operators based on the option and threshold.
// The list it returns will be the same for the same OperatorSelection object.
type OperatorSelection struct {
	// Option is the option for selecting operators.
	Option OperatorSelectionOption
	// Threshold is the threshold for selecting operators.
	Threshold int

	operatorList *[]*so.SigningOperator
}

func NewPreSelectedOperatorSelection(config *so.Config, operatorIDs []string) *OperatorSelection {
	operators := make([]*so.SigningOperator, 0, len(operatorIDs))
	for _, id := range operatorIDs {
		operators = append(operators, config.SigningOperatorMap[id])
	}
	return &OperatorSelection{
		Option:       OperatorSelectionOptionPreSelected,
		operatorList: &operators,
	}
}

// OperatorCount returns the number of operators based on the option.
func (o OperatorSelection) OperatorCount(config *so.Config) int {
	switch o.Option {
	case OperatorSelectionOptionAll:
		return len(config.SigningOperatorMap)
	case OperatorSelectionOptionExcludeSelf:
		return len(config.SigningOperatorMap) - 1
	case OperatorSelectionOptionThreshold:
		return o.Threshold
	case OperatorSelectionOptionPreSelected:
		return len(*o.operatorList)
	}

	return 0
}

// OperatorList returns the list of operators based on the option.
// Lazily creates the list of operators and stores it in the OperatorSelection object.
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
		for key := range config.SigningOperatorMap {
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
	case OperatorSelectionOptionPreSelected:
	}

	if o.operatorList == nil {
		return nil, errors.New("invalid operator selection option")
	}

	return *o.operatorList, nil
}

// TaskResult is the result of a task.
type TaskResult[V any] struct {
	// OperatorIdentifier is the identifier of the operator that executed the task.
	OperatorIdentifier string
	// Result is the result of the task.
	Result V
	// Error is the error that occurred during the task.
	Error error
}

// ExecuteTaskWithAllOperators executes the given task with a list of operators.
// This will run goroutines for each operator and wait for all of them to complete before returning.
// It returns an error if any of the tasks fail.
func ExecuteTaskWithAllOperators[V any](ctx context.Context, config *so.Config, selection *OperatorSelection, task func(ctx context.Context, operator *so.SigningOperator) (V, error)) (map[string]V, error) {
	logger := logging.GetLoggerFromContext(ctx)

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

	logger.Info("Successfully executed task with all operators")

	return resultsMap, nil
}
