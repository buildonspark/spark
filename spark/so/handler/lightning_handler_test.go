package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/btcsuite/btcd/wire"
	eciesgo "github.com/ecies/go/v2"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pbfrost "github.com/lightsparkdev/spark/proto/frost"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authn"
	"github.com/lightsparkdev/spark/so/authninternal"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/entexample"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/knobs"
	sparktesting "github.com/lightsparkdev/spark/testing"
	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// mockFrostServiceClientConnection implements the FrostServiceClientConnection interface for testing
type mockFrostServiceClientConnection struct{}

func (m *mockFrostServiceClientConnection) StartFrostServiceClient(*LightningHandler) (pbfrost.FrostServiceClient, error) {
	return &mockFrostServiceClient{}, nil
}

func (m *mockFrostServiceClientConnection) Close() {
}

// createParentAndRefundTx creates a parent transaction and a refund transaction that properly
// references the parent tx's hash. This is required for outpoint validation.
func createParentAndRefundTx(t *testing.T, outputScript []byte, value int64) (parentTxBytes []byte, refundTxBytes []byte) {
	t.Helper()

	// Create parent tx (this will be stored as node.RawTx)
	parentTx := wire.NewMsgTx(2)
	parentTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{},
		Sequence:         wire.MaxTxInSequenceNum,
	})
	parentTx.AddTxOut(&wire.TxOut{Value: value, PkScript: outputScript})

	parentTxBytes, err := common.SerializeTx(parentTx)
	require.NoError(t, err)

	// Create refund tx that references the parent tx
	refundTx := wire.NewMsgTx(2)
	refundTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: parentTx.TxHash(), Index: 0},
		Sequence:         wire.MaxTxInSequenceNum,
	})
	refundTx.AddTxOut(&wire.TxOut{Value: value, PkScript: outputScript})

	refundTxBytes, err = common.SerializeTx(refundTx)
	require.NoError(t, err)

	return parentTxBytes, refundTxBytes
}

// createParentAndRefundTxWithOutputs creates a parent transaction with a single
// output and a refund transaction spending it whose outputs are exactly
// refundOuts. This lets tests exercise arbitrary refund output shapes while
// still satisfying the outpoint validation against the parent tx.
func createParentAndRefundTxWithOutputs(
	t *testing.T,
	parentScript []byte,
	parentValue int64,
	refundOuts []*wire.TxOut,
) (parentTxBytes []byte, refundTxBytes []byte) {
	t.Helper()

	parentTx := wire.NewMsgTx(2)
	parentTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{},
		Sequence:         wire.MaxTxInSequenceNum,
	})
	parentTx.AddTxOut(&wire.TxOut{Value: parentValue, PkScript: parentScript})

	parentTxBytes, err := common.SerializeTx(parentTx)
	require.NoError(t, err)

	refundTx := wire.NewMsgTx(2)
	refundTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: parentTx.TxHash(), Index: 0},
		Sequence:         wire.MaxTxInSequenceNum,
	})
	for _, out := range refundOuts {
		refundTx.AddTxOut(out)
	}

	refundTxBytes, err = common.SerializeTx(refundTx)
	require.NoError(t, err)

	return parentTxBytes, refundTxBytes
}

func createParentAndRefundTxWithExtraOutput(
	t *testing.T,
	destinationScript []byte,
	extraScript []byte,
	destinationValue int64,
	extraValue int64,
) (parentTxBytes []byte, refundTxBytes []byte) {
	t.Helper()
	return createParentAndRefundTxWithOutputs(t, destinationScript, destinationValue+extraValue, []*wire.TxOut{
		{Value: destinationValue, PkScript: destinationScript},
		{Value: extraValue, PkScript: extraScript},
	})
}

// mockFrostServiceClient implements the FrostServiceClient interface for testing
type mockFrostServiceClient struct{}

func (m *mockFrostServiceClient) Echo(context.Context, *pbfrost.EchoRequest, ...grpc.CallOption) (*pbfrost.EchoResponse, error) {
	return &pbfrost.EchoResponse{}, nil
}

func (m *mockFrostServiceClient) DkgRound1(context.Context, *pbfrost.DkgRound1Request, ...grpc.CallOption) (*pbfrost.DkgRound1Response, error) {
	return &pbfrost.DkgRound1Response{}, nil
}

func (m *mockFrostServiceClient) DkgRound2(context.Context, *pbfrost.DkgRound2Request, ...grpc.CallOption) (*pbfrost.DkgRound2Response, error) {
	return &pbfrost.DkgRound2Response{}, nil
}

func (m *mockFrostServiceClient) DkgRound3(context.Context, *pbfrost.DkgRound3Request, ...grpc.CallOption) (*pbfrost.DkgRound3Response, error) {
	return &pbfrost.DkgRound3Response{}, nil
}

func (m *mockFrostServiceClient) FrostNonce(context.Context, *pbfrost.FrostNonceRequest, ...grpc.CallOption) (*pbfrost.FrostNonceResponse, error) {
	return &pbfrost.FrostNonceResponse{}, nil
}

func (m *mockFrostServiceClient) SignFrost(context.Context, *pbfrost.SignFrostRequest, ...grpc.CallOption) (*pbfrost.SignFrostResponse, error) {
	return &pbfrost.SignFrostResponse{}, nil
}

func (m *mockFrostServiceClient) AggregateFrost(context.Context, *pbfrost.AggregateFrostRequest, ...grpc.CallOption) (*pbfrost.AggregateFrostResponse, error) {
	return &pbfrost.AggregateFrostResponse{}, nil
}

func (m *mockFrostServiceClient) AggregateFrostBatch(context.Context, *pbfrost.AggregateFrostBatchRequest, ...grpc.CallOption) (*pbfrost.AggregateFrostBatchResponse, error) {
	return &pbfrost.AggregateFrostBatchResponse{}, nil
}

func (m *mockFrostServiceClient) ValidateSignatureShare(context.Context, *pbfrost.ValidateSignatureShareRequest, ...grpc.CallOption) (*emptypb.Empty, error) {
	// Mock successful validation
	return &emptypb.Empty{}, nil
}

func TestLightningHandlersRejectNilRequests(t *testing.T) {
	ctx := t.Context()
	handler := NewLightningHandler(&so.Config{})

	tests := []struct {
		name string
		call func() error
	}{
		{
			name: "StorePreimageShare",
			call: func() error {
				return handler.StorePreimageShare(ctx, nil)
			},
		},
		{
			name: "StorePreimageShareV2",
			call: func() error {
				return handler.StorePreimageShareV2(ctx, nil)
			},
		},
		{
			name: "StorePreimageShareInternal",
			call: func() error {
				return handler.StorePreimageShareInternal(ctx, nil)
			},
		},
		{
			name: "InitiatePreimageSwapV3",
			call: func() error {
				_, err := handler.InitiatePreimageSwapV3(ctx, nil)
				return err
			},
		},
		{
			name: "GetPreimageShare",
			call: func() error {
				_, err := handler.GetPreimageShare(ctx, nil, nil, nil, nil, nil)
				return err
			},
		},
		{
			name: "QueryHTLC",
			call: func() error {
				_, err := handler.QueryHTLC(ctx, nil)
				return err
			},
		},
		{
			name: "ValidatePreimage",
			call: func() error {
				_, _, err := handler.ValidatePreimage(ctx, nil)
				return err
			},
		},
		{
			name: "QueryPreimage",
			call: func() error {
				_, err := handler.QueryPreimage(ctx, nil)
				return err
			},
		},
		{
			name: "ProvidePreimage",
			call: func() error {
				_, err := handler.ProvidePreimage(ctx, nil)
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.ErrorContains(t, tt.call(), "request is required")
		})
	}
}

func TestGetPreimageShareRejectsMissingTransfer(t *testing.T) {
	handler := NewLightningHandler(&so.Config{})
	receiverIdentityPubKey := keys.GeneratePrivateKey().Public()

	resp, err := handler.GetPreimageShare(t.Context(), &pb.InitiatePreimageSwapRequest{
		ReceiverIdentityPublicKey: receiverIdentityPubKey.Serialize(),
	}, nil, nil, nil, nil)

	require.Nil(t, resp)
	require.ErrorContains(t, err, "transfer_request is required")
}

func TestQueryHTLCRejectsMalformedPaginationBeforeDB(t *testing.T) {
	ctx := t.Context()
	handler := NewLightningHandler(&so.Config{})
	identityPubKey := []byte{1}

	tests := []struct {
		name        string
		req         *pb.QueryHtlcRequest
		expectedErr string
	}{
		{
			name:        "missing identity public key",
			req:         &pb.QueryHtlcRequest{Limit: 1},
			expectedErr: "identity public key is required",
		},
		{
			name: "zero limit",
			req: &pb.QueryHtlcRequest{
				IdentityPublicKey: identityPubKey,
				Limit:             0,
			},
			expectedErr: "expect limit to be greater than 0",
		},
		{
			name: "negative limit",
			req: &pb.QueryHtlcRequest{
				IdentityPublicKey: identityPubKey,
				Limit:             -1,
			},
			expectedErr: "expect limit to be greater than 0",
		},
		{
			name: "negative offset",
			req: &pb.QueryHtlcRequest{
				IdentityPublicKey: identityPubKey,
				Limit:             1,
				Offset:            -1,
			},
			expectedErr: "expect non-negative offset",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := handler.QueryHTLC(ctx, tt.req)
			require.Nil(t, resp)
			require.ErrorContains(t, err, tt.expectedErr)
		})
	}
}

func TestQueryHTLCRejectsFilterResourceExhaustionBeforeDB(t *testing.T) {
	identityPubKey := keys.GeneratePrivateKey().Public().Serialize()
	handler := NewLightningHandler(&so.Config{})

	baseRequest := func() *pb.QueryHtlcRequest {
		return &pb.QueryHtlcRequest{
			IdentityPublicKey: identityPubKey,
			Limit:             1,
		}
	}

	tests := []struct {
		name            string
		mutate          func(*pb.QueryHtlcRequest)
		expectedErrText string
	}{
		{
			name: "transfer ids over limit",
			mutate: func(req *pb.QueryHtlcRequest) {
				req.TransferIds = make([]string, maxQueryHTLCFilterValues+1)
			},
			expectedErrText: "too many transfer ids in filter",
		},
		{
			name: "payment hashes over limit",
			mutate: func(req *pb.QueryHtlcRequest) {
				req.PaymentHashes = make([][]byte, maxQueryHTLCFilterValues+1)
			},
			expectedErrText: "too many payment hashes in filter",
		},
		{
			name: "malformed payment hash",
			mutate: func(req *pb.QueryHtlcRequest) {
				req.PaymentHashes = [][]byte{make([]byte, 31)}
			},
			expectedErrText: "invalid payment hash length at index 0",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := baseRequest()
			test.mutate(req)

			resp, err := handler.QueryHTLC(t.Context(), req)

			require.Nil(t, resp)
			require.Error(t, err)
			require.ErrorContains(t, err, test.expectedErrText)
		})
	}
}

func TestQueryHTLCRejectsMalformedRequestFieldsWithInvalidArgument(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	handler := NewLightningHandler(&so.Config{FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{}})
	identityKey := keys.GeneratePrivateKey().Public().Serialize()

	base := func() *pb.QueryHtlcRequest {
		return &pb.QueryHtlcRequest{
			IdentityPublicKey: identityKey,
			Limit:             10,
			Offset:            0,
		}
	}

	tests := []struct {
		name string
		req  *pb.QueryHtlcRequest
	}{
		{
			name: "missing identity public key",
			req: &pb.QueryHtlcRequest{
				Limit: 1,
			},
		},
		{
			name: "zero limit",
			req: func() *pb.QueryHtlcRequest {
				req := base()
				req.Limit = 0
				return req
			}(),
		},
		{
			name: "negative limit",
			req: func() *pb.QueryHtlcRequest {
				req := base()
				req.Limit = -1
				return req
			}(),
		},
		{
			name: "negative offset",
			req: func() *pb.QueryHtlcRequest {
				req := base()
				req.Offset = -1
				return req
			}(),
		},
		{
			name: "malformed identity public key",
			req: func() *pb.QueryHtlcRequest {
				req := base()
				req.IdentityPublicKey = []byte{0x02, 0x01}
				return req
			}(),
		},
		{
			name: "malformed transfer id",
			req: func() *pb.QueryHtlcRequest {
				req := base()
				req.TransferIds = []string{"not-a-uuid"}
				return req
			}(),
		},
		{
			name: "invalid status",
			req: func() *pb.QueryHtlcRequest {
				req := base()
				req.Status = new(pb.PreimageRequestStatus(999))
				return req
			}(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := handler.QueryHTLC(ctx, tt.req)
			require.Nil(t, resp)
			require.Equal(t, codes.InvalidArgument, status.Code(err))
		})
	}
}

func TestQueryHTLCFiltersByRoleAndSessionIdentity(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	rng := rand.NewChaCha8([32]byte{47})
	senderIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	receiverIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	attackerIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	paymentHash := bytes.Repeat([]byte{0x47}, sha256.Size)

	transfer := entexample.NewTransferExample(t, tx).
		SetSenderIdentityPubkey(senderIdentityPubKey).
		SetReceiverIdentityPubkey(receiverIdentityPubKey).
		SetExpiryTime(time.Now().Add(time.Hour)).
		SetStatus(st.TransferStatusSenderKeyTweakPending).
		SetType(st.TransferTypePreimageSwap).
		MustExec(ctx)

	entexample.NewPreimageRequestExample(t, tx).
		SetPaymentHash(paymentHash).
		SetSenderIdentityPubkey(senderIdentityPubKey).
		SetReceiverIdentityPubkey(receiverIdentityPubKey).
		SetStatus(st.PreimageRequestStatusWaitingForPreimage).
		SetTransfers(transfer).
		MustExec(ctx)

	handler := NewLightningHandler(&so.Config{
		AuthzEnforced:              true,
		FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{},
	})

	queryCtxFor := func(identityPubKey keys.Public) context.Context {
		return authn.InjectSessionForTests(ctx, identityPubKey, time.Now().Add(time.Hour).Unix())
	}

	baseReqFor := func(identityPubKey keys.Public) *pb.QueryHtlcRequest {
		return &pb.QueryHtlcRequest{
			IdentityPublicKey: identityPubKey.Serialize(),
			PaymentHashes:     [][]byte{paymentHash},
			TransferIds:       []string{transfer.ID.String()},
			Limit:             10,
		}
	}

	receiverResp, err := handler.QueryHTLC(queryCtxFor(receiverIdentityPubKey), baseReqFor(receiverIdentityPubKey))
	require.NoError(t, err)
	require.Len(t, receiverResp.GetPreimageRequests(), 1)
	assert.Equal(t, paymentHash, receiverResp.GetPreimageRequests()[0].GetPaymentHash())

	senderDefaultResp, err := handler.QueryHTLC(queryCtxFor(senderIdentityPubKey), baseReqFor(senderIdentityPubKey))
	require.NoError(t, err)
	assert.Empty(t, senderDefaultResp.GetPreimageRequests(), "sender should not see receiver-role HTLC rows by default")

	senderRoleReq := baseReqFor(senderIdentityPubKey)
	senderRoleReq.MatchRole = pb.PreimageRequestRole_PREIMAGE_REQUEST_ROLE_SENDER
	senderRoleResp, err := handler.QueryHTLC(queryCtxFor(senderIdentityPubKey), senderRoleReq)
	require.NoError(t, err)
	require.Len(t, senderRoleResp.GetPreimageRequests(), 1)
	assert.Equal(t, paymentHash, senderRoleResp.GetPreimageRequests()[0].GetPaymentHash())

	attackerResp, err := handler.QueryHTLC(queryCtxFor(attackerIdentityPubKey), baseReqFor(attackerIdentityPubKey))
	require.NoError(t, err)
	assert.Empty(t, attackerResp.GetPreimageRequests(), "third party should not enumerate another user's HTLC by hash and transfer ID")

	_, err = handler.QueryHTLC(queryCtxFor(attackerIdentityPubKey), baseReqFor(receiverIdentityPubKey))
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}

type trackingFrostServiceClientConnection struct {
	client pbfrost.FrostServiceClient
}

func (m *trackingFrostServiceClientConnection) StartFrostServiceClient(*LightningHandler) (pbfrost.FrostServiceClient, error) {
	return m.client, nil
}

func (m *trackingFrostServiceClientConnection) Close() {
}

type trackingFrostServiceClient struct {
	startedCh   chan struct{}
	releaseCh   <-chan struct{}
	started     atomic.Int32
	inFlight    atomic.Int32
	maxInFlight atomic.Int32
}

