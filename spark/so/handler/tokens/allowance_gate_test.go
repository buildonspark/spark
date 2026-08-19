package tokens

import (
	"testing"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fixedValueProvider backs a real (non-test-double) knobs service so the gate
// is exercised through the same code path production uses.
type fixedValueProvider struct {
	values map[string]float64
}

func (p fixedValueProvider) GetValue(key string, defaultValue float64) float64 {
	if v, ok := p.values[key]; ok {
		return v
	}
	return defaultValue
}

// TestAllowancesEnabledIsDeterministicAtIntermediateValues guards against the
// gate regressing to a per-call random rollout: at any intermediate knob value
// (e.g. 50) every call must return the same answer, otherwise operators
// disagree mid-rollout and a created grant randomly fails to replicate or
// revoke on a peer.
func TestAllowancesEnabledIsDeterministicAtIntermediateValues(t *testing.T) {
	for name, tc := range map[string]struct {
		value           float64
		expectedEnabled bool
	}{
		"intermediate value enables": {value: 50, expectedEnabled: true},
		"fully on":                   {value: 100, expectedEnabled: true},
		"fully off":                  {value: 0, expectedEnabled: false},
	} {
		t.Run(name, func(t *testing.T) {
			service := knobs.New(fixedValueProvider{values: map[string]float64{
				knobs.KnobTokenAllowancesEnabled: tc.value,
			}})
			ctx := knobs.InjectKnobsService(t.Context(), service)
			for range 1000 {
				require.Equal(t, tc.expectedEnabled, allowancesEnabled(ctx))
			}
		})
	}
}

// TestAllowancesEnabledDefaultsOff verifies the gate fails closed when no knob
// value is configured.
func TestAllowancesEnabledDefaultsOff(t *testing.T) {
	ctx := knobs.InjectKnobsService(t.Context(), knobs.New(fixedValueProvider{}))
	require.False(t, allowancesEnabled(ctx))
}

// TestAllowanceSpendMeteringRejectedWhenKnobDisabled verifies the delegated
// spend path stays blocked while the killswitch is off (only revocation is
// exempt from the gate).
func TestAllowanceSpendMeteringRejectedWhenKnobDisabled(t *testing.T) {
	ctx := knobs.InjectKnobsService(t.Context(), knobs.New(fixedValueProvider{}))
	err := validateAndMeterAllowanceSpend(ctx, nil, nil, uuid.New(), nil, nil)
	require.Error(t, err)
	require.Equal(t, codes.Unimplemented, status.Code(err))
}
