package helper

import (
	"context"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go/common"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/objects"

	pb "github.com/lightsparkdev/spark-go/proto"
)

// FrostRound1 performs the first round of the Frost signing. It gathers the signing commitments from all operators.
func FrostRound1(ctx context.Context, config *so.Config, signingKeyshareID uuid.UUID) (map[string]objects.SigningCommitment, error) {
	return ExecuteTaskWithAllOperators(ctx, config, true, func(ctx context.Context, operator *so.SigningOperator) (objects.SigningCommitment, error) {
		conn, err := common.NewGRPCConnection(operator.Address)
		if err != nil {
			return objects.SigningCommitment{}, err
		}
		client := pb.NewSparkInternalServiceClient(conn)
		response, err := client.FrostRound1(ctx, &pb.FrostRound1Request{
			KeyshareId: signingKeyshareID.String(),
		})
		if err != nil {
			return objects.SigningCommitment{}, err
		}

		commitment := objects.SigningCommitment{}
		err = commitment.UnmarshalProto(response.SigningCommitment)
		if err != nil {
			return objects.SigningCommitment{}, err
		}

		return commitment, nil
	})
}

func FrostRound2(
	ctx context.Context,
	config *so.Config,
	signingKeyshareID uuid.UUID,
	message []byte,
	verifyingKey []byte,
	commitments map[string]objects.SigningCommitment,
	userCommitment objects.SigningCommitment,
) (map[string][]byte, error) {
	return ExecuteTaskWithAllOperators(ctx, config, true, func(ctx context.Context, operator *so.SigningOperator) ([]byte, error) {
		conn, err := common.NewGRPCConnection(operator.Address)
		if err != nil {
			return nil, err
		}

		commitmentsMap := make(map[string]*pb.SigningCommitment)
		for operatorID, commitment := range commitments {
			commitmentProto, err := commitment.MarshalProto()
			if err != nil {
				return nil, err
			}
			commitmentsMap[operatorID] = commitmentProto
		}

		userCommitmentProto, err := userCommitment.MarshalProto()
		if err != nil {
			return nil, err
		}

		client := pb.NewSparkInternalServiceClient(conn)
		response, err := client.FrostRound2(ctx, &pb.FrostRound2Request{
			Message:         message,
			KeyshareId:      signingKeyshareID.String(),
			VerifyingKey:    verifyingKey,
			Commitments:     commitmentsMap,
			UserCommitments: userCommitmentProto,
		})
		if err != nil {
			return nil, err
		}

		return response.SignatureShare, nil
	})
}

func SignFrost(
	ctx context.Context,
	config *so.Config,
	signingKeyshareID uuid.UUID,
	message []byte,
	verifyingKey []byte,
	userCommitment objects.SigningCommitment,
) (map[string][]byte, error) {
	round1, err := FrostRound1(ctx, config, signingKeyshareID)
	if err != nil {
		return nil, err
	}

	round2, err := FrostRound2(ctx, config, signingKeyshareID, message, verifyingKey, round1, userCommitment)
	if err != nil {
		return nil, err
	}

	return round2, nil
}
