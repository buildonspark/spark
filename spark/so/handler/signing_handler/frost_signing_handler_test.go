package signing_handler

import (
	"errors"
	"math"
	"testing"

	"github.com/google/uuid"
	sparkgrpc "github.com/lightsparkdev/spark/common/grpc"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pb "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestFrostSigningHandler_GenerateRandomNonces(t *testing.T) {
	tests := []struct {
		name        string
		count       uint32
		expectError bool
	}{
		{
			name:        "Generate single nonce",
			count:       1,
			expectError: false,
		},
		{
			name:        "Generate multiple nonces",
			count:       5,
			expectError: false,
		},
		{
			name:        "Generate zero nonces",
			count:       0,
			expectError: false,
		},
		{
			name:        "Generate large number of nonces",
			count:       10,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, _ := db.NewTestSQLiteContext(t)

			config := &so.Config{FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{}}
			handler := NewFrostSigningHandler(config)

			resp, err := handler.GenerateRandomNonces(ctx, tt.count)

			if tt.expectError {
				require.Error(t, err)
				assert.Nil(t, resp)
				return
			}

			// Verify response
			require.NoError(t, err)
			assert.NotNil(t, resp)
			assert.Len(t, resp.GetSigningCommitments(), int(tt.count))

			// Verify each commitment
			for i, commitment := range resp.GetSigningCommitments() {
				assert.NotNil(t, commitment, "Commitment %d should not be nil", i)
				assert.Len(t, commitment.GetBinding(), 33, "Commitment %d binding should be 33 bytes (compressed public key)", i)
				assert.Len(t, commitment.GetHiding(), 33, "Commitment %d hiding should be 33 bytes (compressed public key)", i)
			}

			// Verify that nonces were stored in database
			dbTx, err := ent.GetDbFromContext(ctx)
			require.NoError(t, err)

			nonces, err := dbTx.SigningNonce.Query().All(ctx)
			require.NoError(t, err)
			assert.Len(t, nonces, int(tt.count), "Expected %d nonces in database", tt.count)

			// Verify that each nonce has a corresponding commitment
			for _, nonce := range nonces {
				assert.NotEmpty(t, nonce.NonceCommitment, "Nonce commitment should not be empty")
				assert.Len(t, nonce.Nonce.MarshalBinary(), 64, "Nonce should be 64 bytes (32 binding + 32 hiding)")
			}
		})
	}
}

func TestFrostSigningHandler_GenerateRandomNonces_UniqueCommitments(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)

	config := &so.Config{FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{}}
	handler := NewFrostSigningHandler(config)

	// Generate multiple nonces
	const count = 10
	resp, err := handler.GenerateRandomNonces(ctx, count)
	require.NoError(t, err)
	assert.Len(t, resp.GetSigningCommitments(), count)

	// Verify that all commitments are unique
	commitmentMap := make(map[string]bool)
	for i, commitment := range resp.GetSigningCommitments() {
		// Create a unique key for each commitment by combining binding and hiding
		key := string(commitment.GetBinding()) + string(commitment.GetHiding())
		assert.NotContains(t, commitmentMap, key, "Commitment %d should be unique", i)
		commitmentMap[key] = true
	}

	// Verify that we have exactly the expected number of unique commitments
	assert.Len(t, commitmentMap, count, "Should have exactly %d unique commitments", count)
}

