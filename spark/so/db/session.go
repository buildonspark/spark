package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so/ent"
	soerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/knobs"
	"go.opentelemetry.io/otel/attribute"
	"go.uber.org/zap"
)

var (
	ErrTxBeginTimeout               = soerrors.UnavailableDatabaseTimeout(fmt.Errorf("the service is currently unavailable, please try again later"))
	DefaultNewTxTimeout             = 15 * time.Second
	DefaultNotificationFlushTimeout = 5 * time.Second
)

// SessionFactory is an interface for creating a new Session.
type SessionFactory interface {
	NewSession(ctx context.Context, opts ...SessionOption) ent.Session
}

// DefaultSessionFactory is the default implementation of SessionFactory that creates sessions
// using an ent.Client. It also provides a timeout for how long it will wait for a new transaction
// to be started, to prevent requests from hanging indefinitely if the database is unresponsive or
// overloaded.
type DefaultSessionFactory struct {
	dbClient *ent.Client
	knobs    knobs.Knobs
}

func NewDefaultSessionFactory(dbClient *ent.Client, knobs knobs.Knobs) *DefaultSessionFactory {
	return &DefaultSessionFactory{
		dbClient: dbClient,
		knobs:    knobs,
	}
}

type sessionConfig struct {
	txBeginTimeout time.Duration
	metricAttrs    []attribute.KeyValue
}

