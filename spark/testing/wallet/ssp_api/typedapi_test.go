package sspapi

import (
	"bytes"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/btcsuite/btcd/wire"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/testing/wallet/ssp_api/mutations"

	"github.com/stretchr/testify/require"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/stretchr/testify/assert"
)

const (
	identityPublicKey = "03abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890ab"
	adaptorPublicKey  = "adaptor-pubkey"
)

var hash = bytes.Repeat([]byte{0x1}, 32)

func TestNewTypedSparkServiceAPI(t *testing.T) {
	requester, err := NewRequester(identityPublicKey)
	require.NoError(t, err)
	api := NewTypedSparkServiceAPI(requester)

	assert.NotNil(t, api)
	assert.Equal(t, requester, api.requester)
}

func TestTypedSparkServiceAPI_CreateInvoice(t *testing.T) {
	response := map[string]any{
		"request_lightning_receive": map[string]any{
			"request": map[string]any{"invoice": map[string]any{"encoded_invoice": "lnbc10u1pw2e2pp..."}},
		},
	}
	server := newValidatingServer(t, response, "RequestLightningReceive", identityPublicKey, false)
	defer server.Close()
	api := apiForServer(t, server)

	result, err := api.CreateInvoice(
		t.Context(),
		btcnetwork.Mainnet,
		1000,
		hash,
		"test memo",
		3600,
	)

	require.NoError(t, err)
	assert.Equal(t, "lnbc10u1pw2e2pp...", result)
}

func TestTypedSparkServiceAPI_CreateInvoice_NetworkError(t *testing.T) {
	server := newErrorServer(t, http.StatusForbidden, nil)
	defer server.Close()
	api := apiForServer(t, server)

	result, err := api.CreateInvoice(
		t.Context(),
		btcnetwork.Mainnet,
		1000,
		hash,
		"test memo",
		3600,
	)

	require.Error(t, err)
	assert.Empty(t, result)
}

func TestTypedSparkServiceAPI_PayInvoice(t *testing.T) {
	response := map[string]any{
		"request_lightning_send": map[string]any{
			"request": map[string]any{"id": "request-123"},
		},
	}
	server := newValidatingServer(t, response, "RequestLightningSend", identityPublicKey, false)
	defer server.Close()
	api := apiForServer(t, server)

	result, err := api.PayInvoice(t.Context(), "lnbc10u1pw2e2pp...")

	require.NoError(t, err)
	assert.Equal(t, "request-123", result)
}

func TestTypedSparkServiceAPI_PayInvoice_NetworkError(t *testing.T) {
	server := newErrorServer(t, http.StatusForbidden, nil)
	defer server.Close()
	api := apiForServer(t, server)

	result, err := api.PayInvoice(t.Context(), "lnbc10u1pw2e2pp...")

	require.Error(t, err)
	assert.Empty(t, result)
}

func TestTypedSparkServiceAPI_InitiateCoopExit(t *testing.T) {
	tx, err := common.SerializeTx(&wire.MsgTx{TxIn: []*wire.TxIn{{}}, TxOut: []*wire.TxOut{{}}})
	require.NoError(t, err)
	txHex := hex.EncodeToString(tx)
	response := map[string]any{
		"request_coop_exit": map[string]any{
			"request": map[string]any{
				"id":                        "coop-exit-123",
				"raw_connector_transaction": txHex,
			},
		},
	}
	server := newValidatingServer(t, response, "RequestCoopExit", identityPublicKey, false)
	defer server.Close()
	api := apiForServer(t, server)

	coopExitID, txID, gotTX, err := api.InitiateCoopExit(
		t.Context(),
		[]uuid.UUID{uuid.New(), uuid.New()},
		"bc1qxy2kgdygjrsqtzq2n0yrf2493p83kkfjhx0wlh",
		mutations.ExitSpeedFAST,
	)

	require.NoError(t, err)
	assert.Equal(t, "coop-exit-123", coopExitID)
	assert.NotNil(t, txID)
	assert.NotNil(t, gotTX)
}

func TestTypedSparkServiceAPI_InitiateCoopExit_NetworkError(t *testing.T) {
	leafID1 := uuid.New()
	leafID2 := uuid.New()

	server := newErrorServer(t, http.StatusForbidden, nil)
	defer server.Close()
	api := apiForServer(t, server)

	coopExitID, txid, tx, err := api.InitiateCoopExit(
		t.Context(),
		[]uuid.UUID{leafID1, leafID2},
		"bc1qxy2kgdygjrsqtzq2n0yrf2493p83kkfjhx0wlh",
		mutations.ExitSpeedFAST,
	)

	require.Error(t, err)
	assert.Empty(t, coopExitID)
	assert.Nil(t, txid)
	assert.Nil(t, tx)
}

func TestTypedSparkServiceAPI_InitiateCoopExit_InvalidHex(t *testing.T) {
	leafID1 := uuid.New()
	leafID2 := uuid.New()

	response := map[string]any{
		"request_coop_exit": map[string]any{
			"request": map[string]any{
				"id":                        "coop-exit-123",
				"raw_connector_transaction": "invalid-hex",
			},
		},
	}
	server := newValidatingServer(t, response, "RequestCoopExit", identityPublicKey, false)
	defer server.Close()
	api := apiForServer(t, server)

	coopExitID, txid, tx, err := api.InitiateCoopExit(
		t.Context(),
		[]uuid.UUID{leafID1, leafID2},
		"bc1qxy2kgdygjrsqtzq2n0yrf2493p83kkfjhx0wlh",
		mutations.ExitSpeedFAST,
	)

	require.Error(t, err)
	assert.Empty(t, coopExitID)
	assert.Nil(t, txid)
	assert.Nil(t, tx)
}

func TestTypedSparkServiceAPI_CompleteCoopExit(t *testing.T) {
	transferID := uuid.New()

	response := map[string]any{
		"complete_coop_exit": map[string]any{
			"request": map[string]any{
				"id": "complete-coop-exit-123",
			},
		},
	}
	server := newValidatingServer(t, response, "CompleteCoopExit", identityPublicKey, false)
	defer server.Close()
	api := apiForServer(t, server)

	result, err := api.CompleteCoopExit(t.Context(), transferID, "coop-exit-123")

	require.NoError(t, err)
	assert.Equal(t, "complete-coop-exit-123", result)
}

func TestTypedSparkServiceAPI_CompleteCoopExit_NetworkError(t *testing.T) {
	transferID := uuid.New()

	server := newErrorServer(t, http.StatusForbidden, nil)
	defer server.Close()
	api := apiForServer(t, server)

	result, err := api.CompleteCoopExit(t.Context(), transferID, "coop-exit-123")

	require.Error(t, err)
	assert.Empty(t, result)
}

func apiForServer(t *testing.T, server *httptest.Server) *TypedSparkServiceAPI {
	requester, err := NewRequesterWithBaseURL(identityPublicKey, server.URL)
	require.NoError(t, err)
	return NewTypedSparkServiceAPI(requester)
}
