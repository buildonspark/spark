package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/lightsparkdev/spark/common/keys"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/logging"
	secretsharing "github.com/lightsparkdev/spark/common/secret_sharing"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pbfrost "github.com/lightsparkdev/spark/proto/frost"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authn"
	"github.com/lightsparkdev/spark/so/authz"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/preimagerequest"
	"github.com/lightsparkdev/spark/so/ent/preimageshare"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/treenode"
	"github.com/lightsparkdev/spark/so/helper"
	"github.com/lightsparkdev/spark/so/knobs"
	decodepay "github.com/nbd-wtf/ln-decodepay"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

const (
	MaximumExpiryTime = 5 * time.Minute
)

// LightningHandler is the handler for the lightning service.
type LightningHandler struct {
	config *so.Config
}

// NewLightningHandler returns a new LightningHandler.
func NewLightningHandler(config *so.Config) *LightningHandler {
	return &LightningHandler{config: config}
}

// StorePreimageShare stores the preimage share for the given payment hash.
func (h *LightningHandler) StorePreimageShare(ctx context.Context, req *pb.StorePreimageShareRequest) error {
	if req.PreimageShare == nil {
		return fmt.Errorf("preimage share is nil")
	}
	if len(req.PreimageShare.Proofs) == 0 {
		return fmt.Errorf("preimage share proofs is empty")
	}

	err := secretsharing.ValidateShare(
		&secretsharing.VerifiableSecretShare{
			SecretShare: secretsharing.SecretShare{
				FieldModulus: secp256k1.S256().N,
				Threshold:    int(h.config.Threshold),
				Index:        big.NewInt(int64(h.config.Index + 1)),
				Share:        new(big.Int).SetBytes(req.PreimageShare.SecretShare),
			},
			Proofs: req.PreimageShare.Proofs,
		},
	)
	if err != nil {
		return fmt.Errorf("unable to validate share: %w", err)
	}

	bolt11, err := decodepay.Decodepay(req.InvoiceString)
	if err != nil {
		return fmt.Errorf("unable to decode invoice: %w", err)
	}

	paymentHash, err := hex.DecodeString(bolt11.PaymentHash)
	if err != nil {
		return fmt.Errorf("unable to decode payment hash: %w", err)
	}

	if !bytes.Equal(paymentHash, req.PaymentHash) {
		return fmt.Errorf("payment hash mismatch")
	}

	tx, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to get or create current tx for request: %w", err)
	}
	_, err = tx.PreimageShare.Create().
		SetPaymentHash(req.PaymentHash).
		SetPreimageShare(req.PreimageShare.SecretShare).
		SetThreshold(int32(req.Threshold)).
		SetInvoiceString(req.InvoiceString).
		SetOwnerIdentityPubkey(req.UserIdentityPublicKey).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("unable to store preimage share: %w", err)
	}
	return nil
}

func (h *LightningHandler) validateNodeOwnership(ctx context.Context, nodes []*ent.TreeNode) error {
	if !h.config.IsAuthzEnforced() {
		return nil
	}

	session, err := authn.GetSessionFromContext(ctx)
	if err != nil {
		return err
	}
	sessionIdentityPubkeyBytes := session.IdentityPublicKey().Serialize()

	var mismatchedNodes []string
	for _, node := range nodes {
		if !bytes.Equal(node.OwnerIdentityPubkey, sessionIdentityPubkeyBytes) {
			mismatchedNodes = append(mismatchedNodes, node.ID.String())
		}
	}

	if len(mismatchedNodes) > 0 {
		return &authz.Error{
			Code: authz.ErrorCodeIdentityMismatch,
			Message: fmt.Sprintf("nodes [%s] are not owned by the authenticated identity public key %x",
				strings.Join(mismatchedNodes, ", "),
				sessionIdentityPubkeyBytes),
			Cause: nil,
		}
	}
	return nil
}

func (h *LightningHandler) validateHasSession(ctx context.Context) error {
	if h.config.IsAuthzEnforced() {
		_, err := authn.GetSessionFromContext(ctx)
		if err != nil {
			return err
		}
	}
	return nil
}

// GetSigningCommitments gets the signing commitments for the given node ids.
func (h *LightningHandler) GetSigningCommitments(ctx context.Context, req *pb.GetSigningCommitmentsRequest) (*pb.GetSigningCommitmentsResponse, error) {
	if err := h.validateHasSession(ctx); err != nil {
		return nil, err
	}

	tx, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}
	nodeIDs := make([]uuid.UUID, len(req.NodeIds))
	for i, nodeID := range req.NodeIds {
		nodeID, err := uuid.Parse(nodeID)
		if err != nil {
			return nil, fmt.Errorf("unable to parse node id: %w", err)
		}
		nodeIDs[i] = nodeID
	}

	nodes, err := tx.TreeNode.Query().WithSigningKeyshare().Where(treenode.IDIn(nodeIDs...)).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get nodes: %w", err)
	}

	if err := h.validateNodeOwnership(ctx, nodes); err != nil {
		return nil, err
	}

	keyshareIDs := make([]uuid.UUID, len(nodes))
	for i, node := range nodes {
		if node.Edges.SigningKeyshare == nil {
			return nil, fmt.Errorf("node %s has no keyshare", node.ID)
		}
		keyshareIDs[i] = node.Edges.SigningKeyshare.ID
	}

	count := req.Count
	if count == 0 {
		count = 1
	}

	commitments, err := helper.GetSigningCommitments(ctx, h.config, keyshareIDs, count)
	if err != nil {
		return nil, fmt.Errorf("unable to get signing commitments: %w", err)
	}

	commitmentsArray := common.MapOfArrayToArrayOfMap(commitments)

	requestedCommitments := make([]*pb.RequestedSigningCommitments, len(commitmentsArray))

	for i, commitment := range commitmentsArray {
		commitmentMapProto, err := common.ConvertObjectMapToProtoMap(commitment)
		if err != nil {
			return nil, fmt.Errorf("unable to convert signing commitment to proto: %w", err)
		}
		requestedCommitments[i] = &pb.RequestedSigningCommitments{
			SigningNonceCommitments: commitmentMapProto,
		}
	}

	return &pb.GetSigningCommitmentsResponse{SigningCommitments: requestedCommitments}, nil
}

func (h *LightningHandler) ValidateDuplicateLeaves(
	ctx context.Context,
	leavesToSend []*pb.UserSignedTxSigningJob,
	directLeavesToSend []*pb.UserSignedTxSigningJob,
	directFromCpfpLeavesToSend []*pb.UserSignedTxSigningJob,
) error {
	logger := logging.GetLoggerFromContext(ctx)
	logger.Info("validating duplicate leaves", "leavesToSend", leavesToSend, "directLeavesToSend", directLeavesToSend, "directFromCpfpLeavesToSend", directFromCpfpLeavesToSend)
	leavesMap := make(map[string]bool)
	directLeavesMap := make(map[string]bool)
	directFromCpfpLeavesMap := make(map[string]bool)
	for _, leaf := range leavesToSend {
		if leavesMap[leaf.LeafId] {
			return fmt.Errorf("duplicate leaf id: %s", leaf.LeafId)
		}
		leavesMap[leaf.LeafId] = true
	}
	for _, leaf := range directLeavesToSend {
		if directLeavesMap[leaf.LeafId] {
			return fmt.Errorf("duplicate leaf id: %s", leaf.LeafId)
		}
		if !leavesMap[leaf.LeafId] {
			return fmt.Errorf("leaf id %s not found in leaves to send", leaf.LeafId)
		}
		directLeavesMap[leaf.LeafId] = true
	}
	for _, leaf := range directFromCpfpLeavesToSend {
		if directFromCpfpLeavesMap[leaf.LeafId] {
			return fmt.Errorf("duplicate leaf id: %s", leaf.LeafId)
		}
		if !leavesMap[leaf.LeafId] {
			return fmt.Errorf("leaf id %s not found in leaves to send", leaf.LeafId)
		}
		directFromCpfpLeavesMap[leaf.LeafId] = true
	}
	return nil
}

