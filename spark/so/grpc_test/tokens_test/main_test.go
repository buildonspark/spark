package tokens_test

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	exitCode := 0
	for _, useV3 := range []bool{
		false,
		true,
	} {
		broadcastTokenTestsUseV3 = useV3
		if code := m.Run(); code != 0 {
			exitCode = code
			break
		}
	}
	os.Exit(exitCode)
}
