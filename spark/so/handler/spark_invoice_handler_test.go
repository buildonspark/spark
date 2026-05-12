package handler

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/db"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/require"
)

// createTestSatsInvoice inserts a sats-denominated SparkInvoice into the test DB
// and returns the encoded invoice string together with its UUID.
func createTestSatsInvoice(t *testing.T, ctx context.Context, tc *db.TestContext) (string, uuid.UUID) {
	t.Helper()
	invoiceID := uuid.New()
	receiverKey := keys.GeneratePrivateKey().Public()
	senderKey := keys.GeneratePrivateKey().Public()
	network := btcnetwork.Regtest
	expiryTime := time.Now().Add(10 * time.Minute)
	amountSats := uint64(1_000)

	invoiceFields := common.CreateSatsSparkInvoiceFields(
		invoiceID[:],
		&amountSats,
		nil,
		senderKey,
		&expiryTime,
	)
	invoiceStr, err := common.EncodeSparkAddress(receiverKey, network, invoiceFields)
	require.NoError(t, err)

	_, err = tc.Client.SparkInvoice.Create().
		SetID(invoiceID).
		SetSparkInvoice(invoiceStr).
		SetExpiryTime(expiryTime).
		SetReceiverPublicKey(receiverKey).
		Save(ctx)
	require.NoError(t, err)

	return invoiceStr, invoiceID
}

// createTransferForInvoice inserts a Transfer with the given status linked to
// an existing SparkInvoice.
func createTransferForInvoice(t *testing.T, ctx context.Context, tc *db.TestContext, invoiceID uuid.UUID, status st.TransferStatus) {
	t.Helper()
	senderKey := keys.GeneratePrivateKey().Public()
	receiverKey := keys.GeneratePrivateKey().Public()
	_, err := tc.Client.Transfer.Create().
		SetID(uuid.New()).
		SetSenderIdentityPubkey(senderKey).
		SetReceiverIdentityPubkey(receiverKey).
		SetStatus(status).
		SetType(st.TransferTypeTransfer).
		SetNetwork(btcnetwork.Regtest).
		SetTotalValue(1000).
		SetExpiryTime(time.Now().Add(10 * time.Minute)).
		SetSparkInvoiceID(invoiceID).
		Save(ctx)
	require.NoError(t, err)
}

func createTestTokenInvoice(
	t *testing.T,
	ctx context.Context,
	tc *db.TestContext,
	status st.TokenTransactionStatus,
	expiryTime time.Time,
) (string, []byte) {
	t.Helper()
	invoiceID := uuid.New()
	receiverKey := keys.GeneratePrivateKey().Public()
	senderKey := keys.GeneratePrivateKey().Public()
	network := btcnetwork.Regtest
	tokenIdentifier := make([]byte, 32)
	tokenIdentifier[31] = 1
	amount := []byte{0x03, 0xe8}

	invoiceFields := common.CreateTokenSparkInvoiceFields(
		invoiceID[:],
		tokenIdentifier,
		amount,
		nil,
		senderKey,
		&expiryTime,
	)
	invoiceStr, err := common.EncodeSparkAddress(receiverKey, network, invoiceFields)
	require.NoError(t, err)

	_, err = tc.Client.SparkInvoice.Create().
		SetID(invoiceID).
		SetSparkInvoice(invoiceStr).
		SetExpiryTime(expiryTime).
		SetReceiverPublicKey(receiverKey).
		Save(ctx)
	require.NoError(t, err)

	partialHash := repeatedUUIDBytes(t)
	finalHash := repeatedUUIDBytes(t)
	_, err = tc.Client.TokenTransaction.Create().
		SetPartialTokenTransactionHash(partialHash).
		SetFinalizedTokenTransactionHash(finalHash).
		SetStatus(status).
		SetExpiryTime(expiryTime).
		AddSparkInvoiceIDs(invoiceID).
		Save(ctx)
	require.NoError(t, err)

	return invoiceStr, finalHash
}

func repeatedUUIDBytes(t *testing.T) []byte {
	t.Helper()
	id := uuid.New()
	out := make([]byte, 0, 32)
	out = append(out, id[:]...)
	out = append(out, id[:]...)
	return out
}

