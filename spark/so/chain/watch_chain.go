package chain

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"runtime/debug"
	"slices"
	"time"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	"go.uber.org/zap"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/rpcclient"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/logging"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/blockheight"
	"github.com/lightsparkdev/spark/so/ent/cooperativeexit"
	"github.com/lightsparkdev/spark/so/ent/depositaddress"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/signingkeyshare"
	"github.com/lightsparkdev/spark/so/ent/treenode"
	entutxo "github.com/lightsparkdev/spark/so/ent/utxo"
	"github.com/lightsparkdev/spark/so/entephemeral"
	"github.com/lightsparkdev/spark/so/helper"
	"github.com/lightsparkdev/spark/so/knobs"
	transferpkg "github.com/lightsparkdev/spark/so/transfer"
	"github.com/lightsparkdev/spark/so/tree"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	"google.golang.org/protobuf/proto"
)

// errEphemeralMainDBDiverged is returned when the ephemeral DB commits but the
// main DB commit fails, leaving the two stores in an irreconcilable state.
// WatchChain treats this as fatal and stops processing further blocks.
var errEphemeralMainDBDiverged = errors.New("ephemeral and main DB state diverged")

var (
	meter = otel.Meter("chain_watcher")

	// Metrics
	eligibleNodesGauge                 metric.Int64Gauge
	blockHeightGauge                   metric.Int64Gauge
	blockHeightProcessingTimeHistogram metric.Int64Histogram
	sparkChainActionTimeHistogram      metric.Int64Histogram
	scanRetryCounter                   metric.Int64Counter

	// tweakKeysForCoopExitFunc is a function variable that can be mocked in tests
	tweakKeysForCoopExitFunc = tweakKeysForCoopExit
)

// txBackedSession adapts an existing transaction to ent.Session so code paths
// that rely on ent.GetDbFromContext can run inside chain watcher block handling.
type txBackedSession struct {
	tx *ent.Tx
}

func (s *txBackedSession) GetOrBeginTx(context.Context) (*ent.Tx, error) {
	if s.tx == nil {
		return nil, fmt.Errorf("no transaction available")
	}
	return s.tx, nil
}

func (s *txBackedSession) GetClient(ctx context.Context) (*ent.Client, error) {
	tx, err := s.GetOrBeginTx(ctx)
	if err != nil {
		return nil, err
	}
	return tx.Client(), nil
}

func (s *txBackedSession) GetTxIfExists() *ent.Tx { return s.tx }

func (s *txBackedSession) Notify(context.Context, ent.Notification) error { return nil }

// txBackedEphemeralSession intentionally omits Notify: entephemeral.Session
// has no notification interface, unlike ent.Session.
type txBackedEphemeralSession struct {
	dbClient     *entephemeral.Client
	tx           *entephemeral.Tx
	txWasStarted bool
}

func newTxBackedEphemeralSession(dbClient *entephemeral.Client, tx *entephemeral.Tx) *txBackedEphemeralSession {
	session := &txBackedEphemeralSession{
		dbClient: dbClient,
	}
	session.bindTx(tx)
	return session
}

func (s *txBackedEphemeralSession) bindTx(tx *entephemeral.Tx) {
	s.tx = tx
	if tx == nil {
		return
	}
	s.txWasStarted = true
	tx.OnCommit(func(fn entephemeral.Committer) entephemeral.Committer {
		return entephemeral.CommitFunc(func(ctx context.Context, tx *entephemeral.Tx) error {
			err := fn.Commit(ctx, tx)
			if err == nil && s.tx == tx {
				s.tx = nil
			}
			return err
		})
	})
	tx.OnRollback(func(fn entephemeral.Rollbacker) entephemeral.Rollbacker {
		return entephemeral.RollbackFunc(func(ctx context.Context, tx *entephemeral.Tx) error {
			err := fn.Rollback(ctx, tx)
			if s.tx == tx {
				s.tx = nil
			}
			return err
		})
	})
}

func (s *txBackedEphemeralSession) GetOrBeginTx(ctx context.Context) (*entephemeral.Tx, error) {
	if s.tx != nil {
		return s.tx, nil
	}
	if s.dbClient == nil {
		return nil, fmt.Errorf("no ephemeral client available")
	}
	tx, err := s.dbClient.Tx(ctx)
	if err != nil {
		return nil, err
	}
	s.bindTx(tx)
	return tx, nil
}

func (s *txBackedEphemeralSession) GetClient(context.Context) (*entephemeral.Client, error) {
	if s.tx != nil {
		return s.tx.Client(), nil
	}
	if s.dbClient == nil {
		return nil, fmt.Errorf("no ephemeral client available")
	}
	return s.dbClient, nil
}

func (s *txBackedEphemeralSession) GetTxIfExists() *entephemeral.Tx { return s.tx }

func (s *txBackedEphemeralSession) CommitError() error { return nil }

func (s *txBackedEphemeralSession) TxWasStarted() bool { return s.txWasStarted }

const (
	nonStaticDefaultConfirmationThreshold = 3
	lookbackThreshold                     = 2
)

func init() {
	var err error

	eligibleNodesGauge, err = meter.Int64Gauge(
		"chain_watcher.eligible_nodes",
		metric.WithDescription("Number of nodes eligible for timelock expiry checks"),
	)
	if err != nil {
		otel.Handle(err)
		eligibleNodesGauge = noop.Int64Gauge{}
	}

	blockHeightGauge, err = meter.Int64Gauge(
		"chain_watcher.current_block_height",
		metric.WithDescription("Current block height processed by chain watcher"),
	)
	if err != nil {
		otel.Handle(err)
		blockHeightGauge = noop.Int64Gauge{}
	}

	blockHeightProcessingTimeHistogram, err = meter.Int64Histogram(
		"chain_watcher.block_height_processing_time_milliseconds",
		metric.WithDescription("Time taken to process a block"),
		metric.WithExplicitBucketBoundaries(
			3000,   // 3 seconds (fast processing)
			7000,   // 7 seconds (below average)
			10000,  // 10 seconds (average)
			20000,  // 20 seconds (above average)
			60000,  // 1 minute (slow)
			120000, // 2 minutes (very slow)
			180000, // 3 minutes (maximum expected)
		),
	)
	if err != nil {
		otel.Handle(err)
		blockHeightProcessingTimeHistogram = noop.Int64Histogram{}
	}

	sparkChainActionTimeHistogram, err = meter.Int64Histogram(
		"chain_watcher.spark_chain_action_time_milliseconds",
		metric.WithDescription("Time taken to perform Spark chain actions (watchtower broadcasts, coop exit key tweaks)"),
		metric.WithExplicitBucketBoundaries(1000, 3000, 7000, 10000, 20000, 60000, 120000, 180000),
	)
	if err != nil {
		otel.Handle(err)
		sparkChainActionTimeHistogram = noop.Int64Histogram{}
	}

	scanRetryCounter, err = meter.Int64Counter(
		"chain_watcher.scan_retries",
		metric.WithDescription("Chain scans retried after a failed scan or one that missed the notified block"),
	)
	if err != nil {
		otel.Handle(err)
		scanRetryCounter = noop.Int64Counter{}
	}
}

