package handler

import (
	"fmt"

	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/ent"
)

// GetTransferSender returns the single sender identity pubkey from a transfer's edges.
// The transfer must have been loaded with WithTransferSenders().
func GetTransferSender(t *ent.Transfer) (keys.Public, error) {
	if len(t.Edges.TransferSenders) != 1 {
		return keys.Public{}, fmt.Errorf("transfer %s has %d transfer senders, expected 1", t.ID, len(t.Edges.TransferSenders))
	}
	return t.Edges.TransferSenders[0].IdentityPubkey, nil
}

// GetTransferSenderReceiver returns the single sender and single receiver identity pubkeys
// from a transfer's edges. The transfer must have been loaded with WithTransferSenders()
// and WithTransferReceivers(). For SIMO transfers there is exactly one sender and one receiver.
func GetTransferSenderReceiver(t *ent.Transfer) (sender, receiver keys.Public, err error) {
	senderPK, err := GetTransferSender(t)
	if err != nil {
		return keys.Public{}, keys.Public{}, err
	}
	if len(t.Edges.TransferReceivers) != 1 {
		return keys.Public{}, keys.Public{}, fmt.Errorf("transfer %s has %d transfer receivers, expected 1", t.ID, len(t.Edges.TransferReceivers))
	}
	return senderPK, t.Edges.TransferReceivers[0].IdentityPubkey, nil
}
