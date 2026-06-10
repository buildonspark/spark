package handler

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"

	"entgo.io/ent/dialect/sql/sqlgraph"
	"github.com/lightsparkdev/spark/common/btcnetwork"
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
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
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
	if utxo == nil {
		return nil, errors.New("on-chain utxo is required")
	}
	tx, err := common.TxFromRawTxBytes(utxo.GetRawTx())
	if err != nil {
		return nil, err
	}
	txHash := tx.TxHash()
	if len(utxo.GetTxid()) > 0 {
		requestedTxid := hex.EncodeToString(utxo.GetTxid())
		if requestedTxid != txHash.String() && requestedTxid != hex.EncodeToString(txHash[:]) {
			return nil, fmt.Errorf("utxo txid does not match raw transaction txid")
		}
	}
	if len(tx.TxOut) <= int(utxo.GetVout()) {
		return nil, fmt.Errorf("vout out of bounds utxo, tx vout: %d, utxo vout: %d", len(tx.TxOut), utxo.GetVout())
	}
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}
	query := db.Tree.Query().Where(
		tree.BaseTxid(st.NewTxID(txHash)),
		tree.Vout(int16(utxo.GetVout())),
	)
	count, err := query.Count(ctx)
	if err != nil {
		return nil, err
	}
	if count > 0 {
		// The only way to detect a parent is split is to check if the subtree of that tree node already exists.
		return nil, fmt.Errorf("tree with base txid %s already exists", txHash.String())
	}
	return tx.TxOut[utxo.GetVout()], nil
}

func (h *TreeCreationHandler) findParentOutputFromNodeOutput(ctx context.Context, nodeOutput *pb.NodeOutput, lockParent bool) (*wire.TxOut, error) {
	if nodeOutput == nil {
		return nil, errors.New("parent node output is required")
	}
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}
	nodeID, err := uuid.Parse(nodeOutput.GetNodeId())
	if err != nil {
		return nil, err
	}
	nodeQuery := db.TreeNode.Query().Where(treenode.ID(nodeID))
	if lockParent {
		nodeQuery = nodeQuery.ForUpdate()
	}
	node, err := nodeQuery.Only(ctx)
	if err != nil {
		return nil, err
	}
	if lockParent && node.Status != st.TreeNodeStatusCreating && node.Status != st.TreeNodeStatusAvailable {
		return nil, fmt.Errorf("node %s is not eligible for tree creation from status %s", nodeID.String(), node.Status)
	}

	tx, err := common.TxFromRawTxBytes(node.RawTx)
	if err != nil {
		return nil, err
	}
	if len(tx.TxOut) <= int(nodeOutput.GetVout()) {
		return nil, fmt.Errorf("vout out of bounds node output, tx vout: %d, node output vout: %d", len(tx.TxOut), nodeOutput.GetVout())
	}

	childQuery := db.TreeNode.Query().Where(
		treenode.HasParentWith(treenode.ID(nodeID)),
		treenode.Vout(int16(nodeOutput.GetVout())),
	)
	children, err := childQuery.Count(ctx)
	if err != nil {
		return nil, err
	}
	if children > 0 {
		// The only way to detect a child is split is to check if the subtree of that tree node already exists.
		return nil, fmt.Errorf("node %s child vout %d already exists", nodeID.String(), nodeOutput.GetVout())
	}
	return tx.TxOut[nodeOutput.GetVout()], nil
}

func (h *TreeCreationHandler) findParentOutputFromPrepareTreeAddressRequest(ctx context.Context, req *pb.PrepareTreeAddressRequest) (*wire.TxOut, error) {
	switch req.GetSource().(type) {
	case *pb.PrepareTreeAddressRequest_ParentNodeOutput:
		return h.findParentOutputFromNodeOutput(ctx, req.GetParentNodeOutput(), false)
	case *pb.PrepareTreeAddressRequest_OnChainUtxo:
		return h.findParentOutputFromUtxo(ctx, req.GetOnChainUtxo())
	default:
		return nil, errors.New("invalid source")
	}
}

