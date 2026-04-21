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
	"github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent/partner"
	"github.com/lightsparkdev/spark/so/ent/preimageshare"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	enttransfer "github.com/lightsparkdev/spark/so/ent/transfer"
	"github.com/lightsparkdev/spark/so/ent/transferpartner"
	"github.com/lightsparkdev/spark/so/knobs"
	sparktesting "github.com/lightsparkdev/spark/testing"
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
	pk, err := coordSetupClient.PartnerKey.Create().
		SetPartnerID(testPartnerID).
		SetPartnerName("Integration Test Partner").
		SetJwtPublicKey(jwtPubKey).
		Save(t.Context())
	require.NoError(t, err, "failed to create partner key on coordinator")
	_, err = coordSetupClient.Partner.Create().
		SetPartnerID(testPartnerID).
		SetLabel(testLabel).
		SetPartnerName("Integration Test Partner").
		SetJwtPublicKey(jwtPubKey).
		SetPartnerKeyID(pk.ID).
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
			transferpartner.HasTransferWith(enttransfer.IDEQ(transferID)),
			transferpartner.HasPartnerWith(
				partner.PartnerID(testPartnerID),
				partner.LabelEQ(testLabel),
			),
		).
		Only(t.Context())
	require.NoError(t, err, "transfer_partners record not found on coordinator for transfer %s", transferID)
	require.Equal(t, st.TransferPartnerTypeTransfer, tp.Type)
}

