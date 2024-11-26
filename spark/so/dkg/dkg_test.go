package dkg

import (
	"testing"

	"github.com/lightsparkdev/spark-go/test_util"
)

func TestDKG(t *testing.T) {
	config, err := test_util.TestConfig()
	if err != nil {
		t.Fatal(err)
	}

	err = GenerateKeys(config, 100)
	if err != nil {
		t.Fatal(err)
	}
}
