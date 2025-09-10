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

func TestTxProviderWithTimeout_Success(t *testing.T) {
	dbClient := NewTestSQLiteClient(t)
	defer dbClient.Close()

	timeout := 5 * time.Second
	provider := NewTxProviderWithTimeout(ent.NewEntClientTxProvider(dbClient), timeout)

	_, err := provider.GetOrBeginTx(t.Context())
	require.NoError(t, err, "Expected to retrieve a transaction within the timeout")
}

func TestTxProviderWithTimeout_Timeout(t *testing.T) {
	t.Parallel()
	timeout := 200 * time.Millisecond
	provider := NewTxProviderWithTimeout(&NeverTxProvider{}, timeout)

	_, err := provider.GetOrBeginTx(t.Context())
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

		return
	}()

	select {
	case <-txChan:
	case <-time.After(1 * time.Second):
		t.Fatal("Timed out waiting for the transaction to be returned.")
	}
}