func newSessionConfig(opts ...SessionOption) sessionConfig {
	cfg := sessionConfig{
		txBeginTimeout: DefaultNewTxTimeout,
		metricAttrs:    []attribute.KeyValue{},
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	return cfg
}

type SessionOption func(*sessionConfig)

// Configures how long to wait for a new transaction to be started. If the timeout is reached, an
// error will be returned to the caller.
//
// A zero or negative duration means there is no timeout.
func WithTxBeginTimeout(timeout time.Duration) SessionOption {
	return func(c *sessionConfig) {
		c.txBeginTimeout = timeout
	}
}

// Configures additional attributes to be added to all metrics emitted by this session.
func WithMetricAttributes(attrs []attribute.KeyValue) SessionOption {
	return func(c *sessionConfig) {
		c.metricAttrs = attrs
	}
}

func (f *DefaultSessionFactory) NewSession(ctx context.Context, opts ...SessionOption) ent.Session {
	sessionConfig := newSessionConfig(opts...)
	baseProvider := ent.NewEntClientTxProvider(f.dbClient)
	metricsProvider := NewMetricsTxProvider(baseProvider, sessionConfig.metricAttrs)
	provider := NewTxProviderWithTimeout(metricsProvider, sessionConfig.txBeginTimeout)

	return &Session{
		ctx:       ctx,
		knobs:     f.knobs,
		dbClient:  f.dbClient,
		provider:  provider,
		currentTx: nil,
		mu:        sync.Mutex{},
	}
}

// A Session manages a transaction over the lifetime of a request or worker. It
// wraps a TxProvider for creating an initial transaction, and stores that transaction for
// subsequent requests until the transaction is committed or rolled back. Once the transaction
// is finished, it is cleared so a new one can begin the next time `GetOrBeginTx` is called.
type Session struct {
	// ctx is the context for this session. It is used to for creating new transactions within the
	// session to ensure that the session can clean those transactions up even if the context in which
	// the caller is operating is cancelled.
	ctx context.Context
	// The underlying ent.Client used to create new transactions and flush notifications.
	dbClient *ent.Client
	// Knobs for controlling session behavior.
	knobs knobs.Knobs
	// TxProvider is used to create a new transaction when needed.
	provider ent.TxProvider
	// Mutex for ensuring thread-safe access to `currentTx`.
	mu sync.Mutex
	// The current transaction being tracked by this session if a transaction has been started. When
	// the tracked transaction is committed or rolled back successfully, this field is set back to nil.
	currentTx *ent.Tx
	// The current set of notifications that have occurred during the transaction. When the current
	// transaction is committed, these notifications are flushed. If the transaction is rolled back,
	// these notifications are discarded.
	currentNotifications *ent.BufferedNotifier
	currentIsDirty       bool
}

// GetOrBeginTx retrieves the current transaction if it exists, otherwise it begins a new one.
// Furthermore, it inserts commit and rollback hooks that will clear the current transaction
// should the transaction be finished by the caller.
func (s *Session) GetOrBeginTx(ctx context.Context) (*ent.Tx, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.currentTx == nil {
		logger := logging.GetLoggerFromContext(ctx)

		// Important! We need to use the context from the session, not the one passed in, because we want
		// to ensure the transaction can be cleaned up even if the context passed in is cancelled.
		tx, err := s.provider.GetOrBeginTx(s.ctx)
		if err != nil {
			logger.Error("Failed to create new transaction", zap.Error(err))
			return nil, err
		}

		notifier := ent.NewBufferedNotifier(s.dbClient)

		s.currentTx = tx
		s.currentNotifications = &notifier
		s.currentIsDirty = false

		tx.OnCommit(func(fn ent.Committer) ent.Committer {
			return ent.CommitFunc(func(ctx context.Context, tx *ent.Tx) error {
				s.mu.Lock()
				defer s.mu.Unlock()

				if !s.currentIsDirty && s.knobs.RolloutRandom(knobs.KnobDatabaseOnlyCommitDirty, 0) {
					// Assume we will clear the state when we do a rollback. We should maybe just rollback but
					// this is the least disruptive for now.
					return nil
				}

				err := fn.Commit(ctx, tx)
				if err != nil {
					logger.Error("Failed to commit transaction", zap.Error(err))
				} else {
					// Send notifications asynchronously to avoid blocking the request.
					// We need to capture the notifier and flush it with a bounded context.
					notifier := s.currentNotifications
					go func() {
						ctx, cancel := context.WithTimeout(context.Background(), DefaultNotificationFlushTimeout)
						defer cancel()
						if flushErr := notifier.Flush(ctx); flushErr != nil {
							logger.Error("Failed to flush notifications after commit", zap.Error(flushErr))
						}
					}()
				}

				// Only set the current tx to nil if the transaction was committed successfully.
				// Otherwise, the transaction will be rolled back at last.
				if err == nil || errors.Is(err, sql.ErrTxDone) || errors.Is(err, context.Canceled) {
					s.currentTx = nil
					s.currentNotifications = nil
					s.currentIsDirty = false
				}

				return err
			})
		})
		tx.OnRollback(func(fn ent.Rollbacker) ent.Rollbacker {
			return ent.RollbackFunc(func(ctx context.Context, tx *ent.Tx) error {
				s.mu.Lock()
				defer s.mu.Unlock()

				err := fn.Rollback(ctx, tx)
				if err != nil && !errors.Is(err, sql.ErrTxDone) {
					logger.Error("Failed to rollback transaction", zap.Error(err))
				}

				s.currentTx = nil
				s.currentNotifications = nil
				s.currentIsDirty = false
				return err
			})
		})
	}
	return s.currentTx, nil
}

func (s *Session) MarkTxDirty(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.currentTx != nil {
		s.currentIsDirty = true
	}
}

// GetTxIfExists retrieves the current transaction if it exists, without starting a new one. If
// no current transaction exists, then returns nil.
func (s *Session) GetTxIfExists() *ent.Tx {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.currentTx
}

// GetClient returns a client that may be backed by a transaction. This is the preferred method
// for most database operations, as it allows the same code to work both inside and outside of
// explicit transactions.
func (s *Session) GetClient(ctx context.Context) (*ent.Client, error) {
	tx, err := s.GetOrBeginTx(ctx)
	if err != nil {
		return nil, err
	}
	return tx.Client(), nil
}

func (s *Session) Notify(ctx context.Context, n ent.Notification) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.currentNotifications == nil {
		return fmt.Errorf("no active transaction to buffer notification")
	}

	return s.currentNotifications.Notify(ctx, n)
}

// ReadOnlySession is a simplified session for read-only database operations.
// It doesn't support transactions or notifications, making it lightweight and safe
// for query-only endpoints.
type ReadOnlySession struct {
	dbClient *ent.Client
}

// NewReadOnlySession creates a new read-only session. SessionOptions are ignored.
func NewReadOnlySession(ctx context.Context, dbClient *ent.Client, opts ...SessionOption) ent.Session {
	return &ReadOnlySession{
		dbClient: dbClient,
	}
}

