package knobs

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStaticValuesProvider(t *testing.T) {
	t.Run("returns value when key exists", func(t *testing.T) {
		p := NewStaticValuesProvider(map[string]float64{
			"spark.so.enable_instant_static_deposit@REGTEST": 100,
			"spark.database.pool.min_conns":                  4,
		})
		assert.InDelta(t, 100.0, p.GetValue("spark.so.enable_instant_static_deposit@REGTEST", 0), 0.001)
		assert.InDelta(t, 4.0, p.GetValue("spark.database.pool.min_conns", 0), 0.001)
	})

	t.Run("returns default when key missing", func(t *testing.T) {
		p := NewStaticValuesProvider(map[string]float64{})
		assert.InDelta(t, 42.0, p.GetValue("missing", 42.0), 0.001)
	})

	t.Run("nil map returns defaults", func(t *testing.T) {
		p := NewStaticValuesProvider(nil)
		assert.InDelta(t, 5.0, p.GetValue("anything", 5.0), 0.001)
	})
}

func TestStaticValuesProviderWithKnobs(t *testing.T) {
	// Verify it works end-to-end through knobs.New()
	p := NewStaticValuesProvider(map[string]float64{
		"my.knob":         75.0,
		"my.knob@REGTEST": 100.0,
	})
	k := New(p)
	require.NotNil(t, k)

	assert.InDelta(t, 75.0, k.GetValue("my.knob", 0), 0.001)
	target := "REGTEST"
	assert.InDelta(t, 100.0, k.GetValueTarget("my.knob", &target, 0), 0.001)
	assert.InDelta(t, 0.0, k.GetValue("unset.knob", 0), 0.001)
}