func pollInterval(network btcnetwork.Network) time.Duration {
	switch network {
	case btcnetwork.Mainnet:
		return 15 * time.Second
	case btcnetwork.Testnet:
		return 1 * time.Minute
	case btcnetwork.Regtest:
		return 3 * time.Second
	case btcnetwork.Signet:
		return 3 * time.Second
	default:
		return 1 * time.Minute
	}
}

// Tip represents the tip of a blockchain.
type Tip struct {
	Height int64
	Hash   chainhash.Hash
}

// NewTip creates a new ChainTip.
func NewTip(height int64, hash chainhash.Hash) Tip {
	return Tip{Height: height, Hash: hash}
}

// Difference represents the difference between two chain tips
// that needs to be rescanned.
type Difference struct {
	CommonAncestor Tip
	Disconnected   []Tip
	Connected      []Tip
}

func findPreviousChainTip(chainTip Tip, client *rpcclient.Client) (Tip, error) {
	blockResp, err := client.GetBlockVerbose(&chainTip.Hash)
	if err != nil {
		return Tip{}, err
	}
	var prevHash chainhash.Hash
	err = chainhash.Decode(&prevHash, blockResp.PreviousHash)
	if err != nil {
		return Tip{}, err
	}
	return Tip{Height: blockResp.Height - 1, Hash: prevHash}, nil
}

func findDifference(currChainTip, newChainTip Tip, client *rpcclient.Client) (Difference, error) {
	var disconnected []Tip
	var connected []Tip

	for !currChainTip.Hash.IsEqual(&newChainTip.Hash) {
		// Walk back the chain, finding blocks needed to connect and disconnect. Only walk back
		// the header with the greater height, or both if equal heights (i.e. same height, different hashes!).
		newHeight := newChainTip.Height
		currHeight := currChainTip.Height
		if newHeight <= currHeight {
			disconnected = append(disconnected, currChainTip)
			prevChainTip, err := findPreviousChainTip(currChainTip, client)
			if err != nil {
				return Difference{}, err
			}
			currChainTip = prevChainTip
		}
		if newHeight >= currHeight {
			connected = append([]Tip{newChainTip}, connected...)
			prevChainTip, err := findPreviousChainTip(newChainTip, client)
			if err != nil {
				return Difference{}, err
			}
			newChainTip = prevChainTip
		}
	}

	return Difference{
		CommonAncestor: newChainTip,
		Disconnected:   disconnected,
		Connected:      connected,
	}, nil
}

func scanChainUpdates(
	ctx context.Context,
	config *so.Config,
	dbClient *ent.Client,
	ephemeralDBClient *entephemeral.Client,
	bitcoinClient *rpcclient.Client,
	network btcnetwork.Network,
	bitcoindConfig so.BitcoindConfig,
) (Tip, error) {
	logger := logging.GetLoggerFromContext(ctx)
	latestBlockHeight, err := bitcoinClient.GetBlockCount()
	if err != nil {
		return Tip{}, fmt.Errorf("failed to get block count: %w", err)
	}
	latestBlockHash, err := bitcoinClient.GetBlockHash(latestBlockHeight)
	if err != nil {
		return Tip{}, fmt.Errorf("failed to get block hash at height %d: %w", latestBlockHeight, err)
	}
	latestChainTip := NewTip(latestBlockHeight, *latestBlockHash)
	logger.Sugar().Infof("Latest chain tip height: %d, hash: %s", latestBlockHeight, latestBlockHash)

	dbBlockHeight, err := dbClient.BlockHeight.Query().
		Where(blockheight.NetworkEQ(network)).
		Only(ctx)
	if ent.IsNotFound(err) {
		startHeight := max(0, latestBlockHeight-18)
		logger.Sugar().Infof("Block height %d not found, creating new entry", startHeight)
		startBlockHash, hashErr := bitcoinClient.GetBlockHash(startHeight)
		if hashErr != nil {
			return Tip{}, fmt.Errorf("failed to get block hash at start height %d: %w", startHeight, hashErr)
		}
		dbBlockHeight, err = dbClient.BlockHeight.Create().
			SetHeight(startHeight).
			SetNetwork(network).
			SetBlockHash(startBlockHash.CloneBytes()).
			Save(ctx)
	}
	if err != nil {
		return Tip{}, fmt.Errorf("failed to query block height: %w", err)
	}
	var dbChainTip Tip
	if dbBlockHeight.BlockHash != nil && len(*dbBlockHeight.BlockHash) == chainhash.HashSize {
		storedHash, err := chainhash.NewHash(*dbBlockHeight.BlockHash)
		if err != nil {
			return Tip{}, fmt.Errorf("failed to parse stored block hash at height %d: %w", dbBlockHeight.Height, err)
		}
		dbChainTip = NewTip(dbBlockHeight.Height, *storedHash)
		logger.Sugar().Infof("DB chain tip height: %d, hash: %s (from stored hash)", dbBlockHeight.Height, storedHash)
	} else {
		// Backwards compatibility: fetch from node if hash not stored yet.
		// After the next block is processed, the hash will be stored.
		dbBlockHash, err := bitcoinClient.GetBlockHash(dbBlockHeight.Height)
		if err != nil {
			return Tip{}, fmt.Errorf("failed to get block hash at db height %d: %w", dbBlockHeight.Height, err)
		}
		dbChainTip = NewTip(dbBlockHeight.Height, *dbBlockHash)
		logger.Sugar().Infof("DB chain tip height: %d, hash: %s (from node, no stored hash)", dbBlockHeight.Height, dbBlockHash)
	}
	difference, err := findDifference(dbChainTip, latestChainTip, bitcoinClient)
	if err != nil {
		return Tip{}, fmt.Errorf("failed to find difference: %w", err)
	}
	err = disconnectBlocks(ctx, dbClient, difference.Disconnected, network)
	if err != nil {
		return Tip{}, fmt.Errorf("failed to disconnect blocks: %w", err)
	}

	// Save the old block height before connecting new blocks so we can query deposits
	// that were confirmed in any of the blocks we're about to connect
	oldBlockHeight := dbBlockHeight.Height

	// Panics from connectBlocks are intentionally not recovered here.
	// A divergence panic (errEphemeralMainDBDiverged) must be fatal: the operator
	// cannot safely continue with split-brain DB state. Any other panic indicates
	// a code bug (nil-ptr, out-of-range, etc.) and is also treated as fatal rather
	// than silently skipping blocks, which would mask the underlying issue.
	err = connectBlocks(
		ctx,
		config,
		dbClient,
		ephemeralDBClient,
		bitcoinClient,
		difference.Connected,
		network,
	)
	if err != nil {
		return Tip{}, fmt.Errorf("failed to connect blocks: %w", err)
	}
	logger.Sugar().Infof("Connected %d blocks", len(difference.Connected))

	// Self-gated on block_heights.chain_action_height: runs only while the
	// cursor is behind the connected tip, so failed, crashed, or interrupted
	// runs are retried on every scan tick until they succeed. Runs before the
	// deposit-availability pass below so a persistent failure there cannot
	// starve watchtower broadcasts and coop-exit tweaks.
	if err := processSparkChainActions(ctx, config, dbClient, ephemeralDBClient, bitcoinClient, network); err != nil {
		if errors.Is(err, errEphemeralMainDBDiverged) {
			return Tip{}, err
		}
		logger.Error("Failed to perform Spark chain actions", zap.Error(err))
	}

	// After connecting blocks, process deposit availability. This runs sequentially
	// to avoid potential issues with parallel database transactions.
	deposits, err := loadDepositAvailabilityCandidates(ctx, dbClient, latestBlockHeight, oldBlockHeight, bitcoindConfig)
	if err != nil {
		return Tip{}, fmt.Errorf("failed to load deposit availability candidates: %w", err)
	}
	err = setDepositAvailability(ctx, dbClient, deposits, network)
	if err != nil {
		return Tip{}, fmt.Errorf("failed to set deposit availability: %w", err)
	}

	// Mark individual UTXOs as confirmed once they meet the confirmation threshold.
	// Each UTXO is tracked independently since new UTXOs can arrive at the same
	// deposit address after the first one was confirmed.
	// UTXOs are only created in connectBlocks (above) when scanning chain data for
	// outputs matching deposit addresses, with BlockHeight set to the block they
	// were found in. This bulk update catches all UTXOs that have reached the
	// required number of confirmations.
	threshold := getNonStaticConfirmationThreshold(bitcoindConfig)
	maxUtxoBlockHeight := latestBlockHeight - threshold + 1
	_, err = dbClient.Utxo.Update().
		Where(entutxo.AvailabilityConfirmedAtIsNil()).
		Where(entutxo.BlockHeightLTE(maxUtxoBlockHeight)).
		Where(entutxo.NetworkEQ(network)).
		SetAvailabilityConfirmedAt(time.Now()).
		Save(ctx)
	if err != nil {
		return Tip{}, fmt.Errorf("failed to mark UTXOs as confirmed: %w", err)
	}

	return latestChainTip, nil
}

