package handler

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pbfrost "github.com/lightsparkdev/spark/proto/frost"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authn"
	"github.com/lightsparkdev/spark/so/authninternal"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	sparktesting "github.com/lightsparkdev/spark/testing"
	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/emptypb"
)

// mockFrostServiceClientConnection implements the FrostServiceClientConnection interface for testing
type mockFrostServiceClientConnection struct{}

func (m *mockFrostServiceClientConnection) StartFrostServiceClient(h *LightningHandler) (pbfrost.FrostServiceClient, error) {
	return &mockFrostServiceClient{}, nil
}

func (m *mockFrostServiceClientConnection) Close() {
}

// mockFrostServiceClient implements the FrostServiceClient interface for testing
type mockFrostServiceClient struct{}

func (m *mockFrostServiceClient) Echo(ctx context.Context, in *pbfrost.EchoRequest, opts ...grpc.CallOption) (*pbfrost.EchoResponse, error) {
	return &pbfrost.EchoResponse{}, nil
}

func (m *mockFrostServiceClient) DkgRound1(ctx context.Context, in *pbfrost.DkgRound1Request, opts ...grpc.CallOption) (*pbfrost.DkgRound1Response, error) {
	return &pbfrost.DkgRound1Response{}, nil
}

func (m *mockFrostServiceClient) DkgRound2(ctx context.Context, in *pbfrost.DkgRound2Request, opts ...grpc.CallOption) (*pbfrost.DkgRound2Response, error) {
	return &pbfrost.DkgRound2Response{}, nil
}

func (m *mockFrostServiceClient) DkgRound3(ctx context.Context, in *pbfrost.DkgRound3Request, opts ...grpc.CallOption) (*pbfrost.DkgRound3Response, error) {
	return &pbfrost.DkgRound3Response{}, nil
}

func (m *mockFrostServiceClient) FrostNonce(ctx context.Context, in *pbfrost.FrostNonceRequest, opts ...grpc.CallOption) (*pbfrost.FrostNonceResponse, error) {
	return &pbfrost.FrostNonceResponse{}, nil
}

func (m *mockFrostServiceClient) SignFrost(ctx context.Context, in *pbfrost.SignFrostRequest, opts ...grpc.CallOption) (*pbfrost.SignFrostResponse, error) {
	return &pbfrost.SignFrostResponse{}, nil
}

func (m *mockFrostServiceClient) AggregateFrost(ctx context.Context, in *pbfrost.AggregateFrostRequest, opts ...grpc.CallOption) (*pbfrost.AggregateFrostResponse, error) {
	return &pbfrost.AggregateFrostResponse{}, nil
}

func (m *mockFrostServiceClient) ValidateSignatureShare(ctx context.Context, in *pbfrost.ValidateSignatureShareRequest, opts ...grpc.CallOption) (*emptypb.Empty, error) {
	// Mock successful validation
	return &emptypb.Empty{}, nil
}

