package uint128

import (
	"database/sql/driver"
	"fmt"
	"math/big"

	"github.com/lightsparkdev/spark/so/errors"
)

var MaxUint128 = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 128), big.NewInt(1))

type Uint128 struct{ value *big.Int }

func New() Uint128 { return Uint128{value: new(big.Int)} }

func (u *Uint128) Validate() error {
	if u.value == nil || u.value.Sign() < 0 || u.value.Cmp(MaxUint128) > 0 {
		return errors.InvalidArgumentOutOfRange(fmt.Errorf("uint128 out of range"))
	}
	return nil
}

func (u *Uint128) Scan(src any) error {
	switch srcType := src.(type) {
	case nil: // remove after backfill
		if u.value == nil {
			u.value = new(big.Int)
		}
		u.value.SetUint64(0)
		return nil
	case []byte:
		val, ok := new(big.Int).SetString(string(srcType), 10)
		if !ok {
			return errors.InternalTypeConversionError(fmt.Errorf("invalid numeric when scanning bytes: %q", string(srcType)))
		}
		if val.Sign() < 0 || val.Cmp(MaxUint128) > 0 {
			return errors.InvalidArgumentOutOfRange(fmt.Errorf("uint128 out of range"))
		}
		u.value = val
		return nil
	case string:
		val, ok := new(big.Int).SetString(srcType, 10)
		if !ok {
			return errors.InternalTypeConversionError(fmt.Errorf("invalid numeric when scanning string: %q", srcType))
		}
		if val.Sign() < 0 || val.Cmp(MaxUint128) > 0 {
			return errors.InvalidArgumentOutOfRange(fmt.Errorf("uint128 out of range"))
		}
		u.value = val
		return nil
	default:
		return errors.InternalTypeConversionError(fmt.Errorf("unsupported src %T", src))
	}
}

func (u Uint128) Value() (driver.Value, error) {
	if err := u.Validate(); err != nil {
		return nil, err
	}
	return u.value.String(), nil
}

func (u *Uint128) SafeSetBytes(b []byte) error {
	if len(b) != 16 {
		return errors.InvalidArgumentOutOfRange(fmt.Errorf("uint128 must be 16 bytes"))
	}
	c := new(big.Int).SetBytes(b)
	if c.Sign() < 0 || c.Cmp(MaxUint128) > 0 {
		return errors.InvalidArgumentOutOfRange(fmt.Errorf("uint128 out of range"))
	}
	if u.value == nil {
		u.value = new(big.Int)
	}
	u.value.SetBytes(b)
	return nil
}

func (u Uint128) String() string {
	if u.value == nil {
		return "0"
	}
	return u.value.String()
}

func (u Uint128) BigInt() *big.Int {
	if u.value == nil {
		return new(big.Int)
	}
	return new(big.Int).Set(u.value)
}