func rpcClientConfig(cfg so.BitcoindConfig) rpcclient.ConnConfig {
	return rpcclient.ConnConfig{
		Host:         cfg.Host,
		User:         cfg.User,
		Pass:         cfg.Password,
		Params:       cfg.Network,
		DisableTLS:   true, // TODO: PE help
		HTTPPostMode: true,
	}
}

func WatchChain(
	ctx context.Context,
	config *so.Config,
	dbClient *ent.Client,
	ephemeralDBClient *entephemeral.Client,
	bitcoindConfig so.BitcoindConfig,
) error {
	logger := logging.GetLoggerFromContext(ctx)

	network, err := btcnetwork.FromString(bitcoindConfig.Network)
	if err != nil {
		return err
	}

	if _, err := scanOnce(ctx, config, dbClient, ephemeralDBClient, bitcoindConfig, network); err != nil {
		if errors.Is(err, errEphemeralMainDBDiverged) {
			return err
		}
		logger.Error("failed to scan chain updates", zap.Error(err))
	}

	zmqSubscriber, err := NewZmqSubscriber()
	if err != nil {
		return err
	}

	defer func() {
		err := zmqSubscriber.Close()
		if err != nil {
			logger.Warn("Failed to close ZMQ subscriber", zap.Error(err))
		}
	}()

	newBlockNotification, errChan, err := zmqSubscriber.Subscribe(ctx, bitcoindConfig.ZmqPubRawBlock, "rawblock")
	if err != nil {
		return err
	}

	// TODO: we should consider alerting on errors within this loop
	for {
		notification := BlockNotification{}
		select {
		case err := <-errChan:
			logger.Error("Error receiving ZMQ message", zap.Error(err))
			return err
		case <-ctx.Done():
			logger.Info("Context done, stopping chain watcher")
			return nil
		case n := <-newBlockNotification:
			notification = n
		case <-time.After(pollInterval(network)):
		}

		if err := scanWithRetry(ctx, config, dbClient, ephemeralDBClient, bitcoindConfig, network, notification); err != nil {
			return err
		}
	}
}

// scanOnce runs one full chain scan on a fresh bitcoind client. The fresh
// client per attempt is load-bearing: bitcoind is a multi-replica Service and
// the client's keep-alive connection pins it to one replica, so a retry on the
// same client would keep asking the lagging replica that just failed it; a new
// client re-round-robins.
func scanOnce(
	ctx context.Context,
	config *so.Config,
	dbClient *ent.Client,
	ephemeralDBClient *entephemeral.Client,
	bitcoindConfig so.BitcoindConfig,
	network btcnetwork.Network,
) (Tip, error) {
	bitcoinClient, err := rpcclient.New(new(rpcClientConfig(bitcoindConfig)), nil)
	if err != nil {
		return Tip{}, err
	}
	defer bitcoinClient.Shutdown()
	return scanChainUpdates(ctx, config, dbClient, ephemeralDBClient, bitcoinClient, network, bitcoindConfig)
}

// reachedNotification reports whether a committed tip covers the notified
// block. Unparsed notifications have no target, so any successful scan
// suffices; the hash comparison (not height alone) catches same-height reorgs.
func reachedNotification(tip Tip, n BlockNotification) bool {
	if !n.Parsed {
		return true
	}
	return tip.Hash.IsEqual(&n.Hash) || tip.Height > n.Height
}

