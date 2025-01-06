package spark

const (
	// DKGKeyThreshold is the number of keyshares required to start the DKG.
	DKGKeyThreshold = 300

	// DKGKeyCount is the number of keyshares to generate during the DKG.
	DKGKeyCount = 500

	// InitialTimeLock is the initial time lock for the deposit.
	InitialTimeLock = 200

	// TimeLockInterval is the interval between time locks.
	TimeLockInterval = 10
)
