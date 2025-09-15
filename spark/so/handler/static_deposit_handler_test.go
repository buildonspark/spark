package handler

import (
	"encoding/hex"
	"testing"

	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
	testutil "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