// scanRetryDelays are the waits before each retry.
var scanRetryDelays = []time.Duration{500 * time.Millisecond, 2 * time.Second, 5 * time.Second}

// scanWithRetry drives the scans for one wake-up. Scans that error — or miss
// the notified block because a lagging replica answered a stale tip — retry on
// the fixed schedule in scanRetryDelays; after that the poll timer is the
// fallback. Only fatal errors are returned.
func scanWithRetry(
	ctx context.Context,
	config *so.Config,
	dbClient *ent.Client,
	ephemeralDBClient *entephemeral.Client,
	bitcoindConfig so.BitcoindConfig,
	network btcnetwork.Network,
	notification BlockNotification,
) error {
	logger := logging.GetLoggerFromContext(ctx)
	start := time.Now()

	for attempt := 0; ; attempt++ {
		tip, err := scanOnce(ctx, config, dbClient, ephemeralDBClient, bitcoindConfig, network)

		if errors.Is(err, errEphemeralMainDBDiverged) {
			return err
		}

		// A scan can succeed and still miss the announced block, when a lagging
		// replica answers the old tip and the reconcile no-ops.
		if err == nil && reachedNotification(tip, notification) {
			if attempt > 0 {
				logger.Sugar().Infof("Chain scan reached tip %d after %d retries, %.1fs after wake-up", tip.Height, attempt, time.Since(start).Seconds())
			}
			return nil
		}

		reason := "stale_tip"
		if err != nil {
			logger.Error("Failed to scan chain updates", zap.Error(err))
			reason = "error"
		}

		// Give up once the schedule is spent, or once the next wait would
		// outlast the poll interval that re-drives the scan anyway.
		elapsed := time.Since(start)
		if attempt >= len(scanRetryDelays) || elapsed+scanRetryDelays[attempt] >= pollInterval(network) {
			logger.Sugar().Warnf("Giving up chain scan after %d attempts (%.1fs, last reason %s); falling back to poll timer", attempt+1, elapsed.Seconds(), reason)
			return nil
		}
		scanRetryCounter.Add(ctx, 1, metric.WithAttributes(
			attribute.String("network", network.String()),
			attribute.String("reason", reason),
		))

		// Back off, or bail out immediately on shutdown: scanOnce's bitcoind RPCs
		// take no context, so finishing the schedule could cost ~7s of a SIGTERM.
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(scanRetryDelays[attempt]):
		}
	}
}

func disconnectBlocks(_ context.Context, _ *ent.Client, _ []Tip, _ btcnetwork.Network) error {
	// TODO(DL-100): Add handling for disconnected token withdrawal transactions.
	return nil
}

// commitBlockTransactions performs the per-block two-phase commit: ephemeral first,
// then main. It updates *ephemeralCommitted so connectBlocks' panic-recovery defer
// can detect a panic that lands between the ephemeral commit and the main commit
// (the only window where divergence is possible).
//
// If a callee has already finalized the ephemeral tx inline, GetTxIfExists()
// returns nil and we treat the ephemeral side as committed so the divergence
// sentinel still arms correctly.
func commitBlockTransactions(
	ephemeralSession *txBackedEphemeralSession,
	dbTx *ent.Tx,
	chainTip Tip,
	logger *zap.Logger,
	ephemeralCommitted *bool,
) error {
	*ephemeralCommitted = false
	if ephemeralSession != nil {
		if currentTx := ephemeralSession.GetTxIfExists(); currentTx != nil {
			if commitErr := currentTx.Commit(); commitErr != nil {
				if rollbackErr := dbTx.Rollback(); rollbackErr != nil {
					return errors.Join(commitErr, fmt.Errorf("failed to rollback main transaction after ephemeral commit failure: %w", rollbackErr))
				}
				return commitErr
			}
		}
		*ephemeralCommitted = true
	}
	if commitErr := dbTx.Commit(); commitErr != nil {
		if *ephemeralCommitted {
			logger.With(zap.Error(commitErr)).Sugar().Errorf(
				"Main database commit failed after ephemeral commit at block height %d (hash %s); ephemeral and main DB state have diverged",
				chainTip.Height,
				chainTip.Hash,
			)
			return errors.Join(
				commitErr,
				fmt.Errorf(
					"ephemeral and main DB state have diverged after main database commit failure at block height %d (hash %s)",
					chainTip.Height,
					chainTip.Hash,
				),
				errEphemeralMainDBDiverged,
			)
		}
		return commitErr
	}
	// Both commits succeeded; reset so a panic on a subsequent block in this
	// batch is not misidentified as a divergence from the current block.
	*ephemeralCommitted = false
	return nil
}

