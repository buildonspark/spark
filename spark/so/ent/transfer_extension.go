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
	return &pb.Transfer{
		Id:                        t.ID.String(),
		SenderIdentityPublicKey:   t.SenderIdentityPubkey,
		ReceiverIdentityPublicKey: t.ReceiverIdentityPubkey,
		Status:                    *status,
		TotalValue:                t.TotalValue,
		ExpiryTime:                timestamppb.New(t.ExpiryTime),
		Leaves:                    leavesProto,
	}, nil
}

func (t *Transfer) getProtoStatus() (*pb.TransferStatus, error) {
	switch t.Status {
	case schema.TransferStatusInitiated:
		return pb.TransferStatus_TRANSFER_STATUS_INITIATED.Enum(), nil
	case schema.TransferStatusClaiming:
		return pb.TransferStatus_TRANSFER_STATUS_CLAIMING.Enum(), nil
	case schema.TransferStatusCompleted:
		return pb.TransferStatus_TRANSFER_STATUS_COMPLETED.Enum(), nil
	case schema.TransferStatusExpired:
		return pb.TransferStatus_TRANSFER_STATUS_EXPIRED.Enum(), nil
	}
	return nil, fmt.Errorf("unknown transfer status %s", t.Status)
}
