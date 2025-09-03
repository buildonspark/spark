package handler

import (
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/schema/schematype"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	sparktesting "github.com/lightsparkdev/spark/testing"
	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestVerifiedTargetUtxo(t *testing.T) {
	ctx, dbCtx := db.NewTestSQLiteContext(t, t.Context())
	defer dbCtx.Close()
	rng := rand.NewChaCha8([32]byte{})

	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	// Create test data
	blockHeight := 100
	txid := []byte("test_txid")
	vout := uint32(0)

	// Create block height records for both networks
	_, err = tx.BlockHeight.Create().
		SetNetwork(st.NetworkMainnet).
		SetHeight(int64(blockHeight)).
		Save(ctx)
	require.NoError(t, err)

	_, err = tx.BlockHeight.Create().
		SetNetwork(st.NetworkRegtest).
		SetHeight(int64(blockHeight)).
		Save(ctx)
	require.NoError(t, err)

	t.Run("successful verification", func(t *testing.T) {
		config := &so.Config{
			BitcoindConfigs: map[string]so.BitcoindConfig{
				"regtest": {
					DepositConfirmationThreshold: 1,
				},
			},
			FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{},
		}
		require.Equal(t, "regtest", strings.ToLower(string(schematype.NetworkRegtest)))

		testSecretKey := keys.MustGeneratePrivateKeyFromRand(rng)
		testPublicKey := testSecretKey.Public()

		// Create signing keyshare first
		signingKeyshare, err := tx.SigningKeyshare.Create().
			SetStatus(st.KeyshareStatusAvailable).
			SetSecretShare(testSecretKey.Serialize()).
			SetPublicShares(map[string][]byte{"test": testPublicKey.Serialize()}).
			SetPublicKey(testPublicKey.Serialize()).
			SetMinSigners(2).
			SetCoordinatorIndex(0).
			Save(ctx)
		require.NoError(t, err)

		testIdentityKey := keys.MustGeneratePrivateKeyFromRand(rng)
		testSigningKey := keys.MustGeneratePrivateKeyFromRand(rng)

		// Create deposit address
		depositAddress, err := tx.DepositAddress.Create().
			SetAddress("test_address").
			SetOwnerIdentityPubkey(testIdentityKey.Public()).
			SetOwnerSigningPubkey(testSigningKey.Public()).
			SetSigningKeyshare(signingKeyshare).
			SetNetwork(st.NetworkRegtest).
			Save(ctx)
		require.NoError(t, err)

		// Create UTXO with sufficient confirmations
		utxoBlockHeight := blockHeight - int(config.BitcoindConfigs["regtest"].DepositConfirmationThreshold) + 1
		utxo, err := tx.Utxo.Create().
			SetNetwork(st.NetworkRegtest).
			SetTxid(txid).
			SetVout(vout).
			SetBlockHeight(int64(utxoBlockHeight)).
			SetAmount(1000).
			SetPkScript([]byte("test_script")).
			SetDepositAddress(depositAddress).
			Save(ctx)
		require.NoError(t, err)

		// Test verification
		verifiedUtxo, err := VerifiedTargetUtxo(ctx, config, tx, st.NetworkRegtest, txid, vout)
		require.NoError(t, err)
		assert.Equal(t, utxo.ID, verifiedUtxo.ID)
		assert.Equal(t, utxo.BlockHeight, verifiedUtxo.BlockHeight)

		// Test verification in mainnet (should fail)
		_, err = VerifiedTargetUtxo(ctx, config, tx, st.NetworkMainnet, txid, vout)
		require.ErrorContains(t, err, "utxo not found")
	})

	t.Run("insufficient confirmations", func(t *testing.T) {
		config := &so.Config{
			BitcoindConfigs: map[string]so.BitcoindConfig{
				"regtest": {
					DepositConfirmationThreshold: 1,
				},
			},
			FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{},
		}

		testSecretKey2 := keys.MustGeneratePrivateKeyFromRand(rng)
		testPublicKey2 := testSecretKey2.Public()

		// Create signing keyshare first
		signingKeyshare, err := tx.SigningKeyshare.Create().
			SetStatus(st.KeyshareStatusAvailable).
			SetSecretShare(testSecretKey2.Serialize()).
			SetPublicShares(map[string][]byte{"test": testPublicKey2.Serialize()}).
			SetPublicKey(testPublicKey2.Serialize()).
			SetMinSigners(2).
			SetCoordinatorIndex(0).
			Save(ctx)
		require.NoError(t, err)

		testIdentityKey2 := keys.MustGeneratePrivateKeyFromRand(rng)
		testSigningKey2 := keys.MustGeneratePrivateKeyFromRand(rng)

		// Create deposit address
		depositAddress, err := tx.DepositAddress.Create().
			SetAddress("test_address2").
			SetOwnerIdentityPubkey(testIdentityKey2.Public()).
			SetOwnerSigningPubkey(testSigningKey2.Public()).
			SetSigningKeyshare(signingKeyshare).
			SetNetwork(st.NetworkRegtest).
			Save(ctx)
		require.NoError(t, err)

		// Test verification with not yet mined utxo
		_, err = VerifiedTargetUtxo(ctx, config, tx, st.NetworkRegtest, []byte("test_txid2"), 1)
		require.Error(t, err)
		grpcError, ok := status.FromError(err)
		require.True(t, ok)
		assert.Equal(t, codes.FailedPrecondition, grpcError.Code())
		assert.Equal(t, fmt.Sprintf("utxo not found: txid: %s vout: 1", hex.EncodeToString([]byte("test_txid2"))), grpcError.Message())

		// Create UTXO with insufficient confirmations
		utxoBlockHeight := blockHeight - int(config.BitcoindConfigs["regtest"].DepositConfirmationThreshold) + 2
		_, err = tx.Utxo.Create().
			SetNetwork(st.NetworkRegtest).
			SetTxid([]byte("test_txid2")).
			SetVout(1).
			SetBlockHeight(int64(utxoBlockHeight)).
			SetAmount(1000).
			SetPkScript([]byte("test_script")).
			SetDepositAddress(depositAddress).
			Save(ctx)
		require.NoError(t, err)

		// Test verification
		_, err = VerifiedTargetUtxo(ctx, config, tx, st.NetworkRegtest, []byte("test_txid2"), 1)
		require.ErrorContains(t, err, "deposit tx doesn't have enough confirmations")
	})
}

func TestGenerateDepositAddress(t *testing.T) {
	ctx, dbCtx := db.NewTestSQLiteContext(t, t.Context())
	defer dbCtx.Close()
	rng := rand.NewChaCha8([32]byte{})

	testIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	testIdentityPubKey := testIdentityPrivKey.Public()

	testSigningPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	testSigningPubKey := testSigningPrivKey.Public()

	// Setup test configuration using supported networks
	config := &so.Config{
		SupportedNetworks: []common.Network{
			common.Regtest,
			common.Mainnet,
		},
		SigningOperatorMap: map[string]*so.SigningOperator{},
		BitcoindConfigs: map[string]so.BitcoindConfig{
			"regtest": {
				DepositConfirmationThreshold: 1,
			},
		},
		FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{},
	}

	handler := NewDepositHandler(config)

	t.Run("prevent duplicate static deposit address for same identity", func(t *testing.T) {
		tx, err := ent.GetDbFromContext(ctx)
		require.NoError(t, err)

		// Generate valid secp256k1 operator public key
		operatorPrivKey2 := keys.MustGeneratePrivateKeyFromRand(rng)
		operatorPubKey2 := operatorPrivKey2.Public()
		testSecretKey := keys.MustGeneratePrivateKeyFromRand(rng)

		// Create a signing keyshare
		signingKeyshare, err := tx.SigningKeyshare.Create().
			SetStatus(st.KeyshareStatusAvailable).
			SetSecretShare(testSecretKey.Serialize()).
			SetPublicShares(map[string][]byte{"test": testSecretKey.Public().Serialize()}).
			SetPublicKey(operatorPubKey2.Serialize()).
			SetMinSigners(2).
			SetCoordinatorIndex(0).
			Save(ctx)
		require.NoError(t, err)

		// Create an existing static deposit address
		existingAddress, err := tx.DepositAddress.Create().
			SetAddress("bcrt1p52zf7gf7pvhvpsje2z0uzcr8nhdd79lund68qaea54kprnxcsdqqt2jz6e").
			SetOwnerIdentityPubkey(testIdentityPubKey).
			SetOwnerSigningPubkey(testSigningPubKey).
			SetSigningKeyshare(signingKeyshare).
			SetIsStatic(true).
			SetNetwork(st.NetworkRegtest).
			Save(ctx)
		require.NoError(t, err)
		require.NotNil(t, existingAddress)

		testConfig := &so.Config{
			SupportedNetworks: []common.Network{
				common.Regtest,
			},
			SigningOperatorMap:         map[string]*so.SigningOperator{},
			FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{},
		}

		isStatic := true
		req := &pb.GenerateDepositAddressRequest{
			SigningPublicKey:  testSigningPubKey.Serialize(),
			IdentityPublicKey: testIdentityPubKey.Serialize(),
			Network:           pb.Network_REGTEST,
			IsStatic:          &isStatic,
		}

		_, err = handler.GenerateDepositAddress(ctx, testConfig, req)
		require.ErrorContains(t, err, "static deposit address already exists: bcrt1p")
		previousError := err.Error()
		_, err = handler.GenerateDepositAddress(ctx, testConfig, req)
		require.EqualError(t, err, previousError)
	})

	t.Run("allow static deposit address for same identity on different network", func(t *testing.T) {
		testConfig := &so.Config{
			SupportedNetworks: []common.Network{
				common.Regtest,
				common.Mainnet,
			},
			SigningOperatorMap:         map[string]*so.SigningOperator{},
			FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{},
		}

		isStatic := true
		req := &pb.GenerateDepositAddressRequest{
			SigningPublicKey:  testSigningPubKey.Serialize(),
			IdentityPublicKey: testIdentityPubKey.Serialize(),
			Network:           pb.Network_MAINNET,
			IsStatic:          &isStatic,
		}

		// Testing that the handler tries to create a new address
		_, err := handler.GenerateDepositAddress(ctx, testConfig, req)
		require.ErrorContains(t, err, "near \"SET\": syntax error")
	})
}

func TestGenerateStaticDepositAddress(t *testing.T) {
	ctx, dbCtx := db.NewTestSQLiteContext(t, t.Context())
	defer dbCtx.Close()
	rng := rand.NewChaCha8([32]byte{})

	testIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	testSigningPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	// Set up test configuration using supported networks
	config := &so.Config{
		SupportedNetworks:  []common.Network{common.Regtest, common.Mainnet},
		SigningOperatorMap: map[string]*so.SigningOperator{},
		BitcoindConfigs: map[string]so.BitcoindConfig{
			"regtest": {DepositConfirmationThreshold: 1},
		},
		FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{},
	}

	handler := NewDepositHandler(config)

	t.Run("allow static deposit address for same identity on different network", func(t *testing.T) {
		testConfig := &so.Config{
			SupportedNetworks:          []common.Network{common.Regtest, common.Mainnet},
			SigningOperatorMap:         map[string]*so.SigningOperator{},
			FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{},
		}

		req := &pb.GenerateStaticDepositAddressRequest{
			SigningPublicKey:  testSigningPrivKey.Public().Serialize(),
			IdentityPublicKey: testIdentityPrivKey.Public().Serialize(),
			Network:           pb.Network_MAINNET,
		}

		// Testing that the handler tries to create a new address
		_, err := handler.GenerateStaticDepositAddress(ctx, testConfig, req)
		require.Error(t, err, "near \"SET\": syntax error")
	})
}

func TestGenerateStaticDepositAddressReturnsDefaultAddress(t *testing.T) {
	config := &so.Config{
		BitcoindConfigs: map[string]so.BitcoindConfig{
			"regtest": {
				DepositConfirmationThreshold: 1,
			},
		},
		FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{},
		SupportedNetworks: []common.Network{
			common.Regtest,
			common.Mainnet,
		},
	}
	ctx, dbCtx := db.NewTestSQLiteContext(t, t.Context())
	defer dbCtx.Close()

	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	rng := rand.NewChaCha8([32]byte{})
	testSigningPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	testIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)

	keyshare1Key := keys.MustGeneratePrivateKeyFromRand(rng)
	signingKeyshare1, err := tx.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare([]byte("test_secret_share1")).
		SetPublicShares(map[string][]byte{"test": []byte("test_public_share1")}).
		SetPublicKey(keyshare1Key.Public().Serialize()).
		SetMinSigners(2).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)

	// Create deposit address
	depositAddress1, err := tx.DepositAddress.Create().
		SetAddress("test_address1").
		SetOwnerIdentityPubkey(testIdentityPrivKey.Public()).
		SetOwnerSigningPubkey(testSigningPrivKey.Public()).
		SetSigningKeyshare(signingKeyshare1).
		SetNetwork(st.NetworkRegtest).
		SetIsStatic(true).
		SetIsDefault(true).
		SetAddressSignatures(map[string][]byte{"test": []byte("test_address_signature2")}).
		SetPossessionSignature([]byte("test_possession_signature2")).
		Save(ctx)
	require.NoError(t, err)

	keyshare2Key := keys.MustGeneratePrivateKeyFromRand(rng)
	signingKeyshare2, err := tx.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare([]byte("test_secret_share2")).
		SetPublicShares(map[string][]byte{"test": []byte("test_public_share2")}).
		SetPublicKey(keyshare2Key.Public().Serialize()).
		SetMinSigners(2).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)

	// Create deposit address
	_, err = tx.DepositAddress.Create().
		SetAddress("test_address2").
		SetOwnerIdentityPubkey(testIdentityPrivKey.Public()).
		SetOwnerSigningPubkey(testSigningPrivKey.Public()).
		SetSigningKeyshare(signingKeyshare2).
		SetNetwork(st.NetworkRegtest).
		SetIsStatic(true).
		SetIsDefault(false).
		SetAddressSignatures(map[string][]byte{"test": []byte("test_address_signature2")}).
		SetPossessionSignature([]byte("test_possession_signature2")).
		Save(ctx)
	require.NoError(t, err)

	req := &pb.GenerateStaticDepositAddressRequest{
		SigningPublicKey:  testSigningPrivKey.Public().Serialize(),
		IdentityPublicKey: testIdentityPrivKey.Public().Serialize(),
		Network:           pb.Network_REGTEST,
	}

	handler := NewDepositHandler(config)
	response, err := handler.GenerateStaticDepositAddress(ctx, config, req)
	require.NoError(t, err)
	require.Equal(t, depositAddress1.Address, response.DepositAddress.Address)
	require.Equal(t, depositAddress1.AddressSignatures, response.DepositAddress.DepositAddressProof.AddressSignatures)
	require.Equal(t, depositAddress1.PossessionSignature, response.DepositAddress.DepositAddressProof.ProofOfPossessionSignature)

}

