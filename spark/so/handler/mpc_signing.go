package handler

import (
	"fmt"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/sighash"
	pbfrost "github.com/lightsparkdev/spark/proto/frost"
	"github.com/lightsparkdev/spark/so/helper"
	transferpkg "github.com/lightsparkdev/spark/so/transfer"

	"github.com/btcsuite/btcd/wire"
)

// mpcSubUserCommitments converts parsed sub-user contributions to the
// frost-signer sign-path form. ParseMpcSubmission aligned the contributions
// to the package positions (ascending, unique, nonzero), which is the order
// the signer requires.
func mpcSubUserCommitments(contributions []transferpkg.SubUserSigningContribution) []*pbfrost.SubUserCommitment {
	out := make([]*pbfrost.SubUserCommitment, len(contributions))
	for i, contribution := range contributions {
		commitment := contribution.NonceCommitment()
		out[i] = &pbfrost.SubUserCommitment{
			Position:   contribution.Position(),
			Commitment: commitment.MarshalProto(),
		}
	}
	return out
}

// mpcSubUserShares converts parsed sub-user contributions to the
// frost-signer aggregate-path form (round-1 commitment + λ-applied round-2
// share per position).
func mpcSubUserShares(contributions []transferpkg.SubUserSigningContribution) []*pbfrost.SubUserSignatureShare {
	out := make([]*pbfrost.SubUserSignatureShare, len(contributions))
	for i, contribution := range contributions {
		commitment := contribution.NonceCommitment()
		out[i] = &pbfrost.SubUserSignatureShare{
			Position:       contribution.Position(),
			Commitment:     commitment.MarshalProto(),
			SignatureShare: contribution.PartialSignature(),
		}
	}
	return out
}

// buildMpcSigningJobForRefund is buildSigningJobForRefund's multi-sub-user
// form: the user side is the sub-user group (SIGNING_SCHEME_MPC_USER_GROUP),
// so there is no single user commitment and adaptor signatures are not
// supported. The signing keyshare is a parameter rather than a leaf query so
// the consensus flow can bulk-load keyshares and this constructor stays
// database-free.
func buildMpcSigningJobForRefund(
	job *transferpkg.MpcRefundSigningJob,
	verifyingKey keys.Public,
	signingKeyshareID uuid.UUID,
	parentTxBytes []byte,
	jobID uuid.UUID,
) (*helper.SigningJobWithPregeneratedNonce, error) {
	refundTx := job.RefundTx()
	parentTx, err := common.TxFromRawTxBytes(parentTxBytes)
	if err != nil {
		return nil, fmt.Errorf("unable to parse parent tx: %w", err)
	}
	if len(parentTx.TxOut) == 0 {
		return nil, fmt.Errorf("parent tx has no outputs")
	}
	if len(refundTx.TxIn) != 1 {
		return nil, fmt.Errorf("refund tx must have exactly 1 input, got %d", len(refundTx.TxIn))
	}
	expectedOutPoint := wire.OutPoint{Hash: parentTx.TxHash(), Index: 0}
	if refundTx.TxIn[0].PreviousOutPoint != expectedOutPoint {
		return nil, fmt.Errorf("refund tx input 0 must spend parent tx output 0")
	}
	sigHash, err := sighash.FromTx(refundTx, 0, parentTx.TxOut[0])
	if err != nil {
		return nil, fmt.Errorf("compute sighash: %w", err)
	}

	round1 := job.SigningCommitments()
	if len(round1) == 0 {
		return nil, fmt.Errorf("missing signing_commitments")
	}
	for opID, c := range round1 {
		if c.IsZero() {
			return nil, fmt.Errorf("round1 commitment for %s is zero", opID)
		}
	}

	return &helper.SigningJobWithPregeneratedNonce{
		SigningJob: helper.SigningJob{
			JobID:              jobID,
			SigningKeyshareID:  signingKeyshareID,
			Message:            sigHash,
			VerifyingKey:       &verifyingKey,
			SigningScheme:      pbfrost.SigningScheme_SIGNING_SCHEME_MPC_USER_GROUP,
			SubUserCommitments: mpcSubUserCommitments(job.Contributions()),
		},
		Round1Packages: round1,
	}, nil
}
