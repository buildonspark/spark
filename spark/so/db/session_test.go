package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/lightsparkdev/spark/so/ent"
	"github.com/stretchr/testify/require"
)

// A TxProvider that never returns a transaction.
type NeverTxProvider struct{}

func (p *NeverTxProvider) GetOrBeginTx(ctx context.Context) (*ent.Tx, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (p *NeverTxProvider) GetClient(ctx context.Context) (*ent.Client, error) {
	tx, err := p.GetOrBeginTx(ctx)
	if err != nil {
		return nil, err
	}
	return tx.Client(), nil
}

// A TxProvider that simulates a slow transaction provider that waits for an external trigger before
// returning a transaction.
type SlowTxProvider struct {
	tx      *ent.Tx
	trigger <-chan struct{}
}

func (p *SlowTxProvider) GetOrBeginTx(ctx context.Context) (*ent.Tx, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.trigger:
		return p.tx, nil
	}
}

func (p *SlowTxProvider) GetClient(ctx context.Context) (*ent.Client, error) {
	tx, err := p.GetOrBeginTx(ctx)
	if err != nil {
		return nil, err
	}
	return tx.Client(), nil
}

func TestSession_GetOrBeginTxReturnsSameTx(t *testing.T) {
	dbClient := NewTestSQLiteClient(t)
	defer dbClient.Close()

	session := NewDefaultSessionFactory(dbClient).NewSession(t.Context())

	tx1, err := session.GetOrBeginTx(t.Context())
	require.NoError(t, err, "Expected to retrieve a transaction")

	tx2, err := session.GetOrBeginTx(t.Context())
	require.NoError(t, err, "Expected to retrieve the same transaction")

	require.Equal(t, tx1, tx2, "Expected both transactions to be the same")
}

func TestSession_GetCurrentTxReturnsNilWithNoTx(t *testing.T) {
	dbClient := NewTestSQLiteClient(t)
	defer dbClient.Close()

	session := NewDefaultSessionFactory(dbClient).NewSession(t.Context())

	tx := session.GetTxIfExists()
	require.Nil(t, tx, "Expected no current transaction to exist")
}

func TestSession_GetCurrentTxReturnsNilAfterSuccessfulCommit(t *testing.T) {
	dbClient := NewTestSQLiteClient(t)
	defer dbClient.Close()

	session := NewDefaultSessionFactory(dbClient).NewSession(t.Context())

	tx, err := session.GetOrBeginTx(t.Context())
	require.NoError(t, err, "Expected to retrieve a transaction")

	err = tx.Commit()
	require.NoError(t, err, "Expected to commit the transaction successfully")

	currentTx := session.GetTxIfExists()
	require.Nil(t, currentTx, "Expected no current transaction to exist after commit")
}

func TestSession_CleanCommitClearsCurrentTx(t *testing.T) {
	dbClient := NewTestSQLiteClient(t)
	defer dbClient.Close()

	session := NewDefaultSessionFactory(dbClient).NewSession(t.Context())

	tx, err := session.GetOrBeginTx(t.Context())
	require.NoError(t, err, "Expected to retrieve a transaction")

	commitCalled := false
	tx.OnCommit(func(fn ent.Committer) ent.Committer {
		return ent.CommitFunc(func(ctx context.Context, tx *ent.Tx) error {
			commitCalled = true
			return fn.Commit(ctx, tx)
		})
	})

	err = tx.Commit()
	require.NoError(t, err, "Expected commit of a clean transaction to succeed")
	require.True(t, commitCalled, "Expected the commit hook to fire for a clean transaction")
	require.Nil(t, session.GetTxIfExists(), "Expected no current transaction to exist after commit")
}

func TestSession_GetCurrentTxReturnsNilAfterSuccessfulRollback(t *testing.T) {
	dbClient := NewTestSQLiteClient(t)
	defer dbClient.Close()

	session := NewDefaultSessionFactory(dbClient).NewSession(t.Context())

	tx, err := session.GetOrBeginTx(t.Context())
	require.NoError(t, err, "Expected to retrieve a transaction")

	err = tx.Rollback()
	require.NoError(t, err, "Expected to rollback the transaction successfully")

	currentTx := session.GetTxIfExists()
	require.Nil(t, currentTx, "Expected no current transaction to exist after rollback")
}

