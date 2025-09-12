package tokens

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	tokeninternalpb "github.com/lightsparkdev/spark/proto/spark_token_internal"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/utils"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// TestPrepareTokenTransactionInternal_NetworkValidation ensures we correctly validate network matching.
func TestPrepareTokenTransactionInternal_NetworkValidation(t *testing.T) {
	testCases := []struct {
		name        string
		tokenNet    st.Network
		txNet       st.Network
		expectError bool
	}{
		{
			name:        "mainnet token, regtest tx should fail",
			tokenNet:    st.NetworkMainnet,
			txNet:       st.NetworkRegtest,
			expectError: true,
		},
		{
			name:        "regtest token, mainnet tx should fail",
			tokenNet:    st.NetworkRegtest,
			txNet:       st.NetworkMainnet,
			expectError: true,
		},
		{
			name:        "mainnet token, mainnet tx should succeed",
			tokenNet:    st.NetworkMainnet,
			txNet:       st.NetworkMainnet,
			expectError: false,
		},
		{
			name:        "regtest token, regtest tx should succeed",
			tokenNet:    st.NetworkRegtest,
			txNet:       st.NetworkRegtest,
			expectError: false,
		},
	}

	cfg, err := sparktesting.TestConfig()
	require.NoError(t, err)

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, _ := db.ConnectToTestPostgres(t)
			dbtx, err := ent.GetDbFromContext(ctx)
			require.NoError(t, err)
			handler := NewInternalPrepareTokenHandler(cfg)

			// Arrange: create a TokenCreate on tokenNet
			issuerPriv, err := keys.GeneratePrivateKey()
			require.NoError(t, err)

			tokenCreate := dbtx.TokenCreate.Create().
				SetIssuerPublicKey(issuerPriv.Public().Serialize()).
				SetTokenName("TT").
				SetTokenTicker("TT").
				SetDecimals(8).
				SetMaxSupply([]byte{0}).
				SetIsFreezable(true).
				SetNetwork(tc.tokenNet).
				SetTokenIdentifier(make([]byte, 32)).
				SetCreationEntityPublicKey(issuerPriv.Public().Serialize()).
				SaveX(ctx)

			// Create an AVAILABLE signing keyshare to be reserved by prepare handler
			ks := dbtx.SigningKeyshare.Create().
				SetSecretShare(make([]byte, 32)).
				SetPublicKey(issuerPriv.Public()).
				SetStatus(st.KeyshareStatusAvailable).
				SetPublicShares(map[string]keys.Public{}).
				SetMinSigners(1).
				SetCoordinatorIndex(1).
				SaveX(ctx)

			// Seed the Entity DKG key referencing the signing keyshare
			_ = dbtx.EntityDkgKey.Create().
				SetSigningKeyshare(ks).
				SaveX(ctx)

			// Build a mint tx on txNet
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
						TokenAmount:          []byte{0, 0, 0, 0, 0, 0, 0, 10},
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
			netCommon, err := common.NetworkFromProtoNetwork(pbNet)
			require.NoError(t, err)
			cfgVals := handler.config.Lrc20Configs[netCommon.String()]
			txProto.TokenOutputs[0].WithdrawBondSats = &cfgVals.WithdrawBondSats
			txProto.TokenOutputs[0].WithdrawRelativeBlockLocktime = &cfgVals.WithdrawRelativeBlockLocktime

			partialHash, err := utils.HashTokenTransaction(txProto, true)
			require.NoError(t, err)
			schnorrSig, err := schnorr.Sign(issuerPriv.ToBTCEC(), partialHash)
			require.NoError(t, err)
			sig := schnorrSig.Serialize()

			// Use the first operator as coordinator
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
				require.ErrorContains(t, err, fmt.Sprintf("transaction network %s does not match token network %s", strings.ToLower(string(tc.txNet)), strings.ToLower(string(tc.tokenNet))))
			} else {
				require.NoError(t, err)
			}
		})
	}
}