// connectBlocks processes each chain tip in order, committing both the ephemeral
// and main transactions per block.
func connectBlocks(
	ctx context.Context,
	config *so.Config,
	dbClient *ent.Client,
	ephemeralDBClient *entephemeral.Client,
	bitcoinClient *rpcclient.Client,
	chainTips []Tip,
	network btcnetwork.Network,
) error {
	logger := logging.GetLoggerFromContext(ctx)

	// Divergence guard: if a panic occurs after ephemeralTx.Commit() but before
	// dbTx.Commit(), re-panic with the sentinel so WatchChain exits rather than
	// continuing on a split-brain state. This must live inside connectBlocks
	// (rather than the caller) so that the local ephemeralCommittedOnLastBlock
	// variable is accessible during stack unwinding.
	ephemeralCommittedOnLastBlock := false
	lastProcessedHeight := int64(0)
	defer func() {
		if r := recover(); r != nil {
			// debug.Stack() is called after recover(), so the stack reflects the deferred
			// frame — not the original panic site. The panic value r still identifies what
			// panicked; locate the exact line by searching for the preceding crash in logs.
			logger.Sugar().Errorf("panic in connectBlocks at height %d (ephemeralCommitted=%v): %v\n%s", lastProcessedHeight, ephemeralCommittedOnLastBlock, r, debug.Stack())
			if ephemeralCommittedOnLastBlock {
				panic(errEphemeralMainDBDiverged)
			}
			panic(r)
		}
	}()

	for _, chainTip := range chainTips {
		ephemeralCommittedOnLastBlock = false
		lastProcessedHeight = chainTip.Height
		blockCtx := ctx

		block, err := bitcoinClient.GetBlockVerboseTx(&chainTip.Hash)
		if err != nil {
			return err
		}
		var txs []wire.MsgTx
		for _, tx := range block.Tx {
			rawTx, err := TxFromRPCTx(tx)
			if err != nil {
				return err
			}
			txs = append(txs, rawTx)
		}

		notifier := ent.NewBufferedNotifier(dbClient)
		blockCtx = ent.InjectNotifier(blockCtx, &notifier)

		dbTx, err := dbClient.Tx(blockCtx)
		if err != nil {
			return err
		}
		var ephemeralTx *entephemeral.Tx
		if ephemeralDBClient != nil {
			ephemeralTx, err = ephemeralDBClient.Tx(blockCtx)
			if err != nil {
				if dbRollbackErr := dbTx.Rollback(); dbRollbackErr != nil {
					return errors.Join(err, fmt.Errorf("failed to rollback main transaction after ephemeral transaction begin failure: %w", dbRollbackErr))
				}
				return err
			}
		}
		blockCtx = ent.Inject(blockCtx, &txBackedSession{tx: dbTx})
		var ephemeralSession *txBackedEphemeralSession
		if ephemeralTx != nil {
			// signing keyshare secret cleanup hooks can fire after the per-block tx has
			// already committed or rolled back, so the session must be able to reopen a
			// fresh tx instead of reusing a finalized handle.
			ephemeralSession = newTxBackedEphemeralSession(ephemeralDBClient, ephemeralTx)
			blockCtx = entephemeral.Inject(blockCtx, ephemeralSession)
		}
		err = handleBlock(
			blockCtx,
			config,
			dbTx.Client(),
			bitcoinClient,
			txs,
			chainTip.Height,
			chainTip.Hash,
			network,
		)
		if err != nil {
			logger.Error("Failed to handle block", zap.Error(err))
			var rollbackErr error
			if ephemeralSession != nil {
				currentEphemeralTx := ephemeralSession.GetTxIfExists()
				if currentEphemeralTx != nil {
					if ephemeralRollbackErr := currentEphemeralTx.Rollback(); ephemeralRollbackErr != nil {
						rollbackErr = errors.Join(
							rollbackErr,
							fmt.Errorf("failed to rollback ephemeral transaction: %w", ephemeralRollbackErr),
						)
					}
				}
			}
			if dbRollbackErr := dbTx.Rollback(); dbRollbackErr != nil {
				rollbackErr = errors.Join(
					rollbackErr,
					fmt.Errorf("failed to rollback main transaction: %w", dbRollbackErr),
				)
			}
			if rollbackErr != nil {
				return errors.Join(err, rollbackErr)
			}
			return err
		}
		if err := commitBlockTransactions(ephemeralSession, dbTx, chainTip, logger, &ephemeralCommittedOnLastBlock); err != nil {
			return err
		}

		err = notifier.Flush(blockCtx)
		if err != nil {
			logger.Error("Failed to flush notifier", zap.Error(err))
		}

		// Record current block height
		if blockHeightGauge != nil {
			blockHeightGauge.Record(blockCtx, chainTip.Height, metric.WithAttributes(
				attribute.String("network", network.String()),
			))
		}
	}
	return nil
}

func TxFromRPCTx(txs btcjson.TxRawResult) (wire.MsgTx, error) {
	rawTxBytes, err := hex.DecodeString(txs.Hex)
	if err != nil {
		return wire.MsgTx{}, err
	}
	r := bytes.NewReader(rawTxBytes)
	var tx wire.MsgTx
	err = tx.Deserialize(r)
	if err != nil {
		return wire.MsgTx{}, err
	}
	return tx, nil
}

type AddressDepositUtxo struct {
	tx     *wire.MsgTx
	amount uint64
	idx    uint32
}

// processTransactions processes a list of transactions and returns:
// - A map of confirmed transaction hashes
// - A list of debited addresses
// - A map of addresses to their UTXOs
func processTransactions(txs []wire.MsgTx, networkParams *chaincfg.Params) (map[[32]byte]bool, []string, map[string][]AddressDepositUtxo, error) {
	confirmedTxHashSet := make(map[[32]byte]bool)
	creditedAddresses := make(map[string]bool)
	addressToUtxoMap := make(map[string][]AddressDepositUtxo)

	for _, tx := range txs {
		for idx, txOut := range tx.TxOut {
			_, addresses, _, err := txscript.ExtractPkScriptAddrs(txOut.PkScript, networkParams)
			if err != nil {
				continue
			}
			for _, address := range addresses {
				creditedAddresses[address.EncodeAddress()] = true
				addressToUtxoMap[address.EncodeAddress()] = append(addressToUtxoMap[address.EncodeAddress()], AddressDepositUtxo{&tx, uint64(txOut.Value), uint32(idx)})
			}
		}
		txid := tx.TxHash()
		confirmedTxHashSet[txid] = true
	}

	return confirmedTxHashSet, slices.Collect(maps.Keys(creditedAddresses)), addressToUtxoMap, nil
}

