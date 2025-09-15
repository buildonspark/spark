package wallet

import (
	"bytes"
	"context"
	"fmt"

	"github.com/lightsparkdev/spark/common/keys"

	"github.com/btcsuite/btcd/wire"
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
		return false, fmt.Errorf("failed to parse refund tx: %w", err)
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
	config *TestWalletConfig,
	leaf *pb.TreeNode,
	signingPrivKey keys.Private,
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
		return nil, fmt.Errorf("failed to parse refund tx: %w", err)
	}
	currSequence := newRefundTx.TxIn[0].Sequence
	newRefundTx.TxIn[0].Sequence, err = spark.NextSequence(currSequence)
	if err != nil {
		return nil, fmt.Errorf("failed to increment sequence: %w", err)
	}

	var newRefundTxBuf bytes.Buffer
	err = newRefundTx.Serialize(&newRefundTxBuf)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize new refund tx: %w", err)
	}

	nonce, err := objects.RandomSigningNonce()
	if err != nil {
		return nil, fmt.Errorf("failed to generate nonce: %w", err)
	}
	nonceCommitmentProto, err := nonce.SigningCommitment().MarshalProto()
	if err != nil {
		return nil, fmt.Errorf("failed to marshal nonce commitment: %w", err)
	}
	signingJobs := []*pb.SigningJob{{
		SigningPublicKey:       signingPrivKey.Public().Serialize(),
		RawTx:                  newRefundTxBuf.Bytes(),
		SigningNonceCommitment: nonceCommitmentProto,
	}}
	nonces := []*objects.SigningNonce{nonce}

	// Connect and call GRPC
	sparkConn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to coordinator: %w", err)
	}
	defer sparkConn.Close()

	token, err := AuthenticateWithConnection(ctx, config, sparkConn)
	if err != nil {
		return nil, fmt.Errorf("failed to authenticate with server: %w", err)
	}
	authCtx := ContextWithToken(ctx, token)

	sparkClient := pb.NewSparkServiceClient(sparkConn)
	response, err := sparkClient.RefreshTimelockV2(authCtx, &pb.RefreshTimelockRequest{
		LeafId:                 leaf.Id,
		OwnerIdentityPublicKey: config.IdentityPublicKey().Serialize(),
		SigningJobs:            signingJobs,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to refresh timelock: %w", err)
	}

	if len(signingJobs) != len(response.SigningResults) {
		return nil, fmt.Errorf("number of signing jobs and signing results do not match: %v != %v", len(signingJobs), len(response.SigningResults))
	}

	// Sign and aggregate
	var userSigningJobs []*pbfrost.FrostSigningJob
	jobToAggregateRequestMap := map[string]*pbfrost.AggregateFrostRequest{}
	jobToNodeIDMap := map[string]string{}
	for i, signingResult := range response.SigningResults {
		nonce := nonces[i]
		signingJob := signingJobs[i]
		refundTx, err := common.TxFromRawTxBytes(signingJob.RawTx)
		if err != nil {
			return nil, fmt.Errorf("failed to parse refund tx: %w", err)
		}
		nodeTx, err := common.TxFromRawTxBytes(leaf.NodeTx)
		if err != nil {
			return nil, fmt.Errorf("failed to parse node tx: %w", err)
		}
		refundTxSighash, err := common.SigHashFromTx(refundTx, 0, nodeTx.TxOut[0])
		if err != nil {
			return nil, fmt.Errorf("failed to calculate sighash: %w", err)
		}

		signingNonce, err := nonce.MarshalProto()
		if err != nil {
			return nil, fmt.Errorf("failed to marshal nonce: %w", err)
		}
		signingNonceCommitment, err := nonce.SigningCommitment().MarshalProto()
		if err != nil {
			return nil, fmt.Errorf("failed to marshal nonce commitment: %w", err)
		}
		userKeyPackage := CreateUserKeyPackage(signingPrivKey)

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
			UserPublicKey:   signingPrivKey.Public().Serialize(),
		}

		jobToNodeIDMap[userSigningJobID] = leaf.Id
	}

	frostConn, err := config.NewFrostGRPCConnection()
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

	var nodeSignatures []*pb.NodeSignatures
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

	resp, err := sparkClient.FinalizeNodeSignaturesV2(authCtx, &pb.FinalizeNodeSignaturesRequest{
		Intent:         pbcommon.SignatureIntent_REFRESH,
		NodeSignatures: nodeSignatures,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to finalize node signatures: %w", err)
	}

	return resp.Nodes[0], nil
}

