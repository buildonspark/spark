package wallet

import (
	"github.com/lightsparkdev/spark-go/common"
)

// Config is the configuration for the wallet.
type Config struct {
	// Network is the network to use for the wallet.
	Network common.Network
	// SparkServiceAddress is the address of the Spark service.
	SparkServiceAddress string
	// FrostSignerAddress is the address of the Frost signer.
	FrostSignerAddress string
}
