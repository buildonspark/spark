package mimo_test

import (
	"testing"

	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/mimo"
	"github.com/stretchr/testify/assert"
)

func TestIsReceiverAxisTranslatable_ReceiverNamed(t *testing.T) {
	for _, s := range []st.TransferStatus{
		st.TransferStatusReceiverKeyTweaked,
		st.TransferStatusReceiverKeyTweakLocked,
		st.TransferStatusReceiverKeyTweakApplied,
		st.TransferStatusReceiverRefundSigned,
	} {
		assert.True(t, mimo.IsReceiverAxisTranslatable(s), "%s should be translatable", s)
	}
}

func TestIsReceiverAxisTranslatable_SenderNamed(t *testing.T) {
	for _, s := range []st.TransferStatus{
		st.TransferStatusSenderInitiated,
		st.TransferStatusSenderInitiatedCoordinator,
		st.TransferStatusApplyingSenderKeyTweak,
		st.TransferStatusSenderKeyTweakPending,
		st.TransferStatusSenderKeyTweaked,
	} {
		assert.True(t, mimo.IsReceiverAxisTranslatable(s), "%s should be translatable", s)
	}
}

func TestIsReceiverAxisTranslatable_Terminals(t *testing.T) {
	for _, s := range []st.TransferStatus{
		st.TransferStatusCompleted,
		st.TransferStatusExpired,
		st.TransferStatusReturned,
	} {
		assert.True(t, mimo.IsReceiverAxisTranslatable(s), "%s should be translatable", s)
	}
}

// TestIsReceiverAxisTranslatable_AllEnumValues locks the structural invariant
// that every TransferStatus enum value translates to a receiver-axis
// equivalent. queryByParticipantFallback's receiver arm returns a
// never-matches predicate when this is false, which would silently diverge
// from legacy queryTransfers for that shape. Iterates over Values() so a
// future status addition without a map entry breaks loudly instead of
// slipping through.
func TestIsReceiverAxisTranslatable_AllEnumValues(t *testing.T) {
	for _, v := range st.TransferStatus("").Values() {
		s := st.TransferStatus(v)
		assert.Truef(t, mimo.IsReceiverAxisTranslatable(s), "%s should be translatable", s)
	}
}

func TestReceiverArmFilters_PureReceiverNamed(t *testing.T) {
	indexSet, exactMatch, narrowing := mimo.ReceiverArmFilters([]st.TransferStatus{
		st.TransferStatusReceiverKeyTweaked,
		st.TransferStatusReceiverKeyTweakLocked,
		st.TransferStatusReceiverKeyTweakApplied,
		st.TransferStatusReceiverRefundSigned,
	})

	expected := []st.TransferReceiverStatus{
		st.TransferReceiverStatusKeyTweaked,
		st.TransferReceiverStatusKeyTweakLocked,
		st.TransferReceiverStatusKeyTweakApplied,
		st.TransferReceiverStatusRefundSigned,
	}
	assert.ElementsMatch(t, expected, indexSet)
	assert.ElementsMatch(t, expected, exactMatch)
	assert.Empty(t, narrowing)
}

func TestReceiverArmFilters_FullSenderPendingUmbrella(t *testing.T) {
	indexSet, exactMatch, narrowing := mimo.ReceiverArmFilters([]st.TransferStatus{
		st.TransferStatusSenderInitiated,
		st.TransferStatusSenderInitiatedCoordinator,
		st.TransferStatusApplyingSenderKeyTweak,
		st.TransferStatusSenderKeyTweakPending,
	})

	assert.ElementsMatch(t, []st.TransferReceiverStatus{st.TransferReceiverStatusInitiated}, indexSet)
	assert.Empty(t, exactMatch)
	assert.ElementsMatch(t, []st.TransferStatus{
		st.TransferStatusSenderInitiated,
		st.TransferStatusSenderInitiatedCoordinator,
		st.TransferStatusApplyingSenderKeyTweak,
		st.TransferStatusSenderKeyTweakPending,
	}, narrowing)
}

func TestReceiverArmFilters_SenderPendingPartialSubset(t *testing.T) {
	indexSet, exactMatch, narrowing := mimo.ReceiverArmFilters([]st.TransferStatus{
		st.TransferStatusSenderInitiated,
		st.TransferStatusSenderKeyTweakPending,
	})

	assert.ElementsMatch(t, []st.TransferReceiverStatus{st.TransferReceiverStatusInitiated}, indexSet)
	assert.Empty(t, exactMatch)
	assert.ElementsMatch(t, []st.TransferStatus{
		st.TransferStatusSenderInitiated,
		st.TransferStatusSenderKeyTweakPending,
	}, narrowing)
}

func TestReceiverArmFilters_SenderKeyTweakedIsPure(t *testing.T) {
	indexSet, exactMatch, narrowing := mimo.ReceiverArmFilters([]st.TransferStatus{
		st.TransferStatusSenderKeyTweaked,
	})

	assert.ElementsMatch(t, []st.TransferReceiverStatus{st.TransferReceiverStatusReceiverClaimPending}, indexSet)
	assert.ElementsMatch(t, []st.TransferReceiverStatus{st.TransferReceiverStatusReceiverClaimPending}, exactMatch)
	assert.Empty(t, narrowing)
}

