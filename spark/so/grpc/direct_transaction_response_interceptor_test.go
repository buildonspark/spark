package grpc

import (
	"bytes"
	"context"
	"testing"

	"github.com/btcsuite/btcd/wire"
	"github.com/lightsparkdev/spark/common"
	pbspark "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/metric"
	googlegrpc "google.golang.org/grpc"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func signedResponseTransaction(t *testing.T) []byte {
	t.Helper()

	tx := wire.NewMsgTx(3)
	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Index: 0},
		Sequence:         1,
		Witness:          wire.TxWitness{bytes.Repeat([]byte{0x42}, 64)},
	})
	tx.AddTxOut(&wire.TxOut{Value: 1000, PkScript: []byte{0x51}})
	rawTx, err := common.SerializeTx(tx)
	require.NoError(t, err)
	return rawTx
}

func transferResponseWithSignedDirectTransactions(t *testing.T) *pbspark.QueryTransfersResponse {
	t.Helper()

	signedTx := signedResponseTransaction(t)
	return &pbspark.QueryTransfersResponse{
		Transfers: []*pbspark.Transfer{{
			Leaves: []*pbspark.TransferLeaf{{
				Leaf: &pbspark.TreeNode{
					NodeTx:                 signedTx,
					DirectTx:               signedTx,
					DirectRefundTx:         signedTx,
					DirectFromCpfpRefundTx: signedTx,
				},
				Sig:                                &pbspark.TransferLeaf_Signature{Signature: []byte("user signature")},
				IntermediateRefundTx:               signedTx,
				IntermediateDirectRefundTx:         signedTx,
				IntermediateDirectFromCpfpRefundTx: signedTx,
			}},
		}},
	}
}

func requireWitness(t *testing.T, rawTx []byte, expected bool) {
	t.Helper()

	tx, err := common.TxFromRawTxBytes(rawTx)
	require.NoError(t, err)
	require.Equal(t, expected, tx.HasWitness())
}

func TestDirectTransactionResponseInterceptorStripsExternalResponses(t *testing.T) {
	tests := []struct {
		name       string
		fullMethod string
	}{
		{name: "user", fullMethod: pbspark.SparkService_QueryAllTransfers_FullMethodName},
		{name: "SSP", fullMethod: "/spark_ssp_internal.SparkSspInternalService/query_transfers"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := transferResponseWithSignedDirectTransactions(t)
			originalLeaf := response.GetTransfers()[0].GetLeaves()[0]
			interceptor := DirectTransactionResponseInterceptor()

			result, err := interceptor(t.Context(), nil, &googlegrpc.UnaryServerInfo{FullMethod: test.fullMethod}, func(context.Context, any) (any, error) {
				return response, nil
			})
			require.NoError(t, err)

			leaf := result.(*pbspark.QueryTransfersResponse).GetTransfers()[0].GetLeaves()[0]
			requireWitness(t, leaf.GetLeaf().GetDirectTx(), false)
			requireWitness(t, leaf.GetLeaf().GetDirectRefundTx(), false)
			requireWitness(t, leaf.GetLeaf().GetDirectFromCpfpRefundTx(), false)
			requireWitness(t, leaf.GetIntermediateDirectRefundTx(), false)
			requireWitness(t, leaf.GetIntermediateDirectFromCpfpRefundTx(), false)
			requireWitness(t, leaf.GetLeaf().GetNodeTx(), true)
			requireWitness(t, leaf.GetIntermediateRefundTx(), true)
			require.Equal(t, []byte("user signature"), leaf.GetSignature())
			requireWitness(t, originalLeaf.GetLeaf().GetDirectTx(), true)
		})
	}
}

func TestDirectTransactionResponseInterceptorPreservesOperatorResponses(t *testing.T) {
	signedTx := signedResponseTransaction(t)
	response := &pbspark.QueryNodesResponse{
		Nodes: map[string]*pbspark.TreeNode{
			"node": {DirectTx: signedTx},
		},
	}
	interceptor := DirectTransactionResponseInterceptor()

	result, err := interceptor(t.Context(), nil, &googlegrpc.UnaryServerInfo{FullMethod: pbinternal.SparkInternalService_QueryNodes_FullMethodName}, func(context.Context, any) (any, error) {
		return response, nil
	})
	require.NoError(t, err)
	require.Same(t, response, result)
	requireWitness(t, result.(*pbspark.QueryNodesResponse).GetNodes()["node"].GetDirectTx(), true)
}

