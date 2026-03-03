package task

import (
	"context"
	"database/sql"
	"fmt"
)

type rawDbClientContextKey struct{}

func InjectRawClient(ctx context.Context, db *sql.DB) context.Context {
	return context.WithValue(ctx, rawDbClientContextKey{}, db)
}

func GetRawClientFromContext(ctx context.Context) (*sql.DB, error) {
	db, ok := ctx.Value(rawDbClientContextKey{}).(*sql.DB)
	if !ok || db == nil {
		return nil, fmt.Errorf("no raw database client found in context")
	}
	return db, nil
}
