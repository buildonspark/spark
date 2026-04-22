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
	"github.com/lightsparkdev/spark/so/ent/depositaddresspartner"
	"github.com/lightsparkdev/spark/so/ent/partner"
	"github.com/lightsparkdev/spark/so/ent/partnerkey"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"
)

func TestRegularDepositAddressPartnerAttribution(t *testing.T) {
	partnerKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	compressedKey := elliptic.MarshalCompressed(elliptic.P256(), partnerKey.PublicKey.X, partnerKey.PublicKey.Y)
	p256Key, err := keys.ParseP256PublicKey(compressedKey)
	require.NoError(t, err)
	jwtPubKey := jwtkeys.PublicFromP256(p256Key)

	testPartnerID := "test-partner-" + uuid.New().String()[:8]
	testLabel := "client-1"

	config := wallet.NewTestWalletConfig(t)

	// Create partner key and partner on coordinator.
	coordSetupClient := db.NewPostgresEntClientForIntegrationTest(t, config.CoordinatorDatabaseURI)
	defer coordSetupClient.Close()
	pk, err := coordSetupClient.PartnerKey.Create().
		SetPartnerID(testPartnerID).
		SetPartnerName("Integration Test Partner").
		SetJwtPublicKey(jwtPubKey).
		Save(t.Context())
	require.NoError(t, err)
	_, err = coordSetupClient.Partner.Create().
		SetLabel(testLabel).
		SetPartnerKeyID(pk.ID).
		Save(t.Context())
	require.NoError(t, err)

	header, err := json.Marshal(map[string]string{"alg": "ES256", "typ": "JWT"})
	require.NoError(t, err)
	claims, err := json.Marshal(map[string]any{
		"iss": testPartnerID,
		"sub": testLabel,
		"aud": "spark-so",
		"iat": time.Now().Unix(),
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	require.NoError(t, err)
	signingInput := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, partnerKey, digest[:])
	require.NoError(t, err)
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	token := signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)

	// Authenticate and generate regular (non-static) deposit address with partner JWT.
	authToken, err := wallet.AuthenticateWithServer(t.Context(), config)
	require.NoError(t, err)
	ctx := wallet.ContextWithToken(t.Context(), authToken)
	ctx = metadata.AppendToOutgoingContext(ctx, "x-partner-jwt", token)

	signingPubKey := keys.GeneratePrivateKey().Public()
	resp, err := wallet.GenerateDepositAddress(ctx, config, signingPubKey, nil, false)
	require.NoError(t, err)
	require.False(t, resp.DepositAddress.IsStatic)

	// Verify deposit_address_partners record on coordinator.
	coordClient := db.NewPostgresEntClientForIntegrationTest(t, config.CoordinatorDatabaseURI)
	defer coordClient.Close()

	dap, err := coordClient.DepositAddressPartner.Query().
		Where(
			depositaddresspartner.HasPartnerWith(
				partner.HasPartnerKeyWith(partnerkey.PartnerIDEQ(testPartnerID)),
				partner.LabelEQ(testLabel),
			),
		).
		Only(t.Context())
	require.NoError(t, err, "deposit_address_partners record not found on coordinator")

	depositAddr, err := dap.QueryDepositAddress().Only(t.Context())
	require.NoError(t, err)
	require.Equal(t, resp.DepositAddress.Address, depositAddr.Address)
}
