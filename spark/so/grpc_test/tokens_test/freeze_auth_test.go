package tokens_test

import (
	"testing"

	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/require"
)

func TestFreezeTokensByNonIssuerFails(t *testing.T) {
	runSignatureTypeTestCases(t, func(t *testing.T, tc signatureTypeTestCase) {
		issuerPrivKey := keys.GeneratePrivateKey()
		issuerConfig := wallet.NewTestWalletConfigWithIdentityKey(t, issuerPrivKey)
		issuerConfig.UseTokenTransactionSchnorrSignatures = tc.useSchnorrSignatures

		err := testCreateNativeSparkTokenWithParams(t, issuerConfig, sparkTokenCreationTestParams{
			issuerPrivateKey: issuerPrivKey,
			name:             "Freeze Auth Token",
			ticker:           "FAT",
			maxSupply:        0,
		})
		require.NoError(t, err, "failed to create native spark token")

		tokenIdentifier := queryTokenIdentifierOrFail(t, issuerConfig, issuerPrivKey.Public())

		mintTx, outputOwners, err := createTestTokenMintTransactionTokenPbWithParams(t, issuerConfig, tokenTransactionParams{
			TokenIdentityPubKey: issuerPrivKey.Public(),
			TokenIdentifier:     tokenIdentifier,
			NumOutputs:          1,
			OutputAmounts:       []uint64{uint64(testIssueOutput1Amount)},
		})
		require.NoError(t, err, "failed to create mint transaction")
		require.Len(t, outputOwners, 1)

		_, err = broadcastTokenTransaction(t, t.Context(), issuerConfig, mintTx, []keys.Private{issuerPrivKey})
		require.NoError(t, err, "failed to broadcast mint transaction")

		ownerPubKey := outputOwners[0].Public()

		// Attempt to freeze using a non-issuer identity key — should be rejected
		nonIssuerPrivKey := keys.GeneratePrivateKey()
		nonIssuerConfig := wallet.NewTestWalletConfigWithIdentityKey(t, nonIssuerPrivKey)
		nonIssuerConfig.UseTokenTransactionSchnorrSignatures = tc.useSchnorrSignatures

		_, err = wallet.FreezeTokens(t.Context(), nonIssuerConfig, ownerPubKey, tokenIdentifier, false)
		require.Error(t, err, "expected freeze by non-issuer to be rejected")
		require.Contains(t, err.Error(), "does not match", "error should indicate identity key mismatch")
	})
}
