package handler

import (
	"bytes"
	"context"
	"fmt"

	"github.com/lightsparkdev/spark/common/keys"

	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authz"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
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
	reqOwnerIDPubKey, err := keys.ParsePublicKey(req.OwnerIdentityPublicKey)
	if err != nil {
		return nil, fmt.Errorf("invalid identity public key: %w", err)
	}
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, reqOwnerIDPubKey); err != nil {
		return nil, err
	}

	var trees []*ent.Tree
	var network *st.Network
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

	signingResults, err := h.signExitTransaction(ctx, req.ExitingTrees, req.RawTx, req.PreviousOutputs, trees)
	if err != nil {
		return nil, err
	}

	if err := h.gossipTreesExited(ctx, trees); err != nil {
		return nil, fmt.Errorf("failed to gossip trees exited: %w", err)
	}

	if err := h.MarkTreesExited(ctx, trees); err != nil {
		return nil, fmt.Errorf("failed to mark trees as exited: %w", err)
	}

	return &pb.ExitSingleNodeTreesResponse{SigningResults: signingResults}, nil
}

func (h *TreeExitHandler) MarkTreesExited(ctx context.Context, trees []*ent.Tree) error {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	for _, tree := range trees {
		if tree.Status != st.TreeStatusExited {
			tree, err := tree.Update().SetStatus(st.TreeStatusExited).Save(ctx)
			if err != nil {
				return fmt.Errorf("failed to update tree %s status: %w", tree.ID.String(), err)
			}
			err = db.TreeNode.
				Update().
				Where(enttreenode.HasTreeWith(enttree.ID(tree.ID))).
				SetStatus(st.TreeNodeStatusExited).
				Exec(ctx)
			if err != nil {
				return fmt.Errorf("failed to update tree nodes status on tree %s: %w", tree.ID.String(), err)
			}
		}
	}
	return nil
}

func (h *TreeExitHandler) gossipTreesExited(ctx context.Context, trees []*ent.Tree) error {
	treeIDs := make([]string, 0)
	for _, tree := range trees {
		treeIDs = append(treeIDs, tree.ID.String())
	}

	selection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}
	operatorList, err := selection.OperatorList(h.config)
	if err != nil {
		return fmt.Errorf("unable to get operator list: %w", err)
	}
	participants := make([]string, len(operatorList))
	for i, operator := range operatorList {
		participants[i] = operator.Identifier
	}
	_, err = NewSendGossipHandler(h.config).CreateAndSendGossipMessage(ctx, &pbgossip.GossipMessage{
		Message: &pbgossip.GossipMessage_MarkTreesExited{
			MarkTreesExited: &pbgossip.GossipMessageMarkTreesExited{
				TreeIds: treeIDs,
			},
		},
	}, participants)
	if err != nil {
		return fmt.Errorf("unable to create and send gossip message: %w", err)
	}

	return nil
}

func (h *TreeExitHandler) signExitTransaction(ctx context.Context, exitingTrees []*pb.ExitingTree, rawExitTx []byte, previousOutputs []*pb.BitcoinTransactionOutput, trees []*ent.Tree) ([]*pb.ExitSingleNodeTreeSigningResult, error) {
	tx, err := common.TxFromRawTxBytes(rawExitTx)
	if err != nil {
		return nil, fmt.Errorf("unable to load tx: %w", err)
	}

	prevOuts := make(map[wire.OutPoint]*wire.TxOut)
	for index, txIn := range tx.TxIn {
		prevOuts[txIn.PreviousOutPoint] = &wire.TxOut{
			Value:    previousOutputs[index].Value,
			PkScript: previousOutputs[index].PkScript,
		}
	}

	var signingJobs []*helper.SigningJob
	for i, exitingTree := range exitingTrees {
		tree := trees[i]
		root, err := tree.GetRoot(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to get root of tree %s: %w", tree.ID.String(), err)
		}
		txSigHash, err := common.SigHashFromMultiPrevOutTx(tx, int(exitingTree.Vin), prevOuts)
		if err != nil {
			return nil, fmt.Errorf("unable to calculate sighash from tx: %w", err)
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
		rootVerifyingPubKey, err := keys.ParsePublicKey(root.VerifyingPubkey)
		if err != nil {
			return nil, fmt.Errorf("failed to parse root verifying public key: %w", err)
		}
		signingJobs = append(
			signingJobs,
			&helper.SigningJob{
				JobID:             jobID,
				SigningKeyshareID: signingKeyshare.ID,
				Message:           txSigHash,
				VerifyingKey:      &rootVerifyingPubKey,
				UserCommitment:    userNonceCommitment,
			},
		)
	}

	signingResults, err := helper.SignFrost(ctx, h.config, signingJobs)
	if err != nil {
		return nil, fmt.Errorf("failed to sign spend tx: %w", err)
	}
	jobIDToSigningResult := make(map[string]*helper.SigningResult)
	for _, signingResult := range signingResults {
		jobIDToSigningResult[signingResult.JobID] = signingResult
	}

	var pbSigningResults []*pb.ExitSingleNodeTreeSigningResult
	for i, tree := range trees {
		signingResultProto, err := jobIDToSigningResult[signingJobs[i].JobID].MarshalProto()
		if err != nil {
			return nil, err
		}
		root, err := tree.GetRoot(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to get root of tree %s: %w", tree.ID.String(), err)
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
		return nil, fmt.Errorf("unable to parse tree_id %s: %w", treeID, err)
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}
	tree, err := db.Tree.
		Query().
		Where(enttree.ID(treeUUID)).
		ForUpdate().
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get tree %s: %w", treeID, err)
	}

	if tree.Status != st.TreeStatusAvailable && tree.Status != st.TreeStatusExited {
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
		return nil, fmt.Errorf("unable to get leaves of tree %s: %w", treeID, err)
	}

	if len(leaves) != 1 {
		return nil, fmt.Errorf("tree %s is not a single node tree", treeID)
	}
	if !bytes.Equal(leaves[0].OwnerIdentityPubkey, ownerIdentityPublicKey) {
		return nil, fmt.Errorf("not the owner of the tree %s", treeID)
	}
	if leaves[0].Status != st.TreeNodeStatusAvailable && leaves[0].Status != st.TreeNodeStatusExited {
		return nil, fmt.Errorf("tree %s is not eligible for exit because leaf %s is in status %s", treeID, leaves[0].ID.String(), leaves[0].Status)
	}

	return tree, nil
}
