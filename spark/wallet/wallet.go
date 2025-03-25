package wallet

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"log"
	"math"
	"sort"
	"time"

	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go"
	"github.com/lightsparkdev/spark-go/common"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	"github.com/lightsparkdev/spark-go/so/ent/schema"
	"github.com/lightsparkdev/spark-go/so/utils"
	sspapi "github.com/lightsparkdev/spark-go/wallet/ssp_api"
	decodepay "github.com/nbd-wtf/ln-decodepay"
	"google.golang.org/grpc"
)

// SingleKeyWallet is a wallet that uses a single private key for all signing keys.
// This is the most simple type of wallet and for testing purposes only.
type SingleKeyWallet struct {
	Config            *Config
	SigningPrivateKey []byte
	OwnedNodes        []*pb.TreeNode
	OwnedTokenLeaves  []*pb.LeafWithPreviousTransactionData
}

// NewSingleKeyWallet creates a new single key wallet.
func NewSingleKeyWallet(config *Config, signingPrivateKey []byte) *SingleKeyWallet {
	return &SingleKeyWallet{
		Config:            config,
		SigningPrivateKey: signingPrivateKey,
	}
}

func (w *SingleKeyWallet) RemoveOwnedNodes(nodeIDs map[string]bool) {
	newOwnedNodes := make([]*pb.TreeNode, 0)
	for i, node := range w.OwnedNodes {
		if !nodeIDs[node.Id] {
			newOwnedNodes = append(newOwnedNodes, w.OwnedNodes[i])
		}
	}
	w.OwnedNodes = newOwnedNodes
}

func (w *SingleKeyWallet) CreateLightningInvoice(ctx context.Context, amount int64, memo string) (*string, int64, error) {
	identityPublicKey := hex.EncodeToString(w.Config.IdentityPublicKey())
	requester, err := sspapi.NewRequesterWithBaseURL(&identityPublicKey, nil)
	if err != nil {
		return nil, 0, err
	}
	api := sspapi.NewSparkServiceAPI(requester)
	invoice, fees, err := CreateLightningInvoice(ctx, w.Config, api, uint64(amount), memo)
	if err != nil {
		return nil, 0, err
	}
	return invoice, fees, nil
}

func (w *SingleKeyWallet) ClaimAllTransfers(ctx context.Context) ([]*pb.TreeNode, error) {
	pendingTransfers, err := QueryPendingTransfers(ctx, w.Config)
	if err != nil {
		return nil, err
	}

	nodesResult := make([]*pb.TreeNode, 0)
	for _, transfer := range pendingTransfers.Transfers {
		log.Println("Claiming transfer", transfer.Id, transfer.Status)
		if transfer.Status != pb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED &&
			transfer.Status != pb.TransferStatus_TRANSFER_STATUS_RECEIVER_KEY_TWEAKED &&
			transfer.Status != pb.TransferStatus_TRANSFER_STATUSR_RECEIVER_REFUND_SIGNED {
			continue
		}
		leavesMap, err := VerifyPendingTransfer(ctx, w.Config, transfer)
		if err != nil {
			return nil, fmt.Errorf("failed to verify pending transfer: %w", err)
		}
		leaves := make([]LeafKeyTweak, 0, len(transfer.Leaves))
		for _, leaf := range transfer.Leaves {
			leafPrivKey, ok := (*leavesMap)[leaf.Leaf.Id]
			if !ok {
				return nil, fmt.Errorf("leaf %s not found", leaf.Leaf.Id)
			}
			leaves = append(leaves, LeafKeyTweak{
				Leaf:              leaf.Leaf,
				SigningPrivKey:    leafPrivKey,
				NewSigningPrivKey: w.SigningPrivateKey,
			})
		}
		nodes, err := ClaimTransfer(ctx, transfer, w.Config, leaves)
		if err != nil {
			return nil, fmt.Errorf("failed to claim transfer: %w", err)
		}
		nodesResult = append(nodesResult, nodes...)
	}
	w.OwnedNodes = append(w.OwnedNodes, nodesResult...)
	return nodesResult, nil
}

func (w *SingleKeyWallet) leafSelection(targetAmount int64) ([]*pb.TreeNode, error) {
	sort.Slice(w.OwnedNodes, func(i, j int) bool {
		return w.OwnedNodes[i].Value > w.OwnedNodes[j].Value
	})

	amount := int64(0)
	nodes := make([]*pb.TreeNode, 0)
	for _, node := range w.OwnedNodes {
		if targetAmount-amount >= int64(node.Value) {
			amount += int64(node.Value)
			nodes = append(nodes, node)
		}
	}
	if amount == targetAmount {
		return nodes, nil
	}
	return nil, fmt.Errorf("there's no exact match for the target amount")
}

func (w *SingleKeyWallet) leafSelectionForSwap(targetAmount int64) ([]*pb.TreeNode, int64, error) {
	if targetAmount == 0 {
		return nil, 0, fmt.Errorf("target amount is 0")
	}
	sort.Slice(w.OwnedNodes, func(i, j int) bool {
		return w.OwnedNodes[i].Value < w.OwnedNodes[j].Value
	})

	amount := int64(0)
	nodes := make([]*pb.TreeNode, 0)
	for _, node := range w.OwnedNodes {
		if amount < targetAmount {
			amount += int64(node.Value)
			nodes = append(nodes, node)
		}
	}
	if amount >= targetAmount {
		return nodes, amount, nil
	}
	return nil, amount, fmt.Errorf("you don't have enough nodes to swap for the target amount")
}

