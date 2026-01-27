package tokens

import (
	"errors"
)

var (
	ErrOutputAlreadyWithdrawnInBlock = errors.New("output already withdrawn in this block")
	ErrOutputNotFound                = errors.New("token output not found")
	ErrOutputNotWithdrawable         = errors.New("output cannot be withdrawn")
	ErrOutputAlreadyWithdrawnOnChain = errors.New("output already withdrawn on-chain")
	ErrInsufficientBond              = errors.New("insufficient bond")
	ErrScriptMismatch                = errors.New("script mismatch")
	ErrVoutOutOfRange                = errors.New("bitcoin vout out of range")
)
