package mimo

import (
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"
	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
)

// CounterSwapArgs is the shape of an incoming queryCounterSwap request.
// The routing predicate requires sender-or-receiver participant, types ⊆
// {COUNTER_SWAP, COUNTER_SWAP_V3}, non-empty statuses, and every status
// receiver-axis translatable (so the receiver arm can be built).
type CounterSwapArgs struct {
	WalletPubkey     keys.Public
	Network          pb.Network
	Types            []pb.TransferType
	Statuses         []st.TransferStatus
	HasCreatedAfter  bool
	CreatedAfter     time.Time
	HasCreatedBefore bool
	CreatedBefore    time.Time
	Order            pb.Order
	Limit            int
	Offset           int
}

// BuildCounterSwapQuery emits a cross-arm UNION DISTINCT for the
// sender-or-receiver + counter-swap-types + receiver-axis-translatable
// statuses shape — the SDK's queryCounterSwapTransfers caller.
//
// The two arms use ASYMMETRIC strategies, intentionally:
//
//   - Sender arm uses column-based predicates on transfers (sender_identity_pubkey
//     leading). There is exactly one sender per transfer in the MIMO v0 (MVP)
//     data model, so transfers.sender_identity_pubkey is canonical — no
//     MIMO divergence on the sender axis. When multi-sender MIMO ships, this
//     shortcut needs to be revisited: see [[REVISIT-MULTI-SENDER]] below.
//   - Receiver arm uses edge-based predicates on transfer_receivers. Multiple
//     receivers can advance independently of the parent transfer's status
//     (e.g. one receiver COMPLETED while parent.status lags at
//     RECEIVER_KEY_TWEAKED for other receivers). Only the edge table gives
//     us correct per-receiver state.
//
// SENDER ARM SHAPE — per-status UNION ALL:
//
// Each input status emits its own sub-query with leading-equality on
// (sender_identity_pubkey, status) against idx_transfers_sender_status_create.
// Top-N pushdown is native via create_time DESC (3rd column of the composite),
// merged across sub-queries via Merge Append. Per-type filter is a heap step
// (type isn't on the composite). The per-status decomposition avoids the
// multi-value-ANY pathology on the second column that defeats top-N pushdown
// (skill: "Multi-value `ANY` on a non-leading composite column…").
//
// RECEIVER ARM SHAPE — per-type × per-bucket UNION ALL, mirroring
// BuildReceiverByTypeStatusQuery (#6825):
//
//   - Pure-postTweakActive bucket (1:1 receiver-axis statuses in the
//     claim-pending partial WHERE): drives
//     idx_transferreceiver_claim_pending_pubkey_time.
//   - Pure-remainder bucket (1:1 receiver-axis statuses outside the partial,
//     e.g. terminals): drives idx_transferreceiver_pubkey_type_time.
//   - Collapsing buckets (RECEIVER_INITIATED from the 4-sender-pending
//     collapse; CANCELLED from the EXPIRED+RETURNED collapse): drive
//     idx_transferreceiver_initiated_pubkey_type_time (the new INITIATED
//     partial, sibling of claim_pending) and the type composite respectively.
//
// COLLAPSING-NARROWING OPTIMIZATION — t.status narrowing on the
// RECEIVER_INITIATED bucket is dropped when the input status set covers
// ALL 4 sender-pending values that collapse to RECEIVER_INITIATED via
// senderToReceiverAxisMap. In that case the narrowing predicate
// `t.status = ANY([4 sender-pending])` is equivalent to "all rows with
// r.status = INITIATED" in steady state (per the receiver_axis state
// machine — INITIATED receivers have not-yet-tweaked parents). Dropping
// the narrowing lets the planner drive directly from the INITIATED partial
// without the expensive Hash Join against a global outgoing-in-flight walk
// of transfers. The SDK's queryCounterSwapTransfers always sends
// ACTIVE_COUNTER_SWAP_STATUSES which includes all 4 sender-pending values
// — so this optimization fires on 100% of prod traffic. Partial-umbrella
// callers (subset of sender-pending) keep the narrowing for correctness.
//
// CROSS-ARM DEDUP — UNION (distinct) on the (id, ct) tuple collapses
// self-transfer duplicates. Tuple stability across arms relies on the
// app-layer invariant r.create_time = s.create_time = t.create_time.
//
// MIMO-CORRECTNESS — pure-1:1 receiver sub-queries omit t.status by design,
// matching the divergence regression locked in by #6825. For multi-receiver
// MIMO transfers, one receiver row can advance past parent.status; pure-1:1
// sub-queries surface those rows for the advanced receiver.
//
// [[REVISIT-MULTI-SENDER]] — when MIMO supports multiple senders, the sender
// arm's column-based shortcut breaks: transfers.sender_identity_pubkey will
// not capture every sender of a transfer. At that point the sender arm
// should be rewritten to drive transfer_senders (edge-based), mirroring the
// current receiver arm's shape. The asymmetric design here is explicitly
// MIMO v0 / MVP.
func BuildCounterSwapQuery(args CounterSwapArgs) (string, []any, error) {
	if len(args.Statuses) == 0 {
		return "", nil, fmt.Errorf("non-empty statuses list required")
	}
	typeStrs, err := resolveTypeStrings(args.Types)
	if err != nil {
		return "", nil, err
	}

	rIndexSet, rExactMatch, tNarrowing := ReceiverArmFilters(args.Statuses)
	if len(rIndexSet) == 0 {
		return "", nil, fmt.Errorf("no statuses translated to receiver axis")
	}
	pure, collapsing := splitPureAndCollapsing(rIndexSet, rExactMatch)
	purePostTweak, pureRemainder := splitByPartialCoverage(pure)
	collapsingPostTweak, collapsingRemainder := splitByPartialCoverage(collapsing)

	collapsingTargets := append(append([]st.TransferReceiverStatus{}, collapsingPostTweak...), collapsingRemainder...)
	narrowingRedundant := narrowingRedundantFor(args.Statuses, collapsingTargets)

	senderStatusStrs := TransferStatusStrings(args.Statuses)

	perArmLimit := args.Offset + args.Limit
	sqlArgs := []any{
		args.WalletPubkey.Serialize(), // $1
		args.Limit,                    // $2
		args.Offset,                   // $3
		perArmLimit,                   // $4
	}
	perArmLimitPos := 4

	sqlArgs, sharedFilters, err := appendByTypesSharedFilters(sqlArgs, args.Network)
	if err != nil {
		return "", nil, err
	}

	// Per-arm time filters share param positions but reference different
	// create_time columns (sender arm walks transfers via t.create_time;
	// receiver arm walks transfer_receivers via r.create_time).
	var senderTime, receiverTime strings.Builder
	if args.HasCreatedAfter {
		sqlArgs = append(sqlArgs, args.CreatedAfter)
		pos := len(sqlArgs)
		fmt.Fprintf(&senderTime, " AND t.create_time > $%d", pos)
		fmt.Fprintf(&receiverTime, " AND r.create_time > $%d", pos)
	}
	if args.HasCreatedBefore {
		sqlArgs = append(sqlArgs, args.CreatedBefore)
		pos := len(sqlArgs)
		fmt.Fprintf(&senderTime, " AND t.create_time < $%d", pos)
		fmt.Fprintf(&receiverTime, " AND r.create_time < $%d", pos)
	}

	// Sender arm: one param per input status (for per-status UNION ALL).
	senderStatusPositions := make([]int, len(senderStatusStrs))
	for i, s := range senderStatusStrs {
		sqlArgs = append(sqlArgs, s)
		senderStatusPositions[i] = len(sqlArgs)
	}

	// Type array param shared by all sender sub-queries (heap-step filter,
	// since idx_transfers_sender_status_create doesn't include type).
	var typesArrayPos int
	if len(typeStrs) > 0 {
		sqlArgs = append(sqlArgs, pq.Array(typeStrs))
		typesArrayPos = len(sqlArgs)
	}

	// Receiver bucket params (each is an array of r.status values).
	purePostTweakPos, pureRemainderPos := 0, 0
	if len(purePostTweak) > 0 {
		sqlArgs = append(sqlArgs, pq.Array(ReceiverStatusStrings(purePostTweak)))
		purePostTweakPos = len(sqlArgs)
	}
	if len(pureRemainder) > 0 {
		sqlArgs = append(sqlArgs, pq.Array(ReceiverStatusStrings(pureRemainder)))
		pureRemainderPos = len(sqlArgs)
	}
	collapsingPostTweakPos, collapsingRemainderPos := 0, 0
	if len(collapsingPostTweak) > 0 {
		sqlArgs = append(sqlArgs, pq.Array(ReceiverStatusStrings(collapsingPostTweak)))
		collapsingPostTweakPos = len(sqlArgs)
	}
	if len(collapsingRemainder) > 0 {
		sqlArgs = append(sqlArgs, pq.Array(ReceiverStatusStrings(collapsingRemainder)))
		collapsingRemainderPos = len(sqlArgs)
	}

	// Narrowing array param (only emitted if narrowing isn't redundant).
	narrowingPos := 0
	if !narrowingRedundant && len(collapsing) > 0 {
		sqlArgs = append(sqlArgs, pq.Array(TransferStatusStrings(tNarrowing)))
		narrowingPos = len(sqlArgs)
	}

	// Per-type params for receiver arm sub-queries.
	typePositions := make([]int, len(typeStrs))
	for i, t := range typeStrs {
		sqlArgs = append(sqlArgs, t)
		typePositions[i] = len(sqlArgs)
	}

	direction := "DESC"
	if args.Order == pb.Order_ASCENDING {
		direction = "ASC"
	}

	senderSubs := make([]string, 0, len(senderStatusPositions))
	for _, sPos := range senderStatusPositions {
		senderSubs = append(senderSubs, buildSenderPerStatusSubQuery(
			sPos, typesArrayPos, perArmLimitPos, sharedFilters, senderTime.String(), direction))
	}

	// TODO: extract a receiver-arm primitive shared with BuildReceiverByTypeStatusQuery
	// once the MIMO handler set stabilizes. The per-type × per-bucket loop is
	// structurally identical between the two builders — they diverge only on
	// (1) inlining the time filter to share param positions across arms, and
	// (2) the collapsing-narrowing optimization. Both are easy to keep as
	// composition points on a shared `buildReceiverArmFromBuckets` helper.
	receiverSubs := make([]string, 0, len(typePositions)*4)
	for _, typePos := range typePositions {
		if purePostTweakPos > 0 {
			receiverSubs = append(receiverSubs, buildReceiverPureSubQuery(
				typePos, purePostTweakPos, perArmLimitPos, sharedFilters, receiverTime.String(), direction))
		}
		if pureRemainderPos > 0 {
			receiverSubs = append(receiverSubs, buildReceiverPureSubQuery(
				typePos, pureRemainderPos, perArmLimitPos, sharedFilters, receiverTime.String(), direction))
		}
		if collapsingPostTweakPos > 0 {
			receiverSubs = append(receiverSubs, buildCollapsingBucketSubQuery(
				typePos, collapsingPostTweakPos, narrowingPos, perArmLimitPos, sharedFilters, receiverTime.String(), direction))
		}
		if collapsingRemainderPos > 0 {
			receiverSubs = append(receiverSubs, buildCollapsingBucketSubQuery(
				typePos, collapsingRemainderPos, narrowingPos, perArmLimitPos, sharedFilters, receiverTime.String(), direction))
		}
	}

	if len(senderSubs) == 0 && len(receiverSubs) == 0 {
		return "", nil, fmt.Errorf("no sub-queries to emit — empty status partition")
	}

	senderUnion := strings.Join(senderSubs, "\n\t\t\tUNION ALL\n\t\t\t")
	receiverUnion := strings.Join(receiverSubs, "\n\t\t\tUNION ALL\n\t\t\t")

	var armsBody string
	switch {
	case len(senderSubs) > 0 && len(receiverSubs) > 0:
		armsBody = fmt.Sprintf("(%s)\n\t\t\tUNION\n\t\t\t(%s)", senderUnion, receiverUnion)
	case len(senderSubs) > 0:
		armsBody = senderUnion
	default:
		armsBody = receiverUnion
	}

	query := fmt.Sprintf(`
		SELECT id FROM (
			%s
		) u
		ORDER BY ct %s, id %s
		LIMIT $2 OFFSET $3
	`, armsBody, direction, direction)

	return query, sqlArgs, nil
}

