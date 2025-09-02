package protoconverter

import (
	"bytes"
	"crypto/sha256"
	"github.com/lightsparkdev/spark/common/keys"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/google/go-cmp/cmp"
	"google.golang.org/protobuf/testing/protocmp"

	pb "github.com/lightsparkdev/spark/proto/spark"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
)

var (
	rng                  = rand.NewChaCha8([32]byte{})
	ownerPubKey          = keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize()
	issuerPubKey         = keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize()
	revocationCommitment = keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize()
	tokenPubKey          = keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize()
	op1Key               = keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize()
	op2Key               = keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize()
	tokenAmount          = bytes.Repeat([]byte{1}, 16)
	prevHash1            = sha256.Sum256([]byte{0})
	prevHash2            = sha256.Sum256([]byte{1})
)

func TestSparkTokenTransactionFromTokenProto(t *testing.T) {
	tests := []struct {
		name  string
		input *tokenpb.TokenTransaction
		want  *pb.TokenTransaction
	}{
		{
			name: "valid mint transaction",
			input: &tokenpb.TokenTransaction{
				TokenOutputs: []*tokenpb.TokenOutput{
					{
						Id:                            proto.String("output1"),
						OwnerPublicKey:                ownerPubKey,
						RevocationCommitment:          revocationCommitment,
						WithdrawBondSats:              proto.Uint64(1000),
						WithdrawRelativeBlockLocktime: proto.Uint64(100),
						TokenPublicKey:                tokenPubKey,
						TokenAmount:                   tokenAmount,
					},
				},
				SparkOperatorIdentityPublicKeys: [][]byte{op1Key, op2Key},
				Network:                         pb.Network_MAINNET,
				TokenInputs: &tokenpb.TokenTransaction_MintInput{
					MintInput: &tokenpb.TokenMintInput{
						IssuerPublicKey: issuerPubKey,
					},
				},
				ClientCreatedTimestamp: timestamppb.New(time.UnixMilli(1234567890)),
			},
			want: &pb.TokenTransaction{
				TokenOutputs: []*pb.TokenOutput{
					{
						Id:                            proto.String("output1"),
						OwnerPublicKey:                ownerPubKey,
						RevocationCommitment:          revocationCommitment,
						WithdrawBondSats:              proto.Uint64(1000),
						WithdrawRelativeBlockLocktime: proto.Uint64(100),
						TokenPublicKey:                tokenPubKey,
						TokenAmount:                   tokenAmount,
					},
				},
				SparkOperatorIdentityPublicKeys: [][]byte{op1Key, op2Key},
				Network:                         pb.Network_MAINNET,
				TokenInputs: &pb.TokenTransaction_MintInput{
					MintInput: &pb.TokenMintInput{
						IssuerPublicKey:         issuerPubKey,
						IssuerProvidedTimestamp: 1234567890,
					},
				},
			},
		},
		{
			name: "zero time stamp",
			input: &tokenpb.TokenTransaction{
				TokenOutputs: []*tokenpb.TokenOutput{
					{
						Id:                            proto.String("output1"),
						OwnerPublicKey:                ownerPubKey,
						RevocationCommitment:          revocationCommitment,
						WithdrawBondSats:              proto.Uint64(1000),
						WithdrawRelativeBlockLocktime: proto.Uint64(100),
						TokenPublicKey:                tokenPubKey,
						TokenAmount:                   tokenAmount,
					},
				},
				SparkOperatorIdentityPublicKeys: [][]byte{op1Key, op2Key},
				Network:                         pb.Network_MAINNET,
				TokenInputs: &tokenpb.TokenTransaction_MintInput{
					MintInput: &tokenpb.TokenMintInput{
						IssuerPublicKey: issuerPubKey,
					},
				},
				ClientCreatedTimestamp: timestamppb.New(time.UnixMilli(0)),
			},
			want: &pb.TokenTransaction{
				TokenOutputs: []*pb.TokenOutput{
					{
						Id:                            proto.String("output1"),
						OwnerPublicKey:                ownerPubKey,
						RevocationCommitment:          revocationCommitment,
						WithdrawBondSats:              proto.Uint64(1000),
						WithdrawRelativeBlockLocktime: proto.Uint64(100),
						TokenPublicKey:                tokenPubKey,
						TokenAmount:                   tokenAmount,
					},
				},
				SparkOperatorIdentityPublicKeys: [][]byte{op1Key, op2Key},
				Network:                         pb.Network_MAINNET,
				TokenInputs: &pb.TokenTransaction_MintInput{
					MintInput: &pb.TokenMintInput{
						IssuerPublicKey:         issuerPubKey,
						IssuerProvidedTimestamp: 0,
					},
				},
			},
		},
		{
			name: "transfer transaction",
			input: &tokenpb.TokenTransaction{
				TokenOutputs: []*tokenpb.TokenOutput{
					{
						Id:                            proto.String("output1"),
						OwnerPublicKey:                ownerPubKey,
						RevocationCommitment:          revocationCommitment,
						WithdrawBondSats:              proto.Uint64(1000),
						WithdrawRelativeBlockLocktime: proto.Uint64(100),
						TokenPublicKey:                tokenPubKey,
						TokenAmount:                   tokenAmount,
					},
				},
				SparkOperatorIdentityPublicKeys: [][]byte{op1Key},
				Network:                         pb.Network_TESTNET,
				TokenInputs: &tokenpb.TokenTransaction_TransferInput{
					TransferInput: &tokenpb.TokenTransferInput{
						OutputsToSpend: []*tokenpb.TokenOutputToSpend{
							{
								PrevTokenTransactionHash: prevHash1[:],
								PrevTokenTransactionVout: 0,
							},
							{
								PrevTokenTransactionHash: prevHash2[:],
								PrevTokenTransactionVout: 1,
							},
						},
					},
				},
			},
			want: &pb.TokenTransaction{
				TokenOutputs: []*pb.TokenOutput{
					{
						Id:                            proto.String("output1"),
						OwnerPublicKey:                ownerPubKey,
						RevocationCommitment:          revocationCommitment,
						WithdrawBondSats:              proto.Uint64(1000),
						WithdrawRelativeBlockLocktime: proto.Uint64(100),
						TokenPublicKey:                tokenPubKey,
						TokenAmount:                   tokenAmount,
					},
				},
				SparkOperatorIdentityPublicKeys: [][]byte{op1Key},
				Network:                         pb.Network_TESTNET,
				TokenInputs: &pb.TokenTransaction_TransferInput{
					TransferInput: &pb.TokenTransferInput{
						OutputsToSpend: []*pb.TokenOutputToSpend{
							{
								PrevTokenTransactionHash: prevHash1[:],
								PrevTokenTransactionVout: 0,
							},
							{
								PrevTokenTransactionHash: prevHash2[:],
								PrevTokenTransactionVout: 1,
							},
						},
					},
				},
			},
		},
		{
			name: "empty token outputs",
			input: &tokenpb.TokenTransaction{
				TokenOutputs:                    []*tokenpb.TokenOutput{},
				SparkOperatorIdentityPublicKeys: [][]byte{},
				Network:                         pb.Network_MAINNET,
				TokenInputs: &tokenpb.TokenTransaction_MintInput{
					MintInput: &tokenpb.TokenMintInput{
						IssuerPublicKey: issuerPubKey,
					},
				},
				ClientCreatedTimestamp: timestamppb.New(time.UnixMilli(1234567890)),
			},
			want: &pb.TokenTransaction{
				TokenOutputs:                    []*pb.TokenOutput{},
				SparkOperatorIdentityPublicKeys: [][]byte{},
				Network:                         pb.Network_MAINNET,
				TokenInputs: &pb.TokenTransaction_MintInput{
					MintInput: &pb.TokenMintInput{
						IssuerPublicKey:         issuerPubKey,
						IssuerProvidedTimestamp: 1234567890,
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := SparkTokenTransactionFromTokenProto(tt.input)
			if err != nil {
				t.Errorf("SparkTokenTransactionFromTokenProto() unexpected error = %v", err)
				return
			}
			if diff := cmp.Diff(tt.want, got, protocmp.Transform()); diff != "" {
				t.Errorf("SparkTokenTransactionFromTokenProto() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestTokenProtoFromSparkTokenTransaction(t *testing.T) {
	tests := []struct {
		name  string
		input *pb.TokenTransaction
		want  *tokenpb.TokenTransaction
	}{
		{
			name: "valid mint transaction",
			input: &pb.TokenTransaction{
				TokenOutputs: []*pb.TokenOutput{
					{
						Id:                            proto.String("output1"),
						OwnerPublicKey:                ownerPubKey,
						RevocationCommitment:          revocationCommitment,
						WithdrawBondSats:              proto.Uint64(1000),
						WithdrawRelativeBlockLocktime: proto.Uint64(100),
						TokenPublicKey:                tokenPubKey,
						TokenAmount:                   tokenAmount,
					},
				},
				SparkOperatorIdentityPublicKeys: [][]byte{op1Key, op2Key},
				Network:                         pb.Network_MAINNET,
				TokenInputs: &pb.TokenTransaction_MintInput{
					MintInput: &pb.TokenMintInput{
						IssuerPublicKey:         issuerPubKey,
						IssuerProvidedTimestamp: 1234567890,
					},
				},
			},
			want: &tokenpb.TokenTransaction{
				TokenOutputs: []*tokenpb.TokenOutput{
					{
						Id:                            proto.String("output1"),
						OwnerPublicKey:                ownerPubKey,
						RevocationCommitment:          revocationCommitment,
						WithdrawBondSats:              proto.Uint64(1000),
						WithdrawRelativeBlockLocktime: proto.Uint64(100),
						TokenPublicKey:                tokenPubKey,
						TokenAmount:                   tokenAmount,
					},
				},
				SparkOperatorIdentityPublicKeys: [][]byte{op1Key, op2Key},
				Network:                         pb.Network_MAINNET,
				TokenInputs: &tokenpb.TokenTransaction_MintInput{
					MintInput: &tokenpb.TokenMintInput{
						IssuerPublicKey: issuerPubKey,
					},
				},
				ClientCreatedTimestamp: timestamppb.New(time.UnixMilli(1234567890)),
			},
		},
		{
			name: "valid transfer transaction",
			input: &pb.TokenTransaction{
				TokenOutputs: []*pb.TokenOutput{
					{
						Id:                            proto.String("output1"),
						OwnerPublicKey:                ownerPubKey,
						RevocationCommitment:          revocationCommitment,
						WithdrawBondSats:              proto.Uint64(1000),
						WithdrawRelativeBlockLocktime: proto.Uint64(100),
						TokenPublicKey:                tokenPubKey,
						TokenAmount:                   tokenAmount,
					},
				},
				SparkOperatorIdentityPublicKeys: [][]byte{op1Key},
				Network:                         pb.Network_TESTNET,
				TokenInputs: &pb.TokenTransaction_TransferInput{
					TransferInput: &pb.TokenTransferInput{
						OutputsToSpend: []*pb.TokenOutputToSpend{
							{
								PrevTokenTransactionHash: prevHash1[:],
								PrevTokenTransactionVout: 0,
							},
							{
								PrevTokenTransactionHash: prevHash2[:],
								PrevTokenTransactionVout: 1,
							},
						},
					},
				},
			},
			want: &tokenpb.TokenTransaction{
				TokenOutputs: []*tokenpb.TokenOutput{
					{
						Id:                            proto.String("output1"),
						OwnerPublicKey:                ownerPubKey,
						RevocationCommitment:          revocationCommitment,
						WithdrawBondSats:              proto.Uint64(1000),
						WithdrawRelativeBlockLocktime: proto.Uint64(100),
						TokenPublicKey:                tokenPubKey,
						TokenAmount:                   tokenAmount,
					},
				},
				SparkOperatorIdentityPublicKeys: [][]byte{op1Key},
				Network:                         pb.Network_TESTNET,
				TokenInputs: &tokenpb.TokenTransaction_TransferInput{
					TransferInput: &tokenpb.TokenTransferInput{
						OutputsToSpend: []*tokenpb.TokenOutputToSpend{
							{
								PrevTokenTransactionHash: prevHash1[:],
								PrevTokenTransactionVout: 0,
							},
							{
								PrevTokenTransactionHash: prevHash2[:],
								PrevTokenTransactionVout: 1,
							},
						},
					},
				},
			},
		},
		{
			name: "empty token outputs",
			input: &pb.TokenTransaction{
				TokenOutputs:                    []*pb.TokenOutput{},
				SparkOperatorIdentityPublicKeys: [][]byte{},
				Network:                         pb.Network_MAINNET,
				TokenInputs: &pb.TokenTransaction_MintInput{
					MintInput: &pb.TokenMintInput{
						IssuerPublicKey:         issuerPubKey,
						IssuerProvidedTimestamp: 1234567890,
					},
				},
			},
			want: &tokenpb.TokenTransaction{
				TokenOutputs:                    []*tokenpb.TokenOutput{},
				SparkOperatorIdentityPublicKeys: [][]byte{},
				Network:                         pb.Network_MAINNET,
				TokenInputs: &tokenpb.TokenTransaction_MintInput{
					MintInput: &tokenpb.TokenMintInput{
						IssuerPublicKey: issuerPubKey,
					},
				},
				ClientCreatedTimestamp: timestamppb.New(time.UnixMilli(1234567890)),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := TokenProtoFromSparkTokenTransaction(tt.input)
			if err != nil {
				t.Errorf("TokenProtoFromSparkTokenTransaction() unexpected error = %v", err)
				return
			}
			if diff := cmp.Diff(tt.want, got, protocmp.Transform()); diff != "" {
				t.Errorf("TokenProtoFromSparkTokenTransaction() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestSparkTokenTransactionFromTokenProto_Errors(t *testing.T) {
	tests := []struct {
		name    string
		input   *tokenpb.TokenTransaction
		wantErr string
	}{
		{
			name:    "nil input",
			input:   nil,
			wantErr: "input token transaction cannot be nil",
		},
		{
			name: "nil mint input",
			input: &tokenpb.TokenTransaction{
				TokenOutputs: []*tokenpb.TokenOutput{},
				TokenInputs: &tokenpb.TokenTransaction_MintInput{
					MintInput: nil,
				},
			},
			wantErr: "mint_input is nil",
		},
		{
			name: "nil transfer input",
			input: &tokenpb.TokenTransaction{
				TokenOutputs: []*tokenpb.TokenOutput{},
				TokenInputs: &tokenpb.TokenTransaction_TransferInput{
					TransferInput: nil,
				},
			},
			wantErr: "transfer_input is nil",
		},
		{
			name: "unknown token inputs type",
			input: &tokenpb.TokenTransaction{
				TokenOutputs: []*tokenpb.TokenOutput{},
				TokenInputs:  nil,
			},
			wantErr: "unknown token_inputs type",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := SparkTokenTransactionFromTokenProto(tt.input)
			if err == nil {
				t.Errorf("SparkTokenTransactionFromTokenProto() expected error but got none")
				return
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("SparkTokenTransactionFromTokenProto() error = %v, want error containing %q", err, tt.wantErr)
			}
			if out != nil {
				t.Errorf("SparkTokenTransactionFromTokenProto() want nil but got %v", out)
			}
		})
	}
}

func TestTokenProtoFromSparkTokenTransaction_Errors(t *testing.T) {
	tests := []struct {
		name    string
		input   *pb.TokenTransaction
		wantErr string
	}{
		{
			name:    "nil input",
			input:   nil,
			wantErr: "input spark token transaction cannot be nil",
		},
		{
			name: "nil mint input",
			input: &pb.TokenTransaction{
				TokenOutputs: []*pb.TokenOutput{},
				TokenInputs: &pb.TokenTransaction_MintInput{
					MintInput: nil,
				},
			},
			wantErr: "mint_input is nil",
		},
		{
			name: "nil transfer input",
			input: &pb.TokenTransaction{
				TokenOutputs: []*pb.TokenOutput{},
				TokenInputs:  &pb.TokenTransaction_TransferInput{TransferInput: nil},
			},
			wantErr: "transfer_input is nil",
		},
		{
			name: "unknown token inputs type",
			input: &pb.TokenTransaction{
				TokenOutputs: []*pb.TokenOutput{},
				TokenInputs:  nil,
			},
			wantErr: "unknown token_inputs type",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := TokenProtoFromSparkTokenTransaction(tt.input)
			if err == nil {
				t.Errorf("TokenProtoFromSparkTokenTransaction() expected error but got none")
				return
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("TokenProtoFromSparkTokenTransaction() error = %v, want error containing %q", err, tt.wantErr)
			}
			if out != nil {
				t.Errorf("SparkTokenTransactionFromTokenProto() want nil but got %v", out)
			}
		})
	}
}

func TestTokenTransactionConversionRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		input *tokenpb.TokenTransaction
	}{
		{
			name: "mint transaction round trip",
			input: &tokenpb.TokenTransaction{
				TokenOutputs: []*tokenpb.TokenOutput{
					{
						Id:                            proto.String("output1"),
						OwnerPublicKey:                ownerPubKey,
						RevocationCommitment:          revocationCommitment,
						WithdrawBondSats:              proto.Uint64(1000),
						WithdrawRelativeBlockLocktime: proto.Uint64(100),
						TokenPublicKey:                tokenPubKey,
						TokenAmount:                   tokenAmount,
					},
				},
				SparkOperatorIdentityPublicKeys: [][]byte{op1Key, op2Key},
				Network:                         pb.Network_MAINNET,
				TokenInputs: &tokenpb.TokenTransaction_MintInput{
					MintInput: &tokenpb.TokenMintInput{
						IssuerPublicKey: issuerPubKey,
					},
				},
				ClientCreatedTimestamp: timestamppb.New(time.UnixMilli(1234567890)),
			},
		},
		{
			name: "transfer transaction round trip",
			input: &tokenpb.TokenTransaction{
				TokenOutputs: []*tokenpb.TokenOutput{
					{
						Id:                            proto.String("output1"),
						OwnerPublicKey:                ownerPubKey,
						RevocationCommitment:          revocationCommitment,
						WithdrawBondSats:              proto.Uint64(1000),
						WithdrawRelativeBlockLocktime: proto.Uint64(100),
						TokenPublicKey:                tokenPubKey,
						TokenAmount:                   tokenAmount,
					},
				},
				SparkOperatorIdentityPublicKeys: [][]byte{op1Key},
				Network:                         pb.Network_TESTNET,
				TokenInputs: &tokenpb.TokenTransaction_TransferInput{
					TransferInput: &tokenpb.TokenTransferInput{
						OutputsToSpend: []*tokenpb.TokenOutputToSpend{
							{
								PrevTokenTransactionHash: prevHash1[:],
								PrevTokenTransactionVout: 0,
							},
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sparkTx, err := SparkTokenTransactionFromTokenProto(tt.input)
			if err != nil {
				t.Errorf("SparkTokenTransactionFromTokenProto() unexpected error = %v", err)
				return
			}

			result, err := TokenProtoFromSparkTokenTransaction(sparkTx)
			if err != nil {
				t.Errorf("TokenProtoFromSparkTokenTransaction() unexpected error = %v", err)
				return
			}

			if diff := cmp.Diff(tt.input, result, protocmp.Transform()); diff != "" {
				t.Errorf("Round trip conversion mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