func TestReceiverArmFilters_SingleTerminalCollapse(t *testing.T) {
	indexSet, exactMatch, narrowing := mimo.ReceiverArmFilters([]st.TransferStatus{
		st.TransferStatusExpired,
	})

	assert.ElementsMatch(t, []st.TransferReceiverStatus{st.TransferReceiverStatusCancelled}, indexSet)
	assert.Empty(t, exactMatch)
	assert.ElementsMatch(t, []st.TransferStatus{st.TransferStatusExpired}, narrowing)
}

func TestReceiverArmFilters_CompletedIsPure(t *testing.T) {
	indexSet, exactMatch, narrowing := mimo.ReceiverArmFilters([]st.TransferStatus{
		st.TransferStatusCompleted,
	})

	assert.ElementsMatch(t, []st.TransferReceiverStatus{st.TransferReceiverStatusCompleted}, indexSet)
	assert.ElementsMatch(t, []st.TransferReceiverStatus{st.TransferReceiverStatusCompleted}, exactMatch)
	assert.Empty(t, narrowing)
}

// Full ACTIVE_COUNTER_SWAP_STATUSES (GOB2 shape): 4 sender-pending umbrella
// (full → narrowing) + SENDER_KEY_TWEAKED (pure 1:1) + 4 RECEIVER_* (pure 1:1).
func TestReceiverArmFilters_ActiveCounterSwapStatuses(t *testing.T) {
	indexSet, exactMatch, narrowing := mimo.ReceiverArmFilters([]st.TransferStatus{
		st.TransferStatusSenderInitiated,
		st.TransferStatusSenderInitiatedCoordinator,
		st.TransferStatusApplyingSenderKeyTweak,
		st.TransferStatusSenderKeyTweakPending,
		st.TransferStatusSenderKeyTweaked,
		st.TransferStatusReceiverKeyTweakLocked,
		st.TransferStatusReceiverKeyTweakApplied,
		st.TransferStatusReceiverKeyTweaked,
		st.TransferStatusReceiverRefundSigned,
	})

	assert.ElementsMatch(t, []st.TransferReceiverStatus{
		st.TransferReceiverStatusInitiated,
		st.TransferReceiverStatusReceiverClaimPending,
		st.TransferReceiverStatusKeyTweakLocked,
		st.TransferReceiverStatusKeyTweakApplied,
		st.TransferReceiverStatusKeyTweaked,
		st.TransferReceiverStatusRefundSigned,
	}, indexSet)
	assert.ElementsMatch(t, []st.TransferReceiverStatus{
		st.TransferReceiverStatusReceiverClaimPending,
		st.TransferReceiverStatusKeyTweakLocked,
		st.TransferReceiverStatusKeyTweakApplied,
		st.TransferReceiverStatusKeyTweaked,
		st.TransferReceiverStatusRefundSigned,
	}, exactMatch)
	assert.ElementsMatch(t, []st.TransferStatus{
		st.TransferStatusSenderInitiated,
		st.TransferStatusSenderInitiatedCoordinator,
		st.TransferStatusApplyingSenderKeyTweak,
		st.TransferStatusSenderKeyTweakPending,
	}, narrowing)
}

func TestReceiverArmFilters_DedupesInput(t *testing.T) {
	indexSet, exactMatch, narrowing := mimo.ReceiverArmFilters([]st.TransferStatus{
		st.TransferStatusSenderInitiated,
		st.TransferStatusSenderInitiated,
		st.TransferStatusSenderInitiated,
	})

	assert.ElementsMatch(t, []st.TransferReceiverStatus{st.TransferReceiverStatusInitiated}, indexSet)
	assert.Empty(t, exactMatch)
	assert.ElementsMatch(t, []st.TransferStatus{st.TransferStatusSenderInitiated}, narrowing)
}

func TestReceiverArmFilters_Empty(t *testing.T) {
	indexSet, exactMatch, narrowing := mimo.ReceiverArmFilters(nil)
	assert.Nil(t, indexSet)
	assert.Nil(t, exactMatch)
	assert.Nil(t, narrowing)
}

func TestReceiverStatusStrings_Dedupes(t *testing.T) {
	got := mimo.ReceiverStatusStrings([]st.TransferReceiverStatus{
		st.TransferReceiverStatusCancelled,
		st.TransferReceiverStatusCancelled,
		st.TransferReceiverStatusInitiated,
	})
	assert.ElementsMatch(t, []string{"CANCELLED", "INITIATED"}, got)
}

func TestTransferStatusStrings_Dedupes(t *testing.T) {
	got := mimo.TransferStatusStrings([]st.TransferStatus{
		st.TransferStatusSenderInitiated,
		st.TransferStatusSenderInitiated,
		st.TransferStatusSenderKeyTweaked,
	})
	assert.ElementsMatch(t, []string{"SENDER_INITIATED", "SENDER_KEY_TWEAKED"}, got)
}
