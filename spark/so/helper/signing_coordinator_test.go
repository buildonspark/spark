package helper_test

import (
	"bytes"
	"context"
	"errors"
	"math"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/lightsparkdev/spark/common/keys"
	"github.com/stretchr/testify/require"

	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pbfrost "github.com/lightsparkdev/spark/proto/frost"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	pbspark "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/helper"
	"github.com/lightsparkdev/spark/so/objects"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
)

var pubKey = keys.MustGeneratePrivateKeyFromRand(rand.NewChaCha8([32]byte{})).Public()

func mockTxBuf(t *testing.T, values []int64) []byte {
	// A minimal valid Bitcoin transaction
	tx := wire.NewMsgTx(2)
	tx.AddTxIn(&wire.TxIn{})
	for _, v := range values {
		tx.AddTxOut(&wire.TxOut{Value: v, PkScript: []byte("test-pkscript")})
	}
	var txBuf []byte
	{
		buf := new(bytes.Buffer)
		err := tx.Serialize(buf)
		if err != nil {
			t.Fatalf("failed to serialize tx: %v", err)
		}
		txBuf = buf.Bytes()
	}
	return txBuf
}

func runWithRawTx(keysharePub keys.Public, protoPub keys.Public, rawTx []byte, commitment *pbcommon.SigningCommitment, prevOutputValue int64) (*helper.SigningJob, *wire.MsgTx, error) {
	keyshare := &ent.SigningKeyshare{
		ID:        uuid.New(),
		PublicKey: keysharePub,
	}
	proto := &pbspark.SigningJob{
		SigningPublicKey:       protoPub.Serialize(),
		RawTx:                  rawTx,
		SigningNonceCommitment: commitment,
	}

	prevOutput := &wire.TxOut{
		Value:    prevOutputValue,
		PkScript: []byte("test-pkscript"),
	}

	return helper.NewSigningJob(keyshare, proto, prevOutput)
}

func runWithValues(t *testing.T, prevOutputValue int64, values []int64) (*helper.SigningJob, *wire.MsgTx, error) {
	rawTx := mockTxBuf(t, values)
	commitment := &pbcommon.SigningCommitment{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()}
	return runWithRawTx(pubKey, pubKey, rawTx, commitment, prevOutputValue)
}

func expectError(t *testing.T, prevOutputValue int64, values []int64, expectedError error) {
	_, _, err := runWithValues(t, prevOutputValue, values)

	if !errors.Is(err, expectedError) {
		t.Fatalf("err should be %v. Instead, got %v", expectedError, err)
	}
}

