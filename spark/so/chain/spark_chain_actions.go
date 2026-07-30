package chain

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
	"time"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/rpcclient"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/blockheight"
	"github.com/lightsparkdev/spark/so/ent/cooperativeexit"
	"github.com/lightsparkdev/spark/so/ent/transfer"
	"github.com/lightsparkdev/spark/so/entephemeral"
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/lightsparkdev/spark/so/watchtower"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.uber.org/zap"
)

// processSparkChainActions performs the Spark protocol actions warranted by the
// current confirmed chain height — watchtower broadcasts and coop-exit key
// tweaks. Everything here is derived from current DB state, not block contents,
// so a failed or crashed run self-heals: chain_action_height only advances on a
// fully successful run, and the caller invokes this on every scan tick, retrying
// until the cursor catches up to the connected tip.
func processSparkChainActions(
	ctx context.Context,
	config *so.Config,
	dbClient *ent.Client,
	ephemeralDBClient *entephemeral.Client,
	bitcoinClient *rpcclient.Client,
	network btcnetwork.Network,
) error {
	dbBlockHeight, err := dbClient.BlockHeight.Query().
		Where(blockheight.NetworkEQ(network)).
		Only(ctx)
	if err != nil {
		return fmt.Errorf("failed to query block height for Spark chain actions: %w", err)
	}
	blockHeight := dbBlockHeight.Height
	if dbBlockHeight.ChainActionHeight != nil && *dbBlockHeight.ChainActionHeight >= blockHeight {
		return nil
	}
	var tipHash chainhash.Hash
	if dbBlockHeight.BlockHash != nil && len(*dbBlockHeight.BlockHash) == chainhash.HashSize {
		copy(tipHash[:], *dbBlockHeight.BlockHash)
	}

	start := time.Now()
	defer func() {
		sparkChainActionTimeHistogram.Record(ctx, time.Since(start).Milliseconds(), metric.WithAttributes(
			attribute.String("network", network.String()),
		))
	}()

	var errs []error
	if processNodesForWatchtowersEnabled(config, network) {
		if err := processWatchtowerBroadcasts(ctx, dbClient, bitcoinClient, blockHeight, network); err != nil {
			errs = append(errs, err)
		}
	}
	if err := tweakEligibleCoopExits(ctx, config, dbClient, ephemeralDBClient, NewTip(blockHeight, tipHash), network); err != nil {
		errs = append(errs, err)
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}

	// Advance to the height this run used (not the current row value): a block
	// connecting mid-run leaves the cursor behind and triggers another pass.
	// Per-item failures above return nil by design — they retry on the next
	// block via their eligibility queries, not by holding the cursor back.
	if _, err := dbClient.BlockHeight.UpdateOne(dbBlockHeight).SetChainActionHeight(blockHeight).Save(ctx); err != nil {
		return fmt.Errorf("failed to advance chain action height: %w", err)
	}
	return nil
}

func processNodesForWatchtowersEnabled(config *so.Config, network btcnetwork.Network) bool {
	if bitcoinConfig, ok := config.BitcoindConfigs[strings.ToLower(network.String())]; ok {
		if bitcoinConfig.ProcessNodesForWatchtowers != nil {
			return *bitcoinConfig.ProcessNodesForWatchtowers
		}
	}
	return true
}

func processWatchtowerBroadcasts(
	ctx context.Context,
	dbClient *ent.Client,
	bitcoinClient *rpcclient.Client,
	blockHeight int64,
	network btcnetwork.Network,
) error {
	logger := logging.GetLoggerFromContext(ctx)
	logger.Sugar().Infof("Started processing nodes & transfer leaves for watchtowers at block height %d", blockHeight)
	nodes, err := watchtower.QueryBroadcastableNodes(ctx, dbClient, blockHeight, network)
	if err != nil {
		return fmt.Errorf("failed to query nodes: %w", err)
	}
	eligibleNodesGauge.Record(ctx, int64(len(nodes)), metric.WithAttributes(
		attribute.String("network", network.String()),
	))
	for _, node := range nodes {
		if err := watchtower.CheckExpiredTimeLocks(ctx, dbClient, bitcoinClient, node, blockHeight, network); err != nil {
			logger.Sugar().Errorf("Failed to check expired time locks for node %s: %v", node.ID, err)
		}
	}

	transferLeaves, err := watchtower.QueryBroadcastableTransferLeaves(ctx, dbClient, network)
	if err != nil {
		return fmt.Errorf("failed to query transfer leaves: %w", err)
	}
	for _, transferLeaf := range transferLeaves {
		leaf := transferLeaf.Edges.Leaf
		if leaf == nil {
			logger.Sugar().Errorf("Transfer leaf %s has no leaf edge (expected with WithLeaf())", transferLeaf.ID)
			continue
		}
		if err := watchtower.BroadcastTransferLeafRefund(ctx, bitcoinClient, transferLeaf, leaf.NodeConfirmationHeight, network, blockHeight); err != nil {
			logger.Sugar().Errorf("Failed to broadcast intermediate refund for transfer leaf %s: %v", transferLeaf.ID, err)
		}
	}
	return nil
}

