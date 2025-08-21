package handler

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"

	"github.com/lightsparkdev/spark/common/keys"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/logging"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/utxo"
	"github.com/lightsparkdev/spark/so/ent/utxoswap"
)

type StaticDepositInternalHandler struct {
	config *so.Config
}

func NewStaticDepositInternalHandler(config *so.Config) *StaticDepositInternalHandler {
	return &StaticDepositInternalHandler{config: config}
}

// Creates a new UTXO swap record and a transfer record to a user.
// The function performs the following steps:
// 1. Validates the request by checking:
//   - The network is supported
//   - The UTXO is paid to a registered static deposit address that belongs to the receiver of the transfer and
//     is confirmed on the blockchain with required number of confirmations
//   - The user signature is valid
//   - Check that the utxo swap is not already registered
//   - The leaves are valid, AVAILABLE and the user (SSP) has signed them with valid signatures (proof of ownership)
//   - UTXO deposit address is static and belongs to the receiver of the transfer
//   - The deposit key provided by the user matches what's in the DB.
//
// 2. Creates a UTXO swap record in the database with status CREATED
// 3. Adds the utxo swap to the deposit address
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
func (h *StaticDepositInternalHandler) CreateStaticDepositUtxoSwap(ctx context.Context, config *so.Config, reqWithSignature *pbinternal.CreateStaticDepositUtxoSwapRequest) (*pbinternal.CreateStaticDepositUtxoSwapResponse, error) {
	ctx, span := tracer.Start(ctx, "StaticDepositInternalHandler.CreateStaticDepositUtxoSwap")
	defer span.End()

	logger := logging.GetLoggerFromContext(ctx)
	req := reqWithSignature.Request
	logger.Info("Start CreateStaticDepositUtxoSwap request for on-chain utxo", "txid", req.OnChainUtxo.Txid)

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
		return nil, fmt.Errorf("failed to get db: %w", err)
	}
	schemaNetwork, err := common.SchemaNetworkFromProtoNetwork(req.OnChainUtxo.Network)
	if err != nil {
		return nil, err
	}

	targetUtxo, err := VerifiedTargetUtxo(ctx, config, db, schemaNetwork, req.OnChainUtxo.Txid, req.OnChainUtxo.Vout)
	if err != nil {
		return nil, err
	}

	// Check that the utxo swap is not already registered
	utxoSwap, err := db.UtxoSwap.Query().
		Where(utxoswap.HasUtxoWith(utxo.IDEQ(targetUtxo.ID))).
		Where(utxoswap.StatusNEQ(st.UtxoSwapStatusCancelled)).
		Only(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return nil, fmt.Errorf("unable to check if utxo swap is already registered: %w", err)
	}
	if utxoSwap != nil {
		return nil, fmt.Errorf("utxo swap is already registered")
	}

	// Check that the utxo deposit address is static and belongs to the receiver of the transfer
	depositAddress, err := targetUtxo.QueryDepositAddress().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get utxo deposit address: %w", err)
	}
	if !depositAddress.IsStatic {
		return nil, fmt.Errorf("unable to claim a deposit to a non-static address: %w", err)
	}
	if !bytes.Equal(depositAddress.OwnerIdentityPubkey, req.Transfer.ReceiverIdentityPublicKey) {
		return nil, fmt.Errorf("transfer is not to the recepient of the deposit")
	}
	// Validate that the deposit key provided by the user matches what's in the DB.
	// SSP should generate the deposit public key from a deposit secret key provided by the customer.
	if !bytes.Equal(depositAddress.OwnerSigningPubkey, req.SpendTxSigningJob.SigningPublicKey) {
		return nil, fmt.Errorf("deposit address owner signing pubkey does not match the signing public key")
	}

	// Validate general transfer signatures and leaves
	if err = validateTransfer(req.Transfer); err != nil {
		return nil, fmt.Errorf("transfer validation failed: %w", err)
	}

	transferHandler := NewBaseTransferHandler(h.config)
	totalAmount := uint64(0)
	quoteSigningBytes := req.SspSignature

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
	if len(leaves) == 0 {
		return nil, fmt.Errorf("no leaves found")
	}
	transferNetwork := leaves[0].QueryTree().OnlyX(ctx).Network
	if transferNetwork != common.SchemaNetwork(network) {
		return nil, fmt.Errorf("transfer network %s does not match utxo network %s", transferNetwork, network)
	}
	totalAmount = getTotalTransferValue(leaves)
	if err = validateUserSignature(req.Transfer.ReceiverIdentityPublicKey, req.UserSignature, req.SspSignature, pb.UtxoSwapRequestType_Fixed, network, targetUtxo.Txid, targetUtxo.Vout, totalAmount); err != nil {
		return nil, fmt.Errorf("user signature validation failed: %w", err)
	}

	logger.Info(
		"Creating UTXO swap record",
		"request_type", pb.UtxoSwapRequestType_Fixed,
		"transfer_id", req.Transfer.TransferId,
		"user_identity_public_key", hex.EncodeToString(req.Transfer.ReceiverIdentityPublicKey),
		"txid", hex.EncodeToString(targetUtxo.Txid),
		"vout", targetUtxo.Vout,
		"network", network,
		"credit_amount_sats", totalAmount,
	)

	// Create a utxo swap record and then a transfer. We rely on DbSessionMiddleware to
	// ensure that all db inserts are rolled back in case of an error.

	transferUUID, err := uuid.Parse(req.Transfer.TransferId)
	if err != nil {
		return nil, fmt.Errorf("unable to parse transfer_id as a uuid %s: %w", req.Transfer.TransferId, err)
	}
	utxoSwap, err = db.UtxoSwap.Create().
		SetStatus(st.UtxoSwapStatusCreated).
		// utxo
		SetUtxo(targetUtxo).
		// quote
		SetRequestType(st.UtxoSwapFromProtoRequestType(pb.UtxoSwapRequestType_Fixed)).
		SetCreditAmountSats(totalAmount).
		// quote signing bytes are the sighash of the spend tx if SSP is not used
		SetSspSignature(quoteSigningBytes).
		SetSspIdentityPublicKey(req.Transfer.OwnerIdentityPublicKey).
		// authorization from a user to claim this utxo after fulfilling the quote
		SetUserSignature(req.UserSignature).
		SetUserIdentityPublicKey(req.Transfer.ReceiverIdentityPublicKey).
		SetCoordinatorIdentityPublicKey(reqWithSignature.CoordinatorPublicKey).
		SetRequestedTransferID(transferUUID).
		Save(ctx)
	if err != nil {
		logger.Error("Failed to create utxo swap", "error", err, "txid", hex.EncodeToString(targetUtxo.Txid), "vout", targetUtxo.Vout, "Operator", config.Identifier)
		return nil, fmt.Errorf("unable to store utxo swap: %w", err)
	}
	// Add the utxo swap to the deposit address
	_, err = db.DepositAddress.UpdateOneID(depositAddress.ID).AddUtxoswaps(utxoSwap).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to add utxo swap to deposit address: %w", err)
	}

	return &pbinternal.CreateStaticDepositUtxoSwapResponse{
		UtxoDepositAddress: depositAddress.Address,
	}, nil
}