func TestNewSigningJob(t *testing.T) {
	tests := []struct {
		name            string
		prevOutputValue int64
		values          []int64
	}{
		{"leaf equal to prev output", 1000000, []int64{1000000}},
		{"sum of leaves equal to prev output", 1000000, []int64{500000, 500000}},
		// TODO: Is it actually correct that they should be able to sum up to a
		// value less than the previous output?
		{"3 leaves less than prev output", 1000000, []int64{200000, 300000, 400000}},
		{"no leaves", 1000000, []int64{}},
		{"1000 leaves", 1000000, slices.Repeat([]int64{1}, 1000)},
		{"max int64 leaf", math.MaxInt64, []int64{math.MaxInt64}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := runWithValues(t, test.prevOutputValue, test.values); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNewSigningJob_BadLeafValues(t *testing.T) {
	tests := []struct {
		name            string
		prevOutputValue int64
		values          []int64
		expectedError   error
	}{
		{"negative leaf", 1000000, []int64{-1000000}, helper.ErrNegativeOutputValue},
		{"negative leaf in sum", 1000000, []int64{500000, -1000000}, helper.ErrNegativeOutputValue},
		{"sum greater than prev output", 1000000, []int64{1000001}, helper.ErrTotalOutputValueGreaterThanPrevOutputValue},
		{"sum greater than prev output in sum", 1000000, []int64{500000, 500001}, helper.ErrTotalOutputValueGreaterThanPrevOutputValue},
		{"sum greater than max int64", 1000000, []int64{1, math.MaxInt64}, helper.ErrTotalOutputValueGreaterThanMaxInt64},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			expectError(t, test.prevOutputValue, test.values, test.expectedError)
		})
	}
}

func TestNewSigningJob_InvalidInputs(t *testing.T) {
	tx := wire.NewMsgTx(2)
	tx.AddTxIn(&wire.TxIn{})
	tx.AddTxOut(&wire.TxOut{Value: 1000000, PkScript: []byte("test-pkscript")})
	buf := new(bytes.Buffer)
	require.NoError(t, tx.Serialize(buf))
	goodTx := buf.Bytes()

	tests := []struct {
		name          string
		keyshare      keys.Public
		protoPub      keys.Public
		rawTx         []byte
		commit        *pbcommon.SigningCommitment
		expectedError string
	}{
		{
			name:          "malformed raw tx",
			keyshare:      pubKey,
			protoPub:      pubKey,
			rawTx:         []byte{0x00, 0x01, 0x02},
			commit:        &pbcommon.SigningCommitment{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
			expectedError: "unexpected EOF",
		},
		{
			name:          "malformed commitment",
			keyshare:      pubKey,
			protoPub:      pubKey,
			rawTx:         goodTx,
			commit:        &pbcommon.SigningCommitment{Binding: pubKey.Serialize()[:30], Hiding: pubKey.Serialize()},
			expectedError: "invalid nonce commitment length",
		},
		{
			name:          "nil commitment",
			keyshare:      pubKey,
			protoPub:      pubKey,
			rawTx:         goodTx,
			commit:        nil,
			expectedError: "nil proto",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := runWithRawTx(tc.keyshare, tc.protoPub, tc.rawTx, tc.commit, 1000000)
			if err == nil {
				t.Errorf("expected error %s, got nil", tc.expectedError)
			}
			if err.Error() != tc.expectedError {
				t.Errorf("expected error %s, got %s", tc.expectedError, err.Error())
			}
		})
	}
}

type MockSparkServiceFrostSignerFactory struct {
	conn *MockSparkServiceFrostSigner
}

func (m *MockSparkServiceFrostSignerFactory) NewFrostSigner(_ *so.Config) (helper.SparkServiceFrostSigner, error) {
	return m.conn, nil
}

func (m *MockSparkServiceFrostSignerFactory) IsMock() bool {
	return true
}

type MockSparkServiceFrostSigner struct {
	frostRound1Response *pbinternal.FrostRound1Response
	frostRound2Response *pbinternal.FrostRound2Response
	frostRound1Error    error
	frostRound2Error    error
}

func (m *MockSparkServiceFrostSigner) CallFrostRound1(ctx context.Context, operator *so.SigningOperator, req *pbinternal.FrostRound1Request) (*pbinternal.FrostRound1Response, error) {
	return m.frostRound1Response, m.frostRound1Error
}

func (m *MockSparkServiceFrostSigner) CallFrostRound2(ctx context.Context, operator *so.SigningOperator, req *pbinternal.FrostRound2Request) (*pbinternal.FrostRound2Response, error) {
	return m.frostRound2Response, m.frostRound2Error
}

// MockSparkInternalServiceClient is a mock implementation for testing
type MockSparkInternalServiceClient struct {
	frostRound1Response *pbinternal.FrostRound1Response
	frostRound2Response *pbinternal.FrostRound2Response
	frostRound1Error    error
	frostRound2Error    error
}

// GetTransfers implements spark_internal.SparkInternalServiceClient.
func (m *MockSparkInternalServiceClient) GetTransfers(context.Context, *pbinternal.GetTransfersRequest, ...grpc.CallOption) (*pbinternal.GetTransfersResponse, error) {
	return &pbinternal.GetTransfersResponse{}, nil
}

func (m *MockSparkInternalServiceClient) FrostRound1(_ context.Context, _ *pbinternal.FrostRound1Request, _ ...grpc.CallOption) (*pbinternal.FrostRound1Response, error) {
	return m.frostRound1Response, m.frostRound1Error
}

func (m *MockSparkInternalServiceClient) FrostRound2(_ context.Context, _ *pbinternal.FrostRound2Request, _ ...grpc.CallOption) (*pbinternal.FrostRound2Response, error) {
	return m.frostRound2Response, m.frostRound2Error
}

func (m *MockSparkInternalServiceClient) ReserveEntityDkgKey(_ context.Context, _ *pbinternal.ReserveEntityDkgKeyRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func (m *MockSparkInternalServiceClient) MarkKeysharesAsUsed(_ context.Context, _ *pbinternal.MarkKeysharesAsUsedRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func (m *MockSparkInternalServiceClient) MarkKeyshareForDepositAddress(_ context.Context, _ *pbinternal.MarkKeyshareForDepositAddressRequest, _ ...grpc.CallOption) (*pbinternal.MarkKeyshareForDepositAddressResponse, error) {
	return &pbinternal.MarkKeyshareForDepositAddressResponse{}, nil
}

func (m *MockSparkInternalServiceClient) FinalizeTreeCreation(_ context.Context, _ *pbinternal.FinalizeTreeCreationRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func (m *MockSparkInternalServiceClient) FinalizeTransfer(_ context.Context, _ *pbinternal.FinalizeTransferRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func (m *MockSparkInternalServiceClient) FinalizeRefreshTimelock(_ context.Context, _ *pbinternal.FinalizeRefreshTimelockRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func (m *MockSparkInternalServiceClient) FinalizeExtendLeaf(_ context.Context, _ *pbinternal.FinalizeExtendLeafRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func (m *MockSparkInternalServiceClient) InitiatePreimageSwap(_ context.Context, _ *pbspark.InitiatePreimageSwapRequest, _ ...grpc.CallOption) (*pbinternal.InitiatePreimageSwapResponse, error) {
	return &pbinternal.InitiatePreimageSwapResponse{}, nil
}

func (m *MockSparkInternalServiceClient) ProvidePreimage(_ context.Context, _ *pbinternal.ProvidePreimageRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func (m *MockSparkInternalServiceClient) UpdatePreimageRequest(_ context.Context, _ *pbinternal.UpdatePreimageRequestRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func (m *MockSparkInternalServiceClient) PrepareTreeAddress(_ context.Context, _ *pbinternal.PrepareTreeAddressRequest, _ ...grpc.CallOption) (*pbinternal.PrepareTreeAddressResponse, error) {
	return &pbinternal.PrepareTreeAddressResponse{}, nil
}

func (m *MockSparkInternalServiceClient) InitiateTransfer(_ context.Context, _ *pbinternal.InitiateTransferRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func (m *MockSparkInternalServiceClient) DeliverSenderKeyTweak(_ context.Context, _ *pbinternal.DeliverSenderKeyTweakRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func (m *MockSparkInternalServiceClient) InitiateCooperativeExit(_ context.Context, _ *pbinternal.InitiateCooperativeExitRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func (m *MockSparkInternalServiceClient) ReturnLightningPayment(_ context.Context, _ *pbspark.ReturnLightningPaymentRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func (m *MockSparkInternalServiceClient) StartTokenTransactionInternal(_ context.Context, _ *pbinternal.StartTokenTransactionInternalRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func (m *MockSparkInternalServiceClient) QueryTokenOutputsInternal(_ context.Context, _ *pbspark.QueryTokenOutputsRequest, _ ...grpc.CallOption) (*pbspark.QueryTokenOutputsResponse, error) {
	return &pbspark.QueryTokenOutputsResponse{}, nil
}

func (m *MockSparkInternalServiceClient) InitiateSettleReceiverKeyTweak(_ context.Context, _ *pbinternal.InitiateSettleReceiverKeyTweakRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func (m *MockSparkInternalServiceClient) SettleReceiverKeyTweak(_ context.Context, _ *pbinternal.SettleReceiverKeyTweakRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func (m *MockSparkInternalServiceClient) QueryLeafSigningPubkeys(_ context.Context, _ *pbinternal.QueryLeafSigningPubkeysRequest, _ ...grpc.CallOption) (*pbinternal.QueryLeafSigningPubkeysResponse, error) {
	return &pbinternal.QueryLeafSigningPubkeysResponse{}, nil
}

func (m *MockSparkInternalServiceClient) ResolveLeafInvestigation(_ context.Context, _ *pbinternal.ResolveLeafInvestigationRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func (m *MockSparkInternalServiceClient) Gossip(_ context.Context, _ *pbgossip.GossipMessage, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func (m *MockSparkInternalServiceClient) CreateUtxoSwap(_ context.Context, _ *pbinternal.CreateUtxoSwapRequest, _ ...grpc.CallOption) (*pbinternal.CreateUtxoSwapResponse, error) {
	return &pbinternal.CreateUtxoSwapResponse{}, nil
}

func (m *MockSparkInternalServiceClient) CreateStaticDepositUtxoSwap(_ context.Context, _ *pbinternal.CreateStaticDepositUtxoSwapRequest, _ ...grpc.CallOption) (*pbinternal.CreateStaticDepositUtxoSwapResponse, error) {
	return &pbinternal.CreateStaticDepositUtxoSwapResponse{}, nil
}

func (m *MockSparkInternalServiceClient) CreateStaticDepositUtxoRefund(_ context.Context, _ *pbinternal.CreateStaticDepositUtxoRefundRequest, _ ...grpc.CallOption) (*pbinternal.CreateStaticDepositUtxoRefundResponse, error) {
	return &pbinternal.CreateStaticDepositUtxoRefundResponse{}, nil
}

func (m *MockSparkInternalServiceClient) RollbackUtxoSwap(_ context.Context, _ *pbinternal.RollbackUtxoSwapRequest, _ ...grpc.CallOption) (*pbinternal.RollbackUtxoSwapResponse, error) {
	return &pbinternal.RollbackUtxoSwapResponse{}, nil
}

func (m *MockSparkInternalServiceClient) UtxoSwapCompleted(_ context.Context, _ *pbinternal.UtxoSwapCompletedRequest, _ ...grpc.CallOption) (*pbinternal.UtxoSwapCompletedResponse, error) {
	return &pbinternal.UtxoSwapCompletedResponse{}, nil
}

func (m *MockSparkInternalServiceClient) SettleSenderKeyTweak(_ context.Context, _ *pbinternal.SettleSenderKeyTweakRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func (m *MockSparkInternalServiceClient) FixKeyshare(_ context.Context, _ *pbinternal.FixKeyshareRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func (m *MockSparkInternalServiceClient) FixKeyshareRound1(_ context.Context, _ *pbinternal.FixKeyshareRound1Request, _ ...grpc.CallOption) (*pbinternal.FixKeyshareRound1Response, error) {
	return &pbinternal.FixKeyshareRound1Response{}, nil
}

func (m *MockSparkInternalServiceClient) FixKeyshareRound2(_ context.Context, _ *pbinternal.FixKeyshareRound2Request, _ ...grpc.CallOption) (*pbinternal.FixKeyshareRound2Response, error) {
	return &pbinternal.FixKeyshareRound2Response{}, nil
}

func TestSignFrostInternal(t *testing.T) {
	// Test the core signing job creation and validation
	t.Run("SigningJobCreation", func(t *testing.T) {
		// Create a mock keyshare ID
		keyshareID := uuid.New()

		// Create a mock signing job
		job := &helper.SigningJob{
			JobID:             "test-job-id",
			SigningKeyshareID: keyshareID,
			Message:           []byte("test message to sign"),
			VerifyingKey:      &pubKey,
			UserCommitment:    &objects.SigningCommitment{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
		}

		// Test that the job is properly created
		if job.JobID != "test-job-id" {
			t.Errorf("Expected job ID %s, got %s", "test-job-id", job.JobID)
		}

		if job.SigningKeyshareID != keyshareID {
			t.Errorf("Expected keyshare ID %s, got %s", keyshareID, job.SigningKeyshareID)
		}

		if len(job.Message) == 0 {
			t.Error("Expected message to be present")
		}

		if job.VerifyingKey == nil {
			t.Error("Expected verifying key to be present")
		}

		if job.UserCommitment == nil {
			t.Error("Expected user commitment to be present")
		}
	})

	// Test the key package creation
	t.Run("KeyPackageCreation", func(t *testing.T) {
		// Create a mock getKeyPackages function that returns predefined key packages
		mockGetKeyPackages := func(_ context.Context, _ *so.Config, keyshareIDs []uuid.UUID) (map[uuid.UUID]*pbfrost.KeyPackage, error) {
			result := make(map[uuid.UUID]*pbfrost.KeyPackage)
			for _, id := range keyshareIDs {
				result[id] = &pbfrost.KeyPackage{
					Identifier:  "test-identifier",
					SecretShare: []byte("test-secret-share-32-bytes-long"),
					PublicShares: map[string][]byte{
						"test-identifier": pubKey.Serialize(),
					},
					PublicKey:  pubKey.Serialize(),
					MinSigners: 1,
				}
			}
			return result, nil
		}

		// Test the key package creation
		keyshareIDs := []uuid.UUID{uuid.New()}
		keyPackages, err := mockGetKeyPackages(t.Context(), nil, keyshareIDs)
		if err != nil {
			t.Fatalf("Expected no error, got %v", err)
		}

		if len(keyPackages) != 1 {
			t.Fatalf("Expected 1 key package, got %d", len(keyPackages))
		}

		keyPackage := keyPackages[keyshareIDs[0]]
		if keyPackage.Identifier != "test-identifier" {
			t.Errorf("Expected identifier %s, got %s", "test-identifier", keyPackage.Identifier)
		}

		if keyPackage.MinSigners != 1 {
			t.Errorf("Expected min signers 1, got %d", keyPackage.MinSigners)
		}

		if len(keyPackage.PublicShares) != 1 {
			t.Errorf("Expected 1 public share, got %d", len(keyPackage.PublicShares))
		}
	})

	// Test the signing commitment creation
	t.Run("SigningCommitmentCreation", func(t *testing.T) {
		commitment := &objects.SigningCommitment{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()}

		// Test that the commitment is properly created
		if len(commitment.Binding) == 0 {
			t.Error("Expected binding to be present")
		}

		if len(commitment.Hiding) == 0 {
			t.Error("Expected hiding to be present")
		}

		// Test marshaling to proto
		proto, err := commitment.MarshalProto()
		if err != nil {
			t.Fatalf("Expected no error marshaling to proto, got %v", err)
		}

		if len(proto.Binding) == 0 {
			t.Error("Expected proto binding to be present")
		}

		if len(proto.Hiding) == 0 {
			t.Error("Expected proto hiding to be present")
		}
	})

	// Test the actual SignFrostInternal function
	t.Run("SignFrost", func(t *testing.T) {
		config := sparktesting.TestConfig(t)

		// Add a mock operator to the config with identifier "operator1"
		if config.SigningOperatorMap == nil {
			config.SigningOperatorMap = make(map[string]*so.SigningOperator)
		}
		config.SigningOperatorMap["operator1"] = &so.SigningOperator{Identifier: "operator1"}

		// Create a mock keyshare ID
		keyshareID := uuid.New()

		// Create a mock signing job
		job := &helper.SigningJob{
			JobID:             "test-job-id",
			SigningKeyshareID: keyshareID,
			Message:           []byte("test message to sign"),
			VerifyingKey:      &pubKey,
			UserCommitment:    &objects.SigningCommitment{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
			AdaptorPublicKey:  nil,
		}

		// Create a mock getKeyPackages function that returns predefined key packages
		mockGetKeyPackages := func(_ context.Context, _ *so.Config, keyshareIDs []uuid.UUID) (map[uuid.UUID]*pbfrost.KeyPackage, error) {
			result := make(map[uuid.UUID]*pbfrost.KeyPackage)
			for _, id := range keyshareIDs {
				result[id] = &pbfrost.KeyPackage{
					Identifier:  "test-identifier",
					SecretShare: []byte("test-secret-share-32-bytes-long"),
					PublicShares: map[string][]byte{
						"test-identifier": pubKey.Serialize(),
					},
					PublicKey:  pubKey.Serialize(),
					MinSigners: 1,
				}
			}
			return result, nil
		}

		// Create a mock connection that returns predefined responses
		mockFrostSigner := &MockSparkServiceFrostSigner{
			frostRound1Response: &pbinternal.FrostRound1Response{
				SigningCommitments: []*pbcommon.SigningCommitment{
					{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
				},
			},
			frostRound2Response: &pbinternal.FrostRound2Response{
				Results: map[string]*pbcommon.SigningResult{
					"test-job-id": {
						SignatureShare: []byte("test-signature-share"),
					},
				},
			},
		}

		mockFrostSignerFactory := &MockSparkServiceFrostSignerFactory{
			conn: mockFrostSigner,
		}

		// Call SignFrostInternal with our mock
		results, err := helper.SignFrostInternal(t.Context(), config, []*helper.SigningJob{job}, mockGetKeyPackages, mockFrostSignerFactory)
		// Verify the results
		if err != nil {
			t.Fatalf("Expected no error, got %v", err)
		}

		if len(results) != 1 {
			t.Fatalf("Expected 1 result, got %d", len(results))
		}

		result := results[0]
		if result.JobID != job.JobID {
			t.Errorf("Expected job ID %s, got %s", job.JobID, result.JobID)
		}

		if len(result.SignatureShares) == 0 {
			t.Error("Expected signature shares to be present")
		}

		if len(result.SigningCommitments) == 0 {
			t.Error("Expected signing commitments to be present")
		}

		if len(result.PublicKeys) == 0 {
			t.Error("Expected public keys to be present")
		}

		if result.KeyshareThreshold != 1 {
			t.Errorf("Expected keyshare threshold 1, got %d", result.KeyshareThreshold)
		}
	})

	// Test error cases and edge cases
	t.Run("ErrorCases", func(t *testing.T) {
		config := sparktesting.TestConfig(t)

		// Test with empty jobs list
		t.Run("EmptyJobsList", func(t *testing.T) {
			mockGetKeyPackages := func(_ context.Context, _ *so.Config, _ []uuid.UUID) (map[uuid.UUID]*pbfrost.KeyPackage, error) {
				return make(map[uuid.UUID]*pbfrost.KeyPackage), nil
			}

			mockFrostSigner := &MockSparkServiceFrostSigner{
				frostRound1Response: &pbinternal.FrostRound1Response{},
				frostRound2Response: &pbinternal.FrostRound2Response{},
			}

			mockFrostSignerFactory := &MockSparkServiceFrostSignerFactory{
				conn: mockFrostSigner,
			}

			// TODO Should we actually be successful on a frost sign with an empty jobs list?
			results, err := helper.SignFrostInternal(t.Context(), config, []*helper.SigningJob{}, mockGetKeyPackages, mockFrostSignerFactory)
			if err != nil {
				t.Fatalf("Expected no error for empty jobs list, got %v", err)
			}

			if len(results) != 0 {
				t.Errorf("Expected 0 results for empty jobs list, got %d", len(results))
			}
		})

		// Test with getKeyPackages error
		t.Run("GetKeyPackagesError", func(t *testing.T) {
			// Add a mock operator to the config with identifier "operator1"
			if config.SigningOperatorMap == nil {
				config.SigningOperatorMap = make(map[string]*so.SigningOperator)
			}
			config.SigningOperatorMap["operator1"] = &so.SigningOperator{Identifier: "operator1"}

			mockGetKeyPackages := func(_ context.Context, _ *so.Config, _ []uuid.UUID) (map[uuid.UUID]*pbfrost.KeyPackage, error) {
				return nil, errors.New("database connection failed")
			}

			mockFrostSigner := &MockSparkServiceFrostSigner{}

			mockFrostSignerFactory := &MockSparkServiceFrostSignerFactory{
				conn: mockFrostSigner,
			}

			job := &helper.SigningJob{
				JobID:             "test-job-id",
				SigningKeyshareID: uuid.New(),
				Message:           []byte("test message"),
				VerifyingKey:      &pubKey,
				UserCommitment:    &objects.SigningCommitment{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
			}

			_, err := helper.SignFrostInternal(t.Context(), config, []*helper.SigningJob{job}, mockGetKeyPackages, mockFrostSignerFactory)
			if err == nil {
				t.Fatal("Expected error when getKeyPackages fails, got nil")
			}

			if err.Error() != "database connection failed" {
				t.Errorf("Expected error 'database connection failed', got '%s'", err.Error())
			}
		})

		// Test with missing keyshare in getKeyPackages response
		t.Run("MissingKeyshare", func(t *testing.T) {
			keyshareID := uuid.New()
			mockGetKeyPackages := func(_ context.Context, _ *so.Config, _ []uuid.UUID) (map[uuid.UUID]*pbfrost.KeyPackage, error) {
				// Return empty map, missing the requested keyshare
				return make(map[uuid.UUID]*pbfrost.KeyPackage), nil
			}

			mockFrostSigner := &MockSparkServiceFrostSigner{}

			mockFrostSignerFactory := &MockSparkServiceFrostSignerFactory{
				conn: mockFrostSigner,
			}

			job := &helper.SigningJob{
				JobID:             "test-job-id",
				SigningKeyshareID: keyshareID,
				Message:           []byte("test message"),
				VerifyingKey:      &pubKey,
				UserCommitment:    &objects.SigningCommitment{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
			}

			_, err := helper.SignFrostInternal(t.Context(), config, []*helper.SigningJob{job}, mockGetKeyPackages, mockFrostSignerFactory)
			if err == nil {
				t.Fatal("Expected error when keyshare is missing, got nil")
			}
		})

		// Test with frostRound1 error
		t.Run("FrostRound1Error", func(t *testing.T) {
			keyshareID := uuid.New()
			mockGetKeyPackages := func(_ context.Context, _ *so.Config, keyshareIDs []uuid.UUID) (map[uuid.UUID]*pbfrost.KeyPackage, error) {
				result := make(map[uuid.UUID]*pbfrost.KeyPackage)
				for _, id := range keyshareIDs {
					result[id] = &pbfrost.KeyPackage{
						Identifier:  "test-identifier",
						SecretShare: []byte("test-secret-share-32-bytes-long"),
						PublicShares: map[string][]byte{
							"test-identifier": pubKey.Serialize(),
						},
						PublicKey:  pubKey.Serialize(),
						MinSigners: 1,
					}
				}
				return result, nil
			}

			// Mock connection that returns error in FrostRound1
			mockFrostSigner := &MockSparkServiceFrostSigner{
				frostRound1Error: errors.New("frost round 1 failed"),
			}

			mockFrostSignerFactory := &MockSparkServiceFrostSignerFactory{
				conn: mockFrostSigner,
			}

			job := &helper.SigningJob{
				JobID:             "test-job-id",
				SigningKeyshareID: keyshareID,
				Message:           []byte("test message"),
				VerifyingKey:      &pubKey,
				UserCommitment:    &objects.SigningCommitment{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
			}

			_, err := helper.SignFrostInternal(t.Context(), config, []*helper.SigningJob{job}, mockGetKeyPackages, mockFrostSignerFactory)
			if err == nil {
				t.Fatal("Expected error when frostRound1 fails, got nil")
			}

			if err.Error() != "frost round 1 failed" {
				t.Errorf("Expected error 'frost round 1 failed', got '%s'", err.Error())
			}
		})

		// Test with frostRound2 error
		t.Run("FrostRound2Error", func(t *testing.T) {
			keyshareID := uuid.New()
			mockGetKeyPackages := func(_ context.Context, _ *so.Config, keyshareIDs []uuid.UUID) (map[uuid.UUID]*pbfrost.KeyPackage, error) {
				result := make(map[uuid.UUID]*pbfrost.KeyPackage)
				for _, id := range keyshareIDs {
					result[id] = &pbfrost.KeyPackage{
						Identifier:  "test-identifier",
						SecretShare: []byte("test-secret-share-32-bytes-long"),
						PublicShares: map[string][]byte{
							"test-identifier": pubKey.Serialize(),
						},
						PublicKey:  pubKey.Serialize(),
						MinSigners: 1,
					}
				}
				return result, nil
			}

			// Mock connection that returns error in FrostRound2
			mockFrostSigner := &MockSparkServiceFrostSigner{
				frostRound1Response: &pbinternal.FrostRound1Response{
					SigningCommitments: []*pbcommon.SigningCommitment{
						{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
					},
				},
				frostRound2Error: errors.New("frost round 2 failed"),
			}

			mockFrostSignerFactory := &MockSparkServiceFrostSignerFactory{
				conn: mockFrostSigner,
			}

			// Add a mock operator to the config with identifier "operator1"
			if config.SigningOperatorMap == nil {
				config.SigningOperatorMap = make(map[string]*so.SigningOperator)
			}
			config.SigningOperatorMap["operator1"] = &so.SigningOperator{Identifier: "operator1"}

			job := &helper.SigningJob{
				JobID:             "test-job-id",
				SigningKeyshareID: keyshareID,
				Message:           []byte("test message"),
				VerifyingKey:      &pubKey,
				UserCommitment:    &objects.SigningCommitment{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
			}

			_, err := helper.SignFrostInternal(t.Context(), config, []*helper.SigningJob{job}, mockGetKeyPackages, mockFrostSignerFactory)
			if err == nil {
				t.Fatal("Expected error when frostRound2 fails, got nil")
			}

			if err.Error() != "frost round 2 failed" {
				t.Errorf("Expected error 'frost round 2 failed', got '%s'", err.Error())
			}
		})
	})

	// Test edge cases
	t.Run("EdgeCases", func(t *testing.T) {
		config := sparktesting.TestConfig(t)

		// Test with multiple jobs
		t.Run("MultipleJobs", func(t *testing.T) {
			keyshareID1 := uuid.New()
			keyshareID2 := uuid.New()

			mockGetKeyPackages := func(_ context.Context, _ *so.Config, keyshareIDs []uuid.UUID) (map[uuid.UUID]*pbfrost.KeyPackage, error) {
				result := make(map[uuid.UUID]*pbfrost.KeyPackage)
				for _, id := range keyshareIDs {
					result[id] = &pbfrost.KeyPackage{
						Identifier:  "test-identifier",
						SecretShare: []byte("test-secret-share-32-bytes-long"),
						PublicShares: map[string][]byte{
							"test-identifier": pubKey.Serialize(),
						},
						PublicKey:  pubKey.Serialize(),
						MinSigners: 1,
					}
				}
				return result, nil
			}

			mockFrostSigner := &MockSparkServiceFrostSigner{
				frostRound1Response: &pbinternal.FrostRound1Response{
					SigningCommitments: []*pbcommon.SigningCommitment{
						{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
						{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
					},
				},
				frostRound2Response: &pbinternal.FrostRound2Response{
					Results: map[string]*pbcommon.SigningResult{
						"job-1": {
							SignatureShare: []byte("signature-share-1"),
						},
						"job-2": {
							SignatureShare: []byte("signature-share-2"),
						},
					},
				},
			}

			mockFrostSignerFactory := &MockSparkServiceFrostSignerFactory{
				conn: mockFrostSigner,
			}

			jobs := []*helper.SigningJob{
				{
					JobID:             "job-1",
					SigningKeyshareID: keyshareID1,
					Message:           []byte("message 1"),
					VerifyingKey:      &pubKey,
					UserCommitment:    &objects.SigningCommitment{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
				},
				{
					JobID:             "job-2",
					SigningKeyshareID: keyshareID2,
					Message:           []byte("message 2"),
					VerifyingKey:      &pubKey,
					UserCommitment:    &objects.SigningCommitment{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
				},
			}

			results, err := helper.SignFrostInternal(t.Context(), config, jobs, mockGetKeyPackages, mockFrostSignerFactory)
			if err != nil {
				t.Fatalf("Expected no error, got %v", err)
			}

			if len(results) != 2 {
				t.Fatalf("Expected 2 results, got %d", len(results))
			}

			// Verify job 1
			if results[0].JobID != "job-1" {
				t.Errorf("Expected job ID 'job-1', got '%s'", results[0].JobID)
			}

			// Verify job 2
			if results[1].JobID != "job-2" {
				t.Errorf("Expected job ID 'job-2', got '%s'", results[1].JobID)
			}
		})

		// Test with nil user commitment
		t.Run("NilUserCommitment", func(t *testing.T) {
			keyshareID := uuid.New()

			mockGetKeyPackages := func(_ context.Context, _ *so.Config, keyshareIDs []uuid.UUID) (map[uuid.UUID]*pbfrost.KeyPackage, error) {
				result := make(map[uuid.UUID]*pbfrost.KeyPackage)
				for _, id := range keyshareIDs {
					result[id] = &pbfrost.KeyPackage{
						Identifier:  "test-identifier",
						SecretShare: []byte("test-secret-share-32-bytes-long"),
						PublicShares: map[string][]byte{
							"test-identifier": pubKey.Serialize(),
						},
						PublicKey:  pubKey.Serialize(),
						MinSigners: 1,
					}
				}
				return result, nil
			}

			mockFrostSigner := &MockSparkServiceFrostSigner{
				frostRound1Response: &pbinternal.FrostRound1Response{
					SigningCommitments: []*pbcommon.SigningCommitment{
						{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
					},
				},
				frostRound2Response: &pbinternal.FrostRound2Response{
					Results: map[string]*pbcommon.SigningResult{
						"test-job-id": {
							SignatureShare: []byte("test-signature-share"),
						},
					},
				},
			}

			mockFrostSignerFactory := &MockSparkServiceFrostSignerFactory{
				conn: mockFrostSigner,
			}

			job := &helper.SigningJob{
				JobID:             "test-job-id",
				SigningKeyshareID: keyshareID,
				Message:           []byte("test message"),
				VerifyingKey:      &pubKey,
				UserCommitment:    nil, // Test with nil user commitment
				AdaptorPublicKey:  nil,
			}

			results, err := helper.SignFrostInternal(t.Context(), config, []*helper.SigningJob{job}, mockGetKeyPackages, mockFrostSignerFactory)
			if err != nil {
				t.Fatalf("Expected no error with nil user commitment, got %v", err)
			}

			if len(results) != 1 {
				t.Fatalf("Expected 1 result, got %d", len(results))
			}
		})

		// Test with adaptor public key
		t.Run("WithAdaptorPublicKey", func(t *testing.T) {
			keyshareID := uuid.New()
			adaptorPubKey := pubKey

			mockGetKeyPackages := func(_ context.Context, _ *so.Config, keyshareIDs []uuid.UUID) (map[uuid.UUID]*pbfrost.KeyPackage, error) {
				result := make(map[uuid.UUID]*pbfrost.KeyPackage)
				for _, id := range keyshareIDs {
					result[id] = &pbfrost.KeyPackage{
						Identifier:  "test-identifier",
						SecretShare: []byte("test-secret-share-32-bytes-long"),
						PublicShares: map[string][]byte{
							"test-identifier": pubKey.Serialize(),
						},
						PublicKey:  pubKey.Serialize(),
						MinSigners: 1,
					}
				}
				return result, nil
			}

			mockFrostSigner := &MockSparkServiceFrostSigner{
				frostRound1Response: &pbinternal.FrostRound1Response{
					SigningCommitments: []*pbcommon.SigningCommitment{
						{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
					},
				},
				frostRound2Response: &pbinternal.FrostRound2Response{
					Results: map[string]*pbcommon.SigningResult{
						"test-job-id": {
							SignatureShare: []byte("test-signature-share"),
						},
					},
				},
			}

			mockFrostSignerFactory := &MockSparkServiceFrostSignerFactory{
				conn: mockFrostSigner,
			}

			job := &helper.SigningJob{
				JobID:             "test-job-id",
				SigningKeyshareID: keyshareID,
				Message:           []byte("test message"),
				VerifyingKey:      &pubKey,
				UserCommitment:    &objects.SigningCommitment{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
				AdaptorPublicKey:  &adaptorPubKey,
			}

			results, err := helper.SignFrostInternal(t.Context(), config, []*helper.SigningJob{job}, mockGetKeyPackages, mockFrostSignerFactory)
			if err != nil {
				t.Fatalf("Expected no error with adaptor public key, got %v", err)
			}

			if len(results) != 1 {
				t.Fatalf("Expected 1 result, got %d", len(results))
			}
		})

		// Test with different threshold values
		t.Run("DifferentThresholds", func(t *testing.T) {
			keyshareID := uuid.New()

			mockGetKeyPackages := func(_ context.Context, _ *so.Config, keyshareIDs []uuid.UUID) (map[uuid.UUID]*pbfrost.KeyPackage, error) {
				result := make(map[uuid.UUID]*pbfrost.KeyPackage)
				for _, id := range keyshareIDs {
					result[id] = &pbfrost.KeyPackage{
						Identifier:  "test-identifier",
						SecretShare: []byte("test-secret-share-32-bytes-long"),
						PublicShares: map[string][]byte{
							"test-identifier": pubKey.Serialize(),
						},
						PublicKey:  pubKey.Serialize(),
						MinSigners: 3, // Test with different threshold
					}
				}
				return result, nil
			}

			mockFrostSigner := &MockSparkServiceFrostSigner{
				frostRound1Response: &pbinternal.FrostRound1Response{
					SigningCommitments: []*pbcommon.SigningCommitment{
						{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
					},
				},
				frostRound2Response: &pbinternal.FrostRound2Response{
					Results: map[string]*pbcommon.SigningResult{
						"test-job-id": {
							SignatureShare: []byte("test-signature-share"),
						},
					},
				},
			}

			mockFrostSignerFactory := &MockSparkServiceFrostSignerFactory{
				conn: mockFrostSigner,
			}

			job := &helper.SigningJob{
				JobID:             "test-job-id",
				SigningKeyshareID: keyshareID,
				Message:           []byte("test message"),
				VerifyingKey:      &pubKey,
				UserCommitment:    &objects.SigningCommitment{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
			}

			results, err := helper.SignFrostInternal(t.Context(), config, []*helper.SigningJob{job}, mockGetKeyPackages, mockFrostSignerFactory)
			if err != nil {
				t.Fatalf("Expected no error with different threshold, got %v", err)
			}

			if len(results) != 1 {
				t.Fatalf("Expected 1 result, got %d", len(results))
			}

			if results[0].KeyshareThreshold != 3 {
				t.Errorf("Expected threshold 3, got %d", results[0].KeyshareThreshold)
			}
		})
	})

	// Test SigningKeyshareIDsFromSigningJobs
	t.Run("SigningKeyshareIDsFromSigningJobs", func(t *testing.T) {
		keyshareID1 := uuid.New()
		keyshareID2 := uuid.New()

		jobs := []*helper.SigningJob{
			{SigningKeyshareID: keyshareID1},
			{SigningKeyshareID: keyshareID2},
		}

		ids := helper.SigningKeyshareIDsFromSigningJobs(jobs)
		if len(ids) != 2 {
			t.Fatalf("Expected 2 keyshare IDs, got %d", len(ids))
		}

		if ids[0] != keyshareID1 {
			t.Errorf("Expected first ID to be %s, got %s", keyshareID1, ids[0])
		}

		if ids[1] != keyshareID2 {
			t.Errorf("Expected second ID to be %s, got %s", keyshareID2, ids[1])
		}
	})

	// Test with context cancellation
	t.Run("ContextCancellation", func(t *testing.T) {
		config := sparktesting.TestConfig(t)

		ctx, cancel := context.WithCancel(t.Context())
		cancel() // Cancel immediately

		keyshareID := uuid.New()
		mockGetKeyPackages := func(_ context.Context, _ *so.Config, keyshareIDs []uuid.UUID) (map[uuid.UUID]*pbfrost.KeyPackage, error) {
			result := make(map[uuid.UUID]*pbfrost.KeyPackage)
			for _, id := range keyshareIDs {
				result[id] = &pbfrost.KeyPackage{
					Identifier:  "test-identifier",
					SecretShare: []byte("test-secret-share-32-bytes-long"),
					PublicShares: map[string][]byte{
						"test-identifier": pubKey.Serialize(),
					},
					PublicKey:  pubKey.Serialize(),
					MinSigners: 1,
				}
			}
			return result, nil
		}

		mockFrostSigner := &MockSparkServiceFrostSigner{
			frostRound1Error: context.Canceled,
		}

		mockFrostSignerFactory := &MockSparkServiceFrostSignerFactory{
			conn: mockFrostSigner,
		}

		job := &helper.SigningJob{
			JobID:             "test-job-id",
			SigningKeyshareID: keyshareID,
			Message:           []byte("test message"),
			VerifyingKey:      &pubKey,
			UserCommitment:    &objects.SigningCommitment{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
		}

		_, err := helper.SignFrostInternal(ctx, config, []*helper.SigningJob{job}, mockGetKeyPackages, mockFrostSignerFactory)
		if err == nil {
			t.Fatal("Expected error when context is cancelled, got nil")
		}
	})
}

// Test SignFrostWithPregeneratedNonce tests the SignFrostWithPregeneratedNonce function
func TestSignFrostWithPregeneratedNonce(t *testing.T) {
	t.Run("BasicFunctionality", func(t *testing.T) {
		config := sparktesting.TestConfig(t)

		// Add mock operators to the config with identifiers "operator1" and "operator2"
		if config.SigningOperatorMap == nil {
			config.SigningOperatorMap = make(map[string]*so.SigningOperator)
		}
		config.SigningOperatorMap["operator1"] = &so.SigningOperator{Identifier: "operator1"}
		config.SigningOperatorMap["operator2"] = &so.SigningOperator{Identifier: "operator2"}

		keyshareID := uuid.New()
		job := &helper.SigningJobWithPregeneratedNonce{
			SigningJob: helper.SigningJob{
				JobID:             "test-job-id",
				SigningKeyshareID: keyshareID,
				Message:           []byte("test message"),
				VerifyingKey:      &pubKey,
				UserCommitment:    &objects.SigningCommitment{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
			},
			Round1Packages: map[string]objects.SigningCommitment{
				"operator1": {Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
				"operator2": {Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
			},
		}

		mockGetKeyPackages := func(_ context.Context, _ *so.Config, keyshareIDs []uuid.UUID) (map[uuid.UUID]*pbfrost.KeyPackage, error) {
			result := make(map[uuid.UUID]*pbfrost.KeyPackage)
			for _, id := range keyshareIDs {
				result[id] = &pbfrost.KeyPackage{
					Identifier:  "test-identifier",
					SecretShare: []byte("test-secret-share-32-bytes-long"),
					PublicShares: map[string][]byte{
						"test-identifier": pubKey.Serialize(),
					},
					PublicKey:  pubKey.Serialize(),
					MinSigners: 1,
				}
			}
			return result, nil
		}

		mockFrostSigner := &MockSparkServiceFrostSigner{
			frostRound1Response: &pbinternal.FrostRound1Response{
				SigningCommitments: []*pbcommon.SigningCommitment{
					{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
				},
			},
			frostRound2Response: &pbinternal.FrostRound2Response{
				Results: map[string]*pbcommon.SigningResult{
					"test-job-id": {
						SignatureShare: []byte("test-signature-share"),
					},
				},
			},
		}

		mockFrostSignerFactory := &MockSparkServiceFrostSignerFactory{
			conn: mockFrostSigner,
		}

		results, err := helper.SignFrostWithPregeneratedNonceInternal(t.Context(), config, []*helper.SigningJobWithPregeneratedNonce{job}, mockGetKeyPackages, mockFrostSignerFactory)
		if err != nil {
			t.Fatalf("Expected no error, got %v", err)
		}

		if len(results) != 1 {
			t.Fatalf("Expected 1 result, got %d", len(results))
		}

		if results[0].JobID != "test-job-id" {
			t.Errorf("Expected job ID 'test-job-id', got '%s'", results[0].JobID)
		}
	})

	t.Run("ErrorCases", func(t *testing.T) {
		config := sparktesting.TestConfig(t)

		// Test with getKeyPackages error
		t.Run("GetKeyPackagesError", func(t *testing.T) {
			// Add a mock operator to the config with identifier "operator1"
			if config.SigningOperatorMap == nil {
				config.SigningOperatorMap = make(map[string]*so.SigningOperator)
			}
			config.SigningOperatorMap["operator1"] = &so.SigningOperator{Identifier: "operator1"}

			mockGetKeyPackages := func(_ context.Context, _ *so.Config, _ []uuid.UUID) (map[uuid.UUID]*pbfrost.KeyPackage, error) {
				return nil, errors.New("database connection failed")
			}

			mockFrostSigner := &MockSparkServiceFrostSigner{}

			mockFrostSignerFactory := &MockSparkServiceFrostSignerFactory{
				conn: mockFrostSigner,
			}

			job := &helper.SigningJobWithPregeneratedNonce{
				SigningJob: helper.SigningJob{
					JobID:             "test-job-id",
					SigningKeyshareID: uuid.New(),
					Message:           []byte("test message"),
					VerifyingKey:      &pubKey,
					UserCommitment:    &objects.SigningCommitment{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
				},
				Round1Packages: map[string]objects.SigningCommitment{
					"operator1": {Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
				},
			}

			_, err := helper.SignFrostWithPregeneratedNonceInternal(t.Context(), config, []*helper.SigningJobWithPregeneratedNonce{job}, mockGetKeyPackages, mockFrostSignerFactory)
			if err == nil {
				t.Fatal("Expected error when getKeyPackages fails, got nil")
			}

			if err.Error() != "database connection failed" {
				t.Errorf("Expected error 'database connection failed', got '%s'", err.Error())
			}
		})

		// Test with frostRound2 error
		t.Run("FrostRound2Error", func(t *testing.T) {
			mockGetKeyPackages := func(_ context.Context, _ *so.Config, keyshareIDs []uuid.UUID) (map[uuid.UUID]*pbfrost.KeyPackage, error) {
				result := make(map[uuid.UUID]*pbfrost.KeyPackage)
				for _, id := range keyshareIDs {
					result[id] = &pbfrost.KeyPackage{
						Identifier:  "test-identifier",
						SecretShare: []byte("test-secret-share-32-bytes-long"),
						PublicShares: map[string][]byte{
							"test-identifier": pubKey.Serialize(),
						},
						PublicKey:  pubKey.Serialize(),
						MinSigners: 1,
					}
				}
				return result, nil
			}

			mockFrostSigner := &MockSparkServiceFrostSigner{
				frostRound2Error: errors.New("frost round 2 failed"),
			}

			mockFrostSignerFactory := &MockSparkServiceFrostSignerFactory{
				conn: mockFrostSigner,
			}

			job := &helper.SigningJobWithPregeneratedNonce{
				SigningJob: helper.SigningJob{
					JobID:             "test-job-id",
					SigningKeyshareID: uuid.New(),
					Message:           []byte("test message"),
					VerifyingKey:      &pubKey,
					UserCommitment:    &objects.SigningCommitment{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
				},
				Round1Packages: map[string]objects.SigningCommitment{
					"operator1": {Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
				},
			}

			_, err := helper.SignFrostWithPregeneratedNonceInternal(t.Context(), config, []*helper.SigningJobWithPregeneratedNonce{job}, mockGetKeyPackages, mockFrostSignerFactory)
			if err == nil {
				t.Fatal("Expected error when frostRound2 fails, got nil")
			}

			if err.Error() != "frost round 2 failed" {
				t.Errorf("Expected error 'frost round 2 failed', got '%s'", err.Error())
			}
		})
	})
}

// TestGetSigningCommitments tests the GetSigningCommitments function
func TestGetSigningCommitments(t *testing.T) {

	t.Run("BasicFunctionality", func(t *testing.T) {
		config := sparktesting.TestConfig(t)

		keyshareIDs := []uuid.UUID{uuid.New(), uuid.New()}

		mockFrostSigner := &MockSparkServiceFrostSigner{
			frostRound1Response: &pbinternal.FrostRound1Response{
				SigningCommitments: []*pbcommon.SigningCommitment{
					{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
					{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
				},
			},
		}

		mockFrostSignerFactory := &MockSparkServiceFrostSignerFactory{
			conn: mockFrostSigner,
		}

		// Create a mock getKeyPackages function
		mockGetKeyPackages := func(_ context.Context, _ *so.Config, keyshareIDs []uuid.UUID) (map[uuid.UUID]*pbfrost.KeyPackage, error) {
			result := make(map[uuid.UUID]*pbfrost.KeyPackage)
			for _, id := range keyshareIDs {
				result[id] = &pbfrost.KeyPackage{
					Identifier:  "test-identifier",
					SecretShare: []byte("test-secret-share-32-bytes-long"),
					PublicShares: map[string][]byte{
						"test-identifier": pubKey.Serialize(),
					},
					PublicKey:  pubKey.Serialize(),
					MinSigners: 1,
				}
			}
			return result, nil
		}

		// Test the function with our mock
		_, err := helper.GetSigningCommitmentsInternal(t.Context(), config, keyshareIDs, mockGetKeyPackages, 1, mockFrostSignerFactory)
		if err != nil {
			t.Fatalf("Expected no error, got %v", err)
		}
	})

	t.Run("ErrorCases", func(t *testing.T) {
		config := sparktesting.TestConfig(t)

		// Test with frostRound1 error
		t.Run("FrostRound1Error", func(t *testing.T) {
			mockFrostSigner := &MockSparkServiceFrostSigner{
				frostRound1Error: errors.New("frost round 1 failed"),
			}

			mockFrostSignerFactory := &MockSparkServiceFrostSignerFactory{
				conn: mockFrostSigner,
			}

			keyshareIDs := []uuid.UUID{uuid.New()}

			mockGetKeyPackages := func(_ context.Context, _ *so.Config, keyshareIDs []uuid.UUID) (map[uuid.UUID]*pbfrost.KeyPackage, error) {
				result := make(map[uuid.UUID]*pbfrost.KeyPackage)
				for _, id := range keyshareIDs {
					result[id] = &pbfrost.KeyPackage{
						Identifier:  "test-identifier",
						SecretShare: []byte("test-secret-share-32-bytes-long"),
						PublicShares: map[string][]byte{
							"test-identifier": pubKey.Serialize(),
						},
						PublicKey:  pubKey.Serialize(),
						MinSigners: 1,
					}
				}
				return result, nil
			}

			_, err := helper.GetSigningCommitmentsInternal(t.Context(), config, keyshareIDs, mockGetKeyPackages, 1, mockFrostSignerFactory)
			if err == nil {
				t.Fatal("Expected error when frostRound1 fails, got nil")
			}

			if err.Error() != "frost round 1 failed" {
				t.Errorf("Expected error 'frost round 1 failed', got '%s'", err.Error())
			}
		})

		// Test with getKeyPackages error
		t.Run("GetKeyPackagesError", func(t *testing.T) {
			mockFrostSigner := &MockSparkServiceFrostSigner{}

			mockFrostSignerFactory := &MockSparkServiceFrostSignerFactory{
				conn: mockFrostSigner,
			}

			keyshareIDs := []uuid.UUID{uuid.New()}

			mockGetKeyPackages := func(_ context.Context, _ *so.Config, _ []uuid.UUID) (map[uuid.UUID]*pbfrost.KeyPackage, error) {
				return nil, errors.New("database connection failed")
			}

			_, err := helper.GetSigningCommitmentsInternal(t.Context(), config, keyshareIDs, mockGetKeyPackages, 1, mockFrostSignerFactory)
			if err == nil {
				t.Fatal("Expected error when getKeyPackages fails, got nil")
			}

			if err.Error() != "database connection failed" {
				t.Errorf("Expected error 'database connection failed', got '%s'", err.Error())
			}
		})
	})
}

// TestNewSigningJobEdgeCases tests edge cases for NewSigningJob
func TestNewSigningJobEdgeCases(t *testing.T) {
	t.Run("InvalidKeyshare", func(t *testing.T) {
		// Test with nil keyshare
		proto := &pbspark.SigningJob{
			SigningPublicKey:       pubKey.Serialize(),
			RawTx:                  mockTxBuf(t, []int64{1000000}),
			SigningNonceCommitment: &pbcommon.SigningCommitment{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
		}

		prevOutput := &wire.TxOut{
			Value:    1000000,
			PkScript: []byte("test-pkscript"),
		}

		_, _, err := helper.NewSigningJob(nil, proto, prevOutput)
		if err == nil {
			t.Fatal("Expected error with nil keyshare, got nil")
		}
	})

	t.Run("InvalidPrevOutput", func(t *testing.T) {
		keyshare := &ent.SigningKeyshare{
			ID:        uuid.New(),
			PublicKey: pubKey,
		}

		proto := &pbspark.SigningJob{
			SigningPublicKey:       pubKey.Serialize(),
			RawTx:                  mockTxBuf(t, []int64{1000000}),
			SigningNonceCommitment: &pbcommon.SigningCommitment{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
		}

		// Test with nil prevOutput
		_, _, err := helper.NewSigningJob(keyshare, proto, nil)
		if err == nil {
			t.Fatal("Expected error with nil prevOutput, got nil")
		}
	})

	t.Run("InvalidPublicKeyCombination", func(t *testing.T) {
		keyshare := &ent.SigningKeyshare{
			ID:        uuid.New(),
			PublicKey: pubKey,
		}

		// Test with invalid proto public key (wrong length)
		proto := &pbspark.SigningJob{
			SigningPublicKey:       []byte("invalid-key"), // Wrong length
			RawTx:                  mockTxBuf(t, []int64{1000000}),
			SigningNonceCommitment: &pbcommon.SigningCommitment{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
		}

		prevOutput := &wire.TxOut{
			Value:    1000000,
			PkScript: []byte("test-pkscript"),
		}

		_, _, err := helper.NewSigningJob(keyshare, proto, prevOutput)
		if err == nil {
			t.Fatal("Expected error with invalid public key, got nil")
		}
	})
}
