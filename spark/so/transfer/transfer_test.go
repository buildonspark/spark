//go:build lightspark

package transfer

import (
	"testing"

	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/stretchr/testify/assert"
)

// TestMapTransferToReceiverStatus pins the dual-write contract used by the
// receiver-status mismatch monitor and the SSP sync-transfer recovery path.
// SENDER_KEY_TWEAKED maps to RECEIVER_CLAIM_PENDING (not INITIATED) — this
// is the load-bearing invariant that the new state machine relies on.
func TestMapTransferToReceiverStatus(t *testing.T) {
	cases := []struct {
		transferStatus         st.TransferStatus
		expectedReceiverStatus st.TransferReceiverStatus
	}{
		// Pre-tweak transfer states map to INITIATED on the receiver side
		// (sender hasn't completed key-tweak handoff yet).
		{st.TransferStatusSenderInitiated, st.TransferReceiverStatusInitiated},
		{st.TransferStatusSenderInitiatedCoordinator, st.TransferReceiverStatusInitiated},
		{st.TransferStatusSenderKeyTweakPending, st.TransferReceiverStatusInitiated},
		{st.TransferStatusApplyingSenderKeyTweak, st.TransferReceiverStatusInitiated},

		// Critical: SENDER_KEY_TWEAKED maps to RECEIVER_CLAIM_PENDING. Sender
		// has completed handoff, receiver is now expected to claim.
		{st.TransferStatusSenderKeyTweaked, st.TransferReceiverStatusReceiverClaimPending},

		// Receiver-side states mirror.
		{st.TransferStatusReceiverKeyTweaked, st.TransferReceiverStatusKeyTweaked},
		{st.TransferStatusReceiverKeyTweakLocked, st.TransferReceiverStatusKeyTweakLocked},
		{st.TransferStatusReceiverKeyTweakApplied, st.TransferReceiverStatusKeyTweakApplied},
		{st.TransferStatusReceiverRefundSigned, st.TransferReceiverStatusRefundSigned},

		// Terminal states.
		{st.TransferStatusCompleted, st.TransferReceiverStatusCompleted},
		{st.TransferStatusExpired, st.TransferReceiverStatusCancelled},
		{st.TransferStatusReturned, st.TransferReceiverStatusCancelled},
	}
	for _, tc := range cases {
		t.Run(string(tc.transferStatus), func(t *testing.T) {
			assert.Equal(t, tc.expectedReceiverStatus, MapTransferToReceiverStatus(tc.transferStatus))
		})
	}
}
