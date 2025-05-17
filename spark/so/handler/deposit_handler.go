package handler

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"

	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/logging"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authz"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/depositaddress"
	"github.com/lightsparkdev/spark/so/ent/schema"
	"github.com/lightsparkdev/spark/so/ent/utxo"
	"github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/helper"
	"github.com/lightsparkdev/spark/so/objects"
)

// The DepositHandler is responsible for handling deposit related requests.
type DepositHandler struct {
	config *so.Config
	db     *ent.Client
}

// NewDepositHandler creates a new DepositHandler.
func NewDepositHandler(config *so.Config, db *ent.Client) *DepositHandler {
	return &DepositHandler{
		config: config,
		db:     db,
	}
}

// GenerateDepositAddress generates a deposit address for the given public key.
func (o *DepositHandler) GenerateDepositAddress(ctx context.Context, config *so.Config, req *pb.GenerateDepositAddressRequest) (*pb.GenerateDepositAddressResponse, error) {
	ctx, span := tracer.Start(ctx, "DepositHandler.GenerateDepositAddress")
	defer span.End()

	network, err := common.NetworkFromProtoNetwork(req.Network)
	logger := logging.GetLoggerFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if !config.IsNetworkSupported(network) {
		return nil, fmt.Errorf("network not supported")
	}
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, o.config, req.IdentityPublicKey); err != nil {
		return nil, err
	}
	logger.Info("Generating deposit address for public key", "public_key", hex.EncodeToString(req.SigningPublicKey), "identity_public_key", hex.EncodeToString(req.IdentityPublicKey))
	keyshares, err := ent.GetUnusedSigningKeyshares(ctx, o.db, config, 1)
	if err != nil {
		return nil, err
	}

	if len(keyshares) == 0 {
		return nil, fmt.Errorf("no keyshares available")
	}

	keyshare := keyshares[0]

	selection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}
	_, err = helper.ExecuteTaskWithAllOperators(ctx, config, &selection, func(ctx context.Context, operator *so.SigningOperator) (interface{}, error) {
		conn, err := operator.NewGRPCConnection()
		if err != nil {
			return nil, err
		}
		defer conn.Close()

		client := pbinternal.NewSparkInternalServiceClient(conn)
		_, err = client.MarkKeysharesAsUsed(ctx, &pbinternal.MarkKeysharesAsUsedRequest{KeyshareId: []string{keyshare.ID.String()}})
		return nil, err
	})
	if err != nil {
		return nil, err
	}

	combinedPublicKey, err := common.AddPublicKeys(keyshare.PublicKey, req.SigningPublicKey)
	if err != nil {
		return nil, err
	}
	depositAddress, err := common.P2TRAddressFromPublicKey(combinedPublicKey, network)
	if err != nil {
		return nil, err
	}

	depositAddressMutator := ent.GetDbFromContext(ctx).DepositAddress.Create().
		SetSigningKeyshareID(keyshare.ID).
		SetOwnerIdentityPubkey(req.IdentityPublicKey).
		SetOwnerSigningPubkey(req.SigningPublicKey).
		SetAddress(*depositAddress)
	// Confirmation height is not set since nothing has been confirmed yet.

	if req.IsStatic != nil && *req.IsStatic {
		depositAddressMutator.SetIsStatic(true)
	}

	if req.LeafId != nil {
		leafID, err := uuid.Parse(*req.LeafId)
		if err != nil {
			return nil, err
		}
		depositAddressMutator.SetNodeID(leafID)
	}

	_, err = depositAddressMutator.Save(ctx)
	if err != nil {
		return nil, err
	}

	response, err := helper.ExecuteTaskWithAllOperators(ctx, config, &selection, func(ctx context.Context, operator *so.SigningOperator) ([]byte, error) {
		conn, err := operator.NewGRPCConnection()
		if err != nil {
			return nil, err
		}
		defer conn.Close()

		client := pbinternal.NewSparkInternalServiceClient(conn)
		response, err := client.MarkKeyshareForDepositAddress(ctx, &pbinternal.MarkKeyshareForDepositAddressRequest{
			KeyshareId:             keyshare.ID.String(),
			Address:                *depositAddress,
			OwnerIdentityPublicKey: req.IdentityPublicKey,
			OwnerSigningPublicKey:  req.SigningPublicKey,
			IsStatic:               req.IsStatic,
		})
		if err != nil {
			return nil, err
		}
		return response.AddressSignature, nil
	})
	if err != nil {
		return nil, err
	}

	verifyingKeyBytes, err := common.AddPublicKeys(keyshare.PublicKey, req.SigningPublicKey)
	if err != nil {
		return nil, err
	}

	msg := common.ProofOfPossessionMessageHashForDepositAddress(req.IdentityPublicKey, keyshare.PublicKey, []byte(*depositAddress))
	proofOfPossessionSignature, err := helper.GenerateProofOfPossessionSignatures(ctx, config, [][]byte{msg}, []*ent.SigningKeyshare{keyshare})
	if err != nil {
		return nil, err
	}
	return &pb.GenerateDepositAddressResponse{
		DepositAddress: &pb.Address{
			Address:      *depositAddress,
			VerifyingKey: verifyingKeyBytes,
			DepositAddressProof: &pb.DepositAddressProof{
				AddressSignatures:          response,
				ProofOfPossessionSignature: proofOfPossessionSignature[0],
			},
			IsStatic: req.IsStatic != nil && *req.IsStatic,
		},
	}, nil
}

