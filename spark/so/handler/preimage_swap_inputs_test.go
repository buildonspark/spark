package handler

import (
	"testing"

	pbspark "github.com/lightsparkdev/spark/proto/spark"
	"github.com/stretchr/testify/require"
)

func TestResolvePreimageSwapInputs(t *testing.T) {
	packageJob := &pbspark.UserSignedTxSigningJob{LeafId: "package-leaf"}
	transferRequest := &pbspark.StartTransferRequest{
		TransferId:                "tr-id",
		OwnerIdentityPublicKey:    []byte{0x03},
		ReceiverIdentityPublicKey: []byte{0x04},
		TransferPackage: &pbspark.TransferPackage{
			LeavesToSend: []*pbspark.UserSignedTxSigningJob{packageJob},
		},
	}

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
