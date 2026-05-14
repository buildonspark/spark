package mimo

import (
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/uuids"
	pb "github.com/lightsparkdev/spark/proto/spark"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
)

// AppendNetworkFilter returns a SQL fragment of the form ` AND t.network = $N`
// (or empty string for Network_UNSPECIFIED), appending the network arg to args
// and using the resulting param position in the fragment.
func AppendNetworkFilter(args []any, networkProto pb.Network) ([]any, string, error) {
	if networkProto == pb.Network_UNSPECIFIED {
		return args, "", nil
	}
	network, err := btcnetwork.FromProtoNetwork(networkProto)
	if err != nil {
		return nil, "", fmt.Errorf("failed to convert proto network: %w", err)
	}
	args = append(args, network.String())
	return args, fmt.Sprintf(" AND t.network = $%d", len(args)), nil
}

// AppendPendingCommonFilters builds the network, types, and transfer_ids
// clauses for pending-transfer SQL. These reference the transfers table
// (t.) since the relevant columns only exist there. Returns the appended
// args slice and the SQL fragment using dynamic param positions based on
// len(sqlArgs).
func AppendPendingCommonFilters(
	sqlArgs []any,
	network pb.Network,
	types []pb.TransferType,
	transferIDsFilter []string,
) ([]any, string, error) {
	var sb strings.Builder
	if network != pb.Network_UNSPECIFIED {
		n, err := btcnetwork.FromProtoNetwork(network)
		if err != nil {
			return nil, "", fmt.Errorf("invalid network: %w", err)
		}
		sqlArgs = append(sqlArgs, n.String())
		fmt.Fprintf(&sb, " AND t.network = $%d", len(sqlArgs))
	}
	if len(types) > 0 {
		typeStrs := make([]string, len(types))
		for i, t := range types {
			schemaType, err := st.TransferTypeFromProto(t.String())
			if err != nil {
				return nil, "", fmt.Errorf("invalid transfer type %s: %w", t.String(), err)
			}
			typeStrs[i] = string(schemaType)
		}
		sqlArgs = append(sqlArgs, pq.Array(typeStrs))
		fmt.Fprintf(&sb, " AND t.type = ANY($%d::text[])", len(sqlArgs))
	}
	if len(transferIDsFilter) > 0 {
		ids, err := uuids.ParseSlice(transferIDsFilter)
		if err != nil {
			return nil, "", fmt.Errorf("invalid transfer IDs: %w", err)
		}
		idStrs := make([]string, len(ids))
		for i, id := range ids {
			idStrs[i] = id.String()
		}
		sqlArgs = append(sqlArgs, pq.Array(idStrs))
		fmt.Fprintf(&sb, " AND t.id = ANY($%d::uuid[])", len(sqlArgs))
	}
	return sqlArgs, sb.String(), nil
}

// PendingTimeColumn enumerates the valid create_time column references
// used by AppendPendingTimeFilter. Typed alias instead of a plain string
// so a future caller can't accidentally interpolate user input into the
// SQL fragment — the column reference is %s-interpolated, not
// parameterized, and would be a SQLi vector if sourced from the request.
type PendingTimeColumn string

const (
	ReceiverCreateTimeColumn   PendingTimeColumn = "r.create_time"
	SenderCreateTimeColumn     PendingTimeColumn = "t.create_time"
	SenderEdgeCreateTimeColumn PendingTimeColumn = "s.create_time"
)

// AppendPendingTimeFilter builds the created_after/created_before clauses
// against the given column reference. Each arm uses its own table's
// column for index-drive efficiency. The has* booleans gate inclusion of
// each bound; zero-valued time.Time is not treated as "no filter" since
// it's a legitimate value to bound against.
func AppendPendingTimeFilter(
	sqlArgs []any,
	hasCreatedAfter bool, createdAfter time.Time,
	hasCreatedBefore bool, createdBefore time.Time,
	col PendingTimeColumn,
) ([]any, string) {
	var sb strings.Builder
	if hasCreatedAfter {
		sqlArgs = append(sqlArgs, createdAfter)
		fmt.Fprintf(&sb, " AND %s > $%d", col, len(sqlArgs))
	}
	if hasCreatedBefore {
		sqlArgs = append(sqlArgs, createdBefore)
		fmt.Fprintf(&sb, " AND %s < $%d", col, len(sqlArgs))
	}
	return sqlArgs, sb.String()
}
