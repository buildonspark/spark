package handler

import (
	"encoding/hex"
	"testing"

	"github.com/lightsparkdev/spark/common/keys"

	"github.com/lightsparkdev/spark/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/stretchr/testify/require"
)

func TestValidateUserSignature(t *testing.T) {
	privKeyHex, err := hex.DecodeString("3418d19f934d800fed3e364568e2d3a34d6574d7fa9459caea7c790e294651a9")
	require.NoError(t, err)
	userIdentityPrivKey, err := keys.ParsePrivateKey(privKeyHex)
	require.NoError(t, err)
	userIdentityPubKey := userIdentityPrivKey.Public()

	// Create test data
	network := common.Regtest
	txidStr := "378dd9b575ef72e28f0addbf6c1f4371d1f33b96ffc9aa9c74fb52b31ec7147d"
	txID, err := hex.DecodeString(txidStr)
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
		userPubKey     keys.Public
		userSignature  []byte
		sspSignature   []byte
		totalAmount    uint64
		expectedErrMsg string
	}{
		{
			name:           "valid signature",
			userPubKey:     userIdentityPubKey,
			userSignature:  userSignatureBytes,
			sspSignature:   sspSignatureBytes,
			totalAmount:    90000,
			expectedErrMsg: "",
		},
		{
			name:           "missing user signature",
			userPubKey:     userIdentityPubKey,
			userSignature:  nil,
			sspSignature:   sspSignatureBytes,
			totalAmount:    90000,
			expectedErrMsg: "user signature is required",
		},
		{
			name:           "invalid signature format",
			userPubKey:     userIdentityPubKey,
			userSignature:  []byte("invalid"),
			sspSignature:   sspSignatureBytes,
			totalAmount:    90000,
			expectedErrMsg: "invalid signature format: malformed DER signature",
		},
		{
			name:           "signature verification failure",
			userPubKey:     userIdentityPubKey,
			userSignature:  sspSignatureBytes, // Using SSP signature as user signature should fail
			sspSignature:   sspSignatureBytes,
			totalAmount:    90000,
			expectedErrMsg: "invalid signature",
		},
		{
			name:           "signature verification failure",
			userPubKey:     userIdentityPubKey,
			userSignature:  userSignatureBytes,
			sspSignature:   sspSignatureBytes,
			totalAmount:    1000, // wrong amount
			expectedErrMsg: "invalid signature",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateUserSignature(tt.userPubKey, tt.userSignature, tt.sspSignature, pb.UtxoSwapRequestType_Fixed, network, txID, vout, tt.totalAmount)
			if tt.expectedErrMsg == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tt.expectedErrMsg)
			}
		})
	}
}

