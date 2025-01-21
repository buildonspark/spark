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
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go"
	"github.com/lightsparkdev/spark-go/common"
	pbfrost "github.com/lightsparkdev/spark-go/proto/frost"
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
		node.VerificationKey = addressNodes[i].Address.VerifyingKey
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
		Children:          tree,
	}
	response, err := client.PrepareTreeAddress(context.Background(), request)
	if err != nil {
		return nil, err
	}

	applyAddressNodesToTree([]*DepositAddressTree{root}, []*pb.AddressNode{response.Node})

	return root, nil
}

func buildCreationNodesFromTree(
	parentTx *wire.MsgTx,
	vout uint32,
	root *DepositAddressTree,
	createLeaves bool,
	network common.Network,
) (*pb.CreationNode, []*objects.SigningNonce, error) {
	type element struct {
		parentTx     *wire.MsgTx
		vout         uint32
		node         *DepositAddressTree
		creationNode *pb.CreationNode
		leafNode     bool
	}

	rootCreationNode := &pb.CreationNode{}

	elements := []element{}
	elements = append(elements, element{
		parentTx:     parentTx,
		vout:         vout,
		node:         root,
		creationNode: rootCreationNode,
		leafNode:     false,
	})

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
				tx.AddTxOut(wire.NewTxOut(int64(currentElement.parentTx.TxOut[currentElement.vout].Value)/2, childPkScript))
				if shouldAddToQueue {
					childCreationNode := &pb.CreationNode{}
					childrenArray = append(childrenArray, childCreationNode)
					elements = append(elements, element{
						parentTx:     tx,
						vout:         uint32(i),
						node:         child,
						creationNode: childCreationNode,
						leafNode:     false,
					})
				}
			}
			if shouldAddToQueue {
				currentElement.creationNode.Children = childrenArray
			}
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

