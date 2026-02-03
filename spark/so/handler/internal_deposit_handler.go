package handler

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"strings"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/hashstructure"
	"github.com/lightsparkdev/spark/common/keys"
	"go.uber.org/zap"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/rpcclient"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/logging"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/depositaddress"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/signingkeyshare"
	"github.com/lightsparkdev/spark/so/ent/utxo"
	"github.com/lightsparkdev/spark/so/ent/utxoswap"
	"github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/helper"
	transferHelper "github.com/lightsparkdev/spark/so/transfer"
	"github.com/lightsparkdev/spark/so/utils"
)

// InternalDepositHandler is the deposit handler for so internal
type InternalDepositHandler struct {
	config *so.Config
}

// NewInternalDepositHandler creates a new InternalDepositHandler.
func NewInternalDepositHandler(config *so.Config) *InternalDepositHandler {
	return &InternalDepositHandler{config: config}
}

// MarkKeyshareForDepositAddress links the keyshare to a deposit address.
func (h *InternalDepositHandler) MarkKeyshareForDepositAddress(ctx context.Context, req *pbinternal.MarkKeyshareForDepositAddressRequest) (*pbinternal.MarkKeyshareForDepositAddressResponse, error) {
	logger := logging.GetLoggerFromContext(ctx)

	logger.Sugar().Infof("Marking keyshare %s for deposit address", req.KeyshareId)

	keyshareID, err := uuid.Parse(req.KeyshareId)
	if err != nil {
		logger.With(zap.Error(err)).Sugar().Errorf("Failed to parse keyshare ID %s as UUID", req.KeyshareId)
		return nil, err
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	var network btcnetwork.Network
	for _, networkVariant := range []btcnetwork.Network{btcnetwork.Mainnet, btcnetwork.Regtest, btcnetwork.Testnet, btcnetwork.Signet} {
		if utils.IsBitcoinAddressForNetwork(req.Address, networkVariant) {
			network = networkVariant
			break
		}
	}
	if network == btcnetwork.Unspecified {
		return nil, fmt.Errorf("can not determine network for address: %s", req.Address)
	}

	ownerIDPubKey, err := keys.ParsePublicKey(req.GetOwnerIdentityPublicKey())
	if err != nil {
		return nil, fmt.Errorf("failed to parse owner identity public key: %w", err)
	}
	ownerSigningPubKey, err := keys.ParsePublicKey(req.GetOwnerSigningPublicKey())
	if err != nil {
		return nil, fmt.Errorf("failed to parse owner signing public key: %w", err)
	}
	_, err = db.DepositAddress.Create().
		SetSigningKeyshareID(keyshareID).
		SetOwnerIdentityPubkey(ownerIDPubKey).
		SetOwnerSigningPubkey(ownerSigningPubKey).
		SetNetwork(network).
		SetAddress(req.Address).
		SetIsStatic(req.GetIsStatic()).
		Save(ctx)
	if err != nil {
		logger.Error("Failed to link keyshare to deposit address", zap.Error(err))
		return nil, err
	}

	logger.Sugar().Infof("Marked keyshare %s for deposit address", req.KeyshareId)

	signingKey := h.config.IdentityPrivateKey
	addrHash := sha256.Sum256([]byte(req.Address))
	addressSignature := ecdsa.Sign(signingKey.ToBTCEC(), addrHash[:])
	return &pbinternal.MarkKeyshareForDepositAddressResponse{
		AddressSignature: addressSignature.Serialize(),
	}, nil
}

// GenerateStaticDepositAddressProofs generates proofs of possession for a static deposit address.
func (h *InternalDepositHandler) GenerateStaticDepositAddressProofs(ctx context.Context, req *pbinternal.GenerateStaticDepositAddressProofsRequest) (*pbinternal.GenerateStaticDepositAddressProofsResponse, error) {
	logger := logging.GetLoggerFromContext(ctx)

	keyshareID, err := uuid.Parse(req.KeyshareId)
	if err != nil {
		return nil, fmt.Errorf("failed to parse keyshare ID: %w", err)
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	ownerIDPubKey, err := keys.ParsePublicKey(req.GetOwnerIdentityPublicKey())
	if err != nil {
		return nil, fmt.Errorf("failed to parse owned identity public key: %w", err)
	}
	depositAddress, err := db.DepositAddress.Query().
		Where(depositaddress.AddressEQ(req.Address)).
		Where(depositaddress.IsStaticEQ(true)).
		Where(depositaddress.HasSigningKeyshareWith(signingkeyshare.IDEQ(keyshareID))).
		Where(depositaddress.OwnerIdentityPubkeyEQ(ownerIDPubKey)).
		WithSigningKeyshare().
		Only(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return nil, fmt.Errorf("failed to get deposit address: %w", err)
	}

	if depositAddress == nil {
		return nil, errors.NotFoundMissingEntity(fmt.Errorf("no static deposit address found for keyshare %s, address %s and identity public key %s", keyshareID, req.Address, ownerIDPubKey))
	}

	logger.Sugar().Infof("Generating proofs of possession for static deposit address %s generated from keyshare %s", req.Address, req.KeyshareId)

	signingKey := h.config.IdentityPrivateKey
	addrHash := sha256.Sum256([]byte(depositAddress.Address))
	addressSignature := ecdsa.Sign(signingKey.ToBTCEC(), addrHash[:])

	return &pbinternal.GenerateStaticDepositAddressProofsResponse{
		AddressSignature: addressSignature.Serialize(),
	}, nil
}

// FinalizeTreeCreation finalizes a tree creation during deposit
func (h *InternalDepositHandler) FinalizeTreeCreation(ctx context.Context, req *pbinternal.FinalizeTreeCreationRequest) error {
	logger := logging.GetLoggerFromContext(ctx)

	treeNodeIDs := make([]string, len(req.Nodes))
	for i, node := range req.Nodes {
		treeNodeIDs[i] = node.Id
	}

	logger.Sugar().Infof("Finalizing tree creation for tree nodes %+q", treeNodeIDs)

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	var tree *ent.Tree
	var selectedNode *pbinternal.TreeNode
	for _, node := range req.Nodes {
		if node.ParentNodeId == nil {
			logger.Sugar().Infof("Selected node %s", node.Id)
			selectedNode = node
			break
		}
		selectedNode = node
	}

	if selectedNode == nil {
		return fmt.Errorf("no node in the request")
	}
	markNodeAsAvailable := false
	if selectedNode.ParentNodeId == nil {
		treeID, err := uuid.Parse(selectedNode.TreeId)
		if err != nil {
			return err
		}
		network, err := btcnetwork.FromProtoNetwork(req.Network)
		if err != nil {
			return err
		}
		if !h.config.IsNetworkSupported(network) {
			return fmt.Errorf("network not supported")
		}
		signingKeyshareID, err := uuid.Parse(selectedNode.SigningKeyshareId)
		if err != nil {
			return err
		}
		address, err := db.DepositAddress.Query().Where(depositaddress.HasSigningKeyshareWith(signingkeyshare.IDEQ(signingKeyshareID))).WithTree().ForUpdate().Only(ctx)
		if err != nil {
			return fmt.Errorf("failed to get deposit address: %w", err)
		}
		if address.Edges.Tree != nil {
			return fmt.Errorf("deposit address already has a tree")
		}
		markNodeAsAvailable = address.ConfirmationHeight != 0
		logger.Sugar().Infof("Marking node as available: %v", markNodeAsAvailable)
		nodeTx, err := common.TxFromRawTxBytes(selectedNode.RawTx)
		if err != nil {
			return fmt.Errorf("failed to get node transaction: %w", err)
		}

		if len(nodeTx.TxIn) == 0 {
			return fmt.Errorf("node tx has no inputs")
		}
		txid := nodeTx.TxIn[0].PreviousOutPoint.Hash

		if nodeTx.TxIn[0].PreviousOutPoint.Index > math.MaxInt16 {
			return fmt.Errorf("previous outpoint index overflows int16: %d", nodeTx.TxIn[0].PreviousOutPoint.Index)
		}
		ownerIDPubKey, err := keys.ParsePublicKey(selectedNode.OwnerIdentityPubkey)
		if err != nil {
			return fmt.Errorf("failed to parse owner identity public key: %w", err)
		}
		treeMutator := db.Tree.
			Create().
			SetID(treeID).
			SetOwnerIdentityPubkey(ownerIDPubKey).
			SetBaseTxid(st.NewTxID(txid)).
			SetVout(int16(nodeTx.TxIn[0].PreviousOutPoint.Index)).
			SetNetwork(network).
			SetDepositAddress(address)

		if markNodeAsAvailable {
			treeMutator.SetStatus(st.TreeStatusAvailable)
		} else {
			treeMutator.SetStatus(st.TreeStatusPending)
		}

		tree, err = treeMutator.Save(ctx)
		if err != nil {
			return err
		}
	} else {
		treeID, err := uuid.Parse(selectedNode.TreeId)
		if err != nil {
			return err
		}
		tree, err = db.Tree.Get(ctx, treeID)
		if err != nil {
			return err
		}
		markNodeAsAvailable = tree.Status == st.TreeStatusAvailable
	}

	for _, node := range req.Nodes {
		nodeID, err := uuid.Parse(node.Id)
		if err != nil {
			return err
		}
		if node.Vout > math.MaxInt16 {
			return fmt.Errorf("node vout value %d overflows int16", node.Vout)
		}
		signingKeyshareID, err := uuid.Parse(node.SigningKeyshareId)
		if err != nil {
			return err
		}
		ownerIdentityPubKey, err := keys.ParsePublicKey(node.GetOwnerIdentityPubkey())
		if err != nil {
			return fmt.Errorf("failed to parse owner identity public key: %w", err)
		}
		ownerSigningPubKey, err := keys.ParsePublicKey(node.GetOwnerSigningPubkey())
		if err != nil {
			return fmt.Errorf("failed to parse owner signing public key: %w", err)
		}
		verifyingPubKey, err := keys.ParsePublicKey(node.GetVerifyingPubkey())
		if err != nil {
			return fmt.Errorf("failed to parse verifying public key: %w", err)
		}
		nodeMutator := db.TreeNode.
			Create().
			SetID(nodeID).
			SetTree(tree).
			SetNetwork(tree.Network).
			SetOwnerIdentityPubkey(ownerIdentityPubKey).
			SetOwnerSigningPubkey(ownerSigningPubKey).
			SetValue(node.Value).
			SetVerifyingPubkey(verifyingPubKey).
			SetSigningKeyshareID(signingKeyshareID).
			SetVout(int16(node.Vout)).
			SetRawTx(node.RawTx).
			SetDirectTx(node.DirectTx).
			SetRawRefundTx(node.RawRefundTx).
			SetDirectRefundTx(node.DirectRefundTx).
			SetDirectFromCpfpRefundTx(node.DirectFromCpfpRefundTx)

		if node.ParentNodeId != nil {
			parentID, err := uuid.Parse(node.GetParentNodeId())
			if err != nil {
				return err
			}
			nodeMutator.SetParentID(parentID)
		}

		if markNodeAsAvailable {
			if len(node.RawRefundTx) > 0 {
				nodeMutator.SetStatus(st.TreeNodeStatusAvailable)
			} else {
				nodeMutator.SetStatus(st.TreeNodeStatusSplitted)
			}
		} else {
			nodeMutator.SetStatus(st.TreeNodeStatusCreating)
		}

		_, err = nodeMutator.Save(ctx)
		if err != nil {
			return err
		}
	}
	return nil
}

func ValidateUtxoIsNotSpent(bitcoinClient *rpcclient.Client, txidHash chainhash.Hash, vout uint32) error {
	txOut, err := bitcoinClient.GetTxOut(&txidHash, vout, true)
	if err != nil {
		return fmt.Errorf("failed to call gettxout: %w", err)
	}
	if txOut == nil {
		return fmt.Errorf("utxo is spent on blockchain: %s:%d", txidHash.String(), vout)
	}
	return nil
}

// validateTransfer checks that
//   - all the required fields are present and valid (protobuf validation)
func validateTransfer(transferRequest *pb.StartTransferRequest) error {
	if transferRequest == nil {
		return fmt.Errorf("transferRequest is required")
	}

	if transferRequest.OwnerIdentityPublicKey == nil {
		return fmt.Errorf("owner identity public key is required")
	}

	if transferRequest.ReceiverIdentityPublicKey == nil {
		return fmt.Errorf("receiver identity public key is required")
	}

	return nil
}

// validateUserSignature verifies that the user has authorized the UTXO swap by validating their signature.
func validateUserSignature(userIdentityPubKey keys.Public, userSignature []byte, sspSignature []byte, requestType pb.UtxoSwapRequestType, network btcnetwork.Network, txIdString string, vout uint32, totalAmount uint64, hashVariant pb.HashVariant) error {
	if len(userSignature) == 0 {
		return fmt.Errorf("user signature is required")
	}

	// Create user statement to authorize the UTXO swap
	messageHash, err := CreateUserStatement(txIdString, vout, network, requestType, totalAmount, sspSignature, hashVariant)
	if err != nil {
		return fmt.Errorf("failed to create user statement: %w", err)
	}
	return common.VerifyECDSASignature(userIdentityPubKey, userSignature, messageHash)
}

// CreateUserStatement creates a user statement to authorize the UTXO swap.
// The signature is expected to be a DER-encoded ECDSA signature of sha256 of the message
// composed of:
//   - action name: "claim_static_deposit"
//   - network: the lowercase network name (e.g., "bitcoin", "testnet")
//   - transactionId: the hex-encoded UTXO transaction ID
//   - outputIndex: the UTXO output index (vout)
//   - requestType: the type of request (fixed amount)
//   - creditAmountSats: the amount of satoshis to credit
//   - sspSignature: the hex-encoded SSP signature (sighash of spendTx if SSP is not used)
func CreateUserStatement(
	transactionID string,
	outputIndex uint32,
	network btcnetwork.Network,
	requestType pb.UtxoSwapRequestType,
	creditAmountSats uint64,
	sspSignature []byte,
	hashVariant pb.HashVariant,
) ([]byte, error) {
	if hashVariant == pb.HashVariant_HASH_VARIANT_V2 {
		return createUserStatementV2(transactionID, outputIndex, network, requestType, creditAmountSats, sspSignature)
	}
	return createUserStatementLegacy(transactionID, outputIndex, network, requestType, creditAmountSats, sspSignature)
}

func createUserStatementLegacy(
	transactionID string,
	outputIndex uint32,
	network btcnetwork.Network,
	requestType pb.UtxoSwapRequestType,
	creditAmountSats uint64,
	sspSignature []byte,
) ([]byte, error) {
	payload := sha256.New()
	_, _ = payload.Write([]byte("claim_static_deposit"))            // Action name
	_, _ = payload.Write([]byte(strings.ToLower(network.String()))) // Network value as UTF-8 bytes
	_, _ = payload.Write([]byte(transactionID))                     // Transaction ID as UTF-8 bytes
	_ = binary.Write(payload, binary.LittleEndian, outputIndex)     // Output index as 4-byte unsigned integer (little-endian)

	requestTypeInt := uint8(0)
	switch requestType {
	case pb.UtxoSwapRequestType_Fixed:
		requestTypeInt = uint8(0)
	case pb.UtxoSwapRequestType_MaxFee:
		requestTypeInt = uint8(1)
	case pb.UtxoSwapRequestType_Refund:
		requestTypeInt = uint8(2)
	case pb.UtxoSwapRequestType_Instant:
		return nil, fmt.Errorf("Instant deposit not supported for normal static deposit flow")
	}
	_ = binary.Write(payload, binary.LittleEndian, requestTypeInt)   // Request type
	_ = binary.Write(payload, binary.LittleEndian, creditAmountSats) // Credit amount as 8-byte unsigned integer (little-endian)
	_, _ = payload.Write(sspSignature)                               // SSP signature as UTF-8 bytes
	return payload.Sum(nil), nil
}

func createUserStatementV2(
	transactionID string,
	outputIndex uint32,
	network btcnetwork.Network,
	requestType pb.UtxoSwapRequestType,
	creditAmountSats uint64,
	sspSignature []byte,
) ([]byte, error) {
	requestTypeInt := uint8(0)
	switch requestType {
	case pb.UtxoSwapRequestType_Fixed:
		requestTypeInt = uint8(0)
	case pb.UtxoSwapRequestType_MaxFee:
		requestTypeInt = uint8(1)
	case pb.UtxoSwapRequestType_Refund:
		requestTypeInt = uint8(2)
	case pb.UtxoSwapRequestType_Instant:
		return nil, fmt.Errorf("Instant deposit not supported for normal static deposit flow")
	}

	hash := hashstructure.NewHasher([]string{"spark", "claim_static_deposit"}).
		AddString(strings.ToLower(network.String())).
		AddString(transactionID).
		AddUint32(outputIndex).
		AddUint8(requestTypeInt).
		AddUint64(creditAmountSats).
		AddBytes(sspSignature).
		Hash()
	return hash, nil
}

func CancelUtxoSwap(ctx context.Context, utxoSwap *ent.UtxoSwap) error {
	if utxoSwap.Status == st.UtxoSwapStatusCompleted {
		return fmt.Errorf("utxo swap is already completed")
	}
	if _, err := utxoSwap.Update().SetStatus(st.UtxoSwapStatusCancelled).Save(ctx); err != nil {
		return fmt.Errorf("unable to cancel utxo swap: %w", err)
	}
	return nil
}

func CompleteUtxoSwap(ctx context.Context, utxoSwap *ent.UtxoSwap) error {
	ctx, span := tracer.Start(ctx, "InternalDepositHandler.CompleteUtxoSwap")
	defer span.End()

	if utxoSwap.Status == st.UtxoSwapStatusCancelled {
		return fmt.Errorf("utxo swap is already cancelled")
	}
	if utxoSwap.RequestType != st.UtxoSwapRequestTypeRefund {
		transfer, needUpdate, err := GetTransferFromUtxoSwap(ctx, utxoSwap)
		if err != nil {
			return fmt.Errorf("unable to get transfer from utxo swap: %w", err)
		}
		if needUpdate {
			_, err := utxoSwap.Update().SetTransfer(transfer).Save(ctx)
			if err != nil {
				return fmt.Errorf("unable to set transfer: %w", err)
			}
		}

		// Validate transfer is in a valid state for UTXO swap completion
		if transfer.Status == st.TransferStatusExpired || transfer.Status == st.TransferStatusReturned {
			return fmt.Errorf("transfer is expired or returned")
		}

		// Only allow UTXO swap completion from valid intermediate states
		if !transferHelper.IsTransferSent(transfer) {
			return fmt.Errorf("UTXO swap cannot be completed from transfer status %s: transfer is not sent", transfer.Status)
		}
	}
	if _, err := utxoSwap.Update().SetStatus(st.UtxoSwapStatusCompleted).Save(ctx); err != nil {
		return fmt.Errorf("unable to complete utxo swap: %w", err)
	}
	return nil
}

func GetTransferFromUtxoSwap(ctx context.Context, utxoSwap *ent.UtxoSwap) (*ent.Transfer, bool, error) {
	transfer, err := utxoSwap.QueryTransfer().Only(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return nil, false, fmt.Errorf("unable to get transfer: %w", err)
	}
	if transfer == nil {
		if utxoSwap.RequestedTransferID == uuid.Nil {
			return nil, false, fmt.Errorf("requested transfer id is nil")
		}
		db, err := ent.GetDbFromContext(ctx)
		if err != nil {
			return nil, false, fmt.Errorf("failed to get or create current tx for request: %w", err)
		}
		transfer, err = db.Transfer.Get(ctx, utxoSwap.RequestedTransferID)
		if err != nil {
			return nil, false, fmt.Errorf("unable to fetch transfer by requested id=%s: %w", utxoSwap.RequestedTransferID, err)
		}
		return transfer, true, nil
	}
	return transfer, false, nil
}

func (h *InternalDepositHandler) RollbackUtxoSwap(ctx context.Context, config *so.Config, req *pbinternal.RollbackUtxoSwapRequest) (*pbinternal.RollbackUtxoSwapResponse, error) {
	logger := logging.GetLoggerFromContext(ctx)
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	network, err := btcnetwork.FromProtoNetwork(req.GetOnChainUtxo().GetNetwork())
	if err != nil {
		return nil, fmt.Errorf("unable to get network: %w", err)
	}

	messageHash, err := CreateUtxoSwapStatement(
		UtxoSwapStatementTypeRollback,
		hex.EncodeToString(req.OnChainUtxo.Txid),
		req.OnChainUtxo.Vout,
		network,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create rollback utxo swap request statement: %w", err)
	}
	// Coordinator pubkey comes from the request, but it's fine because it will be checked against the DB.
	coordinatorPubKey, err := keys.ParsePublicKey(req.CoordinatorPublicKey)
	if err != nil {
		return nil, fmt.Errorf("unable to parse coordinator public key: %w", err)
	}
	if err := common.VerifyECDSASignature(coordinatorPubKey, req.Signature, messageHash); err != nil {
		logger.Sugar().Debugf(
			"Rollback utxo swap request signature (signature: %x txid: %x vout: %d network: %s coordinator: %s message_hash: %x)",
			req.Signature,
			req.OnChainUtxo.Txid,
			req.OnChainUtxo.Vout,
			network,
			req.CoordinatorPublicKey,
			messageHash,
		)
		return nil, fmt.Errorf("unable to verify coordinator signature: %w", err)
	}

	logger.Sugar().Infof("Cancelling UTXO swap for %x:%d", req.OnChainUtxo.Txid, req.OnChainUtxo.Vout)

	schemaNetwork, err := btcnetwork.FromProtoNetwork(req.OnChainUtxo.Network)
	if err != nil {
		return nil, fmt.Errorf("unable to get schema network: %w", err)
	}

	targetUtxo, err := VerifiedTargetUtxoFromRequest(ctx, config, db, schemaNetwork, req.OnChainUtxo)
	if err != nil {
		return nil, err
	}

	utxoSwap, err := db.UtxoSwap.Query().
		Where(
			utxoswap.HasUtxoWith(utxo.IDEQ(targetUtxo.inner.ID)),
			utxoswap.StatusIn(st.UtxoSwapStatusCreated, st.UtxoSwapStatusCompleted),
			// The identity public key of the coordinator that created the utxo swap.
			// It's been verified above.
			utxoswap.CoordinatorIdentityPublicKeyEQ(coordinatorPubKey),
		).
		Only(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return nil, fmt.Errorf("unable to get utxo swap: %w", err)
	}
	if ent.IsNotFound(err) {
		return &pbinternal.RollbackUtxoSwapResponse{}, nil
	}

	if err := CancelUtxoSwap(ctx, utxoSwap); err != nil {
		return nil, err
	}

	logger.Sugar().Infof("UTXO swap %s for %s:%d cancelled", utxoSwap.ID, targetUtxo.Hash().String(), targetUtxo.Vout)
	return &pbinternal.RollbackUtxoSwapResponse{}, nil
}

func CreateUtxoSwapStatement(statementType UtxoSwapStatementType, transactionID string, outputIndex uint32, network btcnetwork.Network) ([]byte, error) {
	hasher := sha256.New()

	// Writing to a sha256 never returns an error, so we don't need to check any of the errors below.
	// Add action name
	_, _ = hasher.Write([]byte(statementType.String()))

	// Add network value as UTF-8 bytes
	_, _ = hasher.Write([]byte(network.String()))

	// Add transaction ID as UTF-8 bytes
	_, _ = hasher.Write([]byte(transactionID))

	// Add output index as 4-byte unsigned integer (little-endian)
	_ = binary.Write(hasher, binary.LittleEndian, outputIndex)

	// Request type fixed amount
	_ = binary.Write(hasher, binary.LittleEndian, uint8(0))

	// Hash the payload with SHA-256
	return hasher.Sum(nil), nil
}

func (h *InternalDepositHandler) UtxoSwapCompleted(ctx context.Context, config *so.Config, req *pbinternal.UtxoSwapCompletedRequest) (*pbinternal.UtxoSwapCompletedResponse, error) {
	logger := logging.GetLoggerFromContext(ctx)
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	network, err := btcnetwork.FromProtoNetwork(req.OnChainUtxo.Network)
	if err != nil {
		return nil, fmt.Errorf("unable to get network: %w", err)
	}
	messageHash, err := CreateUtxoSwapStatement(
		UtxoSwapStatementTypeCompleted,
		hex.EncodeToString(req.OnChainUtxo.Txid),
		req.OnChainUtxo.Vout,
		network,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create utxo swap completed statement: %w", err)
	}
	coordinatorPubKey, err := keys.ParsePublicKey(req.CoordinatorPublicKey)
	if err != nil {
		return nil, fmt.Errorf("unable to parse coordinator public key: %w", err)
	}
	if err := common.VerifyECDSASignature(coordinatorPubKey, req.Signature, messageHash); err != nil {
		return nil, fmt.Errorf("unable to verify coordinator signature: %w", err)
	}

	logger.Sugar().Infof("Marking UTXO swap for %x:%d as COMPLETED", req.OnChainUtxo.Txid, req.OnChainUtxo.Vout)

	schemaNetwork, err := btcnetwork.FromProtoNetwork(req.OnChainUtxo.Network)
	if err != nil {
		return nil, fmt.Errorf("unable to get schema network: %w", err)
	}
	targetUtxo, err := VerifiedTargetUtxoFromRequest(ctx, config, db, schemaNetwork, req.OnChainUtxo)
	if err != nil {
		return nil, err
	}

	utxoSwap, err := db.UtxoSwap.Query().
		Where(utxoswap.HasUtxoWith(utxo.IDEQ(targetUtxo.inner.ID))).
		Where(utxoswap.StatusIn(st.UtxoSwapStatusCreated, st.UtxoSwapStatusCompleted)).
		// The identity public key of the coordinator that created the utxo swap.
		// It's been verified above.
		Where(utxoswap.CoordinatorIdentityPublicKeyEQ(coordinatorPubKey)).
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get utxo swap for utxo %s: %w", targetUtxo.inner.ID, err)
	}

	if utxoSwap != nil && utxoSwap.Status == st.UtxoSwapStatusCompleted {
		return &pbinternal.UtxoSwapCompletedResponse{}, nil
	}

	if err := CompleteUtxoSwap(ctx, utxoSwap); err != nil {
		return nil, fmt.Errorf("unable to complete utxo swap: %w", err)
	}

	logger.Sugar().Infof("UTXO swap %s for %s:%d marked as COMPLETED", utxoSwap.ID, targetUtxo.Hash().String(), targetUtxo.Vout())
	return &pbinternal.UtxoSwapCompletedResponse{}, nil
}

func CreateCompleteSwapForUtxoRequest(config *so.Config, utxo *pb.UTXO) (*pbinternal.UtxoSwapCompletedRequest, error) {
	network, err := btcnetwork.FromProtoNetwork(utxo.Network)
	if err != nil {
		return nil, fmt.Errorf("unable to get network: %w", err)
	}
	completedUtxoSwapRequestMessageHash, err := CreateUtxoSwapStatement(
		UtxoSwapStatementTypeCompleted,
		hex.EncodeToString(utxo.Txid),
		utxo.Vout,
		network,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create utxo swap statement: %w", err)
	}
	completedUtxoSwapRequestSignature := ecdsa.Sign(config.IdentityPrivateKey.ToBTCEC(), completedUtxoSwapRequestMessageHash)
	return &pbinternal.UtxoSwapCompletedRequest{
		OnChainUtxo:          utxo,
		Signature:            completedUtxoSwapRequestSignature.Serialize(),
		CoordinatorPublicKey: config.IdentityPublicKey().Serialize(),
	}, nil
}

func CompleteSwapForUtxoWithOtherOperators(ctx context.Context, config *so.Config, request *pbinternal.UtxoSwapCompletedRequest) error {
	logger := logging.GetLoggerFromContext(ctx)

	_, err := helper.ExecuteTaskWithAllOperators(ctx, config, &helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}, func(ctx context.Context, operator *so.SigningOperator) (any, error) {
		conn, err := operator.NewOperatorGRPCConnection()
		if err != nil {
			logger.With(zap.Error(err)).Sugar().Errorf("Failed to connect to operator %s", operator.Identifier)
			return nil, err
		}
		defer conn.Close()

		client := pbinternal.NewSparkInternalServiceClient(conn)
		internalResp, err := client.UtxoSwapCompleted(ctx, request)
		if err != nil {
			logger.With(zap.Error(err)).Sugar().Errorf("Failed to execute utxo swap completed task with operator %s", operator.Identifier)
			return nil, err
		}
		return internalResp, err
	})
	return err
}

func (h *InternalDepositHandler) CompleteSwapForAllOperators(ctx context.Context, config *so.Config, request *pbinternal.UtxoSwapCompletedRequest) error {
	ctx, span := tracer.Start(ctx, "InternalDepositHandler.CompleteSwapForAllOperators")
	defer span.End()

	// Try to complete with other operators first.
	if err := CompleteSwapForUtxoWithOtherOperators(ctx, config, request); err != nil {
		return err
	}
	// If other operators return success, we can complete the swap in self.
	_, err := h.UtxoSwapCompleted(ctx, config, request)
	return err
}
