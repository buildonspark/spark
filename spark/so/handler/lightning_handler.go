package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/logging"
	secretsharing "github.com/lightsparkdev/spark/common/secret_sharing"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pbfrost "github.com/lightsparkdev/spark/proto/frost"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authn"
	"github.com/lightsparkdev/spark/so/authz"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/preimagerequest"
	"github.com/lightsparkdev/spark/so/ent/preimageshare"
	"github.com/lightsparkdev/spark/so/ent/schema"
	"github.com/lightsparkdev/spark/so/ent/treenode"
	"github.com/lightsparkdev/spark/so/helper"
	"github.com/lightsparkdev/spark/so/objects"
	decodepay "github.com/nbd-wtf/ln-decodepay"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
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
		return fmt.Errorf("unable to validate share: %v", err)
	}

	bolt11, err := decodepay.Decodepay(req.InvoiceString)
	if err != nil {
		return fmt.Errorf("unable to decode invoice: %v", err)
	}

	paymentHash, err := hex.DecodeString(bolt11.PaymentHash)
	if err != nil {
		return fmt.Errorf("unable to decode payment hash: %v", err)
	}

	if !bytes.Equal(paymentHash, req.PaymentHash) {
		return fmt.Errorf("payment hash mismatch")
	}

	db := ent.GetDbFromContext(ctx)
	_, err = db.PreimageShare.Create().
		SetPaymentHash(req.PaymentHash).
		SetPreimageShare(req.PreimageShare.SecretShare).
		SetThreshold(int32(req.Threshold)).
		SetInvoiceString(req.InvoiceString).
		SetOwnerIdentityPubkey(req.UserIdentityPublicKey).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("unable to store preimage share: %v", err)
	}
	return nil
}

func (h *LightningHandler) validateNodeOwnership(ctx context.Context, nodes []*ent.TreeNode) error {
	if !h.config.AuthzEnforced() {
		return nil
	}

	session, err := authn.GetSessionFromContext(ctx)
	if err != nil {
		return err
	}
	sessionIdentityPubkeyBytes := session.IdentityPublicKeyBytes()

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
	if h.config.AuthzEnforced() {
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

	db := ent.GetDbFromContext(ctx)
	nodeIDs := make([]uuid.UUID, len(req.NodeIds))
	for i, nodeID := range req.NodeIds {
		nodeID, err := uuid.Parse(nodeID)
		if err != nil {
			return nil, fmt.Errorf("unable to parse node id: %v", err)
		}
		nodeIDs[i] = nodeID
	}

	nodes, err := db.TreeNode.Query().Where(treenode.IDIn(nodeIDs...)).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get nodes: %v", err)
	}

	if err := h.validateNodeOwnership(ctx, nodes); err != nil {
		return nil, err
	}

	keyshareIDs := make([]uuid.UUID, len(nodes))
	for i, node := range nodes {
		keyshareIDs[i], err = node.QuerySigningKeyshare().OnlyID(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to get keyshare id: %v", err)
		}
	}

	commitments, err := helper.GetSigningCommitments(ctx, h.config, keyshareIDs)
	if err != nil {
		return nil, fmt.Errorf("unable to get signing commitments: %v", err)
	}

	commitmentsArray := common.MapOfArrayToArrayOfMap[string, objects.SigningCommitment](commitments)

	requestedCommitments := make([]*pb.RequestedSigningCommitments, len(commitmentsArray))

	for i, commitment := range commitmentsArray {
		commitmentMapProto, err := common.ConvertObjectMapToProtoMap(commitment)
		if err != nil {
			return nil, fmt.Errorf("unable to convert signing commitment to proto: %v", err)
		}
		requestedCommitments[i] = &pb.RequestedSigningCommitments{
			SigningNonceCommitments: commitmentMapProto,
		}
	}

	return &pb.GetSigningCommitmentsResponse{SigningCommitments: requestedCommitments}, nil
}

