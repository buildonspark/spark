package handler

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/errors"
)

const getUtxosForIdentityCursorVersion = 1

type getUtxosForIdentityCursor struct {
	Version     int    `json:"v"`
	BlockHeight int64  `json:"bh"`
	Txid        string `json:"tx"`
	Vout        uint32 `json:"vo"`
	ID          string `json:"id"`
}

func decodeGetUtxosForIdentityCursor(cursor string) (*getUtxosForIdentityCursor, []byte, uuid.UUID, error) {
	cursorBytes, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		cursorBytes, err = base64.URLEncoding.DecodeString(cursor)
		if err != nil {
			return nil, nil, uuid.Nil, errors.InvalidArgumentMalformedField(fmt.Errorf("invalid cursor: %w", err))
		}
	}

	var payload getUtxosForIdentityCursor
	if err := json.Unmarshal(cursorBytes, &payload); err != nil {
		return nil, nil, uuid.Nil, errors.InvalidArgumentMalformedField(fmt.Errorf("invalid cursor payload: %w", err))
	}

	if payload.Version != getUtxosForIdentityCursorVersion {
		return nil, nil, uuid.Nil, errors.InvalidArgumentMalformedField(
			fmt.Errorf("unsupported cursor version: got %d, expected %d", payload.Version, getUtxosForIdentityCursorVersion),
		)
	}

	txidBytes, err := hex.DecodeString(payload.Txid)
	if err != nil {
		return nil, nil, uuid.Nil, errors.InvalidArgumentMalformedField(fmt.Errorf("invalid cursor txid: %w", err))
	}

	utxoID, err := uuid.Parse(payload.ID)
	if err != nil {
		return nil, nil, uuid.Nil, errors.InvalidArgumentMalformedField(fmt.Errorf("invalid cursor id: %w", err))
	}

	return &payload, txidBytes, utxoID, nil
}

func encodeGetUtxosForIdentityCursor(utxo *ent.Utxo) (string, error) {
	cursorPayload := getUtxosForIdentityCursor{
		Version:     getUtxosForIdentityCursorVersion,
		BlockHeight: utxo.BlockHeight,
		Txid:        hex.EncodeToString(utxo.Txid),
		Vout:        utxo.Vout,
		ID:          utxo.ID.String(),
	}
	cursorPayloadBytes, err := json.Marshal(cursorPayload)
	if err != nil {
		return "", fmt.Errorf("failed to marshal cursor payload: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(cursorPayloadBytes), nil
}
