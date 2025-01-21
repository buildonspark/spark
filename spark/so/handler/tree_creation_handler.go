package handler

import (
	"bytes"
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go/common"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	pbinternal "github.com/lightsparkdev/spark-go/proto/spark_internal"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent"
	"github.com/lightsparkdev/spark-go/so/ent/depositaddress"
	"github.com/lightsparkdev/spark-go/so/helper"
)

// TreeCreationHandler is a handler for tree creation requests.
type TreeCreationHandler struct {
	config        *so.Config
	onchainHelper helper.OnChainHelper
}

// NewTreeCreationHandler creates a new TreeCreationHandler.
func NewTreeCreationHandler(config *so.Config, onchainHelper helper.OnChainHelper) *TreeCreationHandler {
	return &TreeCreationHandler{config: config, onchainHelper: onchainHelper}
}

func (h *TreeCreationHandler) findParentPublicKeys(ctx context.Context, req *pb.PrepareTreeAddressRequest) ([]byte, *ent.SigningKeyshare, error) {
	var addressString *string
	switch req.Source.(type) {
	case *pb.PrepareTreeAddressRequest_ParentNodeOutput:
		db := ent.GetDbFromContext(ctx)
		nodeID, err := uuid.Parse(req.GetParentNodeOutput().NodeId)
		if err != nil {
			return nil, nil, err
		}
		node, err := db.TreeNode.Get(ctx, nodeID)
		if err != nil {
			return nil, nil, err
		}
		tx, err := common.TxFromRawTxBytes(node.RawTx)
		if err != nil {
			return nil, nil, err
		}
		addressString, err = common.P2TRAddressFromPkScript(tx.TxOut[req.GetParentNodeOutput().Vout].PkScript, h.config.Network)
		if err != nil {
			return nil, nil, err
		}
	case *pb.PrepareTreeAddressRequest_OnChainUtxo:
		tx, err := h.onchainHelper.GetTxOnChain(ctx, req.GetOnChainUtxo().Txid)
		if err != nil {
			return nil, nil, err
		}
		addressString, err = common.P2TRAddressFromPkScript(tx.TxOut[req.GetOnChainUtxo().Vout].PkScript, h.config.Network)
		if err != nil {
			return nil, nil, err
		}

	default:
		return nil, nil, errors.New("invalid source")
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

	if bytes.Compare(sum, parentUserPublicKey) != 0 {
		return 0, errors.New("User public key does not add up to the parent public key")
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

		lastKeyshare, err := ent.CalculateAndStoreLastKey(ctx, h.config, currentElement.keyshare, selectedKeyshares, uuid.New())
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

func (h *TreeCreationHandler) createAddressNodeFromPrepareTreeAddressNode(ctx context.Context, node *pbinternal.PrepareTreeAddressNode, keysharesMap map[string]*ent.SigningKeyshare, userIdentityPublicKey []byte, save bool) (addressNode *pb.AddressNode, err error) {
	combinedPublicKey, err := common.AddPublicKeys(keysharesMap[node.SigningKeyshareId].PublicKey, node.UserPublicKey)
	if err != nil {
		return nil, err
	}

	depositAddress, err := common.P2TRAddressFromPublicKey(combinedPublicKey, h.config.Network)
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
		children[i], err = h.createAddressNodeFromPrepareTreeAddressNode(ctx, child, keysharesMap, userIdentityPublicKey, len(node.Children) > 1)
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

func (h *TreeCreationHandler) createAddressNodesFromPrepareTreeAddressNodes(ctx context.Context, nodes []*pbinternal.PrepareTreeAddressNode, keysharesMap map[string]*ent.SigningKeyshare, userIdentityPublicKey []byte) (addressNodes []*pb.AddressNode, err error) {
	addressNodes = make([]*pb.AddressNode, len(nodes))
	for i, node := range nodes {
		addressNodes[i], err = h.createAddressNodeFromPrepareTreeAddressNode(ctx, node, keysharesMap, userIdentityPublicKey, len(nodes) > 1)
		if err != nil {
			return nil, err
		}
	}
	return addressNodes, nil
}

// PrepareTreeAddress prepares a tree address for creation.
func (h *TreeCreationHandler) PrepareTreeAddress(ctx context.Context, req *pb.PrepareTreeAddressRequest) (*pb.PrepareTreeAddressResponse, error) {
	parentUserPublicKey, signingKeyshare, err := h.findParentPublicKeys(ctx, req)
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

	keysharesToMark := make([]uuid.UUID, 0)
	for _, keyshare := range keyshares {
		keysharesToMark = append(keysharesToMark, keyshare.ID)
	}
	err = ent.MarkSigningKeysharesAsUsed(ctx, h.config, keysharesToMark)
	if err != nil {
		return nil, err
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
		conn, err := common.NewGRPCConnection(operator.Address)
		if err != nil {
			return nil, err
		}
		client := pbinternal.NewSparkInternalServiceClient(conn)

		return client.PrepareTreeAddress(ctx, &pbinternal.PrepareTreeAddressRequest{
			TargetKeyshareId:      signingKeyshare.ID.String(),
			Node:                  addressNode,
			UserIdentityPublicKey: req.UserIdentityPublicKey,
		})
	})
	if err != nil {
		return nil, err
	}

	resultRootNode, err := h.createAddressNodeFromPrepareTreeAddressNode(ctx, addressNode, keysharesMap, req.UserIdentityPublicKey, false)
	if err != nil {
		return nil, err
	}

	// TODO: Sign proof of possession for all signing keyshares.

	response := &pb.PrepareTreeAddressResponse{
		Node: resultRootNode,
	}

	return response, nil
}
