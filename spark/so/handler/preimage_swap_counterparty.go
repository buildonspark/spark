package handler

import (
	"fmt"

	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	pbspark "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/ent"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
)

// A manifest is signed only by its senders, and on a receive the sender is the SSP — so nothing in
// it attests that the counterparty agreed to its own edge, however small that edge is.
func verifyCounterpartyManifestSignature(req *pbspark.InitiatePreimageSwapV4Request, counterparty keys.Public) error {
	signature := req.GetCounterpartyManifestSignature()
	manifest := req.GetTransferV3Request().GetTransferManifest()

	if len(signature) == 0 {
		if req.GetReason() == pbspark.InitiatePreimageSwapRequest_REASON_RECEIVE {
			return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("counterparty_manifest_signature is required"))
		}
		return nil
	}
	// A signature over nothing covers nothing, and whoever supplied it believes something was bound.
	if manifest == nil {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("counterparty_manifest_signature set without a transfer_manifest"))
	}

	manifestHash, err := common.HashTransferManifest(manifest)
	if err != nil {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("transfer manifest is not hashable: %w", err))
	}
	if err := common.VerifyECDSASignature(counterparty, signature, manifestHash); err != nil {
		return sparkerrors.InvalidArgumentMalformedField(
			fmt.Errorf("counterparty %x manifest signature is invalid: %w", counterparty.Serialize(), err))
	}
	return nil
}

// assertCounterpartyIsPaid guarantees the transfer routes strictly positive sats, summed from the
// locked rows, to the counterparty the swap names. Being named or routed a leaf is not being paid.
//
// Strictly positive is the ceiling here: the counterparty is owed the net, the invoice is the gross,
// and only the sender ever states the difference — so there is no reference value to check an amount
// against. The counterparty's signature over the manifest is what closes that gap.
func assertCounterpartyIsPaid(
	counterparty keys.Public,
	leafMap map[string]*ent.TreeNode,
	destinations leafDestinations,
	reason pbspark.InitiatePreimageSwapRequest_Reason,
) error {
	var counterpartySats uint64
	for leafID, leaf := range leafMap {
		destination, err := destinations.forLeaf(leafID)
		if err != nil {
			return err
		}
		if destination.Equals(counterparty) {
			counterpartySats += leaf.Value
		}
	}
	if counterpartySats == 0 {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf(
			"counterparty %x receives no value from this transfer (reason %s)", counterparty.Serialize(), reason))
	}
	return nil
}