func (h *LightningHandler) validateGetPreimageRequest(
	ctx context.Context,
	paymentHash []byte,
	transactions []*pb.UserSignedTxSigningJob,
	amount *pb.InvoiceAmount,
	destinationPubkey []byte,
	feeSats uint64,
	reason pb.InitiatePreimageSwapRequest_Reason,
) error {
	logger := logging.GetLoggerFromContext(ctx)

	// Step 0 Validate that there's no existing preimage request for this payment hash
	db := ent.GetDbFromContext(ctx)
	preimageRequests, err := db.PreimageRequest.Query().Where(
		preimagerequest.PaymentHashEQ(paymentHash),
		preimagerequest.ReceiverIdentityPubkeyEQ(destinationPubkey),
		preimagerequest.StatusNEQ(schema.PreimageRequestStatusReturned),
	).All(ctx)
	if err != nil {
		return fmt.Errorf("unable to get preimage request: %v", err)
	}
	if len(preimageRequests) > 0 {
		return fmt.Errorf("preimage request already exists")
	}

	// Step 1 validate all signatures are valid
	conn, err := common.NewGRPCConnectionWithoutTLS(h.config.SignerAddress, nil)
	if err != nil {
		return fmt.Errorf("unable to connect to signer: %v", err)
	}
	defer conn.Close()

	client := pbfrost.NewFrostServiceClient(conn)
	for _, transaction := range transactions {
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
			return fmt.Errorf("unable to parse node id: %v", err)
		}
		node, err := db.TreeNode.Get(ctx, nodeID)
		if err != nil {
			return fmt.Errorf("unable to get node: %v", err)
		}
		if node.Status != schema.TreeNodeStatusAvailable {
			return fmt.Errorf("node %v is not available: %v", node.ID, node.Status)
		}
		keyshare, err := node.QuerySigningKeyshare().First(ctx)
		if err != nil {
			return fmt.Errorf("unable to get keyshare: %v", err)
		}
		tx, err := common.TxFromRawTxBytes(node.RawTx)
		if err != nil {
			return fmt.Errorf("unable to get tx: %v", err)
		}

		refundTx, err := common.TxFromRawTxBytes(transaction.RawTx)
		if err != nil {
			return fmt.Errorf("unable to get refund tx: %v", err)
		}

		if len(tx.TxOut) <= 0 {
			return fmt.Errorf("vout out of bounds")
		}
		sighash, err := common.SigHashFromTx(refundTx, 0, tx.TxOut[0])
		if err != nil {
			return fmt.Errorf("unable to get sighash: %v", err)
		}

		realUserPublicKey, err := common.SubtractPublicKeys(node.VerifyingPubkey, keyshare.PublicKey)
		if err != nil {
			return fmt.Errorf("unable to get real user public key: %v", err)
		}

		if !bytes.Equal(realUserPublicKey, node.OwnerSigningPubkey) {
			logger.Debug("real user public key mismatch", "expected", hex.EncodeToString(node.OwnerSigningPubkey), "got", hex.EncodeToString(realUserPublicKey))
			node, err = node.Update().SetOwnerSigningPubkey(realUserPublicKey).Save(ctx)
			if err != nil {
				return fmt.Errorf("unable to update node: %v", err)
			}
		}

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
			return fmt.Errorf("unable to validate signature share: %v, for sighash: %v, user pubkey: %v", err, hex.EncodeToString(sighash), hex.EncodeToString(node.OwnerSigningPubkey))
		}
	}

	// Step 2 validate the amount is correct and paid to the destination pubkey
	destinationPubkeyBytes, err := secp256k1.ParsePubKey(destinationPubkey)
	if err != nil {
		return fmt.Errorf("unable to parse destination pubkey: %v", err)
	}
	var totalAmount uint64
	for _, transaction := range transactions {
		refundTx, err := common.TxFromRawTxBytes(transaction.RawTx)
		if err != nil {
			return fmt.Errorf("unable to get refund tx: %v", err)
		}
		pubkeyScript, err := common.P2TRScriptFromPubKey(destinationPubkeyBytes)
		if err != nil {
			return fmt.Errorf("unable to extract pubkey from tx: %v", err)
		}
		if len(refundTx.TxOut) <= 0 {
			return fmt.Errorf("vout out of bounds")
		}
		if !bytes.Equal(pubkeyScript, refundTx.TxOut[0].PkScript) {
			return fmt.Errorf("invalid destination pubkey")
		}
		totalAmount += uint64(refundTx.TxOut[0].Value)
	}
	if reason == pb.InitiatePreimageSwapRequest_REASON_SEND {
		totalAmount -= feeSats
	}
	if totalAmount != amount.ValueSats {
		return fmt.Errorf("invalid amount, expected %d, got %d", amount.ValueSats, totalAmount)
	}
	return nil
}

