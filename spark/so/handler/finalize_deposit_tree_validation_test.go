package handler

import (
	"testing"

	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestValidateFinalizeDepositTreeCreationRequestRejectsMissingFields(t *testing.T) {
	for _, tc := range []struct {
		name    string
		req     *pb.FinalizeDepositTreeCreationRequest
		wantErr string
	}{
		{
			name:    "nil request",
			req:     nil,
			wantErr: "request is required",
		},
		{
			name: "missing on chain utxo",
			req: finalizeDepositTreeCreationRequestWith(func(req *pb.FinalizeDepositTreeCreationRequest) {
				req.OnChainUtxo = nil
			}),
			wantErr: "on_chain_utxo is required",
		},
		{
			name: "missing root signing job",
			req: finalizeDepositTreeCreationRequestWith(func(req *pb.FinalizeDepositTreeCreationRequest) {
				req.RootTxSigningJob = nil
			}),
			wantErr: "root_tx_signing_job is required",
		},
		{
			name: "missing refund signing job",
			req: finalizeDepositTreeCreationRequestWith(func(req *pb.FinalizeDepositTreeCreationRequest) {
				req.RefundTxSigningJob = nil
			}),
			wantErr: "refund_tx_signing_job is required",
		},
		{
			name: "missing direct from cpfp refund signing job",
			req: finalizeDepositTreeCreationRequestWith(func(req *pb.FinalizeDepositTreeCreationRequest) {
				req.DirectFromCpfpRefundTxSigningJob = nil
			}),
			wantErr: "direct_from_cpfp_refund_tx_signing_job is required",
		},
		{
			name: "empty signing commitments map",
			req: finalizeDepositTreeCreationRequestWith(func(req *pb.FinalizeDepositTreeCreationRequest) {
				req.RootTxSigningJob.SigningCommitments = &pb.SigningCommitments{}
			}),
			wantErr: "root_tx_signing_job.signing_commitments.signing_commitments map is empty",
		},
		{
			name: "too many additional utxos",
			req: finalizeDepositTreeCreationRequestWith(func(req *pb.FinalizeDepositTreeCreationRequest) {
				req.AdditionalOnChainUtxos = make([]*pb.UTXO, 11)
			}),
			wantErr: "too many additional UTXOs",
		},
		{
			name: "nil additional on chain utxo",
			req: finalizeDepositTreeCreationRequestWith(func(req *pb.FinalizeDepositTreeCreationRequest) {
				req.AdditionalOnChainUtxos = []*pb.UTXO{nil}
				req.RootTxSigningJob.AdditionalInputs = []*pb.InputSigningData{validInputSigningDataForValidation()}
			}),
			wantErr: "additional_on_chain_utxos[0] is required",
		},
		{
			name: "additional input count mismatch",
			req: finalizeDepositTreeCreationRequestWith(func(req *pb.FinalizeDepositTreeCreationRequest) {
				req.AdditionalOnChainUtxos = []*pb.UTXO{{Network: pb.Network_REGTEST}}
				req.RootTxSigningJob.AdditionalInputs = nil
			}),
			wantErr: "additional_inputs count (0) must match additional_on_chain_utxos count (1)",
		},
		{
			name: "nil additional input",
			req: finalizeDepositTreeCreationRequestWith(func(req *pb.FinalizeDepositTreeCreationRequest) {
				req.AdditionalOnChainUtxos = []*pb.UTXO{{Network: pb.Network_REGTEST}}
				req.RootTxSigningJob.AdditionalInputs = []*pb.InputSigningData{nil}
			}),
			wantErr: "root_tx_signing_job.additional_inputs[0] is required",
		},
		{
			name: "additional input missing commitments",
			req: finalizeDepositTreeCreationRequestWith(func(req *pb.FinalizeDepositTreeCreationRequest) {
				req.AdditionalOnChainUtxos = []*pb.UTXO{{Network: pb.Network_REGTEST}}
				req.RootTxSigningJob.AdditionalInputs = []*pb.InputSigningData{{
					SigningNonceCommitment: validSigningCommitmentForValidation(),
					UserSignature:          []byte{0x03},
				}}
			}),
			wantErr: "root_tx_signing_job.additional_inputs[0].signing_commitments is required",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateFinalizeDepositTreeCreationRequest(tc.req)
			require.Error(t, err)
			require.Equal(t, codes.InvalidArgument, status.Code(err))
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

func TestValidateFinalizeDepositTreeCreationRequestAcceptsMinimalValidRequest(t *testing.T) {
	err := validateFinalizeDepositTreeCreationRequest(finalizeDepositTreeCreationRequestWith(func(*pb.FinalizeDepositTreeCreationRequest) {}))
	require.NoError(t, err)
}

func validFinalizeDepositTreeCreationRequestForValidation() *pb.FinalizeDepositTreeCreationRequest {
	return &pb.FinalizeDepositTreeCreationRequest{
		OnChainUtxo:                      &pb.UTXO{Network: pb.Network_REGTEST, RawTx: []byte{0x01}},
		RootTxSigningJob:                 validUserSignedTxSigningJobForValidation(),
		RefundTxSigningJob:               validUserSignedTxSigningJobForValidation(),
		DirectFromCpfpRefundTxSigningJob: validUserSignedTxSigningJobForValidation(),
		AdditionalOnChainUtxos:           nil,
	}
}

func finalizeDepositTreeCreationRequestWith(mutate func(*pb.FinalizeDepositTreeCreationRequest)) *pb.FinalizeDepositTreeCreationRequest {
	req := validFinalizeDepositTreeCreationRequestForValidation()
	mutate(req)
	return req
}

func validUserSignedTxSigningJobForValidation() *pb.UserSignedTxSigningJob {
	return &pb.UserSignedTxSigningJob{
		SigningPublicKey:       []byte{0x02},
		RawTx:                  []byte{0x01},
		SigningNonceCommitment: validSigningCommitmentForValidation(),
		UserSignature:          []byte{0x03},
		SigningCommitments:     validSigningCommitmentsForValidation(),
	}
}

func validInputSigningDataForValidation() *pb.InputSigningData {
	return &pb.InputSigningData{
		SigningNonceCommitment: validSigningCommitmentForValidation(),
		UserSignature:          []byte{0x03},
		SigningCommitments:     validSigningCommitmentsForValidation(),
	}
}

func validSigningCommitmentsForValidation() *pb.SigningCommitments {
	return &pb.SigningCommitments{
		SigningCommitments: map[string]*pbcommon.SigningCommitment{
			"0000000000000000000000000000000000000000000000000000000000000001": validSigningCommitmentForValidation(),
		},
	}
}

func validSigningCommitmentForValidation() *pbcommon.SigningCommitment {
	return &pbcommon.SigningCommitment{
		Hiding:  []byte{0x02},
		Binding: []byte{0x03},
	}
}
