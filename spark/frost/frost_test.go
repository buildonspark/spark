package frost

import (
	"context"
	"testing"

	"github.com/lightsparkdev/spark-go/proto"
)

func TestEcho(t *testing.T) {
	// Create a temporary socket file
	socketPath := "unix:///tmp/frost.sock"

	// Initialize client
	client, err := NewFrostClient(socketPath)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	defer client.Close()

	// Test echo function
	ctx := context.Background()
	testMessage := "hello world"

	request := &proto.EchoRequest{
		Message: testMessage,
	}

	response, err := client.Client.Echo(ctx, request)
	if err != nil {
		t.Fatalf("Echo failed: %v", err)
	}

	expectedResponse := "echo: " + testMessage
	if response.Message != expectedResponse {
		t.Errorf("Expected response %q, got %q", expectedResponse, response)
	}
}

func TestDkgRound1(t *testing.T) {
	// Create a temporary socket file
	socketPath := "/tmp/frost.sock"

	// Initialize client
	client, err := NewFrostClient(socketPath)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	defer client.Close()

	// Test echo function
	ctx := context.Background()

	request := &proto.DkgRound1Request{
		Identifier: "0000000000000000000000000000000000000000000000000000000000000001",
		MaxSigners: 10,
		MinSigners: 5,
		KeyCount:   3,
	}

	response, err := client.Client.DkgRound1(ctx, request)
	if err != nil {
		t.Fatalf("Echo failed: %v", err)
	}

	if len(response.Round1Packages) != 3 {
		t.Errorf("Expected 3 round 1 packages, got %d", len(response.Round1Packages))
	}
}
