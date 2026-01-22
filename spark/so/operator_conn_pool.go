package so

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
)

// Default configuration for operator connection pools
var defaultPoolConfig = OperatorConnectionPoolConfig{
	MinConnections:        5,
	MaxConnections:        100,
	IdleTimeout:           2 * time.Minute,
	MaxLifetime:           30 * time.Minute,
	UsersPerConnectionCap: 50,
	ScaleConcurrency:      3,
}

const operatorConnPoolGracefulCloseTimeout = 5 * time.Second

var (
	errOperatorConnPoolClosing        = errors.New("operator connection pool is closing")
	operatorConnDialFailureCounter    metric.Int64Counter
	operatorConnPoolExhaustionCounter metric.Int64Counter
)

func init() {
	meter := otel.GetMeterProvider().Meter("spark.so.operator_conn_pool")

	// Helper to create counter with fallback to noop on error
	createCounter := func(name, desc string) metric.Int64Counter {
		counter, err := meter.Int64Counter(name, metric.WithDescription(desc))
		if err != nil {
			otel.Handle(err)
			return noop.Int64Counter{}
		}
		return counter
	}

	operatorConnDialFailureCounter = createCounter(
		"operator_conn_pool_dial_failures_total",
		"Number of failed operator connection pool dial attempts",
	)

	operatorConnPoolExhaustionCounter = createCounter(
		"operator_conn_pool_exhaustions_total",
		"Number of times the operator connection pool was exhausted",
	)
}

func (p *operatorConnPool) recordDialFailure(reason string, err error) {
	// Only use 'reason' as a label since it has low cardinality (prewarm, select, scale)
	// Don't include error message as it would blow up cardinality
	operatorConnDialFailureCounter.Add(context.Background(), 1, metric.WithAttributes(
		attribute.String("reason", reason),
	))
	p.logger.Warn("operator connection pool dial failed", zap.String("reason", reason), zap.Error(err))
}

func (p *operatorConnPool) recordPoolExhausted(current, maxConnections int) {
	// Don't include current/max as labels - they're high cardinality
	// Just increment the counter; current/max should be separate gauges if needed
	operatorConnPoolExhaustionCounter.Add(context.Background(), 1)
	p.logger.Error("operator connection pool exhausted",
		zap.Int("current_connections", current),
		zap.Int("max_connections", maxConnections),
	)
}

// DefaultOperatorConnPoolConfig returns the default pool configuration.
func DefaultOperatorConnPoolConfig() OperatorConnectionPoolConfig {
	return defaultPoolConfig
}

// OperatorConnectionPoolConfig contains all tunables for the operator pool.
type OperatorConnectionPoolConfig struct {
	MinConnections        int
	MaxConnections        int
	IdleTimeout           time.Duration
	MaxLifetime           time.Duration
	UsersPerConnectionCap int
	ScaleConcurrency      int
}

// Equal checks whether two configs are identical after defaults/normalization.
func (c OperatorConnectionPoolConfig) Equal(other OperatorConnectionPoolConfig) bool {
	a := c.WithDefaults()
	b := other.WithDefaults()
	return a.MinConnections == b.MinConnections &&
		a.MaxConnections == b.MaxConnections &&
		a.IdleTimeout == b.IdleTimeout &&
		a.MaxLifetime == b.MaxLifetime &&
		a.UsersPerConnectionCap == b.UsersPerConnectionCap &&
		a.ScaleConcurrency == b.ScaleConcurrency
}

// WithDefaults returns a copy of the config with any zero values replaced with defaults.
func (c OperatorConnectionPoolConfig) WithDefaults() OperatorConnectionPoolConfig {
	// Apply defaults for zero values
	if c.IdleTimeout <= 0 {
		c.IdleTimeout = defaultPoolConfig.IdleTimeout
	}
	if c.MaxLifetime <= 0 {
		c.MaxLifetime = defaultPoolConfig.MaxLifetime
	}
	if c.UsersPerConnectionCap <= 0 {
		c.UsersPerConnectionCap = defaultPoolConfig.UsersPerConnectionCap
	}
	if c.ScaleConcurrency <= 0 {
		c.ScaleConcurrency = defaultPoolConfig.ScaleConcurrency
	}

	// Ensure valid min/max relationship
	if c.MaxConnections <= 0 {
		c.MaxConnections = 1
	}
	if c.MinConnections < 0 {
		c.MinConnections = 0
	}
	if c.MinConnections > c.MaxConnections {
		c.MinConnections = c.MaxConnections
	}

	return c
}

