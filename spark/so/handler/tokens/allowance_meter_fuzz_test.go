package tokens

import (
	"bytes"
	"math/big"
	"testing"

	"github.com/lightsparkdev/spark/common/keys"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/stretchr/testify/require"
)

// clampMeterBytes bounds a fuzzed byte input to at most n bytes so the
// interesting range (including just-past-uint128 values) stays reachable
// without wasting the corpus on huge inputs.
func clampMeterBytes(b []byte, n int) []byte {
	if len(b) > n {
		return b[:n]
	}
	return b
}

// FuzzApplyAllowanceCeilings fuzzes the uint128 big-endian metering arithmetic
// of the token allowance engine: the add against the running meter, the
// compare against per_transaction_cap and total_limit, and the saturating
// owner-signed-unlimited path. Invariants:
//
//   - never panics (in particular, FillBytes can never be reached with a
//     value wider than the 16-byte column);
//   - a successful result is exactly 16 bytes and decodes to spent+metered
//     (monotonic accumulation), except on the unlimited path where it
//     saturates at 2^128-1;
//   - the ceilings hold: success with a bounded per-transaction cap implies
//     metered <= cap, success with a bounded total implies the new meter is
//     <= total_limit, and equality is what flips the allowance EXHAUSTED;
//   - the unlimited flags waive exactly their own ceiling and nothing else.
func FuzzApplyAllowanceCeilings(f *testing.F) {
	u128Max := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 128), big.NewInt(1))

	// Frozen-vector-shaped caps plus every boundary the arithmetic has:
	// exact-limit exhaustion, one-over rejection, uint128 saturation, and the
	// out-of-range corruption guard.
	f.Add(u128(10_000), u128(100_000), u128(0), u128(100), false, false)
	f.Add(u128(100), u128(100), u128(40), u128(60), false, false)                 // lands exactly on the limit
	f.Add(u128(100), u128(100), u128(41), u128(60), false, false)                 // one over the limit
	f.Add(u128(0), u128(0), u128(0), u128(0), true, true)                         // fully unlimited, zero caps
	f.Add(u128(0), u128(0), u128Max.Bytes(), u128(1), true, true)                 // saturation
	f.Add(u128(1), u128(100_000), u128(0), u128(2), false, false)                 // per-tx cap rejection
	f.Add(append(u128Max.Bytes(), 0xff), u128(1), u128(0), u128(1), false, false) // >16-byte cap bytes
	f.Add(u128(10), u128(100), []byte{}, []byte{}, false, false)

	f.Fuzz(func(t *testing.T, capBytes, limitBytes, spentBytes, meteredBytes []byte, perTxUnlimited, totalUnlimited bool) {
		allowance := &ent.TokenAllowance{
			PerTransactionCap:       clampMeterBytes(capBytes, 16),
			TotalLimit:              clampMeterBytes(limitBytes, 16),
			PerTransactionUnlimited: perTxUnlimited,
			TotalUnlimited:          totalUnlimited,
		}
		// Allow spent/metered wider than 16 bytes so the out-of-range guard is
		// exercised; the columns themselves are always 16 bytes in production.
		spent := new(big.Int).SetBytes(clampMeterBytes(spentBytes, 20))
		metered := new(big.Int).SetBytes(clampMeterBytes(meteredBytes, 20))
		perTxCap := new(big.Int).SetBytes(allowance.PerTransactionCap)
		totalLimit := new(big.Int).SetBytes(allowance.TotalLimit)

		newSpentBytes, newStatus, appliedBytes, err := applyAllowanceCeilings(allowance, spent, metered)

		// Determinism: same inputs, same outcome.
		newSpentBytes2, newStatus2, appliedBytes2, err2 := applyAllowanceCeilings(allowance, spent, metered)
		require.Equal(t, err == nil, err2 == nil)
		require.True(t, bytes.Equal(newSpentBytes, newSpentBytes2))
		require.True(t, bytes.Equal(appliedBytes, appliedBytes2))
		require.Equal(t, newStatus, newStatus2)

		outOfRange := spent.Cmp(u128Max) > 0 || metered.Cmp(u128Max) > 0

		if err != nil {
			// A rejection must correspond to exactly a rejectable condition.
			overPerTx := !perTxUnlimited && metered.Cmp(perTxCap) > 0
			overTotal := !totalUnlimited && new(big.Int).Add(spent, metered).Cmp(totalLimit) > 0
			require.True(t, outOfRange || overPerTx || overTotal,
				"rejected a spend no ceiling forbids: spent=%s metered=%s cap=%s limit=%s", spent, metered, perTxCap, totalLimit)
			return
		}

		require.False(t, outOfRange, "out-of-range meters must fail closed")
		require.Len(t, newSpentBytes, 16, "the meter must never overflow the 16-byte column")

		newSpent := new(big.Int).SetBytes(newSpentBytes)
		require.GreaterOrEqual(t, newSpent.Cmp(spent), 0, "accumulation must be monotonic")

		exact := new(big.Int).Add(spent, metered)
		if totalUnlimited && exact.Cmp(u128Max) > 0 {
			require.Zero(t, newSpent.Cmp(u128Max), "unlimited path must saturate at 2^128-1")
		} else {
			require.Zero(t, newSpent.Cmp(exact), "the meter must advance by exactly the metered amount")
		}

		// Reserve/release symmetry: releasing this spend subtracts the applied
		// amount, which must restore the meter to exactly its pre-spend value.
		require.Len(t, appliedBytes, 16, "the applied amount shares the 16-byte column width")
		applied := new(big.Int).SetBytes(appliedBytes)
		require.LessOrEqual(t, applied.Cmp(metered), 0, "never meter more than the transaction settled")
		require.Zero(t, new(big.Int).Sub(newSpent, applied).Cmp(spent),
			"releasing the reserve must restore the pre-spend meter")

		if !perTxUnlimited {
			require.LessOrEqual(t, metered.Cmp(perTxCap), 0, "bounded per-transaction cap must hold")
		}
		if totalUnlimited {
			require.Equal(t, st.TokenAllowanceStatusActive, newStatus, "an unlimited total never exhausts")
		} else {
			require.LessOrEqual(t, newSpent.Cmp(totalLimit), 0, "bounded total limit must hold")
			if newSpent.Cmp(totalLimit) == 0 {
				require.Equal(t, st.TokenAllowanceStatusExhausted, newStatus)
			} else {
				require.Equal(t, st.TokenAllowanceStatusActive, newStatus)
			}
		}
	})
}

