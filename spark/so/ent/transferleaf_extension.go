package ent

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"google.golang.org/protobuf/proto"
)

// TransferLeafKeyTweakUpdateInput represents a per-row update payload for transfer_leafs.
// If Signature or SecretCipher are nil, those columns are left unchanged.
type TransferLeafKeyTweakUpdateInput struct {
	ID           uuid.UUID
	KeyTweak     []byte
	Signature    []byte
	SecretCipher []byte
}

// BatchSetTransferLeafKeyTweaks updates only key_tweak for the provided ids in a single SQL statement.
// Returns the number of rows affected.
func BatchSetTransferLeafKeyTweaks(ctx context.Context, updates map[uuid.UUID][]byte) (int, error) {
	if len(updates) == 0 {
		return 0, nil
	}
	inputs := make([]TransferLeafKeyTweakUpdateInput, 0, len(updates))
	for id, tweak := range updates {
		inputs = append(inputs, TransferLeafKeyTweakUpdateInput{ID: id, KeyTweak: tweak})
	}
	return BatchUpdateTransferLeafKeyTweaks(ctx, inputs)
}

// BatchUpdateTransferLeafKeyTweaks performs a single batched UPDATE for key_tweak (and optionally
// signature and secret_cipher) using a VALUES join.
// If Signature or SecretCipher are nil for a row, the existing value is preserved.
// Returns the total number of rows affected across all chunks.
func BatchUpdateTransferLeafKeyTweaks(ctx context.Context, inputs []TransferLeafKeyTweakUpdateInput) (int, error) {
	tx, err := GetDbFromContext(ctx)
	if err != nil {
		return 0, fmt.Errorf("unable to get db: %w", err)
	}
	// Guard against extremely large statements. Each row uses 4 params.
	const maxRowsPerChunk = 1000
	total := 0
	for start := 0; start < len(inputs); start += maxRowsPerChunk {
		end := start + maxRowsPerChunk
		if end > len(inputs) {
			end = len(inputs)
		}
		chunk := inputs[start:end]

		// Build VALUES list: (id, key_tweak, signature, secret_cipher)
		// We set signature/secret_cipher using COALESCE to preserve existing when NULL.
		var sb strings.Builder
		sb.WriteString("UPDATE transfer_leafs AS t SET ")
		sb.WriteString("key_tweak = v.key_tweak, ")
		sb.WriteString("signature = COALESCE(v.signature, t.signature), ")
		sb.WriteString("secret_cipher = COALESCE(v.secret_cipher, t.secret_cipher), ")
		sb.WriteString("update_time = NOW() ")
		sb.WriteString("FROM (VALUES ")

		args := make([]any, 0, len(chunk)*4)
		for i, in := range chunk {
			if i > 0 {
				sb.WriteString(",")
			}
			// ($1,$2,$3,$4), ...
			base := i*4 + 1
			sb.WriteString(fmt.Sprintf("($%d::uuid,$%d::bytea,$%d::bytea,$%d::bytea)", base, base+1, base+2, base+3))
			args = append(args, in.ID, in.KeyTweak, in.Signature, in.SecretCipher)
		}
		sb.WriteString(") AS v(id, key_tweak, signature, secret_cipher) ")
		sb.WriteString("WHERE t.id = v.id")

		// nolint:forbidigo
		res, execErr := tx.ExecContext(ctx, sb.String(), args...)
		if execErr != nil {
			return total, execErr
		}
		MarkTxDirty(ctx)
		if n, countErr := res.RowsAffected(); countErr == nil {
			total += int(n)
		}
	}
	return total, nil
}

// MarshalProto converts a TransferLeaf to a spark protobuf TransferLeaf.
func (t *TransferLeaf) MarshalProto(ctx context.Context) (*pb.TransferLeaf, error) {
	leaf, err := t.QueryLeaf().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to query leaf for transfer leaf %s: %w", t.ID, err)
	}
	leafProto, err := leaf.MarshalSparkProto(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to marshal leaf %s: %w", leaf.ID, err)
	}

	var keyTweakProof []byte
	secretCipher := t.SecretCipher
	signature := t.Signature
	if len(t.KeyTweak) != 0 {
		leafKeyTweak := &pb.SendLeafKeyTweak{}
		if err = proto.Unmarshal(t.KeyTweak, leafKeyTweak); err == nil {
			if sst := leafKeyTweak.GetSecretShareTweak(); sst != nil {
				proofs := sst.GetProofs()
				if len(proofs) > 0 {
					keyTweakProof = proofs[0]
				}
			}
			if len(secretCipher) == 0 {
				secretCipher = leafKeyTweak.SecretCipher
			}
			if len(signature) == 0 {
				signature = leafKeyTweak.Signature
			}
		}
	}

	return &pb.TransferLeaf{
		Leaf:                               leafProto,
		SecretCipher:                       secretCipher,
		Signature:                          signature,
		IntermediateRefundTx:               t.IntermediateRefundTx,
		IntermediateDirectRefundTx:         t.IntermediateDirectRefundTx,
		IntermediateDirectFromCpfpRefundTx: t.IntermediateDirectFromCpfpRefundTx,
		PendingKeyTweakPublicKey:           keyTweakProof,
	}, nil
}
