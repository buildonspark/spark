package ent

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/logging"
	pb "github.com/lightsparkdev/spark/proto/spark"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/transferleaf"
	"github.com/lightsparkdev/spark/so/knobs"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// shouldEmitMultiParticipantFormat reports whether MarshalProto-family functions
// should populate the multi-participant Senders[]/Receivers[] fields on the
// output proto. Controlled by KnobReadMIMOMultiParticipantFormat; off by default.
func shouldEmitMultiParticipantFormat(ctx context.Context) bool {
	return knobs.GetKnobsService(ctx).GetValue(knobs.KnobReadMIMOMultiParticipantFormat, 0) > 0
}

// MarshalProto converts a Transfer to a spark protobuf Transfer.
// To marshal the spark invoice, the edge must be pre-loaded via Transfer.WithSparkInvoice().
// When KnobReadMIMOMultiParticipantFormat is enabled, the Senders[] and Receivers[]
// fields are populated from the TransferSenders/TransferReceivers edges; callers
// should pre-load both via WithTransferSenders()/WithTransferReceivers(). A warning
// is logged for any edge that is not pre-loaded while the knob is on.
// If TransferLeaves (with nested Leaf→Tree/SigningKeyshare/Parent) is pre-loaded, that
// slice is reused; otherwise leaves are lazy-loaded.
func (t *Transfer) MarshalProto(ctx context.Context) (*pb.Transfer, error) {
	leaves := t.Edges.TransferLeaves
	if leaves == nil {
		var err error
		leaves, err = t.QueryTransferLeaves().WithLeaf(func(q *TreeNodeQuery) {
			q.WithTree().WithSigningKeyshare().WithParent()
		}).All(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to query transfer leaves for transfer %s: %w", t.ID, err)
		}
	}
	emitMIMO := shouldEmitMultiParticipantFormat(ctx)
	if emitMIMO {
		t.warnIfParticipantEdgesMissing(ctx, "MarshalProto", true)
	}
	return t.marshalWithLeavesAndReceivers(ctx, leaves, t.Edges.TransferReceivers, emitMIMO)
}

// MarshalProtoForReceiver converts a Transfer to a protobuf Transfer,
// filtering leaves AND the emitted Receivers list to only the given receiver.
// The Transfer's TransferReceivers edge must be pre-loaded (WithTransferReceivers).
// Returns an error if the receiver is not found in this transfer.
// If TransferLeaves is pre-loaded, the receiver filter is applied in-memory;
// otherwise leaves are lazy-loaded with a SQL-side filter.
// When KnobReadMIMOMultiParticipantFormat is enabled, the Senders[] field is
// populated from the TransferSenders edge; callers should pre-load it via
// WithTransferSenders(). A warning is logged if the edge is missing while the
// knob is on.
func (t *Transfer) MarshalProtoForReceiver(ctx context.Context, receiverPubkey keys.Public) (*pb.Transfer, error) {
	if t.Edges.TransferReceivers == nil {
		return nil, fmt.Errorf("TransferReceivers edge not pre-loaded for transfer %s", t.ID)
	}
	receiverID, found := t.findReceiverID(receiverPubkey)
	if !found {
		return nil, fmt.Errorf("receiver %s not found in transfer %s", receiverPubkey, t.ID)
	}
	leaves := t.Edges.TransferLeaves
	if leaves != nil {
		var filtered []*TransferLeaf
		for _, leaf := range leaves {
			if leaf.TransferReceiverID != nil && *leaf.TransferReceiverID == receiverID {
				filtered = append(filtered, leaf)
			}
		}
		leaves = filtered
	} else {
		var err error
		leaves, err = t.QueryTransferLeaves().
			WithLeaf(func(q *TreeNodeQuery) {
				q.WithTree().WithSigningKeyshare().WithParent()
			}).
			Where(transferleaf.TransferReceiverIDEQ(receiverID)).
			All(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to query transfer leaves for transfer %s: %w", t.ID, err)
		}
	}
	var receiverOnly []*TransferReceiver
	for _, r := range t.Edges.TransferReceivers {
		if r.ID == receiverID {
			receiverOnly = append(receiverOnly, r)
			break
		}
	}
	emitMIMO := shouldEmitMultiParticipantFormat(ctx)
	if emitMIMO {
		// TransferReceivers is guaranteed loaded by the early return above; only
		// Senders is worth checking here.
		t.warnIfParticipantEdgesMissing(ctx, "MarshalProtoForReceiver", false)
	}
	return t.marshalWithLeavesAndReceivers(ctx, leaves, receiverOnly, emitMIMO)
}

