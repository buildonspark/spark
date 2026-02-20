package tokens

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	tokeninternalpb "github.com/lightsparkdev/spark/proto/spark_token_internal"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/entfixtures"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/utils"
	sparktesting "github.com/lightsparkdev/spark/testing"
)

// TestPrepareTokenTransactionInternal_NetworkValidation ensures we correctly validate network matching.
func TestPrepareTokenTransactionInternal_NetworkValidation(t *testing.T) {
	testCases := []struct {
		name        string
		tokenNet    btcnetwork.Network
		txNet       btcnetwork.Network
		expectError bool
	}{
		{
			name:        "mainnet token, regtest tx should fail",
			tokenNet:    btcnetwork.Mainnet,
			txNet:       btcnetwork.Regtest,
			expectError: true,
		},
		{
			name:        "regtest token, mainnet tx should fail",
			tokenNet:    btcnetwork.Regtest,
			txNet:       btcnetwork.Mainnet,
			expectError: true,
		},
		{
			name:        "mainnet token, mainnet tx should succeed",
			tokenNet:    btcnetwork.Mainnet,
			txNet:       btcnetwork.Mainnet,
			expectError: false,
		},
		{
			name:        "regtest token, regtest tx should succeed",
			tokenNet:    btcnetwork.Regtest,
			txNet:       btcnetwork.Regtest,
			expectError: false,
		},
	}

	cfg := sparktesting.TestConfig(t)

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rng := rand.NewChaCha8([32]byte{})
			ctx, _ := db.ConnectToTestPostgres(t)
			dbtx, err := ent.GetDbFromContext(ctx)
			require.NoError(t, err)
			f := entfixtures.New(t, ctx, dbtx).WithRNG(rng)
			handler := NewInternalPrepareTokenHandler(cfg)

			issuerPriv := keys.MustGeneratePrivateKeyFromRand(rng)
			tokenCreate := dbtx.TokenCreate.Create().
				SetIssuerPublicKey(issuerPriv.Public()).
				SetTokenName("TT").
				SetTokenTicker("TT").
				SetDecimals(8).
				SetMaxSupply([]byte{0}).
				SetIsFreezable(true).
				SetNetwork(tc.tokenNet).
				SetTokenIdentifier(f.RandomBytes(32)).
				SetCreationEntityPublicKey(issuerPriv.Public()).
				SaveX(ctx)

			secretShare := keys.MustGeneratePrivateKeyFromRand(rng)
			ks := dbtx.SigningKeyshare.Create().
				SetSecretShare(secretShare).
				SetPublicKey(issuerPriv.Public()).
				SetStatus(st.KeyshareStatusAvailable).
				SetPublicShares(map[string]keys.Public{}).
				SetMinSigners(1).
				SetCoordinatorIndex(1).
				SaveX(ctx)

			_ = dbtx.EntityDkgKey.Create().
				SetSigningKeyshare(ks).
				SaveX(ctx)

			now := time.Now()
			txProto := &tokenpb.TokenTransaction{
				Version: 2,
				TokenInputs: &tokenpb.TokenTransaction_MintInput{
					MintInput: &tokenpb.TokenMintInput{
						IssuerPublicKey: issuerPriv.Public().Serialize(),
						TokenIdentifier: tokenCreate.TokenIdentifier,
					},
				},
				TokenOutputs: []*tokenpb.TokenOutput{
					{
						Id:                   proto.String(uuid.Must(uuid.NewV7()).String()),
						OwnerPublicKey:       issuerPriv.Public().Serialize(),
						TokenIdentifier:      tokenCreate.TokenIdentifier,
						TokenAmount:          []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 10},
						RevocationCommitment: ks.PublicKey.Serialize(),
					},
				},
				ExpiryTime:             timestamppb.New(now.Add(24 * time.Hour)),
				ClientCreatedTimestamp: timestamppb.New(now),
			}

			pbNet, err := tc.txNet.MarshalProto()
			require.NoError(t, err)
			txProto.Network = pbNet
			for _, op := range handler.config.GetSigningOperatorList() {
				txProto.SparkOperatorIdentityPublicKeys = append(txProto.SparkOperatorIdentityPublicKeys, op.PublicKey)
			}
			netCommon, err := btcnetwork.FromProtoNetwork(pbNet)
			require.NoError(t, err)
			cfgVals := handler.config.Lrc20Configs[strings.ToLower(netCommon.String())]
			txProto.TokenOutputs[0].WithdrawBondSats = &cfgVals.WithdrawBondSats
			txProto.TokenOutputs[0].WithdrawRelativeBlockLocktime = &cfgVals.WithdrawRelativeBlockLocktime

			partialHash, err := utils.HashTokenTransaction(txProto, true)
			require.NoError(t, err)
			schnorrSig, err := schnorr.Sign(issuerPriv.ToBTCEC(), partialHash)
			require.NoError(t, err)
			sig := schnorrSig.Serialize()

			operatorList := handler.config.GetSigningOperatorList()
			var firstOperator *sparkpb.SigningOperatorInfo
			for _, operator := range operatorList {
				firstOperator = operator
				break
			}
			req := &tokeninternalpb.PrepareTransactionRequest{
				FinalTokenTransaction:      txProto,
				TokenTransactionSignatures: []*tokenpb.SignatureWithIndex{{InputIndex: 0, Signature: sig}},
				KeyshareIds:                []string{ks.ID.String()},
				CoordinatorPublicKey:       firstOperator.PublicKey,
			}

			_, err = handler.PrepareTokenTransactionInternal(ctx, req)

			if tc.expectError {
				require.ErrorContains(t, err, fmt.Sprintf("transaction network %s does not match token network %s", tc.txNet, tc.tokenNet))
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestPrepareTokenTransactionInternal_MintIssuerAuthorizationCheck verifies that minting
// is rejected when the issuer public key in the mint request does not match the original
// token creator's public key. This test guards against unauthorized token minting.
func TestPrepareTokenTransactionInternal_MintIssuerAuthorizationCheck(t *testing.T) {
	t.Parallel()
	rng := rand.NewChaCha8([32]byte{})
	ctx, _ := db.ConnectToTestPostgres(t)
	dbtx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	f := entfixtures.New(t, ctx, dbtx).WithRNG(rng)

	cfg := sparktesting.TestConfig(t)
	handler := NewInternalPrepareTokenHandler(cfg)
	network := btcnetwork.Regtest

	// Create legitimate token issuer
	legitimateIssuerPriv := keys.MustGeneratePrivateKeyFromRand(rng)
	legitimateIssuerPub := legitimateIssuerPriv.Public()

	// Create attacker keypair (different from legitimate issuer)
	attackerPriv := keys.MustGeneratePrivateKeyFromRand(rng)
	attackerPub := attackerPriv.Public()

	// Verify they're different
	require.False(t, legitimateIssuerPub.Equals(attackerPub), "Attacker key must be different from legitimate issuer")

	// Create token with legitimate issuer
	tokenIdentifier := f.RandomBytes(32)
	tokenCreate := dbtx.TokenCreate.Create().
		SetIssuerPublicKey(legitimateIssuerPub).
		SetTokenName("SecureToken").
		SetTokenTicker("SEC").
		SetDecimals(8).
		SetMaxSupply([]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x27, 0x10}). // 10000 tokens
		SetIsFreezable(false).
		SetNetwork(network).
		SetTokenIdentifier(tokenIdentifier).
		SetCreationEntityPublicKey(legitimateIssuerPub).
		SaveX(ctx)

	// Create keyshare for the attacker's mint attempt
	attackerSecretShare := keys.MustGeneratePrivateKeyFromRand(rng)
	ks := dbtx.SigningKeyshare.Create().
		SetSecretShare(attackerSecretShare).
		SetPublicKey(attackerPub).
		SetStatus(st.KeyshareStatusAvailable).
		SetPublicShares(map[string]keys.Public{}).
		SetMinSigners(1).
		SetCoordinatorIndex(1).
		SaveX(ctx)

	_ = dbtx.EntityDkgKey.Create().
		SetSigningKeyshare(ks).
		SaveX(ctx)

	// Construct unauthorized mint transaction with attacker's key
	now := time.Now()
	unauthorizedMintTx := &tokenpb.TokenTransaction{
		Version: 2,
		TokenInputs: &tokenpb.TokenTransaction_MintInput{
			MintInput: &tokenpb.TokenMintInput{
				IssuerPublicKey: attackerPub.Serialize(), // Attacker's key, not legitimate issuer
				TokenIdentifier: tokenCreate.TokenIdentifier,
			},
		},
		TokenOutputs: []*tokenpb.TokenOutput{
			{
				Id:                   proto.String(uuid.Must(uuid.NewV7()).String()),
				OwnerPublicKey:       attackerPub.Serialize(),
				TokenIdentifier:      tokenCreate.TokenIdentifier,
				TokenAmount:          []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 50}, // Mint 50 tokens
				RevocationCommitment: ks.PublicKey.Serialize(),
			},
		},
		ExpiryTime:             timestamppb.New(now.Add(24 * time.Hour)),
		ClientCreatedTimestamp: timestamppb.New(now),
	}

	pbNet, err := network.MarshalProto()
	require.NoError(t, err)
	unauthorizedMintTx.Network = pbNet

	for _, op := range cfg.GetSigningOperatorList() {
		unauthorizedMintTx.SparkOperatorIdentityPublicKeys = append(unauthorizedMintTx.SparkOperatorIdentityPublicKeys, op.PublicKey)
	}

	netCommon, err := btcnetwork.FromProtoNetwork(pbNet)
	require.NoError(t, err)
	cfgVals := cfg.Lrc20Configs[strings.ToLower(netCommon.String())]
	unauthorizedMintTx.TokenOutputs[0].WithdrawBondSats = &cfgVals.WithdrawBondSats
	unauthorizedMintTx.TokenOutputs[0].WithdrawRelativeBlockLocktime = &cfgVals.WithdrawRelativeBlockLocktime

	// Attacker signs the transaction with their own key
	partialHash, err := utils.HashTokenTransaction(unauthorizedMintTx, true)
	require.NoError(t, err)
	attackerSchnorrSig, err := schnorr.Sign(attackerPriv.ToBTCEC(), partialHash)
	require.NoError(t, err)
	attackerSig := attackerSchnorrSig.Serialize()

	operatorList := cfg.GetSigningOperatorList()
	var firstOperator *sparkpb.SigningOperatorInfo
	for _, operator := range operatorList {
		firstOperator = operator
		break
	}

	// Attempt unauthorized mint
	unauthorizedReq := &tokeninternalpb.PrepareTransactionRequest{
		FinalTokenTransaction:      unauthorizedMintTx,
		TokenTransactionSignatures: []*tokenpb.SignatureWithIndex{{InputIndex: 0, Signature: attackerSig}},
		KeyshareIds:                []string{ks.ID.String()},
		CoordinatorPublicKey:       firstOperator.PublicKey,
	}

	// Execute and verify rejection
	_, err = handler.PrepareTokenTransactionInternal(ctx, unauthorizedReq)

	// Verify the mint is rejected with the correct error
	require.Error(t, err, "Unauthorized mint should be rejected")
	require.ErrorContains(t, err, "issuer key mismatch", "Error should indicate issuer key mismatch")
	require.ErrorContains(t, err, "does not match token creator", "Error should explain the authorization failure")

	// Verify the error has the correct reason code
	_, reason := sparkerrors.CodeAndReasonFrom(err)
	require.Equal(t, sparkerrors.ReasonInvalidArgumentPublicKeyMismatch, reason,
		"Error should have PublicKeyMismatch reason")

	// Verify legitimate issuer can still mint
	legitimateMintTx := proto.Clone(unauthorizedMintTx).(*tokenpb.TokenTransaction)
	legitimateMintTx.GetMintInput().IssuerPublicKey = legitimateIssuerPub.Serialize()
	legitimateMintTx.TokenOutputs[0].OwnerPublicKey = legitimateIssuerPub.Serialize()
	legitimateMintTx.TokenOutputs[0].Id = proto.String(uuid.Must(uuid.NewV7()).String())

	// Create keyshare for legitimate issuer
	legitimateSecretShare := keys.MustGeneratePrivateKeyFromRand(rng)
	legitimateKs := dbtx.SigningKeyshare.Create().
		SetSecretShare(legitimateSecretShare).
		SetPublicKey(legitimateIssuerPub).
		SetStatus(st.KeyshareStatusAvailable).
		SetPublicShares(map[string]keys.Public{}).
		SetMinSigners(1).
		SetCoordinatorIndex(1).
		SaveX(ctx)

	legitimateMintTx.TokenOutputs[0].RevocationCommitment = legitimateKs.PublicKey.Serialize()

	legitimatePartialHash, err := utils.HashTokenTransaction(legitimateMintTx, true)
	require.NoError(t, err)
	legitimateSchnorrSig, err := schnorr.Sign(legitimateIssuerPriv.ToBTCEC(), legitimatePartialHash)
	require.NoError(t, err)
	legitimateSig := legitimateSchnorrSig.Serialize()

	legitimateReq := &tokeninternalpb.PrepareTransactionRequest{
		FinalTokenTransaction:      legitimateMintTx,
		TokenTransactionSignatures: []*tokenpb.SignatureWithIndex{{InputIndex: 0, Signature: legitimateSig}},
		KeyshareIds:                []string{legitimateKs.ID.String()},
		CoordinatorPublicKey:       firstOperator.PublicKey,
	}

	_, err = handler.PrepareTokenTransactionInternal(ctx, legitimateReq)
	require.NoError(t, err, "Legitimate issuer should be able to mint")
}
