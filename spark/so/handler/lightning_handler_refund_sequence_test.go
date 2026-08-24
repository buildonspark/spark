package handler

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	pbspark "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func refundWithSequence(t *testing.T, rawTx []byte, sequence uint32) []byte {
	t.Helper()
	tx, err := common.TxFromRawTxBytes(rawTx)
	require.NoError(t, err)
	require.Len(t, tx.TxIn, 1)
	tx.TxIn[0].Sequence = sequence
	serialized, err := common.SerializeTx(tx)
	require.NoError(t, err)
	return serialized
}

func validateReceiveRefundJob(
	t *testing.T,
	ctx context.Context,
	leaf *testLeaf,
	destination keys.Public,
	assign func(*pbspark.UserSignedTxSigningJob) (cpfp, direct, directFromCpfp []*pbspark.UserSignedTxSigningJob),
	rawTx []byte,
) error {
	t.Helper()
	job := makeRefundSigningJob(leaf.node.ID.String(), rawTx)
	cpfp, direct, directFromCpfp := assign(job)
	return NewLightningHandler(&so.Config{}).validateGetPreimageRequestWithFrostServiceClientFactory(
		ctx,
		&mockFrostServiceClientConnection{},
		bytes.Repeat([]byte{0x41}, 32),
		cpfp,
		direct,
		directFromCpfp,
		0,
		destination,
		singleLeafDestination(destination),
		0,
		pbspark.InitiatePreimageSwapRequest_REASON_RECEIVE,
		false,
	)
}

func TestValidateGetPreimageRequestEnforcesCanonicalRefundSequences(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	destination := keys.GeneratePrivateKey().Public()
	leaf := createDbLeaf(t, ctx, false)

	type refundVariant struct {
		name            string
		canonical       func(*testing.T, *testLeaf, keys.Public) []byte
		currentSequence uint32
		assign          func(*pbspark.UserSignedTxSigningJob) (cpfp, direct, directFromCpfp []*pbspark.UserSignedTxSigningJob)
	}
	variants := []refundVariant{
		{
			name:            "cpfp",
			canonical:       makeClientCpfpTx,
			currentSequence: testTimeLock,
			assign: func(job *pbspark.UserSignedTxSigningJob) ([]*pbspark.UserSignedTxSigningJob, []*pbspark.UserSignedTxSigningJob, []*pbspark.UserSignedTxSigningJob) {
				return []*pbspark.UserSignedTxSigningJob{job}, nil, nil
			},
		},
		{
			name:            "direct",
			canonical:       makeClientDirectTx,
			currentSequence: testTimeLock + spark.DirectTimelockOffset,
			assign: func(job *pbspark.UserSignedTxSigningJob) ([]*pbspark.UserSignedTxSigningJob, []*pbspark.UserSignedTxSigningJob, []*pbspark.UserSignedTxSigningJob) {
				return nil, []*pbspark.UserSignedTxSigningJob{job}, nil
			},
		},
		{
			name:            "direct_from_cpfp",
			canonical:       makeClientDirectFromCpfpTx,
			currentSequence: testTimeLock + spark.DirectTimelockOffset,
			assign: func(job *pbspark.UserSignedTxSigningJob) ([]*pbspark.UserSignedTxSigningJob, []*pbspark.UserSignedTxSigningJob, []*pbspark.UserSignedTxSigningJob) {
				return nil, nil, []*pbspark.UserSignedTxSigningJob{job}
			},
		},
	}

	for _, variant := range variants {
		t.Run(variant.name, func(t *testing.T) {
			canonical := variant.canonical(t, leaf, destination)
			require.NoError(t, validateReceiveRefundJob(t, ctx, leaf, destination, variant.assign, canonical))

			for _, invalid := range []struct {
				name     string
				sequence uint32
				reason   string
			}{
				{name: "does_not_decrement", sequence: variant.currentSequence, reason: sparkerrors.ReasonInvalidArgumentTimelockMismatch},
				{name: "shortened_beyond_one_step", sequence: canonicalSequence(t, canonical) - spark.TimeLockInterval, reason: sparkerrors.ReasonInvalidArgumentTimelockMismatch},
				{name: "zero_delay", sequence: 0, reason: sparkerrors.ReasonInvalidArgumentTimelockMismatch},
				{name: "zero_delay_with_spark_flag", sequence: spark.ZeroSequence, reason: sparkerrors.ReasonInvalidArgumentTimelockMismatch},
				{name: "unsupported_high_bit", sequence: canonicalSequence(t, canonical) | 1<<29, reason: sparkerrors.ReasonInvalidArgumentMalformedField},
			} {
				t.Run(invalid.name, func(t *testing.T) {
					err := validateReceiveRefundJob(t, ctx, leaf, destination, variant.assign, refundWithSequence(t, canonical, invalid.sequence))
					require.Error(t, err)
					code, reason := sparkerrors.CodeAndReasonFrom(err)
					require.Equal(t, codes.InvalidArgument, code)
					require.Equal(t, invalid.reason, reason)
				})
			}
		})
	}
}

func canonicalSequence(t *testing.T, rawTx []byte) uint32 {
	t.Helper()
	tx, err := common.TxFromRawTxBytes(rawTx)
	require.NoError(t, err)
	require.Len(t, tx.TxIn, 1)
	return tx.TxIn[0].Sequence
}

