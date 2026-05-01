package entephemeral

import (
	"context"
	"errors"
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

var ErrNoTransactionProvider = errors.New("no transaction provider found in context")

// A TxProvider is an interface that provides a method to either get an existing transaction,
// or begin a new transaction if none exists.
type TxProvider interface {
	// Get the current transaction from the context, or begin a new one if none exists.
	GetOrBeginTx(context.Context) (*Tx, error)
	// Get a client that may be backed by a transaction
	GetClient(context.Context) (*Client, error)
}

// Session is not safe for concurrent use. Although a Session can be retrieved from a context
// (which is accessible from any goroutine), all methods must be called from a single goroutine.
type Session interface {
	TxProvider
	// GetTxIfExists returns the current transaction if one exists, without starting a new one.
	// Returns nil if no transaction is currently active.
	GetTxIfExists() *Tx
	// CommitError returns the error from an in-handler DbCommit attempt that failed, or nil if
	// no commit was attempted or it succeeded. This allows middlewares to detect and propagate
	// masked commit failures even when GetTxIfExists returns nil.
	CommitError() error
	// TxWasStarted reports whether GetOrBeginTx was ever called successfully on this session.
	// This distinguishes a session that committed in-handler (txWasStarted=true, GetTxIfExists=nil)
	// from one that was injected but never used (txWasStarted=false, GetTxIfExists=nil).
	TxWasStarted() bool
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

	return nil, ErrNoTransactionProvider
}

// GetTxFromContext returns the underlying database transaction from the context.
// This should only be used where explicit transaction commit/rollback is needed.
func GetTxFromContext(ctx context.Context) (*Tx, error) {
	if txProvider, ok := ctx.Value(dbSessionKey).(TxProvider); ok {
		return txProvider.GetOrBeginTx(ctx)
	}

	return nil, ErrNoTransactionProvider
}

// DbCommit commits the active transaction if one exists.
// If no transaction is active, it is a no-op.
//
// Warning: callers must always propagate errors from DbCommit. Two divergence
// scenarios exist:
//   - If DbCommit succeeds but the handler returns a non-nil error, the ephemeral
//     TX is already committed while the middleware rolls back the main TX.
//   - If DbCommit fails, currentTx is kept set so a deferred rollback can still
//     fire. The error is also recorded on the session so middlewares can detect
//     the masked failure via CommitError() and return an error even when the
//     handler returns nil.
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
//
// Warning: if a handler calls DbRollback and then returns nil, the ephemeral
// transaction is already rolled back and currentTx is nil, so the middleware
// will skip both its rollback and commit paths — silently discarding any
// ephemeral writes without error.
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
