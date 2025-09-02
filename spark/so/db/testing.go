package db

import (
	"context"
	"net"
	"testing"

	epg "github.com/fergusstrange/embedded-postgres"
	_ "github.com/lib/pq" // postgres driver
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/enttest"
	_ "github.com/mattn/go-sqlite3" // sqlite3 driver
	"github.com/stretchr/testify/require"
)

// TestSessionFactory is a SessionFactory for returning a specific Session, useful for testing.
type TestSessionFactory struct {
	Session *Session
}

func (t *TestSessionFactory) NewSession(ctx context.Context, opts ...SessionOption) *Session {
	return t.Session
}

type TestContext struct {
	t       *testing.T
	Client  *ent.Client
	Session *Session
}

func (tc *TestContext) Close() {
	if tc.Session.currentTx != nil {
		if tc.t.Failed() {
			if err := tc.Session.currentTx.Rollback(); err != nil {
				tc.t.Logf("failed to rollback transaction: %v", err)
			}
		} else {
			if err := tc.Session.currentTx.Commit(); err != nil {
				tc.t.Logf("failed to commit transaction: %v", err)
			}
		}
	}

	if err := tc.Client.Close(); err != nil {
		tc.t.Logf("failed to close client: %v", err)
	}
}

func NewTestContext(
	t *testing.T,
	ctx context.Context,
	driver string,
	path string,
) (context.Context, *TestContext, error) {
	dbClient, err := ent.Open(driver, path)
	if err != nil {
		return nil, nil, err
	}

	dbSession := NewDefaultSessionFactory(dbClient).NewSession(ctx)
	return ent.Inject(ctx, dbSession), &TestContext{t: t, Client: dbClient, Session: dbSession}, nil
}

func NewTestSQLiteContext(
	t *testing.T,
	ctx context.Context,
) (context.Context, *TestContext) {
	dbClient := NewTestSQLiteClient(t)
	session := NewSession(ctx, dbClient)
	return ent.Inject(ctx, session), &TestContext{t: t, Client: dbClient, Session: session}
}

func NewTestSQLiteClient(t *testing.T) *ent.Client {
	return enttest.Open(t, "sqlite3", "file:ent?mode=memory&_fk=1")
}

// SpinUpPostgres starts an ephemeral postgres and returns a DSN and a stop func.
func SpinUpPostgres(t *testing.T) (dsn string, stop func()) {
	// pick a free TCP port for each test
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	tcpAddr, ok := l.Addr().(*net.TCPAddr)
	require.True(t, ok)
	port := tcpAddr.Port
	_ = l.Close()

	// give each test its own runtime dir so parallel runs don't clash
	tmpDir := t.TempDir()

	cfg := epg.DefaultConfig().
		Username("postgres").
		Password("postgres").
		Database("spark_test").
		RuntimePath(tmpDir). // binaries & data
		Port(uint32(port))

	pg := epg.NewDatabase(cfg)
	require.NoError(t, pg.Start())
	return cfg.GetConnectionURL() + "?sslmode=disable", func() { _ = pg.Stop() }
}

// NewPgTestClient opens an ent Client on the given DSN and ensures the schema exists.
func NewPgTestClient(t *testing.T, dsn string) *ent.Client {
	client, err := ent.Open("postgres", dsn)
	require.NoError(t, err)

	return client
}

func NewPgTestContext(t *testing.T, ctx context.Context, dsn string) (context.Context, *TestContext, error) {
	client, err := ent.Open("postgres", dsn)
	require.NoError(t, err)

	err = client.Schema.Create(ctx)
	require.NoError(t, err)

	session := NewSession(ctx, client)
	return ent.Inject(ctx, session), &TestContext{t: t, Client: client, Session: session}, nil
}

// SetUpPostgresTestContext is a convenience helper that combines SpinUpPostgres and NewPgTestContext
// with proper cleanup setup. It returns a context with database session injected and a TestContext
// with automatic cleanup handlers registered.
func SetUpPostgresTestContext(t *testing.T) (context.Context, *TestContext) {
	dsn, stop := SpinUpPostgres(t)
	t.Cleanup(stop)

	ctx, sessionCtx, err := NewPgTestContext(t, t.Context(), dsn)
	require.NoError(t, err)
	t.Cleanup(func() { sessionCtx.Close() })

	return ctx, sessionCtx
}