func (h *LightningHandler) storeUserSignedTransactions(
	ctx context.Context,
	paymentHash []byte,
	preimageShare *ent.PreimageShare,
	transactions []*pb.UserSignedTxSigningJob,
	transfer *ent.Transfer,
	status schema.PreimageRequestStatus,
	receiverIdentityPubkey []byte,
) (*ent.PreimageRequest, error) {
	db := ent.GetDbFromContext(ctx)
	preimageRequestMutator := db.PreimageRequest.Create().
		SetPaymentHash(paymentHash).
		SetReceiverIdentityPubkey(receiverIdentityPubkey).
		SetTransfers(transfer).
		SetStatus(status)
	if preimageShare != nil {
		preimageRequestMutator.SetPreimageShares(preimageShare)
	}
	preimageRequest, err := preimageRequestMutator.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to create preimage request: %v", err)
	}

	for _, transaction := range transactions {
		commitmentsBytes, err := proto.Marshal(transaction.SigningCommitments)
		if err != nil {
			return nil, fmt.Errorf("unable to marshal signing commitments: %v", err)
		}
		nodeID, err := uuid.Parse(transaction.LeafId)
		if err != nil {
			return nil, fmt.Errorf("unable to parse node id: %v", err)
		}
		userSignatureCommitmentBytes, err := proto.Marshal(transaction.SigningNonceCommitment)
		if err != nil {
			return nil, fmt.Errorf("unable to marshal user signature commitment: %v", err)
		}
		_, err = db.UserSignedTransaction.Create().
			SetTransaction(transaction.RawTx).
			SetUserSignature(transaction.UserSignature).
			SetUserSignatureCommitment(userSignatureCommitmentBytes).
			SetSigningCommitments(commitmentsBytes).
			SetPreimageRequest(preimageRequest).
			SetTreeNodeID(nodeID).
			Save(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to store user signed transaction: %v", err)
		}

		node, err := db.TreeNode.Get(ctx, nodeID)
		if err != nil {
			return nil, fmt.Errorf("unable to get node: %v", err)
		}
		_, err = db.TreeNode.UpdateOne(node).SetStatus(schema.TreeNodeStatusTransferLocked).Save(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to update node status: %v", err)
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
		db := ent.GetDbFromContext(ctx)
		var err error
		preimageShare, err = db.PreimageShare.Query().Where(preimageshare.PaymentHash(req.PaymentHash)).First(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to get preimage share: %v", err)
		}
		if !bytes.Equal(preimageShare.OwnerIdentityPubkey, req.ReceiverIdentityPublicKey) {
			return nil, fmt.Errorf("preimage share owner identity public key mismatch")
		}
	}

	invoiceAmount := req.InvoiceAmount
	if preimageShare != nil {
		bolt11, err := decodepay.Decodepay(preimageShare.InvoiceString)
		if err != nil {
			return nil, fmt.Errorf("unable to decode invoice: %v", err)
		}
		invoiceAmount = &pb.InvoiceAmount{
			ValueSats: uint64(bolt11.MSatoshi / 1000),
			InvoiceAmountProof: &pb.InvoiceAmountProof{
				Bolt11Invoice: preimageShare.InvoiceString,
			},
		}
	}

	err := h.validateGetPreimageRequest(
		ctx,
		req.PaymentHash,
		req.Transfer.LeavesToSend,
		invoiceAmount,
		req.ReceiverIdentityPublicKey,
		req.FeeSats,
		req.Reason,
	)
	if err != nil {
		return nil, fmt.Errorf("unable to validate request: %v", err)
	}

	leafRefundMap := make(map[string][]byte)
	for _, transaction := range req.Transfer.LeavesToSend {
		leafRefundMap[transaction.LeafId] = transaction.RawTx
	}

	transferHandler := NewTransferHandler(h.config)
	transfer, _, err := transferHandler.createTransfer(
		ctx,
		req.Transfer.TransferId,
		schema.TransferTypePreimageSwap,
		req.Transfer.ExpiryTime.AsTime(),
		req.Transfer.OwnerIdentityPublicKey,
		req.Transfer.ReceiverIdentityPublicKey,
		leafRefundMap,
		nil,
		TransferRoleCoordinator,
	)
	if err != nil {
		return nil, fmt.Errorf("unable to create transfer: %v", err)
	}

	var status schema.PreimageRequestStatus
	if req.Reason == pb.InitiatePreimageSwapRequest_REASON_RECEIVE {
		status = schema.PreimageRequestStatusPreimageShared
	} else {
		status = schema.PreimageRequestStatusWaitingForPreimage
	}
	_, err = h.storeUserSignedTransactions(ctx, req.PaymentHash, preimageShare, req.Transfer.LeavesToSend, transfer, status, req.ReceiverIdentityPublicKey)
	if err != nil {
		return nil, fmt.Errorf("unable to store user signed transactions: %v", err)
	}

	if preimageShare != nil {
		return preimageShare.PreimageShare, nil
	}

	return nil, nil
}

