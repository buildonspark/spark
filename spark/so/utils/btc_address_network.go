package utils

import (
	"slices"
	"strings"

	"github.com/lightsparkdev/spark/common/btcnetwork"
)

// IsBitcoinAddressForNetwork checks if the given Bitcoin address matches the expected prefix for the specified network.
// It uses simple prefix matching for common address types (legacy, P2SH, SegWit, Taproot) for each Bitcoin network.
// TODO: Investigate using btcutil for this instead of using our own.
func IsBitcoinAddressForNetwork(address string, network btcnetwork.Network) bool {
	switch network {
	case btcnetwork.Mainnet:
		return hasAnyPrefix(address, "bc1", "3", "1")
	case btcnetwork.Regtest:
		return hasAnyPrefix(address, "bcrt", "2", "m", "n")
	case btcnetwork.Testnet:
		return hasAnyPrefix(address, "tb1", "2", "m", "n")
	case btcnetwork.Signet:
		return hasAnyPrefix(address, "tb1", "sb1", "2", "m", "n")
	default:
		return false
	}
}

// networkScanOrder is the order in which prefix matching resolves an address to a
// network.
var networkScanOrder = []btcnetwork.Network{
	btcnetwork.Mainnet,
	btcnetwork.Regtest,
	btcnetwork.Testnet,
	btcnetwork.Signet,
}

// DetermineBitcoinNetworkForAddress returns the network an address belongs to,
// choosing only among the networks the caller supports. Prefix matching cannot tell
// testnet3 from signet: both use the tb1/bech32m HRPs and the same base58 prefixes,
// so scanning every known network always answers TESTNET for a signet address.
// Restricting the scan to the supported set is what disambiguates them. When several
// supported networks share a prefix (regtest and testnet both accept 2/m/n) the first
// match in networkScanOrder wins, as before. An empty supported list means "not
// configured" and falls back to scanning every known network.
func DetermineBitcoinNetworkForAddress(address string, supported []btcnetwork.Network) btcnetwork.Network {
	for _, network := range networkScanOrder {
		if len(supported) > 0 && !slices.Contains(supported, network) {
			continue
		}
		if IsBitcoinAddressForNetwork(address, network) {
			return network
		}
	}
	return btcnetwork.Unspecified
}

func hasAnyPrefix(address string, prefixes ...string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(address, prefix) {
			return true
		}
	}
	return false
}
