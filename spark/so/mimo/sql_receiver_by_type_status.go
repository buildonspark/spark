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

// ReceiverByTypeStatusArgs is the shape of an incoming queryReceiverByTypeStatus
// request. The routing predicate requires len(Types) > 0, len(Statuses) > 0,
// every status receiver-axis translatable, and receiver participant.
type ReceiverByTypeStatusArgs struct {
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

// BuildReceiverByTypeStatusQuery emits per-type × per-bucket UNION ALL against
// transfer_receivers. The translated r.status set is partitioned along two
// independent axes:
//
//  1. Partial coverage — whether the r.status lies inside the partial WHERE
//     of `idx_transferreceiver_claim_pending_pubkey_time` (the receiver
//     claim-pending 5-state partial) or outside it (the REMAINDER, which
//     drives `idx_transferreceiver_pubkey_type_time` via leading-equality on
//     `(identity_pubkey, transfer_type)`).
//  2. Translation provenance — whether the r.status came from a pure 1:1
//     input mapping or from a collapsing input (4-sender-pending → INITIATED,
//     EXPIRED+RETURNED → CANCELLED). Collapsing-bucket sub-queries add a
//     direct `AND t.status = ANY($narrowing::text[])` predicate to recover
//     exact semantics for partial-umbrella callers; pure-bucket sub-queries
//     don't carry that predicate at all.
//
// The two output classes (pure r.statuses {RECEIVER_*, COMPLETED,
// RECEIVER_CLAIM_PENDING} vs collapsing r.statuses {INITIATED, CANCELLED})
// are disjoint by construction, so no sub-query needs both predicates. This
// disjointness is what lets us drop the OR-of-two-tables predicate the
// previous design carried; mixing r.status and t.status in a single
// disjunction defeated the planner's selectivity estimate and tipped it
// toward Parallel Bitmap Heap Scan with lossy heap recheck.
//
// UNION ALL (not UNION DISTINCT) relies on the (transfer_id, identity_pubkey)
// unique index on transfer_receivers: each row has exactly one r.status and
// one transfer_type, so per-type × per-bucket sub-queries can't double-emit
// the same row.
//
// Empty buckets are skipped — no degenerate `r.status IN ()` sub-queries.
func BuildReceiverByTypeStatusQuery(args ReceiverByTypeStatusArgs) (string, []any, error) {
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

	sqlArgs, timeFilter := AppendPendingTimeFilter(
		sqlArgs,
		args.HasCreatedAfter, args.CreatedAfter,
		args.HasCreatedBefore, args.CreatedBefore,
		ReceiverCreateTimeColumn,
	)

	narrowingPos := 0
	if len(collapsing) > 0 {
		sqlArgs = append(sqlArgs, pq.Array(TransferStatusStrings(tNarrowing)))
		narrowingPos = len(sqlArgs)
	}

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

	typePositions := make([]int, len(typeStrs))
	for i, t := range typeStrs {
		sqlArgs = append(sqlArgs, t)
		typePositions[i] = len(sqlArgs)
	}

	direction := "DESC"
	if args.Order == pb.Order_ASCENDING {
		direction = "ASC"
	}

	subs := make([]string, 0, len(typePositions)*4)
	for _, typePos := range typePositions {
		if purePostTweakPos > 0 {
			subs = append(subs, buildReceiverPureSubQuery(typePos, purePostTweakPos, perArmLimitPos, sharedFilters, timeFilter, direction))
		}
		if pureRemainderPos > 0 {
			subs = append(subs, buildReceiverPureSubQuery(typePos, pureRemainderPos, perArmLimitPos, sharedFilters, timeFilter, direction))
		}
		if collapsingPostTweakPos > 0 {
			subs = append(subs, buildReceiverCollapsingSubQuery(typePos, collapsingPostTweakPos, narrowingPos, perArmLimitPos, sharedFilters, timeFilter, direction))
		}
		if collapsingRemainderPos > 0 {
			subs = append(subs, buildReceiverCollapsingSubQuery(typePos, collapsingRemainderPos, narrowingPos, perArmLimitPos, sharedFilters, timeFilter, direction))
		}
	}
	unionAll := strings.Join(subs, "\n\t\t\tUNION ALL\n\t\t\t")

	query := fmt.Sprintf(`
		SELECT id FROM (
			%s
		) u
		ORDER BY ct %s, id %s
		LIMIT $2 OFFSET $3
	`, unionAll, direction, direction)

	return query, sqlArgs, nil
}

// buildReceiverPureSubQuery emits a sub-query for r.statuses that came from
// pure 1:1 input mappings — no narrowing predicate needed. The single
// `r.status = ANY($bucket)` predicate is enough; subset-matching against the
// `idx_transferreceiver_claim_pending_pubkey_time` partial WHERE fires when
// bucket is the postTweakActive split.
//
// Receiver-axis semantics (load-bearing): the absence of a `t.status` predicate
// is intentional. For multi-receiver MIMO transfers, one receiver row can be
// COMPLETED while the parent `transfers.status` (and other receivers) still
// lag — querying as the completed receiver with `statuses=[COMPLETED]` must
// return the transfer. Adding `t.status = ANY(...)` here would silently drop
// these rows by mirroring legacy's parent-axis filter, which is exactly the
// divergence this handler exists to correct. Regression-locked by
// TestQueryAllTransfers_ReceiverByTypeStatus_PerReceiverCompletedDivergence.
func buildReceiverPureSubQuery(typePos, bucketPos, perArmLimitPos int, sharedFilters, timeFilter, direction string) string {
	return fmt.Sprintf(
		`(SELECT r.transfer_id AS id, r.create_time AS ct
			 FROM transfer_receivers r
			 INNER JOIN transfers t ON t.id = r.transfer_id
			 WHERE r.identity_pubkey = $1
			   AND r.transfer_type = $%d
			   AND r.status = ANY($%d::text[])%s%s
			 ORDER BY r.create_time %s, r.transfer_id %s
			 LIMIT $%d)`,
		typePos, bucketPos, sharedFilters, timeFilter, direction, direction, perArmLimitPos)
}

// buildReceiverCollapsingSubQuery emits a sub-query for r.statuses that came
// from collapsing input mappings ({INITIATED, CANCELLED}). The `t.status`
// narrowing predicate restricts collapsed-class rows to the caller's exact
// input set; for full-umbrella callers it's a no-op, for partial-umbrella
// callers it's load-bearing for correctness.
func buildReceiverCollapsingSubQuery(typePos, bucketPos, narrowingPos, perArmLimitPos int, sharedFilters, timeFilter, direction string) string {
	return fmt.Sprintf(
		`(SELECT r.transfer_id AS id, r.create_time AS ct
			 FROM transfer_receivers r
			 INNER JOIN transfers t ON t.id = r.transfer_id
			 WHERE r.identity_pubkey = $1
			   AND r.transfer_type = $%d
			   AND r.status = ANY($%d::text[])
			   AND t.status = ANY($%d::text[])%s%s
			 ORDER BY r.create_time %s, r.transfer_id %s
			 LIMIT $%d)`,
		typePos, bucketPos, narrowingPos, sharedFilters, timeFilter, direction, direction, perArmLimitPos)
}

// splitPureAndCollapsing splits rIndexSet into the subset that came from pure
// 1:1 input mappings (= rExactMatch — by construction the values whose inputs
// don't collide on the receiver axis) and the remainder, which are the
// collapsing-class targets {INITIATED, CANCELLED}.
func splitPureAndCollapsing(rIndexSet, rExactMatch []st.TransferReceiverStatus) (pure, collapsing []st.TransferReceiverStatus) {
	exactSet := make(map[st.TransferReceiverStatus]struct{}, len(rExactMatch))
	for _, s := range rExactMatch {
		exactSet[s] = struct{}{}
	}
	for _, s := range rIndexSet {
		if _, ok := exactSet[s]; ok {
			pure = append(pure, s)
		} else {
			collapsing = append(collapsing, s)
		}
	}
	return pure, collapsing
}

// splitByPartialCoverage splits a receiver-axis status set into the subset that
// lies inside idx_transferreceiver_claim_pending_pubkey_time's partial WHERE
// (drives that partial via subset-rule) and the remainder (drives the type
// composite via leading-equality, with r.status as a Filter).
func splitByPartialCoverage(rIndexSet []st.TransferReceiverStatus) (postTweakActive, remainder []st.TransferReceiverStatus) {
	for _, s := range rIndexSet {
		if _, ok := claimPendingPartialMembers[s]; ok {
			postTweakActive = append(postTweakActive, s)
		} else {
			remainder = append(remainder, s)
		}
	}
	return postTweakActive, remainder
}

// claimPendingPartialMembers mirrors the WHERE clause of
// idx_transferreceiver_claim_pending_pubkey_time. Package-level so the lookup
// avoids per-request allocation — BuildReceiverByTypeStatusQuery invokes
// splitByPartialCoverage twice per call.
var claimPendingPartialMembers = map[st.TransferReceiverStatus]struct{}{
	st.TransferReceiverStatusReceiverClaimPending: {},
	st.TransferReceiverStatusKeyTweaked:           {},
	st.TransferReceiverStatusKeyTweakLocked:       {},
	st.TransferReceiverStatusKeyTweakApplied:      {},
	st.TransferReceiverStatusRefundSigned:         {},
}
