package ent

import (
	"context"
	"fmt"

	pb "github.com/lightsparkdev/spark-go/proto/spark"
)

// MarshalProto converts a Transfer to a spark protobuf Transfer.
func (t *TransferLeaf) MarshalProto(ctx context.Context) (*pb.TransferLeaf, error) {
	leafID, err := t.QueryLeaf().OnlyID(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to query leaf for transfer leaf %s: %v", t.ID.String(), err)
	}
	return &pb.TransferLeaf{
		LeafId:       leafID.String(),
		SecretCipher: t.SecretCipher,
		Signature:    t.Signature,
	}, nil
}
