package utils

import (
	"testing"

	"github.com/lightsparkdev/spark/common/btcnetwork"
)

func TestIsBitcoinAddressForNetwork(t *testing.T) {
	tests := []struct {
		name          string
		address       string
		network       btcnetwork.Network
		expectedMatch bool
	}{
		{
			name:          "Mainnet taproot address",
			address:       "bc1p0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7vqzk5jj0",
			network:       btcnetwork.Mainnet,
			expectedMatch: true,
		},
		{
			name:          "Mainnet segwit address",
			address:       "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4",
			network:       btcnetwork.Mainnet,
			expectedMatch: true,
		},
		{
			name:          "Mainnet P2SH address",
			address:       "3JvL6Ymt8MVWiCNHC7oWU6nLeHNJKLZGLN",
			network:       btcnetwork.Mainnet,
			expectedMatch: true,
		},
		{
			name:          "Mainnet legacy address",
			address:       "1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa",
			network:       btcnetwork.Mainnet,
			expectedMatch: true,
		},
		{
			name:          "Mainnet invalid address",
			address:       "tb1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4",
			network:       btcnetwork.Mainnet,
			expectedMatch: false,
		},
		{
			name:          "Regtest valid address",
			address:       "bcrt1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4",
			network:       btcnetwork.Regtest,
			expectedMatch: true,
		},
		{
			name:          "Regtest invalid address",
			address:       "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4",
			network:       btcnetwork.Regtest,
			expectedMatch: false,
		},

		{
			name:          "Regtest P2SH address",
			address:       "2N1LGaGg836mqSQqiuUBLfcyGBhyZbremDX",
			network:       btcnetwork.Regtest,
			expectedMatch: true,
		},
		{
			name:          "Regtest legacy P2PKH address m",
			address:       "mipcBbFg9gMiCh81Kj8tqqdgoZub1ZJRfn",
			network:       btcnetwork.Regtest,
			expectedMatch: true,
		},
		{
			name:          "Regtest legacy P2PKH address n",
			address:       "n4eA2nbYqErp7H6jebchxAN59DmNpksexv",
			network:       btcnetwork.Regtest,
			expectedMatch: true,
		},
		{
			name:          "Testnet taproot address",
			address:       "tb1p0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7vqzk5jj0",
			network:       btcnetwork.Testnet,
			expectedMatch: true,
		},
		{
			name:          "Testnet segwit address",
			address:       "tb1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4",
			network:       btcnetwork.Testnet,
			expectedMatch: true,
		},
		{
			name:          "Testnet P2SH address",
			address:       "2N1LGaGg836mqSQqiuUBLfcyGBhyZbremDX",
			network:       btcnetwork.Testnet,
			expectedMatch: true,
		},
		{
			name:          "Testnet legacy address m",
			address:       "mipcBbFg9gMiCh81Kj8tqqdgoZub1ZJRfn",
			network:       btcnetwork.Testnet,
			expectedMatch: true,
		},
		{
			name:          "Testnet legacy address n",
			address:       "n4eA2nbYqErp7H6jebchxAN59DmNpksexv",
			network:       btcnetwork.Testnet,
			expectedMatch: true,
		},
		{
			name:          "Testnet invalid address",
			address:       "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4",
			network:       btcnetwork.Testnet,
			expectedMatch: false,
		},
		{
			name:          "Signet taproot address",
			address:       "tb1p0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7vqzk5jj0",
			network:       btcnetwork.Signet,
			expectedMatch: true,
		},
		{
			name:          "Signet segwit address tb1",
			address:       "tb1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4",
			network:       btcnetwork.Signet,
			expectedMatch: true,
		},
		{
			name:          "Signet segwit address sb1",
			address:       "sb1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4",
			network:       btcnetwork.Signet,
			expectedMatch: true,
		},
		{
			name:          "Signet P2SH address",
			address:       "2N1LGaGg836mqSQqiuUBLfcyGBhyZbremDX",
			network:       btcnetwork.Signet,
			expectedMatch: true,
		},
		{
			name:          "Signet legacy address m",
			address:       "mipcBbFg9gMiCh81Kj8tqqdgoZub1ZJRfn",
			network:       btcnetwork.Signet,
			expectedMatch: true,
		},
		{
			name:          "Signet legacy address n",
			address:       "n4eA2nbYqErp7H6jebchxAN59DmNpksexv",
			network:       btcnetwork.Signet,
			expectedMatch: true,
		},
		{
			name:          "Signet invalid address",
			address:       "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4",
			network:       btcnetwork.Signet,
			expectedMatch: false,
		},
		{
			name:          "Invalid network",
			address:       "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4",
			network:       btcnetwork.Unspecified,
			expectedMatch: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if isForNetwork := IsBitcoinAddressForNetwork(tt.address, tt.network); isForNetwork != tt.expectedMatch {
				t.Errorf("IsBitcoinAddressForNetwork() = %v, want %v", isForNetwork, tt.expectedMatch)
			}
		})
	}
}

