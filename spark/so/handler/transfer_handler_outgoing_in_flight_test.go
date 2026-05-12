package handler

import (
	"context"
	"testing"

	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/stretchr/testify/assert"
)

func TestShouldRouteToOutgoingInFlight(t *testing.T) {
	pk := keys.GeneratePrivateKey()
	pkBytes := pk.Public().Serialize()

	ctxWithKnob := func(value float64) context.Context {
		return knobs.InjectKnobsService(t.Context(), knobs.NewFixedKnobs(map[string]float64{
			knobs.KnobReadMIMODataModelOutgoingInFlight: value,
		}))
	}

	tests := []struct {
		name   string
		filter *pb.TransferFilter
		knob   float64
		want   bool
	}{
		{
			name: "sender + 4-state full set + knob on — routes",
			filter: &pb.TransferFilter{
				Participant: &pb.TransferFilter_SenderIdentityPublicKey{SenderIdentityPublicKey: pkBytes},
				Statuses: []pb.TransferStatus{
					pb.TransferStatus_TRANSFER_STATUS_SENDER_INITIATED,
					pb.TransferStatus_TRANSFER_STATUS_SENDER_INITIATED_COORDINATOR,
					pb.TransferStatus_TRANSFER_STATUS_APPLYING_SENDER_KEY_TWEAK,
					pb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING,
				},
			},
			knob: 100,
			want: true,
		},
		{
			name: "sender + 4-state subset + knob on — routes",
			filter: &pb.TransferFilter{
				Participant: &pb.TransferFilter_SenderIdentityPublicKey{SenderIdentityPublicKey: pkBytes},
				Statuses: []pb.TransferStatus{
					pb.TransferStatus_TRANSFER_STATUS_SENDER_INITIATED,
				},
			},
			knob: 100,
			want: true,
		},
		{
			name: "matching shape + knob off — knob-gated, falls through",
			filter: &pb.TransferFilter{
				Participant: &pb.TransferFilter_SenderIdentityPublicKey{SenderIdentityPublicKey: pkBytes},
				Statuses: []pb.TransferStatus{
					pb.TransferStatus_TRANSFER_STATUS_SENDER_INITIATED,
				},
			},
			knob: 0,
			want: false,
		},
		{
			name: "sender + status outside set (SENDER_KEY_TWEAKED) — falls through",
			filter: &pb.TransferFilter{
				Participant: &pb.TransferFilter_SenderIdentityPublicKey{SenderIdentityPublicKey: pkBytes},
				Statuses: []pb.TransferStatus{
					pb.TransferStatus_TRANSFER_STATUS_SENDER_INITIATED,
					pb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED,
				},
			},
			knob: 100,
			want: false,
		},
		{
			name: "sender + receiver-named status — falls through",
			filter: &pb.TransferFilter{
				Participant: &pb.TransferFilter_SenderIdentityPublicKey{SenderIdentityPublicKey: pkBytes},
				Statuses: []pb.TransferStatus{
					pb.TransferStatus_TRANSFER_STATUS_RECEIVER_KEY_TWEAKED,
				},
			},
			knob: 100,
			want: false,
		},
		{
			name: "sender + empty status set — falls through",
			filter: &pb.TransferFilter{
				Participant: &pb.TransferFilter_SenderIdentityPublicKey{SenderIdentityPublicKey: pkBytes},
				Statuses:    []pb.TransferStatus{},
			},
			knob: 100,
			want: false,
		},
		{
			name: "receiver participant — falls through",
			filter: &pb.TransferFilter{
				Participant: &pb.TransferFilter_ReceiverIdentityPublicKey{ReceiverIdentityPublicKey: pkBytes},
				Statuses: []pb.TransferStatus{
					pb.TransferStatus_TRANSFER_STATUS_SENDER_INITIATED,
				},
			},
			knob: 100,
			want: false,
		},
		{
			name: "SR1 participant — falls through",
			filter: &pb.TransferFilter{
				Participant: &pb.TransferFilter_SenderOrReceiverIdentityPublicKey{SenderOrReceiverIdentityPublicKey: pkBytes},
				Statuses: []pb.TransferStatus{
					pb.TransferStatus_TRANSFER_STATUS_SENDER_INITIATED,
				},
			},
			knob: 100,
			want: false,
		},
		{
			name:   "nil participant — falls through",
			filter: &pb.TransferFilter{Statuses: []pb.TransferStatus{pb.TransferStatus_TRANSFER_STATUS_SENDER_INITIATED}},
			knob:   100,
			want:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, shouldRouteToOutgoingInFlight(ctxWithKnob(tt.knob), tt.filter))
		})
	}
}
