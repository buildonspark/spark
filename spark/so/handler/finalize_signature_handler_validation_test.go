package handler

import (
	"testing"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestFinalizeNodeSignaturesV2RejectsMalformedAndMissingNodeIDs(t *testing.T) {
	t.Parallel()
	ctx, _ := db.NewTestSQLiteContext(t)
	handler := NewFinalizeSignatureHandler(&so.Config{
		FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{},
	})

	tests := []struct {
		name     string
		nodeID   string
		wantCode codes.Code
	}{
		{
			name:     "malformed_node_id",
			nodeID:   "not-a-uuid",
			wantCode: codes.InvalidArgument,
		},
		{
			name:     "missing_node_id",
			nodeID:   uuid.New().String(),
			wantCode: codes.NotFound,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resp, err := handler.FinalizeNodeSignaturesV2(ctx, &pb.FinalizeNodeSignaturesRequest{
				Intent: pbcommon.SignatureIntent_CREATION,
				NodeSignatures: []*pb.NodeSignatures{{
					NodeId:            test.nodeID,
					NodeTxSignature:   make([]byte, 64),
					RefundTxSignature: make([]byte, 64),
				}},
			})
			require.Nil(t, resp)
			require.Equal(t, test.wantCode, status.Code(err))
		})
	}
}

func TestFinalizeNodeSignaturesV2RejectsBadSignature(t *testing.T) {
	t.Parallel()
	ctx, _ := db.NewTestSQLiteContext(t)
	handler := NewFinalizeSignatureHandler(&so.Config{
		FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{},
	})
	_, node := createTestTree(t, ctx, btcnetwork.Regtest, st.TreeStatusAvailable)

	resp, err := handler.FinalizeNodeSignaturesV2(ctx, &pb.FinalizeNodeSignaturesRequest{
		Intent: pbcommon.SignatureIntent_CREATION,
		NodeSignatures: []*pb.NodeSignatures{{
			NodeId:            node.ID.String(),
			NodeTxSignature:   make([]byte, 64),
			RefundTxSignature: []byte{0x01, 0x02},
		}},
	})

	require.Nil(t, resp)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
}
