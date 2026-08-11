package logging

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestAddRequestFieldsOnce_KeepsFirstValuePerKey(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	ctx := Inject(InitRequestFields(t.Context()), zap.New(core))

	AddRequestFieldsOnce(ctx, zap.String("network", "MAINNET"))
	AddRequestFieldsOnce(ctx, zap.String("network", "REGTEST"))
	AddRequestFieldsOnce(ctx, zap.String("other", "kept"))

	GetLoggerWithAccumulatedRequestFields(ctx).Info("summary")

	entries := logs.All()
	require.Len(t, entries, 1)
	assert.Len(t, entries[0].Context, 2)
	fields := entries[0].ContextMap()
	assert.Equal(t, "MAINNET", fields["network"])
	assert.Equal(t, "kept", fields["other"])
}

func TestAddRequestFieldsOnce_SkipsKeyAlreadyAddedByPlainVariant(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	ctx := Inject(InitRequestFields(t.Context()), zap.New(core))

	AddRequestFields(ctx, zap.String("network", "MAINNET"))
	AddRequestFieldsOnce(ctx, zap.String("network", "REGTEST"))

	GetLoggerWithAccumulatedRequestFields(ctx).Info("summary")

	entries := logs.All()
	require.Len(t, entries, 1)
	assert.Len(t, entries[0].Context, 1)
	assert.Equal(t, "MAINNET", entries[0].ContextMap()["network"])
}

func TestAddRequestFieldsOnce_NoContainerIsNoop(t *testing.T) {
	AddRequestFieldsOnce(t.Context(), zap.String("network", "MAINNET"))
}