func TestValidateDuplicateLeaves(t *testing.T) {
	ctx, dbCtx := db.NewTestSQLiteContext(t, t.Context())
	defer dbCtx.Close()

	config := &so.Config{FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{}}
	lightningHandler := NewLightningHandler(config)

	// Helper function to create a UserSignedTxSigningJob
	createSigningJob := func(leafID string) *pb.UserSignedTxSigningJob {
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

	t.Run("duplicate in leavesToSend", func(t *testing.T) {
		leavesToSend := []*pb.UserSignedTxSigningJob{
			createSigningJob("leaf1"),
			createSigningJob("leaf1"), // Duplicate
			createSigningJob("leaf2"),
		}

		err := lightningHandler.ValidateDuplicateLeaves(ctx, leavesToSend, []*pb.UserSignedTxSigningJob{}, []*pb.UserSignedTxSigningJob{})
		require.ErrorContains(t, err, "duplicate leaf id: leaf1")
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
}

// Note: StorePreimageShare requires complex cryptographic validation
// that's difficult to mock in unit tests. These tests focus on basic validation.
func TestStorePreimageShareEdgeCases(t *testing.T) {
	ctx, dbCtx := db.NewTestSQLiteContext(t, t.Context())
	defer dbCtx.Close()

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
	})
}

func TestGetSigningCommitments(t *testing.T) {
	ctx, dbCtx := db.NewTestSQLiteContext(t, t.Context())
	defer dbCtx.Close()

	signingOperators, err := sparktesting.GetAllSigningOperators()
	require.NoError(t, err)

	config := &so.Config{
		SigningOperatorMap:         signingOperators,
		FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{},
	}

	lightningHandler := NewLightningHandler(config)

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
			name:        "non-existent node ID",
			nodeIds:     []string{"12345678-1234-1234-1234-123456789012"},
			count:       1,
			expectError: false,
			expectEmpty: true,
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &pb.GetSigningCommitmentsRequest{
				NodeIds: tt.nodeIds,
				Count:   tt.count,
			}

			resp, err := lightningHandler.GetSigningCommitments(ctx, req)

			if tt.expectError {
				require.ErrorContains(t, err, tt.expectedErrMsg)
				assert.Nil(t, resp)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, resp)
				if tt.expectEmpty {
					assert.Empty(t, resp.SigningCommitments)
				}
			}
		})
	}
}

func TestValidatePreimage_InvalidPreimage_Errors(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{})
	ctx, dbCtx := db.NewTestSQLiteContext(t, t.Context())
	defer dbCtx.Close()

	config := &so.Config{FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{}}
	lightningHandler := NewLightningHandler(config)

	identityKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	tests := []struct {
		name           string
		paymentHash    []byte
		preimage       []byte
		identityPubKey keys.Public
		expectedErrMsg string
	}{
		{
			name:           "invalid preimage - hash mismatch",
			paymentHash:    bytes.Repeat([]byte{0}, 32),
			preimage:       []byte("wrong_preimage_that_doesnt_match_hash"),
			identityPubKey: identityKey,
			expectedErrMsg: "invalid preimage",
		},
		{
			name:           "non-existent preimage request",
			paymentHash:    []byte("some_hash_that_matches_preimage_"),
			preimage:       []byte("test_preimage_32_bytes_long_____"),
			identityPubKey: identityKey,
			expectedErrMsg: "invalid preimage",
		},
		{
			name:           "empty payment hash",
			paymentHash:    []byte{},
			preimage:       []byte("test_preimage"),
			identityPubKey: identityKey,
			expectedErrMsg: "payment hash cannot be empty",
		},
		{
			name:           "empty preimage",
			paymentHash:    []byte("payment_hash_32_bytes_long______"),
			preimage:       []byte{},
			identityPubKey: identityKey,
			expectedErrMsg: "preimage cannot be empty",
		},
		{
			name:           "nil identity public key",
			paymentHash:    []byte("payment_hash_32_bytes_long______"),
			preimage:       []byte("test_preimage_32_bytes_long_____"),
			identityPubKey: keys.Public{},
			expectedErrMsg: "identity public key cannot be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &pb.ProvidePreimageRequest{
				PaymentHash:       tt.paymentHash,
				Preimage:          tt.preimage,
				IdentityPublicKey: tt.identityPubKey.Serialize(),
			}

			transfer, err := lightningHandler.ValidatePreimage(ctx, req)

			require.ErrorContains(t, err, tt.expectedErrMsg)
			assert.Nil(t, transfer)
		})
	}
}

