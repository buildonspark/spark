package frost

import (
	"context"
	"testing"
)

func TestEcho(t *testing.T) {
	// Create a temporary socket file
	socketPath := "/tmp/frost.sock"

	// Initialize client
	client, err := NewClient(socketPath)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	defer client.Close()

	// Test echo function
	ctx := context.Background()
	testMessage := "hello world"
	
	response, err := client.Echo(ctx, testMessage)
	if err != nil {
		t.Fatalf("Echo failed: %v", err)
	}

	expectedResponse := "echo: " + testMessage
	if response != expectedResponse {
		t.Errorf("Expected response %q, got %q", expectedResponse, response)
	}
}
