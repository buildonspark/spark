package utils

import (
	"fmt"
	"strings"

	"github.com/lightsparkdev/spark/common"
)

func IsBitcoinAddressForNetwork(address string, network common.Network) bool {
	switch network {
	case common.Mainnet:
		return strings.HasPrefix(address, "bc1p") ||
			strings.HasPrefix(address, "bc1") ||
			strings.HasPrefix(address, "3") ||
			strings.HasPrefix(address, "1")
	case common.Regtest:
		fmt.Println("checking for regtest")
		return strings.HasPrefix(address, "bcrt")
	case common.Testnet:
		return strings.HasPrefix(address, "tb1p") ||
			strings.HasPrefix(address, "tb1") ||
			strings.HasPrefix(address, "2") ||
			strings.HasPrefix(address, "m") ||
			strings.HasPrefix(address, "n")
	case common.Signet:
		return strings.HasPrefix(address, "tb1p") ||
			strings.HasPrefix(address, "tb1") ||
			strings.HasPrefix(address, "sb1") ||
			strings.HasPrefix(address, "2") ||
			strings.HasPrefix(address, "m") ||
			strings.HasPrefix(address, "n")
	default:
		return false
	}
}
