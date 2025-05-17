package wallet

import (
	"bytes"
	"context"
	"fmt"
	"log"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pbfrost "github.com/lightsparkdev/spark/proto/frost"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/objects"
)

// AggregateTreeNodes aggregates the tree nodes and returns the new node.
func AggregateTreeNodes(
	ctx context.Context,
	config *Config,
	nodes []*pb.TreeNode,
	parentNode *pb.TreeNode,
	aggregatedSigningKey []byte,
) (*pb.FinalizeNodeSignaturesResponse, error) {
	sparkConn, err := common.NewGRPCConnectionWithTestTLS(config.CoodinatorAddress(), nil)
	if err != nil {
		return nil, err
	}
	defer sparkConn.Close()
	token, err := AuthenticateWithConnection(ctx, config, sparkConn)
	if err != nil {
		return nil, err
	}
	ctx = ContextWithToken(ctx, token)
	sparkClient := pb.NewSparkServiceClient(sparkConn)

	rawTx := parentNode.NodeTx
	parentID := parentNode.Id
	for _, node := range nodes {
		if node.ParentNodeId != nil && *node.ParentNodeId != parentID {
			return nil, fmt.Errorf("node parent ids are not the same")
		}
	}

	parentTx, err := common.TxFromRawTxBytes(rawTx)
	if err != nil {
		return nil, err
	}

	aggregatedSigningPublicKey := secp256k1.PrivKeyFromBytes(aggregatedSigningKey).PubKey()

	parentRefundTx, err := common.TxFromRawTxBytes(parentNode.RefundTx)
	if err != nil {
		return nil, fmt.Errorf("failed to parse parent refund tx: %v", err)
	}
	sequence, err := spark.NextSequence(uint32(parentRefundTx.TxIn[0].Sequence))
	if err != nil {
		return nil, fmt.Errorf("failed to get next sequence: %v", err)
	}
	parentOutPoint := wire.OutPoint{Hash: parentTx.TxHash(), Index: uint32(parentNode.Vout)}
	cpfpRefundTx, _, err := createRefundTxs(sequence, &parentOutPoint,
		parentTx.TxOut[parentNode.Vout].Value, aggregatedSigningPublicKey, false)
	if err != nil {
		return nil, err
	}
	var refundBuf bytes.Buffer
	err = cpfpRefundTx.Serialize(&refundBuf)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize refund tx: %v", err)
	}

	signingNonce, err := objects.RandomSigningNonce()
	if err != nil {
		return nil, err
	}

	signingNonceCommitmentProto, err := signingNonce.SigningCommitment().MarshalProto()
	if err != nil {
		return nil, err
	}

	signingJob := &pb.SigningJob{
		RawTx:                  refundBuf.Bytes(),
		SigningPublicKey:       aggregatedSigningPublicKey.SerializeCompressed(),
		SigningNonceCommitment: signingNonceCommitmentProto,
	}

	nodeIDs := make([]string, len(nodes))
	for i, node := range nodes {
		nodeIDs[i] = node.Id
	}

	aggResp, err := sparkClient.AggregateNodes(ctx, &pb.AggregateNodesRequest{
		NodeIds:                nodeIDs,
		SigningJob:             signingJob,
		OwnerIdentityPublicKey: config.IdentityPublicKey(),
	})
	if err != nil {
		log.Printf("failed to aggregate nodes: %v", err)
		return nil, err
	}

	userKeyPackage := CreateUserKeyPackage(aggregatedSigningKey)
	refundSighash, err := common.SigHashFromTx(cpfpRefundTx, 0, parentTx.TxOut[parentNode.Vout])
	if err != nil {
		return nil, err
	}

	userSigningJobs := make([]*pbfrost.FrostSigningJob, 0)
	nodeJobID := uuid.NewString()
	signingNonceProto, err := signingNonce.MarshalProto()
	if err != nil {
		return nil, err
	}
	userSigningJobs = append(userSigningJobs, &pbfrost.FrostSigningJob{
		JobId:           nodeJobID,
		Message:         refundSighash,
		KeyPackage:      userKeyPackage,
		VerifyingKey:    aggResp.VerifyingKey,
		Nonce:           signingNonceProto,
		Commitments:     aggResp.AggregateSignature.SigningNonceCommitments,
		UserCommitments: signingNonceCommitmentProto,
	})

	frostConn, err := common.NewGRPCConnectionWithoutTLS(config.FrostSignerAddress, nil)
	if err != nil {
		return nil, err
	}
	defer frostConn.Close()

	frostClient := pbfrost.NewFrostServiceClient(frostConn)

	userSignatures, err := frostClient.SignFrost(context.Background(), &pbfrost.SignFrostRequest{
		SigningJobs: userSigningJobs,
		Role:        pbfrost.SigningRole_USER,
	})
	if err != nil {
		return nil, err
	}

	refundSignature, err := frostClient.AggregateFrost(context.Background(), &pbfrost.AggregateFrostRequest{
		Message:            refundSighash,
		SignatureShares:    aggResp.AggregateSignature.SignatureShares,
		PublicShares:       aggResp.AggregateSignature.PublicKeys,
		VerifyingKey:       aggResp.VerifyingKey,
		Commitments:        aggResp.AggregateSignature.SigningNonceCommitments,
		UserCommitments:    signingNonceCommitmentProto,
		UserPublicKey:      aggregatedSigningPublicKey.SerializeCompressed(),
		UserSignatureShare: userSignatures.Results[nodeJobID].SignatureShare,
	})
	if err != nil {
		return nil, err
	}

	sig, err := schnorr.ParseSignature(refundSignature.Signature)
	if err != nil {
		return nil, err
	}

	pubKey, err := btcec.ParsePubKey(aggResp.VerifyingKey)
	if err != nil {
		return nil, err
	}
	taprootKey := txscript.ComputeTaprootKeyNoScript(pubKey)

	verified := sig.Verify(refundSighash, taprootKey)
	if !verified {
		return nil, fmt.Errorf("signature verification failed")
	}

	return sparkClient.FinalizeNodeSignatures(context.Background(), &pb.FinalizeNodeSignaturesRequest{
		Intent: pbcommon.SignatureIntent_AGGREGATE,
		NodeSignatures: []*pb.NodeSignatures{
			{
				NodeId:            parentID,
				RefundTxSignature: refundSignature.Signature,
			},
		},
	})
}