type frostServiceClientConnection interface {
	StartFrostServiceClient(h *LightningHandler) (pbfrost.FrostServiceClient, error)
	Close()
}

type defaultFrostServiceClientConnection struct {
	conn *grpc.ClientConn
}

func (f *defaultFrostServiceClientConnection) StartFrostServiceClient(h *LightningHandler) (pbfrost.FrostServiceClient, error) {
	var err error

	if f.conn != nil {
		return nil, fmt.Errorf("frost service client already started")
	}

	f.conn, err = h.config.NewFrostGRPCConnection()
	if err != nil {
		return nil, fmt.Errorf("unable to connect to signer: %w", err)
	}

	return pbfrost.NewFrostServiceClient(f.conn), nil
}

func (f *defaultFrostServiceClientConnection) Close() {
	// The only caller is a defer and doesn't handle errors
	_ = f.conn.Close()
}

func (h *LightningHandler) ValidateGetPreimageRequest(
	ctx context.Context,
	paymentHash []byte,
	cpfpTransactions []*pb.UserSignedTxSigningJob,
	directTransactions []*pb.UserSignedTxSigningJob,
	directFromCpfpTransactions []*pb.UserSignedTxSigningJob,
	amount *pb.InvoiceAmount,
	destinationPubKey keys.Public,
	feeSats uint64,
	reason pb.InitiatePreimageSwapRequest_Reason,
	validateNodeOwnership bool,
) error {
	return h.validateGetPreimageRequestWithFrostServiceClientFactory(ctx, &defaultFrostServiceClientConnection{}, paymentHash, cpfpTransactions, directTransactions, directFromCpfpTransactions, amount, destinationPubKey, feeSats, reason, validateNodeOwnership)
}