func TestHodlReceiveWithPartnerAttribution_ES256(t *testing.T) {
	partnerKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	compressedKey := elliptic.MarshalCompressed(elliptic.P256(), partnerKey.PublicKey.X, partnerKey.PublicKey.Y)
	p256Key, err := keys.ParseP256PublicKey(compressedKey)
	require.NoError(t, err)
	jwtPubKey := jwtkeys.PublicFromP256(p256Key)

	testHodlReceiveWithPartnerJWT(t, jwtPubKey, func(partnerID, label string) string {
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

func TestHodlReceiveWithPartnerAttribution_ES256K(t *testing.T) {
	partnerKey := keys.GeneratePrivateKey()
	jwtPubKey := jwtkeys.PublicFromSecp256k1(partnerKey.Public())

	ecKey := partnerKey.ToBTCEC().ToECDSA()
	testHodlReceiveWithPartnerJWT(t, jwtPubKey, func(partnerID, label string) string {
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

func testHodlReceiveWithPartnerJWT(t *testing.T, jwtPubKey jwtkeys.Public, signToken func(partnerID, label string) string) {
	t.Helper()

	testPartnerID := "test-partner-" + uuid.New().String()[:8]
	testLabel := "client-1"

	// User config (receiver who will ProvidePreimage) and SSP config (sender).
	userConfig := wallet.NewTestWalletConfig(t)
	sspConfig := wallet.NewTestWalletConfig(t)

	// Create the partner record on the coordinator database.
	coordSetupClient := db.NewPostgresEntClientForIntegrationTest(t, userConfig.CoordinatorDatabaseURI)
	defer coordSetupClient.Close()
	pk, err := coordSetupClient.PartnerKey.Create().
		SetPartnerID(testPartnerID).
		SetPartnerName("Integration Test Partner").
		SetJwtPublicKey(jwtPubKey).
		Save(t.Context())
	require.NoError(t, err, "failed to create partner key on coordinator")
	_, err = coordSetupClient.Partner.Create().
		SetPartnerID(testPartnerID).
		SetLabel(testLabel).
		SetPartnerName("Integration Test Partner").
		SetJwtPublicKey(jwtPubKey).
		SetPartnerKeyID(pk.ID).
		Save(t.Context())
	require.NoError(t, err, "failed to create partner on coordinator")

	amountSats := uint64(100)
	preimage, paymentHash := testPreimageHash(t, amountSats)
	invoice := testInvoice

	// SSP creates a tree and initiates the preimage swap.
	userLeafPrivKey := keys.GeneratePrivateKey()
	feeSats := uint64(2)
	nodeToSend, err := wallet.CreateNewTree(sspConfig, faucet, userLeafPrivKey, 12347)
	require.NoError(t, err)

	newLeafPrivKey := keys.GeneratePrivateKey()
	leaves := []wallet.LeafKeyTweak{{
		Leaf:              nodeToSend,
		SigningPrivKey:    userLeafPrivKey,
		NewSigningPrivKey: newLeafPrivKey,
	}}

	response, err := wallet.SwapNodesForPreimage(
		t.Context(),
		sspConfig,
		leaves,
		userConfig.IdentityPublicKey(),
		paymentHash[:],
		&invoice,
		feeSats,
		false,
		amountSats,
	)
	require.NoError(t, err)

	transfer, err := wallet.DeliverTransferPackage(t.Context(), sspConfig, response.Transfer, leaves, nil)
	require.NoError(t, err)

	// User provides preimage WITH partner JWT header.
	token := signToken(testPartnerID, testLabel)
	ctx := metadata.AppendToOutgoingContext(t.Context(), "x-partner-jwt", token)
	receiverTransfer, err := wallet.ProvidePreimage(ctx, userConfig, preimage[:])
	require.NoError(t, err)
	require.Equal(t, transfer.Id, receiverTransfer.Id)

	transferID, err := uuid.Parse(receiverTransfer.Id)
	require.NoError(t, err)

	// Verify a transfer_partners record was created on the coordinator.
	coordClient := db.NewPostgresEntClientForIntegrationTest(t, userConfig.CoordinatorDatabaseURI)
	defer coordClient.Close()

	tp, err := coordClient.TransferPartner.Query().
		Where(
			transferpartner.HasTransferWith(enttransfer.IDEQ(transferID)),
			transferpartner.HasPartnerWith(
				partner.PartnerID(testPartnerID),
				partner.LabelEQ(testLabel),
			),
		).
		Only(t.Context())
	require.NoError(t, err, "transfer_partners record not found on coordinator for transfer %s", transferID)
	require.Equal(t, st.TransferPartnerTypeLightningReceive, tp.Type)
}

func TestLightningSendWithPartnerAttribution_ES256(t *testing.T) {
	partnerKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	compressedKey := elliptic.MarshalCompressed(elliptic.P256(), partnerKey.PublicKey.X, partnerKey.PublicKey.Y)
	p256Key, err := keys.ParseP256PublicKey(compressedKey)
	require.NoError(t, err)
	jwtPubKey := jwtkeys.PublicFromP256(p256Key)

	testLightningSendWithPartnerJWT(t, jwtPubKey, func(partnerID, label string) string {
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

func testLightningSendWithPartnerJWT(t *testing.T, jwtPubKey jwtkeys.Public, signToken func(partnerID, label string) string) {
	t.Helper()

	testPartnerID := "test-partner-" + uuid.New().String()[:8]
	testLabel := "client-1"

	// User (sender) and receiver configs.
	userConfig := wallet.NewTestWalletConfig(t)
	receiverConfig := wallet.NewTestWalletConfig(t)

	// Create the partner record on the coordinator database.
	coordSetupClient := db.NewPostgresEntClientForIntegrationTest(t, userConfig.CoordinatorDatabaseURI)
	defer coordSetupClient.Close()
	pk, err := coordSetupClient.PartnerKey.Create().
		SetPartnerID(testPartnerID).
		SetPartnerName("Integration Test Partner").
		SetJwtPublicKey(jwtPubKey).
		Save(t.Context())
	require.NoError(t, err, "failed to create partner key on coordinator")
	_, err = coordSetupClient.Partner.Create().
		SetPartnerID(testPartnerID).
		SetLabel(testLabel).
		SetPartnerName("Integration Test Partner").
		SetJwtPublicKey(jwtPubKey).
		SetPartnerKeyID(pk.ID).
		Save(t.Context())
	require.NoError(t, err, "failed to create partner on coordinator")

	amountSats := uint64(100)
	_, paymentHash := testPreimageHash(t, amountSats)
	invoice := testInvoice

	// User creates a tree to send from.
	userLeafPrivKey := keys.GeneratePrivateKey()
	feeSats := uint64(2)
	nodeToSend, err := wallet.CreateNewTree(userConfig, faucet, userLeafPrivKey, 12347)
	require.NoError(t, err)

	newLeafPrivKey := keys.GeneratePrivateKey()
	leaves := []wallet.LeafKeyTweak{{
		Leaf:              nodeToSend,
		SigningPrivKey:    userLeafPrivKey,
		NewSigningPrivKey: newLeafPrivKey,
	}}

	// User calls SwapNodesForPreimage with REASON_SEND and partner JWT.
	token := signToken(testPartnerID, testLabel)
	ctx := metadata.AppendToOutgoingContext(t.Context(), "x-partner-jwt", token)

	response, err := wallet.SwapNodesForPreimage(
		ctx,
		userConfig,
		leaves,
		receiverConfig.IdentityPublicKey(),
		paymentHash[:],
		&invoice,
		feeSats,
		false, // REASON_SEND
		amountSats,
	)
	require.NoError(t, err)
	require.NotNil(t, response.Transfer)

	transferID, err := uuid.Parse(response.Transfer.Id)
	require.NoError(t, err)

	// Verify a transfer_partners record was created on the coordinator.
	coordClient := db.NewPostgresEntClientForIntegrationTest(t, userConfig.CoordinatorDatabaseURI)
	defer coordClient.Close()

	tp, err := coordClient.TransferPartner.Query().
		Where(
			transferpartner.HasTransferWith(enttransfer.IDEQ(transferID)),
			transferpartner.HasPartnerWith(
				partner.PartnerID(testPartnerID),
				partner.LabelEQ(testLabel),
			),
		).
		Only(t.Context())
	require.NoError(t, err, "transfer_partners record not found on coordinator for transfer %s", transferID)
	require.Equal(t, st.TransferPartnerTypeLightningSend, tp.Type)
}

// TestNonHodlReceiveWithPartnerAttribution verifies that partner info stored
// with StorePreimageShareV2 propagates to transfer_partner during
// InitiatePreimageSwapV2 (non-hodl receive).
func TestNonHodlReceiveWithPartnerAttribution(t *testing.T) {
	partnerKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	compressedKey := elliptic.MarshalCompressed(elliptic.P256(), partnerKey.PublicKey.X, partnerKey.PublicKey.Y)
	p256Key, err := keys.ParseP256PublicKey(compressedKey)
	require.NoError(t, err)
	jwtPubKey := jwtkeys.PublicFromP256(p256Key)

	testPartnerID := "test-partner-" + uuid.New().String()[:8]
	testLabel := "client-1"

	userConfig := wallet.NewTestWalletConfig(t)
	sspConfig := wallet.NewTestWalletConfig(t)

	// Enable partner JWT knob.
	kc, err := sparktesting.NewKnobController(t)
	require.NoError(t, err)
	err = kc.SetKnob(t, knobs.KnobEnablePartnerJWT, 100)
	require.NoError(t, err)

	// Create partner on coordinator.
	coordSetupClient := db.NewPostgresEntClientForIntegrationTest(t, userConfig.CoordinatorDatabaseURI)
	defer coordSetupClient.Close()
	pk, err := coordSetupClient.PartnerKey.Create().
		SetPartnerID(testPartnerID).
		SetPartnerName("Integration Test Partner").
		SetJwtPublicKey(jwtPubKey).
		Save(t.Context())
	require.NoError(t, err)
	_, err = coordSetupClient.Partner.Create().
		SetPartnerID(testPartnerID).
		SetLabel(testLabel).
		SetPartnerName("Integration Test Partner").
		SetJwtPublicKey(jwtPubKey).
		SetPartnerKeyID(pk.ID).
		Save(t.Context())
	require.NoError(t, err)

	amountSats := uint64(100)
	preimage, paymentHash := testPreimageHash(t, amountSats)
	invoice := testInvoice

	// Clean stale preimage data from prior runs (hardcoded preimage is shared).
	coordSetupClient.PreimageSharePartner.Delete().Exec(t.Context())                                           //nolint:errcheck // best-effort cleanup
	coordSetupClient.PreimageShare.Delete().Where(preimageshare.PaymentHash(paymentHash[:])).Exec(t.Context()) //nolint:errcheck // best-effort cleanup
	cleanUpPreimageShareOnNonCoordinators(t, userConfig, paymentHash)

	// Store preimage shares on all SOs with partner JWT in context.
	// The v1 StorePreimageShare handler saves preimage_share_partner when the
	// partner JWT knob is enabled and the JWT is in context.
	partnerJWT := signJWT(t, "ES256", testPartnerID, testLabel, func(digest []byte) []byte {
		r, s, err := ecdsa.Sign(rand.Reader, partnerKey, digest)
		require.NoError(t, err)
		sig := make([]byte, 64)
		r.FillBytes(sig[:32])
		s.FillBytes(sig[32:])
		return sig
	})
	jwtCtx := metadata.AppendToOutgoingContext(t.Context(), "x-partner-jwt", partnerJWT)

	fakeInvoiceCreator := NewFakeLightningInvoiceCreator()
	_, err = wallet.CreateLightningInvoiceWithPreimage(
		jwtCtx, userConfig, fakeInvoiceCreator, amountSats, "test", preimage,
	)
	require.NoError(t, err)

	// Clean up after this test: delete partner FK on coordinator, then use the
	// shared cleanUp which handles all SOs via mock RPC.
	// Use defer instead of t.Cleanup because cleanUp makes gRPC calls with
	// t.Context(), which is canceled before t.Cleanup runs (Go 1.24+).
	defer func() {
		coordSetupClient.PreimageSharePartner.Delete().Exec(t.Context()) //nolint:errcheck // best-effort cleanup
		cleanUp(t, userConfig, paymentHash)
	}()

	// Initiate preimage swap (non-hodl receive).
	sspLeafPrivKey := keys.GeneratePrivateKey()
	nodeToSend, err := wallet.CreateNewTree(sspConfig, faucet, sspLeafPrivKey, 12345)
	require.NoError(t, err)

	newLeafPrivKey := keys.GeneratePrivateKey()
	leaves := []wallet.LeafKeyTweak{{
		Leaf:              nodeToSend,
		SigningPrivKey:    sspLeafPrivKey,
		NewSigningPrivKey: newLeafPrivKey,
	}}

	conn, err := sspConfig.NewCoordinatorGRPCConnection()
	require.NoError(t, err)
	defer conn.Close()

	token, err := wallet.AuthenticateWithConnection(t.Context(), sspConfig, conn)
	require.NoError(t, err)
	ctx := wallet.ContextWithToken(t.Context(), token)
	client := spark.NewSparkServiceClient(conn)

	transferID, err := uuid.NewV7()
	require.NoError(t, err)

	keyTweakInputMap, err := wallet.PrepareSendTransferKeyTweaks(
		sspConfig, transferID, userConfig.IdentityPublicKey(), leaves, map[string][]byte{},
	)
	require.NoError(t, err)

	transferPackage, err := wallet.PrepareTransferPackage(
		ctx, sspConfig, client, transferID, keyTweakInputMap, leaves,
		userConfig.IdentityPublicKey(), keys.Public{},
	)
	require.NoError(t, err)

	userSignedLeavesToSend, err := wallet.PrepareUserSignedLeafSigningJobs(
		ctx, sspConfig, client, leaves, userConfig.IdentityPublicKey(), keys.Public{},
	)
	require.NoError(t, err)

	response, err := client.InitiatePreimageSwapV2(ctx, &spark.InitiatePreimageSwapRequest{
		PaymentHash: paymentHash[:],
		Reason:      spark.InitiatePreimageSwapRequest_REASON_RECEIVE,
		InvoiceAmount: &spark.InvoiceAmount{
			InvoiceAmountProof: &spark.InvoiceAmountProof{Bolt11Invoice: invoice},
			ValueSats:          amountSats,
		},
		Transfer: &spark.StartUserSignedTransferRequest{
			TransferId:                transferID.String(),
			OwnerIdentityPublicKey:    sspConfig.IdentityPublicKey().Serialize(),
			ReceiverIdentityPublicKey: userConfig.IdentityPublicKey().Serialize(),
			LeavesToSend:              userSignedLeavesToSend,
		},
		TransferRequest: &spark.StartTransferRequest{
			TransferId:                transferID.String(),
			OwnerIdentityPublicKey:    sspConfig.IdentityPublicKey().Serialize(),
			ReceiverIdentityPublicKey: userConfig.IdentityPublicKey().Serialize(),
			TransferPackage:           transferPackage,
		},
		ReceiverIdentityPublicKey: userConfig.IdentityPublicKey().Serialize(),
		FeeSats:                   0,
	})
	require.NoError(t, err)
	require.NotNil(t, response)
	require.NotEmpty(t, response.Preimage)

	// Verify transfer_partner on coordinator.
	coordClient := db.NewPostgresEntClientForIntegrationTest(t, userConfig.CoordinatorDatabaseURI)
	defer coordClient.Close()

	tp, err := coordClient.TransferPartner.Query().
		Where(
			transferpartner.HasTransferWith(enttransfer.IDEQ(transferID)),
			transferpartner.HasPartnerWith(
				partner.PartnerID(testPartnerID),
				partner.LabelEQ(testLabel),
			),
		).
		Only(t.Context())
	require.NoError(t, err, "transfer_partners record not found on coordinator for transfer %s", transferID)
	require.Equal(t, st.TransferPartnerTypeLightningReceive, tp.Type)
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

// cleanUpPreimageShareOnNonCoordinators deletes preimage shares from non-coordinator
// SOs via direct DB. Errors are ignored since the shares may not exist.
func cleanUpPreimageShareOnNonCoordinators(t *testing.T, config *wallet.TestWalletConfig, paymentHash [32]byte) {
	t.Helper()
	numOperators := len(sparktesting.GetAllSigningOperators(t))
	for i := range numOperators {
		dbURI := sparktesting.GetTestDatabasePath(i)
		if dbURI == config.CoordinatorDatabaseURI {
			continue
		}
		entClient := db.NewPostgresEntClientForIntegrationTest(t, dbURI)
		entClient.PreimageShare.Delete().Where(preimageshare.PaymentHash(paymentHash[:])).Exec(t.Context()) //nolint:errcheck // best-effort cleanup
		entClient.Close()
	}
}
