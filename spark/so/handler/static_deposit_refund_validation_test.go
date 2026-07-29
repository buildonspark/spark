package handler

import (
	"testing"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestValidateStaticDepositRefundTxRejectsClientRawTxInputs(t *testing.T) {
	txid := chainhash.DoubleHashH([]byte("static-deposit-refund-validation"))
	targetUtxo := &VerifiedTargetUtxo{
		inner: &ent.Utxo{Vout: 0},
		txid:  txid,
	}
	receiverPubKey := keys.GeneratePrivateKey().Public()
	validRefundTx := createSpendTxBytesSpendingOutpoint(t, txid, 0, receiverPubKey, 1000)
	wrongOutpointRefundTx := createSpendTxBytesSpendingOutpoint(t, txid, 1, receiverPubKey, 1000)

	tests := []struct {
		name         string
		rawTx        []byte
		expectedCode codes.Code
	}{
		{
			name:         "missing_raw_tx",
			rawTx:        nil,
			expectedCode: codes.InvalidArgument,
		},
		{
			name:         "malformed_raw_tx",
			rawTx:        []byte{0x01, 0x02},
			expectedCode: codes.InvalidArgument,
		},
		{
			name:         "wrong_outpoint",
			rawTx:        wrongOutpointRefundTx,
			expectedCode: codes.InvalidArgument,
		},
		{
			name:         "valid",
			rawTx:        validRefundTx,
			expectedCode: codes.OK,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateStaticDepositRefundTx(targetUtxo, test.rawTx)
			if test.expectedCode == codes.OK {
				require.NoError(t, err)
				return
			}
			require.Equal(t, test.expectedCode, status.Code(err))
		})
	}
}