// InitiatePreimageSwap initiates a preimage swap for the given payment hash.
func (h *LightningHandler) InitiatePreimageSwap(ctx context.Context, req *pb.InitiatePreimageSwapRequest) (*pb.InitiatePreimageSwapResponse, error) {
	if req.Transfer == nil {
		return nil, fmt.Errorf("transfer is required")
	}

	if len(req.Transfer.LeavesToSend) == 0 {
		return nil, fmt.Errorf("at least one leaf must be provided")
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
		db := ent.GetDbFromContext(ctx)
		var err error
		preimageShare, err = db.PreimageShare.Query().Where(preimageshare.PaymentHash(req.PaymentHash)).First(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to get preimage share: %v", err)
		}
		if !bytes.Equal(preimageShare.OwnerIdentityPubkey, req.ReceiverIdentityPublicKey) {
			return nil, fmt.Errorf("preimage share owner identity public key mismatch")
		}
	}

	invoiceAmount := req.InvoiceAmount
	if preimageShare != nil {
		bolt11, err := decodepay.Decodepay(preimageShare.InvoiceString)
		if err != nil {
			return nil, fmt.Errorf("unable to decode invoice: %v", err)
		}
		invoiceAmount = &pb.InvoiceAmount{
			ValueSats: uint64(bolt11.MSatoshi / 1000),
			InvoiceAmountProof: &pb.InvoiceAmountProof{
				Bolt11Invoice: preimageShare.InvoiceString,
			},
		}
	}

	err := h.validateGetPreimageRequest(
		ctx,
		req.PaymentHash,
		req.Transfer.LeavesToSend,
		invoiceAmount,
		req.ReceiverIdentityPublicKey,
		req.FeeSats,
		req.Reason,
	)
	if err != nil {
		return nil, fmt.Errorf("unable to validate request: %v", err)
	}

	leafRefundMap := make(map[string][]byte)
	for _, transaction := range req.Transfer.LeavesToSend {
		leafRefundMap[transaction.LeafId] = transaction.RawTx
	}

	transferHandler := NewTransferHandler(h.config)
	transfer, _, err := transferHandler.createTransfer(
		ctx,
		req.Transfer.TransferId,
		schema.TransferTypePreimageSwap,
		req.Transfer.ExpiryTime.AsTime(),
		req.Transfer.OwnerIdentityPublicKey,
		req.Transfer.ReceiverIdentityPublicKey,
		leafRefundMap,
		nil,
		TransferRoleCoordinator,
	)
	if err != nil {
		return nil, fmt.Errorf("unable to create transfer: %v", err)
	}

	var status schema.PreimageRequestStatus
	if req.Reason == pb.InitiatePreimageSwapRequest_REASON_RECEIVE {
		status = schema.PreimageRequestStatusPreimageShared
	} else {
		status = schema.PreimageRequestStatusWaitingForPreimage
	}
	preimageRequest, err := h.storeUserSignedTransactions(ctx, req.PaymentHash, preimageShare, req.Transfer.LeavesToSend, transfer, status, req.ReceiverIdentityPublicKey)
	if err != nil {
		return nil, fmt.Errorf("unable to store user signed transactions: %v", err)
	}

	selection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}
	result, err := helper.ExecuteTaskWithAllOperators(ctx, h.config, &selection, func(ctx context.Context, operator *so.SigningOperator) ([]byte, error) {
		conn, err := operator.NewGRPCConnection()
		if err != nil {
			return nil, err
		}
		defer conn.Close()

		client := pbinternal.NewSparkInternalServiceClient(conn)
		response, err := client.InitiatePreimageSwap(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("unable to initiate preimage swap: %v", err)
		}
		return response.PreimageShare, nil
	})
	if err != nil {
		return nil, fmt.Errorf("unable to execute task with all operators: %v", err)
	}

	transferProto, err := transfer.MarshalProto(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to marshal transfer: %v", err)
	}

	// Recover secret if necessary
	if req.Reason == pb.InitiatePreimageSwapRequest_REASON_SEND {
		return &pb.InitiatePreimageSwapResponse{Transfer: transferProto}, nil
	}

	shares := make([]*secretsharing.SecretShare, 0)
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
		return nil, fmt.Errorf("unable to recover secret: %v", err)
	}

	secretBytes := secret.Bytes()
	if len(secretBytes) < 32 {
		secretBytes = append(make([]byte, 32-len(secretBytes)), secretBytes...)
	}

	hash := sha256.Sum256(secretBytes)
	if !bytes.Equal(hash[:], req.PaymentHash) {
		baseHandler := NewBaseTransferHandler(h.config)
		_, err := baseHandler.CancelTransfer(ctx, &pb.CancelTransferRequest{
			TransferId:              transfer.ID.String(),
			SenderIdentityPublicKey: transfer.SenderIdentityPubkey,
		}, CancelTransferIntentTask)
		if err != nil {
			logger.Error("InitiatePreimageSwap: unable to cancel own send transfer", "error", err)
		}

		selection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}
		_, err = helper.ExecuteTaskWithAllOperators(ctx, h.config, &selection, func(ctx context.Context, operator *so.SigningOperator) ([]byte, error) {
			conn, err := operator.NewGRPCConnection()
			if err != nil {
				return nil, err
			}
			defer conn.Close()

			client := pbinternal.NewSparkInternalServiceClient(conn)
			_, err = client.CancelTransfer(ctx, &pb.CancelTransferRequest{
				TransferId:              req.Transfer.TransferId,
				SenderIdentityPublicKey: req.Transfer.OwnerIdentityPublicKey,
			})
			if err != nil {
				return nil, fmt.Errorf("unable to cancel other operator's send transfer: %v", err)
			}
			return nil, nil
		})
		if err != nil {
			logger.Error("InitiatePreimageSwap: unable to cancel transfer", "error", err)
		}

		return nil, fmt.Errorf("recovered preimage did not match payment hash: %w", ent.ErrNoRollback)
	}

	err = preimageRequest.Update().SetStatus(schema.PreimageRequestStatusPreimageShared).Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to update preimage request status: %v", err)
	}

	return &pb.InitiatePreimageSwapResponse{Preimage: secretBytes, Transfer: transferProto}, nil
}