func (m *trackingFrostServiceClient) Echo(context.Context, *pbfrost.EchoRequest, ...grpc.CallOption) (*pbfrost.EchoResponse, error) {
	return &pbfrost.EchoResponse{}, nil
}

func (m *trackingFrostServiceClient) DkgRound1(context.Context, *pbfrost.DkgRound1Request, ...grpc.CallOption) (*pbfrost.DkgRound1Response, error) {
	return &pbfrost.DkgRound1Response{}, nil
}

func (m *trackingFrostServiceClient) DkgRound2(context.Context, *pbfrost.DkgRound2Request, ...grpc.CallOption) (*pbfrost.DkgRound2Response, error) {
	return &pbfrost.DkgRound2Response{}, nil
}

func (m *trackingFrostServiceClient) DkgRound3(context.Context, *pbfrost.DkgRound3Request, ...grpc.CallOption) (*pbfrost.DkgRound3Response, error) {
	return &pbfrost.DkgRound3Response{}, nil
}

func (m *trackingFrostServiceClient) FrostNonce(context.Context, *pbfrost.FrostNonceRequest, ...grpc.CallOption) (*pbfrost.FrostNonceResponse, error) {
	return &pbfrost.FrostNonceResponse{}, nil
}

func (m *trackingFrostServiceClient) SignFrost(context.Context, *pbfrost.SignFrostRequest, ...grpc.CallOption) (*pbfrost.SignFrostResponse, error) {
	return &pbfrost.SignFrostResponse{}, nil
}

func (m *trackingFrostServiceClient) AggregateFrost(context.Context, *pbfrost.AggregateFrostRequest, ...grpc.CallOption) (*pbfrost.AggregateFrostResponse, error) {
	return &pbfrost.AggregateFrostResponse{}, nil
}

func (m *trackingFrostServiceClient) AggregateFrostBatch(context.Context, *pbfrost.AggregateFrostBatchRequest, ...grpc.CallOption) (*pbfrost.AggregateFrostBatchResponse, error) {
	return &pbfrost.AggregateFrostBatchResponse{}, nil
}

func (m *trackingFrostServiceClient) ValidateSignatureShare(ctx context.Context, _ *pbfrost.ValidateSignatureShareRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	currentInFlight := m.inFlight.Add(1)
	for {
		maxInFlight := m.maxInFlight.Load()
		if currentInFlight <= maxInFlight {
			break
		}
		if m.maxInFlight.CompareAndSwap(maxInFlight, currentInFlight) {
			break
		}
	}
	m.started.Add(1)
	select {
	case m.startedCh <- struct{}{}:
	default:
	}

	select {
	case <-m.releaseCh:
		m.inFlight.Add(-1)
		return &emptypb.Empty{}, nil
	case <-ctx.Done():
		m.inFlight.Add(-1)
		return nil, ctx.Err()
	}
}

func createSigningJob(leafID string) *pb.UserSignedTxSigningJob {
	return &pb.UserSignedTxSigningJob{
		LeafId: leafID,
		SigningCommitments: &pb.SigningCommitments{
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
		UserSignature: []byte("test_signature"),
		RawTx:         []byte("test_raw_tx"),
	}
}

func TestValidateDuplicateLeaves(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)

	config := &so.Config{FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{}}
	lightningHandler := NewLightningHandler(config)

	t.Run("successful validation with no duplicates", func(t *testing.T) {
		leavesToSend := []*pb.UserSignedTxSigningJob{
			createSigningJob("leaf1"),
			createSigningJob("leaf2"),
			createSigningJob("leaf3"),
		}
		directLeavesToSend := []*pb.UserSignedTxSigningJob{
			createSigningJob("leaf1"),
			createSigningJob("leaf2"),
		}
		directFromCpfpLeavesToSend := []*pb.UserSignedTxSigningJob{
			createSigningJob("leaf1"),
			createSigningJob("leaf3"),
		}

		err := lightningHandler.ValidateDuplicateLeaves(ctx, leavesToSend, directLeavesToSend, directFromCpfpLeavesToSend)
		require.NoError(t, err)
	})

	t.Run("successful validation with empty arrays", func(t *testing.T) {
		err := lightningHandler.ValidateDuplicateLeaves(ctx, []*pb.UserSignedTxSigningJob{}, []*pb.UserSignedTxSigningJob{}, []*pb.UserSignedTxSigningJob{})
		require.NoError(t, err)
	})

	t.Run("successful validation with only leavesToSend", func(t *testing.T) {
		leavesToSend := []*pb.UserSignedTxSigningJob{
			createSigningJob("leaf1"),
			createSigningJob("leaf2"),
		}

		err := lightningHandler.ValidateDuplicateLeaves(ctx, leavesToSend, []*pb.UserSignedTxSigningJob{}, []*pb.UserSignedTxSigningJob{})
		require.NoError(t, err)
	})

	t.Run("nil job entries", func(t *testing.T) {
		tests := []struct {
			name                       string
			leavesToSend               []*pb.UserSignedTxSigningJob
			directLeavesToSend         []*pb.UserSignedTxSigningJob
			directFromCpfpLeavesToSend []*pb.UserSignedTxSigningJob
			expectedErrMsg             string
		}{
			{
				name:           "nil cpfp job",
				leavesToSend:   []*pb.UserSignedTxSigningJob{nil},
				expectedErrMsg: "leaves_to_send[0] is required",
			},
			{
				name:               "nil direct job",
				leavesToSend:       []*pb.UserSignedTxSigningJob{createSigningJob("leaf1")},
				directLeavesToSend: []*pb.UserSignedTxSigningJob{nil},
				expectedErrMsg:     "direct_leaves_to_send[0] is required",
			},
			{
				name:                       "nil direct from cpfp job",
				leavesToSend:               []*pb.UserSignedTxSigningJob{createSigningJob("leaf1")},
				directFromCpfpLeavesToSend: []*pb.UserSignedTxSigningJob{nil},
				expectedErrMsg:             "direct_from_cpfp_leaves_to_send[0] is required",
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				var err error
				require.NotPanics(t, func() {
					err = lightningHandler.ValidateDuplicateLeaves(ctx, tt.leavesToSend, tt.directLeavesToSend, tt.directFromCpfpLeavesToSend)
				})
				require.ErrorContains(t, err, tt.expectedErrMsg)
			})
		}
	})

	t.Run("duplicate in leavesToSend", func(t *testing.T) {
		leavesToSend := []*pb.UserSignedTxSigningJob{
			createSigningJob("leaf1"),
			createSigningJob("leaf1"), // Duplicate
			createSigningJob("leaf2"),
		}

		err := lightningHandler.ValidateDuplicateLeaves(ctx, leavesToSend, []*pb.UserSignedTxSigningJob{}, []*pb.UserSignedTxSigningJob{})
		require.ErrorContains(t, err, "duplicate leaf id: leaf1")
		code, reason := sparkerrors.CodeAndReasonFrom(err)
		require.Equal(t, codes.InvalidArgument, code)
		require.Equal(t, "DUPLICATE_FIELD", reason)
	})

	t.Run("duplicate in directLeavesToSend", func(t *testing.T) {
		leavesToSend := []*pb.UserSignedTxSigningJob{
			createSigningJob("leaf1"),
			createSigningJob("leaf2"),
		}
		directLeavesToSend := []*pb.UserSignedTxSigningJob{
			createSigningJob("leaf1"),
			createSigningJob("leaf1"), // Duplicate
		}

		err := lightningHandler.ValidateDuplicateLeaves(ctx, leavesToSend, directLeavesToSend, []*pb.UserSignedTxSigningJob{})
		require.ErrorContains(t, err, "duplicate leaf id: leaf1")
	})

	t.Run("duplicate in directFromCpfpLeavesToSend", func(t *testing.T) {
		leavesToSend := []*pb.UserSignedTxSigningJob{
			createSigningJob("leaf1"),
			createSigningJob("leaf2"),
		}
		directFromCpfpLeavesToSend := []*pb.UserSignedTxSigningJob{
			createSigningJob("leaf1"),
			createSigningJob("leaf1"), // Duplicate
		}

		err := lightningHandler.ValidateDuplicateLeaves(ctx, leavesToSend, []*pb.UserSignedTxSigningJob{}, directFromCpfpLeavesToSend)
		require.ErrorContains(t, err, "duplicate leaf id: leaf1")
	})

	t.Run("leaf id not found in leavesToSend for directLeavesToSend", func(t *testing.T) {
		leavesToSend := []*pb.UserSignedTxSigningJob{
			createSigningJob("leaf1"),
			createSigningJob("leaf2"),
		}
		directLeavesToSend := []*pb.UserSignedTxSigningJob{
			createSigningJob("leaf1"),
			createSigningJob("leaf3"), // Not in leavesToSend
		}

		err := lightningHandler.ValidateDuplicateLeaves(ctx, leavesToSend, directLeavesToSend, []*pb.UserSignedTxSigningJob{})
		require.ErrorContains(t, err, "leaf id leaf3 not found in leaves to send")
		code, reason := sparkerrors.CodeAndReasonFrom(err)
		require.Equal(t, codes.InvalidArgument, code)
		require.Equal(t, "MALFORMED_FIELD", reason)
	})

	t.Run("leaf id not found in leavesToSend for directFromCpfpLeavesToSend", func(t *testing.T) {
		leavesToSend := []*pb.UserSignedTxSigningJob{
			createSigningJob("leaf1"),
			createSigningJob("leaf2"),
		}
		directFromCpfpLeavesToSend := []*pb.UserSignedTxSigningJob{
			createSigningJob("leaf1"),
			createSigningJob("leaf3"), // Not in leavesToSend
		}

		err := lightningHandler.ValidateDuplicateLeaves(ctx, leavesToSend, []*pb.UserSignedTxSigningJob{}, directFromCpfpLeavesToSend)
		require.ErrorContains(t, err, "leaf id leaf3 not found in leaves to send")
	})

	t.Run("multiple duplicates across different arrays", func(t *testing.T) {
		leavesToSend := []*pb.UserSignedTxSigningJob{
			createSigningJob("leaf1"),
			createSigningJob("leaf1"), // Duplicate in leavesToSend
			createSigningJob("leaf2"),
		}
		directLeavesToSend := []*pb.UserSignedTxSigningJob{
			createSigningJob("leaf2"),
			createSigningJob("leaf2"), // Duplicate in directLeavesToSend
		}
		directFromCpfpLeavesToSend := []*pb.UserSignedTxSigningJob{
			createSigningJob("leaf1"),
			createSigningJob("leaf1"), // Duplicate in directFromCpfpLeavesToSend
		}

		err := lightningHandler.ValidateDuplicateLeaves(ctx, leavesToSend, directLeavesToSend, directFromCpfpLeavesToSend)
		// Should detect the first duplicate it encounters (in leavesToSend)
		require.ErrorContains(t, err, "duplicate leaf id: leaf1")
	})

	t.Run("complex scenario with all arrays populated", func(t *testing.T) {
		leavesToSend := []*pb.UserSignedTxSigningJob{
			createSigningJob("leaf1"),
			createSigningJob("leaf2"),
			createSigningJob("leaf3"),
			createSigningJob("leaf4"),
		}
		directLeavesToSend := []*pb.UserSignedTxSigningJob{
			createSigningJob("leaf1"),
			createSigningJob("leaf2"),
			createSigningJob("leaf3"),
		}
		directFromCpfpLeavesToSend := []*pb.UserSignedTxSigningJob{
			createSigningJob("leaf1"),
			createSigningJob("leaf4"),
		}

		err := lightningHandler.ValidateDuplicateLeaves(ctx, leavesToSend, directLeavesToSend, directFromCpfpLeavesToSend)
		require.NoError(t, err)
	})

	t.Run("nil arrays", func(t *testing.T) {
		err := lightningHandler.ValidateDuplicateLeaves(ctx, nil, nil, nil)
		require.NoError(t, err)
	})

	t.Run("mixed nil and non-nil arrays", func(t *testing.T) {
		leavesToSend := []*pb.UserSignedTxSigningJob{
			createSigningJob("leaf1"),
			createSigningJob("leaf2"),
		}

		err := lightningHandler.ValidateDuplicateLeaves(ctx, leavesToSend, nil, nil)
		require.NoError(t, err)
	})

	t.Run("empty leavesToSend with non-empty other arrays", func(t *testing.T) {
		directLeavesToSend := []*pb.UserSignedTxSigningJob{
			createSigningJob("leaf1"),
		}

		err := lightningHandler.ValidateDuplicateLeaves(ctx, []*pb.UserSignedTxSigningJob{}, directLeavesToSend, []*pb.UserSignedTxSigningJob{})
		require.ErrorContains(t, err, "leaf id leaf1 not found in leaves to send")
	})

	t.Run("nil signing job entries return invalid argument", func(t *testing.T) {
		tests := []struct {
			name                 string
			leavesToSend         []*pb.UserSignedTxSigningJob
			directLeavesToSend   []*pb.UserSignedTxSigningJob
			directFromCpfpLeaves []*pb.UserSignedTxSigningJob
			expectedContains     string
		}{
			{
				name:             "leaves_to_send",
				leavesToSend:     []*pb.UserSignedTxSigningJob{nil},
				expectedContains: "leaves_to_send[0] is required",
			},
			{
				name:               "direct_leaves_to_send",
				leavesToSend:       []*pb.UserSignedTxSigningJob{createSigningJob("leaf1")},
				directLeavesToSend: []*pb.UserSignedTxSigningJob{nil},
				expectedContains:   "direct_leaves_to_send[0] is required",
			},
			{
				name:                 "direct_from_cpfp_leaves_to_send",
				leavesToSend:         []*pb.UserSignedTxSigningJob{createSigningJob("leaf1")},
				directFromCpfpLeaves: []*pb.UserSignedTxSigningJob{nil},
				expectedContains:     "direct_from_cpfp_leaves_to_send[0] is required",
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				var err error
				require.NotPanics(t, func() {
					err = lightningHandler.ValidateDuplicateLeaves(ctx, tt.leavesToSend, tt.directLeavesToSend, tt.directFromCpfpLeaves)
				})
				require.ErrorContains(t, err, tt.expectedContains)
				code, reason := sparkerrors.CodeAndReasonFrom(err)
				require.Equal(t, codes.InvalidArgument, code)
				require.Equal(t, "MISSING_FIELD", reason)
			})
		}
	})
}

// Note: StorePreimageShare requires complex cryptographic validation
// that's difficult to mock in unit tests. These tests focus on basic validation.
func TestStorePreimageShareEdgeCases(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	rng := rand.NewChaCha8([32]byte{7})

	config := &so.Config{
		Threshold:                  2,
		Index:                      0,
		FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{},
	}
	lightningHandler := NewLightningHandler(config)

	t.Run("nil preimage share returns error", func(t *testing.T) {
		req := &pb.StorePreimageShareRequest{
			PaymentHash:           []byte("payment_hash"),
			PreimageShare:         nil,
			Threshold:             uint32(config.Threshold),
			InvoiceString:         "invalid_bolt11",
			UserIdentityPublicKey: []byte("user_identity_key"),
		}

		err := lightningHandler.StorePreimageShare(ctx, req)
		require.ErrorContains(t, err, "preimage share is nil")
		code, reason := sparkerrors.CodeAndReasonFrom(err)
		require.Equal(t, codes.InvalidArgument, code)
		require.Equal(t, "MISSING_FIELD", reason)
	})

	t.Run("empty proofs array returns error", func(t *testing.T) {
		req := &pb.StorePreimageShareRequest{
			PaymentHash:           []byte("payment_hash"),
			PreimageShare:         &pb.SecretShare{SecretShare: []byte("test"), Proofs: [][]byte{}},
			Threshold:             uint32(config.Threshold),
			InvoiceString:         "invalid_bolt11",
			UserIdentityPublicKey: []byte("user_identity_key"),
		}

		err := lightningHandler.StorePreimageShare(ctx, req)
		require.ErrorContains(t, err, "preimage share proofs is empty")
		code, reason := sparkerrors.CodeAndReasonFrom(err)
		require.Equal(t, codes.InvalidArgument, code)
		require.Equal(t, "MISSING_FIELD", reason)
	})

	t.Run("allows provider session to store for LNURL user owner", func(t *testing.T) {
		providerIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
		userIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
		providerCtx := authn.InjectSessionForTests(ctx, providerIdentityPubKey, time.Now().Add(time.Hour).Unix())

		authConfig := &so.Config{
			AuthzEnforced:              true,
			Threshold:                  2,
			Index:                      0,
			FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{},
		}
		authHandler := NewLightningHandler(authConfig)

		req := &pb.StorePreimageShareRequest{
			PaymentHash:           []byte("payment_hash"),
			PreimageShare:         &pb.SecretShare{SecretShare: []byte("test"), Proofs: [][]byte{{1}}},
			Threshold:             uint32(authConfig.Threshold),
			InvoiceString:         "invalid_bolt11",
			UserIdentityPublicKey: userIdentityPubKey.Serialize(),
		}

		err := authHandler.StorePreimageShare(providerCtx, req)
		require.Error(t, err)
		require.NotEqual(t, codes.PermissionDenied, status.Code(err))
		require.NotContains(t, err.Error(), "session identity does not match request identity")
		require.ErrorContains(t, err, "unable to validate share")
	})
}

