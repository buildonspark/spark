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
	"github.com/lightsparkdev/spark-go/common"
	secretsharing "github.com/lightsparkdev/spark-go/common/secret_sharing"
	pbcommon "github.com/lightsparkdev/spark-go/proto/common"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	pbinternal "github.com/lightsparkdev/spark-go/proto/spark_internal"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/authn"
	"github.com/lightsparkdev/spark-go/so/authz"
	"github.com/lightsparkdev/spark-go/so/ent"
	"github.com/lightsparkdev/spark-go/so/ent/preimagerequest"
	"github.com/lightsparkdev/spark-go/so/ent/preimageshare"
	"github.com/lightsparkdev/spark-go/so/ent/schema"
	"github.com/lightsparkdev/spark-go/so/ent/treenode"
	"github.com/lightsparkdev/spark-go/so/helper"
	"github.com/lightsparkdev/spark-go/so/objects"
	decodepay "github.com/nbd-wtf/ln-decodepay"
	"google.golang.org/protobuf/proto"
)

// LightningHandler is the handler for the lightning service.
type LightningHandler struct {
	config        *so.Config
	onchainHelper helper.OnChainHelper
}

// NewLightningHandler returns a new LightningHandler.
func NewLightningHandler(config *so.Config, onchainHelper helper.OnChainHelper) *LightningHandler {
	return &LightningHandler{config: config, onchainHelper: onchainHelper}
}

// StorePreimageShare stores the preimage share for the given payment hash.
func (h *LightningHandler) StorePreimageShare(ctx context.Context, req *pb.StorePreimageShareRequest) error {
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, req.UserIdentityPublicKey); err != nil {
		return err
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
		SetThreshold(req.Threshold).
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
	nodeIds := make([]uuid.UUID, len(req.NodeIds))
	for i, nodeID := range req.NodeIds {
		nodeID, err := uuid.Parse(nodeID)
		if err != nil {
			return nil, fmt.Errorf("unable to parse node id: %v", err)
		}
		nodeIds[i] = nodeID
	}

	nodes, err := db.TreeNode.Query().Where(treenode.IDIn(nodeIds...)).All(ctx)
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

func (h *LightningHandler) validateGetPreimageRequest(ctx context.Context, transactions []*pb.UserSignedRefund, amount *pb.InvoiceAmount) error {
	// TODO: This validation requires validate partial signature.
	return nil
}

func (h *LightningHandler) storeUserSignedTransactions(
	ctx context.Context,
	paymentHash []byte,
	preimageShare *ent.PreimageShare,
	transactions []*pb.UserSignedRefund,
	transfer *ent.Transfer,
	status schema.PreimageRequestStatus,
) (*ent.PreimageRequest, error) {
	db := ent.GetDbFromContext(ctx)
	preimageRequestMutator := db.PreimageRequest.Create().
		SetPaymentHash(paymentHash).
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
		nodeID, err := uuid.Parse(transaction.NodeId)
		if err != nil {
			return nil, fmt.Errorf("unable to parse node id: %v", err)
		}
		userSignatureCommitmentBytes, err := proto.Marshal(transaction.UserSignatureCommitment)
		if err != nil {
			return nil, fmt.Errorf("unable to marshal user signature commitment: %v", err)
		}
		_, err = db.UserSignedTransaction.Create().
			SetTransaction(transaction.RefundTx).
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
		db.TreeNode.UpdateOne(node).
			SetStatus(schema.TreeNodeStatusTransferLocked).
			Exec(ctx)
	}
	return preimageRequest, nil
}

