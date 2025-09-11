package db

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	epg "github.com/fergusstrange/embedded-postgres"
	_ "github.com/jackc/pgx/v5/stdlib" // postgres driver
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/enttest"
	"github.com/lightsparkdev/spark/so/knobs"
	sparktesting "github.com/lightsparkdev/spark/testing"
	_ "github.com/mattn/go-sqlite3" // sqlite3 driver
	"github.com/peterldowns/pgtestdb"
	"github.com/peterldowns/pgtestdb/migrators/atlasmigrator"
	"github.com/stretchr/testify/require"
)

// TestSessionFactory is a SessionFactory for returning a specific Session, useful for testing.
type TestSessionFactory struct {
	Session *Session
}

func (t *TestSessionFactory) NewSession(_ context.Context, _ ...SessionOption) *Session {
	return t.Session
}

type TestContext struct {
	t            testing.TB
	Client       *ent.Client
	Session      *Session
	databasePath string
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

func NewTestContext(t *testing.T, ctx context.Context, driver string, path string) (context.Context, *TestContext, error) {
	dbClient, err := ent.Open(driver, path)
	if err != nil {
		return nil, nil, err
	}

	dbSession := NewDefaultSessionFactory(dbClient).NewSession(ctx)
	return ent.Inject(ctx, dbSession), &TestContext{t: t, Client: dbClient, Session: dbSession, databasePath: path}, nil
}

const sqlitePath = "file:ent?mode=memory&_fk=1"

func NewTestSQLiteContext(t *testing.T, ctx context.Context) (context.Context, *TestContext) {
	dbClient := NewTestSQLiteClient(t)
	session := NewSession(ctx, dbClient)
	return ent.Inject(ctx, session), &TestContext{t: t, Client: dbClient, Session: session, databasePath: sqlitePath}
}

func NewTestSQLiteClient(t *testing.T) *ent.Client {
	return enttest.Open(t, "sqlite3", sqlitePath)
}

var postgresPort string

// StartPostgresServer starts an ephemeral postgres server and returns a stop func.
// This is meant to be called in a TestMain function, like so:
//
//	func TestMain(m *testing.M) {
//		stop := db.StartPostgresServer()
//		defer stop()
//
//		m.Run()
//	}
//
// Then, in your actual tests, call [ConnectToTestPostgres].
func StartPostgresServer() (stop func()) {
	if !sparktesting.PostgresTestsEnabled() {
		return func() {}
	}
	port, err := findFreePort()
	if err != nil {
		panic(err)
	}
	postgresPort = strconv.Itoa(port)
	tmpDir, err := os.MkdirTemp("/tmp", postgresPort)
	if err != nil {
		panic(fmt.Errorf("failed to create temp dir: %w", err))
	}

	cfg := epg.DefaultConfig().
		Username("postgres").
		Password("postgres").
		Database("spark_test").
		RuntimePath(tmpDir). // binaries & data
		Port(uint32(port)).
		StartParameters(map[string]string{"fsync": "off"})

	pg := epg.NewDatabase(cfg)
	if err := pg.Start(); err != nil {
		panic(fmt.Errorf("unable to start postgres DB: %w", err))
	}
	return func() { _ = pg.Stop() }
}

func findFreePort() (port int, err error) {
	if a, err := net.ResolveTCPAddr("tcp", "localhost:0"); err == nil {
		if l, err := net.ListenTCP("tcp", a); err == nil {
			defer func() { _ = l.Close() }()
			tcpAddr, _ := l.Addr().(*net.TCPAddr) // This is guaranteed to be a TCPAddr by the spec
			return tcpAddr.Port, nil
		}
	}
	return 0, fmt.Errorf("failed to find a free port")
}

// ConnectToTestPostgres is a helper that returns an open connection to a unique and isolated
// test database, fully migrated and ready for you to query. There's no need to manually close the returned values.
func ConnectToTestPostgres(t testing.TB) (context.Context, *TestContext) {
	t.Helper()
	if !sparktesting.PostgresTestsEnabled() {
		t.Skipf("skipping %s because it's a Postgres test and SKIP_POSTGRES_TESTS is true", t.Name())
		return nil, nil
	}
	conf := pgtestdb.Config{
		DriverName:                "pgx",
		User:                      "postgres",
		Password:                  "postgres",
		Host:                      "localhost",
		Database:                  "spark_test",
		Port:                      postgresPort,
		Options:                   "sslmode=disable",
		ForceTerminateConnections: true,
	}

	// We have to find the module root in order to get the path to the migrations folder, since the working directory
	// is based on the file running the test, not this file.
	migrator := atlasmigrator.NewDirMigrator(moduleRoot)
	dbConn := pgtestdb.Custom(t, conf, migrator)
	require.NotNil(t, dbConn)

	client, err := connectEntToPostgres(dbConn.URL())
	require.NoError(t, err)

	ctx := t.Context()
	session := NewSession(ctx, client)
	tc := &TestContext{t: t, Client: client, Session: session, databasePath: dbConn.URL()}
	t.Cleanup(tc.Close)
	return ent.Inject(ctx, session), tc
}

var moduleRoot = filepath.Join(findModuleRoot(), "so/ent/migrate/migrations")

// findModuleRoot finds the absolute path to the root of the current module.
// It's based on the findModuleRoot function from the Go stdlib:
// https://github.com/golang/go/blob/9e3b1d53a012e98cfd02de2de8b1bd53522464d4/src/cmd/go/internal/modload/init.go#L1504
func findModuleRoot() string {
	wd, err := os.Getwd()
	if err != nil {
		panic(fmt.Sprintf("unable to get current directory: %v", err))
	}
	dir := filepath.Clean(wd)

	// Look for enclosing go.mod.
	for {
		if fi, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil && !fi.IsDir() {
			return dir
		}
		d := filepath.Dir(dir)
		if d == dir {
			break
		}
		dir = d
	}
	return ""
}

// NewPostgresEntClientForIntegrationTest creates a new ent client connecting to the Postgres DB at the given URI.
// Non-integration tests should use [StartPostgresServer] and [ConnectToTestPostgres].
func NewPostgresEntClientForIntegrationTest(t testing.TB, databaseURI string) *ent.Client {
	var err error
	for i := range 3 {
		entClient, err := connectEntToPostgres(databaseURI)
		if err == nil {
			return entClient
		}
		t.Logf("failed to connect to postgres database; attempt %d/3: %v", i, err)
		time.Sleep(1 * time.Second)
	}
	require.NoError(t, err, "failed to connect to database")
	return nil
}

func connectEntToPostgres(databaseURI string) (*ent.Client, error) {
	db, err := sql.Open("pgx", databaseURI)
	if err != nil {
		return nil, err
	}
	drv := entsql.OpenDB(dialect.Postgres, db)
	return ent.NewClient(ent.Driver(drv)), nil
}

// SetUpDBEventsTestContext creates a complete test environment with PostgreSQL, DBConnector, and DBEvents
// ready for testing. It returns all the necessary components.
func SetUpDBEventsTestContext(t *testing.T) (*TestContext, *so.DBConnector, *DBEvents) {
	t.Helper()
	ctx, sessionCtx := ConnectToTestPostgres(t)

	config := &so.Config{
		DatabasePath: sessionCtx.databasePath,
	}

	knobsService := knobs.New(nil)

	connector, err := so.NewDBConnector(ctx, config, knobsService)
	require.NoError(t, err)

	logger := slog.Default().With("component", "dbevents")
	dbEvents, err := NewDBEvents(t.Context(), connector, logger)
	require.NoError(t, err)

	go func() {
		if err := dbEvents.Start(); err != nil {
			t.Errorf("failed to start db events: %v", err)
		}
	}()

	t.Cleanup(connector.Close)
	return sessionCtx, connector, dbEvents
}