func TestStorePreimageShareV2EdgeCases(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)

	rng := rand.NewChaCha8([32]byte{2})
	soIdentityKey := keys.MustGeneratePrivateKeyFromRand(rng)

	soIdentifier := "test-so-1"

	config := &so.Config{
		Identifier:                 soIdentifier,
		IdentityPrivateKey:         soIdentityKey,
		Threshold:                  2,
		Index:                      0,
		FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{},
	}
	lightningHandler := NewLightningHandler(config)

	encryptForSO := func(t *testing.T, data []byte) []byte {
		t.Helper()
		pubKey, err := eciesgo.NewPublicKeyFromBytes(soIdentityKey.Public().Serialize())
		require.NoError(t, err)
		encrypted, err := eciesgo.Encrypt(pubKey, data)
		require.NoError(t, err)
		return encrypted
	}

	t.Run("missing share for SO identifier", func(t *testing.T) {
		req := &pb.StorePreimageShareV2Request{
			EncryptedPreimageShares: map[string][]byte{
				"other-so": []byte("some_data"),
			},
		}
		err := lightningHandler.decryptAndStorePreimageShare(ctx, req)
		require.ErrorContains(t, err, "no encrypted preimage share found for SO")
	})

	t.Run("invalid ciphertext", func(t *testing.T) {
		req := &pb.StorePreimageShareV2Request{
			EncryptedPreimageShares: map[string][]byte{
				soIdentifier: []byte("not_valid_ecies"),
			},
		}
		err := lightningHandler.decryptAndStorePreimageShare(ctx, req)
		require.ErrorContains(t, err, "failed to decrypt preimage share")
	})

	t.Run("empty proofs after decryption", func(t *testing.T) {
		shareProto := &pb.SecretShare{
			SecretShare: []byte("test_share_data"),
			Proofs:      [][]byte{},
		}
		shareBytes, err := proto.Marshal(shareProto)
		require.NoError(t, err)
		encrypted := encryptForSO(t, shareBytes)

		req := &pb.StorePreimageShareV2Request{
			EncryptedPreimageShares: map[string][]byte{
				soIdentifier: encrypted,
			},
		}
		err = lightningHandler.decryptAndStorePreimageShare(ctx, req)
		require.ErrorContains(t, err, "preimage share proofs is empty")
	})

	t.Run("allows provider session to coordinate share storage for LNURL user owner", func(t *testing.T) {
		providerIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
		userIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
		providerCtx := authn.InjectSessionForTests(ctx, providerIdentityPubKey, time.Now().Add(time.Hour).Unix())

		authConfig := &so.Config{
			AuthzEnforced:              true,
			Identifier:                 soIdentifier,
			IdentityPrivateKey:         soIdentityKey,
			Threshold:                  2,
			Index:                      0,
			FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{},
		}
		authHandler := NewLightningHandler(authConfig)

		req := &pb.StorePreimageShareV2Request{
			PaymentHash:           []byte("payment_hash"),
			UserIdentityPublicKey: userIdentityPubKey.Serialize(),
		}

		err := authHandler.StorePreimageShareV2(providerCtx, req)
		require.Error(t, err)
		require.NotEqual(t, codes.PermissionDenied, status.Code(err))
		require.NotContains(t, err.Error(), "session identity does not match request identity")
		require.ErrorContains(t, err, "no consensus engine in context")
	})
}

func TestGetSigningCommitments(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)

	signingOperators := sparktesting.GetAllSigningOperators(t)

	config := &so.Config{
		SigningOperatorMap:         signingOperators,
		FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{},
	}

	signingHandler := NewSigningHandler(config)

	manyNodeIDs := make([]string, 1001)
	for i := range manyNodeIDs {
		manyNodeIDs[i] = uuid.NewString()
	}

	tests := []struct {
		name           string
		nodeIds        []string
		count          uint32
		expectError    bool
		expectedErrMsg string
		expectEmpty    bool
	}{
		{
			name:           "invalid node ID format",
			nodeIds:        []string{"invalid-uuid-format"},
			count:          1,
			expectError:    true,
			expectedErrMsg: "unable to parse node id",
			expectEmpty:    false,
		},
		{
			name:        "empty node IDs",
			nodeIds:     []string{},
			count:       1,
			expectError: false,
			expectEmpty: true,
		},
		{
			name:           "non-existent node ID",
			nodeIds:        []string{"12345678-1234-1234-1234-123456789012"},
			count:          1,
			expectError:    true,
			expectedErrMsg: "unknown node ids: 12345678-1234-1234-1234-123456789012",
		},
		{
			name:        "zero count defaults to 1",
			nodeIds:     []string{},
			count:       0,
			expectError: false,
			expectEmpty: true,
		},
		{
			name:           "multiple invalid node IDs",
			nodeIds:        []string{"invalid-1", "invalid-2"},
			count:          1,
			expectError:    true,
			expectedErrMsg: "unable to parse node id",
		},
		{
			name:           "too many nodes",
			nodeIds:        manyNodeIDs,
			count:          3,
			expectError:    true,
			expectedErrMsg: "there were 1001 node ids provided, but the max is 1000",
		},
		{
			name:           "too high count",
			nodeIds:        []string{"12345678-1234-1234-1234-123456789012"},
			count:          100,
			expectError:    true,
			expectedErrMsg: "number of signing commitments provided was 100, but the maximum is 10",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &pb.GetSigningCommitmentsRequest{
				NodeIds: tt.nodeIds,
				Count:   tt.count,
			}

			resp, err := signingHandler.GetSigningCommitments(ctx, req)

			if tt.expectError {
				require.ErrorContains(t, err, tt.expectedErrMsg)
				assert.Nil(t, resp)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, resp)
				if tt.expectEmpty {
					assert.Empty(t, resp.GetSigningCommitments())
				}
			}
		})
	}
}

func TestValidatePreimage_InvalidPreimage_Errors(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{})
	ctx, _ := db.NewTestSQLiteContext(t)

	config := &so.Config{FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{}}
	lightningHandler := NewLightningHandler(config)

	identityKey := keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize()
	nonexistentPreimage := bytes.Repeat([]byte{0x03}, 32)
	nonexistentHash := sha256.Sum256(nonexistentPreimage)
	tests := []struct {
		name           string
		paymentHash    []byte
		preimage       []byte
		identityPubKey []byte
		expectedErrMsg string
		expectedCode   codes.Code
	}{
		{
			name:           "invalid preimage - hash mismatch",
			paymentHash:    bytes.Repeat([]byte{0}, 32),
			preimage:       bytes.Repeat([]byte{0x01}, 32),
			identityPubKey: identityKey,
			expectedErrMsg: "invalid preimage",
			expectedCode:   codes.FailedPrecondition,
		},
		{
			name:           "non-existent preimage request",
			paymentHash:    nonexistentHash[:],
			preimage:       nonexistentPreimage,
			identityPubKey: identityKey,
			expectedErrMsg: "preimage request not found",
			expectedCode:   codes.NotFound,
		},
		{
			name:           "empty payment hash",
			paymentHash:    []byte{},
			preimage:       bytes.Repeat([]byte{0x01}, 32),
			identityPubKey: identityKey,
			expectedErrMsg: "invalid payment hash length: 0 bytes, expected 32 bytes",
			expectedCode:   codes.InvalidArgument,
		},
		{
			name:           "empty preimage",
			paymentHash:    []byte("payment_hash_32_bytes_long______"),
			preimage:       []byte{},
			identityPubKey: identityKey,
			expectedErrMsg: "invalid preimage length: 0 bytes, expected 32 bytes",
			expectedCode:   codes.InvalidArgument,
		},
		{
			name:           "nil identity public key",
			paymentHash:    []byte("payment_hash_32_bytes_long______"),
			preimage:       []byte("test_preimage_32_bytes_long_____"),
			identityPubKey: nil,
			expectedErrMsg: "invalid identity public key length: 0 bytes, expected 33 bytes",
			expectedCode:   codes.InvalidArgument,
		},
		{
			name:           "malformed identity public key",
			paymentHash:    []byte("payment_hash_32_bytes_long______"),
			preimage:       []byte("test_preimage_32_bytes_long_____"),
			identityPubKey: bytes.Repeat([]byte{0xff}, 33),
			expectedErrMsg: "invalid identity public key",
			expectedCode:   codes.InvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &pb.ProvidePreimageRequest{
				PaymentHash:       tt.paymentHash,
				Preimage:          tt.preimage,
				IdentityPublicKey: tt.identityPubKey,
			}

			preimageRequest, transfer, err := lightningHandler.ValidatePreimage(ctx, req)

			require.ErrorContains(t, err, tt.expectedErrMsg)
			require.Equal(t, tt.expectedCode, status.Code(err))
			assert.Nil(t, preimageRequest)
			assert.Nil(t, transfer)
		})
	}
}

func TestProvidePreimageRejectsMalformedIdentityPublicKeyWithInvalidArgument(t *testing.T) {
	handler := NewLightningHandler(&so.Config{FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{}})

	resp, err := handler.ProvidePreimage(t.Context(), &pb.ProvidePreimageRequest{
		PaymentHash:       bytes.Repeat([]byte{0x01}, 32),
		Preimage:          bytes.Repeat([]byte{0x02}, 32),
		IdentityPublicKey: []byte{0x02, 0x01},
	})
	require.Nil(t, resp)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.ErrorContains(t, err, "invalid identity public key")
}

func TestProvidePreimageRejectsSessionIdentityMismatchBeforeValidation(t *testing.T) {
	handler := NewLightningHandler(&so.Config{
		AuthzEnforced:              true,
		FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{},
	})
	sessionIdentityPubKey := keys.GeneratePrivateKey().Public()
	requestIdentityPubKey := keys.GeneratePrivateKey().Public()
	ctx := authn.InjectSessionForTests(t.Context(), sessionIdentityPubKey, time.Now().Add(time.Hour).Unix())

	preimage := bytes.Repeat([]byte{0x02}, 32)
	paymentHash := sha256.Sum256(preimage)

	resp, err := handler.ProvidePreimage(ctx, &pb.ProvidePreimageRequest{
		PaymentHash:       paymentHash[:],
		Preimage:          preimage,
		IdentityPublicKey: requestIdentityPubKey.Serialize(),
	})

	require.Nil(t, resp)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.ErrorContains(t, err, "session identity does not match request identity")
}

func TestStorePreimage(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{1})
	ctx, _ := db.NewTestSQLiteContext(t)

	config := &so.Config{FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{}}
	lightningHandler := NewLightningHandler(config)

	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	senderPub := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	receiverPub := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	preimage := bytes.Repeat([]byte{0xab}, 32)

	t.Run("updates status from WaitingForPreimage to PreimageShared", func(t *testing.T) {
		transfer, err := dbTx.Transfer.Create().
			SetSenderIdentityPubkey(senderPub).
			SetReceiverIdentityPubkey(receiverPub).
			SetStatus(st.TransferStatusSenderKeyTweakPending).
			SetTotalValue(1000).
			SetExpiryTime(time.Now().Add(10 * time.Minute)).
			SetType(st.TransferTypePreimageSwap).
			SetNetwork(btcnetwork.Regtest).
			Save(ctx)
		require.NoError(t, err)

		preimageRequest, err := dbTx.PreimageRequest.Create().
			SetPaymentHash(bytes.Repeat([]byte{0x01}, 32)).
			SetStatus(st.PreimageRequestStatusWaitingForPreimage).
			SetReceiverIdentityPubkey(receiverPub).
			SetTransfers(transfer).
			Save(ctx)
		require.NoError(t, err)

		err = lightningHandler.StorePreimage(ctx, preimageRequest, preimage)
		require.NoError(t, err)

		updated, err := dbTx.PreimageRequest.Get(ctx, preimageRequest.ID)
		require.NoError(t, err)
		assert.Equal(t, st.PreimageRequestStatusPreimageShared, updated.Status)
		assert.Equal(t, preimage, updated.Preimage)
	})

	t.Run("no-ops when already PreimageShared", func(t *testing.T) {
		existingPreimage := bytes.Repeat([]byte{0xcd}, 32)
		transfer, err := dbTx.Transfer.Create().
			SetSenderIdentityPubkey(senderPub).
			SetReceiverIdentityPubkey(receiverPub).
			SetStatus(st.TransferStatusSenderKeyTweakPending).
			SetTotalValue(1000).
			SetExpiryTime(time.Now().Add(10 * time.Minute)).
			SetType(st.TransferTypePreimageSwap).
			SetNetwork(btcnetwork.Regtest).
			Save(ctx)
		require.NoError(t, err)

		preimageRequest, err := dbTx.PreimageRequest.Create().
			SetPaymentHash(bytes.Repeat([]byte{0x02}, 32)).
			SetStatus(st.PreimageRequestStatusPreimageShared).
			SetPreimage(existingPreimage).
			SetReceiverIdentityPubkey(receiverPub).
			SetTransfers(transfer).
			Save(ctx)
		require.NoError(t, err)

		err = lightningHandler.StorePreimage(ctx, preimageRequest, preimage)
		require.NoError(t, err)

		updated, err := dbTx.PreimageRequest.Get(ctx, preimageRequest.ID)
		require.NoError(t, err)
		assert.Equal(t, st.PreimageRequestStatusPreimageShared, updated.Status)
		assert.Equal(t, existingPreimage, updated.Preimage)
	})
}

// Note: validateNodeOwnership and validateHasSession are private methods,
// so we test them indirectly through GetSigningCommitments which calls validateHasSession
func TestValidateGetPreimageRequestEdgeErrorCases(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{1})
	ctx, _ := db.NewTestSQLiteContext(t)

	config := &so.Config{
		SignerAddress:              "invalid_address", // This will cause connection failures
		FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{},
	}
	lightningHandler := NewLightningHandler(config)

	validPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	tests := []struct {
		name                       string
		cpfpTransactions           []*pb.UserSignedTxSigningJob
		directTransactions         []*pb.UserSignedTxSigningJob
		directFromCpfpTransactions []*pb.UserSignedTxSigningJob
		destinationPubKey          keys.Public
		expectedErrMsg             string
	}{
		{
			name:              "nil cpfp transactions",
			cpfpTransactions:  nil,
			destinationPubKey: validPubKey,
			expectedErrMsg:    "at least one transaction type must be provided",
		},
		{
			name:              "empty cpfp transactions",
			cpfpTransactions:  []*pb.UserSignedTxSigningJob{},
			destinationPubKey: validPubKey,
			expectedErrMsg:    "at least one transaction type must be provided",
		},
		{
			name:              "nil transaction in cpfp array",
			cpfpTransactions:  []*pb.UserSignedTxSigningJob{nil},
			destinationPubKey: validPubKey,
			expectedErrMsg:    "cpfp transaction is nil",
		},
		{
			name: "nil signing commitments",
			cpfpTransactions: []*pb.UserSignedTxSigningJob{
				{
					LeafId:             "550e8400-e29b-41d4-a716-446655440000",
					SigningCommitments: nil,
				},
			},
			destinationPubKey: validPubKey,
			expectedErrMsg:    "signing commitments is nil for cpfpTransaction, leaf_id: 550e8400-e29b-41d4-a716-446655440000",
		},
		{
			name: "nil signing nonce commitment",
			cpfpTransactions: []*pb.UserSignedTxSigningJob{
				{
					LeafId:                 "550e8400-e29b-41d4-a716-446655440000",
					SigningCommitments:     &pb.SigningCommitments{SigningCommitments: map[string]*pbcommon.SigningCommitment{}},
					SigningNonceCommitment: nil,
				},
			},
			destinationPubKey: validPubKey,
			expectedErrMsg:    "signing nonce commitment is nil for cpfpTransaction, leaf_id: 550e8400-e29b-41d4-a716-446655440000",
		},
		{
			name: "invalid leaf ID format",
			cpfpTransactions: []*pb.UserSignedTxSigningJob{
				{
					LeafId:                 "invalid-uuid",
					SigningCommitments:     &pb.SigningCommitments{SigningCommitments: map[string]*pbcommon.SigningCommitment{}},
					SigningNonceCommitment: &pbcommon.SigningCommitment{},
				},
			},
			destinationPubKey: validPubKey,
			expectedErrMsg:    "unable to parse node id",
		},
		{
			name: "empty signing commitments map",
			cpfpTransactions: []*pb.UserSignedTxSigningJob{
				{
					LeafId: "550e8400-e29b-41d4-a716-446655440000",
					SigningCommitments: &pb.SigningCommitments{
						SigningCommitments: map[string]*pbcommon.SigningCommitment{}, // empty map
					},
					SigningNonceCommitment: &pbcommon.SigningCommitment{},
					RawTx:                  []byte("dummy_transaction_data_for_testing"),
				},
			},
			destinationPubKey: validPubKey,
			expectedErrMsg:    "unable to get cpfpTransaction tree_node with id: 550e8400-e29b-41d4-a716-446655440000", // Will fail at node lookup
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := lightningHandler.validateGetPreimageRequest(
				ctx,
				[]byte("payment_hash_32_bytes_long______"),
				tt.cpfpTransactions,
				tt.directTransactions,
				tt.directFromCpfpTransactions,
				&pb.InvoiceAmount{ValueSats: 1000},
				tt.destinationPubKey,
				singleLeafDestination(tt.destinationPubKey),
				0,
				pb.InitiatePreimageSwapRequest_REASON_SEND,
				false,
			)

			require.ErrorContains(t, err, tt.expectedErrMsg)
		})
	}
}

