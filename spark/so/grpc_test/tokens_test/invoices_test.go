package tokens_test

import (
	"encoding/binary"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so/utils"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestCoordinatedTransferTransactionWithSparkInvoices(t *testing.T) {
	// TODO: (CNT-493) Re-enable invoice functionality once spark address migration is complete
	sparktesting.SkipIfGithubActions(t)
	testCases := []struct {
		name                                      string
		batchTransfer                             bool
		mismatchedIdentifier                      bool
		mismatchedOwner                           bool
		emptyInvoiceAmount                        bool
		invoiceAmountGreaterThanCreatedOutputs    bool
		invoiceAmountLessThanCreatedOutputs       bool
		expiredInvoice                            bool
		satsInvoice                               bool
		expiredAtSign                             bool
		transferFailsIfUnexpiredTransactionExists bool
		signedInvoice                             bool
		invalidSignature                          bool
		mismatchedSenderPublicKey                 bool
		emptySenderPublicKey                      bool
		mismatchedNetwork                         bool
	}{
		{
			name: "transfer should succeed with valid spark invoice",
		},
		{
			name:          "transfer should succeed with valid signed spark invoice",
			signedInvoice: true,
		},
		{
			name:             "transfer should fail with invalid spark invoice signature",
			signedInvoice:    true,
			invalidSignature: true,
		},
		{
			name:          "batch transfer should succeed with valid spark invoices",
			batchTransfer: true,
		},
		{
			name:               "transfer should succeed with empty invoice amount",
			emptyInvoiceAmount: true,
		},
		{
			name:               "batch transfer should succeed with empty invoice amount",
			batchTransfer:      true,
			emptyInvoiceAmount: true,
		},
		{
			name:            "transfer should fail with mismatched owner",
			mismatchedOwner: true,
		},
		{
			name:            "batch transfer should fail with mismatched owner",
			batchTransfer:   true,
			mismatchedOwner: true,
		},
		{
			name:                 "transfer should fail with mismatched identifier",
			mismatchedIdentifier: true,
		},
		{
			name:                 "batch transfer should fail with mismatched identifier",
			batchTransfer:        true,
			mismatchedIdentifier: true,
		},
		{
			name:                                   "transfer should fail with invoice amount greater than created outputs",
			invoiceAmountGreaterThanCreatedOutputs: true,
		},
		{
			name:                                "transfer should fail with invoice amount less than created outputs",
			invoiceAmountLessThanCreatedOutputs: true,
		},
		{
			name:           "transfer should fail with expired spark invoice",
			expiredInvoice: true,
		},
		{
			name:           "batch transfer should fail with expired spark invoice",
			batchTransfer:  true,
			expiredInvoice: true,
		},
		{
			name:        "transfer should fail with sats spark invoice",
			satsInvoice: true,
		},
		{
			name: "new transfers should fail if paying an invoice that is already attached to an unexpired transaction",
			transferFailsIfUnexpiredTransactionExists: true,
		},
		{
			name:          "batch transfer should fail if paying an invoice that is already attached to an unexpired transaction",
			batchTransfer: true,
			transferFailsIfUnexpiredTransactionExists: true,
		},
		{
			name:          "sign should fail when a spark invoice is expired",
			expiredAtSign: true,
		},
		{
			name:                      "transfer should fail when a spark invoice encodes a sender pub key that does not match the owner of the spent outputs on the token transaction",
			mismatchedSenderPublicKey: true,
		},
		{
			name:                 "transfer should succeed when no sender public key is encoded",
			emptySenderPublicKey: true,
		},
		{
			name:              "transfer should fail when a spark invoice encodes a mismatched network",
			mismatchedNetwork: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			issuerPrivateKey := keys.GeneratePrivateKey()
			config := wallet.NewTestWalletConfigWithIdentityKey(t, issuerPrivateKey)

			tokenPrivKey := config.IdentityPrivateKey
			err := testCoordinatedCreateNativeSparkTokenWithParams(t, config, sparkTokenCreationTestParams{
				issuerPrivateKey: issuerPrivateKey,
				name:             testTokenName,
				ticker:           testTokenTicker,
				maxSupply:        testTokenMaxSupply,
			})
			require.NoError(t, err, "failed to create native spark token")

			issueTokenTransaction, _, err := createTestTokenMintTransactionTokenPbWithParams(t, config, tokenTransactionParams{
				TokenIdentityPubKey: tokenPrivKey.Public(),
				IsNativeSparkToken:  false,
				UseTokenIdentifier:  true,
				NumOutputs:          2,
				OutputAmounts:       []uint64{uint64(testIssueOutput1Amount), uint64(testIssueOutput2Amount)},
				MintToSelf:          true,
			})
			require.NoError(t, err, "failed to create test token issuance transaction")
			finalIssueTokenTransaction, err := wallet.BroadcastTokenTransfer(
				t.Context(), config, issueTokenTransaction,
				[]keys.Private{tokenPrivKey},
			)
			require.NoError(t, err, "failed to broadcast issuance token transaction")

			testCoordinatedTransferTransactionWithSparkInvoicesScenarios(
				t, config, finalIssueTokenTransaction,
				tokenPrivKey.Public(),
				tc.batchTransfer,
				tc.mismatchedIdentifier,
				tc.mismatchedOwner,
				tc.emptyInvoiceAmount,
				tc.invoiceAmountGreaterThanCreatedOutputs,
				tc.invoiceAmountLessThanCreatedOutputs,
				tc.expiredInvoice,
				tc.satsInvoice,
				tc.expiredAtSign,
				tc.transferFailsIfUnexpiredTransactionExists,
				tc.signedInvoice,
				tc.invalidSignature,
				tc.mismatchedSenderPublicKey,
				tc.emptySenderPublicKey,
				tc.mismatchedNetwork,
			)
		})
	}
}

