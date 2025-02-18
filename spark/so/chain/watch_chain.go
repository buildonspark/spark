package chain

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"log"
	"log/slog"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/rpcclient"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightsparkdev/spark-go/common"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent"
	"github.com/lightsparkdev/spark-go/so/ent/blockheight"
	"github.com/lightsparkdev/spark-go/so/ent/cooperativeexit"
	"github.com/lightsparkdev/spark-go/so/ent/depositaddress"
	"github.com/lightsparkdev/spark-go/so/ent/schema"
	"github.com/lightsparkdev/spark-go/so/ent/signingkeyshare"
	"github.com/lightsparkdev/spark-go/so/ent/treenode"
	"github.com/pebbe/zmq4"
)

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

	for {
		if currChainTip.Hash.IsEqual(&newChainTip.Hash) {
			break
		}

		// Walk back the chain, finding blocks needed to connect and disconnect. Only walk back
		// the header with the greater height, or both if equal heights (i.e. same height, different hashes!).
		if newChainTip.Height <= currChainTip.Height {
			disconnected = append(disconnected, currChainTip)
			prevChainTip, err := findPreviousChainTip(currChainTip, client)
			if err != nil {
				return Difference{}, err
			}
			currChainTip = prevChainTip
		}
		if newChainTip.Height >= currChainTip.Height {
			connected = append(connected, newChainTip)
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

func WatchChain(dbClient *ent.Client, cfg so.BitcoindConfig) error {
	network, err := common.NetworkFromString(cfg.Network)
	if err != nil {
		return err
	}
	connConfig := RPCClientConfig(cfg)
	client, err := rpcclient.New(&connConfig, nil)
	if err != nil {
		return err
	}

	latestBlockHeight, err := client.GetBlockCount()
	if err != nil {
		return err
	}
	latestBlockHash, err := client.GetBlockHash(latestBlockHeight)
	if err != nil {
		return err
	}
	latestChainTip := NewTip(latestBlockHeight, *latestBlockHash)

	// Load the latest scanned block height
	ctx := context.Background()
	entNetwork := common.SchemaNetwork(network)
	entBlockHeight, err := dbClient.BlockHeight.Query().
		Where(blockheight.NetworkEQ(entNetwork)).
		Only(ctx)
	if ent.IsNotFound(err) {
		startHeight := max(0, latestBlockHeight-6)
		entBlockHeight, err = dbClient.BlockHeight.Create().SetHeight(startHeight).SetNetwork(entNetwork).Save(ctx)
	}
	if err != nil {
		return err
	}
	blockHash, err := client.GetBlockHash(entBlockHeight.Height)
	if err != nil {
		return err
	}

	chainTip := NewTip(entBlockHeight.Height, *blockHash)
	difference, err := findDifference(chainTip, latestChainTip, client)
	if err != nil {
		return fmt.Errorf("failed to find difference: %v", err)
	}

	err = disconnectBlocks(ctx, dbClient, difference.Disconnected, network)
	if err != nil {
		return fmt.Errorf("failed to disconnect blocks: %v", err)
	}

	err = connectBlocks(ctx, dbClient, client, difference.Connected, network)
	if err != nil {
		return fmt.Errorf("failed to connect blocks: %v", err)
	}

	chainTip = latestChainTip

	zmqCtx, subscriber, err := initZmq(cfg.ZmqPubRawBlock)
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

	log.Println("Listening for block notifications via ZMQ endpoint", cfg.ZmqPubRawBlock)

	// TODO: we should consider alerting on errors within this loop
	for {
		msg, err := subscriber.RecvMessage(0)
		if err != nil {
			log.Fatalf("Failed to receive message: %v", err)
		}

		_ = msg[0] // topic
		rawBlock := msg[1]
		_ = msg[2] // sequence number

		block := wire.MsgBlock{}
		err = block.Deserialize(bytes.NewReader([]byte(rawBlock)))
		if err != nil {
			log.Printf("Failed to deserialize block: %v\n", err)
			log.Printf("Failed deserialization raw block: %s\n", hex.EncodeToString([]byte(rawBlock)))
			continue
		}

		// We don't actually do anything with the block receive since
		// we need to query bitcoind for the height anyway. We just
		// treat it as a notification that a new block appeared.
		latestBlockHeight, err = client.GetBlockCount()
		if err != nil {
			log.Printf("Failed to get block count: %v", err)
			continue
		}
		latestBlockHash, err = client.GetBlockHash(latestBlockHeight)
		if err != nil {
			log.Printf("Failed to get block hash: %v", err)
			continue
		}

		newChainTip := NewTip(latestBlockHeight, *latestBlockHash)
		difference, err := findDifference(chainTip, newChainTip, client)
		if err != nil {
			log.Printf("Failed to find difference: %v", err)
			continue
		}

		err = disconnectBlocks(ctx, dbClient, difference.Disconnected, network)
		if err != nil {
			log.Printf("Failed to disconnect blocks: %v", err)
			continue
		}

		err = connectBlocks(ctx, dbClient, client, difference.Connected, network)
		if err != nil {
			log.Printf("Failed to connect blocks: %v", err)
			continue
		}

		chainTip = newChainTip
	}
}

func disconnectBlocks(_ context.Context, _ *ent.Client, _ []Tip, _ common.Network) error {
	return nil
}

func connectBlocks(ctx context.Context, dbClient *ent.Client, client *rpcclient.Client, chainTips []Tip, network common.Network) error {
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
		err = handleBlock(ctx, dbTx, txs, chainTip.Height, network)
		if err != nil {
			log.Printf("Failed to handle block: %v", err)
			rollbackErr := dbTx.Rollback()
			if err != nil {
				return rollbackErr
			}
			return err
		}
		err = dbTx.Commit()
		if err != nil {
			return err
		}
		log.Printf("Successfully processed %s block %d", network, chainTip.Height)
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

func handleBlock(ctx context.Context, dbTx *ent.Tx, txs []wire.MsgTx, blockHeight int64, network common.Network) error {
	networkParams := common.NetworkParams(network)
	_, err := dbTx.BlockHeight.Update().
		SetHeight(blockHeight).
		Where(blockheight.NetworkEQ(common.SchemaNetwork(network))).
		Save(ctx)
	if err != nil {
		return err
	}
	confirmedTxids := make([][]byte, 0)
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
		confirmedTxids = append(confirmedTxids, txid[:])
	}

	_, err = dbTx.CooperativeExit.Update().
		Where(cooperativeexit.ConfirmationHeightIsNil()).
		Where(cooperativeexit.ExitTxidIn(confirmedTxids...)).
		SetConfirmationHeight(blockHeight).
		Save(ctx)
	if err != nil {
		return err
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
			log.Printf("Deposit confirmed before tree creation: %s", deposit.Address)
			continue
		}
		if err != nil {
			return err
		}
		log.Printf("Found tree node: %s, start processing", treeNode.ID)
		if treeNode.Status != schema.TreeNodeStatusCreating {
			log.Printf("Expected tree node status to be creating, got %s", treeNode.Status)
		}
		tree, err := treeNode.QueryTree().Only(ctx)
		if err != nil {
			return err
		}
		if tree.Status != schema.TreeStatusPending {
			log.Printf("Expected tree status to be pending, got %s", tree.Status)
			continue
		}
		foundTx := false
		for _, tx := range confirmedTxids {
			if bytes.Equal(tx, tree.BaseTxid) {
				foundTx = true
				break
			}
		}
		if !foundTx {
			slog.Info("Base txid not found in confirmed txids", "base_txid", hex.EncodeToString(tree.BaseTxid))
			for _, txid := range confirmedTxids {
				slog.Info("confirmed txid", "txid", hex.EncodeToString(txid))
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

	return nil
}
