package handler

import (
	"context"
	"fmt"

	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
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

type cachedRoot struct {
	index int
	value *ent.TreeNode
}

// NewTreeExitHandler creates a new TreeExitHandler.
func NewTreeExitHandler(config *so.Config) *TreeExitHandler {
	return &TreeExitHandler{config: config}
}

func (h *TreeExitHandler) MarkTreesExited(ctx context.Context, trees []*ent.Tree) error {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	// Collect all tree IDs that need updating
	var treeIDs []uuid.UUID
	for _, tree := range trees {
		if tree.Status != st.TreeStatusExited {
			treeIDs = append(treeIDs, tree.ID)
		}
	}
	if len(treeIDs) == 0 {
		return nil
	}

	if _, err := db.Tree.
		Update().
		Where(enttree.IDIn(treeIDs...)).
		SetStatus(st.TreeStatusExited).
		Save(ctx); err != nil {
		return fmt.Errorf("failed to update tree statuses: %w", err)
	}

	if _, err := db.TreeNode.
		Update().
		Where(enttreenode.HasTreeWith(enttree.IDIn(treeIDs...))).
		SetStatus(st.TreeNodeStatusExited).
		Save(ctx); err != nil {
		return fmt.Errorf("failed to update tree node statuses: %w", err)
	}

	return nil
}

func (h *TreeExitHandler) gossipTreesExited(ctx context.Context, trees []*ent.Tree) error {
	treeIDs := make([]string, len(trees))
	for i, tree := range trees {
		treeIDs[i] = tree.ID.String()
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
	cachedRootsMap := make(map[uuid.UUID]*cachedRoot, len(exitingTrees))
	for i, exitingTree := range exitingTrees {
		tree := trees[i]
		root, err := tree.GetRoot(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to get root of tree %s: %w", tree.ID.String(), err)
		}

		cachedRootsMap[tree.ID] = &cachedRoot{
			index: i,
			value: root,
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

		signingJobs = append(
			signingJobs,
			&helper.SigningJob{
				JobID:             jobID,
				SigningKeyshareID: signingKeyshare.ID,
				Message:           txSigHash,
				VerifyingKey:      &root.VerifyingPubkey,
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
	for id, root := range cachedRootsMap {
		signingResultProto, err := jobIDToSigningResult[signingJobs[root.index].JobID].MarshalProto()
		if err != nil {
			return nil, err
		}
		pbSigningResults = append(pbSigningResults, &pb.ExitSingleNodeTreeSigningResult{
			TreeId:        id.String(),
			SigningResult: signingResultProto,
			VerifyingKey:  root.value.VerifyingPubkey.Serialize(),
		})
	}

	return pbSigningResults, nil
}