func (h *LightningHandler) validateGetPreimageRequestWithFrostServiceClientFactory(
	ctx context.Context,
	frostServiceClientConnection frostServiceClientConnection,
	paymentHash []byte,
	cpfpTransactions []*pb.UserSignedTxSigningJob,
	directTransactions []*pb.UserSignedTxSigningJob,
	directFromCpfpTransactions []*pb.UserSignedTxSigningJob,
	amount *pb.InvoiceAmount,
	destinationPubKey keys.Public,
	feeSats uint64,
	reason pb.InitiatePreimageSwapRequest_Reason,
	validateNodeOwnership bool,
) error {
	logger := logging.GetLoggerFromContext(ctx)

	// Validate input parameters
	if len(paymentHash) != 32 {
		return fmt.Errorf("invalid payment hash length: %d bytes, expected 32 bytes", len(paymentHash))
	}

	if len(cpfpTransactions) == 0 && len(directTransactions) == 0 && len(directFromCpfpTransactions) == 0 {
		return fmt.Errorf("at least one transaction type must be provided")
	}

	// Validate transaction limits to prevent DoS
	maxTransactionsPerRequest := int(knobs.GetKnobsService(ctx).GetValue(knobs.KnobSoMaxTransactionsPerRequest, 100))
	totalTransactions := len(cpfpTransactions) + len(directTransactions) + len(directFromCpfpTransactions)
	if totalTransactions > maxTransactionsPerRequest {
		return fmt.Errorf("too many transactions: %d, maximum allowed: %d", totalTransactions, maxTransactionsPerRequest)
	}

	// Step 0 Validate that there's no existing preimage request for this payment hash
	tx, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to get or create current tx for request: %w", err)
	}
	// Check for existing preimage requests (duplicate prevention)
	preimageRequests, err := tx.PreimageRequest.Query().Where(
		preimagerequest.PaymentHashEQ(paymentHash),
		preimagerequest.ReceiverIdentityPubkeyEQ(destinationPubKey.Serialize()),
		preimagerequest.StatusNEQ(st.PreimageRequestStatusReturned),
	).All(ctx)
	if err != nil {
		return fmt.Errorf("unable to get preimage request with paymentHash %x: %w ", paymentHash, err)
	}
	if len(preimageRequests) > 0 {
		return fmt.Errorf("preimage request already exists for paymentHash %x", paymentHash)
	}

	// Step 1 validate all signatures are valid
	client, err := frostServiceClientConnection.StartFrostServiceClient(h)
	if err != nil {
		return fmt.Errorf("unable to start frost service client: %w", err)
	}
	defer frostServiceClientConnection.Close()

	var nodes []*ent.TreeNode
	// Validate CPFP transaction.
	for i := range cpfpTransactions {
		cpfpTransaction := cpfpTransactions[i]

		if cpfpTransaction == nil {
			return fmt.Errorf("cpfp transaction is nil")
		}

		// Validate leaf ID format
		if len(cpfpTransaction.LeafId) == 0 {
			return fmt.Errorf("leaf ID cannot be empty")
		}

		nodeID, err := uuid.Parse(cpfpTransaction.LeafId)
		if err != nil {
			return fmt.Errorf("unable to parse node id: %w", err)
		}

		if cpfpTransaction.SigningCommitments == nil {
			return fmt.Errorf("signing commitments is nil for cpfpTransaction, leaf_id: %s", nodeID)
		}

		if cpfpTransaction.SigningNonceCommitment == nil {
			return fmt.Errorf("signing nonce commitment is nil for cpfpTransaction, leaf_id: %s", nodeID)
		}

		// Validate raw transaction data
		if len(cpfpTransaction.RawTx) == 0 {
			return fmt.Errorf("raw transaction data cannot be empty for cpfpTransaction, leaf_id: %s", nodeID)
		}

		const MaxTransactionSize = 100000 // 100KB limit for individual transactions
		if len(cpfpTransaction.RawTx) > MaxTransactionSize {
			return fmt.Errorf("raw transaction too large: %d bytes, maximum allowed: %d bytes for leaf_id: %s", len(cpfpTransaction.RawTx), MaxTransactionSize, nodeID)
		}

		node, err := tx.TreeNode.Get(ctx, nodeID)
		if err != nil {
			return fmt.Errorf("unable to get cpfpTransaction tree_node with id: %s: %w", nodeID, err)
		}
		nodes = append(nodes, node)
		if node.Status != st.TreeNodeStatusAvailable {
			return fmt.Errorf("node %v is not available: %v", node.ID, node.Status)
		}
		keyshare, err := node.QuerySigningKeyshare().First(ctx)
		if err != nil {
			return fmt.Errorf("unable to get keyshare for cpfpTransaction, tree_node id: %s: %w", nodeID, err)
		}
		cpfpTx, err := common.TxFromRawTxBytes(node.RawTx)
		if err != nil {
			return fmt.Errorf("unable to get cpfpTx for cpfpTransaction, tree_node id: %s: %w", nodeID, err)
		}

		cpfpRefundTx, err := common.TxFromRawTxBytes(cpfpTransaction.RawTx)
		if err != nil {
			return fmt.Errorf("unable to get cpfp refund tx for cpfpTransaction, tree_node id: %s: %w", nodeID, err)
		}

		if len(cpfpTx.TxOut) <= 0 {
			return fmt.Errorf("cpfpTx vout out of bounds for cpfpTransaction, tree_node id: %s", nodeID)
		}
		cpfpSighash, err := common.SigHashFromTx(cpfpRefundTx, 0, cpfpTx.TxOut[0])
		if err != nil {
			return fmt.Errorf("unable to get cpfp sighash for cpfpTransaction, tree_node id: %s: %w", nodeID, err)
		}

		realUserPublicKey, err := common.SubtractPublicKeys(node.VerifyingPubkey, keyshare.PublicKey)
		if err != nil {
			return fmt.Errorf("unable to get real user public key for cpfpTransaction, tree_node id: %s: %w", nodeID, err)
		}

		if !bytes.Equal(realUserPublicKey, node.OwnerSigningPubkey) {
			logger.Debug("real user public key mismatch", "expected", hex.EncodeToString(node.OwnerSigningPubkey), "got", hex.EncodeToString(realUserPublicKey))
			node, err = node.Update().SetOwnerSigningPubkey(realUserPublicKey).Save(ctx)
			if err != nil {
				return fmt.Errorf("unable to update tree_node: %s: %w", nodeID, err)
			}
		}

		_, err = client.ValidateSignatureShare(ctx, &pbfrost.ValidateSignatureShareRequest{
			Message:         cpfpSighash,
			SignatureShare:  cpfpTransaction.UserSignature,
			Role:            pbfrost.SigningRole_USER,
			VerifyingKey:    node.VerifyingPubkey,
			PublicShare:     node.OwnerSigningPubkey,
			Commitments:     cpfpTransaction.SigningCommitments.SigningCommitments,
			UserCommitments: cpfpTransaction.SigningNonceCommitment,
		})
		if err != nil {
			return fmt.Errorf("unable to validate cpfp signature share: %w, for sighash: %v, user pubkey: %v", err, hex.EncodeToString(cpfpSighash), hex.EncodeToString(node.OwnerSigningPubkey))
		}
	}

	// Only validate direct and direct-from-cpfp transactions if both are present
	for i := range directTransactions {
		directTransaction := directTransactions[i]

		if directTransaction == nil {
			return fmt.Errorf("direct transaction is nil")
		}

		nodeID, err := uuid.Parse(directTransaction.LeafId)
		if err != nil {
			return fmt.Errorf("unable to parse node id: %w", err)
		}

		if directTransaction.SigningCommitments == nil {
			return fmt.Errorf("signing commitments is nil for directTransaction, leaf_id: %s", nodeID)
		}

		if directTransaction.SigningNonceCommitment == nil {
			return fmt.Errorf("signing nonce commitment is nil for directTransaction, leaf_id: %s", nodeID)
		}

		node, err := tx.TreeNode.Get(ctx, nodeID)
		if err != nil {
			return fmt.Errorf("unable to get tree_node with id: %s: %w", nodeID, err)
		}

		directTx, err := common.TxFromRawTxBytes(node.DirectTx)
		if err != nil {
			return fmt.Errorf("unable to get directTx for directTransaction, tree_node id: %s: %w", nodeID, err)
		}
		directRefundTx, err := common.TxFromRawTxBytes(directTransaction.RawTx)
		if err != nil {
			return fmt.Errorf("unable to get direct refund tx for directTransaction, tree_node id: %s: %w", nodeID, err)
		}
		if len(directTx.TxOut) <= 0 {
			return fmt.Errorf("direct tx vout out of bounds for directTransaction, tree_node id: %s", nodeID)
		}
		directSighash, err := common.SigHashFromTx(directRefundTx, 0, directTx.TxOut[0])
		if err != nil {
			return fmt.Errorf("unable to get direct sighash for directTransaction, tree_node id: %s: %w", nodeID, err)
		}

		_, err = client.ValidateSignatureShare(ctx, &pbfrost.ValidateSignatureShareRequest{
			Message:         directSighash,
			SignatureShare:  directTransaction.UserSignature,
			Role:            pbfrost.SigningRole_USER,
			VerifyingKey:    node.VerifyingPubkey,
			PublicShare:     node.OwnerSigningPubkey,
			Commitments:     directTransaction.SigningCommitments.SigningCommitments,
			UserCommitments: directTransaction.SigningNonceCommitment,
		})
		if err != nil {
			return fmt.Errorf("unable to validate direct signature share: %w, for sighash: %v, user pubkey: %v", err, hex.EncodeToString(directSighash), hex.EncodeToString(node.OwnerSigningPubkey))
		}
	}

	// Validate direct-from-cpfp transactions
	for i := range directFromCpfpTransactions {
		directFromCpfpTransaction := directFromCpfpTransactions[i]
		if directFromCpfpTransaction == nil {
			return fmt.Errorf("direct from cpfp transaction is nil")
		}

		nodeID, err := uuid.Parse(directFromCpfpTransaction.LeafId)
		if err != nil {
			return fmt.Errorf("unable to parse node id for directFromCpfpTransaction: %w", err)
		}

		if directFromCpfpTransaction.SigningCommitments == nil {
			return fmt.Errorf("signing commitments is nil for directFromCpfpTransaction, leaf_id: %s", nodeID)
		}

		if directFromCpfpTransaction.SigningNonceCommitment == nil {
			return fmt.Errorf("signing nonce commitment is nil for directFromCpfpTransaction, leaf_id: %s", nodeID)
		}

		node, err := tx.TreeNode.Get(ctx, nodeID)
		if err != nil {
			return fmt.Errorf("unable to get tree_node with id: %s for directFromCpfpTransaction: %w", nodeID, err)
		}

		cpfpTx, err := common.TxFromRawTxBytes(node.RawTx)
		if err != nil {
			return fmt.Errorf("unable to get cpfpTx for directFromCpfpTransaction, tree_node id: %s: %w", nodeID, err)
		}
		directFromCpfpRefundTx, err := common.TxFromRawTxBytes(directFromCpfpTransaction.RawTx)
		if err != nil {
			return fmt.Errorf("unable to get direct from cpfp refund tx for directFromCpfpTransaction, tree_node id: %s: %w", nodeID, err)
		}
		if len(cpfpTx.TxOut) <= 0 {
			return fmt.Errorf("direct from cpfp vout out of bounds for directFromCpfpTransaction, tree_node id: %s", nodeID)
		}
		directFromCpfpSighash, err := common.SigHashFromTx(directFromCpfpRefundTx, 0, cpfpTx.TxOut[0])
		if err != nil {
			return fmt.Errorf("unable to get direct from cpfp sighash for directFromCpfpTransaction, tree_node id: %s: %w", nodeID, err)
		}

		_, err = client.ValidateSignatureShare(ctx, &pbfrost.ValidateSignatureShareRequest{
			Message:         directFromCpfpSighash,
			SignatureShare:  directFromCpfpTransaction.UserSignature,
			Role:            pbfrost.SigningRole_USER,
			VerifyingKey:    node.VerifyingPubkey,
			PublicShare:     node.OwnerSigningPubkey,
			Commitments:     directFromCpfpTransaction.SigningCommitments.SigningCommitments,
			UserCommitments: directFromCpfpTransaction.SigningNonceCommitment,
		})
		if err != nil {
			return fmt.Errorf("unable to validate direct from cpfp signature share: %w, for sighash: %v, user pubkey: %v", err, hex.EncodeToString(directFromCpfpSighash), hex.EncodeToString(node.OwnerSigningPubkey))
		}
	}

	if validateNodeOwnership {
		err = h.validateNodeOwnership(ctx, nodes)
		if err != nil {
			return fmt.Errorf("unable to validate node ownership: %w", err)
		}
	}

	// Step 2 validate the amount is correct and paid to the destination pubkey
	var totalAmount uint64

	// Validate CPFP transactions
	for i := range cpfpTransactions {
		cpfpTransaction := cpfpTransactions[i]
		cpfpRefundTx, err := common.TxFromRawTxBytes(cpfpTransaction.RawTx)
		if err != nil {
			return fmt.Errorf("unable to get cpfp refund tx: %w", err)
		}

		pubkeyScript, err := common.P2TRScriptFromPubKey(destinationPubKey)
		if err != nil {
			return fmt.Errorf("unable to extract pubkey from tx: %w", err)
		}
		if len(cpfpRefundTx.TxOut) <= 0 {
			return fmt.Errorf("cpfp tx vout out of bounds")
		}
		if !bytes.Equal(pubkeyScript, cpfpRefundTx.TxOut[0].PkScript) {
			return fmt.Errorf("invalid cpfp destination pubkey")
		}
		totalAmount += uint64(cpfpRefundTx.TxOut[0].Value)
	}

	// Validate direct transactions
	for i := range directTransactions {
		directTransaction := directTransactions[i]
		directRefundTx, err := common.TxFromRawTxBytes(directTransaction.RawTx)
		if err != nil {
			return fmt.Errorf("unable to get direct refund tx for directTransaction leaf_id: %s: %w", directTransaction.LeafId, err)
		}

		pubkeyScript, err := common.P2TRScriptFromPubKey(destinationPubKey)
		if err != nil {
			return fmt.Errorf("unable to extract pubkey from tx for directTransaction leaf_id: %s: %w", directTransaction.LeafId, err)
		}
		if len(directRefundTx.TxOut) <= 0 {
			return fmt.Errorf("direct tx vout out of bounds for directTransaction leaf_id: %s", directTransaction.LeafId)
		}
		if !bytes.Equal(pubkeyScript, directRefundTx.TxOut[0].PkScript) {
			return fmt.Errorf("invalid direct destination pubkey for directTransaction leaf_id: %s", directTransaction.LeafId)
		}
	}

	// Validate direct-from-cpfp transactions
	for i := range directFromCpfpTransactions {
		directFromCpfpTransaction := directFromCpfpTransactions[i]
		directFromCpfpRefundTx, err := common.TxFromRawTxBytes(directFromCpfpTransaction.RawTx)
		if err != nil {
			return fmt.Errorf("unable to get direct from cpfp refund tx for directFromCpfpTransaction leaf_id: %s: %w", directFromCpfpTransaction.LeafId, err)
		}

		pubkeyScript, err := common.P2TRScriptFromPubKey(destinationPubKey)
		if err != nil {
			return fmt.Errorf("unable to extract pubkey from tx for directFromCpfpTransaction leaf_id: %s: %w", directFromCpfpTransaction.LeafId, err)
		}
		if len(directFromCpfpRefundTx.TxOut) <= 0 {
			return fmt.Errorf("direct from cpfp tx vout out of bounds for directFromCpfpTransaction leaf_id: %s", directFromCpfpTransaction.LeafId)
		}
		if !bytes.Equal(pubkeyScript, directFromCpfpRefundTx.TxOut[0].PkScript) {
			return fmt.Errorf("invalid direct from cpfp destination pubkey for directFromCpfpTransaction leaf_id: %s", directFromCpfpTransaction.LeafId)
		}
	}

	if reason == pb.InitiatePreimageSwapRequest_REASON_SEND {
		totalAmount -= feeSats
	}
	if amount.ValueSats != 0 && totalAmount < amount.ValueSats {
		return fmt.Errorf("invalid amount, expected: %d or more, got: %d", amount.ValueSats, totalAmount)
	}
	return nil
}

