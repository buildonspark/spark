package dkg

import (
	"testing"

	testutil "github.com/lightsparkdev/spark-go/test_util"
)

func TestDKG(t *testing.T) {
	config, err := testutil.TestConfig()
	if err != nil {
		t.Fatal(err)
	}

	err = GenerateKeys(config, 100)
	if err != nil {
		t.Fatal(err)
	}
}
