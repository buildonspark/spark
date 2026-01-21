//go:build ignore

// This program dumps the Ent schema as PostgreSQL DDL.
// It's used instead of `ent schema` because it correctly includes
// PostgreSQL-specific features like INCLUDE columns on indexes.
//
// Usage: go run ./so/ent/migrate/dump_schema.go
package main

import (
	"context"
	"fmt"
	"os"

	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql/schema"
	"github.com/lightsparkdev/spark/so/ent/migrate"
)

func main() {
	// PostgreSQL version for DDL syntax compatibility
	const postgresVersion = "17"
	ddl, err := schema.Dump(context.Background(), dialect.Postgres, postgresVersion, migrate.Tables)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error dumping schema: %v\n", err)
		os.Exit(1)
	}
	fmt.Print(ddl)
}
