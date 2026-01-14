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
		if err != nil {
			return
		}

		// Create a default wallet if it doesn't exist
		err = ensureWalletLoaded(bitcoinClientInstance)
	})

	return bitcoinClientInstance, err
}

// ensureWalletLoaded creates and loads a default wallet if needed
func ensureWalletLoaded(client *rpcclient.Client) error {
	// Check if any wallets are loaded
	result, err := client.RawRequest("listwallets", nil)
	if err != nil {
		return fmt.Errorf("failed to list wallets: %w", err)
	}

	var wallets []string
	if err := json.Unmarshal(result, &wallets); err != nil {
		return fmt.Errorf("failed to parse wallet list: %w", err)
	}

	if len(wallets) > 0 {
		// Wallet already loaded, check if it has funds
		zap.S().Infof("Wallet already loaded: %v", wallets)
		if err := ensureWalletFunded(client); err != nil {
			zap.S().Warnf("Failed to ensure wallet funded: %v", err)
		}
		return nil
	}

	// Try to load the default wallet
	_, err = client.RawRequest("loadwallet", []json.RawMessage{
		json.RawMessage(`"default"`),
	})
	if err == nil {
		zap.S().Info("Loaded existing 'default' wallet")
		if err := ensureWalletFunded(client); err != nil {
			zap.S().Warnf("Failed to ensure wallet funded: %v", err)
		}
		return nil
	}

	// If load failed, create a new wallet
	zap.S().Info("Creating new 'default' wallet for tests")
	_, err = client.RawRequest("createwallet", []json.RawMessage{
		json.RawMessage(`"default"`), // wallet_name
		json.RawMessage(`false`),     // disable_private_keys
		json.RawMessage(`false`),     // blank
		json.RawMessage(`""`),        // passphrase
		json.RawMessage(`false`),     // avoid_reuse
		json.RawMessage(`true`),      // descriptors (required in Bitcoin Core 30+)
	})
	if err != nil {
		return fmt.Errorf("failed to create wallet: %w", err)
	}

	zap.S().Info("Successfully created 'default' wallet")

	// Fund the newly created wallet
	if err := ensureWalletFunded(client); err != nil {
		zap.S().Warnf("Failed to fund wallet: %v", err)
	}

	return nil
}

// ensureWalletFunded checks wallet balance and mines blocks if needed
func ensureWalletFunded(client *rpcclient.Client) error {
	// Check current balance
	result, err := client.RawRequest("getbalance", nil)
	if err != nil {
		return fmt.Errorf("failed to get balance: %w", err)
	}

	var balance float64
	if err := json.Unmarshal(result, &balance); err != nil {
		return fmt.Errorf("failed to parse balance: %w", err)
	}

	// If balance is sufficient, no need to mine
	if balance > 1.0 { // More than 1 BTC
		zap.S().Infof("Wallet balance is sufficient: %.8f BTC", balance)
		return nil
	}

	zap.S().Infof("Wallet balance is low (%.8f BTC), mining blocks to fund", balance)
	return mineBlocksToWallet(client)
}

// mineBlocksToWallet mines blocks to fund the wallet for tests
func mineBlocksToWallet(client *rpcclient.Client) error {
	// Get a new address from the wallet
	result, err := client.RawRequest("getnewaddress", nil)
	if err != nil {
		return fmt.Errorf("failed to get new address: %w", err)
	}

	var address string
	if err := json.Unmarshal(result, &address); err != nil {
		return fmt.Errorf("failed to parse address: %w", err)
	}

	// Mine 101 blocks to the address (coinbase maturity requires 100 confirmations)
	zap.S().Infof("Mining 101 blocks to %s for wallet funding", address)
	_, err = client.RawRequest("generatetoaddress", []json.RawMessage{
		json.RawMessage(`101`),
		json.RawMessage(fmt.Sprintf(`"%s"`, address)),
	})
	if err != nil {
		return fmt.Errorf("failed to mine blocks: %w", err)
	}

	zap.S().Info("Successfully funded wallet with 101 blocks")
	return nil
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