// warnIfParticipantEdgesMissing logs a warning AND increments
// spark_transfer_marshal_missing_edge_total when the participant edges that
// MarshalProto-family functions need to populate Senders[]/Receivers[] are not
// pre-loaded. Only called when the multi-participant format knob is on.
// checkReceivers should be false for MarshalProtoForReceiver, which guarantees
// TransferReceivers is loaded via its own precondition check.
//
// TODO(SP-3161): once spark_transfer_marshal_missing_edge_total holds at zero, promote to hard error.
func (t *Transfer) warnIfParticipantEdgesMissing(ctx context.Context, caller string, checkReceivers bool) {
	logger := logging.GetLoggerFromContext(ctx)
	if checkReceivers && t.Edges.TransferReceivers == nil {
		logger.Sugar().Warnf("%s: TransferReceivers not pre-loaded for transfer %s; emitting empty Receivers[]", caller, t.ID)
		transferMarshalMissingEdgeCounter.Add(ctx, 1, metric.WithAttributes(
			attribute.String("caller", caller),
			attribute.String("edge", "TransferReceivers"),
		))
	}
	if t.Edges.TransferSenders == nil {
		logger.Sugar().Warnf("%s: TransferSenders not pre-loaded for transfer %s; emitting empty Senders[]", caller, t.ID)
		transferMarshalMissingEdgeCounter.Add(ctx, 1, metric.WithAttributes(
			attribute.String("caller", caller),
			attribute.String("edge", "TransferSenders"),
		))
	}
}

// HasReceiver reports whether the given pubkey matches a TransferReceiver on this transfer.
// Requires the TransferReceivers edge to be pre-loaded.
func (t *Transfer) HasReceiver(pubkey keys.Public) bool {
	_, found := t.findReceiverID(pubkey)
	return found
}

// findReceiverID looks up the TransferReceiver ID for a given identity pubkey.
// Requires TransferReceivers edge to be pre-loaded.
func (t *Transfer) findReceiverID(pubkey keys.Public) (uuid.UUID, bool) {
	for _, r := range t.Edges.TransferReceivers {
		if r.IdentityPubkey.Equals(pubkey) {
			return r.ID, true
		}
	}
	return uuid.UUID{}, false
}

func (t *Transfer) marshalWithLeavesAndReceivers(ctx context.Context, leaves []*TransferLeaf, receiverEdges []*TransferReceiver, emitMultiParticipantFormat bool) (*pb.Transfer, error) {
	var leavesProto []*pb.TransferLeaf
	for _, leaf := range leaves {
		treeNode := leaf.Edges.Leaf
		if treeNode == nil {
			return nil, fmt.Errorf("tree node not pre-loaded for transfer leaf %s", leaf.ID)
		}
		leafProto, err := leaf.marshalTransferLeafProto(ctx, treeNode)
		if err != nil {
			return nil, fmt.Errorf("unable to marshal transfer leaf %s: %w", leaf.ID, err)
		}
		leavesProto = append(leavesProto, leafProto)
	}

	var receivers []*pb.TransferReceiver
	var senders []*pb.TransferSender
	if emitMultiParticipantFormat {
		amountByReceiver := make(map[uuid.UUID]uint64, len(receiverEdges))
		for _, leaf := range leaves {
			if leaf.TransferReceiverID != nil {
				amountByReceiver[*leaf.TransferReceiverID] += leaf.Edges.Leaf.Value
			}
		}
		for _, r := range receiverEdges {
			receiverStatus, err := TransferReceiverStatusProto(r.Status)
			if err != nil {
				return nil, err
			}
			receiver := &pb.TransferReceiver{
				Id:                r.ID.String(),
				IdentityPublicKey: r.IdentityPubkey.Serialize(),
				AmountSats:        amountByReceiver[r.ID],
				Status:            *receiverStatus,
			}
			if !r.CompletionTime.IsZero() {
				receiver.CompletionTime = timestamppb.New(r.CompletionTime)
			}
			receivers = append(receivers, receiver)
		}

		for _, s := range t.Edges.TransferSenders {
			senders = append(senders, &pb.TransferSender{
				Id:                s.ID.String(),
				IdentityPublicKey: s.IdentityPubkey.Serialize(),
			})
		}
	}

	status, err := t.getProtoStatus()
	if err != nil {
		return nil, err
	}
	network, err := t.Network.MarshalProto()
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
		Network:                   network,
		Receivers:                 receivers,
		Senders:                   senders,
	}, nil
}