// TestQuerySparkInvoicesReturnsPendingStatus verifies that an invoice attached to
// a transfer in SenderInitiatedCoordinator state is reported as PENDING.
//
// This is the status assigned immediately when StartTransferV2 is received before
// the key-tweak processing begins.  It was previously untested: a regression that
// returned NOT_FOUND here would cause clients to believe in-flight payments had
// never been attempted.
func TestQuerySparkInvoicesReturnsPendingStatus(t *testing.T) {
	config := sparktesting.TestConfig(t)
	ctx, tc := db.ConnectToTestPostgres(t)

	invoiceStr, invoiceID := createTestSatsInvoice(t, ctx, tc)
	createTransferForInvoice(t, ctx, tc, invoiceID, st.TransferStatusSenderInitiatedCoordinator)

	handler := NewSparkInvoiceHandler(config)
	resp, err := handler.QuerySparkInvoices(ctx, &sparkpb.QuerySparkInvoicesRequest{
		Invoice: []string{invoiceStr},
	})
	require.NoError(t, err)
	require.Len(t, resp.InvoiceStatuses, 1)
	require.Equal(t, sparkpb.InvoiceStatus_PENDING, resp.InvoiceStatuses[0].Status,
		"expected PENDING for SenderInitiatedCoordinator transfer, got %s",
		resp.InvoiceStatuses[0].Status)
}

func TestQuerySparkInvoicesRejectsNilRequest(t *testing.T) {
	config := sparktesting.TestConfig(t)
	handler := NewSparkInvoiceHandler(config)

	_, err := handler.QuerySparkInvoices(t.Context(), nil)
	require.Error(t, err)
	require.ErrorContains(t, err, "request is required")
}

func TestQuerySparkInvoicesReturnsNotFoundForStoredInvoiceWithoutPaymentEdge(t *testing.T) {
	config := sparktesting.TestConfig(t)
	ctx, tc := db.ConnectToTestPostgres(t)

	invoiceStr, _ := createTestSatsInvoice(t, ctx, tc)

	handler := NewSparkInvoiceHandler(config)
	resp, err := handler.QuerySparkInvoices(ctx, &sparkpb.QuerySparkInvoicesRequest{
		Invoice: []string{invoiceStr},
	})
	require.NoError(t, err)
	require.Len(t, resp.InvoiceStatuses, 1)
	require.Equal(t, invoiceStr, resp.InvoiceStatuses[0].Invoice)
	require.Equal(t, sparkpb.InvoiceStatus_NOT_FOUND, resp.InvoiceStatuses[0].Status)
	require.Nil(t, resp.InvoiceStatuses[0].GetTransferType())
}

func TestQuerySparkInvoicesReturnsTokenInvoiceStatuses(t *testing.T) {
	config := sparktesting.TestConfig(t)
	ctx, tc := db.ConnectToTestPostgres(t)

	pendingStr, pendingFinalHash := createTestTokenInvoice(t, ctx, tc, st.TokenTransactionStatusStarted, time.Now().Add(10*time.Minute))
	returnedStr, returnedFinalHash := createTestTokenInvoice(t, ctx, tc, st.TokenTransactionStatusSignedCancelled, time.Now().Add(10*time.Minute))

	handler := NewSparkInvoiceHandler(config)
	resp, err := handler.QuerySparkInvoices(ctx, &sparkpb.QuerySparkInvoicesRequest{
		Invoice: []string{returnedStr, pendingStr},
	})
	require.NoError(t, err)
	require.Len(t, resp.InvoiceStatuses, 2)

	byInvoice := make(map[string]*sparkpb.InvoiceResponse, 2)
	for _, invoiceStatus := range resp.InvoiceStatuses {
		byInvoice[invoiceStatus.Invoice] = invoiceStatus
	}

	require.Contains(t, byInvoice, returnedStr)
	require.Equal(t, sparkpb.InvoiceStatus_RETURNED, byInvoice[returnedStr].Status)
	require.Equal(t, returnedFinalHash, byInvoice[returnedStr].GetTokenTransfer().GetFinalTokenTransactionHash())

	require.Contains(t, byInvoice, pendingStr)
	require.Equal(t, sparkpb.InvoiceStatus_PENDING, byInvoice[pendingStr].Status)
	require.Equal(t, pendingFinalHash, byInvoice[pendingStr].GetTokenTransfer().GetFinalTokenTransactionHash())
}