func (h *LightningHandler) storeUserSignedTransactions(
	ctx context.Context,
	paymentHash []byte,
	preimageShare *ent.PreimageShare,
	cpfpTransactions []*pb.UserSignedTxSigningJob,
	directTransactions []*pb.UserSignedTxSigningJob,
	directFromCpfpTransactions []*pb.UserSignedTxSigningJob,
	transfer *ent.Transfer,
	status st.PreimageRequestStatus,
	receiverIdentityPubKey keys.Public,
) (*ent.PreimageRequest, error) {
	tx, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}
	preimageRequestMutator := tx.PreimageRequest.Create().
		SetPaymentHash(paymentHash).
		SetReceiverIdentityPubkey(receiverIdentityPubKey.Serialize()).
		SetTransfers(transfer).
		SetStatus(status)
	if preimageShare != nil {
		preimageRequestMutator.SetPreimageShares(preimageShare)
	}
	preimageRequest, err := preimageRequestMutator.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to create preimage request: %w", err)
	}

	// Store CPFP transactions
	for i := range cpfpTransactions {
		cpfpTransaction := cpfpTransactions[i]
		cpfpCommitmentsBytes, err := proto.Marshal(cpfpTransaction.SigningCommitments)
		if err != nil {
			return nil, fmt.Errorf("unable to marshal signing commitments: %w", err)
		}

		nodeID, err := uuid.Parse(cpfpTransaction.LeafId)
		if err != nil {
			return nil, fmt.Errorf("unable to parse node id: %w", err)
		}
		cpfpUserSignatureCommitmentBytes, err := proto.Marshal(cpfpTransaction.SigningNonceCommitment)
		if err != nil {
			return nil, fmt.Errorf("unable to marshal cpfp user signature commitment: %w", err)
		}
		_, err = tx.UserSignedTransaction.Create().
			SetTransaction(cpfpTransaction.RawTx).
			SetUserSignature(cpfpTransaction.UserSignature).
			SetUserSignatureCommitment(cpfpUserSignatureCommitmentBytes).
			SetSigningCommitments(cpfpCommitmentsBytes).
			SetPreimageRequest(preimageRequest).
			SetTreeNodeID(nodeID).
			Save(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to store user signed transaction: %w", err)
		}

		node, err := tx.TreeNode.Get(ctx, nodeID)
		if err != nil {
			return nil, fmt.Errorf("unable to get node: %w", err)
		}
		_, err = tx.TreeNode.UpdateOne(node).SetStatus(st.TreeNodeStatusTransferLocked).Save(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to update node status: %w", err)
		}
	}

	// Store direct transactions if present
	for i := range directTransactions {
		directTransaction := directTransactions[i]
		directCommitmentsBytes, err := proto.Marshal(directTransaction.SigningCommitments)
		if err != nil {
			return nil, fmt.Errorf("unable to marshal signing commitments: %w", err)
		}
		nodeID, err := uuid.Parse(directTransaction.LeafId)
		if err != nil {
			return nil, fmt.Errorf("unable to parse node id: %w", err)
		}
		directUserSignatureCommitmentBytes, err := proto.Marshal(directTransaction.SigningNonceCommitment)
		if err != nil {
			return nil, fmt.Errorf("unable to marshal direct user signature commitment: %w", err)
		}
		_, err = tx.UserSignedTransaction.Create().
			SetTransaction(directTransaction.RawTx).
			SetUserSignature(directTransaction.UserSignature).
			SetUserSignatureCommitment(directUserSignatureCommitmentBytes).
			SetSigningCommitments(directCommitmentsBytes).
			SetPreimageRequest(preimageRequest).
			SetTreeNodeID(nodeID).
			Save(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to store user signed transaction: %w", err)
		}
	}

	// Store direct-from-cpfp transactions if present
	for i := range directFromCpfpTransactions {
		directFromCpfpTransaction := directFromCpfpTransactions[i]
		directFromCpfpCommitmentsBytes, err := proto.Marshal(directFromCpfpTransaction.SigningCommitments)
		if err != nil {
			return nil, fmt.Errorf("unable to marshal signing commitments: %w", err)
		}
		nodeID, err := uuid.Parse(directFromCpfpTransaction.LeafId)
		if err != nil {
			return nil, fmt.Errorf("unable to parse node id: %w", err)
		}
		directFromCpfpUserSignatureCommitmentBytes, err := proto.Marshal(directFromCpfpTransaction.SigningNonceCommitment)
		if err != nil {
			return nil, fmt.Errorf("unable to marshal direct from cpfp user signature commitment: %w", err)
		}
		_, err = tx.UserSignedTransaction.Create().
			SetTransaction(directFromCpfpTransaction.RawTx).
			SetUserSignature(directFromCpfpTransaction.UserSignature).
			SetUserSignatureCommitment(directFromCpfpUserSignatureCommitmentBytes).
			SetSigningCommitments(directFromCpfpCommitmentsBytes).
			SetPreimageRequest(preimageRequest).
			SetTreeNodeID(nodeID).
			Save(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to store user signed transaction: %w", err)
		}
	}

	return preimageRequest, nil
}

