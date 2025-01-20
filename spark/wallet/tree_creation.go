package wallet

import (
	"context"
	"errors"
	"log"

	"github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrd/dcrec/secp256k1"
	"github.com/lightsparkdev/spark-go/common"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
)

// DepositAddressTree is a tree of deposit addresses.
type DepositAddressTree struct {
	// Address is the address of the deposit address.
	Address *string
	// SigningPrivateKey is the private key of the signing key.
	SigningPrivateKey []byte
	// VerificationKey is the public key of the verification key.
	VerificationKey []byte
	// Children is the children of the node.
	Children []*DepositAddressTree
}

func createDepositAddressBinaryTree(
	config *Config,
	splitLevel uint32,
	targetSigningPrivateKey []byte,
) ([]*DepositAddressTree, error) {
	if splitLevel == 0 {
		return nil, nil
	}
	leftKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		return nil, err
	}
	leftKeyBytes := leftKey.Serialize()
	leftNode := &DepositAddressTree{
		Address:           nil,
		SigningPrivateKey: leftKeyBytes,
		VerificationKey:   leftKeyBytes,
		Children:          nil,
	}
	leftNode.Children, err = createDepositAddressBinaryTree(config, splitLevel-1, leftKeyBytes)
	if err != nil {
		log.Printf("failed to create left node: %v", err)
		return nil, err
	}

	rightKeyBytes, err := common.SubtractPrivateKeys(targetSigningPrivateKey, leftKeyBytes)
	if err != nil {
		log.Printf("failed to create right node: %v", err)
		return nil, err
	}
	rightNode := &DepositAddressTree{
		Address:           nil,
		SigningPrivateKey: rightKeyBytes,
		VerificationKey:   rightKeyBytes,
		Children:          nil,
	}
	rightNode.Children, err = createDepositAddressBinaryTree(config, splitLevel-1, rightKeyBytes)
	if err != nil {
		return nil, err
	}
	return []*DepositAddressTree{leftNode, rightNode}, nil
}

func createAddressRequestNodeFromTreeNodes(
	treeNodes []*DepositAddressTree,
) []*pb.AddressRequestNode {
	results := []*pb.AddressRequestNode{}
	for _, node := range treeNodes {
		_, pubkey := secp256k1.PrivKeyFromBytes(node.SigningPrivateKey)
		result := &pb.AddressRequestNode{
			UserPublicKey: pubkey.Serialize(),
			Children:      nil,
		}
		result.Children = createAddressRequestNodeFromTreeNodes(node.Children)
		results = append(results, result)
	}
	return results
}

func applyAddressNodesToTree(
	tree []*DepositAddressTree,
	addressNodes []*pb.AddressNode,
) {
	for i, node := range tree {
		node.Address = &addressNodes[i].Address.Address
		applyAddressNodesToTree(node.Children, addressNodes[i].Children)
	}
}

// GenerateDepositAddressesForTree generates the deposit addresses for the tree.
func GenerateDepositAddressesForTree(
	config *Config,
	parentTx *wire.MsgTx,
	parentNode *pb.TreeNode,
	vout uint32,
	parentSigningPrivateKey []byte,
	splitLevel uint32,
) ([]*DepositAddressTree, error) {
	tree, err := createDepositAddressBinaryTree(config, splitLevel, parentSigningPrivateKey)
	if err != nil {
		return nil, err
	}
	addressRequestNodes := createAddressRequestNodeFromTreeNodes(tree)

	conn, err := common.NewGRPCConnection(config.CoodinatorAddress())
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	client := pb.NewSparkServiceClient(conn)

	request := &pb.PrepareTreeAddressRequest{
		UserIdentityPublicKey: config.IdentityPublicKey(),
	}

	if parentNode != nil {
		request.Source = &pb.PrepareTreeAddressRequest_ParentNodeOutput{
			ParentNodeOutput: &pb.NodeOutput{
				NodeId: *parentNode.ParentNodeId,
				Vout:   uint32(vout),
			},
		}
		request.Nodes = addressRequestNodes
	} else if parentTx != nil {
		request.Source = &pb.PrepareTreeAddressRequest_OnChainUtxo{
			OnChainUtxo: &pb.UTXO{
				Txid: parentTx.TxHash().String(),
				Vout: uint32(vout),
			},
		}
		_, pubkey := secp256k1.PrivKeyFromBytes(parentSigningPrivateKey)
		request.Nodes = []*pb.AddressRequestNode{
			{
				UserPublicKey: pubkey.Serialize(),
				Children:      addressRequestNodes,
			},
		}
		tree = []*DepositAddressTree{
			{
				Address:           nil,
				SigningPrivateKey: parentSigningPrivateKey,
				VerificationKey:   pubkey.Serialize(),
				Children:          tree,
			},
		}
	} else {
		return nil, errors.New("no parent node or parent tx provided")
	}
	response, err := client.PrepareTreeAddress(context.Background(), request)
	if err != nil {
		return nil, err
	}

	log.Printf("response: %v", response)

	applyAddressNodesToTree(tree, response.Nodes)

	return tree, nil
}