func (w *SingleKeyWallet) PayInvoice(ctx context.Context, invoice string) (string, error) {
	// TODO: query fee

	bolt11, err := decodepay.Decodepay(invoice)
	if err != nil {
		return "", fmt.Errorf("failed to parse invoice: %w", err)
	}

	amount := math.Ceil(float64(bolt11.MSatoshi) / 1000.0)
	nodes, err := w.leafSelection(int64(amount))
	if err != nil {
		_, err = w.RequestLeavesSwap(ctx, int64(amount))
		if err != nil {
			return "", fmt.Errorf("failed to select nodes: %w", err)
		}
		err = w.SyncWallet(ctx)
		if err != nil {
			return "", fmt.Errorf("failed to sync wallet: %w", err)
		}
		nodes, err = w.leafSelection(int64(amount))
		if err != nil {
			return "", fmt.Errorf("failed to select nodes: %w", err)
		}
	}

	nodeKeyTweaks := make([]LeafKeyTweak, 0, len(nodes))
	nodesToRemove := make(map[string]bool)
	for _, node := range nodes {
		newLeafPrivKey, err := secp256k1.GeneratePrivateKey()
		if err != nil {
			return "", fmt.Errorf("failed to generate new leaf private key: %w", err)
		}
		nodeKeyTweaks = append(nodeKeyTweaks, LeafKeyTweak{
			Leaf:              node,
			SigningPrivKey:    w.SigningPrivateKey,
			NewSigningPrivKey: newLeafPrivKey.Serialize(),
		})
		nodesToRemove[node.Id] = true
	}

	paymentHash, err := hex.DecodeString(bolt11.PaymentHash)
	if err != nil {
		return "", fmt.Errorf("failed to decode payment hash: %w", err)
	}

	resp, err := SwapNodesForPreimage(ctx, w.Config, nodeKeyTweaks, w.Config.SparkServiceProviderIdentityPublicKey, paymentHash, &invoice, 0, false)
	if err != nil {
		return "", fmt.Errorf("failed to swap nodes for preimage: %w", err)
	}

	_, err = SendTransferTweakKey(ctx, w.Config, resp.Transfer, nodeKeyTweaks, nil)
	if err != nil {
		return "", fmt.Errorf("failed to send transfer: %w", err)
	}

	identityPublicKey := hex.EncodeToString(w.Config.IdentityPublicKey())
	requester, err := sspapi.NewRequesterWithBaseURL(&identityPublicKey, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create requester: %w", err)
	}
	api := sspapi.NewSparkServiceAPI(requester)

	requestID, err := api.PayInvoice(invoice)
	if err != nil {
		return "", fmt.Errorf("failed to pay invoice: %w", err)
	}

	w.RemoveOwnedNodes(nodesToRemove)
	return requestID, nil
}

func (w *SingleKeyWallet) grpcClient(ctx context.Context) (context.Context, *pb.SparkServiceClient, *grpc.ClientConn, error) {
	conn, err := common.NewGRPCConnectionWithTestTLS(w.Config.CoodinatorAddress(), nil)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to connect to operator: %w", err)
	}

	token, err := AuthenticateWithConnection(ctx, w.Config, conn)
	if err != nil {
		return nil, nil, conn, fmt.Errorf("failed to authenticate: %w", err)
	}
	ctx = ContextWithToken(ctx, token)

	client := pb.NewSparkServiceClient(conn)
	return ctx, &client, conn, nil
}

func (w *SingleKeyWallet) SyncWallet(ctx context.Context) error {
	ctx, client, conn, err := w.grpcClient(ctx)
	if err != nil {
		return fmt.Errorf("failed to create grpc client: %w", err)
	}
	defer conn.Close()

	response, err := (*client).QueryNodes(ctx, &pb.QueryNodesRequest{
		Source:         &pb.QueryNodesRequest_OwnerIdentityPubkey{OwnerIdentityPubkey: w.Config.IdentityPublicKey()},
		IncludeParents: true,
	})
	if err != nil {
		return fmt.Errorf("failed to get owned nodes: %w", err)
	}
	ownedNodes := make([]*pb.TreeNode, 0)
	for _, node := range response.Nodes {
		if node.Status == string(schema.TreeNodeStatusAvailable) {
			ownedNodes = append(ownedNodes, node)
		}
	}
	w.OwnedNodes = ownedNodes
	return nil
}

func (w *SingleKeyWallet) OptimizeLeaves(ctx context.Context) error {
	balance := uint64(0)
	for _, node := range w.OwnedNodes {
		balance += node.Value
	}
	if balance > 0 {
		_, err := w.RequestLeavesSwap(ctx, int64(balance))
		return err
	}
	return nil
}