func (o *DepositHandler) StartTreeCreation(ctx context.Context, config *so.Config, req *pb.StartTreeCreationRequest) (*pb.StartTreeCreationResponse, error) {
	ctx, span := tracer.Start(ctx, "DepositHandler.StartTreeCreation")
	defer span.End()

	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, o.config, req.IdentityPublicKey); err != nil {
		return nil, err
	}
	// Get the on chain tx
	onChainTx, err := common.TxFromRawTxBytes(req.OnChainUtxo.RawTx)
	if err != nil {
		return nil, err
	}
	if len(onChainTx.TxOut) <= int(req.OnChainUtxo.Vout) {
		return nil, fmt.Errorf("utxo index out of bounds")
	}

	// Verify that the on chain utxo is paid to the registered deposit address
	if len(onChainTx.TxOut) <= int(req.OnChainUtxo.Vout) {
		return nil, fmt.Errorf("utxo index out of bounds")
	}
	onChainOutput := onChainTx.TxOut[req.OnChainUtxo.Vout]
	network, err := common.NetworkFromProtoNetwork(req.OnChainUtxo.Network)
	if err != nil {
		return nil, err
	}
	if !config.IsNetworkSupported(network) {
		return nil, fmt.Errorf("network not supported")
	}
	utxoAddress, err := common.P2TRAddressFromPkScript(onChainOutput.PkScript, network)
	if err != nil {
		return nil, err
	}
	db := ent.GetDbFromContext(ctx)
	depositAddress, err := db.DepositAddress.Query().Where(depositaddress.Address(*utxoAddress)).First(ctx)
	if err != nil {
		return nil, err
	}
	if depositAddress == nil || !bytes.Equal(depositAddress.OwnerIdentityPubkey, req.IdentityPublicKey) {
		return nil, fmt.Errorf("deposit address not found for address: %s", *utxoAddress)
	}
	if !bytes.Equal(depositAddress.OwnerSigningPubkey, req.RootTxSigningJob.SigningPublicKey) || !bytes.Equal(depositAddress.OwnerSigningPubkey, req.RefundTxSigningJob.SigningPublicKey) {
		return nil, fmt.Errorf("unexpected signing public key")
	}
	txConfirmed := depositAddress.ConfirmationHeight != 0

	if txConfirmed && depositAddress.ConfirmationTxid != "" {
		onChainTxid := onChainTx.TxHash().String()
		if onChainTxid != depositAddress.ConfirmationTxid {
			return nil, fmt.Errorf("transaction ID does not match confirmed transaction ID")
		}
	}

	// Verify the root transaction
	rootTx, err := common.TxFromRawTxBytes(req.RootTxSigningJob.RawTx)
	if err != nil {
		return nil, err
	}
	err = o.verifyRootTransaction(rootTx, onChainTx, req.OnChainUtxo.Vout)
	if err != nil {
		return nil, err
	}
	rootTxSigHash, err := common.SigHashFromTx(rootTx, 0, onChainOutput)
	if err != nil {
		return nil, err
	}

	// Verify the refund transaction
	refundTx, err := common.TxFromRawTxBytes(req.RefundTxSigningJob.RawTx)
	if err != nil {
		return nil, err
	}
	err = o.verifyRefundTransaction(rootTx, refundTx)
	if err != nil {
		return nil, err
	}
	if len(rootTx.TxOut) <= 0 {
		return nil, fmt.Errorf("vout out of bounds, root tx has no outputs")
	}
	refundTxSigHash, err := common.SigHashFromTx(refundTx, 0, rootTx.TxOut[0])
	if err != nil {
		return nil, err
	}

	// Sign the root and refund transactions
	signingKeyShare, err := depositAddress.QuerySigningKeyshare().Only(ctx)
	if err != nil {
		return nil, err
	}
	verifyingKeyBytes, err := common.AddPublicKeys(signingKeyShare.PublicKey, depositAddress.OwnerSigningPubkey)
	if err != nil {
		return nil, err
	}

	signingJobs := make([]*helper.SigningJob, 0)
	userRootTxNonceCommitment, err := objects.NewSigningCommitment(req.RootTxSigningJob.SigningNonceCommitment.Binding, req.RootTxSigningJob.SigningNonceCommitment.Hiding)
	if err != nil {
		return nil, err
	}
	userRefundTxNonceCommitment, err := objects.NewSigningCommitment(req.RefundTxSigningJob.SigningNonceCommitment.Binding, req.RefundTxSigningJob.SigningNonceCommitment.Hiding)
	if err != nil {
		return nil, err
	}
	signingJobs = append(
		signingJobs,
		&helper.SigningJob{
			JobID:             uuid.New().String(),
			SigningKeyshareID: signingKeyShare.ID,
			Message:           rootTxSigHash,
			VerifyingKey:      verifyingKeyBytes,
			UserCommitment:    userRootTxNonceCommitment,
		},
		&helper.SigningJob{
			JobID:             uuid.New().String(),
			SigningKeyshareID: signingKeyShare.ID,
			Message:           refundTxSigHash,
			VerifyingKey:      verifyingKeyBytes,
			UserCommitment:    userRefundTxNonceCommitment,
		},
	)
	signingResults, err := helper.SignFrost(ctx, config, signingJobs)
	if err != nil {
		return nil, err
	}

	nodeTxSigningResult, err := signingResults[0].MarshalProto()
	if err != nil {
		return nil, err
	}
	refundTxSigningResult, err := signingResults[1].MarshalProto()
	if err != nil {
		return nil, err
	}
	// Create the tree
	schemaNetwork, err := common.SchemaNetworkFromNetwork(network)
	if err != nil {
		return nil, err
	}
	txid := onChainTx.TxHash()
	treeMutator := db.Tree.
		Create().
		SetOwnerIdentityPubkey(depositAddress.OwnerIdentityPubkey).
		SetNetwork(schemaNetwork).
		SetBaseTxid(txid[:]).
		SetVout(int16(req.OnChainUtxo.Vout))
	if txConfirmed {
		treeMutator.SetStatus(schema.TreeStatusAvailable)
	} else {
		treeMutator.SetStatus(schema.TreeStatusPending)
	}
	tree, err := treeMutator.Save(ctx)
	if err != nil {
		return nil, err
	}
	root, err := db.TreeNode.
		Create().
		SetTree(tree).
		SetStatus(schema.TreeNodeStatusCreating).
		SetOwnerIdentityPubkey(depositAddress.OwnerIdentityPubkey).
		SetOwnerSigningPubkey(depositAddress.OwnerSigningPubkey).
		SetValue(uint64(onChainOutput.Value)).
		SetVerifyingPubkey(verifyingKeyBytes).
		SetSigningKeyshare(signingKeyShare).
		SetRawTx(req.RootTxSigningJob.RawTx).
		SetRawRefundTx(req.RefundTxSigningJob.RawTx).
		SetVout(int16(req.OnChainUtxo.Vout)).
		Save(ctx)
	if err != nil {
		return nil, err
	}
	tree, err = tree.Update().SetRoot(root).Save(ctx)
	if err != nil {
		return nil, err
	}

	return &pb.StartTreeCreationResponse{
		TreeId: tree.ID.String(),
		RootNodeSignatureShares: &pb.NodeSignatureShares{
			NodeId:                root.ID.String(),
			NodeTxSigningResult:   nodeTxSigningResult,
			RefundTxSigningResult: refundTxSigningResult,
			VerifyingKey:          verifyingKeyBytes,
		},
	}, nil
}