func signingJobFromTx(newTx *wire.MsgTx, signingPrivKey keys.Private) (*pb.SigningJob, *objects.SigningNonce, error) {
	var newTxBuf bytes.Buffer
	err := newTx.Serialize(&newTxBuf)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to serialize new refund tx: %w", err)
	}

	nonce, err := objects.RandomSigningNonce()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate nonce: %w", err)
	}
	nonceCommitmentProto, err := nonce.SigningCommitment().MarshalProto()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal nonce commitment: %w", err)
	}

	signingJob := &pb.SigningJob{
		SigningPublicKey:       signingPrivKey.Public().Serialize(),
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
	config *TestWalletConfig,
	nodes []*pb.TreeNode,
	parentNode *pb.TreeNode,
	signingPrivKey keys.Private,
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
			return nil, fmt.Errorf("failed to parse node tx: %w", err)
		}
		if i == 0 {
			currSequence := newTx.TxIn[0].Sequence
			newTx.TxIn[0].Sequence, err = spark.NextSequence(currSequence)
			if err != nil {
				return nil, fmt.Errorf("failed to increment sequence: %w", err)
			}
			// No need to change outpoint since parent did not change
		} else {
			newTx.TxIn[0].Sequence = spark.InitialSequence()
			newTx.TxIn[0].PreviousOutPoint.Hash = newNodeTxs[i-1].TxHash()
		}

		signingJob, nonce, err := signingJobFromTx(newTx, signingPrivKey)
		if err != nil {
			return nil, fmt.Errorf("failed to create signing job: %w", err)
		}
		signingJobs[i] = signingJob
		nonces[i] = nonce
		newNodeTxs[i] = newTx
	}

	// Add one more job for the refund tx
	leaf := nodes[len(nodes)-1]
	newRefundTx, err := common.TxFromRawTxBytes(leaf.RefundTx)
	if err != nil {
		return nil, fmt.Errorf("failed to parse refund tx: %w", err)
	}
	newRefundTx.TxIn[0].Sequence = spark.InitialSequence()
	newRefundTx.TxIn[0].PreviousOutPoint.Hash = newNodeTxs[len(newNodeTxs)-1].TxHash()
	signingJob, nonce, err := signingJobFromTx(newRefundTx, signingPrivKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create signing job: %w", err)
	}
	signingJobs[len(signingJobs)-1] = signingJob
	nonces[len(nonces)-1] = nonce

	// Connect and call GRPC
	sparkConn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to coordinator: %w", err)
	}
	defer sparkConn.Close()

	token, err := AuthenticateWithConnection(ctx, config, sparkConn)
	if err != nil {
		return nil, fmt.Errorf("failed to authenticate with server: %w", err)
	}
	authCtx := ContextWithToken(ctx, token)

	sparkClient := pb.NewSparkServiceClient(sparkConn)
	response, err := sparkClient.RefreshTimelockV2(authCtx, &pb.RefreshTimelockRequest{
		LeafId:                 leaf.Id,
		OwnerIdentityPublicKey: config.IdentityPublicKey().Serialize(),
		SigningJobs:            signingJobs,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to refresh timelock: %w", err)
	}

	if len(signingJobs) != len(response.SigningResults) {
		return nil, fmt.Errorf("number of signing jobs and signing results do not match: %v != %v", len(signingJobs), len(response.SigningResults))
	}

	// Sign and aggregate
	var userSigningJobs []*pbfrost.FrostSigningJob
	jobToAggregateRequestMap := map[string]*pbfrost.AggregateFrostRequest{}
	jobToNodeIDMap := map[string]string{}
	refundJobID := ""
	leafNodeJobID := ""
	for i, signingResult := range response.SigningResults {
		nonce := nonces[i]
		signingJob := signingJobs[i]
		rawTx, err := common.TxFromRawTxBytes(signingJob.RawTx)
		if err != nil {
			return nil, fmt.Errorf("failed to parse refund tx: %w", err)
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
				return nil, fmt.Errorf("failed to parse parent tx: %w", err)
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
			return nil, fmt.Errorf("failed to calculate sighash: %w", err)
		}

		signingNonce, err := nonce.MarshalProto()
		if err != nil {
			return nil, fmt.Errorf("failed to marshal nonce: %w", err)
		}
		signingNonceCommitment, err := nonce.SigningCommitment().MarshalProto()
		if err != nil {
			return nil, fmt.Errorf("failed to marshal nonce commitment: %w", err)
		}
		userKeyPackage := CreateUserKeyPackage(signingPrivKey)

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
			UserPublicKey:   signingPrivKey.Public().Serialize(),
		}

		jobToNodeIDMap[userSigningJobID] = nodeID
	}

	frostConn, err := config.NewFrostGRPCConnection()
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

	var nodeSignatures []*pb.NodeSignatures
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

	finalResp, err := sparkClient.FinalizeNodeSignaturesV2(authCtx, &pb.FinalizeNodeSignaturesRequest{
		Intent:         pbcommon.SignatureIntent_REFRESH,
		NodeSignatures: nodeSignatures,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to finalize node signatures: %w", err)
	}

	return finalResp.Nodes, nil
}