func TestReturnLightningPayment(t *testing.T) {
	ctx, dbCtx := db.NewTestSQLiteContext(t, t.Context())
	defer dbCtx.Close()

	rng := rand.NewChaCha8([32]byte{})
	userIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	config := &so.Config{FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{}}
	lightningHandler := NewLightningHandler(config)

	t.Run("non-existent payment hash", func(t *testing.T) {
		req := &pb.ReturnLightningPaymentRequest{
			PaymentHash:           []byte("non_existent_payment_hash_______"),
			UserIdentityPublicKey: userIdentityPubKey.Serialize(),
		}

		resp, err := lightningHandler.ReturnLightningPayment(ctx, req, true) // internal=true to skip auth
		require.ErrorContains(t, err, "unable to get preimage request")
		assert.Nil(t, resp)
	})
}

// Note: validateNodeOwnership and validateHasSession are private methods,
// so we test them indirectly through GetSigningCommitments which calls validateHasSession
func TestValidateGetPreimageRequestEdgeErrorCases(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{1})

	ctx, dbCtx := db.NewTestSQLiteContext(t, t.Context())
	defer dbCtx.Close()

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
			err := lightningHandler.ValidateGetPreimageRequest(
				ctx,
				[]byte("payment_hash_32_bytes_long______"),
				tt.cpfpTransactions,
				tt.directTransactions,
				tt.directFromCpfpTransactions,
				&pb.InvoiceAmount{ValueSats: 1000},
				tt.destinationPubKey,
				0,
				pb.InitiatePreimageSwapRequest_REASON_SEND,
				false,
			)

			require.ErrorContains(t, err, tt.expectedErrMsg)
		})
	}
}

