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

type SigningResult struct {
	SignatureShares    map[string][]byte
	SigningCommitments map[string]objects.SigningCommitment
}

// frostRound1 performs the first round of the Frost signing. It gathers the signing commitments from all operators.
func frostRound1(ctx context.Context, config *so.Config, signingKeyshareID uuid.UUID, operatorSelection *OperatorSelection) (map[string]objects.SigningCommitment, error) {
	return ExecuteTaskWithAllOperators(ctx, config, operatorSelection, func(ctx context.Context, operator *so.SigningOperator) (objects.SigningCommitment, error) {
		conn, err := common.NewGRPCConnection(operator.Address)
		if err != nil {
			return objects.SigningCommitment{}, err
		}
		defer conn.Close()

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
			log.Println("FrostRound1 UnmarshalProto failed:", err)
			return objects.SigningCommitment{}, err
		}

		return commitment, nil
	})
}

// frostRound2 performs the second round of the Frost signing. It gathers the signature shares from all operators.
func frostRound2(
	ctx context.Context,
	config *so.Config,
	signingKeyshareID uuid.UUID,
	message []byte,
	verifyingKey []byte,
	commitments map[string]objects.SigningCommitment,
	userCommitment objects.SigningCommitment,
	operatorSelection *OperatorSelection,
) (map[string][]byte, error) {
	return ExecuteTaskWithAllOperators(ctx, config, operatorSelection, func(ctx context.Context, operator *so.SigningOperator) ([]byte, error) {
		log.Println("FrostRound2 started for operator:", operator.Identifier)
		conn, err := common.NewGRPCConnection(operator.Address)
		if err != nil {
			return nil, err
		}
		defer conn.Close()

		commitmentsMap := make(map[string]*pb.SigningCommitment)
		for operatorID, commitment := range commitments {
			commitmentProto, err := commitment.MarshalProto()
			if err != nil {
				log.Println("Round2 MarshalProto failed:", err)
				return nil, err
			}
			commitmentsMap[operatorID] = commitmentProto
		}

		userCommitmentProto, err := userCommitment.MarshalProto()
		if err != nil {
			log.Println("MarshalProto failed:", err)
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
			log.Println("FrostRound2 failed:", err)
			return nil, err
		}

		return response.SignatureShare, nil
	})
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
	signingKeyshareID uuid.UUID,
	message []byte,
	verifyingKey []byte,
	userCommitment objects.SigningCommitment,
) (*SigningResult, error) {
	selection := OperatorSelection{Option: OperatorSelectionOptionThreshold, Threshold: int(config.Threshold)}
	round1, err := frostRound1(ctx, config, signingKeyshareID, &selection)
	if err != nil {
		log.Println("FrostRound1 failed:", err)
		return nil, err
	}

	round2, err := frostRound2(ctx, config, signingKeyshareID, message, verifyingKey, round1, userCommitment, &selection)
	if err != nil {
		log.Println("FrostRound2 failed:", err)
		return nil, err
	}

	return &SigningResult{
		SignatureShares:    round2,
		SigningCommitments: round1,
	}, nil
}
