package helper

import (
	"context"

	"github.com/google/uuid"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pbfrost "github.com/lightsparkdev/spark/proto/frost"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
)

// GenerateProofOfPossessionSignatures generates the proof of possession signatures for the given messages and keyshares.
func GenerateProofOfPossessionSignatures(ctx context.Context, config *so.Config, messages [][]byte, keyshares []*ent.SigningKeyshare) ([][]byte, error) {
	jobID := uuid.New().String()
	signingJobs := make([]*SigningJob, len(messages))
	for i, message := range messages {
		signingJob := SigningJob{
			JobID:             jobID,
			SigningKeyshareID: keyshares[i].ID,
			Message:           message,
			VerifyingKey:      &keyshares[i].PublicKey,
			UserCommitment:    nil,
		}
		signingJobs[i] = &signingJob
	}
	signingResult, err := SignFrost(ctx, config, signingJobs)
	if err != nil {
		return nil, err
	}

	operatorCommitments := signingResult[0].SigningCommitments
	operatorCommitmentsProto := make(map[string]*pbcommon.SigningCommitment)
	for id, commitment := range operatorCommitments {
		commitmentProto, err := commitment.MarshalProto()
		if err != nil {
			return nil, err
		}
		operatorCommitmentsProto[id] = commitmentProto
	}

	frostConn, err := config.NewFrostGRPCConnection()
	if err != nil {
		return nil, err
	}
	defer frostConn.Close()
	client := pbfrost.NewFrostServiceClient(frostConn)
	signatures := make([][]byte, len(messages))
	for i, message := range messages {
		signature, err := client.AggregateFrost(ctx, &pbfrost.AggregateFrostRequest{
			Message:         message,
			SignatureShares: signingResult[i].SignatureShares,
			PublicShares:    signingResult[i].PublicKeys,
			VerifyingKey:    keyshares[i].PublicKey.Serialize(),
			Commitments:     operatorCommitmentsProto,
		})
		if err != nil {
			return nil, err
		}
		signatures[i] = signature.Signature
	}
	return signatures, nil
}
