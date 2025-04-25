package wallet

import (
	"context"

	"github.com/lightsparkdev/spark/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
)

func SubscribeToEvents(ctx context.Context, config *Config) (pb.SparkService_SubscribeToEventsClient, error) {
	sparkConn, err := common.NewGRPCConnectionWithTestTLS(config.CoodinatorAddress(), nil)
	if err != nil {
		return nil, err
	}
	// Note: We don't defer close here because the stream needs the connection
	sparkClient := pb.NewSparkServiceClient(sparkConn)

	return sparkClient.SubscribeToEvents(ctx, &pb.SubscribeToEventsRequest{
		IdentityPublicKey: config.IdentityPublicKey(),
	})
}