func (t *Transfer) getProtoStatus() (*pb.TransferStatus, error) {
	switch t.Status {
	case st.TransferStatusSenderInitiated:
		return pb.TransferStatus_TRANSFER_STATUS_SENDER_INITIATED.Enum(), nil
	case st.TransferStatusSenderKeyTweakPending:
		return pb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING.Enum(), nil
	case st.TransferStatusApplyingSenderKeyTweak:
		return pb.TransferStatus_TRANSFER_STATUS_APPLYING_SENDER_KEY_TWEAK.Enum(), nil
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
	case pb.TransferStatus_TRANSFER_STATUS_APPLYING_SENDER_KEY_TWEAK:
		return st.TransferStatusApplyingSenderKeyTweak, nil
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

func TransferReceiverStatusProto(s st.TransferReceiverStatus) (*pb.TransferReceiverStatus, error) {
	switch s {
	case st.TransferReceiverStatusInitiated:
		return pb.TransferReceiverStatus_TRANSFER_RECEIVER_STATUS_INITIATED.Enum(), nil
	case st.TransferReceiverStatusReceiverClaimPending:
		return pb.TransferReceiverStatus_TRANSFER_RECEIVER_STATUS_CLAIM_PENDING.Enum(), nil
	case st.TransferReceiverStatusKeyTweaked:
		return pb.TransferReceiverStatus_TRANSFER_RECEIVER_STATUS_KEY_TWEAKED.Enum(), nil
	case st.TransferReceiverStatusKeyTweakLocked:
		return pb.TransferReceiverStatus_TRANSFER_RECEIVER_STATUS_KEY_TWEAK_LOCKED.Enum(), nil
	case st.TransferReceiverStatusKeyTweakApplied:
		return pb.TransferReceiverStatus_TRANSFER_RECEIVER_STATUS_KEY_TWEAK_APPLIED.Enum(), nil
	case st.TransferReceiverStatusRefundSigned:
		return pb.TransferReceiverStatus_TRANSFER_RECEIVER_STATUS_REFUND_SIGNED.Enum(), nil
	case st.TransferReceiverStatusCompleted:
		return pb.TransferReceiverStatus_TRANSFER_RECEIVER_STATUS_COMPLETED.Enum(), nil
	case st.TransferReceiverStatusCancelled:
		return pb.TransferReceiverStatus_TRANSFER_RECEIVER_STATUS_CANCELLED.Enum(), nil
	}
	return nil, fmt.Errorf("unknown transfer receiver status %s", s)
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
	case st.TransferTypePrimarySwapV3:
		return pb.TransferType_PRIMARY_SWAP_V3.Enum(), nil
	case st.TransferTypeCounterSwapV3:
		return pb.TransferType_COUNTER_SWAP_V3.Enum(), nil
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
	case pb.TransferType_PRIMARY_SWAP_V3:
		return st.TransferTypePrimarySwapV3, nil
	case pb.TransferType_COUNTER_SWAP_V3:
		return st.TransferTypeCounterSwapV3, nil
	}
	return "", fmt.Errorf("unknown transfer type %s", transferType)
}