// GetPreimageShare gets the preimage share for the given payment hash.
func (h *LightningHandler) GetPreimageShare(ctx context.Context, req *pb.InitiatePreimageSwapRequest) ([]byte, error) {
	if req.Reason == pb.InitiatePreimageSwapRequest_REASON_RECEIVE && req.FeeSats != 0 {
		return nil, fmt.Errorf("fee is not allowed for receive preimage swap")
	}

	var preimageShare *ent.PreimageShare
	if req.Reason == pb.InitiatePreimageSwapRequest_REASON_RECEIVE {
		tx, err := ent.GetDbFromContext(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
		}
		preimageShare, err = tx.PreimageShare.Query().Where(preimageshare.PaymentHash(req.PaymentHash)).First(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to get preimage share: %w", err)
		}
		if !bytes.Equal(preimageShare.OwnerIdentityPubkey, req.ReceiverIdentityPublicKey) {
			return nil, fmt.Errorf("preimage share owner identity public key mismatch")
		}
	}

	invoiceAmount := req.InvoiceAmount
	if preimageShare != nil {
		bolt11, err := decodepay.Decodepay(preimageShare.InvoiceString)
		if err != nil {
			return nil, fmt.Errorf("unable to decode invoice: %w", err)
		}
		invoiceAmount = &pb.InvoiceAmount{
			ValueSats: uint64(bolt11.MSatoshi / 1000),
			InvoiceAmountProof: &pb.InvoiceAmountProof{
				Bolt11Invoice: preimageShare.InvoiceString,
			},
		}
	}

	err := h.ValidateDuplicateLeaves(ctx, req.Transfer.LeavesToSend, req.Transfer.DirectLeavesToSend, req.Transfer.DirectFromCpfpLeavesToSend)
	if err != nil {
		return nil, fmt.Errorf("unable to validate duplicate leaves: %w", err)
	}

	receiverIDPubKey, err := keys.ParsePublicKey(req.ReceiverIdentityPublicKey)
	if err != nil {
		return nil, fmt.Errorf("unable to parse receiver identity public key: %w", err)
	}
	err = h.ValidateGetPreimageRequest(
		ctx,
		req.PaymentHash,
		req.Transfer.LeavesToSend,
		req.Transfer.DirectLeavesToSend,
		req.Transfer.DirectFromCpfpLeavesToSend,
		invoiceAmount,
		receiverIDPubKey,
		req.FeeSats,
		req.Reason,
		false,
	)
	if err != nil {
		return nil, fmt.Errorf("unable to validate get preimage request: %w", err)
	}

	cpfpLeafRefundMap := make(map[string][]byte)
	directLeafRefundMap := make(map[string][]byte)
	directFromCpfpLeafRefundMap := make(map[string][]byte)
	for i := range req.Transfer.LeavesToSend {
		cpfpTransaction := req.Transfer.LeavesToSend[i]
		cpfpLeafRefundMap[cpfpTransaction.LeafId] = cpfpTransaction.RawTx
	}
	for i := range req.Transfer.DirectLeavesToSend {
		directTransaction := req.Transfer.DirectLeavesToSend[i]
		directLeafRefundMap[directTransaction.LeafId] = directTransaction.RawTx
	}
	for i := range req.Transfer.DirectFromCpfpLeavesToSend {
		directFromCpfpTransaction := req.Transfer.DirectFromCpfpLeavesToSend[i]
		directFromCpfpLeafRefundMap[directFromCpfpTransaction.LeafId] = directFromCpfpTransaction.RawTx
	}

	transferHandler := NewTransferHandler(h.config)
	ownerIDPubKey, err := keys.ParsePublicKey(req.Transfer.OwnerIdentityPublicKey)
	if err != nil {
		return nil, fmt.Errorf("unable to parse owner identity public key: %w", err)
	}
	transfer, _, err := transferHandler.createTransfer(
		ctx,
		req.Transfer.TransferId,
		st.TransferTypePreimageSwap,
		req.Transfer.ExpiryTime.AsTime(),
		ownerIDPubKey,
		receiverIDPubKey,
		cpfpLeafRefundMap,
		directLeafRefundMap,
		directFromCpfpLeafRefundMap,
		nil,
		TransferRoleCoordinator,
		false,
	)
	if err != nil {
		return nil, fmt.Errorf("unable to create transfer: %w", err)
	}

	var status st.PreimageRequestStatus
	if req.Reason == pb.InitiatePreimageSwapRequest_REASON_RECEIVE {
		status = st.PreimageRequestStatusPreimageShared
	} else {
		status = st.PreimageRequestStatusWaitingForPreimage
	}
	_, err = h.storeUserSignedTransactions(
		ctx,
		req.PaymentHash,
		preimageShare,
		req.Transfer.LeavesToSend,
		req.Transfer.DirectLeavesToSend,
		req.Transfer.DirectFromCpfpLeavesToSend,
		transfer,
		status,
		receiverIDPubKey,
	)
	if err != nil {
		return nil, fmt.Errorf("unable to store user signed transactions: %w", err)
	}

	if preimageShare != nil {
		return preimageShare.PreimageShare, nil
	}

	return nil, nil
}

// InitiatePreimageSwapV2 initiates a preimage swap for the given payment hash.
func (h *LightningHandler) InitiatePreimageSwapV2(ctx context.Context, req *pb.InitiatePreimageSwapRequest) (*pb.InitiatePreimageSwapResponse, error) {
	return h.initiatePreimageSwap(ctx, req, true)
}

func (h *LightningHandler) InitiatePreimageSwap(ctx context.Context, req *pb.InitiatePreimageSwapRequest) (*pb.InitiatePreimageSwapResponse, error) {
	return h.initiatePreimageSwap(ctx, req, false)
}

