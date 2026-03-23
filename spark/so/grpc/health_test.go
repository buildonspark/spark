package grpc

import (
	"context"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/lightsparkdev/spark/so/db"
	entephemeraltest "github.com/lightsparkdev/spark/so/entephemeral/enttest"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/health/grpc_health_v1"
)

func TestNewHealthServer_AllowsNilEphemeralClient(t *testing.T) {
	ctx := t.Context()
	dbClient := db.NewTestSQLiteClient(t)

	healthServer := NewHealthServer(ctx, dbClient, nil)

	require.Eventually(t, func() bool {
		resp, err := healthServer.Check(ctx, &grpc_health_v1.HealthCheckRequest{Service: readinessService})
		return err == nil && resp.GetStatus() == grpc_health_v1.HealthCheckResponse_SERVING
	}, 5*time.Second, 50*time.Millisecond)
}

func TestNewHealthServer_BothClientsReady(t *testing.T) {
	ctx := t.Context()
	mainClient := db.NewTestSQLiteClient(t)
	ephemeralClient := entephemeraltest.Open(t, "sqlite3", fmt.Sprintf("file:%s?mode=memory&_fk=1", url.PathEscape(t.Name())))
	defer func() { _ = ephemeralClient.Close() }()

	healthServer := NewHealthServer(ctx, mainClient, ephemeralClient)

	require.Eventually(t, func() bool {
		resp, err := healthServer.Check(ctx, &grpc_health_v1.HealthCheckRequest{Service: readinessService})
		return err == nil && resp.GetStatus() == grpc_health_v1.HealthCheckResponse_SERVING
	}, 5*time.Second, 50*time.Millisecond)
}

func TestWaitForDatabasesReady_RunsChecksConcurrently(t *testing.T) {
	mainStarted := make(chan struct{}, 1)
	ephemeralStarted := make(chan struct{}, 1)
	unblock := make(chan struct{})
	result := make(chan error, 1)

	go func() {
		result <- waitForDatabasesReady(
			t.Context(),
			func(context.Context) error {
				mainStarted <- struct{}{}
				<-unblock
				return nil
			},
			func(context.Context) error {
				ephemeralStarted <- struct{}{}
				<-unblock
				return nil
			},
		)
	}()

	select {
	case <-mainStarted:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("main readiness check did not start")
	}

	select {
	case <-ephemeralStarted:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("ephemeral readiness check did not start in parallel")
	}

	close(unblock)
	require.NoError(t, <-result)
}

type blockedTx struct{}

func (blockedTx) Rollback() error {
	return nil
}

type blockedDBClient struct{}

func (blockedDBClient) Tx(ctx context.Context) (blockedTx, error) {
	<-ctx.Done()
	return blockedTx{}, ctx.Err()
}

func TestWaitForDatabaseReady_ReturnsErrorWhenContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := waitForDatabaseReady(ctx, "test", blockedDBClient{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "test database readiness check canceled")
	require.ErrorIs(t, err, context.Canceled)
}