func TestDirectTransactionResponseInterceptorLeavesIrrelevantResponsesUncloned(t *testing.T) {
	response := &pbspark.QueryBalanceResponse{}
	interceptor := DirectTransactionResponseInterceptor()

	result, err := interceptor(t.Context(), nil, &googlegrpc.UnaryServerInfo{FullMethod: pbspark.SparkService_QueryBalance_FullMethodName}, func(context.Context, any) (any, error) {
		return response, nil
	})
	require.NoError(t, err)
	require.Same(t, response, result)
}

type responseRecordingServerStream struct {
	googlegrpc.ServerStream
	ctx      context.Context
	response any
}

func (s *responseRecordingServerStream) Context() context.Context {
	return s.ctx
}

func (s *responseRecordingServerStream) SendMsg(response any) error {
	s.response = response
	return nil
}

func TestDirectTransactionResponseStreamInterceptorStripsExternalResponses(t *testing.T) {
	signedTx := signedResponseTransaction(t)
	response := &pbspark.SubscribeToEventsResponse{
		Event: &pbspark.SubscribeToEventsResponse_Deposit{
			Deposit: &pbspark.DepositEvent{
				Deposit: &pbspark.TreeNode{DirectTx: signedTx},
			},
		},
	}
	stream := &responseRecordingServerStream{ctx: t.Context()}
	interceptor := DirectTransactionResponseStreamInterceptor()

	err := interceptor(nil, stream, &googlegrpc.StreamServerInfo{FullMethod: pbspark.SparkService_SubscribeToEvents_FullMethodName}, func(_ any, stream googlegrpc.ServerStream) error {
		return stream.SendMsg(response)
	})
	require.NoError(t, err)

	result := stream.response.(*pbspark.SubscribeToEventsResponse)
	requireWitness(t, result.GetDeposit().GetDeposit().GetDirectTx(), false)
	requireWitness(t, response.GetDeposit().GetDeposit().GetDirectTx(), true)
}

func TestDirectTransactionResponseInterceptorClearsMalformedExternalTransaction(t *testing.T) {
	counter := &recordingInt64Counter{}
	previousCounter := directTransactionWitnessSanitizationFailures
	directTransactionWitnessSanitizationFailures = counter
	t.Cleanup(func() {
		directTransactionWitnessSanitizationFailures = previousCounter
	})

	response := &pbspark.QueryNodesResponse{
		Nodes: map[string]*pbspark.TreeNode{
			"node": {DirectTx: []byte{0x01}},
		},
	}
	interceptor := DirectTransactionResponseInterceptor()

	result, err := interceptor(t.Context(), nil, &googlegrpc.UnaryServerInfo{FullMethod: pbspark.SparkService_QueryNodes_FullMethodName}, func(context.Context, any) (any, error) {
		return response, nil
	})
	require.NoError(t, err)
	require.Empty(t, result.(*pbspark.QueryNodesResponse).GetNodes()["node"].GetDirectTx())
	require.Equal(t, []byte{0x01}, response.GetNodes()["node"].GetDirectTx())
	require.Equal(t, int64(1), counter.value)
}

type recordingInt64Counter struct {
	metric.Int64Counter
	value int64
}

func (c *recordingInt64Counter) Add(_ context.Context, value int64, _ ...metric.AddOption) {
	c.value += value
}

func TestDirectTransactionResponseRegistryMatchesPublicSchema(t *testing.T) {
	expected := map[protoreflect.FullName][]protoreflect.Name{
		"spark.TreeNode": {
			"direct_tx",
			"direct_refund_tx",
			"direct_from_cpfp_refund_tx",
		},
		"spark.TransferLeaf": {
			"intermediate_direct_refund_tx",
			"intermediate_direct_from_cpfp_refund_tx",
		},
	}
	if _, ok := directTransactionResponses.directFields["spark_ssp_internal.TreeVizNode"]; ok {
		expected["spark_ssp_internal.TreeVizNode"] = []protoreflect.Name{
			"direct_tx",
			"direct_refund_tx",
			"direct_from_cpfp_refund_tx",
		}
	}

	actual := make(map[protoreflect.FullName][]protoreflect.Name, len(directTransactionResponses.directFields))
	for messageName, fields := range directTransactionResponses.directFields {
		for _, field := range fields {
			actual[messageName] = append(actual[messageName], field.Name())
		}
	}
	require.Equal(t, expected, actual)
}