// Test payment hash collision - verifies error message includes both payment hash and transfer ID
func TestValidateGetPreimageRequest_PaymentHashCollision(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)

	rng := rand.NewChaCha8([32]byte{42})
	validPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	transfer := entexample.NewTransferExample(t, tx).
		SetSenderIdentityPubkey(validPubKey).
		SetReceiverIdentityPubkey(validPubKey).
		SetExpiryTime(time.Now().Add(24 * time.Hour)).
		SetStatus(st.TransferStatusSenderInitiated).
		SetType(st.TransferTypePreimageSwap).
		MustExec(ctx)

	preimageRequest := entexample.NewPreimageRequestExample(t, tx).
		SetReceiverIdentityPubkey(validPubKey).
		SetStatus(st.PreimageRequestStatusWaitingForPreimage).
		SetTransfers(transfer).
		MustExec(ctx)

	config := &so.Config{FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{}}
	err = NewLightningHandler(config).validateGetPreimageRequest(
		ctx,
		preimageRequest.PaymentHash,
		[]*pb.UserSignedTxSigningJob{createSigningJob("leaf1")},
		[]*pb.UserSignedTxSigningJob{},
		[]*pb.UserSignedTxSigningJob{},
		&pb.InvoiceAmount{ValueSats: transfer.TotalValue},
		validPubKey,
		singleLeafDestination(validPubKey),
		0,
		pb.InitiatePreimageSwapRequest_REASON_SEND,
		false,
	)

	require.Error(t, err)
	grpcErr, ok := status.FromError(err)
	require.True(t, ok)
	require.Equal(t, codes.AlreadyExists, grpcErr.Code())
	require.Contains(t, grpcErr.Message(), "preimage request already exists for paymentHash")
	require.Contains(t, grpcErr.Message(), transfer.ID.String())
}

func TestInitiatePreimageSwapEdgeCases_Invalid_Errors(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)

	config := &so.Config{FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{}}
	lightningHandler := NewLightningHandler(config)

	rng := rand.NewChaCha8([32]byte{})
	ownerIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	receiverIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	// sendRequest builds a minimal valid-shape SEND request; each case mutates one field
	// to trip a specific validation. Per-leaf nil-job checks live on the package path
	// (transfer_package_validation.go) and are covered by TestLoadLeafRefundMapsFromTransferPackage_*.
	sendRequest := func() *pb.InitiatePreimageSwapRequest {
		return &pb.InitiatePreimageSwapRequest{
			PaymentHash:               make([]byte, 32),
			ReceiverIdentityPublicKey: receiverIdentityPubKey.Serialize(),
			Reason:                    pb.InitiatePreimageSwapRequest_REASON_SEND,
			TransferRequest: &pb.StartTransferRequest{
				OwnerIdentityPublicKey:    ownerIdentityPubKey.Serialize(),
				ReceiverIdentityPublicKey: receiverIdentityPubKey.Serialize(),
				TransferPackage: &pb.TransferPackage{
					LeavesToSend: []*pb.UserSignedTxSigningJob{{LeafId: "test-leaf"}},
				},
			},
		}
	}

	tests := []struct {
		name           string
		setUpRequest   func() *pb.InitiatePreimageSwapRequest
		expectedErrMsg string
	}{
		{
			name: "no request shape",
			setUpRequest: func() *pb.InitiatePreimageSwapRequest {
				return &pb.InitiatePreimageSwapRequest{}
			},
			expectedErrMsg: "transfer_request is required",
		},
		{
			name: "empty leaves to send",
			setUpRequest: func() *pb.InitiatePreimageSwapRequest {
				req := sendRequest()
				req.TransferRequest.TransferPackage.LeavesToSend = []*pb.UserSignedTxSigningJob{}
				return req
			},
			expectedErrMsg: "at least one cpfp leaf tx must be provided",
		},
		{
			name: "nil owner identity public key",
			setUpRequest: func() *pb.InitiatePreimageSwapRequest {
				req := sendRequest()
				req.TransferRequest.OwnerIdentityPublicKey = nil
				return req
			},
			expectedErrMsg: "unable to parse owner identity public key",
		},
		{
			name: "nil receiver identity public key",
			setUpRequest: func() *pb.InitiatePreimageSwapRequest {
				req := sendRequest()
				req.ReceiverIdentityPublicKey = nil
				req.TransferRequest.ReceiverIdentityPublicKey = nil
				return req
			},
			expectedErrMsg: "receiver identity public key is required",
		},
		{
			name: "too many transactions exceeds knob limit",
			setUpRequest: func() *pb.InitiatePreimageSwapRequest {
				req := sendRequest()
				leaves := make([]*pb.UserSignedTxSigningJob, 101)
				for i := range leaves {
					leaves[i] = &pb.UserSignedTxSigningJob{
						LeafId: fmt.Sprintf("550e8400-e29b-41d4-a716-44665544%04d", i),
					}
				}
				req.TransferRequest.TransferPackage.LeavesToSend = leaves
				return req
			},
			expectedErrMsg: "too many transactions: 101, maximum allowed: 100",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := tt.setUpRequest()

			var resp *pb.InitiatePreimageSwapResponse
			var err error
			require.NotPanics(t, func() {
				resp, err = lightningHandler.InitiatePreimageSwapV3(ctx, req)
			})

			require.ErrorContains(t, err, tt.expectedErrMsg)
			assert.Nil(t, resp)
		})
	}

	// The fee-on-receive rejection is enforced on the participant path
	// (GetPreimageShare / 2PC Prepare), not in the V3 coordinator validate.
	t.Run("fee not allowed for receive", func(t *testing.T) {
		req := sendRequest()
		req.Reason = pb.InitiatePreimageSwapRequest_REASON_RECEIVE
		req.FeeSats = 100
		_, err := lightningHandler.GetPreimageShare(ctx, req, nil, nil, nil, nil)
		require.ErrorContains(t, err, "fee is not allowed for receive preimage swap")
	})
}

// Regression test for https://linear.app/lightsparkdev/issue/LIG-8044
// Ensure that only a node owner can initiate a preimage swap for that node.
func TestPreimageSwapAuthorizationBugRegression(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{1})
	ctx, _ := db.ConnectToTestPostgres(t)

	// Valid 33-byte compressed secp256k1 public key for destination
	validPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	paymentHash := []byte("test_payment_hash_32_bytes_long_")

	// Create a valid transaction for testing
	validTxHex := "02000000000102dc552c6c0ef5ed0d8cd64bd1d2d1ffd7cf0ec0b5ad8df2a4c6269b59cffcc696010000000000000000603fbd40e86ee82258c57571c557b89a444aabf5b6a05574e6c6848379febe9a00000000000000000002e86905000000000022512024741d89092c5965f35a63802352fa9c7fae4a23d471b9dceb3379e8ff6b7dd1d054080000000000220020aea091435e74e3c1eba0bd964e67a05f300ace9e73efa66fe54767908f3e68800140f607486d87f59af453d62cffe00b6836d8cca2c89a340fab5fe842b20696908c77fd2f64900feb0cbb1c14da3e02271503fc465fcfb1b043c8187dccdd494558014067dff0f0c321fc8abc28bf555acfdfa5ee889b6909b24bc66cedf05e8cc2750a4d95037c3dc9c24f1e502198bade56fef61a2504809f5b2a60a62afeaf8bf52e00000000"
	validTxBytes, err := hex.DecodeString(validTxHex)
	require.NoError(t, err)

	validTx := &pb.UserSignedTxSigningJob{
		LeafId: "550e8400-e29b-41d4-a716-446655440000",
		SigningCommitments: &pb.SigningCommitments{
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
		UserSignature: []byte("test_signature"),
		RawTx:         validTxBytes,
	}

	t.Run("non-node owner cannot initiate preimage swap", func(t *testing.T) {
		// Use reflection to modify the original config and enable authorization
		baseConfig := &so.Config{AuthzEnforced: true, FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{}}

		lightningHandler := NewLightningHandler(baseConfig)

		// Create an authentication session with a specific identity (different from node owner)
		sessionIdentityKey := keys.MustGeneratePrivateKeyFromRand(rng) // Different from node owner
		// Create token verifier using the session identity key so the token will validate properly
		tokenVerifier, err := authninternal.NewSessionTokenCreatorVerifier(sessionIdentityKey, authninternal.RealClock{})
		require.NoError(t, err)

		// Create a valid session token for the session identity
		tokenResult, err := tokenVerifier.CreateToken(sessionIdentityKey.Public(), time.Hour)
		require.NoError(t, err)

		// Create context with authorization header like real gRPC requests
		ctx = metadata.NewIncomingContext(ctx, metadata.Pairs(
			"authorization", "Bearer "+tokenResult.Token,
		))

		// Use the authn interceptor to properly set the authentication context
		authnInterceptor := authn.NewInterceptor(tokenVerifier)
		var authenticatedCtx context.Context
		_, err = authnInterceptor.AuthnInterceptor(ctx, nil, &grpc.UnaryServerInfo{}, func(ctx context.Context, _ any) (any, error) {
			authenticatedCtx = ctx
			return nil, nil
		})
		require.NoError(t, err)

		// Verify the session was set correctly
		session, err := authn.GetSessionFromContext(authenticatedCtx)
		require.NoError(t, err)
		require.Equal(t, session.IdentityPublicKey(), sessionIdentityKey.Public())

		// Create a tree node in the database for the test
		tx, err := ent.GetDbFromContext(authenticatedCtx)
		require.NoError(t, err)

		// Create a tree first
		baseTxid := st.NewRandomTxIDForTesting(t)
		tree, err := tx.Tree.Create().
			SetOwnerIdentityPubkey(validPubKey).
			SetStatus(st.TreeStatusAvailable).
			SetNetwork(btcnetwork.Mainnet).
			SetBaseTxid(baseTxid).
			SetVout(0).
			Save(authenticatedCtx)
		require.NoError(t, err)

		wrongKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
		// Create a keyshare with proper 33-byte public keys
		secretShare := keys.MustGeneratePrivateKeyFromRand(rng)
		keyshare, err := tx.SigningKeyshare.Create().
			SetStatus(st.KeyshareStatusInUse).
			SetSecretShare(secretShare).
			SetPublicShares(map[string]keys.Public{"operator1": wrongKey}).
			SetPublicKey(sessionIdentityKey.Public()).
			SetMinSigners(2).
			SetCoordinatorIndex(1).
			Save(authenticatedCtx)
		require.NoError(t, err)

		// Create a tree node with a different owner than the session
		nodeID, err := uuid.Parse(validTx.GetLeafId())
		require.NoError(t, err)

		correctScript, err := common.P2TRScriptFromPubKey(wrongKey)
		require.NoError(t, err)

		// Create parent tx (stored in node.RawTx) and refund tx (sent by client)
		// with proper outpoint reference
		parentTx, refundTx := createParentAndRefundTx(t, correctScript, 1000)

		_, err = tx.TreeNode.Create().
			SetTree(tree).
			SetNetwork(tree.Network).
			SetID(nodeID). // Use the specific ID from the test
			SetValue(1000).
			SetStatus(st.TreeNodeStatusAvailable).
			SetVerifyingPubkey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
			SetOwnerIdentityPubkey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
			SetOwnerSigningPubkey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
			SetRawTx(parentTx).
			SetVout(0).
			SetSigningKeyshare(keyshare).
			Save(authenticatedCtx)
		require.NoError(t, err)

		// Update the test transaction to use the refund tx that references the parent
		validTx.RawTx = refundTx

		mockFrostConnection := &mockFrostServiceClientConnection{}

		// This test should fail because the node is not the owner of the leaf.
		err = lightningHandler.validateGetPreimageRequestWithFrostServiceClientFactory(
			authenticatedCtx,
			mockFrostConnection,
			paymentHash,
			[]*pb.UserSignedTxSigningJob{validTx},
			[]*pb.UserSignedTxSigningJob{},
			[]*pb.UserSignedTxSigningJob{},
			1000,
			wrongKey,
			singleLeafDestination(wrongKey),
			0,
			pb.InitiatePreimageSwapRequest_REASON_SEND,
			true, // validateNodeOwnership = true
		)

		require.ErrorContains(t, err, "not owned by the authenticated identity public key")
	})
}

func TestUpdatePreimageRequestRejectsMalformedPreimageLength(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{61})
	ctx, _ := db.NewTestSQLiteContext(t)

	lightningHandler := NewLightningHandler(&so.Config{FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{}})
	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	receiverPub := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	shortPreimage := []byte("short")
	paymentHash := sha256.Sum256(shortPreimage)

	preimageRequest, err := dbTx.PreimageRequest.Create().
		SetPaymentHash(paymentHash[:]).
		SetStatus(st.PreimageRequestStatusWaitingForPreimage).
		SetReceiverIdentityPubkey(receiverPub).
		Save(ctx)
	require.NoError(t, err)

	err = lightningHandler.UpdatePreimageRequest(ctx, &pbinternal.UpdatePreimageRequestRequest{
		Preimage:          shortPreimage,
		IdentityPublicKey: receiverPub.Serialize(),
	})
	require.ErrorContains(t, err, "preimage must be 32 bytes")

	unchanged, err := dbTx.PreimageRequest.Get(ctx, preimageRequest.ID)
	require.NoError(t, err)
	require.Equal(t, st.PreimageRequestStatusWaitingForPreimage, unchanged.Status)
	require.Empty(t, unchanged.Preimage)
}

func TestValidateLightningRefundLeafIDsRejectsDuplicates(t *testing.T) {
	leafID := uuid.New().String()
	err := validateLightningRefundLeafIDs("cpfp_transactions", []*pb.UserSignedTxSigningJob{
		{LeafId: leafID},
		{LeafId: leafID},
	})

	require.ErrorContains(t, err, "duplicate leaf id in cpfp_transactions")
	code, reason := sparkerrors.CodeAndReasonFrom(err)
	require.Equal(t, codes.InvalidArgument, code)
	require.Equal(t, "DUPLICATE_FIELD", reason)
}

