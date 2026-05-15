package handler

import (
	"encoding/hex"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/require"
)

func TestStaticDepositInternalHandlersRejectMalformedRequestShapeWithoutPanic(t *testing.T) {
	handler := NewStaticDepositInternalHandler(&so.Config{})
	cfg := &so.Config{}

	tests := []struct {
		name        string
		run         func() error
		errContains string
	}{
		{
			name: "create fixed nil signed request",
			run: func() error {
				_, err := handler.CreateStaticDepositUtxoSwap(t.Context(), cfg, nil)
				return err
			},
			errContains: "request is required",
		},
		{
			name: "create fixed nil request body",
			run: func() error {
				_, err := handler.CreateStaticDepositUtxoSwap(t.Context(), cfg, &pbinternal.CreateStaticDepositUtxoSwapRequest{})
				return err
			},
			errContains: "request is required",
		},
		{
			name: "create fixed nil on-chain utxo",
			run: func() error {
				_, err := handler.CreateStaticDepositUtxoSwap(t.Context(), cfg, &pbinternal.CreateStaticDepositUtxoSwapRequest{
					Request: &pbinternal.InitiateStaticDepositUtxoSwapRequest{},
				})
				return err
			},
			errContains: "on_chain_utxo is required",
		},
		{
			name: "create fixed nil transfer",
			run: func() error {
				_, err := handler.CreateStaticDepositUtxoSwap(t.Context(), cfg, &pbinternal.CreateStaticDepositUtxoSwapRequest{
					Request: &pbinternal.InitiateStaticDepositUtxoSwapRequest{
						OnChainUtxo: &pb.UTXO{},
					},
				})
				return err
			},
			errContains: "transfer is required",
		},
		{
			name: "create fixed nil transfer package",
			run: func() error {
				_, err := handler.CreateStaticDepositUtxoSwap(t.Context(), cfg, &pbinternal.CreateStaticDepositUtxoSwapRequest{
					Request: &pbinternal.InitiateStaticDepositUtxoSwapRequest{
						OnChainUtxo: &pb.UTXO{},
						Transfer:    &pb.StartTransferRequest{},
					},
				})
				return err
			},
			errContains: "transfer_package is required",
		},
		{
			name: "create fixed nil spend signing job",
			run: func() error {
				_, err := handler.CreateStaticDepositUtxoSwap(t.Context(), cfg, &pbinternal.CreateStaticDepositUtxoSwapRequest{
					Request: &pbinternal.InitiateStaticDepositUtxoSwapRequest{
						OnChainUtxo: &pb.UTXO{},
						Transfer: &pb.StartTransferRequest{
							TransferPackage: &pb.TransferPackage{},
						},
					},
				})
				return err
			},
			errContains: "spend_tx_signing_job is required",
		},
		{
			name: "create instant nil signed request",
			run: func() error {
				_, err := handler.CreateInstantStaticDepositUtxoSwap(t.Context(), cfg, nil)
				return err
			},
			errContains: "request is required",
		},
		{
			name: "create instant nil request body",
			run: func() error {
				_, err := handler.CreateInstantStaticDepositUtxoSwap(t.Context(), cfg, &pbinternal.CreateInstantStaticDepositUtxoSwapRequest{})
				return err
			},
			errContains: "request is required",
		},
		{
			name: "create instant nil on-chain utxo",
			run: func() error {
				_, err := handler.CreateInstantStaticDepositUtxoSwap(t.Context(), cfg, &pbinternal.CreateInstantStaticDepositUtxoSwapRequest{
					Request: &pbinternal.ReserveInstantStaticDepositUtxoSwapRequest{},
				})
				return err
			},
			errContains: "on_chain_utxo is required",
		},
		{
			name: "create instant nil transfer",
			run: func() error {
				_, err := handler.CreateInstantStaticDepositUtxoSwap(t.Context(), cfg, &pbinternal.CreateInstantStaticDepositUtxoSwapRequest{
					Request: &pbinternal.ReserveInstantStaticDepositUtxoSwapRequest{
						OnChainUtxo: &pb.UTXO{},
					},
				})
				return err
			},
			errContains: "transfer is required",
		},
		{
			name: "create instant nil transfer package",
			run: func() error {
				_, err := handler.CreateInstantStaticDepositUtxoSwap(t.Context(), cfg, &pbinternal.CreateInstantStaticDepositUtxoSwapRequest{
					Request: &pbinternal.ReserveInstantStaticDepositUtxoSwapRequest{
						OnChainUtxo: &pb.UTXO{},
						Transfer:    &pb.StartTransferRequest{},
					},
				})
				return err
			},
			errContains: "transfer_package is required",
		},
		{
			name: "create instant rejects malformed transfer package before loading leaves",
			run: func() error {
				testCfg := sparktesting.TestConfig(t)
				testHandler := NewStaticDepositInternalHandler(testCfg)
				txid := make([]byte, 32)
				txid[0] = 1
				onChainUtxo := &pb.UTXO{
					Network: pb.Network_REGTEST,
					Txid:    txid,
					Vout:    2,
				}
				messageHash, err := CreateUtxoSwapStatement(
					UtxoSwapStatementTypeCreated,
					hex.EncodeToString(onChainUtxo.Txid),
					onChainUtxo.Vout,
					btcnetwork.Regtest,
				)
				require.NoError(t, err)
				signature := ecdsa.Sign(testCfg.IdentityPrivateKey.ToBTCEC(), messageHash)

				ownerIdentityKey := keys.GeneratePrivateKey()
				receiverIdentityKey := keys.GeneratePrivateKey()
				_, err = testHandler.CreateInstantStaticDepositUtxoSwap(t.Context(), testCfg, &pbinternal.CreateInstantStaticDepositUtxoSwapRequest{
					Request: &pbinternal.ReserveInstantStaticDepositUtxoSwapRequest{
						OnChainUtxo: onChainUtxo,
						Transfer: &pb.StartTransferRequest{
							TransferId:                uuid.NewString(),
							OwnerIdentityPublicKey:    ownerIdentityKey.Public().Serialize(),
							ReceiverIdentityPublicKey: receiverIdentityKey.Public().Serialize(),
							TransferPackage: &pb.TransferPackage{
								KeyTweakPackage: map[string][]byte{testCfg.Identifier: {1, 2, 3}},
								LeavesToSend:    []*pb.UserSignedTxSigningJob{nil},
							},
						},
						DestinationAddress: "spark-test-address",
						ValueSats:          100,
						CreditAmountSats:   90,
					},
					Signature:            signature.Serialize(),
					CoordinatorPublicKey: testCfg.IdentityPublicKey().Serialize(),
				})
				return err
			},
			errContains: "leaves_to_send[0] is required",
		},
		{
			name: "save instant utxo nil request",
			run: func() error {
				_, err := handler.SaveUtxoForInstantStaticDeposit(t.Context(), cfg, nil)
				return err
			},
			errContains: "request is required",
		},
		{
			name: "save instant utxo nil on-chain utxo",
			run: func() error {
				_, err := handler.SaveUtxoForInstantStaticDeposit(t.Context(), cfg, &pbinternal.SaveUtxoForInstantStaticDepositRequest{})
				return err
			},
			errContains: "on_chain_utxo is required",
		},
		{
			name: "link utxo swap transfer nil request",
			run: func() error {
				_, err := handler.LinkUtxoSwapTransfer(t.Context(), cfg, nil)
				return err
			},
			errContains: "request is required",
		},
		{
			name: "refund nil signed request",
			run: func() error {
				_, err := handler.CreateStaticDepositUtxoRefund(t.Context(), cfg, nil)
				return err
			},
			errContains: "request is required",
		},
		{
			name: "refund nil request body",
			run: func() error {
				_, err := handler.CreateStaticDepositUtxoRefund(t.Context(), cfg, &pbinternal.CreateStaticDepositUtxoRefundRequest{})
				return err
			},
			errContains: "request is required",
		},
		{
			name: "refund nil on-chain utxo",
			run: func() error {
				_, err := handler.CreateStaticDepositUtxoRefund(t.Context(), cfg, &pbinternal.CreateStaticDepositUtxoRefundRequest{
					Request: &pb.InitiateStaticDepositUtxoRefundRequest{},
				})
				return err
			},
			errContains: "on_chain_utxo is required",
		},
		{
			name: "refund nil signing job",
			run: func() error {
				_, err := handler.CreateStaticDepositUtxoRefund(t.Context(), cfg, &pbinternal.CreateStaticDepositUtxoRefundRequest{
					Request: &pb.InitiateStaticDepositUtxoRefundRequest{
						OnChainUtxo: &pb.UTXO{},
					},
				})
				return err
			},
			errContains: "refund_tx_signing_job is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var err error
			require.NotPanics(t, func() {
				err = tt.run()
			})
			require.Error(t, err)
			require.ErrorContains(t, err, tt.errContains)
		})
	}
}