func TestInitiatePreimageSwapEdgeCases_Invalid_Errors(t *testing.T) {
	ctx, dbCtx := db.NewTestSQLiteContext(t, t.Context())
	defer dbCtx.Close()

	config := &so.Config{FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{}}
	lightningHandler := NewLightningHandler(config)

	rng := rand.NewChaCha8([32]byte{})
	ownerIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	receiverIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	tests := []struct {
		name           string
		setUpRequest   func() *pb.InitiatePreimageSwapRequest
		expectedErrMsg string
	}{
		{
			name: "nil transfer",
			setUpRequest: func() *pb.InitiatePreimageSwapRequest {
				return &pb.InitiatePreimageSwapRequest{Transfer: nil}
			},
			expectedErrMsg: "transfer is required",
		},
		{
			name: "empty leaves to send",
			setUpRequest: func() *pb.InitiatePreimageSwapRequest {
				return &pb.InitiatePreimageSwapRequest{
					Transfer: &pb.StartUserSignedTransferRequest{
						LeavesToSend:           []*pb.UserSignedTxSigningJob{}, // empty
						OwnerIdentityPublicKey: ownerIdentityPubKey.Serialize(),
					},
				}
			},
			expectedErrMsg: "at least one cpfp leaf tx must be provided",
		},
		{
			name: "nil owner identity public key",
			setUpRequest: func() *pb.InitiatePreimageSwapRequest {
				return &pb.InitiatePreimageSwapRequest{
					Transfer: &pb.StartUserSignedTransferRequest{
						LeavesToSend: []*pb.UserSignedTxSigningJob{
							{LeafId: "test-leaf"},
						},
						OwnerIdentityPublicKey: nil,
					},
				}
			},
			expectedErrMsg: "owner identity public key is required",
		},
		{
			name: "nil receiver identity public key",
			setUpRequest: func() *pb.InitiatePreimageSwapRequest {
				return &pb.InitiatePreimageSwapRequest{
					Transfer: &pb.StartUserSignedTransferRequest{
						LeavesToSend: []*pb.UserSignedTxSigningJob{
							{LeafId: "test-leaf"},
						},
						OwnerIdentityPublicKey:    ownerIdentityPubKey.Serialize(),
						ReceiverIdentityPublicKey: nil,
					},
				}
			},
			expectedErrMsg: "receiver identity public key is required",
		},
		{
			name: "fee not allowed for receive",
			setUpRequest: func() *pb.InitiatePreimageSwapRequest {
				return &pb.InitiatePreimageSwapRequest{
					Transfer: &pb.StartUserSignedTransferRequest{
						LeavesToSend: []*pb.UserSignedTxSigningJob{
							{LeafId: "test-leaf"},
						},
						OwnerIdentityPublicKey:    ownerIdentityPubKey.Serialize(),
						ReceiverIdentityPublicKey: receiverIdentityPubKey.Serialize(),
					},
					Reason:  pb.InitiatePreimageSwapRequest_REASON_RECEIVE,
					FeeSats: 100, // fee not allowed for receive
				}
			},
			expectedErrMsg: "fee is not allowed for receive preimage swap",
		},
		{
			name: "too many transactions exceeds knob limit",
			setUpRequest: func() *pb.InitiatePreimageSwapRequest {
				// Create 101 transactions to exceed the default limit of 100
				leaves := make([]*pb.UserSignedTxSigningJob, 101)
				for i := 0; i < 101; i++ {
					leaves[i] = &pb.UserSignedTxSigningJob{
						LeafId:                 fmt.Sprintf("550e8400-e29b-41d4-a716-44665544%04d", i),
						SigningCommitments:     &pb.SigningCommitments{SigningCommitments: map[string]*pbcommon.SigningCommitment{}},
						SigningNonceCommitment: &pbcommon.SigningCommitment{},
						RawTx:                  []byte("dummy_transaction_data_for_testing"),
					}
				}
				return &pb.InitiatePreimageSwapRequest{
					PaymentHash: []byte("payment_hash_32_bytes_long______"),
					Transfer: &pb.StartUserSignedTransferRequest{
						LeavesToSend:              leaves,
						OwnerIdentityPublicKey:    ownerIdentityPubKey.Serialize(),
						ReceiverIdentityPublicKey: receiverIdentityPubKey.Serialize(),
					},
					Reason: pb.InitiatePreimageSwapRequest_REASON_SEND,
				}
			},
			expectedErrMsg: "too many transactions: 101, maximum allowed: 100",
		},
		{
			name: "nil leaves to send",
			setUpRequest: func() *pb.InitiatePreimageSwapRequest {
				return &pb.InitiatePreimageSwapRequest{
					Transfer: &pb.StartUserSignedTransferRequest{
						LeavesToSend:              nil, // nil instead of empty
						OwnerIdentityPublicKey:    ownerIdentityPubKey.Serialize(),
						ReceiverIdentityPublicKey: receiverIdentityPubKey.Serialize(),
					},
				}
			},
			expectedErrMsg: "at least one cpfp leaf tx must be provided",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := tt.setUpRequest()

			resp, err := lightningHandler.InitiatePreimageSwap(ctx, req)

			require.ErrorContains(t, err, tt.expectedErrMsg)
			assert.Nil(t, resp)
		})
	}
}

