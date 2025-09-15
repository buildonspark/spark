package spark

import "fmt"

const (
	// DKGKeyCount is the number of keyshares to generate during the DKG.
	DKGKeyCount = 1000

	// InitialTimeLock is the initial time lock for the deposit.
	InitialTimeLock = 2000

	// TimeLockInterval is the interval between time locks.
	TimeLockInterval = 100

	// WatchtowerTimeLockBuffer is the buffer for the watchtower to wait before broadcasting the refund tx.
	WatchtowerTimeLockBuffer = 60

	// TokenMaxValidityDuration is the max duration a token transaction can be valid for
	TokenMaxValidityDuration = 300

	// SigningCommitmentReserve is the reserve for the signing commitments.
	SigningCommitmentReserve = 100000

	// SigningCommitmentBatchSize is the batch size for the signing commitments.
	SigningCommitmentBatchSize = 1000
)

var ZeroSequence = uint32(0)

func InitialSequence() uint32 {
	return uint32((1 << 30) | InitialTimeLock)
}

func NextSequence(currSequence uint32) (uint32, error) {
	if currSequence&0xFFFF-TimeLockInterval <= 0 {
		return 0, fmt.Errorf("timelock interval is less or equal to 0")
	}
	return (1 << 30) | (currSequence&0xFFFF - TimeLockInterval), nil
}