// testnet3 and signet share their bech32 HRPs and their base58 prefixes, so the
// same address string is valid on both networks.
const (
	// Signet deposit address from the report, also a valid testnet3 address.
	ambiguousTaprootAddress = "tb1p57vmusgydhrsqdrc0zn2cuc4we8uktm0uxgchhx5x0gzu8t2ha4s0gzxyk"
	regtestSegwitAddress    = "bcrt1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4"
	mainnetSegwitAddress    = "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4"
	p2shAddress             = "2N1LGaGg836mqSQqiuUBLfcyGBhyZbremDX"
	scaleTaprootAddress     = "sb1p0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7vqzk5jj0"
	unparsableAddress       = "invalid-address"
)

func TestDetermineBitcoinNetworkForAddress(t *testing.T) {
	tests := []struct {
		name      string
		address   string
		supported []btcnetwork.Network
		expected  btcnetwork.Network
	}{
		{
			name:      "signet-only operator resolves a tb1p address to signet",
			address:   ambiguousTaprootAddress,
			supported: []btcnetwork.Network{btcnetwork.Signet},
			expected:  btcnetwork.Signet,
		},
		{
			name:      "testnet-only operator resolves the same address to testnet",
			address:   ambiguousTaprootAddress,
			supported: []btcnetwork.Network{btcnetwork.Testnet},
			expected:  btcnetwork.Testnet,
		},
		{
			name:      "regtest address on a regtest operator",
			address:   regtestSegwitAddress,
			supported: []btcnetwork.Network{btcnetwork.Regtest},
			expected:  btcnetwork.Regtest,
		},
		{
			name:      "mainnet address on a multi-network operator",
			address:   mainnetSegwitAddress,
			supported: []btcnetwork.Network{btcnetwork.Mainnet, btcnetwork.Testnet, btcnetwork.Signet},
			expected:  btcnetwork.Mainnet,
		},
		{
			name:      "scale taproot address only matches signet",
			address:   scaleTaprootAddress,
			supported: []btcnetwork.Network{btcnetwork.Mainnet, btcnetwork.Testnet, btcnetwork.Regtest, btcnetwork.Signet},
			expected:  btcnetwork.Signet,
		},
		{
			name:      "p2sh still resolves to regtest before testnet as before",
			address:   p2shAddress,
			supported: []btcnetwork.Network{btcnetwork.Regtest, btcnetwork.Testnet},
			expected:  btcnetwork.Regtest,
		},
		{
			name:      "both testnet and signet supported stays on the first match",
			address:   ambiguousTaprootAddress,
			supported: []btcnetwork.Network{btcnetwork.Testnet, btcnetwork.Signet},
			expected:  btcnetwork.Testnet,
		},
		{
			name:      "address of an unsupported network is unspecified",
			address:   mainnetSegwitAddress,
			supported: []btcnetwork.Network{btcnetwork.Signet},
			expected:  btcnetwork.Unspecified,
		},
		{
			name:      "unparsable address is unspecified",
			address:   unparsableAddress,
			supported: []btcnetwork.Network{btcnetwork.Signet, btcnetwork.Testnet},
			expected:  btcnetwork.Unspecified,
		},
		{
			name:      "empty supported list falls back to every known network",
			address:   ambiguousTaprootAddress,
			supported: nil,
			expected:  btcnetwork.Testnet,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if network := DetermineBitcoinNetworkForAddress(tt.address, tt.supported); network != tt.expected {
				t.Errorf("DetermineBitcoinNetworkForAddress() = %v, want %v", network, tt.expected)
			}
		})
	}
}

// A deposit address is marked on every signing operator through the internal
// MarkKeyshareForDepositAddress RPC, which does not carry a network. Resolving it
// against all known networks instead of the ones the operator serves records a
// signet address as TESTNET, so the peer's later NetworkEQ(SIGNET) lookups in
// consensus prepare never find the row and no deposit can be finalized.
func TestDetermineBitcoinNetworkForAddressSignetDeployment(t *testing.T) {
	t.Parallel()

	// The operator of the report runs signet only: gossip config, chain watcher and
	// every client request use SIGNET.
	supported := []btcnetwork.Network{btcnetwork.Signet}

	network := DetermineBitcoinNetworkForAddress(ambiguousTaprootAddress, supported)
	if network != btcnetwork.Signet {
		t.Errorf("deposit address recorded as %v on a signet-only operator, want SIGNET", network)
	}

	// The finalization query the peer runs against its own row.
	if !IsBitcoinAddressForNetwork(ambiguousTaprootAddress, network) {
		t.Errorf("resolved network %v does not accept the deposit address", network)
	}
}
