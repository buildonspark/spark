package utils

import (
	"github.com/btcsuite/btcd/btcutil"
	"github.com/lightsparkdev/spark/common/btcnetwork"
)

// IsBitcoinAddressForNetwork checks if the given Bitcoin address is valid for the specified network.
// It uses btcutil.DecodeAddress for proper address validation including checksum verification.
func IsBitcoinAddressForNetwork(address string, network btcnetwork.Network) bool {
	params, err := network.Params()
	if err != nil {
		return false
	}
	_, err = btcutil.DecodeAddress(address, params)
	return err == nil
}
