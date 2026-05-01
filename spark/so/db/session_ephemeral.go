package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so/entephemeral"
	soerrors "github.com/lightsparkdev/spark/so/errors"
	"go.uber.org/zap"
)

var errFailedToCreateEphemeralTx = errors.New("failed to create ephemeral database transaction")

// EphemeralSessionFactory is an interface for creating a new ephemeral DB session.
type EphemeralSessionFactory interface {
	NewSession(ctx context.Context, opts ...SessionOption) entephemeral.Session
	NewReadOnlySession(ctx context.Context, opts ...SessionOption) entephemeral.Session
}

// DefaultEphemeralSessionFactory creates ephemeral sessions backed by an entephemeral client.
type DefaultEphemeralSessionFactory struct {
	dbClient *entephemeral.Client
}

func NewDefaultEphemeralSessionFactory(dbClient *entephemeral.Client) *DefaultEphemeralSessionFactory {
	return &DefaultEphemeralSessionFactory{dbClient: dbClient}
}

func (f *DefaultEphemeralSessionFactory) NewSession(ctx context.Context, opts ...SessionOption) entephemeral.Session {
	sessionConfig := newSessionConfig(opts...)
	baseProvider := entephemeral.NewEntClientTxProvider(f.dbClient)
	provider := NewEphemeralTxProviderWithTimeout(baseProvider, sessionConfig.txBeginTimeout)
	return &EphemeralSession{
		ctx:      ctx,
		dbClient: f.dbClient,
		provider: provider,
	}
}

func (f *DefaultEphemeralSessionFactory) NewReadOnlySession(ctx context.Context, opts ...SessionOption) entephemeral.Session {
	return NewReadOnlyEphemeralSession(ctx, f.dbClient, opts...)
}

// EphemeralSession manages an ephemeral database transaction over a request/task lifetime.
// Unlike Session, GetClient returns the raw client when no transaction exists (lazy begin),
// and there is no notification buffering.
//
// EphemeralSession is not safe for concurrent use. The underlying database connection does
// not support concurrent queries, so all methods must be called from a single goroutine.
type EphemeralSession struct {
	ctx      context.Context
	dbClient *entephemeral.Client
	provider entephemeral.TxProvider
	// currentTx is the live transaction, or nil if none has been started.
	currentTx *entephemeral.Tx
	// commitErr records the error from an in-handler DbCommit attempt that failed. It allows
	// middlewares to detect and propagate the failure even after currentTx has been cleared.
	commitErr error
	// txWasStarted is set to true the first time GetOrBeginTx successfully begins a transaction.
	// It remains true even after the transaction is committed or rolled back, distinguishing
	// "session used" from "session injected but never accessed".
	txWasStarted bool
}

// GetOrBeginTx retrieves the current transaction if one exists, or begins a new one.
func (s *EphemeralSession) GetOrBeginTx(ctx context.Context) (*entephemeral.Tx, error) {
	if s.currentTx != nil {
		return s.currentTx, nil
	}

	logger := logging.GetLoggerFromContext(ctx)

	// Use the session's base context for tx begin so tx lifetime/timeouts remain
	// tied to session construction rather than the caller's transient context.
	tx, err := s.provider.GetOrBeginTx(s.ctx)
	if err != nil {
		logger.Error("Failed to create new ephemeral transaction", zap.Error(err))
		return nil, err
	}

	tx.OnCommit(func(fn entephemeral.Committer) entephemeral.Committer {
		return entephemeral.CommitFunc(func(ctx context.Context, tx *entephemeral.Tx) error {
			err := fn.Commit(ctx, tx)
			if err != nil {
				logging.GetLoggerFromContext(ctx).Error("Failed to commit ephemeral transaction", zap.Error(err))
				s.commitErr = err
				// Leave currentTx set so that a deferred DbRollback (e.g. from middleware)
				// can still issue a rollback via GetTxIfExists.
				return err
			}
			s.currentTx = nil
			return nil
		})
	})
	tx.OnRollback(func(fn entephemeral.Rollbacker) entephemeral.Rollbacker {
		return entephemeral.RollbackFunc(func(ctx context.Context, tx *entephemeral.Tx) error {
			err := fn.Rollback(ctx, tx)
			if err != nil && !errors.Is(err, sql.ErrTxDone) {
				logging.GetLoggerFromContext(ctx).Error("Failed to rollback ephemeral transaction", zap.Error(err))
			}
			s.currentTx = nil
			s.commitErr = nil
			return err
		})
	})

	s.currentTx = tx
	s.txWasStarted = true
	return tx, nil
}