// StartDepositTreeCreation verifies the on chain utxo, and then verifies and signs the offchain root and refund transactions.
func (o *DepositHandler) StartDepositTreeCreation(ctx context.Context, config *so.Config, req *pb.StartDepositTreeCreationRequest) (*pb.StartDepositTreeCreationResponse, error) {
	ctx, span := tracer.Start(ctx, "DepositHandler.StartDepositTreeCreation")
	defer span.End()

	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, o.config, req.IdentityPublicKey); err != nil {
		return nil, err
	}
	// Get the on chain tx
	onChainTx, err := common.TxFromRawTxBytes(req.OnChainUtxo.RawTx)
	if err != nil {
		return nil, err
	}
	if len(onChainTx.TxOut) <= int(req.OnChainUtxo.Vout) {
		return nil, fmt.Errorf("utxo index out of bounds")
	}

	// Verify that the on chain utxo is paid to the registered deposit address
	if len(onChainTx.TxOut) <= int(req.OnChainUtxo.Vout) {
		return nil, fmt.Errorf("utxo index out of bounds")
	}
	onChainOutput := onChainTx.TxOut[req.OnChainUtxo.Vout]
	network, err := common.NetworkFromProtoNetwork(req.OnChainUtxo.Network)
	if err != nil {
		return nil, err
	}
	if !config.IsNetworkSupported(network) {
		return nil, fmt.Errorf("network not supported")
	}
	utxoAddress, err := common.P2TRAddressFromPkScript(onChainOutput.PkScript, network)
	if err != nil {
		return nil, err
	}
	db := ent.GetDbFromContext(ctx)
	depositAddress, err := db.DepositAddress.Query().Where(depositaddress.Address(*utxoAddress)).First(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			err = errors.NotFoundErrorf("The requested deposit address could not be found.")
		}
		return nil, err
	}
	if depositAddress == nil || !bytes.Equal(depositAddress.OwnerIdentityPubkey, req.IdentityPublicKey) {
		return nil, fmt.Errorf("deposit address not found for address: %s", *utxoAddress)
	}
	if !bytes.Equal(depositAddress.OwnerSigningPubkey, req.RootTxSigningJob.SigningPublicKey) || !bytes.Equal(depositAddress.OwnerSigningPubkey, req.RefundTxSigningJob.SigningPublicKey) {
		return nil, fmt.Errorf("unexpected signing public key")
	}
	txConfirmed := depositAddress.ConfirmationHeight != 0

	if txConfirmed && depositAddress.ConfirmationTxid != "" {
		onChainTxid := onChainTx.TxHash().String()
		if onChainTxid != depositAddress.ConfirmationTxid {
			return nil, fmt.Errorf("transaction ID does not match confirmed transaction ID")
		}
	}

	// Verify the root transaction
	rootTx, err := common.TxFromRawTxBytes(req.RootTxSigningJob.RawTx)
	if err != nil {
		return nil, err
	}
	err = o.verifyRootTransaction(rootTx, onChainTx, req.OnChainUtxo.Vout)
	if err != nil {
		return nil, err
	}
	rootTxSigHash, err := common.SigHashFromTx(rootTx, 0, onChainOutput)
	if err != nil {
		return nil, err
	}

	// Verify the refund transaction
	refundTx, err := common.TxFromRawTxBytes(req.RefundTxSigningJob.RawTx)
	if err != nil {
		return nil, err
	}
	err = o.verifyRefundTransaction(rootTx, refundTx)
	if err != nil {
		return nil, err
	}
	if len(rootTx.TxOut) <= 0 {
		return nil, fmt.Errorf("vout out of bounds, root tx has no outputs")
	}
	refundTxSigHash, err := common.SigHashFromTx(refundTx, 0, rootTx.TxOut[0])
	if err != nil {
		return nil, err
	}

	// Sign the root and refund transactions
	signingKeyShare, err := depositAddress.QuerySigningKeyshare().Only(ctx)
	if err != nil {
		return nil, err
	}
	verifyingKeyBytes, err := common.AddPublicKeys(signingKeyShare.PublicKey, depositAddress.OwnerSigningPubkey)
	if err != nil {
		return nil, err
	}

	signingJobs := make([]*helper.SigningJob, 0)
	userRootTxNonceCommitment, err := objects.NewSigningCommitment(req.RootTxSigningJob.SigningNonceCommitment.Binding, req.RootTxSigningJob.SigningNonceCommitment.Hiding)
	if err != nil {
		return nil, err
	}
	userRefundTxNonceCommitment, err := objects.NewSigningCommitment(req.RefundTxSigningJob.SigningNonceCommitment.Binding, req.RefundTxSigningJob.SigningNonceCommitment.Hiding)
	if err != nil {
		return nil, err
	}
	signingJobs = append(
		signingJobs,
		&helper.SigningJob{
			JobID:             uuid.New().String(),
			SigningKeyshareID: signingKeyShare.ID,
			Message:           rootTxSigHash,
			VerifyingKey:      verifyingKeyBytes,
			UserCommitment:    userRootTxNonceCommitment,
		},
		&helper.SigningJob{
			JobID:             uuid.New().String(),
			SigningKeyshareID: signingKeyShare.ID,
			Message:           refundTxSigHash,
			VerifyingKey:      verifyingKeyBytes,
			UserCommitment:    userRefundTxNonceCommitment,
		},
	)
	signingResults, err := helper.SignFrost(ctx, config, signingJobs)
	if err != nil {
		return nil, err
	}

	nodeTxSigningResult, err := signingResults[0].MarshalProto()
	if err != nil {
		return nil, err
	}
	refundTxSigningResult, err := signingResults[1].MarshalProto()
	if err != nil {
		return nil, err
	}
	// Create the tree
	schemaNetwork, err := common.SchemaNetworkFromNetwork(network)
	if err != nil {
		return nil, err
	}
	txid := onChainTx.TxHash()
	treeMutator := db.Tree.
		Create().
		SetOwnerIdentityPubkey(depositAddress.OwnerIdentityPubkey).
		SetNetwork(schemaNetwork).
		SetBaseTxid(txid[:]).
		SetVout(int16(req.OnChainUtxo.Vout))

	if txConfirmed {
		treeMutator.SetStatus(schema.TreeStatusAvailable)
	} else {
		treeMutator.SetStatus(schema.TreeStatusPending)
	}
	tree, err := treeMutator.Save(ctx)
	if err != nil {
		return nil, err
	}
	treeNodeMutator := db.TreeNode.
		Create().
		SetTree(tree).
		SetStatus(schema.TreeNodeStatusCreating).
		SetOwnerIdentityPubkey(depositAddress.OwnerIdentityPubkey).
		SetOwnerSigningPubkey(depositAddress.OwnerSigningPubkey).
		SetValue(uint64(onChainOutput.Value)).
		SetVerifyingPubkey(verifyingKeyBytes).
		SetSigningKeyshare(signingKeyShare).
		SetRawTx(req.RootTxSigningJob.RawTx).
		SetRawRefundTx(req.RefundTxSigningJob.RawTx).
		SetVout(int16(req.OnChainUtxo.Vout))

	if depositAddress.NodeID != uuid.Nil {
		treeNodeMutator.SetID(depositAddress.NodeID)
	}

	root, err := treeNodeMutator.Save(ctx)
	if err != nil {
		return nil, err
	}
	tree, err = tree.Update().SetRoot(root).Save(ctx)
	if err != nil {
		return nil, err
	}

	return &pb.StartDepositTreeCreationResponse{
		TreeId: tree.ID.String(),
		RootNodeSignatureShares: &pb.NodeSignatureShares{
			NodeId:                root.ID.String(),
			NodeTxSigningResult:   nodeTxSigningResult,
			RefundTxSigningResult: refundTxSigningResult,
			VerifyingKey:          verifyingKeyBytes,
		},
	}, nil
}

