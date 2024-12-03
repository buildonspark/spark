package helper

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"log"

	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go/common"
	pbcommon "github.com/lightsparkdev/spark-go/proto/common"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	pbinternal "github.com/lightsparkdev/spark-go/proto/spark_internal"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent/depositaddress"
	"github.com/lightsparkdev/spark-go/so/ent/schema"
	"github.com/lightsparkdev/spark-go/so/entutils"
	"github.com/lightsparkdev/spark-go/so/objects"
)

// The DepositHandler is responsible for handling deposit related requests.
type DepositHandler struct{}

// GenerateDepositAddress generates a deposit address for the given public key.
func (o *DepositHandler) GenerateDepositAddress(ctx context.Context, config *so.Config, req *pb.GenerateDepositAddressRequest) (*pb.GenerateDepositAddressResponse, error) {
	log.Printf("Generating deposit address for public key: %s", hex.EncodeToString(req.SigningPublicKey))
	keyshares, err := entutils.GetUnusedSigningKeyshares(ctx, config, 1)
	if err != nil {
		return nil, err
	}

	if len(keyshares) == 0 {
		log.Printf("No keyshares available")
		return nil, fmt.Errorf("no keyshares available")
	}

	keyshare := keyshares[0]

	err = entutils.MarkSigningKeysharesAsUsed(ctx, config, []uuid.UUID{keyshare.ID})
	if err != nil {
		log.Printf("Failed to mark keyshare as used: %v", err)
		return nil, err
	}

	selection := OperatorSelection{Option: OperatorSelectionOptionExcludeSelf}
	_, err = ExecuteTaskWithAllOperators(ctx, config, &selection, func(ctx context.Context, operator *so.SigningOperator) (interface{}, error) {
		conn, err := common.NewGRPCConnection(operator.Address)
		if err != nil {
			log.Printf("Failed to connect to operator: %v", err)
			return nil, err
		}
		defer conn.Close()

		client := pbinternal.NewSparkInternalServiceClient(conn)
		_, err = client.MarkKeysharesAsUsed(ctx, &pbinternal.MarkKeysharesAsUsedRequest{KeyshareId: []string{keyshare.ID.String()}})
		return nil, err
	})
	if err != nil {
		log.Printf("Failed to execute task with all operators: %v", err)
		return nil, err
	}

	combinedPublicKey, err := common.AddPublicKeys(keyshare.PublicKey, req.SigningPublicKey)
	if err != nil {
		log.Printf("Failed to add public keys: %v", err)
		return nil, err
	}

	depositAddress, err := common.P2TRAddressFromPublicKey(combinedPublicKey, config.Network)
	if err != nil {
		log.Printf("Failed to generate deposit address: %v", err)
		return nil, err
	}

	_, err = common.GetDbFromContext(ctx).DepositAddress.Create().
		SetSigningKeyshareID(keyshare.ID).
		SetOwnerIdentityPubkey(req.IdentityPublicKey).
		SetOwnerSigningPubkey(req.SigningPublicKey).
		SetAddress(*depositAddress).
		Save(ctx)
	if err != nil {
		log.Printf("Failed to link keyshare to deposit address: %v", err)
		return nil, err
	}

	_, err = ExecuteTaskWithAllOperators(ctx, config, &selection, func(ctx context.Context, operator *so.SigningOperator) (interface{}, error) {
		conn, err := common.NewGRPCConnection(operator.Address)
		if err != nil {
			log.Printf("Failed to connect to operator: %v", err)
			return nil, err
		}
		defer conn.Close()

		client := pbinternal.NewSparkInternalServiceClient(conn)
		_, err = client.MarkKeyshareForDepositAddress(ctx, &pbinternal.MarkKeyshareForDepositAddressRequest{
			KeyshareId:             keyshare.ID.String(),
			Address:                *depositAddress,
			OwnerIdentityPublicKey: req.IdentityPublicKey,
			OwnerSigningPublicKey:  req.SigningPublicKey,
		})
		return nil, err
	})
	if err != nil {
		log.Printf("Failed to execute task with all operators: %v", err)
		return nil, err
	}

	log.Printf("Generated deposit address: %s", *depositAddress)
	return &pb.GenerateDepositAddressResponse{Address: *depositAddress}, nil
}