func TestQuerySparkInvoicesLimitDoesNotMisclassifyExplicitInvoiceList(t *testing.T) {
	config := sparktesting.TestConfig(t)
	ctx, tc := db.ConnectToTestPostgres(t)

	firstStr, firstID := createTestSatsInvoice(t, ctx, tc)
	secondStr, secondID := createTestSatsInvoice(t, ctx, tc)
	createTransferForInvoice(t, ctx, tc, firstID, st.TransferStatusSenderKeyTweaked)
	createTransferForInvoice(t, ctx, tc, secondID, st.TransferStatusSenderKeyTweaked)

	handler := NewSparkInvoiceHandler(config)
	resp, err := handler.QuerySparkInvoices(ctx, &sparkpb.QuerySparkInvoicesRequest{
		Limit:   1,
		Invoice: []string{firstStr, secondStr},
	})
	require.NoError(t, err)
	require.Len(t, resp.InvoiceStatuses, 2)

	byInvoice := make(map[string]sparkpb.InvoiceStatus, 2)
	for _, invoiceStatus := range resp.InvoiceStatuses {
		byInvoice[invoiceStatus.Invoice] = invoiceStatus.Status
	}
	require.Equal(t, sparkpb.InvoiceStatus_FINALIZED, byInvoice[firstStr])
	require.Equal(t, sparkpb.InvoiceStatus_FINALIZED, byInvoice[secondStr])
}

func TestQuerySparkInvoicesRejectsOversizedExplicitInvoiceList(t *testing.T) {
	config := sparktesting.TestConfig(t)
	ctx, tc := db.ConnectToTestPostgres(t)

	invoiceStr, _ := createTestSatsInvoice(t, ctx, tc)
	invoices := make([]string, maxSparkInvoiceLimit+1)
	for i := range invoices {
		invoices[i] = invoiceStr
	}

	handler := NewSparkInvoiceHandler(config)
	_, err := handler.QuerySparkInvoices(ctx, &sparkpb.QuerySparkInvoicesRequest{
		Invoice: invoices,
	})
	require.Error(t, err)
	require.ErrorContains(t, err, "too many invoice strings provided")
}

// TestQuerySparkInvoicesReturnsPendingStatusForKeyTweakPending checks the second
// PENDING-eligible status: SenderKeyTweakPending.
func TestQuerySparkInvoicesReturnsPendingStatusForKeyTweakPending(t *testing.T) {
	config := sparktesting.TestConfig(t)
	ctx, tc := db.ConnectToTestPostgres(t)

	invoiceStr, invoiceID := createTestSatsInvoice(t, ctx, tc)
	createTransferForInvoice(t, ctx, tc, invoiceID, st.TransferStatusSenderKeyTweakPending)

	handler := NewSparkInvoiceHandler(config)
	resp, err := handler.QuerySparkInvoices(ctx, &sparkpb.QuerySparkInvoicesRequest{
		Invoice: []string{invoiceStr},
	})
	require.NoError(t, err)
	require.Len(t, resp.InvoiceStatuses, 1)
	require.Equal(t, sparkpb.InvoiceStatus_PENDING, resp.InvoiceStatuses[0].Status,
		"expected PENDING for SenderKeyTweakPending transfer, got %s",
		resp.InvoiceStatuses[0].Status)
}

