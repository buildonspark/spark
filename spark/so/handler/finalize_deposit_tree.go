package handler

import (
	"context"
	"fmt"

	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/logging"
	pbfrost "github.com/lightsparkdev/spark/proto/frost"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/depositaddress"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/tree"
	"github.com/lightsparkdev/spark/so/ent/treenode"
	"github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/frost"
	"github.com/lightsparkdev/spark/so/helper"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// validateSigningJobFields validates that all required fields in a UserSignedTxSigningJob are present
func validateSigningJobFields(job *pb.UserSignedTxSigningJob, jobName string) error {
	if job == nil {
		return status.Errorf(codes.InvalidArgument, "%s is required", jobName)
	}

	if len(job.SigningPublicKey) == 0 {
		return status.Errorf(codes.InvalidArgument, "%s.signing_public_key is required", jobName)
	}

	if len(job.RawTx) == 0 {
		return status.Errorf(codes.InvalidArgument, "%s.raw_tx is required", jobName)
	}

	if job.SigningNonceCommitment == nil {
		return status.Errorf(codes.InvalidArgument, "%s.signing_nonce_commitment is required", jobName)
	}

	if len(job.UserSignature) == 0 {
		return status.Errorf(codes.InvalidArgument, "%s.user_signature is required", jobName)
	}

	if job.SigningCommitments == nil {
		return status.Errorf(codes.InvalidArgument, "%s.signing_commitments is required", jobName)
	}

	if len(job.SigningCommitments.SigningCommitments) == 0 {
		return status.Errorf(codes.InvalidArgument, "%s.signing_commitments.signing_commitments map is empty", jobName)
	}

	return nil
}

func validateFinalizeDepositTreeCreationRequest(
	req *pb.FinalizeDepositTreeCreationRequest,
) error {
	if err := validateSigningJobFields(req.RootTxSigningJob, "root_tx_signing_job"); err != nil {
		return err
	}

	if err := validateSigningJobFields(req.RefundTxSigningJob, "refund_tx_signing_job"); err != nil {
		return err
	}

	if err := validateSigningJobFields(req.DirectFromCpfpRefundTxSigningJob, "direct_from_cpfp_refund_tx_signing_job"); err != nil {
		return err
	}

	return nil
}

// validateSigningJob validates that a signing job's public key matches the expected key
func validateSigningJob(job *pb.UserSignedTxSigningJob, expectedPubKey keys.Public, jobName string) error {
	if job == nil {
		return nil
	}
	pubKey, err := keys.ParsePublicKey(job.SigningPublicKey)
	if err != nil {
		return fmt.Errorf("invalid %s signing public key: %w", jobName, err)
	}
	if !pubKey.Equals(expectedPubKey) {
		return fmt.Errorf("%s signing public key does not match", jobName)
	}
	return nil
}

// load the deposit address and validate it
func loadAndValidateDepositAddress(
	ctx context.Context,
	network btcnetwork.Network,
	req *pb.FinalizeDepositTreeCreationRequest,
	reqIDPubKey keys.Public,
) (depositAddress *ent.DepositAddress, onChainTx *wire.MsgTx, onChainOutput *wire.TxOut, err error) {
	// Parse on-chain UTXO
	onChainTx, err = common.TxFromRawTxBytes(req.OnChainUtxo.RawTx)
	if err != nil {
		err = fmt.Errorf("invalid on-chain transaction: %w", err)
		return
	}

	if int(req.OnChainUtxo.Vout) >= len(onChainTx.TxOut) {
		err = fmt.Errorf("utxo index out of bounds")
		return
	}
	onChainOutput = onChainTx.TxOut[req.OnChainUtxo.Vout]

	utxoAddress, err := common.P2TRAddressFromPkScript(onChainOutput.PkScript, network)
	if err != nil {
		err = fmt.Errorf("invalid utxo address: %w", err)
		return
	}

	// Look up deposit address
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		err = fmt.Errorf("failed to get database: %w", err)
		return
	}

	depositAddress, err = db.DepositAddress.Query().
		Where(depositaddress.Address(*utxoAddress)).
		Where(depositaddress.IsStatic(false)).
		Where(depositaddress.NetworkEQ(network)).
		WithTree().
		WithSigningKeyshare().
		ForUpdate().
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			err = errors.NotFoundMissingEntity(fmt.Errorf("the requested deposit address could not be found: %s", *utxoAddress))
			return
		}
		if ent.IsNotSingular(err) {
			err = fmt.Errorf("multiple deposit addresses found for: %s", *utxoAddress)
			return
		}
		return
	}

	if !depositAddress.OwnerIdentityPubkey.Equals(reqIDPubKey) {
		err = fmt.Errorf("identity public key does not match deposit address owner")
		return
	}

	// Validate signing public keys
	rootSigningPubKey, err := keys.ParsePublicKey(req.RootTxSigningJob.SigningPublicKey)
	if err != nil {
		err = fmt.Errorf("invalid root tx signing public key: %w", err)
		return
	}
	if !depositAddress.OwnerSigningPubkey.Equals(rootSigningPubKey) {
		err = fmt.Errorf("signing public key does not match deposit address owner")
		return
	}

	// Validate all signing jobs have matching public keys
	if err = validateSigningJob(req.RefundTxSigningJob, rootSigningPubKey, "refund"); err != nil {
		return
	}
	if err = validateSigningJob(req.DirectFromCpfpRefundTxSigningJob, rootSigningPubKey, "direct_from_cpfp_refund"); err != nil {
		return
	}

	signingKeyShare := depositAddress.Edges.SigningKeyshare
	if signingKeyShare == nil {
		err = fmt.Errorf("signing keyshare not found for deposit address")
		return
	}

	combinedPublicKey := signingKeyShare.PublicKey.Add(depositAddress.OwnerSigningPubkey)
	err = validateBitcoinTransactions(
		ctx,
		req.OnChainUtxo.RawTx,
		req.OnChainUtxo.Vout,
		req.RootTxSigningJob.RawTx,
		req.RefundTxSigningJob.RawTx,
		req.DirectFromCpfpRefundTxSigningJob.RawTx,
		nil, // directRootTx - not used in FinalizeDepositTreeCreation
		nil, // directRefundTx - not used in FinalizeDepositTreeCreation
		combinedPublicKey,
		depositAddress.OwnerSigningPubkey,
		network.String(),
	)
	if err != nil {
		err = fmt.Errorf("failed to validate transaction in tree creation request: %w", err)
		return
	}

	return
}

