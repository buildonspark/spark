package wallet

import (
	"bytes"
	"context"
	"fmt"

	"github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go"
	"github.com/lightsparkdev/spark-go/common"
	pbcommon "github.com/lightsparkdev/spark-go/proto/common"
	pbfrost "github.com/lightsparkdev/spark-go/proto/frost"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	"github.com/lightsparkdev/spark-go/so/objects"
)

type RefreshSigningData struct {
	nonce *objects.SigningNonce
}

// RefreshTimelockRefundTx just decrements the sequence number of the refund tx
// and resigns it with the SO.
// TODO: merge this with RefreshTimelockNodes since they're doing almost the
// same thing.
func RefreshTimelockRefundTx(
	ctx context.Context,
	config *Config,
	leaf *pb.TreeNode,
	signingPrivKey *secp256k1.PrivateKey,
) error {
	// New refund tx is just the old refund tx with a
	// decremented sequence number. Practically,
	// user's probably wouldn't do this, and is here
	// to just demonstrate the genericness of the RPC call.
	// It could function as a cooperation to decrease the
	// timelock if a user plans to unilateral exit soon (but
	// actual SE cooperative unilateral exit will probably
	// be integrated into the aggregation process).
	newRefundTx, err := common.TxFromRawTxBytes(leaf.RefundTx)
	if err != nil {
		return fmt.Errorf("failed to parse refund tx: %v", err)
	}
	currSequence := newRefundTx.TxIn[0].Sequence
	newRefundTx.TxIn[0].Sequence, err = spark.NextSequence(currSequence)
	if err != nil {
		return fmt.Errorf("failed to increment sequence: %v", err)
	}

	var newRefundTxBuf bytes.Buffer
	err = newRefundTx.Serialize(&newRefundTxBuf)
	if err != nil {
		return fmt.Errorf("failed to serialize new refund tx: %v", err)
	}

	nonce, err := objects.RandomSigningNonce()
	if err != nil {
		return fmt.Errorf("failed to generate nonce: %v", err)
	}
	nonceCommitmentProto, err := nonce.SigningCommitment().MarshalProto()
	if err != nil {
		return fmt.Errorf("failed to marshal nonce commitment: %v", err)
	}
	signingJobs := make([]*pb.SigningJob, 0)
	signingJobs = append(signingJobs, &pb.SigningJob{
		SigningPublicKey:       signingPrivKey.PubKey().SerializeCompressed(),
		RawTx:                  newRefundTxBuf.Bytes(),
		SigningNonceCommitment: nonceCommitmentProto,
	})
	signingDatas := []*RefreshSigningData{}
	signingDatas = append(signingDatas, &RefreshSigningData{
		nonce: nonce,
	})

	// Connect and call GRPC
	sparkConn, err := common.NewGRPCConnectionWithoutTLS(config.CoodinatorAddress())
	if err != nil {
		return fmt.Errorf("failed to create grpc connection: %v", err)
	}
	defer sparkConn.Close()

	token, err := AuthenticateWithConnection(ctx, config, sparkConn)
	if err != nil {
		return fmt.Errorf("failed to authenticate with server: %v", err)
	}
	authCtx := ContextWithToken(ctx, token)

	sparkClient := pb.NewSparkServiceClient(sparkConn)
	response, err := sparkClient.RefreshTimelock(authCtx, &pb.RefreshTimelockRequest{
		LeafId:                 leaf.Id,
		OwnerIdentityPublicKey: config.IdentityPublicKey(),
		SigningJobs:            signingJobs,
	})
	if err != nil {
		return fmt.Errorf("failed to refresh timelock: %v", err)
	}

	if len(signingJobs) != len(response.SigningResults) {
		return fmt.Errorf("number of signing jobs and signing results do not match: %v != %v", len(signingJobs), len(response.SigningResults))
	}

	// Sign and aggregate
	userSigningJobs := []*pbfrost.FrostSigningJob{}
	jobToAggregateRequestMap := map[string]*pbfrost.AggregateFrostRequest{}
	jobToNodeIDMap := map[string]string{}
	for i, signingResult := range response.SigningResults {
		signingData := signingDatas[i]
		signingJob := signingJobs[i]
		refundTx, err := common.TxFromRawTxBytes(signingJob.RawTx)
		if err != nil {
			return fmt.Errorf("failed to parse refund tx: %v", err)
		}
		nodeTx, err := common.TxFromRawTxBytes(leaf.NodeTx)
		if err != nil {
			return fmt.Errorf("failed to parse node tx: %v", err)
		}
		refundTxSighash, err := common.SigHashFromTx(refundTx, 0, nodeTx.TxOut[0])
		if err != nil {
			return fmt.Errorf("failed to calculate sighash: %v", err)
		}

		signingNonce, err := signingData.nonce.MarshalProto()
		if err != nil {
			return fmt.Errorf("failed to marshal nonce: %v", err)
		}
		signingNonceCommitment, err := signingData.nonce.SigningCommitment().MarshalProto()
		if err != nil {
			return fmt.Errorf("failed to marshal nonce commitment: %v", err)
		}
		userKeyPackage := CreateUserKeyPackage(signingPrivKey.Serialize())

		userSigningJobID := uuid.New().String()

		userSigningJobs = append(userSigningJobs, &pbfrost.FrostSigningJob{
			JobId:           userSigningJobID,
			Message:         refundTxSighash,
			KeyPackage:      userKeyPackage,
			VerifyingKey:    signingResult.VerifyingKey,
			Nonce:           signingNonce,
			Commitments:     signingResult.SigningResult.SigningNonceCommitments,
			UserCommitments: signingNonceCommitment,
		})

		jobToAggregateRequestMap[userSigningJobID] = &pbfrost.AggregateFrostRequest{
			Message:         refundTxSighash,
			SignatureShares: signingResult.SigningResult.SignatureShares,
			PublicShares:    signingResult.SigningResult.PublicKeys,
			VerifyingKey:    signingResult.VerifyingKey,
			Commitments:     signingResult.SigningResult.SigningNonceCommitments,
			UserCommitments: signingNonceCommitment,
			UserPublicKey:   signingPrivKey.PubKey().SerializeCompressed(),
		}

		jobToNodeIDMap[userSigningJobID] = leaf.Id
	}

	frostConn, _ := common.NewGRPCConnectionWithoutTLS(config.FrostSignerAddress)
	defer frostConn.Close()
	frostClient := pbfrost.NewFrostServiceClient(frostConn)
	userSignatures, err := frostClient.SignFrost(context.Background(), &pbfrost.SignFrostRequest{
		SigningJobs: userSigningJobs,
		Role:        pbfrost.SigningRole_USER,
	})
	if err != nil {
		return err
	}

	nodeSignatures := []*pb.NodeSignatures{}
	for jobID, userSignature := range userSignatures.Results {
		request := jobToAggregateRequestMap[jobID]
		request.UserSignatureShare = userSignature.SignatureShare
		response, err := frostClient.AggregateFrost(context.Background(), request)
		if err != nil {
			return err
		}
		nodeSignatures = append(nodeSignatures, &pb.NodeSignatures{
			NodeId:            jobToNodeIDMap[jobID],
			RefundTxSignature: response.Signature,
		})
	}

	_, err = sparkClient.FinalizeNodeSignatures(authCtx, &pb.FinalizeNodeSignaturesRequest{
		Intent:         pbcommon.SignatureIntent_REFRESH,
		NodeSignatures: nodeSignatures,
	})
	if err != nil {
		return fmt.Errorf("failed to finalize node signatures: %v", err)
	}

	return nil
}

