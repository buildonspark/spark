package handler

import (
	"testing"

	"github.com/google/uuid"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestGetSigningCommitmentsRejectsMalformedRequestsWithInvalidArgument(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	handler := NewSigningHandler(&so.Config{
		SigningOperatorMap:         sparktesting.GetAllSigningOperators(t),
		FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{},
	})

	tooManyNodeIDs := make([]string, DefaultMaxSigningCommitmentNodes+1)
	for i := range tooManyNodeIDs {
		tooManyNodeIDs[i] = uuid.NewString()
	}

	for _, tc := range []struct {
		name    string
		req     *pb.GetSigningCommitmentsRequest
		wantErr string
	}{
		{
			name:    "nil request",
			req:     nil,
			wantErr: "request is required",
		},
		{
			name: "node ids and count both set",
			req: &pb.GetSigningCommitmentsRequest{
				NodeIds:     []string{uuid.NewString()},
				NodeIdCount: 1,
			},
			wantErr: "can provide node_ids or node_id_count, but not both",
		},
		{
			name: "malformed node id",
			req: &pb.GetSigningCommitmentsRequest{
				NodeIds: []string{"not-a-uuid"},
				Count:   1,
			},
			wantErr: "unable to parse node id",
		},
		{
			name: "too many node ids",
			req: &pb.GetSigningCommitmentsRequest{
				NodeIds: tooManyNodeIDs,
				Count:   1,
			},
			wantErr: "there were 1001 node ids provided",
		},
		{
			name: "node id count too large",
			req: &pb.GetSigningCommitmentsRequest{
				NodeIdCount: DefaultMaxSigningCommitmentNodes + 1,
				Count:       1,
			},
			wantErr: "node ID count provided was 1001",
		},
		{
			name: "count too large",
			req: &pb.GetSigningCommitmentsRequest{
				NodeIdCount: 1,
				Count:       DefaultMaxSigningCommitmentCount + 1,
			},
			wantErr: "number of signing commitments provided was 11",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := handler.GetSigningCommitments(ctx, tc.req)
			require.Nil(t, resp)
			require.Error(t, err)
			require.Equal(t, codes.InvalidArgument, status.Code(err))
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

// When a request supplies node_ids that don't exist in tree_nodes, the
// response must be NotFound with the missing IDs named — not a silently
// truncated success that breaks the [count] x [num_node_ids] ordering
// contract on GetSigningCommitmentsResponse.
func TestGetSigningCommitmentsRejectsUnknownNodeIDs(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	handler := NewSigningHandler(&so.Config{
		SigningOperatorMap:         sparktesting.GetAllSigningOperators(t),
		FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{},
	})

	missingID := uuid.NewString()
	resp, err := handler.GetSigningCommitments(ctx, &pb.GetSigningCommitmentsRequest{
		NodeIds: []string{missingID},
		Count:   1,
	})
	require.Nil(t, resp)
	require.Error(t, err)
	require.Equal(t, codes.NotFound, status.Code(err))
	require.ErrorContains(t, err, missingID)
	require.ErrorContains(t, err, "unknown node ids")
}
