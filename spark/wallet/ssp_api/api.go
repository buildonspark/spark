package sspapi

import (
	"context"
	"encoding/hex"

	"github.com/lightsparkdev/spark-go/common"
)

func CreateInvoice(
	identityPublicKey []byte,
	bitcoinNetwork common.Network,
	amountSats int64,
	paymentHash []byte,
	memo string,
	expirySecs int,
) (*string, int64, error) {
	identityPublicKeyString := hex.EncodeToString(identityPublicKey)
	requester, err := NewRequesterWithBaseURL(identityPublicKeyString, nil)
	if err != nil {
		return nil, 0, err
	}

	variables := map[string]interface{}{
		"network":      bitcoinNetwork.String(),
		"amount_sats":  amountSats,
		"payment_hash": hex.EncodeToString(paymentHash),
		"memo":         memo,
		"expiry_secs":  expirySecs,
	}

	response, err := requester.ExecuteGraphqlWithContext(context.Background(), RequestLightningReceiveMutation, variables)
	if err != nil {
		return nil, 0, err
	}

	encodedInvoice := response["request_lightning_receive"].(map[string]interface{})["request"].(map[string]interface{})["invoice"].(map[string]interface{})["encoded_envoice"].(string)

	fees := response["request_lightning_receive"].(map[string]interface{})["request"].(map[string]interface{})["fee"].(map[string]interface{})["original_value"].(float64)

	return &encodedInvoice, int64(fees), nil
}
