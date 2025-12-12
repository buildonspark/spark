package uint128

import (
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"math/bits"
	"strconv"

	"entgo.io/ent/schema/field"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
)

// Uint128 represents an 128-bit unsigned integer.
type Uint128 struct {
	lo, hi uint64
}

// New creates a new Uint128 initialized to zero.
func New() Uint128 { return Uint128{} }

// FromUint64 creates a Uint128 from a uint64 value.
func FromUint64(b uint64) Uint128 {
	return Uint128{lo: b}
}

// FromBytes creates a Uint128 from a 16-byte slice in big-endian format.
// It returns an error if the byte slice is not exactly 16 bytes long.
func FromBytes(b []byte) (Uint128, error) {
	if len(b) != 16 {
		return Uint128{}, sparkerrors.InvalidArgumentOutOfRange(errors.New("uint128 must be 16 bytes"))
	}
	return Uint128{
		lo: binary.BigEndian.Uint64(b[8:]),
		hi: binary.BigEndian.Uint64(b[:8]),
	}, nil
}

// FromBigInt creates a Uint128 from a big.Int value.
// It returns an error if the value is negative, nil, or exceeds 128 bits.
func FromBigInt(b *big.Int) (Uint128, error) {
	u := Uint128{}
	if err := u.fillFromBigInt(b); err != nil {
		return Uint128{}, err
	}
	return u, nil
}

// Low returns the low 64 bits of this Uint128.
func (u Uint128) Low() uint64 {
	return u.lo
}

// High returns the high 64 bits of this Uint128.
func (u Uint128) High() uint64 {
	return u.hi
}

// IsZero returns true if this value is 0, and false otherwise.
func (u Uint128) IsZero() bool {
	return u == Uint128{}
}

// Cmp compares u and v and returns:
//
//	-1 if u < v
//	 0 if u == v
//	+1 if u > v
func (u Uint128) Cmp(v Uint128) int {
	switch {
	case u == v:
		return 0
	case u.hi < v.hi || (u.hi == v.hi && u.lo < v.lo):
		return -1
	default:
		return 1
	}
}

var _ field.ValueScanner = &Uint128{} // Ensure that Uint128 implements [field.ValueScanner].

// Scan implements the sql.Scanner interface, allowing Uint128 to be read from a database.
// It accepts nil, string, []byte, and *sql.Null[[]byte] types as input.
// Numeric strings are parsed as base-10 integers.
func (u *Uint128) Scan(src any) error {
	switch srcType := src.(type) {
	case nil:
		u.lo, u.hi = 0, 0
		return nil
	case *sql.Null[[]byte]:
		if srcType == nil || !srcType.Valid { // It can be a nil pointer to a Null, or just a null Null.
			u.lo, u.hi = 0, 0
			return nil
		}
		return u.parseString(string(srcType.V))
	case []byte:
		return u.parseString(string(srcType))
	case string:
		return u.parseString(srcType)
	default:
		return sparkerrors.InternalTypeConversionError(fmt.Errorf("unsupported src %T", src))
	}
}

// parseString parses a base-10 numeric string and fills this Uint128 with the parsed value.
// Returns an error if the string is not a valid base-10 integer or is out of range.
func (u *Uint128) parseString(src string) error {
	val, ok := new(big.Int).SetString(src, 10)
	if !ok {
		return sparkerrors.InternalTypeConversionError(fmt.Errorf("invalid numeric when scanning: %q", src))
	}
	return u.fillFromBigInt(val)

}

// fillFromBigInt fills this Uint128 with the value from a big.Int.
// It returns an error if val is nil, negative, or exceeds 128 bits.
func (u *Uint128) fillFromBigInt(val *big.Int) error {
	if val == nil || val.Sign() < 0 || val.BitLen() > 128 {
		return sparkerrors.InvalidArgumentOutOfRange(errors.New("uint128 out of range"))
	}

	u.lo = val.Uint64()
	u.hi = new(big.Int).Rsh(val, 64).Uint64()
	return nil
}

// Value implements the driver.Valuer interface, allowing Uint128 to be written to a database.
// It returns a base-10 string representation of the Uint128, and never returns an error.
func (u Uint128) Value() (driver.Value, error) {
	return u.String(), nil
}

// Bytes returns the 16-byte big-endian representation of this Uint128.
func (u Uint128) Bytes() []byte {
	out := make([]byte, 16)
	binary.BigEndian.PutUint64(out[8:], u.lo)
	binary.BigEndian.PutUint64(out[:8], u.hi)
	return out
}

// BigInt converts this Uint128 to a *big.Int.
func (u Uint128) BigInt() *big.Int {
	if u.IsZero() {
		return new(big.Int)
	}
	hi := new(big.Int).SetUint64(u.hi)
	lo := new(big.Int).SetUint64(u.lo)
	out := new(big.Int).Lsh(hi, 64)
	return out.Xor(out, lo)
}

// String returns the base-10 representation of u as a string.
// This implementation is relatively optimized, because, in the database, Uint128s are stored as strings, so this is
// potentially very hot code.
func (u Uint128) String() string {
	const (
		max64BitPow10         = 1e19
		max64BitPow10Exponent = 19
		hiWordStartDigit      = 39 // 2^128-1 has 39 decimal digits, so 39's the highest index
		loWordStartDigit      = hiWordStartDigit - max64BitPow10Exponent
	)

	if u.IsZero() {
		return "0"
	}
	if u.hi == 0 {
		return strconv.FormatUint(u.lo, 10)
	}

	buf := []byte("000000000000000000000000000000000000000") // 2^128-1 has 39 decimal digits, so we'll need at most that much space.

	// Extract the high digits
	q, r := u.div(max64BitPow10)
	if n := fillReverse(hiWordStartDigit, r, buf); q.IsZero() {
		return string(buf[hiWordStartDigit-n:])
	}

	// Now the low ones
	q, r = q.div(max64BitPow10)
	if n := fillReverse(loWordStartDigit, r, buf); q.lo == 0 {
		return string(buf[loWordStartDigit-n:])
	}
	buf[0] += byte(q.lo % 10)
	return string(buf)
}

// fillReverse fills a buffer with decimal digits in reverse order.
// It writes the decimal representation of r into buf starting at offset and moving backward.
// It returns the number of digits written.
func fillReverse(offset int, r uint64, buf []byte) int {
	n := 0
	for ; r != 0; r /= 10 {
		n++
		buf[offset-n] += byte(r % 10)
	}
	return n
}

// div divides u by a uint64 dividend and returns the quotient and remainder.
// This is a helper function for efficient division during string conversion.
func (u Uint128) div(dividend uint64) (Uint128, uint64) {
	if u.hi < dividend {
		lo, rem := bits.Div64(u.hi, u.lo, dividend)
		return Uint128{lo: lo, hi: 0}, rem
	}
	hi, rem := bits.Div64(0, u.hi, dividend)
	lo, rem := bits.Div64(rem, u.lo, dividend)
	return Uint128{lo: lo, hi: hi}, rem
}