func testCoordinatedTransferTransactionWithSparkInvoicesScenarios(t *testing.T, config *wallet.TestWalletConfig, finalIssueTokenTransaction *tokenpb.TokenTransaction, tokenIdentityPubKey keys.Public, batchTransfer bool, mismatchedIdentifier bool, mismatchedOwner bool, emptyInvoiceAmount bool, invoiceAmountGreaterThanCreatedOutputs bool, invoiceAmountLessThanCreatedOutputs bool, expiredInvoice bool, satsInvoice bool, expiredAtSign bool, transferFailsIfUnexpiredTransactionExists bool, signedInvoice bool, invalidSignature bool, mismatchedSenderPublicKey bool, emptySenderPublicKey bool, mismatchedNetwork bool) {
	finalMintTransactionHash, err := utils.HashTokenTransaction(finalIssueTokenTransaction, false)
	require.NoError(t, err, "failed to hash final issue token transaction")
	tokenIdentifier, err := getTokenIdentifierFromMetadata(t.Context(), config, tokenIdentityPubKey)
	require.NoError(t, err, "failed to get token identifier from metadata")
	receiver1 := keys.GeneratePrivateKey()
	receiver1PubKey := receiver1.Public()
	receiver2 := keys.GeneratePrivateKey()
	receiver2PubKey := receiver2.Public()

	expiryTime := timestamppb.New(time.Now().Add(time.Minute * 30))
	if expiredInvoice {
		expiryTime = timestamppb.New(time.Now().Add(-time.Minute * 30))
	} else if expiredAtSign {
		expiryTime = timestamppb.New(time.Now().Add(time.Second * 4))
	}

	var transferTransaction *tokenpb.TokenTransaction
	var nonBatchReceiver keys.Private
	if !batchTransfer {
		transferTransaction, nonBatchReceiver, err = createTestTokenTransferTransactionTokenPbWithParams(t, config, tokenTransactionParams{
			TokenIdentityPubKey:            tokenIdentityPubKey,
			IsNativeSparkToken:             true,
			UseTokenIdentifier:             true,
			FinalIssueTokenTransactionHash: finalMintTransactionHash,
			NumOutputs:                     1,
			OutputAmounts:                  []uint64{uint64(testTransferOutput1Amount)},
		})
		require.NoError(t, err, "failed to create transfer transaction")
	} else {
		transferTransaction = &tokenpb.TokenTransaction{
			Version: TokenTransactionVersion2,
			TokenInputs: &tokenpb.TokenTransaction_TransferInput{
				TransferInput: &tokenpb.TokenTransferInput{
					OutputsToSpend: []*tokenpb.TokenOutputToSpend{
						{
							PrevTokenTransactionHash: finalMintTransactionHash,
							PrevTokenTransactionVout: 0,
						},
						{
							PrevTokenTransactionHash: finalMintTransactionHash,
							PrevTokenTransactionVout: 1,
						},
					},
				},
			},
			TokenOutputs: []*tokenpb.TokenOutput{
				{
					OwnerPublicKey:  receiver1PubKey.Serialize(),
					TokenIdentifier: tokenIdentifier,
					TokenAmount:     int64ToUint128Bytes(0, testIssueOutput1Amount),
				},
				{
					OwnerPublicKey:  receiver2PubKey.Serialize(),
					TokenIdentifier: tokenIdentifier,
					TokenAmount:     int64ToUint128Bytes(0, testIssueOutput2Amount),
				},
			},
			Network:                         config.ProtoNetwork(),
			SparkOperatorIdentityPublicKeys: getSigningOperatorPublicKeyBytes(config),
			ClientCreatedTimestamp:          timestamppb.New(time.Now()),
		}
	}

	rng := rand.NewChaCha8([32]byte{})
	var invoiceAttachments []*tokenpb.InvoiceAttachment
	for _, output := range transferTransaction.TokenOutputs {
		receiverPublicKey, _ := keys.ParsePublicKey(output.GetOwnerPublicKey())
		newTokenIdentifier := make([]byte, len(output.TokenIdentifier))
		copy(newTokenIdentifier, output.TokenIdentifier)
		version := uint32(1)
		senderPublicKey := config.IdentityPrivateKey.Public()
		memo := "Test memo"
		network := config.Network
		satsPayment := false

		var amount *uint64
		if !emptyInvoiceAmount {
			amount = new(uint64)
			amountToEncode := binary.BigEndian.Uint64(output.TokenAmount[8:])
			if invoiceAmountGreaterThanCreatedOutputs {
				amountToEncode += 2
			} else if invoiceAmountLessThanCreatedOutputs {
				amountToEncode -= 2
			}
			*amount = amountToEncode
		}
		if mismatchedIdentifier {
			newTokenIdentifier[0] ^= 0xFF
		}
		if mismatchedOwner {
			receiverPublicKey = keys.MustGeneratePrivateKeyFromRand(rng).Public()
		}
		if satsInvoice {
			satsPayment = true
		}
		if mismatchedSenderPublicKey {
			senderPublicKey = keys.MustGeneratePrivateKeyFromRand(rng).Public()
		}
		if emptySenderPublicKey {
			senderPublicKey = keys.Public{}
		}
		if mismatchedNetwork {
			if config.Network == common.Mainnet {
				network = common.Regtest
			} else {
				network = common.Mainnet
			}
		}

		createParams := createSparkInvoiceParams{
			Version:           version,
			ReceiverPublicKey: receiverPublicKey,
			SenderPublicKey:   senderPublicKey,
			Amount:            amount,
			ExpiryTime:        expiryTime,
			Memo:              &memo,
			TokenIdentifier:   newTokenIdentifier,
			Network:           network,
			SatsPayment:       satsPayment,
		}

		// If signature testing is requested, set signer for helper to embed the signature
		if signedInvoice {
			if invalidSignature {
				s := keys.GeneratePrivateKey()
				createParams.SignerPrivKey = s
			} else {
				if batchTransfer {
					// Batch: use the corresponding receiver key by matching public keys
					if receiverPublicKey.Equals(receiver1PubKey) {
						createParams.SignerPrivKey = receiver1
					} else {
						createParams.SignerPrivKey = receiver2
					}
				} else {
					createParams.SignerPrivKey = nonBatchReceiver
				}
			}
		}

		sparkInvoice, err := createSparkInvoice(createParams)
		require.NoError(t, err, "failed to create spark invoice")
		attachment := &tokenpb.InvoiceAttachment{SparkInvoice: sparkInvoice}
		invoiceAttachments = append(invoiceAttachments, attachment)
	}
	transferTransaction.InvoiceAttachments = invoiceAttachments

	startResp, finalTxHash, err := wallet.StartTokenTransaction(
		t.Context(),
		config,
		transferTransaction,
		[]keys.Private{config.IdentityPrivateKey, config.IdentityPrivateKey},
		wallet.DefaultValidityDuration,
		nil,
	)

	if mismatchedIdentifier {
		require.Error(t, err, "expected error when mismatched identifier")
		return
	} else if mismatchedOwner {
		require.Error(t, err, "expected error when mismatched owner")
		return
	} else if expiredInvoice {
		require.Error(t, err, "expected error when expired spark invoice")
		return
	} else if satsInvoice {
		require.Error(t, err, "expected error when sats spark invoice")
		return
	} else if invoiceAmountGreaterThanCreatedOutputs {
		require.Error(t, err, "expected error when invoice amount greater than created outputs")
		return
	} else if invoiceAmountLessThanCreatedOutputs {
		require.Error(t, err, "expected error when invoice amount less than created outputs")
		return
	} else if invalidSignature {
		require.Error(t, err, "expected error when invalid spark invoice signature")
		return
	} else if mismatchedSenderPublicKey {
		require.Error(t, err, "expected error when mismatched sender public key")
		return
	} else if mismatchedNetwork {
		require.Error(t, err, "expected error when mismatched network")
		return
	} else {
		require.NoError(t, err, "expected no error when valid spark invoice")
	}

	if transferFailsIfUnexpiredTransactionExists {
		issueTokenTransaction, _, err := createTestTokenMintTransactionTokenPbWithParams(t, config, tokenTransactionParams{
			TokenIdentityPubKey: tokenIdentityPubKey,
			IsNativeSparkToken:  false,
			UseTokenIdentifier:  true,
			NumOutputs:          2,
			OutputAmounts:       []uint64{uint64(testIssueOutput1Amount), uint64(testIssueOutput2Amount)},
			MintToSelf:          true,
		})
		require.NoError(t, err, "failed to create test token issuance transaction")
		finalIssueTokenTransaction, err := wallet.BroadcastTokenTransfer(
			t.Context(), config, issueTokenTransaction,
			[]keys.Private{config.IdentityPrivateKey},
		)
		require.NoError(t, err, "failed to broadcast issuance token transaction")
		finalMintTransactionHash, err = utils.HashTokenTransaction(finalIssueTokenTransaction, false)
		require.NoError(t, err, "failed to hash final issue token transaction")

		var shouldFailTransfer *tokenpb.TokenTransaction
		if !batchTransfer {
			shouldFailTransfer, _, err = createTestTokenTransferTransactionTokenPb(t, config, finalMintTransactionHash, tokenIdentityPubKey)
			require.NoError(t, err, "failed to create transfer transaction")
		} else {
			shouldFailTransfer = &tokenpb.TokenTransaction{
				Version: TokenTransactionVersion2,
				TokenInputs: &tokenpb.TokenTransaction_TransferInput{
					TransferInput: &tokenpb.TokenTransferInput{
						OutputsToSpend: []*tokenpb.TokenOutputToSpend{
							{
								PrevTokenTransactionHash: finalMintTransactionHash,
								PrevTokenTransactionVout: 0,
							},
							{
								PrevTokenTransactionHash: finalMintTransactionHash,
								PrevTokenTransactionVout: 1,
							},
						},
					},
				},
				TokenOutputs: []*tokenpb.TokenOutput{
					{
						OwnerPublicKey:  receiver1PubKey.Serialize(),
						TokenIdentifier: tokenIdentifier,
						TokenAmount:     int64ToUint128Bytes(0, testIssueOutput1Amount),
					},
					{
						OwnerPublicKey:  receiver2PubKey.Serialize(),
						TokenIdentifier: tokenIdentifier,
						TokenAmount:     int64ToUint128Bytes(0, testIssueOutput2Amount),
					},
				},
				Network:                         config.ProtoNetwork(),
				SparkOperatorIdentityPublicKeys: getSigningOperatorPublicKeyBytes(config),
				ClientCreatedTimestamp:          timestamppb.New(time.Now()),
			}
		}
		shouldFailTransfer.InvoiceAttachments = invoiceAttachments

		_, _, err = wallet.StartTokenTransaction(
			t.Context(),
			config,
			shouldFailTransfer,
			[]keys.Private{config.IdentityPrivateKey, config.IdentityPrivateKey},
			wallet.DefaultValidityDuration,
			nil,
		)
		require.Error(t, err, "expected error when transfer fails if unexpired transaction exists")
		return
	}
	startResponseTransactionResult := &TransactionResult{
		config:     config,
		resp:       startResp,
		txFullHash: finalTxHash,
	}

	if expiredAtSign {
		time.Sleep(time.Second * 6)
	}
	_, err = signAndCommitTransaction(t, startResponseTransactionResult, []keys.Private{config.IdentityPrivateKey, config.IdentityPrivateKey})
	if expiredAtSign {
		require.Error(t, err, "expected error when expired at sign")
		return
	}
	require.NoError(t, err, "failed to sign and commit transaction")

	queryTokenTransactionParms := wallet.QueryTokenTransactionsParams{
		IssuerPublicKeys:  nil,
		OwnerPublicKeys:   nil,
		OutputIDs:         nil,
		TransactionHashes: [][]byte{finalTxHash},
		Offset:            0,
		Limit:             1,
	}
	tokenTransactionResponse, err := wallet.QueryTokenTransactions(
		t.Context(),
		config,
		queryTokenTransactionParms,
	)
	require.NoError(t, err, "failed to query token transactions")
	require.Len(t, tokenTransactionResponse.TokenTransactionsWithStatus, 1, "expected 1 token transaction")
	// match the length of the outputs since we create one spark invoice per output in batch testing
	expectedLen := len(transferTransaction.TokenOutputs)
	require.Len(t, tokenTransactionResponse.TokenTransactionsWithStatus[0].TokenTransaction.GetInvoiceAttachments(), expectedLen, "expected same number of outputs")

	invoicesToQuery := make([]string, 0, len(invoiceAttachments))
	for _, invoiceAttachment := range invoiceAttachments {
		invoicesToQuery = append(invoicesToQuery, invoiceAttachment.GetSparkInvoice())
	}
	invoiceResponse, err := wallet.QuerySparkInvoicesByRawString(
		t.Context(),
		config,
		invoicesToQuery,
	)
	require.NoError(t, err, "failed to query spark invoices")
	require.Len(t, invoiceResponse.InvoiceStatuses, len(invoicesToQuery))
	for i, invoiceResponse := range invoiceResponse.InvoiceStatuses {
		require.Equal(t, invoiceResponse.Invoice, invoicesToQuery[i])
		require.Equal(t, sparkpb.InvoiceStatus_FINALIZED, invoiceResponse.Status)
		require.Equal(t, &sparkpb.InvoiceResponse_TokenTransfer{
			TokenTransfer: &sparkpb.TokenTransfer{
				FinalTokenTransactionHash: finalTxHash[:],
			},
		}, invoiceResponse.TransferType)
	}
}

