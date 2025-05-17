package wallet

import (
	"bytes"
	"context"
	"fmt"

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

func NeedToRefreshTimelock(
	leaf *pb.TreeNode,
) (bool, error) {
	refundTx, err := common.TxFromRawTxBytes(leaf.RefundTx)
	if err != nil {
		return false, fmt.Errorf("failed to parse refund tx: %v", err)
	}
	if refundTx.TxIn[0].Sequence&0xFFFF-spark.TimeLockInterval <= 0 {
		return true, nil
	}
	return false, nil
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
) (*pb.TreeNode, error) {
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
		return nil, fmt.Errorf("failed to parse refund tx: %v", err)
	}
	currSequence := newRefundTx.TxIn[0].Sequence
	newRefundTx.TxIn[0].Sequence, err = spark.NextSequence(currSequence)
	if err != nil {
		return nil, fmt.Errorf("failed to increment sequence: %v", err)
	}

	var newRefundTxBuf bytes.Buffer
	err = newRefundTx.Serialize(&newRefundTxBuf)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize new refund tx: %v", err)
	}

	nonce, err := objects.RandomSigningNonce()
	if err != nil {
		return nil, fmt.Errorf("failed to generate nonce: %v", err)
	}
	nonceCommitmentProto, err := nonce.SigningCommitment().MarshalProto()
	if err != nil {
		return nil, fmt.Errorf("failed to marshal nonce commitment: %v", err)
	}
	signingJobs := make([]*pb.SigningJob, 0)
	signingJobs = append(signingJobs, &pb.SigningJob{
		SigningPublicKey:       signingPrivKey.PubKey().SerializeCompressed(),
		RawTx:                  newRefundTxBuf.Bytes(),
		SigningNonceCommitment: nonceCommitmentProto,
	})
	nonces := []*objects.SigningNonce{}
	nonces = append(nonces, nonce)

	// Connect and call GRPC
	sparkConn, err := common.NewGRPCConnectionWithTestTLS(config.CoodinatorAddress(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create grpc connection: %v", err)
	}
	defer sparkConn.Close()

	token, err := AuthenticateWithConnection(ctx, config, sparkConn)
	if err != nil {
		return nil, fmt.Errorf("failed to authenticate with server: %v", err)
	}
	authCtx := ContextWithToken(ctx, token)

	sparkClient := pb.NewSparkServiceClient(sparkConn)
	response, err := sparkClient.RefreshTimelock(authCtx, &pb.RefreshTimelockRequest{
		LeafId:                 leaf.Id,
		OwnerIdentityPublicKey: config.IdentityPublicKey(),
		SigningJobs:            signingJobs,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to refresh timelock: %v", err)
	}

	if len(signingJobs) != len(response.SigningResults) {
		return nil, fmt.Errorf("number of signing jobs and signing results do not match: %v != %v", len(signingJobs), len(response.SigningResults))
	}

	// Sign and aggregate
	userSigningJobs := []*pbfrost.FrostSigningJob{}
	jobToAggregateRequestMap := map[string]*pbfrost.AggregateFrostRequest{}
	jobToNodeIDMap := map[string]string{}
	for i, signingResult := range response.SigningResults {
		nonce := nonces[i]
		signingJob := signingJobs[i]
		refundTx, err := common.TxFromRawTxBytes(signingJob.RawTx)
		if err != nil {
			return nil, fmt.Errorf("failed to parse refund tx: %v", err)
		}
		nodeTx, err := common.TxFromRawTxBytes(leaf.NodeTx)
		if err != nil {
			return nil, fmt.Errorf("failed to parse node tx: %v", err)
		}
		refundTxSighash, err := common.SigHashFromTx(refundTx, 0, nodeTx.TxOut[0])
		if err != nil {
			return nil, fmt.Errorf("failed to calculate sighash: %v", err)
		}

		signingNonce, err := nonce.MarshalProto()
		if err != nil {
			return nil, fmt.Errorf("failed to marshal nonce: %v", err)
		}
		signingNonceCommitment, err := nonce.SigningCommitment().MarshalProto()
		if err != nil {
			return nil, fmt.Errorf("failed to marshal nonce commitment: %v", err)
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

	frostConn, _ := common.NewGRPCConnectionWithoutTLS(config.FrostSignerAddress, nil)
	defer frostConn.Close()
	frostClient := pbfrost.NewFrostServiceClient(frostConn)
	userSignatures, err := frostClient.SignFrost(context.Background(), &pbfrost.SignFrostRequest{
		SigningJobs: userSigningJobs,
		Role:        pbfrost.SigningRole_USER,
	})
	if err != nil {
		return nil, err
	}

	nodeSignatures := []*pb.NodeSignatures{}
	for jobID, userSignature := range userSignatures.Results {
		request := jobToAggregateRequestMap[jobID]
		request.UserSignatureShare = userSignature.SignatureShare
		response, err := frostClient.AggregateFrost(context.Background(), request)
		if err != nil {
			return nil, err
		}
		nodeSignatures = append(nodeSignatures, &pb.NodeSignatures{
			NodeId:            jobToNodeIDMap[jobID],
			RefundTxSignature: response.Signature,
		})
	}

	resp, err := sparkClient.FinalizeNodeSignatures(authCtx, &pb.FinalizeNodeSignaturesRequest{
		Intent:         pbcommon.SignatureIntent_REFRESH,
		NodeSignatures: nodeSignatures,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to finalize node signatures: %v", err)
	}

	return resp.Nodes[0], nil
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
	parentNode *pb.TreeNode,
	signingPrivKey *secp256k1.PrivateKey,
) ([]*pb.TreeNode, error) {
	if len(nodes) == 0 {
		return nil, fmt.Errorf("no nodes to refresh")
	}

	signingJobs := make([]*pb.SigningJob, len(nodes)+1)
	nonces := make([]*objects.SigningNonce, len(nodes)+1)

	newNodeTxs := make([]*wire.MsgTx, len(nodes))
	for i, node := range nodes {
		newTx, err := common.TxFromRawTxBytes(node.NodeTx)
		if err != nil {
			return nil, fmt.Errorf("failed to parse node tx: %v", err)
		}
		if i == 0 {
			currSequence := newTx.TxIn[0].Sequence
			newTx.TxIn[0].Sequence, err = spark.NextSequence(currSequence)
			if err != nil {
				return nil, fmt.Errorf("failed to increment sequence: %v", err)
			}
			// No need to change outpoint since parent did not change
		} else {
			newTx.TxIn[0].Sequence = spark.InitialSequence()
			newTx.TxIn[0].PreviousOutPoint.Hash = newNodeTxs[i-1].TxHash()
		}

		signingJob, nonce, err := signingJobFromTx(newTx, signingPrivKey)
		if err != nil {
			return nil, fmt.Errorf("failed to create signing job: %v", err)
		}
		signingJobs[i] = signingJob
		nonces[i] = nonce
		newNodeTxs[i] = newTx
	}

	// Add one more job for the refund tx
	leaf := nodes[len(nodes)-1]
	newRefundTx, err := common.TxFromRawTxBytes(leaf.RefundTx)
	if err != nil {
		return nil, fmt.Errorf("failed to parse refund tx: %v", err)
	}
	newRefundTx.TxIn[0].Sequence = spark.InitialSequence()
	newRefundTx.TxIn[0].PreviousOutPoint.Hash = newNodeTxs[len(newNodeTxs)-1].TxHash()
	signingJob, nonce, err := signingJobFromTx(newRefundTx, signingPrivKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create signing job: %v", err)
	}
	signingJobs[len(signingJobs)-1] = signingJob
	nonces[len(nonces)-1] = nonce

	// Connect and call GRPC
	sparkConn, err := common.NewGRPCConnectionWithTestTLS(config.CoodinatorAddress(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create grpc connection: %v", err)
	}
	defer sparkConn.Close()

	token, err := AuthenticateWithConnection(ctx, config, sparkConn)
	if err != nil {
		return nil, fmt.Errorf("failed to authenticate with server: %v", err)
	}
	authCtx := ContextWithToken(ctx, token)

	sparkClient := pb.NewSparkServiceClient(sparkConn)
	response, err := sparkClient.RefreshTimelock(authCtx, &pb.RefreshTimelockRequest{
		LeafId:                 leaf.Id,
		OwnerIdentityPublicKey: config.IdentityPublicKey(),
		SigningJobs:            signingJobs,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to refresh timelock: %v", err)
	}

	if len(signingJobs) != len(response.SigningResults) {
		return nil, fmt.Errorf("number of signing jobs and signing results do not match: %v != %v", len(signingJobs), len(response.SigningResults))
	}

	// Sign and aggregate
	userSigningJobs := []*pbfrost.FrostSigningJob{}
	jobToAggregateRequestMap := map[string]*pbfrost.AggregateFrostRequest{}
	jobToNodeIDMap := map[string]string{}
	refundJobID := ""
	leafNodeJobID := ""
	for i, signingResult := range response.SigningResults {
		nonce := nonces[i]
		signingJob := signingJobs[i]
		rawTx, err := common.TxFromRawTxBytes(signingJob.RawTx)
		if err != nil {
			return nil, fmt.Errorf("failed to parse refund tx: %v", err)
		}

		// Get parent node for txout for sighash
		var parentTx *wire.MsgTx
		var nodeID string
		var vout int
		if i == len(nodes) {
			// Refund tx
			nodeID = nodes[i-1].Id
			parentTx = newNodeTxs[i-1]
			vout = 0
		} else if i == 0 {
			// First node
			nodeID = nodes[i].Id
			parentTx, err = common.TxFromRawTxBytes(parentNode.NodeTx)
			if err != nil {
				return nil, fmt.Errorf("failed to parse parent tx: %v", err)
			}
			vout = int(nodes[i].Vout)
		} else {
			nodeID = nodes[i].Id
			parentTx = newNodeTxs[i-1]
			vout = int(nodes[i].Vout)
		}
		txOut := parentTx.TxOut[vout]

		rawTxSighash, err := common.SigHashFromTx(rawTx, 0, txOut)
		if err != nil {
			return nil, fmt.Errorf("failed to calculate sighash: %v", err)
		}

		signingNonce, err := nonce.MarshalProto()
		if err != nil {
			return nil, fmt.Errorf("failed to marshal nonce: %v", err)
		}
		signingNonceCommitment, err := nonce.SigningCommitment().MarshalProto()
		if err != nil {
			return nil, fmt.Errorf("failed to marshal nonce commitment: %v", err)
		}
		userKeyPackage := CreateUserKeyPackage(signingPrivKey.Serialize())

		userSigningJobID := uuid.New().String()
		if i == len(nodes) {
			refundJobID = userSigningJobID
		} else if i == len(nodes)-1 {
			leafNodeJobID = userSigningJobID
		}

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

		jobToNodeIDMap[userSigningJobID] = nodeID
	}

	frostConn, _ := common.NewGRPCConnectionWithoutTLS(config.FrostSignerAddress, nil)
	defer frostConn.Close()
	frostClient := pbfrost.NewFrostServiceClient(frostConn)
	userSignatures, err := frostClient.SignFrost(context.Background(), &pbfrost.SignFrostRequest{
		SigningJobs: userSigningJobs,
		Role:        pbfrost.SigningRole_USER,
	})
	if err != nil {
		return nil, err
	}

	nodeSignatures := []*pb.NodeSignatures{}
	for jobID, userSignature := range userSignatures.Results {
		if jobID == refundJobID || jobID == leafNodeJobID {
			continue
		}
		request := jobToAggregateRequestMap[jobID]
		request.UserSignatureShare = userSignature.SignatureShare
		response, err := frostClient.AggregateFrost(context.Background(), request)
		if err != nil {
			return nil, err
		}
		nodeSignatures = append(nodeSignatures, &pb.NodeSignatures{
			NodeId:          jobToNodeIDMap[jobID],
			NodeTxSignature: response.Signature,
		})
	}

	leafRequest := jobToAggregateRequestMap[leafNodeJobID]
	leafRequest.UserSignatureShare = userSignatures.Results[leafNodeJobID].SignatureShare
	leafResponse, err := frostClient.AggregateFrost(context.Background(), leafRequest)
	if err != nil {
		return nil, err
	}
	refundRequest := jobToAggregateRequestMap[refundJobID]
	refundRequest.UserSignatureShare = userSignatures.Results[refundJobID].SignatureShare
	refundResponse, err := frostClient.AggregateFrost(context.Background(), refundRequest)
	if err != nil {
		return nil, err
	}
	nodeSignatures = append(nodeSignatures, &pb.NodeSignatures{
		NodeId:            jobToNodeIDMap[leafNodeJobID],
		NodeTxSignature:   leafResponse.Signature,
		RefundTxSignature: refundResponse.Signature,
	})

	finalResp, err := sparkClient.FinalizeNodeSignatures(authCtx, &pb.FinalizeNodeSignaturesRequest{
		Intent:         pbcommon.SignatureIntent_REFRESH,
		NodeSignatures: nodeSignatures,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to finalize node signatures: %v", err)
	}

	return finalResp.Nodes, nil
}

func ExtendTimelock(
	ctx context.Context,
	config *Config,
	node *pb.TreeNode,
	signingPrivKey *secp256k1.PrivateKey,
) error {
	// Insert a new node in between the current refund and the node tx
	nodeTx, err := common.TxFromRawTxBytes(node.NodeTx)
	if err != nil {
		return fmt.Errorf("failed to parse node tx: %v", err)
	}

	refundTx, err := common.TxFromRawTxBytes(node.RefundTx)
	if err != nil {
		return fmt.Errorf("failed to parse refund tx: %v", err)
	}

	// Create new node tx to spend the node tx and send to a new refund tx
	refundSequence := refundTx.TxIn[0].Sequence
	newNodeSequence, err := spark.NextSequence(refundSequence)
	if err != nil {
		return fmt.Errorf("failed to increment sequence: %v", err)
	}
	newNodeOutPoint := wire.OutPoint{Hash: nodeTx.TxHash(), Index: 0}
	newNodeTx := createLeafNodeTx(newNodeSequence, &newNodeOutPoint, nodeTx.TxOut[0])

	// Create new refund tx to spend the new node tx
	// (signing pubkey is used here as the destination for convenience,
	// though normally it should just be the same output as the refund tx)
	newRefundOutPoint := wire.OutPoint{Hash: newNodeTx.TxHash(), Index: 0}
	cpfpRefundTx, _, err := createRefundTxs(spark.InitialSequence(), &newRefundOutPoint, refundTx.TxOut[0].Value, signingPrivKey.PubKey(), false)
	if err != nil {
		return fmt.Errorf("failed to create refund tx: %v", err)
	}

	// Create signing jobs
	newNodeSigningJob, newNodeNonce, err := signingJobFromTx(newNodeTx, signingPrivKey)
	if err != nil {
		return fmt.Errorf("failed to create signing job: %v", err)
	}
	newRefundSigningJob, newRefundNonce, err := signingJobFromTx(cpfpRefundTx, signingPrivKey)
	if err != nil {
		return fmt.Errorf("failed to create signing job: %v", err)
	}

	// Send to SO to sign
	sparkConn, err := common.NewGRPCConnectionWithTestTLS(config.CoodinatorAddress(), nil)
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
	response, err := sparkClient.ExtendLeaf(authCtx, &pb.ExtendLeafRequest{
		LeafId:                 node.Id,
		OwnerIdentityPublicKey: config.IdentityPublicKey(),
		NodeTxSigningJob:       newNodeSigningJob,
		RefundTxSigningJob:     newRefundSigningJob,
	})
	if err != nil {
		return fmt.Errorf("failed to extend leaf: %v", err)
	}

	// Sign and aggregate
	newNodeSignFrostJob, newNodeAggFrostJob, err := createFrostJobsFromTx(newNodeTx, nodeTx.TxOut[0], newNodeNonce, signingPrivKey, response.NodeTxSigningResult)
	if err != nil {
		return fmt.Errorf("failed to create node frost signing job: %v", err)
	}
	newRefundSignFrostJob, newRefundAggFrostJob, err := createFrostJobsFromTx(cpfpRefundTx, newNodeTx.TxOut[0], newRefundNonce, signingPrivKey, response.RefundTxSigningResult)
	if err != nil {
		return fmt.Errorf("failed to create refund frost signing job: %v", err)
	}

	frostConn, _ := common.NewGRPCConnectionWithoutTLS(config.FrostSignerAddress, nil)
	defer frostConn.Close()
	frostClient := pbfrost.NewFrostServiceClient(frostConn)
	userSignatures, err := frostClient.SignFrost(context.Background(), &pbfrost.SignFrostRequest{
		SigningJobs: []*pbfrost.FrostSigningJob{newNodeSignFrostJob, newRefundSignFrostJob},
		Role:        pbfrost.SigningRole_USER,
	})
	if err != nil {
		return err
	}
	if len(userSignatures.Results) != 2 {
		return fmt.Errorf("expected 2 signing results, got %d", len(userSignatures.Results))
	}
	newNodeAggFrostJob.UserSignatureShare = userSignatures.Results[newNodeSignFrostJob.JobId].SignatureShare
	newRefundAggFrostJob.UserSignatureShare = userSignatures.Results[newRefundSignFrostJob.JobId].SignatureShare

	// Aggregate
	newNodeResp, err := frostClient.AggregateFrost(context.Background(), newNodeAggFrostJob)
	if err != nil {
		return fmt.Errorf("failed to aggregate node tx: %v", err)
	}
	newRefundResp, err := frostClient.AggregateFrost(context.Background(), newRefundAggFrostJob)
	if err != nil {
		return fmt.Errorf("failed to aggregate refund tx: %v", err)
	}

	// Finalize signatures
	_, err = sparkClient.FinalizeNodeSignatures(authCtx, &pb.FinalizeNodeSignaturesRequest{
		Intent: pbcommon.SignatureIntent_EXTEND,
		NodeSignatures: []*pb.NodeSignatures{{
			NodeId:            response.LeafId,
			NodeTxSignature:   newNodeResp.Signature,
			RefundTxSignature: newRefundResp.Signature,
		}},
	})
	if err != nil {
		return fmt.Errorf("failed to finalize node signatures: %v", err)
	}

	// Call it a day
	return nil
}

func createFrostJobsFromTx(
	tx *wire.MsgTx,
	parentTxOut *wire.TxOut,
	nonce *objects.SigningNonce,
	signingPrivKey *secp256k1.PrivateKey,
	signingResult *pb.ExtendLeafSigningResult,
) (*pbfrost.FrostSigningJob, *pbfrost.AggregateFrostRequest, error) {
	sigHash, err := common.SigHashFromTx(tx, 0, parentTxOut)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to calculate sighash: %v", err)
	}
	signingNonce, err := nonce.MarshalProto()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal nonce: %v", err)
	}
	signingNonceCommitment, err := nonce.SigningCommitment().MarshalProto()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal nonce commitment: %v", err)
	}
	frostKeyPackage := CreateUserKeyPackage(signingPrivKey.Serialize())
	userSigningJobID := uuid.New().String()
	signingJob := &pbfrost.FrostSigningJob{
		JobId:           userSigningJobID,
		Message:         sigHash,
		KeyPackage:      frostKeyPackage,
		VerifyingKey:    signingResult.VerifyingKey,
		Nonce:           signingNonce,
		Commitments:     signingResult.SigningResult.SigningNonceCommitments,
		UserCommitments: signingNonceCommitment,
	}
	aggregateJob := &pbfrost.AggregateFrostRequest{
		Message:         sigHash,
		SignatureShares: signingResult.SigningResult.SignatureShares,
		PublicShares:    signingResult.SigningResult.PublicKeys,
		VerifyingKey:    signingResult.VerifyingKey,
		Commitments:     signingResult.SigningResult.SigningNonceCommitments,
		UserCommitments: signingNonceCommitment,
		UserPublicKey:   signingPrivKey.PubKey().SerializeCompressed(),
	}
	return signingJob, aggregateJob, nil
}
