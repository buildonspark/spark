package sparktesting

import (
	"fmt"
	"os"
	"sync"

	"github.com/btcsuite/btcd/rpcclient"
)

var (
	ErrClientAlreadyInitialized = fmt.Errorf("regtest client already initialized")

	bitcoinClientInstance *rpcclient.Client
	bitcoinClientOnce     sync.Once
)

func newClient() (*rpcclient.Client, error) {
	addr, exists := os.LookupEnv("BITCOIN_RPC_URL")
	if !exists {
		if minikubeIp, exists := os.LookupEnv("MINIKUBE_IP"); exists {
			addr = fmt.Sprintf("%s:8332", minikubeIp)
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
	return rpcclient.New(
		&connConfig,
		nil,
	)
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

func getEnvOrDefault(key, defaultValue string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return defaultValue
}