func signTreeCreation(
	config *Config,
	tx *wire.MsgTx,
	vout uint32,
	internalTreeRoot *DepositAddressTree,
	requestTreeRoot *pb.CreationNode,
	creationResultTreeRoot *pb.CreationResponseNode,
	signingNonces []*objects.SigningNonce,
) ([]*pb.NodeSignatures, error) {
	signingNonceIndex := 0
	type element struct {
		parentTx             *wire.MsgTx
		vout                 uint32
		internalNode         *DepositAddressTree
		creationNode         *pb.CreationNode
		creationResponseNode *pb.CreationResponseNode
	}
	elements := []element{}
	elements = append(elements, element{
		parentTx:             tx,
		vout:                 vout,
		internalNode:         internalTreeRoot,
		creationNode:         requestTreeRoot,
		creationResponseNode: creationResultTreeRoot,
	})

	conn, err := common.NewGRPCConnection(config.FrostSignerAddress)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	frostClient := pbfrost.NewFrostServiceClient(conn)

	nodeSignatures := []*pb.NodeSignatures{}
	for len(elements) > 0 {
		currentElement := elements[0]
		elements = elements[1:]

		keyPackage := CreateUserKeyPackage(currentElement.internalNode.SigningPrivateKey)
		nodeTx, err := common.TxFromRawTxBytes(currentElement.creationNode.NodeTxSigningJob.RawTx)
		if err != nil {
			return nil, err
		}
		nodeTxSighash, err := common.SigHashFromTx(nodeTx, 0, currentElement.parentTx.TxOut[currentElement.vout])
		if err != nil {
			return nil, err
		}

		signingNonce := signingNonces[signingNonceIndex]
		signingNonceIndex++

		signingNonceCommitment, err := signingNonce.SigningCommitment().MarshalProto()
		if err != nil {
			return nil, err
		}

		signingNonceProto, err := signingNonce.MarshalProto()
		if err != nil {
			return nil, err
		}

		log.Printf("nodeTxSighash: %s", hex.EncodeToString(nodeTxSighash))
		log.Printf("verifying key: %s", hex.EncodeToString(currentElement.internalNode.VerificationKey))
		nodeTxSigningJob := &pbfrost.FrostSigningJob{
			JobId:           uuid.NewString(),
			Message:         nodeTxSighash,
			KeyPackage:      keyPackage,
			VerifyingKey:    currentElement.internalNode.VerificationKey,
			Nonce:           signingNonceProto,
			Commitments:     currentElement.creationResponseNode.NodeTxSigningResult.SigningNonceCommitments,
			UserCommitments: signingNonceCommitment,
		}

		response, err := frostClient.SignFrost(context.Background(), &pbfrost.SignFrostRequest{
			SigningJobs: []*pbfrost.FrostSigningJob{nodeTxSigningJob},
			Role:        pbfrost.SigningRole_USER,
		})
		if err != nil {
			return nil, err
		}

		aggResponse, err := frostClient.AggregateFrost(context.Background(), &pbfrost.AggregateFrostRequest{
			Message:            nodeTxSighash,
			SignatureShares:    currentElement.creationResponseNode.NodeTxSigningResult.SignatureShares,
			PublicShares:       currentElement.creationResponseNode.NodeTxSigningResult.PublicKeys,
			VerifyingKey:       currentElement.internalNode.VerificationKey,
			Commitments:        currentElement.creationResponseNode.NodeTxSigningResult.SigningNonceCommitments,
			UserCommitments:    signingNonceCommitment,
			UserPublicKey:      currentElement.internalNode.VerificationKey,
			UserSignatureShare: response.Results[nodeTxSigningJob.JobId].SignatureShare,
		})
		if err != nil {
			return nil, err
		}

		nodeSignature := &pb.NodeSignatures{
			NodeId:          currentElement.creationResponseNode.NodeId,
			NodeTxSignature: aggResponse.Signature,
		}

		if currentElement.creationResponseNode.RefundTxSigningResult != nil {
			refundTx, err := common.TxFromRawTxBytes(currentElement.creationNode.RefundTxSigningJob.RawTx)
			if err != nil {
				return nil, err
			}
			refundTxSighash, err := common.SigHashFromTx(refundTx, 0, nodeTx.TxOut[0])
			if err != nil {
				return nil, err
			}

			signingNonce = signingNonces[signingNonceIndex]
			signingNonceIndex++

			signingNonceCommitment, err := signingNonce.SigningCommitment().MarshalProto()
			if err != nil {
				return nil, err
			}

			signingNonceProto, err = signingNonce.MarshalProto()
			if err != nil {
				return nil, err
			}

			refundNodeTxSigningJob := &pbfrost.FrostSigningJob{
				JobId:           uuid.NewString(),
				Message:         refundTxSighash,
				KeyPackage:      keyPackage,
				VerifyingKey:    currentElement.internalNode.VerificationKey,
				Nonce:           signingNonceProto,
				Commitments:     currentElement.creationResponseNode.RefundTxSigningResult.SigningNonceCommitments,
				UserCommitments: signingNonceCommitment,
			}

			response, err := frostClient.SignFrost(context.Background(), &pbfrost.SignFrostRequest{
				SigningJobs: []*pbfrost.FrostSigningJob{refundNodeTxSigningJob},
				Role:        pbfrost.SigningRole_USER,
			})
			if err != nil {
				return nil, err
			}

			aggResponse, err := frostClient.AggregateFrost(context.Background(), &pbfrost.AggregateFrostRequest{
				Message:            refundTxSighash,
				SignatureShares:    currentElement.creationResponseNode.RefundTxSigningResult.SignatureShares,
				PublicShares:       currentElement.creationResponseNode.RefundTxSigningResult.PublicKeys,
				VerifyingKey:       currentElement.internalNode.VerificationKey,
				Commitments:        currentElement.creationResponseNode.RefundTxSigningResult.SigningNonceCommitments,
				UserCommitments:    signingNonceCommitment,
				UserPublicKey:      currentElement.internalNode.VerificationKey,
				UserSignatureShare: response.Results[refundNodeTxSigningJob.JobId].SignatureShare,
			})
			if err != nil {
				return nil, err
			}
			nodeSignature.RefundTxSignature = aggResponse.Signature
		}

		nodeSignatures = append(nodeSignatures, nodeSignature)

		for i, child := range currentElement.creationNode.Children {
			var newInternalNode *DepositAddressTree
			if currentElement.internalNode.Children != nil {
				newInternalNode = currentElement.internalNode.Children[i]
			} else {
				newInternalNode = currentElement.internalNode
			}
			elements = append(elements, element{
				parentTx:             nodeTx,
				vout:                 uint32(i),
				internalNode:         newInternalNode,
				creationNode:         child,
				creationResponseNode: currentElement.creationResponseNode.Children[i],
			})
		}
	}

	return nodeSignatures, nil
}

// CreateTree creates the tree.
func CreateTree(
	config *Config,
	parentTx *wire.MsgTx,
	parentNode *pb.TreeNode,
	vout uint32,
	root *DepositAddressTree,
	createLeaves bool,
) (*pb.FinalizeNodeSignaturesResponse, error) {
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

	rootNode, signingNonces, err := buildCreationNodesFromTree(tx, vout, root, createLeaves, config.Network)
	log.Printf("signingNonces count: %d", len(signingNonces))
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

	response, err := client.CreateTree(context.Background(), &request)
	if err != nil {
		return nil, err
	}
	creationResultTreeRoot := response.Node

	nodeSignatures, err := signTreeCreation(config, tx, vout, root, rootNode, creationResultTreeRoot, signingNonces)
	if err != nil {
		return nil, err
	}

	return client.FinalizeNodeSignatures(context.Background(), &pb.FinalizeNodeSignaturesRequest{
		NodeSignatures: nodeSignatures,
	})
}
