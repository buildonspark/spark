package helper

import (
	"context"
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
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

	operatorList []*so.SigningOperator
}

func NewPreSelectedOperatorSelection(config *so.Config, operatorIDs []string) (*OperatorSelection, error) {
	operators := make([]*so.SigningOperator, len(operatorIDs))
	for i, id := range operatorIDs {
		if operator, ok := config.SigningOperatorMap[id]; ok {
			operators[i] = operator
		} else {
			return nil, fmt.Errorf("operator ID %s not found in signing operator map", id)
		}
	}
	return &OperatorSelection{
		Option:       OperatorSelectionOptionPreSelected,
		operatorList: operators,
	}, nil
}

// OperatorList returns the list of operators based on the option.
// Lazily creates the list of operators and stores it in the OperatorSelection object.
func (o *OperatorSelection) OperatorList(config *so.Config) ([]*so.SigningOperator, error) {
	if o.operatorList != nil {
		return o.operatorList, nil
	}
	if config == nil || len(config.SigningOperatorMap) == 0 {
		return nil, fmt.Errorf("no signing operators configured")
	}

	switch o.Option {
	case OperatorSelectionOptionAll:
		o.operatorList = slices.Collect(maps.Values(config.SigningOperatorMap))
	case OperatorSelectionOptionExcludeSelf:
		operators := make([]*so.SigningOperator, 0, len(config.SigningOperatorMap)-1)
		for _, operator := range config.SigningOperatorMap {
			if operator.Identifier != config.Identifier {
				operators = append(operators, operator)
			}
		}
		o.operatorList = operators
	case OperatorSelectionOptionThreshold:
		if o.Threshold > len(config.SigningOperatorMap) {
			return nil, fmt.Errorf("threshold %d exceeds length of signing operator list %d", o.Threshold, len(config.SigningOperatorMap))
		}
		operators := slices.Collect(maps.Values(config.SigningOperatorMap))
		rand.Shuffle(len(operators), func(i, j int) { operators[i], operators[j] = operators[j], operators[i] })
		o.operatorList = operators[:o.Threshold]
	case OperatorSelectionOptionPreSelected:
	}

	return o.operatorList, nil
}

func (o *OperatorSelection) OperatorIdentifierList(config *so.Config) ([]string, error) {
	operators, err := o.OperatorList(config)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(operators))
	for i, operator := range operators {
		ids[i] = operator.Identifier
	}
	return ids, nil
}

// taskResult is the result of a task.
type taskResult[V any] struct {
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

	operators, err := selection.OperatorList(config)
	if err != nil {
		return nil, err
	}

	results := make(chan taskResult[V], len(operators))

	hasSelf := false
	for _, operator := range operators {
		if operator.Identifier == config.Identifier {
			hasSelf = true
			continue
		}

		wg.Add(1)
		go func(operator *so.SigningOperator) {
			defer wg.Done()
			result, err := task(ctx, operator)
			results <- taskResult[V]{
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

	if hasSelf {
		result, err := task(ctx, config.SigningOperatorMap[config.Identifier])
		if err != nil {
			return nil, err
		}
		resultsMap[config.Identifier] = result
	}

	logger.Info("Successfully executed task with all operators")

	return resultsMap, nil
}