// InitiatePreimageSwap initiates a preimage swap for the given payment hash.
func (h *LightningHandler) initiatePreimageSwap(ctx context.Context, req *pb.InitiatePreimageSwapRequest, requireDirectTx bool) (*pb.InitiatePreimageSwapResponse, error) {
	if req.Transfer == nil {
		return nil, fmt.Errorf("transfer is required")
	}

	if len(req.Transfer.LeavesToSend) == 0 {
		return nil, fmt.Errorf("at least one cpfp leaf tx must be provided")
	}

	if req.Transfer.OwnerIdentityPublicKey == nil {
		return nil, fmt.Errorf("owner identity public key is required")
	}

	if req.Transfer.ReceiverIdentityPublicKey == nil {
		return nil, fmt.Errorf("receiver identity public key is required")
	}

	if req.Reason == pb.InitiatePreimageSwapRequest_REASON_RECEIVE && req.FeeSats != 0 {
		return nil, fmt.Errorf("fee is not allowed for receive preimage swap")
	}

	logger := logging.GetLoggerFromContext(ctx)

	var preimageShare *ent.PreimageShare
	if req.Reason == pb.InitiatePreimageSwapRequest_REASON_RECEIVE {
		tx, err := ent.GetDbFromContext(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
		}
		preimageShare, err = tx.PreimageShare.Query().Where(preimageshare.PaymentHash(req.PaymentHash)).First(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to get preimage share for payment hash: %x: %w", req.PaymentHash, err)
		}
		if !bytes.Equal(preimageShare.OwnerIdentityPubkey, req.ReceiverIdentityPublicKey) {
			return nil, fmt.Errorf("preimage share owner identity public key mismatch for payment hash: %x", req.PaymentHash)
		}
	}

	invoiceAmount := req.InvoiceAmount
	if preimageShare != nil {
		bolt11, err := decodepay.Decodepay(preimageShare.InvoiceString)
		if err != nil {
			return nil, fmt.Errorf("unable to decode invoice: %w", err)
		}
		if bolt11.MSatoshi > 0 {
			invoiceAmount = &pb.InvoiceAmount{
				ValueSats: uint64(bolt11.MSatoshi / 1000),
				InvoiceAmountProof: &pb.InvoiceAmountProof{
					Bolt11Invoice: preimageShare.InvoiceString,
				},
			}
		}
	}

	err := h.ValidateDuplicateLeaves(ctx, req.Transfer.LeavesToSend, req.Transfer.DirectLeavesToSend, req.Transfer.DirectFromCpfpLeavesToSend)
	if err != nil {
		return nil, fmt.Errorf("unable to validate duplicate leaves: %w", err)
	}

	receiverIDPubKey, err := keys.ParsePublicKey(req.Transfer.ReceiverIdentityPublicKey)
	if err != nil {
		return nil, fmt.Errorf("unable to parse receiver identity public key: %w", err)
	}
	err = h.ValidateGetPreimageRequest(
		ctx,
		req.PaymentHash,
		req.Transfer.LeavesToSend,
		req.Transfer.DirectLeavesToSend,
		req.Transfer.DirectFromCpfpLeavesToSend,
		invoiceAmount,
		receiverIDPubKey,
		req.FeeSats,
		req.Reason,
		true,
	)
	if err != nil {
		return nil, fmt.Errorf("unable to validate request for payment hash: %x: %w", req.PaymentHash, err)
	}

	cpfpLeafRefundMap := make(map[string][]byte)
	directLeafRefundMap := make(map[string][]byte)
	directFromCpfpLeafRefundMap := make(map[string][]byte)
	for i := range req.Transfer.LeavesToSend {
		cpfpTransaction := req.Transfer.LeavesToSend[i]
		cpfpLeafRefundMap[cpfpTransaction.LeafId] = cpfpTransaction.RawTx
	}
	for i := range req.Transfer.DirectLeavesToSend {
		directTransaction := req.Transfer.DirectLeavesToSend[i]
		directLeafRefundMap[directTransaction.LeafId] = directTransaction.RawTx
	}
	for i := range req.Transfer.DirectFromCpfpLeavesToSend {
		directFromCpfpTransaction := req.Transfer.DirectFromCpfpLeavesToSend[i]
		directFromCpfpLeafRefundMap[directFromCpfpTransaction.LeafId] = directFromCpfpTransaction.RawTx
	}

	expiryTime := req.Transfer.ExpiryTime.AsTime()
	if expiryTime.Unix() != 0 && expiryTime.After(time.Now().Add(MaximumExpiryTime)) {
		return nil, fmt.Errorf("expiry time is greater than maximum expiry time")
	}

	transferHandler := NewTransferHandler(h.config)
	ownerIDPubKey, err := keys.ParsePublicKey(req.Transfer.OwnerIdentityPublicKey)
	if err != nil {
		return nil, fmt.Errorf("unable to parse owner identity public key: %w", err)
	}
	transfer, _, err := transferHandler.createTransfer(
		ctx,
		req.Transfer.TransferId,
		st.TransferTypePreimageSwap,
		req.Transfer.ExpiryTime.AsTime(),
		ownerIDPubKey,
		receiverIDPubKey,
		cpfpLeafRefundMap,
		directLeafRefundMap,
		directFromCpfpLeafRefundMap,
		nil,
		TransferRoleCoordinator,
		requireDirectTx,
	)
	if err != nil {
		return nil, fmt.Errorf("unable to create transfer for payment hash: %x: %w", req.PaymentHash, err)
	}

	var status st.PreimageRequestStatus
	if req.Reason == pb.InitiatePreimageSwapRequest_REASON_RECEIVE {
		status = st.PreimageRequestStatusPreimageShared
	} else {
		status = st.PreimageRequestStatusWaitingForPreimage
	}
	preimageRequest, err := h.storeUserSignedTransactions(
		ctx,
		req.PaymentHash,
		preimageShare,
		req.Transfer.LeavesToSend,
		req.Transfer.DirectLeavesToSend,
		req.Transfer.DirectFromCpfpLeavesToSend,
		transfer,
		status,
		receiverIDPubKey,
	)
	if err != nil {
		return nil, fmt.Errorf("unable to store user signed transactions for payment hash: %x and transfer id: %s: %w", req.PaymentHash, transfer.ID.String(), err)
	}

	selection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}
	result, err := helper.ExecuteTaskWithAllOperators(ctx, h.config, &selection, func(ctx context.Context, operator *so.SigningOperator) ([]byte, error) {
		conn, err := operator.NewOperatorGRPCConnection()
		if err != nil {
			return nil, err
		}
		defer conn.Close()

		client := pbinternal.NewSparkInternalServiceClient(conn)
		response, err := client.InitiatePreimageSwap(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("unable to initiate preimage swap for payment hash: %x and transfer id: %s: %w", req.PaymentHash, transfer.ID.String(), err)
		}
		return response.PreimageShare, nil
	})
	if err != nil {
		// At least one operator failed to initiate preimage swap, cancel the transfer.
		baseHandler := NewBaseTransferHandler(h.config)
		cancelErr := baseHandler.CreateCancelTransferGossipMessage(ctx, transfer.ID.String())
		if cancelErr != nil {
			logger.Error("InitiatePreimageSwap: unable to cancel own send transfer", "error", cancelErr)
		}
		return nil, fmt.Errorf("unable to execute task with all operators: %w", err)
	}

	transferProto, err := transfer.MarshalProto(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to marshal transfer for payment hash: %x and transfer id: %s: %w", req.PaymentHash, transfer.ID.String(), err)
	}

	// Recover secret if necessary
	if req.Reason == pb.InitiatePreimageSwapRequest_REASON_SEND {
		return &pb.InitiatePreimageSwapResponse{Transfer: transferProto}, nil
	}

	var shares []*secretsharing.SecretShare
	for identifier, share := range result {
		if share == nil {
			continue
		}
		index, ok := new(big.Int).SetString(identifier, 16)
		if !ok {
			return nil, fmt.Errorf("unable to parse index: %v", identifier)
		}
		shares = append(shares, &secretsharing.SecretShare{
			FieldModulus: secp256k1.S256().N,
			Threshold:    int(h.config.Threshold),
			Index:        index,
			Share:        new(big.Int).SetBytes(share),
		})
	}

	secret, err := secretsharing.RecoverSecret(shares)
	if err != nil {
		return nil, fmt.Errorf("unable to recover secret for payment hash: %x and transfer id: %s: %w", req.PaymentHash, transfer.ID.String(), err)
	}

	secretBytes := secret.Bytes()
	if len(secretBytes) < 32 {
		secretBytes = append(make([]byte, 32-len(secretBytes)), secretBytes...)
	}

	hash := sha256.Sum256(secretBytes)
	if !bytes.Equal(hash[:], req.PaymentHash) {
		baseHandler := NewBaseTransferHandler(h.config)
		err := baseHandler.CreateCancelTransferGossipMessage(ctx, transfer.ID.String())
		if err != nil {
			logger.Error("InitiatePreimageSwap: unable to cancel own send transfer",
				"error", err,
				"payment_hash", hex.EncodeToString(req.PaymentHash),
				"transfer_id", transfer.ID.String())
		}

		commitErr := ent.DbCommit(ctx)
		if commitErr != nil {
			logger.Error("Unable to commit transaction after canceling transfer", "error", commitErr)
		}

		return nil, fmt.Errorf("recovered preimage did not match payment hash: %x and transfer id: %s", req.PaymentHash, transfer.ID.String())
	}

	err = preimageRequest.Update().SetStatus(st.PreimageRequestStatusPreimageShared).Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to update preimage request status for payment hash: %x and transfer id: %s: %w", req.PaymentHash, transfer.ID.String(), err)
	}

	return &pb.InitiatePreimageSwapResponse{Preimage: secretBytes, Transfer: transferProto}, nil
}