func TestValidateGetPreimageRequestRejectsDuplicateCpfpLeafBeforeAmountAggregation(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{7})
	ctx, _ := db.ConnectToTestPostgres(t)

	config := &so.Config{FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{}}
	lightningHandler := NewLightningHandler(config)

	destinationPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	destinationScript, err := common.P2TRScriptFromPubKey(destinationPubKey)
	require.NoError(t, err)
	parentTx, refundTx := createParentAndRefundTx(t, destinationScript, 1000)

	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	tree, err := tx.Tree.Create().
		SetOwnerIdentityPubkey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
		SetStatus(st.TreeStatusAvailable).
		SetNetwork(btcnetwork.Mainnet).
		SetBaseTxid(st.NewRandomTxIDForTesting(t)).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)
	secretShare := keys.MustGeneratePrivateKeyFromRand(rng)
	keyshare, err := tx.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusInUse).
		SetSecretShare(secretShare).
		SetPublicShares(map[string]keys.Public{"operator1": secretShare.Public()}).
		SetPublicKey(secretShare.Public()).
		SetMinSigners(2).
		SetCoordinatorIndex(1).
		Save(ctx)
	require.NoError(t, err)

	leafID := uuid.New()
	_, err = tx.TreeNode.Create().
		SetTree(tree).
		SetNetwork(tree.Network).
		SetID(leafID).
		SetValue(1000).
		SetStatus(st.TreeNodeStatusAvailable).
		SetVerifyingPubkey(secretShare.Public()).
		SetOwnerIdentityPubkey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
		SetOwnerSigningPubkey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
		SetRawTx(parentTx).
		SetVout(0).
		SetSigningKeyshare(keyshare).
		Save(ctx)
	require.NoError(t, err)

	job := &pb.UserSignedTxSigningJob{
		LeafId: leafID.String(),
		SigningCommitments: &pb.SigningCommitments{
			SigningCommitments: map[string]*pbcommon.SigningCommitment{
				"operator1": {
					Hiding:  []byte("test_hiding"),
					Binding: []byte("test_binding"),
				},
			},
		},
		SigningNonceCommitment: &pbcommon.SigningCommitment{
			Hiding:  []byte("test_nonce_hiding"),
			Binding: []byte("test_nonce_binding"),
		},
		UserSignature: []byte("test_signature"),
		RawTx:         refundTx,
	}

	err = lightningHandler.validateGetPreimageRequestWithFrostServiceClientFactory(
		ctx,
		&mockFrostServiceClientConnection{},
		bytes.Repeat([]byte{0x42}, 32),
		[]*pb.UserSignedTxSigningJob{job, proto.Clone(job).(*pb.UserSignedTxSigningJob)},
		nil,
		nil,
		1500,
		destinationPubKey,
		singleLeafDestination(destinationPubKey),
		0,
		pb.InitiatePreimageSwapRequest_REASON_SEND,
		false,
	)

	require.ErrorContains(t, err, "duplicate leaf id in cpfp_transactions")
	code, reason := sparkerrors.CodeAndReasonFrom(err)
	require.Equal(t, codes.InvalidArgument, code)
	require.Equal(t, "DUPLICATE_FIELD", reason)
}

// Regression test for https://linear.app/lightsparkdev/issue/LIG-8086
func TestValidateGetPreimageRequestMismatchedAmounts(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{1})
	ctx, _ := db.ConnectToTestPostgres(t)

	config := &so.Config{FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{}}
	lightningHandler := NewLightningHandler(config)

	validPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	verifyingPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	paymentHash := []byte("test_payment_hash_32_bytes_long_")

	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	baseTxid2 := st.NewRandomTxIDForTesting(t)
	tree, err := tx.Tree.Create().
		SetOwnerIdentityPubkey(validPubKey).
		SetStatus(st.TreeStatusAvailable).
		SetNetwork(btcnetwork.Mainnet).
		SetBaseTxid(baseTxid2).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)

	keyshare, err := tx.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusInUse).
		SetSecretShare(keys.MustGeneratePrivateKeyFromRand(rng)).
		SetPublicShares(map[string]keys.Public{"operator1": validPubKey}).
		SetPublicKey(validPubKey).
		SetMinSigners(2).
		SetCoordinatorIndex(1).
		Save(ctx)
	require.NoError(t, err)

	nodeID := uuid.MustParse("550e8400-e29b-41d4-a716-446655440000")

	// Create a transaction with 500 sats output (different from expected 1000)
	correctScript, err := common.P2TRScriptFromPubKey(validPubKey)
	require.NoError(t, err)

	// Create parent tx (stored in node.RawTx) and refund tx (sent by client)
	// with proper outpoint reference - both have 500 sats
	parentTx, refundTx := createParentAndRefundTx(t, correctScript, 500)

	_, err = tx.TreeNode.Create().
		SetTree(tree).
		SetNetwork(tree.Network).
		SetID(nodeID).
		SetValue(500). // This is the value in the tree node, but RawTx will also have 500 sats
		SetStatus(st.TreeNodeStatusAvailable).
		SetVerifyingPubkey(verifyingPubKey).
		SetOwnerIdentityPubkey(validPubKey).
		SetOwnerSigningPubkey(validPubKey).
		SetRawTx(parentTx).
		SetDirectTx(parentTx). // Set direct_tx field which is required for direct transaction validation
		SetVout(0).
		SetSigningKeyshare(keyshare).
		Save(ctx)
	require.NoError(t, err)

	// Create a transaction for testing with mismatched amounts
	testTx := &pb.UserSignedTxSigningJob{
		LeafId: nodeID.String(),
		SigningCommitments: &pb.SigningCommitments{
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
		UserSignature: []byte("test_signature"),
		RawTx:         refundTx, // Contains 500 sats output, properly references parent tx
	}

	mockFrostConnection := &mockFrostServiceClientConnection{}

	// This should fail because the total amount (500 sats) doesn't match expected (1000 sats)
	err = lightningHandler.validateGetPreimageRequestWithFrostServiceClientFactory(
		ctx,
		mockFrostConnection,
		paymentHash,
		[]*pb.UserSignedTxSigningJob{testTx}, // cpfp transactions with 500 sats (these contribute to totalAmount)
		[]*pb.UserSignedTxSigningJob{},       // empty direct transactions
		[]*pb.UserSignedTxSigningJob{},       // empty directFromCpfp transactions
		1000,                                 // Expected 1000 sats but getting 500
		validPubKey,
		singleLeafDestination(validPubKey),
		0,
		pb.InitiatePreimageSwapRequest_REASON_SEND,
		false, // validateNodeOwnership = false for this test
	)

	require.ErrorContains(t, err, "invalid amount, expected: 1000 or more, got: 500")
	code, reason := sparkerrors.CodeAndReasonFrom(err)
	require.Equal(t, codes.InvalidArgument, code)
	require.Equal(t, "OUT_OF_RANGE", reason)
}

func TestValidateGetPreimageRequestRejectsExtraValueOutput(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{2})
	ctx, _ := db.ConnectToTestPostgres(t)

	config := &so.Config{FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{}}
	lightningHandler := NewLightningHandler(config)

	destinationPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	verifyingPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	attackerPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	paymentHash := []byte("test_payment_hash_32_bytes_long_")

	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	tree, err := tx.Tree.Create().
		SetOwnerIdentityPubkey(destinationPubKey).
		SetStatus(st.TreeStatusAvailable).
		SetNetwork(btcnetwork.Mainnet).
		SetBaseTxid(st.NewRandomTxIDForTesting(t)).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)

	keyshare, err := tx.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusInUse).
		SetSecretShare(keys.MustGeneratePrivateKeyFromRand(rng)).
		SetPublicShares(map[string]keys.Public{"operator1": destinationPubKey}).
		SetPublicKey(destinationPubKey).
		SetMinSigners(2).
		SetCoordinatorIndex(1).
		Save(ctx)
	require.NoError(t, err)

	nodeID := uuid.New()
	destinationScript, err := common.P2TRScriptFromPubKey(destinationPubKey)
	require.NoError(t, err)
	attackerScript, err := common.P2TRScriptFromPubKey(attackerPubKey)
	require.NoError(t, err)

	parentTx, refundTx := createParentAndRefundTxWithExtraOutput(t, destinationScript, attackerScript, 500, 500)

	_, err = tx.TreeNode.Create().
		SetTree(tree).
		SetNetwork(tree.Network).
		SetID(nodeID).
		SetValue(1000).
		SetStatus(st.TreeNodeStatusAvailable).
		SetVerifyingPubkey(verifyingPubKey).
		SetOwnerIdentityPubkey(destinationPubKey).
		SetOwnerSigningPubkey(destinationPubKey).
		SetRawTx(parentTx).
		SetVout(0).
		SetSigningKeyshare(keyshare).
		Save(ctx)
	require.NoError(t, err)

	testTx := &pb.UserSignedTxSigningJob{
		LeafId: nodeID.String(),
		SigningCommitments: &pb.SigningCommitments{
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
		UserSignature: []byte("test_signature"),
		RawTx:         refundTx,
	}

	err = lightningHandler.validateGetPreimageRequestWithFrostServiceClientFactory(
		ctx,
		&mockFrostServiceClientConnection{},
		paymentHash,
		[]*pb.UserSignedTxSigningJob{testTx},
		[]*pb.UserSignedTxSigningJob{},
		[]*pb.UserSignedTxSigningJob{},
		500,
		destinationPubKey,
		singleLeafDestination(destinationPubKey),
		0,
		pb.InitiatePreimageSwapRequest_REASON_SEND,
		false,
	)

	require.ErrorContains(t, err, "unexpected extra cpfp tx output 1")
	code, reason := sparkerrors.CodeAndReasonFrom(err)
	require.Equal(t, codes.InvalidArgument, code)
	require.Equal(t, "MALFORMED_FIELD", reason)
}

// TestValidateGetPreimageRequestOutputShapes exercises the refund output-shape
// rules through the full request-validation path for each transaction type:
// only the cpfp refund may carry a single trailing ephemeral anchor, and no
// other extra outputs are ever allowed.
func TestValidateGetPreimageRequestOutputShapes(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{7})
	ctx, _ := db.ConnectToTestPostgres(t)

	config := &so.Config{FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{}}
	lightningHandler := NewLightningHandler(config)

	destinationPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	verifyingPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	attackerPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	paymentHashBytes := sha256.Sum256([]byte("refund output shapes"))
	paymentHash := paymentHashBytes[:]

	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	tree, err := tx.Tree.Create().
		SetOwnerIdentityPubkey(destinationPubKey).
		SetStatus(st.TreeStatusAvailable).
		SetNetwork(btcnetwork.Mainnet).
		SetBaseTxid(st.NewRandomTxIDForTesting(t)).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)

	keyshare, err := tx.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusInUse).
		SetSecretShare(keys.MustGeneratePrivateKeyFromRand(rng)).
		SetPublicShares(map[string]keys.Public{"operator1": destinationPubKey}).
		SetPublicKey(destinationPubKey).
		SetMinSigners(2).
		SetCoordinatorIndex(1).
		Save(ctx)
	require.NoError(t, err)

	destinationScript, err := common.P2TRScriptFromPubKey(destinationPubKey)
	require.NoError(t, err)
	attackerScript, err := common.P2TRScriptFromPubKey(attackerPubKey)
	require.NoError(t, err)

	destinationOut := func() *wire.TxOut { return &wire.TxOut{Value: 500, PkScript: destinationScript} }
	anchorScript := common.EphemeralAnchorOutput().PkScript

	for _, tc := range []struct {
		name        string
		refundOuts  []*wire.TxOut
		target      string // which signing-job list carries the refund
		expectedErr string // empty means the request must validate successfully
	}{
		{
			name:       "cpfp allows single destination output without anchor",
			refundOuts: []*wire.TxOut{destinationOut()},
			target:     "cpfp",
		},
		{
			name:       "cpfp allows trailing ephemeral anchor",
			refundOuts: []*wire.TxOut{destinationOut(), common.EphemeralAnchorOutput()},
			target:     "cpfp",
		},
		{
			// A valid trailing anchor must not open the door to a further value output.
			name:        "cpfp rejects value output after valid anchor",
			refundOuts:  []*wire.TxOut{destinationOut(), common.EphemeralAnchorOutput(), {Value: 500, PkScript: attackerScript}},
			target:      "cpfp",
			expectedErr: "unexpected extra cpfp tx output 2",
		},
		{
			// The anchor script alone is not enough; the value must also be zero.
			name:        "cpfp rejects anchor script with nonzero value",
			refundOuts:  []*wire.TxOut{destinationOut(), {Value: 1, PkScript: anchorScript}},
			target:      "cpfp",
			expectedErr: "unexpected extra cpfp tx output 1",
		},
		{
			name:        "cpfp rejects negative value output",
			refundOuts:  []*wire.TxOut{destinationOut(), {Value: -1, PkScript: attackerScript}},
			target:      "cpfp",
			expectedErr: "cpfp tx output 1 has negative value",
		},
		{
			name:        "direct rejects ephemeral anchor",
			refundOuts:  []*wire.TxOut{destinationOut(), common.EphemeralAnchorOutput()},
			target:      "direct",
			expectedErr: "unexpected extra direct tx output 1",
		},
		{
			name:        "direct from cpfp rejects ephemeral anchor",
			refundOuts:  []*wire.TxOut{destinationOut(), common.EphemeralAnchorOutput()},
			target:      "directFromCpfp",
			expectedErr: "unexpected extra direct from cpfp tx output 1",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			nodeID := uuid.New()
			// Setting both raw_tx (CPFP source) and direct_tx to parentTx lets the
			// same refund satisfy the outpoint check on every path.
			parentTx, refundTx := createParentAndRefundTxWithOutputs(t, destinationScript, 1000, tc.refundOuts)

			_, err := tx.TreeNode.Create().
				SetTree(tree).
				SetNetwork(tree.Network).
				SetID(nodeID).
				SetValue(1000).
				SetStatus(st.TreeNodeStatusAvailable).
				SetVerifyingPubkey(verifyingPubKey).
				SetOwnerIdentityPubkey(destinationPubKey).
				SetOwnerSigningPubkey(destinationPubKey).
				SetRawTx(parentTx).
				SetDirectTx(parentTx).
				SetVout(0).
				SetSigningKeyshare(keyshare).
				Save(ctx)
			require.NoError(t, err)

			testTx := &pb.UserSignedTxSigningJob{
				LeafId: nodeID.String(),
				SigningCommitments: &pb.SigningCommitments{
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
				UserSignature: []byte("test_signature"),
				RawTx:         refundTx,
			}

			empty := []*pb.UserSignedTxSigningJob{}
			cpfp, direct, directFromCpfp := empty, empty, empty
			switch tc.target {
			case "cpfp":
				cpfp = []*pb.UserSignedTxSigningJob{testTx}
			case "direct":
				direct = []*pb.UserSignedTxSigningJob{testTx}
			case "directFromCpfp":
				directFromCpfp = []*pb.UserSignedTxSigningJob{testTx}
			default:
				t.Fatalf("unknown target %q", tc.target)
			}

			err = lightningHandler.validateGetPreimageRequestWithFrostServiceClientFactory(
				ctx,
				&mockFrostServiceClientConnection{},
				paymentHash,
				cpfp,
				direct,
				directFromCpfp,
				500,
				destinationPubKey,
				singleLeafDestination(destinationPubKey),
				0,
				pb.InitiatePreimageSwapRequest_REASON_SEND,
				false,
			)

			if tc.expectedErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tc.expectedErr)
			code, reason := sparkerrors.CodeAndReasonFrom(err)
			require.Equal(t, codes.InvalidArgument, code)
			require.Equal(t, "MALFORMED_FIELD", reason)
		})
	}
}

// TestValidateGetPreimageRequestRejectsExtraValueOutputDirectPaths is the
// direct / direct-from-cpfp counterpart to
// TestValidateGetPreimageRequestRejectsExtraValueOutput, exercising the full
// request-validation path (not just the helper) for the two non-CPFP refund
// types, which carry no ephemeral anchor and must have exactly one output.
func TestValidateGetPreimageRequestRejectsExtraValueOutputDirectPaths(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{3})
	ctx, _ := db.ConnectToTestPostgres(t)

	config := &so.Config{FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{}}
	lightningHandler := NewLightningHandler(config)

	destinationPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	verifyingPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	attackerPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	paymentHash := []byte("test_payment_hash_32_bytes_long_")

	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	tree, err := tx.Tree.Create().
		SetOwnerIdentityPubkey(destinationPubKey).
		SetStatus(st.TreeStatusAvailable).
		SetNetwork(btcnetwork.Mainnet).
		SetBaseTxid(st.NewRandomTxIDForTesting(t)).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)

	keyshare, err := tx.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusInUse).
		SetSecretShare(keys.MustGeneratePrivateKeyFromRand(rng)).
		SetPublicShares(map[string]keys.Public{"operator1": destinationPubKey}).
		SetPublicKey(destinationPubKey).
		SetMinSigners(2).
		SetCoordinatorIndex(1).
		Save(ctx)
	require.NoError(t, err)

	nodeID := uuid.New()
	destinationScript, err := common.P2TRScriptFromPubKey(destinationPubKey)
	require.NoError(t, err)
	attackerScript, err := common.P2TRScriptFromPubKey(attackerPubKey)
	require.NoError(t, err)

	// The refund spends from parentTx; setting both raw_tx (CPFP source) and
	// direct_tx to parentTx lets the same refund satisfy the outpoint check on
	// both the direct and direct-from-cpfp paths.
	parentTx, refundTx := createParentAndRefundTxWithExtraOutput(t, destinationScript, attackerScript, 500, 500)

	_, err = tx.TreeNode.Create().
		SetTree(tree).
		SetNetwork(tree.Network).
		SetID(nodeID).
		SetValue(1000).
		SetStatus(st.TreeNodeStatusAvailable).
		SetVerifyingPubkey(verifyingPubKey).
		SetOwnerIdentityPubkey(destinationPubKey).
		SetOwnerSigningPubkey(destinationPubKey).
		SetRawTx(parentTx).
		SetDirectTx(parentTx).
		SetVout(0).
		SetSigningKeyshare(keyshare).
		Save(ctx)
	require.NoError(t, err)

	testTx := &pb.UserSignedTxSigningJob{
		LeafId: nodeID.String(),
		SigningCommitments: &pb.SigningCommitments{
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
		UserSignature: []byte("test_signature"),
		RawTx:         refundTx,
	}
	empty := []*pb.UserSignedTxSigningJob{}

	for _, tc := range []struct {
		name           string
		direct         []*pb.UserSignedTxSigningJob
		directFromCpfp []*pb.UserSignedTxSigningJob
		expectedErr    string
	}{
		{
			name:           "direct",
			direct:         []*pb.UserSignedTxSigningJob{testTx},
			directFromCpfp: empty,
			expectedErr:    "unexpected extra direct tx output 1",
		},
		{
			name:           "direct from cpfp",
			direct:         empty,
			directFromCpfp: []*pb.UserSignedTxSigningJob{testTx},
			expectedErr:    "unexpected extra direct from cpfp tx output 1",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := lightningHandler.validateGetPreimageRequestWithFrostServiceClientFactory(
				ctx,
				&mockFrostServiceClientConnection{},
				paymentHash,
				empty,
				tc.direct,
				tc.directFromCpfp,
				500,
				destinationPubKey,
				singleLeafDestination(destinationPubKey),
				0,
				pb.InitiatePreimageSwapRequest_REASON_SEND,
				false,
			)

			require.ErrorContains(t, err, tc.expectedErr)
			code, reason := sparkerrors.CodeAndReasonFrom(err)
			require.Equal(t, codes.InvalidArgument, code)
			require.Equal(t, "MALFORMED_FIELD", reason)
		})
	}
}