func (h *TreeCreationHandler) findParentOutputFromCreateTreeRequest(ctx context.Context, req *pb.CreateTreeRequest) (*wire.TxOut, error) {
	switch req.GetSource().(type) {
	case *pb.CreateTreeRequest_ParentNodeOutput:
		return h.findParentOutputFromNodeOutput(ctx, req.GetParentNodeOutput(), true)
	case *pb.CreateTreeRequest_OnChainUtxo:
		return h.findParentOutputFromUtxo(ctx, req.GetOnChainUtxo())
	default:
		return nil, errors.New("invalid source")
	}
}

func validateTreeCreationTxSpendsOutpoint(tx *wire.MsgTx, expectedOutPoint wire.OutPoint, txName string) error {
	if tx == nil {
		return fmt.Errorf("%s is required", txName)
	}
	if len(tx.TxIn) != 1 {
		return fmt.Errorf("%s must have exactly one input, got %d", txName, len(tx.TxIn))
	}
	if tx.TxIn[0].PreviousOutPoint != expectedOutPoint {
		return fmt.Errorf("%s input 0 must spend %s, got %s", txName, expectedOutPoint.String(), tx.TxIn[0].PreviousOutPoint.String())
	}
	return nil
}

func isTreeCreationParentStatusEligible(status st.TreeNodeStatus) bool {
	return status == st.TreeNodeStatusCreating || status == st.TreeNodeStatusAvailable
}

func (h *TreeCreationHandler) getDepositAddressFromOutput(ctx context.Context, network btcnetwork.Network, output *wire.TxOut) (*ent.DepositAddress, error) {
	addressString, err := common.P2TRAddressFromPkScript(output.PkScript, network)
	if err != nil {
		return nil, err
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}
	depositAddress, err := db.DepositAddress.Query().Where(depositaddress.Address(*addressString)).Only(ctx)
	if err != nil {
		return nil, err
	}
	return depositAddress, nil
}

func (h *TreeCreationHandler) getSigningKeyshareFromOutput(ctx context.Context, network btcnetwork.Network, output *wire.TxOut) (keys.Public, *ent.SigningKeyshare, error) {
	depositAddress, err := h.getDepositAddressFromOutput(ctx, network, output)
	if err != nil {
		return keys.Public{}, nil, err
	}

	keyshare, err := depositAddress.QuerySigningKeyshare().First(ctx)
	if err != nil {
		return keys.Public{}, nil, err
	}

	return depositAddress.OwnerSigningPubkey, keyshare, nil
}

func (h *TreeCreationHandler) getOwnedSigningKeyshareFromOutput(ctx context.Context, network btcnetwork.Network, output *wire.TxOut, ownerIdentityPubkey keys.Public) (keys.Public, *ent.SigningKeyshare, error) {
	depositAddress, err := h.getDepositAddressFromOutput(ctx, network, output)
	if err != nil {
		return keys.Public{}, nil, err
	}
	if !ownerIdentityPubkey.Equals(depositAddress.OwnerIdentityPubkey) {
		return keys.Public{}, nil, sparkerrors.PermissionDeniedNoReadAccess(
			fmt.Errorf("user identity public key does not match child deposit address owner"),
		)
	}

	keyshare, err := depositAddress.QuerySigningKeyshare().First(ctx)
	if err != nil {
		return keys.Public{}, nil, err
	}

	return depositAddress.OwnerSigningPubkey, keyshare, nil
}