// Regression test for https://linear.app/lightsparkdev/issue/LIG-8044
// Ensure that only a node owner can initiate a preimage swap for that node.
func TestPreimageSwapAuthorizationBugRegression(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{1})

	ctx, dbCtx := db.NewTestSQLiteContext(t, t.Context())
	defer dbCtx.Close()

	// Valid 33-byte compressed secp256k1 public key for destination
	validPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize()
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
		tree, err := tx.Tree.Create().
			SetOwnerIdentityPubkey(validPubKey).
			SetStatus(st.TreeStatusAvailable).
			SetNetwork(st.NetworkMainnet).
			SetBaseTxid([]byte("test_base_txid_32_bytes_long_")).
			SetVout(0).
			Save(authenticatedCtx)
		require.NoError(t, err)

		wrongKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
		// Create a keyshare with proper 33-byte public keys
		secretShare := keys.MustGeneratePrivateKeyFromRand(rng).Serialize()
		keyshare, err := tx.SigningKeyshare.Create().
			SetStatus(st.KeyshareStatusInUse).
			SetSecretShare(secretShare).
			SetPublicShares(map[string][]byte{"operator1": wrongKey.Serialize()}).
			SetPublicKey(sessionIdentityKey.Public().Serialize()).
			SetMinSigners(2).
			SetCoordinatorIndex(1).
			Save(authenticatedCtx)
		require.NoError(t, err)

		// Create a tree node with a different owner than the session
		nodeID, err := uuid.Parse(validTx.LeafId)
		require.NoError(t, err)

		correctScript, err := common.P2TRScriptFromPubKey(wrongKey)
		require.NoError(t, err)

		// Create a minimal transaction with the correct P2TR output script
		// Format: version(4) + input_count(1) + input(36) + output_count(1) + output_value(8) + output_script_len(1) + output_script + locktime(4)
		testTx := []byte{0x02, 0x00, 0x00, 0x00} // version = 2
		testTx = append(testTx, 0x01)            // input count = 1
		// Add a dummy input (prev hash + vout + script_len + scriptSig + sequence)
		testTx = append(testTx, make([]byte, 32)...)                            // prev hash (32 zeros)
		testTx = append(testTx, 0x00, 0x00, 0x00, 0x00)                         // vout = 0
		testTx = append(testTx, 0x00)                                           // scriptSig length = 0
		testTx = append(testTx, 0xff, 0xff, 0xff, 0xff)                         // sequence
		testTx = append(testTx, 0x01)                                           // output count = 1
		testTx = append(testTx, 0xe8, 0x03, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00) // value = 1000 satoshis
		testTx = append(testTx, byte(len(correctScript)))                       // script length
		testTx = append(testTx, correctScript...)                               // the correct P2TR script
		testTx = append(testTx, 0x00, 0x00, 0x00, 0x00)                         // locktime = 0

		_, err = tx.TreeNode.Create().
			SetTree(tree).
			SetID(nodeID). // Use the specific ID from the test
			SetValue(1000).
			SetStatus(st.TreeNodeStatusAvailable).
			SetVerifyingPubkey(keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize()).
			SetOwnerIdentityPubkey(keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize()).
			SetOwnerSigningPubkey(keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize()).
			SetRawTx(testTx).
			SetVout(0).
			SetSigningKeyshare(keyshare).
			Save(authenticatedCtx)
		require.NoError(t, err)

		// Update the test transaction to use our generated transaction bytes
		validTx.RawTx = testTx

		mockFrostConnection := &mockFrostServiceClientConnection{}

		// This test should fail because the node is not the owner of the leaf.
		err = lightningHandler.validateGetPreimageRequestWithFrostServiceClientFactory(
			authenticatedCtx,
			mockFrostConnection,
			paymentHash,
			[]*pb.UserSignedTxSigningJob{validTx},
			[]*pb.UserSignedTxSigningJob{},
			[]*pb.UserSignedTxSigningJob{},
			&pb.InvoiceAmount{ValueSats: 1000},
			wrongKey,
			0,
			pb.InitiatePreimageSwapRequest_REASON_SEND,
			true, // validateNodeOwnership = true
		)

		require.ErrorContains(t, err, "not owned by the authenticated identity public key")
	})
}