// GetOrBeginTx always returns an error for read-only sessions.
func (r *ReadOnlySession) GetOrBeginTx(ctx context.Context) (*ent.Tx, error) {
	return nil, fmt.Errorf("read-only session does not support explicit transactions")
}

// GetClient returns the underlying database client directly without a transaction.
func (r *ReadOnlySession) GetClient(ctx context.Context) (*ent.Client, error) {
	return r.dbClient, nil
}

// MarkTxDirty is a no-op for read-only sessions.
func (r *ReadOnlySession) MarkTxDirty(ctx context.Context) {
	// No-op: read-only sessions don't have transactions
}

// GetTxIfExists always returns nil for read-only sessions.
func (r *ReadOnlySession) GetTxIfExists() *ent.Tx {
	return nil
}

// Notify always returns an error for read-only sessions.
func (r *ReadOnlySession) Notify(ctx context.Context, n ent.Notification) error {
	return fmt.Errorf("read-only session does not support notifications")
}

// A wrapper around a TxProvider that includes a timeout for if it takes to long to call `GetOrBeginTx`.
type TxProviderWithTimeout struct {
	wrapped ent.TxProvider
	timeout time.Duration
}

func NewTxProviderWithTimeout(provider ent.TxProvider, timeout time.Duration) *TxProviderWithTimeout {
	return &TxProviderWithTimeout{
		wrapped: provider,
		timeout: timeout,
	}
}

func (t *TxProviderWithTimeout) GetClient(ctx context.Context) (*ent.Client, error) {
	tx, err := t.GetOrBeginTx(ctx)
	if err != nil {
		return nil, err
	}
	return tx.Client(), nil
}

func (t *TxProviderWithTimeout) GetOrBeginTx(ctx context.Context) (*ent.Tx, error) {
	if t.timeout <= 0 {
		// If the timeout is zero or negative, assume there is no timeout and we should call
		// `GetOrBeginTx` normally.
		return t.wrapped.GetOrBeginTx(ctx)
	}

	type result struct {
		tx  *ent.Tx
		err error
	}

	resultChan := make(chan result)

	logger := logging.GetLoggerFromContext(ctx)

	timeoutCtx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()

	// We can't pass timeoutCtx directly into `NewTx`, because the context we pass into `NewTx` isn't
	// just for starting the transaction. It's also used for the lifetime of the transaction. So throw
	// this into a goroutine so that we can run a select on:
	//
	// 		1. A connection being acquired and a transaction being started successfully.
	//		2. An error occuring while staring the transaction.
	//		3. Neither (1) nor (2) happening within the timeout period.
	//
	// If (3) happens, this function will return an error AND the caller needs to cancel the context in
	// order to stop the transaction process.
	go func() {
		defer close(resultChan)

		tx, err := t.wrapped.GetOrBeginTx(ctx)
		if err != nil {
			select {
			case resultChan <- result{tx: nil, err: err}:
			case <-timeoutCtx.Done():
				logger.Warn("Failed to start transaction within timeout", zap.Error(err))
				return
			}
		}

		select {
		case resultChan <- result{tx: tx, err: nil}:
		case <-timeoutCtx.Done():
			// If the timeout context is done, there are no receivers for the transaction, so we need to
			// rollback the transaction so that we aren't just leaving it idle.
			// Only rollback if tx is not nil (it could be nil if GetOrBeginTx failed)
			if tx == nil {
				logger.Warn("Wanted to rollback transaction after timeout, but tx is nil", zap.Error(err))
			} else {
				err := tx.Rollback()
				if err != nil {
					logger.Warn("Failed to rollback transaction after timeout", zap.Error(err))
				}
			}
			return
		}
	}()

	select {
	case res, ok := <-resultChan:
		if ok {
			return res.tx, res.err
		}
	case <-timeoutCtx.Done():
	}

	if timeoutCtx.Err() == context.DeadlineExceeded {
		return nil, ErrTxBeginTimeout
	}

	return nil, fmt.Errorf("failed to create database transaction %w", timeoutCtx.Err())
}
