package ent

import (
	"context"
	"fmt"

	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"google.golang.org/protobuf/proto"
)

// MarshalProto converts a Transfer to a spark protobuf Transfer.
func (t *TransferLeaf) MarshalProto(ctx context.Context) (*pb.TransferLeaf, error) {
	leaf, err := t.QueryLeaf().WithTree().WithSigningKeyshare().WithParent().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to query leaf for transfer leaf %s: %w", t.ID, err)
	}
	return t.marshalTransferLeafProto(ctx, leaf)
}

func (t *TransferLeaf) marshalTransferLeafProto(ctx context.Context, leaf *TreeNode) (*pb.TransferLeaf, error) {
	leafProto, err := leaf.MarshalSparkProto(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to marshal leaf %s: %w", leaf.ID, err)
	}
	var keyTweakProof []byte
	secretCipher := t.SecretCipher
	signature := t.Signature
	signatureScheme := pbcommon.SignatureScheme(t.SignatureScheme)
	if len(t.KeyTweak) != 0 {
		leafKeyTweak := &pb.SendLeafKeyTweak{}
		if err = proto.Unmarshal(t.KeyTweak, leafKeyTweak); err == nil {
			keyTweakProof = leafKeyTweak.GetSecretShareTweak().GetProofs()[0]
			if len(secretCipher) == 0 {
				secretCipher = leafKeyTweak.GetSecretCipher()
			}
			if len(signature) == 0 {
				if ts := leafKeyTweak.GetTypedSignature(); ts != nil {
					signature = ts.GetSignature()
					signatureScheme = ts.GetScheme()
				} else {
					// Source bytes and scheme together: the blob's legacy arm
					// carries no scheme, so ignore any stale column value.
					signature = leafKeyTweak.GetSignature()
					signatureScheme = pbcommon.SignatureScheme_SIGNATURE_SCHEME_UNSPECIFIED
				}
			}
		}
	}

	pbLeaf := &pb.TransferLeaf{
		Leaf:                               leafProto,
		SecretCipher:                       secretCipher,
		IntermediateRefundTx:               t.IntermediateRefundTx,
		IntermediateDirectRefundTx:         t.IntermediateDirectRefundTx,
		IntermediateDirectFromCpfpRefundTx: t.IntermediateDirectFromCpfpRefundTx,
		PendingKeyTweakPublicKey:           keyTweakProof,
	}
	// Re-split storage back into the wire's mutually-exclusive `sig` oneof: the
	// legacy bytes form for ECDSA (unspecified scheme), the scheme-tagged form
	// otherwise.
	if len(signature) > 0 {
		if signatureScheme == pbcommon.SignatureScheme_SIGNATURE_SCHEME_UNSPECIFIED {
			pbLeaf.Sig = &pb.TransferLeaf_Signature{Signature: signature}
		} else {
			pbLeaf.Sig = &pb.TransferLeaf_TypedSignature{
				TypedSignature: &pbcommon.Signature{Scheme: signatureScheme, Signature: signature},
			}
		}
	}
	if t.TransferReceiverID != nil {
		pbLeaf.TransferReceiverId = t.TransferReceiverID.String()
	}
	if t.TransferSenderID != nil {
		pbLeaf.TransferSenderId = t.TransferSenderID.String()
	}
	return pbLeaf, nil
}
