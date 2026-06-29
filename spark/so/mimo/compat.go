package mimo

import (
	"fmt"

	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/ent"
)

// GetSingleTransferSender returns the transfer's sole sender from the
// (eager-loaded) TransferSenders edge, erroring unless exactly one is present.
// For multi-sender, see SP-2784.
func GetSingleTransferSender(t *ent.Transfer) (keys.Public, error) {
	if len(t.Edges.TransferSenders) != 1 {
		return keys.Public{}, fmt.Errorf("transfer %s has %d transfer senders, expected 1", t.ID, len(t.Edges.TransferSenders))
	}
	return t.Edges.TransferSenders[0].IdentityPubkey, nil
}

// GetSingleTransferSenderReceiver returns the transfer's sole sender and
// receiver from the (eager-loaded) TransferSenders and TransferReceivers edges,
// erroring unless exactly one of each is present. For multi-sender, see SP-2784.
func GetSingleTransferSenderReceiver(t *ent.Transfer) (sender, receiver keys.Public, err error) {
	senderPK, err := GetSingleTransferSender(t)
	if err != nil {
		return keys.Public{}, keys.Public{}, err
	}
	if len(t.Edges.TransferReceivers) != 1 {
		return keys.Public{}, keys.Public{}, fmt.Errorf("transfer %s has %d transfer receivers, expected 1", t.ID, len(t.Edges.TransferReceivers))
	}
	return senderPK, t.Edges.TransferReceivers[0].IdentityPubkey, nil
}
