package so

import (
	"testing"

	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/stretchr/testify/require"
)

func TestDatabaseDriver_RecognizesPostgresURI(t *testing.T) {
	t.Parallel()
	for _, scheme := range []string{"postgres://", "postgresql://", "POSTGRES://", "PostgreSQL://"} {
		t.Run(scheme, func(t *testing.T) {
			t.Parallel()
			cfg := &Config{DatabasePath: scheme + "user:pass@localhost:5432/main"}
			require.Equal(t, "postgres", cfg.DatabaseDriver())
		})
	}
}

func TestEphemeralDatabaseDriver_RecognizesPostgresURI(t *testing.T) {
	t.Parallel()
	for _, scheme := range []string{"postgres://", "postgresql://"} {
		t.Run(scheme, func(t *testing.T) {
			t.Parallel()
			cfg := &Config{EphemeralDatabasePath: scheme + "host/ephemeral"}
			require.Equal(t, "postgres", cfg.EphemeralDatabaseDriver())
		})
	}
}

func TestDatabaseDriver_DefaultsToSQLite(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		DatabasePath:          "/tmp/spark.db",
		EphemeralDatabasePath: "/tmp/spark_ephemeral.db",
	}

	require.Equal(t, "sqlite3", cfg.DatabaseDriver())
	require.Equal(t, "sqlite3", cfg.EphemeralDatabaseDriver())
}

func TestEphemeralDatabaseDriver_ReturnsEmptyStringWhenPathEmpty(t *testing.T) {
	t.Parallel()
	cfg := &Config{EphemeralDatabasePath: ""}
	require.Empty(t, cfg.EphemeralDatabaseDriver())
}

func TestNewEphemeralDBConnector_ReturnsErrorWhenPathEmpty(t *testing.T) {
	t.Parallel()
	cfg := &Config{EphemeralDatabasePath: ""}
	_, err := NewEphemeralDBConnector(t.Context(), cfg, knobs.NewEmptyFixedKnobs())
	require.Error(t, err)
}

func TestNewEphemeralDBConnector_UsesEphemeralIsRDS(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		EphemeralDatabasePath: "/tmp/spark_ephemeral.db",
		IsRDS:                 true,
		EphemeralIsRDS:        false,
	}

	connector, err := NewEphemeralDBConnector(t.Context(), cfg, knobs.NewEmptyFixedKnobs())
	require.NoError(t, err)
	require.False(t, connector.isRDS)
}