// prepareSigningJobs creates signing jobs for all transactions
func (o *DepositHandler) prepareSigningJobs(
	req *pb.FinalizeDepositTreeCreationRequest,
	depositAddress *ent.DepositAddress,
	onChainTx *wire.MsgTx,
	onChainOutput *wire.TxOut,
) (signingJobs []*helper.SigningJob, verifyingKey keys.Public, err error) {
	// Parse and validate root transaction
	cpfpRootTx, err := common.TxFromRawTxBytes(req.RootTxSigningJob.RawTx)
	if err != nil {
		err = fmt.Errorf("invalid root transaction: %w", err)
		return
	}
	if err = o.verifyRootTransaction(cpfpRootTx, onChainTx, req.OnChainUtxo.Vout, false); err != nil {
		err = fmt.Errorf("root transaction verification failed: %w", err)
		return
	}

	// Parse and validate refund transaction
	cpfpRefundTx, err := common.TxFromRawTxBytes(req.RefundTxSigningJob.RawTx)
	if err != nil {
		err = fmt.Errorf("invalid refund transaction: %w", err)
		return
	}
	if err = o.verifyRefundTransaction(cpfpRootTx, cpfpRefundTx); err != nil {
		err = fmt.Errorf("cpfp refund verification failed: %w", err)
		return
	}

	// Get keyshare and verifying key
	signingKeyShare := depositAddress.Edges.SigningKeyshare
	if signingKeyShare == nil {
		err = fmt.Errorf("signing keyshare not found for deposit address")
		return
	}
	verifyingKey = signingKeyShare.PublicKey.Add(depositAddress.OwnerSigningPubkey)

	// Compute sighashes
	cpfpRootTxSigHash, err := common.SigHashFromTx(cpfpRootTx, 0, onChainOutput)
	if err != nil {
		err = fmt.Errorf("failed to compute root tx sighash: %w", err)
		return
	}

	cpfpRefundTxSigHash, err := common.SigHashFromTx(cpfpRefundTx, 0, cpfpRootTx.TxOut[0])
	if err != nil {
		err = fmt.Errorf("failed to compute refund tx sighash: %w", err)
		return
	}

	// Parse user commitments
	userCpfpRootTxCommitment := frost.SigningCommitment{}
	if err = userCpfpRootTxCommitment.UnmarshalProto(req.RootTxSigningJob.SigningNonceCommitment); err != nil {
		err = fmt.Errorf("invalid root tx signing commitment: %w", err)
		return
	}

	userCpfpRefundTxCommitment := frost.SigningCommitment{}
	if err = userCpfpRefundTxCommitment.UnmarshalProto(req.RefundTxSigningJob.SigningNonceCommitment); err != nil {
		err = fmt.Errorf("invalid refund tx signing commitment: %w", err)
		return
	}

	// Create base signing jobs
	signingJobs = []*helper.SigningJob{
		{
			JobID:             uuid.New(),
			SigningKeyshareID: signingKeyShare.ID,
			Message:           cpfpRootTxSigHash,
			VerifyingKey:      &verifyingKey,
			UserCommitment:    &userCpfpRootTxCommitment,
		},
		{
			JobID:             uuid.New(),
			SigningKeyshareID: signingKeyShare.ID,
			Message:           cpfpRefundTxSigHash,
			VerifyingKey:      &verifyingKey,
			UserCommitment:    &userCpfpRefundTxCommitment,
		},
	}

	// Handle DirectFromCpfpRefund transaction
	var directFromCpfpRefundTx *wire.MsgTx
	var directFromCpfpRefundTxSigHash []byte

	directFromCpfpRefundTx, err = common.TxFromRawTxBytes(req.DirectFromCpfpRefundTxSigningJob.RawTx)
	if err != nil {
		err = fmt.Errorf("invalid direct from cpfp refund transaction: %w", err)
		return
	}
	if err = o.verifyRefundTransaction(cpfpRootTx, directFromCpfpRefundTx); err != nil {
		err = fmt.Errorf("direct from cpfp refund verification failed: %w", err)
		return
	}
	if len(cpfpRootTx.TxOut) == 0 {
		err = fmt.Errorf("vout out of bounds, root tx has no outputs")
		return
	}
	directFromCpfpRefundTxSigHash, err = common.SigHashFromTx(directFromCpfpRefundTx, 0, cpfpRootTx.TxOut[0])
	if err != nil {
		err = fmt.Errorf("failed to compute direct from cpfp refund tx sighash: %w", err)
		return
	}

	userDirectFromCpfpRefundTxCommitment := frost.SigningCommitment{}
	if err = userDirectFromCpfpRefundTxCommitment.UnmarshalProto(req.DirectFromCpfpRefundTxSigningJob.SigningNonceCommitment); err != nil {
		err = fmt.Errorf("invalid direct from cpfp refund tx signing commitment: %w", err)
		return
	}
	signingJobs = append(signingJobs, &helper.SigningJob{
		JobID:             uuid.New(),
		SigningKeyshareID: signingKeyShare.ID,
		Message:           directFromCpfpRefundTxSigHash,
		VerifyingKey:      &verifyingKey,
		UserCommitment:    &userDirectFromCpfpRefundTxCommitment,
	})

	return
}

