//go:build lightspark

package handler

import (
	"testing"

	pbssp "github.com/lightsparkdev/spark/proto/spark_ssp_internal"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestSspQueryHandlersRejectNilRequestsWithoutPanic(t *testing.T) {
	handler := NewSspRequestHandler(nil)

	tests := []struct {
		name string
		call func() error
	}{
		{
			name: "return stuck transfers",
			call: func() error {
				_, err := handler.ReturnStuckTransfers(t.Context(), nil)
				return err
			},
		},
		{
			name: "get stuck lightning payments",
			call: func() error {
				_, err := handler.GetStuckLightningPayments(t.Context(), nil)
				return err
			},
		},
		{
			name: "get stuck transfers",
			call: func() error {
				_, err := handler.GetStuckTransfers(t.Context(), nil)
				return err
			},
		},
		{
			name: "query stuck transfer",
			call: func() error {
				_, err := handler.QueryStuckTransfer(t.Context(), nil)
				return err
			},
		},
		{
			name: "cancel stuck transfer",
			call: func() error {
				_, err := handler.CancelStuckTransfer(t.Context(), nil)
				return err
			},
		},
		{
			name: "query node transfer history",
			call: func() error {
				_, err := handler.QueryNodeTransferHistory(t.Context(), nil)
				return err
			},
		},
		{
			name: "query pending preimage swap transfer",
			call: func() error {
				_, err := handler.QueryPendingPreimageSwapTransfer(t.Context(), nil)
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var err error
			require.NotPanics(t, func() {
				err = tt.call()
			})
			require.ErrorContains(t, err, "request is required")
			require.Equal(t, codes.InvalidArgument, status.Code(err))
		})
	}
}

func TestSspQueryHandlersRejectNegativeOffsetsBeforeDatabase(t *testing.T) {
	handler := NewSspRequestHandler(nil)

	tests := []struct {
		name string
		call func() error
	}{
		{
			name: "get stuck lightning payments",
			call: func() error {
				_, err := handler.GetStuckLightningPayments(t.Context(), &pbssp.GetStuckLightningPaymentsRequest{Offset: -1})
				return err
			},
		},
		{
			name: "get stuck transfers",
			call: func() error {
				_, err := handler.GetStuckTransfers(t.Context(), &pbssp.GetStuckTransfersRequest{Offset: -1})
				return err
			},
		},
		{
			name: "query node transfer history",
			call: func() error {
				_, err := handler.QueryNodeTransferHistory(t.Context(), &pbssp.QueryNodeTransferHistoryRequest{Offset: -1})
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call()
			require.ErrorContains(t, err, "offset must be non-negative")
			require.Equal(t, codes.InvalidArgument, status.Code(err))
		})
	}
}
