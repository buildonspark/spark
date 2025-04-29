package handler

import (
	"context"
	"fmt"
	"slices"

	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authz"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/schema"
	"github.com/lightsparkdev/spark/so/helper"
	"github.com/lightsparkdev/spark/so/objects"
)

// RefreshTimelockHandler is a handler for refreshing timelocks.
type RefreshTimelockHandler struct {
	config *so.Config
}

// NewRefreshTimelockHandler creates a new RefreshTimelockHandler.
func NewRefreshTimelockHandler(config *so.Config) *RefreshTimelockHandler {
	return &RefreshTimelockHandler{
		config: config,
	}
}

// RefreshTimelock refreshes the timelocks of a leaf and its ancestors.
func (h *RefreshTimelockHandler) RefreshTimelock(ctx context.Context, req *pb.RefreshTimelockRequest) (*pb.RefreshTimelockResponse, error) {
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, req.OwnerIdentityPublicKey); err != nil {
		return nil, err
	}

	leafUUID, err := uuid.Parse(req.LeafId)
	if err != nil {
		return nil, err
	}

	db := ent.GetDbFromContext(ctx)
	leaf, err := db.TreeNode.Get(ctx, leafUUID)
	if err != nil {
		return nil, err
	}

	// Start at the node and collect txs by going backwards through the signing jobs
	node := leaf
	nodes := make([]*ent.TreeNode, len(req.SigningJobs))
	currentTxs := make([]*wire.MsgTx, len(req.SigningJobs))
	signingTxs := make([]*wire.MsgTx, len(req.SigningJobs))
	for i, signingJob := range slices.Backward(req.SigningJobs) {
		var rawTxBytes []byte
		if i == len(req.SigningJobs)-1 {
			rawTxBytes = node.RawRefundTx
		} else if i == len(req.SigningJobs)-2 {
			rawTxBytes = node.RawTx
		} else {
			node, err = node.QueryParent().First(ctx)
			if err != nil {
				return nil, fmt.Errorf("unable to query parent node: %w", err)
			}
			rawTxBytes = node.RawTx
		}
		if i == len(req.SigningJobs)-1 && node.Status != schema.TreeNodeStatusAvailable {
			return nil, fmt.Errorf("cannot refresh leaf node %s because it is not available", node.ID)
		}

		currentTx, err := common.TxFromRawTxBytes(rawTxBytes)
		if err != nil {
			return nil, fmt.Errorf("unable to deserialize current tx: %w", err)
		}

		signingTx, err := common.TxFromRawTxBytes(signingJob.RawTx)
		if err != nil {
			return nil, fmt.Errorf("unable to deserialize signing job signing tx: %w", err)
		}

		nodes[i] = node
		signingTxs[i] = signingTx
		currentTxs[i] = currentTx
	}

	// Validate the signing requests
	for i := range signingTxs {
		signingTx := signingTxs[i]
		currentTx := currentTxs[i]

		// Output should just be destination + ephemeral anchor
		if len(signingTx.TxOut) > 2 {
			return nil, fmt.Errorf("unexpected number of outputs on signing tx: %d", len(signingTx.TxOut))
		}
		if len(currentTx.TxOut) > 2 {
			return nil, fmt.Errorf("unexpected number of outputs on current tx: %d", len(currentTx.TxOut))
		}
		if signingTx.TxOut[0].Value != currentTx.TxOut[0].Value {
			return nil, fmt.Errorf("expected output value to be %d, got %d", currentTx.TxOut[0].Value, signingTx.TxOut[0].Value)
		}

		signingSequence := signingTx.TxIn[0].Sequence
		currentSequence := currentTx.TxIn[0].Sequence

		if i > 0 && signingSequence != spark.InitialSequence() {
			// We should be resetting the timelocks for the last N txs
			return nil, fmt.Errorf("sequence %d should be %d", signingSequence, spark.InitialSequence())
		} else if i == 0 && signingSequence >= currentSequence {
			// We should be decrementing the timelocks for the very first tx
			return nil, fmt.Errorf("sequence %d should be less than %d", signingSequence, currentSequence)
		}
	}

	// Prepare frost signing jobs
	signingJobs := make([]*helper.SigningJob, 0, len(req.SigningJobs))
	for i, signingJob := range req.SigningJobs {
		var parentTx *wire.MsgTx
		if i == 0 && len(nodes) == 1 {
			// Only signing refund tx
			parentTx, err = common.TxFromRawTxBytes(nodes[0].RawTx)
			if err != nil {
				return nil, fmt.Errorf("unable to deserialize refund signing tx: %w", err)
			}
		} else if i == 0 {
			// Greatest ancestor tx
			parentNode, err := nodes[0].QueryParent().First(ctx)
			if err != nil {
				return nil, fmt.Errorf("unable to query parent node: %w", err)
			}
			parentTx, err = common.TxFromRawTxBytes(parentNode.RawTx)
			if err != nil {
				return nil, fmt.Errorf("unable to deserialize parent signing tx: %w", err)
			}
		} else {
			parentTx = signingTxs[i-1]
		}
		parentTxOut := parentTx.TxOut[nodes[i].Vout]

		// Validate the current tx spends the parent tx
		parentTxHash := parentTx.TxHash()
		if !signingTxs[i].TxIn[0].PreviousOutPoint.Hash.IsEqual(&parentTxHash) || signingTxs[i].TxIn[0].PreviousOutPoint.Index != uint32(nodes[i].Vout) {
			return nil, fmt.Errorf("signing tx must spend parent tx vout, expected %s:%d, got %s:%d", parentTxHash, nodes[i].Vout, signingTxs[i].TxIn[0].PreviousOutPoint.Hash, signingTxs[i].TxIn[0].PreviousOutPoint.Index)
		}

		sigHash, err := common.SigHashFromTx(signingTxs[i], 0, parentTxOut)
		if err != nil {
			return nil, fmt.Errorf("unable to calculate sighash from refund tx: %v", err)
		}
		userNonceCommitment, err := objects.NewSigningCommitment(signingJob.SigningNonceCommitment.Binding, signingJob.SigningNonceCommitment.Hiding)
		if err != nil {
			return nil, fmt.Errorf("unable to create user nonce commitment: %v", err)
		}

		signingKeyshare, err := nodes[i].QuerySigningKeyshare().Only(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get signing keyshare id: %w", err)
		}

		signingJobs = append(signingJobs, &helper.SigningJob{
			JobID:             uuid.New().String(),
			SigningKeyshareID: signingKeyshare.ID,
			Message:           sigHash,
			VerifyingKey:      nodes[i].VerifyingPubkey,
			UserCommitment:    userNonceCommitment,
		})
	}

	// Save new raw txs in the DB
	for i, signingJob := range req.SigningJobs {
		var err error
		if i == len(req.SigningJobs)-1 {
			_, err = nodes[i].Update().SetRawRefundTx(signingJob.RawTx).Save(ctx)
		} else {
			_, err = nodes[i].Update().SetRawTx(signingJob.RawTx).Save(ctx)
		}
		if err != nil {
			return nil, err
		}
	}

	// Sign the transactions with all the SOs
	signingResults, err := helper.SignFrost(ctx, h.config, signingJobs)
	if err != nil {
		return nil, err
	}

	// Prepare response
	pbSigningResults := make([]*pb.RefreshTimelockSigningResult, 0)
	for i, signingResult := range signingResults {
		signingResultProto, err := signingResult.MarshalProto()
		if err != nil {
			return nil, err
		}
		pbSigningResults = append(pbSigningResults, &pb.RefreshTimelockSigningResult{
			SigningResult: signingResultProto,
			VerifyingKey:  nodes[i].VerifyingPubkey,
		})
	}

	return &pb.RefreshTimelockResponse{
		SigningResults: pbSigningResults,
	}, nil
}
