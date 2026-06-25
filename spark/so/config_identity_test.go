package so

import (
	"strconv"
	"sync"
	"testing"

	"github.com/lightsparkdev/spark/common/keys"
	"github.com/stretchr/testify/require"
)

// TestGetOperatorIdentifierFromIdentityPublicKey_ConcurrentAccess is a regression
// test for a data race in the lazy build of identityPubkeyToOperatorIdentifierMap.
// Concurrent callers sharing one Config (e.g. parallel subtests) all saw an empty
// map and raced into buildIdentityPubkeyMap, tripping "fatal error: concurrent map
// writes". Run with -race to also catch the unsynchronized map field assignment.
func TestGetOperatorIdentifierFromIdentityPublicKey_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	const operatorCount = 8
	signingOperatorMap := make(map[string]*SigningOperator, operatorCount)
	pubkeys := make([]keys.Public, operatorCount)
	for i := range operatorCount {
		identifier := strconv.Itoa(i + 1)
		pubkey := keys.GeneratePrivateKey().Public()
		signingOperatorMap[identifier] = &SigningOperator{
			ID:                uint64(i),
			Identifier:        identifier,
			IdentityPublicKey: pubkey,
		}
		pubkeys[i] = pubkey
	}
	cfg := &Config{SigningOperatorMap: signingOperatorMap}

	const goroutines = 64
	results := make([]string, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := range goroutines {
		go func(g int) {
			defer wg.Done()
			results[g] = cfg.GetOperatorIdentifierFromIdentityPublicKey(pubkeys[g%operatorCount])
		}(g)
	}
	wg.Wait()

	for g := range goroutines {
		require.Equal(t, strconv.Itoa(g%operatorCount+1), results[g])
	}
}
