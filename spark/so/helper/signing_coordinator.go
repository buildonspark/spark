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
	return ExecuteTaskWithAllOperators(ctx, config, func(ctx context.Context, operator *so.SigningOperator) (objects.SigningCommitment, error) {
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
	}, true)
}
