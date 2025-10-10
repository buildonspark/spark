package handler

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"slices"

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

type cachedRoot struct {
	index int
	value *ent.TreeNode
}

// NewTreeExitHandler creates a new TreeExitHandler.
func NewTreeExitHandler(config *so.Config) *TreeExitHandler {
	return &TreeExitHandler{config: config}
}

func (h *TreeExitHandler) ExitSingleNodeTrees(ctx context.Context, req *pb.ExitSingleNodeTreesRequest) (*pb.ExitSingleNodeTreesResponse, error) {
	reqOwnerIdentityPubKey, err := keys.ParsePublicKey(req.OwnerIdentityPublicKey)
	if err != nil {
		return nil, fmt.Errorf("invalid identity public key: %w", err)
	}
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, reqOwnerIdentityPubKey); err != nil {
		return nil, err
	}

	treeUUIDs := make([]uuid.UUID, len(req.ExitingTrees))
	exitingTreeMap := make(map[uuid.UUID]*pb.ExitingTree, len(req.ExitingTrees))
	for i, exitingTree := range req.ExitingTrees {
		treeUUID, err := uuid.Parse(exitingTree.TreeId)
		if err != nil {
			return nil, fmt.Errorf("unable to parse tree_id %s: %w", exitingTree.TreeId, err)
		}

		treeUUIDs[i] = treeUUID
		exitingTreeMap[treeUUID] = exitingTree
	}

	trees, err := h.validateNodeTrees(ctx, treeUUIDs, reqOwnerIdentityPubKey)
	if err != nil {
		return nil, fmt.Errorf("invalid node tree: %w", err)
	}

	// Perform ascending sorting to guarantee similar order of trees
	slices.SortFunc(trees, func(a, b *ent.Tree) int {
		return bytes.Compare(a.ID[:], b.ID[:])
	})
	exitingTrees := sortExitingTrees(exitingTreeMap)

	var network *st.Network
	for _, tree := range trees {
		if network == nil {
			network = &tree.Network
		} else if *network != tree.Network {
			return nil, fmt.Errorf("all trees must be on the same network")
		}
	}

	signingResults, err := h.signExitTransaction(ctx, exitingTrees, req.RawTx, req.PreviousOutputs, trees)
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

func (h *TreeExitHandler) validateNodeTrees(ctx context.Context, treeUUIDs []uuid.UUID, ownerIdentityPubKey keys.Public) ([]*ent.Tree, error) {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}
	trees, err := db.Tree.
		Query().
		Where(enttree.IDIn(treeUUIDs...)).
		ForUpdate().
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get trees %s: %w", treeUUIDs, err)
	}

	treeMap := make(map[string]struct{}, len(trees))
	for _, tree := range trees {
		_, ok := treeMap[tree.ID.String()]

		if !ok {
			treeMap[tree.ID.String()] = struct{}{}
		} else {
			return nil, fmt.Errorf("tree with id: %s, already exists", tree.ID.String())
		}

		if tree.Status != st.TreeStatusAvailable && tree.Status != st.TreeStatusExited {
			return nil, fmt.Errorf("tree %s is in a status not eligible to exit", tree.ID.String())
		}

		leaves, err := db.TreeNode.
			Query().
			Where(
				enttreenode.HasTreeWith(enttree.ID(tree.ID)),
				enttreenode.Not(enttreenode.HasChildren()),
			).
			ForUpdate().
			All(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to get leaves of tree %s: %w", tree.ID, err)
		}

		if len(leaves) != 1 {
			return nil, fmt.Errorf("tree %s is not a single node tree", tree.ID)
		}
		if !leaves[0].OwnerIdentityPubkey.Equals(ownerIdentityPubKey) {
			return nil, fmt.Errorf("not the owner of the tree %s", tree.ID)
		}
		if leaves[0].Status != st.TreeNodeStatusAvailable && leaves[0].Status != st.TreeNodeStatusExited {
			return nil, fmt.Errorf("tree %s is not eligible for exit because leaf %s is in status %s", tree.ID, leaves[0].ID, leaves[0].Status)
		}
	}

	return trees, nil
}

func sortExitingTrees(treeMap map[uuid.UUID]*pb.ExitingTree) []*pb.ExitingTree {
	treeKeys := slices.SortedFunc(maps.Keys(treeMap), func(a, b uuid.UUID) int {
		return bytes.Compare(a[:], b[:])
	})

	sorted := make([]*pb.ExitingTree, len(treeMap))
	for i, key := range treeKeys {
		sorted[i] = treeMap[key]
	}

	return sorted
}
