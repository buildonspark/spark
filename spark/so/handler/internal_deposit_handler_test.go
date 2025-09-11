package handler

import (
	"bytes"
	"context"
	"encoding/hex"
	"math"
	"math/rand/v2"
	"testing"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	sparktesting "github.com/lightsparkdev/spark/testing"

	"github.com/lightsparkdev/spark/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/stretchr/testify/require"
)

var (
	rng = rand.NewChaCha8([32]byte{1})
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

func TestFinalizeTreeCreationErrorCases(t *testing.T) {
	t.Parallel()
	ctx, _ := db.NewTestSQLiteContext(t)

	config := &so.Config{
		SigningOperatorMap: map[string]*so.SigningOperator{
			"test-operator": {
				ID:         0,
				Identifier: "test-operator",
				AddressRpc: "localhost:8080",
				AddressDkg: "localhost:8081",
			},
		},
		SupportedNetworks:          []common.Network{common.Regtest},
		FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{},
	}
	handler := NewInternalDepositHandler(config)

	brokenRawTx := wire.MsgTx{Version: 1, TxIn: []*wire.TxIn{}, TxOut: []*wire.TxOut{}}
	var nodeTxBuf bytes.Buffer
	err := brokenRawTx.Serialize(&nodeTxBuf)
	require.NoError(t, err)
	rawTx1 := nodeTxBuf.Bytes()
	node1 := createTestNode(t, ctx, rawTx1, 0)

	rawTx2 := createTestTxBytesWithIndex(t, 1000, 0)
	node2 := createTestNode(t, ctx, rawTx2, math.MaxInt16+1)

	rawTx3 := createTestTxBytesWithIndex(t, 1000, math.MaxInt16+1)
	node3 := createTestNode(t, ctx, rawTx3, 0)

	rawTx4 := createTestTxBytesWithIndex(t, 1, math.MaxInt16)
	node4 := createTestNode(t, ctx, rawTx4, math.MaxInt16)

	tests := []struct {
		name            string
		finalizeRequest *pbinternal.FinalizeTreeCreationRequest
		expectedError   string
	}{
		{
			name: "node with nil TxIn",
			finalizeRequest: &pbinternal.FinalizeTreeCreationRequest{
				Network: pb.Network_REGTEST,
				Nodes: []*pbinternal.TreeNode{
					node1,
				},
			},
			expectedError: "failed to get node transaction",
		},
		{
			name: "node with overflowing Vout value",
			finalizeRequest: &pbinternal.FinalizeTreeCreationRequest{
				Network: pb.Network_REGTEST,
				Nodes: []*pbinternal.TreeNode{
					node2,
				},
			},
			expectedError: "node vout value",
		},
		{
			name: "node with overflowing previous outpoint index",
			finalizeRequest: &pbinternal.FinalizeTreeCreationRequest{
				Network: pb.Network_REGTEST,
				Nodes: []*pbinternal.TreeNode{
					node3,
				},
			},
			expectedError: "previous outpoint index overflows int16",
		},
		{
			name: "valid node",
			finalizeRequest: &pbinternal.FinalizeTreeCreationRequest{
				Network: pb.Network_REGTEST,
				Nodes: []*pbinternal.TreeNode{
					node4,
				},
			},
			expectedError: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err = handler.FinalizeTreeCreation(ctx, tt.finalizeRequest)

			if tt.expectedError != "" {
				require.ErrorContains(t, err, tt.expectedError)
				return
			}

			require.NoError(t, err)
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

func createTestNode(t *testing.T, ctx context.Context, rawTx []byte, vout uint32) *pbinternal.TreeNode {
	dbTX, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	testID := uuid.New()
	ownerIdentity := keys.MustGeneratePrivateKeyFromRand(rng)
	ownerSigningKey := keys.MustGeneratePrivateKeyFromRand(rng)
	secretShare := keys.MustGeneratePrivateKeyFromRand(rng)
	publicShare1 := keys.MustGeneratePrivateKeyFromRand(rng)
	publicShare2 := keys.MustGeneratePrivateKeyFromRand(rng)
	publicShare3 := keys.MustGeneratePrivateKeyFromRand(rng)

	keyshare, err := dbTX.SigningKeyshare.Create().
		SetID(uuid.New()).
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(secretShare.Serialize()).
		SetPublicShares(map[string][]byte{"1": publicShare1.Public().Serialize(), "2": publicShare2.Public().Serialize(), "3": publicShare3.Public().Serialize()}).
		SetPublicKey(secretShare.Public().Serialize()).
		SetMinSigners(2).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)

	_, err = dbTX.DepositAddress.Create().
		SetID(uuid.New()).
		SetAddress("deposit_address_" + testID.String()).
		SetOwnerIdentityPubkey(ownerIdentity.Public()).
		SetOwnerSigningPubkey(ownerSigningKey.Public()).
		SetConfirmationHeight(100).
		SetConfirmationTxid("other_non_root_deposit_txid_" + testID.String()).
		SetSigningKeyshare(keyshare).
		Save(ctx)
	require.NoError(t, err)

	return &pbinternal.TreeNode{
		Id:                  uuid.New().String(),
		Value:               1000,
		VerifyingPubkey:     append([]byte("verifying_pubkey_"), []byte(testID.String())...),
		OwnerIdentityPubkey: ownerIdentity.Public().Serialize(),
		OwnerSigningPubkey:  ownerSigningKey.Public().Serialize(),
		RawTx:               rawTx,
		RawRefundTx:         append([]byte("raw_refund_tx_"), []byte(testID.String())...),
		TreeId:              uuid.New().String(),
		ParentNodeId:        nil,
		SigningKeyshareId:   keyshare.ID.String(),
		Vout:                vout,
	}
}

func createTestTxBytesWithIndex(t *testing.T, value int64, outpointIndex uint32) []byte {
	tx := wire.NewMsgTx(2)
	tx.AddTxIn(wire.NewTxIn(&wire.OutPoint{Hash: chainhash.Hash{1}, Index: outpointIndex}, nil, nil))
	pkScript, err := txscript.NewScriptBuilder().AddOp(txscript.OP_TRUE).Script()
	require.NoError(t, err)

	tx.AddTxOut(wire.NewTxOut(value, pkScript))

	var buf bytes.Buffer
	err = tx.Serialize(&buf)
	require.NoError(t, err)

	return buf.Bytes()
}
