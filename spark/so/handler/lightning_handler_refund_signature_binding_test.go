package handler

import (
	"bytes"
	"context"
	"testing"

	"github.com/btcsuite/btcd/wire"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pbfrost "github.com/lightsparkdev/spark/proto/frost"
	pbspark "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

type messageCheckingFrostServiceClient struct {
	mockFrostServiceClient
	expectedMessage []byte
}

func (m *messageCheckingFrostServiceClient) ValidateSignatureShare(
	ctx context.Context,
	req *pbfrost.ValidateSignatureShareRequest,
	_ ...grpc.CallOption,
) (*emptypb.Empty, error) {
	if !bytes.Equal(req.Message, m.expectedMessage) {
		return nil, status.Error(codes.InvalidArgument, "signature share does not match recomputed sighash")
	}
	return &emptypb.Empty{}, nil
}

func makeRefundSigningJob(leafID string, rawTx []byte) *pbspark.UserSignedTxSigningJob {
	return &pbspark.UserSignedTxSigningJob{
		LeafId: leafID,
		SigningCommitments: &pbspark.SigningCommitments{
			SigningCommitments: map[string]*pbcommon.SigningCommitment{
				"test": {
					Hiding:  []byte("test_hiding"),
					Binding: []byte("test_binding"),
				},
			},
		},
		SigningNonceCommitment: &pbcommon.SigningCommitment{
			Hiding:  []byte("test_nonce_hiding"),
			Binding: []byte("test_nonce_binding"),
		},
		UserSignature: []byte("user_signature_share"),
		RawTx:         rawTx,
	}
}

func mutateRefundSequence(t *testing.T, rawTx []byte) []byte {
	t.Helper()

	refundTx, err := common.TxFromRawTxBytes(rawTx)
	require.NoError(t, err)
	require.NotEmpty(t, refundTx.TxIn)
	require.NotZero(t, refundTx.TxIn[0].Sequence)
	refundTx.TxIn[0].Sequence--

	mutated, err := common.SerializeTx(refundTx)
	require.NoError(t, err)
	return mutated
}

func computeRefundSighash(t *testing.T, prevRawTx []byte, refundRawTx []byte) []byte {
	t.Helper()

	prevTx, err := common.TxFromRawTxBytes(prevRawTx)
	require.NoError(t, err)
	require.NotEmpty(t, prevTx.TxOut)

	refundTx, err := common.TxFromRawTxBytes(refundRawTx)
	require.NoError(t, err)

	sighash, err := common.SigHashFromTx(refundTx, 0, prevTx.TxOut[0])
	require.NoError(t, err)
	return sighash
}

func TestValidateGetPreimageRequestBindsRefundSignatureSharesToSubmittedTxBytes(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	lightningHandler := NewLightningHandler(&so.Config{})
	destinationPubKey := keys.GeneratePrivateKey().Public()
	leaf := createDbLeaf(t, ctx, false)
	paymentHash := bytes.Repeat([]byte{0x11}, 32)

	type txVariant struct {
		name               string
		buildRawTx         func(*testing.T, *testLeaf, keys.Public) []byte
		prevRawTx          func(*testLeaf) []byte
		assign             func(*pbspark.UserSignedTxSigningJob) (cpfp, direct, directFromCpfp []*pbspark.UserSignedTxSigningJob)
		expectedErrMessage string
	}

	tests := []txVariant{
		{
			name:       "cpfp",
			buildRawTx: makeClientCpfpTx,
			prevRawTx: func(leaf *testLeaf) []byte {
				return leaf.node.RawTx
			},
			assign: func(job *pbspark.UserSignedTxSigningJob) ([]*pbspark.UserSignedTxSigningJob, []*pbspark.UserSignedTxSigningJob, []*pbspark.UserSignedTxSigningJob) {
				return []*pbspark.UserSignedTxSigningJob{job}, nil, nil
			},
			expectedErrMessage: "unable to validate cpfp signature share",
		},
		{
			name:       "direct",
			buildRawTx: makeClientDirectTx,
			prevRawTx: func(leaf *testLeaf) []byte {
				return leaf.node.DirectTx
			},
			assign: func(job *pbspark.UserSignedTxSigningJob) ([]*pbspark.UserSignedTxSigningJob, []*pbspark.UserSignedTxSigningJob, []*pbspark.UserSignedTxSigningJob) {
				return nil, []*pbspark.UserSignedTxSigningJob{job}, nil
			},
			expectedErrMessage: "unable to validate direct signature share",
		},
		{
			name:       "direct_from_cpfp",
			buildRawTx: makeClientDirectFromCpfpTx,
			prevRawTx: func(leaf *testLeaf) []byte {
				return leaf.node.RawTx
			},
			assign: func(job *pbspark.UserSignedTxSigningJob) ([]*pbspark.UserSignedTxSigningJob, []*pbspark.UserSignedTxSigningJob, []*pbspark.UserSignedTxSigningJob) {
				return nil, nil, []*pbspark.UserSignedTxSigningJob{job}
			},
			expectedErrMessage: "unable to validate direct from cpfp signature share",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			originalRawTx := tt.buildRawTx(t, leaf, destinationPubKey)
			expectedMessage := computeRefundSighash(t, tt.prevRawTx(leaf), originalRawTx)
			originalJob := makeRefundSigningJob(leaf.node.ID.String(), originalRawTx)
			cpfpTransactions, directTransactions, directFromCpfpTransactions := tt.assign(originalJob)

			err := lightningHandler.validateGetPreimageRequestWithFrostServiceClientFactory(
				ctx,
				&trackingFrostServiceClientConnection{
					client: &messageCheckingFrostServiceClient{
						expectedMessage: expectedMessage,
					},
				},
				paymentHash,
				cpfpTransactions,
				directTransactions,
				directFromCpfpTransactions,
				&pbspark.InvoiceAmount{ValueSats: 0},
				destinationPubKey,
				0,
				pbspark.InitiatePreimageSwapRequest_REASON_RECEIVE,
				false,
			)
			require.NoError(t, err)

			mutatedRawTx := mutateRefundSequence(t, originalRawTx)
			mutatedJob := makeRefundSigningJob(leaf.node.ID.String(), mutatedRawTx)
			cpfpTransactions, directTransactions, directFromCpfpTransactions = tt.assign(mutatedJob)

			err = lightningHandler.validateGetPreimageRequestWithFrostServiceClientFactory(
				ctx,
				&trackingFrostServiceClientConnection{
					client: &messageCheckingFrostServiceClient{
						expectedMessage: expectedMessage,
					},
				},
				paymentHash,
				cpfpTransactions,
				directTransactions,
				directFromCpfpTransactions,
				&pbspark.InvoiceAmount{ValueSats: 0},
				destinationPubKey,
				0,
				pbspark.InitiatePreimageSwapRequest_REASON_RECEIVE,
				false,
			)
			require.ErrorContains(t, err, tt.expectedErrMessage)
			code, reason := sparkerrors.CodeAndReasonFrom(err)
			require.Equal(t, codes.FailedPrecondition, code)
			require.Equal(t, "BAD_SIGNATURE", reason)
		})
	}
}

