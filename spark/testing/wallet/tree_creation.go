package wallet

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log"

	"github.com/lightsparkdev/spark/common/keys"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common"
	pbfrost "github.com/lightsparkdev/spark/proto/frost"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbssp "github.com/lightsparkdev/spark/proto/spark_ssp_internal"
	"github.com/lightsparkdev/spark/so/objects"
)

// DepositAddressTree is a tree of deposit addresses.
type DepositAddressTree struct {
	// Address is the address of the deposit address.
	Address *string
	// SigningPrivateKey is the private key of the signing key.
	SigningPrivateKey keys.Private
	// VerificationKey is the public key of the verification key.
	VerificationKey keys.Public
	// Children is the children of the node.
	Children []*DepositAddressTree
}

func createDepositAddressBinaryTree(
	config *TestWalletConfig,
	splitLevel uint32,
	targetSigningPrivateKey keys.Private,
) ([]*DepositAddressTree, error) {
	if splitLevel == 0 {
		return nil, nil
	}
	leftKey, err := keys.GeneratePrivateKey()
	if err != nil {
		return nil, err
	}
	leftNode := &DepositAddressTree{
		Address:           nil,
		SigningPrivateKey: leftKey,
		Children:          nil,
	}
	leftNode.Children, err = createDepositAddressBinaryTree(config, splitLevel-1, leftKey)
	if err != nil {
		log.Printf("failed to create left node: %v", err)
		return nil, err
	}

	rightKey := targetSigningPrivateKey.Sub(leftKey)
	rightNode := &DepositAddressTree{
		Address:           nil,
		SigningPrivateKey: rightKey,
		Children:          nil,
	}
	rightNode.Children, err = createDepositAddressBinaryTree(config, splitLevel-1, rightKey)
	if err != nil {
		return nil, err
	}
	return []*DepositAddressTree{leftNode, rightNode}, nil
}

func createAddressRequestNodeFromTreeNodes(treeNodes []*DepositAddressTree) []*pb.AddressRequestNode {
	var results []*pb.AddressRequestNode
	for _, node := range treeNodes {
		result := &pb.AddressRequestNode{
			UserPublicKey: node.SigningPrivateKey.Public().Serialize(),
			Children:      createAddressRequestNodeFromTreeNodes(node.Children),
		}
		results = append(results, result)
	}
	return results
}

func applyAddressNodesToTree(
	tree []*DepositAddressTree,
	addressNodes []*pb.AddressNode,
) error {
	for i, node := range tree {
		node.Address = &addressNodes[i].Address.Address
		verifyingKey, err := keys.ParsePublicKey(addressNodes[i].Address.VerifyingKey)
		if err != nil {
			return fmt.Errorf("unable to parse verifying key: %w", err)
		}
		node.VerificationKey = verifyingKey
		if err := applyAddressNodesToTree(node.Children, addressNodes[i].Children); err != nil {
			return err
		}
	}
	return nil
}