func (w *SingleKeyWallet) RequestLeavesSwap(ctx context.Context, targetAmount int64) ([]*pb.TreeNode, error) {
	// Claim all transfers to get the latest leaves
	_, err := w.ClaimAllTransfers(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to claim all transfers: %w", err)
	}

	nodes, totalAmount, err := w.leafSelectionForSwap(targetAmount)
	if err != nil {
		return nil, fmt.Errorf("failed to select nodes: %w", err)
	}

	leafKeyTweaks := make([]LeafKeyTweak, 0, len(nodes))
	nodesToRemove := make(map[string]bool)
	for _, node := range nodes {
		newLeafPrivKey, err := secp256k1.GeneratePrivateKey()
		if err != nil {
			return nil, fmt.Errorf("failed to generate new leaf private key: %w", err)
		}
		leafKeyTweaks = append(leafKeyTweaks, LeafKeyTweak{
			Leaf:              node,
			SigningPrivKey:    w.SigningPrivateKey,
			NewSigningPrivKey: newLeafPrivKey.Serialize(),
		})
		nodesToRemove[node.Id] = true
	}

	// Get signature for refunds (normal flow)
	transfer, refundSignatureMap, _, err := SendTransferSignRefund(
		ctx,
		w.Config,
		leafKeyTweaks[:],
		w.Config.SparkServiceProviderIdentityPublicKey,
		time.Now().Add(10*time.Minute),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to send transfer sign refund: %w", err)
	}

	// This signature needs to be sent to the SSP.
	adaptorSignature, adaptorPrivKeyBytes, err := common.GenerateAdaptorFromSignature(refundSignatureMap[transfer.Leaves[0].Leaf.Id])
	if err != nil {
		return nil, fmt.Errorf("failed to generate adaptor: %w", err)
	}

	userLeaves := make([]sspapi.SwapLeaf, 0, len(nodes))
	userLeaves = append(userLeaves, sspapi.SwapLeaf{
		LeafID:                       transfer.Leaves[0].Leaf.Id,
		RawUnsignedRefundTransaction: hex.EncodeToString(transfer.Leaves[0].IntermediateRefundTx),
		AdaptorAddedSignature:        hex.EncodeToString(adaptorSignature),
	})

	for i, leaf := range transfer.Leaves {
		if i == 0 {
			continue
		}
		signature, err := common.GenerateSignatureFromExistingAdaptor(refundSignatureMap[leaf.Leaf.Id], adaptorPrivKeyBytes)
		if err != nil {
			return nil, fmt.Errorf("failed to generate signature: %w", err)
		}
		userLeaves = append(userLeaves, sspapi.SwapLeaf{
			LeafID:                       leaf.Leaf.Id,
			RawUnsignedRefundTransaction: hex.EncodeToString(leaf.IntermediateRefundTx),
			AdaptorAddedSignature:        hex.EncodeToString(signature),
		})
	}

	adaptorPrivateKey := secp256k1.PrivKeyFromBytes(adaptorPrivKeyBytes)
	adaptorPubKey := adaptorPrivateKey.PubKey()

	identityPublicKey := hex.EncodeToString(w.Config.IdentityPublicKey())
	requester, err := sspapi.NewRequesterWithBaseURL(&identityPublicKey, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create requester: %w", err)
	}
	api := sspapi.NewSparkServiceAPI(requester)

	requestID, leaves, err := api.RequestLeavesSwap(hex.EncodeToString(adaptorPubKey.SerializeCompressed()), uint64(totalAmount), uint64(targetAmount), 0, userLeaves)
	if err != nil {
		_, cancelErr := CancelSendTransfer(ctx, w.Config, transfer)
		if cancelErr != nil {
			return nil, fmt.Errorf("failed to cancel send transfer: %w", cancelErr)
		}
		fmt.Printf("cancelled send transfer %s\n", transfer.Id)
		return nil, fmt.Errorf("failed to request leaves swap: %w", err)
	}

	ctx, grpcClient, conn, err := w.grpcClient(ctx)
	defer conn.Close()
	if err != nil {
		return nil, fmt.Errorf("failed to create grpc client: %w", err)
	}

	for _, leaf := range leaves {
		response, err := (*grpcClient).QueryNodes(ctx, &pb.QueryNodesRequest{
			Source: &pb.QueryNodesRequest_NodeIds{
				NodeIds: &pb.TreeNodeIds{
					NodeIds: []string{leaf.LeafID},
				},
			},
		})
		if err != nil {
			return nil, fmt.Errorf("failed to query nodes: %w", err)
		}
		if len(response.Nodes) != 1 {
			return nil, fmt.Errorf("expected 1 node, got %d", len(response.Nodes))
		}
		nodeTx, err := common.TxFromRawTxBytes(response.Nodes[leaf.LeafID].NodeTx)
		if err != nil {
			return nil, fmt.Errorf("failed to get node tx: %w", err)
		}
		refundTxBytes, err := hex.DecodeString(leaf.RawUnsignedRefundTransaction)
		if err != nil {
			return nil, fmt.Errorf("failed to decode refund tx: %w", err)
		}
		refundTx, err := common.TxFromRawTxBytes(refundTxBytes)
		if err != nil {
			return nil, fmt.Errorf("failed to get refund tx: %w", err)
		}
		sighash, err := common.SigHashFromTx(refundTx, 0, nodeTx.TxOut[0])
		if err != nil {
			return nil, fmt.Errorf("failed to get sighash: %w", err)
		}

		nodePublicKey, err := secp256k1.ParsePubKey(response.Nodes[leaf.LeafID].VerifyingPublicKey)
		if err != nil {
			return nil, fmt.Errorf("failed to parse node public key: %w", err)
		}
		taprootKey := txscript.ComputeTaprootKeyNoScript(nodePublicKey)
		adaptorSignatureBytes, err := hex.DecodeString(leaf.AdaptorAddedSignature)
		if err != nil {
			return nil, fmt.Errorf("failed to decode adaptor signature: %w", err)
		}
		_, err = common.ApplyAdaptorToSignature(taprootKey, sighash, adaptorSignatureBytes, adaptorPrivKeyBytes)
		if err != nil {
			return nil, fmt.Errorf("failed to apply adaptor to signature: %w", err)
		}
	}

	// send the transfer
	_, err = SendTransferTweakKey(ctx, w.Config, transfer, leafKeyTweaks, refundSignatureMap)
	if err != nil {
		return nil, fmt.Errorf("failed to send transfer: %w", err)
	}

	_, err = api.CompleteLeavesSwap(hex.EncodeToString(adaptorPrivKeyBytes), transfer.Id, requestID)
	if err != nil {
		return nil, fmt.Errorf("failed to complete leaves swap: %w", err)
	}

	claimedNodes, err := w.ClaimAllTransfers(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to claim all transfers: %w", err)
	}

	amountClaimed := int64(0)
	for _, node := range claimedNodes {
		amountClaimed += int64(node.Value)
	}

	// TODO: accomodate for fees
	if amountClaimed != totalAmount {
		return nil, fmt.Errorf("amount claimed is not equal to the total amount")
	}

	w.RemoveOwnedNodes(nodesToRemove)
	w.OwnedNodes = append(w.OwnedNodes, claimedNodes...)
	return claimedNodes, nil
}