func TestFrostSigningHandler_FrostRound1RequestBounds(t *testing.T) {
	t.Run("nil request", func(t *testing.T) {
		handler := NewFrostSigningHandler(&so.Config{FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{}})

		resp, err := handler.FrostRound1(t.Context(), nil)
		require.Nil(t, resp)
		require.Error(t, err)
		require.Equal(t, codes.InvalidArgument, status.Code(err))
		require.ErrorContains(t, err, "request is required")
	})

	t.Run("derived count uses count times keyshare ids", func(t *testing.T) {
		ctx, _ := db.NewTestSQLiteContext(t)
		handler := NewFrostSigningHandler(&so.Config{FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{}})

		resp, err := handler.FrostRound1(ctx, &pb.FrostRound1Request{
			Count:       2,
			KeyshareIds: []string{"keyshare-a", "keyshare-b"},
		})
		require.NoError(t, err)
		require.Len(t, resp.GetSigningCommitments(), 4)
	})

	t.Run("random nonce count overrides derived count", func(t *testing.T) {
		ctx, _ := db.NewTestSQLiteContext(t)
		handler := NewFrostSigningHandler(&so.Config{FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{}})

		resp, err := handler.FrostRound1(ctx, &pb.FrostRound1Request{
			RandomNonceCount: 3,
			Count:            2,
			KeyshareIds:      []string{"keyshare-a", "keyshare-b"},
		})
		require.NoError(t, err)
		require.Len(t, resp.GetSigningCommitments(), 3)
	})

	for _, tc := range []struct {
		name string
		req  *pb.FrostRound1Request
	}{
		{
			name: "random nonce count exceeds cap",
			req: &pb.FrostRound1Request{
				RandomNonceCount: 1_000_001,
			},
		},
		{
			name: "derived count overflow exceeds cap",
			req: &pb.FrostRound1Request{
				Count:       math.MaxUint32,
				KeyshareIds: []string{"keyshare-a", "keyshare-b"},
			},
		},
		{
			name: "derived product exceeds cap while operands are under cap",
			req: &pb.FrostRound1Request{
				Count:       1000,
				KeyshareIds: make([]string, 1001),
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := NewFrostSigningHandler(&so.Config{FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{}})

			resp, err := handler.FrostRound1(t.Context(), tc.req)
			require.Nil(t, resp)
			require.Error(t, err)
			require.Equal(t, codes.InvalidArgument, status.Code(err))
			require.ErrorContains(t, err, "too many nonces requested")
		})
	}
}

