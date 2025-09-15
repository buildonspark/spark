package helper

import (
	"context"
	"fmt"

	"github.com/lightsparkdev/spark/common/logging"
	pbspark "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/knobs"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type signingCommitmentsKeyType string

const signingCommitmentsKey signingCommitmentsKeyType = "ReservedSigningCommitments"

func SigningCommitmentInterceptor(operatorMap map[string]*so.SigningOperator, knobs knobs.Knobs) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if knobs == nil || knobs.GetValue("spark.so.enable_prefetch_frost_round_1", 0) == 0 {
			return handler(ctx, req)
		}
		protoReq, ok := req.(proto.Message)
		if !ok {
			return nil, status.Errorf(codes.Internal, "invalid request type %T", req)
		}
		signingCommitmentsCount := calculateSigningCommitmentCount(protoReq)
		if signingCommitmentsCount > 0 {
			logger := logging.GetLoggerFromContext(ctx)
			logger.Sugar().Infof("Counted %d signing commitments necessary for request", signingCommitmentsCount)
			dbTx, err := ent.GetDbFromContext(ctx)
			if err != nil {
				return nil, err
			}

			commitmentsMap := make(map[uint][]*ent.SigningCommitment)
			for _, operator := range operatorMap {
				idx := uint(operator.ID)
				commitments, err := ent.ReserveSigningCommitments(ctx, dbTx, uint32(signingCommitmentsCount), idx)
				if err != nil {
					logger.Error("Failed to get unused signing commitments", zap.Error(err))
					if rollbackErr := dbTx.Rollback(); rollbackErr != nil {
						return nil, rollbackErr
					}
					return handler(ctx, req)
				}
				commitmentsMap[idx] = commitments
			}
			if err := dbTx.Commit(); err != nil {
				return nil, err
			}
			ctx = injectSigningCommitments(ctx, commitmentsMap)
		}

		return handler(ctx, req)
	}
}

func calculateSigningCommitmentCount(req proto.Message) int {
	if getSigningCommitmentReq, ok := req.(*pbspark.GetSigningCommitmentsRequest); ok {
		return len(getSigningCommitmentReq.NodeIds) * int(getSigningCommitmentReq.Count)
	}
	return countMessageType(req.ProtoReflect(), "spark.SigningJob")
}

func injectSigningCommitments(ctx context.Context, commitmentsMap map[uint][]*ent.SigningCommitment) context.Context {
	return context.WithValue(ctx, signingCommitmentsKey, commitmentsMap)
}

func GetSigningCommitmentsFromContext(ctx context.Context, count int, operatorIndex uint) ([]*ent.SigningCommitment, error) {
	commitmentsMapValue := ctx.Value(signingCommitmentsKey)
	if commitmentsMapValue == nil {
		return nil, fmt.Errorf("no signing commitments found in context")
	}
	commitmentsMap, ok := commitmentsMapValue.(map[uint][]*ent.SigningCommitment)
	if !ok {
		return nil, fmt.Errorf("no signing commitments found in context")
	}
	commitments := commitmentsMap[operatorIndex]
	if len(commitments) < count {
		return nil, fmt.Errorf("not enough signing commitments for operator index %d", operatorIndex)
	}
	newCommitments := commitments[count:]
	commitmentsMap[operatorIndex] = newCommitments
	return commitments[:count], nil
}
