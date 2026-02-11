package handler

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"testing"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	testutil "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testTransferID = uuid.Must(uuid.Parse("550e8400-e29b-41d4-a716-446655440000"))

func createVersion3ParentTx(t *testing.T, receiverPubKey keys.Public, amount int64, vout uint32) ([]byte, chainhash.Hash) {
	tx := wire.NewMsgTx(3)

	prevHash, _ := chainhash.NewHashFromStr("1111111111111111111111111111111111111111111111111111111111111111")
	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{
			Hash:  *prevHash,
			Index: 0,
		},
		Sequence: spark.InitialSequence(),
	})

	p2trScript, err := common.P2TRScriptFromPubKey(receiverPubKey)
	require.NoError(t, err)

	tx.AddTxOut(&wire.TxOut{
		Value:    amount,
		PkScript: p2trScript,
	})

	var buf bytes.Buffer
	err = tx.Serialize(&buf)
	require.NoError(t, err)

	return buf.Bytes(), tx.TxHash()
}

func createVersion3CPFPRefundTx(t *testing.T, parentTxHash chainhash.Hash, vout uint32, receiverPubKey keys.Public, amount int64, sequence uint32) []byte {
	tx := wire.NewMsgTx(3)

	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{
			Hash:  parentTxHash,
			Index: vout,
		},
		Sequence: sequence,
	})

	p2trScript, err := common.P2TRScriptFromPubKey(receiverPubKey)
	require.NoError(t, err)

	tx.AddTxOut(&wire.TxOut{
		Value:    amount,
		PkScript: p2trScript,
	})

	tx.AddTxOut(common.EphemeralAnchorOutput())

	var buf bytes.Buffer
	err = tx.Serialize(&buf)
	require.NoError(t, err)

	return buf.Bytes()
}

func createVersion3DirectRefundTx(t *testing.T, parentTxHash chainhash.Hash, vout uint32, receiverPubKey keys.Public, amount int64, sequence uint32) []byte {
	tx := wire.NewMsgTx(3)

	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{
			Hash:  parentTxHash,
			Index: vout,
		},
		Sequence: sequence,
	})

	p2trScript, err := common.P2TRScriptFromPubKey(receiverPubKey)
	require.NoError(t, err)

	refundAmount := common.MaybeApplyFee(amount)

	tx.AddTxOut(&wire.TxOut{
		Value:    refundAmount,
		PkScript: p2trScript,
	})

	var buf bytes.Buffer
	err = tx.Serialize(&buf)
	require.NoError(t, err)

	return buf.Bytes()
}

func getRefundTxSigHash(t *testing.T, refundTxBytes []byte, parentTxOut *wire.TxOut) []byte {
	refundTx, err := common.TxFromRawTxBytes(refundTxBytes)
	require.NoError(t, err, "failed to parse refund transaction")

	sighash, err := common.SigHashFromTx(refundTx, 0, parentTxOut)
	require.NoError(t, err, "failed to calculate sighash")

	return sighash
}

func createOldBitcoinTxBytes(t *testing.T, receiverPubKey keys.Public) []byte {
	p2trScript, err := common.P2TRScriptFromPubKey(receiverPubKey)
	require.NoError(t, err)

	// sequence = 10275 = 0x2823 (little-endian: 23 28 00 00)
	scriptLen := fmt.Sprintf("%02x", len(p2trScript))
	hexStr := "01010101010000000000000000000000000000000000000000000000000000000000000000ffffffff002328000001e803000000000000" +
		scriptLen +
		hex.EncodeToString(p2trScript) +
		"000000000000000000000000000000000000000000"
	asBytes, _ := hex.DecodeString(hexStr)
	return asBytes
}

func createValidUserSignatureForTest(
	t *testing.T,
	txid []byte,
	vout uint32,
	network btcnetwork.Network,
	requestType pb.UtxoSwapRequestType,
	totalAmount uint64,
	sspSignature []byte,
	userPrivateKey keys.Private,
) []byte {
	hash, err := CreateUserStatement(hex.EncodeToString(txid), vout, network, requestType, totalAmount, sspSignature, pb.HashVariant_HASH_VARIANT_UNSPECIFIED)
	require.NoError(t, err)
	return ecdsa.Sign(userPrivateKey.ToBTCEC(), hash).Serialize()
}

func createTestStaticDepositAddress(t *testing.T, ctx context.Context, client *ent.Client, keyshare *ent.SigningKeyshare, ownerIdentityPubKey, ownerSigningPubKey keys.Public) *ent.DepositAddress {
	depositAddress, err := client.DepositAddress.Create().
		SetAddress("bc1ptest_static_deposit_address_for_testing").
		SetOwnerIdentityPubkey(ownerIdentityPubKey).
		SetOwnerSigningPubkey(ownerSigningPubKey).
		SetSigningKeyshare(keyshare).
		SetIsStatic(true).
		Save(ctx)
	require.NoError(t, err)
	return depositAddress
}

func createTestUtxo(t *testing.T, ctx context.Context, client *ent.Client, depositAddress *ent.DepositAddress, blockHeight int64) *ent.Utxo {
	validTxBytes := createOldBitcoinTxBytes(t, depositAddress.OwnerIdentityPubkey)
	txid := validTxBytes[:32] // Mock txid from tx bytes

	testUtxo, err := client.Utxo.Create().
		SetNetwork(btcnetwork.Regtest).
		SetTxid(txid).
		SetVout(0).
		SetBlockHeight(blockHeight).
		SetAmount(10000).
		SetPkScript([]byte("test_pk_script")).
		SetDepositAddress(depositAddress).
		Save(ctx)
	require.NoError(t, err)
	return testUtxo
}