// StartTreeCreation verifies the on chain utxo, and then verifies and signs the offchain root and refund transactions.
func (o *DepositHandler) StartTreeCreation(ctx context.Context, config *so.Config, req *pb.StartTreeCreationRequest) (*pb.StartTreeCreationResponse, error) {
	// Get the on chain tx
	onChainHelper := &OnChainHelper{}
	onChainTx, err := onChainHelper.GetTxOnChain(ctx, req.OnChainUtxo.Txid)
	if err != nil {
		return nil, err
	}
	if len(onChainTx.TxOut) <= int(req.OnChainUtxo.Vout) {
		return nil, fmt.Errorf("utxo index out of bounds")
	}

	// Verify that the on chain utxo is paid to the registered deposit address
	onChainOutput := onChainTx.TxOut[req.OnChainUtxo.Vout]
	utxoAddress, err := common.P2TRAddressFromPkScript(onChainOutput.PkScript, config.Network)
	if err != nil {
		return nil, err
	}
	depositAddress, err := common.GetDbFromContext(ctx).DepositAddress.Query().Where(depositaddress.Address(*utxoAddress)).First(ctx)
	if err != nil {
		return nil, err
	}
	if depositAddress == nil || !bytes.Equal(depositAddress.OwnerIdentityPubkey, req.IdentityPublicKey) {
		return nil, fmt.Errorf("deposit address not found for address: %s", *utxoAddress)
	}
	if !bytes.Equal(depositAddress.OwnerSigningPubkey, req.RootTxSigningJob.SigningPublicKey) || !bytes.Equal(depositAddress.OwnerSigningPubkey, req.RefundTxSigningJob.SigningPublicKey) {
		return nil, fmt.Errorf("unexpected signing public key")
	}

	// Verify the root transaction
	rootTx, err := common.TxFromTxHex(req.RootTxSigningJob.RawTxHex)
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
	refundTx, err := common.TxFromTxHex(req.RefundTxSigningJob.RawTxHex)
	if err != nil {
		return nil, err
	}
	err = o.verifyRefundTransaction(rootTx, refundTx)
	if err != nil {
		return nil, err
	}
	refundTxSigHash, err := common.SigHashFromTx(refundTx, 0, rootTx.TxOut[0])
	if err != nil {
		return nil, err
	}

	// Sign the root and refund transactions
	signingKeyShare := depositAddress.QuerySigningKeyshare().OnlyX(ctx)
	verifyingKeyBytes, err := common.AddPublicKeys(signingKeyShare.PublicKey, depositAddress.OwnerSigningPubkey)
	if err != nil {
		return nil, err
	}

	signingJobs := make([]*SigningJob, 0)
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
		&SigningJob{
			JobID:             uuid.New().String(),
			SigningKeyshareID: signingKeyShare.ID,
			Message:           rootTxSigHash,
			VerifyingKey:      verifyingKeyBytes,
			UserCommitment:    *userRootTxNonceCommitment,
		},
		&SigningJob{
			JobID:             uuid.New().String(),
			SigningKeyshareID: signingKeyShare.ID,
			Message:           refundTxSigHash,
			VerifyingKey:      verifyingKeyBytes,
			UserCommitment:    *userRefundTxNonceCommitment,
		},
	)
	signingResult, err := SignFrost(ctx, config, signingJobs)
	if err != nil {
		return nil, err
	}
	rootTxSigningCommitments := make(map[string]*pbcommon.SigningCommitment)
	for id, commitment := range signingResult[0].SigningCommitments {
		commitmentProto, err := commitment.MarshalProto()
		if err != nil {
			return nil, err
		}
		rootTxSigningCommitments[id] = commitmentProto
	}
	rootTxSignatureShare := signingResult[0].SignatureShares

	refundTxSigningCommitments := make(map[string]*pbcommon.SigningCommitment)
	for id, commitment := range signingResult[1].SigningCommitments {
		commitmentProto, err := commitment.MarshalProto()
		if err != nil {
			return nil, err
		}
		refundTxSigningCommitments[id] = commitmentProto
	}
	refundTxSignatureShare := signingResult[1].SignatureShares

	// Create the tree
	db := common.GetDbFromContext(ctx)
	tree := db.Tree.Create().SetOwnerIdentityPubkey(depositAddress.OwnerIdentityPubkey).SaveX(ctx)
	root := db.TreeNode.
		Create().
		SetTree(tree).
		SetStatus(schema.TreeNodeStatusCreating).
		SetOwnerIdentityPubkey(depositAddress.OwnerIdentityPubkey).
		SetOwnerSigningPubkey(depositAddress.OwnerSigningPubkey).
		SetValue(uint64(onChainOutput.Value)).
		SetVerifyingPubkey(verifyingKeyBytes).
		SetSigningKeyshare(signingKeyShare).
		SaveX(ctx)
	tree = tree.Update().SetRoot(root).SaveX(ctx)

	return &pb.StartTreeCreationResponse{
		TreeId: tree.ID.String(),
		RootNodeSignatureShares: &pb.NodeSignatureShares{
			NodeId:                     root.ID.String(),
			NodeTxSigningCommitments:   rootTxSigningCommitments,
			NodeTxSignatureShares:      rootTxSignatureShare,
			RefundTxSigningCommitments: refundTxSigningCommitments,
			RefundTxSignatureShares:    refundTxSignatureShare,
		},
	}, nil
}

func (o *DepositHandler) verifyRootTransaction(rootTx *wire.MsgTx, onChainTx *wire.MsgTx, onChainVout uint32) error {
	// Root transaction must have 1 input and 1 output
	if len(rootTx.TxIn) != 1 || len(rootTx.TxOut) != 1 {
		return fmt.Errorf("root transaction must have 1 input and 1 output")
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

	// Root transaction should not have timelock
	if rootTx.LockTime != 0 {
		return fmt.Errorf("root transaction must not have timelock")
	}
	return nil
}

func (o *DepositHandler) verifyRefundTransaction(tx *wire.MsgTx, refundTx *wire.MsgTx) error {
	// Refund transaction should have timelock
	if refundTx.LockTime == 0 {
		return fmt.Errorf("refund transaction must have timelock")
	}

	// Refund transaction should have the given tx as input
	previousTxid := tx.TxHash()
	for _, refundTxIn := range refundTx.TxIn {
		if refundTxIn.PreviousOutPoint.Hash == previousTxid && refundTxIn.PreviousOutPoint.Index == 0 {
			return nil
		}
	}

	return fmt.Errorf("refund transaction should have the node tx as input")
}
