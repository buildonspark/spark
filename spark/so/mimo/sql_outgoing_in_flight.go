package mimo

import (
	"fmt"
	"time"

	"github.com/lib/pq"
	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
)

// OutgoingInFlightArgs is the input for BuildOutgoingInFlightQuery.
//
// Statuses must be a non-empty subset of OutgoingInFlightSenderStatuses —
// the dispatcher enforces this. Subset matching against the partial index
// is the load-bearing perf property of this shape.
type OutgoingInFlightArgs struct {
	WalletPubkey      keys.Public
	Statuses          []st.TransferStatus
	Network           pb.Network
	Types             []pb.TransferType
	TransferIDsFilter []string
	HasCreatedAfter   bool
	CreatedAfter      time.Time
	HasCreatedBefore  bool
	CreatedBefore     time.Time
	Order             pb.Order
	Limit             int
	Offset            int
}

// BuildOutgoingInFlightQuery emits SQL that drives
// idx_transfers_outgoing_in_flight_sender_pubkey_time directly via the
// leading equality on sender_identity_pubkey plus a status filter that's a
// subset of the partial's WHERE:
//
//	transfers (sender_identity_pubkey, create_time DESC, id DESC)
//	WHERE status IN (SENDER_INITIATED, SENDER_INITIATED_COORDINATOR,
//	                 APPLYING_SENDER_KEY_TWEAK, SENDER_KEY_TWEAK_PENDING)
//
// The ORDER BY matches the index ordering exactly, so the planner uses
// top-N pushdown — walk the index in (create_time DESC, id DESC) order for
// the wallet and stop at LIMIT.
func BuildOutgoingInFlightQuery(args OutgoingInFlightArgs) (string, []any, error) {
	if len(args.Statuses) == 0 {
		return "", nil, fmt.Errorf("BuildOutgoingInFlightQuery requires at least one status")
	}

	statusStrs := make([]string, len(args.Statuses))
	for i, s := range args.Statuses {
		statusStrs[i] = string(s)
	}

	sqlArgs := []any{
		args.WalletPubkey.Serialize(), // $1
		pq.Array(statusStrs),          // $2
		args.Limit,                    // $3
		args.Offset,                   // $4
	}

	sqlArgs, commonFilters, err := AppendPendingCommonFilters(sqlArgs, args.Network, args.Types, args.TransferIDsFilter)
	if err != nil {
		return "", nil, err
	}
	sqlArgs, timeFilter := AppendPendingTimeFilter(
		sqlArgs,
		args.HasCreatedAfter, args.CreatedAfter,
		args.HasCreatedBefore, args.CreatedBefore,
		SenderCreateTimeColumn,
	)

	direction := "DESC"
	if args.Order == pb.Order_ASCENDING {
		direction = "ASC"
	}

	query := fmt.Sprintf(`
		SELECT t.id
		FROM transfers t
		WHERE t.sender_identity_pubkey = $1
		  AND t.status = ANY($2::text[])%s%s
		ORDER BY t.create_time %s, t.id %s
		LIMIT $3 OFFSET $4
	`, commonFilters, timeFilter, direction, direction)

	return query, sqlArgs, nil
}
