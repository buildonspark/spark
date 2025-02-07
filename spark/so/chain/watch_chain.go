package chain

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"log"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/btcsuite/btcd/chaincfg"
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

func NewRegtestClient() (*rpcclient.Client, error) {
	connConfig := rpcclient.ConnConfig{
		Host:         "127.0.0.1:18443",
		User:         "admin1",
		Pass:         "123",
		Params:       "regtest",
		DisableTLS:   true, // TODO: PE help
		HTTPPostMode: true,
	}
	return rpcclient.New(
		&connConfig,
		nil,
	)
}

func initZmq(endpoint string) (*zmq4.Context, *zmq4.Socket, error) {
	zmqCtx, err := zmq4.NewContext()
	if err != nil {
		log.Fatalf("Failed to create ZMQ context: %v", err)
	}

	subscriber, err := zmqCtx.NewSocket(zmq4.SUB)
	if err != nil {
		log.Fatalf("Failed to create ZMQ socket: %v", err)
	}

	err = subscriber.Connect(endpoint)
	if err != nil {
		log.Fatalf("Failed to connect to ZMQ endpoint: %v", err)
	}

	err = subscriber.SetSubscribe("rawblock")
	if err != nil {
		log.Fatalf("Failed to subscribe to topic: %v", err)
	}
	return zmqCtx, subscriber, nil
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

func WatchChain(dbClient *ent.Client, cfg so.BitcoindConfig) error {
	network, err := common.NetworkFromString(cfg.Network)
	if err != nil {
		return err
	}
	connConfig := RPCClientConfig(cfg)
	client, err := rpcclient.New(
		&connConfig,
		nil,
	)
	if err != nil {
		return err
	}

	latestBlockHeight, err := client.GetBlockCount()
	if err != nil {
		return err
	}

	// Load the latest scanned block height
	ctx := context.Background()
	entNetwork := common.SchemaNetwork(network)
	entBlockHeight, err := dbClient.BlockHeight.Query().Where(blockheight.NetworkEQ(schema.NetworkRegtest)).Only(ctx)
	if ent.IsNotFound(err) {
		startHeight := max(0, latestBlockHeight-6)
		entBlockHeight, err = dbClient.BlockHeight.Create().SetHeight(startHeight).SetNetwork(entNetwork).Save(ctx)
	}
	if err != nil {
		return err
	}

	// Scan missed blocks between restart
	networkParams := common.NetworkParams(network)
	if entBlockHeight.Height < latestBlockHeight {
		log.Printf("Scanning missed blocks from %d and %d\n", entBlockHeight.Height+1, latestBlockHeight)
		entBlockHeight, err = handleNewBlocks(ctx, dbClient, client, entBlockHeight, latestBlockHeight, *networkParams)
		if err != nil {
			return fmt.Errorf("failed to handle new blocks: %v", err)
		}
	} else {
		handleReorg()
	}

	zmqCtx, subscriber, err := initZmq(cfg.ZmqPubRawBlock) // This should match our bitcoin config
	if err != nil {
		log.Fatalf("Failed to initialize ZMQ: %v", err)
	}
	defer func() {
		err := subscriber.Close()
		if err != nil {
			log.Fatalf("Failed to close ZMQ socket: %v", err)
		}
		err = zmqCtx.Term()
		if err != nil {
			log.Fatalf("Failed to terminate ZMQ context: %v", err)
		}
	}()

	fmt.Println("Listening for ZMQ messages...")

	// TODO: we should alert on errors within this loop
	for {
		// Receive the message
		msg, err := subscriber.RecvMessage(0)
		if err != nil {
			log.Fatalf("Failed to receive message: %v", err)
		}

		topic := msg[0]
		rawBlock := msg[1]
		sequence := msg[2]

		fmt.Printf("Received ZMQ message: topic=%s sequence=%s\n", topic, sequence)

		block := &wire.MsgBlock{}
		err = block.Deserialize(bytes.NewReader([]byte(rawBlock)))
		if err != nil {
			log.Printf("Failed to deserialize block: %v", err)
			continue
		}

		// Extract the block hash and previous block hash
		blockHash := block.BlockHash()
		prevBlockHash := block.Header.PrevBlock

		currBlockHash, err := client.GetBlockHash(entBlockHeight.Height)
		if err != nil {
			log.Printf("Failed to get block hash: %v", err)
			continue
		}

		if blockHash.IsEqual(currBlockHash) {
			log.Printf("Block %s is already in the database\n", blockHash)
			continue
		} else if prevBlockHash.IsEqual(currBlockHash) {
			newEntBlockHeight, err := handleNewBlocks(ctx, dbClient, client, entBlockHeight,
				entBlockHeight.Height+1, *networkParams)
			if err != nil {
				log.Printf("Failed to handle new blocks: %v\n", err)
				continue
			}
			if newEntBlockHeight == nil {
				log.Printf("Failed to update block height\n")
				continue
			}
			entBlockHeight = newEntBlockHeight
		} else {
			log.Printf("Block %s is not the next block\n", blockHash)
			// TODO: handle missing a block, i.e. failed to process last block,
			// and just needs to rescan from last scanned block height
			handleReorg()
		}
	}
}

func handleReorg() {
	// TOOD: implement reorg handling
	// coop closes - just set confirmation height to 0 if new count is less than height
	// deposits - lock the tree...?
}

func handleNewBlocks(ctx context.Context, db *ent.Client, client *rpcclient.Client, entBlockHeight *ent.BlockHeight, latestBlockHeight int64, network chaincfg.Params) (*ent.BlockHeight, error) {
	// Process blocks
	initialLastScannedBlockHeight := entBlockHeight.Height
	newEntBlockHeight := entBlockHeight
	for blockHeight := initialLastScannedBlockHeight + 1; blockHeight < latestBlockHeight+1; blockHeight++ {
		blockHash, err := client.GetBlockHash(blockHeight)
		if err != nil {
			return nil, err
		}
		block, err := client.GetBlockVerboseTx(blockHash)
		if err != nil {
			return nil, err
		}
		txs := []wire.MsgTx{}
		for _, tx := range block.Tx {
			rawTx, err := TxFromRPCTx(tx)
			if err != nil {
				return nil, err
			}
			txs = append(txs, rawTx)
		}

		dbTx, err := db.Tx(ctx)
		if err != nil {
			return nil, err
		}
		newEntBlockHeight, err = handleBlock(ctx, dbTx, txs, newEntBlockHeight, blockHeight, network)
		if err != nil {
			log.Printf("Failed to handle block: %v", err)
			rollbackErr := dbTx.Rollback()
			if err != nil {
				return nil, rollbackErr
			}
			return nil, err
		}
		err = dbTx.Commit()
		if err != nil {
			return nil, err
		}
	}
	return newEntBlockHeight, nil
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

func handleBlock(ctx context.Context, dbTx *ent.Tx, txs []wire.MsgTx, entBlockHeight *ent.BlockHeight, blockHeight int64, network chaincfg.Params) (*ent.BlockHeight, error) {
	entBlockHeight, err := dbTx.BlockHeight.UpdateOne(entBlockHeight).SetHeight(blockHeight).Save(ctx)
	if err != nil {
		return nil, err
	}
	confirmedTxids := make([][]byte, 0)
	debitedAddresses := make([]string, 0)
	for _, tx := range txs {
		for _, txOut := range tx.TxOut {
			_, addresses, _, err := txscript.ExtractPkScriptAddrs(txOut.PkScript, &network)
			if err != nil {
				return nil, err
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
		return nil, err
	}

	confirmedDeposits, err := dbTx.DepositAddress.Query().
		Where(depositaddress.ConfirmationHeightIsNil()).
		Where(depositaddress.AddressIn(debitedAddresses...)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	for _, deposit := range confirmedDeposits {
		_, err = dbTx.DepositAddress.UpdateOne(deposit).
			SetConfirmationHeight(blockHeight).
			Save(ctx)
		if err != nil {
			return nil, err
		}
		// TODO: only unlock if deposit reaches X confirmations
		signingKeyShare, err := deposit.QuerySigningKeyshare().Only(ctx)
		if err != nil {
			return nil, err
		}
		treeNode, err := dbTx.TreeNode.Query().
			Where(treenode.HasSigningKeyshareWith(signingkeyshare.ID(signingKeyShare.ID))).
			Only(ctx)
		if ent.IsNotFound(err) {
			log.Printf("Deposit confirmed before tree creation: %s", deposit.Address)
			continue
		}
		if err != nil {
			return nil, err
		}
		if treeNode.Status != schema.TreeNodeStatusCreating {
			log.Printf("Expected tree node status to be creating, got %s", treeNode.Status)
			continue
		}
		treeNode, err = dbTx.TreeNode.UpdateOne(treeNode).
			SetStatus(schema.TreeNodeStatusAvailable).
			Save(ctx)
		if err != nil {
			return nil, err
		}
		tree, err := treeNode.QueryTree().Only(ctx)
		if err != nil {
			return nil, err
		}
		if tree.Status != schema.TreeStatusPending {
			log.Printf("Expected tree status to be pending, got %s", tree.Status)
			continue
		}
		_, err = dbTx.Tree.UpdateOne(tree).
			SetStatus(schema.TreeStatusAvailable).
			Save(ctx)
		if err != nil {
			return nil, err
		}
	}

	return entBlockHeight, nil
}