func (o *DepositHandler) verifyRootTransaction(rootTx *wire.MsgTx, onChainTx *wire.MsgTx, onChainVout uint32) error {
	if len(rootTx.TxIn) <= 0 || len(rootTx.TxOut) <= 0 {
		return fmt.Errorf("root transaction should have at least 1 input and 1 output")
	}

	if len(onChainTx.TxOut) <= int(onChainVout) {
		return fmt.Errorf("vout out of bounds")
	}

	// Check root transaction input
	if rootTx.TxIn[0].PreviousOutPoint.Index != onChainVout || rootTx.TxIn[0].PreviousOutPoint.Hash != onChainTx.TxHash() {
		return fmt.Errorf("root transaction must use the on chain utxo as input")
	}

	// Check root transaction output address
	if !bytes.Equal(rootTx.TxOut[0].PkScript, onChainTx.TxOut[onChainVout].PkScript) {
		return fmt.Errorf("root transaction must pay to the same deposit address")
	}

	// Check root transaction amount
	if rootTx.TxOut[0].Value != onChainTx.TxOut[onChainVout].Value {
		return fmt.Errorf("root transaction has wrong value")
	}

	return nil
}

func (o *DepositHandler) verifyRefundTransaction(tx *wire.MsgTx, refundTx *wire.MsgTx) error {
	// Refund transaction should have the given tx as input
	previousTxid := tx.TxHash()
	for _, refundTxIn := range refundTx.TxIn {
		if refundTxIn.PreviousOutPoint.Hash == previousTxid && refundTxIn.PreviousOutPoint.Index == 0 {
			return nil
		}
	}

	return fmt.Errorf("refund transaction should have the node tx as input")
}

