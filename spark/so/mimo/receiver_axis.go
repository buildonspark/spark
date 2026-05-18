package mimo

import (
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
)

// transferStatusToReceiverStatusMap encodes the receiver-axis equivalent of
// each transfer.status value that has one. Receiver-named statuses share
// string identity across enums; terminals collapse per the correlation rules
// (EXPIRED/RETURNED → CANCELLED, COMPLETED → COMPLETED).
var transferStatusToReceiverStatusMap = map[st.TransferStatus]st.TransferReceiverStatus{
	st.TransferStatusReceiverKeyTweaked:      st.TransferReceiverStatusKeyTweaked,
	st.TransferStatusReceiverKeyTweakLocked:  st.TransferReceiverStatusKeyTweakLocked,
	st.TransferStatusReceiverKeyTweakApplied: st.TransferReceiverStatusKeyTweakApplied,
	st.TransferStatusReceiverRefundSigned:    st.TransferReceiverStatusRefundSigned,
	st.TransferStatusCompleted:               st.TransferReceiverStatusCompleted,
	st.TransferStatusExpired:                 st.TransferReceiverStatusCancelled,
	st.TransferStatusReturned:                st.TransferReceiverStatusCancelled,
}

// senderToReceiverAxisMap encodes the receiver-axis equivalent of each
// SENDER_*-named transfer.status value. The 4 sender-pending values collapse
// to INITIATED (the receiver edge's pre-tweak umbrella); SENDER_KEY_TWEAKED
// (post-tweak umbrella) maps 1:1 to RECEIVER_CLAIM_PENDING.
var senderToReceiverAxisMap = map[st.TransferStatus]st.TransferReceiverStatus{
	st.TransferStatusSenderInitiated:            st.TransferReceiverStatusInitiated,
	st.TransferStatusSenderInitiatedCoordinator: st.TransferReceiverStatusInitiated,
	st.TransferStatusApplyingSenderKeyTweak:     st.TransferReceiverStatusInitiated,
	st.TransferStatusSenderKeyTweakPending:      st.TransferReceiverStatusInitiated,
	st.TransferStatusSenderKeyTweaked:           st.TransferReceiverStatusReceiverClaimPending,
}

// collapsingReceiverStatuses are r.status values that aren't reached by a 1:1
// mapping — multiple transfer.status inputs translate to the same r.status.
// Filtering r.status alone over-matches when the caller asked for a partial
// subset of the collapsing class, so the SQL must add a t.status narrowing
// predicate to recover exact semantics.
//
//   - INITIATED ← {SENDER_INITIATED, SENDER_INITIATED_COORDINATOR,
//     APPLYING_SENDER_KEY_TWEAK, SENDER_KEY_TWEAK_PENDING} (4-to-1).
//   - CANCELLED ← {EXPIRED, RETURNED} (2-to-1).
//
// All other mappings are 1:1 and don't need narrowing.
var collapsingReceiverStatuses = map[st.TransferReceiverStatus]struct{}{
	st.TransferReceiverStatusInitiated: {},
	st.TransferReceiverStatusCancelled: {},
}

// IsReceiverAxisTranslatable reports whether s has a receiver-axis equivalent.
// Used by the routing predicate to fall through to legacy queryTransfers when
// a future enum value appears that our translation maps don't cover.
func IsReceiverAxisTranslatable(s st.TransferStatus) bool {
	if _, ok := transferStatusToReceiverStatusMap[s]; ok {
		return true
	}
	if _, ok := senderToReceiverAxisMap[s]; ok {
		return true
	}
	return false
}

// ReceiverArmFilters builds the three deduped status buckets the receiver-arm
// SQL needs to preserve exact semantics across the transfer.status →
// transfer_receivers.status translation. The SQL shape is:
//
//		r.status = ANY($receiverIndexSet)
//		AND (r.status = ANY($receiverExactMatch) OR t.status = ANY($transferStatusNarrowing))
//
//	  - receiverIndexSet: every translated r.status. Drives the index probe.
//	  - receiverExactMatch: subset whose mapping was 1:1 from t.status. Rows
//	    matching here need no further narrowing.
//	  - transferStatusNarrowing: original t.status values for inputs that
//	    translated to a collapsing r.status target (INITIATED or CANCELLED).
//	    Narrows rows in collapsing buckets to the caller's exact intent.
//
// Empty-array Postgres semantics (`x = ANY('{}'::text[])` is FALSE) make the
// OR collapse cleanly when either side is empty.
func ReceiverArmFilters(input []st.TransferStatus) (receiverIndexSet, receiverExactMatch []st.TransferReceiverStatus, transferStatusNarrowing []st.TransferStatus) {
	if len(input) == 0 {
		return nil, nil, nil
	}
	seenInIndexSet := make(map[st.TransferReceiverStatus]struct{}, len(input))
	seenInExactMatch := make(map[st.TransferReceiverStatus]struct{}, len(input))
	seenInNarrowing := make(map[st.TransferStatus]struct{}, len(input))

	addToIndexSet := func(r st.TransferReceiverStatus) {
		if _, ok := seenInIndexSet[r]; !ok {
			seenInIndexSet[r] = struct{}{}
			receiverIndexSet = append(receiverIndexSet, r)
		}
	}
	addToExactMatch := func(r st.TransferReceiverStatus) {
		if _, ok := seenInExactMatch[r]; !ok {
			seenInExactMatch[r] = struct{}{}
			receiverExactMatch = append(receiverExactMatch, r)
		}
	}
	addToNarrowing := func(t st.TransferStatus) {
		if _, ok := seenInNarrowing[t]; !ok {
			seenInNarrowing[t] = struct{}{}
			transferStatusNarrowing = append(transferStatusNarrowing, t)
		}
	}

	for _, s := range input {
		r, ok := transferStatusToReceiverStatusMap[s]
		if !ok {
			r, ok = senderToReceiverAxisMap[s]
			if !ok {
				continue
			}
		}
		addToIndexSet(r)
		if _, collapsing := collapsingReceiverStatuses[r]; collapsing {
			addToNarrowing(s)
		} else {
			addToExactMatch(r)
		}
	}
	return receiverIndexSet, receiverExactMatch, transferStatusNarrowing
}

// ReceiverStatusStrings converts a TransferReceiverStatus slice to its
// underlying string representation for binding into raw SQL via pq.Array.
// Dedupes input.
func ReceiverStatusStrings(statuses []st.TransferReceiverStatus) []string {
	seen := make(map[string]struct{}, len(statuses))
	out := make([]string, 0, len(statuses))
	for _, s := range statuses {
		k := string(s)
		if _, dup := seen[k]; !dup {
			seen[k] = struct{}{}
			out = append(out, k)
		}
	}
	return out
}

// TransferStatusStrings converts a TransferStatus slice to its underlying
// string representation for binding into raw SQL via pq.Array. Dedupes input.
func TransferStatusStrings(statuses []st.TransferStatus) []string {
	seen := make(map[string]struct{}, len(statuses))
	out := make([]string, 0, len(statuses))
	for _, s := range statuses {
		k := string(s)
		if _, dup := seen[k]; !dup {
			seen[k] = struct{}{}
			out = append(out, k)
		}
	}
	return out
}
