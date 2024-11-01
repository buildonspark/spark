package frost

import (
	"context"
	"log"

	frost "github.com/lightsparkdev/spark-go/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Client represents a FROST gRPC client
type Client struct {
	conn   *grpc.ClientConn
	client frost.FrostServiceClient
}

// NewClient creates a new FROST client connected to the given Unix socket
func NewClient(socketPath string) (*Client, error) {
    target := "unix://" + socketPath
    
    // Create connection with minimal options
    conn, err := grpc.Dial(
        target,
        grpc.WithTransportCredentials(insecure.NewCredentials()),
        grpc.WithBlock(),
    )
    if err != nil {
        log.Fatalf("Failed to connect: %v", err)
    }


	client := frost.NewFrostServiceClient(conn)
	return &Client{
		conn:   conn,
		client: client,
	}, nil
}

// Close closes the client connection
func (c *Client) Close() error {
	return c.conn.Close()
}

// Echo sends an echo request to test the connection
func (c *Client) Echo(ctx context.Context, message string) (string, error) {
	resp, err := c.client.Echo(ctx, &frost.EchoRequest{
		Message: message,
	})
	if err != nil {
		return "", err
	}
	return resp.Message, nil
}
