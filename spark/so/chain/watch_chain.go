package chain

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"log"
	"log/slog"
	"slices"
	"time"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/rpcclient"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightsparkdev/spark-go/common"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent"
	"github.com/lightsparkdev/spark-go/so/ent/blockheight"
	"github.com/lightsparkdev/spark-go/so/ent/cooperativeexit"
	"github.com/lightsparkdev/spark-go/so/ent/depositaddress"
	"github.com/lightsparkdev/spark-go/so/ent/schema"
	"github.com/lightsparkdev/spark-go/so/ent/signingkeyshare"
	"github.com/lightsparkdev/spark-go/so/ent/treenode"
	"github.com/lightsparkdev/spark-go/so/helper"
	"github.com/lightsparkdev/spark-go/so/lrc20"
	"github.com/pebbe/zmq4"
	"google.golang.org/protobuf/proto"
)

func initZmq(endpoint string) (*zmq4.Context, *zmq4.Socket, error) {
	zmqCtx, err := zmq4.NewContext()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create ZMQ context: %v", err)
	}

	subscriber, err := zmqCtx.NewSocket(zmq4.SUB)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create ZMQ socket: %v", err)
	}

	err = subscriber.Connect(endpoint)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to connect to ZMQ endpoint: %v", err)
	}

	err = subscriber.SetSubscribe("rawblock")
	if err != nil {
		return nil, nil, fmt.Errorf("failed to set ZMQ subscription: %v", err)
	}

	return zmqCtx, subscriber, nil
}

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
	dbClient *ent.Client,
	soConfig so.Config,
	client *rpcclient.Client,
	network common.Network,
) error {
	logger := slog.Default().With("method", "watch_chain.scanChainUpdates", "network", network.String())
	latestBlockHeight, err := client.GetBlockCount()
	if err != nil {
		return fmt.Errorf("failed to get block count: %v", err)
	}
	latestBlockHash, err := client.GetBlockHash(latestBlockHeight)
	if err != nil {
		return fmt.Errorf("failed to get block hash: %v", err)
	}
	latestChainTip := NewTip(latestBlockHeight, *latestBlockHash)

	ctx := context.Background()
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
	dbBlockHash, err := client.GetBlockHash(dbBlockHeight.Height)
	if err != nil {
		return fmt.Errorf("failed to get block hash: %v", err)
	}

	dbChainTip := NewTip(dbBlockHeight.Height, *dbBlockHash)
	difference, err := findDifference(dbChainTip, latestChainTip, client)
	if err != nil {
		return fmt.Errorf("failed to find difference: %v", err)
	}
	err = disconnectBlocks(ctx, dbClient, difference.Disconnected, network)
	if err != nil {
		return fmt.Errorf("failed to disconnect blocks: %v", err)
	}
	err = connectBlocks(ctx,
		&soConfig,
		dbClient, client, difference.Connected, network)
	if err != nil {
		return fmt.Errorf("failed to connect blocks: %v", err)
	}
	return nil
}