func TestValidateGetPreimageRequestPreservesLeafRenewalRequired(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	destination := keys.GeneratePrivateKey().Public()
	leaf := createDbLeafWithRefundTimelock(t, ctx, false, spark.TimeLockInterval)
	rawTx := makeClientCpfpTxWithSequence(t, leaf, destination, 0)

	err := validateReceiveRefundJob(
		t,
		ctx,
		leaf,
		destination,
		func(job *pbspark.UserSignedTxSigningJob) ([]*pbspark.UserSignedTxSigningJob, []*pbspark.UserSignedTxSigningJob, []*pbspark.UserSignedTxSigningJob) {
			return []*pbspark.UserSignedTxSigningJob{job}, nil, nil
		},
		rawTx,
	)

	require.Error(t, err)
	code, reason := sparkerrors.CodeAndReasonFrom(err)
	require.Equal(t, codes.InvalidArgument, code)
	require.Equal(t, sparkerrors.ReasonInvalidArgumentLeafRenewalRequired, reason)
}

func TestPreimageSwapReceivePathsRejectNoncanonicalRefundSequence(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	destination := keys.GeneratePrivateKey().Public()
	leaf := createDbLeaf(t, ctx, false)
	noncanonical := makeClientCpfpTxWithSequence(t, leaf, destination, testTimeLock)
	job := makeRefundSigningJob(leaf.node.ID.String(), noncanonical)
	job.SigningPublicKey = leaf.node.OwnerSigningPubkey.Serialize()
	paymentHash := bytes.Repeat([]byte{0x52}, 32)
	transferID := uuid.NewString()
	config := sparktesting.TestConfig(t)

	assertTimelockMismatch := func(t *testing.T, err error) {
		t.Helper()
		require.Error(t, err)
		code, reason := sparkerrors.CodeAndReasonFrom(err)
		require.Equal(t, codes.InvalidArgument, code)
		require.Equal(t, sparkerrors.ReasonInvalidArgumentTimelockMismatch, reason, "unexpected error: %v", err)
	}

	t.Run("v3", func(t *testing.T) {
		req := &pbspark.InitiatePreimageSwapRequest{
			PaymentHash:               paymentHash,
			ReceiverIdentityPublicKey: destination.Serialize(),
			Reason:                    pbspark.InitiatePreimageSwapRequest_REASON_RECEIVE,
			TransferRequest: &pbspark.StartTransferRequest{
				TransferId:                transferID,
				ExpiryTime:                timestamppb.New(time.Now().Add(time.Hour)),
				OwnerIdentityPublicKey:    leaf.node.OwnerIdentityPubkey.Serialize(),
				ReceiverIdentityPublicKey: destination.Serialize(),
				TransferPackage:           &pbspark.TransferPackage{LeavesToSend: []*pbspark.UserSignedTxSigningJob{job}},
			},
		}

		_, err := NewInitiatePreimageSwapFlowHandler(config).prepareState(ctx, req, nil)
		assertTimelockMismatch(t, err)
	})

	t.Run("v4", func(t *testing.T) {
		attestor := keys.GeneratePrivateKey()
		commitment := keys.GeneratePrivateKey().Public().Serialize()
		job.SigningNonceCommitment.Binding = commitment
		job.SigningNonceCommitment.Hiding = commitment
		for _, operatorCommitment := range job.GetSigningCommitments().GetSigningCommitments() {
			operatorCommitment.Binding = commitment
			operatorCommitment.Hiding = commitment
		}
		manifest := &pbspark.TransferManifest{
			Version:    common.SupportedTransferManifestVersion,
			TransferId: transferID,
			Network:    pbspark.Network_REGTEST,
			Edges: []*pbspark.ManifestEdge{{
				SenderIdentityPublicKey:   leaf.node.OwnerIdentityPubkey.Serialize(),
				ReceiverIdentityPublicKey: destination.Serialize(),
				Amount:                    &pbspark.ManifestAmount{Amount: &pbspark.ManifestAmount_Sats{Sats: leaf.node.Value}},
			}},
		}
		manifestHash, err := common.HashTransferManifest(manifest)
		require.NoError(t, err)
		target, err := common.ReceiveAttestorTarget(paymentHash)
		require.NoError(t, err)
		digest, err := common.QuoteEnvelopeDigest(
			manifest.GetNetwork(), manifestHash, common.QuoteReasonReceive, common.QuoteRoleAttestor, target,
		)
		require.NoError(t, err)

		req := &pbspark.InitiatePreimageSwapV4Request{
			PaymentHash:               paymentHash,
			Reason:                    pbspark.InitiatePreimageSwapRequest_REASON_RECEIVE,
			AttestorIdentityPublicKey: attestor.Public().Serialize(),
			AttestorSignature:         ecdsa.Sign(attestor.ToBTCEC(), digest).Serialize(),
			TransferV3Request: &pbspark.StartTransferV3Request{
				TransferId:       transferID,
				ExpiryTime:       timestamppb.New(time.Now().Add(time.Hour)),
				TransferManifest: manifest,
				SenderPackages: []*pbspark.SenderTransferPackage{{
					OwnerIdentityPublicKey: leaf.node.OwnerIdentityPubkey.Serialize(),
					TransferPackage: withDummyPackageAuth(&pbspark.TransferPackage{
						LeavesToSend: []*pbspark.UserSignedTxSigningJob{job},
					}),
					ReceiverIdentityPublicKeys: map[string][]byte{leaf.node.ID.String(): destination.Serialize()},
				}},
			},
		}

		_, err = NewInitiatePreimageSwapV4FlowHandler(config).prepareStateV4(ctx, req, nil)
		assertTimelockMismatch(t, err)
	})
}
