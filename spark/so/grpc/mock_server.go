package grpc

import (
	"context"

	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent"

	pbmock "github.com/lightsparkdev/spark-go/proto/mock"
	"github.com/lightsparkdev/spark-go/so/ent/preimagerequest"
	"github.com/lightsparkdev/spark-go/so/ent/preimageshare"
	"github.com/lightsparkdev/spark-go/so/ent/usersignedtransaction"
	"google.golang.org/protobuf/types/known/emptypb"
)

// MockServer is a mock server for the Spark protocol.
type MockServer struct {
	config *so.Config
	pbmock.UnimplementedMockServiceServer
}

// NewMockServer creates a new MockServer.
func NewMockServer(config *so.Config) *MockServer {
	return &MockServer{config: config}
}

// CleanUpPreimageShare cleans up the preimage share for the given payment hash.
func (o *MockServer) CleanUpPreimageShare(ctx context.Context, req *pbmock.CleanUpPreimageShareRequest) (*emptypb.Empty, error) {
	db := ent.GetDbFromContext(ctx)
	_, err := db.PreimageShare.Delete().Where(preimageshare.PaymentHashEQ(req.PaymentHash)).Exec(ctx)
	if err != nil {
		return nil, err
	}
	preimageRequestQuery := db.PreimageRequest.Query().Where(preimagerequest.PaymentHashEQ(req.PaymentHash))
	if preimageRequestQuery.CountX(ctx) == 0 {
		return nil, nil
	}
	preimageRequests, err := preimageRequestQuery.All(ctx)
	if err != nil {
		return nil, err
	}
	for _, preimageRequest := range preimageRequests {
		txs, err := preimageRequest.QueryTransactions().All(ctx)
		if err != nil {
			return nil, err
		}
		for _, tx := range txs {
			_, err = db.UserSignedTransaction.Delete().Where(usersignedtransaction.IDEQ(tx.ID)).Exec(ctx)
			if err != nil {
				return nil, err
			}
		}
	}
	_, err = db.PreimageRequest.Delete().Where(preimagerequest.PaymentHashEQ(req.PaymentHash)).Exec(ctx)
	if err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}