// buildSenderPerStatusSubQuery emits a single-status sub-query against transfers
// with leading-equality on (sender_identity_pubkey, status). Drives
// idx_transfers_sender_status_create with native top-N pushdown via the
// composite's create_time DESC ordering. Type filter is a heap step
// (the composite doesn't include type); for counter-swap-heavy callers
// it's selective enough that walks stay bounded near LIMIT.
//
// [[REVISIT-IF-TYPE-FILTER-BROADENS]] — the heap-step shortcut is load-bearing
// on shouldRouteToCounterSwap admitting only requests where every type is in
// {COUNTER_SWAP, COUNTER_SWAP_V3}. If the routing predicate is ever broadened
// to mixed type sets, this sub-query must be revisited: a wallet with many
// non-counter-swap outgoing transfers in a given status would force walks
// to scan deep into the composite while heap-filtering out non-matching
// types, blowing the bounded-walk assumption.
func buildSenderPerStatusSubQuery(statusPos, typesArrayPos, perArmLimitPos int, sharedFilters, timeFilter, direction string) string {
	return fmt.Sprintf(
		`(SELECT t.id AS id, t.create_time AS ct
			 FROM transfers t
			 WHERE t.sender_identity_pubkey = $1
			   AND t.status = $%d
			   AND t.type = ANY($%d::text[])%s%s
			 ORDER BY t.create_time %s, t.id %s
			 LIMIT $%d)`,
		statusPos, typesArrayPos, sharedFilters, timeFilter, direction, direction, perArmLimitPos)
}

