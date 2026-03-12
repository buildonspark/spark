package entephemeral

import (
	"context"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// contextKey is a type for context keys.
type (
	dbSessionContextKey string
)

// dbSessionKey is the context key for the transaction provider.
const (
	dbSessionKey dbSessionContextKey = "dbsession_ephemeral"
)

// A TxProvider is an interface that provides a method to either get an existing transaction,
// or begin a new transaction if none exists.
type TxProvider interface {
	// Get the current transaction from the context, or begin a new one if none exists.
	GetOrBeginTx(context.Context) (*Tx, error)
	// Get a client that may be backed by a transaction
	GetClient(context.Context) (*Client, error)
}

type Session interface {
	TxProvider
	MarkTxDirty(context.Context)
	// GetTxIfExists returns the current transaction if one exists, without starting a new one.
	// Returns nil if no transaction is currently active.
	GetTxIfExists() *Tx
}

// ClientTxProvider is a TxProvider that uses an underlying ent.Client to create new transactions.
type ClientTxProvider struct {
	dbClient *Client
}

// NewEntClientTxProvider returns a low-level TxProvider backed by dbClient.
// Use it to construct a Session implementation (e.g. EphemeralSession in db/session_ephemeral.go)
// rather than passing it directly to Inject, which requires a full Session.
func NewEntClientTxProvider(dbClient *Client) *ClientTxProvider {
	return &ClientTxProvider{dbClient: dbClient}
}

func (e *ClientTxProvider) GetOrBeginTx(ctx context.Context) (*Tx, error) {
	tx, err := e.dbClient.Tx(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "failed to begin transaction: %v", err)
	}
	return tx, nil
}

func (e *ClientTxProvider) GetClient(_ context.Context) (*Client, error) {
	return e.dbClient, nil
}

// Inject the transaction provider into the context. This should ONLY be called from the start of
// a request or worker context (e.g. in a top-level gRPC interceptor).
func Inject(ctx context.Context, session Session) context.Context {
	return context.WithValue(ctx, dbSessionKey, session)
}

// GetDbFromContext returns the database client from the context. The client may be backed by a transaction.
func GetDbFromContext(ctx context.Context) (*Client, error) {
	if txProvider, ok := ctx.Value(dbSessionKey).(TxProvider); ok {
		return txProvider.GetClient(ctx)
	}

	return nil, fmt.Errorf("no transaction provider found in context")
}

// GetTxFromContext returns the underlying database transaction from the context.
// This should only be used where explicit transaction commit/rollback is needed.
func GetTxFromContext(ctx context.Context) (*Tx, error) {
	if txProvider, ok := ctx.Value(dbSessionKey).(TxProvider); ok {
		return txProvider.GetOrBeginTx(ctx)
	}

	return nil, fmt.Errorf("no transaction provider found in context")
}

func MarkTxDirty(ctx context.Context) {
	if session, ok := ctx.Value(dbSessionKey).(Session); ok {
		session.MarkTxDirty(ctx)
	}
}

// DbCommit commits the active transaction if one exists.
// If no transaction is active, it is a no-op.
func DbCommit(ctx context.Context) error {
	session, ok := ctx.Value(dbSessionKey).(Session)
	if !ok {
		return nil
	}

	tx := session.GetTxIfExists()
	if tx == nil {
		return nil
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}

// DbRollback rolls back the active transaction if one exists.
// If no transaction is active, it is a no-op.
func DbRollback(ctx context.Context) error {
	session, ok := ctx.Value(dbSessionKey).(Session)
	if !ok {
		return nil
	}

	tx := session.GetTxIfExists()
	if tx == nil {
		return nil
	}

	if err := tx.Rollback(); err != nil {
		return fmt.Errorf("failed to rollback transaction: %w", err)
	}

	return nil
}