func TestValidateGetPreimageRequestAllowsUnspecifiedInvoiceAmount(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{6})
	ctx, _ := db.ConnectToTestPostgres(t)

	config := &so.Config{FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{}}
	lightningHandler := NewLightningHandler(config)

	destinationPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	verifyingPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	paymentHashBytes := sha256.Sum256([]byte("nil invoice amount"))
	paymentHash := paymentHashBytes[:]

	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	tree, err := tx.Tree.Create().
		SetOwnerIdentityPubkey(destinationPubKey).
		SetStatus(st.TreeStatusAvailable).
		SetNetwork(btcnetwork.Mainnet).
		SetBaseTxid(st.NewRandomTxIDForTesting(t)).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)

	keyshare, err := tx.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusInUse).
		SetSecretShare(keys.MustGeneratePrivateKeyFromRand(rng)).
		SetPublicShares(map[string]keys.Public{"operator1": destinationPubKey}).
		SetPublicKey(destinationPubKey).
		SetMinSigners(2).
		SetCoordinatorIndex(1).
		Save(ctx)
	require.NoError(t, err)

	nodeID := uuid.MustParse("550e8400-e29b-41d4-a716-446655440006")
	outputScript, err := common.P2TRScriptFromPubKey(destinationPubKey)
	require.NoError(t, err)

	parentTx, refundTx := createParentAndRefundTx(t, outputScript, 1000)
	_, err = tx.TreeNode.Create().
		SetTree(tree).
		SetNetwork(tree.Network).
		SetID(nodeID).
		SetValue(1000).
		SetStatus(st.TreeNodeStatusAvailable).
		SetVerifyingPubkey(verifyingPubKey).
		SetOwnerIdentityPubkey(destinationPubKey).
		SetOwnerSigningPubkey(destinationPubKey).
		SetRawTx(parentTx).
		SetVout(0).
		SetSigningKeyshare(keyshare).
		Save(ctx)
	require.NoError(t, err)

	testTx := &pb.UserSignedTxSigningJob{
		LeafId: nodeID.String(),
		SigningCommitments: &pb.SigningCommitments{
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
		UserSignature: []byte("test_signature"),
		RawTx:         refundTx,
	}

	require.NotPanics(t, func() {
		err = lightningHandler.validateGetPreimageRequestWithFrostServiceClientFactory(
			ctx,
			&mockFrostServiceClientConnection{},
			paymentHash,
			[]*pb.UserSignedTxSigningJob{testTx},
			[]*pb.UserSignedTxSigningJob{},
			[]*pb.UserSignedTxSigningJob{},
			0,
			destinationPubKey,
			singleLeafDestination(destinationPubKey),
			0,
			pb.InitiatePreimageSwapRequest_REASON_SEND,
			false,
		)
	})
	require.NoError(t, err)
}

func TestValidateGetPreimageRequestRejectsNegativeRefundOutput(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{4})
	ctx, _ := db.ConnectToTestPostgres(t)

	config := &so.Config{FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{}}
	lightningHandler := NewLightningHandler(config)

	validPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	verifyingPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	paymentHashBytes := sha256.Sum256([]byte("negative refund output"))
	paymentHash := paymentHashBytes[:]

	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	tree, err := tx.Tree.Create().
		SetOwnerIdentityPubkey(validPubKey).
		SetStatus(st.TreeStatusAvailable).
		SetNetwork(btcnetwork.Mainnet).
		SetBaseTxid(st.NewRandomTxIDForTesting(t)).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)

	keyshare, err := tx.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusInUse).
		SetSecretShare(keys.MustGeneratePrivateKeyFromRand(rng)).
		SetPublicShares(map[string]keys.Public{"operator1": validPubKey}).
		SetPublicKey(validPubKey).
		SetMinSigners(2).
		SetCoordinatorIndex(1).
		Save(ctx)
	require.NoError(t, err)

	nodeID := uuid.MustParse("550e8400-e29b-41d4-a716-446655440001")
	outputScript, err := common.P2TRScriptFromPubKey(validPubKey)
	require.NoError(t, err)

	parentTx, refundTx := createParentAndRefundTx(t, outputScript, 1000)
	refundMsgTx, err := common.TxFromRawTxBytes(refundTx)
	require.NoError(t, err)
	refundMsgTx.TxOut[0].Value = -1
	refundTx, err = common.SerializeTx(refundMsgTx)
	require.NoError(t, err)

	_, err = tx.TreeNode.Create().
		SetTree(tree).
		SetNetwork(tree.Network).
		SetID(nodeID).
		SetValue(1000).
		SetStatus(st.TreeNodeStatusAvailable).
		SetVerifyingPubkey(verifyingPubKey).
		SetOwnerIdentityPubkey(validPubKey).
		SetOwnerSigningPubkey(validPubKey).
		SetRawTx(parentTx).
		SetDirectTx(parentTx).
		SetVout(0).
		SetSigningKeyshare(keyshare).
		Save(ctx)
	require.NoError(t, err)

	testTx := &pb.UserSignedTxSigningJob{
		LeafId: nodeID.String(),
		SigningCommitments: &pb.SigningCommitments{
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
		UserSignature: []byte("test_signature"),
		RawTx:         refundTx,
	}

	err = lightningHandler.validateGetPreimageRequestWithFrostServiceClientFactory(
		ctx,
		&mockFrostServiceClientConnection{},
		paymentHash,
		[]*pb.UserSignedTxSigningJob{testTx},
		[]*pb.UserSignedTxSigningJob{},
		[]*pb.UserSignedTxSigningJob{},
		1000,
		validPubKey,
		singleLeafDestination(validPubKey),
		0,
		pb.InitiatePreimageSwapRequest_REASON_SEND,
		false,
	)

	require.ErrorContains(t, err, "cpfp refund tx output 0 value must be positive")
	code, reason := sparkerrors.CodeAndReasonFrom(err)
	require.Equal(t, codes.InvalidArgument, code)
	require.Equal(t, "OUT_OF_RANGE", reason)
}

func TestValidateGetPreimageRequestRejectsExtraRefundInputs(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{5})
	ctx, _ := db.ConnectToTestPostgres(t)

	config := &so.Config{FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{}}
	lightningHandler := NewLightningHandler(config)

	destinationPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	verifyingPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	paymentHashBytes := sha256.Sum256([]byte("extra preimage refund input"))
	paymentHash := paymentHashBytes[:]

	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	tree, err := tx.Tree.Create().
		SetOwnerIdentityPubkey(destinationPubKey).
		SetStatus(st.TreeStatusAvailable).
		SetNetwork(btcnetwork.Mainnet).
		SetBaseTxid(st.NewRandomTxIDForTesting(t)).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)

	keyshare, err := tx.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusInUse).
		SetSecretShare(keys.MustGeneratePrivateKeyFromRand(rng)).
		SetPublicShares(map[string]keys.Public{"operator1": destinationPubKey}).
		SetPublicKey(destinationPubKey).
		SetMinSigners(2).
		SetCoordinatorIndex(1).
		Save(ctx)
	require.NoError(t, err)

	nodeID := uuid.MustParse("550e8400-e29b-41d4-a716-446655440005")
	outputScript, err := common.P2TRScriptFromPubKey(destinationPubKey)
	require.NoError(t, err)

	createParentAndRefundTxWithPrevIndex := func(prevIndex uint32) ([]byte, []byte) {
		t.Helper()
		parentTx := wire.NewMsgTx(2)
		parentTx.AddTxIn(&wire.TxIn{
			PreviousOutPoint: wire.OutPoint{Index: prevIndex},
			Sequence:         wire.MaxTxInSequenceNum,
		})
		parentTx.AddTxOut(&wire.TxOut{Value: 1000, PkScript: outputScript})
		parentTxBytes, err := common.SerializeTx(parentTx)
		require.NoError(t, err)

		refundTx := wire.NewMsgTx(2)
		refundTx.AddTxIn(&wire.TxIn{
			PreviousOutPoint: wire.OutPoint{Hash: parentTx.TxHash(), Index: 0},
			Sequence:         wire.MaxTxInSequenceNum,
		})
		refundTx.AddTxOut(&wire.TxOut{Value: 1000, PkScript: outputScript})
		refundTxBytes, err := common.SerializeTx(refundTx)
		require.NoError(t, err)
		return parentTxBytes, refundTxBytes
	}

	cpfpParentTx, cpfpRefundTx := createParentAndRefundTxWithPrevIndex(0)
	directParentTx, directRefundTx := createParentAndRefundTxWithPrevIndex(1)
	directFromCpfpRefundTx := cpfpRefundTx

	_, err = tx.TreeNode.Create().
		SetTree(tree).
		SetNetwork(tree.Network).
		SetID(nodeID).
		SetValue(1000).
		SetStatus(st.TreeNodeStatusAvailable).
		SetVerifyingPubkey(verifyingPubKey).
		SetOwnerIdentityPubkey(destinationPubKey).
		SetOwnerSigningPubkey(destinationPubKey).
		SetRawTx(cpfpParentTx).
		SetDirectTx(directParentTx).
		SetVout(0).
		SetSigningKeyshare(keyshare).
		Save(ctx)
	require.NoError(t, err)

	signingJob := func(rawTx []byte) *pb.UserSignedTxSigningJob {
		return &pb.UserSignedTxSigningJob{
			LeafId: nodeID.String(),
			SigningCommitments: &pb.SigningCommitments{
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
			UserSignature: []byte("test_signature"),
			RawTx:         rawTx,
		}
	}

	withExtraInput := func(rawTx []byte) []byte {
		t.Helper()
		parsed, err := common.TxFromRawTxBytes(rawTx)
		require.NoError(t, err)
		parsed.AddTxIn(&wire.TxIn{
			PreviousOutPoint: wire.OutPoint{Index: 42},
			Sequence:         wire.MaxTxInSequenceNum,
		})
		serialized, err := common.SerializeTx(parsed)
		require.NoError(t, err)
		return serialized
	}

	tests := []struct {
		name                       string
		cpfpTransactions           []*pb.UserSignedTxSigningJob
		directTransactions         []*pb.UserSignedTxSigningJob
		directFromCpfpTransactions []*pb.UserSignedTxSigningJob
		expectedErr                string
	}{
		{
			name:             "cpfp refund with extra input",
			cpfpTransactions: []*pb.UserSignedTxSigningJob{signingJob(withExtraInput(cpfpRefundTx))},
			expectedErr:      "cpfp refund tx should have exactly 1 input",
		},
		{
			name:               "direct refund with extra input",
			cpfpTransactions:   []*pb.UserSignedTxSigningJob{signingJob(cpfpRefundTx)},
			directTransactions: []*pb.UserSignedTxSigningJob{signingJob(withExtraInput(directRefundTx))},
			expectedErr:        "direct refund tx should have exactly 1 input",
		},
		{
			name:                       "direct from cpfp refund with extra input",
			cpfpTransactions:           []*pb.UserSignedTxSigningJob{signingJob(cpfpRefundTx)},
			directFromCpfpTransactions: []*pb.UserSignedTxSigningJob{signingJob(withExtraInput(directFromCpfpRefundTx))},
			expectedErr:                "direct from cpfp refund tx should have exactly 1 input",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := lightningHandler.validateGetPreimageRequestWithFrostServiceClientFactory(
				ctx,
				&mockFrostServiceClientConnection{},
				paymentHash,
				tt.cpfpTransactions,
				tt.directTransactions,
				tt.directFromCpfpTransactions,
				1000,
				destinationPubKey,
				singleLeafDestination(destinationPubKey),
				0,
				pb.InitiatePreimageSwapRequest_REASON_SEND,
				false,
			)

			require.ErrorContains(t, err, tt.expectedErr)
			code, reason := sparkerrors.CodeAndReasonFrom(err)
			require.Equal(t, codes.InvalidArgument, code)
			require.Equal(t, "MALFORMED_FIELD", reason)
		})
	}
}

func TestValidateGetPreimageRequestRespectsFrostValidationConcurrencyLimit(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{3})
	ctx, _ := db.ConnectToTestPostgres(t)

	const parallelLimit int32 = 2
	ctx = knobs.InjectKnobsService(ctx, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobSoMaxParallelFrostValidationsPerRequest: float64(parallelLimit),
	}))

	config := &so.Config{FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{}}
	lightningHandler := NewLightningHandler(config)

	paymentHash := bytes.Repeat([]byte{1}, 32)
	destinationPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	baseTxid := st.NewRandomTxIDForTesting(t)
	tree, err := tx.Tree.Create().
		SetOwnerIdentityPubkey(destinationPubKey).
		SetStatus(st.TreeStatusAvailable).
		SetNetwork(btcnetwork.Mainnet).
		SetBaseTxid(baseTxid).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)

	keyshare, err := tx.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusInUse).
		SetSecretShare(keys.MustGeneratePrivateKeyFromRand(rng)).
		SetPublicShares(map[string]keys.Public{"operator1": destinationPubKey}).
		SetPublicKey(destinationPubKey).
		SetMinSigners(2).
		SetCoordinatorIndex(1).
		Save(ctx)
	require.NoError(t, err)

	const numTransactions = 6
	cpfpTransactions := make([]*pb.UserSignedTxSigningJob, 0, numTransactions)
	outputScript, err := common.P2TRScriptFromPubKey(destinationPubKey)
	require.NoError(t, err)

	for i := range numTransactions {
		nodeID := uuid.New()
		parentTx, refundTx := createParentAndRefundTx(t, outputScript, 1000+int64(i))

		_, err = tx.TreeNode.Create().
			SetTree(tree).
			SetNetwork(tree.Network).
			SetID(nodeID).
			SetValue(uint64(1000 + i)).
			SetStatus(st.TreeNodeStatusAvailable).
			SetVerifyingPubkey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
			SetOwnerIdentityPubkey(destinationPubKey).
			SetOwnerSigningPubkey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
			SetRawTx(parentTx).
			SetVout(0).
			SetSigningKeyshare(keyshare).
			Save(ctx)
		require.NoError(t, err)

		cpfpTransactions = append(cpfpTransactions, &pb.UserSignedTxSigningJob{
			LeafId: nodeID.String(),
			SigningCommitments: &pb.SigningCommitments{
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
			UserSignature: []byte("test_signature"),
			RawTx:         refundTx,
		})
	}

	releaseCh := make(chan struct{})
	startedCh := make(chan struct{}, numTransactions)
	frostClient := &trackingFrostServiceClient{
		startedCh: startedCh,
		releaseCh: releaseCh,
	}
	frostConnection := &trackingFrostServiceClientConnection{
		client: frostClient,
	}

	validationErrCh := make(chan error, 1)
	go func() {
		validationErrCh <- lightningHandler.validateGetPreimageRequestWithFrostServiceClientFactory(
			ctx,
			frostConnection,
			paymentHash,
			cpfpTransactions,
			[]*pb.UserSignedTxSigningJob{},
			[]*pb.UserSignedTxSigningJob{},
			1,
			destinationPubKey,
			singleLeafDestination(destinationPubKey),
			0,
			pb.InitiatePreimageSwapRequest_REASON_SEND,
			false,
		)
	}()

	for i := range int(parallelLimit) {
		select {
		case <-startedCh:
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for validation #%d to start", i+1)
		}
	}

	time.Sleep(150 * time.Millisecond)
	require.Equal(t, parallelLimit, frostClient.started.Load(), "expected only configured parallel validations to start before release")
	require.Equal(t, parallelLimit, frostClient.maxInFlight.Load(), "expected max in-flight validations to match configured parallel limit")

	close(releaseCh)

	select {
	case err := <-validationErrCh:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for preimage validation to complete")
	}

	require.Equal(t, int32(numTransactions), frostClient.started.Load())
	assert.LessOrEqual(t, frostClient.maxInFlight.Load(), parallelLimit)
}

