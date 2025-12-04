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

func TestTypedSparkServiceAPI_RequestLeavesSwap(t *testing.T) {
	id := uuid.New().String()
	userLeaves := []SwapLeaf{{
		LeafID:                       id,
		RawUnsignedRefundTransaction: "raw-tx-1",
		AdaptorAddedSignature:        "signature-1",
	}}
	response := map[string]any{
		"request_leaves_swap": map[string]any{
			"request": map[string]any{
				"id": "swap-request-123",
				"swap_leaves": []map[string]any{{
					"leaf_id":                         id,
					"raw_unsigned_refund_transaction": "raw-tx-2",
					"adaptor_signed_signature":        "signature-2",
				}},
			},
		},
	}
	server := newValidatingServer(t, response, "RequestLeavesSwap", identityPublicKey, false)
	defer server.Close()
	api := apiForServer(t, server)

	totalSats := int64(1000)
	targetAmountSats := int64(800)
	feeSats := totalSats - targetAmountSats

	requestID, leaves, err := api.RequestLeavesSwap(
		t.Context(),
		adaptorPublicKey,
		totalSats,
		targetAmountSats,
		feeSats,
		userLeaves,
	)

	require.NoError(t, err)
	assert.Equal(t, "swap-request-123", requestID)
	assert.Equal(t, []SwapLeaf{{
		LeafID:                       id,
		RawUnsignedRefundTransaction: "raw-tx-2",
		AdaptorAddedSignature:        "signature-2",
	}}, leaves)
}

func TestTypedSparkServiceAPI_RequestLeavesSwap_InvalidUUID(t *testing.T) {
	userLeaves := []SwapLeaf{{
		LeafID:                       "invalid-uuid",
		RawUnsignedRefundTransaction: "raw-tx-1",
		AdaptorAddedSignature:        "signature-1",
	}}

	requester, err := NewRequesterWithBaseURL(identityPublicKey, "http://localhost:8080")
	require.NoError(t, err)
	api := NewTypedSparkServiceAPI(requester)

	requestID, leaves, err := api.RequestLeavesSwap(t.Context(),
		adaptorPublicKey,
		1000,
		800,
		200,
		userLeaves,
	)

	require.Error(t, err)
	assert.Empty(t, requestID)
	assert.Nil(t, leaves)
}

func TestTypedSparkServiceAPI_RequestLeavesSwap_NetworkError(t *testing.T) {
	userLeaves := []SwapLeaf{{
		LeafID:                       uuid.New().String(),
		RawUnsignedRefundTransaction: "raw-tx-1",
		AdaptorAddedSignature:        "signature-1",
	}}
	server := newErrorServer(t, http.StatusForbidden, nil)
	defer server.Close()
	api := apiForServer(t, server)

	requestID, leaves, err := api.RequestLeavesSwap(
		t.Context(),
		adaptorPublicKey,
		1000,
		800,
		200,
		userLeaves,
	)

	require.Error(t, err)
	assert.Empty(t, requestID)
	assert.Nil(t, leaves)
}

func TestTypedSparkServiceAPI_CompleteLeavesSwap(t *testing.T) {
	transferID := uuid.New()
	response := map[string]any{
		"complete_leaves_swap": map[string]any{
			"request": map[string]any{"id": "complete-request-123"},
		},
	}
	server := newValidatingServer(t, response, "CompleteLeavesSwap", identityPublicKey, false)
	defer server.Close()
	api := apiForServer(t, server)

	result, err := api.CompleteLeavesSwap(
		t.Context(),
		"secret-key",
		transferID,
		"swap-request-123",
	)

	require.NoError(t, err)
	assert.Equal(t, "complete-request-123", result)
}