func ExtendTimelock(
	ctx context.Context,
	config *TestWalletConfig,
	node *pb.TreeNode,
	signingPrivKey keys.Private,
) error {
	// Insert a new node in between the current refund and the node tx
	nodeTx, err := common.TxFromRawTxBytes(node.NodeTx)
	if err != nil {
		return fmt.Errorf("failed to parse node tx: %w", err)
	}

	refundTx, err := common.TxFromRawTxBytes(node.RefundTx)
	if err != nil {
		return fmt.Errorf("failed to parse refund tx: %w", err)
	}

	// Create new node tx to spend the node tx and send to a new refund tx
	refundSequence := refundTx.TxIn[0].Sequence
	newNodeSequence, err := spark.NextSequence(refundSequence)
	if err != nil {
		return fmt.Errorf("failed to increment sequence: %w", err)
	}
	newNodeOutPoint := wire.OutPoint{Hash: nodeTx.TxHash(), Index: 0}
	newNodeTx := createLeafNodeTx(newNodeSequence, &newNodeOutPoint, nodeTx.TxOut[0])

	// Create new refund tx to spend the new node tx
	// (signing pubkey is used here as the destination for convenience,
	// though normally it should just be the same output as the refund tx)
	newRefundOutPoint := wire.OutPoint{Hash: newNodeTx.TxHash(), Index: 0}
	cpfpRefundTx, _, err := createRefundTxs(spark.InitialSequence(), &newRefundOutPoint, refundTx.TxOut[0].Value, signingPrivKey.Public(), false)
	if err != nil {
		return fmt.Errorf("failed to create refund tx: %w", err)
	}

	// Create signing jobs
	newNodeSigningJob, newNodeNonce, err := signingJobFromTx(newNodeTx, signingPrivKey)
	if err != nil {
		return fmt.Errorf("failed to create signing job: %w", err)
	}
	newRefundSigningJob, newRefundNonce, err := signingJobFromTx(cpfpRefundTx, signingPrivKey)
	if err != nil {
		return fmt.Errorf("failed to create signing job: %w", err)
	}

	// Send to SO to sign
	sparkConn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		return fmt.Errorf("failed to connect to coordinator: %w", err)
	}
	defer sparkConn.Close()

	token, err := AuthenticateWithConnection(ctx, config, sparkConn)
	if err != nil {
		return fmt.Errorf("failed to authenticate with server: %w", err)
	}
	authCtx := ContextWithToken(ctx, token)

	sparkClient := pb.NewSparkServiceClient(sparkConn)
	response, err := sparkClient.ExtendLeafV2(authCtx, &pb.ExtendLeafRequest{
		LeafId:                 node.Id,
		OwnerIdentityPublicKey: config.IdentityPublicKey().Serialize(),
		NodeTxSigningJob:       newNodeSigningJob,
		RefundTxSigningJob:     newRefundSigningJob,
	})
	if err != nil {
		return fmt.Errorf("failed to extend leaf: %w", err)
	}

	// Sign and aggregate
	newNodeSignFrostJob, newNodeAggFrostJob, err := createFrostJobsFromTx(newNodeTx, nodeTx.TxOut[0], newNodeNonce, signingPrivKey, response.NodeTxSigningResult)
	if err != nil {
		return fmt.Errorf("failed to create node frost signing job: %w", err)
	}
	newRefundSignFrostJob, newRefundAggFrostJob, err := createFrostJobsFromTx(cpfpRefundTx, newNodeTx.TxOut[0], newRefundNonce, signingPrivKey, response.RefundTxSigningResult)
	if err != nil {
		return fmt.Errorf("failed to create refund frost signing job: %w", err)
	}

	frostConn, err := config.NewFrostGRPCConnection()
	if err != nil {
		return err
	}
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
		return fmt.Errorf("failed to aggregate node tx: %w", err)
	}
	newRefundResp, err := frostClient.AggregateFrost(context.Background(), newRefundAggFrostJob)
	if err != nil {
		return fmt.Errorf("failed to aggregate refund tx: %w", err)
	}

	// Finalize signatures
	_, err = sparkClient.FinalizeNodeSignaturesV2(authCtx, &pb.FinalizeNodeSignaturesRequest{
		Intent: pbcommon.SignatureIntent_EXTEND,
		NodeSignatures: []*pb.NodeSignatures{{
			NodeId:            response.LeafId,
			NodeTxSignature:   newNodeResp.Signature,
			RefundTxSignature: newRefundResp.Signature,
		}},
	})
	if err != nil {
		return fmt.Errorf("failed to finalize node signatures: %w", err)
	}

	// Call it a day
	return nil
}

