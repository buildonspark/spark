package handler

import (
	"testing"

	pbspark "github.com/lightsparkdev/spark/proto/spark"
	"github.com/stretchr/testify/require"
)

func TestResolvePreimageSwapInputs(t *testing.T) {
	transferJob := &pbspark.UserSignedTxSigningJob{LeafId: "transfer-leaf"}
	packageJob := &pbspark.UserSignedTxSigningJob{LeafId: "package-leaf"}
	transfer := &pbspark.StartUserSignedTransferRequest{
		TransferId:                "transfer-id",
		OwnerIdentityPublicKey:    []byte{0x01},
		ReceiverIdentityPublicKey: []byte{0x02},
		LeavesToSend:              []*pbspark.UserSignedTxSigningJob{transferJob},
	}
	transferRequest := &pbspark.StartTransferRequest{
		TransferId:                "tr-id",
		OwnerIdentityPublicKey:    []byte{0x03},
		ReceiverIdentityPublicKey: []byte{0x04},
		TransferPackage: &pbspark.TransferPackage{
			LeavesToSend: []*pbspark.UserSignedTxSigningJob{packageJob},
		},
	}

	t.Run("transfer present wins, plain lists from transfer", func(t *testing.T) {
		inputs, err := preimageSwapInputsFromRequest(&pbspark.InitiatePreimageSwapRequest{
			Transfer:        transfer,
			TransferRequest: transferRequest,
			Reason:          pbspark.InitiatePreimageSwapRequest_REASON_SEND,
		})
		require.NoError(t, err)
		require.Equal(t, "transfer-id", inputs.transferID)
		require.Equal(t, []byte{0x01}, inputs.ownerIdentityPublicKey)
		require.False(t, inputs.isPackageOnlySend)
		require.Equal(t, "transfer-leaf", inputs.cpfpLeaves()[0].GetLeafId())
	})

	t.Run("package-only send skips plain validation", func(t *testing.T) {
		inputs, err := preimageSwapInputsFromRequest(&pbspark.InitiatePreimageSwapRequest{
			TransferRequest:           transferRequest,
			ReceiverIdentityPublicKey: []byte{0x04},
			Reason:                    pbspark.InitiatePreimageSwapRequest_REASON_SEND,
		})
		require.NoError(t, err)
		require.Equal(t, "tr-id", inputs.transferID)
		require.True(t, inputs.isPackageOnlySend)
		require.Nil(t, inputs.validationCpfp)
		require.Equal(t, "package-leaf", inputs.cpfpLeaves()[0].GetLeafId())
	})

	t.Run("package-only receive feeds package lists to plain validation", func(t *testing.T) {
		inputs, err := preimageSwapInputsFromRequest(&pbspark.InitiatePreimageSwapRequest{
			TransferRequest:           transferRequest,
			ReceiverIdentityPublicKey: []byte{0x04},
			Reason:                    pbspark.InitiatePreimageSwapRequest_REASON_RECEIVE,
		})
		require.NoError(t, err)
		require.False(t, inputs.isPackageOnlySend)
		require.Equal(t, "package-leaf", inputs.validationCpfp[0].GetLeafId())
	})

	t.Run("transfer_request without package rejected", func(t *testing.T) {
		_, err := preimageSwapInputsFromRequest(&pbspark.InitiatePreimageSwapRequest{
			TransferRequest: &pbspark.StartTransferRequest{TransferId: "tr-id"},
		})
		require.ErrorContains(t, err, "transfer_request.transfer_package is required")
	})

	t.Run("package-only rejects top-level receiver mismatch", func(t *testing.T) {
		_, err := preimageSwapInputsFromRequest(&pbspark.InitiatePreimageSwapRequest{
			TransferRequest:           transferRequest,
			ReceiverIdentityPublicKey: []byte{0x09},
			Reason:                    pbspark.InitiatePreimageSwapRequest_REASON_SEND,
		})
		require.ErrorContains(t, err, "receiver identity public key mismatch")
	})

	t.Run("neither shape rejected", func(t *testing.T) {
		_, err := preimageSwapInputsFromRequest(&pbspark.InitiatePreimageSwapRequest{})
		require.ErrorContains(t, err, "transfer_request is required")
	})
}

func TestIgnoreLegacyTransfer(t *testing.T) {
	transferJob := &pbspark.UserSignedTxSigningJob{LeafId: "transfer-leaf"}
	packageJob := &pbspark.UserSignedTxSigningJob{LeafId: "package-leaf"}
	newBoth := func(reason pbspark.InitiatePreimageSwapRequest_Reason) *pbspark.InitiatePreimageSwapRequest {
		return &pbspark.InitiatePreimageSwapRequest{
			Transfer: &pbspark.StartUserSignedTransferRequest{
				TransferId:                "transfer-id",
				OwnerIdentityPublicKey:    []byte{0x01},
				ReceiverIdentityPublicKey: []byte{0x04},
				LeavesToSend:              []*pbspark.UserSignedTxSigningJob{transferJob},
			},
			TransferRequest: &pbspark.StartTransferRequest{
				TransferId:                "tr-id",
				OwnerIdentityPublicKey:    []byte{0x03},
				ReceiverIdentityPublicKey: []byte{0x04},
				TransferPackage: &pbspark.TransferPackage{
					LeavesToSend: []*pbspark.UserSignedTxSigningJob{packageJob},
				},
			},
			ReceiverIdentityPublicKey: []byte{0x04},
			Reason:                    reason,
		}
	}
	t.Run("strips both-shape SEND onto the package path", func(t *testing.T) {
		req := newBoth(pbspark.InitiatePreimageSwapRequest_REASON_SEND)
		require.True(t, ignoreLegacyTransfer(req))
		require.Nil(t, req.GetTransfer())
		inputs, err := preimageSwapInputsFromRequest(req)
		require.NoError(t, err)
		require.True(t, inputs.isPackageOnlySend)
		require.Equal(t, "tr-id", inputs.transferID)
	})

	t.Run("strips RECEIVE, feeding package lists to plain validation", func(t *testing.T) {
		req := newBoth(pbspark.InitiatePreimageSwapRequest_REASON_RECEIVE)
		require.True(t, ignoreLegacyTransfer(req))
		inputs, err := preimageSwapInputsFromRequest(req)
		require.NoError(t, err)
		require.False(t, inputs.isPackageOnlySend)
		require.Equal(t, "package-leaf", inputs.validationCpfp[0].GetLeafId())
	})

	t.Run("no-op when the caller already omits transfer", func(t *testing.T) {
		req := newBoth(pbspark.InitiatePreimageSwapRequest_REASON_SEND)
		req.Transfer = nil
		require.False(t, ignoreLegacyTransfer(req))
	})

	t.Run("strips a transfer-only straggler into a closed rejection", func(t *testing.T) {
		req := newBoth(pbspark.InitiatePreimageSwapRequest_REASON_SEND)
		req.TransferRequest = nil
		require.True(t, ignoreLegacyTransfer(req))
		_, err := preimageSwapInputsFromRequest(req)
		require.ErrorContains(t, err, "transfer_request is required")
	})
}
