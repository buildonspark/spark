package handler

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/lightsparkdev/spark/common/keys"

	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authz"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/depositaddress"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/tree"
	"github.com/lightsparkdev/spark/so/ent/treenode"
	"github.com/lightsparkdev/spark/so/helper"
)

// TreeCreationHandler is a handler for tree creation requests.
type TreeCreationHandler struct {
	config *so.Config
}

// NewTreeCreationHandler creates a new TreeCreationHandler.
func NewTreeCreationHandler(config *so.Config) *TreeCreationHandler {
	return &TreeCreationHandler{config: config}
}

func (h *TreeCreationHandler) findParentOutputFromUtxo(ctx context.Context, utxo *pb.UTXO) (*wire.TxOut, error) {
	tx, err := common.TxFromRawTxBytes(utxo.RawTx)
	if err != nil {
		return nil, err
	}
	if len(tx.TxOut) <= int(utxo.Vout) {
		return nil, fmt.Errorf("vout out of bounds utxo, tx vout: %d, utxo vout: %d", len(tx.TxOut), utxo.Vout)
	}
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}
	txHash := tx.TxHash()
	query := db.Tree.Query().Where(tree.BaseTxid(txHash[:]))
	count, err := query.Count(ctx)
	if err != nil {
		return nil, err
	}
	if count > 0 {
		// The only way to detect a parent is split is to check if the subtree of that tree node already exists.
		return nil, fmt.Errorf("tree with base txid %s already exists", txHash.String())
	}
	return tx.TxOut[utxo.Vout], nil
}

func (h *TreeCreationHandler) findParentOutputFromNodeOutput(ctx context.Context, nodeOutput *pb.NodeOutput) (*wire.TxOut, error) {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}
	nodeID, err := uuid.Parse(nodeOutput.NodeId)
	if err != nil {
		return nil, err
	}
	node, err := db.TreeNode.Get(ctx, nodeID)
	if err != nil {
		return nil, err
	}

	tx, err := common.TxFromRawTxBytes(node.RawTx)
	if err != nil {
		return nil, err
	}
	if len(tx.TxOut) <= int(nodeOutput.Vout) {
		return nil, fmt.Errorf("vout out of bounds node output, tx vout: %d, node output vout: %d", len(tx.TxOut), nodeOutput.Vout)
	}

	query := db.TreeNode.Query().Where(
		treenode.HasParentWith(treenode.ID(nodeID)),
		treenode.Vout(int16(nodeOutput.Vout)),
	)
	children, err := query.Count(ctx)
	if err != nil {
		return nil, err
	}
	if children > 0 {
		// The only way to detect a child is split is to check if the subtree of that tree node already exists.
		return nil, fmt.Errorf("node %s child vout %d already exists", nodeID.String(), nodeOutput.Vout)
	}
	return tx.TxOut[nodeOutput.Vout], nil
}

func (h *TreeCreationHandler) findParentOutputFromPrepareTreeAddressRequest(ctx context.Context, req *pb.PrepareTreeAddressRequest) (*wire.TxOut, error) {
	switch req.Source.(type) {
	case *pb.PrepareTreeAddressRequest_ParentNodeOutput:
		return h.findParentOutputFromNodeOutput(ctx, req.GetParentNodeOutput())
	case *pb.PrepareTreeAddressRequest_OnChainUtxo:
		return h.findParentOutputFromUtxo(ctx, req.GetOnChainUtxo())
	default:
		return nil, errors.New("invalid source")
	}
}

func (h *TreeCreationHandler) findParentOutputFromCreateTreeRequest(ctx context.Context, req *pb.CreateTreeRequest) (*wire.TxOut, error) {
	switch req.Source.(type) {
	case *pb.CreateTreeRequest_ParentNodeOutput:
		return h.findParentOutputFromNodeOutput(ctx, req.GetParentNodeOutput())
	case *pb.CreateTreeRequest_OnChainUtxo:
		return h.findParentOutputFromUtxo(ctx, req.GetOnChainUtxo())
	default:
		return nil, errors.New("invalid source")
	}
}