func TestSession_GetCurrrentTxReturnsSameTxAfterFailedCommit(t *testing.T) {
	dbClient := NewTestSQLiteClient(t)
	defer dbClient.Close()

	session := NewDefaultSessionFactory(dbClient).NewSession(t.Context())

	tx, err := session.GetOrBeginTx(t.Context())
	require.NoError(t, err, "Expected to retrieve a transaction")

	tx.OnCommit(func(fn ent.Committer) ent.Committer {
		return ent.CommitFunc(func(ctx context.Context, tx *ent.Tx) error {
			return fmt.Errorf("commit failed because you asked it to")
		})
	})

	err = tx.Commit()
	require.Error(t, err, "Expected commit to fail")

	currentTx := session.GetTxIfExists()
	require.Equal(t, tx, currentTx, "Expected current transaction to be the same after failed commit")
}

func TestSession_GetCurrrentTxReturnsSameTxAfterFailedRollback(t *testing.T) {
	dbClient := NewTestSQLiteClient(t)
	defer dbClient.Close()

	session := NewDefaultSessionFactory(dbClient).NewSession(t.Context())

	tx, err := session.GetOrBeginTx(t.Context())
	require.NoError(t, err, "Expected to retrieve a transaction")

	tx.OnRollback(func(fn ent.Rollbacker) ent.Rollbacker {
		return ent.RollbackFunc(func(ctx context.Context, tx *ent.Tx) error {
			return fmt.Errorf("rollback failed because you asked it to")
		})
	})

	err = tx.Rollback()
	require.Error(t, err, "Expected rollback to fail")

	currentTx := session.GetTxIfExists()
	require.Nil(t, currentTx, "Expected current transaction to be nil after failed rollback")
}

func TestSession_GetOrBeginTxCommitAfterCancelledTransactionContext(t *testing.T) {
	dbClient := NewTestSQLiteClient(t)
	defer dbClient.Close()

	session := NewDefaultSessionFactory(dbClient).NewSession(t.Context())

	innerCtx, innerCancel := context.WithCancel(t.Context())

	tx, err := session.GetOrBeginTx(innerCtx)
	require.NoError(t, err, "Expected to retrieve a transaction")

	// Cancel the inner context. The transaction should still be valid.
	innerCancel()

	err = tx.Commit()
	require.NoError(t, err, "Expected to commit the transaction successfully after inner context cancellation")
}

func TestSession_GetOrBeginTxCommitAfterCancelledSessionContext(t *testing.T) {
	dbClient := NewTestSQLiteClient(t)
	defer dbClient.Close()

	sessionCtx, sessionCancel := context.WithCancel(t.Context())
	session := NewDefaultSessionFactory(dbClient).NewSession(sessionCtx)

	tx, err := session.GetOrBeginTx(t.Context())
	require.NoError(t, err, "Expected to retrieve a transaction")

	// Cancel the session context. The transaction should throw an error.
	sessionCancel()

	err = tx.Commit()
	require.Error(t, err, "Expected commit to fail after session context cancellation")
	require.True(t, errors.Is(err, context.Canceled) || errors.Is(err, sql.ErrTxDone))

	// Also make sure we don't hang on to that transaction.
	currentTx := session.GetTxIfExists()
	require.Nil(t, currentTx, "Expected no current transaction to exist after session context cancellation")
}

func TestSession_GetOrBeginTxRollbackAfterCancelledTransactionContext(t *testing.T) {
	dbClient := NewTestSQLiteClient(t)
	defer dbClient.Close()

	session := NewDefaultSessionFactory(dbClient).NewSession(t.Context())

	innerCtx, innerCancel := context.WithCancel(t.Context())

	tx, err := session.GetOrBeginTx(innerCtx)
	require.NoError(t, err, "Expected to retrieve a transaction")

	// Cancel the inner context. The transaction should still be valid.
	innerCancel()

	err = tx.Rollback()
	require.NoError(t, err, "Expected to rollback the transaction successfully after inner context cancellation")
}

func TestSession_GetOrBeginTxRollbackAfterCancelledSessionContext(t *testing.T) {
	dbClient := NewTestSQLiteClient(t)
	defer dbClient.Close()

	sessionCtx, sessionCancel := context.WithCancel(t.Context())
	session := NewDefaultSessionFactory(dbClient).NewSession(sessionCtx)

	tx, err := session.GetOrBeginTx(t.Context())
	require.NoError(t, err, "Expected to retrieve a transaction")

	// Cancel the session context.
	sessionCancel()

	err = tx.Rollback()
	require.True(t, err == nil || errors.Is(err, context.Canceled) || errors.Is(err, sql.ErrTxDone))

	// Also make sure we don't hang on to that transaction.
	currentTx := session.GetTxIfExists()
	require.Nil(t, currentTx, "Expected no current transaction to exist after session context cancellation")
}