// UpdatePreimageRequest updates the preimage request.
func (h *LightningHandler) UpdatePreimageRequest(ctx context.Context, req *pbinternal.UpdatePreimageRequestRequest) error {
	logger := logging.GetLoggerFromContext(ctx)
	db := ent.GetDbFromContext(ctx)

	paymentHash := sha256.Sum256(req.Preimage)
	preimageRequest, err := db.PreimageRequest.Query().Where(
		preimagerequest.And(
			preimagerequest.PaymentHashEQ(paymentHash[:]),
			preimagerequest.ReceiverIdentityPubkeyEQ(req.IdentityPublicKey),
			preimagerequest.StatusEQ(schema.PreimageRequestStatusWaitingForPreimage),
		),
	).First(ctx)
	if err != nil {
		logger.Error("UpdatePreimageRequest: unable to get preimage request", "error", err, "paymentHash", hex.EncodeToString(paymentHash[:]), "identityPublicKey", hex.EncodeToString(req.IdentityPublicKey))
		return fmt.Errorf("UpdatePreimageRequest:unable to get preimage request: %v", err)
	}

	err = preimageRequest.Update().SetStatus(schema.PreimageRequestStatusPreimageShared).Exec(ctx)
	if err != nil {
		return fmt.Errorf("unable to update preimage request status: %v", err)
	}
	return nil
}