// TestApplyAllowanceCeilings_SaturatedReserveReleasesExactlyWhatItAdded pins
// the reserve/release symmetry the meter depends on: a release subtracts the
// amount the reserve recorded on the spend row, so that amount must be what
// the meter actually advanced by. On the saturating unlimited path the meter
// advances by less than the metered value, and recording the metered value
// instead would drive the meter below its pre-spend reading on release.
func TestApplyAllowanceCeilings_SaturatedReserveReleasesExactlyWhatItAdded(t *testing.T) {
	u128Max := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 128), big.NewInt(1))
	allowance := &ent.TokenAllowance{
		PerTransactionUnlimited: true,
		TotalUnlimited:          true,
	}

	spent := new(big.Int).Sub(u128Max, big.NewInt(5))
	metered := big.NewInt(10)

	newSpentBytes, _, applied, err := applyAllowanceCeilings(allowance, spent, metered)
	require.NoError(t, err)
	require.Zero(t, new(big.Int).SetBytes(newSpentBytes).Cmp(u128Max), "the unlimited path saturates")

	released := new(big.Int).Sub(new(big.Int).SetBytes(newSpentBytes), new(big.Int).SetBytes(applied))
	require.Zero(t, released.Cmp(spent),
		"releasing a saturated reserve must restore the pre-spend meter, got %s want %s", released, spent)
}

// FuzzMeterAllowanceOutputs fuzzes the output-classification half of the
// metering (what feeds applyAllowanceCeilings): change back to the owner is
// free, and every other output is settled value metered against the budget.
// Invariants: no panic on arbitrary amount bytes, and the metered value equals
// the settled sum of the non-owner outputs.
func FuzzMeterAllowanceOutputs(f *testing.F) {
	f.Add([]byte{0x27, 0x10}, []byte{0x01, 0xf4}, []byte{0x00}, uint8(0), uint8(1), uint8(2))
	f.Add([]byte{0xff}, []byte{0x00}, []byte{0x10}, uint8(2), uint8(2), uint8(2))

	ownerKey := keys.GeneratePrivateKey().Public()
	recipientKey := keys.GeneratePrivateKey().Public()
	recipientKey2 := keys.GeneratePrivateKey().Public()
	routes := []keys.Public{ownerKey, recipientKey, recipientKey2}

	f.Fuzz(func(t *testing.T, amount0, amount1, amount2 []byte, route0, route1, route2 uint8) {
		amounts := [][]byte{clampMeterBytes(amount0, 16), clampMeterBytes(amount1, 16), clampMeterBytes(amount2, 16)}
		routeChoices := []uint8{route0, route1, route2}

		allowance := &ent.TokenAllowance{
			OwnerPublicKey: ownerKey,
		}

		outputs := make([]*tokenpb.TokenOutput, len(amounts))
		expectedSettled := new(big.Int)
		for i, amountBytes := range amounts {
			route := routes[routeChoices[i]%3]
			outputs[i] = &tokenpb.TokenOutput{
				OwnerPublicKey: route.Serialize(),
				TokenAmount:    amountBytes,
			}
			if route.Equals(ownerKey) {
				// Change back to the owner is never metered.
				continue
			}
			expectedSettled.Add(expectedSettled, new(big.Int).SetBytes(amountBytes))
		}
		txProto := &tokenpb.TokenTransaction{TokenOutputs: outputs}

		metered, err := meterAllowanceOutputs(txProto, allowance)
		require.NoError(t, err, "metering with no allowlist must never reject")
		require.Zero(t, metered.Cmp(expectedSettled),
			"metered must equal the settled sum, with owner change free")
	})
}
