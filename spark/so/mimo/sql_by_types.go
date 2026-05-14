package mimo

import (
	"fmt"
	"strings"
	"time"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
)

// ByTypesArgs is the shape of an incoming queryByTypes request. The routing
// predicate (shouldRouteToByTypes) requires len(filter.Types) > 0 and rejects
// any request with filter.TransferIds set — by-id lookups stay on legacy
// queryTransfers, which drives transfers_pkey directly.
type ByTypesArgs struct {
	WalletPubkey     keys.Public
	Network          pb.Network
	Types            []pb.TransferType
	HasCreatedAfter  bool
	CreatedAfter     time.Time
	HasCreatedBefore bool
	CreatedBefore    time.Time
	Order            pb.Order
	Limit            int
	Offset           int
}

// ByTypesEdgeTable is the edge-table alias the type filter targets.
// Must be one of the literals below — never set from user input.
type ByTypesEdgeTable string

const (
	ByTypesReceiverAlias ByTypesEdgeTable = "r"
	ByTypesSenderAlias   ByTypesEdgeTable = "s"
)

// resolveTypeStrings converts proto transfer-type values to their schema string
// form, deduping repeated values to preserve the per-type UNION ALL invariant
// that sub-queries within an arm are disjoint by transfer_type — duplicate
// types would emit identical sub-queries and inflate the SQL page with repeat
// transfer_ids, producing short pages after IDIn dedup at the marshal step.
//
// Post-dedup length is bounded by pb.TransferType enum cardinality (single-digit).
func resolveTypeStrings(types []pb.TransferType) ([]string, error) {
	if len(types) == 0 {
		return nil, fmt.Errorf("non-empty types list required")
	}
	seen := make(map[string]struct{}, len(types))
	out := make([]string, 0, len(types))
	for _, t := range types {
		schemaType, err := st.TransferTypeFromProto(t.String())
		if err != nil {
			return nil, fmt.Errorf("invalid transfer type %s: %w", t.String(), err)
		}
		s := string(schemaType)
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out, nil
}

// appendByTypesSharedFilters appends the network parameter to sqlArgs and
// returns the SQL fragment to inject into each per-type sub-query. The type
// filter is emitted per-sub-query (single equality) so the composite gets
// leading-equality on (identity_pubkey, transfer_type) — a multi-value ANY on
// transfer_type defeats top-N pushdown when the index orders by create_time after
// type, forcing materialize-then-sort across all matching rows.
//
// Network_UNSPECIFIED is rejected outright (rather than producing an empty
// filter) — defense-in-depth against direct builder invocations that bypass
// the handler's network-required check. Silently dropping the network
// predicate would risk cross-network data leakage.
func appendByTypesSharedFilters(sqlArgs []any, network pb.Network) ([]any, string, error) {
	if network == pb.Network_UNSPECIFIED {
		return nil, "", fmt.Errorf("network must be specified")
	}
	n, err := btcnetwork.FromProtoNetwork(network)
	if err != nil {
		return nil, "", fmt.Errorf("invalid network: %w", err)
	}
	var sb strings.Builder
	sqlArgs = append(sqlArgs, n.String())
	fmt.Fprintf(&sb, " AND t.network = $%d", len(sqlArgs))
	return sqlArgs, sb.String(), nil
}

// buildPerTypeUnionAll emits "(sub_T1) UNION ALL (sub_T2) ..." for one arm.
// Each sub-query targets a single type value so the composite drives the scan
// via leading-equality on (identity_pubkey, transfer_type) with ordered top-N
// over (create_time, transfer_id). Per-type sub-queries within an arm are
// disjoint by transfer_type, so UNION ALL is safe and cheaper than UNION.
//
// tl;dr: We force the query into separate sorted stacks we can merge sort.
//
// Cost scales O((offset+limit) × N_types) per arm. Past ~OFFSET 1000 the
// per-type pushdown advantage vs a single multi-value-ANY scan diminishes.
func buildPerTypeUnionAll(
	edge ByTypesEdgeTable,
	edgeTable string,
	sharedFilters string,
	timeFilter string,
	typePositions []int,
	perArmLimitPos int,
	direction string,
) string {
	subs := make([]string, len(typePositions))
	for i, typePos := range typePositions {
		subs[i] = fmt.Sprintf(
			`(SELECT %[1]s.transfer_id AS id, %[1]s.create_time AS ct
			 FROM %[2]s %[1]s
			 INNER JOIN transfers t ON t.id = %[1]s.transfer_id
			 WHERE %[1]s.identity_pubkey = $1
			   AND %[1]s.transfer_type = $%[3]d%[4]s%[5]s
			 ORDER BY %[1]s.create_time %[6]s, %[1]s.transfer_id %[6]s
			 LIMIT $%[7]d)`,
			edge, edgeTable, typePos, sharedFilters, timeFilter, direction, perArmLimitPos)
	}
	return strings.Join(subs, "\n\t\t\tUNION ALL\n\t\t\t")
}

// BuildByTypesQuerySender drives idx_transfersender_pubkey_type_time per type
// via a UNION ALL of N per-type sub-queries. Each sub-query has leading-equality
// on (identity_pubkey, transfer_type) so the composite produces ordered top-N
// over (create_time, transfer_id) bounded by the per-arm LIMIT. JOIN to
// transfers is a PK lookup used for the network filter.
func BuildByTypesQuerySender(args ByTypesArgs) (string, []any, error) {
	return buildByTypesSingleArm(args, ByTypesSenderAlias, "transfer_senders", SenderEdgeCreateTimeColumn)
}

// BuildByTypesQueryReceiver — symmetric receiver-side form. See BuildByTypesQuerySender.
func BuildByTypesQueryReceiver(args ByTypesArgs) (string, []any, error) {
	return buildByTypesSingleArm(args, ByTypesReceiverAlias, "transfer_receivers", ReceiverCreateTimeColumn)
}

func buildByTypesSingleArm(args ByTypesArgs, edge ByTypesEdgeTable, edgeTable string, timeColumn PendingTimeColumn) (string, []any, error) {
	typeStrs, err := resolveTypeStrings(args.Types)
	if err != nil {
		return "", nil, err
	}

	perArmLimit := args.Offset + args.Limit

	sqlArgs := []any{
		args.WalletPubkey.Serialize(),
		args.Limit,
		args.Offset,
		perArmLimit,
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
		timeColumn,
	)

	typePositions := make([]int, len(typeStrs))
	for i, t := range typeStrs {
		sqlArgs = append(sqlArgs, t)
		typePositions[i] = len(sqlArgs)
	}

	direction := "DESC"
	if args.Order == pb.Order_ASCENDING {
		direction = "ASC"
	}

	unionAll := buildPerTypeUnionAll(edge, edgeTable, sharedFilters, timeFilter, typePositions, perArmLimitPos, direction)

	query := fmt.Sprintf(`
		SELECT id FROM (
			%s
		) u
		ORDER BY ct %s, id %s
		LIMIT $2 OFFSET $3
	`, unionAll, direction, direction)

	return query, sqlArgs, nil
}

// BuildByTypesQuerySenderOrReceiver emits both arms as parallel per-type
// UNION ALL blocks, then UNIONs (distinct) the two arms for self-transfer
// dedup across the (transfer_id, create_time) tuple. The (id, ct) pair is
// stable across arms because edge rows are written with create_time =
// transfers.create_time at transfer creation — an app-layer invariant.
func BuildByTypesQuerySenderOrReceiver(args ByTypesArgs) (string, []any, error) {
	typeStrs, err := resolveTypeStrings(args.Types)
	if err != nil {
		return "", nil, err
	}

	perArmLimit := args.Offset + args.Limit

	sqlArgs := []any{
		args.WalletPubkey.Serialize(),
		args.Limit,
		args.Offset,
		perArmLimit,
	}
	perArmLimitPos := 4

	sqlArgs, sharedFilters, err := appendByTypesSharedFilters(sqlArgs, args.Network)
	if err != nil {
		return "", nil, err
	}

	// Inlined (not AppendPendingTimeFilter) so both arms share the same param positions.
	var senderTimeFilter, receiverTimeFilter strings.Builder
	if args.HasCreatedAfter {
		sqlArgs = append(sqlArgs, args.CreatedAfter)
		pos := len(sqlArgs)
		fmt.Fprintf(&senderTimeFilter, " AND s.create_time > $%d", pos)
		fmt.Fprintf(&receiverTimeFilter, " AND r.create_time > $%d", pos)
	}
	if args.HasCreatedBefore {
		sqlArgs = append(sqlArgs, args.CreatedBefore)
		pos := len(sqlArgs)
		fmt.Fprintf(&senderTimeFilter, " AND s.create_time < $%d", pos)
		fmt.Fprintf(&receiverTimeFilter, " AND r.create_time < $%d", pos)
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

	senderUnion := buildPerTypeUnionAll(
		ByTypesSenderAlias, "transfer_senders",
		sharedFilters, senderTimeFilter.String(),
		typePositions, perArmLimitPos, direction)
	receiverUnion := buildPerTypeUnionAll(
		ByTypesReceiverAlias, "transfer_receivers",
		sharedFilters, receiverTimeFilter.String(),
		typePositions, perArmLimitPos, direction)

	query := fmt.Sprintf(`
		SELECT id FROM (
			(%s)
			UNION
			(%s)
		) u
		ORDER BY ct %s, id %s
		LIMIT $2 OFFSET $3
	`, senderUnion, receiverUnion, direction, direction)

	return query, sqlArgs, nil
}
