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
		name  string
		proto pb.Network
		want  Network
	}{
		{
			name:  "mainnet",
			proto: pb.Network_MAINNET,
			want:  Mainnet,
		},
		{
			name:  "regtest",
			proto: pb.Network_REGTEST,
			want:  Regtest,
		},
		{
			name:  "testnet",
			proto: pb.Network_TESTNET,
			want:  Testnet,
		},
		{
			name:  "signet",
			proto: pb.Network_SIGNET,
			want:  Signet,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := FromProtoNetwork(tt.proto)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestFromProtoNetworkUnknownValue(t *testing.T) {
	_, err := FromProtoNetwork(pb.Network(999))
	require.Error(t, err)
}

func TestToProtoNetwork(t *testing.T) {
	tests := []struct {
		name    string
		network Network
		want    pb.Network
	}{
		{
			name:    "mainnet",
			network: Mainnet,
			want:    pb.Network_MAINNET,
		},
		{
			name:    "regtest",
			network: Regtest,
			want:    pb.Network_REGTEST,
		},
		{
			name:    "testnet",
			network: Testnet,
			want:    pb.Network_TESTNET,
		},
		{
			name:    "signet",
			network: Signet,
			want:    pb.Network_SIGNET,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.network.ToProtoNetwork()
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestToProtoNetworkUnknownValue(t *testing.T) {
	_, err := Network(999).ToProtoNetwork()
	require.Error(t, err)
}

func TestFromString(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  Network
	}{
		{
			name:  "mainnet uppercase",
			input: "MAINNET",
			want:  Mainnet,
		},
		{
			name:  "mainnet lowercase",
			input: "mainnet",
			want:  Mainnet,
		},
		{
			name:  "regtest uppercase",
			input: "REGTEST",
			want:  Regtest,
		},
		{
			name:  "regtest lowercase",
			input: "regtest",
			want:  Regtest,
		},
		{
			name:  "testnet uppercase",
			input: "TESTNET",
			want:  Testnet,
		},
		{
			name:  "testnet lowercase",
			input: "testnet",
			want:  Testnet,
		},
		{
			name:  "signet uppercase",
			input: "SIGNET",
			want:  Signet,
		},
		{
			name:  "signet lowercase",
			input: "signet",
			want:  Signet,
		},
		{
			name:  "unspecified uppercase",
			input: "UNSPECIFIED",
			want:  Unspecified,
		},
		{
			name:  "unspecified lowercase",
			input: "unspecified",
			want:  Unspecified,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := FromString(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
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
