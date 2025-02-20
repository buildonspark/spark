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

func (w *SingleKeyWallet) PayInvoice(ctx context.Context, invoice string) (string, error) {
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

	resp, err := SwapNodesForPreimage(ctx, w.Config, nodeKeyTweaks, w.Config.SparkServiceProviderIdentityPublicKey, paymentHash, &invoice, 0, false)
	if err != nil {
		return "", fmt.Errorf("failed to swap nodes for preimage: %w", err)
	}

	_, err = SendTransferTweakKey(ctx, w.Config, resp.Transfer, nodeKeyTweaks, nil)
	if err != nil {
		return "", fmt.Errorf("failed to send transfer: %w", err)
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

	w.RemoveOwnedNodes(nodesToRemove)
	return requestID, nil
}

func (w *SingleKeyWallet) grpcClient(ctx context.Context) (context.Context, *pb.SparkServiceClient, *grpc.ClientConn, error) {
	conn, err := common.NewGRPCConnectionWithTestTLS(w.Config.CoodinatorAddress())
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

	requester, err := sspapi.NewRequesterWithBaseURL(hex.EncodeToString(w.Config.IdentityPublicKey()), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create requester: %w", err)
	}
	api := sspapi.NewSparkServiceAPI(requester)

	requestID, leaves, err := api.RequestLeavesSwap(hex.EncodeToString(adaptorPubKey.SerializeCompressed()), uint64(totalAmount), uint64(targetAmount), 0, w.Config.Network, userLeaves)
	if err != nil {
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
	if err != nil {
		return nil, fmt.Errorf("failed to complete leaves swap: %w", err)
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
	return claimedNodes, nil
}

func (w *SingleKeyWallet) SendTransfer(ctx context.Context, receiverIdentityPubkey []byte, targetAmount int64) (*pb.Transfer, error) {
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
	requester, err := sspapi.NewRequesterWithBaseURL(hex.EncodeToString(w.Config.IdentityPublicKey()), nil)
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

// For simplicity always mint directly to the issuer wallet (eg. owner == token public key)
func (w *SingleKeyWallet) MintTokens(ctx context.Context, amount uint64) error {
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

	tokenIdentityPubKeyBytes := w.Config.IdentityPublicKey()
	mintTransaction := &pb.TokenTransaction{
		TokenInput: &pb.TokenTransaction_MintInput{
			MintInput: &pb.MintInput{
				IssuerPublicKey: tokenIdentityPubKeyBytes,
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

// For simplicity always use the wallet's identity public key as the token public key.
func (w *SingleKeyWallet) TransferTokens(ctx context.Context, amount uint64, receiverPubKey []byte) error {
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
	selectedLeavesWithPrevTxData, selectedLeavesAmount, err := selectTokenLeaves(amount, w.OwnedTokenLeaves)
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
				TokenPublicKey: w.Config.IdentityPublicKey(),
				TokenAmount:    int64ToUint128Bytes(0, uint64(amount)),
			},
		},
	}

	// Send the remainder back to our wallet with an additional output if necessary.
	if selectedLeavesAmount > amount {
		remainder := selectedLeavesAmount - amount
		changeOutput := &pb.TokenLeafOutput{
			OwnerPublicKey: w.Config.IdentityPublicKey(),
			TokenPublicKey: w.Config.IdentityPublicKey(),
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

// For simplicity always use the wallet's identity public key as the token public key.
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

func selectTokenLeaves(targetAmount uint64, leavesWithPrevTxData []*pb.LeafWithPreviousTransactionData) ([]*pb.LeafWithPreviousTransactionData, uint64, error) {
	getTokenAmount := func(leaf *pb.LeafWithPreviousTransactionData) (uint64, error) {
		_, amount, err := uint128BytesToInt64(leaf.Leaf.TokenAmount)
		return amount, err
	}

	// Pre-check all leaves for valid token amounts
	for _, leaf := range leavesWithPrevTxData {
		if _, err := getTokenAmount(leaf); err != nil {
			return nil, 0, fmt.Errorf("invalid token amount in leaf: %w", err)
		}
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
		// Checked above so no err is expected.
		leafTokenAmount, _ := getTokenAmount(leaf)
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
					OwnerPublicKey: leaf.OwnerPublicKey,
					TokenPublicKey: leaf.TokenPublicKey,
					TokenAmount:    leaf.TokenAmount,
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
