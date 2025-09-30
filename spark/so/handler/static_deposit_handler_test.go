package handler

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
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

func createOldBitcoinTxBytes(t *testing.T, receiverPubKey keys.Public) []byte {
	p2trScript, err := common.P2TRScriptFromPubKey(receiverPubKey)
	require.NoError(t, err)

	// sequence = 10275 = 0x2823 (little-endian: 23 28 00 00)
	scriptLen := fmt.Sprintf("%02x", len(p2trScript))
	hexStr := "01010101010000000000000000000000000000000000000000000000000000000000000000ffffffff002328000001e803000000000000" +
		scriptLen +
		hex.EncodeToString(p2trScript) +
		"000000000000000000000000000000000000000000"
	bytes, _ := hex.DecodeString(hexStr)
	return bytes
}

func createValidUserSignatureForTest(
	txid []byte,
	vout uint32,
	network common.Network,
	requestType pb.UtxoSwapRequestType,
	totalAmount uint64,
	sspSignature []byte,
	userPrivateKey keys.Private,
) []byte {
	hash := CreateUserStatement(hex.EncodeToString(txid), vout, network, requestType, totalAmount, sspSignature)
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
		SetNetwork(st.NetworkRegtest).
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
		SetRequestType(st.UtxoSwapRequestTypeRefund).
		SetCreditAmountSats(10000).
		SetSspSignature([]byte("test_ssp_signature")).
		SetSspIdentityPublicKey(userPubKey.Serialize()).
		SetUserIdentityPublicKey(userPubKey.Serialize()).
		SetCoordinatorIdentityPublicKey(coordinatorPubKey.Serialize()).
		Save(ctx)
	require.NoError(t, err)
	return utxoSwap
}

func createTestBlockHeight(t *testing.T, ctx context.Context, client *ent.Client, height int64) {
	_, err := client.BlockHeight.Create().SetNetwork(st.NetworkRegtest).SetHeight(height).Save(ctx)
	require.NoError(t, err)
}

func setUpTestConfigWithRegtestNoAuthz(t *testing.T) *so.Config {
	cfg := testutil.TestConfig(t)

	// Add regtest support and disable authz for tests
	cfg.SupportedNetworks = []common.Network{common.Regtest}
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
			network := common.Network(tc.utxo.Network)

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
