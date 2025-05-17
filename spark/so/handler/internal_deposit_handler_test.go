package handler

import (
	"encoding/hex"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/lightsparkdev/spark/common"
	"github.com/stretchr/testify/require"
)

func TestValidateUserSignature(t *testing.T) {
	privKeyHex, err := hex.DecodeString("3418d19f934d800fed3e364568e2d3a34d6574d7fa9459caea7c790e294651a9")
	require.NoError(t, err)
	userIdentityPrivKey := secp256k1.PrivKeyFromBytes(privKeyHex)
	userIdentityPubKey := userIdentityPrivKey.PubKey().SerializeCompressed()

	// Create test data
	network := common.Regtest
	txidStr := "378dd9b575ef72e28f0addbf6c1f4371d1f33b96ffc9aa9c74fb52b31ec7147d"
	txid, err := hex.DecodeString(txidStr)
	require.NoError(t, err)
	vout := uint32(1)
	sspSignature := "304502210080012f5565ff92bceb130d793eedd5eb7516ca16e21cb4eaa19a238a412679a10220367f78f4de21d377f61c6970968d5af52959d8df3c312878ac7af422e4a0245e"
	userSignature := "304402202afee9d9a9330e9aeb8d17904d2ed1306b9ecfc9c7554e30f44d2783872e818602204ee7f5225088f95f6fd10333ac21d48041e3ba7aaaa5894b0b4b1b55bcac5765"

	sspSignatureBytes, err := hex.DecodeString(sspSignature)
	require.NoError(t, err)
	userSignatureBytes, err := hex.DecodeString(userSignature)
	require.NoError(t, err)

	tests := []struct {
		name           string
		userPubKey     []byte
		userSignature  []byte
		sspSignature   []byte
		network        common.Network
		txid           []byte
		vout           uint32
		totalAmount    uint64
		expectedErrMsg string
	}{
		{
			name:           "valid signature",
			userPubKey:     userIdentityPubKey,
			userSignature:  userSignatureBytes,
			sspSignature:   sspSignatureBytes,
			network:        network,
			txid:           txid,
			vout:           vout,
			totalAmount:    90000,
			expectedErrMsg: "",
		},
		{
			name:           "missing user signature",
			userPubKey:     userIdentityPubKey,
			userSignature:  nil,
			sspSignature:   sspSignatureBytes,
			network:        network,
			txid:           txid,
			vout:           vout,
			totalAmount:    90000,
			expectedErrMsg: "user signature is required",
		},
		{
			name:           "invalid public key",
			userPubKey:     []byte("invalid"),
			userSignature:  userSignatureBytes,
			sspSignature:   sspSignatureBytes,
			network:        network,
			txid:           txid,
			vout:           vout,
			totalAmount:    90000,
			expectedErrMsg: "failed to parse user identity public key",
		},
		{
			name:           "invalid signature format",
			userPubKey:     userIdentityPubKey,
			userSignature:  []byte("invalid"),
			sspSignature:   sspSignatureBytes,
			network:        network,
			txid:           txid,
			vout:           vout,
			totalAmount:    90000,
			expectedErrMsg: "failed to parse user signature",
		},
		{
			name:           "signature verification failure",
			userPubKey:     userIdentityPubKey,
			userSignature:  sspSignatureBytes, // Using SSP signature as user signature should fail
			sspSignature:   sspSignatureBytes,
			network:        network,
			txid:           txid,
			vout:           vout,
			totalAmount:    90000,
			expectedErrMsg: "invalid user signature",
		},
		{
			name:           "signature verification failure",
			userPubKey:     userIdentityPubKey,
			userSignature:  userSignatureBytes,
			sspSignature:   sspSignatureBytes,
			network:        network,
			txid:           txid,
			vout:           vout,
			totalAmount:    1000, // wrong amount
			expectedErrMsg: "invalid user signature",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateUserSignature(tt.userPubKey, tt.userSignature, tt.sspSignature, tt.network, tt.txid, tt.vout, tt.totalAmount)
			if tt.expectedErrMsg != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.expectedErrMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