func (w *SingleKeyWallet) SendTransfer(ctx context.Context, receiverIdentityPubkey []byte, targetAmount int64) (*pb.Transfer, error) {
	nodes, err := w.leafSelection(targetAmount)
	if err != nil {
		_, err = w.RequestLeavesSwap(ctx, int64(targetAmount))
		if err != nil {
			return nil, fmt.Errorf("failed to select nodes: %w", err)
		}
		nodes, err = w.leafSelection(int64(targetAmount))
		if err != nil {
			return nil, fmt.Errorf("failed to select nodes: %w", err)
		}
	}

	leafKeyTweaks := make([]LeafKeyTweak, 0, len(nodes))
	nodesToRemove := make(map[string]bool)
	for _, node := range nodes {
		newLeafPrivKey, err := secp256k1.GeneratePrivateKey()
		if err != nil {
			return nil, fmt.Errorf("failed to generate new leaf private key: %w", err)
		}
		leafKeyTweaks = append(leafKeyTweaks, LeafKeyTweak{
			Leaf:              node,
			SigningPrivKey:    w.SigningPrivateKey,
			NewSigningPrivKey: newLeafPrivKey.Serialize(),
		})
		nodesToRemove[node.Id] = true
	}

	transfer, err := SendTransfer(ctx, w.Config, leafKeyTweaks, receiverIdentityPubkey, time.Unix(0, 0))
	if err != nil {
		return nil, fmt.Errorf("failed to send transfer: %w", err)
	}

	w.RemoveOwnedNodes(nodesToRemove)
	return transfer, nil
}

func (w *SingleKeyWallet) CoopExit(ctx context.Context, targetAmountSats int64, onchainAddress string) (*pb.Transfer, error) {
	// Prepare leaves to send
	nodes, err := w.leafSelection(targetAmountSats)
	if err != nil {
		return nil, fmt.Errorf("failed to select nodes: %w", err)
	}

	leafIDs := make([]string, 0, len(nodes))
	leafKeyTweaks := make([]LeafKeyTweak, 0, len(nodes))
	nodesToRemove := make(map[string]bool)
	for _, node := range nodes {
		newLeafPrivKey, err := secp256k1.GeneratePrivateKey()
		if err != nil {
			return nil, fmt.Errorf("failed to generate new leaf private key: %w", err)
		}
		leafKeyTweaks = append(leafKeyTweaks, LeafKeyTweak{
			Leaf:              node,
			SigningPrivKey:    w.SigningPrivateKey,
			NewSigningPrivKey: newLeafPrivKey.Serialize(),
		})
		nodesToRemove[node.Id] = true
		leafIDs = append(leafIDs, node.Id)
	}

	// Get tx from SSP
	identityPublicKey := hex.EncodeToString(w.Config.IdentityPublicKey())
	requester, err := sspapi.NewRequesterWithBaseURL(&identityPublicKey, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create requester: %w", err)
	}
	api := sspapi.NewSparkServiceAPI(requester)
	coopExitID, coopExitTxid, connectorTx, err := api.InitiateCoopExit(leafIDs, onchainAddress)
	if err != nil {
		return nil, fmt.Errorf("failed to initiate coop exit: %w", err)
	}
	connectorOutputs := make([]*wire.OutPoint, 0)
	connectorTxid := connectorTx.TxHash()
	for i := range connectorTx.TxOut[:len(connectorTx.TxOut)-1] {
		connectorOutputs = append(connectorOutputs, wire.NewOutPoint(&connectorTxid, uint32(i)))
	}

	// Get refund signatures and send tweak
	sspPubIdentityKey, err := secp256k1.ParsePubKey(w.Config.SparkServiceProviderIdentityPublicKey)
	if err != nil {
		return nil, fmt.Errorf("failed to parse ssp pubkey: %w", err)
	}

	transfer, _, err := GetConnectorRefundSignatures(ctx, w.Config, leafKeyTweaks, coopExitTxid, connectorOutputs, sspPubIdentityKey)
	if err != nil {
		return nil, fmt.Errorf("failed to get connector refund signatures: %w", err)
	}

	completeID, err := api.CompleteCoopExit(transfer.Id, coopExitID)
	if err != nil {
		return nil, fmt.Errorf("failed to complete coop exit: %w", err)
	}
	fmt.Printf("Coop exit completed with id %s\n", completeID)

	w.RemoveOwnedNodes(nodesToRemove)
	return transfer, nil
}

