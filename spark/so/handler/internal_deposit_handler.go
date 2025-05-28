package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/rpcclient"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/logging"
	pbfrost "github.com/lightsparkdev/spark/proto/frost"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/depositaddress"
	"github.com/lightsparkdev/spark/so/ent/schema"
	"github.com/lightsparkdev/spark/so/ent/signingkeyshare"
	"github.com/lightsparkdev/spark/so/ent/utxo"
	"github.com/lightsparkdev/spark/so/ent/utxoswap"
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

	depositAddressMutator := ent.GetDbFromContext(ctx).DepositAddress.Create().
		SetSigningKeyshareID(keyshareID).
		SetOwnerIdentityPubkey(req.OwnerIdentityPublicKey).
		SetOwnerSigningPubkey(req.OwnerSigningPublicKey).
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

	signingKey := secp256k1.PrivKeyFromBytes(h.config.IdentityPrivateKey)
	addrHash := sha256.Sum256([]byte(req.Address))
	addressSignature := ecdsa.Sign(signingKey, addrHash[:])
	return &pbinternal.MarkKeyshareForDepositAddressResponse{
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

	db := ent.GetDbFromContext(ctx)
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
			treeMutator.SetStatus(schema.TreeStatusAvailable)
		} else {
			treeMutator.SetStatus(schema.TreeStatusPending)
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
		markNodeAsAvailable = tree.Status == schema.TreeStatusAvailable
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
			SetRawRefundTx(node.RawRefundTx)

		if node.ParentNodeId != nil {
			parentID, err := uuid.Parse(*node.ParentNodeId)
			if err != nil {
				return err
			}
			nodeMutator.SetParentID(parentID)
		}

		if markNodeAsAvailable {
			if len(node.RawRefundTx) > 0 {
				nodeMutator.SetStatus(schema.TreeNodeStatusAvailable)
			} else {
				nodeMutator.SetStatus(schema.TreeNodeStatusSplitted)
			}
		} else {
			nodeMutator.SetStatus(schema.TreeNodeStatusCreating)
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
//   - The UTXO is paid to a registered static deposit address and is confirmed on the blockchain
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
	logger := logging.GetLoggerFromContext(ctx)
	req := reqWithSignature.Request
	logger.Info("Start CreateUtxoSwap request for on-chain utxo", "txid", hex.EncodeToString(req.OnChainUtxo.Txid), "vout", req.OnChainUtxo.Vout)

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
	coordinatorIsSO := false
	for _, op := range config.SigningOperatorMap {
		if bytes.Equal(op.IdentityPublicKey, reqWithSignature.CoordinatorPublicKey) {
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

	db := ent.GetDbFromContext(ctx)
	schemaNetwork, err := common.SchemaNetworkFromProtoNetwork(req.OnChainUtxo.Network)
	if err != nil {
		return nil, err
	}

	targetUtxo, err := VerifiedTargetUtxo(ctx, db, schemaNetwork, req.OnChainUtxo.Txid, req.OnChainUtxo.Vout)
	if err != nil {
		return nil, err
	}

	totalAmount := uint64(0)
	leafRefundMap := make(map[string][]byte)
	quoteSigningBytes := req.SspSignature
	if req.RequestType == pb.UtxoSwapRequestType_Fixed {
		// *** Validate fixed amount request ***

		// Validate general transfer signatures and leaves
		if err = validateTransfer(ctx, config, req.Transfer); err != nil {
			return nil, fmt.Errorf("transfer validation failed: %v", err)
		}

		// Validate user signature, receiver identitypubkey and amount in transfer
		for _, transaction := range req.Transfer.LeavesToSend {
			leafRefundMap[transaction.LeafId] = transaction.RawTx
		}
		leaves, err := loadLeavesWithLock(ctx, db, leafRefundMap)
		if err != nil {
			return nil, fmt.Errorf("unable to load leaves: %v", err)
		}
		totalAmount := getTotalTransferValue(leaves)
		if err = validateUserSignature(req.Transfer.ReceiverIdentityPublicKey, req.UserSignature, req.SspSignature, req.RequestType, network, targetUtxo.Txid, targetUtxo.Vout, totalAmount); err != nil {
			return nil, fmt.Errorf("user signature validation failed: %v", err)
		}
	} else if req.RequestType == pb.UtxoSwapRequestType_Refund {
		// *** Validate refund request ***

		if req.Transfer.OwnerIdentityPublicKey == nil {
			return nil, fmt.Errorf("owner identity public key is required")
		}

		if req.Transfer.ReceiverIdentityPublicKey == nil {
			return nil, fmt.Errorf("receiver identity public key is required")
		}

		spendTxSighash, totalAmount, err := GetTxSigningInfo(ctx, targetUtxo, req.SpendTxSigningJob.RawTx)
		if err != nil {
			return nil, fmt.Errorf("failed to get spend tx sighash: %v", err)
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
			uint64(totalAmount)); err != nil {
			return nil, fmt.Errorf("user signature validation failed: %v", err)
		}
		quoteSigningBytes = spendTxSighash
	}

	// Check that the utxo swap is not already registered
	utxoSwap, err := db.UtxoSwap.Query().
		Where(utxoswap.HasUtxoWith(utxo.IDEQ(targetUtxo.ID))).
		Where(utxoswap.StatusNEQ(schema.UtxoSwapStatusCancelled)).
		First(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return nil, fmt.Errorf("unable to check if utxo swap is already registered: %w", err)
	}
	if utxoSwap != nil {
		return nil, fmt.Errorf("utxo swap is already registered")
	}

	logger.Info(
		"Creating UTXO swap record",
		"user_identity_public_key", hex.EncodeToString(req.Transfer.ReceiverIdentityPublicKey),
		"txid", hex.EncodeToString(targetUtxo.Txid),
		"vout", targetUtxo.Vout,
		"network", network,
		"credit_amount_sats", totalAmount,
	)

	// Create a utxo swap record and then a transfer. We rely on DbSessionMiddleware to
	// ensure that all db inserts are rolled back in case of an error.
	utxoSwap, err = db.UtxoSwap.Create().
		SetStatus(schema.UtxoSwapStatusCreated).
		// utxo
		SetUtxo(targetUtxo).
		// quote
		SetRequestType(schema.UtxoSwapFromProtoRequestType(req.RequestType)).
		SetCreditAmountSats(totalAmount).
		// quote signing bytes are the sighash of the spend tx if SSP is not used
		SetSspSignature(quoteSigningBytes).
		SetSspIdentityPublicKey(req.Transfer.OwnerIdentityPublicKey).
		// authorization from a user to claim this utxo after fulfilling the quote
		SetUserSignature(req.UserSignature).
		SetUserIdentityPublicKey(req.Transfer.ReceiverIdentityPublicKey).
		// Identity of the owner who can cancel this swap (if it's not yet completed), normally -- the coordinator SO
		SetCoordinatorIdentityPublicKey(reqWithSignature.CoordinatorPublicKey).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to store utxo swap: %w", err)
	}

	depositAddress, err := targetUtxo.QueryDepositAddress().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get utxo deposit address: %w", err)
	}
	_, err = db.DepositAddress.UpdateOneID(depositAddress.ID).AddUtxoswaps(utxoSwap).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to add utxo swap to deposit address: %w", err)
	}
	if !bytes.Equal(depositAddress.OwnerIdentityPubkey, req.Transfer.ReceiverIdentityPublicKey) {
		return nil, fmt.Errorf("transfer is not to the recepient of the deposit")
	}

	transferProto := &pb.Transfer{}
	if req.RequestType == pb.UtxoSwapRequestType_Fixed {
		// Validate and create a transfer to the user.
		// Validates that the leaves are to the desired destination pubkey.
		// This will send the leaves from the SSP to the user and create a transfer record in the database.
		// The leaves will be locked until the utxo swap is finalized.
		transferHandler := NewTransferHandler(config)
		transfer, _, err := transferHandler.createTransfer(
			ctx,
			req.Transfer.TransferId,
			schema.TransferTypeUtxoSwap,
			req.Transfer.ExpiryTime.AsTime(),
			req.Transfer.OwnerIdentityPublicKey,
			req.Transfer.ReceiverIdentityPublicKey,
			leafRefundMap,
			nil,
			TransferRoleCoordinator,
		)
		if err != nil {
			// if transfer creation fails, the utxo swap insert will be rolled back
			return nil, fmt.Errorf("unable to create transfer: %v", err)
		}
		transferProto, err = transfer.MarshalProto(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to marshal transfer: %v", err)
		}

		_, err = db.UtxoSwap.UpdateOneID(utxoSwap.ID).
			SetTransfer(transfer).
			Save(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to update utxo swap: %v", err)
		}
	}

	return &pbinternal.CreateUtxoSwapResponse{
		UtxoDepositAddress: depositAddress.Address,
		Transfer:           transferProto,
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
//   - the transfer is authorized by the user
//   - the leaves are valid, AVAILABLE and the user (SSP) has signed them with valid signatures (proof of ownership)
func validateTransfer(ctx context.Context, config *so.Config, transferRequest *pb.StartUserSignedTransferRequest) error {
	if transferRequest == nil {
		return fmt.Errorf("transferRequest is required")
	}

	if len(transferRequest.LeavesToSend) == 0 {
		return fmt.Errorf("at least one leaf must be provided")
	}

	if transferRequest.OwnerIdentityPublicKey == nil {
		return fmt.Errorf("owner identity public key is required")
	}

	if transferRequest.ReceiverIdentityPublicKey == nil {
		return fmt.Errorf("receiver identity public key is required")
	}

	conn, err := common.NewGRPCConnectionWithoutTLS(config.SignerAddress, nil)
	if err != nil {
		return fmt.Errorf("unable to connect to signer: %w", err)
	}
	defer conn.Close()

	db := ent.GetDbFromContext(ctx)

	client := pbfrost.NewFrostServiceClient(conn)
	for _, transaction := range transferRequest.LeavesToSend {
		if transaction == nil {
			return fmt.Errorf("transaction is nil")
		}
		if transaction.SigningCommitments == nil {
			return fmt.Errorf("signing commitments is nil")
		}
		if transaction.SigningNonceCommitment == nil {
			return fmt.Errorf("signing nonce commitment is nil")
		}
		// First fetch the node tx in order to calculate the sighash
		nodeID, err := uuid.Parse(transaction.LeafId)
		if err != nil {
			return fmt.Errorf("unable to parse node id: %w", err)
		}
		node, err := db.TreeNode.Get(ctx, nodeID)
		if err != nil {
			return fmt.Errorf("unable to get node: %w", err)
		}
		if node.Status != schema.TreeNodeStatusAvailable {
			return fmt.Errorf("node %v is not available: %v", node.ID, node.Status)
		}
		// check that the keyshare exists
		_, err = node.QuerySigningKeyshare().First(ctx)
		if err != nil {
			return fmt.Errorf("unable to get keyshare: %w", err)
		}
		tx, err := common.TxFromRawTxBytes(node.RawTx)
		if err != nil {
			return fmt.Errorf("unable to get tx: %w", err)
		}
		if len(tx.TxOut) <= 0 {
			return fmt.Errorf("tx vout out of bounds")
		}

		refundTx, err := common.TxFromRawTxBytes(transaction.RawTx)
		if err != nil {
			return fmt.Errorf("unable to get refund tx: %w", err)
		}
		if len(refundTx.TxOut) <= 0 {
			return fmt.Errorf("refund tx vout out of bounds")
		}

		sighash, err := common.SigHashFromTx(refundTx, 0, tx.TxOut[0])
		if err != nil {
			return fmt.Errorf("unable to get sighash for refund tx: %w", err)
		}

		// Validate that the user's signature for the refund transaction is valid.
		// The User won't be able to sign the node if he does not own it.
		_, err = client.ValidateSignatureShare(ctx, &pbfrost.ValidateSignatureShareRequest{
			Message:         sighash,
			SignatureShare:  transaction.UserSignature,
			Role:            pbfrost.SigningRole_USER,
			VerifyingKey:    node.VerifyingPubkey,
			PublicShare:     node.OwnerSigningPubkey,
			Commitments:     transaction.SigningCommitments.SigningCommitments,
			UserCommitments: transaction.SigningNonceCommitment,
		})
		if err != nil {
			return fmt.Errorf("unable to validate signature share: %w, for sighash: %v, user pubkey: %v", err, hex.EncodeToString(sighash), hex.EncodeToString(node.OwnerSigningPubkey))
		}
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
	err = binary.Write(&payload, binary.LittleEndian, uint64(creditAmountSats))
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

func (h *InternalDepositHandler) RollbackUtxoSwap(ctx context.Context, _ *so.Config, req *pbinternal.RollbackUtxoSwapRequest) (*pbinternal.RollbackUtxoSwapResponse, error) {
	logger := logging.GetLoggerFromContext(ctx)
	db := ent.GetDbFromContext(ctx)

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
		return nil, fmt.Errorf("unable to verify coordinator signature: %w", err)
	}

	logger.Info("Cancelling UTXO swap", "txid", hex.EncodeToString(req.OnChainUtxo.Txid), "vout", req.OnChainUtxo.Vout)

	schemaNetwork, err := common.SchemaNetworkFromProtoNetwork(req.OnChainUtxo.Network)
	if err != nil {
		return nil, fmt.Errorf("unable to get schema network: %w", err)
	}
	targetUtxo, err := VerifiedTargetUtxo(ctx, db, schemaNetwork, req.OnChainUtxo.Txid, req.OnChainUtxo.Vout)
	if err != nil {
		return nil, err
	}

	utxoSwap, err := db.UtxoSwap.Query().
		Where(utxoswap.HasUtxoWith(utxo.IDEQ(targetUtxo.ID))).
		Where(utxoswap.Or(utxoswap.StatusEQ(schema.UtxoSwapStatusCreated), utxoswap.StatusEQ(schema.UtxoSwapStatusCompleted))).
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

	if utxoSwap.Status == schema.UtxoSwapStatusCompleted {
		return nil, fmt.Errorf("utxo swap is already completed")
	}

	if utxoSwap.Status == schema.UtxoSwapStatusCompleted {
		return nil, fmt.Errorf("utxo swap is already completed")
	}
	if utxoSwap.RequestType != schema.UtxoSwapRequestTypeRefund {
		transfer, err := utxoSwap.QueryTransfer().Only(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to get transfer: %v", err)
		}
		baseHandler := NewBaseTransferHandler(h.config)
		_, err = baseHandler.CancelTransfer(ctx, &pb.CancelTransferRequest{
			TransferId:              transfer.ID.String(),
			SenderIdentityPublicKey: transfer.SenderIdentityPubkey,
		}, CancelTransferIntentTask)
		if err != nil {
			return nil, fmt.Errorf("unable to cancel transfer: %w", err)
		}
	}

	_, err = utxoSwap.Update().
		SetStatus(schema.UtxoSwapStatusCancelled).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to update utxo swap status to CANCELLED: %w", err)
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

func CreateUtxoSwapStatement(
	statementType UtxoSwapStatementType,
	transactionID string,
	outputIndex uint32,
	network common.Network,
) ([]byte, error) {
	// Create a buffer to hold all the data
	var payload bytes.Buffer

	// Add action name
	_, err := payload.WriteString(string(statementType.String()))
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

	// Request type fixed amount
	err = binary.Write(&payload, binary.LittleEndian, uint8(0))
	if err != nil {
		return nil, err
	}

	// Hash the payload with SHA-256
	hash := sha256.Sum256(payload.Bytes())

	return hash[:], nil
}

func (h *InternalDepositHandler) UtxoSwapCompleted(ctx context.Context, _ *so.Config, req *pbinternal.UtxoSwapCompletedRequest) (*pbinternal.UtxoSwapCompletedResponse, error) {
	logger := logging.GetLoggerFromContext(ctx)
	db := ent.GetDbFromContext(ctx)

	messageHash, err := CreateUtxoSwapStatement(
		UtxoSwapStatementTypeCompleted,
		hex.EncodeToString(req.OnChainUtxo.Txid),
		req.OnChainUtxo.Vout,
		common.Network(req.OnChainUtxo.Network),
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
	targetUtxo, err := VerifiedTargetUtxo(ctx, db, schemaNetwork, req.OnChainUtxo.Txid, req.OnChainUtxo.Vout)
	if err != nil {
		return nil, err
	}

	utxoSwap, err := db.UtxoSwap.Query().
		Where(utxoswap.HasUtxoWith(utxo.IDEQ(targetUtxo.ID))).
		Where(utxoswap.StatusEQ(schema.UtxoSwapStatusCreated)).
		// The identity public key of the coordinator that created the utxo swap.
		// It's been verified above.
		Where(utxoswap.CoordinatorIdentityPublicKeyEQ(req.CoordinatorPublicKey)).
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get utxo swap: %w", err)
	}

	_, err = utxoSwap.Update().
		SetStatus(schema.UtxoSwapStatusCompleted).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to update utxo swap status to COMPLETED: %w", err)
	}

	logger.Info("UTXO swap marked as COMPLETED", "utxo_swap_id", utxoSwap.ID, "txid", hex.EncodeToString(targetUtxo.Txid), "vout", targetUtxo.Vout)

	return &pbinternal.UtxoSwapCompletedResponse{}, nil
}
