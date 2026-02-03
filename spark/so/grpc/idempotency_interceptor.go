package grpc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/idempotencykey"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

const IdempotencyKeyHeader = "x-idempotency-key"

func IdempotencyInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		key := extractIdempotencyKey(ctx)

		if key == "" {
			// Non-idempotent request
			return handler(ctx, req)
		}

		cachedResp, err := createAndLockIdempotencyRecord(ctx, key, info.FullMethod)
		if err != nil {
			return nil, fmt.Errorf("failed to create idempotency record: %w", err)
		}
		if cachedResp != nil {
			return cachedResp, nil
		}

		// Process request
		resp, err := handler(ctx, req)
		if err != nil {
			return resp, err
		}

		// Store response
		if err := storeResponse(ctx, key, info.FullMethod, resp); err != nil {
			return nil, fmt.Errorf("failed to store idempotent response: %w", err)
		}

		return resp, nil
	}
}

func extractIdempotencyKey(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	if values := md.Get(IdempotencyKeyHeader); len(values) > 0 {
		return values[0]
	}
	return ""
}

func createAndLockIdempotencyRecord(ctx context.Context, key string, methodName string) (any, error) {
	tx, err := ent.GetTxFromContext(ctx)
	if err != nil {
		return nil, err
	}

	// We try to create the record first, so we are guaranteed to get a lock on the row
	// in the below ForUpdate call. This method is a no-op if the record already exists.
	err = createIdempotencyRecord(ctx, key, methodName)
	if err != nil {
		return nil, fmt.Errorf("failed to create idempotency record: %w", err)
	}

	// Lock it, we will wait here for any in-flight requests with the same key to finish
	idempotencyRecord, err := tx.IdempotencyKey.Query().
		Where(
			idempotencykey.IdempotencyKeyEQ(key),
			idempotencykey.MethodNameEQ(methodName),
		).
		ForUpdate().
		Only(ctx)

	if ent.IsNotFound(err) {
		// Previous request rolled back and deleted the record.
		// We'll treat this as a cache miss, and error on all queued requests.
		return nil, fmt.Errorf("idempotent request failed, please try again")
	} else if err != nil {
		return nil, fmt.Errorf("failed to lock idempotency record: %w", err)
	} else if idempotencyRecord.Response == nil {
		// This request won the race to process, no cached response yet
		return nil, nil
	}

	// Cache hit
	var anyMsg anypb.Any
	if err := protojson.Unmarshal([]byte(idempotencyRecord.Response), &anyMsg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal cached response: %w", err)
	}

	msg, err := anyMsg.UnmarshalNew()
	if err != nil {
		return nil, fmt.Errorf("failed to unwrap cached response: %w", err)
	}

	return msg, nil
}

func createIdempotencyRecord(ctx context.Context, key string, methodName string) error {
	tx, err := ent.GetTxFromContext(ctx)
	if err != nil {
		return err
	}

	// Use INSERT ... ON CONFLICT DO NOTHING to avoid transaction abort
	err = tx.IdempotencyKey.Create().
		SetIdempotencyKey(key).
		SetMethodName(methodName).
		OnConflictColumns("idempotency_key", "method_name").
		DoNothing().
		Exec(ctx)

	// Create expects a returning row, but ON CONFLICT DO NOTHING returns 0 rows.
	// As 0 rows is expected in conflict cases, ignore sql.ErrNoRows.
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("failed to create idempotency record: %w", err)
	}

	return nil
}

func storeResponse(ctx context.Context, key string, methodName string, resp any) error {
	msg, ok := resp.(proto.Message)
	if !ok {
		return fmt.Errorf("response expected to be proto.Message, got %T", resp)
	}
	anyMsg, err := anypb.New(msg)
	if err != nil {
		return fmt.Errorf("failed to wrap response in Any: %w", err)
	}

	mo := protojson.MarshalOptions{
		UseProtoNames:   true, // Preserves original field names (snake_case)
		EmitUnpopulated: true, // Prints zero-values (0, false, "") for better DB readability
	}
	jsonBytes, err := mo.Marshal(anyMsg)
	if err != nil {
		return err
	}

	rawJSON := json.RawMessage(jsonBytes)

	tx, err := ent.GetTxFromContext(ctx)
	if err != nil {
		return err
	}

	_, err = tx.IdempotencyKey.Update().
		Where(
			idempotencykey.IdempotencyKeyEQ(key),
			idempotencykey.MethodNameEQ(methodName),
		).
		SetResponse(rawJSON).
		Save(ctx)

	return err
}