func (h *TreeCreationHandler) getSigningKeyshareFromOutput(ctx context.Context, network common.Network, output *wire.TxOut) (keys.Public, *ent.SigningKeyshare, error) {
	addressString, err := common.P2TRAddressFromPkScript(output.PkScript, network)
	if err != nil {
		return keys.Public{}, nil, err
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return keys.Public{}, nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}
	depositAddress, err := db.DepositAddress.Query().Where(depositaddress.Address(*addressString)).Only(ctx)
	if err != nil {
		return keys.Public{}, nil, err
	}

	keyshare, err := depositAddress.QuerySigningKeyshare().First(ctx)
	if err != nil {
		return keys.Public{}, nil, err
	}

	return depositAddress.OwnerSigningPubkey, keyshare, nil
}

func (h *TreeCreationHandler) findParentPublicKeys(ctx context.Context, network common.Network, req *pb.PrepareTreeAddressRequest) (keys.Public, *ent.SigningKeyshare, error) {
	parentOutput, err := h.findParentOutputFromPrepareTreeAddressRequest(ctx, req)
	if err != nil {
		return keys.Public{}, nil, err
	}
	return h.getSigningKeyshareFromOutput(ctx, network, parentOutput)
}

func (h *TreeCreationHandler) validateAndCountTreeAddressNodes(ctx context.Context, parentUserPubKey keys.Public, nodes []*pb.AddressRequestNode) (int, error) {
	if len(nodes) == 0 {
		return 0, nil
	}

	count := len(nodes) - 1
	var publicKeys []keys.Public
	for _, child := range nodes {
		childPubKey, err := keys.ParsePublicKey(child.UserPublicKey)
		if err != nil {
			return 0, err
		}
		childCount, err := h.validateAndCountTreeAddressNodes(ctx, childPubKey, child.Children)
		if err != nil {
			return 0, err
		}
		count += childCount
		publicKeys = append(publicKeys, childPubKey)
	}

	sum, err := keys.SumPublicKeys(publicKeys)
	if err != nil {
		return 0, err
	}

	if !sum.Equals(parentUserPubKey) {
		return 0, errors.New("user public key does not add up to the parent public key")
	}
	return count, nil
}

func (h *TreeCreationHandler) createPrepareTreeAddressNodeFromAddressNode(ctx context.Context, node *pb.AddressRequestNode) (*pbinternal.PrepareTreeAddressNode, error) {
	if node.Children == nil {
		return &pbinternal.PrepareTreeAddressNode{UserPublicKey: node.UserPublicKey}, nil
	}
	children := make([]*pbinternal.PrepareTreeAddressNode, len(node.Children))
	var err error
	for i, child := range node.Children {
		children[i], err = h.createPrepareTreeAddressNodeFromAddressNode(ctx, child)
		if err != nil {
			return nil, err
		}
	}
	return &pbinternal.PrepareTreeAddressNode{
		UserPublicKey: node.UserPublicKey,
		Children:      children,
	}, nil
}

func (h *TreeCreationHandler) applyKeysharesToTree(ctx context.Context, targetKeyshare *ent.SigningKeyshare, node *pbinternal.PrepareTreeAddressNode, keyshares []*ent.SigningKeyshare) (*pbinternal.PrepareTreeAddressNode, map[string]*ent.SigningKeyshare, error) {
	keyshareIndex := 0

	type element struct {
		keyshare *ent.SigningKeyshare
		children []*pbinternal.PrepareTreeAddressNode
	}

	queue := []*element{{
		keyshare: targetKeyshare,
		children: []*pbinternal.PrepareTreeAddressNode{node},
	}}

	keysharesMap := make(map[string]*ent.SigningKeyshare)

	for len(queue) > 0 {
		currentElement := queue[0]
		queue = queue[1:]

		if len(currentElement.children) == 0 {
			continue
		}

		var selectedKeyshares []*ent.SigningKeyshare
		for _, child := range currentElement.children[:len(currentElement.children)-1] {
			electedKeyShare := keyshares[keyshareIndex]
			child.SigningKeyshareId = electedKeyShare.ID.String()
			keysharesMap[electedKeyShare.ID.String()] = electedKeyShare
			keyshareIndex++
			queue = append(queue, &element{
				keyshare: electedKeyShare,
				children: child.Children,
			})
			selectedKeyshares = append(selectedKeyshares, electedKeyShare)
		}

		id, err := uuid.NewV7()
		if err != nil {
			return nil, nil, err
		}
		lastKeyshare, err := ent.CalculateAndStoreLastKey(ctx, h.config, currentElement.keyshare, selectedKeyshares, id)
		if err != nil {
			return nil, nil, err
		}
		currentElement.children[len(currentElement.children)-1].SigningKeyshareId = lastKeyshare.ID.String()
		keysharesMap[lastKeyshare.ID.String()] = lastKeyshare
		queue = append(queue, &element{
			keyshare: lastKeyshare,
			children: currentElement.children[len(currentElement.children)-1].Children,
		})
	}

	return node, keysharesMap, nil
}