func tweakEligibleCoopExits(
	ctx context.Context,
	config *so.Config,
	dbClient *ent.Client,
	ephemeralDBClient *entephemeral.Client,
	chainTip Tip,
	network btcnetwork.Network,
) error {
	logger := logging.GetLoggerFromContext(ctx)
	coopExits, err := dbClient.CooperativeExit.Query().
		Where(
			cooperativeexit.ConfirmationHeightNotNil(),
			cooperativeexit.KeyTweakedHeightIsNil(),
			cooperativeexit.HasTransferWith(transfer.NetworkEQ(network)),
		).
		All(ctx)
	if err != nil {
		return fmt.Errorf("failed to query coop exits to tweak: %w", err)
	}

	requiredConfirmations := int64(knobs.CoopExitConfirmationThreshold)
	for _, coopExit := range coopExits {
		if chainTip.Height-*coopExit.ConfirmationHeight+1 < requiredConfirmations {
			continue
		}
		err := tweakCoopExitInOwnTx(ctx, config, dbClient, ephemeralDBClient, coopExit.ID, chainTip)
		if errors.Is(err, errEphemeralMainDBDiverged) {
			return err
		}
		if err != nil {
			// Logged, not returned: KeyTweakedHeight is still nil so the exit stays
			// eligible and is retried on the next run.
			logger.With(zap.Error(err)).Sugar().Errorf("Failed to handle transfer key tweak for coop exit %s", coopExit.ID)
		}
	}
	return nil
}

// tweakCoopExitInOwnTx runs one coop exit's key tweak in its own main+ephemeral
// transaction pair: the tweak's main-DB effects and the KeyTweakedHeight marker
// commit in a single main transaction, so a failed tweak leaves the exit
// eligible for retry and one failing exit cannot roll back the others.
// Cross-DB atomicity is not possible — the ephemeral commit lands first, and a
// main commit failure after it is fatal divergence (errEphemeralMainDBDiverged),
// exactly as in block processing.
func tweakCoopExitInOwnTx(
	ctx context.Context,
	config *so.Config,
	dbClient *ent.Client,
	ephemeralDBClient *entephemeral.Client,
	coopExitID uuid.UUID,
	chainTip Tip,
) error {
	logger := logging.GetLoggerFromContext(ctx)

	// Divergence guard mirroring connectBlocks: a panic landing between the
	// ephemeral commit and the main commit must be reclassified so the watcher
	// dies with the sentinel instead of silently retrying on restart against
	// already-committed ephemeral writes.
	ephemeralCommitted := false
	defer func() {
		if r := recover(); r != nil {
			logger.Sugar().Errorf("panic during key tweak for coop exit %s (ephemeralCommitted=%v): %v\n%s", coopExitID, ephemeralCommitted, r, debug.Stack())
			if ephemeralCommitted {
				panic(errEphemeralMainDBDiverged)
			}
			panic(r)
		}
	}()

	notifier := ent.NewBufferedNotifier(dbClient)
	ctx = ent.InjectNotifier(ctx, &notifier)

	dbTx, err := dbClient.Tx(ctx)
	if err != nil {
		return err
	}
	var ephemeralSession *txBackedEphemeralSession
	if ephemeralDBClient != nil {
		ephemeralTx, err := ephemeralDBClient.Tx(ctx)
		if err != nil {
			if dbRollbackErr := dbTx.Rollback(); dbRollbackErr != nil {
				return errors.Join(err, fmt.Errorf("failed to rollback main transaction after ephemeral transaction begin failure: %w", dbRollbackErr))
			}
			return err
		}
		ephemeralSession = newTxBackedEphemeralSession(ephemeralDBClient, ephemeralTx)
		ctx = entephemeral.Inject(ctx, ephemeralSession)
	}
	ctx = ent.Inject(ctx, &txBackedSession{tx: dbTx})

	rollback := func() {
		if ephemeralSession != nil {
			if tx := ephemeralSession.GetTxIfExists(); tx != nil {
				if rbErr := tx.Rollback(); rbErr != nil {
					logger.Error("Failed to rollback ephemeral transaction for coop exit tweak", zap.Error(rbErr))
				}
			}
		}
		if rbErr := dbTx.Rollback(); rbErr != nil {
			logger.Error("Failed to rollback main transaction for coop exit tweak", zap.Error(rbErr))
		}
	}

	// Re-fetch inside the transaction so the entity and every query the tweak
	// makes run against this tx. No row lock: the watcher is single-goroutine
	// per network, and a concurrent tweak from another path is serialized by
	// the transfer ForUpdate inside tweakKeysForCoopExit.
	coopExit, err := dbTx.Client().CooperativeExit.Query().
		Where(
			cooperativeexit.ID(coopExitID),
			cooperativeexit.KeyTweakedHeightIsNil(),
		).
		Only(ctx)
	if ent.IsNotFound(err) {
		rollback()
		return nil
	}
	if err != nil {
		rollback()
		return fmt.Errorf("failed to load coop exit %s: %w", coopExitID, err)
	}
	if err := tweakKeysForCoopExitFunc(ctx, config, coopExit, chainTip.Height); err != nil {
		rollback()
		return err
	}
	if _, err := dbTx.Client().CooperativeExit.UpdateOne(coopExit).SetKeyTweakedHeight(chainTip.Height).Save(ctx); err != nil {
		rollback()
		return fmt.Errorf("failed to update KeyTweakedHeight for coop exit %s: %w", coopExitID, err)
	}

	if err := commitBlockTransactions(ephemeralSession, dbTx, chainTip, logger, &ephemeralCommitted); err != nil {
		return err
	}
	if err := notifier.Flush(ctx); err != nil {
		logger.Error("Failed to flush notifier", zap.Error(err))
	}
	return nil
}