func ExtendTimelockUsingRenew(
	ctx context.Context,
	config *TestWalletConfig,
	node *pb.TreeNode,
	parentNode *pb.TreeNode,
	signingPrivKey keys.Private,
) (*pb.RenewLeafResponse, error) {
	nodeTx, err := common.TxFromRawTxBytes(node.NodeTx)
	if err != nil {
		return nil, fmt.Errorf("failed to parse node tx: %w", err)
	}
	refundTx, err := common.TxFromRawTxBytes(node.RefundTx)
	if err != nil {
		return nil, fmt.Errorf("failed to parse refund tx: %w", err)
	}

	// Decrement timelock of original node tx
	decrementedNodeTx := nodeTx.Copy()
	if len(decrementedNodeTx.TxIn) != 1 {
		return nil, fmt.Errorf("expected 1 input in tx %s: %w", decrementedNodeTx.TxHash(), err)
	}
	decrementedNodeTx.TxIn[0].SignatureScript = nil
	decrementedNodeTx.TxIn[0].Witness = nil
	decrementedNodeTx.TxIn[0].Sequence = spark.ZeroSequence

	// Create new node tx to spend the original node tx and send to a new refund tx
	newNodeOutPoint := wire.OutPoint{Hash: decrementedNodeTx.TxHash(), Index: 0}
	newNodeTx := createLeafNodeTxWithAnchor(spark.InitialSequence(), &newNodeOutPoint, nodeTx.TxOut[0])

	// Create new refund tx to spend the new node tx
	newRefundOutPoint := wire.OutPoint{Hash: newNodeTx.TxHash(), Index: 0}
	cpfpRefundTx, _, err := createRefundTxs(spark.InitialSequence(), &newRefundOutPoint, refundTx.TxOut[0].Value, signingPrivKey.Public(), false)
	if err != nil {
		return nil, fmt.Errorf("failed to create refund tx: %w", err)
	}

	// Get signing commitments
	sparkConn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		return nil, fmt.Errorf("failed to create grpc connection: %w", err)
	}
	defer sparkConn.Close()
	token, err := AuthenticateWithConnection(ctx, config, sparkConn)
	if err != nil {
		return nil, fmt.Errorf("failed to authenticate with server: %w", err)
	}
	authCtx := ContextWithToken(ctx, token)
	sparkClient := pb.NewSparkServiceClient(sparkConn)
	signingCommitments, err := sparkClient.GetSigningCommitments(authCtx, &pb.GetSigningCommitmentsRequest{
		NodeIds: []string{node.Id},
		Count:   3,
	})
	if err != nil {
		return nil, err
	}

	// Prepare signing jobs
	parentNodeTx, err := common.TxFromRawTxBytes(parentNode.NodeTx)
	if err != nil {
		return nil, err
	}
	signingJobs := []*pbfrost.FrostSigningJob{}
	userCommitments := []*objects.SigningCommitment{}

	// decremented node tx
	decrementedNodeSigningJob, decrementedNodeCommitment, err := createSigningJobForRenewLeaf(
		decrementedNodeTx, parentNodeTx, "Decremented node tx",
		signingCommitments.SigningCommitments[0], node, signingPrivKey,
	)
	if err != nil {
		return nil, err
	}
	signingJobs = append(signingJobs, decrementedNodeSigningJob)
	userCommitments = append(userCommitments, decrementedNodeCommitment)

	// node tx
	nodeSigningJob, nodeCommitment, err := createSigningJobForRenewLeaf(
		newNodeTx, decrementedNodeTx, "Node tx",
		signingCommitments.SigningCommitments[1], node, signingPrivKey,
	)
	if err != nil {
		return nil, err
	}
	signingJobs = append(signingJobs, nodeSigningJob)
	userCommitments = append(userCommitments, nodeCommitment)

	// refund tx
	refundSigningJob, refundCommitment, err := createSigningJobForRenewLeaf(
		cpfpRefundTx, newNodeTx, "Refund tx",
		signingCommitments.SigningCommitments[2], node, signingPrivKey,
	)
	if err != nil {
		return nil, err
	}
	signingJobs = append(signingJobs, refundSigningJob)
	userCommitments = append(userCommitments, refundCommitment)

	// Sign user refund.
	signerConn, err := config.NewFrostGRPCConnection()
	if err != nil {
		return nil, err
	}
	defer signerConn.Close()
	signerClient := pbfrost.NewFrostServiceClient(signerConn)
	fmt.Printf("signing jobs: %v\n", signingJobs)
	signingResults, err := signerClient.SignFrost(ctx, &pbfrost.SignFrostRequest{
		SigningJobs: signingJobs,
		Role:        pbfrost.SigningRole_USER,
	})
	if err != nil {
		return nil, err
	}
	if len(signingResults.Results) != len(signingJobs) {
		return nil, fmt.Errorf("expected %d signing results, got %d", len(signingJobs), len(signingResults.Results))
	}

	signingResultsArray := make([]*pbcommon.SigningResult, 0, len(signingResults.Results))
	for _, job := range signingJobs {
		signingResultsArray = append(signingResultsArray, signingResults.Results[job.JobId])
	}

	// Serialize transactions to bytes
	decrementedNodeTxBytes, err := common.SerializeTx(decrementedNodeTx)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize decremented node tx: %w", err)
	}
	newNodeTxBytes, err := common.SerializeTx(newNodeTx)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize new node tx: %w", err)
	}
	cpfpRefundTxBytes, err := common.SerializeTx(cpfpRefundTx)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize refund tx: %w", err)
	}

	// Create signing jobs
	decrementedNodeUserCommitmentProto, err := userCommitments[0].MarshalProto()
	if err != nil {
		return nil, fmt.Errorf("failed to marshal user commitment: %w", err)
	}
	decrementedNodeUserSigningJob := &pb.UserSignedTxSigningJob{
		LeafId:                 node.Id,
		SigningPublicKey:       node.OwnerSigningPublicKey,
		SigningNonceCommitment: decrementedNodeUserCommitmentProto,
		UserSignature:          signingResultsArray[0].SignatureShare,
		RawTx:                  decrementedNodeTxBytes,
		SigningCommitments: &pb.SigningCommitments{
			SigningCommitments: signingCommitments.SigningCommitments[0].SigningNonceCommitments,
		},
	}

	nodeUserCommitmentProto, err := userCommitments[1].MarshalProto()
	if err != nil {
		return nil, fmt.Errorf("failed to marshal user commitment: %w", err)
	}
	newNodeSigningJob := &pb.UserSignedTxSigningJob{
		LeafId:                 node.Id,
		SigningPublicKey:       node.OwnerSigningPublicKey,
		SigningNonceCommitment: nodeUserCommitmentProto,
		UserSignature:          signingResultsArray[1].SignatureShare,
		RawTx:                  newNodeTxBytes,
		SigningCommitments: &pb.SigningCommitments{
			SigningCommitments: signingCommitments.SigningCommitments[1].SigningNonceCommitments,
		},
	}

	refundUserCommitmentProto, err := userCommitments[2].MarshalProto()
	if err != nil {
		return nil, fmt.Errorf("failed to marshal user commitment: %w", err)
	}
	cpfpRefundSigningJob := &pb.UserSignedTxSigningJob{
		LeafId:                 node.Id,
		SigningPublicKey:       node.OwnerSigningPublicKey,
		SigningNonceCommitment: refundUserCommitmentProto,
		UserSignature:          signingResultsArray[2].SignatureShare,
		RawTx:                  cpfpRefundTxBytes,
		SigningCommitments: &pb.SigningCommitments{
			SigningCommitments: signingCommitments.SigningCommitments[2].SigningNonceCommitments,
		},
	}

	// Send to SO to sign
	response, err := sparkClient.RenewLeaf(authCtx, &pb.RenewLeafRequest{
		LeafId: node.Id,
		SigningJobs: &pb.RenewLeafRequest_RenewNodeTimelockSigningJob{
			RenewNodeTimelockSigningJob: &pb.RenewNodeTimelockSigningJob{
				SplitNodeTxSigningJob: decrementedNodeUserSigningJob,
				NodeTxSigningJob:      newNodeSigningJob,
				RefundTxSigningJob:    cpfpRefundSigningJob,
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to extend leaf: %w", err)
	}

	// Return the response
	return response, nil
}

func RefreshTimelockUsingRenew(
	ctx context.Context,
	config *TestWalletConfig,
	node *pb.TreeNode,
	parentNode *pb.TreeNode,
	signingPrivKey keys.Private,
) (*pb.RenewLeafResponse, error) {
	nodeTx, err := common.TxFromRawTxBytes(node.NodeTx)
	if err != nil {
		return nil, fmt.Errorf("failed to parse node tx: %w", err)
	}
	refundTx, err := common.TxFromRawTxBytes(node.RefundTx)
	if err != nil {
		return nil, fmt.Errorf("failed to parse refund tx: %w", err)
	}

	// Create new node tx with next sequence from original node tx
	newNodeTx := nodeTx.Copy()
	for _, txin := range newNodeTx.TxIn {
		txin.SignatureScript = nil
		txin.Witness = nil
		txin.Sequence, err = spark.NextSequence(txin.Sequence)
		if err != nil {
			return nil, fmt.Errorf("failed to increment sequence: %w", err)
		}
	}

	// Create new refund tx to spend the new node tx with initial sequence
	newRefundOutPoint := wire.OutPoint{Hash: newNodeTx.TxHash(), Index: 0}
	cpfpRefundTx, _, err := createRefundTxs(spark.InitialSequence(), &newRefundOutPoint, refundTx.TxOut[0].Value, signingPrivKey.Public(), false)
	if err != nil {
		return nil, fmt.Errorf("failed to create refund tx: %w", err)
	}

	// Get signing commitments
	sparkConn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		return nil, fmt.Errorf("failed to create grpc connection: %w", err)
	}
	defer sparkConn.Close()
	token, err := AuthenticateWithConnection(ctx, config, sparkConn)
	if err != nil {
		return nil, fmt.Errorf("failed to authenticate with server: %w", err)
	}
	authCtx := ContextWithToken(ctx, token)
	sparkClient := pb.NewSparkServiceClient(sparkConn)
	signingCommitments, err := sparkClient.GetSigningCommitments(authCtx, &pb.GetSigningCommitmentsRequest{
		NodeIds: []string{node.Id},
		Count:   2,
	})
	if err != nil {
		return nil, err
	}

	// Prepare signing jobs (only 2 transactions: node and refund)
	signingJobs := []*pbfrost.FrostSigningJob{}
	userCommitments := []*objects.SigningCommitment{}

	// node tx
	parentNodeTx, err := common.TxFromRawTxBytes(parentNode.NodeTx)
	if err != nil {
		return nil, err
	}
	nodeSigningJob, nodeCommitment, err := createSigningJobForRenewLeaf(
		newNodeTx, parentNodeTx, "Node tx",
		signingCommitments.SigningCommitments[0], node, signingPrivKey,
	)
	if err != nil {
		return nil, err
	}
	signingJobs = append(signingJobs, nodeSigningJob)
	userCommitments = append(userCommitments, nodeCommitment)

	// refund tx
	refundSigningJob, refundCommitment, err := createSigningJobForRenewLeaf(
		cpfpRefundTx, newNodeTx, "Refund tx",
		signingCommitments.SigningCommitments[1], node, signingPrivKey,
	)
	if err != nil {
		return nil, err
	}
	signingJobs = append(signingJobs, refundSigningJob)
	userCommitments = append(userCommitments, refundCommitment)

	// Sign user transactions
	signerConn, err := config.NewFrostGRPCConnection()
	if err != nil {
		return nil, err
	}
	defer signerConn.Close()
	signerClient := pbfrost.NewFrostServiceClient(signerConn)
	signingResults, err := signerClient.SignFrost(ctx, &pbfrost.SignFrostRequest{
		SigningJobs: signingJobs,
		Role:        pbfrost.SigningRole_USER,
	})
	if err != nil {
		return nil, err
	}
	if len(signingResults.Results) != len(signingJobs) {
		return nil, fmt.Errorf("expected %d signing results, got %d", len(signingJobs), len(signingResults.Results))
	}

	signingResultsArray := make([]*pbcommon.SigningResult, 0, len(signingResults.Results))
	for _, job := range signingJobs {
		signingResultsArray = append(signingResultsArray, signingResults.Results[job.JobId])
	}

	// Serialize transactions to bytes
	newNodeTxBytes, err := common.SerializeTx(newNodeTx)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize new node tx: %w", err)
	}
	cpfpRefundTxBytes, err := common.SerializeTx(cpfpRefundTx)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize refund tx: %w", err)
	}

	// Create signing jobs for refresh (only 2 transactions)
	nodeUserCommitmentProto, err := userCommitments[0].MarshalProto()
	if err != nil {
		return nil, fmt.Errorf("failed to marshal user commitment: %w", err)
	}
	newNodeSigningJob := &pb.UserSignedTxSigningJob{
		LeafId:                 node.Id,
		SigningPublicKey:       node.OwnerSigningPublicKey,
		SigningNonceCommitment: nodeUserCommitmentProto,
		UserSignature:          signingResultsArray[0].SignatureShare,
		RawTx:                  newNodeTxBytes,
		SigningCommitments: &pb.SigningCommitments{
			SigningCommitments: signingCommitments.SigningCommitments[0].SigningNonceCommitments,
		},
	}

	refundUserCommitmentProto, err := userCommitments[1].MarshalProto()
	if err != nil {
		return nil, fmt.Errorf("failed to marshal user commitment: %w", err)
	}
	cpfpRefundSigningJob := &pb.UserSignedTxSigningJob{
		LeafId:                 node.Id,
		SigningPublicKey:       node.OwnerSigningPublicKey,
		SigningNonceCommitment: refundUserCommitmentProto,
		UserSignature:          signingResultsArray[1].SignatureShare,
		RawTx:                  cpfpRefundTxBytes,
		SigningCommitments: &pb.SigningCommitments{
			SigningCommitments: signingCommitments.SigningCommitments[1].SigningNonceCommitments,
		},
	}

	// Send to SO to sign with refresh signing job
	response, err := sparkClient.RenewLeaf(authCtx, &pb.RenewLeafRequest{
		LeafId: node.Id,
		SigningJobs: &pb.RenewLeafRequest_RenewRefundTimelockSigningJob{
			RenewRefundTimelockSigningJob: &pb.RenewRefundTimelockSigningJob{
				NodeTxSigningJob:   newNodeSigningJob,
				RefundTxSigningJob: cpfpRefundSigningJob,
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to refresh leaf: %w", err)
	}

	// Return the response
	return response, nil
}

func createFrostJobsFromTx(
	tx *wire.MsgTx,
	parentTxOut *wire.TxOut,
	nonce *objects.SigningNonce,
	signingPrivKey keys.Private,
	signingResult *pb.ExtendLeafSigningResult,
) (*pbfrost.FrostSigningJob, *pbfrost.AggregateFrostRequest, error) {
	sigHash, err := common.SigHashFromTx(tx, 0, parentTxOut)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to calculate sighash: %w", err)
	}
	signingNonce, err := nonce.MarshalProto()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal nonce: %w", err)
	}
	signingNonceCommitment, err := nonce.SigningCommitment().MarshalProto()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal nonce commitment: %w", err)
	}
	frostKeyPackage := CreateUserKeyPackage(signingPrivKey)
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
		UserPublicKey:   signingPrivKey.Public().Serialize(),
	}
	return signingJob, aggregateJob, nil
}