type UtxoSwapRequestType int

const (
	UtxoSwapRequestFixed UtxoSwapRequestType = iota
	UtxoSwapRequestMaxFee
)

// InitiateUtxoSwap initiates a UTXO swap operation, allowing a User to swap their on-chain UTXOs for Spark funds.
// It is used in static deposit address flow.
// The function performs the following steps:
// 1. Creates a swap record in all SOs to prevent concurrent spending of the same UTXO
// 2. Validates the swap request and stores it in the database with status CREATED
// 3. Creates a transfer record to the user with the specified leaves
// 4. Signs the spend transaction for SSP using FROST
//
// Parameters:
//   - ctx: The context for the operation
//   - config: The service configuration containing network and operator settings
//   - req: The UTXO swap request containing:
//   - OnChainUtxo: The UTXO to be swapped (network, txid, vout)
//   - Transfer: The transfer details (receiver identity, leaves to send, etc.)
//   - SpendTxSigningJob: The SSP's spend transaction signing job details
//
// Returns:
//   - InitiateUtxoSwapResponse containing:
//   - SpendTxSigningResult: The signed spend transaction
//   - Transfer: The created transfer record
//   - DepositAddress: Information about the deposit address including:
//   - error if the operation fails
//
// TODO(oleg): implement cancellation of the swap
func (o *DepositHandler) InitiateUtxoSwap(ctx context.Context, config *so.Config, req *pb.InitiateUtxoSwapRequest) (*pb.InitiateUtxoSwapResponse, error) {
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, config, req.Transfer.OwnerIdentityPublicKey); err != nil {
		return nil, err
	}

	logger := logging.GetLoggerFromContext(ctx)
	logger.Info("Start InitiateUtxoSwap request for on-chain utxo", "txid", hex.EncodeToString(req.OnChainUtxo.Txid), "vout", req.OnChainUtxo.Vout)

	// Create a swap record in all SEs so they can not be called concurrently to spend the same utxo.
	// This will validate the swap request and store it in the database with status CREATED,
	// blocking any other swap requests. Then it will create a transfer to the user.
	allSelection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionAll}
	responses, err := helper.ExecuteTaskWithAllOperators(ctx, config, &allSelection, func(ctx context.Context, operator *so.SigningOperator) (interface{}, error) {
		conn, err := operator.NewGRPCConnection()
		if err != nil {
			logger.Info("Failed to connect to operator for validating transaction state before cancelling", "error", err)
			return nil, err
		}
		defer conn.Close()

		client := pbinternal.NewSparkInternalServiceClient(conn)
		internalResp, err := client.CreateUtxoSwap(ctx, req)
		if err != nil {
			logger.Info("Failed to execute create utxo swap task with operator", "operator", operator.Identifier, "error", err)
			return nil, err
		}
		return internalResp, err
	})
	if err != nil {
		// TODO(oleg): cancel the utxo swap for every operator
		return nil, fmt.Errorf("failed to successfully execute create utxo swap task with all operators: %v", err)
	}

	transfer := responses[config.Identifier].(*pbinternal.CreateUtxoSwapResponse).Transfer
	if transfer == nil {
		return nil, fmt.Errorf("create utxo swap task with operator %s returned nil transfer", config.Identifier)
	}

	db := ent.GetDbFromContext(ctx)
	schemaNetwork, err := common.SchemaNetworkFromProtoNetwork(req.OnChainUtxo.Network)
	if err != nil {
		return nil, fmt.Errorf("failed to get schema network: %v", err)
	}
	targetUtxo, err := db.Utxo.Query().
		Where(utxo.NetworkEQ(schemaNetwork)).
		Where(utxo.Txid(req.OnChainUtxo.Txid)).
		Where(utxo.Vout(req.OnChainUtxo.Vout)).
		First(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get target utxo: %v", err)
	}
	depositAddress, err := targetUtxo.QueryDepositAddress().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get deposit address: %v", err)
	}

	// Recover the signature for the utxo spend
	// Execute signing jobs with all operators and create a refund transaction
	userRootTxNonceCommitment, err := objects.NewSigningCommitment(req.SpendTxSigningJob.SigningNonceCommitment.Binding, req.SpendTxSigningJob.SigningNonceCommitment.Hiding)
	if err != nil {
		return nil, fmt.Errorf("failed to create signing commitment: %v", err)
	}
	verifyingKeyBytes, spendTxSigningResult, err := getSpendTxSigningResult(ctx, config, depositAddress, targetUtxo, req.SpendTxSigningJob.RawTx, userRootTxNonceCommitment)
	if err != nil {
		return nil, fmt.Errorf("failed to get spend tx signing result: %v", err)
	}

	nodeIDStr := depositAddress.NodeID.String()
	return &pb.InitiateUtxoSwapResponse{
		SpendTxSigningResult: spendTxSigningResult,
		Transfer:             transfer,
		DepositAddress: &pb.DepositAddressQueryResult{
			DepositAddress:       depositAddress.Address,
			UserSigningPublicKey: depositAddress.OwnerSigningPubkey,
			VerifyingPublicKey:   verifyingKeyBytes,
			LeafId:               &nodeIDStr,
		},
	}, nil
}

