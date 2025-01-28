package handler

import (
	"bytes"
	"context"
	"fmt"

	"github.com/google/uuid"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent"
	"github.com/lightsparkdev/spark-go/so/ent/schema"
)

// CooperativeExitHandler tracks transfers
// and on-chain txs events for cooperative exits.
type CooperativeExitHandler struct {
	config *so.Config
	// leaf to UTXO map, i.e. leaves are locked to a certain UTXO (put this in the DB somewhere)
}

// NewCooperativeExitHandler creates a new CooperativeExitHandler.
func NewCooperativeExitHandler(config *so.Config) *CooperativeExitHandler {
	return &CooperativeExitHandler{
		config: config,
	}
}

func leafAvailableToExit(leaf *ent.TreeNode, leafUUID uuid.UUID, ownerIdentityPubkey []byte) error {
	if leaf.Status != schema.TreeNodeStatusAvailable {
		return fmt.Errorf("leaf %s is not available to transfer, status is %s", leafUUID, leaf.Status)
	}
	if !bytes.Equal(leaf.OwnerIdentityPubkey, ownerIdentityPubkey) {
		return fmt.Errorf("leaf %s is not owned by the owner", leafUUID)
	}
	return nil
}

// CooperativeExit signs refund transactions for leaves, spending connector outputs.
// It will lock the transferred leaves based on seeing a txid confirming on-chain.
func (h *CooperativeExitHandler) CooperativeExit(ctx context.Context, req *pb.CooperativeExitRequest) (*pb.CooperativeExitResponse, error) {
	// TODO(alec): combine this with StartSendTransfer handler since it's so similar
	jobToLeafMap := make(map[string]*ent.TreeNode)
	for _, job := range req.SigningJobs {
		leafUUID, err := uuid.Parse(job.LeafId)
		if err != nil {
			return nil, fmt.Errorf("unable to parse leaf_id %s: %v", job.LeafId, err)
		}

		db := ent.GetDbFromContext(ctx)
		leaf, err := db.TreeNode.Get(ctx, leafUUID)
		if err != nil || leaf == nil {
			return nil, fmt.Errorf("unable to find leaf %s: %v", leafUUID, err)
		}
		err = leafAvailableToExit(leaf, leafUUID, req.OwnerIdentityPublicKey)
		if err != nil {
			return nil, err
		}
		jobToLeafMap[job.LeafId] = leaf
	}

	signingResults, err := signRefunds(ctx, h.config, req.SigningJobs, jobToLeafMap)
	if err != nil {
		return nil, fmt.Errorf("failed to sign refund transactions: %v", err)
	}

	response := &pb.CooperativeExitResponse{
		SigningResults: signingResults,
	}
	return response, nil
}
