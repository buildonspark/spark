package ent

import (
	"context"
	"fmt"

	pb "github.com/lightsparkdev/spark/proto/spark"
	"google.golang.org/protobuf/proto"
)

// MarshalProto converts a Transfer to a spark protobuf Transfer.
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
			keyTweakProof = leafKeyTweak.GetSecretShareTweak().GetProofs()[0]
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
