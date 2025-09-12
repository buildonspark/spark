package helper

import (
	"testing"

	"github.com/lightsparkdev/spark/proto/common"
	"github.com/lightsparkdev/spark/proto/lrc20"
	"github.com/lightsparkdev/spark/proto/spark"
	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestCountMessageTypeInProto(t *testing.T) {
	tests := []struct {
		name       string
		message    proto.Message
		targetType protoreflect.FullName
		want       int
	}{
		{
			name:       "simple message matches itself",
			message:    &common.PackageMap{},
			targetType: "common.PackageMap",
			want:       1,
		},
		{
			name:       "message not matching target type",
			message:    &common.PackageMap{},
			targetType: "common.SigningCommitment",
			want:       0,
		},
		{
			name: "message with nested message field",
			message: &spark.TransferEvent{
				Transfer: &spark.Transfer{},
			},
			targetType: "spark.Transfer",
			want:       1,
		},
		{
			name: "message with repeated message field",
			message: &lrc20.ListSparkTxsResponse{
				TokenTransactions: []*lrc20.TokenTransactionResponse{
					{Finalized: true},
					{Finalized: true},
				},
			},
			targetType: "rpc.v1.TokenTransactionResponse",
			want:       2,
		},
		{
			name: "message with map containing byte values only counts container",
			message: &common.PackageMap{
				Packages: map[string][]byte{
					"key1": []byte("value1"),
					"key2": []byte("value2"),
				},
			},
			targetType: "common.PackageMap",
			want:       1,
		},
		{
			name: "deeply nested messages",
			message: &lrc20.ListSparkTxsResponse{
				TokenTransactions: []*lrc20.TokenTransactionResponse{
					{
						FinalTokenTransaction: &spark.TokenTransaction{},
					},
					{
						FinalTokenTransaction: &spark.TokenTransaction{},
					},
				},
			},
			targetType: "spark.TokenTransaction",
			want:       2,
		},
		{
			name: "count both parent and nested of same type",
			message: &spark.SubscribeToEventsResponse{
				Event: &spark.SubscribeToEventsResponse_Transfer{
					Transfer: &spark.TransferEvent{
						Transfer: &spark.Transfer{},
					},
				},
			},
			targetType: "spark.TransferEvent",
			want:       1,
		},
		{
			name: "count with oneof field",
			message: &spark.SubscribeToEventsResponse{
				Event: &spark.SubscribeToEventsResponse_Deposit{
					Deposit: &spark.DepositEvent{},
				},
			},
			targetType: "spark.DepositEvent",
			want:       1,
		},
		{
			name:       "non-existent message type",
			message:    &common.PackageMap{},
			targetType: "nonexistent.MessageType",
			want:       0,
		},
		{
			name:       "nil message",
			message:    (*common.PackageMap)(nil),
			targetType: "common.PackageMap",
			want:       1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			count := countMessageType(tt.message.ProtoReflect(), tt.targetType)
			assert.Equal(t, tt.want, count)
		})
	}
}
