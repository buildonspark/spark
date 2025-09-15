package helper_test

import (
	"context"
	"errors"
	"maps"
	"slices"
	"testing"

	"github.com/lightsparkdev/spark/so"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lightsparkdev/spark/so/helper"
	sparktesting "github.com/lightsparkdev/spark/testing"
)

func TestNewPreSelectedOperatorSelection_InvalidIDs_Errors(t *testing.T) {
	config := sparktesting.TestConfig(t)
	selectedIDs := []string{"not-a-real-id"}

	got, err := helper.NewPreSelectedOperatorSelection(config, selectedIDs)
	require.ErrorContains(t, err, "not found in signing operator map")
	assert.Nil(t, got)
}

func TestOperatorList_PreSelected(t *testing.T) {
	config := sparktesting.TestConfig(t)
	selectedIDs := slices.Collect(maps.Keys(config.SigningOperatorMap))[:2]
	want := make([]*so.SigningOperator, 2)
	for i, id := range selectedIDs {
		want[i] = config.SigningOperatorMap[id]
	}

	selection, err := helper.NewPreSelectedOperatorSelection(config, selectedIDs)
	require.NoError(t, err)

	assert.Equal(t, helper.OperatorSelectionOptionPreSelected, selection.Option)
	got, err := selection.OperatorList(config)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestOperatorList_OperatorSelectionAll(t *testing.T) {
	config := sparktesting.TestConfig(t)
	selection := helper.OperatorSelection{
		Option: helper.OperatorSelectionOptionAll,
	}

	got, err := selection.OperatorList(config)
	require.NoError(t, err)
	assert.Len(t, got, len(config.SigningOperatorMap))
}

func TestOperatorList_OptionThreshold(t *testing.T) {
	config := sparktesting.TestConfig(t)
	selection := helper.OperatorSelection{
		Option:    helper.OperatorSelectionOptionThreshold,
		Threshold: 2,
	}

	got, err := selection.OperatorList(config)
	require.NoError(t, err)
	assert.Len(t, got, selection.Threshold)
}

func TestOperatorList_OptionThreshold_ThresholdTooHigh_Errors(t *testing.T) {
	config := sparktesting.TestConfig(t)
	selection := helper.OperatorSelection{
		Option:    helper.OperatorSelectionOptionThreshold,
		Threshold: len(config.SigningOperatorMap) + 1, // Too high
	}

	got, err := selection.OperatorList(config)
	require.ErrorContains(t, err, "exceeds length of signing operator list")
	assert.Empty(t, got)
}

func TestOperatorList_ExcludeSelf(t *testing.T) {
	config := sparktesting.TestConfig(t)
	selection := helper.OperatorSelection{
		Option: helper.OperatorSelectionOptionExcludeSelf,
	}

	got, err := selection.OperatorList(config)
	require.NoError(t, err)

	wantLen := len(config.SigningOperatorMap) - 1
	assert.Len(t, got, wantLen)
	for _, op := range got {
		assert.NotEqual(t, config.Identifier, op.Identifier, "operator list should not include self identifier %s", config.Identifier)
	}
}

func TestOperatorList_CachesRepeatedCalls(t *testing.T) {
	config := sparktesting.TestConfig(t)
	preselected, _ := helper.NewPreSelectedOperatorSelection(config, []string{config.Identifier})

	tests := []struct {
		name      string
		selection *helper.OperatorSelection
	}{
		{
			name:      "PreSelected",
			selection: preselected,
		},
		{
			name: "All",
			selection: &helper.OperatorSelection{
				Option: helper.OperatorSelectionOptionAll,
			},
		},
		{
			name: "ExcludeSelf",
			selection: &helper.OperatorSelection{
				Option: helper.OperatorSelectionOptionExcludeSelf,
			},
		},
		{
			name: "Threshold",
			selection: &helper.OperatorSelection{
				Option:    helper.OperatorSelectionOptionThreshold,
				Threshold: 2,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// First call
			operatorList, err := tt.selection.OperatorList(config)
			require.NoError(t, err)
			// Second call should return the same value as the first one, due to caching.
			operatorNewList, err := tt.selection.OperatorList(config)
			require.NoError(t, err)
			assert.Equal(t, operatorList, operatorNewList)
		})
	}
}

func TestExecuteTaskWithAllOperators(t *testing.T) {
	config := sparktesting.TestConfig(t)
	selection := helper.OperatorSelection{
		Option: helper.OperatorSelectionOptionAll,
	}
	task := func(_ context.Context, operator *so.SigningOperator) (string, error) {
		return operator.Identifier, nil
	}

	results, err := helper.ExecuteTaskWithAllOperators(t.Context(), config, &selection, task)
	require.NoError(t, err)
	assert.Len(t, results, len(config.SigningOperatorMap))

	for id := range config.SigningOperatorMap {
		val, ok := results[id]
		assert.True(t, ok, "missing result for operator %s", id)
		assert.Equal(t, id, val, "expected result for operator %s to be %s, got %s", id, id, val)
	}
}

func TestExecuteTaskWithAllOperators_Error(t *testing.T) {
	config := sparktesting.TestConfig(t)
	selection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionAll}
	errMsg := "intentional error"
	var failID string
	for id := range config.SigningOperatorMap {
		failID = id
		break
	}
	task := func(_ context.Context, operator *so.SigningOperator) (string, error) {
		if operator.Identifier == failID {
			return "", errors.New(errMsg)
		}
		return operator.Identifier, nil
	}

	_, err := helper.ExecuteTaskWithAllOperators(t.Context(), config, &selection, task)
	require.Error(t, err)
	assert.Equal(t, errMsg, err.Error())
}

func TestOperatorList_EmptyOperatorsMap(t *testing.T) {
	config := &so.Config{
		SigningOperatorMap: map[string]*so.SigningOperator{},
	}
	errMsg := "no signing operators configured"
	operatorSelection := helper.OperatorSelection{
		Option: helper.OperatorSelectionOptionPreSelected,
	}

	tests := []struct {
		name   string
		config *so.Config
	}{
		{
			name:   "WithNilConfig",
			config: nil,
		},
		{
			name: "WithEmptyOperatorsMap",
			config: &so.Config{
				SigningOperatorMap: map[string]*so.SigningOperator{},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := operatorSelection.OperatorList(config)
			require.ErrorContains(t, err, errMsg)
		})
	}
}
