package handler

import (
	"bytes"
	"context"
	"fmt"

	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authz"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/schema"
	enttree "github.com/lightsparkdev/spark/so/ent/tree"
	enttreenode "github.com/lightsparkdev/spark/so/ent/treenode"
	"github.com/lightsparkdev/spark/so/helper"
	"github.com/lightsparkdev/spark/so/objects"
)

// TreeExitHandler is a handler for tree exit requests.
type TreeExitHandler struct {
	config *so.Config
}

// NewTreeExitHandler creates a new TreeExitHandler.
func NewTreeExitHandler(config *so.Config) *TreeExitHandler {
	return &TreeExitHandler{config: config}
}

func (h *TreeExitHandler) ExitSingleNodeTrees(ctx context.Context, req *pb.ExitSingleNodeTreesRequest) (*pb.ExitSingleNodeTreesResponse, error) {
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, req.OwnerIdentityPublicKey); err != nil {
		return nil, err
	}

	trees := make([]*ent.Tree, 0)
	var network *schema.Network
	for _, exitingTree := range req.ExitingTrees {
		tree, err := h.validateSingleNodeTree(ctx, exitingTree.TreeId, req.OwnerIdentityPublicKey)
		if err != nil {
			return nil, err
		}
		if network == nil {
			network = &tree.Network
		} else if *network != tree.Network {
			return nil, fmt.Errorf("all trees must be on the same network")
		}
		trees = append(trees, tree)
	}

	signingResults, err := h.signExitTransaction(ctx, req, trees)
	if err != nil {
		return nil, err
	}

	if err := h.updateTrees(ctx, trees); err != nil {
		return nil, fmt.Errorf("failed to update trees: %v", err)
	}

	return &pb.ExitSingleNodeTreesResponse{
		SigningResults: signingResults,
	}, nil
}

func (h *TreeExitHandler) updateTrees(ctx context.Context, trees []*ent.Tree) error {
	db := ent.GetDbFromContext(ctx)
	for _, tree := range trees {
		if tree.Status != schema.TreeStatusExited {
			tree, err := tree.Update().SetStatus(schema.TreeStatusExited).Save(ctx)
			if err != nil {
				return fmt.Errorf("failed to update tree %s status: %v", tree.ID.String(), err)
			}
			err = db.TreeNode.
				Update().
				Where(enttreenode.HasTreeWith(enttree.ID(tree.ID))).
				SetStatus(schema.TreeNodeStatusExited).
				Exec(ctx)
			if err != nil {
				return fmt.Errorf("failed to update tree nodes status on tree %s: %v", tree.ID.String(), err)
			}
		}
	}
	return nil
}

func (h *TreeExitHandler) signExitTransaction(ctx context.Context, req *pb.ExitSingleNodeTreesRequest, trees []*ent.Tree) ([]*pb.ExitSingleNodeTreeSigningResult, error) {
	tx, err := common.TxFromRawTxBytes(req.RawTx)
	if err != nil {
		return nil, fmt.Errorf("unable to load tx: %v", err)
	}

	prevOuts := make(map[wire.OutPoint]*wire.TxOut)
	for index, txIn := range tx.TxIn {
		prevOuts[txIn.PreviousOutPoint] = &wire.TxOut{
			Value:    req.PreviousOutputs[index].Value,
			PkScript: req.PreviousOutputs[index].PkScript,
		}
	}

	signingJobs := make([]*helper.SigningJob, 0)
	for i, exitingTree := range req.ExitingTrees {
		tree := trees[i]
		root, err := tree.GetRoot(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to get root of tree %s: %v", tree.ID.String(), err)
		}
		txSigHash, err := common.SigHashFromMultiPrevOutTx(tx, int(exitingTree.Vin), prevOuts)
		if err != nil {
			return nil, fmt.Errorf("unable to calculate sighash from tx: %v", err)
		}

		userNonceCommitment, err := objects.NewSigningCommitment(
			exitingTree.UserSigningCommitment.Binding,
			exitingTree.UserSigningCommitment.Hiding,
		)
		if err != nil {
			return nil, err
		}
		jobID := uuid.New().String()
		signingKeyshare, err := root.QuerySigningKeyshare().Only(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get signing keyshare id: %w", err)
		}
		signingJobs = append(
			signingJobs,
			&helper.SigningJob{
				JobID:             jobID,
				SigningKeyshareID: signingKeyshare.ID,
				Message:           txSigHash,
				VerifyingKey:      root.VerifyingPubkey,
				UserCommitment:    userNonceCommitment,
			},
		)
	}

	signingResults, err := helper.SignFrost(ctx, h.config, signingJobs)
	if err != nil {
		return nil, fmt.Errorf("failed to sign spend tx: %v", err)
	}
	jobIDToSigningResult := make(map[string]*helper.SigningResult)
	for _, signingResult := range signingResults {
		jobIDToSigningResult[signingResult.JobID] = signingResult
	}

	pbSigningResults := make([]*pb.ExitSingleNodeTreeSigningResult, 0)
	for i, tree := range trees {
		signingResultProto, err := jobIDToSigningResult[signingJobs[i].JobID].MarshalProto()
		if err != nil {
			return nil, err
		}
		root, err := tree.GetRoot(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to get root of tree %s: %v", tree.ID.String(), err)
		}
		pbSigningResults = append(pbSigningResults, &pb.ExitSingleNodeTreeSigningResult{
			TreeId:        tree.ID.String(),
			SigningResult: signingResultProto,
			VerifyingKey:  root.VerifyingPubkey,
		})
	}
	return pbSigningResults, nil
}

func (h *TreeExitHandler) validateSingleNodeTree(ctx context.Context, treeID string, ownerIdentityPublicKey []byte) (*ent.Tree, error) {
	treeUUID, err := uuid.Parse(treeID)
	if err != nil {
		return nil, fmt.Errorf("unable to parse tree_id %s: %v", treeID, err)
	}

	db := ent.GetDbFromContext(ctx)
	tree, err := db.Tree.
		Query().
		Where(enttree.ID(treeUUID)).
		ForUpdate().
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get tree %s: %v", treeID, err)
	}

	if tree.Status != schema.TreeStatusAvailable && tree.Status != schema.TreeStatusExited {
		return nil, fmt.Errorf("tree %s is in a status not eligible to exit", treeID)
	}

	leaves, err := db.TreeNode.
		Query().
		Where(
			enttreenode.HasTreeWith(enttree.ID(treeUUID)),
			enttreenode.Not(enttreenode.HasChildren()),
		).
		ForUpdate().
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get leaves of tree %s: %v", treeID, err)
	}

	if len(leaves) != 1 {
		return nil, fmt.Errorf("tree %s is not a single node tree", treeID)
	}
	if !bytes.Equal(leaves[0].OwnerIdentityPubkey, ownerIdentityPublicKey) {
		return nil, fmt.Errorf("not the owner of the tree %s", treeID)
	}
	if leaves[0].Status != schema.TreeNodeStatusAvailable && leaves[0].Status != schema.TreeNodeStatusExited {
		return nil, fmt.Errorf("tree %s is not eligible for exit because leaf %s is in status %s", treeID, leaves[0].ID.String(), leaves[0].Status)
	}

	return tree, nil
}
