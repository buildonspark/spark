package protohash

import (
	"testing"

	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestBasicHasher(t *testing.T) {
	// Test with a simple wrapper message
	msg := &wrapperspb.StringValue{Value: "hello world"}

	_, err := Hash(msg)
	if err == nil {
		t.Fatalf("Expected error for top-level scalar/value wrapper, got nil")
	}
}

func TestNilMessage(t *testing.T) {
	_, err := HashMessage(nil)
	if err == nil {
		t.Fatal("Expected an error when hashing a nil message, but got nil")
	}
}
