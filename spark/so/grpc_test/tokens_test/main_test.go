package tokens_test

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	for _, useV3 := range []bool{false, true} {
		broadcastTokenTestsUseV3 = useV3
		if code := m.Run(); code != 0 {
			os.Exit(code)
		}
	}
}
