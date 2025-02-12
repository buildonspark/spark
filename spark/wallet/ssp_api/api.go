package sspapi

import (
	"context"
	"encoding/hex"

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
		"network":      bitcoinNetwork.String(),
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