// GetPreimageShare gets the preimage share for the given payment hash.
func (h *LightningHandler) GetPreimageShare(ctx context.Context, req *pb.InitiatePreimageSwapRequest) ([]byte, error) {
	err := h.validateGetPreimageRequest(ctx, req.UserSignedRefunds, req.InvoiceAmount)
	if err != nil {
		return nil, fmt.Errorf("unable to validate request: %v", err)
	}

	var preimageShare *ent.PreimageShare
	if req.Reason == pb.InitiatePreimageSwapRequest_REASON_RECEIVE {
		db := ent.GetDbFromContext(ctx)
		preimageShare, err = db.PreimageShare.Query().Where(preimageshare.PaymentHash(req.PaymentHash)).First(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to get preimage share: %v", err)
		}
		if !bytes.Equal(preimageShare.OwnerIdentityPubkey, req.ReceiverIdentityPublicKey) {
			return nil, fmt.Errorf("preimage share owner identity public key mismatch")
		}
	}

	leafRefundMap := make(map[string][]byte)
	for _, transaction := range req.UserSignedRefunds {
		leafRefundMap[transaction.NodeId] = transaction.RefundTx
	}

	transferHandler := NewTransferHandler(h.onchainHelper, h.config)
	transfer, _, err := transferHandler.createTransfer(ctx, req.Transfer.TransferId, schema.TransferTypePreimageSwap, req.Transfer.ExpiryTime.AsTime(), req.Transfer.OwnerIdentityPublicKey, req.Transfer.ReceiverIdentityPublicKey, leafRefundMap)
	if err != nil {
		return nil, fmt.Errorf("unable to create transfer: %v", err)
	}

	var status schema.PreimageRequestStatus
	if req.Reason == pb.InitiatePreimageSwapRequest_REASON_RECEIVE {
		status = schema.PreimageRequestStatusPreimageShared
	} else {
		status = schema.PreimageRequestStatusWaitingForPreimage
	}
	_, err = h.storeUserSignedTransactions(ctx, req.PaymentHash, preimageShare, req.UserSignedRefunds, transfer, status)
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
	err := h.validateGetPreimageRequest(ctx, req.UserSignedRefunds, req.InvoiceAmount)
	if err != nil {
		return nil, fmt.Errorf("unable to validate request: %v", err)
	}

	var preimageShare *ent.PreimageShare
	if req.Reason == pb.InitiatePreimageSwapRequest_REASON_RECEIVE {
		db := ent.GetDbFromContext(ctx)
		preimageShare, err = db.PreimageShare.Query().Where(preimageshare.PaymentHash(req.PaymentHash)).First(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to get preimage share: %v", err)
		}
		if !bytes.Equal(preimageShare.OwnerIdentityPubkey, req.ReceiverIdentityPublicKey) {
			return nil, fmt.Errorf("preimage share owner identity public key mismatch")
		}
	}

	leafRefundMap := make(map[string][]byte)
	for _, transaction := range req.UserSignedRefunds {
		leafRefundMap[transaction.NodeId] = transaction.RefundTx
	}

	transferHandler := NewTransferHandler(h.onchainHelper, h.config)
	transfer, _, err := transferHandler.createTransfer(ctx, req.Transfer.TransferId, schema.TransferTypePreimageSwap, req.Transfer.ExpiryTime.AsTime(), req.Transfer.OwnerIdentityPublicKey, req.Transfer.ReceiverIdentityPublicKey, leafRefundMap)
	if err != nil {
		return nil, fmt.Errorf("unable to create transfer: %v", err)
	}

	var status schema.PreimageRequestStatus
	if req.Reason == pb.InitiatePreimageSwapRequest_REASON_RECEIVE {
		status = schema.PreimageRequestStatusPreimageShared
	} else {
		status = schema.PreimageRequestStatusWaitingForPreimage
	}
	preimageRequest, err := h.storeUserSignedTransactions(ctx, req.PaymentHash, preimageShare, req.UserSignedRefunds, transfer, status)
	if err != nil {
		return nil, fmt.Errorf("unable to store user signed transactions: %v", err)
	}

	selection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}
	result, err := helper.ExecuteTaskWithAllOperators(ctx, h.config, &selection, func(ctx context.Context, operator *so.SigningOperator) ([]byte, error) {
		conn, err := common.NewGRPCConnection(operator.Address)
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

	hash := sha256.Sum256(secret.Bytes())
	if !bytes.Equal(hash[:], req.PaymentHash) {
		// TODO: Notify the operator that the preimage is wrong
		return nil, fmt.Errorf("invalid preimage")
	}

	err = preimageRequest.Update().SetStatus(schema.PreimageRequestStatusPreimageShared).Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to update preimage request status: %v", err)
	}

	return &pb.InitiatePreimageSwapResponse{Preimage: secret.Bytes(), Transfer: transferProto}, nil
}