func TestValidateGetPreimageRequestRejectsSignatureShareValidationFailure(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	lightningHandler := NewLightningHandler(&so.Config{})
	destinationPubKey := keys.GeneratePrivateKey().Public()
	leaf := createDbLeaf(t, ctx, false)
	paymentHash := bytes.Repeat([]byte{0x22}, 32)

	originalRawTx := makeClientCpfpTx(t, leaf, destinationPubKey)
	refundTx, err := common.TxFromRawTxBytes(originalRawTx)
	require.NoError(t, err)
	require.NotEmpty(t, refundTx.TxIn)
	refundTx.TxIn[0].Sequence = wire.MaxTxInSequenceNum
	mutatedRawTx, err := common.SerializeTx(refundTx)
	require.NoError(t, err)

	err = lightningHandler.validateGetPreimageRequestWithFrostServiceClientFactory(
		ctx,
		&trackingFrostServiceClientConnection{
			client: &messageCheckingFrostServiceClient{
				expectedMessage: computeRefundSighash(t, leaf.node.RawTx, originalRawTx),
			},
		},
		paymentHash,
		[]*pbspark.UserSignedTxSigningJob{makeRefundSigningJob(leaf.node.ID.String(), mutatedRawTx)},
		nil,
		nil,
		&pbspark.InvoiceAmount{ValueSats: 0},
		destinationPubKey,
		0,
		pbspark.InitiatePreimageSwapRequest_REASON_RECEIVE,
		false,
	)

	require.ErrorContains(t, err, "unable to validate cpfp signature share")
	code, reason := sparkerrors.CodeAndReasonFrom(err)
	require.Equal(t, codes.FailedPrecondition, code)
	require.Equal(t, "BAD_SIGNATURE", reason)
}
