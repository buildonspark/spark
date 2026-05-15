package handler

import (
	"testing"

	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestInternalDepositHandlerRejectsMalformedRequestsWithoutPanic(t *testing.T) {
	handler := NewInternalDepositHandler(nil)

	tests := []struct {
		name    string
		call    func() error
		wantErr string
	}{
		{
			name: "mark keyshare nil request",
			call: func() error {
				_, err := handler.MarkKeyshareForDepositAddress(t.Context(), nil)
				return err
			},
			wantErr: "request is required",
		},
		{
			name: "generate static proofs nil request",
			call: func() error {
				_, err := handler.GenerateStaticDepositAddressProofs(t.Context(), nil)
				return err
			},
			wantErr: "request is required",
		},
		{
			name: "finalize tree nil request",
			call: func() error {
				return handler.FinalizeTreeCreation(t.Context(), nil)
			},
			wantErr: "request is required",
		},
		{
			name: "finalize tree empty nodes",
			call: func() error {
				return handler.FinalizeTreeCreation(t.Context(), &pbinternal.FinalizeTreeCreationRequest{})
			},
			wantErr: "at least one node is required",
		},
		{
			name: "finalize tree nil node",
			call: func() error {
				return handler.FinalizeTreeCreation(t.Context(), &pbinternal.FinalizeTreeCreationRequest{
					Nodes: []*pbinternal.TreeNode{nil},
				})
			},
			wantErr: "nodes[0] is required",
		},
		{
			name: "rollback nil request",
			call: func() error {
				_, err := handler.RollbackUtxoSwap(t.Context(), nil, nil)
				return err
			},
			wantErr: "request is required",
		},
		{
			name: "rollback missing utxo",
			call: func() error {
				_, err := handler.RollbackUtxoSwap(t.Context(), nil, &pbinternal.RollbackUtxoSwapRequest{})
				return err
			},
			wantErr: "on_chain_utxo is required",
		},
		{
			name: "instant rollback nil request",
			call: func() error {
				_, err := handler.RollbackInstantUtxoSwap(t.Context(), nil, nil)
				return err
			},
			wantErr: "request is required",
		},
		{
			name: "instant rollback missing utxo",
			call: func() error {
				_, err := handler.RollbackInstantUtxoSwap(t.Context(), nil, &pbinternal.RollbackInstantUtxoSwapRequest{})
				return err
			},
			wantErr: "on_chain_utxo is required",
		},
		{
			name: "completed nil request",
			call: func() error {
				_, err := handler.UtxoSwapCompleted(t.Context(), nil, nil)
				return err
			},
			wantErr: "request is required",
		},
		{
			name: "completed missing utxo",
			call: func() error {
				_, err := handler.UtxoSwapCompleted(t.Context(), nil, &pbinternal.UtxoSwapCompletedRequest{})
				return err
			},
			wantErr: "on_chain_utxo is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var err error
			require.NotPanics(t, func() {
				err = tt.call()
			})
			require.ErrorContains(t, err, tt.wantErr)
			require.Equal(t, codes.InvalidArgument, status.Code(err))
		})
	}
}
