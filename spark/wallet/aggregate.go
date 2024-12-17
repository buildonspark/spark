package wallet

import (
	"bytes"
	"context"
	"fmt"
	"log"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrd/dcrec/secp256k1"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go/common"
	pbcommon "github.com/lightsparkdev/spark-go/proto/common"
	pbfrost "github.com/lightsparkdev/spark-go/proto/frost"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	"github.com/lightsparkdev/spark-go/so/objects"
)

// AggregateTreeNodes aggregates the tree nodes and returns the new node.
func AggregateTreeNodes(
	ctx context.Context,
	config *Config,
	nodes []*pb.TreeNode,
	aggregatedSigningKey []byte,
) (*pb.FinalizeNodeSignaturesResponse, error) {
	sparkConn, err := common.NewGRPCConnection(config.CoodinatorAddress())
	if err != nil {
		return nil, err
	}
	defer sparkConn.Close()
	sparkClient := pb.NewSparkServiceClient(sparkConn)

	rawTx := nodes[0].NodeTx
	parentID := *nodes[0].ParentNodeId
	for _, node := range nodes {
		if !bytes.Equal(rawTx, node.NodeTx) {
			return nil, fmt.Errorf("node txs are not the same")
		}
		log.Printf("node parent id: %s, parent id: %s", node.ParentNodeId, parentID)
		if node.ParentNodeId != nil && *node.ParentNodeId != parentID {
			return nil, fmt.Errorf("node parent ids are not the same")
		}
	}

	nodeTx, err := common.TxFromRawTxBytes(rawTx)
	if err != nil {
		return nil, err
	}

	_, aggregatedSigningPublicKey := secp256k1.PrivKeyFromBytes(aggregatedSigningKey)

	newRefundTx := wire.NewMsgTx(2)
	newRefundTx.AddTxIn(wire.NewTxIn(
		&nodeTx.TxIn[0].PreviousOutPoint,
		nodeTx.TxIn[0].SignatureScript,
		nil, // witness
	))
	refundP2trAddress, _ := common.P2TRAddressFromPublicKey(aggregatedSigningPublicKey.SerializeCompressed(), config.Network)
	refundAddress, _ := btcutil.DecodeAddress(*refundP2trAddress, common.NetworkParams(config.Network))
	refundPkScript, _ := txscript.PayToAddrScript(refundAddress)
	newRefundTx.AddTxOut(wire.NewTxOut(nodeTx.TxOut[0].Value, refundPkScript))
	// TODO(zhenlu): Lock time should be from parent node
	newRefundTx.LockTime = 60000
	var refundBuf bytes.Buffer
	newRefundTx.Serialize(&refundBuf)

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
	parentTx, err := common.TxFromRawTxBytes(aggResp.ParentNodeTx)
	if err != nil {
		return nil, err
	}
	refundSighash, err := common.SigHashFromTx(newRefundTx, 0, parentTx.TxOut[aggResp.ParentNodeVout])
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

	frostConn, err := common.NewGRPCConnection(config.FrostSignerAddress)
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
