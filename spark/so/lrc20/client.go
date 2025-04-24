package lrc20

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go/common"
	pblrc20 "github.com/lightsparkdev/spark-go/proto/lrc20"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent"
	"github.com/lightsparkdev/spark-go/so/ent/tokenoutput"
	"github.com/lightsparkdev/spark-go/so/logging"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
)

// connPool manages a pool of gRPC connections with fast access and health checks
type connPool struct {
	// Use channel for efficient semaphore-like behavior
	// Available connections can be taken without locking
	availableConns chan *grpc.ClientConn

	// For management operations only
	mu       sync.Mutex
	allConns map[*grpc.ClientConn]struct{} // Track all connections for cleanup
	config   *so.Config
	maxSize  int
	network  string
	logger   *slog.Logger

	// Health check related fields
	healthCheckInterval time.Duration
	stopHealthCheck     chan struct{}
	wg                  sync.WaitGroup
}

// newConnPool creates a new connection pool with improved performance characteristics
func newConnPool(config *so.Config) (*connPool, error) {
	logger := slog.Default()
	network := common.Regtest.String()

	// Validate network support
	if !slices.Contains(config.SupportedNetworks, common.Regtest) {
		return nil, fmt.Errorf("regtest network not supported by this operator")
	}

	size := int(config.Lrc20Configs[network].GRPCPoolSize)
	if size <= 0 {
		size = 5 // Set a reasonable default if config is invalid
	}

	pool := &connPool{
		availableConns:      make(chan *grpc.ClientConn, size),
		allConns:            make(map[*grpc.ClientConn]struct{}, size),
		config:              config,
		maxSize:             size,
		network:             network,
		logger:              logger,
		healthCheckInterval: 30 * time.Second, // Configurable health check interval
		stopHealthCheck:     make(chan struct{}),
	}

	// Initialize the pool with connections
	for i := 0; i < size; i++ {
		conn, err := createConnection(config)
		if err != nil {
			// Log the error but continue trying to create more connections
			logger.Warn("failed to create initial connection", "error", err, "index", i)
			continue
		}

		pool.mu.Lock()
		pool.allConns[conn] = struct{}{}
		pool.mu.Unlock()

		// Add to available connections
		pool.availableConns <- conn
	}

	// Check if we have at least one valid connection
	if len(pool.allConns) == 0 {
		return nil, fmt.Errorf("failed to initialize connection pool: could not create any valid connections")
	}

	// Start health check goroutine
	pool.wg.Add(1)
	go pool.healthCheckLoop()

	return pool, nil
}

// healthCheckLoop runs periodic health checks on idle connections
func (p *connPool) healthCheckLoop() {
	defer p.wg.Done()

	ticker := time.NewTicker(p.healthCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-p.stopHealthCheck:
			return
		case <-ticker.C:
			p.performHealthCheck()
		}
	}
}

// performHealthCheck verifies idle connections and replaces bad ones
func (p *connPool) performHealthCheck() {
	// Count how many connections we can check (available ones)
	checkCount := len(p.availableConns)
	if checkCount == 0 {
		return
	}

	p.logger.Debug("performing health check on idle connections", "count", checkCount)

	for i := 0; i < checkCount; i++ {
		// Get a connection from the pool
		select {
		case conn := <-p.availableConns:
			// Check if connection is healthy
			if p.isConnectionHealthy(conn) {
				// Return healthy connection to the pool
				p.availableConns <- conn
			} else {
				// Replace the unhealthy connection
				p.mu.Lock()
				delete(p.allConns, conn)
				p.mu.Unlock()

				conn.Close()

				// Create a new connection to replace the bad one
				newConn, err := createConnection(p.config)
				if err != nil {
					p.logger.Error("failed to create replacement connection", "error", err)
				} else {
					p.mu.Lock()
					p.allConns[newConn] = struct{}{}
					p.mu.Unlock()

					p.availableConns <- newConn
				}
			}
		default:
			// No more connections available to check
			return
		}
	}
}

