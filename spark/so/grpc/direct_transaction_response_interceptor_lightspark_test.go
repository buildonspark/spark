//go:build lightspark

package grpc

import (
	"context"
	"testing"

	pbssp "github.com/lightsparkdev/spark/proto/spark_ssp_internal"
	"github.com/stretchr/testify/require"
	googlegrpc "google.golang.org/grpc"
)

func TestDirectTransactionResponseInterceptorStripsTreeVizNodeResponses(t *testing.T) {
	signedTx := signedResponseTransaction(t)
	response := &pbssp.TreeVizGetTreeSnapshotResponse{
		Nodes: []*pbssp.TreeVizNode{{
			RawTx:                  signedTx,
			DirectTx:               signedTx,
			DirectRefundTx:         signedTx,
			DirectFromCpfpRefundTx: signedTx,
		}},
	}
	interceptor := DirectTransactionResponseInterceptor()

	result, err := interceptor(t.Context(), nil, &googlegrpc.UnaryServerInfo{FullMethod: pbssp.SparkSspInternalService_GetTreeSnapshot_FullMethodName}, func(context.Context, any) (any, error) {
		return response, nil
	})
	require.NoError(t, err)

	node := result.(*pbssp.TreeVizGetTreeSnapshotResponse).GetNodes()[0]
	requireWitness(t, node.GetDirectTx(), false)
	requireWitness(t, node.GetDirectRefundTx(), false)
	requireWitness(t, node.GetDirectFromCpfpRefundTx(), false)
	requireWitness(t, node.GetRawTx(), true)
	requireWitness(t, response.GetNodes()[0].GetDirectTx(), true)
}