// buildCollapsingBucketSubQuery emits a receiver-arm sub-query for collapsing
// r.status buckets ({INITIATED, CANCELLED}). When narrowingPos > 0 the
// t.status narrowing predicate is included (slow but correct for
// partial-umbrella callers). When narrowingPos == 0 the narrowing is
// dropped (fast path; relies on the steady-state invariant that
// r.status = INITIATED ↔ parent.status ∈ {4 sender-pending}).
func buildCollapsingBucketSubQuery(typePos, bucketPos, narrowingPos, perArmLimitPos int, sharedFilters, timeFilter, direction string) string {
	if narrowingPos == 0 {
		return buildReceiverPureSubQuery(typePos, bucketPos, perArmLimitPos, sharedFilters, timeFilter, direction)
	}
	return buildReceiverCollapsingSubQuery(typePos, bucketPos, narrowingPos, perArmLimitPos, sharedFilters, timeFilter, direction)
}

// collapseTargetPrereqs is keyed by collapsing receiver r.status and lists
// the t.status inputs that map to that r.status via receiver_axis. The
// narrowing predicate on a collapsing-class sub-query is redundant only
// when every prereq for that class is present in the input — otherwise
// dropping it over-matches rows from sibling inputs.
//
// Parity with collapsingReceiverStatuses (receiver_axis.go) is enforced
// by TestCollapseTargetPrereqsMatchesCollapsingSet; if a new collapsing
// r.status is added there without a prereq entry here, the test fails
// loudly rather than narrowing silently becoming unsafe.
var collapseTargetPrereqs = map[st.TransferReceiverStatus][]st.TransferStatus{
	st.TransferReceiverStatusInitiated: {
		st.TransferStatusSenderInitiated,
		st.TransferStatusSenderInitiatedCoordinator,
		st.TransferStatusApplyingSenderKeyTweak,
		st.TransferStatusSenderKeyTweakPending,
	},
	st.TransferReceiverStatusCancelled: {
		st.TransferStatusExpired,
		st.TransferStatusReturned,
	},
}