func (h *StaticDepositInternalHandler) CreateStaticDepositUtxoRefund(ctx context.Context, config *so.Config, reqWithSignature *pbinternal.CreateStaticDepositUtxoRefundRequest) (*pbinternal.CreateStaticDepositUtxoRefundResponse, error) {
	ctx, span := tracer.Start(ctx, "StaticDepositInternalHandler.CreateStaticDepositUtxoRefund")
	defer span.End()

	logger := logging.GetLoggerFromContext(ctx)
	req := reqWithSignature.Request
	logger.Info("Start CreateStaticDepositUtxoRefund request for on-chain utxo", "txid", req.OnChainUtxo.Txid)

	// Verify CoordinatorPublicKey is correct.
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
		return nil, fmt.Errorf("failed to get db: %w", err)
	}
	schemaNetwork, err := common.SchemaNetworkFromProtoNetwork(req.OnChainUtxo.Network)
	if err != nil {
		return nil, err
	}

	// Validate UTXO
	targetUtxo, err := VerifiedTargetUtxo(ctx, config, db, schemaNetwork, req.OnChainUtxo.Txid, req.OnChainUtxo.Vout)
	if err != nil {
		return nil, err
	}

	// Validate Deposit Address ownership
	depositAddress, err := targetUtxo.QueryDepositAddress().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get utxo deposit address: %w", err)
	}
	if !depositAddress.IsStatic {
		return nil, fmt.Errorf("unable to claim a deposit to a non-static address: %w", err)
	}

	spendTxSighash, totalAmount, err := GetTxSigningInfo(ctx, targetUtxo, req.RefundTxSigningJob.RawTx)
	if err != nil {
		return nil, fmt.Errorf("failed to get spend tx sighash: %w", err)
	}

	// Check that the utxo swap is not already registered
	utxoSwap, err := db.UtxoSwap.Query().
		Where(utxoswap.HasUtxoWith(utxo.IDEQ(targetUtxo.ID))).
		Where(utxoswap.StatusNEQ(st.UtxoSwapStatusCancelled)).
		First(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return nil, fmt.Errorf("unable to check if utxo swap is already registered: %w", err)
	}
	if utxoSwap != nil {
		return nil, fmt.Errorf("utxo swap is already registered")
	}

	// Validate user statement
	if err = validateUserSignature(depositAddress.OwnerIdentityPubkey, req.UserSignature, spendTxSighash, pb.UtxoSwapRequestType_Refund, network, targetUtxo.Txid, targetUtxo.Vout, totalAmount); err != nil {
		return nil, fmt.Errorf("user signature validation failed: %w", err)
	}

	logger.Info(
		"Creating UTXO swap record",
		"request_type", pb.UtxoSwapRequestType_Refund,
		"user_identity_public_key", hex.EncodeToString(depositAddress.OwnerIdentityPubkey),
		"txid", hex.EncodeToString(targetUtxo.Txid),
		"vout", targetUtxo.Vout,
		"network", network,
		"credit_amount_sats", totalAmount,
	)

	utxoSwap, err = db.UtxoSwap.Create().
		SetStatus(st.UtxoSwapStatusCreated).
		// utxo
		SetUtxo(targetUtxo).
		// quote
		SetRequestType(st.UtxoSwapFromProtoRequestType(pb.UtxoSwapRequestType_Refund)).
		SetCreditAmountSats(totalAmount).
		// quote signing bytes are the sighash of the spend tx if SSP is not used
		SetSspSignature(spendTxSighash).
		SetSspIdentityPublicKey(depositAddress.OwnerIdentityPubkey).
		SetUserIdentityPublicKey(depositAddress.OwnerIdentityPubkey).
		SetCoordinatorIdentityPublicKey(reqWithSignature.CoordinatorPublicKey).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to store utxo swap: %w", err)
	}

	_, err = db.DepositAddress.UpdateOneID(depositAddress.ID).AddUtxoswaps(utxoSwap).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to add utxo swap to deposit address: %w", err)
	}

	return &pbinternal.CreateStaticDepositUtxoRefundResponse{
		UtxoDepositAddress: depositAddress.Address,
	}, nil
}
