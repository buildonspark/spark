package grpc

import (
	"context"

	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/helper"

	pbmock "github.com/lightsparkdev/spark-go/proto/mock"
	"google.golang.org/protobuf/types/known/emptypb"
)

// MockServer is a mock server for the Spark protocol.
type MockServer struct {
	config        *so.Config
	onchainHelper *helper.MockOnChainHelper
	pbmock.UnimplementedMockServiceServer
}

// NewMockServer creates a new MockServer.
func NewMockServer(config *so.Config, onchainHelper *helper.MockOnChainHelper) *MockServer {
	return &MockServer{config: config, onchainHelper: onchainHelper}
}

// SetMockOnchainTx sets a mock onchain tx for the given txid.
func (o *MockServer) SetMockOnchainTx(ctx context.Context, req *pbmock.SetMockOnchainTxRequest) (*emptypb.Empty, error) {
	o.onchainHelper.SetMockOnchainTx(req.Txid, req.Tx)
	return &emptypb.Empty{}, nil
}
