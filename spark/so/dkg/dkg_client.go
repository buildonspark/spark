package dkg

import (
	"log"

	pb "github.com/lightsparkdev/spark-go/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Client represents a FROST gRPC client
type DkgClient struct {
	conn   *grpc.ClientConn
	Client pb.DKGServiceClient
}

// NewClient creates a new FROST client connected to the given Unix socket
func NewDKGServiceClient(address string) (*DkgClient, error) {
	// Create connection with minimal options
	conn, err := grpc.NewClient(
		address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}

	client := pb.NewDKGServiceClient(conn)
	return &DkgClient{
		conn:   conn,
		Client: client,
	}, nil
}

// Close closes the client connection
func (c *DkgClient) Close() error {
	return c.conn.Close()
}
