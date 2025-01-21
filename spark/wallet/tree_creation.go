package wallet

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"log"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrd/dcrec/secp256k1"
	"github.com/lightsparkdev/spark-go"
	"github.com/lightsparkdev/spark-go/common"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	"github.com/lightsparkdev/spark-go/so/objects"
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
) (*DepositAddressTree, error) {
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
	} else if parentTx != nil {
		request.Source = &pb.PrepareTreeAddressRequest_OnChainUtxo{
			OnChainUtxo: &pb.UTXO{
				Txid: parentTx.TxHash().String(),
				Vout: uint32(vout),
			},
		}
	} else {
		return nil, errors.New("no parent node or parent tx provided")
	}

	_, pubkey := secp256k1.PrivKeyFromBytes(parentSigningPrivateKey)
	request.Node = &pb.AddressRequestNode{
		UserPublicKey: pubkey.Serialize(),
		Children:      addressRequestNodes,
	}
	root := &DepositAddressTree{
		Address:           nil,
		SigningPrivateKey: parentSigningPrivateKey,
		VerificationKey:   pubkey.Serialize(),
		Children:          tree,
	}
	response, err := client.PrepareTreeAddress(context.Background(), request)
	if err != nil {
		return nil, err
	}

	applyAddressNodesToTree([]*DepositAddressTree{root}, []*pb.AddressNode{response.Node})

	return root, nil
}

func buildCreationNodesFromTree(parentTx *wire.MsgTx, vout uint32, root *DepositAddressTree, createLeaves bool, network common.Network) (*pb.CreationNode, []*objects.SigningNonce, error) {
	type element struct {
		parentTx     *wire.MsgTx
		vout         uint32
		node         *DepositAddressTree
		creationNode *pb.CreationNode
		leafNode     bool
	}

	rootCreationNode := &pb.CreationNode{}

	elements := []element{}
	elements = append(elements, element{parentTx: parentTx, vout: vout, node: root, creationNode: rootCreationNode, leafNode: false})

	signingNonces := make([]*objects.SigningNonce, 0)

	for len(elements) > 0 {
		currentElement := elements[0]
		elements = elements[1:]

		if currentElement.node.Children != nil {
			shouldAddToQueue := currentElement.node.Children[0].Children != nil || createLeaves

			tx := wire.NewMsgTx(2)
			tx.AddTxIn(wire.NewTxIn(
				&wire.OutPoint{Hash: currentElement.parentTx.TxHash(), Index: currentElement.vout},
				currentElement.parentTx.TxOut[currentElement.vout].PkScript,
				nil, // witness
			))

			childrenArray := make([]*pb.CreationNode, 0)

			for i, child := range currentElement.node.Children {
				childAddress, _ := btcutil.DecodeAddress(*child.Address, common.NetworkParams(network))
				childPkScript, _ := txscript.PayToAddrScript(childAddress)
				tx.AddTxOut(wire.NewTxOut(currentElement.parentTx.TxOut[currentElement.vout].Value, childPkScript))
				if shouldAddToQueue {
					childCreationNode := &pb.CreationNode{}
					childrenArray = append(childrenArray, childCreationNode)
					elements = append(elements, element{parentTx: tx, vout: uint32(i), node: child, creationNode: childCreationNode, leafNode: false})
				}
			}
			if shouldAddToQueue {
				currentElement.creationNode.Children = childrenArray
			}
			var txBuf bytes.Buffer
			tx.Serialize(&txBuf)
			_, pubkey := secp256k1.PrivKeyFromBytes(currentElement.node.SigningPrivateKey)
			log.Printf("node pubkey: %x", hex.EncodeToString(pubkey.Serialize()))
			signingNonce, err := objects.RandomSigningNonce()
			if err != nil {
				return nil, nil, err
			}
			signingNonceCommitment, err := signingNonce.SigningCommitment().MarshalProto()
			if err != nil {
				return nil, nil, err
			}
			signingNonces = append(signingNonces, signingNonce)
			signingJob := &pb.SigningJob{
				SigningPublicKey:       pubkey.Serialize(),
				RawTx:                  txBuf.Bytes(),
				SigningNonceCommitment: signingNonceCommitment,
			}

			currentElement.creationNode.NodeTxSigningJob = signingJob
		} else {
			if currentElement.leafNode {
				tx := wire.NewMsgTx(2)
				sequence := uint32((1 << 30) | spark.InitialTimeLock)
				tx.AddTxIn(&wire.TxIn{
					PreviousOutPoint: wire.OutPoint{Hash: currentElement.parentTx.TxHash(), Index: currentElement.vout},
					SignatureScript:  currentElement.parentTx.TxOut[currentElement.vout].PkScript,
					Witness:          nil,
					Sequence:         sequence,
				})
				tx.AddTxOut(wire.NewTxOut(currentElement.parentTx.TxOut[currentElement.vout].Value, currentElement.parentTx.TxOut[currentElement.vout].PkScript))
				var txBuf bytes.Buffer
				tx.Serialize(&txBuf)

				_, pubkey := secp256k1.PrivKeyFromBytes(currentElement.node.SigningPrivateKey)
				signingNonce, err := objects.RandomSigningNonce()
				if err != nil {
					return nil, nil, err
				}
				signingNonceCommitment, err := signingNonce.SigningCommitment().MarshalProto()
				if err != nil {
					return nil, nil, err
				}
				signingNonces = append(signingNonces, signingNonce)
				signingJob := &pb.SigningJob{
					SigningPublicKey:       pubkey.Serialize(),
					RawTx:                  txBuf.Bytes(),
					SigningNonceCommitment: signingNonceCommitment,
				}
				currentElement.creationNode.NodeTxSigningJob = signingJob

				refundTx := wire.NewMsgTx(2)
				refundTx.AddTxIn(&wire.TxIn{
					PreviousOutPoint: wire.OutPoint{Hash: tx.TxHash(), Index: 0},
					SignatureScript:  tx.TxOut[0].PkScript,
					Witness:          nil,
					Sequence:         sequence,
				})

				refundP2trAddress, _ := common.P2TRAddressFromPublicKey(pubkey.Serialize(), network)
				refundAddress, _ := btcutil.DecodeAddress(*refundP2trAddress, common.NetworkParams(network))
				refundPkScript, _ := txscript.PayToAddrScript(refundAddress)
				refundTx.AddTxOut(wire.NewTxOut(tx.TxOut[0].Value, refundPkScript))
				var refundTxBuf bytes.Buffer
				refundTx.Serialize(&refundTxBuf)
				refundSigningNonce, err := objects.RandomSigningNonce()
				if err != nil {
					return nil, nil, err
				}
				refundSigningNonceCommitment, err := refundSigningNonce.SigningCommitment().MarshalProto()
				if err != nil {
					return nil, nil, err
				}
				signingNonces = append(signingNonces, refundSigningNonce)
				refundSigningJob := &pb.SigningJob{
					SigningPublicKey:       pubkey.Serialize(),
					RawTx:                  refundTxBuf.Bytes(),
					SigningNonceCommitment: refundSigningNonceCommitment,
				}
				currentElement.creationNode.RefundTxSigningJob = refundSigningJob
			} else {
				tx := wire.NewMsgTx(2)
				tx.AddTxIn(wire.NewTxIn(
					&wire.OutPoint{Hash: currentElement.parentTx.TxHash(), Index: currentElement.vout},
					currentElement.parentTx.TxOut[currentElement.vout].PkScript,
					nil, // witness
				))
				tx.AddTxOut(wire.NewTxOut(currentElement.parentTx.TxOut[currentElement.vout].Value, currentElement.parentTx.TxOut[currentElement.vout].PkScript))
				var txBuf bytes.Buffer
				tx.Serialize(&txBuf)

				_, pubkey := secp256k1.PrivKeyFromBytes(currentElement.node.SigningPrivateKey)
				signingNonce, err := objects.RandomSigningNonce()
				if err != nil {
					return nil, nil, err
				}
				signingNonceCommitment, err := signingNonce.SigningCommitment().MarshalProto()
				if err != nil {
					return nil, nil, err
				}
				signingNonces = append(signingNonces, signingNonce)
				signingJob := &pb.SigningJob{
					SigningPublicKey:       pubkey.Serialize(),
					RawTx:                  txBuf.Bytes(),
					SigningNonceCommitment: signingNonceCommitment,
				}
				currentElement.creationNode.NodeTxSigningJob = signingJob
				creationNode := &pb.CreationNode{}
				currentElement.creationNode.Children = []*pb.CreationNode{creationNode}
				elements = append(elements, element{parentTx: tx, vout: 0, node: currentElement.node, creationNode: creationNode, leafNode: true})
			}
		}
	}

	return rootCreationNode, signingNonces, nil
}