func (h *TreeCreationHandler) createAddressNodeFromPrepareTreeAddressNode(
	ctx context.Context,
	network common.Network,
	node *pbinternal.PrepareTreeAddressNode,
	keysharesMap map[string]*ent.SigningKeyshare,
	userIdentityPubKey keys.Public,
	save bool,
) (addressNode *pb.AddressNode, err error) {
	signingKeyshare := keysharesMap[node.SigningKeyshareId]
	nodeUserPubKey, err := keys.ParsePublicKey(node.UserPublicKey)
	if err != nil {
		return nil, err
	}
	combinedPublicKey := signingKeyshare.PublicKey.Add(nodeUserPubKey)

	depositAddress, err := common.P2TRAddressFromPublicKey(combinedPublicKey, network)
	if err != nil {
		return nil, err
	}

	if save {
		db, err := ent.GetDbFromContext(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
		}
		schemaNetwork, err := common.SchemaNetworkFromNetwork(network)
		if err != nil {
			return nil, err
		}
		_, err = db.DepositAddress.Create().
			SetSigningKeyshareID(signingKeyshare.ID).
			SetOwnerIdentityPubkey(userIdentityPubKey).
			SetOwnerSigningPubkey(nodeUserPubKey).
			SetAddress(depositAddress).
			SetNetwork(schemaNetwork).
			Save(ctx)
		if err != nil {
			return nil, err
		}
	}
	if len(node.Children) == 0 {
		return &pb.AddressNode{
			Address: &pb.Address{
				Address:      depositAddress,
				VerifyingKey: combinedPublicKey.Serialize(),
			},
		}, nil
	}
	children := make([]*pb.AddressNode, len(node.Children))
	for i, child := range node.Children {
		children[i], err = h.createAddressNodeFromPrepareTreeAddressNode(ctx, network, child, keysharesMap, userIdentityPubKey, len(node.Children) > 1)
		if err != nil {
			return nil, err
		}
	}
	return &pb.AddressNode{
		Address: &pb.Address{
			Address:      depositAddress,
			VerifyingKey: combinedPublicKey.Serialize(),
		},
		Children: children,
	}, nil
}

