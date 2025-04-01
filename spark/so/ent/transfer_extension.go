package ent

import (
	"context"
	"fmt"

	pb "github.com/lightsparkdev/spark-go/proto/spark"
	"github.com/lightsparkdev/spark-go/so/ent/schema"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// MarshalProto converts a Transfer to a spark protobuf Transfer.
func (t *Transfer) MarshalProto(ctx context.Context) (*pb.Transfer, error) {
	leaves, err := t.QueryTransferLeaves().All(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to query transfer leaves for transfer %s: %v", t.ID.String(), err)
	}
	leavesProto := []*pb.TransferLeaf{}
	for _, leaf := range leaves {
		leafProto, err := leaf.MarshalProto(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to marshal transfer leaf %s: %v", leaf.ID.String(), err)
		}
		leavesProto = append(leavesProto, leafProto)
	}

	status, err := t.getProtoStatus()
	if err != nil {
		return nil, err
	}
	transferType, err := t.getProtoType()
	if err != nil {
		return nil, err
	}
	return &pb.Transfer{
		Id:                        t.ID.String(),
		SenderIdentityPublicKey:   t.SenderIdentityPubkey,
		ReceiverIdentityPublicKey: t.ReceiverIdentityPubkey,
		Status:                    *status,
		TotalValue:                t.TotalValue,
		ExpiryTime:                timestamppb.New(t.ExpiryTime),
		Leaves:                    leavesProto,
		CreatedTime:               timestamppb.New(t.CreateTime),
		UpdatedTime:               timestamppb.New(t.UpdateTime),
		Type:                      *transferType,
	}, nil
}

func (t *Transfer) getProtoStatus() (*pb.TransferStatus, error) {
	switch t.Status {
	case schema.TransferStatusSenderInitiated:
		return pb.TransferStatus_TRANSFER_STATUS_SENDER_INITIATED.Enum(), nil
	case schema.TransferStatusSenderKeyTweakPending:
		return pb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING.Enum(), nil
	case schema.TransferStatusSenderKeyTweaked:
		return pb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED.Enum(), nil
	case schema.TransferStatusReceiverKeyTweaked:
		return pb.TransferStatus_TRANSFER_STATUS_RECEIVER_KEY_TWEAKED.Enum(), nil
	case schema.TransferStatusReceiverRefundSigned:
		return pb.TransferStatus_TRANSFER_STATUSR_RECEIVER_REFUND_SIGNED.Enum().Enum(), nil
	case schema.TransferStatusCompleted:
		return pb.TransferStatus_TRANSFER_STATUS_COMPLETED.Enum(), nil
	case schema.TransferStatusExpired:
		return pb.TransferStatus_TRANSFER_STATUS_EXPIRED.Enum(), nil
	case schema.TransferStatusReturned:
		return pb.TransferStatus_TRANSFER_STATUS_RETURNED.Enum(), nil
	}
	return nil, fmt.Errorf("unknown transfer status %s", t.Status)
}

func (t *Transfer) getProtoType() (*pb.TransferType, error) {
	switch t.Type {
	case schema.TransferTypePreimageSwap:
		return pb.TransferType_PREIMAGE_SWAP.Enum(), nil
	case schema.TransferTypeCooperativeExit:
		return pb.TransferType_COOPERATIVE_EXIT.Enum(), nil
	case schema.TransferTypeTransfer:
		return pb.TransferType_TRANSFER.Enum(), nil
	case schema.TransferTypeSwap:
		return pb.TransferType_SWAP.Enum(), nil
	case schema.TransferTypeCounterSwap:
		return pb.TransferType_COUNTER_SWAP.Enum(), nil
	}
	return nil, fmt.Errorf("unknown transfer type %s", t.Type)
}
