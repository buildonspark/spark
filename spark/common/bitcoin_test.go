package common

import (
	"encoding/hex"
	"testing"
)

func TestP2TRAddressFromPublicKey(t *testing.T) {
	testVectors := []struct {
		pubKeyHex string
		p2trAddr  string
		network   Network
	}{
		{"0279be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798", "bc1pmfr3p9j00pfxjh0zmgp99y8zftmd3s5pmedqhyptwy6lm87hf5sspknck9", Mainnet},
		{"03797dd653040d344fd048c1ad05d4cbcb2178b30c6a0c4276994795f3e833da41", "tb1p8dlmzllfah294ntwatr8j5uuvcj7yg0dete94ck2krrk0ka2c9qqex96hv", Testnet},
	}

	for _, tv := range testVectors {
		pubKey, err := hex.DecodeString(tv.pubKeyHex)
		if err != nil {
			t.Fatalf("Failed to decode public key: %v", err)
		}

		addr, err := P2TRAddressFromPublicKey(pubKey, tv.network)
		if err != nil {
			t.Fatalf("Failed to get P2TR address: %v", err)
		}

		if *addr != tv.p2trAddr {
			t.Fatalf("P2TR address mismatch: got %s, want %s", *addr, tv.p2trAddr)
		}
	}
}