func createSigningJobForRenewLeaf(
	tx *wire.MsgTx,
	parentTx *wire.MsgTx,
	txName string,
	signingCommitments *pb.RequestedSigningCommitments,
	node *pb.TreeNode,
	signingPrivKey keys.Private,
) (*pbfrost.FrostSigningJob, *objects.SigningCommitment, error) {
	sighash, err := common.SigHashFromTx(tx, 0, parentTx.TxOut[0])
	if err != nil {
		return nil, nil, fmt.Errorf("failed to calculate sighash for %s: %w", txName, err)
	}
	fmt.Printf("%s sighash: %x\n", txName, sighash)

	signingNonce, err := objects.RandomSigningNonce()
	if err != nil {
		return nil, nil, err
	}
	signingNonceProto, err := signingNonce.MarshalProto()
	if err != nil {
		return nil, nil, err
	}
	userCommitmentProto, err := signingNonce.SigningCommitment().MarshalProto()
	if err != nil {
		return nil, nil, err
	}

	userKeyPackage := CreateUserKeyPackage(signingPrivKey)

	signingJobID := uuid.New().String()
	signingJob := &pbfrost.FrostSigningJob{
		JobId:           signingJobID,
		Message:         sighash,
		KeyPackage:      userKeyPackage,
		VerifyingKey:    node.VerifyingPublicKey,
		Nonce:           signingNonceProto,
		Commitments:     signingCommitments.SigningNonceCommitments,
		UserCommitments: userCommitmentProto,
	}

	return signingJob, signingNonce.SigningCommitment(), nil
}