func TestGetUtxosFromAddress(t *testing.T) {
	ctx, dbCtx := db.NewTestSQLiteContext(t, t.Context())
	defer dbCtx.Close()
	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	rng := rand.NewChaCha8([32]byte{})

	// Create block height records for both networks
	_, err = tx.BlockHeight.Create().
		SetNetwork(st.NetworkRegtest).
		SetHeight(200).
		Save(ctx)
	require.NoError(t, err)

	_, err = tx.BlockHeight.Create().
		SetNetwork(st.NetworkMainnet).
		SetHeight(200).
		Save(ctx)
	require.NoError(t, err)

	testIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	testIdentityPubKey := testIdentityPrivKey.Public()
	testSigningPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	testSigningPubKey := testSigningPrivKey.Public()
	secretShare := keys.MustGeneratePrivateKeyFromRand(rng)

	signingKeyshare, err := tx.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(secretShare.Serialize()).
		SetPublicShares(map[string][]byte{"test": secretShare.Public().Serialize()}).
		SetPublicKey(secretShare.Public().Serialize()).
		SetMinSigners(2).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)

	handler := NewDepositHandler(&so.Config{FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{}})

	t.Run("static deposit address with UTXOs", func(t *testing.T) {
		// Create static deposit address
		staticAddress := "bcrt1p52zf7gf7pvhvpsje2z0uzcr8nhdd79lund68qaea54kprnxcsdqqt2jz6e"
		depositAddress, err := tx.DepositAddress.Create().
			SetAddress(staticAddress).
			SetOwnerIdentityPubkey(testIdentityPubKey).
			SetOwnerSigningPubkey(testSigningPubKey).
			SetSigningKeyshare(signingKeyshare).
			SetIsStatic(true).
			SetNetwork(st.NetworkRegtest).
			Save(ctx)
		require.NoError(t, err)

		// Create some UTXOs for this address with sufficient confirmations
		_, err = tx.Utxo.Create().
			SetNetwork(st.NetworkRegtest).
			SetTxid([]byte("test_txid_1")).
			SetVout(0).
			SetBlockHeight(100).
			SetAmount(1000).
			SetPkScript([]byte("test_script_1")).
			SetDepositAddress(depositAddress).
			Save(ctx)
		require.NoError(t, err)

		_, err = tx.Utxo.Create().
			SetNetwork(st.NetworkRegtest).
			SetTxid([]byte("test_txid_2")).
			SetVout(1).
			SetBlockHeight(101).
			SetAmount(2000).
			SetPkScript([]byte("test_script_2")).
			SetDepositAddress(depositAddress).
			Save(ctx)
		require.NoError(t, err)

		req := &pb.GetUtxosForAddressRequest{
			Address: staticAddress,
			Network: pb.Network_REGTEST,
			Offset:  0,
			Limit:   10,
		}

		response, err := handler.GetUtxosForAddress(ctx, req)
		require.NoError(t, err)
		require.Len(t, response.Utxos, 2)

		// Check that both UTXOs are returned with correct fields
		txids := make(map[string]bool)
		for _, utxo := range response.Utxos {
			txids[hex.EncodeToString(utxo.Txid)] = true
			assert.Equal(t, pb.Network_REGTEST, utxo.Network)
		}
		assert.True(t, txids["746573745f747869645f31"]) // "test_txid_1" in hex
		assert.True(t, txids["746573745f747869645f32"]) // "test_txid_2" in hex
	})

	t.Run("static deposit address with no UTXOs", func(t *testing.T) {
		// Create static deposit address with no UTXOs
		staticAddress := "bcrt1p52zf7gf7pvhvpsje2z0uzcr8nhdd79lund68qaea54kprnxcsdqqt2jz6e2"
		rng := rand.NewChaCha8([32]byte{2})
		testIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
		testSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
		_, err = tx.DepositAddress.Create().
			SetAddress(staticAddress).
			SetOwnerIdentityPubkey(testIdentityPubKey).
			SetOwnerSigningPubkey(testSigningPubKey).
			SetSigningKeyshare(signingKeyshare).
			SetIsStatic(true).
			SetNetwork(st.NetworkRegtest).
			Save(ctx)
		require.NoError(t, err)

		req := &pb.GetUtxosForAddressRequest{
			Address: staticAddress,
			Network: pb.Network_REGTEST,
			Offset:  0,
			Limit:   10,
		}

		response, err := handler.GetUtxosForAddress(ctx, req)
		require.NoError(t, err)
		require.Empty(t, response.Utxos)
	})

	t.Run("non-static deposit address with confirmation txid", func(t *testing.T) {
		// Create non-static deposit address with confirmation txid
		nonStaticAddress := "bcrt1p52zf7gf7pvhvpsje2z0uzcr8nhdd79lund68qaea54kprnxcsdqqt2jz6e3"
		confirmationTxid := "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"
		_, err := tx.DepositAddress.Create().
			SetAddress(nonStaticAddress).
			SetOwnerIdentityPubkey(testIdentityPubKey).
			SetOwnerSigningPubkey(testSigningPubKey).
			SetSigningKeyshare(signingKeyshare).
			SetIsStatic(false).
			SetConfirmationTxid(confirmationTxid).
			SetConfirmationHeight(195). // Set confirmation height to satisfy threshold (current height 200 - 3 = 197, so <= 197)
			SetNetwork(st.NetworkRegtest).
			Save(ctx)
		require.NoError(t, err)

		req := &pb.GetUtxosForAddressRequest{
			Address: nonStaticAddress,
			Network: pb.Network_REGTEST,
			Offset:  0,
			Limit:   10,
		}

		response, err := handler.GetUtxosForAddress(ctx, req)
		require.NoError(t, err)
		require.Len(t, response.Utxos, 1)
		assert.Equal(t, confirmationTxid, hex.EncodeToString(response.Utxos[0].Txid))
	})

	t.Run("non-static deposit address without confirmation txid", func(t *testing.T) {
		// Create non-static deposit address without confirmation txid
		nonStaticAddress := "bcrt1p52zf7gf7pvhvpsje2z0uzcr8nhdd79lund68qaea54kprnxcsdqqt2jz6e4"
		_, err := tx.DepositAddress.Create().
			SetAddress(nonStaticAddress).
			SetOwnerIdentityPubkey(testIdentityPubKey).
			SetOwnerSigningPubkey(testSigningPubKey).
			SetSigningKeyshare(signingKeyshare).
			SetIsStatic(false).
			SetNetwork(st.NetworkRegtest).
			Save(ctx)
		require.NoError(t, err)

		req := &pb.GetUtxosForAddressRequest{
			Address: nonStaticAddress,
			Network: pb.Network_REGTEST,
			Offset:  0,
			Limit:   10,
		}

		response, err := handler.GetUtxosForAddress(ctx, req)
		require.NoError(t, err)
		require.Empty(t, response.Utxos)
	})

	t.Run("deposit address not found", func(t *testing.T) {
		req := &pb.GetUtxosForAddressRequest{
			Address: "nonexistent_address",
			Network: pb.Network_REGTEST,
			Offset:  0,
			Limit:   10,
		}

		_, err := handler.GetUtxosForAddress(ctx, req)
		require.ErrorContains(t, err, "failed to get deposit address")
	})

	t.Run("pagination limits", func(t *testing.T) {
		// Create static deposit address
		staticAddress := "bcrt1p52zf7gf7pvhvpsje2z0uzcr8nhdd79lund68qaea54kprnxcsdqqt2jz6e5"
		rng := rand.NewChaCha8([32]byte{3})
		testIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
		testSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
		depositAddress, err := tx.DepositAddress.Create().
			SetAddress(staticAddress).
			SetOwnerIdentityPubkey(testIdentityPubKey).
			SetOwnerSigningPubkey(testSigningPubKey).
			SetSigningKeyshare(signingKeyshare).
			SetIsStatic(true).
			SetNetwork(st.NetworkRegtest).
			Save(ctx)
		require.NoError(t, err)

		// Create multiple UTXOs with sufficient confirmations
		for i := 0; i < 5; i++ {
			_, err := tx.Utxo.Create().
				SetNetwork(st.NetworkRegtest).
				SetTxid([]byte(fmt.Sprintf("test_txid_%d", i))).
				SetVout(uint32(i)).
				SetBlockHeight(int64(100 + i)).
				SetAmount(uint64(1000 + i*100)).
				SetPkScript([]byte(fmt.Sprintf("test_script_%d", i))).
				SetDepositAddress(depositAddress).
				Save(ctx)
			require.NoError(t, err)
		}

		// Test limit enforcement
		req := &pb.GetUtxosForAddressRequest{
			Address: staticAddress,
			Network: pb.Network_REGTEST,
			Offset:  0,
			Limit:   3, // Should be limited to 3
		}

		response, err := handler.GetUtxosForAddress(ctx, req)
		require.NoError(t, err)
		require.Len(t, response.Utxos, 3)

		// Test offset
		req.Offset = 2
		req.Limit = 10
		response, err = handler.GetUtxosForAddress(ctx, req)
		require.NoError(t, err)
		require.Len(t, response.Utxos, 3) // Should return remaining 3 UTXOs

		// Test invalid limit (should be clamped to 100)
		req.Offset = 0
		req.Limit = 150
		response, err = handler.GetUtxosForAddress(ctx, req)
		require.NoError(t, err)
		require.Len(t, response.Utxos, 5) // Should return all 5 UTXOs

		// Test zero limit (should be clamped to 100)
		req.Limit = 0
		response, err = handler.GetUtxosForAddress(ctx, req)
		require.NoError(t, err)
		require.Len(t, response.Utxos, 5) // Should return all 5 UTXOs
	})

	t.Run("invalid confirmation txid", func(t *testing.T) {
		// Create non-static deposit address with invalid confirmation txid
		nonStaticAddress := "bcrt1p52zf7gf7pvhvpsje2z0uzcr8nhdd79lund68qaea54kprnxcsdqqt2jz6e6"
		invalidTxid := "invalid_hex_string"
		_, err := tx.DepositAddress.Create().
			SetAddress(nonStaticAddress).
			SetOwnerIdentityPubkey(testIdentityPubKey).
			SetOwnerSigningPubkey(testSigningPubKey).
			SetSigningKeyshare(signingKeyshare).
			SetIsStatic(false).
			SetConfirmationTxid(invalidTxid).
			SetNetwork(st.NetworkRegtest).
			Save(ctx)
		require.NoError(t, err)

		req := &pb.GetUtxosForAddressRequest{
			Address: nonStaticAddress,
			Network: pb.Network_REGTEST,
			Offset:  0,
			Limit:   10,
		}

		_, err = handler.GetUtxosForAddress(ctx, req)
		require.ErrorContains(t, err, "failed to decode confirmation txid")
	})

	t.Run("static deposit address with insufficient confirmations", func(t *testing.T) {
		// Create static deposit address
		staticAddress := "bcrt1p52zf7gf7pvhvpsje2z0uzcr8nhdd79lund68qaea54kprnxcsdqqt2jz6e7"
		rng := rand.NewChaCha8([32]byte{4})
		testIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
		testSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
		depositAddress, err := tx.DepositAddress.Create().
			SetAddress(staticAddress).
			SetOwnerIdentityPubkey(testIdentityPubKey).
			SetOwnerSigningPubkey(testSigningPubKey).
			SetSigningKeyshare(signingKeyshare).
			SetIsStatic(true).
			SetNetwork(st.NetworkRegtest).
			Save(ctx)
		require.NoError(t, err)

		// Create UTXO with insufficient confirmations (block height too recent)
		_, err = tx.Utxo.Create().
			SetNetwork(st.NetworkRegtest).
			SetTxid([]byte("test_txid_recent")).
			SetVout(0).
			SetBlockHeight(198). // Current height is 200, so only 2 confirmations
			SetAmount(1000).
			SetPkScript([]byte("test_script_recent")).
			SetDepositAddress(depositAddress).
			Save(ctx)
		require.NoError(t, err)

		req := &pb.GetUtxosForAddressRequest{
			Address: staticAddress,
			Network: pb.Network_REGTEST,
			Offset:  0,
			Limit:   10,
		}

		response, err := handler.GetUtxosForAddress(ctx, req)
		require.NoError(t, err)
		require.Empty(t, response.Utxos) // Should not return UTXO with insufficient confirmations
	})

	t.Run("network validation error", func(t *testing.T) {
		// Create static deposit address
		staticAddress := "bcrt1p52zf7gf7pvhvpsje2z0uzcr8nhdd79lund68qaea54kprnxcsdqqt2jz6e8"
		rng := rand.NewChaCha8([32]byte{5})
		testIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
		testSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
		_, err := tx.DepositAddress.Create().
			SetAddress(staticAddress).
			SetOwnerIdentityPubkey(testIdentityPubKey).
			SetOwnerSigningPubkey(testSigningPubKey).
			SetSigningKeyshare(signingKeyshare).
			SetIsStatic(true).
			SetNetwork(st.NetworkRegtest).
			Save(ctx)
		require.NoError(t, err)

		req := &pb.GetUtxosForAddressRequest{
			Address: staticAddress,
			Network: pb.Network_MAINNET, // Wrong network for regtest address
			Offset:  0,
			Limit:   10,
		}

		_, err = handler.GetUtxosForAddress(ctx, req)
		require.ErrorContains(t, err, "deposit address is not aligned with the requested network")
	})

	t.Run("multiple deposit addresses with UTXOs - verify correct filtering", func(t *testing.T) {
		// This test is to verify that the correct UTXOs are returned when a user
		// has multiple static deposit addresses. A user should only have one static
		// deposit address that is the default address, but this was not enforced
		// initially so there are legacy cases where a user may have multiple static
		// deposit addresses.

		// Create first static deposit address
		staticAddress1 := "bcrt1p52zf7gf7pvhvpsje2z0uzcr8nhdd79lund68qaea54kprnxcsdqqt2jz6e9"
		rng := rand.NewChaCha8([32]byte{6})
		testIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
		testSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
		depositAddress1, err := tx.DepositAddress.Create().
			SetAddress(staticAddress1).
			SetOwnerIdentityPubkey(testIdentityPubKey).
			SetOwnerSigningPubkey(testSigningPubKey).
			SetSigningKeyshare(signingKeyshare).
			SetIsStatic(true).
			SetIsDefault(true).
			SetNetwork(st.NetworkRegtest).
			Save(ctx)
		require.NoError(t, err)

		// Create second static deposit address
		staticAddress2 := "bcrt1p52zf7gf7pvhvpsje2z0uzcr8nhdd79lund68qaea54kprnxcsdqqt2jz6ea"
		depositAddress2, err := tx.DepositAddress.Create().
			SetAddress(staticAddress2).
			SetOwnerIdentityPubkey(testIdentityPubKey).
			SetOwnerSigningPubkey(testSigningPubKey).
			SetSigningKeyshare(signingKeyshare).
			SetIsStatic(true).
			SetIsDefault(false).
			SetNetwork(st.NetworkRegtest).
			Save(ctx)
		require.NoError(t, err)

		// Create UTXOs for first address
		_, err = tx.Utxo.Create().
			SetNetwork(st.NetworkRegtest).
			SetTxid([]byte("address1_txid_1")).
			SetVout(0).
			SetBlockHeight(100).
			SetAmount(1000).
			SetPkScript([]byte("address1_script_1")).
			SetDepositAddress(depositAddress1).
			Save(ctx)
		require.NoError(t, err)

		_, err = tx.Utxo.Create().
			SetNetwork(st.NetworkRegtest).
			SetTxid([]byte("address1_txid_2")).
			SetVout(1).
			SetBlockHeight(101).
			SetAmount(2000).
			SetPkScript([]byte("address1_script_2")).
			SetDepositAddress(depositAddress1).
			Save(ctx)
		require.NoError(t, err)

		// Create UTXOs for second address
		_, err = tx.Utxo.Create().
			SetNetwork(st.NetworkRegtest).
			SetTxid([]byte("address2_txid_1")).
			SetVout(0).
			SetBlockHeight(102).
			SetAmount(3000).
			SetPkScript([]byte("address2_script_1")).
			SetDepositAddress(depositAddress2).
			Save(ctx)
		require.NoError(t, err)

		_, err = tx.Utxo.Create().
			SetNetwork(st.NetworkRegtest).
			SetTxid([]byte("address2_txid_2")).
			SetVout(1).
			SetBlockHeight(103).
			SetAmount(4000).
			SetPkScript([]byte("address2_script_2")).
			SetDepositAddress(depositAddress2).
			Save(ctx)
		require.NoError(t, err)

		// Test that querying first address only returns its UTXOs
		req1 := &pb.GetUtxosForAddressRequest{
			Address: staticAddress1,
			Network: pb.Network_REGTEST,
			Offset:  0,
			Limit:   10,
		}

		response1, err := handler.GetUtxosForAddress(ctx, req1)
		require.NoError(t, err)
		require.Len(t, response1.Utxos, 2)

		// Verify only address1 UTXOs are returned
		txids1 := make(map[string]bool)
		for _, utxo := range response1.Utxos {
			txids1[hex.EncodeToString(utxo.Txid)] = true
			assert.Equal(t, pb.Network_REGTEST, utxo.Network)
		}
		assert.True(t, txids1["61646472657373315f747869645f31"])  // "address1_txid_1" in hex
		assert.True(t, txids1["61646472657373315f747869645f32"])  // "address1_txid_2" in hex
		assert.False(t, txids1["61646472657373325f747869645f31"]) // "address2_txid_1" in hex - should not be present
		assert.False(t, txids1["61646472657373325f747869645f32"]) // "address2_txid_2" in hex - should not be present

		// Test that querying second address only returns its UTXOs
		req2 := &pb.GetUtxosForAddressRequest{
			Address: staticAddress2,
			Network: pb.Network_REGTEST,
			Offset:  0,
			Limit:   10,
		}

		response2, err := handler.GetUtxosForAddress(ctx, req2)
		require.NoError(t, err)
		require.Len(t, response2.Utxos, 2)

		// Verify only address2 UTXOs are returned
		txids2 := make(map[string]bool)
		for _, utxo := range response2.Utxos {
			txids2[hex.EncodeToString(utxo.Txid)] = true
			assert.Equal(t, pb.Network_REGTEST, utxo.Network)
		}
		assert.True(t, txids2["61646472657373325f747869645f31"])  // "address2_txid_1" in hex
		assert.True(t, txids2["61646472657373325f747869645f32"])  // "address2_txid_2" in hex
		assert.False(t, txids2["61646472657373315f747869645f31"]) // "address1_txid_1" in hex - should not be present
		assert.False(t, txids2["61646472657373315f747869645f32"]) // "address1_txid_2" in hex - should not be present
	})

	t.Run("UTXOs with UTXO swaps - verify correct filtering", func(t *testing.T) {
		// Create static deposit address
		staticAddress := "bcrt1p52zf7gf7pvhvpsje2z0uzcr8nhdd79lund68qaea54kprnxcsdqqt2jzeb"
		rng := rand.NewChaCha8([32]byte{7})
		testIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
		testSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
		depositAddress, err := tx.DepositAddress.Create().
			SetAddress(staticAddress).
			SetOwnerIdentityPubkey(testIdentityPubKey).
			SetOwnerSigningPubkey(testSigningPubKey).
			SetSigningKeyshare(signingKeyshare).
			SetIsStatic(true).
			SetNetwork(st.NetworkRegtest).
			Save(ctx)
		require.NoError(t, err)

		// Create UTXOs for this address with sufficient confirmations
		// UTXO 1: Will have an active UTXO swap (should be excluded)
		utxo1, err := tx.Utxo.Create().
			SetNetwork(st.NetworkRegtest).
			SetTxid([]byte("swap_test_txid_1")).
			SetVout(0).
			SetBlockHeight(100).
			SetAmount(1000).
			SetPkScript([]byte("swap_test_script_1")).
			SetDepositAddress(depositAddress).
			Save(ctx)
		require.NoError(t, err)

		// UTXO 2: Will have a cancelled UTXO swap (should be included)
		utxo2, err := tx.Utxo.Create().
			SetNetwork(st.NetworkRegtest).
			SetTxid([]byte("swap_test_txid_2")).
			SetVout(1).
			SetBlockHeight(101).
			SetAmount(2000).
			SetPkScript([]byte("swap_test_script_2")).
			SetDepositAddress(depositAddress).
			Save(ctx)
		require.NoError(t, err)

		// UTXO 3: No UTXO swap (should be included)
		_, err = tx.Utxo.Create().
			SetNetwork(st.NetworkRegtest).
			SetTxid([]byte("swap_test_txid_3")).
			SetVout(2).
			SetBlockHeight(102).
			SetAmount(3000).
			SetPkScript([]byte("swap_test_script_3")).
			SetDepositAddress(depositAddress).
			Save(ctx)
		require.NoError(t, err)

		// Create active UTXO swap for utxo1 (this should exclude utxo1 from results)
		_, err = tx.UtxoSwap.Create().
			SetStatus(st.UtxoSwapStatusCreated). // Active status
			SetRequestType(st.UtxoSwapRequestTypeFixedAmount).
			SetCoordinatorIdentityPublicKey(keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize()).
			SetUtxo(utxo1).
			Save(ctx)
		require.NoError(t, err)

		// Create cancelled UTXO swap for utxo2 (this should NOT exclude utxo2 from results)
		_, err = tx.UtxoSwap.Create().
			SetStatus(st.UtxoSwapStatusCancelled). // Cancelled status
			SetRequestType(st.UtxoSwapRequestTypeFixedAmount).
			SetCoordinatorIdentityPublicKey(keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize()).
			SetUtxo(utxo2).
			Save(ctx)
		require.NoError(t, err)

		// Test GetUtxosForAddress - should return only UTXOs without active swaps
		req := &pb.GetUtxosForAddressRequest{
			Address:        staticAddress,
			Network:        pb.Network_REGTEST,
			Offset:         0,
			Limit:          10,
			ExcludeClaimed: true,
		}

		response, err := handler.GetUtxosForAddress(ctx, req)
		require.NoError(t, err)
		require.Len(t, response.Utxos, 2) // Should return utxo2 (cancelled swap) and utxo3 (no swap)

		// Verify the correct UTXOs are returned
		txids := make(map[string]bool)
		for _, utxo := range response.Utxos {
			txids[hex.EncodeToString(utxo.Txid)] = true
			assert.Equal(t, pb.Network_REGTEST, utxo.Network)
		}

		// Should include utxo2 (cancelled swap) and utxo3 (no swap)
		assert.Contains(t, txids, hex.EncodeToString([]byte("swap_test_txid_2")))
		assert.Contains(t, txids, hex.EncodeToString([]byte("swap_test_txid_3")))

		// Should NOT include utxo1 (active swap)
		assert.NotContains(t, txids, hex.EncodeToString([]byte("swap_test_txid_1")))

		// Not specifying exclude claimed should return all UTXOs
		req = &pb.GetUtxosForAddressRequest{
			Address:        staticAddress,
			Network:        pb.Network_REGTEST,
			Offset:         0,
			Limit:          10,
			ExcludeClaimed: false,
		}

		response, err = handler.GetUtxosForAddress(ctx, req)
		require.NoError(t, err)
		require.Len(t, response.Utxos, 3)
	})
}