func TestSession_GetOrBeginTxReturnsNewTxAfterCommit(t *testing.T) {
	dbClient := NewTestSQLiteClient(t)
	defer dbClient.Close()

	session := NewDefaultSessionFactory(dbClient).NewSession(t.Context())

	tx1, err := session.GetOrBeginTx(t.Context())
	require.NoError(t, err, "Expected to retrieve a transaction")

	err = tx1.Commit()
	require.NoError(t, err, "Expected to commit the transaction successfully")

	tx2, err := session.GetOrBeginTx(t.Context())
	require.NoError(t, err, "Expected to retrieve a new transaction after commit")
	require.NotEqual(t, tx1, tx2, "Expected a new transaction after the previous one was committed")
}

func TestTxProviderWithTimeout_Success(t *testing.T) {
	dbClient := NewTestSQLiteClient(t)
	defer dbClient.Close()

	timeout := 5 * time.Second
	provider := NewTxProviderWithTimeout(ent.NewEntClientTxProvider(dbClient), timeout)

	_, err := provider.GetOrBeginTx(t.Context())
	require.NoError(t, err, "Expected to retrieve a transaction within the timeout")
}

// contextSpyProvider wraps a TxProvider and captures the context passed to GetOrBeginTx.
// This lets tests inspect beginCtx — the child context that getOrBeginTxWithTimeout creates
// and passes to the underlying Begin call.
type contextSpyProvider struct {
	wrapped     ent.TxProvider
	capturedCtx context.Context
}

func (p *contextSpyProvider) GetOrBeginTx(ctx context.Context) (*ent.Tx, error) {
	p.capturedCtx = ctx
	return p.wrapped.GetOrBeginTx(ctx)
}

func (p *contextSpyProvider) GetClient(ctx context.Context) (*ent.Client, error) {
	return p.wrapped.GetClient(ctx)
}

// TestTxProviderWithTimeout_BeginCtxSurvivesCommit verifies that beginCtx — the context
// stored by pgx/stdlib in wrapTx.ctx for all connection operations — is NOT cancelled by
// the commit hook. Cancelling it early interacts with database/sql's awaitDone goroutine
// under discardConn=true semantics, causing connection churn.
//
// This test would have failed with the original #5640 onTxStarted implementation that
// called defer cancel() from the commit hook.
func TestTxProviderWithTimeout_BeginCtxSurvivesCommit(t *testing.T) {
	t.Parallel()
	dbClient := NewTestSQLiteClient(t)
	defer dbClient.Close()

	spy := &contextSpyProvider{wrapped: ent.NewEntClientTxProvider(dbClient)}
	provider := NewTxProviderWithTimeout(spy, 5*time.Second)

	tx, err := provider.GetOrBeginTx(t.Context())
	require.NoError(t, err)
	require.NotNil(t, spy.capturedCtx, "begin must have been called")
	require.NoError(t, spy.capturedCtx.Err(), "beginCtx should be active before commit")

	require.NoError(t, tx.Commit())

	require.NoError(t, spy.capturedCtx.Err(), "beginCtx must not be cancelled by the commit hook")
}

// TestTxProviderWithTimeout_BeginCtxSurvivesRollback is the rollback counterpart of
// TestTxProviderWithTimeout_BeginCtxSurvivesCommit.
func TestTxProviderWithTimeout_BeginCtxSurvivesRollback(t *testing.T) {
	t.Parallel()
	dbClient := NewTestSQLiteClient(t)
	defer dbClient.Close()

	spy := &contextSpyProvider{wrapped: ent.NewEntClientTxProvider(dbClient)}
	provider := NewTxProviderWithTimeout(spy, 5*time.Second)

	tx, err := provider.GetOrBeginTx(t.Context())
	require.NoError(t, err)
	require.NotNil(t, spy.capturedCtx)
	require.NoError(t, spy.capturedCtx.Err(), "beginCtx should be active before rollback")

	require.NoError(t, tx.Rollback())

	require.NoError(t, spy.capturedCtx.Err(), "beginCtx must not be cancelled by the rollback hook")
}

