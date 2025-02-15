package sspapi

import (
	"context"
	"encoding/hex"
	"strings"

	"github.com/decred/dcrd/dcrec/secp256k1"
	"github.com/lightsparkdev/spark-go/common"
)

type SparkServiceAPI struct {
	Requester *Requester
}

func NewSparkServiceAPI(requester *Requester) *SparkServiceAPI {
	return &SparkServiceAPI{
		Requester: requester,
	}
}

func (s *SparkServiceAPI) CreateInvoice(
	bitcoinNetwork common.Network,
	amountSats uint64,
	paymentHash []byte,
	memo string,
	expirySecs int,
) (*string, int64, error) {
	variables := map[string]interface{}{
		"network":      strings.ToUpper(bitcoinNetwork.String()),
		"amount_sats":  amountSats,
		"payment_hash": hex.EncodeToString(paymentHash),
		"memo":         memo,
		"expiry_secs":  expirySecs,
	}

	response, err := s.Requester.ExecuteGraphqlWithContext(context.Background(), RequestLightningReceiveMutation, variables)
	if err != nil {
		return nil, 0, err
	}

	encodedInvoice := response["request_lightning_receive"].(map[string]interface{})["request"].(map[string]interface{})["invoice"].(map[string]interface{})["encoded_envoice"].(string)

	fees := response["request_lightning_receive"].(map[string]interface{})["request"].(map[string]interface{})["fee"].(map[string]interface{})["original_value"].(float64)

	return &encodedInvoice, int64(fees), nil
}

func (s *SparkServiceAPI) PayInvoice(
	invoice string,
) (string, error) {
	randomKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		return "", err
	}
	idempotencyKey := hex.EncodeToString(randomKey.Serialize())
	variables := map[string]interface{}{
		"encoded_invoice": invoice,
		"idempotency_key": idempotencyKey,
	}

	response, err := s.Requester.ExecuteGraphqlWithContext(context.Background(), RequestLightningSendMutation, variables)
	if err != nil {
		return "", err
	}

	request := response["request_lightning_send"].(map[string]interface{})["request"].(map[string]interface{})
	return request["id"].(string), nil
}

func (s *SparkServiceAPI) RequestLeavesSwap(
	adaptorPubkey string,
	totalAmountSats uint64,
	targetAmountSats uint64,
	feeSats uint64,
	network common.Network,
) (string, error) {
	variables := map[string]interface{}{
		"adaptor_pubkey":     adaptorPubkey,
		"total_amount_sats":  totalAmountSats,
		"target_amount_sats": targetAmountSats,
		"fee_sats":           feeSats,
		"network":            strings.ToUpper(network.String()),
	}

	response, err := s.Requester.ExecuteGraphqlWithContext(context.Background(), RequestLeavesSwapMutation, variables)
	if err != nil {
		return "", err
	}

	request := response["request_leaves_swap"].(map[string]interface{})["request"].(map[string]interface{})["id"].(string)
	return request, nil
}

func (s *SparkServiceAPI) CompleteLeavesSwap(
	adaptorSecretKey string,
	userOutboundTransferExternalID string,
	leavesSwapRequestID string,
) (string, error) {
	variables := map[string]interface{}{
		"adaptor_secret_key":                 adaptorSecretKey,
		"user_outbound_transfer_external_id": userOutboundTransferExternalID,
		"leaves_swap_request_id":             leavesSwapRequestID,
	}

	response, err := s.Requester.ExecuteGraphqlWithContext(context.Background(), CompleteLeavesSwapMutation, variables)
	if err != nil {
		return "", err
	}

	request := response["complete_leaves_swap"].(map[string]interface{})["request"].(map[string]interface{})["id"].(string)
	return request, nil
}
