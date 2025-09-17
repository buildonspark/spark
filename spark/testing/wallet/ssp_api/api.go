package sspapi

import (
	"bytes"
	"context"
	"encoding/hex"
	"log"
	"slices"
	"strings"

	"github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
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
	variables := map[string]any{
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

	request := response["request_lightning_receive"].(map[string]any)["request"].(map[string]any)
	encodedInvoice := request["invoice"].(map[string]any)["encoded_envoice"].(string)

	fees := request["fee"].(map[string]any)["original_value"].(float64)

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
	variables := map[string]any{
		"encoded_invoice": invoice,
		"idempotency_key": idempotencyKey,
	}

	response, err := s.Requester.ExecuteGraphqlWithContext(context.Background(), RequestLightningSendMutation, variables)
	if err != nil {
		return "", err
	}

	request := response["request_lightning_send"].(map[string]any)["request"].(map[string]any)
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
	userLeaves []SwapLeaf,
) (string, []SwapLeaf, error) {
	variables := map[string]any{
		"adaptor_pubkey":     adaptorPubkey,
		"total_amount_sats":  totalAmountSats,
		"target_amount_sats": targetAmountSats,
		"fee_sats":           feeSats,
		"user_leaves":        userLeaves,
	}

	response, err := s.Requester.ExecuteGraphqlWithContext(context.Background(), RequestLeavesSwapMutation, variables)
	if err != nil {
		return "", nil, err
	}

	request := response["request_leaves_swap"].(map[string]any)["request"].(map[string]any)["id"].(string)
	leavesJSON := response["request_leaves_swap"].(map[string]any)["request"].(map[string]any)["swap_leaves"].([]any)
	var leaves []SwapLeaf
	for _, leaf := range leavesJSON {
		leafMap := leaf.(map[string]any)
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
	variables := map[string]any{
		"adaptor_secret_key":                 adaptorSecretKey,
		"user_outbound_transfer_external_id": userOutboundTransferExternalID,
		"leaves_swap_request_id":             leavesSwapRequestID,
	}

	response, err := s.Requester.ExecuteGraphqlWithContext(context.Background(), CompleteLeavesSwapMutation, variables)
	if err != nil {
		return "", err
	}

	request := response["complete_leaves_swap"].(map[string]any)["request"].(map[string]any)["id"].(string)
	return request, nil
}

func (s *SparkServiceAPI) InitiateCoopExit(
	leafExternalIDs []string,
	address string,
) (string, []byte, *wire.MsgTx, error) {
	variables := map[string]any{
		"leaf_external_ids":  leafExternalIDs,
		"withdrawal_address": address,
		"idempotency_key":    uuid.New().String(),
	}

	response, err := s.Requester.ExecuteGraphqlWithContext(context.Background(), RequestCoopExitMutation, variables)
	if err != nil {
		return "", nil, nil, err
	}

	coopExitID := response["request_coop_exit"].(map[string]any)["request"].(map[string]any)["id"].(string)

	connectorTxString := response["request_coop_exit"].(map[string]any)["request"].(map[string]any)["raw_connector_transaction"].(string)
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
	slices.Reverse(coopExitTxid)

	return coopExitID, coopExitTxid, &connectorTx, nil
}

func (s *SparkServiceAPI) CompleteCoopExit(
	userOutboundTransferExternalID string,
	coopExitRequestID string,
) (string, error) {
	variables := map[string]any{
		"user_outbound_transfer_external_id": userOutboundTransferExternalID,
		"coop_exit_request_id":               coopExitRequestID,
	}

	response, err := s.Requester.ExecuteGraphqlWithContext(context.Background(), CompleteCoopExitMutation, variables)
	if err != nil {
		return "", err
	}

	requestID := response["complete_coop_exit"].(map[string]any)["request"].(map[string]any)["id"].(string)
	return requestID, nil
}

func (s *SparkServiceAPI) FetchPublicKeyByPhoneNumber(phoneNumber string) (string, error) {
	variables := map[string]any{
		"phone_number": phoneNumber,
	}

	response, err := s.Requester.ExecuteGraphqlWithContext(context.Background(), WalletUserIdentityPublicKeyMutation, variables)
	if err != nil {
		return "", err
	}

	return response["wallet_user_identity_public_key"].(map[string]any)["identity_public_key"].(string), nil
}

func (s *SparkServiceAPI) StartReleaseSeed(phoneNumber string) error {
	variables := map[string]any{
		"phone_number": phoneNumber,
	}

	_, err := s.Requester.ExecuteGraphqlWithContext(context.Background(), StartReleaseSeedMutation, variables)
	if err != nil {
		return err
	}

	return nil
}

func (s *SparkServiceAPI) CompleteReleaseSeed(phoneNumber string, code string) ([]byte, error) {
	variables := map[string]any{
		"phone_number": phoneNumber,
		"code":         code,
	}

	response, err := s.Requester.ExecuteGraphqlWithContext(context.Background(), CompleteReleaseSeedMutation, variables)
	if err != nil {
		return nil, err
	}

	seed := response["complete_seed_release"].(map[string]any)["seed"].(string)
	seedBytes, err := hex.DecodeString(seed)
	if err != nil {
		return nil, err
	}

	return seedBytes, nil
}

func (s *SparkServiceAPI) NotifyReceiverTransfer(phoneNumber string, amountSats uint64) error {
	variables := map[string]any{
		"phone_number": phoneNumber,
		"amount_sats":  amountSats,
	}

	_, err := s.Requester.ExecuteGraphqlWithContext(context.Background(), NotifyReceiverTransferMutation, variables)
	if err != nil {
		return err
	}

	return nil
}
