package handler

import (
	"fmt"

	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/ent"
)

// GetTransferSenderReceiver returns the single sender and single receiver identity pubkeys
// from a transfer's edges. The transfer must have been loaded with WithTransferSenders()
// and WithTransferReceivers(). For SIMO transfers there is exactly one sender and one receiver.
func GetTransferSenderReceiver(t *ent.Transfer) (sender, receiver keys.Public, err error) {
	if len(t.Edges.TransferSenders) != 1 {
		return keys.Public{}, keys.Public{}, fmt.Errorf("transfer %s has %d transfer senders, expected 1", t.ID, len(t.Edges.TransferSenders))
	}
	if len(t.Edges.TransferReceivers) != 1 {
		return keys.Public{}, keys.Public{}, fmt.Errorf("transfer %s has %d transfer receivers, expected 1", t.ID, len(t.Edges.TransferReceivers))
	}
	return t.Edges.TransferSenders[0].IdentityPubkey, t.Edges.TransferReceivers[0].IdentityPubkey, nil
}
