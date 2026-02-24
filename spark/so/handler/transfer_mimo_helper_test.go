package handler

import (
	"testing"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/stretchr/testify/require"
)

func TestGetTransferSenderReceiver_Success(t *testing.T) {
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

	gotSender, gotReceiver, err := GetTransferSenderReceiver(transfer)
	require.NoError(t, err)
	require.True(t, senderPub.Equals(gotSender))
	require.True(t, receiverPub.Equals(gotReceiver))
}

func TestGetTransferSenderReceiver_ZeroSenders_ReturnsError(t *testing.T) {
	receiverPub := keys.GeneratePrivateKey().Public()

	transfer := &ent.Transfer{
		ID: uuid.New(),
		Edges: ent.TransferEdges{
			TransferSenders:   nil,
			TransferReceivers: []*ent.TransferReceiver{{IdentityPubkey: receiverPub}},
		},
	}

	_, _, err := GetTransferSenderReceiver(transfer)
	require.Error(t, err)
	require.Contains(t, err.Error(), "transfer senders")
	require.Contains(t, err.Error(), "expected 1")
}

func TestGetTransferSenderReceiver_MultipleSenders_ReturnsError(t *testing.T) {
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

	_, _, err := GetTransferSenderReceiver(transfer)
	require.Error(t, err)
	require.Contains(t, err.Error(), "transfer senders")
	require.Contains(t, err.Error(), "expected 1")
}

func TestGetTransferSenderReceiver_ZeroReceivers_ReturnsError(t *testing.T) {
	senderPub := keys.GeneratePrivateKey().Public()

	transfer := &ent.Transfer{
		ID: uuid.New(),
		Edges: ent.TransferEdges{
			TransferSenders:   []*ent.TransferSender{{IdentityPubkey: senderPub}},
			TransferReceivers: nil,
		},
	}

	_, _, err := GetTransferSenderReceiver(transfer)
	require.Error(t, err)
	require.Contains(t, err.Error(), "transfer receivers")
	require.Contains(t, err.Error(), "expected 1")
}

func TestGetTransferSenderReceiver_MultipleReceivers_ReturnsError(t *testing.T) {
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

	_, _, err := GetTransferSenderReceiver(transfer)
	require.Error(t, err)
	require.Contains(t, err.Error(), "transfer receivers")
	require.Contains(t, err.Error(), "expected 1")
}
