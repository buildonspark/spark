package grpctest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	jwtkeys "github.com/lightsparkdev/spark/common/keys/jwt"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent/partner"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/transfer"
	"github.com/lightsparkdev/spark/so/ent/transferpartner"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"
)

func TestTransferWithPartnerAttribution_ES256(t *testing.T) {
	partnerKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	compressedKey := elliptic.MarshalCompressed(elliptic.P256(), partnerKey.PublicKey.X, partnerKey.PublicKey.Y)
	p256Key, err := keys.ParseP256PublicKey(compressedKey)
	require.NoError(t, err)
	jwtPubKey := jwtkeys.PublicFromP256(p256Key)

	testTransferWithPartnerJWT(t, jwtPubKey, func(partnerID, label string) string {
		return signJWT(t, "ES256", partnerID, label, func(digest []byte) []byte {
			r, s, err := ecdsa.Sign(rand.Reader, partnerKey, digest)
			require.NoError(t, err)
			sig := make([]byte, 64)
			r.FillBytes(sig[:32])
			s.FillBytes(sig[32:])
			return sig
		})
	})
}

func TestTransferWithPartnerAttribution_ES256K(t *testing.T) {
	partnerKey := keys.GeneratePrivateKey()

	jwtPubKey := jwtkeys.PublicFromSecp256k1(partnerKey.Public())

	ecKey := partnerKey.ToBTCEC().ToECDSA()
	testTransferWithPartnerJWT(t, jwtPubKey, func(partnerID, label string) string {
		return signJWT(t, "ES256K", partnerID, label, func(digest []byte) []byte {
			r, s, err := ecdsa.Sign(rand.Reader, ecKey, digest)
			require.NoError(t, err)
			sig := make([]byte, 64)
			r.FillBytes(sig[:32])
			s.FillBytes(sig[32:])
			return sig
		})
	})
}

func testTransferWithPartnerJWT(t *testing.T, jwtPubKey jwtkeys.Public, signToken func(partnerID, label string) string) {
	t.Helper()

	testPartnerID := "test-partner-" + uuid.New().String()[:8]
	testLabel := "client-1"

	// Set up sender wallet.
	senderConfig := wallet.NewTestWalletConfig(t)

	// Create the partner record on the coordinator database.
	coordSetupClient := db.NewPostgresEntClientForIntegrationTest(t, senderConfig.CoordinatorDatabaseURI)
	defer coordSetupClient.Close()
	_, err := coordSetupClient.Partner.Create().
		SetPartnerID(testPartnerID).
		SetLabel(testLabel).
		SetPartnerName("Integration Test Partner").
		SetJwtPublicKey(jwtPubKey).
		Save(t.Context())
	require.NoError(t, err, "failed to create partner on coordinator")

	token := signToken(testPartnerID, testLabel)

	// Create a tree for the transfer.
	leafPrivKey := keys.GeneratePrivateKey()
	rootNode, err := wallet.CreateNewTree(senderConfig, faucet, leafPrivKey, amountSatsToSend)
	require.NoError(t, err)

	newLeafPrivKey := keys.GeneratePrivateKey()
	receiverPrivKey := keys.GeneratePrivateKey()

	leavesToTransfer := []wallet.LeafKeyTweak{{
		Leaf:              rootNode,
		SigningPrivKey:    leafPrivKey,
		NewSigningPrivKey: newLeafPrivKey,
	}}

	// Inject the partner JWT header into the context.
	ctx := metadata.AppendToOutgoingContext(t.Context(), "x-partner-jwt", token)

	senderTransfer, err := wallet.SendTransferWithKeyTweaks(
		ctx,
		senderConfig,
		leavesToTransfer,
		receiverPrivKey.Public(),
		time.Now().Add(10*time.Minute),
	)
	require.NoError(t, err, "failed to initiate transfer")

	transferID, err := uuid.Parse(senderTransfer.Id)
	require.NoError(t, err)

	// Verify a transfer_partners record was created on the coordinator.
	coordClient := db.NewPostgresEntClientForIntegrationTest(t, senderConfig.CoordinatorDatabaseURI)
	defer coordClient.Close()

	tp, err := coordClient.TransferPartner.Query().
		Where(
			transferpartner.HasTransferWith(transfer.IDEQ(transferID)),
			transferpartner.HasPartnerWith(
				partner.PartnerID(testPartnerID),
				partner.LabelEQ(testLabel),
			),
		).
		Only(t.Context())
	require.NoError(t, err, "transfer_partners record not found on coordinator for transfer %s", transferID)
	require.Equal(t, st.TransferPartnerTypeTransfer, tp.Type)
}

func signJWT(t *testing.T, alg, partnerID, label string, signer func(digest []byte) []byte) string {
	t.Helper()

	header, err := json.Marshal(map[string]string{"alg": alg, "typ": "JWT"})
	require.NoError(t, err)
	claims, err := json.Marshal(map[string]any{
		"iss": partnerID,
		"sub": label,
		"aud": "spark-so",
		"iat": time.Now().Unix(),
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	require.NoError(t, err)

	signingInput := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signingInput))
	sig := signer(digest[:])

	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}
