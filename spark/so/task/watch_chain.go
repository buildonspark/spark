package task

import (
	"context"
	"fmt"

	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/rpcclient"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent"
	"github.com/lightsparkdev/spark-go/so/ent/blockheight"
	"github.com/lightsparkdev/spark-go/so/ent/cooperativeexit"
	"github.com/lightsparkdev/spark-go/so/ent/schema"
)

// watchRegtest is a wrapper function to conform to the task function signature.
func watchRegtest(config *so.Config, dbClient *ent.Client) error {
	return watchChain(config, dbClient, &chaincfg.RegressionNetParams)
}

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

func chainParamsToNetwork(network *chaincfg.Params) (schema.Network, error) {
	switch network.Name {
	case "regtest":
		return schema.NetworkRegtest, nil
	case "mainnet":
		return schema.NetworkMainnet, nil
	default:
		return "", fmt.Errorf("unsupported network: %v", network.Name)
	}
}

func watchChain(_ *so.Config, dbClient *ent.Client, network *chaincfg.Params) error {
	// TODO: make sure only one instance of this is running
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

	ctx := context.Background()

	tx, err := dbClient.Tx(ctx)
	if err != nil {
		return err
	}

	entNetwork, err := chainParamsToNetwork(network)
	if err != nil {
		return err
	}

	lastScannedBlockHeight, err := tx.BlockHeight.Query().Where(
		blockheight.NetworkEQ(entNetwork),
	).Only(ctx)
	if ent.IsNotFound(err) {
		startHeight := max(0, latestBlockHeight-6)
		lastScannedBlockHeight, err = tx.BlockHeight.Create().SetHeight(startHeight).SetNetwork(entNetwork).Save(ctx)
		if err != nil {
			return err
		}
	} else if err != nil {
		return err
	}

	if latestBlockHeight == lastScannedBlockHeight.Height {
		return nil
	} else if latestBlockHeight < lastScannedBlockHeight.Height {
		return handleReorg(ctx, tx, lastScannedBlockHeight.Height, latestBlockHeight)
	}
	return handleNewBlocks(ctx, tx, client, lastScannedBlockHeight, latestBlockHeight)
}

func handleNewBlocks(ctx context.Context, db *ent.Tx, client *rpcclient.Client, lastScannedBlockHeight *ent.BlockHeight, latestBlockHeight int64) error {
	// TODO: deposits

	// Cooperative exits
	// TODO: how to make sure no more get added while we're processing?
	coopExits, err := db.CooperativeExit.Query().Where(
		cooperativeexit.ConfirmationHeightIsNil(),
	).All(ctx)
	if err != nil {
		return err
	}
	coopExitTxs := make(map[[32]byte][]*ent.CooperativeExit)
	for _, coopExit := range coopExits {
		if len(coopExit.ExitTxid) != 32 {
			return fmt.Errorf("coop exit txid is not 32 bytes: %v", coopExit.ExitTxid)
		}
		exitTxid := [32]byte(coopExit.ExitTxid)
		coopExitTxs[exitTxid] = append(coopExitTxs[exitTxid], coopExit)
	}

	// Process blocks
	initialLastScannedBlockHeight := lastScannedBlockHeight.Height
	for blockHeight := initialLastScannedBlockHeight + 1; blockHeight < latestBlockHeight+1; blockHeight++ {
		dbTx := ent.TxFromContext(ctx)
		blockHash, err := client.GetBlockHash(blockHeight)
		if err != nil {
			return err
		}
		block, err := client.GetBlockVerboseTx(blockHash)
		if err != nil {
			return err
		}
		_, err = db.BlockHeight.UpdateOne(lastScannedBlockHeight).SetHeight(blockHeight).Save(ctx)
		if err != nil {
			return err
		}
		// Save in DB
		// mark confirmation height
		for _, tx := range block.Tx {
			txid, err := chainhash.NewHashFromStr(tx.Txid)
			if err != nil {
				return err
			}
			txidBytes := txid.CloneBytes()
			if len(txidBytes) != 32 {
				return fmt.Errorf("txid returned by bitcoin RPC is not 32 bytes")
			}
			for _, coopExit := range coopExitTxs[[32]byte(txidBytes)] {
				_, err = coopExit.Update().SetConfirmationHeight(blockHeight).Save(ctx)
				if err != nil {
					return err
				}
			}
		}
		err = dbTx.Commit()
		if err != nil {
			return err
		}
	}
	return nil
}

func handleReorg(_ context.Context, _ *ent.Tx, _, _ int64) error {
	// TODO: implement reorg handling
	return nil
}