// GetClient returns a client that may be backed by a transaction if one already exists.
// Unlike Session.GetClient, this does NOT begin a new transaction — it returns the raw
// client when no transaction is active (lazy semantics).
func (s *EphemeralSession) GetClient(context.Context) (*entephemeral.Client, error) {
	if s.currentTx != nil {
		return s.currentTx.Client(), nil
	}
	return s.dbClient, nil
}

func (s *EphemeralSession) GetTxIfExists() *entephemeral.Tx {
	return s.currentTx
}

func (s *EphemeralSession) CommitError() error {
	return s.commitErr
}

func (s *EphemeralSession) TxWasStarted() bool {
	return s.txWasStarted
}

// ReadOnlyEphemeralSession is a lightweight session for read-only ephemeral DB access.
type ReadOnlyEphemeralSession struct {
	dbClient *entephemeral.Client
}

func NewReadOnlyEphemeralSession(_ context.Context, dbClient *entephemeral.Client, _ ...SessionOption) entephemeral.Session {
	return &ReadOnlyEphemeralSession{dbClient: dbClient}
}

func (r *ReadOnlyEphemeralSession) GetOrBeginTx(context.Context) (*entephemeral.Tx, error) {
	return nil, fmt.Errorf("read-only ephemeral session does not support explicit transactions")
}

func (r *ReadOnlyEphemeralSession) GetClient(context.Context) (*entephemeral.Client, error) {
	return r.dbClient, nil
}

func (r *ReadOnlyEphemeralSession) GetTxIfExists() *entephemeral.Tx { return nil }

func (r *ReadOnlyEphemeralSession) CommitError() error { return nil }

func (r *ReadOnlyEphemeralSession) TxWasStarted() bool { return false }

// EphemeralTxProviderWithTimeout wraps an entephemeral.TxProvider with timeout behavior.
type EphemeralTxProviderWithTimeout struct {
	wrapped entephemeral.TxProvider
	timeout time.Duration
}

func NewEphemeralTxProviderWithTimeout(provider entephemeral.TxProvider, timeout time.Duration) *EphemeralTxProviderWithTimeout {
	return &EphemeralTxProviderWithTimeout{
		wrapped: provider,
		timeout: timeout,
	}
}

func (t *EphemeralTxProviderWithTimeout) GetClient(ctx context.Context) (*entephemeral.Client, error) {
	// Note: EphemeralSession.GetClient does not delegate to the provider; it reads s.dbClient
	// directly. This method satisfies the TxProvider interface but is not called in normal usage.
	return t.wrapped.GetClient(ctx)
}

func (t *EphemeralTxProviderWithTimeout) GetOrBeginTx(ctx context.Context) (*entephemeral.Tx, error) {
	return getOrBeginTxWithTimeout(ctx, txBeginTimeoutConfig[*entephemeral.Tx]{
		timeout:    t.timeout,
		txTypeName: "ephemeral db transaction",
		begin:      t.wrapped.GetOrBeginTx,
		rollback: func(tx *entephemeral.Tx) error {
			return tx.Rollback()
		},
		timeoutErr:      ErrTxBeginTimeout,
		beginFailureErr: soerrors.InternalDatabaseTransactionLifecycleError(errFailedToCreateEphemeralTx),
	})
}