// QueryUserSignedRefunds queries the user signed refunds for the given payment hash.
func (h *LightningHandler) QueryUserSignedRefunds(ctx context.Context, req *pb.QueryUserSignedRefundsRequest) (*pb.QueryUserSignedRefundsResponse, error) {
	logger := logging.GetLoggerFromContext(ctx)
	db := ent.GetDbFromContext(ctx)

	preimageRequest, err := db.PreimageRequest.Query().Where(
		preimagerequest.And(
			preimagerequest.PaymentHashEQ(req.PaymentHash),
			preimagerequest.ReceiverIdentityPubkeyEQ(req.IdentityPublicKey),
			preimagerequest.StatusEQ(schema.PreimageRequestStatusWaitingForPreimage),
		),
	).First(ctx)
	if err != nil {
		logger.Error("QueryUserSignedRefunds: unable to get preimage request", "error", err, "paymentHash", hex.EncodeToString(req.PaymentHash), "identityPublicKey", hex.EncodeToString(req.IdentityPublicKey))
		return nil, fmt.Errorf("QueryUserSignedRefunds: unable to get preimage request: %v", err)
	}

	transfer, err := preimageRequest.QueryTransfers().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get transfer: %v", err)
	}

	if transfer.Status != schema.TransferStatusSenderKeyTweakPending {
		return nil, fmt.Errorf("transfer is not in the sender key tweak pending status, status: %s", transfer.Status)
	}

	userSignedRefunds, err := preimageRequest.QueryTransactions().All(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get user signed transactions: %v", err)
	}

	protos := make([]*pb.UserSignedRefund, len(userSignedRefunds))
	for i, userSignedRefund := range userSignedRefunds {
		userSigningCommitment := &pbcommon.SigningCommitment{}
		err := proto.Unmarshal(userSignedRefund.SigningCommitments, userSigningCommitment)
		if err != nil {
			return nil, fmt.Errorf("unable to unmarshal user signed refund: %v", err)
		}
		signingCommitments := &pb.SigningCommitments{}
		err = proto.Unmarshal(userSignedRefund.SigningCommitments, signingCommitments)
		if err != nil {
			return nil, fmt.Errorf("unable to unmarshal user signed refund: %v", err)
		}
		treeNode, err := userSignedRefund.QueryTreeNode().WithTree().Only(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to get tree node: %v", err)
		}
		networkProto, err := treeNode.Edges.Tree.Network.MarshalProto()
		if err != nil {
			return nil, fmt.Errorf("unable to marshal network: %v", err)
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
	return &pb.QueryUserSignedRefundsResponse{UserSignedRefunds: protos}, nil
}

func (h *LightningHandler) ProvidePreimageInternal(ctx context.Context, req *pb.ProvidePreimageRequest) (*ent.Transfer, error) {
	logger := logging.GetLoggerFromContext(ctx)
	db := ent.GetDbFromContext(ctx)

	calculatedPaymentHash := sha256.Sum256(req.Preimage)
	if !bytes.Equal(calculatedPaymentHash[:], req.PaymentHash) {
		return nil, fmt.Errorf("invalid preimage")
	}
	logger.Debug("ProvidePreimage: hash calculated")

	preimageRequest, err := db.PreimageRequest.Query().Where(
		preimagerequest.And(
			preimagerequest.PaymentHashEQ(req.PaymentHash),
			preimagerequest.ReceiverIdentityPubkeyEQ(req.IdentityPublicKey),
			preimagerequest.StatusEQ(schema.PreimageRequestStatusWaitingForPreimage),
		),
	).First(ctx)
	if err != nil {
		logger.Error("ProvidePreimage: unable to get preimage request", "error", err, "paymentHash", hex.EncodeToString(req.PaymentHash), "identityPublicKey", hex.EncodeToString(req.IdentityPublicKey))
		return nil, fmt.Errorf("ProvidePreimage: unable to get preimage request: %v", err)
	}
	logger.Debug("ProvidePreimage: preimage request found")

	preimageRequest, err = preimageRequest.Update().
		SetStatus(schema.PreimageRequestStatusPreimageShared).
		SetPreimage(req.Preimage).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to update preimage request status: %v", err)
	}
	logger.Debug("ProvidePreimage: preimage request status updated")

	transfer, err := preimageRequest.QueryTransfers().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get transfer: %v", err)
	}
	logger.Debug("ProvidePreimage: transfer loaded")

	// apply key tweaks for all transfer_leaves
	transferLeaves, err := transfer.QueryTransferLeaves().All(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get transfer leaves: %v", err)
	}

	for _, leaf := range transferLeaves {
		keyTweak := &pb.SendLeafKeyTweak{}
		err := proto.Unmarshal(leaf.KeyTweak, keyTweak)
		if err != nil {
			return nil, fmt.Errorf("unable to unmarshal key tweak: %v", err)
		}
		treeNode, err := leaf.QueryLeaf().Only(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to get tree node: %v", err)
		}
		err = helper.TweakLeafKey(ctx, treeNode, keyTweak, nil)
		if err != nil {
			return nil, fmt.Errorf("unable to tweak leaf key: %v", err)
		}
		_, err = leaf.Update().SetKeyTweak(nil).Save(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to update leaf key tweak: %v", err)
		}
	}

	transfer, err = transfer.Update().SetStatus(schema.TransferStatusSenderKeyTweaked).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to update transfer status: %v", err)
	}

	return transfer, nil
}

