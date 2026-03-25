package knobs

// staticValuesProvider implements KnobsValuesProvider using a fixed map.
// Used for local development when K8s ConfigMap is not available.
// Unlike the K8s provider, values are immutable — changes require a process restart.
type staticValuesProvider struct {
	values map[string]float64
}

// NewStaticValuesProvider creates a KnobsValuesProvider backed by a static map.
// Keys use the same "name" or "name@target" format as the K8s ConfigMap provider.
func NewStaticValuesProvider(values map[string]float64) KnobsValuesProvider {
	if values == nil {
		values = make(map[string]float64)
	}
	return &staticValuesProvider{values: values}
}

func (p *staticValuesProvider) GetValue(key string, defaultValue float64) float64 {
	if v, ok := p.values[key]; ok {
		return v
	}
	return defaultValue
}