// Regression test for https://linear.app/lightsparkdev/issue/LIG-8086
func TestValidateGetPreimageRequestMismatchedAmounts(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{1})

	ctx, dbCtx := db.NewTestSQLiteContext(t, t.Context())
	defer dbCtx.Close()

	config := &so.Config{FrostGRPCConnectionFactory: &sparktesting.TestGRPCConnectionFactory{}}
	lightningHandler := NewLightningHandler(config)

	validPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	paymentHash := []byte("test_payment_hash_32_bytes_long_")

	tx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	tree, err := tx.Tree.Create().
		SetOwnerIdentityPubkey(validPubKey.Serialize()).
		SetStatus(st.TreeStatusAvailable).
		SetNetwork(st.NetworkMainnet).
		SetBaseTxid([]byte("test_base_txid_32_bytes_long_")).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)

	keyshare, err := tx.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusInUse).
		SetSecretShare([]byte("test_secret_share_32_bytes_long_")).
		SetPublicShares(map[string][]byte{"operator1": validPubKey.Serialize()}).
		SetPublicKey(validPubKey.Serialize()).
		SetMinSigners(2).
		SetCoordinatorIndex(1).
		Save(ctx)
	require.NoError(t, err)

	nodeID := uuid.MustParse("550e8400-e29b-41d4-a716-446655440000")

	// Create a transaction with 500 sats output (different from expected 1000)
	// Use the same pattern as the existing createMockSigningJob function
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
	// Add 500 sats value (little endian)
	valueBytes := make([]byte, 8)
	value := uint64(500)
	for i := 0; i < 8; i++ {
		valueBytes[i] = byte(value >> (i * 8))
	}
	mockTx = append(mockTx, valueBytes...)
	// Add the correct P2TR script for the destination pubkey
	correctScript, err := common.P2TRScriptFromPubKey(validPubKey)
	require.NoError(t, err)
	mockScript := []byte{byte(len(correctScript))}    // script length
	mockScript = append(mockScript, correctScript...) // the correct P2TR script
	mockTx = append(mockTx, mockScript...)
	// Add locktime
	mockTx = append(mockTx, 0x00, 0x00, 0x00, 0x00)

	_, err = tx.TreeNode.Create().
		SetTree(tree).
		SetID(nodeID).
		SetValue(500). // This is the value in the tree node, but RawTx will also have 500 sats
		SetStatus(st.TreeNodeStatusAvailable).
		SetVerifyingPubkey(validPubKey.Serialize()).
		SetOwnerIdentityPubkey(validPubKey.Serialize()).
		SetOwnerSigningPubkey(validPubKey.Serialize()).
		SetRawTx(mockTx).
		SetDirectTx(mockTx). // Set direct_tx field which is required for direct transaction validation
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
		RawTx:         mockTx, // Contains 500 sats output
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
		&pb.InvoiceAmount{ValueSats: 1000},   // Expected 1000 sats but getting 500
		validPubKey,
		0,
		pb.InitiatePreimageSwapRequest_REASON_SEND,
		false, // validateNodeOwnership = false for this test
	)

	require.ErrorContains(t, err, "invalid amount, expected: 1000 or more, got: 500")
}

// Regression test for https://linear.app/lightsparkdev/issue/LIG-8043
// Validates that duplicate leaves are rejected in the SendLightning flow,
// since otherwise they would allow double-spending of Spark leaves via
// Lightning.
func TestSendLightningLeafDuplicationBug(t *testing.T) {
	ctx, dbCtx := db.NewTestSQLiteContext(t, t.Context())
	defer dbCtx.Close()

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
		valueBytes := make([]byte, 8)
		for i := 0; i < 8; i++ {
			valueBytes[i] = byte(value >> (i * 8))
		}
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
		// Create request with duplicated leaves
		req := &pb.InitiatePreimageSwapRequest{
			PaymentHash: []byte("payment_hash_32_bytes_long______"),
			Transfer: &pb.StartUserSignedTransferRequest{
				TransferId: "transfer-id-123",
				LeavesToSend: []*pb.UserSignedTxSigningJob{
					originalLeaf,
					duplicatedLeaf, // Same leaf ID - this should be rejected but currently isn't
				},
				OwnerIdentityPublicKey:    ownerIdentityPubKey.Serialize(),
				ReceiverIdentityPublicKey: []byte("receiver_identity_key_32_bytes__"),
			},
			InvoiceAmount: &pb.InvoiceAmount{
				ValueSats: 1000, // Invoice is for 1000 sats, but we're attempting to send 2000 sats due to duplication
			},
			Reason:  pb.InitiatePreimageSwapRequest_REASON_SEND,
			FeeSats: 0,
		}

		_, err := lightningHandler.InitiatePreimageSwap(ctx, req)

		require.ErrorContains(t, err, "duplicate leaf id")
	})
}
