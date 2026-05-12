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
		name     string
		rawTx    []byte
		wantCode codes.Code
	}{
		{
			name:     "missing_raw_tx",
			rawTx:    nil,
			wantCode: codes.InvalidArgument,
		},
		{
			name:     "malformed_raw_tx",
			rawTx:    []byte{0x01, 0x02},
			wantCode: codes.InvalidArgument,
		},
		{
			name:     "wrong_outpoint",
			rawTx:    wrongOutpointRefundTx,
			wantCode: codes.InvalidArgument,
		},
		{
			name:     "valid",
			rawTx:    validRefundTx,
			wantCode: codes.OK,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateStaticDepositRefundTx(targetUtxo, test.rawTx)
			if test.wantCode == codes.OK {
				require.NoError(t, err)
				return
			}
			require.Equal(t, test.wantCode, status.Code(err))
		})
	}
}