// splitTxOutputsWithoutEphemeralAnchor returns the tx outputs, excluding an
// optional trailing output that is exactly the canonical ephemeral anchor.
// Production clients (SSP and SDK) append a zero-value anchor output to CPFP
// txs for fee bumping; it is not a child output and has no prepared deposit
// address. Anything that is not byte-exact (nonzero value or a different
// script) is treated as a regular output and rejected by the prepared-address
// and count checks.
func splitTxOutputsWithoutEphemeralAnchor(tx *wire.MsgTx) []*wire.TxOut {
	anchor := common.EphemeralAnchorOutput()
	if last := len(tx.TxOut) - 1; last >= 0 && tx.TxOut[last].Value == anchor.Value && bytes.Equal(tx.TxOut[last].PkScript, anchor.PkScript) {
		return tx.TxOut[:last]
	}
	return tx.TxOut
}

func (h *TreeCreationHandler) validateTreeCreationSplitOutputs(
	ctx context.Context,
	network btcnetwork.Network,
	outputs []*wire.TxOut,
	ownerIdentityPubkey keys.Public,
	expectedUserPubkey keys.Public,
	expectedStatechainPubkey keys.Public,
	txName string,
) ([]keys.Public, []*ent.SigningKeyshare, error) {
	userPublicKeys := make([]keys.Public, 0, len(outputs))
	signingKeyshares := make([]*ent.SigningKeyshare, 0, len(outputs))
	statechainPublicKeys := make([]keys.Public, 0, len(outputs))
	for i, output := range outputs {
		userSigningKey, signingKeyshare, err := h.getOwnedSigningKeyshareFromOutput(ctx, network, output, ownerIdentityPubkey)
		if err != nil {
			return nil, nil, fmt.Errorf("%s output %d must pay an owned prepared deposit address: %w", txName, i, err)
		}
		userPublicKeys = append(userPublicKeys, userSigningKey)
		signingKeyshares = append(signingKeyshares, signingKeyshare)
		statechainPublicKeys = append(statechainPublicKeys, signingKeyshare.PublicKey)
	}

	userPublicKeySum, err := keys.SumPublicKeys(userPublicKeys)
	if err != nil {
		return nil, nil, err
	}
	if !userPublicKeySum.Equals(expectedUserPubkey) {
		return nil, nil, errors.New("user public key does not add up")
	}

	statechainPublicKeySum, err := keys.SumPublicKeys(statechainPublicKeys)
	if err != nil {
		return nil, nil, err
	}
	if !statechainPublicKeySum.Equals(expectedStatechainPubkey) {
		return nil, nil, errors.New("statechain public key does not add up")
	}

	return userPublicKeys, signingKeyshares, nil
}

// validateTreeCreationDirectSplitOutputs binds each direct tx output to the
// CPFP output at the same index so a caller cannot permute outputs or skew
// values on the direct exit path relative to the validated CPFP outputs. The
// direct tx carries no anchor and pays the fee-adjusted CPFP amounts.
func validateTreeCreationDirectSplitOutputs(directTx *wire.MsgTx, cpfpOutputs []*wire.TxOut, txName string) error {
	if len(directTx.TxOut) != len(cpfpOutputs) {
		return fmt.Errorf("%s output count must match cpfp tx non-anchor output count, had: %d, needed: %d", txName, len(directTx.TxOut), len(cpfpOutputs))
	}
	for i, output := range directTx.TxOut {
		if !bytes.Equal(output.PkScript, cpfpOutputs[i].PkScript) {
			return fmt.Errorf("%s output %d script must match cpfp tx output %d script", txName, i, i)
		}
		if expectedValue := common.MaybeApplyFee(cpfpOutputs[i].Value); output.Value != expectedValue {
			return fmt.Errorf("%s output %d value must be the fee-adjusted cpfp tx output value, had: %d, needed: %d", txName, i, output.Value, expectedValue)
		}
	}
	return nil
}

func (h *TreeCreationHandler) findParentDepositAddress(ctx context.Context, network btcnetwork.Network, req *pb.PrepareTreeAddressRequest) (*ent.DepositAddress, error) {
	parentOutput, err := h.findParentOutputFromPrepareTreeAddressRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	return h.getDepositAddressFromOutput(ctx, network, parentOutput)
}