func (w *SingleKeyWallet) RefreshTimelocks(ctx context.Context, nodeUUID *uuid.UUID) error {
	nodesToRefresh := make([]*pb.TreeNode, 0)
	nodeIDs := make([]string, 0)

	if nodeUUID != nil {
		for _, node := range w.OwnedNodes {
			if node.Id == nodeUUID.String() {
				nodesToRefresh = append(nodesToRefresh, node)
				nodeIDs = append(nodeIDs, node.Id)
				break
			}
		}
		if len(nodesToRefresh) == 0 {
			return fmt.Errorf("node %s not found", nodeUUID.String())
		}
	} else {
		for _, node := range w.OwnedNodes {
			refundTx, err := common.TxFromRawTxBytes(node.RefundTx)
			if err != nil {
				return fmt.Errorf("failed to parse refund tx: %v", err)
			}
			_, err = spark.NextSequence(refundTx.TxIn[0].Sequence)
			needRefresh := err != nil
			if err != nil {
				return fmt.Errorf("failed to check if node needs to be refreshed: %w", err)
			}
			if needRefresh {
				nodesToRefresh = append(nodesToRefresh, node)
				nodeIDs = append(nodeIDs, node.Id)
			}
		}
	}
	fmt.Printf("Refreshing %d nodes\n", len(nodesToRefresh))

	authCtx, client, conn, err := w.grpcClient(ctx)
	if err != nil {
		return fmt.Errorf("failed to create grpc client: %w", err)
	}
	defer conn.Close()

	nodesResp, err := (*client).QueryNodes(authCtx, &pb.QueryNodesRequest{
		Source: &pb.QueryNodesRequest_NodeIds{
			NodeIds: &pb.TreeNodeIds{
				NodeIds: nodeIDs,
			},
		},
		IncludeParents: true,
	})
	if err != nil {
		return fmt.Errorf("failed to query nodes: %w", err)
	}

	nodesMap := make(map[string]*pb.TreeNode)
	for _, node := range nodesResp.Nodes {
		nodesMap[node.Id] = node
	}

	for _, node := range nodesToRefresh {
		fmt.Printf("Refreshing node %s\n", node.Id)
		// Get the parent node
		parentNode, ok := nodesMap[*node.ParentNodeId]
		if !ok {
			return fmt.Errorf("parent node %s not found", *node.ParentNodeId)
		}
		signingPrivKey := secp256k1.PrivKeyFromBytes(w.SigningPrivateKey)
		nodes, err := RefreshTimelockNodes(
			ctx, w.Config, []*pb.TreeNode{node}, parentNode, signingPrivKey)
		if err != nil {
			return fmt.Errorf("failed to refresh timelock nodes: %w", err)
		}
		// We only expect to refresh leaf nodes, not chains of nodes right now
		if len(nodes) != 1 {
			return fmt.Errorf("expected 1 nodes, got %d", len(nodes))
		}
		newNode := nodes[0]
		w.RemoveOwnedNodes(map[string]bool{node.Id: true})
		w.OwnedNodes = append(w.OwnedNodes, newNode)
	}

	return nil
}

// For simplicity always mint directly to the issuer wallet (eg. owner == token public key)
func (w *SingleKeyWallet) MintTokens(ctx context.Context, amount uint64) error {
	conn, err := common.NewGRPCConnectionWithTestTLS(w.Config.CoodinatorAddress(), nil)
	if err != nil {
		return fmt.Errorf("failed to connect to operator: %w", err)
	}
	defer conn.Close()

	token, err := AuthenticateWithConnection(ctx, w.Config, conn)
	if err != nil {
		return fmt.Errorf("failed to authenticate: %w", err)
	}
	ctx = ContextWithToken(ctx, token)

	tokenIdentityPubKeyBytes := w.Config.IdentityPublicKey()
	mintTransaction := &pb.TokenTransaction{
		TokenInput: &pb.TokenTransaction_MintInput{
			MintInput: &pb.MintInput{
				IssuerPublicKey:         tokenIdentityPubKeyBytes,
				IssuerProvidedTimestamp: uint64(time.Now().UnixMilli()),
			},
		},
		OutputLeaves: []*pb.TokenLeafOutput{
			{
				OwnerPublicKey: tokenIdentityPubKeyBytes,
				TokenPublicKey: tokenIdentityPubKeyBytes,       // Using user pubkey as token ID for this example
				TokenAmount:    int64ToUint128Bytes(0, amount), // high bits = 0, low bits = 99999
			},
		},
	}
	finalTokenTransaction, err := BroadcastTokenTransaction(ctx, w.Config, mintTransaction,
		[]*secp256k1.PrivateKey{&w.Config.IdentityPrivateKey},
		nil,
	)
	if err != nil {
		return fmt.Errorf("failed to broadcast mint transaction: %w", err)
	}
	newOwnedLeaves, err := getOwnedLeavesFromTokenTransaction(finalTokenTransaction, w.Config.IdentityPublicKey())
	if err != nil {
		return fmt.Errorf("failed to add owned leaves: %w", err)
	}
	w.OwnedTokenLeaves = append(w.OwnedTokenLeaves, newOwnedLeaves...)
	return nil
}

