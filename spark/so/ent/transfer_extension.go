package ent

import (
	"context"
	"fmt"

	pb "github.com/lightsparkdev/spark/proto/spark"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// MarshalProto converts a Transfer to a spark protobuf Transfer.
// To marshal the spark invoice, the edge must be pre-loaded via Transfer.WithSparkInvoice().
func (t *Transfer) MarshalProto(ctx context.Context) (*pb.Transfer, error) {
	leaves, err := t.QueryTransferLeaves().All(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to query transfer leaves for transfer %s: %w", t.ID.String(), err)
	}
	var leavesProto []*pb.TransferLeaf
	for _, leaf := range leaves {
		leafProto, err := leaf.MarshalProto(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to marshal transfer leaf %s: %w", leaf.ID.String(), err)
		}
		leavesProto = append(leavesProto, leafProto)
	}

	status, err := t.getProtoStatus()
	if err != nil {
		return nil, err
	}
	transferType, err := TransferTypeProto(t.Type)
	if err != nil {
		return nil, err
	}
	invoice := ""
	if inv := t.Edges.SparkInvoice; inv != nil {
		invoice = inv.SparkInvoice
	}
	return &pb.Transfer{
		Id:                        t.ID.String(),
		SenderIdentityPublicKey:   t.SenderIdentityPubkey.Serialize(),
		ReceiverIdentityPublicKey: t.ReceiverIdentityPubkey.Serialize(),
		Status:                    *status,
		TotalValue:                t.TotalValue,
		ExpiryTime:                timestamppb.New(t.ExpiryTime),
		Leaves:                    leavesProto,
		CreatedTime:               timestamppb.New(t.CreateTime),
		UpdatedTime:               timestamppb.New(t.UpdateTime),
		Type:                      *transferType,
		SparkInvoice:              invoice,
	}, nil
}

func (t *Transfer) getProtoStatus() (*pb.TransferStatus, error) {
	switch t.Status {
	case st.TransferStatusSenderInitiated:
		return pb.TransferStatus_TRANSFER_STATUS_SENDER_INITIATED.Enum(), nil
	case st.TransferStatusSenderKeyTweakPending:
		return pb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING.Enum(), nil
	case st.TransferStatusSenderKeyTweaked:
		return pb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED.Enum(), nil
	case st.TransferStatusReceiverKeyTweaked:
		return pb.TransferStatus_TRANSFER_STATUS_RECEIVER_KEY_TWEAKED.Enum(), nil
	case st.TransferStatusReceiverRefundSigned:
		return pb.TransferStatus_TRANSFER_STATUS_RECEIVER_REFUND_SIGNED.Enum().Enum(), nil
	case st.TransferStatusCompleted:
		return pb.TransferStatus_TRANSFER_STATUS_COMPLETED.Enum(), nil
	case st.TransferStatusExpired:
		return pb.TransferStatus_TRANSFER_STATUS_EXPIRED.Enum(), nil
	case st.TransferStatusReturned:
		return pb.TransferStatus_TRANSFER_STATUS_RETURNED.Enum(), nil
	case st.TransferStatusSenderInitiatedCoordinator:
		return pb.TransferStatus_TRANSFER_STATUS_SENDER_INITIATED_COORDINATOR.Enum(), nil
	case st.TransferStatusReceiverKeyTweakLocked:
		return pb.TransferStatus_TRANSFER_STATUS_RECEIVER_KEY_TWEAK_LOCKED.Enum(), nil
	case st.TransferStatusReceiverKeyTweakApplied:
		return pb.TransferStatus_TRANSFER_STATUS_RECEIVER_KEY_TWEAK_APPLIED.Enum(), nil
	}
	return nil, fmt.Errorf("unknown transfer status %s", t.Status)
}

func TransferStatusSchema(transferStatusProto pb.TransferStatus) (st.TransferStatus, error) {
	switch transferStatusProto {
	case pb.TransferStatus_TRANSFER_STATUS_SENDER_INITIATED:
		return st.TransferStatusSenderInitiated, nil
	case pb.TransferStatus_TRANSFER_STATUS_SENDER_INITIATED_COORDINATOR:
		return st.TransferStatusSenderInitiatedCoordinator, nil
	case pb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING:
		return st.TransferStatusSenderKeyTweakPending, nil
	case pb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED:
		return st.TransferStatusSenderKeyTweaked, nil
	case pb.TransferStatus_TRANSFER_STATUS_RECEIVER_KEY_TWEAKED:
		return st.TransferStatusReceiverKeyTweaked, nil
	case pb.TransferStatus_TRANSFER_STATUS_RECEIVER_KEY_TWEAK_LOCKED:
		return st.TransferStatusReceiverKeyTweakLocked, nil
	case pb.TransferStatus_TRANSFER_STATUS_RECEIVER_KEY_TWEAK_APPLIED:
		return st.TransferStatusReceiverKeyTweakApplied, nil
	case pb.TransferStatus_TRANSFER_STATUS_RECEIVER_REFUND_SIGNED:
		return st.TransferStatusReceiverRefundSigned, nil
	case pb.TransferStatus_TRANSFER_STATUS_COMPLETED:
		return st.TransferStatusCompleted, nil
	case pb.TransferStatus_TRANSFER_STATUS_EXPIRED:
		return st.TransferStatusExpired, nil
	case pb.TransferStatus_TRANSFER_STATUS_RETURNED:
		return st.TransferStatusReturned, nil
	default:
		return "", fmt.Errorf("unknown transfer status: %v", transferStatusProto)
	}
}

func TransferTypeProto(transferType st.TransferType) (*pb.TransferType, error) {
	switch transferType {
	case st.TransferTypePreimageSwap:
		return pb.TransferType_PREIMAGE_SWAP.Enum(), nil
	case st.TransferTypeCooperativeExit:
		return pb.TransferType_COOPERATIVE_EXIT.Enum(), nil
	case st.TransferTypeTransfer:
		return pb.TransferType_TRANSFER.Enum(), nil
	case st.TransferTypeSwap:
		return pb.TransferType_SWAP.Enum(), nil
	case st.TransferTypeCounterSwap:
		return pb.TransferType_COUNTER_SWAP.Enum(), nil
	case st.TransferTypeUtxoSwap:
		return pb.TransferType_UTXO_SWAP.Enum(), nil
	}
	return nil, fmt.Errorf("unknown transfer type %s", transferType)
}

func TransferTypeSchema(transferType pb.TransferType) (st.TransferType, error) {
	switch transferType {
	case pb.TransferType_PREIMAGE_SWAP:
		return st.TransferTypePreimageSwap, nil
	case pb.TransferType_COOPERATIVE_EXIT:
		return st.TransferTypeCooperativeExit, nil
	case pb.TransferType_TRANSFER:
		return st.TransferTypeTransfer, nil
	case pb.TransferType_SWAP:
		return st.TransferTypeSwap, nil
	case pb.TransferType_COUNTER_SWAP:
		return st.TransferTypeCounterSwap, nil
	case pb.TransferType_UTXO_SWAP:
		return st.TransferTypeUtxoSwap, nil
	}
	return "", fmt.Errorf("unknown transfer type %s", transferType)
}