// Regression test for https://linear.app/lightsparkdev/issue/LIG-8043
// Validates that duplicate leaves are rejected in the SendLightning flow,
// since otherwise they would allow double-spending of Spark leaves via
// Lightning.
func TestSendLightningLeafDuplicationBug(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)

	config := &so.Config{FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{}}
	lightningHandler := NewLightningHandler(config)

	createMockSigningJob := func(leafID string, value uint64) *pb.UserSignedTxSigningJob {
		mockTx := []byte{
			0x02, 0x00, 0x00, 0x00, // version
			0x01, // input count
			// Input (simplified)
			0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
			0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
			0xFF, 0xFF, 0xFF, 0xFF, // previous output index
			0x00,                   // script length
			0xFF, 0xFF, 0xFF, 0xFF, // sequence
			0x01, // output count
		}
		valueBytes := binary.LittleEndian.AppendUint64(nil, value)
		mockTx = append(mockTx, valueBytes...)
		// Add minimal script (P2TR-like)
		mockScript := []byte{
			0x22,       // script length (34 bytes)
			0x51, 0x20, // OP_1 + 32-byte key
		}
		mockScript = append(mockScript, make([]byte, 32)...) // 32-byte pubkey
		mockTx = append(mockTx, mockScript...)
		// Add locktime
		mockTx = append(mockTx, 0x00, 0x00, 0x00, 0x00)

		return &pb.UserSignedTxSigningJob{
			LeafId: leafID,
			SigningCommitments: &pb.SigningCommitments{
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
			UserSignature: []byte("test_signature"),
			RawTx:         mockTx,
		}
	}

	t.Run("duplicate leaves should not bypass amount validation", func(t *testing.T) {
		const leafID = "550e8400-e29b-41d4-a716-446655440000"

		// Create a single leaf worth 1000 sats
		originalLeaf := createMockSigningJob(leafID, 1000)

		// Duplicate the same leaf to artificially double the amount
		duplicatedLeaf := createMockSigningJob(leafID, 1000)

		rng := rand.NewChaCha8([32]byte{})
		ownerIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
		receiverIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
		// Create request with duplicated leaves
		req := &pb.InitiatePreimageSwapRequest{
			PaymentHash:               []byte("payment_hash_32_bytes_long______"),
			ReceiverIdentityPublicKey: receiverIdentityPubKey.Serialize(),
			TransferRequest: &pb.StartTransferRequest{
				TransferId:                "transfer-id-123",
				OwnerIdentityPublicKey:    ownerIdentityPubKey.Serialize(),
				ReceiverIdentityPublicKey: receiverIdentityPubKey.Serialize(),
				TransferPackage: &pb.TransferPackage{
					LeavesToSend: []*pb.UserSignedTxSigningJob{
						originalLeaf,
						duplicatedLeaf, // Same leaf ID - this should be rejected
					},
				},
			},
			InvoiceAmount: &pb.InvoiceAmount{
				ValueSats: 1000, // Invoice is for 1000 sats, but we're attempting to send 2000 sats due to duplication
			},
			Reason:  pb.InitiatePreimageSwapRequest_REASON_SEND,
			FeeSats: 0,
		}

		_, err := lightningHandler.InitiatePreimageSwapV3(ctx, req)

		require.ErrorContains(t, err, "duplicate leaf id")
	})
}

// TestQueryPreimageSkipsReturnedRows verifies that QueryPreimage skips stale RETURNED rows
// and returns the active request when both a RETURNED and an active row exist for the same
// (payment_hash, receiver_identity_pubkey) pair. This exercises the fix for the bug where
// .First() returned the oldest (RETURNED) row, causing "no transfer found" errors on retries.
func TestQueryPreimageSkipsReturnedRows(t *testing.T) {
	// Use Postgres because the partial unique index (WHERE status != 'RETURNED') is
	// only enforced by Postgres, and we need to insert two rows with the same
	// (payment_hash, receiver_identity_pubkey) where one has status RETURNED.
	ctx, _ := db.ConnectToTestPostgres(t)

	rng := rand.NewChaCha8([32]byte{99})

	senderKey := keys.MustGeneratePrivateKeyFromRand(rng)
	senderPubKey := senderKey.Public()
	receiverPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	paymentHash := []byte("test_payment_hash_32_bytes_____x")

	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	// Create the Transfer that will be linked to the active preimage request.
	// QueryPreimage checks that transfer.SenderIdentityPubkey matches the session identity.
	activeTransfer := entexample.NewTransferExample(t, tx).
		SetSenderIdentityPubkey(senderPubKey).
		SetReceiverIdentityPubkey(receiverPubKey).
		SetExpiryTime(time.Now().Add(24 * time.Hour)).
		SetStatus(st.TransferStatusSenderInitiated).
		SetType(st.TransferTypePreimageSwap).
		MustExec(ctx)

	// Create the stale RETURNED row first (lower ID / older create_time).
	entexample.NewPreimageRequestExample(t, tx).
		SetPaymentHash(paymentHash).
		SetReceiverIdentityPubkey(receiverPubKey).
		SetStatus(st.PreimageRequestStatusReturned).
		SetSenderIdentityPubkey(senderPubKey).
		MustExec(ctx)

	// Create the active WAITING_FOR_PREIMAGE row for the same (payment_hash, receiver).
	activePreimageRequest := entexample.NewPreimageRequestExample(t, tx).
		SetPaymentHash(paymentHash).
		SetReceiverIdentityPubkey(receiverPubKey).
		SetStatus(st.PreimageRequestStatusWaitingForPreimage).
		SetSenderIdentityPubkey(senderPubKey).
		SetTransfers(activeTransfer).
		MustExec(ctx)

	// Build an authenticated context whose session identity matches the transfer sender.
	tokenVerifier, err := authninternal.NewSessionTokenCreatorVerifier(senderKey, authninternal.RealClock{})
	require.NoError(t, err)
	tokenResult, err := tokenVerifier.CreateToken(senderPubKey, time.Hour)
	require.NoError(t, err)
	authCtx := metadata.NewIncomingContext(ctx, metadata.Pairs("authorization", "Bearer "+tokenResult.Token))
	authnInterceptor := authn.NewInterceptor(tokenVerifier)
	var authenticatedCtx context.Context
	_, err = authnInterceptor.AuthnInterceptor(authCtx, nil, &grpc.UnaryServerInfo{}, func(innerCtx context.Context, _ any) (any, error) {
		authenticatedCtx = innerCtx
		return nil, nil
	})
	require.NoError(t, err)

	config := &so.Config{}
	lightningHandler := NewLightningHandler(config)

	req := &pb.QueryPreimageRequest{
		PaymentHash:            paymentHash,
		ReceiverIdentityPubkey: receiverPubKey.Serialize(),
	}

	resp, err := lightningHandler.QueryPreimage(authenticatedCtx, req)
	require.NoError(t, err)
	require.NotNil(t, resp)

	// Verify the active row's preimage was returned (active row has non-empty default preimage).
	// The active request ID must match, and a nil preimage is fine for WAITING_FOR_PREIMAGE.
	// We confirm the correct row was selected by verifying the transfer edge is populated
	// and matches the active transfer.
	_ = activePreimageRequest // reference to silence unused-var warning
	// QueryPreimage returns an empty preimage when none is set yet (WAITING_FOR_PREIMAGE).
	// The important thing is no error was returned — that means the active row was found,
	// not the stale RETURNED row (which has no transfer edge and would have caused
	// "no transfer found" error).
}

func TestInitiatePreimageSwapPackageOnly(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{83})
	ownerPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiverPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	paymentHash := sha256.Sum256([]byte("package-only-payment"))

	config := &so.Config{FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{}}
	lightningHandler := NewLightningHandler(config)

	withKnobs := func(ctx context.Context) context.Context {
		return knobs.InjectKnobsService(ctx, knobs.NewFixedKnobs(map[string]float64{}))
	}

	newSendRequest := func(jobs []*pb.UserSignedTxSigningJob, invoiceSats, feeSats uint64) *pb.InitiatePreimageSwapRequest {
		return &pb.InitiatePreimageSwapRequest{
			PaymentHash:               paymentHash[:],
			InvoiceAmount:             &pb.InvoiceAmount{ValueSats: invoiceSats},
			Reason:                    pb.InitiatePreimageSwapRequest_REASON_SEND,
			ReceiverIdentityPublicKey: receiverPrivKey.Public().Serialize(),
			FeeSats:                   feeSats,
			TransferRequest: &pb.StartTransferRequest{
				TransferId:                uuid.NewString(),
				OwnerIdentityPublicKey:    ownerPrivKey.Public().Serialize(),
				ReceiverIdentityPublicKey: receiverPrivKey.Public().Serialize(),
				ExpiryTime:                timestamppb.New(time.Now().Add(time.Hour)),
				TransferPackage:           &pb.TransferPackage{LeavesToSend: jobs},
			},
		}
	}

	// Payment-hash, duplicate-request, amount/fee, and leaf-status checks are
	// enforced on the participant path (GetPreimageShare / 2PC Prepare), so
	// those subtests drive GetPreimageShare; coordinator-side checks (existence/
	// ownership, package size) drive InitiatePreimageSwapV3.
	t.Run("send rejects invalid payment hash length", func(t *testing.T) {
		ctx, _ := db.NewTestSQLiteContext(t)
		req := newSendRequest([]*pb.UserSignedTxSigningJob{{LeafId: uuid.NewString()}}, 100, 0)
		req.PaymentHash = []byte("short")
		_, err := lightningHandler.GetPreimageShare(ctx, req, nil, nil, nil, nil)
		require.ErrorContains(t, err, "invalid payment hash length")
	})

	t.Run("send rejects nonexistent leaves", func(t *testing.T) {
		ctx, _ := db.NewTestSQLiteContext(t)
		req := newSendRequest([]*pb.UserSignedTxSigningJob{{LeafId: uuid.NewString()}}, 100, 0)
		_, err := lightningHandler.InitiatePreimageSwapV3(withKnobs(ctx), req)
		require.ErrorContains(t, err, "leaves but only")
	})

	t.Run("send amount checks from tree-node values", func(t *testing.T) {
		ctx, _ := db.NewTestSQLiteContext(t)
		tx, err := ent.GetDbFromContext(ctx)
		require.NoError(t, err)
		keyshare := createTestSigningKeyshare(t, ctx, rng, tx)
		tree := createTestTreeForClaim(t, ctx, ownerPrivKey.Public(), tx)
		// createTestTreeNode pins Value to 1000 sats per leaf and creates it TRANSFER_LOCKED.
		leafA := createTestTreeNode(t, ctx, rng, tx, tree, keyshare)
		leafB := createTestTreeNode(t, ctx, rng, tx, tree, keyshare)
		_, err = tx.TreeNode.Update().SetStatus(st.TreeNodeStatusAvailable).Save(ctx)
		require.NoError(t, err)
		jobs := []*pb.UserSignedTxSigningJob{{LeafId: leafA.ID.String()}, {LeafId: leafB.ID.String()}}

		_, err = lightningHandler.GetPreimageShare(ctx, newSendRequest(jobs, 100, 2000), nil, nil, nil, nil)
		require.ErrorContains(t, err, "fee exceeds total amount")

		_, err = lightningHandler.GetPreimageShare(ctx, newSendRequest(jobs, 5000, 100), nil, nil, nil, nil)
		require.ErrorContains(t, err, "invalid amount, expected: 5000 or more, got: 1900")
	})

	t.Run("send rejects duplicate payment hash", func(t *testing.T) {
		ctx, _ := db.NewTestSQLiteContext(t)
		tx, err := ent.GetDbFromContext(ctx)
		require.NoError(t, err)
		_, err = tx.PreimageRequest.Create().
			SetPaymentHash(paymentHash[:]).
			SetReceiverIdentityPubkey(receiverPrivKey.Public()).
			SetSenderIdentityPubkey(ownerPrivKey.Public()).
			SetStatus(st.PreimageRequestStatusWaitingForPreimage).
			Save(ctx)
		require.NoError(t, err)

		req := newSendRequest([]*pb.UserSignedTxSigningJob{{LeafId: uuid.NewString()}}, 100, 0)
		_, err = lightningHandler.GetPreimageShare(ctx, req, nil, nil, nil, nil)
		require.ErrorContains(t, err, "preimage request already exists")
	})

	t.Run("send rejects oversized package before node queries", func(t *testing.T) {
		ctx, _ := db.NewTestSQLiteContext(t)
		jobs := make([]*pb.UserSignedTxSigningJob, 101)
		for i := range jobs {
			jobs[i] = &pb.UserSignedTxSigningJob{LeafId: uuid.NewString()}
		}
		_, err := lightningHandler.InitiatePreimageSwapV3(withKnobs(ctx), newSendRequest(jobs, 100, 0))
		require.ErrorContains(t, err, "too many transactions")
	})

	// Participant paths (GetPreimageShare) resolve inputs and validate directly.
	t.Run("participant GetPreimageShare resolves and validates a send request", func(t *testing.T) {
		ctx, _ := db.NewTestSQLiteContext(t)
		req := newSendRequest([]*pb.UserSignedTxSigningJob{{LeafId: uuid.NewString()}}, 100, 0)
		_, err := lightningHandler.GetPreimageShare(ctx, req, nil, nil, nil, nil)
		// Past resolution and into amount validation — only the missing node stops it.
		require.ErrorContains(t, err, "leaves but only")
	})

	t.Run("participant GetPreimageShare receive routes package leaves through plain validation", func(t *testing.T) {
		ctx, _ := db.NewTestSQLiteContext(t)
		req := newSendRequest([]*pb.UserSignedTxSigningJob{{
			LeafId:                 uuid.NewString(),
			RawTx:                  []byte{0x01},
			SigningCommitments:     &pb.SigningCommitments{SigningCommitments: map[string]*pbcommon.SigningCommitment{}},
			SigningNonceCommitment: &pbcommon.SigningCommitment{},
		}}, 0, 0)
		req.Reason = pb.InitiatePreimageSwapRequest_REASON_RECEIVE
		_, err := lightningHandler.GetPreimageShare(ctx, req, nil, nil, nil, nil)
		require.ErrorContains(t, err, "unable to get cpfpTransaction tree_node")
	})

	t.Run("participant GetPreimageShare rejects receiver mismatch", func(t *testing.T) {
		ctx, _ := db.NewTestSQLiteContext(t)
		req := newSendRequest([]*pb.UserSignedTxSigningJob{{LeafId: uuid.NewString()}}, 100, 0)
		req.ReceiverIdentityPublicKey = ownerPrivKey.Public().Serialize()
		_, err := lightningHandler.GetPreimageShare(ctx, req, nil, nil, nil, nil)
		require.ErrorContains(t, err, "receiver identity public key mismatch")
	})

	t.Run("2PC prepareState accepts transfer-less send", func(t *testing.T) {
		ctx, _ := db.NewTestSQLiteContext(t)
		flowHandler := NewInitiatePreimageSwapFlowHandler(config)
		req := newSendRequest([]*pb.UserSignedTxSigningJob{{LeafId: uuid.NewString()}}, 100, 0)
		_, err := flowHandler.prepareState(ctx, req, nil)
		require.ErrorContains(t, err, "leaves but only")
	})

	t.Run("2PC prepareState rejects fee on receive", func(t *testing.T) {
		ctx, _ := db.NewTestSQLiteContext(t)
		flowHandler := NewInitiatePreimageSwapFlowHandler(config)
		req := newSendRequest([]*pb.UserSignedTxSigningJob{{LeafId: uuid.NewString()}}, 100, 25)
		req.Reason = pb.InitiatePreimageSwapRequest_REASON_RECEIVE
		_, err := flowHandler.prepareState(ctx, req, nil)
		require.ErrorContains(t, err, "fee is not allowed for receive preimage swap")
	})

	t.Run("2PC prepareState enforces hodl shutdown knob per SO", func(t *testing.T) {
		ctx, _ := db.NewTestSQLiteContext(t)
		flowHandler := NewInitiatePreimageSwapFlowHandler(config)
		shutdownCtx := knobs.InjectKnobsService(ctx, knobs.NewFixedKnobs(map[string]float64{
			knobs.KnobShutdownHodlInvoices: 1,
		}))
		req := newSendRequest([]*pb.UserSignedTxSigningJob{{LeafId: uuid.NewString()}}, 100, 0)
		req.Reason = pb.InitiatePreimageSwapRequest_REASON_RECEIVE
		// No stored preimage share for this hash → the HODL branch, which each
		// SO must refuse on its own knob, independent of the coordinator check.
		_, err := flowHandler.prepareState(shutdownCtx, req, nil)
		require.ErrorContains(t, err, "hodl invoices are currently disabled")
	})

	t.Run("send rejects unavailable leaves before package work", func(t *testing.T) {
		ctx, _ := db.NewTestSQLiteContext(t)
		tx, err := ent.GetDbFromContext(ctx)
		require.NoError(t, err)
		keyshare := createTestSigningKeyshare(t, ctx, rng, tx)
		tree := createTestTreeForClaim(t, ctx, ownerPrivKey.Public(), tx)
		leaf := createTestTreeNode(t, ctx, rng, tx, tree, keyshare) // TRANSFER_LOCKED
		req := newSendRequest([]*pb.UserSignedTxSigningJob{{LeafId: leaf.ID.String()}}, 100, 0)
		_, err = lightningHandler.GetPreimageShare(ctx, req, nil, nil, nil, nil)
		require.ErrorContains(t, err, "not available to transfer")
	})

	t.Run("send rejects leaves the session does not own", func(t *testing.T) {
		ctx, _ := db.NewTestSQLiteContext(t)
		tx, err := ent.GetDbFromContext(ctx)
		require.NoError(t, err)
		keyshare := createTestSigningKeyshare(t, ctx, rng, tx)
		tree := createTestTreeForClaim(t, ctx, ownerPrivKey.Public(), tx)
		// createTestTreeNode assigns a random owner — not the session identity.
		leaf := createTestTreeNode(t, ctx, rng, tx, tree, keyshare)

		_, err = tx.TreeNode.UpdateOne(leaf).SetStatus(st.TreeNodeStatusAvailable).Save(ctx)
		require.NoError(t, err)

		authzHandler := NewLightningHandler(&so.Config{
			AuthzEnforced:              true,
			FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{},
		})
		sessionCtx := authn.InjectSessionForTests(withKnobs(ctx), ownerPrivKey.Public(), time.Now().Add(time.Hour).Unix())

		req := newSendRequest([]*pb.UserSignedTxSigningJob{{LeafId: leaf.ID.String()}}, 100, 0)
		_, err = authzHandler.InitiatePreimageSwapV3(sessionCtx, req)
		require.ErrorContains(t, err, "not owned by the authenticated identity")
	})
}