func FuzzValidateUserSignature(f *testing.F) {
	// Add some seed corpus data based on the existing test cases
	f.Add(
		[]byte("3418d19f934d800fed3e364568e2d3a34d6574d7fa9459caea7c790e294651a9"),                                                                               // valid private key bytes
		[]byte("304402202afee9d9a9330e9aeb8d17904d2ed1306b9ecfc9c7554e30f44d2783872e818602204ee7f5225088f95f6fd10333ac21d48041e3ba7aaaa5894b0b4b1b55bcac5765"),   // valid signature hex
		[]byte("304502210080012f5565ff92bceb130d793eedd5eb7516ca16e21cb4eaa19a238a412679a10220367f78f4de21d377f61c6970968d5af52959d8df3c312878ac7af422e4a0245e"), // valid ssp signature hex
		int32(0),  // Fixed request type
		int32(20), // Regtest network
		[]byte("378dd9b575ef72e28f0addbf6c1f4371d1f33b96ffc9aa9c74fb52b31ec7147d"), // valid txid hex
		uint32(1),     // vout
		uint64(90000), // totalAmount
	)

	// Add edge cases for empty/nil values
	f.Add([]byte{}, []byte{}, []byte{}, int32(0), int32(10), []byte{}, uint32(0), uint64(0))
	f.Add([]byte("invalid"), []byte("invalid"), []byte("invalid"), int32(1), int32(30), []byte("deadbeef"), uint32(999), uint64(999999))

	parsePrivKeyHex := func(privKeyHex string) (keys.Private, error) {
		decodedPrivKey, err := hex.DecodeString(privKeyHex)
		if err != nil {
			return keys.Private{}, err
		}
		return keys.ParsePrivateKey(decodedPrivKey)
	}

	f.Fuzz(func(t *testing.T, privKeyHex, userSigHex, sspSigHex []byte, requestTypeInt, networkInt int32, txidHex []byte, vout uint32, totalAmount uint64) {
		// Convert inputs to appropriate types
		var userIdentityPublicKey keys.Public
		var userSignature []byte
		var sspSignature []byte
		var txid []byte

		// Try to decode private key to get public key (if valid)
		if len(privKeyHex) > 0 {
			if privKey, err := parsePrivKeyHex(string(privKeyHex)); err == nil {
				// Valid private key - generate public key
				userIdentityPublicKey = privKey.Public()
			}
		} else {
			// Use empty public key
			userIdentityPublicKey = keys.Public{}
		}

		// Try to decode user signature
		if len(userSigHex) > 0 {
			if decoded, err := hex.DecodeString(string(userSigHex)); err == nil {
				userSignature = decoded
			} else {
				// Use raw bytes if hex decode fails
				userSignature = userSigHex
			}
		}

		// Try to decode SSP signature
		if len(sspSigHex) > 0 {
			if decoded, err := hex.DecodeString(string(sspSigHex)); err == nil {
				sspSignature = decoded
			} else {
				// Use raw bytes if hex decode fails
				sspSignature = sspSigHex
			}
		}

		// Try to decode txid
		if len(txidHex) > 0 {
			if decoded, err := hex.DecodeString(string(txidHex)); err == nil {
				txid = decoded
			} else {
				// Use raw bytes if hex decode fails
				txid = txidHex
			}
		}

		// Convert enum values, using modulo to ensure valid range
		var requestType pb.UtxoSwapRequestType
		switch requestTypeInt % 3 {
		case 0:
			requestType = pb.UtxoSwapRequestType_Fixed
		case 1:
			requestType = pb.UtxoSwapRequestType_MaxFee
		case 2:
			requestType = pb.UtxoSwapRequestType_Refund
		}

		var network common.Network
		switch networkInt % 5 {
		case 0:
			network = common.Unspecified
		case 1:
			network = common.Mainnet
		case 2:
			network = common.Regtest
		case 3:
			network = common.Testnet
		case 4:
			network = common.Signet
		}

		// The function should never panic, regardless of input
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("validateUserSignature panicked with input: userPubKey=%x, userSig=%x, sspSig=%x, requestType=%v, network=%v, txid=%x, vout=%d, amount=%d. Panic: %v",
					userIdentityPublicKey, userSignature, sspSignature, requestType, network, txid, vout, totalAmount, r)
			}
		}()

		// Call the function - it may return an error but should not panic
		err := validateUserSignature(userIdentityPublicKey, userSignature, sspSignature, requestType, network, txid, vout, totalAmount)

		// We don't assert specific error conditions since we're fuzzing with random data
		// The main goal is to ensure no panics occur and the function handles all inputs gracefully
		_ = err

		// Verify that nil user signature always returns an error
		if userSignature == nil {
			if err == nil {
				t.Error("Expected error when userSignature is nil, but got nil")
			}
		}

		// If we have valid-looking inputs, we can perform some additional checks
		if !userIdentityPublicKey.IsZero() && len(userSignature) > 0 && len(sspSignature) > 0 && len(txid) == 32 {
			// These look like valid inputs, so function should at least parse them
			// Even if signature verification fails, parsing should succeed
			if err != nil {
				// Error is expected with random data, but should contain meaningful message
				errMsg := err.Error()
				if errMsg == "" {
					t.Error("Error message should not be empty")
				}
			}
		}
	})
}