// UpdatePreimageRequest updates the preimage request.
func (h *LightningHandler) UpdatePreimageRequest(ctx context.Context, req *pbinternal.UpdatePreimageRequestRequest) error {
	db := ent.GetDbFromContext(ctx)

	paymentHash := sha256.Sum256(req.Preimage)
	preimageRequest, err := db.PreimageRequest.Query().Where(preimagerequest.PaymentHashEQ(paymentHash[:])).First(ctx)
	if err != nil {
		return fmt.Errorf("unable to get preimage request: %v", err)
	}

	err = preimageRequest.Update().SetStatus(schema.PreimageRequestStatusPreimageShared).Exec(ctx)
	if err != nil {
		return fmt.Errorf("unable to update preimage request status: %v", err)
	}
	return nil
}

// QueryUserSignedRefunds queries the user signed refunds for the given payment hash.
func (h *LightningHandler) QueryUserSignedRefunds(ctx context.Context, req *pb.QueryUserSignedRefundsRequest) (*pb.QueryUserSignedRefundsResponse, error) {
	db := ent.GetDbFromContext(ctx)
	preimageRequest, err := db.PreimageRequest.Query().Where(preimagerequest.PaymentHashEQ(req.PaymentHash)).First(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get preimage request: %v", err)
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
		treeNode, err := userSignedRefund.QueryTreeNode().Only(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to get tree node: %v", err)
		}
		protos[i] = &pb.UserSignedRefund{
			NodeId:                  treeNode.ID.String(),
			RefundTx:                userSignedRefund.Transaction,
			UserSignature:           userSignedRefund.UserSignature,
			SigningCommitments:      signingCommitments,
			UserSignatureCommitment: userSigningCommitment,
		}
	}
	return &pb.QueryUserSignedRefundsResponse{UserSignedRefunds: protos}, nil
}

func (h *LightningHandler) ProvidePreimageInternal(ctx context.Context, req *pb.ProvidePreimageRequest) (*ent.Transfer, error) {
	db := ent.GetDbFromContext(ctx)
	calculatedPaymentHash := sha256.Sum256(req.Preimage)
	if !bytes.Equal(calculatedPaymentHash[:], req.PaymentHash) {
		return nil, fmt.Errorf("invalid preimage")
	}

	preimageRequest, err := db.PreimageRequest.Query().Where(preimagerequest.PaymentHashEQ(req.PaymentHash)).First(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get preimage request: %v", err)
	}

	preimageRequest, err = preimageRequest.Update().SetStatus(schema.PreimageRequestStatusPreimageShared).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to update preimage request status: %v", err)
	}

	transfer, err := preimageRequest.QueryTransfers().Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get transfer: %v", err)
	}

	// apply key tweaks for all transfer_leaves
	transferHandler := NewTransferHandler(h.onchainHelper, h.config)
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
		err = transferHandler.tweakLeafKey(ctx, treeNode, keyTweak, nil)
		if err != nil {
			return nil, fmt.Errorf("unable to tweak leaf key: %v", err)
		}
	}

	transfer, err = transfer.Update().SetStatus(schema.TransferStatusSenderKeyTweaked).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to update transfer status: %v", err)
	}

	return transfer, nil
}

func (h *LightningHandler) ProvidePreimage(ctx context.Context, req *pb.ProvidePreimageRequest) (*pb.ProvidePreimageResponse, error) {
	transfer, err := h.ProvidePreimageInternal(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("unable to provide preimage: %v", err)
	}

	transferProto, err := transfer.MarshalProto(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to marshal transfer: %v", err)
	}

	operatorSelection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}
	_, err = helper.ExecuteTaskWithAllOperators(ctx, h.config, &operatorSelection, func(ctx context.Context, operator *so.SigningOperator) (interface{}, error) {
		conn, err := common.NewGRPCConnection(operator.Address)
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

	return &pb.ProvidePreimageResponse{Transfer: transferProto}, nil
}
