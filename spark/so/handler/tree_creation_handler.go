package handler

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authz"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/depositaddress"
	"github.com/lightsparkdev/spark/so/ent/schema"
	"github.com/lightsparkdev/spark/so/helper"
)

// TreeCreationHandler is a handler for tree creation requests.
type TreeCreationHandler struct {
	config *so.Config
	db     *ent.Client
}

// NewTreeCreationHandler creates a new TreeCreationHandler.
func NewTreeCreationHandler(config *so.Config, db *ent.Client) *TreeCreationHandler {
	return &TreeCreationHandler{config: config, db: db}
}

func (h *TreeCreationHandler) findParentOutputFromUtxo(_ context.Context, utxo *pb.UTXO) (*wire.TxOut, error) {
	tx, err := common.TxFromRawTxBytes(utxo.RawTx)
	if err != nil {
		return nil, err
	}
	if len(tx.TxOut) <= int(utxo.Vout) {
		return nil, fmt.Errorf("vout out of bounds utxo, tx vout: %d, utxo vout: %d", len(tx.TxOut), utxo.Vout)
	}
	return tx.TxOut[utxo.Vout], nil
}

func (h *TreeCreationHandler) findParentOutputFromNodeOutput(ctx context.Context, nodeOutput *pb.NodeOutput) (*wire.TxOut, error) {
	db := ent.GetDbFromContext(ctx)
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

func (h *TreeCreationHandler) getSigningKeyshareFromOutput(ctx context.Context, network common.Network, output *wire.TxOut) ([]byte, *ent.SigningKeyshare, error) {
	addressString, err := common.P2TRAddressFromPkScript(output.PkScript, network)
	if err != nil {
		return nil, nil, err
	}

	db := ent.GetDbFromContext(ctx)
	depositAddress, err := db.DepositAddress.Query().Where(depositaddress.Address(*addressString)).Only(ctx)
	if err != nil {
		return nil, nil, err
	}

	keyshare, err := depositAddress.QuerySigningKeyshare().First(ctx)
	if err != nil {
		return nil, nil, err
	}

	return depositAddress.OwnerSigningPubkey, keyshare, nil
}

func (h *TreeCreationHandler) findParentPublicKeys(ctx context.Context, network common.Network, req *pb.PrepareTreeAddressRequest) ([]byte, *ent.SigningKeyshare, error) {
	parentOutput, err := h.findParentOutputFromPrepareTreeAddressRequest(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	return h.getSigningKeyshareFromOutput(ctx, network, parentOutput)
}

func (h *TreeCreationHandler) validateAndCountTreeAddressNodes(ctx context.Context, parentUserPublicKey []byte, nodes []*pb.AddressRequestNode) (int, error) {
	if len(nodes) == 0 {
		return 0, nil
	}

	count := len(nodes) - 1
	publicKeys := [][]byte{}
	for _, child := range nodes {
		childCount, err := h.validateAndCountTreeAddressNodes(ctx, child.UserPublicKey, child.Children)
		if err != nil {
			return 0, err
		}
		count += childCount
		publicKeys = append(publicKeys, child.UserPublicKey)
	}

	sum, err := common.AddPublicKeysList(publicKeys)
	if err != nil {
		return 0, err
	}

	if !bytes.Equal(sum, parentUserPublicKey) {
		return 0, errors.New("user public key does not add up to the parent public key")
	}
	return count, nil
}

func (h *TreeCreationHandler) createPrepareTreeAddressNodeFromAddressNode(ctx context.Context, node *pb.AddressRequestNode) (*pbinternal.PrepareTreeAddressNode, error) {
	if node.Children == nil {
		return &pbinternal.PrepareTreeAddressNode{
			UserPublicKey: node.UserPublicKey,
		}, nil
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

	queue := []*element{}
	queue = append(queue, &element{
		keyshare: targetKeyshare,
		children: []*pbinternal.PrepareTreeAddressNode{node},
	})

	keysharesMap := make(map[string]*ent.SigningKeyshare)

	for len(queue) > 0 {
		currentElement := queue[0]
		queue = queue[1:]

		selectedKeyshares := make([]*ent.SigningKeyshare, 0)

		if len(currentElement.children) == 0 {
			continue
		}

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
	userIdentityPublicKey []byte,
	save bool,
) (addressNode *pb.AddressNode, err error) {
	combinedPublicKey, err := common.AddPublicKeys(keysharesMap[node.SigningKeyshareId].PublicKey, node.UserPublicKey)
	if err != nil {
		return nil, err
	}

	depositAddress, err := common.P2TRAddressFromPublicKey(combinedPublicKey, network)
	if err != nil {
		return nil, err
	}

	if save {
		_, err = ent.GetDbFromContext(ctx).DepositAddress.Create().
			SetSigningKeyshareID(keysharesMap[node.SigningKeyshareId].ID).
			SetOwnerIdentityPubkey(userIdentityPublicKey).
			SetOwnerSigningPubkey(node.UserPublicKey).
			SetAddress(*depositAddress).
			Save(ctx)
		if err != nil {
			return nil, err
		}
	}
	if len(node.Children) == 0 {
		return &pb.AddressNode{
			Address: &pb.Address{
				Address:      *depositAddress,
				VerifyingKey: combinedPublicKey,
			},
		}, nil
	}
	children := make([]*pb.AddressNode, len(node.Children))
	for i, child := range node.Children {
		children[i], err = h.createAddressNodeFromPrepareTreeAddressNode(ctx, network, child, keysharesMap, userIdentityPublicKey, len(node.Children) > 1)
		if err != nil {
			return nil, err
		}
	}
	return &pb.AddressNode{
		Address: &pb.Address{
			Address:      *depositAddress,
			VerifyingKey: combinedPublicKey,
		},
		Children: children,
	}, nil
}

// PrepareTreeAddress prepares the tree address for the given public key.
func (h *TreeCreationHandler) PrepareTreeAddress(ctx context.Context, req *pb.PrepareTreeAddressRequest) (*pb.PrepareTreeAddressResponse, error) {
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, req.UserIdentityPublicKey); err != nil {
		return nil, err
	}

	var network common.Network
	var err error
	switch req.Source.(type) {
	case *pb.PrepareTreeAddressRequest_ParentNodeOutput:
		uuid, err := uuid.Parse(req.GetParentNodeOutput().NodeId)
		if err != nil {
			return nil, err
		}
		db := ent.GetDbFromContext(ctx)
		treeNode, err := db.TreeNode.Get(ctx, uuid)
		if err != nil {
			return nil, err
		}
		tree, err := treeNode.QueryTree().Only(ctx)
		if err != nil {
			return nil, err
		}
		network, err = common.NetworkFromSchemaNetwork(tree.Network)
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

	keyshares, err := ent.GetUnusedSigningKeyshares(ctx, h.db, h.config, keyCount)
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
	_, err = helper.ExecuteTaskWithAllOperators(ctx, h.config, operatorSelection, func(ctx context.Context, operator *so.SigningOperator) (interface{}, error) {
		conn, err := operator.NewGRPCConnection()
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
			UserIdentityPublicKey: req.UserIdentityPublicKey,
			Network:               protoNetwork,
		})
	})
	if err != nil {
		return nil, err
	}

	resultRootNode, err := h.createAddressNodeFromPrepareTreeAddressNode(ctx, network, addressNode, keysharesMap, req.UserIdentityPublicKey, false)
	if err != nil {
		return nil, err
	}

	// TODO: Sign proof of possession for all signing keyshares.

	response := &pb.PrepareTreeAddressResponse{
		Node: resultRootNode,
	}

	return response, nil
}

func (h *TreeCreationHandler) prepareSigningJobs(ctx context.Context, req *pb.CreateTreeRequest) ([]*helper.SigningJob, []*ent.TreeNode, error) {
	parentOutput, err := h.findParentOutputFromCreateTreeRequest(ctx, req)
	if err != nil {
		return nil, nil, err
	}

	db := ent.GetDbFromContext(ctx)
	var parentNode *ent.TreeNode
	var vout uint32
	var network common.Network
	switch req.Source.(type) {
	case *pb.CreateTreeRequest_ParentNodeOutput:
		uuid, err := uuid.Parse(req.GetParentNodeOutput().NodeId)
		if err != nil {
			return nil, nil, err
		}
		parentNode, err = db.TreeNode.Get(ctx, uuid)
		if err != nil {
			return nil, nil, err
		}
		vout = req.GetParentNodeOutput().Vout
		tree, err := parentNode.QueryTree().Only(ctx)
		if err != nil {
			return nil, nil, err
		}
		network, err = common.NetworkFromSchemaNetwork(tree.Network)
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
		output        *wire.TxOut
		node          *pb.CreationNode
		userPublicKey []byte
		keyshare      *ent.SigningKeyshare
		parentNode    *ent.TreeNode
		vout          uint32
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
	userPublicKey := depositAddress.OwnerSigningPubkey
	onchain := depositAddress.ConfirmationHeight != 0

	queue := []*element{}
	queue = append(queue, &element{
		output:        parentOutput,
		node:          req.Node,
		userPublicKey: userPublicKey,
		keyshare:      keyshare,
		parentNode:    parentNode,
		vout:          vout,
	})

	signingJobs := make([]*helper.SigningJob, 0)

	nodes := make([]*ent.TreeNode, 0)

	for len(queue) > 0 {
		currentElement := queue[0]
		queue = queue[1:]
		if len(currentElement.node.Children) > 0 && currentElement.node.RefundTxSigningJob != nil {
			return nil, nil, errors.New("refund tx should be on leaf node")
		}

		signingJob, tx, err := helper.NewSigningJob(currentElement.keyshare, currentElement.node.NodeTxSigningJob, currentElement.output, nil)
		if err != nil {
			return nil, nil, err
		}
		signingJobs = append(signingJobs, signingJob)

		var tree *ent.Tree
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
			if onchain {
				treeMutator.SetStatus(schema.TreeStatusAvailable)
			} else {
				treeMutator.SetStatus(schema.TreeStatusPending)
			}
			tree, err = treeMutator.Save(ctx)
			if err != nil {
				return nil, nil, err
			}
			parentNodeID = nil
		} else {
			tree, err = currentElement.parentNode.QueryTree().Only(ctx)
			if err != nil {
				return nil, nil, err
			}
			parentNodeID = &currentElement.parentNode.ID
		}

		verifyingKey, err := common.AddPublicKeys(currentElement.keyshare.PublicKey, currentElement.userPublicKey)
		if err != nil {
			return nil, nil, err
		}

		var rawRefundTx []byte
		if currentElement.node.RefundTxSigningJob != nil {
			rawRefundTx = currentElement.node.RefundTxSigningJob.RawTx
		}

		createNode := db.
			TreeNode.
			Create().
			SetTree(tree).
			SetStatus(schema.TreeNodeStatusCreating).
			SetOwnerIdentityPubkey(req.UserIdentityPublicKey).
			SetOwnerSigningPubkey(currentElement.userPublicKey).
			SetValue(uint64(currentElement.output.Value)).
			SetVerifyingPubkey(verifyingKey).
			SetSigningKeyshare(currentElement.keyshare).
			SetRawTx(currentElement.node.NodeTxSigningJob.RawTx).
			SetRawRefundTx(rawRefundTx).
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
			if len(tx.TxOut) <= 0 {
				return nil, nil, fmt.Errorf("vout out of bounds for node tx, need at least one output")
			}
			refundSigningJob, _, err := helper.NewSigningJob(currentElement.keyshare, currentElement.node.RefundTxSigningJob, tx.TxOut[0], nil)
			if err != nil {
				return nil, nil, err
			}
			signingJobs = append(signingJobs, refundSigningJob)
		} else if len(currentElement.node.Children) > 0 {
			userPublicKeys := [][]byte{}
			statechainPublicKeys := [][]byte{}
			if len(tx.TxOut) < len(currentElement.node.Children) {
				return nil, nil, fmt.Errorf("vout out of bounds for node split tx, had: %d, needed: %d", len(tx.TxOut), len(currentElement.node.Children))
			}
			for i, child := range currentElement.node.Children {
				userSigningKey, keyshare, err := h.getSigningKeyshareFromOutput(ctx, network, tx.TxOut[i])
				if err != nil {
					return nil, nil, err
				}
				userPublicKeys = append(userPublicKeys, userSigningKey)
				statechainPublicKeys = append(statechainPublicKeys, keyshare.PublicKey)
				queue = append(queue, &element{
					output:        tx.TxOut[i],
					node:          child,
					userPublicKey: userSigningKey,
					keyshare:      keyshare,
					parentNode:    node,
					vout:          uint32(i),
				})
			}

			userPublicKeySum, err := common.AddPublicKeysList(userPublicKeys)
			if err != nil {
				return nil, nil, err
			}
			if !bytes.Equal(userPublicKeySum, currentElement.userPublicKey) {
				return nil, nil, errors.New("user public key does not add up")
			}

			statechainPublicKeySum, err := common.AddPublicKeysList(statechainPublicKeys)
			if err != nil {
				return nil, nil, err
			}
			if !bytes.Equal(statechainPublicKeySum, currentElement.keyshare.PublicKey) {
				return nil, nil, errors.New("statechain public key does not add up")
			}
		}
	}

	return signingJobs, nodes, nil
}

func (h *TreeCreationHandler) createTreeResponseNodesFromSigningResults(req *pb.CreateTreeRequest, signingResults []*helper.SigningResult, nodes []*ent.TreeNode) (*pb.CreationResponseNode, error) {
	signingResultIndex := 0
	nodesIndex := 0
	root := &pb.CreationResponseNode{}

	type element struct {
		node         *pb.CreationResponseNode
		creationNode *pb.CreationNode
	}

	queue := []*element{}
	queue = append(queue, &element{
		node:         root,
		creationNode: req.Node,
	})

	for len(queue) > 0 {
		currentElement := queue[0]
		queue = queue[1:]

		signingResult := signingResults[signingResultIndex]
		signingResultIndex++

		signingResultProto, err := signingResult.MarshalProto()
		if err != nil {
			return nil, err
		}
		currentElement.node.NodeTxSigningResult = signingResultProto

		if currentElement.creationNode.RefundTxSigningJob != nil {
			signingResult = signingResults[signingResultIndex]
			signingResultIndex++

			refundSigningResultProto, err := signingResult.MarshalProto()
			if err != nil {
				return nil, err
			}
			currentElement.node.RefundTxSigningResult = refundSigningResultProto
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

// CreateTree creates a tree from user input and signs the transactions in the tree.
func (h *TreeCreationHandler) CreateTree(ctx context.Context, req *pb.CreateTreeRequest) (*pb.CreateTreeResponse, error) {
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, req.UserIdentityPublicKey); err != nil {
		return nil, err
	}

	signingJobs, nodes, err := h.prepareSigningJobs(ctx, req)
	if err != nil {
		return nil, err
	}

	signingResults, err := helper.SignFrost(ctx, h.config, signingJobs)
	if err != nil {
		return nil, err
	}

	node, err := h.createTreeResponseNodesFromSigningResults(req, signingResults, nodes)
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

func (h *TreeCreationHandler) updateParentNodeStatus(ctx context.Context, parentNodeOutput *pb.NodeOutput) error {
	if parentNodeOutput == nil {
		return nil
	}

	parentNodeID, err := uuid.Parse(parentNodeOutput.NodeId)
	if err != nil {
		return err
	}

	db := ent.GetDbFromContext(ctx)
	parentNode, err := db.TreeNode.Get(ctx, parentNodeID)
	if err != nil {
		return err
	}

	if parentNode.Status != schema.TreeNodeStatusAvailable {
		return nil
	}

	err = db.TreeNode.UpdateOneID(parentNodeID).SetStatus(schema.TreeNodeStatusSplitted).Exec(ctx)
	if err != nil {
		return fmt.Errorf("unable to update status of parent node: %v", err)
	}
	return nil
}
