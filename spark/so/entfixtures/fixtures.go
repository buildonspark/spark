package entfixtures

import (
	"context"
	"math/rand/v2"
	"testing"

	"github.com/lightsparkdev/spark/so/ent"
	"github.com/stretchr/testify/require"
)

// Fixtures provides base helper functions for creating test data
type Fixtures struct {
	T      *testing.T
	Ctx    context.Context
	Client *ent.Client
	rng    *rand.ChaCha8
}

// New creates a new Fixtures helper
func New(t *testing.T, ctx context.Context, client *ent.Client) *Fixtures {
	return &Fixtures{
		T:      t,
		Ctx:    ctx,
		Client: client,
		rng:    rand.NewChaCha8([32]byte{}),
	}
}

// WithRNG sets a specific random number generator for reproducible tests
func (f *Fixtures) WithRNG(rng *rand.ChaCha8) *Fixtures {
	f.rng = rng
	return f
}

// RandomBytes generates n random bytes using the fixture's RNG
func (f *Fixtures) RandomBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = f.rng.Read(b)
	return b
}

// RequireNoError is a convenience method for require.NoError
func (f *Fixtures) RequireNoError(err error, msgAndArgs ...any) {
	require.NoError(f.T, err, msgAndArgs...)
}
