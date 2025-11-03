package sparktesting

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"sync"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/btcsuite/btcd/rpcclient"
	"go.uber.org/zap"
)

var (
	ErrClientAlreadyInitialized = fmt.Errorf("regtest client already initialized")

	bitcoinClientInstance *rpcclient.Client
	bitcoinClientOnce     sync.Once
)

type submitPackageCmd struct {
	// An array of hex strings of raw transactions.
	RawTxns []string
}

type txResult struct {
	TxID  string `json:"txid"`
	Error string `json:"error,omitempty"`
	// Several fields omitted for brevity
}

type submitPackageResult struct {
	PackageMsg           string              `json:"package_msg"`
	TxResults            map[string]txResult `json:"tx-results"`
	ReplacedTransactions []string            `json:"replaced-transactions"`
}

func newSubmitPackageCmd(rawTxns []string) *submitPackageCmd {
	return &submitPackageCmd{RawTxns: rawTxns}
}

func newClient() (*rpcclient.Client, error) {
	addr, exists := os.LookupEnv("BITCOIN_RPC_URL")
	if !exists {
		if minikubeIp, exists := os.LookupEnv("MINIKUBE_IP"); exists {
			addr = net.JoinHostPort(minikubeIp, "8332")
		} else {
			addr = "127.0.0.1:8332"
		}
	}

	username := getEnvOrDefault("BITCOIN_RPC_USER", "testutil")
	password := getEnvOrDefault("BITCOIN_RPC_PASSWORD", "testutilpassword")

	connConfig := rpcclient.ConnConfig{
		Host:         addr,
		User:         username,
		Pass:         password,
		Params:       "regtest",
		DisableTLS:   true,
		HTTPPostMode: true,
	}

	client, err := rpcclient.New(&connConfig, nil)
	if err != nil {
		return nil, err
	}

	err = btcjson.RegisterCmd("submitpackage", (*submitPackageCmd)(nil), btcjson.UsageFlag(0))
	if err != nil {
		return nil, err
	}

	return client, nil
}

func InitBitcoinClient() (*rpcclient.Client, error) {
	err := ErrClientAlreadyInitialized

	bitcoinClientOnce.Do(func() {
		bitcoinClientInstance, err = newClient()
	})

	return bitcoinClientInstance, err
}

func GetBitcoinClient() *rpcclient.Client {
	return bitcoinClientInstance
}

func SubmitPackage(client *rpcclient.Client, rawTxns []string) error {
	cmd := newSubmitPackageCmd(rawTxns)
	respChan := client.SendCmd(cmd)
	resBytes, err := rpcclient.ReceiveFuture(respChan)
	if err != nil {
		return fmt.Errorf("failed to send command: %w", err)
	}

	var result submitPackageResult
	if err := json.Unmarshal(resBytes, &result); err != nil {
		return err
	}
	if result.PackageMsg != "success" {
		zap.S().Infof("failed to submit package with %d raw transactions", len(rawTxns))
		for _, rawTxn := range rawTxns {
			zap.S().Infof("submitted raw transaction: %s", rawTxn)
		}
		return fmt.Errorf("package submission failed: %s", resBytes)
	}
	return nil
}

func getEnvOrDefault(key, defaultValue string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return defaultValue
}