// TransferTokens transfers tokens to a receiver. If tokenPublicKey is nil, the wallet's identity public key is used.
func (w *SingleKeyWallet) TransferTokens(ctx context.Context, amount uint64, receiverPubKey []byte, tokenPublicKey []byte) error {
	conn, err := common.NewGRPCConnectionWithTestTLS(w.Config.CoodinatorAddress(), nil)
	if err != nil {
		return fmt.Errorf("failed to connect to operator: %w", err)
	}
	defer conn.Close()

	token, err := AuthenticateWithConnection(ctx, w.Config, conn)
	if err != nil {
		return fmt.Errorf("failed to authenticate: %w", err)
	}
	ctx = ContextWithToken(ctx, token)

	// If no token public key specified, use wallet's identity public key
	if tokenPublicKey == nil {
		tokenPublicKey = w.Config.IdentityPublicKey()
	}

	selectedLeavesWithPrevTxData, selectedLeavesAmount, err := selectTokenLeaves(ctx, w.Config, amount, tokenPublicKey, w.Config.IdentityPublicKey())
	if err != nil {
		return fmt.Errorf("failed to select token leaves: %w", err)
	}

	leavesToSpend := make([]*pb.TokenLeafToSpend, len(selectedLeavesWithPrevTxData))
	revocationPublicKeys := make([][]byte, len(selectedLeavesWithPrevTxData))
	leavesToSpendPrivateKeys := make([]*secp256k1.PrivateKey, len(selectedLeavesWithPrevTxData))
	for i, leaf := range selectedLeavesWithPrevTxData {
		leavesToSpend[i] = &pb.TokenLeafToSpend{
			PrevTokenTransactionHash:     leaf.GetPreviousTransactionHash(),
			PrevTokenTransactionLeafVout: leaf.GetPreviousTransactionVout(),
		}
		revocationPublicKeys[i] = leaf.Leaf.RevocationPublicKey
		// Assume all leaves to spend are owned by the wallet.
		leavesToSpendPrivateKeys[i] = &w.Config.IdentityPrivateKey
	}

	transferTransaction := &pb.TokenTransaction{
		TokenInput: &pb.TokenTransaction_TransferInput{
			TransferInput: &pb.TransferInput{
				LeavesToSpend: leavesToSpend,
			},
		},
		OutputLeaves: []*pb.TokenLeafOutput{
			{
				OwnerPublicKey: receiverPubKey,
				TokenPublicKey: tokenPublicKey,
				TokenAmount:    int64ToUint128Bytes(0, uint64(amount)),
			},
		},
	}

	// Send the remainder back to our wallet with an additional output if necessary.
	if selectedLeavesAmount > amount {
		remainder := selectedLeavesAmount - amount
		changeOutput := &pb.TokenLeafOutput{
			OwnerPublicKey: w.Config.IdentityPublicKey(),
			TokenPublicKey: tokenPublicKey,
			TokenAmount:    int64ToUint128Bytes(0, remainder),
		}
		transferTransaction.OutputLeaves = append(transferTransaction.OutputLeaves, changeOutput)
	}

	finalTokenTransaction, err := BroadcastTokenTransaction(ctx, w.Config, transferTransaction, leavesToSpendPrivateKeys,
		revocationPublicKeys,
	)
	if err != nil {
		return fmt.Errorf("failed to broadcast transfer transaction: %w", err)
	}
	// Remove the spent leaves from the owned leaves list.
	spentLeafMap := make(map[string]bool)
	j := 0
	for _, leaf := range selectedLeavesWithPrevTxData {
		spentLeafMap[getLeafWithPrevTxKey(leaf)] = true
	}
	for i := range w.OwnedTokenLeaves {
		if !spentLeafMap[getLeafWithPrevTxKey(w.OwnedTokenLeaves[i])] {
			w.OwnedTokenLeaves[j] = w.OwnedTokenLeaves[i]
			j++
		}
	}
	w.OwnedTokenLeaves = w.OwnedTokenLeaves[:j]

	// Add the created leaves to the owned leaves list.
	newOwnedLeaves, err := getOwnedLeavesFromTokenTransaction(finalTokenTransaction, w.Config.IdentityPublicKey())
	if err != nil {
		return fmt.Errorf("failed to add owned leaves: %w", err)
	}
	w.OwnedTokenLeaves = append(w.OwnedTokenLeaves, newOwnedLeaves...)

	return nil
}

// TokenBalance represents the balance for a specific token
type TokenBalance struct {
	NumLeaves   int
	TotalAmount uint64
}

