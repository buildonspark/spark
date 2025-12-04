package utils

import (
	"testing"

	"github.com/lightsparkdev/spark/common/btcnetwork"
)

func TestIsBitcoinAddressForNetwork(t *testing.T) {
	tests := []struct {
		name    string
		address string
		network btcnetwork.Network
		want    bool
	}{
		{
			name:    "Mainnet taproot address",
			address: "bc1p0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7vqzk5jj0",
			network: btcnetwork.Mainnet,
			want:    true,
		},
		{
			name:    "Mainnet segwit address",
			address: "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4",
			network: btcnetwork.Mainnet,
			want:    true,
		},
		{
			name:    "Mainnet P2SH address",
			address: "3JvL6Ymt8MVWiCNHC7oWU6nLeHNJKLZGLN",
			network: btcnetwork.Mainnet,
			want:    true,
		},
		{
			name:    "Mainnet legacy address",
			address: "1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa",
			network: btcnetwork.Mainnet,
			want:    true,
		},
		{
			name:    "Mainnet invalid address",
			address: "tb1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4",
			network: btcnetwork.Mainnet,
			want:    false,
		},
		{
			name:    "Regtest valid address",
			address: "bcrt1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4",
			network: btcnetwork.Regtest,
			want:    true,
		},
		{
			name:    "Regtest invalid address",
			address: "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4",
			network: btcnetwork.Regtest,
			want:    false,
		},

		{
			name:    "Regtest P2SH address",
			address: "2N1LGaGg836mqSQqiuUBLfcyGBhyZbremDX",
			network: btcnetwork.Regtest,
			want:    true,
		},
		{
			name:    "Regtest legacy P2PKH address m",
			address: "mipcBbFg9gMiCh81Kj8tqqdgoZub1ZJRfn",
			network: btcnetwork.Regtest,
			want:    true,
		},
		{
			name:    "Regtest legacy P2PKH address n",
			address: "n4eA2nbYqErp7H6jebchxAN59DmNpksexv",
			network: btcnetwork.Regtest,
			want:    true,
		},
		{
			name:    "Testnet taproot address",
			address: "tb1p0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7vqzk5jj0",
			network: btcnetwork.Testnet,
			want:    true,
		},
		{
			name:    "Testnet segwit address",
			address: "tb1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4",
			network: btcnetwork.Testnet,
			want:    true,
		},
		{
			name:    "Testnet P2SH address",
			address: "2N1LGaGg836mqSQqiuUBLfcyGBhyZbremDX",
			network: btcnetwork.Testnet,
			want:    true,
		},
		{
			name:    "Testnet legacy address m",
			address: "mipcBbFg9gMiCh81Kj8tqqdgoZub1ZJRfn",
			network: btcnetwork.Testnet,
			want:    true,
		},
		{
			name:    "Testnet legacy address n",
			address: "n4eA2nbYqErp7H6jebchxAN59DmNpksexv",
			network: btcnetwork.Testnet,
			want:    true,
		},
		{
			name:    "Testnet invalid address",
			address: "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4",
			network: btcnetwork.Testnet,
			want:    false,
		},
		{
			name:    "Signet taproot address",
			address: "tb1p0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7vqzk5jj0",
			network: btcnetwork.Signet,
			want:    true,
		},
		{
			name:    "Signet segwit address tb1",
			address: "tb1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4",
			network: btcnetwork.Signet,
			want:    true,
		},
		{
			name:    "Signet segwit address sb1",
			address: "sb1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4",
			network: btcnetwork.Signet,
			want:    true,
		},
		{
			name:    "Signet P2SH address",
			address: "2N1LGaGg836mqSQqiuUBLfcyGBhyZbremDX",
			network: btcnetwork.Signet,
			want:    true,
		},
		{
			name:    "Signet legacy address m",
			address: "mipcBbFg9gMiCh81Kj8tqqdgoZub1ZJRfn",
			network: btcnetwork.Signet,
			want:    true,
		},
		{
			name:    "Signet legacy address n",
			address: "n4eA2nbYqErp7H6jebchxAN59DmNpksexv",
			network: btcnetwork.Signet,
			want:    true,
		},
		{
			name:    "Signet invalid address",
			address: "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4",
			network: btcnetwork.Signet,
			want:    false,
		},
		{
			name:    "Invalid network",
			address: "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4",
			network: btcnetwork.Unspecified,
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := IsBitcoinAddressForNetwork(tt.address, tt.network); got != tt.want {
				t.Errorf("IsBitcoinAddressForNetwork() = %v, want %v", got, tt.want)
			}
		})
	}
}