// aggregateSignatures aggregates SE and user signature shares
func (o *DepositHandler) aggregateSignatures(
	ctx context.Context,
	config *so.Config,
	req *pb.FinalizeDepositTreeCreationRequest,
	signingResults []*helper.SigningResult,
	verifyingKey keys.Public,
	rootSigningPubKey keys.Public,
) ([][]byte, error) {
	// Connect to FROST service
	frostConn, err := config.NewFrostGRPCConnection()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to FROST signer: %w", err)
	}
	defer frostConn.Close()

	frostClient := pbfrost.NewFrostServiceClient(frostConn)
	logger := logging.GetLoggerFromContext(ctx)

	// Aggregate root transaction signature using SE commitments from client request
	logger.Sugar().Infof("Aggregating cpfp root tx signature")
	rootSigResult, err := frostClient.AggregateFrost(ctx, &pbfrost.AggregateFrostRequest{
		Message:            signingResults[0].Message,
		SignatureShares:    signingResults[0].SignatureShares,
		PublicShares:       signingResults[0].PublicKeys,
		VerifyingKey:       verifyingKey.Serialize(),
		Commitments:        req.RootTxSigningJob.SigningCommitments.SigningCommitments,
		UserCommitments:    req.RootTxSigningJob.SigningNonceCommitment,
		UserPublicKey:      rootSigningPubKey.Serialize(),
		UserSignatureShare: req.RootTxSigningJob.UserSignature,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate root tx signature: %w", err)
	}

	// Aggregate refund transaction signature using SE commitments from client request
	logger.Sugar().Infof("Aggregating cpfp refund tx signature")
	refundSigResult, err := frostClient.AggregateFrost(ctx, &pbfrost.AggregateFrostRequest{
		Message:            signingResults[1].Message,
		SignatureShares:    signingResults[1].SignatureShares,
		PublicShares:       signingResults[1].PublicKeys,
		VerifyingKey:       verifyingKey.Serialize(),
		Commitments:        req.RefundTxSigningJob.SigningCommitments.SigningCommitments,
		UserCommitments:    req.RefundTxSigningJob.SigningNonceCommitment,
		UserPublicKey:      rootSigningPubKey.Serialize(),
		UserSignatureShare: req.RefundTxSigningJob.UserSignature,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate refund tx signature: %w", err)
	}

	// Aggregate DirectFromCpfpRefund signature
	logger.Sugar().Infof("Aggregating direct from cpfp refund tx signature")
	directFromCpfpRefundSigResult, err := frostClient.AggregateFrost(ctx, &pbfrost.AggregateFrostRequest{
		Message:            signingResults[2].Message,
		SignatureShares:    signingResults[2].SignatureShares,
		PublicShares:       signingResults[2].PublicKeys,
		VerifyingKey:       verifyingKey.Serialize(),
		Commitments:        req.DirectFromCpfpRefundTxSigningJob.SigningCommitments.SigningCommitments,
		UserCommitments:    req.DirectFromCpfpRefundTxSigningJob.SigningNonceCommitment,
		UserPublicKey:      rootSigningPubKey.Serialize(),
		UserSignatureShare: req.DirectFromCpfpRefundTxSigningJob.UserSignature,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate direct from cpfp refund tx signature: %w", err)
	}

	return [][]byte{rootSigResult.Signature, refundSigResult.Signature, directFromCpfpRefundSigResult.Signature}, nil
}

// applySignaturesToTransactions applies aggregated signatures to the raw transactions
func (o *DepositHandler) applySignaturesToTransactions(
	req *pb.FinalizeDepositTreeCreationRequest,
	signatures [][]byte,
) (signedCpfpRootTx []byte, signedCpfpRefundTx []byte, signedDirectFromCpfpRefundTx []byte, err error) {
	// Apply signature to CPFP root transaction
	signedCpfpRootTx, err = common.UpdateTxWithSignature(req.RootTxSigningJob.RawTx, 0, signatures[0])
	if err != nil {
		err = fmt.Errorf("failed to apply signature to cpfp root tx: %w", err)
		return
	}

	// Apply signature to CPFP refund transaction
	signedCpfpRefundTx, err = common.UpdateTxWithSignature(req.RefundTxSigningJob.RawTx, 0, signatures[1])
	if err != nil {
		err = fmt.Errorf("failed to apply signature to cpfp refund tx: %w", err)
		return
	}

	// Apply signature to DirectFromCpfpRefund transaction
	signedDirectFromCpfpRefundTx, err = common.UpdateTxWithSignature(req.DirectFromCpfpRefundTxSigningJob.RawTx, 0, signatures[2])
	if err != nil {
		err = fmt.Errorf("failed to apply signature to direct from cpfp refund tx: %w", err)
		return
	}

	return
}

// createTreeAndNode creates the tree and root node in the database
func (o *DepositHandler) createTreeAndNode(
	ctx context.Context,
	depositAddress *ent.DepositAddress,
	onChainTx *wire.MsgTx,
	onChainOutput *wire.TxOut,
	req *pb.FinalizeDepositTreeCreationRequest,
	network btcnetwork.Network,
	verifyingKey keys.Public,
	signedCpfpRootTx []byte,
	signedCpfpRefundTx []byte,
	signedDirectFromCpfpRefundTx []byte,
) (*ent.Tree, *ent.TreeNode, error) {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get database: %w", err)
	}

	logger := logging.GetLoggerFromContext(ctx)
	txid := onChainTx.TxHash()

	// Check if tree already exists
	existingTree, err := db.Tree.Query().
		Where(tree.BaseTxid(st.NewTxID(txid))).
		Where(tree.Vout(int16(req.OnChainUtxo.Vout))).
		WithRoot().
		First(ctx)

	if err != nil && !ent.IsNotFound(err) {
		return nil, nil, fmt.Errorf("failed to query for existing tree: %w", err)
	}

	if existingTree != nil {
		logger.Sugar().Warnf("Tree already exists for txid %s vout %d", txid.String(), req.OnChainUtxo.Vout)

		// Use the Tree→Root relationship to get the root node
		if existingTree.Edges.Root != nil {
			return existingTree, existingTree.Edges.Root, nil
		}

		// If Root edge is not populated, query for the root node belonging to this tree
		rootNode, err := db.TreeNode.Query().
			Where(treenode.HasTreeWith(tree.ID(existingTree.ID))).
			Where(treenode.Not(treenode.HasParent())).
			First(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to find root node for existing tree: %w", err)
		}
		return existingTree, rootNode, nil
	}

	var treeStatus st.TreeStatus
	var treeNodeStatus st.TreeNodeStatus
	if depositAddress.AvailabilityConfirmedAt.IsZero() {
		treeStatus = st.TreeStatusPending
		treeNodeStatus = st.TreeNodeStatusCreating
	} else {
		treeStatus = st.TreeStatusAvailable
		treeNodeStatus = st.TreeNodeStatusAvailable
	}

	// Create new tree following StartDepositTreeCreation pattern
	signingKeyShare := depositAddress.Edges.SigningKeyshare

	// Create tree with Pending status if the DepositAddress is not available yet.
	// chain watcher will mark it Available after confirming the transaction and
	// verifying signatures
	newTree := db.Tree.Create().
		SetOwnerIdentityPubkey(depositAddress.OwnerIdentityPubkey).
		SetNetwork(network).
		SetBaseTxid(st.NewTxID(txid)).
		SetVout(int16(req.OnChainUtxo.Vout)).
		SetDepositAddress(depositAddress).
		SetStatus(treeStatus)

	createdTree, err := newTree.Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, nil, errors.AlreadyExistsDuplicateOperation(fmt.Errorf("tree already exists: %w", err))
		}
		return nil, nil, err
	}

	// Create root node with signed transactions
	rootNode := db.TreeNode.Create().
		SetTree(createdTree).
		SetNetwork(network).
		SetStatus(treeNodeStatus).
		SetOwnerIdentityPubkey(depositAddress.OwnerIdentityPubkey).
		SetOwnerSigningPubkey(depositAddress.OwnerSigningPubkey).
		SetValue(uint64(onChainOutput.Value)).
		SetVerifyingPubkey(verifyingKey).
		SetSigningKeyshare(signingKeyShare).
		SetRawTx(signedCpfpRootTx).
		SetRawRefundTx(signedCpfpRefundTx).
		SetVout(int16(req.OnChainUtxo.Vout))

	// Add signed direct transactions if present
	if len(signedDirectFromCpfpRefundTx) > 0 {
		rootNode.SetDirectFromCpfpRefundTx(signedDirectFromCpfpRefundTx)
	}

	if depositAddress.NodeID != uuid.Nil {
		rootNode.SetID(depositAddress.NodeID)
	}

	createdNode, err := rootNode.Save(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create root node: %w", err)
	}

	logger.Sugar().Infof("Created root node %s for tree %s", createdNode.ID, createdTree.ID)

	// Update tree with root node
	createdTree, err = createdTree.Update().SetRoot(createdNode).Save(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to update tree with root: %w", err)
	}

	return createdTree, createdNode, nil
}