func WatchChain(dbClient *ent.Client,
	soConfig so.Config,
	bitcoindConfig so.BitcoindConfig,
) error {
	logger := slog.Default().With("method", "watch_chain.WatchChain")
	network, err := common.NetworkFromString(bitcoindConfig.Network)
	if err != nil {
		return err
	}
	connConfig := helper.RPCClientConfig(bitcoindConfig)
	client, err := rpcclient.New(&connConfig, nil)
	if err != nil {
		return err
	}

	err = scanChainUpdates(dbClient, soConfig, client, network)
	if err != nil {
		return fmt.Errorf("failed to scan chain updates: %v", err)
	}

	zmqCtx, subscriber, err := initZmq(bitcoindConfig.ZmqPubRawBlock)
	if err != nil {
		return err
	}
	defer func() {
		err := zmqCtx.Term()
		if err != nil {
			log.Fatalf("Failed to terminate ZMQ context: %v", err)
		}
		err = subscriber.Close()
		if err != nil {
			log.Fatalf("Failed to close ZMQ subscriber: %v", err)
		}
	}()

	logger.Info("Listening for block notifications via ZMQ endpoint", "endpoint", bitcoindConfig.ZmqPubRawBlock)

	newBlockNotification := make(chan struct{})
	go func() {
		for {
			_, err := subscriber.RecvMessage(0)
			if err != nil {
				log.Fatalf("Failed to receive message: %v", err)
			}
			newBlockNotification <- struct{}{}
		}
	}()

	// TODO: we should consider alerting on errors within this loop
	for {
		select {
		case <-newBlockNotification:
		case <-time.After(pollInterval(network)):
		}
		// We don't actually do anything with the block receive since
		// we need to query bitcoind for the height anyway. We just
		// treat it as a notification that a new block appeared.

		err = scanChainUpdates(dbClient, soConfig, client, network)
		if err != nil {
			logger.Error("Failed to scan chain updates", "error", err)
		}
	}
}

func disconnectBlocks(_ context.Context, _ *ent.Client, _ []Tip, _ common.Network) error {
	// TODO(DL-100): Add handling for disconnected token withdrawal transactions.
	return nil
}

func connectBlocks(ctx context.Context, soConfig *so.Config, dbClient *ent.Client, client *rpcclient.Client, chainTips []Tip, network common.Network) error {
	logger := slog.Default().With("method", "watch_chain.connectBlocks").With("network", network.String())
	for _, chainTip := range chainTips {
		blockHash, err := client.GetBlockHash(chainTip.Height)
		if err != nil {
			return err
		}
		block, err := client.GetBlockVerboseTx(blockHash)
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
			soConfig,
			dbTx, txs, chainTip.Height,
			blockHash,
			network)
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

func handleBlock(ctx context.Context,
	soConfig *so.Config,
	dbTx *ent.Tx, txs []wire.MsgTx, blockHeight int64, blockHash *chainhash.Hash,
	network common.Network,
) error {
	logger := slog.Default().With("method", "watch_chain.handleBlock")
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
	for _, tx := range txs {
		for _, txOut := range tx.TxOut {
			_, addresses, _, err := txscript.ExtractPkScriptAddrs(txOut.PkScript, networkParams)
			if err != nil {
				return err
			}
			for _, address := range addresses {
				debitedAddresses = append(debitedAddresses, address.EncodeAddress())
			}
		}
		txid := tx.TxHash()
		confirmedTxHashSet[txid] = true
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

	confirmedDeposits, err := dbTx.DepositAddress.Query().
		Where(depositaddress.ConfirmationHeightIsNil()).
		Where(depositaddress.AddressIn(debitedAddresses...)).
		All(ctx)
	if err != nil {
		return err
	}
	for _, deposit := range confirmedDeposits {
		// TODO: only unlock if deposit reaches X confirmations
		signingKeyShare, err := deposit.QuerySigningKeyshare().Only(ctx)
		if err != nil {
			return err
		}
		treeNode, err := dbTx.TreeNode.Query().
			Where(treenode.HasSigningKeyshareWith(signingkeyshare.ID(signingKeyShare.ID))).
			Only(ctx)
		if ent.IsNotFound(err) {
			logger.Info("Deposit confirmed before tree creation", "address", deposit.Address)
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
			} else {
				_, err = dbTx.TreeNode.UpdateOne(treeNode).
					SetStatus(schema.TreeNodeStatusSplitted).
					Save(ctx)
				if err != nil {
					return err
				}
			}
		}
		_, err = dbTx.DepositAddress.UpdateOne(deposit).
			SetConfirmationHeight(blockHeight).
			Save(ctx)
		if err != nil {
			return err
		}
	}

	logger.Info("Checking for withdrawn token leaves in block", "height", blockHeight)

	// Use the lrc20 client to sync withdrawn leaves - it will handle all the processing internally
	lrc20Client := lrc20.NewClient(soConfig)
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