// UpdatePreimageRequest updates the preimage request.
func (h *LightningHandler) UpdatePreimageRequest(ctx context.Context, req *pbinternal.UpdatePreimageRequestRequest) error {
	logger := logging.GetLoggerFromContext(ctx)
	tx, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	paymentHash := sha256.Sum256(req.Preimage)
	preimageRequest, err := tx.PreimageRequest.Query().Where(
		preimagerequest.And(
			preimagerequest.PaymentHashEQ(paymentHash[:]),
			preimagerequest.ReceiverIdentityPubkeyEQ(req.IdentityPublicKey),
			preimagerequest.StatusEQ(st.PreimageRequestStatusWaitingForPreimage),
		),
	).First(ctx)
	if err != nil {
		logger.Error("UpdatePreimageRequest: unable to get preimage request", "error", err, "paymentHash", hex.EncodeToString(paymentHash[:]), "identityPublicKey", hex.EncodeToString(req.IdentityPublicKey))
		return fmt.Errorf("UpdatePreimageRequest:unable to get preimage request: %w", err)
	}

	err = preimageRequest.Update().SetStatus(st.PreimageRequestStatusPreimageShared).Exec(ctx)
	if err != nil {
		return fmt.Errorf("unable to update preimage request status: %w", err)
	}
	return nil
}

// QueryUserSignedRefunds queries the user signed refunds for the given payment hash.
func (h *LightningHandler) QueryUserSignedRefunds(ctx context.Context, req *pb.QueryUserSignedRefundsRequest) (*pb.QueryUserSignedRefundsResponse, error) {
	logger := logging.GetLoggerFromContext(ctx)
	tx, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	preimageRequest, err := tx.PreimageRequest.Query().Where(
		preimagerequest.And(
			preimagerequest.PaymentHashEQ(req.PaymentHash),
			preimagerequest.ReceiverIdentityPubkeyEQ(req.IdentityPublicKey),
			preimagerequest.StatusEQ(st.PreimageRequestStatusWaitingForPreimage),
		),
	).First(ctx)
	if err != nil {
		logger.Error("QueryUserSignedRefunds: unable to get preimage request", "error", err, "paymentHash", hex.EncodeToString(req.PaymentHash), "identityPublicKey", hex.EncodeToString(req.IdentityPublicKey))
		return nil, fmt.Errorf("QueryUserSignedRefunds: unable to get preimage request: %w", err)
	}

	transfer, err := preimageRequest.QueryTransfers().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get transfer: %w", err)
	}

	if transfer.Status != st.TransferStatusSenderKeyTweakPending && transfer.Status != st.TransferStatusSenderInitiatedCoordinator {
		return nil, fmt.Errorf("expected either status sender key tweak pending or sender initiated coordinator, got status: %s", transfer.Status)
	}

	if transfer.ExpiryTime.Before(time.Now()) {
		return nil, fmt.Errorf("expiry time is in the past")
	}

	userSignedRefunds, err := preimageRequest.QueryTransactions().All(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get user signed transactions: %w", err)
	}

	protos := make([]*pb.UserSignedRefund, len(userSignedRefunds))
	for i, userSignedRefund := range userSignedRefunds {
		userSigningCommitment := &pbcommon.SigningCommitment{}
		err := proto.Unmarshal(userSignedRefund.SigningCommitments, userSigningCommitment)
		if err != nil {
			return nil, fmt.Errorf("unable to unmarshal user signed refund: %w", err)
		}
		signingCommitments := &pb.SigningCommitments{}
		err = proto.Unmarshal(userSignedRefund.SigningCommitments, signingCommitments)
		if err != nil {
			return nil, fmt.Errorf("unable to unmarshal user signed refund: %w", err)
		}
		treeNode, err := userSignedRefund.QueryTreeNode().WithTree().Only(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to get tree node: %w", err)
		}
		networkProto, err := treeNode.Edges.Tree.Network.MarshalProto()
		if err != nil {
			return nil, fmt.Errorf("unable to marshal network: %w", err)
		}

		protos[i] = &pb.UserSignedRefund{
			NodeId:                  treeNode.ID.String(),
			RefundTx:                userSignedRefund.Transaction,
			UserSignature:           userSignedRefund.UserSignature,
			SigningCommitments:      signingCommitments,
			UserSignatureCommitment: userSigningCommitment,
			Network:                 networkProto,
		}
	}

	transferProto, err := transfer.MarshalProto(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to marshal transfer: %w", err)
	}
	return &pb.QueryUserSignedRefundsResponse{
		UserSignedRefunds: protos,
		Transfer:          transferProto,
	}, nil
}

func (h *LightningHandler) ValidatePreimage(ctx context.Context, req *pb.ProvidePreimageRequest) (*ent.Transfer, error) {
	logger := logging.GetLoggerFromContext(ctx)

	// Validate input parameters
	if len(req.PaymentHash) == 0 {
		return nil, fmt.Errorf("payment hash cannot be empty")
	}
	if len(req.PaymentHash) != 32 {
		return nil, fmt.Errorf("invalid payment hash length: %d bytes, expected 32 bytes", len(req.PaymentHash))
	}
	if len(req.Preimage) == 0 {
		return nil, fmt.Errorf("preimage cannot be empty")
	}
	if len(req.Preimage) != 32 {
		return nil, fmt.Errorf("invalid preimage length: %d bytes, expected 32 bytes", len(req.Preimage))
	}
	if len(req.IdentityPublicKey) == 0 {
		return nil, fmt.Errorf("identity public key cannot be empty")
	}
	if len(req.IdentityPublicKey) != 33 {
		return nil, fmt.Errorf("invalid identity public key length: %d bytes, expected 33 bytes", len(req.IdentityPublicKey))
	}

	tx, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	// Validate preimage produces the correct payment hash
	calculatedPaymentHash := sha256.Sum256(req.Preimage)
	if !bytes.Equal(calculatedPaymentHash[:], req.PaymentHash) {
		return nil, fmt.Errorf("invalid preimage")
	}

	preimageRequest, err := tx.PreimageRequest.Query().Where(
		preimagerequest.And(
			preimagerequest.PaymentHashEQ(req.PaymentHash),
			preimagerequest.ReceiverIdentityPubkeyEQ(req.IdentityPublicKey),
			preimagerequest.StatusIn(st.PreimageRequestStatusWaitingForPreimage, st.PreimageRequestStatusPreimageShared),
		),
	).First(ctx)
	if err != nil {
		logger.Error("ProvidePreimage: unable to get preimage request", "error", err, "paymentHash", hex.EncodeToString(req.PaymentHash), "identityPublicKey", hex.EncodeToString(req.IdentityPublicKey))
		return nil, fmt.Errorf("ProvidePreimage: unable to get preimage request: %w", err)
	}

	if preimageRequest.Status == st.PreimageRequestStatusWaitingForPreimage {
		preimageRequest, err = preimageRequest.Update().
			SetStatus(st.PreimageRequestStatusPreimageShared).
			SetPreimage(req.Preimage).
			Save(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to update preimage request status: %w", err)
		}
	}

	transfer, err := preimageRequest.QueryTransfers().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get transfer: %w", err)
	}
	return transfer, nil
}