// convertToSigningJobsWithPregeneratedNonce converts signing jobs to jobs with pregenerated nonces
// using the SE commitments provided by the client
func (o *DepositHandler) convertToSigningJobsWithPregeneratedNonce(
	signingJobs []*helper.SigningJob,
	req *pb.FinalizeDepositTreeCreationRequest,
) ([]*helper.SigningJobWithPregeneratedNonce, error) {
	result := make([]*helper.SigningJobWithPregeneratedNonce, len(signingJobs))

	// Root transaction
	rootCommitments := make(map[string]frost.SigningCommitment)
	for key, commitment := range req.RootTxSigningJob.SigningCommitments.SigningCommitments {
		obj := frost.SigningCommitment{}
		if err := obj.UnmarshalProto(commitment); err != nil {
			return nil, fmt.Errorf("failed to unmarshal root tx SE commitment for key %s: %w", key, err)
		}
		rootCommitments[key] = obj
	}
	result[0] = &helper.SigningJobWithPregeneratedNonce{
		SigningJob:     *signingJobs[0],
		Round1Packages: rootCommitments,
	}

	// Refund transaction
	refundCommitments := make(map[string]frost.SigningCommitment)
	for key, commitment := range req.RefundTxSigningJob.SigningCommitments.SigningCommitments {
		obj := frost.SigningCommitment{}
		if err := obj.UnmarshalProto(commitment); err != nil {
			return nil, fmt.Errorf("failed to unmarshal refund tx SE commitment for key %s: %w", key, err)
		}
		refundCommitments[key] = obj
	}
	result[1] = &helper.SigningJobWithPregeneratedNonce{
		SigningJob:     *signingJobs[1],
		Round1Packages: refundCommitments,
	}

	// DirectFromCpfpRefund transaction
	directFromCpfpRefundCommitments := make(map[string]frost.SigningCommitment)
	for key, commitment := range req.DirectFromCpfpRefundTxSigningJob.SigningCommitments.SigningCommitments {
		obj := frost.SigningCommitment{}
		if err := obj.UnmarshalProto(commitment); err != nil {
			return nil, fmt.Errorf("failed to unmarshal direct from cpfp refund tx SE commitment for key %s: %w", key, err)
		}
		directFromCpfpRefundCommitments[key] = obj
	}
	result[2] = &helper.SigningJobWithPregeneratedNonce{
		SigningJob:     *signingJobs[2],
		Round1Packages: directFromCpfpRefundCommitments,
	}

	return result, nil
}