// PrepareTreeAddress prepares the tree address for the given public key.
func (h *TreeCreationHandler) PrepareTreeAddress(ctx context.Context, req *pb.PrepareTreeAddressRequest) (*pb.PrepareTreeAddressResponse, error) {
	reqUserIDPubKey, err := keys.ParsePublicKey(req.GetUserIdentityPublicKey())
	if err != nil {
		return nil, fmt.Errorf("invalid identity public key: %w", err)
	}
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, reqUserIDPubKey); err != nil {
		return nil, err
	}

	var network common.Network
	switch req.Source.(type) {
	case *pb.PrepareTreeAddressRequest_ParentNodeOutput:
		nodeID, err := uuid.Parse(req.GetParentNodeOutput().NodeId)
		if err != nil {
			return nil, err
		}
		db, err := ent.GetDbFromContext(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
		}
		treeNode, err := db.TreeNode.Get(ctx, nodeID)
		if err != nil {
			return nil, err
		}

		if !bytes.Equal(req.UserIdentityPublicKey, treeNode.OwnerIdentityPubkey) {
			return nil, errors.New("user identity public key does not match tree node owner")
		}

		nodeTree, err := treeNode.QueryTree().Only(ctx)
		if err != nil {
			return nil, err
		}
		network, err = common.NetworkFromSchemaNetwork(nodeTree.Network)
		if err != nil {
			return nil, err
		}
	case *pb.PrepareTreeAddressRequest_OnChainUtxo:
		network, err = common.NetworkFromProtoNetwork(req.GetOnChainUtxo().Network)
		if err != nil {
			return nil, err
		}
	}

	parentUserPublicKey, signingKeyshare, err := h.findParentPublicKeys(ctx, network, req)
	if err != nil {
		return nil, err
	}

	keyCount, err := h.validateAndCountTreeAddressNodes(ctx, parentUserPublicKey, []*pb.AddressRequestNode{req.Node})
	if err != nil {
		return nil, err
	}

	keyshares, err := ent.GetUnusedSigningKeyshares(ctx, h.config, keyCount)
	if err != nil {
		return nil, err
	}

	if len(keyshares) < keyCount {
		return nil, fmt.Errorf("not enough keyshares available, need: %d, available: %d", keyCount, len(keyshares))
	}

	addressNode, err := h.createPrepareTreeAddressNodeFromAddressNode(ctx, req.Node)
	if err != nil {
		return nil, err
	}

	addressNode, keysharesMap, err := h.applyKeysharesToTree(ctx, signingKeyshare, addressNode, keyshares)
	if err != nil {
		return nil, err
	}

	operatorSelection := &helper.OperatorSelection{
		Option: helper.OperatorSelectionOptionExcludeSelf,
	}
	// TODO: Extract the address signature from response and adds to the proofs.
	_, err = helper.ExecuteTaskWithAllOperators(ctx, h.config, operatorSelection, func(ctx context.Context, operator *so.SigningOperator) (any, error) {
		conn, err := operator.NewOperatorGRPCConnection()
		if err != nil {
			return nil, err
		}
		defer conn.Close()
		client := pbinternal.NewSparkInternalServiceClient(conn)

		protoNetwork, err := common.ProtoNetworkFromNetwork(network)
		if err != nil {
			return nil, err
		}
		return client.PrepareTreeAddress(ctx, &pbinternal.PrepareTreeAddressRequest{
			TargetKeyshareId:      signingKeyshare.ID.String(),
			Node:                  addressNode,
			UserIdentityPublicKey: reqUserIDPubKey.Serialize(),
			Network:               protoNetwork,
		})
	})
	if err != nil {
		return nil, err
	}

	resultRootNode, err := h.createAddressNodeFromPrepareTreeAddressNode(ctx, network, addressNode, keysharesMap, reqUserIDPubKey, false)
	if err != nil {
		return nil, err
	}

	// TODO: Sign proof of possession for all signing keyshares.

	return &pb.PrepareTreeAddressResponse{Node: resultRootNode}, nil
}