func createTestUtxoSwap(t *testing.T, ctx context.Context, rng io.Reader, client *ent.Client, utxo *ent.Utxo, status st.UtxoSwapStatus) *ent.UtxoSwap {
	userPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	coordinatorPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	utxoSwap, err := client.UtxoSwap.Create().
		SetStatus(status).
		SetUtxo(utxo).
		SetUtxoValueSats(utxo.Amount).
		SetRequestType(st.UtxoSwapRequestTypeRefund).
		SetCreditAmountSats(10000).
		SetSspSignature([]byte("test_ssp_signature")).
		SetSspIdentityPublicKey(userPubKey).
		SetUserIdentityPublicKey(userPubKey).
		SetCoordinatorIdentityPublicKey(coordinatorPubKey).
		Save(ctx)
	require.NoError(t, err)
	return utxoSwap
}

func createTestBlockHeight(t *testing.T, ctx context.Context, client *ent.Client, height int64) {
	_, err := client.BlockHeight.Create().SetNetwork(btcnetwork.Regtest).SetHeight(height).Save(ctx)
	require.NoError(t, err)
}

func setUpTestConfigWithRegtestNoAuthz(t *testing.T) *so.Config {
	cfg := testutil.TestConfig(t)

	// Add regtest support and disable authz for tests
	cfg.SupportedNetworks = []btcnetwork.Network{btcnetwork.Regtest}
	cfg.BitcoindConfigs = map[string]so.BitcoindConfig{
		"regtest": {DepositConfirmationThreshold: 1},
	}
	return cfg
}

func TestGenerateRollbackStaticDepositUtxoSwapForUtxoRequest(t *testing.T) {
	// Create a proper test config
	config := testutil.TestConfig(t)

	// Test cases
	testCases := []struct {
		name        string
		utxo        *pb.UTXO
		expectError bool
		errorMsg    string
	}{
		{
			name: "successful rollback request generation",
			utxo: &pb.UTXO{
				Txid:    []byte("test_txid_1234567890abcdef"),
				Vout:    0,
				Network: pb.Network_REGTEST,
			},
			expectError: false,
		},
		{
			name: "successful rollback request generation with vout 1",
			utxo: &pb.UTXO{
				Txid:    []byte("test_txid_abcdef1234567890"),
				Vout:    1,
				Network: pb.Network_MAINNET,
			},
			expectError: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := GenerateRollbackStaticDepositUtxoSwapForUtxoRequest(t.Context(), config, tc.utxo)

			if tc.expectError {
				require.ErrorContains(t, err, tc.errorMsg)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, result)

			// Verify the result structure
			assert.NotNil(t, result.Signature)
			assert.NotNil(t, result.CoordinatorPublicKey)

			// Verify the UTXO data matches input
			assert.Equal(t, tc.utxo.Txid, result.GetOnChainUtxo().GetTxid())
			assert.Equal(t, tc.utxo.Vout, result.GetOnChainUtxo().GetVout())
			assert.Equal(t, tc.utxo.Network, result.GetOnChainUtxo().GetNetwork())

			// Verify signature is valid
			// First, recreate the expected message hash
			network, err := btcnetwork.FromProtoNetwork(tc.utxo.GetNetwork())
			require.NoError(t, err)

			expectedMessageHash, err := CreateUtxoSwapStatement(
				UtxoSwapStatementTypeRollback,
				hex.EncodeToString(result.GetOnChainUtxo().GetTxid()),
				result.OnChainUtxo.Vout,
				network,
			)
			require.NoError(t, err)

			// Verify the signature
			coordinatorPubKey, err := keys.ParsePublicKey(result.GetCoordinatorPublicKey())
			require.NoError(t, err)
			assert.Equal(t, config.IdentityPublicKey(), coordinatorPubKey)
			err = common.VerifyECDSASignature(coordinatorPubKey, result.Signature, expectedMessageHash)
			require.NoError(t, err, "Signature verification failed")
		})
	}
}

func TestGenerateRollbackStaticDepositUtxoSwapForUtxoRequest_InvalidNetwork(t *testing.T) {
	// Create a proper test config
	config := testutil.TestConfig(t)

	// Test with invalid network
	utxo := &pb.UTXO{
		Txid:    []byte("test_txid"),
		Vout:    0,
		Network: pb.Network_UNSPECIFIED, // Invalid network
	}

	_, err := GenerateRollbackStaticDepositUtxoSwapForUtxoRequest(t.Context(), config, utxo)
	require.ErrorContains(t, err, "network is required")
}

func TestGenerateRollbackStaticDepositUtxoSwapForUtxoRequest_EmptyTxid(t *testing.T) {
	// Create a proper test config
	config := testutil.TestConfig(t)

	// Test with empty txid
	utxo := &pb.UTXO{
		Txid:    []byte{}, // Empty txid
		Vout:    0,
		Network: pb.Network_REGTEST,
	}

	result, err := GenerateRollbackStaticDepositUtxoSwapForUtxoRequest(t.Context(), config, utxo)
	require.Error(t, err)
	require.Nil(t, result)
}
