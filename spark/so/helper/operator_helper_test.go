package helper_test

import (
	"testing"

	"github.com/lightsparkdev/spark/so/helper"
	testutil "github.com/lightsparkdev/spark/test_util"
)

func TestOperatorSelectionAll(t *testing.T) {
	config, err := testutil.TestConfig()
	if err != nil {
		t.Fatal(err)
	}
	selection := helper.OperatorSelection{
		Option: helper.OperatorSelectionOptionAll,
	}

	operatorList, err := selection.OperatorList(config)
	if err != nil {
		t.Fatal(err)
	}

	if len(operatorList) != len(config.SigningOperatorMap) {
		t.Fatalf("expected %d operators, got %d", len(config.SigningOperatorMap), len(operatorList))
	}
}

func TestOperatorSelectionThreshold(t *testing.T) {
	config, err := testutil.TestConfig()
	if err != nil {
		t.Fatal(err)
	}

	selection := helper.OperatorSelection{
		Option:    helper.OperatorSelectionOptionThreshold,
		Threshold: 2,
	}

	operatorList, err := selection.OperatorList(config)
	if err != nil {
		t.Fatal(err)
	}

	if len(operatorList) != selection.Threshold {
		t.Fatalf("expected %d operators, got %d", selection.Threshold, len(operatorList))
	}

	operatorNewList, err := selection.OperatorList(config)
	if err != nil {
		t.Fatal(err)
	}

	// Since we're using the same selection object, the operator list should be cached and return the same list
	if len(operatorList) != len(operatorNewList) {
		t.Fatalf("expected lists to be same length, got %d and %d", len(operatorList), len(operatorNewList))
	}

	for i := range operatorList {
		if operatorList[i].Identifier != operatorNewList[i].Identifier {
			t.Fatalf("expected same operators in same order, got different operator at index %d", i)
		}
	}
}