func TestTxProviderWithTimeout_Timeout(t *testing.T) {
	t.Parallel()
	timeout := 200 * time.Millisecond
	provider := NewTxProviderWithTimeout(&NeverTxProvider{}, timeout)

	_, err := provider.GetOrBeginTx(t.Context())
	require.ErrorIs(t, err, ErrTxBeginTimeout)
}

func TestTxProviderWithTimeout_GetClientUsesTxTimeout(t *testing.T) {
	t.Parallel()
	timeout := 200 * time.Millisecond
	provider := NewTxProviderWithTimeout(&NeverTxProvider{}, timeout)

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	_, err := provider.GetClient(ctx)
	require.ErrorIs(t, err, ErrTxBeginTimeout)
}

func TestTxProviderWithTimeout_SlowProvider(t *testing.T) {
	t.Parallel()
	dbClient := NewTestSQLiteClient(t)
	defer dbClient.Close()

	tx, err := dbClient.Tx(t.Context())
	require.NoError(t, err, "Failed to create a transaction")

	rollback := make(chan struct{})
	defer close(rollback)

	tx.OnRollback(func(rollbacker ent.Rollbacker) ent.Rollbacker {
		rollback <- struct{}{}
		return rollbacker
	})

	trigger := make(chan struct{})
	defer close(trigger)

	timeout := 200 * time.Millisecond
	provider := NewTxProviderWithTimeout(&SlowTxProvider{tx: tx, trigger: trigger}, timeout)

	_, err = provider.GetOrBeginTx(t.Context())
	require.ErrorIs(t, err, ErrTxBeginTimeout)

	// Now have the slow provider return the transaction.
	select {
	case trigger <- struct{}{}:
	case <-time.After(1 * time.Second):
		t.Fatal("Timed out waiting for the slow provider to trigger")
	}

	select {
	case <-rollback:
	case <-time.After(1 * time.Second):
		t.Fatal("Timed out waiting for the rollback to complete")
	}
}

func TestTxProviderWithTimeout_NoTimeout(t *testing.T) {
	t.Parallel()
	dbClient := NewTestSQLiteClient(t)
	defer dbClient.Close()

	tx, err := dbClient.Tx(t.Context())
	require.NoError(t, err, "Failed to create a transaction")

	trigger := make(chan struct{})
	defer close(trigger)

	txChan := make(chan *ent.Tx)
	defer close(txChan)

	timeout := 0 * time.Second
	provider := NewTxProviderWithTimeout(&SlowTxProvider{tx: tx, trigger: trigger}, timeout)

	go func() {
		tx, err := provider.GetOrBeginTx(t.Context())
		if err != nil {
			return
		}

		select {
		case txChan <- tx:
		case <-t.Context().Done():
		}
	}()

	go func() {
		time.Sleep(200 * time.Millisecond)

		select {
		case trigger <- struct{}{}:
		case <-t.Context().Done():
		}
	}()

	select {
	case <-txChan:
	case <-time.After(1 * time.Second):
		t.Fatal("Timed out waiting for the transaction to be returned.")
	}
}

func TestReadOnlySession_GetClient(t *testing.T) {
	dbClient := NewTestSQLiteClient(t)
	defer dbClient.Close()

	session := NewReadOnlySession(t.Context(), dbClient)

	// GetClient should work fine
	client, err := session.GetClient(t.Context())
	require.NoError(t, err)
	require.NotNil(t, client)
	require.Equal(t, dbClient, client)
}

func TestReadOnlySession_GetOrBeginTxErrors(t *testing.T) {
	dbClient := NewTestSQLiteClient(t)
	defer dbClient.Close()

	session := NewReadOnlySession(t.Context(), dbClient)

	// GetOrBeginTx should return an error
	tx, err := session.GetOrBeginTx(t.Context())
	require.Error(t, err)
	require.Nil(t, tx)
	require.Contains(t, err.Error(), "read-only session does not support")
}

func TestReadOnlySession_GetTxIfExists(t *testing.T) {
	dbClient := NewTestSQLiteClient(t)
	defer dbClient.Close()

	session := NewReadOnlySession(t.Context(), dbClient)

	// GetTxIfExists should always return nil
	tx := session.GetTxIfExists()
	require.Nil(t, tx)
}
