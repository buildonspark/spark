package schematype

// PendingSendTransferStatus is the status of a pending send transfer.
type PendingSendTransferStatus string

const (
	// PendingSendTransferStatusPending is the status of a pending send transfer.
	PendingSendTransferStatusPending  PendingSendTransferStatus = "STARTED"
	PendingSendTransferStatusFinished PendingSendTransferStatus = "FINISHED"
)

// Values returns the values of the pending send transfer status.
func (PendingSendTransferStatus) Values() []string {
	return []string{
		string(PendingSendTransferStatusPending),
		string(PendingSendTransferStatusFinished),
	}
}
