package grpctest

import (
	"context"
	"testing"

	"github.com/lightsparkdev/spark-go/so/dkg"
	testutil "github.com/lightsparkdev/spark-go/test_util"
)

func TestDKG(t *testing.T) {
	config, err := testutil.TestConfig()
	if err != nil {
		t.Fatal(err)
	}

	err = dkg.GenerateKeys(context.Background(), config, 100)
	if err != nil {
		t.Fatal(err)
	}
}
