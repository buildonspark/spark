package handler

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"

	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go/common"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	pbinternal "github.com/lightsparkdev/spark-go/proto/spark_internal"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/authz"
	"github.com/lightsparkdev/spark-go/so/ent"
	"github.com/lightsparkdev/spark-go/so/ent/depositaddress"
	"github.com/lightsparkdev/spark-go/so/ent/schema"
	"github.com/lightsparkdev/spark-go/so/helper"
	"github.com/lightsparkdev/spark-go/so/objects"
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
	network, err := common.NetworkFromProtoNetwork(req.Network)
	logger := helper.GetLoggerFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if !config.IsNetworkSupported(network) {
		return nil, fmt.Errorf("network not supported")
	}
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, o.config, req.IdentityPublicKey); err != nil {
		return nil, err
	}
	logger.Info("Generating deposit address for public key", "public_key", hex.EncodeToString(req.SigningPublicKey))
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

	_, err = ent.GetDbFromContext(ctx).DepositAddress.Create().
		SetSigningKeyshareID(keyshare.ID).
		SetOwnerIdentityPubkey(req.IdentityPublicKey).
		SetOwnerSigningPubkey(req.SigningPublicKey).
		SetAddress(*depositAddress).
		// Confirmation height is not set since nothing has been confirmed yet.
		Save(ctx)
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
		},
	}, nil
}

func (o *DepositHandler) StartTreeCreation(ctx context.Context, config *so.Config, req *pb.StartTreeCreationRequest) (*pb.StartTreeCreationResponse, error) {
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
	txConfirmed := helper.CheckUTXOOnchain(config, req.OnChainUtxo)

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
		SetNetwork(schemaNetwork).SetBaseTxid(txid[:]).
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
	txConfirmed := helper.CheckUTXOOnchain(config, req.OnChainUtxo)

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