func TestFrostSigningHandler_FrostRound2RejectsMalformedRequestsBeforeDB(t *testing.T) {
	handler := NewFrostSigningHandler(&so.Config{FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{}})

	for _, tc := range []struct {
		name    string
		req     *pb.FrostRound2Request
		wantErr string
	}{
		{
			name:    "nil request",
			req:     nil,
			wantErr: "request is required",
		},
		{
			name:    "empty signing jobs",
			req:     &pb.FrostRound2Request{},
			wantErr: "signing_jobs is required",
		},
		{
			name: "nil signing job",
			req: &pb.FrostRound2Request{
				SigningJobs: []*pb.SigningJob{nil},
			},
			wantErr: "signing_jobs[0] is required",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := handler.FrostRound2(t.Context(), tc.req)
			require.Nil(t, resp)
			require.Error(t, err)
			require.Equal(t, codes.InvalidArgument, status.Code(err))
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

func TestFrostSigningHandler_FrostRound2RejectsMissingKeyshareBeforeSigner(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)
	frostFactory := &rejectingFrostConnectionFactory{}
	config := &so.Config{
		Identifier:                 "operator-1",
		FrostGRPCConnectionFactory: frostFactory,
	}
	handler := NewFrostSigningHandler(config)

	nonceResp, err := handler.GenerateRandomNonces(ctx, 1)
	require.NoError(t, err)
	require.Len(t, nonceResp.GetSigningCommitments(), 1)

	missingKeyshareID := uuid.NewString()
	resp, err := handler.FrostRound2(ctx, &pb.FrostRound2Request{
		SigningJobs: []*pb.SigningJob{{
			JobId:        "job-1",
			KeyshareId:   missingKeyshareID,
			Message:      []byte("message"),
			VerifyingKey: []byte("verifying-key"),
			Commitments: map[string]*pbcommon.SigningCommitment{
				config.Identifier: nonceResp.GetSigningCommitments()[0],
			},
		}},
	})
	require.Nil(t, resp)
	require.Error(t, err)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.ErrorContains(t, err, "signing keyshare")
	require.False(t, frostFactory.called, "missing keyshare IDs must be rejected before calling the signer")
}

type rejectingFrostConnectionFactory struct {
	called bool
}

func (f *rejectingFrostConnectionFactory) NewFrostGRPCConnection(string) (*grpc.ClientConn, error) {
	f.called = true
	return nil, errors.New("frost signer should not be called")
}

func (f *rejectingFrostConnectionFactory) SetTimeoutProvider(sparkgrpc.TimeoutProvider) {}

func TestFrostSigningHandler_NewFrostSigningHandler(t *testing.T) {
	config := &so.Config{FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{}}
	handler := NewFrostSigningHandler(config)

	assert.NotNil(t, handler)
	assert.Equal(t, config, handler.config)
}

func TestFrostSigningHandler_GenerateRandomNonces_DatabaseError(t *testing.T) {
	// Test with a context that doesn't have a database connection
	ctx := t.Context()
	config := &so.Config{FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{}}
	handler := NewFrostSigningHandler(config)

	// This should fail because there's no database context
	resp, err := handler.GenerateRandomNonces(ctx, 1)
	require.Error(t, err)
	assert.Nil(t, resp)
}

func TestRetryFingerprintBindsSigningJobInputs(t *testing.T) {
	newJob := func() *pb.SigningJob {
		return &pb.SigningJob{
			Message:          []byte("message"),
			VerifyingKey:     []byte("verifying-key"),
			AdaptorPublicKey: []byte("adaptor-public-key"),
			UserCommitments: &pbcommon.SigningCommitment{
				Hiding:  []byte("user-hiding"),
				Binding: []byte("user-binding"),
			},
			Commitments: map[string]*pbcommon.SigningCommitment{
				"operator-b": {
					Hiding:  []byte("operator-b-hiding"),
					Binding: []byte("operator-b-binding"),
				},
				"operator-a": {
					Hiding:  []byte("operator-a-hiding"),
					Binding: []byte("operator-a-binding"),
				},
			},
		}
	}

	baseFingerprint := retryFingerprint(newJob())

	sameJobDifferentMapOrder := &pb.SigningJob{
		Message:          []byte("message"),
		VerifyingKey:     []byte("verifying-key"),
		AdaptorPublicKey: []byte("adaptor-public-key"),
		UserCommitments: &pbcommon.SigningCommitment{
			Hiding:  []byte("user-hiding"),
			Binding: []byte("user-binding"),
		},
		Commitments: map[string]*pbcommon.SigningCommitment{
			"operator-a": {
				Hiding:  []byte("operator-a-hiding"),
				Binding: []byte("operator-a-binding"),
			},
			"operator-b": {
				Hiding:  []byte("operator-b-hiding"),
				Binding: []byte("operator-b-binding"),
			},
		},
	}
	assert.Equal(t, baseFingerprint, retryFingerprint(sameJobDifferentMapOrder))

	for _, tc := range []struct {
		name   string
		mutate func(*pb.SigningJob)
	}{
		{
			name: "message",
			mutate: func(job *pb.SigningJob) {
				job.Message = []byte("other-message")
			},
		},
		{
			name: "verifying key",
			mutate: func(job *pb.SigningJob) {
				job.VerifyingKey = []byte("other-verifying-key")
			},
		},
		{
			name: "adaptor key",
			mutate: func(job *pb.SigningJob) {
				job.AdaptorPublicKey = []byte("other-adaptor-public-key")
			},
		},
		{
			name: "user commitment",
			mutate: func(job *pb.SigningJob) {
				job.UserCommitments.Binding = []byte("other-user-binding")
			},
		},
		{
			name: "operator identifier",
			mutate: func(job *pb.SigningJob) {
				job.Commitments["operator-c"] = job.GetCommitments()["operator-b"]
				delete(job.GetCommitments(), "operator-b")
			},
		},
		{
			name: "operator commitment",
			mutate: func(job *pb.SigningJob) {
				job.Commitments["operator-b"].Hiding = []byte("other-operator-hiding")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			job := newJob()
			tc.mutate(job)
			assert.NotEqual(t, baseFingerprint, retryFingerprint(job))
		})
	}
}
