package helper

import (
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/rpcclient"
	"github.com/lightsparkdev/spark-go/common"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/chain"
)

func CheckUTXOOnchain(config *so.Config, utxo *pb.UTXO) bool {
	network, err := common.NetworkFromString(utxo.Network.String())
	if err != nil {
		return false
	}
	tx, err := common.TxFromRawTxBytes(utxo.RawTx)
	if err != nil {
		return false
	}
	txid := tx.TxHash()
	return CheckTxIDOnchain(config, txid[:], network)
}

func CheckTxIDOnchain(config *so.Config, txid []byte, network common.Network) bool {
	connConfig := chain.RPCClientConfig(config.BitcoindConfigs[network.String()])
	client, err := rpcclient.New(&connConfig, nil)
	if err != nil {
		return false
	}
	txidHash := chainhash.Hash(txid)
	tx, err := client.GetRawTransaction(&txidHash)
	if err != nil {
		return false
	}
	return tx != nil
}
