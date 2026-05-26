package handler

import (
	"testing"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent/pendingsendtransfer"
	"github.com/stretchr/testify/require"
)

func TestStartTransferV3RejectsMissingExpiryBeforePendingSend(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	handler := NewTransferHandler(cfg)

	transferID := uuid.New()
	ownerIdentity := keys.GeneratePrivateKey()
	receiverIdentity := keys.GeneratePrivateKey()
	req := &pb.StartTransferV3Request{
		TransferId: transferID.String(),
		SenderPackages: []*pb.SenderTransferPackage{{
			OwnerIdentityPublicKey: ownerIdentity.Public().Serialize(),
			TransferPackage:        &pb.TransferPackage{},
			ReceiverIdentityPublicKeys: map[string][]byte{
				uuid.NewString(): receiverIdentity.Public().Serialize(),
			},
		}},
	}

	var err error
	require.NotPanics(t, func() {
		_, err = handler.StartTransferV3(ctx, req)
	})
	require.Error(t, err)
	require.ErrorContains(t, err, "expiry_time is required")

	pendingCount, err := sessionCtx.Client.PendingSendTransfer.Query().
		Where(pendingsendtransfer.TransferID(transferID)).
		Count(ctx)
	require.NoError(t, err)
	require.Zero(t, pendingCount)
}