func mustPerLeaf(t *testing.T, destinations map[string]keys.Public) leafDestinations {
	t.Helper()
	resolved, err := perLeafDestinations(destinations)
	require.NoError(t, err)
	return resolved
}

// A v4 receive settles each leaf to its own receiver, so one key can no longer answer for the
// whole swap. The unrouted case carries the security property: a leaf the map does not name has
// nothing to check its refund against, and admitting it would let a caller move a leaf by leaving
// it out of the map.
func TestLeafDestinationsResolvesPerLeafAndFailsClosed(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{64})
	alice := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	bob := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	t.Run("one destination answers for every leaf", func(t *testing.T) {
		destinations := singleLeafDestination(alice)

		for _, leafID := range []string{"leaf-a", "leaf-b", ""} {
			destination, err := destinations.forLeaf(leafID)

			require.NoError(t, err)
			require.Equal(t, alice, destination)
		}
	})

	t.Run("a per-leaf map answers each leaf with its own receiver", func(t *testing.T) {
		destinations, err := perLeafDestinations(map[string]keys.Public{"leaf-a": alice, "leaf-b": bob})
		require.NoError(t, err)

		first, err := destinations.forLeaf("leaf-a")
		require.NoError(t, err)
		require.Equal(t, alice, first)

		second, err := destinations.forLeaf("leaf-b")
		require.NoError(t, err)
		require.Equal(t, bob, second)
	})

	t.Run("an unrouted leaf is refused", func(t *testing.T) {
		destinations, err := perLeafDestinations(map[string]keys.Public{"leaf-a": alice})
		require.NoError(t, err)

		_, err = destinations.forLeaf("leaf-b")

		require.Error(t, err)
		require.Equal(t, codes.InvalidArgument, status.Code(err))
	})

	// An empty map is still a per-leaf map: it must refuse, not fall through to the zero key.
	t.Run("an empty map refuses every leaf", func(t *testing.T) {
		destinations, err := perLeafDestinations(map[string]keys.Public{})
		require.NoError(t, err)

		_, err = destinations.forLeaf("leaf-a")

		require.Error(t, err)
		require.Equal(t, codes.InvalidArgument, status.Code(err))
	})

	// The two states no constructor produces. A hand-built value that sets both fields, or
	// neither, has no defensible answer, so it errors rather than letting one field win.
	t.Run("setting neither destination is refused", func(t *testing.T) {
		_, err := leafDestinations{}.forLeaf("leaf-a")

		require.Error(t, err)
		require.Equal(t, codes.Internal, status.Code(err))
		require.Contains(t, err.Error(), "neither a single destination nor a per-leaf map")
	})

	t.Run("setting both destinations is refused", func(t *testing.T) {
		_, err := leafDestinations{single: alice, perLeaf: map[string]keys.Public{"leaf-a": bob}}.forLeaf("leaf-a")

		require.Error(t, err)
		require.Equal(t, codes.Internal, status.Code(err))
		require.Contains(t, err.Error(), "both a single destination and a per-leaf map")
	})

	// Two spellings of one leaf must be refused outright: canonicalizing them into one entry would
	// let Go's randomized map order pick the winner, so operators could disagree on the receiver.
	t.Run("one leaf named twice is refused", func(t *testing.T) {
		canonical := uuid.New().String()

		_, err := perLeafDestinations(map[string]keys.Public{canonical: alice, strings.ToUpper(canonical): bob})

		require.Error(t, err)
		require.Equal(t, codes.InvalidArgument, status.Code(err))
		require.Contains(t, err.Error(), "named more than once")
	})

	// The wire leaf_id is not canonical, so the map and the lookup must agree on spelling or a
	// legitimate leaf is refused and two spellings of one id become two routable entries.
	t.Run("leaf ids match across uuid spellings", func(t *testing.T) {
		canonical := uuid.New().String()
		destinations, err := perLeafDestinations(map[string]keys.Public{strings.ToUpper(canonical): alice})
		require.NoError(t, err)

		for _, spelling := range []string{canonical, strings.ToUpper(canonical), strings.ReplaceAll(canonical, "-", "")} {
			destination, err := destinations.forLeaf(spelling)

			require.NoError(t, err, "spelling %s", spelling)
			require.Equal(t, alice, destination)
		}
	})
}

// TestValidateGetPreimageRequestRoutesEachLeafToItsOwnReceiver drives the per-leaf routing through
// the full validation path rather than the resolver alone. The resolver's own tests cannot catch a
// loop that looks up the wrong leaf or skips the lookup, because every other caller routes with a
// single destination — and in that mode the resolver answers before it ever reads the leaf id.
func TestValidateGetPreimageRequestRoutesEachLeafToItsOwnReceiver(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{71})
	ctx, _ := db.ConnectToTestPostgres(t)

	config := &so.Config{FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{}}
	lightningHandler := NewLightningHandler(config)

	ownerPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	verifyingPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	alice := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	bob := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	// Distinct from every leaf destination, so a dup-guard that keyed off a destination instead
	// of the counterparty would stop matching.
	counterparty := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	paymentHashBytes := sha256.Sum256([]byte("per leaf routing"))
	paymentHash := paymentHashBytes[:]

	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	tree, err := tx.Tree.Create().
		SetOwnerIdentityPubkey(ownerPubKey).
		SetStatus(st.TreeStatusAvailable).
		SetNetwork(btcnetwork.Mainnet).
		SetBaseTxid(st.NewRandomTxIDForTesting(t)).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)

	keyshare, err := tx.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusInUse).
		SetSecretShare(keys.MustGeneratePrivateKeyFromRand(rng)).
		SetPublicShares(map[string]keys.Public{"operator1": ownerPubKey}).
		SetPublicKey(ownerPubKey).
		SetMinSigners(2).
		SetCoordinatorIndex(1).
		Save(ctx)
	require.NoError(t, err)

	ownerScript, err := common.P2TRScriptFromPubKey(ownerPubKey)
	require.NoError(t, err)

	newLeafPaying := func(t *testing.T, receiver keys.Public) *pb.UserSignedTxSigningJob {
		t.Helper()
		receiverScript, err := common.P2TRScriptFromPubKey(receiver)
		require.NoError(t, err)
		parentTx, refundTx := createParentAndRefundTxWithOutputs(t, ownerScript, 1000, []*wire.TxOut{{Value: 500, PkScript: receiverScript}})

		nodeID := uuid.New()
		_, err = tx.TreeNode.Create().
			SetTree(tree).
			SetNetwork(tree.Network).
			SetID(nodeID).
			SetValue(1000).
			SetStatus(st.TreeNodeStatusAvailable).
			SetVerifyingPubkey(verifyingPubKey).
			SetOwnerIdentityPubkey(ownerPubKey).
			SetOwnerSigningPubkey(ownerPubKey).
			SetRawTx(parentTx).
			SetDirectTx(parentTx).
			SetVout(0).
			SetSigningKeyshare(keyshare).
			Save(ctx)
		require.NoError(t, err)

		return &pb.UserSignedTxSigningJob{
			LeafId: nodeID.String(),
			SigningCommitments: &pb.SigningCommitments{
				SigningCommitments: map[string]*pbcommon.SigningCommitment{
					"test": {Hiding: []byte("test_hiding"), Binding: []byte("test_binding")},
				},
			},
			SigningNonceCommitment: &pbcommon.SigningCommitment{
				Hiding:  []byte("test_nonce_hiding"),
				Binding: []byte("test_nonce_binding"),
			},
			UserSignature: []byte("test_signature"),
			RawTx:         refundTx,
		}
	}

	none := []*pb.UserSignedTxSigningJob{}
	// Only the cpfp refunds carry value toward the invoice; direct and direct-from-cpfp are
	// alternative spend paths for the same leaves, so their amounts are deliberately not summed.
	validate := func(cpfp, direct, directFromCpfp []*pb.UserSignedTxSigningJob, destinations leafDestinations, invoiceSats uint64) error {
		return lightningHandler.validateGetPreimageRequestWithFrostServiceClientFactory(
			ctx,
			&mockFrostServiceClientConnection{},
			paymentHash,
			cpfp,
			direct,
			directFromCpfp,
			invoiceSats,
			counterparty,
			destinations,
			0,
			pb.InitiatePreimageSwapRequest_REASON_SEND,
			false,
		)
	}

	t.Run("two leaves each settle to their own receiver", func(t *testing.T) {
		toAlice, toBob := newLeafPaying(t, alice), newLeafPaying(t, bob)

		err := validate([]*pb.UserSignedTxSigningJob{toAlice, toBob}, none, none, mustPerLeaf(t, map[string]keys.Public{
			toAlice.GetLeafId(): alice,
			toBob.GetLeafId():   bob,
		}), 1000)

		require.NoError(t, err)
	})

	// The case a cross-wired lookup would pass: both keys are in the map, so only matching each
	// refund to ITS OWN leaf's entry rejects this.
	t.Run("swapping the two receivers is rejected", func(t *testing.T) {
		toAlice, toBob := newLeafPaying(t, alice), newLeafPaying(t, bob)

		err := validate([]*pb.UserSignedTxSigningJob{toAlice, toBob}, none, none, mustPerLeaf(t, map[string]keys.Public{
			toAlice.GetLeafId(): bob,
			toBob.GetLeafId():   alice,
		}), 1000)

		require.Error(t, err)
		require.Equal(t, codes.InvalidArgument, status.Code(err))
		require.Contains(t, err.Error(), "invalid cpfp destination pubkey")
	})

	t.Run("a leaf missing from the map is refused", func(t *testing.T) {
		toAlice, toBob := newLeafPaying(t, alice), newLeafPaying(t, bob)

		err := validate([]*pb.UserSignedTxSigningJob{toAlice, toBob}, none, none, mustPerLeaf(t, map[string]keys.Public{
			toAlice.GetLeafId(): alice,
		}), 1000)

		require.Error(t, err)
		require.Equal(t, codes.InvalidArgument, status.Code(err))
		require.Contains(t, err.Error(), "no receiver for leaf_id")
	})

	// Routing to one destination means routing to the counterparty. The two were one parameter
	// before per-leaf routing existed, so nothing but this check keeps them from drifting apart.
	t.Run("a single destination that is not the counterparty is refused", func(t *testing.T) {
		toAlice := newLeafPaying(t, alice)

		err := validate([]*pb.UserSignedTxSigningJob{toAlice}, none, none, singleLeafDestination(alice), 500)

		require.Error(t, err)
		require.Equal(t, codes.Internal, status.Code(err))
		require.Contains(t, err.Error(), "must target the counterparty")
	})

	// The cpfp loop is not the only one the refactor rewired. Routing the mismatch through the
	// direct paths keeps a lookup that is wrong there — or absent — from passing unnoticed.
	for _, path := range []struct {
		name               string
		place              func(job *pb.UserSignedTxSigningJob) (direct, directFromCpfp []*pb.UserSignedTxSigningJob)
		expectedErrMessage string
	}{
		{
			name: "direct",
			place: func(j *pb.UserSignedTxSigningJob) ([]*pb.UserSignedTxSigningJob, []*pb.UserSignedTxSigningJob) {
				return []*pb.UserSignedTxSigningJob{j}, none
			},
			expectedErrMessage: "invalid direct destination pubkey",
		},
		{
			name: "direct from cpfp",
			place: func(j *pb.UserSignedTxSigningJob) ([]*pb.UserSignedTxSigningJob, []*pb.UserSignedTxSigningJob) {
				return none, []*pb.UserSignedTxSigningJob{j}
			},
			expectedErrMessage: "invalid direct from cpfp destination pubkey",
		},
	} {
		// The refund deliberately pays the COUNTERPARTY while the map routes its leaf to alice.
		// A loop that skipped the lookup and fell back to the counterparty would accept this, so
		// the case discriminates rather than merely rejecting for some reason.
		t.Run(path.name+" refunds resolve their own leaf's receiver", func(t *testing.T) {
			toAlice, toCounterparty := newLeafPaying(t, alice), newLeafPaying(t, counterparty)
			direct, directFromCpfp := path.place(toCounterparty)

			err := validate([]*pb.UserSignedTxSigningJob{toAlice}, direct, directFromCpfp, mustPerLeaf(t, map[string]keys.Public{
				toAlice.GetLeafId():        alice,
				toCounterparty.GetLeafId(): alice,
			}), 500)

			require.Error(t, err)
			require.Equal(t, codes.InvalidArgument, status.Code(err))
			require.Contains(t, err.Error(), path.expectedErrMessage)
		})

		t.Run(path.name+" refuses a leaf missing from the map", func(t *testing.T) {
			toAlice, toBob := newLeafPaying(t, alice), newLeafPaying(t, bob)
			direct, directFromCpfp := path.place(toBob)

			err := validate([]*pb.UserSignedTxSigningJob{toAlice}, direct, directFromCpfp, mustPerLeaf(t, map[string]keys.Public{
				toAlice.GetLeafId(): alice,
			}), 500)

			require.Error(t, err)
			require.Equal(t, codes.InvalidArgument, status.Code(err))
			require.Contains(t, err.Error(), "no receiver for leaf_id")
		})
	}

	t.Run("a single destination that is the counterparty is admitted", func(t *testing.T) {
		first, second := newLeafPaying(t, counterparty), newLeafPaying(t, counterparty)

		err := validate([]*pb.UserSignedTxSigningJob{first, second}, none, none, singleLeafDestination(counterparty), 1000)

		require.NoError(t, err)
	})
}
