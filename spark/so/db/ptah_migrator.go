package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"time"

	"github.com/peterldowns/pgtestdb"
	"github.com/peterldowns/pgtestdb/migrators/common"
)

type ptahDirMigrator struct {
	dir string
}

// Hash binds a cached template to the migration bytes, checksum, and runner.
func (m ptahDirMigrator) Hash() (string, error) {
	directory, err := common.HashDir(m.dir)
	if err != nil {
		return "", err
	}
	checksum, err := common.HashFile(filepath.Join(m.dir, "atlas.sum"))
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	version, err := common.Execute(ctx, nil, "ptah-compat", "version")
	if err != nil {
		return "", err
	}
	hash := common.NewRecursiveHash(
		common.Field("runner", version),
		common.Field("directory", directory),
		common.Field("checksum", checksum),
	)
	return hash.String(), nil
}

func (m ptahDirMigrator) Migrate(ctx context.Context, _ *sql.DB, template pgtestdb.Config) error {
	_, err := common.Execute(ctx, nil, "ptah-compat", "migrate", "apply",
		"--url", template.URL(), "--dir", "file://"+filepath.ToSlash(m.dir),
	)
	return err
}
