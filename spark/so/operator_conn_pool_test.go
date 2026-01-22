package so

import (
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// createTestConnection creates a test grpc.ClientConn that doesn't actually connect anywhere
// but has properly initialized internal state so GetState() works
func createTestConnection(t *testing.T) *grpc.ClientConn {
	// Use a passthrough address that won't actually connect
	// This creates a real ClientConn with all internal state properly initialized
	conn, err := grpc.NewClient(
		"passthrough:///test-address",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultServiceConfig(`{"loadBalancingPolicy":"round_robin"}`),
	)
	if err != nil {
		t.Fatalf("failed to create test connection: %v", err)
	}
	return conn
}

func TestOperatorConnPoolAutoScaleAndRelease(t *testing.T) {
	factoryCalls := atomic.Int32{}
	factory := func() (*grpc.ClientConn, error) {
		factoryCalls.Add(1)
		return createTestConnection(t), nil
	}

	pool := newOperatorConnPool(factory, OperatorConnectionPoolConfig{
		MinConnections:        1,
		MaxConnections:        4,
		UsersPerConnectionCap: 2,
		IdleTimeout:           time.Hour,
		MaxLifetime:           time.Hour,
	}, nil)

	var handles []OperatorClientConn
	for range 3 {
		conn, err := pool.getConnection()
		if err != nil {
			t.Fatalf("getConnection failed: %v", err)
		}
		handles = append(handles, conn)
	}

	// Wait a bit for async autoscaling to complete
	time.Sleep(100 * time.Millisecond)

	// Check that we created at least 2 connections total (1 initial + 1 autoscaled)
	if factoryCalls.Load() < 2 {
		t.Fatalf("expected autoscaling to create >=2 conns, got %d", factoryCalls.Load())
	}

	for _, handle := range handles {
		if err := handle.Close(); err != nil {
			t.Fatalf("close failed: %v", err)
		}
	}

	pool.mu.Lock()
	if len(pool.conns) == 0 {
		pool.mu.Unlock()
		t.Fatalf("expected pooled connections to remain after release")
	}
	pool.mu.Unlock()
}

func TestOperatorConnPoolCleanupAndMinConnections(t *testing.T) {
	factory := func() (*grpc.ClientConn, error) {
		return createTestConnection(t), nil
	}

	p := newOperatorConnPool(factory, OperatorConnectionPoolConfig{
		MinConnections:        2,
		MaxConnections:        2,
		IdleTimeout:           10 * time.Millisecond,
		MaxLifetime:           10 * time.Millisecond,
		UsersPerConnectionCap: 10,
	}, nil)

	pool := p
	pool.mu.Lock()
	for _, pc := range pool.conns {
		pc.lastUsedNanos.Store(time.Now().Add(-time.Minute).UnixNano())
	}
	pool.mu.Unlock()
	pool.cleanupLocked(time.Now())
	pool.mu.Lock()
	if len(pool.conns) != pool.cfg.MinConnections {
		pool.mu.Unlock()
		t.Fatalf("expected min connections to remain, got %d", len(pool.conns))
	}
	pool.mu.Unlock()
}

func TestOperatorConnPoolDifferentTargets(t *testing.T) {
	factoryCalls := atomic.Int32{}
	addresses := []string{"host1", "host2"}
	factory := func() (*grpc.ClientConn, error) {
		factoryCalls.Add(1)
		return createTestConnection(t), nil
	}

	pool := newOperatorConnPool(factory, OperatorConnectionPoolConfig{
		MinConnections:        1,
		MaxConnections:        10,
		UsersPerConnectionCap: 5,
		IdleTimeout:           time.Hour,
		MaxLifetime:           time.Hour,
	}, nil)

	for _, addr := range addresses {
		s := &SigningOperator{AddressRpc: addr, connPoolConfig: pool.cfg}
		s.connPools = map[string]*operatorConnPool{addr: pool}
		conn, err := s.NewOperatorGRPCConnection()
		if err != nil {
			t.Fatalf("failed to get connection: %v", err)
		}
		_ = conn.Close()
	}

	if factoryCalls.Load() == 0 {
		t.Fatalf("expected factory calls for different targets")
	}
}

func TestOperatorConnPoolGracefulClose(t *testing.T) {
	factory := func() (*grpc.ClientConn, error) {
		return createTestConnection(t), nil
	}

	pool := newOperatorConnPool(factory, OperatorConnectionPoolConfig{
		MinConnections:        1,
		MaxConnections:        2,
		UsersPerConnectionCap: 5,
		IdleTimeout:           time.Hour,
		MaxLifetime:           time.Hour,
	}, nil)

	handle, err := pool.getConnection()
	if err != nil {
		t.Fatalf("getConnection failed: %v", err)
	}

	closedCh := make(chan struct{})
	go func() {
		pool.Close()
		close(closedCh)
	}()

	select {
	case <-closedCh:
		t.Fatalf("pool closed before borrower released connection")
	case <-time.After(50 * time.Millisecond):
	}

	if err := handle.Close(); err != nil {
		t.Fatalf("failed to close handle: %v", err)
	}

	select {
	case <-closedCh:
	case <-time.After(time.Second):
		t.Fatalf("pool did not close after borrower released connection")
	}
}
