package handler

import (
	"testing"

	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/stretchr/testify/assert"
)

// TestIsProvidePreimageCommittableStatus pins the committable-status set that
// Prepare fails closed on. It enumerates every TransferStatus so adding a new
// status to the enum forces a deliberate decision here rather than silently
// landing in the non-committable bucket (or, worse, the committable one).
//
// Committable == the pre-commit states where the staged sender key tweaks can
// still be applied (matches commitSenderKeyTweaks' accepted set). Everything
// else — SenderInitiated (never staged), already-committed, and
// terminal-cancelled — is non-committable: Prepare must refuse to persist the
// preimage for those.
func TestIsProvidePreimageCommittableStatus(t *testing.T) {
	committable := map[st.TransferStatus]bool{
		st.TransferStatusSenderInitiatedCoordinator: true,
		st.TransferStatusSenderKeyTweakPending:      true,
		st.TransferStatusApplyingSenderKeyTweak:     true,

		st.TransferStatusSenderInitiated:         false,
		st.TransferStatusSenderKeyTweaked:        false,
		st.TransferStatusReceiverKeyTweaked:      false,
		st.TransferStatusReceiverKeyTweakLocked:  false,
		st.TransferStatusReceiverKeyTweakApplied: false,
		st.TransferStatusReceiverRefundSigned:    false,
		st.TransferStatusCompleted:               false,
		st.TransferStatusExpired:                 false,
		st.TransferStatusReturned:                false,
	}

	// Guard: the table must cover the full enum. If a new status is added to
	// TransferStatus.Values() without a row here, this fails.
	var enumValues st.TransferStatus
	for _, v := range enumValues.Values() {
		status := st.TransferStatus(v)
		if _, ok := committable[status]; !ok {
			t.Fatalf("TransferStatus %q is not covered by this test — add it to the committable map with the intended classification", v)
		}
	}

	for status, want := range committable {
		assert.Equalf(t, want, isProvidePreimageCommittableStatus(status),
			"isProvidePreimageCommittableStatus(%s)", status)
	}
}
