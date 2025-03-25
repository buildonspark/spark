package helper

import (
	"context"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go/common"
	pbcommon "github.com/lightsparkdev/spark-go/proto/common"
	pbfrost "github.com/lightsparkdev/spark-go/proto/frost"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent"
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
			VerifyingKey:      keyshares[i].PublicKey,
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

	conn, err := common.NewGRPCConnectionWithoutTLS(config.SignerAddress, nil)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	client := pbfrost.NewFrostServiceClient(conn)
	signatures := make([][]byte, len(messages))
	for i, message := range messages {
		signatureShares := signingResult[i].SignatureShares
		publicKeys := signingResult[i].PublicKeys
		signature, err := client.AggregateFrost(ctx, &pbfrost.AggregateFrostRequest{
			Message:         message,
			SignatureShares: signatureShares,
			PublicShares:    publicKeys,
			VerifyingKey:    keyshares[i].PublicKey,
			Commitments:     operatorCommitmentsProto,
		})
		if err != nil {
			return nil, err
		}
		signatures[i] = signature.Signature
	}
	return signatures, nil
}