func (h *LightningHandler) ProvidePreimage(ctx context.Context, req *pb.ProvidePreimageRequest) (*pb.ProvidePreimageResponse, error) {
	logger := logging.GetLoggerFromContext(ctx)

	logger.Debug("ProvidePreimage: request received")
	transfer, err := h.ProvidePreimageInternal(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("unable to provide preimage: %v", err)
	}
	logger.Debug("ProvidePreimage: provided preimage internal completed")

	transferProto, err := transfer.MarshalProto(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to marshal transfer: %v", err)
	}
	logger.Debug("ProvidePreimage: transfer marshalled")

	operatorSelection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}
	_, err = helper.ExecuteTaskWithAllOperators(ctx, h.config, &operatorSelection, func(ctx context.Context, operator *so.SigningOperator) (interface{}, error) {
		conn, err := operator.NewGRPCConnection()
		if err != nil {
			return nil, err
		}
		defer conn.Close()

		client := pbinternal.NewSparkInternalServiceClient(conn)
		_, err = client.ProvidePreimage(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("unable to provide preimage: %v", err)
		}
		return nil, nil
	})
	if err != nil {
		return nil, fmt.Errorf("unable to execute task with all operators: %v", err)
	}
	logger.Debug("ProvidePreimage: SO synced")

	return &pb.ProvidePreimageResponse{Transfer: transferProto}, nil
}