func (h *TreeCreationHandler) prepareSigningJobs(ctx context.Context, req *pb.CreateTreeRequest, requireDirectTx bool) ([]*helper.SigningJob, []*ent.TreeNode, error) {
	parentOutput, err := h.findParentOutputFromCreateTreeRequest(ctx, req)
	if err != nil {
		return nil, nil, err
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}
	var parentNode *ent.TreeNode
	var vout uint32
	var network common.Network
	switch req.Source.(type) {
	case *pb.CreateTreeRequest_ParentNodeOutput:
		outputID, err := uuid.Parse(req.GetParentNodeOutput().NodeId)
		if err != nil {
			return nil, nil, err
		}
		parentNode, err = db.TreeNode.Get(ctx, outputID)
		if err != nil {
			return nil, nil, err
		}
		vout = req.GetParentNodeOutput().Vout
		parentTree, err := parentNode.QueryTree().Only(ctx)
		if err != nil {
			return nil, nil, err
		}
		network, err = common.NetworkFromSchemaNetwork(parentTree.Network)
		if err != nil {
			return nil, nil, err
		}
	case *pb.CreateTreeRequest_OnChainUtxo:
		parentNode = nil
		vout = req.GetOnChainUtxo().Vout
		network, err = common.NetworkFromProtoNetwork(req.GetOnChainUtxo().Network)
		if err != nil {
			return nil, nil, err
		}
	default:
		return nil, nil, errors.New("invalid source")
	}

	type element struct {
		output     *wire.TxOut
		node       *pb.CreationNode
		userPubKey keys.Public
		keyshare   *ent.SigningKeyshare
		parentNode *ent.TreeNode
		vout       uint32
	}

	addressString, err := common.P2TRAddressFromPkScript(parentOutput.PkScript, network)
	if err != nil {
		return nil, nil, err
	}
	depositAddress, err := db.DepositAddress.Query().Where(depositaddress.Address(*addressString)).Only(ctx)
	if err != nil {
		return nil, nil, err
	}
	keyshare, err := depositAddress.QuerySigningKeyshare().First(ctx)
	if err != nil {
		return nil, nil, err
	}
	unchainUtxo := req.GetOnChainUtxo()
	onChain := depositAddress.ConfirmationHeight != 0
	if depositAddress.ConfirmationTxid != "" && unchainUtxo != nil {
		if depositAddress.ConfirmationTxid != hex.EncodeToString(unchainUtxo.Txid) {
			return nil, nil, errors.New("confirmation txid does not match utxo txid")
		}
	}

	queue := []*element{{
		output:     parentOutput,
		node:       req.Node,
		userPubKey: depositAddress.OwnerSigningPubkey,
		keyshare:   keyshare,
		parentNode: parentNode,
		vout:       vout,
	}}

	var signingJobs []*helper.SigningJob
	var nodes []*ent.TreeNode

	for len(queue) > 0 {
		currentElement := queue[0]
		queue = queue[1:]
		if len(currentElement.node.Children) > 0 && currentElement.node.RefundTxSigningJob != nil {
			return nil, nil, errors.New("refund tx should be on leaf node")
		}

		cpfpSigningJob, cpfpTx, err := helper.NewSigningJob(currentElement.keyshare, currentElement.node.NodeTxSigningJob, currentElement.output)
		if err != nil {
			return nil, nil, err
		}
		signingJobs = append(signingJobs, cpfpSigningJob)

		var directSigningJob *helper.SigningJob
		var directTx *wire.MsgTx
		if currentElement.node.DirectNodeTxSigningJob != nil {
			directSigningJob, directTx, err = helper.NewSigningJob(currentElement.keyshare, currentElement.node.DirectNodeTxSigningJob, currentElement.output)
			if err != nil {
				return nil, nil, err
			}
			signingJobs = append(signingJobs, directSigningJob)
		} else if requireDirectTx {
			return nil, nil, errors.New("field DirectNodeTxSigningJob is required. Please upgrade to the latest SDK version")
		}

		var savedTree *ent.Tree
		var parentNodeID *uuid.UUID
		if currentElement.parentNode == nil {
			schemaNetwork, err := common.SchemaNetworkFromNetwork(network)
			if err != nil {
				return nil, nil, err
			}
			if req.GetOnChainUtxo() == nil {
				return nil, nil, errors.New("onchain utxo is required for new tree")
			}
			tx, err := common.TxFromRawTxBytes(req.GetOnChainUtxo().RawTx)
			if err != nil {
				return nil, nil, err
			}
			txid := tx.TxHash()
			treeMutator := db.Tree.
				Create().
				SetOwnerIdentityPubkey(req.UserIdentityPublicKey).
				SetNetwork(schemaNetwork).
				SetBaseTxid(txid[:]).
				SetVout(int16(req.GetOnChainUtxo().Vout))
			if onChain {
				treeMutator.SetStatus(st.TreeStatusAvailable)
			} else {
				treeMutator.SetStatus(st.TreeStatusPending)
			}
			savedTree, err = treeMutator.Save(ctx)
			if err != nil {
				return nil, nil, err
			}
			parentNodeID = nil
		} else {
			savedTree, err = currentElement.parentNode.QueryTree().Only(ctx)
			if err != nil {
				return nil, nil, err
			}
			parentNodeID = &currentElement.parentNode.ID
		}
		verifyingKey := currentElement.keyshare.PublicKey.Add(currentElement.userPubKey)

		var cpfpRefundTx []byte
		if currentElement.node.RefundTxSigningJob != nil {
			cpfpRefundTx = currentElement.node.RefundTxSigningJob.RawTx
		}

		var directRefundTx []byte
		if currentElement.node.DirectRefundTxSigningJob != nil {
			directRefundTx = currentElement.node.DirectRefundTxSigningJob.RawTx
		} else if requireDirectTx {
			return nil, nil, errors.New("directRefundTxSigningJob is required. Please upgrade to the latest SDK version")
		}

		var directFromCpfpRefundTx []byte
		if currentElement.node.DirectFromCpfpRefundTxSigningJob != nil {
			directFromCpfpRefundTx = currentElement.node.DirectFromCpfpRefundTxSigningJob.RawTx
		} else if requireDirectTx {
			return nil, nil, errors.New("directFromCpfpRefundTxSigningJob is required. Please upgrade to the latest SDK version")
		}

		var directTxRaw []byte
		if currentElement.node.DirectNodeTxSigningJob != nil {
			directTxRaw = currentElement.node.DirectNodeTxSigningJob.RawTx
		} else if requireDirectTx {
			return nil, nil, errors.New("directNodeTxSigningJob is required. Please upgrade to the latest SDK version")
		}

		createNode := db.
			TreeNode.
			Create().
			SetTree(savedTree).
			SetStatus(st.TreeNodeStatusCreating).
			SetOwnerIdentityPubkey(req.UserIdentityPublicKey).
			SetOwnerSigningPubkey(currentElement.userPubKey.Serialize()).
			SetValue(uint64(currentElement.output.Value)).
			SetVerifyingPubkey(verifyingKey.Serialize()).
			SetSigningKeyshare(currentElement.keyshare).
			SetRawTx(currentElement.node.NodeTxSigningJob.RawTx).
			SetRawRefundTx(cpfpRefundTx).
			SetDirectTx(directTxRaw).
			SetDirectRefundTx(directRefundTx).
			SetDirectFromCpfpRefundTx(directFromCpfpRefundTx).
			SetVout(int16(currentElement.vout))

		if parentNodeID != nil {
			createNode.SetParentID(*parentNodeID)
		}

		node, err := createNode.Save(ctx)
		if err != nil {
			return nil, nil, err
		}
		nodes = append(nodes, node)
		if currentElement.node.RefundTxSigningJob != nil {
			if len(cpfpTx.TxOut) <= 0 {
				return nil, nil, fmt.Errorf("vout out of bounds for cpfp node tx, need at least one output")
			}
			cpfpRefundSigningJob, _, err := helper.NewSigningJob(currentElement.keyshare, currentElement.node.RefundTxSigningJob, cpfpTx.TxOut[0])
			if err != nil {
				return nil, nil, err
			}
			signingJobs = append(signingJobs, cpfpRefundSigningJob)
			if currentElement.node.DirectRefundTxSigningJob != nil && currentElement.node.DirectFromCpfpRefundTxSigningJob != nil {
				if len(directTx.TxOut) <= 0 {
					return nil, nil, fmt.Errorf("vout out of bounds for cpfp node tx, need at least one output")
				}
				directRefundSigningJob, _, err := helper.NewSigningJob(currentElement.keyshare, currentElement.node.DirectRefundTxSigningJob, directTx.TxOut[0])
				if err != nil {
					return nil, nil, err
				}
				directFromCpfpRefundSigningJob, _, err := helper.NewSigningJob(currentElement.keyshare, currentElement.node.DirectFromCpfpRefundTxSigningJob, cpfpTx.TxOut[0])
				if err != nil {
					return nil, nil, err
				}
				signingJobs = append(signingJobs, directRefundSigningJob, directFromCpfpRefundSigningJob)
			} else if requireDirectTx {
				return nil, nil, errors.New("directRefundTxSigningJob or DirectFromCpfpRefundTxSigningJob is required. Please upgrade to the latest SDK version")
			}
		} else if len(currentElement.node.Children) > 0 {
			var userPublicKeys []keys.Public
			var statechainPublicKeys []keys.Public
			if len(cpfpTx.TxOut) < len(currentElement.node.Children) {
				return nil, nil, fmt.Errorf("vout out of bounds for node split cpfp tx, had: %d, needed: %d", len(cpfpTx.TxOut), len(currentElement.node.Children))
			}
			if directTx != nil && len(directTx.TxOut) < len(currentElement.node.Children) {
				return nil, nil, fmt.Errorf("vout out of bounds for node split direct tx, had: %d, needed: %d", len(directTx.TxOut), len(currentElement.node.Children))
			}
			for i, child := range currentElement.node.Children {
				userSigningKey, signingKeyshare, err := h.getSigningKeyshareFromOutput(ctx, network, cpfpTx.TxOut[i])
				if err != nil {
					return nil, nil, err
				}
				userPublicKeys = append(userPublicKeys, userSigningKey)
				statechainPublicKeys = append(statechainPublicKeys, signingKeyshare.PublicKey)
				queue = append(queue, &element{
					output:     cpfpTx.TxOut[i],
					node:       child,
					userPubKey: userSigningKey,
					keyshare:   signingKeyshare,
					parentNode: node,
					vout:       uint32(i),
				})
			}

			userPublicKeySum, err := keys.SumPublicKeys(userPublicKeys)
			if err != nil {
				return nil, nil, err
			}
			if !userPublicKeySum.Equals(currentElement.userPubKey) {
				return nil, nil, errors.New("user public key does not add up")
			}

			statechainPublicKeySum, err := keys.SumPublicKeys(statechainPublicKeys)
			if err != nil {
				return nil, nil, err
			}
			if !statechainPublicKeySum.Equals(currentElement.keyshare.PublicKey) {
				return nil, nil, errors.New("statechain public key does not add up")
			}
		}
	}

	return signingJobs, nodes, nil
}

