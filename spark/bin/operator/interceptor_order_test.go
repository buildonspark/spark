package main

import (
	"os"
	"strings"
	"testing"
)

func TestUnaryInterceptorOrderDoesNotLetIdempotencyBypassAuthzOrValidation(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}

	unaryStart := strings.Index(string(source), "grpc.UnaryInterceptor(grpcmiddleware.ChainUnaryServer(")
	if unaryStart < 0 {
		t.Fatal("unary interceptor chain not found")
	}
	unaryEnd := strings.Index(string(source[unaryStart:]), "grpc.StreamInterceptor(")
	if unaryEnd < 0 {
		t.Fatal("stream interceptor chain marker not found")
	}
	unaryChain := string(source[unaryStart : unaryStart+unaryEnd])

	authzIndex := strings.Index(unaryChain, "authz.NewAuthzInterceptor")
	validationIndex := strings.Index(unaryChain, "sparkgrpc.ValidationInterceptor()")
	idempotencyIndex := strings.Index(unaryChain, "sparkgrpc.IdempotencyInterceptor()")
	if authzIndex < 0 || validationIndex < 0 || idempotencyIndex < 0 {
		t.Fatalf("expected authz, validation, and idempotency interceptors in unary chain")
	}
	if authzIndex > idempotencyIndex {
		t.Fatal("idempotency interceptor must run after authz so cache hits cannot bypass internal-service allowlists")
	}
	if validationIndex > idempotencyIndex {
		t.Fatal("idempotency interceptor must run after validation so cache hits cannot bypass request-shape checks")
	}
}
