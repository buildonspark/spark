package partner

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	jwtkeys "github.com/lightsparkdev/spark/common/keys/jwt"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests verify SaveTransferPartner's write path behavior using a real
// in-memory SQLite database: records are created when partner context exists
// and skipped when absent.

func getDB(t *testing.T, ctx context.Context) *ent.Client {
	t.Helper()
	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	return dbClient
}

func TestSaveTransferPartner_NoPartnerInContext(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = knobs.InjectKnobsService(ctx, knobs.New(knobs.NewStaticValuesProvider(map[string]float64{
		knobs.KnobEnablePartnerJWT: 100,
	})))
	dbClient := getDB(t, ctx)

	transferID := createTestTransfer(t, ctx, dbClient)
	SaveTransferPartner(ctx, transferID, schematype.TransferPartnerTypeTransfer)

	count, err := dbClient.TransferPartner.Query().Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

func TestSaveTransferPartner_WithPartnerInContext(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = knobs.InjectKnobsService(ctx, knobs.New(knobs.NewStaticValuesProvider(map[string]float64{
		knobs.KnobEnablePartnerJWT: 100,
	})))
	dbClient := getDB(t, ctx)

	p := createTestPartner(t, ctx, dbClient, "partner-a", "client-1")
	transferID := createTestTransfer(t, ctx, dbClient)

	ctx = context.WithValue(ctx, partnerContextKey, &PartnerInfo{
		PartnerDBID: p.ID,
		PartnerID:   "partner-a",
		Label:       "client-1",
	})

	SaveTransferPartner(ctx, transferID, schematype.TransferPartnerTypeTransfer)

	count, err := dbClient.TransferPartner.Query().Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

// Iss-only PartnerInfo (Label="") should not create a transfer_partner record.
func TestSaveTransferPartner_IssOnlyPartner_Skips(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	ctx = knobs.InjectKnobsService(ctx, knobs.New(knobs.NewStaticValuesProvider(map[string]float64{
		knobs.KnobEnablePartnerJWT: 100,
	})))
	dbClient := getDB(t, ctx)

	transferID := createTestTransfer(t, ctx, dbClient)

	ctx = context.WithValue(ctx, partnerContextKey, &PartnerInfo{
		PartnerID: "partner-a",
		Label:     "",
	})

	SaveTransferPartner(ctx, transferID, schematype.TransferPartnerTypeTransfer)

	count, err := dbClient.TransferPartner.Query().Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

func createTestPartner(t *testing.T, ctx context.Context, client *ent.Client, partnerID, label string) *ent.Partner {
	t.Helper()
	p, err := client.Partner.Create().
		SetPartnerID(partnerID).
		SetLabel(label).
		SetPartnerName("Test Partner").
		SetJwtPublicKey(jwtkeys.MustParsePublicHex("0102112b5bc18676433c593f8b02127354b9db8de6070088c1646a3cd58a60b90be3")).
		Save(ctx)
	require.NoError(t, err)
	return p
}

func createTestTransfer(t *testing.T, ctx context.Context, client *ent.Client) uuid.UUID {
	t.Helper()
	senderKey := keys.GeneratePrivateKey().Public()
	receiverKey := keys.GeneratePrivateKey().Public()
	transfer, err := client.Transfer.Create().
		SetSenderIdentityPubkey(senderKey).
		SetReceiverIdentityPubkey(receiverKey).
		SetStatus(schematype.TransferStatusSenderInitiated).
		SetTotalValue(0).
		SetExpiryTime(time.Now().Add(24 * time.Hour)).
		SetNetwork(btcnetwork.Regtest).
		SetType(schematype.TransferTypeTransfer).
		Save(ctx)
	require.NoError(t, err)
	return transfer.ID
}
