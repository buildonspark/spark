package handler

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestDecodeGetUtxosForIdentityCursor(t *testing.T) {
	validPayload := getUtxosForIdentityCursor{
		Version:     getUtxosForIdentityCursorVersion,
		BlockHeight: 123,
		Txid:        strings.Repeat("ab", 32),
		Vout:        7,
		ID:          uuid.NewString(),
	}

	t.Run("valid raw URL encoding", func(t *testing.T) {
		payload, txidBytes, utxoID, err := decodeGetUtxosForIdentityCursor(encodeGetUtxosCursorPayload(t, validPayload, false))
		require.NoError(t, err)
		require.Equal(t, validPayload.Version, payload.Version)
		require.Equal(t, validPayload.BlockHeight, payload.BlockHeight)
		require.Equal(t, validPayload.Txid, payload.Txid)
		require.Equal(t, validPayload.Vout, payload.Vout)
		require.Equal(t, validPayload.ID, payload.ID)
		require.Len(t, txidBytes, 32)
		require.Equal(t, validPayload.ID, utxoID.String())
	})

	t.Run("valid padded URL encoding", func(t *testing.T) {
		payload, txidBytes, utxoID, err := decodeGetUtxosForIdentityCursor(encodeGetUtxosCursorPayload(t, validPayload, true))
		require.NoError(t, err)
		require.Equal(t, validPayload.ID, payload.ID)
		require.Len(t, txidBytes, 32)
		require.Equal(t, validPayload.ID, utxoID.String())
	})

	for _, tc := range []struct {
		name    string
		cursor  string
		wantErr string
	}{
		{
			name:    "malformed base64",
			cursor:  "not@@base64",
			wantErr: "invalid cursor",
		},
		{
			name:    "invalid json payload",
			cursor:  base64.RawURLEncoding.EncodeToString([]byte("{")),
			wantErr: "invalid cursor payload",
		},
		{
			name: "unsupported version",
			cursor: encodeGetUtxosCursorPayload(t, cursorPayloadWith(validPayload, func(payload *getUtxosForIdentityCursor) {
				payload.Version = getUtxosForIdentityCursorVersion + 1
			}), false),
			wantErr: "unsupported cursor version",
		},
		{
			name: "invalid txid hex",
			cursor: encodeGetUtxosCursorPayload(t, cursorPayloadWith(validPayload, func(payload *getUtxosForIdentityCursor) {
				payload.Txid = "not-hex"
			}), false),
			wantErr: "invalid cursor txid",
		},
		{
			name: "short txid",
			cursor: encodeGetUtxosCursorPayload(t, cursorPayloadWith(validPayload, func(payload *getUtxosForIdentityCursor) {
				payload.Txid = "00"
			}), false),
			wantErr: "invalid cursor txid length",
		},
		{
			name: "long txid",
			cursor: encodeGetUtxosCursorPayload(t, cursorPayloadWith(validPayload, func(payload *getUtxosForIdentityCursor) {
				payload.Txid = strings.Repeat("00", 33)
			}), false),
			wantErr: "invalid cursor txid length",
		},
		{
			name: "invalid uuid",
			cursor: encodeGetUtxosCursorPayload(t, cursorPayloadWith(validPayload, func(payload *getUtxosForIdentityCursor) {
				payload.ID = "not-a-uuid"
			}), false),
			wantErr: "invalid cursor id",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, err := decodeGetUtxosForIdentityCursor(tc.cursor)
			require.Error(t, err)
			require.Equal(t, codes.InvalidArgument, status.Code(err))
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

func encodeGetUtxosCursorPayload(t *testing.T, payload getUtxosForIdentityCursor, padded bool) string {
	t.Helper()

	payloadBytes, err := json.Marshal(payload)
	require.NoError(t, err)
	if padded {
		return base64.URLEncoding.EncodeToString(payloadBytes)
	}
	return base64.RawURLEncoding.EncodeToString(payloadBytes)
}

func cursorPayloadWith(payload getUtxosForIdentityCursor, mutate func(*getUtxosForIdentityCursor)) getUtxosForIdentityCursor {
	mutate(&payload)
	return payload
}