// CreateTree creates the tree.
func CreateTree(
	config *Config,
	parentTx *wire.MsgTx,
	parentNode *pb.TreeNode,
	vout uint32,
	root *DepositAddressTree,
	createLeaves bool,
) ([]*pb.TreeNode, error) {
	request := pb.CreateTreeRequest{
		UserIdentityPublicKey: config.IdentityPublicKey(),
	}

	var tx *wire.MsgTx
	if parentTx != nil {
		tx = parentTx
		request.Source = &pb.CreateTreeRequest_OnChainUtxo{
			OnChainUtxo: &pb.UTXO{
				Txid: parentTx.TxHash().String(),
				Vout: uint32(vout),
			},
		}
	} else if parentNode != nil {
		var err error
		tx, err = common.TxFromRawTxBytes(parentNode.NodeTx)
		if err != nil {
			return nil, err
		}
		request.Source = &pb.CreateTreeRequest_ParentNodeOutput{
			ParentNodeOutput: &pb.NodeOutput{
				NodeId: *parentNode.ParentNodeId,
				Vout:   uint32(vout),
			},
		}
	} else {
		return nil, errors.New("no parent tx or parent node provided")
	}

	rootNode, _, err := buildCreationNodesFromTree(tx, vout, root, createLeaves, config.Network)
	if err != nil {
		return nil, err
	}

	request.Node = rootNode

	conn, err := common.NewGRPCConnection(config.CoodinatorAddress())
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	client := pb.NewSparkServiceClient(conn)

	_, err = client.CreateTree(context.Background(), &request)
	if err != nil {
		return nil, err
	}

	return nil, nil
}