// A helper function to generate a FROST signature for a spend transaction. This
// function is used in the static deposit address flow to create a spending
// transaction for the SSP.
//
// Parameters:
//   - ctx: The context for the operation
//   - config: The service configuration containing network and operator settings
//   - depositAddress: The deposit address entity containing:
//   - targetUtxo: The target UTXO entity containing:
//   - spendTxRaw: The raw spend transaction bytes
//   - userSpendTxNonceCommitment: The user's nonce commitment for the spend tx signing job
//
// Returns:
//   - []byte: The verifying public key to verify the combined signature in frost aggregate.
//   - *pb.SigningResult: Signing result containing a partial FROST signature that can
//     be aggregated with other signatures.
//   - error if the operation fails.
func getSpendTxSigningResult(ctx context.Context, config *so.Config, depositAddress *ent.DepositAddress, targetUtxo *ent.Utxo, spendTxRaw []byte, userSpendTxNonceCommitment *objects.SigningCommitment) ([]byte, *pb.SigningResult, error) {
	logger := logging.GetLoggerFromContext(ctx)

	signingKeyShare, err := depositAddress.QuerySigningKeyshare().Only(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get signing keyshare: %v", err)
	}
	verifyingKeyBytes, err := common.AddPublicKeys(signingKeyShare.PublicKey, depositAddress.OwnerSigningPubkey)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to add public keys: %v", err)
	}

	onChainTxOut := wire.NewTxOut(int64(targetUtxo.Amount), targetUtxo.PkScript)
	spendTx, err := common.TxFromRawTxBytes(spendTxRaw)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse spend tx: %v", err)
	}

	spendTxSigHash, err := common.SigHashFromTx(spendTx, 0, onChainTxOut)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get spend tx sig hash: %v", err)
	}
	logger.Debug("spendTxSigHash", "spendTxSigHash", hex.EncodeToString(spendTxSigHash))

	signingJobs := make([]*helper.SigningJob, 0)

	signingJobs = append(
		signingJobs,
		&helper.SigningJob{
			JobID:             uuid.New().String(),
			SigningKeyshareID: signingKeyShare.ID,
			Message:           spendTxSigHash,
			VerifyingKey:      verifyingKeyBytes,
			UserCommitment:    userSpendTxNonceCommitment,
		},
	)
	signingResults, err := helper.SignFrost(ctx, config, signingJobs)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to sign spend tx: %v", err)
	}

	spendTxSigningResult, err := signingResults[0].MarshalProto()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal spend tx signing result: %v", err)
	}
	return verifyingKeyBytes, spendTxSigningResult, nil
}
