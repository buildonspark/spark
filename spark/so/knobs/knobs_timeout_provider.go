package knobs

import (
	"time"
)

type KnobsTimeoutProvider struct {
	knobsService   Knobs
	defaultTimeout time.Duration
}

// NewKnobsTimeoutProvider creates a new timeout provider that uses knobs for configuration.
func NewKnobsTimeoutProvider(knobsService Knobs, defaultTimeout time.Duration) *KnobsTimeoutProvider {
	return &KnobsTimeoutProvider{
		knobsService:   knobsService,
		defaultTimeout: defaultTimeout,
	}
}

// GetTimeoutForMethod implements common.TimeoutProvider.
// Uses the knobs service to get a method-specific timeout value.
func (k *KnobsTimeoutProvider) GetTimeoutForMethod(method string) time.Duration {
	return GetTargetDurationSeconds(k.knobsService, KnobGRPCClientTimeout, &method, k.defaultTimeout)
}
