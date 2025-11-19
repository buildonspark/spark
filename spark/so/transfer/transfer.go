package transfer

import (
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
)

// Maps the transfer status to a boolean indicating if the transfer is irrevokably sent.
func IsTransferSent(transfer *ent.Transfer) bool {
	switch transfer.Status {
	case st.TransferStatusSenderKeyTweaked,
		st.TransferStatusReceiverKeyTweaked,
		st.TransferStatusReceiverKeyTweakLocked,
		st.TransferStatusReceiverKeyTweakApplied,
		st.TransferStatusReceiverRefundSigned,
		st.TransferStatusCompleted:
		return true
	default:
		return false
	}
}
