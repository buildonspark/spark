package chain

import (
	"bytes"
	"context"
	"fmt"
	"log"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/rpcclient"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightsparkdev/spark-go/common"
	"github.com/lightsparkdev/spark-go/so/ent"
	"github.com/lightsparkdev/spark-go/so/ent/blockheight"
	"github.com/lightsparkdev/spark-go/so/ent/cooperativeexit"
	"github.com/lightsparkdev/spark-go/so/ent/schema"
	"github.com/pebbe/zmq4"
)

func regtestConfig() rpcclient.ConnConfig {
	return rpcclient.ConnConfig{
		Host:         "127.0.0.1:18443",
		User:         "admin1",
		Pass:         "123",
		Params:       "regtest",
		DisableTLS:   true, // TODO: PE help
		HTTPPostMode: true,
	}
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

func WatchChain(dbClient *ent.Client, network common.Network) error {
	connConfig := regtestConfig()
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

	if entBlockHeight.Height < latestBlockHeight {
		log.Printf("Scanning missed blocks from %d and %d\n", entBlockHeight.Height+1, latestBlockHeight)
		entBlockHeight, err = handleNewBlocks(ctx, dbClient, client, entBlockHeight, latestBlockHeight)
		if err != nil {
			return fmt.Errorf("failed to handle new blocks: %v", err)
		}
	} else {
		handleReorg()
	}

	zmqCtx, subscriber, err := initZmq("tcp://127.0.0.1:28332") // This should match our bitcoin config
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
			entBlockHeight, err = handleNewBlocks(ctx, dbClient, client, entBlockHeight, entBlockHeight.Height+1)
			if err != nil {
				log.Printf("Failed to handle new blocks: %v\n", err)
			}
		} else {
			log.Printf("Block %s is not the next block\n", blockHash)
			handleReorg()
		}
	}
}

func handleReorg() {
	// TOOD: implement reorg handling
}

func handleNewBlocks(ctx context.Context, db *ent.Client, client *rpcclient.Client, entBlockHeight *ent.BlockHeight, latestBlockHeight int64) (*ent.BlockHeight, error) {
	// TODO: deposits

	// Cooperative exits
	// TODO: how to make sure no more get added while we're processing?
	coopExits, err := db.CooperativeExit.Query().Where(
		cooperativeexit.ConfirmationHeightIsNil(),
	).All(ctx)
	if err != nil {
		return nil, err
	}
	coopExitTxs := make(map[[32]byte][]*ent.CooperativeExit)
	for _, coopExit := range coopExits {
		if len(coopExit.ExitTxid) != 32 {
			return nil, fmt.Errorf("coop exit txid is not 32 bytes: %v", coopExit.ExitTxid)
		}
		exitTxid := [32]byte(coopExit.ExitTxid)
		coopExitTxs[exitTxid] = append(coopExitTxs[exitTxid], coopExit)
	}

	// Process blocks
	initialLastScannedBlockHeight := entBlockHeight.Height
	for blockHeight := initialLastScannedBlockHeight + 1; blockHeight < latestBlockHeight+1; blockHeight++ {
		blockHash, err := client.GetBlockHash(blockHeight)
		if err != nil {
			return nil, err
		}
		block, err := client.GetBlockVerboseTx(blockHash)
		if err != nil {
			return nil, err
		}

		tx, err := db.Tx(ctx)
		if err != nil {
			return nil, err
		}
		entBlockHeight, err = handleBlock(ctx, tx, block, entBlockHeight, blockHeight, coopExitTxs)
		if err != nil {
			err = tx.Rollback()
			if err != nil {
				return nil, err
			}
		}
		err = tx.Commit()
		if err != nil {
			return nil, err
		}
	}
	return entBlockHeight, nil
}

func handleBlock(ctx context.Context, tx *ent.Tx, block *btcjson.GetBlockVerboseTxResult, entBlockHeight *ent.BlockHeight, blockHeight int64, coopExitTxs map[[32]byte][]*ent.CooperativeExit) (*ent.BlockHeight, error) {
	entBlockHeight, err := tx.BlockHeight.UpdateOne(entBlockHeight).SetHeight(blockHeight).Save(ctx)
	if err != nil {
		return nil, err
	}
	for _, tx := range block.Tx {
		txid, err := chainhash.NewHashFromStr(tx.Txid)
		if err != nil {
			return nil, err
		}
		txidBytes := txid.CloneBytes()
		if len(txidBytes) != 32 {
			return nil, fmt.Errorf("txid returned by bitcoin RPC is not 32 bytes")
		}
		for _, coopExit := range coopExitTxs[[32]byte(txidBytes)] {
			_, err = coopExit.Update().SetConfirmationHeight(blockHeight).Save(ctx)
			if err != nil {
				return nil, err
			}
		}
	}
	return entBlockHeight, nil
}