func (h *TreeCreationHandler) validateAndCountTreeAddressNodes(ctx context.Context, parentUserPubKey keys.Public, nodes []*pb.AddressRequestNode) (int, error) {
	if len(nodes) == 0 {
		return 0, nil
	}

	count := len(nodes) - 1
	var publicKeys []keys.Public
	for _, child := range nodes {
		childPubKey, err := keys.ParsePublicKey(child.GetUserPublicKey())
		if err != nil {
			return 0, err
		}
		childCount, err := h.validateAndCountTreeAddressNodes(ctx, childPubKey, child.GetChildren())
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
		return &pbinternal.PrepareTreeAddressNode{UserPublicKey: node.GetUserPublicKey()}, nil
	}
	children := make([]*pbinternal.PrepareTreeAddressNode, len(node.GetChildren()))
	var err error
	for i, child := range node.GetChildren() {
		children[i], err = h.createPrepareTreeAddressNodeFromAddressNode(ctx, child)
		if err != nil {
			return nil, err
		}
	}
	return &pbinternal.PrepareTreeAddressNode{
		UserPublicKey: node.GetUserPublicKey(),
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
				children: child.GetChildren(),
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
			children: currentElement.children[len(currentElement.children)-1].GetChildren(),
		})
	}

	return node, keysharesMap, nil
}

