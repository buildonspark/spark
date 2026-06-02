package wallet

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common"
	bitcointransaction "github.com/lightsparkdev/spark/common/bitcoin_transaction"
	"github.com/lightsparkdev/spark/common/keys"
	pbfrost "github.com/lightsparkdev/spark/proto/frost"
	pb "github.com/lightsparkdev/spark/proto/spark"
)

// RenewNodeZeroTimelock drives the zero-timelock renewal protocol (Path 3).
// Used when node_tx timelock == 0 and refund_tx timelock <= 300.
func RenewNodeZeroTimelock(
	ctx context.Context,
	config *TestWalletConfig,
	leaf *pb.TreeNode,
	signingPrivKey keys.Private,
) (*pb.TreeNode, error) {
	signingPubKey := signingPrivKey.Public()

	leafNodeTx, err := common.TxFromRawTxBytes(leaf.NodeTx)
	if err != nil {
		return nil, fmt.Errorf("failed to parse leaf node tx: %w", err)
	}
	leafAmount := leafNodeTx.TxOut[0].Value
	leafNodeOutPoint := &wire.OutPoint{Hash: leafNodeTx.TxHash(), Index: 0}

	verifyingKey, err := keys.ParsePublicKey(leaf.VerifyingPublicKey)
	if err != nil {
		return nil, fmt.Errorf("failed to parse verifying key: %w", err)
	}
	nodePkScript, err := common.P2TRScriptFromPubKey(verifyingKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create node pkscript: %w", err)
	}

	ownerSigningPubKey, err := keys.ParsePublicKey(leaf.OwnerSigningPublicKey)
	if err != nil {
		return nil, fmt.Errorf("failed to parse owner signing pubkey: %w", err)
	}
	refundPkScript, err := common.P2TRScriptFromPubKey(ownerSigningPubKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create refund pkscript: %w", err)
	}

	upperBits := leafNodeTx.TxIn[0].Sequence & 0xFFFF0000

	// 1. New node tx (zero timelock, spending current leaf node tx)
	newNodeTx := wire.NewMsgTx(3)
	newNodeTx.AddTxIn(&wire.TxIn{PreviousOutPoint: *leafNodeOutPoint, Sequence: upperBits | spark.ZeroTimelock})
	newNodeTx.AddTxOut(wire.NewTxOut(leafAmount, nodePkScript))
	newNodeTx.AddTxOut(common.EphemeralAnchorOutput())

	// 2. New refund tx (initial timelock, spending new node tx)
	refundTx := wire.NewMsgTx(3)
	refundTx.AddTxIn(&wire.TxIn{PreviousOutPoint: wire.OutPoint{Hash: newNodeTx.TxHash(), Index: 0}, Sequence: upperBits | spark.InitialTimeLock})
	refundTx.AddTxOut(&wire.TxOut{Value: leafAmount, PkScript: refundPkScript})
	refundTx.AddTxOut(common.EphemeralAnchorOutput())

	// 3. Direct node tx (DirectTimelockOffset, spending current leaf node tx)
	directNodeTx := wire.NewMsgTx(3)
	directNodeTx.AddTxIn(&wire.TxIn{PreviousOutPoint: *leafNodeOutPoint, Sequence: upperBits | spark.DirectTimelockOffset})
	directNodeTx.AddTxOut(&wire.TxOut{Value: common.MaybeApplyFee(leafAmount), PkScript: nodePkScript})

	// 4. Direct from CPFP refund tx (InitialTimeLock + DirectTimelockOffset, spending new node tx)
	directFromCpfpRefundTx := wire.NewMsgTx(3)
	directFromCpfpRefundTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: newNodeTx.TxHash(), Index: 0},
		Sequence:         upperBits | (spark.InitialTimeLock + spark.DirectTimelockOffset),
	})
	directFromCpfpRefundTx.AddTxOut(&wire.TxOut{Value: common.MaybeApplyFee(leafAmount), PkScript: refundPkScript})

	leafNodeTxOut := leafNodeTx.TxOut[0]
	newNodeTxOut := newNodeTx.TxOut[0]

	nodePrepared, err := prepareTxSigningArtifacts(newNodeTx, leafNodeTxOut, signingPubKey)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare node tx artifacts: %w", err)
	}
	refundPrepared, err := prepareTxSigningArtifacts(refundTx, newNodeTxOut, signingPubKey)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare refund tx artifacts: %w", err)
	}
	directNodePrepared, err := prepareTxSigningArtifacts(directNodeTx, leafNodeTxOut, signingPubKey)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare direct node tx artifacts: %w", err)
	}
	directFromCpfpRefundPrepared, err := prepareTxSigningArtifacts(directFromCpfpRefundTx, newNodeTxOut, signingPubKey)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare direct from cpfp refund tx artifacts: %w", err)
	}

	allPrepared := []*preparedTxSigningArtifacts{nodePrepared, refundPrepared, directNodePrepared, directFromCpfpRefundPrepared}

	userSignedJobs, err := signRenewTransactions(ctx, config, signingPrivKey, verifyingKey, allPrepared)
	if err != nil {
		return nil, err
	}

	sparkConn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		return nil, err
	}
	defer sparkConn.Close()
	sparkClient := pb.NewSparkServiceClient(sparkConn)

	resp, err := sparkClient.RenewLeaf(ctx, &pb.RenewLeafRequest{
		LeafId: leaf.Id,
		SigningJobs: &pb.RenewLeafRequest_RenewNodeZeroTimelockSigningJob{
			RenewNodeZeroTimelockSigningJob: &pb.RenewNodeZeroTimelockSigningJob{
				NodeTxSigningJob:                 userSignedJobs[0],
				RefundTxSigningJob:               userSignedJobs[1],
				DirectNodeTxSigningJob:           userSignedJobs[2],
				DirectFromCpfpRefundTxSigningJob: userSignedJobs[3],
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("renew leaf RPC failed: %w", err)
	}

	result := resp.GetRenewNodeZeroTimelockResult()
	if result == nil || result.Node == nil {
		return nil, fmt.Errorf("unexpected renew result type")
	}
	return result.Node, nil
}

// RenewNodeTimelock drives the full node+refund renewal protocol (Path 1).
// Used when both node_tx timelock <= 300 AND refund_tx timelock <= 300.
func RenewNodeTimelock(
	ctx context.Context,
	config *TestWalletConfig,
	leaf *pb.TreeNode,
	parentLeaf *pb.TreeNode,
	signingPrivKey keys.Private,
) (*pb.TreeNode, error) {
	signingPubKey := signingPrivKey.Public()

	parentTx, err := common.TxFromRawTxBytes(parentLeaf.NodeTx)
	if err != nil {
		return nil, fmt.Errorf("failed to parse parent node tx: %w", err)
	}
	parentAmount := parentTx.TxOut[0].Value
	parentOutPoint := &wire.OutPoint{Hash: parentTx.TxHash(), Index: 0}

	verifyingKey, err := keys.ParsePublicKey(leaf.VerifyingPublicKey)
	if err != nil {
		return nil, fmt.Errorf("failed to parse verifying key: %w", err)
	}
	nodePkScript, err := common.P2TRScriptFromPubKey(verifyingKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create node pkscript: %w", err)
	}

	ownerSigningPubKey, err := keys.ParsePublicKey(leaf.OwnerSigningPublicKey)
	if err != nil {
		return nil, fmt.Errorf("failed to parse owner signing pubkey: %w", err)
	}
	refundPkScript, err := common.P2TRScriptFromPubKey(ownerSigningPubKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create refund pkscript: %w", err)
	}

	upperBits := parentTx.TxIn[0].Sequence & 0xFFFF0000

	// 1. Split node tx (zero timelock, spending parent)
	splitNodeTx := wire.NewMsgTx(3)
	splitNodeTx.AddTxIn(&wire.TxIn{PreviousOutPoint: *parentOutPoint, Sequence: upperBits | spark.ZeroTimelock})
	splitNodeTx.AddTxOut(wire.NewTxOut(parentAmount, nodePkScript))
	splitNodeTx.AddTxOut(common.EphemeralAnchorOutput())

	// 2. Extended node tx (initial timelock, spending split node)
	extendedNodeTx := wire.NewMsgTx(3)
	extendedNodeTx.AddTxIn(&wire.TxIn{PreviousOutPoint: wire.OutPoint{Hash: splitNodeTx.TxHash(), Index: 0}, Sequence: upperBits | spark.InitialTimeLock})
	extendedNodeTx.AddTxOut(wire.NewTxOut(parentAmount, nodePkScript))
	extendedNodeTx.AddTxOut(common.EphemeralAnchorOutput())

	// 3. Refund tx (initial timelock, spending extended node)
	refundTx := wire.NewMsgTx(3)
	refundTx.AddTxIn(&wire.TxIn{PreviousOutPoint: wire.OutPoint{Hash: extendedNodeTx.TxHash(), Index: 0}, Sequence: upperBits | spark.InitialTimeLock})
	refundTx.AddTxOut(&wire.TxOut{Value: parentAmount, PkScript: refundPkScript})
	refundTx.AddTxOut(common.EphemeralAnchorOutput())

	// 4. Direct split node tx (DirectTimelockOffset, spending parent)
	directSplitNodeTx := wire.NewMsgTx(3)
	directSplitNodeTx.AddTxIn(&wire.TxIn{PreviousOutPoint: *parentOutPoint, Sequence: upperBits | spark.DirectTimelockOffset})
	directSplitNodeTx.AddTxOut(&wire.TxOut{Value: common.MaybeApplyFee(parentAmount), PkScript: nodePkScript})

	// 5. Direct node tx (InitialTimeLock + DirectTimelockOffset, spending split node)
	directNodeTx := wire.NewMsgTx(3)
	directNodeTx.AddTxIn(&wire.TxIn{PreviousOutPoint: wire.OutPoint{Hash: splitNodeTx.TxHash(), Index: 0}, Sequence: upperBits | (spark.InitialTimeLock + spark.DirectTimelockOffset)})
	directNodeTx.AddTxOut(&wire.TxOut{Value: common.MaybeApplyFee(parentAmount), PkScript: nodePkScript})

	// 6. Direct refund tx (InitialTimeLock + DirectTimelockOffset, spending direct node)
	directRefundTx := wire.NewMsgTx(3)
	directRefundTx.AddTxIn(&wire.TxIn{PreviousOutPoint: wire.OutPoint{Hash: directNodeTx.TxHash(), Index: 0}, Sequence: upperBits | (spark.InitialTimeLock + spark.DirectTimelockOffset)})
	directRefundTx.AddTxOut(&wire.TxOut{Value: common.MaybeApplyFee(common.MaybeApplyFee(parentAmount)), PkScript: refundPkScript})

	// 7. Direct from CPFP refund tx (InitialTimeLock + DirectTimelockOffset, spending extended node)
	directFromCpfpRefundTx := wire.NewMsgTx(3)
	directFromCpfpRefundTx.AddTxIn(&wire.TxIn{PreviousOutPoint: wire.OutPoint{Hash: extendedNodeTx.TxHash(), Index: 0}, Sequence: upperBits | (spark.InitialTimeLock + spark.DirectTimelockOffset)})
	directFromCpfpRefundTx.AddTxOut(&wire.TxOut{Value: common.MaybeApplyFee(parentAmount), PkScript: refundPkScript})

	parentTxOut := parentTx.TxOut[0]
	splitNodeTxOut := splitNodeTx.TxOut[0]
	extendedNodeTxOut := extendedNodeTx.TxOut[0]
	directNodeTxOut := directNodeTx.TxOut[0]

	splitNodePrepared, err := prepareTxSigningArtifacts(splitNodeTx, parentTxOut, signingPubKey)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare split node tx: %w", err)
	}
	directSplitNodePrepared, err := prepareTxSigningArtifacts(directSplitNodeTx, parentTxOut, signingPubKey)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare direct split node tx: %w", err)
	}
	extendedNodePrepared, err := prepareTxSigningArtifacts(extendedNodeTx, splitNodeTxOut, signingPubKey)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare extended node tx: %w", err)
	}
	refundPrepared, err := prepareTxSigningArtifacts(refundTx, extendedNodeTxOut, signingPubKey)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare refund tx: %w", err)
	}
	directNodePrepared, err := prepareTxSigningArtifacts(directNodeTx, splitNodeTxOut, signingPubKey)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare direct node tx: %w", err)
	}
	directRefundPrepared, err := prepareTxSigningArtifacts(directRefundTx, directNodeTxOut, signingPubKey)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare direct refund tx: %w", err)
	}
	directFromCpfpRefundPrepared, err := prepareTxSigningArtifacts(directFromCpfpRefundTx, extendedNodeTxOut, signingPubKey)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare direct from cpfp refund tx: %w", err)
	}

	allPrepared := []*preparedTxSigningArtifacts{
		splitNodePrepared, directSplitNodePrepared, extendedNodePrepared,
		refundPrepared, directNodePrepared, directRefundPrepared, directFromCpfpRefundPrepared,
	}

	userSignedJobs, err := signRenewTransactions(ctx, config, signingPrivKey, verifyingKey, allPrepared)
	if err != nil {
		return nil, err
	}

	sparkConn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		return nil, err
	}
	defer sparkConn.Close()
	sparkClient := pb.NewSparkServiceClient(sparkConn)

	resp, err := sparkClient.RenewLeaf(ctx, &pb.RenewLeafRequest{
		LeafId: leaf.Id,
		SigningJobs: &pb.RenewLeafRequest_RenewNodeTimelockSigningJob{
			RenewNodeTimelockSigningJob: &pb.RenewNodeTimelockSigningJob{
				SplitNodeTxSigningJob:            userSignedJobs[0],
				SplitNodeDirectTxSigningJob:      userSignedJobs[1],
				NodeTxSigningJob:                 userSignedJobs[2],
				RefundTxSigningJob:               userSignedJobs[3],
				DirectNodeTxSigningJob:           userSignedJobs[4],
				DirectRefundTxSigningJob:         userSignedJobs[5],
				DirectFromCpfpRefundTxSigningJob: userSignedJobs[6],
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("renew leaf RPC failed: %w", err)
	}

	result := resp.GetRenewNodeTimelockResult()
	if result == nil || result.Node == nil {
		return nil, fmt.Errorf("unexpected renew result type")
	}
	return result.Node, nil
}

// RenewRefundTimelock drives the refund-only renewal protocol (Path 2).
// Used when refund_tx timelock <= 300 and node_tx timelock > 300.
func RenewRefundTimelock(
	ctx context.Context,
	config *TestWalletConfig,
	leaf *pb.TreeNode,
	parentLeaf *pb.TreeNode,
	signingPrivKey keys.Private,
) (*pb.TreeNode, error) {
	signingPubKey := signingPrivKey.Public()

	parentTx, err := common.TxFromRawTxBytes(parentLeaf.NodeTx)
	if err != nil {
		return nil, fmt.Errorf("failed to parse parent node tx: %w", err)
	}
	parentAmount := parentTx.TxOut[0].Value
	parentOutPoint := &wire.OutPoint{Hash: parentTx.TxHash(), Index: 0}

	leafNodeTx, err := common.TxFromRawTxBytes(leaf.NodeTx)
	if err != nil {
		return nil, fmt.Errorf("failed to parse leaf node tx: %w", err)
	}
	currentNodeSequence := leafNodeTx.TxIn[0].Sequence

	newNodeSequence, newDirectNodeSequence, err := bitcointransaction.NextSequence(currentNodeSequence)
	if err != nil {
		return nil, fmt.Errorf("failed to compute next sequence: %w", err)
	}

	verifyingKey, err := keys.ParsePublicKey(leaf.VerifyingPublicKey)
	if err != nil {
		return nil, fmt.Errorf("failed to parse verifying key: %w", err)
	}
	nodePkScript, err := common.P2TRScriptFromPubKey(verifyingKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create node pkscript: %w", err)
	}

	ownerSigningPubKey, err := keys.ParsePublicKey(leaf.OwnerSigningPublicKey)
	if err != nil {
		return nil, fmt.Errorf("failed to parse owner signing pubkey: %w", err)
	}
	refundPkScript, err := common.P2TRScriptFromPubKey(ownerSigningPubKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create refund pkscript: %w", err)
	}

	upperBits := currentNodeSequence & 0xFFFF0000

	// 1. Node tx (decremented timelock, spending parent)
	nodeTx := wire.NewMsgTx(3)
	nodeTx.AddTxIn(&wire.TxIn{PreviousOutPoint: *parentOutPoint, Sequence: newNodeSequence})
	nodeTx.AddTxOut(&wire.TxOut{Value: parentAmount, PkScript: nodePkScript})
	nodeTx.AddTxOut(common.EphemeralAnchorOutput())

	// 2. Refund tx (initial timelock, spending new node tx)
	refundTx := wire.NewMsgTx(3)
	refundTx.AddTxIn(&wire.TxIn{PreviousOutPoint: wire.OutPoint{Hash: nodeTx.TxHash(), Index: 0}, Sequence: upperBits | spark.InitialTimeLock})
	refundTx.AddTxOut(&wire.TxOut{Value: parentAmount, PkScript: refundPkScript})
	refundTx.AddTxOut(common.EphemeralAnchorOutput())

	// 3. Direct node tx (decremented + DirectTimelockOffset, spending parent)
	directNodeTx := wire.NewMsgTx(3)
	directNodeTx.AddTxIn(&wire.TxIn{PreviousOutPoint: *parentOutPoint, Sequence: newDirectNodeSequence})
	directNodeTx.AddTxOut(&wire.TxOut{Value: common.MaybeApplyFee(parentAmount), PkScript: nodePkScript})

	// 4. Direct refund tx (InitialTimeLock + DirectTimelockOffset, spending direct node)
	directRefundTx := wire.NewMsgTx(3)
	directRefundTx.AddTxIn(&wire.TxIn{PreviousOutPoint: wire.OutPoint{Hash: directNodeTx.TxHash(), Index: 0}, Sequence: upperBits | (spark.InitialTimeLock + spark.DirectTimelockOffset)})
	directRefundTx.AddTxOut(&wire.TxOut{Value: common.MaybeApplyFee(directNodeTx.TxOut[0].Value), PkScript: refundPkScript})

	// 5. Direct from CPFP refund tx (InitialTimeLock + DirectTimelockOffset, spending node tx)
	directFromCpfpRefundTx := wire.NewMsgTx(3)
	directFromCpfpRefundTx.AddTxIn(&wire.TxIn{PreviousOutPoint: wire.OutPoint{Hash: nodeTx.TxHash(), Index: 0}, Sequence: upperBits | (spark.InitialTimeLock + spark.DirectTimelockOffset)})
	directFromCpfpRefundTx.AddTxOut(&wire.TxOut{Value: common.MaybeApplyFee(parentAmount), PkScript: refundPkScript})

	parentTxOut := parentTx.TxOut[0]
	nodeTxOut := nodeTx.TxOut[0]
	directNodeTxOut := directNodeTx.TxOut[0]

	nodePrepared, err := prepareTxSigningArtifacts(nodeTx, parentTxOut, signingPubKey)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare node tx: %w", err)
	}
	refundPrepared, err := prepareTxSigningArtifacts(refundTx, nodeTxOut, signingPubKey)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare refund tx: %w", err)
	}
	directNodePrepared, err := prepareTxSigningArtifacts(directNodeTx, parentTxOut, signingPubKey)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare direct node tx: %w", err)
	}
	directRefundPrepared, err := prepareTxSigningArtifacts(directRefundTx, directNodeTxOut, signingPubKey)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare direct refund tx: %w", err)
	}
	directFromCpfpRefundPrepared, err := prepareTxSigningArtifacts(directFromCpfpRefundTx, nodeTxOut, signingPubKey)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare direct from cpfp refund tx: %w", err)
	}

	allPrepared := []*preparedTxSigningArtifacts{
		nodePrepared, refundPrepared, directNodePrepared, directRefundPrepared, directFromCpfpRefundPrepared,
	}

	userSignedJobs, err := signRenewTransactions(ctx, config, signingPrivKey, verifyingKey, allPrepared)
	if err != nil {
		return nil, err
	}

	sparkConn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		return nil, err
	}
	defer sparkConn.Close()
	sparkClient := pb.NewSparkServiceClient(sparkConn)

	resp, err := sparkClient.RenewLeaf(ctx, &pb.RenewLeafRequest{
		LeafId: leaf.Id,
		SigningJobs: &pb.RenewLeafRequest_RenewRefundTimelockSigningJob{
			RenewRefundTimelockSigningJob: &pb.RenewRefundTimelockSigningJob{
				NodeTxSigningJob:                 userSignedJobs[0],
				RefundTxSigningJob:               userSignedJobs[1],
				DirectNodeTxSigningJob:           userSignedJobs[2],
				DirectRefundTxSigningJob:         userSignedJobs[3],
				DirectFromCpfpRefundTxSigningJob: userSignedJobs[4],
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("renew leaf RPC failed: %w", err)
	}

	result := resp.GetRenewRefundTimelockResult()
	if result == nil || result.Node == nil {
		return nil, fmt.Errorf("unexpected renew result type")
	}
	return result.Node, nil
}

// signRenewTransactions handles the common FROST signing flow for all renewal paths.
func signRenewTransactions(
	ctx context.Context,
	config *TestWalletConfig,
	signingPrivKey keys.Private,
	verifyingKey keys.Public,
	prepared []*preparedTxSigningArtifacts,
) ([]*pb.UserSignedTxSigningJob, error) {
	sparkConn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to coordinator: %w", err)
	}
	defer sparkConn.Close()
	sparkClient := pb.NewSparkServiceClient(sparkConn)

	commitmentsResp, err := sparkClient.GetSigningCommitments(ctx, &pb.GetSigningCommitmentsRequest{
		Count:       uint32(len(prepared)),
		NodeIdCount: 1,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get signing commitments: %w", err)
	}
	if len(commitmentsResp.SigningCommitments) != len(prepared) {
		return nil, fmt.Errorf("got %d commitments, expected %d", len(commitmentsResp.SigningCommitments), len(prepared))
	}

	userKeyPackage := CreateUserKeyPackage(signingPrivKey)
	frostJobs := make([]*pbfrost.FrostSigningJob, len(prepared))
	jobIDs := make([]string, len(prepared))

	for i, p := range prepared {
		jobID := uuid.NewString()
		jobIDs[i] = jobID
		frostJobs[i] = &pbfrost.FrostSigningJob{
			JobId:           jobID,
			Message:         p.sighash.Serialize(),
			KeyPackage:      userKeyPackage,
			VerifyingKey:    verifyingKey.Serialize(),
			Nonce:           p.nonce,
			UserCommitments: p.commitment,
			Commitments:     commitmentsResp.SigningCommitments[i].SigningNonceCommitments,
		}
	}

	frostConn, err := config.NewFrostGRPCConnection()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to frost: %w", err)
	}
	defer frostConn.Close()
	frostClient := pbfrost.NewFrostServiceClient(frostConn)

	signingResp, err := frostClient.SignFrost(ctx, &pbfrost.SignFrostRequest{
		SigningJobs: frostJobs,
		Role:        pbfrost.SigningRole_USER,
	})
	if err != nil {
		return nil, fmt.Errorf("FROST signing failed: %w", err)
	}

	signingPubKey := signingPrivKey.Public()
	result := make([]*pb.UserSignedTxSigningJob, len(prepared))
	for i, p := range prepared {
		sig, ok := signingResp.Results[jobIDs[i]]
		if !ok || sig == nil {
			returnedResults := slices.Collect(maps.Keys(signingResp.Results))
			return nil, fmt.Errorf("signature for job %s not returned (returned: %s)", jobIDs[i], strings.Join(returnedResults, ","))
		}
		result[i] = &pb.UserSignedTxSigningJob{
			SigningPublicKey:       signingPubKey.Serialize(),
			RawTx:                  p.rawTx,
			SigningNonceCommitment: p.commitment,
			UserSignature:          sig.SignatureShare,
			SigningCommitments: &pb.SigningCommitments{
				SigningCommitments: commitmentsResp.SigningCommitments[i].SigningNonceCommitments,
			},
		}
	}

	return result, nil
}