// Attempts to process all transactions in the block and update the block
// height. If an error occurs, none of the transactions are processed and the block
// height is not updated so the block can be retried.
// Spark chain actions derived from DB state (watchtower broadcasts, coop-exit key tweaks) run in processSparkChainActions after blocks commit.
func handleBlock(
	ctx context.Context,
	config *so.Config,
	dbClient *ent.Client,
	bitcoinClient *rpcclient.Client,
	txs []wire.MsgTx,
	blockHeight int64,
	blockHash chainhash.Hash,
	network btcnetwork.Network,
) error {
	logger := logging.GetLoggerFromContext(ctx)
	start := time.Now()
	logger.Sugar().Infof("Starting to handle block at height %d", blockHeight)

	networkParams, err := network.Params()
	if err != nil {
		return err
	}
	hashBytes := blockHash.CloneBytes()
	_, err = dbClient.BlockHeight.Update().
		SetHeight(blockHeight).
		SetBlockHash(hashBytes).
		Where(blockheight.NetworkEQ(network)).
		Save(ctx)
	if err != nil {
		return err
	}
	handleTokenUpdatesForBlock(ctx, config, bitcoinClient, dbClient, txs, blockHeight, blockHash, network)

	confirmedTxHashSet, creditedAddresses, addressToUtxoMap, err := processTransactions(txs, networkParams)
	if err != nil {
		return err
	}

	// If marking exiting nodes is slow, it can be disabled by setting the knob to 0,
	// but this should be done for a short period of time to avoid any potential double spends.
	if knobs.GetKnobsService(ctx).GetValueTarget(knobs.KnobWatchChainMarkExitingNodesEnabled, new(network.String()), 1.0) > 0 {
		logger.Sugar().Infof("Started processing confirmed transactions for exiting tree nodes at height %d", blockHeight)
		if err := tree.MarkExitingNodes(ctx, dbClient, confirmedTxHashSet, blockHeight); err != nil {
			return fmt.Errorf("failed to mark exiting nodes: %w", err)
		}
	}

	logger.Sugar().Infof("Started processing coop exits at block height %d", blockHeight)
	// TODO: expire pending coop exits after some time so this doesn't become too large
	// Build lists of both normal and reversed TxIDs to handle both endianness
	confirmedTxIDs := make([]st.TxID, 0, len(confirmedTxHashSet)*2)
	for txHashBytes := range confirmedTxHashSet {
		normalTxid, err := st.NewTxIDFromBytes(txHashBytes[:])
		if err != nil {
			return fmt.Errorf("failed to parse normal txid from confirmed tx hash: %w", err)
		}
		confirmedTxIDs = append(confirmedTxIDs, normalTxid)
		reversedTxHashBytes := slices.Clone(txHashBytes[:])
		slices.Reverse(reversedTxHashBytes)
		reversedTxid, err := st.NewTxIDFromBytes(reversedTxHashBytes)
		if err != nil {
			return fmt.Errorf("failed to parse reversed txid from confirmed tx hash: %w", err)
		}
		confirmedTxIDs = append(confirmedTxIDs, reversedTxid)
	}

	if len(confirmedTxIDs) > 0 {
		unconfirmedCoopExits, err := dbClient.CooperativeExit.Query().
			Where(
				cooperativeexit.And(
					cooperativeexit.ConfirmationHeightIsNil(),
					cooperativeexit.ExitTxidIn(confirmedTxIDs...),
				),
			).
			All(ctx)
		if err != nil {
			return fmt.Errorf("failed to query unconfirmed coop exits: %w", err)
		}

		for _, coopExit := range unconfirmedCoopExits {
			if coopExit.KeyTweakedHeight != nil {
				return fmt.Errorf("coop exit %s has KeyTweakedHeight set but ConfirmationHeight not set", coopExit.ID)
			}
			logger.Sugar().Debugf("Found coop exit %s at block height %d", coopExit.ID, blockHeight)
			_, err = coopExit.Update().SetConfirmationHeight(blockHeight).Save(ctx)
			if err != nil {
				return fmt.Errorf("failed to update ConfirmationHeight for coop exit %s: %w", coopExit.ID, err)
			}
		}
	}

	logger.Sugar().Infof("Started storing deposit UTXOs at block height %d", blockHeight)
	err = storeDepositUtxos(ctx, dbClient, creditedAddresses, addressToUtxoMap, network, blockHeight)
	if err != nil {
		return fmt.Errorf("failed to store deposit utxos: %w", err)
	}

	logger.Sugar().Infof("Started processing confirmed deposits at block height %d", blockHeight)
	confirmedDeposits, err := dbClient.DepositAddress.Query().
		Where(depositaddress.ConfirmationHeightIsNil()).
		Where(depositaddress.IsStaticEQ(false)).
		Where(depositaddress.NetworkEQ(network)).
		Where(depositaddress.AddressIn(creditedAddresses...)).
		All(ctx)
	if err != nil {
		return err
	}
	for _, deposit := range confirmedDeposits {
		utxos, ok := addressToUtxoMap[deposit.Address]
		if !ok || len(utxos) == 0 {
			logger.Sugar().Infof("UTXO not found for deposit address %s", deposit.Address)
			continue
		}
		utxo := utxos[0]
		if len(utxos) > 1 {
			logger.Sugar().Warnf("Multiple UTXOs found for a single use deposit address %s, picking the one with the biggest amount", deposit.Address)
			// Find the UTXO with the biggest amount
			utxo = utxos[0]
			for _, u := range utxos[1:] {
				if u.amount > utxo.amount {
					utxo = u
				}
			}
		}
		_, err = dbClient.DepositAddress.UpdateOne(deposit).
			SetConfirmationHeight(blockHeight).
			SetConfirmationTxid(utxo.tx.TxHash().String()).
			Save(ctx)
		if err != nil {
			return err
		}
	}

	logger.Sugar().Infof("Finished handling block height %d", blockHeight)
	blockHeightProcessingTimeHistogram.Record(ctx, time.Since(start).Milliseconds(), metric.WithAttributes(
		attribute.String("network", network.String()),
	))
	return nil
}