// TestQuerySparkInvoicesReturnsReturnedStatus verifies that an invoice with an
// associated transfer that was returned to the sender (TransferStatusReturned) is
// reported as RETURNED — not NOT_FOUND.
//
// RETURNED means the invoice was used but the underlying transfer did not
// complete; funds went back to the sender.  Without this test, a regression that
// collapsed RETURNED into NOT_FOUND would be invisible to clients.
func TestQuerySparkInvoicesReturnsReturnedStatus(t *testing.T) {
	config := sparktesting.TestConfig(t)
	ctx, tc := db.ConnectToTestPostgres(t)

	invoiceStr, invoiceID := createTestSatsInvoice(t, ctx, tc)
	// TransferStatusReturned sits outside both the PENDING set
	// (SenderKeyTweakPending / SenderInitiatedCoordinator) and the FINALIZED set
	// (SenderKeyTweaked … Completed), so it exercises the RETURNED branch.
	createTransferForInvoice(t, ctx, tc, invoiceID, st.TransferStatusReturned)

	handler := NewSparkInvoiceHandler(config)
	resp, err := handler.QuerySparkInvoices(ctx, &sparkpb.QuerySparkInvoicesRequest{
		Invoice: []string{invoiceStr},
	})
	require.NoError(t, err)
	require.Len(t, resp.InvoiceStatuses, 1)
	require.Equal(t, sparkpb.InvoiceStatus_RETURNED, resp.InvoiceStatuses[0].Status,
		"expected RETURNED for a returned transfer, got %s",
		resp.InvoiceStatuses[0].Status)
}

// TestQuerySparkInvoicesReturnsReturnedStatusForExpiredTransfer checks that an
// expired transfer (TransferStatusExpired) also maps to RETURNED, not NOT_FOUND.
func TestQuerySparkInvoicesReturnsReturnedStatusForExpiredTransfer(t *testing.T) {
	config := sparktesting.TestConfig(t)
	ctx, tc := db.ConnectToTestPostgres(t)

	invoiceStr, invoiceID := createTestSatsInvoice(t, ctx, tc)
	createTransferForInvoice(t, ctx, tc, invoiceID, st.TransferStatusExpired)

	handler := NewSparkInvoiceHandler(config)
	resp, err := handler.QuerySparkInvoices(ctx, &sparkpb.QuerySparkInvoicesRequest{
		Invoice: []string{invoiceStr},
	})
	require.NoError(t, err)
	require.Len(t, resp.InvoiceStatuses, 1)
	require.Equal(t, sparkpb.InvoiceStatus_RETURNED, resp.InvoiceStatuses[0].Status,
		"expected RETURNED for an expired transfer, got %s",
		resp.InvoiceStatuses[0].Status)
}

// TestQuerySparkInvoicesDistinguishesPendingAndFinalized ensures that a single
// batch query correctly returns different statuses for a PENDING and a FINALIZED
// invoice simultaneously.
func TestQuerySparkInvoicesDistinguishesPendingAndFinalized(t *testing.T) {
	config := sparktesting.TestConfig(t)
	ctx, tc := db.ConnectToTestPostgres(t)

	pendingStr, pendingID := createTestSatsInvoice(t, ctx, tc)
	finalizedStr, finalizedID := createTestSatsInvoice(t, ctx, tc)

	createTransferForInvoice(t, ctx, tc, pendingID, st.TransferStatusSenderInitiatedCoordinator)
	// SenderKeyTweaked is the first status in the FINALIZED set.
	createTransferForInvoice(t, ctx, tc, finalizedID, st.TransferStatusSenderKeyTweaked)

	handler := NewSparkInvoiceHandler(config)
	resp, err := handler.QuerySparkInvoices(ctx, &sparkpb.QuerySparkInvoicesRequest{
		Invoice: []string{pendingStr, finalizedStr},
	})
	require.NoError(t, err)
	require.Len(t, resp.InvoiceStatuses, 2)

	statusByInvoice := make(map[string]sparkpb.InvoiceStatus, 2)
	for _, s := range resp.InvoiceStatuses {
		statusByInvoice[s.Invoice] = s.Status
	}
	require.Equal(t, sparkpb.InvoiceStatus_PENDING, statusByInvoice[pendingStr],
		"pending invoice should be PENDING")
	require.Equal(t, sparkpb.InvoiceStatus_FINALIZED, statusByInvoice[finalizedStr],
		"finalized invoice should be FINALIZED")
}