// operatorConnPool maintains a set of gRPC connections for operator requests.
type operatorConnPool struct {
	cfg     OperatorConnectionPoolConfig
	factory func() (*grpc.ClientConn, error)
	logger  *zap.Logger

	mu           sync.Mutex
	conns        []*pooledConn
	scaleLimiter chan struct{}
	closing      bool
}

// pooledConn wraps a gRPC client connection with metadata required by the pool.
type pooledConn struct {
	conn          *grpc.ClientConn
	createdAt     time.Time
	lastUsedNanos atomic.Int64
	usage         atomic.Int32
}

func newPooledConn(conn *grpc.ClientConn, now time.Time) *pooledConn {
	pc := &pooledConn{
		conn:      conn,
		createdAt: now,
	}
	pc.lastUsedNanos.Store(now.UnixNano())
	return pc
}

// newOperatorConnPool constructs a pool and pre-warms the minimum connections.
func newOperatorConnPool(factory func() (*grpc.ClientConn, error), cfg OperatorConnectionPoolConfig, logger *zap.Logger) *operatorConnPool {
	cfg = cfg.WithDefaults()
	if logger == nil {
		logger = zap.NewNop()
	}
	pool := &operatorConnPool{
		cfg:          cfg,
		factory:      factory,
		logger:       logger,
		scaleLimiter: make(chan struct{}, cfg.ScaleConcurrency),
	}

	now := time.Now()
	for range cfg.MinConnections {
		conn, err := factory()
		if err != nil {
			pool.recordDialFailure("prewarm", err)
			break
		}
		pool.conns = append(pool.conns, newPooledConn(conn, now))
	}

	return pool
}

// getConnection returns a pooled connection handle implementing OperatorClientConn.
func (p *operatorConnPool) getConnection() (OperatorClientConn, error) {
	now := time.Now()

	p.mu.Lock()
	if p.closing {
		p.mu.Unlock()
		return nil, errOperatorConnPoolClosing
	}
	p.cleanupLocked(now)

	best, err := p.selectConnLocked(now)
	if err != nil {
		p.mu.Unlock()
		return nil, err
	}

	handle := &pooledConnHandle{
		conn:  best.conn,
		pool:  p,
		entry: best,
	}

	newUsage := best.borrow()
	shouldGrow := int(newUsage) >= p.cfg.UsersPerConnectionCap && len(p.conns) < p.cfg.MaxConnections

	p.mu.Unlock()

	if shouldGrow {
		p.scheduleScaleAttempt()
	}

	return handle, nil
}

// cleanupLocked prunes unhealthy or expired connections while holding the lock.
func (p *operatorConnPool) cleanupLocked(now time.Time) {
	if len(p.conns) == 0 {
		return
	}

	removalsAllowed := len(p.conns) - p.cfg.MinConnections
	filtered := p.conns[:0]

	for _, pc := range p.conns {
		if pc == nil {
			continue
		}
		if !isHealthy(pc.conn) {
			_ = pc.conn.Close()
			continue
		}

		if removalsAllowed > 0 && p.shouldRemoveConn(pc, now) {
			_ = pc.conn.Close()
			removalsAllowed--
			continue
		}

		filtered = append(filtered, pc)
	}

	p.conns = filtered
}

func (p *operatorConnPool) addConnLocked(conn *grpc.ClientConn, now time.Time) *pooledConn {
	pc := newPooledConn(conn, now)
	p.conns = append(p.conns, pc)
	return pc
}

func (p *operatorConnPool) shouldRemoveConn(pc *pooledConn, now time.Time) bool {
	if pc.usage.Load() != 0 {
		return false
	}
	if p.cfg.MaxLifetime > 0 && now.Sub(pc.createdAt) > p.cfg.MaxLifetime {
		return true
	}
	if p.cfg.IdleTimeout > 0 && now.Sub(pc.lastUsedTime()) > p.cfg.IdleTimeout {
		return true
	}
	return false
}

func (p *operatorConnPool) selectConnLocked(now time.Time) (*pooledConn, error) {
	if candidate := p.leastUsedConnLocked(); candidate != nil {
		return candidate, nil
	}
	if len(p.conns) >= p.cfg.MaxConnections {
		p.recordPoolExhausted(len(p.conns), p.cfg.MaxConnections)
		return nil, fmt.Errorf("operator connection pool exhausted: %d connections", len(p.conns))
	}

	conn, err := p.factory()
	if err != nil {
		p.recordDialFailure("select", err)
		return nil, err
	}
	return p.addConnLocked(conn, now), nil
}

