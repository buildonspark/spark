package tokens

import (
	"errors"
)

// Withdrawal validation errors allow callers to distinguish between different failure modes.
// Use fmt.Errorf with %w to wrap these sentinels with additional context while preserving
// errors.Is() compatibility.
var (
	// ErrOutputAlreadyWithdrawnInBlock indicates the output was already processed in this block.
	ErrOutputAlreadyWithdrawnInBlock = errors.New("output already withdrawn in this block")

	// ErrOutputNotFound indicates the token output was not found in the database.
	ErrOutputNotFound = errors.New("token output not found")

	// ErrOutputNotWithdrawable indicates the output's status doesn't allow withdrawal.
	ErrOutputNotWithdrawable = errors.New("output cannot be withdrawn")

	// ErrOutputAlreadyWithdrawnOnChain indicates the output already has a confirmed L1 withdrawal.
	ErrOutputAlreadyWithdrawnOnChain = errors.New("output already withdrawn on-chain")

	// ErrInsufficientBond indicates the withdrawal transaction has insufficient bond amount.
	ErrInsufficientBond = errors.New("insufficient bond")

	// ErrScriptMismatch indicates the withdrawal transaction script doesn't match expected.
	ErrScriptMismatch = errors.New("script mismatch")

	// ErrVoutOutOfRange indicates the bitcoin vout is out of range for the transaction.
	ErrVoutOutOfRange = errors.New("bitcoin vout out of range")
)