func (h *LightningHandler) ValidatePreimageInternal(ctx context.Context, req *pbinternal.ProvidePreimageRequest) (*ent.Transfer, error) {
	providePreimageRequest := &pb.ProvidePreimageRequest{
		PaymentHash:       req.PaymentHash,
		Preimage:          req.Preimage,
		IdentityPublicKey: req.IdentityPublicKey,
	}
	transfer, err := h.ValidatePreimage(ctx, providePreimageRequest)
	if err != nil {
		return nil, fmt.Errorf("unable to validate preimage: %w", err)
	}

	transferHandler := NewBaseTransferHandler(h.config)
	err = transferHandler.validateKeyTweakProofs(ctx, transfer, req.KeyTweakProofs)
	if err != nil {
		return nil, fmt.Errorf("unable to get transfer leaves: %w", err)
	}
	return transfer, nil
}

func (h *LightningHandler) ProvidePreimage(ctx context.Context, req *pb.ProvidePreimageRequest) (*pb.ProvidePreimageResponse, error) {
	transfer, err := h.ValidatePreimage(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("unable to provide preimage: %w", err)
	}
	if transfer.Status != st.TransferStatusSenderKeyTweakPending && transfer.Status != st.TransferStatusSenderInitiatedCoordinator {
		transferProto, err := transfer.MarshalProto(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to marshal transfer: %w", err)
		}

		return &pb.ProvidePreimageResponse{Transfer: transferProto}, nil
	}

	transferLeaves, err := transfer.QueryTransferLeaves().All(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get transfer leaves: %w", err)
	}
	internalReq := &pbinternal.ProvidePreimageRequest{
		PaymentHash:       req.PaymentHash,
		Preimage:          req.Preimage,
		IdentityPublicKey: req.IdentityPublicKey,
	}
	keyTweakProofMap := make(map[string]*pb.SecretProof)
	for _, leaf := range transferLeaves {
		keyTweakProto := &pb.SendLeafKeyTweak{}
		err := proto.Unmarshal(leaf.KeyTweak, keyTweakProto)
		if err != nil {
			return nil, fmt.Errorf("unable to unmarshal key tweak: %w", err)
		}
		keyTweakProofMap[keyTweakProto.LeafId] = &pb.SecretProof{
			Proofs: keyTweakProto.SecretShareTweak.Proofs,
		}
	}
	internalReq.KeyTweakProofs = keyTweakProofMap

	operatorSelection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}
	_, err = helper.ExecuteTaskWithAllOperators(ctx, h.config, &operatorSelection, func(ctx context.Context, operator *so.SigningOperator) (any, error) {
		conn, err := operator.NewOperatorGRPCConnection()
		if err != nil {
			return nil, err
		}
		defer conn.Close()

		client := pbinternal.NewSparkInternalServiceClient(conn)
		_, err = client.ProvidePreimage(ctx, internalReq)
		if err != nil {
			return nil, fmt.Errorf("unable to provide preimage: %w", err)
		}
		return nil, nil
	})
	if err != nil {
		return nil, fmt.Errorf("unable to execute task with all operators: %w", err)
	}

	participants, err := operatorSelection.OperatorIdentifierList(h.config)
	if err != nil {
		return nil, fmt.Errorf("unable to get operator list: %w", err)
	}
	sendGossipHandler := NewSendGossipHandler(h.config)
	_, err = sendGossipHandler.CreateAndSendGossipMessage(ctx, &pbgossip.GossipMessage{
		Message: &pbgossip.GossipMessage_SettleSenderKeyTweak{
			SettleSenderKeyTweak: &pbgossip.GossipMessageSettleSenderKeyTweak{
				TransferId:           transfer.ID.String(),
				SenderKeyTweakProofs: keyTweakProofMap,
			},
		},
	}, participants)
	if err != nil {
		return nil, fmt.Errorf("unable to create and send gossip message to settle sender key tweak: %w", err)
	}

	tx, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}
	transfer, err = tx.Transfer.Get(ctx, transfer.ID)
	if err != nil {
		return nil, fmt.Errorf("unable to get transfer: %w", err)
	}

	transferProto, err := transfer.MarshalProto(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to marshal transfer: %w", err)
	}

	return &pb.ProvidePreimageResponse{Transfer: transferProto}, nil
}

func (h *LightningHandler) ReturnLightningPayment(ctx context.Context, req *pb.ReturnLightningPaymentRequest, internal bool) (*emptypb.Empty, error) {
	logger := logging.GetLoggerFromContext(ctx)
	if !internal {
		reqUserIDPubKey, err := keys.ParsePublicKey(req.UserIdentityPublicKey)
		if err != nil {
			return nil, fmt.Errorf("invalid identity public key: %w", err)
		}
		if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, reqUserIDPubKey); err != nil {
			return nil, err
		}
	}

	preimageRequestStatuses := []st.PreimageRequestStatus{
		st.PreimageRequestStatusWaitingForPreimage,
		st.PreimageRequestStatusReturned,
	}

	tx, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}
	preimageRequest, err := tx.PreimageRequest.Query().Where(
		preimagerequest.And(
			preimagerequest.PaymentHashEQ(req.PaymentHash),
			preimagerequest.ReceiverIdentityPubkeyEQ(req.UserIdentityPublicKey),
			preimagerequest.StatusIn(preimageRequestStatuses...),
		),
	).First(ctx)
	if err != nil {
		logger.Error("ReturnLightningPayment: unable to get preimage request", "error", err, "paymentHash", hex.EncodeToString(req.PaymentHash), "identityPublicKey", hex.EncodeToString(req.UserIdentityPublicKey))
		return nil, fmt.Errorf("ReturnLightningPayment: unable to get preimage request: %w", err)
	}

	if preimageRequest.Status == st.PreimageRequestStatusReturned {
		logger.Info("preimage request is already in the returned status")
		return &emptypb.Empty{}, nil
	}

	if preimageRequest.Status != st.PreimageRequestStatusWaitingForPreimage {
		return nil, fmt.Errorf("preimage request is not in the waiting for preimage status")
	}

	err = preimageRequest.Update().SetStatus(st.PreimageRequestStatusReturned).Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to update preimage request status: %w", err)
	}

	transfer, err := preimageRequest.QueryTransfers().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get transfer: %w", err)
	}

	if !bytes.Equal(transfer.ReceiverIdentityPubkey, req.UserIdentityPublicKey) {
		return nil, fmt.Errorf("transfer receiver identity public key mismatch")
	}

	transfer, err = transfer.Update().SetStatus(st.TransferStatusReturned).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to update transfer status: %w", err)
	}

	transferLeaves, err := transfer.QueryTransferLeaves().All(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get transfer leaves: %w", err)
	}

	for _, leaf := range transferLeaves {
		treeNode, err := leaf.QueryLeaf().Only(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to get tree node: %w", err)
		}
		_, err = treeNode.Update().SetStatus(st.TreeNodeStatusAvailable).Save(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to update tree node status: %w", err)
		}
	}

	if !internal {
		operatorSelection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}
		_, err = helper.ExecuteTaskWithAllOperators(ctx, h.config, &operatorSelection, func(ctx context.Context, operator *so.SigningOperator) (any, error) {
			conn, err := operator.NewOperatorGRPCConnection()
			if err != nil {
				return nil, err
			}
			defer conn.Close()

			client := pbinternal.NewSparkInternalServiceClient(conn)
			_, err = client.ReturnLightningPayment(ctx, req)
			if err != nil {
				return nil, fmt.Errorf("unable to return lightning payment: %w", err)
			}
			return nil, nil
		})
		if err != nil {
			return nil, fmt.Errorf("unable to execute task with all operators: %w", err)
		}
	}

	return &emptypb.Empty{}, nil
}