type createSparkInvoiceParams struct {
	Version           uint32
	ReceiverPublicKey keys.Public
	SenderPublicKey   keys.Public
	Amount            *uint64
	ExpiryTime        *timestamppb.Timestamp
	Memo              *string
	TokenIdentifier   []byte
	Network           common.Network
	SatsPayment       bool
	// Optional: include a signature by the receiver over the invoice fields
	SignerPrivKey keys.Private
}

func createSparkInvoice(params createSparkInvoiceParams) (string, error) {
	version := params.Version
	receiverPublicKey := params.ReceiverPublicKey
	senderPublicKeyPtr := params.SenderPublicKey
	amount := params.Amount
	expiryTime := params.ExpiryTime
	memo := params.Memo
	tokenIdentifier := params.TokenIdentifier
	network := params.Network
	satsPayment := params.SatsPayment

	var senderPublicKey []byte
	if senderPublicKeyPtr != (keys.Public{}) {
		senderPublicKey = senderPublicKeyPtr.Serialize()
	}

	uuid := uuid.New()
	sparkInvoiceFields := &sparkpb.SparkInvoiceFields{
		Version:         version,
		Id:              uuid[:],
		ExpiryTime:      expiryTime,
		Memo:            memo,
		SenderPublicKey: senderPublicKey,
	}
	if satsPayment {
		sparkInvoiceFields.PaymentType = &sparkpb.SparkInvoiceFields_SatsPayment{
			SatsPayment: &sparkpb.SatsPayment{
				Amount: amount,
			},
		}
	} else {
		var amountBytes []byte
		if amount != nil {
			amountBytes = int64ToUint128Bytes(0, *amount)
		}
		sparkInvoiceFields.PaymentType = &sparkpb.SparkInvoiceFields_TokensPayment{
			TokensPayment: &sparkpb.TokensPayment{
				TokenIdentifier: tokenIdentifier,
				Amount:          amountBytes,
			},
		}
	}
	// If a signer key is provided, compute a signature and use the WithSignature helper
	if params.SignerPrivKey != (keys.Private{}) {
		hash, err := common.HashSparkInvoiceFields(sparkInvoiceFields, network, receiverPublicKey)
		if err != nil {
			return "", err
		}
		sig, err := schnorr.Sign(params.SignerPrivKey.ToBTCEC(), hash)
		if err != nil {
			return "", err
		}
		return common.EncodeSparkAddressWithSignature(receiverPublicKey.Serialize(), network, sparkInvoiceFields, sig.Serialize())
	}

	sparkAddress, err := common.EncodeSparkAddress(receiverPublicKey.Serialize(), network, sparkInvoiceFields)
	if err != nil {
		return "", err
	}
	return sparkAddress, nil
}