func (w *SingleKeyWallet) GetAllTokenBalances(ctx context.Context) (map[string]TokenBalance, error) {
	// Get all token leaves owned by the wallet
	response, err := GetOwnedTokenLeaves(
		ctx,
		w.Config,
		[][]byte{w.Config.IdentityPublicKey()},
		nil, // nil to get all tokens
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get owned token leaves: %w", err)
	}

	// Group leaves by token public key and calculate totals
	balances := make(map[string]TokenBalance)
	for _, leaf := range response.LeavesWithPreviousTransactionData {
		tokenPubKey := leaf.Leaf.TokenPublicKey
		balance := balances[hex.EncodeToString(tokenPubKey)]

		_, amount, err := uint128BytesToInt64(leaf.Leaf.TokenAmount)
		if err != nil {
			return nil, fmt.Errorf("invalid token amount in leaf: %w", err)
		}

		balance.NumLeaves++
		balance.TotalAmount += amount
		balances[hex.EncodeToString(tokenPubKey)] = balance
	}

	return balances, nil
}

func (w *SingleKeyWallet) GetTokenBalance(ctx context.Context, tokenPublicKey []byte) (int, uint64, error) {
	// Claim all transfers first to ensure we have the latest state
	_, err := w.ClaimAllTransfers(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to claim all transfers: %w", err)
	}

	// Call the GetOwnedTokenLeaves function with the wallet's identity public key
	response, err := GetOwnedTokenLeaves(
		ctx,
		w.Config,
		[][]byte{w.Config.IdentityPublicKey()},
		[][]byte{tokenPublicKey},
	)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to get owned token leaves: %w", err)
	}

	// Calculate total amount across all leaves
	totalAmount := uint64(0)
	for _, leaf := range response.LeavesWithPreviousTransactionData {
		_, amount, err := uint128BytesToInt64(leaf.Leaf.TokenAmount)
		if err != nil {
			return 0, 0, fmt.Errorf("invalid token amount in leaf: %w", err)
		}
		totalAmount += amount
	}

	return len(response.LeavesWithPreviousTransactionData), totalAmount, nil
}

func selectTokenLeaves(ctx context.Context, config *Config, targetAmount uint64, tokenPublicKey []byte, ownerPublicKey []byte) ([]*pb.LeafWithPreviousTransactionData, uint64, error) {
	// Fetch owned token leaves
	ownedLeavesResponse, err := GetOwnedTokenLeaves(ctx, config, [][]byte{ownerPublicKey}, [][]byte{tokenPublicKey})
	if err != nil {
		return nil, 0, fmt.Errorf("failed to get owned token leaves: %w", err)
	}
	leavesWithPrevTxData := ownedLeavesResponse.LeavesWithPreviousTransactionData

	getTokenAmount := func(leaf *pb.LeafWithPreviousTransactionData) (uint64, error) {
		_, amount, err := uint128BytesToInt64(leaf.Leaf.TokenAmount)
		return amount, err
	}

	// Sort to spend smallest leaves first to proactively reduce withdrawal cost.
	sort.Slice(leavesWithPrevTxData, func(i, j int) bool {
		iAmount, _ := getTokenAmount(leavesWithPrevTxData[i])
		jAmount, _ := getTokenAmount(leavesWithPrevTxData[j])
		return iAmount < jAmount
	})

	selectedLeavesAmount := uint64(0)
	selectedLeaves := make([]*pb.LeafWithPreviousTransactionData, 0)
	for _, leaf := range leavesWithPrevTxData {
		leafTokenAmount, err := getTokenAmount(leaf)
		if err != nil {
			return nil, 0, fmt.Errorf("invalid token amount in leaf: %w", err)
		}
		selectedLeavesAmount += uint64(leafTokenAmount)
		selectedLeaves = append(selectedLeaves, leaf)
		if selectedLeavesAmount >= targetAmount {
			break
		}
	}

	if selectedLeavesAmount < targetAmount {
		return nil, 0, fmt.Errorf("insufficient tokens: have %d, need %d", selectedLeavesAmount, targetAmount)
	}
	return selectedLeaves, selectedLeavesAmount, nil
}

func uint128BytesToInt64(bytes []byte) (high uint64, low uint64, err error) {
	if len(bytes) != 16 {
		return 0, 0, fmt.Errorf("invalid uint128 bytes length: expected 16, got %d", len(bytes))
	}
	high = binary.BigEndian.Uint64(bytes[:8])
	low = binary.BigEndian.Uint64(bytes[8:])
	return high, low, nil
}

func int64ToUint128Bytes(high, low uint64) []byte {
	return append(
		binary.BigEndian.AppendUint64(make([]byte, 0), high),
		binary.BigEndian.AppendUint64(make([]byte, 0), low)...,
	)
}

func getOwnedLeavesFromTokenTransaction(leaf *pb.TokenTransaction, walletPublicKey []byte) ([]*pb.LeafWithPreviousTransactionData, error) {
	finalTokenTransactionHash, err := utils.HashTokenTransaction(leaf, false)
	if err != nil {
		return nil, err
	}
	newLeavesToSpend := make([]*pb.LeafWithPreviousTransactionData, 0)
	for i, leaf := range leaf.OutputLeaves {
		if bytes.Equal(leaf.OwnerPublicKey, walletPublicKey) {
			leafWithPrevTxData := &pb.LeafWithPreviousTransactionData{
				Leaf: &pb.TokenLeafOutput{
					OwnerPublicKey:      leaf.OwnerPublicKey,
					RevocationPublicKey: leaf.RevocationPublicKey,
					TokenPublicKey:      leaf.TokenPublicKey,
					TokenAmount:         leaf.TokenAmount,
				},
				PreviousTransactionHash: finalTokenTransactionHash,
				PreviousTransactionVout: uint32(i),
			}
			newLeavesToSpend = append(newLeavesToSpend, leafWithPrevTxData)
		}
	}
	return newLeavesToSpend, nil
}