// narrowingRedundantFor reports whether the t.status narrowing predicate can
// be safely dropped from collapsing-class sub-queries.
//
// For each collapsing r.status target actually emitted in this query
// (`collapsingTargets`), the input must contain ALL transfer.status values
// that map to it via the receiver_axis collapse. Otherwise dropping
// narrowing would over-match rows from sibling inputs — e.g. an input of
// EXPIRED-only would, without narrowing, also surface RETURNED rows
// (both map to RECEIVER_CANCELLED).
//
// For the SDK's queryCounterSwapTransfers (ACTIVE_COUNTER_SWAP_STATUSES, no
// terminals), only RECEIVER_INITIATED appears in collapsingTargets — fast
// path fires whenever the 4 sender-pending values are present (always true
// for the SDK). Partial-umbrella callers that include only one of a
// collapsing pair keep narrowing for correctness.
//
// Unknown collapsing targets (e.g. a future receiver-axis collapse class
// added without an entry in collapseTargetPrereqs) conservatively return
// false — narrowing is preserved until the prereqs are explicitly declared.
func narrowingRedundantFor(input []st.TransferStatus, collapsingTargets []st.TransferReceiverStatus) bool {
	if len(collapsingTargets) == 0 {
		return true
	}
	inputSet := make(map[st.TransferStatus]struct{}, len(input))
	for _, s := range input {
		inputSet[s] = struct{}{}
	}
	for _, target := range collapsingTargets {
		prereqs, ok := collapseTargetPrereqs[target]
		if !ok {
			return false
		}
		for _, p := range prereqs {
			if _, found := inputSet[p]; !found {
				return false
			}
		}
	}
	return true
}
