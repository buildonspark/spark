package protoconverter

import (
	"bytes"
	"crypto/sha256"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lightsparkdev/spark/common/keys"

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
				Network:                         pb.Network_MAINNET,
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
				Network:                         pb.Network_MAINNET,
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

func TestConvertPartialToV2TxShape(t *testing.T) {
	ts := timestamppb.New(time.UnixMilli(111))
	tests := []struct {
		name   string
		input  *tokenpb.PartialTokenTransaction
		expect *tokenpb.TokenTransaction
	}{
		{
			name: "mint input maps to legacy",
			input: &tokenpb.PartialTokenTransaction{
				Version: 1,
				TokenTransactionMetadata: &tokenpb.TokenTransactionMetadata{
					SparkOperatorIdentityPublicKeys: [][]byte{op1Key, op2Key},
					Network:                         pb.Network_MAINNET,
					ClientCreatedTimestamp:          ts,
					ValidityDurationSeconds:         42,
				},
				TokenInputs: &tokenpb.PartialTokenTransaction_MintInput{
					MintInput: &tokenpb.TokenMintInput{
						IssuerPublicKey: issuerPubKey,
					},
				},
				PartialTokenOutputs: []*tokenpb.PartialTokenOutput{
					{
						OwnerPublicKey:                ownerPubKey,
						WithdrawBondSats:              1000,
						WithdrawRelativeBlockLocktime: 100,
						TokenIdentifier:               []byte{9, 9, 9},
						TokenAmount:                   tokenAmount,
					},
				},
			},
			expect: &tokenpb.TokenTransaction{
				Version:                         1,
				SparkOperatorIdentityPublicKeys: [][]byte{op1Key, op2Key},
				Network:                         pb.Network_MAINNET,
				ClientCreatedTimestamp:          ts,
				ValidityDurationSeconds:         proto.Uint64(42),
				TokenInputs: &tokenpb.TokenTransaction_MintInput{
					MintInput: &tokenpb.TokenMintInput{
						IssuerPublicKey: issuerPubKey,
					},
				},
				TokenOutputs: []*tokenpb.TokenOutput{
					{
						OwnerPublicKey:                ownerPubKey,
						WithdrawBondSats:              proto.Uint64(1000),
						WithdrawRelativeBlockLocktime: proto.Uint64(100),
						TokenIdentifier:               []byte{9, 9, 9},
						TokenAmount:                   tokenAmount,
					},
				},
			},
		},
		{
			name: "transfer input maps to legacy",
			input: &tokenpb.PartialTokenTransaction{
				Version: 2,
				TokenTransactionMetadata: &tokenpb.TokenTransactionMetadata{
					SparkOperatorIdentityPublicKeys: [][]byte{op1Key},
					Network:                         pb.Network_MAINNET,
					ClientCreatedTimestamp:          ts,
				},
				TokenInputs: &tokenpb.PartialTokenTransaction_TransferInput{
					TransferInput: &tokenpb.TokenTransferInput{
						OutputsToSpend: []*tokenpb.TokenOutputToSpend{
							{PrevTokenTransactionHash: prevHash1[:], PrevTokenTransactionVout: 0},
						},
					},
				},
				PartialTokenOutputs: []*tokenpb.PartialTokenOutput{
					{OwnerPublicKey: ownerPubKey},
				},
			},
			expect: &tokenpb.TokenTransaction{
				Version:                         2,
				SparkOperatorIdentityPublicKeys: [][]byte{op1Key},
				Network:                         pb.Network_MAINNET,
				ClientCreatedTimestamp:          ts,
				TokenInputs: &tokenpb.TokenTransaction_TransferInput{
					TransferInput: &tokenpb.TokenTransferInput{
						OutputsToSpend: []*tokenpb.TokenOutputToSpend{
							{PrevTokenTransactionHash: prevHash1[:], PrevTokenTransactionVout: 0},
						},
					},
				},
				TokenOutputs: []*tokenpb.TokenOutput{
					{OwnerPublicKey: ownerPubKey},
				},
			},
		},
		{
			name: "create input maps to legacy",
			input: &tokenpb.PartialTokenTransaction{
				Version: 3,
				TokenTransactionMetadata: &tokenpb.TokenTransactionMetadata{
					SparkOperatorIdentityPublicKeys: [][]byte{},
					Network:                         pb.Network_MAINNET,
				},
				TokenInputs: &tokenpb.PartialTokenTransaction_CreateInput{
					CreateInput: &tokenpb.TokenCreateInput{
						IssuerPublicKey:         issuerPubKey,
						TokenName:               "Name",
						TokenTicker:             "TCK",
						Decimals:                9,
						MaxSupply:               tokenAmount,
						IsFreezable:             true,
						CreationEntityPublicKey: tokenPubKey,
					},
				},
				PartialTokenOutputs: []*tokenpb.PartialTokenOutput{},
			},
			expect: &tokenpb.TokenTransaction{
				Version:                         3,
				SparkOperatorIdentityPublicKeys: [][]byte{},
				Network:                         pb.Network_MAINNET,
				TokenInputs: &tokenpb.TokenTransaction_CreateInput{
					CreateInput: &tokenpb.TokenCreateInput{
						IssuerPublicKey:         issuerPubKey,
						TokenName:               "Name",
						TokenTicker:             "TCK",
						Decimals:                9,
						MaxSupply:               tokenAmount,
						IsFreezable:             true,
						CreationEntityPublicKey: tokenPubKey,
					},
				},
				TokenOutputs: []*tokenpb.TokenOutput{},
			},
		},
		{
			name:   "nil returns nil",
			input:  nil,
			expect: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ConvertPartialToV2TxShape(tt.input)
			if tt.input == nil {
				if err != nil {
					t.Fatalf("ConvertPartialToV2TxShape(nil) unexpected error: %v", err)
				}
				if got != nil {
					t.Fatalf("ConvertPartialToV2TxShape(nil) expected nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("ConvertPartialToV2TxShape() unexpected error: %v", err)
			}
			if diff := cmp.Diff(tt.expect, got, protocmp.Transform()); diff != "" {
				t.Fatalf("ConvertPartialToV2TxShape() mismatch (-want +got):\n%s", diff)
			}
		})
	}

	// Unknown input type error
	_, err := ConvertPartialToV2TxShape(&tokenpb.PartialTokenTransaction{
		Version:                  1,
		TokenTransactionMetadata: &tokenpb.TokenTransactionMetadata{},
		// TokenInputs left nil
	})
	if err == nil || !strings.Contains(err.Error(), "unknown token input type") {
		t.Fatalf("ConvertPartialToV2TxShape() expected unknown token input type error, got: %v", err)
	}
}

func TestConvertFinalToV2TxShape(t *testing.T) {
	ts := timestamppb.New(time.UnixMilli(222))
	tests := []struct {
		name   string
		input  *tokenpb.FinalTokenTransaction
		expect *tokenpb.TokenTransaction
	}{
		{
			name: "mint with final outputs maps",
			input: &tokenpb.FinalTokenTransaction{
				Version: 7,
				TokenTransactionMetadata: &tokenpb.TokenTransactionMetadata{
					ClientCreatedTimestamp:  ts,
					Network:                 pb.Network_MAINNET,
					ValidityDurationSeconds: 600,
				},
				TokenInputs: &tokenpb.FinalTokenTransaction_MintInput{
					MintInput: &tokenpb.TokenMintInput{IssuerPublicKey: issuerPubKey},
				},
				FinalTokenOutputs: []*tokenpb.FinalTokenOutput{
					{
						PartialTokenOutput: &tokenpb.PartialTokenOutput{
							OwnerPublicKey: ownerPubKey,
							TokenAmount:    tokenAmount,
						},
						RevocationCommitment: revocationCommitment,
					},
				},
			},
			expect: &tokenpb.TokenTransaction{
				Version:                 7,
				ClientCreatedTimestamp:  ts,
				Network:                 pb.Network_MAINNET,
				ValidityDurationSeconds: proto.Uint64(600),
				TokenInputs: &tokenpb.TokenTransaction_MintInput{
					MintInput: &tokenpb.TokenMintInput{IssuerPublicKey: issuerPubKey},
				},
				TokenOutputs: []*tokenpb.TokenOutput{
					{
						OwnerPublicKey:       ownerPubKey,
						TokenAmount:          tokenAmount,
						RevocationCommitment: revocationCommitment,
					},
				},
			},
		},
		{
			name: "nil partial in final output yields empty output",
			input: &tokenpb.FinalTokenTransaction{
				TokenTransactionMetadata: &tokenpb.TokenTransactionMetadata{},
				TokenInputs: &tokenpb.FinalTokenTransaction_TransferInput{
					TransferInput: &tokenpb.TokenTransferInput{},
				},
				FinalTokenOutputs: []*tokenpb.FinalTokenOutput{
					{PartialTokenOutput: nil},
				},
			},
			expect: &tokenpb.TokenTransaction{
				TokenInputs: &tokenpb.TokenTransaction_TransferInput{
					TransferInput: &tokenpb.TokenTransferInput{},
				},
				TokenOutputs: []*tokenpb.TokenOutput{
					{},
				},
			},
		},
		{
			name:   "nil returns nil",
			input:  nil,
			expect: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ConvertFinalToV2TxShape(tt.input)
			if tt.input == nil {
				if err != nil {
					t.Fatalf("ConvertFinalToV2TxShape(nil) unexpected error: %v", err)
				}
				if got != nil {
					t.Fatalf("ConvertFinalToV2TxShape(nil) expected nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("ConvertFinalToV2TxShape() unexpected error: %v", err)
			}
			if diff := cmp.Diff(tt.expect, got, protocmp.Transform()); diff != "" {
				t.Fatalf("ConvertFinalToV2TxShape() mismatch (-want +got):\n%s", diff)
			}
		})
	}

	// Unknown input type error
	_, err := ConvertFinalToV2TxShape(&tokenpb.FinalTokenTransaction{
		Version:                  1,
		TokenTransactionMetadata: &tokenpb.TokenTransactionMetadata{},
		// TokenInputs left nil
	})
	if err == nil || !strings.Contains(err.Error(), "unknown token input type") {
		t.Fatalf("ConvertFinalToV2TxShape() expected unknown token input type error, got: %v", err)
	}
}

func TestConvertV2TxShapeToFinal(t *testing.T) {
	tests := []struct {
		name   string
		input  *tokenpb.TokenTransaction
		expect *tokenpb.FinalTokenTransaction
	}{
		{
			name: "create maps to final",
			input: &tokenpb.TokenTransaction{
				Version:                 5,
				Network:                 pb.Network_MAINNET,
				ValidityDurationSeconds: proto.Uint64(777),
				TokenInputs: &tokenpb.TokenTransaction_CreateInput{
					CreateInput: &tokenpb.TokenCreateInput{
						IssuerPublicKey: issuerPubKey,
						TokenName:       "X",
					},
				},
				TokenOutputs: []*tokenpb.TokenOutput{
					{
						OwnerPublicKey:       ownerPubKey,
						TokenIdentifier:      []byte("id"),
						TokenAmount:          tokenAmount,
						RevocationCommitment: revocationCommitment,
					},
				},
			},
			expect: &tokenpb.FinalTokenTransaction{
				Version: 5,
				TokenTransactionMetadata: &tokenpb.TokenTransactionMetadata{
					Network:                 pb.Network_MAINNET,
					ValidityDurationSeconds: 777,
				},
				TokenInputs: &tokenpb.FinalTokenTransaction_CreateInput{
					CreateInput: &tokenpb.TokenCreateInput{
						IssuerPublicKey: issuerPubKey,
						TokenName:       "X",
					},
				},
				FinalTokenOutputs: []*tokenpb.FinalTokenOutput{
					{
						PartialTokenOutput: &tokenpb.PartialTokenOutput{
							OwnerPublicKey:  ownerPubKey,
							TokenIdentifier: []byte("id"),
							TokenAmount:     tokenAmount,
						},
						RevocationCommitment: revocationCommitment,
					},
				},
			},
		},
		{
			name:   "nil returns nil",
			input:  nil,
			expect: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ConvertV2TxShapeToFinal(tt.input)
			if tt.input == nil {
				if err != nil {
					t.Fatalf("ConvertV2TxShapeToFinal(nil) unexpected error: %v", err)
				}
				if got != nil {
					t.Fatalf("ConvertV2TxShapeToFinal(nil) expected nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("ConvertV2TxShapeToFinal() unexpected error: %v", err)
			}
			if diff := cmp.Diff(tt.expect, got, protocmp.Transform()); diff != "" {
				t.Fatalf("ConvertV2TxShapeToFinal() mismatch (-want +got):\n%s", diff)
			}
		})
	}

	// Unknown input type error
	_, err := ConvertV2TxShapeToFinal(&tokenpb.TokenTransaction{
		// TokenInputs left nil
	})
	if err == nil || !strings.Contains(err.Error(), "unknown token input type") {
		t.Fatalf("ConvertV2TxShapeToFinal() expected unknown token input type error, got: %v", err)
	}
}

func TestConvertBroadcastToStart(t *testing.T) {
	ownerSigs := []*tokenpb.SignatureWithIndex{
		{Signature: []byte{1, 2, 3}, InputIndex: 0},
		{Signature: []byte{4, 5, 6}, InputIndex: 1},
	}
	ptx := &tokenpb.PartialTokenTransaction{
		Version: 9,
		TokenTransactionMetadata: &tokenpb.TokenTransactionMetadata{
			Network:                 pb.Network_MAINNET,
			ValidityDurationSeconds: 300,
		},
		TokenInputs: &tokenpb.PartialTokenTransaction_MintInput{
			MintInput: &tokenpb.TokenMintInput{IssuerPublicKey: issuerPubKey},
		},
		PartialTokenOutputs: []*tokenpb.PartialTokenOutput{
			{OwnerPublicKey: ownerPubKey},
		},
	}
	req := &tokenpb.BroadcastTransactionRequest{
		IdentityPublicKey:               op1Key,
		PartialTokenTransaction:         ptx,
		TokenTransactionOwnerSignatures: ownerSigs,
	}

	got, err := ConvertBroadcastToStart(req)
	if err != nil {
		t.Fatalf("ConvertBroadcastToStart() unexpected error: %v", err)
	}
	if got.GetValidityDurationSeconds() != ptx.GetTokenTransactionMetadata().GetValidityDurationSeconds() {
		t.Fatalf("ValidityDurationSeconds mismatch: want %d got %d",
			ptx.GetTokenTransactionMetadata().GetValidityDurationSeconds(), got.GetValidityDurationSeconds())
	}
	if diff := cmp.Diff(op1Key, got.GetIdentityPublicKey()); diff != "" {
		t.Fatalf("IdentityPublicKey mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(ownerSigs, got.GetPartialTokenTransactionOwnerSignatures(), protocmp.Transform()); diff != "" {
		t.Fatalf("OwnerSignatures mismatch (-want +got):\n%s", diff)
	}
	// Ensure partial got converted to legacy TokenTransaction
	if got.GetPartialTokenTransaction() == nil {
		t.Fatalf("expected non-nil PartialTokenTransaction")
	}

	// Nil input -> nil output, nil error
	out, err := ConvertBroadcastToStart(nil)
	if err != nil {
		t.Fatalf("ConvertBroadcastToStart(nil) unexpected error: %v", err)
	}
	if out != nil {
		t.Fatalf("ConvertBroadcastToStart(nil) expected nil output")
	}
}

func TestRoundTrip_Final_Legacy_Equivalent(t *testing.T) {
	ts := timestamppb.New(time.UnixMilli(333))
	// For V3+ transactions, operator keys must be sorted for deterministic hashing
	sortedOpKeys := [][]byte{op1Key, op2Key}
	slices.SortFunc(sortedOpKeys, bytes.Compare)
	final := &tokenpb.FinalTokenTransaction{
		Version: 3,
		TokenTransactionMetadata: &tokenpb.TokenTransactionMetadata{
			SparkOperatorIdentityPublicKeys: sortedOpKeys,
			Network:                         pb.Network_REGTEST,
			ClientCreatedTimestamp:          ts,
			InvoiceAttachments:              []*tokenpb.InvoiceAttachment{{SparkInvoice: "inv-1"}, {SparkInvoice: "inv-2"}},
			ValidityDurationSeconds:         120,
		},
		TokenInputs: &tokenpb.FinalTokenTransaction_MintInput{
			MintInput: &tokenpb.TokenMintInput{IssuerPublicKey: issuerPubKey, TokenIdentifier: prevHash1[:]},
		},
		FinalTokenOutputs: []*tokenpb.FinalTokenOutput{
			{
				PartialTokenOutput: &tokenpb.PartialTokenOutput{
					OwnerPublicKey:                ownerPubKey,
					WithdrawBondSats:              123,
					WithdrawRelativeBlockLocktime: 45,
					TokenIdentifier:               prevHash1[:],
					TokenAmount:                   tokenAmount,
				},
				RevocationCommitment: revocationCommitment,
			},
		},
	}

	legacy, err := ConvertFinalToV2TxShape(final)
	if err != nil {
		t.Fatalf("ConvertFinalToV2TxShape() error: %v", err)
	}
	back, err := ConvertV2TxShapeToFinal(legacy)
	if err != nil {
		t.Fatalf("ConvertV2TxShapeToFinal() error: %v", err)
	}
	if diff := cmp.Diff(final, back, protocmp.Transform()); diff != "" {
		t.Fatalf("final -> legacy -> final mismatch (-want +got):\n%s", diff)
	}
}

func TestRoundTrip_Partial_Legacy_Equivalent(t *testing.T) {
	ts := timestamppb.New(time.UnixMilli(444))
	partial := &tokenpb.PartialTokenTransaction{
		TokenTransactionMetadata: &tokenpb.TokenTransactionMetadata{
			SparkOperatorIdentityPublicKeys: [][]byte{op1Key},
			Network:                         pb.Network_MAINNET,
			ClientCreatedTimestamp:          ts,
			InvoiceAttachments:              []*tokenpb.InvoiceAttachment{{SparkInvoice: "a"}, {SparkInvoice: "b"}},
			ValidityDurationSeconds:         60,
		},
		TokenInputs: &tokenpb.PartialTokenTransaction_CreateInput{
			CreateInput: &tokenpb.TokenCreateInput{
				IssuerPublicKey: issuerPubKey,
				TokenName:       "Token",
				TokenTicker:     "TOK",
				Decimals:        9,
				MaxSupply:       tokenAmount,
				IsFreezable:     true,
				// CreationEntityPublicKey intentionally omitted in partial
			},
		},
		PartialTokenOutputs: []*tokenpb.PartialTokenOutput{
			{
				OwnerPublicKey:                ownerPubKey,
				WithdrawBondSats:              1000,
				WithdrawRelativeBlockLocktime: 50,
				TokenIdentifier:               prevHash1[:],
				TokenAmount:                   tokenAmount,
			},
		},
	}

	legacy, err := ConvertPartialToV2TxShape(partial)
	if err != nil {
		t.Fatalf("ConvertPartialToV2TxShape() error: %v", err)
	}
	back, err := ConvertV2TxShapeToPartial(legacy)
	if err != nil {
		t.Fatalf("ConvertV2TxShapeToPartial() error: %v", err)
	}
	if diff := cmp.Diff(partial, back, protocmp.Transform()); diff != "" {
		t.Fatalf("partial -> legacy -> partial mismatch (-want +got):\n%s", diff)
	}
}
