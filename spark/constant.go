package spark

import "fmt"

const (
	// DKGKeyThreshold is the number of keyshares required to start the DKG.
	DKGKeyThreshold = 10000

	// DKGKeyCount is the number of keyshares to generate during the DKG.
	DKGKeyCount = 1000

	// InitialTimeLock is the initial time lock for the deposit.
	InitialTimeLock = 2000

	// TimeLockInterval is the interval between time locks.
	TimeLockInterval = 100
)

func InitialSequence() uint32 {
	return uint32((1 << 30) | InitialTimeLock)
}

func NextSequence(currSequence uint32) (uint32, error) {
	if currSequence&0xFFFF-TimeLockInterval <= 0 {
		return 0, fmt.Errorf("timelock interval is less or equal to 0")
	}
	return uint32((1 << 30) | (currSequence&0xFFFF - TimeLockInterval)), nil
}
