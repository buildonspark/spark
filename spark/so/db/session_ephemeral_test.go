package db

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/lightsparkdev/spark/so/entephemeral"
	entephemeraltest "github.com/lightsparkdev/spark/so/entephemeral/enttest"
	"github.com/stretchr/testify/require"
)

type countingEphemeralTxProvider struct {
	client     *entephemeral.Client
	getTxCalls int
}

func (p *countingEphemeralTxProvider) GetOrBeginTx(context.Context) (*entephemeral.Tx, error) {
	p.getTxCalls++
	return nil, fmt.Errorf("unexpected call to GetOrBeginTx")
}

func (p *countingEphemeralTxProvider) GetClient(context.Context) (*entephemeral.Client, error) {
	return p.client, nil
}

func TestEphemeralSession_GetClientDoesNotBeginTx(t *testing.T) {
	dbClient := entephemeraltest.Open(t, "sqlite3", "file:entephemeral?mode=memory&_fk=1")
	defer func() {
		require.NoError(t, dbClient.Close())
	}()

	session := NewDefaultEphemeralSessionFactory(dbClient).NewSession(t.Context())

	client, err := session.GetClient(t.Context())
	require.NoError(t, err)
	require.Equal(t, dbClient, client)
	require.Nil(t, session.GetTxIfExists())
}

func TestEphemeralSession_GetClientReturnsTxClientWhenTxExists(t *testing.T) {
	dbClient := entephemeraltest.Open(t, "sqlite3", "file:entephemeral?mode=memory&_fk=1")
	defer func() {
		require.NoError(t, dbClient.Close())
	}()

	session := NewDefaultEphemeralSessionFactory(dbClient).NewSession(t.Context())

	tx, err := session.GetOrBeginTx(t.Context())
	require.NoError(t, err)
	defer func() {
		require.NoError(t, tx.Rollback())
	}()

	client, err := session.GetClient(t.Context())
	require.NoError(t, err)
	require.Equal(t, tx.Client(), client)
	require.Equal(t, tx, session.GetTxIfExists())
}

func TestEphemeralTxProviderWithTimeout_GetClientDoesNotBeginTx(t *testing.T) {
	dbClient := entephemeraltest.Open(t, "sqlite3", "file:entephemeral?mode=memory&_fk=1")
	defer func() {
		require.NoError(t, dbClient.Close())
	}()

	wrapped := &countingEphemeralTxProvider{client: dbClient}
	provider := NewEphemeralTxProviderWithTimeout(wrapped, time.Second)

	client, err := provider.GetClient(t.Context())
	require.NoError(t, err)
	require.Equal(t, dbClient, client)
	require.Equal(t, 0, wrapped.getTxCalls)
}

func TestEphemeralSession_GetTxIfExistsReturnsSameTxAfterFailedCommit(t *testing.T) {
	dbClient := entephemeraltest.Open(t, "sqlite3", "file:entephemeral?mode=memory&_fk=1")
	defer func() {
		require.NoError(t, dbClient.Close())
	}()

	session := NewDefaultEphemeralSessionFactory(dbClient).NewSession(t.Context())

	tx, err := session.GetOrBeginTx(t.Context())
	require.NoError(t, err)
	defer func() {
		_ = tx.Rollback()
	}()

	tx.OnCommit(func(fn entephemeral.Committer) entephemeral.Committer {
		return entephemeral.CommitFunc(func(ctx context.Context, tx *entephemeral.Tx) error {
			return fmt.Errorf("forced commit failure")
		})
	})

	err = tx.Commit()
	require.Error(t, err)

	// currentTx must be preserved so that middleware can still issue a rollback via DbRollback.
	require.Equal(t, tx, session.GetTxIfExists())
	// CommitError lets middleware distinguish a failed commit from a handler that never committed.
	require.ErrorContains(t, session.CommitError(), "forced commit failure")
	// The original tx reference must still be rollback-able.
	require.NoError(t, tx.Rollback())
}

func TestEphemeralSession_GetOrBeginTxCancelledContextReturnsExistingTx(t *testing.T) {
	dbClient := entephemeraltest.Open(t, "sqlite3", "file:entephemeral?mode=memory&_fk=1")
	defer func() {
		require.NoError(t, dbClient.Close())
	}()

	session := NewDefaultEphemeralSessionFactory(dbClient).NewSession(t.Context())

	tx, err := session.GetOrBeginTx(t.Context())
	require.NoError(t, err)
	defer func() {
		require.NoError(t, tx.Rollback())
	}()

	canceledCtx, cancel := context.WithCancel(t.Context())
	cancel()

	gotTx, gotErr := session.GetOrBeginTx(canceledCtx)
	require.NoError(t, gotErr)
	require.Equal(t, tx, gotTx)
}
