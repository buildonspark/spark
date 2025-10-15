package sspapi

import (
	"bytes"
	"context"
	"encoding/hex"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
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
	bitcoinNetwork common.Network,
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

func (s *TypedSparkServiceAPI) RequestLeavesSwap(
	ctx context.Context,
	adaptorPubkey string,
	totalAmountSats int64,
	targetAmountSats int64,
	feeSats int64,
	userLeaves []SwapLeaf,
) (string, []SwapLeaf, error) {
	idempotencyKey := uuid.New().String()
	asLeafInput := make([]mutations.UserLeafInput, len(userLeaves))
	for i, leaf := range userLeaves {
		id, err := uuid.Parse(leaf.LeafID)
		if err != nil {
			return "", nil, err
		}
		asLeafInput[i] = mutations.UserLeafInput{
			LeafId:                       id,
			RawUnsignedRefundTransaction: leaf.RawUnsignedRefundTransaction,
			AdaptorAddedSignature:        leaf.AdaptorAddedSignature,
		}
	}

	response, err := mutations.RequestLeavesSwap(ctx, s.requester, adaptorPubkey, totalAmountSats, targetAmountSats, feeSats, asLeafInput, idempotencyKey)
	if err != nil {
		return "", nil, err
	}
	request := response.RequestLeavesSwap.Request
	requestID := request.Id
	leavesJSON := request.SwapLeaves
	leaves := make([]SwapLeaf, len(leavesJSON))
	for i, leaf := range leavesJSON {
		leaves[i] = SwapLeaf{
			LeafID:                       leaf.LeafId.String(),
			RawUnsignedRefundTransaction: leaf.RawUnsignedRefundTransaction,
			AdaptorAddedSignature:        leaf.AdaptorSignedSignature,
		}
	}
	return requestID, leaves, nil
}

func (s *TypedSparkServiceAPI) CompleteLeavesSwap(
	ctx context.Context,
	adaptorSecretKey string,
	userOutboundTransferExternalID uuid.UUID,
	leavesSwapRequestID string,
) (string, error) {
	response, err := mutations.CompleteLeavesSwap(ctx, s.requester, adaptorSecretKey, userOutboundTransferExternalID, leavesSwapRequestID)
	if err != nil {
		return "", err
	}
	return response.CompleteLeavesSwap.Request.Id, nil
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
	log.Printf("connectorTxString: %s\n", connectorTxString)
	connectorTxBytes, err := hex.DecodeString(connectorTxString)
	if err != nil {
		return "", nil, nil, err
	}
	var connectorTx wire.MsgTx
	if err = connectorTx.Deserialize(bytes.NewReader(connectorTxBytes)); err != nil {
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

func (s *TypedSparkServiceAPI) FetchPublicKeyByPhoneNumber(ctx context.Context, phoneNumber string) (string, error) {
	response, err := mutations.WalletUserIdentityPublicKey(ctx, s.requester, phoneNumber)
	if err != nil {
		return "", err
	}
	return response.WalletUserIdentityPublicKey.IdentityPublicKey, nil
}

func (s *TypedSparkServiceAPI) StartReleaseSeed(ctx context.Context, phoneNumber string) error {
	_, err := mutations.StartReleaseSeed(ctx, s.requester, phoneNumber)
	return err
}

func (s *TypedSparkServiceAPI) CompleteReleaseSeed(ctx context.Context, phoneNumber string, code string) ([]byte, error) {
	response, err := mutations.CompleteReleaseSeed(ctx, s.requester, phoneNumber, code)
	if err != nil {
		return nil, err
	}
	return hex.DecodeString(response.CompleteSeedRelease.Seed)
}

func (s *TypedSparkServiceAPI) NotifyReceiverTransfer(ctx context.Context, phoneNumber string, amountSats int64) error {
	_, err := mutations.NotifyReceiverTransfer(ctx, s.requester, phoneNumber, amountSats)
	return err
}