func getLeafWithPrevTxKey(leaf *pb.LeafWithPreviousTransactionData) string {
	txHashStr := hex.EncodeToString(leaf.GetPreviousTransactionHash())
	return txHashStr + ":" + fmt.Sprintf("%d", leaf.GetPreviousTransactionVout())
}

// FreezeTokens freezes all tokens owned by a specific owner public key.
func (w *SingleKeyWallet) FreezeTokens(ctx context.Context, ownerPublicKey []byte) ([]string, uint64, error) {
	// For simplicity, we're using the wallet's identity public key as the token public key
	tokenPublicKey := w.Config.IdentityPublicKey()
	response, err := FreezeTokens(ctx, w.Config, ownerPublicKey, tokenPublicKey, false)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to freeze tokens: %w", err)
	}

	// Convert token amount from uint128 bytes to uint64
	_, amount, err := uint128BytesToInt64(response.ImpactedTokenAmount)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to convert token amount: %w", err)
	}

	return response.ImpactedLeafIds, amount, nil
}

// UnfreezeTokens unfreezes all tokens owned by a specific owner public key.
func (w *SingleKeyWallet) UnfreezeTokens(ctx context.Context, ownerPublicKey []byte) ([]string, uint64, error) {
	// For simplicity, we're using the wallet's identity public key as the token public key
	tokenPublicKey := w.Config.IdentityPublicKey()
	response, err := FreezeTokens(ctx, w.Config, ownerPublicKey, tokenPublicKey, true)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to unfreeze tokens: %w", err)
	}

	// Convert token amount from uint128 bytes to uint64
	_, amount, err := uint128BytesToInt64(response.ImpactedTokenAmount)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to convert token amount: %w", err)
	}

	return response.ImpactedLeafIds, amount, nil
}

func (w *SingleKeyWallet) SendToPhone(ctx context.Context, amount int64, phoneNumber string) (*pb.Transfer, error) {
	identityPublicKey := hex.EncodeToString(w.Config.IdentityPublicKey())
	requester, err := sspapi.NewRequesterWithBaseURL(&identityPublicKey, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create requester: %w", err)
	}
	api := sspapi.NewSparkServiceAPI(requester)
	publicKey, err := api.FetchPublicKeyByPhoneNumber(phoneNumber)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch public key: %w", err)
	}
	publicKeyBytes, err := hex.DecodeString(publicKey)
	if err != nil {
		return nil, fmt.Errorf("failed to decode public key: %w", err)
	}

	transfer, err := w.SendTransfer(ctx, publicKeyBytes, amount)
	if err != nil {
		return nil, fmt.Errorf("failed to send transfer: %w", err)
	}
	err = api.NotifyReceiverTransfer(phoneNumber, uint64(amount))
	if err != nil {
		return transfer, fmt.Errorf("failed to notify receiver transfer: %w", err)
	}
	return transfer, nil
}

func (w *SingleKeyWallet) StartReleaseSeed(phoneNumber string) error {
	requester, err := sspapi.NewRequesterWithBaseURL(nil, nil)
	if err != nil {
		return fmt.Errorf("failed to create requester: %w", err)
	}
	api := sspapi.NewSparkServiceAPI(requester)
	err = api.StartReleaseSeed(phoneNumber)
	if err != nil {
		return fmt.Errorf("failed to start release seed: %w", err)
	}
	return nil
}

func (w *SingleKeyWallet) CompleteReleaseSeed(phoneNumber string, code string) ([]byte, error) {
	requester, err := sspapi.NewRequesterWithBaseURL(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create requester: %w", err)
	}
	api := sspapi.NewSparkServiceAPI(requester)
	seed, err := api.CompleteReleaseSeed(phoneNumber, code)
	if err != nil {
		return nil, fmt.Errorf("failed to complete release seed: %w", err)
	}
	return seed, nil
}

func (w *SingleKeyWallet) CancelAllSenderInitiatedTransfers(ctx context.Context) error {
	transfers, err := QueryPendingTransfersBySender(ctx, w.Config)
	if err != nil {
		return fmt.Errorf("failed to query pending transfers: %w", err)
	}
	for _, transfer := range transfers.Transfers {
		if transfer.Status == pb.TransferStatus_TRANSFER_STATUS_SENDER_INITIATED {
			_, err = CancelSendTransfer(ctx, w.Config, transfer)
			if err != nil {
				return fmt.Errorf("failed to cancel send transfer: %w", err)
			}
		}
	}
	return nil
}

func (w *SingleKeyWallet) QueryAllTransfers(ctx context.Context) ([]*pb.Transfer, error) {
	transfers, _, err := QueryAllTransfers(ctx, w.Config, 100, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to query all transfers: %w", err)
	}
	return transfers, nil
}