func signingJobFromTx(
	newTx *wire.MsgTx,
	signingPrivKey *secp256k1.PrivateKey,
) (*pb.SigningJob, *objects.SigningNonce, error) {
	var newTxBuf bytes.Buffer
	err := newTx.Serialize(&newTxBuf)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to serialize new refund tx: %v", err)
	}

	nonce, err := objects.RandomSigningNonce()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate nonce: %v", err)
	}
	nonceCommitmentProto, err := nonce.SigningCommitment().MarshalProto()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal nonce commitment: %v", err)
	}

	signingJob := &pb.SigningJob{
		SigningPublicKey:       signingPrivKey.PubKey().SerializeCompressed(),
		RawTx:                  newTxBuf.Bytes(),
		SigningNonceCommitment: nonceCommitmentProto,
	}
	return signingJob, nonce, nil
}

// RefreshTimelockNodes takes the nodes, decrements the sequence number
// of the first node, resets the sequence number of the rest of nodes
// (adding the refund tx of the last node), and resigns the txs with the SO.
func RefreshTimelockNodes(
	ctx context.Context,
	config *Config,
	nodes []*pb.TreeNode,
	parentNodes []*pb.TreeNode,
	signingPrivKey *secp256k1.PrivateKey,
) error {
	if len(nodes) == 0 {
		return fmt.Errorf("no nodes to refresh")
	}

	signingJobs := make([]*pb.SigningJob, len(nodes)+1)
	nonces := make([]*objects.SigningNonce, len(nodes)+1)

	for i, node := range nodes {
		newTx, err := common.TxFromRawTxBytes(node.NodeTx)
		if err != nil {
			return fmt.Errorf("failed to parse node tx: %v", err)
		}
		if i == 0 {
			currSequence := newTx.TxIn[0].Sequence
			newTx.TxIn[0].Sequence, err = spark.NextSequence(currSequence)
			if err != nil {
				return fmt.Errorf("failed to increment sequence: %v", err)
			}
		} else {
			newTx.TxIn[0].Sequence = spark.InitialSequence()
		}

		signingJob, nonce, err := signingJobFromTx(newTx, signingPrivKey)
		if err != nil {
			return fmt.Errorf("failed to create signing job: %v", err)
		}
		signingJobs[i] = signingJob
		nonces[i] = nonce
	}

	// Add one more job for the refund tx
	leaf := nodes[len(nodes)-1]
	newRefundTx, err := common.TxFromRawTxBytes(leaf.RefundTx)
	if err != nil {
		return fmt.Errorf("failed to parse refund tx: %v", err)
	}
	newRefundTx.TxIn[0].Sequence = spark.InitialSequence()
	signingJob, nonce, err := signingJobFromTx(newRefundTx, signingPrivKey)
	if err != nil {
		return fmt.Errorf("failed to create signing job: %v", err)
	}
	signingJobs[len(signingJobs)-1] = signingJob
	nonces[len(nonces)-1] = nonce

	// Connect and call GRPC
	sparkConn, err := common.NewGRPCConnectionWithoutTLS(config.CoodinatorAddress())
	if err != nil {
		return fmt.Errorf("failed to create grpc connection: %v", err)
	}
	defer sparkConn.Close()

	token, err := AuthenticateWithConnection(ctx, config, sparkConn)
	if err != nil {
		return fmt.Errorf("failed to authenticate with server: %v", err)
	}
	authCtx := ContextWithToken(ctx, token)

	sparkClient := pb.NewSparkServiceClient(sparkConn)
	response, err := sparkClient.RefreshTimelock(authCtx, &pb.RefreshTimelockRequest{
		LeafId:                 leaf.Id,
		OwnerIdentityPublicKey: config.IdentityPublicKey(),
		SigningJobs:            signingJobs,
	})
	if err != nil {
		return fmt.Errorf("failed to refresh timelock: %v", err)
	}

	if len(signingJobs) != len(response.SigningResults) {
		return fmt.Errorf("number of signing jobs and signing results do not match: %v != %v", len(signingJobs), len(response.SigningResults))
	}

	// Sign and aggregate
	userSigningJobs := []*pbfrost.FrostSigningJob{}
	jobToAggregateRequestMap := map[string]*pbfrost.AggregateFrostRequest{}
	jobToNodeIDMap := map[string]string{}
	jobToRefundMap := map[string]bool{}
	for i, signingResult := range response.SigningResults {
		nonce := nonces[i]
		signingJob := signingJobs[i]
		rawTx, err := common.TxFromRawTxBytes(signingJob.RawTx)
		if err != nil {
			return fmt.Errorf("failed to parse refund tx: %v", err)
		}

		// Get parent node for txout for sighash
		var parentNode *pb.TreeNode
		var node *pb.TreeNode
		var refund bool
		var vout int
		if i == len(nodes) {
			// Refund tx
			node = nodes[i-1]
			refund = true
			parentNode = nodes[i-1]
			vout = 0
		} else {
			node = nodes[i]
			refund = false
			parentNode = parentNodes[i]
			vout = int(node.Vout)
		}
		parentTx, err := common.TxFromRawTxBytes(parentNode.NodeTx)
		if err != nil {
			return fmt.Errorf("failed to parse parent tx: %v", err)
		}
		txOut := parentTx.TxOut[vout]

		rawTxSighash, err := common.SigHashFromTx(rawTx, 0, txOut)
		if err != nil {
			return fmt.Errorf("failed to calculate sighash: %v", err)
		}

		signingNonce, err := nonce.MarshalProto()
		if err != nil {
			return fmt.Errorf("failed to marshal nonce: %v", err)
		}
		signingNonceCommitment, err := nonce.SigningCommitment().MarshalProto()
		if err != nil {
			return fmt.Errorf("failed to marshal nonce commitment: %v", err)
		}
		userKeyPackage := CreateUserKeyPackage(signingPrivKey.Serialize())

		userSigningJobID := uuid.New().String()

		userSigningJobs = append(userSigningJobs, &pbfrost.FrostSigningJob{
			JobId:           userSigningJobID,
			Message:         rawTxSighash,
			KeyPackage:      userKeyPackage,
			VerifyingKey:    signingResult.VerifyingKey,
			Nonce:           signingNonce,
			Commitments:     signingResult.SigningResult.SigningNonceCommitments,
			UserCommitments: signingNonceCommitment,
		})

		jobToAggregateRequestMap[userSigningJobID] = &pbfrost.AggregateFrostRequest{
			Message:         rawTxSighash,
			SignatureShares: signingResult.SigningResult.SignatureShares,
			PublicShares:    signingResult.SigningResult.PublicKeys,
			VerifyingKey:    signingResult.VerifyingKey,
			Commitments:     signingResult.SigningResult.SigningNonceCommitments,
			UserCommitments: signingNonceCommitment,
			UserPublicKey:   signingPrivKey.PubKey().SerializeCompressed(),
		}

		jobToNodeIDMap[userSigningJobID] = node.Id
		jobToRefundMap[userSigningJobID] = refund
	}

	frostConn, _ := common.NewGRPCConnectionWithoutTLS(config.FrostSignerAddress)
	defer frostConn.Close()
	frostClient := pbfrost.NewFrostServiceClient(frostConn)
	userSignatures, err := frostClient.SignFrost(context.Background(), &pbfrost.SignFrostRequest{
		SigningJobs: userSigningJobs,
		Role:        pbfrost.SigningRole_USER,
	})
	if err != nil {
		return err
	}

	nodeSignatures := []*pb.NodeSignatures{}
	for jobID, userSignature := range userSignatures.Results {
		request := jobToAggregateRequestMap[jobID]
		request.UserSignatureShare = userSignature.SignatureShare
		response, err := frostClient.AggregateFrost(context.Background(), request)
		if err != nil {
			return err
		}
		if jobToRefundMap[jobID] {
			nodeSignatures = append(nodeSignatures, &pb.NodeSignatures{
				NodeId:            jobToNodeIDMap[jobID],
				RefundTxSignature: response.Signature,
			})
		} else {
			nodeSignatures = append(nodeSignatures, &pb.NodeSignatures{
				NodeId:          jobToNodeIDMap[jobID],
				NodeTxSignature: response.Signature,
			})
		}
	}

	_, err = sparkClient.FinalizeNodeSignatures(authCtx, &pb.FinalizeNodeSignaturesRequest{
		Intent:         pbcommon.SignatureIntent_REFRESH,
		NodeSignatures: nodeSignatures,
	})
	if err != nil {
		return fmt.Errorf("failed to finalize node signatures: %v", err)
	}

	return nil
}