func (h *TreeCreationHandler) createAddressNodeFromPrepareTreeAddressNode(
	ctx context.Context,
	network btcnetwork.Network,
	node *pbinternal.PrepareTreeAddressNode,
	keysharesMap map[string]*ent.SigningKeyshare,
	userIdentityPubKey keys.Public,
	save bool,
) (addressNode *pb.AddressNode, err error) {
	signingKeyshare := keysharesMap[node.GetSigningKeyshareId()]
	nodeUserPubKey, err := keys.ParsePublicKey(node.GetUserPublicKey())
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
		_, err = db.DepositAddress.Create().
			SetSigningKeyshareID(signingKeyshare.ID).
			SetOwnerIdentityPubkey(userIdentityPubKey).
			SetOwnerSigningPubkey(nodeUserPubKey).
			SetAddress(depositAddress).
			SetNetwork(network).
			Save(ctx)
		if err != nil {
			return nil, err
		}
	}
	if len(node.GetChildren()) == 0 {
		return &pb.AddressNode{
			Address: &pb.Address{
				Address:      depositAddress,
				VerifyingKey: combinedPublicKey.Serialize(),
			},
		}, nil
	}
	children := make([]*pb.AddressNode, len(node.GetChildren()))
	for i, child := range node.GetChildren() {
		children[i], err = h.createAddressNodeFromPrepareTreeAddressNode(ctx, network, child, keysharesMap, userIdentityPubKey, len(node.GetChildren()) > 1)
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
	if err := authz.EnforceWalletNotKillSwitched(ctx, reqUserIDPubKey); err != nil {
		return nil, err
	}

	var network btcnetwork.Network
	switch reqSource := req.GetSource().(type) {
	case *pb.PrepareTreeAddressRequest_ParentNodeOutput:
		nodeID, err := uuid.Parse(req.GetParentNodeOutput().GetNodeId())
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

		if !reqUserIDPubKey.Equals(treeNode.OwnerIdentityPubkey) {
			return nil, sparkerrors.PermissionDeniedNoReadAccess(
				fmt.Errorf("user identity public key does not match tree node owner"),
			)
		}

		nodeTree, err := treeNode.QueryTree().Only(ctx)
		if err != nil {
			return nil, err
		}
		network = nodeTree.Network
	case *pb.PrepareTreeAddressRequest_OnChainUtxo:
		network, err = btcnetwork.FromProtoNetwork(reqSource.OnChainUtxo.GetNetwork())
		if err != nil {
			return nil, err
		}
	}

	parentDepositAddress, err := h.findParentDepositAddress(ctx, network, req)
	if err != nil {
		return nil, err
	}
	if !reqUserIDPubKey.Equals(parentDepositAddress.OwnerIdentityPubkey) {
		return nil, sparkerrors.PermissionDeniedNoReadAccess(
			fmt.Errorf("user identity public key does not match deposit address owner"),
		)
	}
	signingKeyshare, err := parentDepositAddress.QuerySigningKeyshare().First(ctx)
	if err != nil {
		return nil, err
	}
	parentUserPublicKey := parentDepositAddress.OwnerSigningPubkey

	keyCount, err := h.validateAndCountTreeAddressNodes(ctx, parentUserPublicKey, []*pb.AddressRequestNode{req.GetNode()})
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

	addressNode, err := h.createPrepareTreeAddressNodeFromAddressNode(ctx, req.GetNode())
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

		protoNetwork, err := network.ToProtoNetwork()
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
	userIDPubKey, err := keys.ParsePublicKey(req.GetUserIdentityPublicKey())
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse user identity public key: %w", err)
	}

	var parentNode *ent.TreeNode
	var vout uint32
	var network btcnetwork.Network
	var parentOutPoint wire.OutPoint
	switch req.GetSource().(type) {
	case *pb.CreateTreeRequest_ParentNodeOutput:
		outputID, err := uuid.Parse(req.GetParentNodeOutput().GetNodeId())
		if err != nil {
			return nil, nil, err
		}
		parentNode, err = db.TreeNode.Get(ctx, outputID)
		if err != nil {
			return nil, nil, err
		}
		if !parentNode.OwnerIdentityPubkey.Equals(userIDPubKey) {
			return nil, nil, sparkerrors.PermissionDeniedNoReadAccess(
				fmt.Errorf("user identity public key does not match parent node owner"),
			)
		}
		if !isTreeCreationParentStatusEligible(parentNode.Status) {
			return nil, nil, fmt.Errorf("parent node %s status %s is not eligible for tree creation", parentNode.ID, parentNode.Status)
		}
		vout = req.GetParentNodeOutput().GetVout()
		parentTree, err := parentNode.QueryTree().Only(ctx)
		if err != nil {
			return nil, nil, err
		}
		network = parentTree.Network
		parentTx, err := common.TxFromRawTxBytes(parentNode.RawTx)
		if err != nil {
			return nil, nil, err
		}
		parentOutPoint = wire.OutPoint{Hash: parentTx.TxHash(), Index: vout}
	case *pb.CreateTreeRequest_OnChainUtxo:
		parentNode = nil
		vout = req.GetOnChainUtxo().GetVout()
		network, err = btcnetwork.FromProtoNetwork(req.GetOnChainUtxo().GetNetwork())
		if err != nil {
			return nil, nil, err
		}
		onChainTx, err := common.TxFromRawTxBytes(req.GetOnChainUtxo().GetRawTx())
		if err != nil {
			return nil, nil, err
		}
		parentOutPoint = wire.OutPoint{Hash: onChainTx.TxHash(), Index: vout}
	default:
		return nil, nil, errors.New("invalid source")
	}

	type element struct {
		output     *wire.TxOut
		outPoint   wire.OutPoint
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
	depositAddress, err := db.DepositAddress.Query().Where(depositaddress.Address(*addressString)).WithTree().ForUpdate().Only(ctx)
	if err != nil {
		return nil, nil, err
	}
	if !userIDPubKey.Equals(depositAddress.OwnerIdentityPubkey) {
		return nil, nil, sparkerrors.PermissionDeniedNoReadAccess(
			fmt.Errorf("user identity public key does not match deposit address owner"),
		)
	}
	keyshare, err := depositAddress.QuerySigningKeyshare().First(ctx)
	if err != nil {
		return nil, nil, err
	}
	unchainUtxo := req.GetOnChainUtxo()
	onChain := depositAddress.ConfirmationHeight != 0
	if depositAddress.ConfirmationTxid != "" && unchainUtxo != nil {
		if depositAddress.ConfirmationTxid != hex.EncodeToString(unchainUtxo.GetTxid()) {
			return nil, nil, errors.New("confirmation txid does not match utxo txid")
		}
	}

	queue := []*element{{
		output:     parentOutput,
		outPoint:   parentOutPoint,
		node:       req.GetNode(),
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
		if len(currentElement.node.GetChildren()) > 0 && currentElement.node.GetRefundTxSigningJob() != nil {
			return nil, nil, errors.New("refund tx should be on leaf node")
		}

		cpfpSigningJob, cpfpTx, err := helper.NewSigningJob(currentElement.keyshare, currentElement.node.GetNodeTxSigningJob(), currentElement.output)
		if err != nil {
			return nil, nil, err
		}
		if err := validateTreeCreationTxSpendsOutpoint(cpfpTx, currentElement.outPoint, "node transaction"); err != nil {
			return nil, nil, err
		}
		signingJobs = append(signingJobs, cpfpSigningJob)

		var directSigningJob *helper.SigningJob
		var directTx *wire.MsgTx
		if currentElement.node.GetDirectNodeTxSigningJob() != nil {
			directSigningJob, directTx, err = helper.NewSigningJob(currentElement.keyshare, currentElement.node.GetDirectNodeTxSigningJob(), currentElement.output)
			if err != nil {
				return nil, nil, err
			}
			if err := validateTreeCreationTxSpendsOutpoint(directTx, currentElement.outPoint, "direct node transaction"); err != nil {
				return nil, nil, err
			}
			signingJobs = append(signingJobs, directSigningJob)
		} else if requireDirectTx {
			return nil, nil, errors.New("field DirectNodeTxSigningJob is required. Please upgrade to the latest SDK version")
		}

		var savedTree *ent.Tree
		var parentNodeID *uuid.UUID
		if currentElement.parentNode == nil {
			if depositAddress.Edges.Tree != nil {
				return nil, nil, errors.New("deposit address already has a tree")
			}
			if req.GetOnChainUtxo() == nil {
				return nil, nil, errors.New("onchain utxo is required for new tree")
			}
			tx, err := common.TxFromRawTxBytes(req.GetOnChainUtxo().GetRawTx())
			if err != nil {
				return nil, nil, err
			}
			txid := tx.TxHash()
			treeMutator := db.Tree.
				Create().
				SetOwnerIdentityPubkey(userIDPubKey).
				SetNetwork(network).
				SetBaseTxid(st.NewTxID(txid)).
				SetVout(int16(req.GetOnChainUtxo().GetVout())).
				SetDepositAddress(depositAddress)
			if onChain {
				treeMutator.SetStatus(st.TreeStatusAvailable)
			} else {
				treeMutator.SetStatus(st.TreeStatusPending)
			}
			savedTree, err = treeMutator.Save(ctx)
			if err != nil {
				if sqlgraph.IsUniqueConstraintError(err) {
					return nil, nil, sparkerrors.AlreadyExistsDuplicateOperation(fmt.Errorf("tree already exists: %w", err))
				}
				return nil, nil, fmt.Errorf("failed to create tree: %w", err)
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
		if currentElement.node.GetRefundTxSigningJob() != nil {
			cpfpRefundTx = currentElement.node.GetRefundTxSigningJob().GetRawTx()
		}

		var directRefundTx []byte
		if currentElement.node.GetDirectRefundTxSigningJob() != nil {
			directRefundTx = currentElement.node.GetDirectRefundTxSigningJob().GetRawTx()
		} else if requireDirectTx {
			return nil, nil, errors.New("directRefundTxSigningJob is required. Please upgrade to the latest SDK version")
		}

		var directFromCpfpRefundTx []byte
		if currentElement.node.GetDirectFromCpfpRefundTxSigningJob() != nil {
			directFromCpfpRefundTx = currentElement.node.GetDirectFromCpfpRefundTxSigningJob().GetRawTx()
		} else if requireDirectTx {
			return nil, nil, errors.New("directFromCpfpRefundTxSigningJob is required. Please upgrade to the latest SDK version")
		}

		var directTxRaw []byte
		if currentElement.node.GetDirectNodeTxSigningJob() != nil {
			directTxRaw = currentElement.node.GetDirectNodeTxSigningJob().GetRawTx()
		} else if requireDirectTx {
			return nil, nil, errors.New("directNodeTxSigningJob is required. Please upgrade to the latest SDK version")
		}

		createNode := db.TreeNode.Create().
			SetTree(savedTree).
			SetNetwork(network).
			SetStatus(st.TreeNodeStatusCreating).
			SetOwnerIdentityPubkey(userIDPubKey).
			SetOwnerSigningPubkey(currentElement.userPubKey).
			SetValue(uint64(currentElement.output.Value)).
			SetVerifyingPubkey(verifyingKey).
			SetSigningKeyshare(currentElement.keyshare).
			SetRawTx(currentElement.node.GetNodeTxSigningJob().GetRawTx()).
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
			if sqlgraph.IsUniqueConstraintError(err) {
				return nil, nil, sparkerrors.AlreadyExistsDuplicateOperation(fmt.Errorf("tree node already exists: %w", err))
			}
			if sqlgraph.IsForeignKeyConstraintError(err) {
				return nil, nil, sparkerrors.NotFoundMissingEntity(fmt.Errorf("referenced entity not found: %w", err))
			}
			return nil, nil, fmt.Errorf("failed to create tree node: %w", err)
		}
		nodes = append(nodes, node)
		if currentElement.node.GetRefundTxSigningJob() != nil {
			if len(cpfpTx.TxOut) == 0 {
				return nil, nil, fmt.Errorf("vout out of bounds for cpfp node tx, need at least one output")
			}
			cpfpRefundOutPoint := wire.OutPoint{Hash: cpfpTx.TxHash(), Index: 0}
			cpfpRefundSigningJob, cpfpRefundTx, err := helper.NewSigningJob(currentElement.keyshare, currentElement.node.GetRefundTxSigningJob(), cpfpTx.TxOut[0])
			if err != nil {
				return nil, nil, err
			}
			if err := validateTreeCreationTxSpendsOutpoint(cpfpRefundTx, cpfpRefundOutPoint, "refund transaction"); err != nil {
				return nil, nil, err
			}
			signingJobs = append(signingJobs, cpfpRefundSigningJob)
			if currentElement.node.GetDirectRefundTxSigningJob() != nil && currentElement.node.GetDirectFromCpfpRefundTxSigningJob() != nil {
				if directTx == nil {
					return nil, nil, errors.New("direct node tx signing job is required when direct refund tx signing jobs are provided")
				}
				if len(directTx.TxOut) == 0 {
					return nil, nil, fmt.Errorf("vout out of bounds for cpfp node tx, need at least one output")
				}
				directRefundOutPoint := wire.OutPoint{Hash: directTx.TxHash(), Index: 0}
				directRefundSigningJob, directRefundTx, err := helper.NewSigningJob(currentElement.keyshare, currentElement.node.GetDirectRefundTxSigningJob(), directTx.TxOut[0])
				if err != nil {
					return nil, nil, err
				}
				if err := validateTreeCreationTxSpendsOutpoint(directRefundTx, directRefundOutPoint, "direct refund transaction"); err != nil {
					return nil, nil, err
				}
				directFromCpfpRefundSigningJob, directFromCpfpRefundTx, err := helper.NewSigningJob(currentElement.keyshare, currentElement.node.GetDirectFromCpfpRefundTxSigningJob(), cpfpTx.TxOut[0])
				if err != nil {
					return nil, nil, err
				}
				if err := validateTreeCreationTxSpendsOutpoint(directFromCpfpRefundTx, cpfpRefundOutPoint, "direct-from-cpfp refund transaction"); err != nil {
					return nil, nil, err
				}
				signingJobs = append(signingJobs, directRefundSigningJob, directFromCpfpRefundSigningJob)
			} else if requireDirectTx {
				return nil, nil, errors.New("directRefundTxSigningJob or DirectFromCpfpRefundTxSigningJob is required. Please upgrade to the latest SDK version")
			}
		} else if len(currentElement.node.GetChildren()) > 0 {
			cpfpOutputs := splitTxOutputsWithoutEphemeralAnchor(cpfpTx)
			if len(cpfpOutputs) != len(currentElement.node.GetChildren()) {
				return nil, nil, fmt.Errorf("node split cpfp tx output count must match split child count, had: %d, needed: %d", len(cpfpOutputs), len(currentElement.node.GetChildren()))
			}
			userPublicKeys, signingKeyshares, err := h.validateTreeCreationSplitOutputs(
				ctx,
				network,
				cpfpOutputs,
				userIDPubKey,
				currentElement.userPubKey,
				currentElement.keyshare.PublicKey,
				"node split cpfp tx",
			)
			if err != nil {
				return nil, nil, err
			}
			for i, child := range currentElement.node.GetChildren() {
				queue = append(queue, &element{
					output:     cpfpOutputs[i],
					outPoint:   wire.OutPoint{Hash: cpfpTx.TxHash(), Index: uint32(i)},
					node:       child,
					userPubKey: userPublicKeys[i],
					keyshare:   signingKeyshares[i],
					parentNode: node,
					vout:       uint32(i),
				})
			}
			if directTx != nil {
				if err := validateTreeCreationDirectSplitOutputs(directTx, cpfpOutputs, "node split direct tx"); err != nil {
					return nil, nil, err
				}
			}
		} else {
			cpfpOutputs := splitTxOutputsWithoutEphemeralAnchor(cpfpTx)
			_, _, err := h.validateTreeCreationSplitOutputs(
				ctx,
				network,
				cpfpOutputs,
				userIDPubKey,
				currentElement.userPubKey,
				currentElement.keyshare.PublicKey,
				"deferred node split cpfp tx",
			)
			if err != nil {
				return nil, nil, err
			}
			if directTx != nil {
				if err := validateTreeCreationDirectSplitOutputs(directTx, cpfpOutputs, "deferred node split direct tx"); err != nil {
					return nil, nil, err
				}
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
		creationNode: req.GetNode(),
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
		if currentElement.creationNode.GetDirectNodeTxSigningJob() != nil {
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

		if currentElement.creationNode.GetRefundTxSigningJob() != nil {
			cpfpSigningResult := signingResults[signingResultIndex]
			signingResultIndex++

			cpfpRefundSigningResultProto, err := cpfpSigningResult.MarshalProto()
			if err != nil {
				return nil, err
			}

			currentElement.node.RefundTxSigningResult = cpfpRefundSigningResultProto

			if currentElement.creationNode.GetDirectRefundTxSigningJob() != nil && currentElement.creationNode.GetDirectFromCpfpRefundTxSigningJob() != nil {
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
		} else if len(currentElement.creationNode.GetChildren()) > 0 {
			children := make([]*pb.CreationResponseNode, len(currentElement.creationNode.GetChildren()))
			for i, child := range currentElement.creationNode.GetChildren() {
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
	if req == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("request is required"))
	}

	reqUserIDPubKey, err := keys.ParsePublicKey(req.GetUserIdentityPublicKey())
	if err != nil {
		return nil, fmt.Errorf("invalid identity public key: %w", err)
	}
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, reqUserIDPubKey); err != nil {
		return nil, err
	}
	if err := authz.EnforceWalletNotKillSwitched(ctx, reqUserIDPubKey); err != nil {
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

	parentNodeID, err := uuid.Parse(parentNodeOutput.GetNodeId())
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
