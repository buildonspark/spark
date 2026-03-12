package so

import (
	"testing"

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

func TestDatabaseDriver_DefaultsToSQLite(t *testing.T) {
	t.Parallel()
	cfg := &Config{DatabasePath: "/tmp/spark.db"}
	require.Equal(t, "sqlite3", cfg.DatabaseDriver())
}
