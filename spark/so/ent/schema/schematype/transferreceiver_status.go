package schematype

type TransferReceiverStatus string

const (
	// TransferReceiverStatusSenderInitiated is the status of a transfer receiver that has been initiated.
	TransferReceiverStatusSenderInitiated TransferReceiverStatus = "INITIATED"
	// TransferReceiverStatusPendingReceiverClaim is the status of transfer receiver awaiting claim from the receiver themselves.
	TransferReceiverStatusPendingReceiverClaim TransferReceiverStatus = "PENDING_RECEIVER_CLAIM"
	// TransferReceiverStatusKeyTweaked is the status of transfer receiver where key has been tweaked for the receiver.
	TransferReceiverStatusKeyTweaked TransferReceiverStatus = "RECEIVER_KEY_TWEAKED"
	// TransferReceiverStatusKeyTweakLocked is the status of transfer receiver where key has been tweaked and locked for update.
	TransferReceiverStatusKeyTweakLocked TransferReceiverStatus = "RECEIVER_KEY_TWEAK_LOCKED"
	// TransferReceiverStatusKeyTweakApplied is the status of transfer receiver where key has been tweaked and applied for the receiver.
	TransferReceiverStatusKeyTweakApplied TransferReceiverStatus = "RECEIVER_KEY_TWEAK_APPLIED"
	// TransferReceiverStatusRefundSigned is the status of transfer receiver where refund transaction has been signed.
	TransferReceiverStatusRefundSigned TransferReceiverStatus = "RECEIVER_REFUND_SIGNED"
	// TransferReceiverStatusCompleted is the status of transfer receiver that has completed the claim process.
	TransferReceiverStatusCompleted TransferReceiverStatus = "COMPLETED"
	// TransferReceiverStatusCancelled is the status of transfer receiver that may never claim due to incomplete sender states.
	TransferReceiverStatusCancelled TransferReceiverStatus = "CANCELLED"
)

func (TransferReceiverStatus) Values() []string {
	return []string{
		string(TransferReceiverStatusSenderInitiated),
		string(TransferReceiverStatusPendingReceiverClaim),
		string(TransferReceiverStatusKeyTweaked),
		string(TransferReceiverStatusKeyTweakLocked),
		string(TransferReceiverStatusKeyTweakApplied),
		string(TransferReceiverStatusRefundSigned),
		string(TransferReceiverStatusCompleted),
		string(TransferReceiverStatusCancelled),
	}
}
