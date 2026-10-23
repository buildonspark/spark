// Command entschema exports the generated Ent tables as PostgreSQL HCL.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"time"

	"ariga.io/atlas/sql/postgres"
	atlasschema "ariga.io/atlas/sql/schema"
	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	entschema "entgo.io/ent/dialect/sql/schema"
	_ "github.com/jackc/pgx/v5/stdlib"
	operatormigrate "github.com/lightsparkdev/spark/so/ent/migrate"
	ephemeralmigrate "github.com/lightsparkdev/spark/so/entephemeral/migrate"
)

func main() {
	family := flag.String("database", "", "operator or ephemeral")
	flag.Parse()
	if err := run(*family); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(family string) error {
	var source []*entschema.Table
	switch family {
	case "operator":
		source = operatormigrate.Tables
	case "ephemeral":
		source = ephemeralmigrate.Tables
	default:
		return fmt.Errorf("database must be operator or ephemeral")
	}
	url := os.Getenv("SPARK_SCHEMA_DEV_URL")
	if url == "" {
		return fmt.Errorf("SPARK_SCHEMA_DEV_URL must name a disposable PostgreSQL database")
	}
	db, err := sql.Open("pgx", url)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	model, err := entschema.NewMigrate(entsql.OpenDB(dialect.Postgres, db), entschema.WithSchemaName("public"))
	if err != nil {
		return err
	}
	tables, err := entschema.CopyTables(source)
	if err != nil {
		return err
	}
	selected := make([]*entschema.Table, 0, len(tables))
	for _, table := range tables {
		// The external SQL owns this parent; Ent still generates its client.
		if family == "operator" && table.Name == "signing_nonces" {
			continue
		}
		selected = append(selected, table)
	}
	realm, err := model.StateReader(selected...).ReadState(ctx)
	if err != nil {
		return err
	}
	for _, schema := range realm.Schemas {
		if schema.Name == "" {
			schema.Name = "public"
		}
	}
	if err := normalizeIndexNames(ctx, db, realm); err != nil {
		return err
	}
	encoded, err := postgres.MarshalHCL.MarshalSpec(realm)
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(encoded)
	return err
}

// PostgreSQL names are server-defined. Cast only identifiers, preserving Ent's
// original expressions so the exporter does not rewrite partial-index predicates.
func normalizeIndexNames(ctx context.Context, db *sql.DB, realm *atlasschema.Realm) error {
	var indexes []*atlasschema.Index
	var names []string
	for _, schema := range realm.Schemas {
		for _, table := range schema.Tables {
			for _, index := range table.Indexes {
				indexes = append(indexes, index)
				names = append(names, index.Name)
			}
		}
	}
	if len(names) == 0 {
		return nil
	}
	rows, err := db.QueryContext(ctx, "SELECT value::pg_catalog.name::text FROM unnest($1::text[]) WITH ORDINALITY AS names(value, position) ORDER BY position", names)
	if err != nil {
		return err
	}
	defer rows.Close()
	seen := make(map[string]bool)
	position := 0
	for rows.Next() {
		if position >= len(indexes) {
			return fmt.Errorf("unexpected identifier count")
		}
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		index := indexes[position]
		key := index.Table.Schema.Name + "/" + name
		if seen[key] {
			return fmt.Errorf("index names collide after PostgreSQL normalization: %s", key)
		}
		seen[key] = true
		index.Name = name
		position++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if position != len(indexes) {
		return fmt.Errorf("incomplete identifier normalization")
	}
	return nil
}
