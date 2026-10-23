package db

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPtahMigrationTemplate(t *testing.T) {
	t.Parallel()
	_, tc := ConnectToTestPostgres(t)
	database, err := sql.Open("pgx", tc.DatabasePath())
	require.NoError(t, err)
	defer database.Close()

	var revisions, writtenByPtah int
	err = database.QueryRowContext(t.Context(), `
		SELECT count(*), count(*) FILTER (WHERE operator_version ILIKE 'ptah%')
		FROM atlas_schema_revisions.atlas_schema_revisions
	`).Scan(&revisions, &writtenByPtah)
	require.NoError(t, err)
	files, err := filepath.Glob(filepath.Join(moduleRoot, "*.sql"))
	require.NoError(t, err)
	require.NotEmpty(t, files)
	require.Equal(t, len(files), revisions)
	require.Equal(t, revisions, writtenByPtah)
}
