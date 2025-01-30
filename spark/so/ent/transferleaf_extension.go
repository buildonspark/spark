package ent

import (
	"context"
	"fmt"

	pb "github.com/lightsparkdev/spark-go/proto/spark"
)

// MarshalProto converts a Transfer to a spark protobuf Transfer.
func (t *TransferLeaf) MarshalProto(ctx context.Context) (*pb.TransferLeaf, error) {
	leaf, err := t.QueryLeaf().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to query leaf for transfer leaf %s: %v", t.ID.String(), err)
	}
	signingKeyshare, err := leaf.QuerySigningKeyshare().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to query signing keyshare for leaf %s: %v", leaf.ID.String(), err)
	}

	return &pb.TransferLeaf{
		Leaf:                 leaf.MarshalSparkProto(ctx),
		SecretCipher:         t.SecretCipher,
		Signature:            t.Signature,
		IntermediateRefundTx: t.IntermediateRefundTx,
		SigningKeyshare:      signingKeyshare.MarshalProto(),
	}, nil
}
