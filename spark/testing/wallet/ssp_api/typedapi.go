package sspapi

import (
	"bytes"
	"context"
	"encoding/hex"
	"slices"
	"strings"
	"time"

	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/testing/wallet/ssp_api/mutations"
)

// Generates the typed function signatures for the mutations in ./mutations
//go:generate go run github.com/Khan/genqlient

type TypedSparkServiceAPI struct {
	requester *Requester
}

func NewTypedSparkServiceAPI(requester *Requester) *TypedSparkServiceAPI {
	return &TypedSparkServiceAPI{requester: requester}
}

func (s *TypedSparkServiceAPI) CreateInvoice(
	ctx context.Context,
	bitcoinNetwork btcnetwork.Network,
	amountSats int64,
	paymentHash []byte,
	memo string,
	expiry time.Duration,
) (string, error) {
	network := mutations.BitcoinNetwork(strings.ToUpper(bitcoinNetwork.String()))
	response, err := mutations.RequestLightningReceive(ctx, s.requester, network, amountSats, paymentHash, int(expiry.Seconds()), memo)
	if err != nil {
		return "", err
	}
	return response.RequestLightningReceive.Request.Invoice.EncodedInvoice, nil
}

func (s *TypedSparkServiceAPI) PayInvoice(ctx context.Context, invoice string) (string, error) {
	idempotencyKey := uuid.NewString()
	response, err := mutations.RequestLightningSend(ctx, s.requester, invoice, idempotencyKey)
	if err != nil {
		return "", err
	}
	return response.RequestLightningSend.Request.Id, nil
}

func (s *TypedSparkServiceAPI) InitiateCoopExit(
	ctx context.Context,
	leafExternalIDs []uuid.UUID,
	address string,
	speed mutations.ExitSpeed,
) (string, []byte, *wire.MsgTx, error) {
	idempotencyKey := uuid.NewString()

	response, err := mutations.RequestCoopExit(ctx, s.requester, leafExternalIDs, address, idempotencyKey, speed)
	if err != nil {
		return "", nil, nil, err
	}

	request := response.RequestCoopExit.Request
	coopExitID := request.Id
	connectorTxString := request.RawConnectorTransaction
	logger := logging.GetLoggerFromContext(ctx)
	logger.Sugar().Infof("connectorTxString: %s", connectorTxString)
	connectorTxBytes, err := hex.DecodeString(connectorTxString)
	if err != nil {
		return "", nil, nil, err
	}
	var connectorTx wire.MsgTx
	if err := connectorTx.Deserialize(bytes.NewReader(connectorTxBytes)); err != nil {
		return "", nil, nil, err
	}
	coopExitTxid := connectorTx.TxIn[0].PreviousOutPoint.Hash[:]
	slices.Reverse(coopExitTxid)

	return coopExitID, coopExitTxid, &connectorTx, nil
}

func (s *TypedSparkServiceAPI) CompleteCoopExit(ctx context.Context, userOutboundTransferExternalID uuid.UUID, coopExitRequestID string) (string, error) {
	response, err := mutations.CompleteCoopExit(ctx, s.requester, userOutboundTransferExternalID, coopExitRequestID)
	if err != nil {
		return "", err
	}
	return response.CompleteCoopExit.Request.Id, nil
}
