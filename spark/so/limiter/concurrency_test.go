package limiter

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResourceGuard_AcquireRelease(t *testing.T) {
	rg := NewResourceGuard()

	require.True(t, rg.Acquire("r1"))
	require.True(t, rg.Acquire("r2"))
	require.False(t, rg.Acquire("r1"))
	require.False(t, rg.Acquire("r1"))

	rg.Release("r1")

	require.True(t, rg.Acquire("r1"))
	require.False(t, rg.Acquire("r2"))

	rg.Release("r2")

	require.False(t, rg.Acquire("r1"))
	require.True(t, rg.Acquire("r2"))

	// Releasing a non-acquired resource should be a no-op
	rg.Release("r3")

	// We can still acquire the resource after the no-op.
	require.False(t, rg.Acquire("r1"))
	require.False(t, rg.Acquire("r2"))
	require.True(t, rg.Acquire("r3"))
}
