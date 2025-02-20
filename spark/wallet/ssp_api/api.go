package sspapi

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"log"
	"strings"

	"github.com/btcsuite/btcd/wire"
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

type SwapLeaf struct {
	LeafID                       string `json:"leaf_id"`
	RawUnsignedRefundTransaction string `json:"raw_unsigned_refund_transaction"`
	AdaptorAddedSignature        string `json:"adaptor_added_signature"`
}

func (s *SparkServiceAPI) RequestLeavesSwap(
	adaptorPubkey string,
	totalAmountSats uint64,
	targetAmountSats uint64,
	feeSats uint64,
	network common.Network,
	userLeaves []SwapLeaf,
) (string, []SwapLeaf, error) {
	variables := map[string]interface{}{
		"adaptor_pubkey":     adaptorPubkey,
		"total_amount_sats":  totalAmountSats,
		"target_amount_sats": targetAmountSats,
		"fee_sats":           feeSats,
		"network":            strings.ToUpper(network.String()),
		"user_leaves":        userLeaves,
	}

	response, err := s.Requester.ExecuteGraphqlWithContext(context.Background(), RequestLeavesSwapMutation, variables)
	if err != nil {
		return "", nil, err
	}

	fmt.Printf("leaves swap request response: %v\n", response)

	request := response["request_leaves_swap"].(map[string]interface{})["request"].(map[string]interface{})["id"].(string)
	leavesJSON := response["request_leaves_swap"].(map[string]interface{})["request"].(map[string]interface{})["swap_leaves"].([]interface{})
	var leaves []SwapLeaf
	for _, leaf := range leavesJSON {
		leafMap := leaf.(map[string]interface{})
		leaves = append(leaves, SwapLeaf{
			LeafID:                       leafMap["leaf_id"].(string),
			RawUnsignedRefundTransaction: leafMap["raw_unsigned_refund_transaction"].(string),
			AdaptorAddedSignature:        leafMap["adaptor_signed_signature"].(string),
		})
	}
	return request, leaves, nil
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

func (s *SparkServiceAPI) InitiateCoopExit(
	leafExternalIDs []string,
	address string,
) (string, []byte, *wire.MsgTx, error) {
	variables := map[string]interface{}{
		"leaf_external_ids":  leafExternalIDs,
		"withdrawal_address": address,
	}

	response, err := s.Requester.ExecuteGraphqlWithContext(context.Background(), RequestCoopExitMutation, variables)
	if err != nil {
		return "", nil, nil, err
	}

	coopExitID := response["request_coop_exit"].(map[string]interface{})["request"].(map[string]interface{})["id"].(string)

	connectorTxString := response["request_coop_exit"].(map[string]interface{})["request"].(map[string]interface{})["raw_connector_transaction"].(string)
	log.Printf("connectorTxString: %s", connectorTxString)
	connectorTxBytes, err := hex.DecodeString(connectorTxString)
	if err != nil {
		return "", nil, nil, err
	}
	var connectorTx wire.MsgTx
	err = connectorTx.Deserialize(bytes.NewReader(connectorTxBytes))
	if err != nil {
		return "", nil, nil, err
	}
	coopExitTxid := connectorTx.TxIn[0].PreviousOutPoint.Hash[:]

	return coopExitID, coopExitTxid, &connectorTx, nil
}

func (s *SparkServiceAPI) CompleteCoopExit(
	userOutboundTransferExternalID string,
	coopExitRequestID string,
) (string, error) {
	variables := map[string]interface{}{
		"user_outbound_transfer_external_id": userOutboundTransferExternalID,
		"coop_exit_request_id":               coopExitRequestID,
	}

	response, err := s.Requester.ExecuteGraphqlWithContext(context.Background(), CompleteCoopExitMutation, variables)
	if err != nil {
		return "", err
	}

	requestID := response["complete_coop_exit"].(map[string]interface{})["request"].(map[string]interface{})["id"].(string)
	return requestID, nil
}
