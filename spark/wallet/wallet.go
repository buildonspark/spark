package wallet

import (
	"context"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/lightsparkdev/spark-go/common"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	"github.com/lightsparkdev/spark-go/so/ent/schema"
	sspapi "github.com/lightsparkdev/spark-go/wallet/ssp_api"
	decodepay "github.com/nbd-wtf/ln-decodepay"
)

// SignleKeyWallet is a wallet that uses a single private key for all signing keys.
// This is the most simple type of wallet and for testing purposes only.
type SignleKeyWallet struct {
	Config            *Config
	SigningPrivateKey []byte
	OwnedNodes        []*pb.TreeNode
}

// NewSignleKeyWallet creates a new single key wallet.
func NewSignleKeyWallet(config *Config, signingPrivateKey []byte) *SignleKeyWallet {
	return &SignleKeyWallet{
		Config:            config,
		SigningPrivateKey: signingPrivateKey,
	}
}

func (w *SignleKeyWallet) CreateLightningInvoice(ctx context.Context, amount int64, memo string) (*string, int64, error) {
	requester, err := sspapi.NewRequesterWithBaseURL(hex.EncodeToString(w.Config.IdentityPublicKey()), nil)
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

func (w *SignleKeyWallet) ClaimAllTransfers(ctx context.Context) ([]*pb.TreeNode, error) {
	pendingTransfers, err := QueryPendingTransfers(ctx, w.Config)
	if err != nil {
		return nil, err
	}

	nodesResult := make([]*pb.TreeNode, 0)
	for _, transfer := range pendingTransfers.Transfers {
		leavesMap, err := VerifyPendingTransfer(ctx, w.Config, transfer)
		if err != nil {
			return nil, err
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
			return nil, err
		}
		nodesResult = append(nodesResult, nodes...)
	}
	w.OwnedNodes = append(w.OwnedNodes, nodesResult...)
	return nodesResult, nil
}

func (w *SignleKeyWallet) leafSelection(targetAmount int64) ([]*pb.TreeNode, error) {
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

func (w *SignleKeyWallet) leafSelectionForSwap(targetAmount int64) ([]*pb.TreeNode, int64, error) {
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
	if amount > targetAmount {
		return nodes, amount, nil
	}
	if amount == targetAmount {
		return nil, amount, fmt.Errorf("you're trying to swap for the exact amount you have, no need to swap")
	}
	return nil, amount, fmt.Errorf("you don't have enough nodes to swap for the target amount")
}

func (w *SignleKeyWallet) PayInvoice(ctx context.Context, invoice string) (string, error) {
	// TODO: query fee

	bolt11, err := decodepay.Decodepay(invoice)
	if err != nil {
		return "", fmt.Errorf("failed to parse invoice: %w", err)
	}

	amount := math.Ceil(float64(bolt11.MSatoshi) / 1000.0)
	nodes, err := w.leafSelection(int64(amount))
	if err != nil {
		return "", fmt.Errorf("failed to select nodes: %w", err)
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

	_, err = SwapNodesForPreimage(ctx, w.Config, nodeKeyTweaks, w.Config.SparkServiceProviderIdentityPublicKey, paymentHash, &invoice, 0, false)
	if err != nil {
		return "", fmt.Errorf("failed to swap nodes for preimage: %w", err)
	}

	requester, err := sspapi.NewRequesterWithBaseURL(hex.EncodeToString(w.Config.IdentityPublicKey()), nil)
	if err != nil {
		return "", fmt.Errorf("failed to create requester: %w", err)
	}
	api := sspapi.NewSparkServiceAPI(requester)

	requestID, err := api.PayInvoice(invoice)
	if err != nil {
		return "", fmt.Errorf("failed to pay invoice: %w", err)
	}

	for i, node := range w.OwnedNodes {
		if nodesToRemove[node.Id] {
			w.OwnedNodes = append(w.OwnedNodes[:i], w.OwnedNodes[i+1:]...)
		}
	}
	return requestID, nil
}

func (w *SignleKeyWallet) SyncWallet(ctx context.Context) error {
	conn, err := common.NewGRPCConnectionWithTestTLS(w.Config.CoodinatorAddress())
	if err != nil {
		return fmt.Errorf("failed to connect to operator: %w", err)
	}
	defer conn.Close()

	token, err := AuthenticateWithConnection(ctx, w.Config, conn)
	if err != nil {
		return fmt.Errorf("failed to authenticate: %w", err)
	}
	ctx = ContextWithToken(ctx, token)

	client := pb.NewSparkServiceClient(conn)
	response, err := client.GetTreeNodesByPublicKey(ctx, &pb.TreeNodesByPublicKeyRequest{
		OwnerIdentityPubkey: w.Config.IdentityPublicKey(),
	})
	if err != nil {
		return fmt.Errorf("failed to get owned nodes: %w", err)
	}
	for _, node := range response.Nodes {
		if node.Status == string(schema.TreeNodeStatusAvailable) {
			w.OwnedNodes = append(w.OwnedNodes, node)
		}
	}
	return nil
}

func (w *SignleKeyWallet) RequestLeavesSwap(ctx context.Context, targetAmount int64) ([]*pb.TreeNode, error) {
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
	_, adaptorPrivKeyBytes, err := common.GenerateAdaptorFromSignature(refundSignatureMap[nodes[0].Id])
	if err != nil {
		return nil, fmt.Errorf("failed to generate adaptor: %w", err)
	}

	adaptorPrivateKey := secp256k1.PrivKeyFromBytes(adaptorPrivKeyBytes)
	adaptorPubKey := adaptorPrivateKey.PubKey()

	if err != nil {
		return nil, fmt.Errorf("failed to parse adaptor private key: %w", err)
	}

	requester, err := sspapi.NewRequesterWithBaseURL(hex.EncodeToString(w.Config.IdentityPublicKey()), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create requester: %w", err)
	}
	api := sspapi.NewSparkServiceAPI(requester)

	requestID, err := api.RequestLeavesSwap(hex.EncodeToString(adaptorPubKey.SerializeCompressed()), uint64(totalAmount), uint64(targetAmount), 0, w.Config.Network)
	if err != nil {
		return nil, fmt.Errorf("failed to request leaves swap: %w", err)
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

	// send the transfer
	_, err = SendTransferTweakKey(ctx, w.Config, transfer, leafKeyTweaks, refundSignatureMap)
	if err != nil {
		return nil, fmt.Errorf("failed to send transfer: %w", err)
	}

	for i, node := range w.OwnedNodes {
		if nodesToRemove[node.Id] {
			w.OwnedNodes = append(w.OwnedNodes[:i], w.OwnedNodes[i+1:]...)
		}
	}

	return claimedNodes, nil
}

func (w *SignleKeyWallet) SendTransfer(ctx context.Context, receiverIdentityPubkey []byte, targetAmount int64) (*pb.Transfer, error) {
	nodes, err := w.leafSelection(targetAmount)
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

	transfer, err := SendTransfer(ctx, w.Config, leafKeyTweaks, receiverIdentityPubkey, time.Time{})
	if err != nil {
		return nil, fmt.Errorf("failed to send transfer: %w", err)
	}

	for i, node := range w.OwnedNodes {
		if nodesToRemove[node.Id] {
			w.OwnedNodes = append(w.OwnedNodes[:i], w.OwnedNodes[i+1:]...)
		}
	}

	return transfer, nil
}