func (h *LightningHandler) ReturnLightningPayment(ctx context.Context, req *pb.ReturnLightningPaymentRequest, internal bool) (*emptypb.Empty, error) {
	logger := logging.GetLoggerFromContext(ctx)

	if !internal {
		if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, req.UserIdentityPublicKey); err != nil {
			return nil, err
		}
	}

	db := ent.GetDbFromContext(ctx)
	preimageRequest, err := db.PreimageRequest.Query().Where(
		preimagerequest.And(
			preimagerequest.PaymentHashEQ(req.PaymentHash),
			preimagerequest.ReceiverIdentityPubkeyEQ(req.UserIdentityPublicKey),
			preimagerequest.StatusEQ(schema.PreimageRequestStatusWaitingForPreimage),
		),
	).First(ctx)
	if err != nil {
		logger.Error("ReturnLightningPayment: unable to get preimage request", "error", err, "paymentHash", hex.EncodeToString(req.PaymentHash), "identityPublicKey", hex.EncodeToString(req.UserIdentityPublicKey))
		return nil, fmt.Errorf("ReturnLightningPayment: unable to get preimage request: %v", err)
	}

	if preimageRequest.Status != schema.PreimageRequestStatusWaitingForPreimage {
		return nil, fmt.Errorf("preimage request is not in the waiting for preimage status")
	}

	err = preimageRequest.Update().SetStatus(schema.PreimageRequestStatusReturned).Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to update preimage request status: %v", err)
	}

	transfer, err := preimageRequest.QueryTransfers().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get transfer: %v", err)
	}

	if !bytes.Equal(transfer.ReceiverIdentityPubkey, req.UserIdentityPublicKey) {
		return nil, fmt.Errorf("transfer receiver identity public key mismatch")
	}

	transfer, err = transfer.Update().SetStatus(schema.TransferStatusReturned).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to update transfer status: %v", err)
	}

	transferLeaves, err := transfer.QueryTransferLeaves().All(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get transfer leaves: %v", err)
	}

	for _, leaf := range transferLeaves {
		treenode, err := leaf.QueryLeaf().Only(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to get tree node: %v", err)
		}
		_, err = treenode.Update().SetStatus(schema.TreeNodeStatusAvailable).Save(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to update tree node status: %v", err)
		}
	}

	if !internal {
		operatorSelection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}
		_, err = helper.ExecuteTaskWithAllOperators(ctx, h.config, &operatorSelection, func(ctx context.Context, operator *so.SigningOperator) (interface{}, error) {
			conn, err := operator.NewGRPCConnection()
			if err != nil {
				return nil, err
			}
			defer conn.Close()

			client := pbinternal.NewSparkInternalServiceClient(conn)
			_, err = client.ReturnLightningPayment(ctx, req)
			if err != nil {
				return nil, fmt.Errorf("unable to return lightning payment: %v", err)
			}
			return nil, nil
		})
		if err != nil {
			return nil, fmt.Errorf("unable to execute task with all operators: %v", err)
		}
	}

	return &emptypb.Empty{}, nil
}