func (h *TreeCreationHandler) createTreeResponseNodesFromSigningResults(
	req *pb.CreateTreeRequest,
	signingResults []*helper.SigningResult,
	nodes []*ent.TreeNode,
	requireDirectTx bool,
) (*pb.CreationResponseNode, error) {
	signingResultIndex := 0
	nodesIndex := 0
	root := &pb.CreationResponseNode{}

	type element struct {
		node         *pb.CreationResponseNode
		creationNode *pb.CreationNode
	}

	queue := []*element{{
		node:         root,
		creationNode: req.Node,
	}}

	for len(queue) > 0 {
		currentElement := queue[0]
		queue = queue[1:]

		cpfpSigningResult := signingResults[signingResultIndex]
		signingResultIndex++

		cpfpSigningResultProto, err := cpfpSigningResult.MarshalProto()
		if err != nil {
			return nil, err
		}

		currentElement.node.NodeTxSigningResult = cpfpSigningResultProto

		var directSigningResult *helper.SigningResult
		if currentElement.creationNode.DirectNodeTxSigningJob != nil {
			directSigningResult = signingResults[signingResultIndex]
			signingResultIndex++
			directSigningResultProto, err := directSigningResult.MarshalProto()
			if err != nil {
				return nil, err
			}
			currentElement.node.DirectNodeTxSigningResult = directSigningResultProto

		} else if requireDirectTx {
			return nil, errors.New("directNodeTxSigningJob is required. Please upgrade to the latest SDK version")
		}

		if currentElement.creationNode.RefundTxSigningJob != nil {
			cpfpSigningResult := signingResults[signingResultIndex]
			signingResultIndex++

			cpfpRefundSigningResultProto, err := cpfpSigningResult.MarshalProto()
			if err != nil {
				return nil, err
			}

			currentElement.node.RefundTxSigningResult = cpfpRefundSigningResultProto

			if currentElement.creationNode.DirectRefundTxSigningJob != nil && currentElement.creationNode.DirectFromCpfpRefundTxSigningJob != nil {
				directSigningResult := signingResults[signingResultIndex]
				signingResultIndex++
				directFromCpfpRefundSigningResult := signingResults[signingResultIndex]
				signingResultIndex++
				directRefundSigningResultProto, err := directSigningResult.MarshalProto()
				if err != nil {
					return nil, err
				}
				directFromCpfpRefundSigningResultProto, err := directFromCpfpRefundSigningResult.MarshalProto()
				if err != nil {
					return nil, err
				}
				currentElement.node.DirectRefundTxSigningResult = directRefundSigningResultProto
				currentElement.node.DirectFromCpfpRefundTxSigningResult = directFromCpfpRefundSigningResultProto
			} else if requireDirectTx {
				return nil, errors.New("directRefundTxSigningJob or DirectFromCpfpRefundTxSigningJob is required. Please upgrade to the latest SDK version")
			}
		} else if len(currentElement.creationNode.Children) > 0 {
			children := make([]*pb.CreationResponseNode, len(currentElement.creationNode.Children))
			for i, child := range currentElement.creationNode.Children {
				children[i] = &pb.CreationResponseNode{}
				queue = append(queue, &element{
					node:         children[i],
					creationNode: child,
				})
			}
			currentElement.node.Children = children
		}

		currentElement.node.NodeId = nodes[nodesIndex].ID.String()
		nodesIndex++
	}

	return root, nil
}

