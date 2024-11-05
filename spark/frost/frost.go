package frost

import (
	"log"

	frost "github.com/lightsparkdev/spark-go/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Client represents a FROST gRPC client
type FrostClient struct {
	conn   *grpc.ClientConn
	Client frost.FrostServiceClient
}

// NewClient creates a new FROST client connected to the given Unix socket
func NewFrostClient(socketPath string) (*FrostClient, error) {
	target := "unix://" + socketPath

	// Create connection with minimal options
	conn, err := grpc.NewClient(
		target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}

	client := frost.NewFrostServiceClient(conn)
	return &FrostClient{
		conn:   conn,
		Client: client,
	}, nil
}

// Close closes the client connection
func (c *FrostClient) Close() error {
	return c.conn.Close()
}