// GenerateDepositAddressesForTree generates the deposit addresses for the tree.
func GenerateDepositAddressesForTree(
	ctx context.Context,
	config *TestWalletConfig,
	parentTx *wire.MsgTx,
	parentNode *pb.TreeNode,
	vout uint32,
	parentSigningPrivateKey keys.Private,
	splitLevel uint32,
) (*DepositAddressTree, error) {
	tree, err := createDepositAddressBinaryTree(config, splitLevel, parentSigningPrivateKey)
	if err != nil {
		return nil, err
	}
	addressRequestNodes := createAddressRequestNodeFromTreeNodes(tree)

	conn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to coordinator: %w", err)
	}
	defer conn.Close()

	client := pbssp.NewSparkSspInternalServiceClient(conn)

	request := &pb.PrepareTreeAddressRequest{
		UserIdentityPublicKey: config.IdentityPublicKey().Serialize(),
	}

	if parentNode != nil {
		request.Source = &pb.PrepareTreeAddressRequest_ParentNodeOutput{
			ParentNodeOutput: &pb.NodeOutput{
				NodeId: parentNode.Id,
				Vout:   vout,
			},
		}
	} else if parentTx != nil {
		var bytebuf bytes.Buffer
		err := parentTx.Serialize(&bytebuf)
		if err != nil {
			return nil, err
		}
		request.Source = &pb.PrepareTreeAddressRequest_OnChainUtxo{
			OnChainUtxo: &pb.UTXO{
				Vout:    vout,
				RawTx:   bytebuf.Bytes(),
				Network: config.ProtoNetwork(),
			},
		}
	} else {
		return nil, errors.New("no parent node or parent tx provided")
	}

	request.Node = &pb.AddressRequestNode{
		UserPublicKey: parentSigningPrivateKey.Public().Serialize(),
		Children:      addressRequestNodes,
	}
	root := &DepositAddressTree{
		Address:           nil,
		SigningPrivateKey: parentSigningPrivateKey,
		Children:          tree,
	}
	response, err := client.PrepareTreeAddress(ctx, request)
	if err != nil {
		return nil, err
	}

	if err := applyAddressNodesToTree([]*DepositAddressTree{root}, []*pb.AddressNode{response.Node}); err != nil {
		return nil, err
	}
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

	elements := []element{{
		parentTx:     parentTx,
		vout:         vout,
		node:         root,
		creationNode: rootCreationNode,
		leafNode:     false,
	}}

	var signingNonces []*objects.SigningNonce

	for len(elements) > 0 {
		currentElement := elements[0]
		elements = elements[1:]

		if currentElement.node.Children != nil {
			shouldAddToQueue := currentElement.node.Children[0].Children != nil || createLeaves

			// Form tx
			var childTxOuts []*wire.TxOut
			for _, child := range currentElement.node.Children {
				childAddress, _ := btcutil.DecodeAddress(*child.Address, common.NetworkParams(network))
				childPkScript, _ := txscript.PayToAddrScript(childAddress)
				childTxOut := wire.NewTxOut(currentElement.parentTx.TxOut[currentElement.vout].Value/2, childPkScript)
				childTxOuts = append(childTxOuts, childTxOut)
			}
			parentOutPoint := &wire.OutPoint{Hash: currentElement.parentTx.TxHash(), Index: currentElement.vout}
			tx := createSplitTx(parentOutPoint, childTxOuts)

			// Form children/elements
			var childrenArray []*pb.CreationNode
			var newElements []element
			for i, child := range currentElement.node.Children {
				childCreationNode := &pb.CreationNode{}
				childrenArray = append(childrenArray, childCreationNode)
				newElements = append(newElements, element{
					parentTx:     tx,
					vout:         uint32(i),
					node:         child,
					creationNode: childCreationNode,
					leafNode:     false,
				})
			}
			if shouldAddToQueue {
				currentElement.creationNode.Children = childrenArray
				elements = append(elements, newElements...)
			}

			var txBuf bytes.Buffer
			err := tx.Serialize(&txBuf)
			if err != nil {
				return nil, nil, err
			}
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
				SigningPublicKey:       currentElement.node.SigningPrivateKey.Public().Serialize(),
				RawTx:                  txBuf.Bytes(),
				SigningNonceCommitment: signingNonceCommitment,
			}

			currentElement.creationNode.NodeTxSigningJob = signingJob
		} else {
			if currentElement.leafNode {
				parentOutPoint := wire.OutPoint{Hash: currentElement.parentTx.TxHash(), Index: currentElement.vout}
				parentTxOut := currentElement.parentTx.TxOut[currentElement.vout]
				tx := createLeafNodeTx(
					spark.InitialSequence(),
					&parentOutPoint,
					wire.NewTxOut(parentTxOut.Value, parentTxOut.PkScript),
				)
				var txBuf bytes.Buffer
				err := tx.Serialize(&txBuf)
				if err != nil {
					return nil, nil, err
				}

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
					SigningPublicKey:       currentElement.node.SigningPrivateKey.Public().Serialize(),
					RawTx:                  txBuf.Bytes(),
					SigningNonceCommitment: signingNonceCommitment,
				}
				currentElement.creationNode.NodeTxSigningJob = signingJob

				cpfpRefundTx, _, err := createRefundTxs(spark.InitialSequence(),
					&wire.OutPoint{Hash: tx.TxHash(), Index: 0},
					tx.TxOut[0].Value, currentElement.node.SigningPrivateKey.Public(), true)
				if err != nil {
					return nil, nil, err
				}
				var refundTxBuf bytes.Buffer
				err = cpfpRefundTx.Serialize(&refundTxBuf)
				if err != nil {
					return nil, nil, err
				}
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
					SigningPublicKey:       currentElement.node.SigningPrivateKey.Public().Serialize(),
					RawTx:                  refundTxBuf.Bytes(),
					SigningNonceCommitment: refundSigningNonceCommitment,
				}
				currentElement.creationNode.RefundTxSigningJob = refundSigningJob
			} else {
				parentOutPoint := wire.OutPoint{Hash: currentElement.parentTx.TxHash(), Index: currentElement.vout}
				parentTxOut := currentElement.parentTx.TxOut[currentElement.vout]
				tx := createNodeTx(&parentOutPoint, wire.NewTxOut(parentTxOut.Value, parentTxOut.PkScript))
				var txBuf bytes.Buffer
				err := tx.Serialize(&txBuf)
				if err != nil {
					return nil, nil, err
				}

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
					SigningPublicKey:       currentElement.node.SigningPrivateKey.Public().Serialize(),
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
	config *TestWalletConfig,
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
	elements := []element{{
		parentTx:             tx,
		vout:                 vout,
		internalNode:         internalTreeRoot,
		creationNode:         requestTreeRoot,
		creationResponseNode: creationResultTreeRoot,
	}}

	conn, err := config.NewFrostGRPCConnection()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to frost signer: %w", err)
	}
	defer conn.Close()

	frostClient := pbfrost.NewFrostServiceClient(conn)

	var nodeSignatures []*pb.NodeSignatures
	for len(elements) > 0 {
		currentElement := elements[0]
		elements = elements[1:]

		signingPrivateKey := currentElement.internalNode.SigningPrivateKey
		keyPackage := CreateUserKeyPackage(signingPrivateKey)
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
		log.Printf("verifying key: %s", currentElement.internalNode.VerificationKey.ToHex())
		nodeTxSigningJob := &pbfrost.FrostSigningJob{
			JobId:           uuid.NewString(),
			Message:         nodeTxSighash,
			KeyPackage:      keyPackage,
			VerifyingKey:    currentElement.internalNode.VerificationKey.Serialize(),
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
			VerifyingKey:       currentElement.internalNode.VerificationKey.Serialize(),
			Commitments:        currentElement.creationResponseNode.NodeTxSigningResult.SigningNonceCommitments,
			UserCommitments:    signingNonceCommitment,
			UserPublicKey:      currentElement.internalNode.VerificationKey.Serialize(),
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
				VerifyingKey:    currentElement.internalNode.VerificationKey.Serialize(),
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
				VerifyingKey:       currentElement.internalNode.VerificationKey.Serialize(),
				Commitments:        currentElement.creationResponseNode.RefundTxSigningResult.SigningNonceCommitments,
				UserCommitments:    signingNonceCommitment,
				UserPublicKey:      currentElement.internalNode.VerificationKey.Serialize(),
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
	ctx context.Context,
	config *TestWalletConfig,
	parentTx *wire.MsgTx,
	parentNode *pb.TreeNode,
	vout uint32,
	root *DepositAddressTree,
	createLeaves bool,
) (*pb.FinalizeNodeSignaturesResponse, error) {
	request := pb.CreateTreeRequest{
		UserIdentityPublicKey: config.IdentityPublicKey().Serialize(),
	}

	var tx *wire.MsgTx
	if parentTx != nil {
		tx = parentTx
		var bytebuf bytes.Buffer
		err := parentTx.Serialize(&bytebuf)
		if err != nil {
			return nil, err
		}
		request.Source = &pb.CreateTreeRequest_OnChainUtxo{
			OnChainUtxo: &pb.UTXO{
				Vout:    vout,
				RawTx:   bytebuf.Bytes(),
				Network: config.ProtoNetwork(),
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
				NodeId: parentNode.Id,
				Vout:   vout,
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

	conn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to coordinator: %w", err)
	}
	defer conn.Close()

	sspClient := pbssp.NewSparkSspInternalServiceClient(conn)
	client := pb.NewSparkServiceClient(conn)

	response, err := sspClient.CreateTree(ctx, &request)
	if err != nil {
		return nil, err
	}
	creationResultTreeRoot := response.Node

	nodeSignatures, err := signTreeCreation(config, tx, vout, root, rootNode, creationResultTreeRoot, signingNonces)
	if err != nil {
		return nil, err
	}

	return client.FinalizeNodeSignaturesV2(ctx, &pb.FinalizeNodeSignaturesRequest{
		NodeSignatures: nodeSignatures,
	})
}
