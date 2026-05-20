package handler

import (
	"context"
	"fmt"

	"github.com/lightsparkdev/spark/so/frost"

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
)

// treeExitHandler is an internal helper for LS/SSP-owned tree exit flows.
type treeExitHandler struct {
	config *so.Config
}

type cachedRoot struct {
	index int
	value *ent.TreeNode
}

// newTreeExitHandler creates a new internal tree-exit helper.
func newTreeExitHandler(config *so.Config) *treeExitHandler {
	return &treeExitHandler{config: config}
}

// lockedNodeStatuses are node statuses that indicate an active operation is in
// progress. Overwriting these with Exited would corrupt the in-flight operation.
var lockedNodeStatuses = []st.TreeNodeStatus{
	st.TreeNodeStatusTransferLocked,
	st.TreeNodeStatusRenewLocked,
}

func (h *treeExitHandler) markTreesExited(ctx context.Context, trees []*ent.Tree) error {
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

	// Check for nodes in active/locked statuses before proceeding.
	// If any node is locked (e.g. TransferLocked), overwriting its status
	// to Exited would corrupt the in-flight transfer (TOCTOU race).
	lockedNodes, err := db.TreeNode.
		Query().
		Where(
			enttreenode.HasTreeWith(enttree.IDIn(treeIDs...)),
			enttreenode.StatusIn(lockedNodeStatuses...),
		).
		Select(enttreenode.FieldID).
		All(ctx)
	if err != nil {
		return fmt.Errorf("failed to check for locked tree nodes: %w", err)
	}
	if len(lockedNodes) > 0 {
		return fmt.Errorf("cannot mark trees as exited: %d node(s) are in a locked status: %v", len(lockedNodes), lockedNodes)
	}

	if _, err := db.Tree.
		Update().
		Where(enttree.IDIn(treeIDs...)).
		SetStatus(st.TreeStatusExited).
		Save(ctx); err != nil {
		return fmt.Errorf("failed to update tree statuses: %w", err)
	}

	// Only update nodes that are not already in a terminal or locked status.
	// This is a defense-in-depth measure: the check above should have caught
	// locked nodes, but we add the WHERE clause to prevent races between the
	// check and the update.
	if _, err := db.TreeNode.
		Update().
		Where(
			enttreenode.HasTreeWith(enttree.IDIn(treeIDs...)),
			enttreenode.StatusNotIn(lockedNodeStatuses...),
		).
		SetStatus(st.TreeNodeStatusExited).
		Save(ctx); err != nil {
		return fmt.Errorf("failed to update tree node statuses: %w", err)
	}

	return nil
}

func (h *treeExitHandler) gossipTreesExited(ctx context.Context, trees []*ent.Tree) error {
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

func (h *treeExitHandler) signExitTransaction(ctx context.Context, exitingTrees []*pb.ExitingTree, rawExitTx []byte, previousOutputs []*pb.BitcoinTransactionOutput, trees []*ent.Tree) ([]*pb.ExitSingleNodeTreeSigningResult, error) {
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
		tree, err := validateExitTreeStillSignable(ctx, tree)
		if err != nil {
			return nil, err
		}
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

		userNonceCommitment := frost.SigningCommitment{}
		if err := userNonceCommitment.UnmarshalProto(exitingTree.GetUserSigningCommitment()); err != nil {
			return nil, fmt.Errorf("unable to unmarshal user nonce commitment: %w", err)
		}

		signingKeyshare, err := root.QuerySigningKeyshare().Only(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get signing keyshare id: %w", err)
		}

		signingJobs = append(
			signingJobs,
			&helper.SigningJob{
				JobID:             uuid.New(),
				SigningKeyshareID: signingKeyshare.ID,
				Message:           txSigHash,
				VerifyingKey:      &root.VerifyingPubkey,
				UserCommitment:    &userNonceCommitment,
			},
		)
	}

	signingResults, err := helper.SignFrost(ctx, h.config, signingJobs)
	if err != nil {
		return nil, fmt.Errorf("failed to sign spend tx: %w", err)
	}
	jobIDToSigningResult := make(map[uuid.UUID]*helper.SigningResult)
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

func validateExitTreeStillSignable(ctx context.Context, tree *ent.Tree) (*ent.Tree, error) {
	if tree == nil {
		return nil, fmt.Errorf("tree is required")
	}
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}
	latest, err := db.Tree.
		Query().
		Where(enttree.ID(tree.ID)).
		ForUpdate().
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to reload tree %s before exit signing: %w", tree.ID.String(), err)
	}
	if latest.Status != st.TreeStatusAvailable {
		return nil, fmt.Errorf("tree %s is in status %s and is not eligible for exit signing", tree.ID.String(), latest.Status)
	}
	return latest, nil
}
