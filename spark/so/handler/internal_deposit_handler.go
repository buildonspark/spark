package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"

	"github.com/lightsparkdev/spark/common/keys"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/rpcclient"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
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

	logger.Info("Marking keyshare for deposit address", "keyshare_id", req.KeyshareId)

	keyshareID, err := uuid.Parse(req.KeyshareId)
	if err != nil {
		logger.Error("Failed to parse keyshare ID", "error", err)
		return nil, err
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	var network common.Network
	for _, networkVariant := range []common.Network{common.Mainnet, common.Regtest, common.Testnet, common.Signet} {
		if utils.IsBitcoinAddressForNetwork(req.Address, networkVariant) {
			network = networkVariant
			break
		}
	}
	if network == common.Unspecified {
		return nil, fmt.Errorf("can not determine network for address: %s", req.Address)
	}

	schemaNetwork, err := common.SchemaNetworkFromNetwork(network)
	if err != nil {
		return nil, err
	}
	depositAddressMutator := db.DepositAddress.Create().
		SetSigningKeyshareID(keyshareID).
		SetOwnerIdentityPubkey(req.OwnerIdentityPublicKey).
		SetOwnerSigningPubkey(req.OwnerSigningPublicKey).
		SetNetwork(schemaNetwork).
		SetAddress(req.Address)

	if req.IsStatic != nil && *req.IsStatic {
		depositAddressMutator.SetIsStatic(true)
	}

	_, err = depositAddressMutator.Save(ctx)
	if err != nil {
		logger.Error("Failed to link keyshare to deposit address", "error", err)
		return nil, err
	}

	logger.Info("Marked keyshare for deposit address", "keyshare_id", req.KeyshareId)

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

	depositAddress, err := db.DepositAddress.Query().
		Where(depositaddress.AddressEQ(req.Address)).
		Where(depositaddress.IsStaticEQ(true)).
		Where(depositaddress.HasSigningKeyshareWith(signingkeyshare.IDEQ(keyshareID))).
		Where(depositaddress.OwnerIdentityPubkeyEQ(req.OwnerIdentityPublicKey)).
		WithSigningKeyshare().
		Only(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return nil, fmt.Errorf("failed to get deposit address: %w", err)
	}

	if depositAddress == nil {
		return nil, errors.NotFoundErrorf("no static deposit address found for keyshare %s, address %s and identity public key %s", keyshareID, req.Address, hex.EncodeToString(req.OwnerIdentityPublicKey))
	}

	logger.Info("Generating proofs of possession for static deposit address generated from keyshare", "keyshare_id", req.KeyshareId, "address", req.Address)

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

	logger.Info("Finalizing tree creation", "tree_node_ids", treeNodeIDs)

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	var tree *ent.Tree
	var selectedNode *pbinternal.TreeNode
	for _, node := range req.Nodes {
		if node.ParentNodeId == nil {
			logger.Info("Selected node", "tree_node_id", node.Id)
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
		network, err := common.NetworkFromProtoNetwork(req.Network)
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
		address, err := db.DepositAddress.Query().Where(depositaddress.HasSigningKeyshareWith(signingkeyshare.IDEQ(signingKeyshareID))).Only(ctx)
		if err != nil {
			return fmt.Errorf("failed to get deposit address: %w", err)
		}
		markNodeAsAvailable = address.ConfirmationHeight != 0
		logger.Info(fmt.Sprintf("Marking node as available: %v", markNodeAsAvailable))
		nodeTx, err := common.TxFromRawTxBytes(selectedNode.RawTx)
		if err != nil {
			return err
		}
		txid := nodeTx.TxIn[0].PreviousOutPoint.Hash

		schemaNetwork, err := common.SchemaNetworkFromNetwork(network)
		if err != nil {
			return err
		}

		treeMutator := db.Tree.
			Create().
			SetID(treeID).
			SetOwnerIdentityPubkey(selectedNode.OwnerIdentityPubkey).
			SetBaseTxid(txid[:]).
			SetVout(int16(nodeTx.TxIn[0].PreviousOutPoint.Index)).
			SetNetwork(schemaNetwork)

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
		signingKeyshareID, err := uuid.Parse(node.SigningKeyshareId)
		if err != nil {
			return err
		}
		nodeMutator := db.TreeNode.
			Create().
			SetID(nodeID).
			SetTree(tree).
			SetOwnerIdentityPubkey(node.OwnerIdentityPubkey).
			SetOwnerSigningPubkey(node.OwnerSigningPubkey).
			SetValue(node.Value).
			SetVerifyingPubkey(node.VerifyingPubkey).
			SetSigningKeyshareID(signingKeyshareID).
			SetVout(int16(node.Vout)).
			SetRawTx(node.RawTx).
			SetDirectTx(node.DirectTx).
			SetRawRefundTx(node.RawRefundTx).
			SetDirectRefundTx(node.DirectRefundTx).
			SetDirectFromCpfpRefundTx(node.DirectFromCpfpRefundTx)

		if node.ParentNodeId != nil {
			parentID, err := uuid.Parse(*node.ParentNodeId)
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

// CreateUtxoSwap creates a new UTXO swap record and a transfer record to a user.
// The function performs the following steps:
// 1. Validates the request by checking:
//   - The network is supported
//   - The UTXO is paid to a registered static deposit address that belongs to the receiver of the transfer and
//     is confirmed on the blockchain with required number of confirmations
//   - The user signature is valid
//   - The leaves are valid, AVAILABLE and the user (SSP) has signed them with valid signatures (proof of ownership)
//
// 2. Checks that the UTXO swap is not already registered
// 3. Creates a UTXO swap record in the database with status CREATED
// 4. Creates a transfer to the user with the specified leaves
//
// Parameters:
//   - ctx: The context for the operation
//   - config: The service configuration
//   - req: The UTXO swap request containing:
//   - OnChainUtxo: The UTXO to be swapped (network, txid, vout)
//   - Transfer: The transfer details (receiver identity, leaves to send, etc.)
//   - SpendTxSigningJob: The signing job for the spend transaction
//   - UserSignature: The user's signature authorizing the swap
//   - SspSignature: The SSP's signature (optional)
//   - Amount: Quote amount (either fixed amount or max fee)
//
// Returns:
//   - CreateUtxoSwapResponse containing:
//   - UtxoDepositAddress: The deposit address associated with the UTXO
//   - Transfer: The created transfer record (empty for user refund call)
//   - error if the operation fails
//
// Possible errors:
//   - Network not supported
//   - UTXO not found
//   - User signature validation failed
//   - UTXO swap already registered
//   - Failed to create transfer
func (h *InternalDepositHandler) CreateUtxoSwap(ctx context.Context, config *so.Config, reqWithSignature *pbinternal.CreateUtxoSwapRequest) (*pbinternal.CreateUtxoSwapResponse, error) {
	ctx, span := tracer.Start(ctx, "InternalDepositHandler.CreateUtxoSwap")
	defer span.End()

	logger := logging.GetLoggerFromContext(ctx)
	req := reqWithSignature.Request
	logger.Info("Start CreateUtxoSwap request for on-chain utxo", "transfer_id", req.Transfer.TransferId)

	// Verify CoordinatorPublicKey is correct. It does not actually prove that the
	// caller is the coordinator, but that there is a message to create a swap
	// signed by some identity key. This identity owner will be able to call a
	// cancel on this utxo swap.
	messageHash, err := CreateUtxoSwapStatement(
		UtxoSwapStatementTypeCreated,
		hex.EncodeToString(req.OnChainUtxo.Txid),
		req.OnChainUtxo.Vout,
		common.Network(req.OnChainUtxo.Network),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create create utxo swap request statement: %w", err)
	}

	coordinatorPubKey, err := keys.ParsePublicKey(reqWithSignature.CoordinatorPublicKey)
	if err != nil {
		return nil, fmt.Errorf("failed to parse coordinator public key: %w", err)
	}
	coordinatorIsSO := false
	for _, op := range config.SigningOperatorMap {
		if op.IdentityPublicKey.Equals(coordinatorPubKey) {
			coordinatorIsSO = true
			break
		}
	}
	if !coordinatorIsSO {
		return nil, fmt.Errorf("coordinator is not a signing operator")
	}

	if err := verifySignature(reqWithSignature.CoordinatorPublicKey, reqWithSignature.Signature, messageHash); err != nil {
		return nil, fmt.Errorf("unable to verify coordinator signature for creating a swap: %w", err)
	}

	// Validate the request
	// Check that the on chain utxo is paid to a registered static deposit address and
	// is confirmed on the blockchain. This logic is implemented in chain watcher.
	network, err := common.NetworkFromProtoNetwork(req.OnChainUtxo.Network)
	if err != nil {
		return nil, err
	}
	if !config.IsNetworkSupported(network) {
		return nil, fmt.Errorf("network %s not supported", network)
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	schemaNetwork, err := common.SchemaNetworkFromProtoNetwork(req.OnChainUtxo.Network)
	if err != nil {
		return nil, err
	}

	targetUtxo, err := VerifiedTargetUtxo(ctx, config, db, schemaNetwork, req.OnChainUtxo.Txid, req.OnChainUtxo.Vout)
	if err != nil {
		return nil, err
	}

	// Validate general transfer signatures and leaves
	if err = validateTransfer(req.Transfer); err != nil {
		return nil, fmt.Errorf("transfer validation failed: %w", err)
	}

	transferHandler := NewBaseTransferHandler(h.config)
	totalAmount := uint64(0)
	quoteSigningBytes := req.SspSignature

	switch req.RequestType {
	case pb.UtxoSwapRequestType_Fixed:
		// *** Validate fixed amount request ***

		if _, err := transferHandler.validateTransferPackage(ctx, req.Transfer.TransferId, req.Transfer.TransferPackage, req.Transfer.OwnerIdentityPublicKey); err != nil {
			return nil, fmt.Errorf("error validating transfer package: %w", err)
		}

		leafRefundMap := make(map[string][]byte)
		for _, leaf := range req.Transfer.TransferPackage.LeavesToSend {
			leafRefundMap[leaf.LeafId] = leaf.RawTx
		}

		// Validate user signature, receiver identitypubkey and amount in transfer
		leaves, err := loadLeavesWithLock(ctx, db, leafRefundMap)
		if err != nil {
			return nil, fmt.Errorf("unable to load leaves: %w", err)
		}
		totalAmount = getTotalTransferValue(leaves)
		if err = validateUserSignature(req.Transfer.ReceiverIdentityPublicKey, req.UserSignature, req.SspSignature, req.RequestType, network, targetUtxo.Txid, targetUtxo.Vout, totalAmount); err != nil {
			return nil, fmt.Errorf("user signature validation failed: %w", err)
		}

	case pb.UtxoSwapRequestType_MaxFee:
		// *** Validate max fee request ***
		return nil, fmt.Errorf("max fee request type is not implemented")

	case pb.UtxoSwapRequestType_Refund:
		// *** Validate refund request ***

		if req.Transfer.OwnerIdentityPublicKey == nil {
			return nil, fmt.Errorf("owner identity public key is required")
		}

		if req.Transfer.ReceiverIdentityPublicKey == nil {
			return nil, fmt.Errorf("receiver identity public key is required")
		}

		spendTxSighash, totalAmount, err := GetTxSigningInfo(ctx, targetUtxo, req.SpendTxSigningJob.RawTx)
		if err != nil {
			return nil, fmt.Errorf("failed to get spend tx sighash: %w", err)
		}
		// Validate user signature, receiver identitypubkey and amount in transfer
		if err = validateUserSignature(
			req.Transfer.ReceiverIdentityPublicKey,
			req.UserSignature,
			spendTxSighash,
			req.RequestType,
			network,
			targetUtxo.Txid,
			targetUtxo.Vout,
			totalAmount); err != nil {
			return nil, fmt.Errorf("user signature validation failed: %w", err)
		}
		quoteSigningBytes = spendTxSighash
	}

	// Check that the utxo swap is not already registered
	utxoSwap, err := db.UtxoSwap.Query().
		Where(utxoswap.HasUtxoWith(utxo.IDEQ(targetUtxo.ID))).
		Where(utxoswap.StatusIn(st.UtxoSwapStatusCreated, st.UtxoSwapStatusCompleted)).
		First(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return nil, fmt.Errorf("unable to check if utxo swap is already registered: %w", err)
	}
	if utxoSwap != nil {
		return nil, fmt.Errorf("utxo swap is already registered")
	}

	logger.Info(
		"Creating UTXO swap record",
		"request_type", req.RequestType,
		"transfer_id", req.Transfer.TransferId,
		"user_identity_public_key", hex.EncodeToString(req.Transfer.ReceiverIdentityPublicKey),
		"txid", hex.EncodeToString(targetUtxo.Txid),
		"vout", targetUtxo.Vout,
		"network", network,
		"credit_amount_sats", totalAmount,
	)

	// Create a utxo swap record and then a transfer. We rely on DbSessionMiddleware to
	// ensure that all db inserts are rolled back in case of an error.
	transferUUID := uuid.Nil
	if req.RequestType != pb.UtxoSwapRequestType_Refund {
		transferUUID, err = uuid.Parse(req.Transfer.TransferId)
		if err != nil {
			return nil, fmt.Errorf("unable to parse transfer_id as a uuid %s: %w", req.Transfer.TransferId, err)
		}
	}
	utxoSwap, err = db.UtxoSwap.Create().
		SetStatus(st.UtxoSwapStatusCreated).
		// utxo
		SetUtxo(targetUtxo).
		// quote
		SetRequestType(st.UtxoSwapFromProtoRequestType(req.RequestType)).
		SetCreditAmountSats(totalAmount).
		// quote signing bytes are the sighash of the spend tx if SSP is not used
		SetSspSignature(quoteSigningBytes).
		SetSspIdentityPublicKey(req.Transfer.OwnerIdentityPublicKey).
		// authorization from a user to claim this utxo after fulfilling the quote
		SetUserSignature(req.UserSignature).
		SetUserIdentityPublicKey(req.Transfer.ReceiverIdentityPublicKey).
		// Identity of the owner who can cancel this swap (if it's not yet completed), normally -- the coordinator SO
		SetCoordinatorIdentityPublicKey(reqWithSignature.CoordinatorPublicKey).
		SetRequestedTransferID(transferUUID).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to store utxo swap: %w", err)
	}

	depositAddress, err := targetUtxo.QueryDepositAddress().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get utxo deposit address: %w", err)
	}
	if !depositAddress.IsStatic {
		return nil, fmt.Errorf("unable to claim a deposit to a non-static address: %w", err)
	}
	_, err = db.DepositAddress.UpdateOneID(depositAddress.ID).AddUtxoswaps(utxoSwap).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to add utxo swap to deposit address: %w", err)
	}
	if !bytes.Equal(depositAddress.OwnerIdentityPubkey, req.Transfer.ReceiverIdentityPublicKey) {
		return nil, fmt.Errorf("transfer is not to the recepient of the deposit")
	}
	// Validate that the deposit key provided by the user matches what's in the DB.
	// SSP should generate the deposit public key from a deposit secret key provide by the customer.
	if !bytes.Equal(depositAddress.OwnerSigningPubkey, req.SpendTxSigningJob.SigningPublicKey) {
		return nil, fmt.Errorf("deposit address owner signing pubkey does not match the signing public key")
	}

	return &pbinternal.CreateUtxoSwapResponse{
		UtxoDepositAddress: depositAddress.Address,
	}, nil
}

func ValidateUtxoIsNotSpent(bitcoinClient *rpcclient.Client, txid []byte, vout uint32) error {
	txidHash, err := chainhash.NewHash(txid)
	if err != nil {
		return fmt.Errorf("failed to create txid hash: %w", err)
	}
	txOut, err := bitcoinClient.GetTxOut(txidHash, vout, true)
	if err != nil {
		return fmt.Errorf("failed to call gettxout: %w", err)
	}
	if txOut == nil {
		return fmt.Errorf("utxo is spent on blockchain: %s:%d", hex.EncodeToString(txidHash[:]), vout)
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
func validateUserSignature(userIdentityPublicKey []byte, userSignature []byte, sspSignature []byte, requestType pb.UtxoSwapRequestType, network common.Network, txid []byte, vout uint32, totalAmount uint64) error {
	if userSignature == nil {
		return fmt.Errorf("user signature is required")
	}

	// Create user statement to authorize the UTXO swap
	messageHash, err := CreateUserStatement(
		hex.EncodeToString(txid),
		vout,
		network,
		requestType,
		totalAmount,
		sspSignature,
	)
	if err != nil {
		return fmt.Errorf("failed to create user statement: %w", err)
	}

	return verifySignature(userIdentityPublicKey, userSignature, messageHash)
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
	network common.Network,
	requestType pb.UtxoSwapRequestType,
	creditAmountSats uint64,
	sspSignature []byte,
) ([]byte, error) {
	// Create a buffer to hold all the data
	var payload bytes.Buffer

	// Add action name
	_, err := payload.WriteString("claim_static_deposit")
	if err != nil {
		return nil, err
	}

	// Add network value as UTF-8 bytes
	_, err = payload.WriteString(network.String())
	if err != nil {
		return nil, err
	}

	// Add transaction ID as UTF-8 bytes
	_, err = payload.WriteString(transactionID)
	if err != nil {
		return nil, err
	}

	// Add output index as 4-byte unsigned integer (little-endian)
	err = binary.Write(&payload, binary.LittleEndian, outputIndex)
	if err != nil {
		return nil, err
	}

	// Request type
	requestTypeInt := uint8(0)
	switch requestType {
	case pb.UtxoSwapRequestType_Fixed:
		requestTypeInt = uint8(0)
	case pb.UtxoSwapRequestType_MaxFee:
		requestTypeInt = uint8(1)
	case pb.UtxoSwapRequestType_Refund:
		requestTypeInt = uint8(2)
	}

	err = binary.Write(&payload, binary.LittleEndian, requestTypeInt)
	if err != nil {
		return nil, err
	}

	// Add credit amount as 8-byte unsigned integer (little-endian)
	err = binary.Write(&payload, binary.LittleEndian, creditAmountSats)
	if err != nil {
		return nil, err
	}

	// Add SSP signature as UTF-8 bytes
	_, err = payload.Write(sspSignature)
	if err != nil {
		return nil, err
	}

	// Hash the payload with SHA-256
	hash := sha256.Sum256(payload.Bytes())

	return hash[:], nil
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

		// Validate transfer is in a valid state for completion
		if transfer.Status == st.TransferStatusExpired || transfer.Status == st.TransferStatusReturned {
			return fmt.Errorf("transfer is expired or returned")
		}
		if transfer.Status == st.TransferStatusCompleted {
			return nil
		}
		// Only allow completion from valid intermediate states
		if transfer.Status != st.TransferStatusSenderKeyTweaked &&
			transfer.Status != st.TransferStatusReceiverKeyTweakApplied &&
			transfer.Status != st.TransferStatusReceiverRefundSigned {
			return fmt.Errorf("transfer cannot be completed from status %s", transfer.Status)
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

	messageHash, err := CreateUtxoSwapStatement(
		UtxoSwapStatementTypeRollback,
		hex.EncodeToString(req.OnChainUtxo.Txid),
		req.OnChainUtxo.Vout,
		common.Network(req.OnChainUtxo.Network),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create rollback utxo swap request statement: %w", err)
	}
	// Coordinator pubkey comes from the request, but it's fine because it will be checked against the DB.
	if err := verifySignature(req.CoordinatorPublicKey, req.Signature, messageHash); err != nil {
		logger.Debug("Rollback utxo swap request signature", "signature", hex.EncodeToString(req.Signature), "txid", hex.EncodeToString(req.OnChainUtxo.Txid), "vout", req.OnChainUtxo.Vout, "network", common.Network(req.OnChainUtxo.Network).String(), "coordinator", req.CoordinatorPublicKey, "message_hash", hex.EncodeToString(messageHash))
		return nil, fmt.Errorf("unable to verify coordinator signature: %w", err)
	}

	logger.Info("Cancelling UTXO swap", "txid", hex.EncodeToString(req.OnChainUtxo.Txid), "vout", req.OnChainUtxo.Vout)

	schemaNetwork, err := common.SchemaNetworkFromProtoNetwork(req.OnChainUtxo.Network)
	if err != nil {
		return nil, fmt.Errorf("unable to get schema network: %w", err)
	}
	targetUtxo, err := VerifiedTargetUtxo(ctx, config, db, schemaNetwork, req.OnChainUtxo.Txid, req.OnChainUtxo.Vout)
	if err != nil {
		return nil, err
	}

	utxoSwap, err := db.UtxoSwap.Query().
		Where(utxoswap.HasUtxoWith(utxo.IDEQ(targetUtxo.ID))).
		Where(utxoswap.Or(utxoswap.StatusEQ(st.UtxoSwapStatusCreated), utxoswap.StatusEQ(st.UtxoSwapStatusCompleted))).
		// The identity public key of the coordinator that created the utxo swap.
		// It's been verified above.
		Where(utxoswap.CoordinatorIdentityPublicKeyEQ(req.CoordinatorPublicKey)).
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

	logger.Info("UTXO swap cancelled", "utxo_swap_id", utxoSwap.ID, "txid", hex.EncodeToString(targetUtxo.Txid), "vout", targetUtxo.Vout)
	return &pbinternal.RollbackUtxoSwapResponse{}, nil
}

// verifySignature verifies that the signature is correct for the given message and public key
func verifySignature(publicKey []byte, signature []byte, messageHash []byte) error {
	// Parse the user's identity public key
	userPubKey, err := secp256k1.ParsePubKey(publicKey)
	if err != nil {
		return fmt.Errorf("failed to parse user identity public key: %w", err)
	}

	// Parse and verify the signature
	sig, err := ecdsa.ParseDERSignature(signature)
	if err != nil {
		return fmt.Errorf("failed to parse user signature: %w", err)
	}

	if !sig.Verify(messageHash[:], userPubKey) {
		return fmt.Errorf("invalid signature")
	}
	return nil
}

func CreateUtxoSwapStatement(statementType UtxoSwapStatementType, transactionID string, outputIndex uint32, network common.Network) ([]byte, error) {
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

	network, err := common.NetworkFromProtoNetwork(req.OnChainUtxo.Network)
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
	if err := verifySignature(req.CoordinatorPublicKey, req.Signature, messageHash); err != nil {
		return nil, fmt.Errorf("unable to verify coordinator signature: %w", err)
	}

	logger.Info("Marking UTXO swap as COMPLETED", "txid", hex.EncodeToString(req.OnChainUtxo.Txid), "vout", req.OnChainUtxo.Vout)

	schemaNetwork, err := common.SchemaNetworkFromProtoNetwork(req.OnChainUtxo.Network)
	if err != nil {
		return nil, fmt.Errorf("unable to get schema network: %w", err)
	}
	targetUtxo, err := VerifiedTargetUtxo(ctx, config, db, schemaNetwork, req.OnChainUtxo.Txid, req.OnChainUtxo.Vout)
	if err != nil {
		return nil, err
	}

	utxoSwap, err := db.UtxoSwap.Query().
		Where(utxoswap.HasUtxoWith(utxo.IDEQ(targetUtxo.ID))).
		Where(utxoswap.StatusEQ(st.UtxoSwapStatusCreated)).
		// The identity public key of the coordinator that created the utxo swap.
		// It's been verified above.
		Where(utxoswap.CoordinatorIdentityPublicKeyEQ(req.CoordinatorPublicKey)).
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get utxo swap: %w", err)
	}

	if err := CompleteUtxoSwap(ctx, utxoSwap); err != nil {
		return nil, fmt.Errorf("unable to complete utxo swap: %w", err)
	}

	logger.Info("UTXO swap marked as COMPLETED", "utxo_swap_id", utxoSwap.ID, "txid", hex.EncodeToString(targetUtxo.Txid), "vout", targetUtxo.Vout)
	return &pbinternal.UtxoSwapCompletedResponse{}, nil
}

func CreateCompleteSwapForUtxoRequest(config *so.Config, utxo *pb.UTXO) (*pbinternal.UtxoSwapCompletedRequest, error) {
	network, err := common.NetworkFromProtoNetwork(utxo.Network)
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
			logger.Error("Failed to connect to operator", "operator", operator.Identifier, "error", err)
			return nil, err
		}
		defer conn.Close()

		client := pbinternal.NewSparkInternalServiceClient(conn)
		internalResp, err := client.UtxoSwapCompleted(ctx, request)
		if err != nil {
			logger.Error("Failed to execute utxo swap completed task with operator", "operator", operator.Identifier, "error", err)
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

func CreateCreateSwapForUtxoRequest(config *so.Config, req *pb.InitiateUtxoSwapRequest) (*pbinternal.CreateUtxoSwapRequest, error) {
	createUtxoSwapRequestMessageHash, err := CreateUtxoSwapStatement(
		UtxoSwapStatementTypeCreated,
		hex.EncodeToString(req.OnChainUtxo.Txid),
		req.OnChainUtxo.Vout,
		common.Network(req.OnChainUtxo.Network),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create utxo swap statement: %w", err)
	}
	createUtxoSwapRequestSignature := ecdsa.Sign(config.IdentityPrivateKey.ToBTCEC(), createUtxoSwapRequestMessageHash)

	return &pbinternal.CreateUtxoSwapRequest{
		Request:              req,
		Signature:            createUtxoSwapRequestSignature.Serialize(),
		CoordinatorPublicKey: config.IdentityPublicKey().Serialize(),
	}, nil
}

func CreateSwapForUtxoWithOtherOperators(ctx context.Context, config *so.Config, request *pbinternal.CreateUtxoSwapRequest) error {
	logger := logging.GetLoggerFromContext(ctx)

	_, err := helper.ExecuteTaskWithAllOperators(ctx, config, &helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}, func(ctx context.Context, operator *so.SigningOperator) (any, error) {
		conn, err := operator.NewOperatorGRPCConnection()
		if err != nil {
			logger.Error("Failed to connect to operator", "operator", operator.Identifier, "error", err)
			return nil, err
		}
		defer conn.Close()

		client := pbinternal.NewSparkInternalServiceClient(conn)
		internalResp, err := client.CreateUtxoSwap(ctx, request)
		if err != nil {
			logger.Error("Failed to execute utxo swap completed task with operator", "operator", operator.Identifier, "error", err)
			return nil, err
		}
		return internalResp, err
	})
	return err
}

func (h *InternalDepositHandler) CreateSwapForAllOperators(ctx context.Context, config *so.Config, request *pbinternal.CreateUtxoSwapRequest) error {
	ctx, span := tracer.Start(ctx, "InternalDepositHandler.CreateSwapForAllOperators")
	defer span.End()

	// Try to complete with other operators first.
	if err := CreateSwapForUtxoWithOtherOperators(ctx, config, request); err != nil {
		return err
	}
	// If other operators return success, we can complete the swap in self.
	_, err := h.CreateUtxoSwap(ctx, config, request)
	return err
}

func (h *InternalDepositHandler) RollbackSwapForAllOperators(ctx context.Context, config *so.Config, request *pbinternal.CreateUtxoSwapRequest) error {
	logger := logging.GetLoggerFromContext(ctx)
	req := request.Request
	// Sign a statement that this coordinator is rolling back the utxo swap.
	rollbackUtxoSwapRequestMessageHash, err := CreateUtxoSwapStatement(
		UtxoSwapStatementTypeRollback,
		hex.EncodeToString(req.OnChainUtxo.Txid),
		req.OnChainUtxo.Vout,
		common.Network(req.OnChainUtxo.Network),
	)
	if err != nil {
		return fmt.Errorf("failed to create rollback utxo swap statement: %w", err)
	}
	rollbackUtxoSwapRequestSignature := ecdsa.Sign(config.IdentityPrivateKey.ToBTCEC(), rollbackUtxoSwapRequestMessageHash)
	logger.Debug("Rollback utxo swap request signature", "signature", hex.EncodeToString(rollbackUtxoSwapRequestSignature.Serialize()), "txid", hex.EncodeToString(req.OnChainUtxo.Txid), "vout", req.OnChainUtxo.Vout, "network", common.Network(req.OnChainUtxo.Network).String(), "coordinator", config.IdentityPublicKey(), "message_hash", hex.EncodeToString(rollbackUtxoSwapRequestMessageHash))
	allSelection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionAll}
	_, err = helper.ExecuteTaskWithAllOperators(ctx, config, &allSelection, func(ctx context.Context, operator *so.SigningOperator) (any, error) {
		conn, err := operator.NewOperatorGRPCConnection()
		if err != nil {
			logger.Error("Failed to connect to operator for rollback utxo swap", "error", err)
			return nil, err
		}
		defer conn.Close()

		client := pbinternal.NewSparkInternalServiceClient(conn)
		internalResp, err := client.RollbackUtxoSwap(ctx, &pbinternal.RollbackUtxoSwapRequest{
			CoordinatorPublicKey: config.IdentityPublicKey().Serialize(),
			Signature:            rollbackUtxoSwapRequestSignature.Serialize(),
			OnChainUtxo:          req.OnChainUtxo,
		})
		if err != nil {
			logger.Error("Failed to execute rollback utxo swap task with operator", "operator", operator.Identifier, "error", err, "txid", hex.EncodeToString(req.OnChainUtxo.Txid), "vout", req.OnChainUtxo.Vout)
			return nil, err
		}
		return internalResp, err
	})
	return err
}