func TestTypedSparkServiceAPI_CompleteLeavesSwap_NetworkError(t *testing.T) {
	server := newErrorServer(t, http.StatusForbidden, nil)
	defer server.Close()
	api := apiForServer(t, server)

	result, err := api.CompleteLeavesSwap(t.Context(), "secret-key", uuid.New(), "swap-request-123")

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

func TestTypedSparkServiceAPI_FetchPublicKeyByPhoneNumber(t *testing.T) {
	response := map[string]any{
		"wallet_user_identity_public_key": map[string]any{"identity_public_key": identityPublicKey},
	}
	server := newValidatingServer(t, response, "WalletUserIdentityPublicKey", identityPublicKey, false)
	defer server.Close()
	api := apiForServer(t, server)

	result, err := api.FetchPublicKeyByPhoneNumber(t.Context(), "+1234567890")

	require.NoError(t, err)
	assert.Equal(t, identityPublicKey, result)
}

func TestTypedSparkServiceAPI_FetchPublicKeyByPhoneNumber_NetworkError(t *testing.T) {
	server := newErrorServer(t, http.StatusForbidden, nil)
	defer server.Close()
	api := apiForServer(t, server)

	result, err := api.FetchPublicKeyByPhoneNumber(t.Context(), "+1234567890")

	require.Error(t, err)
	assert.Empty(t, result)
}

func TestTypedSparkServiceAPI_StartReleaseSeed(t *testing.T) {
	response := map[string]any{"start_release_seed": map[string]any{"success": true}}
	server := newValidatingServer(t, response, "StartReleaseSeed", identityPublicKey, false)
	defer server.Close()
	api := apiForServer(t, server)

	err := api.StartReleaseSeed(t.Context(), "+1234567890")
	require.NoError(t, err)
}

func TestTypedSparkServiceAPI_StartReleaseSeed_NetworkError(t *testing.T) {
	server := newErrorServer(t, http.StatusForbidden, nil)
	defer server.Close()
	api := apiForServer(t, server)

	err := api.StartReleaseSeed(t.Context(), "+1234567890")
	require.Error(t, err)
}

func TestTypedSparkServiceAPI_CompleteReleaseSeed(t *testing.T) {
	response := map[string]any{
		"complete_seed_release": map[string]any{
			"seed": "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
		},
	}
	server := newValidatingServer(t, response, "CompleteReleaseSeed", identityPublicKey, false)
	defer server.Close()
	api := apiForServer(t, server)

	result, err := api.CompleteReleaseSeed(t.Context(), "+1234567890", "123456")

	require.NoError(t, err)
	assert.Equal(t, []byte{0xab, 0xcd, 0xef, 0x12, 0x34, 0x56, 0x78, 0x90, 0xab, 0xcd, 0xef, 0x12, 0x34, 0x56, 0x78, 0x90, 0xab, 0xcd, 0xef, 0x12, 0x34, 0x56, 0x78, 0x90, 0xab, 0xcd, 0xef, 0x12, 0x34, 0x56, 0x78, 0x90}, result)
}

func TestTypedSparkServiceAPI_CompleteReleaseSeed_NetworkError(t *testing.T) {
	server := newErrorServer(t, http.StatusForbidden, nil)
	defer server.Close()
	api := apiForServer(t, server)

	result, err := api.CompleteReleaseSeed(t.Context(), "+1234567890", "123456")

	require.Error(t, err)
	assert.Nil(t, result)
}

func TestTypedSparkServiceAPI_CompleteReleaseSeed_InvalidHex(t *testing.T) {
	response := map[string]any{"complete_seed_release": map[string]any{"seed": "invalid-hex"}}
	server := newValidatingServer(t, response, "CompleteReleaseSeed", identityPublicKey, false)
	defer server.Close()
	api := apiForServer(t, server)

	result, err := api.CompleteReleaseSeed(t.Context(), "+1234567890", "123456")

	require.Error(t, err)
	assert.Empty(t, result)
}

func TestTypedSparkServiceAPI_NotifyReceiverTransfer(t *testing.T) {
	response := map[string]any{"notify_receiver_transfer": map[string]any{"success": true}}
	server := newValidatingServer(t, response, "NotifyReceiverTransfer", identityPublicKey, false)
	defer server.Close()
	api := apiForServer(t, server)

	err := api.NotifyReceiverTransfer(t.Context(), "+1234567890", 1000)
	require.NoError(t, err)
}

func TestTypedSparkServiceAPI_NotifyReceiverTransfer_NetworkError(t *testing.T) {
	server := newErrorServer(t, http.StatusForbidden, nil)
	defer server.Close()
	api := apiForServer(t, server)

	err := api.NotifyReceiverTransfer(t.Context(), "+1234567890", 1000)
	require.Error(t, err)
}

func apiForServer(t *testing.T, server *httptest.Server) *TypedSparkServiceAPI {
	requester, err := NewRequesterWithBaseURL(identityPublicKey, server.URL)
	require.NoError(t, err)
	return NewTypedSparkServiceAPI(requester)
}