// loadDepositAvailabilityCandidates loads deposits that are ready to be marked as available.
// A deposit is ready when:
// - It has a confirmation height set
// - Its confirmation height is at least getNonStaticConfirmationThreshold() blocks old
// - We also check a few blocks back (2) to catch any deposits that may have been missed on previous runs
func loadDepositAvailabilityCandidates(
	ctx context.Context,
	dbClient *ent.Client,
	blockHeight int64,
	dbBlockHeight int64,
	bitcoindConfig so.BitcoindConfig,
) ([]*ent.DepositAddress, error) {
	threshold := getNonStaticConfirmationThreshold(bitcoindConfig)
	// "threshold" is 1-based (i.e., setting it to 1 means the funds are available on the same block as the first confirmation)
	maxConfirmationHeight := blockHeight - threshold + 1
	//TODO(SPARK-289) Set to a constant height after this has been running for awhile
	minConfirmationHeight := min(dbBlockHeight, blockHeight) - threshold - lookbackThreshold
	network, err := btcnetwork.FromString(bitcoindConfig.Network)
	if err != nil {
		return nil, fmt.Errorf("cannot load deposit availability candidates: invalid network %s: %w", bitcoindConfig.Network, err)
	}

	deposits, err := dbClient.DepositAddress.Query().
		Where(depositaddress.IsStaticEQ(false)).
		Where(depositaddress.AvailabilityConfirmedAtIsNil()).
		Where(depositaddress.ConfirmationHeightLTE(maxConfirmationHeight)).
		Where(depositaddress.ConfirmationHeightGTE(minConfirmationHeight)).
		Where(depositaddress.NetworkEQ(network)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	return deposits, nil
}

// setDepositAvailability processes a list of deposit addresses and marks their associated
// trees and nodes as available if all conditions are met.
func setDepositAvailability(
	ctx context.Context,
	dbClient *ent.Client,
	deposits []*ent.DepositAddress,
	network btcnetwork.Network,
) error {
	logger := logging.GetLoggerFromContext(ctx)
	logger.Sugar().Infof("Processing %d deposit availability candidates for network %s", len(deposits), network)

	if len(deposits) == 0 {
		return nil
	}

	// Build a set of confirmed transaction hashes
	// Since these deposits already have confirmation_height set (verified in handleBlock),
	// we just need to track which txids we've seen for the tree availability check
	confirmedTxHashSet := make(map[[32]byte]bool)

	for _, deposit := range deposits {
		if deposit.ConfirmationTxid == "" {
			continue
		}
		txidHash, err := chainhash.NewHashFromStr(deposit.ConfirmationTxid)
		if err != nil {
			logger.Sugar().Warnf("Failed to parse confirmation txid %s: %v", deposit.ConfirmationTxid, err)
			continue
		}

		// No need to verify with RPC - if confirmation_height is set, it was already verified
		var txHashBytes [32]byte
		copy(txHashBytes[:], txidHash.CloneBytes())
		confirmedTxHashSet[txHashBytes] = true
	}

	// Start a database transaction to make all updates atomic
	notifier := ent.NewBufferedNotifier(dbClient)
	ctx = ent.InjectNotifier(ctx, &notifier)

	dbTx, err := dbClient.Tx(ctx)
	if err != nil {
		return err
	}

	// Process each deposit within the transaction
	var failedDeposits []string
	for _, deposit := range deposits {
		err := markDepositAsAvailable(ctx, dbTx.Client(), deposit, confirmedTxHashSet)
		if err != nil {
			logger.Sugar().Warnf("Failed to mark deposit %s as available: %v", deposit.Address, err)
			failedDeposits = append(failedDeposits, deposit.Address)
			// Continue processing other deposits even if one fails
		}
	}

	// Commit the transaction
	err = dbTx.Commit()
	if err != nil {
		logger.Error("Failed to commit deposit availability transaction", zap.Error(err))
		return err
	}

	// Flush notifier after successful commit
	err = notifier.Flush(ctx)
	if err != nil {
		logger.Error("Failed to flush notifier", zap.Error(err))
	}

	if len(failedDeposits) > 0 {
		logger.Sugar().Warnf("Failed to process %d deposits: %v", len(failedDeposits), failedDeposits)
	}

	logger.Sugar().Infof("Finished processing deposit availability candidates")
	return nil
}

// markDepositAsAvailable marks a deposit's tree and tree nodes as available once the deposit
// has been confirmed on-chain and all signatures have been finalized.
func markDepositAsAvailable(
	ctx context.Context,
	dbClient *ent.Client,
	deposit *ent.DepositAddress,
	confirmedTxHashSet map[[32]byte]bool,
) error {
	logger := logging.GetLoggerFromContext(ctx)

	signingKeyShare, err := deposit.QuerySigningKeyshare().Only(ctx)
	if err != nil {
		return err
	}
	treeNode, err := dbClient.TreeNode.Query().
		Where(treenode.HasSigningKeyshareWith(signingkeyshare.ID(signingKeyShare.ID))).
		// FIXME(mhr): Unblocking deployment. Is this what we should do if we encounter a tree node that
		// has already been marked available (e.g. through `FinalizeNodeSignatures`)?
		Where(treenode.StatusIn(st.TreeNodeStatusCreating, st.TreeNodeStatusAvailable)).
		Only(ctx)
	if ent.IsNotFound(err) {
		logger.Sugar().Infof("tree not found in available or creating status for %s", deposit.Address)
		return markDepositAddressUTXOConfirmed(ctx, dbClient, deposit)
	}
	if ent.IsNotSingular(err) {
		logger.Sugar().Warnf("tree has multiple nodes in CREATING and AVAILABLE for %s", deposit.Address)
		return fmt.Errorf("multiple nodes found for deposit address")
	}
	if err != nil {
		return err
	}
	if treeNode.Status == st.TreeNodeStatusAvailable {
		return markDepositAddressUTXOConfirmed(ctx, dbClient, deposit)
	}
	logger.Sugar().Infof("Found tree node %s", treeNode.ID)
	if treeNode.Status != st.TreeNodeStatusCreating {
		logger.Sugar().Infof("Expected tree node status to be creating (was: %s)", treeNode.Status)
	}
	nodeTree, err := treeNode.QueryTree().Only(ctx)
	if err != nil {
		return err
	}
	if nodeTree.Status != st.TreeStatusPending {
		logger.Sugar().Infof("Expected tree status to be pending (was: %s)", nodeTree.Status)
		if nodeTree.Status == st.TreeStatusAvailable || nodeTree.Status == st.TreeStatusExited {
			return markDepositAddressUTXOConfirmed(ctx, dbClient, deposit)
		}
		return nil
	}
	baseTxidHash := nodeTree.BaseTxid.Hash()
	if _, ok := confirmedTxHashSet[baseTxidHash]; !ok {
		logger.Sugar().Debugf("Base txid %s not found in confirmed txids", baseTxidHash)
		for txid := range confirmedTxHashSet {
			logger.Sugar().Debugf("Found confirmed txid %s", chainhash.Hash(txid))
		}
		return nil
	}

	_, err = dbClient.Tree.UpdateOne(nodeTree).
		SetStatus(st.TreeStatusAvailable).
		Save(ctx)
	if err != nil {
		return err
	}

	treeNodes, err := nodeTree.QueryNodes().All(ctx)
	if err != nil {
		return err
	}
	for _, treeNode := range treeNodes {
		if treeNode.Status != st.TreeNodeStatusCreating {
			logger.Sugar().Debugf("Tree node %s is not in creating status", treeNode.ID)
			continue
		}
		if len(treeNode.RawRefundTx) > 0 {
			tx, err := common.TxFromRawTxBytes(treeNode.RawRefundTx)
			if err != nil {
				return err
			}

			// A deposit is a two-step protocol that creates a tree in the first step.
			// In the second step, the operators validate and populate the witness of each tree node.
			// The witness will be valid if and only if it is populated,
			// and this is the only available signal that the deposit is complete.
			if !tx.HasWitness() {
				logger.Sugar().Debugf("Tree node %s has not been signed", treeNode.ID)
				continue
			}

			_, err = dbClient.TreeNode.UpdateOne(treeNode).
				SetStatus(st.TreeNodeStatusAvailable).
				Save(ctx)
			if err != nil {
				return err
			}
		} else {
			_, err = dbClient.TreeNode.UpdateOne(treeNode).
				SetStatus(st.TreeNodeStatusSplitted).
				Save(ctx)
			if err != nil {
				return err
			}
		}
	}

	return markDepositAddressUTXOConfirmed(ctx, dbClient, deposit)
}

func markDepositAddressUTXOConfirmed(
	ctx context.Context,
	dbClient *ent.Client,
	deposit *ent.DepositAddress,
) error {
	logger := logging.GetLoggerFromContext(ctx)

	// There is a race condition that, in practice, should not occur, but
	// if multiple blocks are mined in rapid succession, duplicate gRPC events
	// can be sent. To avoid this, only update availability_confirmed_at if it's
	// already NULL.
	// Note: We use UpdateOne (not Update) to ensure the Ent hooks fire and trigger notifications.
	_, err := dbClient.DepositAddress.UpdateOne(deposit).
		Where(depositaddress.AvailabilityConfirmedAtIsNil()).
		SetAvailabilityConfirmedAt(time.Now()).
		Save(ctx)

	if err != nil {
		if ent.IsNotFound(err) {
			// Deposit was already marked as available by another process - this is fine
			logger.Sugar().Debugf("Deposit %s was already marked as available", deposit.ID)
			return nil
		}
		return fmt.Errorf("failed to mark deposit %s as availability confirmed: %w", deposit.ID, err)
	}

	return nil
}

func storeUtxosForAddress(ctx context.Context, dbClient *ent.Client, address *ent.DepositAddress, utxos []AddressDepositUtxo, network btcnetwork.Network, blockHeight int64) error {
	logger := logging.GetLoggerFromContext(ctx)
	for _, utxo := range utxos {
		// Convert transaction ID string to bytes for storage.
		// Note: Bitcoin transaction IDs are displayed as hex strings with reversed byte order,
		// but we convert it to the byte representation in the database for faster lookup
		// while keeping the reversed byte order.
		txidStringBytes, err := hex.DecodeString(utxo.tx.TxID())
		if err != nil {
			return fmt.Errorf("unable to decode txid for a new utxo: %w", err)
		}
		err = dbClient.Utxo.Create().
			SetTxid(txidStringBytes).
			SetVout(utxo.idx).
			SetAmount(utxo.amount).
			SetPkScript(utxo.tx.TxOut[utxo.idx].PkScript).
			SetNetwork(network).
			SetBlockHeight(blockHeight).
			SetDepositAddress(address).
			OnConflictColumns("network", "txid", "vout").
			UpdateNewValues().
			Exec(ctx)
		if err != nil {
			return fmt.Errorf("unable to store a new utxo: %w", err)
		}
		logger.Sugar().Debugf(
			"Stored an L1 utxo for deposit address %s (txid: %x, vout: %v, amount: %v)",
			address.Address,
			utxo.tx.TxID(),
			utxo.idx,
			utxo.amount,
		)
	}
	return nil
}

func storeDepositUtxos(ctx context.Context, dbClient *ent.Client, creditedAddresses []string, addressToUtxoMap map[string][]AddressDepositUtxo, network btcnetwork.Network, blockHeight int64) error {
	depositAddresses, err := dbClient.DepositAddress.Query().
		Where(depositaddress.NetworkEQ(network)).
		Where(depositaddress.AddressIn(creditedAddresses...)).
		All(ctx)
	if err != nil {
		return err
	}

	for _, address := range depositAddresses {
		if utxos, ok := addressToUtxoMap[address.Address]; ok {
			if err := storeUtxosForAddress(ctx, dbClient, address, utxos, network, blockHeight); err != nil {
				return err
			}
		}
	}
	return nil
}

func tweakKeysForCoopExit(ctx context.Context, config *so.Config, coopExit *ent.CooperativeExit, blockHeight int64) error {
	logger := logging.GetLoggerFromContext(ctx)
	transfer, err := coopExit.QueryTransfer().ForUpdate().Only(ctx)
	if err != nil {
		return fmt.Errorf("failed to query transfer: %w", err)
	}

	if transfer.Status == st.TransferStatusSenderKeyTweaked {
		logger.Sugar().Infof("Transfer %s already tweaked, skipping", transfer.ID)
		return nil
	}

	if transfer.Status != st.TransferStatusSenderInitiatedCoordinator && transfer.Status != st.TransferStatusSenderKeyTweakPending {
		return fmt.Errorf("transfer is not in the expected status for key tweak: %s", transfer.Status)
	}

	transferLeaves, err := transfer.QueryTransferLeaves().All(ctx)
	if err != nil {
		return fmt.Errorf("failed to query transfer leaves: %w", err)
	}
	ctx = ent.FreezeSigningKeyshareSecretDualWriteDecision(ctx)
	for _, leaf := range transferLeaves {
		if leaf.KeyTweak == nil {
			// A prior block's run of this loop already tweaked this leaf and
			// cleared the field but bailed before processing the rest. Skip
			// so subsequent leaves can be tweaked one-per-block until done.
			continue
		}
		keyTweak := &pb.SendLeafKeyTweak{}
		err := proto.Unmarshal(leaf.KeyTweak, keyTweak)
		if err != nil {
			return fmt.Errorf("failed to unmarshal key tweak: %w", err)
		}
		treeNode, err := leaf.QueryLeaf().Only(ctx)
		if err != nil {
			return fmt.Errorf("failed to query leaf: %w", err)
		}
		treeNodeUpdate, err := helper.TweakLeafKeyUpdate(ctx, config, treeNode, keyTweak)
		if err != nil {
			return fmt.Errorf("failed to tweak leaf key: %w", err)
		}
		err = treeNodeUpdate.Exec(ctx)
		if err != nil {
			return fmt.Errorf("failed to update tree node: %w", err)
		}
		_, err = leaf.Update().ClearKeyTweak().Save(ctx)
		if err != nil {
			return fmt.Errorf("failed to clear key tweak: %w", err)
		}
	}

	_, err = transfer.Update().SetStatus(st.TransferStatusSenderKeyTweaked).Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to update transfer status: %w", err)
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to get db: %w", err)
	}
	if err := transferpkg.MarkReceiversClaimPending(ctx, db, transfer.ID); err != nil {
		return fmt.Errorf("failed to mark receivers claim pending for coop-exit transfer %s: %w", transfer.ID, err)
	}

	logger.Sugar().Infof("Successfully tweaked key for coop exit transaction %x at block height %d", coopExit.ExitTxid, blockHeight)
	return nil
}

func getNonStaticConfirmationThreshold(bitcoindConfig so.BitcoindConfig) int64 {
	if bitcoindConfig.NonStaticConfirmationThreshold > 0 {
		return int64(bitcoindConfig.NonStaticConfirmationThreshold)
	}

	return nonStaticDefaultConfirmationThreshold
}
