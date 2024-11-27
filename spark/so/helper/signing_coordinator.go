package helper

import (
	"context"
	"log"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go/common"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/objects"

	pb "github.com/lightsparkdev/spark-go/proto"
)

// SigningResult is the result of a signing job.
type SigningResult struct {
	// JobID is the ID of the signing job.
	JobID string
	// SignatureShares is the signature shares from all operators.
	SignatureShares map[string][]byte
	// SigningCommitments is the signing commitments from all operators.
	SigningCommitments map[string]objects.SigningCommitment
}

// frostRound1 performs the first round of the Frost signing. It gathers the signing commitments from all operators.
func frostRound1(ctx context.Context, config *so.Config, signingKeyshareIDs []uuid.UUID, operatorSelection *OperatorSelection) (map[string][]objects.SigningCommitment, error) {
	return ExecuteTaskWithAllOperators(ctx, config, operatorSelection, func(ctx context.Context, operator *so.SigningOperator) ([]objects.SigningCommitment, error) {
		conn, err := common.NewGRPCConnection(operator.Address)
		if err != nil {
			return nil, err
		}
		defer conn.Close()

		keyshareIDs := make([]string, len(signingKeyshareIDs))
		for i, id := range signingKeyshareIDs {
			keyshareIDs[i] = id.String()
		}

		client := pb.NewSparkInternalServiceClient(conn)
		response, err := client.FrostRound1(ctx, &pb.FrostRound1Request{
			KeyshareIds: keyshareIDs,
		})
		if err != nil {
			return nil, err
		}

		commitments := make([]objects.SigningCommitment, len(response.SigningCommitments))
		for i, commitment := range response.SigningCommitments {
			err = commitments[i].UnmarshalProto(commitment)
			if err != nil {
				log.Println("FrostRound1 UnmarshalProto failed:", err)
				return nil, err
			}
		}

		return commitments, nil
	})
}

// frostRound2 performs the second round of the Frost signing. It gathers the signature shares from all operators.
func frostRound2(
	ctx context.Context,
	config *so.Config,
	jobs []*SigningJob,
	round1 map[string][]objects.SigningCommitment,
	operatorSelection *OperatorSelection,
) (map[string]map[string][]byte, error) {
	operatorResult, err := ExecuteTaskWithAllOperators(ctx, config, operatorSelection, func(ctx context.Context, operator *so.SigningOperator) (map[string][]byte, error) {
		log.Println("FrostRound2 started for operator:", operator.Identifier)
		conn, err := common.NewGRPCConnection(operator.Address)
		if err != nil {
			return nil, err
		}
		defer conn.Close()

		commitmentsArray := common.MapOfArrayToArrayOfMap(round1)

		signingJobs := make([]*pb.SigningJob, len(jobs))
		for i, job := range jobs {
			commitments := make(map[string]*pb.SigningCommitment)
			for operatorID, commitment := range commitmentsArray[i] {
				commitmentProto, err := commitment.MarshalProto()
				if err != nil {
					log.Println("Round2 MarshalProto failed:", err)
					return nil, err
				}
				commitments[operatorID] = commitmentProto
			}
			userCommitmentProto, err := job.UserCommitment.MarshalProto()
			if err != nil {
				log.Println("Round2 MarshalProto failed:", err)
				return nil, err
			}
			signingJobs[i] = &pb.SigningJob{
				JobId:           job.JobID,
				Message:         job.Message,
				KeyshareId:      job.SigningKeyshareID.String(),
				VerifyingKey:    job.VerifyingKey,
				Commitments:     commitments,
				UserCommitments: userCommitmentProto,
			}

			log.Println("FrostRound2 signing job:", signingJobs[i])
		}

		client := pb.NewSparkInternalServiceClient(conn)
		response, err := client.FrostRound2(ctx, &pb.FrostRound2Request{
			SigningJobs: signingJobs,
		})
		if err != nil {
			log.Println("FrostRound2 failed:", err)
			return nil, err
		}

		results := make(map[string][]byte)
		for operatorID, result := range response.Results {
			results[operatorID] = result.SignatureShare
		}

		return results, nil
	})

	if err != nil {
		return nil, err
	}

	log.Println("FrostRound2 operator result:", operatorResult)

	result := common.SwapMapKeys(operatorResult)
	return result, nil
}

// SigningJob is a job for signing.
type SigningJob struct {
	// JobID is the ID of the signing job.
	JobID string
	// SigningKeyshareID is the ID of the keyshare to use for signing.
	SigningKeyshareID uuid.UUID
	// Message is the message to sign.
	Message []byte
	// VerifyingKey is the verifying key for the message.
	VerifyingKey []byte
	// UserCommitment is the user commitment for the message.
	UserCommitment objects.SigningCommitment
}

// SigningKeyshareIDsFromSigningJobs returns the IDs of the keyshares used for signing.
func SigningKeyshareIDsFromSigningJobs(jobs []*SigningJob) []uuid.UUID {
	ids := make([]uuid.UUID, len(jobs))
	for i, job := range jobs {
		ids[i] = job.SigningKeyshareID
	}
	return ids
}

// SignFrost performs the Frost signing.
// It will perform two rounds internally, and collect the final signature along with signing commitments.
// This is for 1 + (t, n) signing scheme, on the group side.
// The result for this function is not the final signature, the user side needs to perform their signing part
// and then aggregate the results to have the final signature.
//
// Args:
//   - ctx: context
//   - config: the config
//   - signingKeyshareID: the keyshare ID to use for signing.
//   - message: the message to sign
//   - verifyingKey: the combined verifying key, this will be user's public key + operator's public key
//   - userCommitment: the user commitment
//
// Returns:
//   - *SigningResult: the result of the signing, containing the signature shares and signing commitments
func SignFrost(
	ctx context.Context,
	config *so.Config,
	jobs []*SigningJob,
) ([]*SigningResult, error) {
	selection := OperatorSelection{Option: OperatorSelectionOptionThreshold, Threshold: int(config.Threshold)}
	signingKeyshareIDs := SigningKeyshareIDsFromSigningJobs(jobs)
	round1, err := frostRound1(ctx, config, signingKeyshareIDs, &selection)
	if err != nil {
		log.Println("FrostRound1 failed:", err)
		return nil, err
	}

	round2, err := frostRound2(ctx, config, jobs, round1, &selection)
	if err != nil {
		log.Println("FrostRound2 failed:", err)
		return nil, err
	}

	round1Array := common.MapOfArrayToArrayOfMap(round1)

	results := make([]*SigningResult, len(jobs))
	for i, job := range jobs {
		results[i] = &SigningResult{
			JobID:              job.JobID,
			SignatureShares:    round2[job.JobID],
			SigningCommitments: round1Array[i],
		}
	}

	return results, nil
}
