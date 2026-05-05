package mimo_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/lightsparkdev/spark/so/mimo"
	"github.com/stretchr/testify/require"
)

func mimoSendCtx() context.Context {
	return knobs.InjectKnobsService(context.Background(), knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobReadMIMODataModelTransferSend: 100,
	}))
}

func TestGetSingleTransferSenderReceiver_Success(t *testing.T) {
	senderPub := keys.GeneratePrivateKey().Public()
	receiverPub := keys.GeneratePrivateKey().Public()

	transfer := &ent.Transfer{
		ID: uuid.New(),
		Edges: ent.TransferEdges{
			TransferSenders: []*ent.TransferSender{
				{IdentityPubkey: senderPub},
			},
			TransferReceivers: []*ent.TransferReceiver{
				{IdentityPubkey: receiverPub},
			},
		},
	}

	gotSender, gotReceiver, err := mimo.GetSingleTransferSenderReceiver(mimoSendCtx(), transfer)
	require.NoError(t, err)
	require.True(t, senderPub.Equals(gotSender))
	require.True(t, receiverPub.Equals(gotReceiver))
}

func TestGetSingleTransferSenderReceiver_ZeroSenders_FallsBack(t *testing.T) {
	senderPub := keys.GeneratePrivateKey().Public()
	receiverPub := keys.GeneratePrivateKey().Public()

	transfer := &ent.Transfer{
		ID:                     uuid.New(),
		SenderIdentityPubkey:   senderPub,
		ReceiverIdentityPubkey: receiverPub,
		Edges: ent.TransferEdges{
			TransferSenders:   nil,
			TransferReceivers: []*ent.TransferReceiver{{IdentityPubkey: receiverPub}},
		},
	}

	gotSender, gotReceiver, err := mimo.GetSingleTransferSenderReceiver(mimoSendCtx(), transfer)
	require.NoError(t, err)
	require.True(t, senderPub.Equals(gotSender))
	require.True(t, receiverPub.Equals(gotReceiver))
}

func TestGetSingleTransferSenderReceiver_MultipleSenders_ReturnsError(t *testing.T) {
	sender1 := keys.GeneratePrivateKey().Public()
	sender2 := keys.GeneratePrivateKey().Public()
	receiverPub := keys.GeneratePrivateKey().Public()

	transfer := &ent.Transfer{
		ID: uuid.New(),
		Edges: ent.TransferEdges{
			TransferSenders: []*ent.TransferSender{
				{IdentityPubkey: sender1},
				{IdentityPubkey: sender2},
			},
			TransferReceivers: []*ent.TransferReceiver{{IdentityPubkey: receiverPub}},
		},
	}

	_, _, err := mimo.GetSingleTransferSenderReceiver(mimoSendCtx(), transfer)
	require.Error(t, err)
	require.Contains(t, err.Error(), "transfer senders")
	require.Contains(t, err.Error(), "expected 1")
}

func TestGetSingleTransferSenderReceiver_ZeroReceivers_FallsBack(t *testing.T) {
	senderPub := keys.GeneratePrivateKey().Public()
	receiverPub := keys.GeneratePrivateKey().Public()

	transfer := &ent.Transfer{
		ID:                     uuid.New(),
		SenderIdentityPubkey:   senderPub,
		ReceiverIdentityPubkey: receiverPub,
		Edges: ent.TransferEdges{
			TransferSenders:   []*ent.TransferSender{{IdentityPubkey: senderPub}},
			TransferReceivers: nil,
		},
	}

	gotSender, gotReceiver, err := mimo.GetSingleTransferSenderReceiver(mimoSendCtx(), transfer)
	require.NoError(t, err)
	require.True(t, senderPub.Equals(gotSender))
	require.True(t, receiverPub.Equals(gotReceiver))
}

func TestGetSingleTransferSenderReceiver_MultipleReceivers_ReturnsError(t *testing.T) {
	senderPub := keys.GeneratePrivateKey().Public()
	receiver1 := keys.GeneratePrivateKey().Public()
	receiver2 := keys.GeneratePrivateKey().Public()

	transfer := &ent.Transfer{
		ID: uuid.New(),
		Edges: ent.TransferEdges{
			TransferSenders: []*ent.TransferSender{{IdentityPubkey: senderPub}},
			TransferReceivers: []*ent.TransferReceiver{
				{IdentityPubkey: receiver1},
				{IdentityPubkey: receiver2},
			},
		},
	}

	_, _, err := mimo.GetSingleTransferSenderReceiver(mimoSendCtx(), transfer)
	require.Error(t, err)
	require.Contains(t, err.Error(), "transfer receivers")
	require.Contains(t, err.Error(), "expected 1")
}

func TestGetSingleTransferSender_LegacyFallback(t *testing.T) {
	senderPub := keys.GeneratePrivateKey().Public()
	transfer := &ent.Transfer{
		ID:                   uuid.New(),
		SenderIdentityPubkey: senderPub,
	}

	// Knob off — should fall back to the deprecated column.
	got, err := mimo.GetSingleTransferSender(t.Context(), transfer)
	require.NoError(t, err)
	require.True(t, senderPub.Equals(got))
}