func (o *DepositHandler) sendFinalizeNodeGossip(
	ctx context.Context,
	tree *ent.Tree,
	rootNode *ent.TreeNode,
) error {
	selection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}
	participants, err := selection.OperatorIdentifierList(o.config)
	if err != nil {
		return fmt.Errorf("unable to get operator list: %w", err)
	}
	sendGossipHandler := NewSendGossipHandler(o.config)

	protoNetwork, err := tree.Network.ToProtoNetwork()
	if err != nil {
		return err
	}

	// Load the node with all required edges for marshaling
	// This must happen within the transaction before it commits
	// We MUST load all edges that MarshalInternalProto might query
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to get database: %w", err)
	}

	rootNodeWithEdges, err := db.TreeNode.Query().
		Where(treenode.ID(rootNode.ID)).
		WithTree().
		WithSigningKeyshare().
		WithParent(). // Load parent edge to avoid lazy loading in getParentNodeID
		Only(ctx)
	if err != nil {
		return fmt.Errorf("failed to load root node with edges: %w", err)
	}

	// Marshal the node BEFORE CreateCommitAndSendGossipMessage commits the transaction
	// This ensures all database queries happen within the active transaction
	internalNode, err := rootNodeWithEdges.MarshalInternalProto(ctx)
	if err != nil {
		return fmt.Errorf("failed to marshal root node %s: %w", rootNode.ID.String(), err)
	}
	internalNodes := []*spark_internal.TreeNode{internalNode}

	_, err = sendGossipHandler.CreateCommitAndSendGossipMessage(ctx, &pbgossip.GossipMessage{
		Message: &pbgossip.GossipMessage_FinalizeTreeCreation{
			FinalizeTreeCreation: &pbgossip.GossipMessageFinalizeTreeCreation{
				InternalNodes: internalNodes,
				ProtoNetwork:  protoNetwork,
			},
		},
	}, participants)
	if err != nil {
		return fmt.Errorf("unable to create and send gossip message: %w", err)
	}

	return nil
}
