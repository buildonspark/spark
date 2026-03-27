package grpc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/authn"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/idempotencykey"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

func IdempotencyInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		key := extractIdempotencyKey(ctx)

		if key == "" {
			return handler(ctx, req)
		}

		if ent.IsReadOnlySession(ctx) {
			return handler(ctx, req)
		}

		identity := extractIdentity(ctx)
		identityBytes := identity.Serialize()
		if identityBytes == nil {
			identityBytes = []byte{}
		}

		cachedResp, err := createAndLockIdempotencyRecord(ctx, key, info.FullMethod, identityBytes)
		if err != nil {
			return nil, fmt.Errorf("failed to create idempotency record: %w", err)
		}
		if cachedResp != nil {
			return cachedResp, nil
		}

		resp, err := handler(ctx, req)
		if err != nil {
			return resp, err
		}

		if err := storeResponse(ctx, key, info.FullMethod, identityBytes, resp); err != nil {
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
	if values := md.Get(common.IdempotencyKeyHeader); len(values) > 0 {
		return values[0]
	}
	return ""
}

// extractIdentity returns the public key of the authenticated user,
// or a zero-value keys.Public if no session is in context (internal SO-to-SO calls).
func extractIdentity(ctx context.Context) keys.Public {
	session, err := authn.GetSessionFromContext(ctx)
	if err != nil {
		return keys.Public{}
	}
	return session.IdentityPublicKey()
}

func createAndLockIdempotencyRecord(ctx context.Context, key string, methodName string, identity []byte) (any, error) {
	tx, err := ent.GetTxFromContext(ctx)
	if err != nil {
		return nil, err
	}

	err = createIdempotencyRecord(ctx, key, methodName, identity)
	if err != nil {
		return nil, fmt.Errorf("failed to create idempotency record: %w", err)
	}

	idempotencyRecord, err := tx.IdempotencyKey.Query().
		Where(
			idempotencykey.IdempotencyKeyEQ(key),
			idempotencykey.MethodNameEQ(methodName),
			idempotencykey.IdentityPublicKey(identity),
		).
		ForUpdate().
		Only(ctx)

	if ent.IsNotFound(err) {
		return nil, fmt.Errorf("idempotent request failed, please try again")
	} else if err != nil {
		return nil, fmt.Errorf("failed to lock idempotency record: %w", err)
	} else if idempotencyRecord.Response == nil {
		return nil, nil
	}

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

func createIdempotencyRecord(ctx context.Context, key string, methodName string, identity []byte) error {
	tx, err := ent.GetTxFromContext(ctx)
	if err != nil {
		return err
	}

	err = tx.IdempotencyKey.Create().
		SetIdempotencyKey(key).
		SetMethodName(methodName).
		SetIdentityPublicKey(identity).
		OnConflictColumns("idempotency_key", "method_name", "identity_public_key").
		DoNothing().
		Exec(ctx)

	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("failed to create idempotency record: %w", err)
	}

	return nil
}

func storeResponse(ctx context.Context, key string, methodName string, identity []byte, resp any) error {
	msg, ok := resp.(proto.Message)
	if !ok {
		return fmt.Errorf("response expected to be proto.Message, got %T", resp)
	}
	anyMsg, err := anypb.New(msg)
	if err != nil {
		return fmt.Errorf("failed to wrap response in Any: %w", err)
	}

	mo := protojson.MarshalOptions{
		UseProtoNames:   true,
		EmitUnpopulated: true,
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
			idempotencykey.IdentityPublicKey(identity),
		).
		SetResponse(rawJSON).
		Save(ctx)

	return err
}
