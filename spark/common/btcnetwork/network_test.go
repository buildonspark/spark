package btcnetwork

import (
	"testing"

	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGoEnumMatchesProtoEnum(t *testing.T) {
	enumVals := []Network{Unspecified, Mainnet, Regtest, Testnet, Signet}
	protoVals := pb.Network(0).Descriptor().Values()
	require.Len(t, enumVals, protoVals.Len())
	for i := range protoVals.Len() {
		assert.EqualValues(t, enumVals[i], protoVals.Get(i).Number())
	}
}

func TestFromProtoNetwork(t *testing.T) {
	tests := []struct {
		name            string
		proto           pb.Network
		expectedNetwork Network
	}{
		{
			name:            "mainnet",
			proto:           pb.Network_MAINNET,
			expectedNetwork: Mainnet,
		},
		{
			name:            "regtest",
			proto:           pb.Network_REGTEST,
			expectedNetwork: Regtest,
		},
		{
			name:            "testnet",
			proto:           pb.Network_TESTNET,
			expectedNetwork: Testnet,
		},
		{
			name:            "signet",
			proto:           pb.Network_SIGNET,
			expectedNetwork: Signet,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			network, err := FromProtoNetwork(tt.proto)
			require.NoError(t, err)
			assert.Equal(t, tt.expectedNetwork, network)
		})
	}
}

func TestFromProtoNetworkUnknownValue(t *testing.T) {
	_, err := FromProtoNetwork(pb.Network(999))
	require.Error(t, err)
}

func TestToProtoNetwork(t *testing.T) {
	tests := []struct {
		name          string
		network       Network
		expectedProto pb.Network
	}{
		{
			name:          "mainnet",
			network:       Mainnet,
			expectedProto: pb.Network_MAINNET,
		},
		{
			name:          "regtest",
			network:       Regtest,
			expectedProto: pb.Network_REGTEST,
		},
		{
			name:          "testnet",
			network:       Testnet,
			expectedProto: pb.Network_TESTNET,
		},
		{
			name:          "signet",
			network:       Signet,
			expectedProto: pb.Network_SIGNET,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proto, err := tt.network.ToProtoNetwork()
			require.NoError(t, err)
			assert.Equal(t, tt.expectedProto, proto)
		})
	}
}

func TestToProtoNetworkUnknownValue(t *testing.T) {
	_, err := Network(999).ToProtoNetwork()
	require.Error(t, err)
}

func TestFromString(t *testing.T) {
	tests := []struct {
		name            string
		input           string
		expectedNetwork Network
	}{
		{
			name:            "mainnet uppercase",
			input:           "MAINNET",
			expectedNetwork: Mainnet,
		},
		{
			name:            "mainnet lowercase",
			input:           "mainnet",
			expectedNetwork: Mainnet,
		},
		{
			name:            "regtest uppercase",
			input:           "REGTEST",
			expectedNetwork: Regtest,
		},
		{
			name:            "regtest lowercase",
			input:           "regtest",
			expectedNetwork: Regtest,
		},
		{
			name:            "testnet uppercase",
			input:           "TESTNET",
			expectedNetwork: Testnet,
		},
		{
			name:            "testnet lowercase",
			input:           "testnet",
			expectedNetwork: Testnet,
		},
		{
			name:            "signet uppercase",
			input:           "SIGNET",
			expectedNetwork: Signet,
		},
		{
			name:            "signet lowercase",
			input:           "signet",
			expectedNetwork: Signet,
		},
		{
			name:            "unspecified uppercase",
			input:           "UNSPECIFIED",
			expectedNetwork: Unspecified,
		},
		{
			name:            "unspecified lowercase",
			input:           "unspecified",
			expectedNetwork: Unspecified,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			network, err := FromString(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.expectedNetwork, network)
		})
	}
}

func TestFromStringUnknownValue(t *testing.T) {
	_, err := FromString("invalid_network")
	require.Error(t, err)
}

func TestParamsRequiresSpecifiedNetwork(t *testing.T) {
	_, err := Unspecified.Params()
	require.Error(t, err)
}

func TestParamsSignet(t *testing.T) {
	_, err := Signet.Params()
	require.NoError(t, err)
}