func (p *operatorConnPool) leastUsedConnLocked() *pooledConn {
	var (
		best       *pooledConn
		usageFloor int32
		seen       bool
	)

	if len(p.conns) == 0 {
		return nil
	}

	filtered := p.conns[:0]
	for _, pc := range p.conns {
		if pc == nil {
			continue
		}
		if !isHealthy(pc.conn) {
			_ = pc.conn.Close()
			continue
		}
		filtered = append(filtered, pc)
		usage := pc.usage.Load()
		if !seen || usage < usageFloor {
			best = pc
			usageFloor = usage
			seen = true
		}
	}
	p.conns = filtered

	return best
}

func (p *operatorConnPool) release(entry *pooledConn) {
	if entry == nil {
		return
	}
	entry.release()
}

func (p *operatorConnPool) scheduleScaleAttempt() {
	p.mu.Lock()
	limiter := p.scaleLimiter
	p.mu.Unlock()

	if limiter == nil {
		go p.tryAddConnection()
		return
	}
	select {
	case limiter <- struct{}{}:
		go func(ch chan struct{}) {
			defer func() { <-ch }()
			p.tryAddConnection()
		}(limiter)
	default:
	}
}

func (p *operatorConnPool) updateConfig(cfg OperatorConnectionPoolConfig) {
	cfg = cfg.WithDefaults()

	p.mu.Lock()
	if p.closing {
		p.mu.Unlock()
		return
	}
	defer p.mu.Unlock()

	if cfg.ScaleConcurrency != p.cfg.ScaleConcurrency {
		if cfg.ScaleConcurrency > 0 {
			p.scaleLimiter = make(chan struct{}, cfg.ScaleConcurrency)
		} else {
			p.scaleLimiter = nil
		}
	}

	p.cfg = cfg
	p.cleanupLocked(time.Now())
}

// tryAddConnection attempts to grow the pool by one connection.
func (p *operatorConnPool) tryAddConnection() {
	conn, err := p.factory()
	if err != nil {
		p.recordDialFailure("scale", err)
		return
	}
	p.mu.Lock()
	if p.closing || len(p.conns) >= p.cfg.MaxConnections {
		p.mu.Unlock()
		_ = conn.Close()
		return
	}
	p.addConnLocked(conn, time.Now())
	p.mu.Unlock()
}

func isHealthy(conn *grpc.ClientConn) bool {
	if conn == nil {
		return false
	}
	switch conn.GetState() {
	case connectivity.Shutdown, connectivity.TransientFailure:
		return false
	default:
		return true
	}
}

func (pc *pooledConn) borrow() int32 {
	return pc.usage.Add(1)
}

func (pc *pooledConn) release() {
	if pc.usage.Add(-1) < 0 {
		pc.usage.Store(0)
	}
	pc.lastUsedNanos.Store(time.Now().UnixNano())
}

func (pc *pooledConn) lastUsedTime() time.Time {
	ts := pc.lastUsedNanos.Load()
	if ts == 0 {
		return pc.createdAt
	}
	return time.Unix(0, ts)
}

// Close gracefully shuts down the connection pool, waiting for active connections to be released.
func (p *operatorConnPool) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), operatorConnPoolGracefulCloseTimeout)
	defer cancel()
	p.closeWithContext(ctx)
}

func (p *operatorConnPool) closeWithContext(ctx context.Context) {
	p.mu.Lock()
	if p.closing {
		conns := p.conns
		p.conns = nil
		p.mu.Unlock()
		closeConnections(conns)
		return
	}
	p.closing = true
	p.mu.Unlock()

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for p.activeBorrowers() != 0 {

		select {
		case <-ctx.Done():
			break
		case <-ticker.C:
		}
	}

	p.mu.Lock()
	conns := p.conns
	p.conns = nil
	p.mu.Unlock()
	closeConnections(conns)
}

func (p *operatorConnPool) activeBorrowers() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	total := 0
	for _, pc := range p.conns {
		if pc != nil {
			total += int(pc.usage.Load())
		}
	}
	return total
}

func closeConnections(conns []*pooledConn) {
	for _, pc := range conns {
		if pc != nil && pc.conn != nil {
			_ = pc.conn.Close()
		}
	}
}

type pooledConnHandle struct {
	conn  *grpc.ClientConn
	pool  *operatorConnPool
	entry *pooledConn
	once  sync.Once
}

func (h *pooledConnHandle) Close() error {
	h.once.Do(func() {
		h.pool.release(h.entry)
	})
	return nil
}

func (h *pooledConnHandle) Invoke(ctx context.Context, method string, args any, reply any, opts ...grpc.CallOption) error {
	return h.conn.Invoke(ctx, method, args, reply, opts...)
}

func (h *pooledConnHandle) NewStream(ctx context.Context, desc *grpc.StreamDesc, method string, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	return h.conn.NewStream(ctx, desc, method, opts...)
}