// isConnectionHealthy checks if a connection is still usable
func (p *connPool) isConnectionHealthy(conn *grpc.ClientConn) bool {
	state := conn.GetState()

	// Consider READY, IDLE, and even CONNECTING as healthy states
	return state != connectivity.TransientFailure && state != connectivity.Shutdown
}

// getConn gets a connection from the pool with timeout
func (p *connPool) getConn(ctx context.Context) (*grpc.ClientConn, error) {
	// Try to get an existing connection with context timeout
	select {
	case conn := <-p.availableConns:
		return conn, nil
	case <-ctx.Done():
		// Context timed out while waiting for connection
		return nil, ctx.Err()
	default:
		// No connection immediately available, try to create a new one
		// This happens outside the critical section
		conn, err := createConnection(p.config)
		if err != nil {
			return nil, fmt.Errorf("failed to create new connection: %w", err)
		}

		p.mu.Lock()
		// Check if we're allowed to add a new connection
		if len(p.allConns) < p.maxSize*2 { // Allow bursting to 2x the normal pool size
			p.allConns[conn] = struct{}{}
			p.mu.Unlock()
			return conn, nil
		}
		p.mu.Unlock()

		// We're over capacity, close the connection and try to get one from the pool
		conn.Close()

		// Wait with timeout for a connection to become available
		select {
		case conn := <-p.availableConns:
			return conn, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// returnConn returns a connection to the pool if it's healthy, otherwise creates a replacement
func (p *connPool) returnConn(conn *grpc.ClientConn) {
	// Quick check if connection is valid
	if conn == nil {
		return
	}

	// Check if this connection is one we know about
	p.mu.Lock()
	_, exists := p.allConns[conn]
	p.mu.Unlock()

	if !exists {
		// Not our connection, just close it
		conn.Close()
		return
	}

	// Check if the connection is still healthy
	if !p.isConnectionHealthy(conn) {
		// Connection is unhealthy, remove and replace it
		p.mu.Lock()
		delete(p.allConns, conn)
		p.mu.Unlock()

		conn.Close()

		// Try to create a replacement
		newConn, err := createConnection(p.config)
		if err != nil {
			p.logger.Error("failed to create replacement connection", "error", err)
			return
		}

		p.mu.Lock()
		p.allConns[newConn] = struct{}{}
		p.mu.Unlock()

		conn = newConn
	}

	// Try to return to pool, but don't block if full
	select {
	case p.availableConns <- conn:
		// Successfully returned to pool
	default:
		// Pool is full, close this connection
		p.mu.Lock()
		delete(p.allConns, conn)
		p.mu.Unlock()

		conn.Close()
	}
}

// Close closes all connections in the pool
func (p *connPool) Close() error {
	// Signal health checker to stop
	close(p.stopHealthCheck)

	// Wait for health checker to finish
	p.wg.Wait()

	p.mu.Lock()
	defer p.mu.Unlock()

	// Close all connections
	var lastErr error
	for conn := range p.allConns {
		if err := conn.Close(); err != nil {
			lastErr = err
			p.logger.Error("error closing connection", "error", err)
		}
		delete(p.allConns, conn)
	}

	// Drain the channel
	close(p.availableConns)
	for conn := range p.availableConns {
		if err := conn.Close(); err != nil {
			lastErr = err
			p.logger.Error("error closing connection from channel", "error", err)
		}
	}

	return lastErr
}

// createConnection creates a new gRPC connection with retry policy
func createConnection(config *so.Config) (*grpc.ClientConn, error) {
	network := common.Regtest.String()
	if !slices.Contains(config.SupportedNetworks, common.Regtest) {
		return nil, fmt.Errorf("regtest network not supported by this operator")
	}

	lrc20Config := config.Lrc20Configs[network]
	retryConfig := common.RetryPolicyConfig{
		MaxAttempts:          12,
		InitialBackoff:       1 * time.Second,
		MaxBackoff:           30 * time.Second,
		BackoffMultiplier:    1.5,
		RetryableStatusCodes: []string{"UNAVAILABLE", "NOT_FOUND"},
	}

	if lrc20Config.RelativeCertPath != "" {
		certPath := fmt.Sprintf("%s/%s", config.RunDirectory, lrc20Config.RelativeCertPath)
		return common.NewGRPCConnectionWithCert(lrc20Config.Host, certPath, &retryConfig)
	}
	return common.NewGRPCConnectionWithoutTLS(lrc20Config.Host, &retryConfig)
}

// Client provides methods for interacting with the LRC20 node with built-in retries
type Client struct {
	config *so.Config
	pool   *connPool
}

// NewClient creates a new LRC20 client with a connection pool
func NewClient(config *so.Config) (*Client, error) {
	pool, err := newConnPool(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection pool: %w", err)
	}

	return &Client{
		config: config,
		pool:   pool,
	}, nil
}

// executeLrc20Call handles common LRC20 RPC call pattern with proper connection management
func (c *Client) executeLrc20Call(ctx context.Context, operation func(client pblrc20.SparkServiceClient) error) error {
	network := common.Regtest.String()
	if c.shouldSkipLrc20Call(ctx, network) {
		return nil
	}

	// Use the context for timeout when getting connection
	conn, err := c.pool.getConn(ctx)
	if err != nil {
		return fmt.Errorf("failed to get connection from pool: %w", err)
	}

	// Always return the connection, whether the operation succeeds or fails
	defer c.pool.returnConn(conn)

	client := pblrc20.NewSparkServiceClient(conn)
	return operation(client)
}

// SendSparkSignature sends a token transaction signature to the LRC20 node
func (c *Client) SendSparkSignature(
	ctx context.Context,
	signatureData *pblrc20.SparkSignatureData,
) error {
	return c.executeLrc20Call(ctx, func(client pblrc20.SparkServiceClient) error {
		_, err := client.SendSparkSignature(ctx, &pblrc20.SendSparkSignatureRequest{
			SignatureData: signatureData,
		})
		return err
	})
}

// FreezeTokens freezes or unfreezes tokens on the LRC20 node
func (c *Client) FreezeTokens(
	ctx context.Context,
	req *pb.FreezeTokensRequest,
) error {
	return c.executeLrc20Call(ctx, func(client pblrc20.SparkServiceClient) error {
		_, err := client.FreezeTokens(ctx, req)
		return err
	})
}

// VerifySparkTx verifies a token transaction with the LRC20 node
func (c *Client) VerifySparkTx(
	ctx context.Context,
	tokenTransaction *pb.TokenTransaction,
) error {
	return c.executeLrc20Call(ctx, func(client pblrc20.SparkServiceClient) error {
		_, err := client.VerifySparkTx(ctx, &pblrc20.VerifySparkTxRequest{
			FinalTokenTransaction: tokenTransaction,
		})
		if err != nil {
			return err
		}
		// If the error response is null the transaction is valid.
		return nil
	})
}

// shouldSkipLrc20Call checks if LRC20 RPCs are disabled for the given network
func (c *Client) shouldSkipLrc20Call(ctx context.Context, network string) bool {
	logger := logging.GetLoggerFromContext(ctx)

	if lrc20Config, ok := c.config.Lrc20Configs[network]; ok && lrc20Config.DisableRpcs {
		logger.Info("Skipping LRC20 node call due to DisableRpcs flag")
		return true
	}
	return false
}

// MarkWithdrawnTokenOutputs gets a list of withdrawn token outputs from the LRC20 node.  This
// marks these outputs as 'Withdrawn' and accordingly does not return them as 'owned outputs'
// when requested by wallets / external parties (which also allows for updating balance).
func (c *Client) MarkWithdrawnTokenOutputs(
	ctx context.Context,
	_ common.Network,
	dbTx *ent.Tx,
	blockHash *chainhash.Hash,
) error {
	logger := logging.GetLoggerFromContext(ctx)
	network := common.Regtest.String()
	if lrc20Config, ok := c.config.Lrc20Configs[network]; ok && lrc20Config.DisableRpcs {
		logger.Info("Skipping LRC20 node call due to DisableRpcs flag")
		return nil
	}
	if lrc20Config, ok := c.config.Lrc20Configs[network]; ok && lrc20Config.DisableL1 {
		logger.Info("Skipping LRC20 node call due to DisableL1 flag")
		return nil
	}

	allOutputs := []*pb.TokenOutput{}

	pageResponse, err := func(ctx context.Context) (*pblrc20.ListWithdrawnOutputsResponse, error) {
		conn, err := c.pool.getConn(ctx)
		if err != nil {
			return nil, err
		}
		defer c.pool.returnConn(conn)

		client := pblrc20.NewSparkServiceClient(conn)

		pageSize := uint32(c.config.Lrc20Configs[network].GRPCPageSize)
		pageResponse, err := client.ListWithdrawnOutputs(ctx, &pblrc20.ListWithdrawnOutputsRequest{
			// TODO(DL-99): Fetch just for the latest blockhash instead of all withdrawn outputs.
			// TODO(DL-98): Add support for pagination.
			PageSize: &pageSize,
		})
		return pageResponse, err
	}(ctx)
	if err != nil {
		return fmt.Errorf("error fetching withdrawn outputs: %w", err)
	}

	// Add the current page of results to our collection
	allOutputs = append(allOutputs, pageResponse.Outputs...)

	logger.Info("Completed fetching all withdrawn outputs", "total", len(allOutputs))

	// Mark each output as withdrawn in the database
	if len(allOutputs) > 0 {
		client := dbTx.TokenOutput
		var outputIDs []uuid.UUID

		// First collect all valid output IDs
		for _, output := range allOutputs {
			outputUUID, err := uuid.Parse(*output.Id)
			if err != nil {
				logger.Warn("Failed to parse output ID as UUID",
					"output_id", output.Id,
					"error", err)
				continue
			}
			outputIDs = append(outputIDs, outputUUID)
		}

		if len(outputIDs) > 0 {
			_, err := client.Update().
				Where(tokenoutput.IDIn(outputIDs...)).
				SetConfirmedWithdrawBlockHash(blockHash.CloneBytes()).
				Save(ctx)
			if err != nil {
				return fmt.Errorf("failed to bulk update token outputs: %w", err)
			}

			logger.Debug("Successfully marked token outputs as withdrawn",
				"count", len(outputIDs))
		}
	}

	return nil
}

// UnmarkWithdrawnTokenOutputs clears the withdrawn status for token outputs that were previously
// marked as withdrawn in a specific block. This is used during blockchain reorganizations to
// restore token outputs that were withdrawn in blocks that are no longer part of the main chain.
func (c *Client) UnmarkWithdrawnTokenOutputs(
	ctx context.Context,
	dbTx *ent.Tx,
	blockHash *chainhash.Hash,
) error {
	logger := logging.GetLoggerFromContext(ctx)

	// Get all token outputs that were marked as withdrawn in this block
	tokenOutputs, err := dbTx.TokenOutput.Query().
		Where(tokenoutput.ConfirmedWithdrawBlockHashEQ(blockHash.CloneBytes())).
		All(ctx)
	if err != nil {
		return fmt.Errorf("error querying withdrawn outputs for block %s: %w", blockHash.String(), err)
	}

	count := len(tokenOutputs)
	if count == 0 {
		logger.Info("No withdrawn token outputs found for block", "block_hash", blockHash.String())
		return nil
	}

	logger.Info("Unmarking withdrawn token outputs due to reorg",
		"block_hash", blockHash.String(),
		"count", count)

	// Clear the confirmed_withdraw_block_hash field for all affected outputs
	outputIDs := make([]uuid.UUID, len(tokenOutputs))
	for i, output := range tokenOutputs {
		outputIDs[i] = output.ID
	}
	_, err = dbTx.TokenOutput.Update().
		Where(tokenoutput.IDIn(outputIDs...)).
		ClearConfirmedWithdrawBlockHash().
		Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to clear withdraw block hash for outputs: %w", err)
	}

	logger.Info("Successfully unmarked token outputs",
		"block_hash", blockHash.String(),
		"count", count)

	return nil
}

// Close closes the client and its connection pool
func (c *Client) Close() error {
	if c.pool != nil {
		return c.pool.Close()
	}
	return nil
}