// createTree creates a tree from user input and signs the transactions in the tree.
func (h *TreeCreationHandler) createTree(ctx context.Context, req *pb.CreateTreeRequest, requireDirectTx bool) (*pb.CreateTreeResponse, error) {
	reqUserIDPubKey, err := keys.ParsePublicKey(req.GetUserIdentityPublicKey())
	if err != nil {
		return nil, fmt.Errorf("invalid identity public key: %w", err)
	}
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, reqUserIDPubKey); err != nil {
		return nil, err
	}

	signingJobs, nodes, err := h.prepareSigningJobs(ctx, req, requireDirectTx)
	if err != nil {
		return nil, err
	}

	signingResults, err := helper.SignFrost(ctx, h.config, signingJobs)
	if err != nil {
		return nil, err
	}

	node, err := h.createTreeResponseNodesFromSigningResults(req, signingResults, nodes, requireDirectTx)
	if err != nil {
		return nil, err
	}

	err = h.updateParentNodeStatus(ctx, req.GetParentNodeOutput())
	if err != nil {
		return nil, err
	}

	return &pb.CreateTreeResponse{
		Node: node,
	}, nil
}

// CreateTree creates a tree from user input and signs the transactions in the tree.
func (h *TreeCreationHandler) CreateTree(ctx context.Context, req *pb.CreateTreeRequest) (*pb.CreateTreeResponse, error) {
	return h.createTree(ctx, req, false)
}

// CreateTreeV2 creates a tree from user input and signs the transactions in the tree.
func (h *TreeCreationHandler) CreateTreeV2(ctx context.Context, req *pb.CreateTreeRequest) (*pb.CreateTreeResponse, error) {
	return h.createTree(ctx, req, true)
}

func (h *TreeCreationHandler) updateParentNodeStatus(ctx context.Context, parentNodeOutput *pb.NodeOutput) error {
	if parentNodeOutput == nil {
		return nil
	}

	parentNodeID, err := uuid.Parse(parentNodeOutput.NodeId)
	if err != nil {
		return err
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to get or create current tx for request: %w", err)
	}
	parentNode, err := db.TreeNode.Get(ctx, parentNodeID)
	if err != nil {
		return err
	}

	if parentNode.Status != st.TreeNodeStatusAvailable {
		return nil
	}

	err = db.TreeNode.UpdateOneID(parentNodeID).SetStatus(st.TreeNodeStatusSplitted).Exec(ctx)
	if err != nil {
		return fmt.Errorf("unable to update status of parent node: %w", err)
	}
	return nil
}
