package chain

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"slices"
	"time"

	"github.com/btcsuite/btcd/btcjson"
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
	"github.com/lightsparkdev/spark/so/ent/schema"
	"github.com/lightsparkdev/spark/so/ent/signingkeyshare"
	"github.com/lightsparkdev/spark/so/ent/treenode"
	"github.com/lightsparkdev/spark/so/helper"
	"github.com/lightsparkdev/spark/so/lrc20"
	events "github.com/lightsparkdev/spark/so/stream"
	"github.com/lightsparkdev/spark/so/watchtower"
	"google.golang.org/protobuf/proto"
)

func pollInterval(network common.Network) time.Duration {
	switch network {
	case common.Mainnet:
		return 1 * time.Minute
	case common.Testnet:
		return 1 * time.Minute
	case common.Regtest:
		return 3 * time.Second
	case common.Signet:
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
	disconnected := []Tip{}
	connected := []Tip{}

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
	dbClient *ent.Client,
	bitcoinClient *rpcclient.Client,
	lrc20Client *lrc20.Client,
	network common.Network,
) error {
	logger := logging.GetLoggerFromContext(ctx)

	latestBlockHeight, err := bitcoinClient.GetBlockCount()
	if err != nil {
		return fmt.Errorf("failed to get block count: %v", err)
	}
	latestBlockHash, err := bitcoinClient.GetBlockHash(latestBlockHeight)
	if err != nil {
		return fmt.Errorf("failed to get block hash: %v", err)
	}
	latestChainTip := NewTip(latestBlockHeight, *latestBlockHash)

	entNetwork := common.SchemaNetwork(network)
	dbBlockHeight, err := dbClient.BlockHeight.Query().
		Where(blockheight.NetworkEQ(entNetwork)).
		Only(ctx)
	if ent.IsNotFound(err) {
		startHeight := max(0, latestBlockHeight-6)
		logger.Info("Block height not found, creating new entry", "height", startHeight)
		dbBlockHeight, err = dbClient.BlockHeight.Create().SetHeight(startHeight).SetNetwork(entNetwork).Save(ctx)
	}
	if err != nil {
		return fmt.Errorf("failed to query block height: %v", err)
	}
	dbBlockHash, err := bitcoinClient.GetBlockHash(dbBlockHeight.Height)
	if err != nil {
		return fmt.Errorf("failed to get block hash: %v", err)
	}

	dbChainTip := NewTip(dbBlockHeight.Height, *dbBlockHash)
	difference, err := findDifference(dbChainTip, latestChainTip, bitcoinClient)
	if err != nil {
		return fmt.Errorf("failed to find difference: %v", err)
	}
	err = disconnectBlocks(ctx, dbClient, difference.Disconnected, network)
	if err != nil {
		return fmt.Errorf("failed to disconnect blocks: %v", err)
	}
	err = connectBlocks(
		ctx,
		dbClient,
		bitcoinClient,
		lrc20Client,
		difference.Connected,
		network,
	)
	if err != nil {
		return fmt.Errorf("failed to connect blocks: %v", err)
	}
	return nil
}

func RPCClientConfig(cfg so.BitcoindConfig) rpcclient.ConnConfig {
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
	dbClient *ent.Client,
	lrc20Client *lrc20.Client,
	bitcoindConfig so.BitcoindConfig,
) error {
	logger := logging.GetLoggerFromContext(ctx)

	network, err := common.NetworkFromString(bitcoindConfig.Network)
	if err != nil {
		return err
	}
	connConfig := RPCClientConfig(bitcoindConfig)
	bitcoinClient, err := rpcclient.New(&connConfig, nil)
	if err != nil {
		return err
	}

	err = scanChainUpdates(ctx, dbClient, bitcoinClient, lrc20Client, network)
	if err != nil {
		return fmt.Errorf("failed to scan chain updates: %v", err)
	}

	zmqSubscriber, err := NewZmqSubscriber()
	if err != nil {
		return err
	}

	defer func() {
		err := zmqSubscriber.Close()
		if err != nil {
			logger.Warn("Failed to close ZMQ subscriber", "error", err)
		}
	}()

	newBlockNotification, errChan, err := zmqSubscriber.Subscribe(ctx, bitcoindConfig.ZmqPubRawBlock, "rawblock")
	if err != nil {
		return err
	}

	// TODO: we should consider alerting on errors within this loop
	for {
		select {
		case err := <-errChan:
			logger.Error("Error receiving ZMQ message", "error", err)
			return err
		case <-ctx.Done():
			logger.Info("Context done, stopping chain watcher")
			return nil
		case <-newBlockNotification:
		case <-time.After(pollInterval(network)):
		}
		// We don't actually do anything with the block receive since
		// we need to query bitcoind for the height anyway. We just
		// treat it as a notification that a new block appeared.

		err = scanChainUpdates(ctx, dbClient, bitcoinClient, lrc20Client, network)
		if err != nil {
			logger.Error("Failed to scan chain updates", "error", err)
		}
	}
}

func disconnectBlocks(_ context.Context, _ *ent.Client, _ []Tip, _ common.Network) error {
	// TODO(DL-100): Add handling for disconnected token withdrawal transactions.
	return nil
}

func connectBlocks(
	ctx context.Context,
	dbClient *ent.Client,
	bitcoinClient *rpcclient.Client,
	lrc20Client *lrc20.Client,
	chainTips []Tip,
	network common.Network,
) error {
	logger := logging.GetLoggerFromContext(ctx)

	for _, chainTip := range chainTips {
		blockHash, err := bitcoinClient.GetBlockHash(chainTip.Height)
		if err != nil {
			return err
		}
		block, err := bitcoinClient.GetBlockVerboseTx(blockHash)
		if err != nil {
			return err
		}
		txs := []wire.MsgTx{}
		for _, tx := range block.Tx {
			rawTx, err := TxFromRPCTx(tx)
			if err != nil {
				return err
			}
			txs = append(txs, rawTx)
		}

		dbTx, err := dbClient.Tx(ctx)
		if err != nil {
			return err
		}
		err = handleBlock(ctx,
			lrc20Client,
			dbTx,
			bitcoinClient,
			txs,
			chainTip.Height,
			blockHash,
			network,
		)
		if err != nil {
			logger.Error("Failed to handle block", "error", err)
			rollbackErr := dbTx.Rollback()
			if rollbackErr != nil {
				return rollbackErr
			}
			return err
		}
		err = dbTx.Commit()
		if err != nil {
			return err
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

func handleBlock(
	ctx context.Context,
	lrc20Client *lrc20.Client,
	dbTx *ent.Tx,
	bitcoinClient *rpcclient.Client,
	txs []wire.MsgTx,
	blockHeight int64,
	blockHash *chainhash.Hash,
	network common.Network,
) error {
	logger := logging.GetLoggerFromContext(ctx)

	networkParams := common.NetworkParams(network)
	_, err := dbTx.BlockHeight.Update().
		SetHeight(blockHeight).
		Where(blockheight.NetworkEQ(common.SchemaNetwork(network))).
		Save(ctx)
	if err != nil {
		return err
	}

	confirmedTxHashSet := make(map[[32]byte]bool)
	debitedAddresses := make([]string, 0)
	addressToUtxoMap := make(map[string]AddressDepositUtxo)
	for _, tx := range txs {
		for idx, txOut := range tx.TxOut {
			_, addresses, _, err := txscript.ExtractPkScriptAddrs(txOut.PkScript, networkParams)
			if err != nil {
				return err
			}
			for _, address := range addresses {
				debitedAddresses = append(debitedAddresses, address.EncodeAddress())
				addressToUtxoMap[address.EncodeAddress()] = AddressDepositUtxo{&tx, uint64(txOut.Value), uint32(idx)}
			}
		}
		txid := tx.TxHash()
		confirmedTxHashSet[txid] = true
	}

	// Fetch nodes with a confirmed parent and unconfirmed node/refund TX
	nodes, err := dbTx.TreeNode.Query().
		Where(
			treenode.Or(
				// Root nodes that need node confirmation or refund confirmation
				treenode.And(
					treenode.Not(treenode.HasParent()),
					treenode.Or(
						treenode.NodeConfirmationHeightIsNil(),
						treenode.RefundConfirmationHeightIsNil(),
					),
				),
				// Child nodes with confirmed parent that need node confirmation
				treenode.And(
					treenode.HasParentWith(treenode.NodeConfirmationHeightNotNil()),
					treenode.NodeConfirmationHeightIsNil(),
				),
				// Nodes with confirmed node tx and refund tx that need refund confirmation
				treenode.And(
					treenode.NodeConfirmationHeightNotNil(),
					treenode.RefundConfirmationHeightIsNil(),
				),
			),
		).
		All(ctx)
	if err != nil {
		return fmt.Errorf("failed to query nodes: %v", err)
	}

	for _, node := range nodes {
		tx, err := common.TxFromRawTxBytes(node.RawTx)
		if err != nil {
			return fmt.Errorf("failed to parse node tx: %v", err)
		}

		txid := tx.TxHash()
		if confirmedTxHashSet[txid] {
			_, err = dbTx.TreeNode.UpdateOne(node).
				SetNodeConfirmationHeight(uint64(blockHeight)).
				Save(ctx)
			if err != nil {
				return fmt.Errorf("failed to update node status: %v", err)
			}
		}

		if len(node.RawRefundTx) > 0 {
			refundTx, err := common.TxFromRawTxBytes(node.RawRefundTx)
			if err != nil {
				return fmt.Errorf("failed to parse node tx: %v", err)
			}

			refundTxid := refundTx.TxHash()
			if confirmedTxHashSet[refundTxid] {
				_, err = dbTx.TreeNode.UpdateOne(node).
					SetRefundConfirmationHeight(uint64(blockHeight)).
					Save(ctx)
				if err != nil {
					return fmt.Errorf("failed to update node status: %v", err)
				}
			}
		}

		// Check if node or refund TX timelock has expired
		if err := watchtower.CheckExpiredTimeLocks(ctx, bitcoinClient, node, blockHeight); err != nil {
			logger.Error("Failed to check expired time locks", "error", err)
		}
	}

	// TODO: expire pending coop exits after some time so this doesn't become too large
	pendingCoopExits, err := dbTx.CooperativeExit.Query().Where(cooperativeexit.ConfirmationHeightIsNil()).All(ctx)
	if err != nil {
		return err
	}
	for _, coopExit := range pendingCoopExits {
		txHash := coopExit.ExitTxid
		slices.Reverse(txHash)
		if _, ok := confirmedTxHashSet[[32]byte(txHash)]; !ok {
			continue
		}
		err = handleCoopExitConfirmation(ctx, coopExit, blockHeight)
		if err != nil {
			return fmt.Errorf("failed to handle coop exit confirmation: %v", err)
		}
	}

	// Handle static deposits
	staticDepositAddresses, err := dbTx.DepositAddress.Query().
		Where(depositaddress.IsStaticEQ(true)).
		Where(depositaddress.AddressIn(debitedAddresses...)).
		All(ctx)
	if err != nil {
		return err
	}

	for _, address := range staticDepositAddresses {
		if utxo, ok := addressToUtxoMap[address.Address]; ok {
			txidBytes, err := hex.DecodeString(utxo.tx.TxID())
			if err != nil {
				return fmt.Errorf("unable to decode txid for a new utxo: %v", err)
			}
			_, err = dbTx.Utxo.Create().
				SetTxid(txidBytes).
				SetVout(uint32(utxo.idx)).
				SetAmount(utxo.amount).
				SetPkScript(utxo.tx.TxOut[utxo.idx].PkScript).
				SetNetwork(common.SchemaNetwork(network)).
				SetBlockHeight(blockHeight).
				SetDepositAddress(address).
				Save(ctx)
			if err != nil {
				return fmt.Errorf("unable to store a new utxo: %v", err)
			}
			logger.Debug("Stored an L1 utxo to a static deposit address", "address", address.Address, "txid", hex.EncodeToString(txidBytes), "amount", utxo.amount)
		}
	}

	confirmedDeposits, err := dbTx.DepositAddress.Query().
		Where(depositaddress.ConfirmationHeightIsNil()).
		Where(depositaddress.IsStaticEQ(false)).
		Where(depositaddress.AddressIn(debitedAddresses...)).
		All(ctx)
	if err != nil {
		return err
	}
	for _, deposit := range confirmedDeposits {
		// TODO: only unlock if deposit reaches X confirmations
		utxo, ok := addressToUtxoMap[deposit.Address]
		if !ok {
			logger.Info("UTXO not found for deposit address", "address", deposit.Address)
			continue
		}
		_, err = dbTx.DepositAddress.UpdateOne(deposit).
			SetConfirmationHeight(blockHeight).
			SetConfirmationTxid(utxo.tx.TxHash().String()).
			Save(ctx)
		if err != nil {
			return err
		}
		signingKeyShare, err := deposit.QuerySigningKeyshare().Only(ctx)
		if err != nil {
			return err
		}
		treeNode, err := dbTx.TreeNode.Query().
			Where(treenode.HasSigningKeyshareWith(signingkeyshare.ID(signingKeyShare.ID))).
			// FIXME(mhr): Unblocking deployment. Is this what we should do if we encounter a tree node that
			// has already been marked available (e.g. through `FinalizeNodeSignatures`)?
			Where(treenode.StatusEQ(schema.TreeNodeStatusCreating)).
			Only(ctx)
		if ent.IsNotFound(err) {
			logger.Info("Deposit confirmed before tree creation or tree already available", "address", deposit.Address)
			continue
		}
		if err != nil {
			return err
		}
		logger.Info("Found tree node", "node", treeNode.ID)
		if treeNode.Status != schema.TreeNodeStatusCreating {
			logger.Info("Expected tree node status to be creating", "status", treeNode.Status)
		}
		tree, err := treeNode.QueryTree().Only(ctx)
		if err != nil {
			return err
		}
		if tree.Status != schema.TreeStatusPending {
			logger.Info("Expected tree status to be pending", "status", tree.Status)
			continue
		}
		if _, ok := confirmedTxHashSet[[32]byte(tree.BaseTxid)]; !ok {
			logger.Debug("Base txid not found in confirmed txids", "base_txid", hex.EncodeToString(tree.BaseTxid))
			for txid := range confirmedTxHashSet {
				logger.Debug("confirmed txid", "txid", hex.EncodeToString(txid[:]))
			}
			continue
		}

		_, err = dbTx.Tree.UpdateOne(tree).
			SetStatus(schema.TreeStatusAvailable).
			Save(ctx)
		if err != nil {
			return err
		}

		treeNodes, err := tree.QueryNodes().All(ctx)
		if err != nil {
			return err
		}
		for _, treeNode := range treeNodes {
			if treeNode.Status != schema.TreeNodeStatusCreating {
				logger.Debug("Tree node is not in creating status", "node", treeNode.ID)
				continue
			}
			if len(treeNode.RawRefundTx) > 0 {
				_, err = dbTx.TreeNode.UpdateOne(treeNode).
					SetStatus(schema.TreeNodeStatusAvailable).
					Save(ctx)
				if err != nil {
					return err
				}
				treeNodeProto, err := treeNode.MarshalSparkProto(ctx)
				if err != nil {
					return err
				}

				eventRouter := events.GetDefaultRouter()
				err = eventRouter.NotifyUser(treeNode.OwnerIdentityPubkey, &pb.SubscribeToEventsResponse{
					Event: &pb.SubscribeToEventsResponse_Deposit{
						Deposit: &pb.DepositEvent{
							Deposit: treeNodeProto,
						},
					},
				})
				if err != nil {
					logger.Error("Failed to notify user of deposit event", "error", err, "identity_public_key", logging.Pubkey{Pubkey: treeNode.OwnerIdentityPubkey})
				}
			} else {
				_, err = dbTx.TreeNode.UpdateOne(treeNode).
					SetStatus(schema.TreeNodeStatusSplitted).
					Save(ctx)
				if err != nil {
					return err
				}
			}
		}
	}

	logger.Info("Checking for withdrawn token leaves in block", "height", blockHeight)

	// Use the lrc20 client to sync withdrawn leaves - it will handle all the processing internally
	err = lrc20Client.MarkWithdrawnTokenOutputs(ctx, network, dbTx, blockHash)
	if err != nil {
		logger.Error("Failed to sync withdrawn leaves", "error", err)
		return err
	}

	return nil
}

func handleCoopExitConfirmation(ctx context.Context, coopExit *ent.CooperativeExit, blockHeight int64) error {
	transfer, err := coopExit.QueryTransfer().Only(ctx)
	if err != nil {
		return fmt.Errorf("failed to query transfer: %v", err)
	}
	transferLeaves, err := transfer.QueryTransferLeaves().All(ctx)
	if err != nil {
		return fmt.Errorf("failed to query transfer leaves: %v", err)
	}
	for _, leaf := range transferLeaves {
		keyTweak := &pb.SendLeafKeyTweak{}
		err := proto.Unmarshal(leaf.KeyTweak, keyTweak)
		if err != nil {
			return fmt.Errorf("failed to unmarshal key tweak: %v", err)
		}
		treeNode, err := leaf.QueryLeaf().Only(ctx)
		if err != nil {
			return fmt.Errorf("failed to query leaf: %v", err)
		}
		err = helper.TweakLeafKey(ctx, treeNode, keyTweak, nil)
		if err != nil {
			return fmt.Errorf("failed to tweak leaf key: %v", err)
		}
		_, err = leaf.Update().SetKeyTweak(nil).Save(ctx)
		if err != nil {
			return fmt.Errorf("failed to clear key tweak: %v", err)
		}
	}

	_, err = transfer.Update().SetStatus(schema.TransferStatusSenderKeyTweaked).Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to update transfer status: %v", err)
	}

	_, err = coopExit.Update().SetConfirmationHeight(blockHeight).Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to update coop exit: %v", err)
	}
	return nil
}
