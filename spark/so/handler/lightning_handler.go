package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"math/big"
	"strings"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go/common"
	secretsharing "github.com/lightsparkdev/spark-go/common/secret_sharing"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	pbinternal "github.com/lightsparkdev/spark-go/proto/spark_internal"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/authn"
	"github.com/lightsparkdev/spark-go/so/authz"
	"github.com/lightsparkdev/spark-go/so/ent"
	"github.com/lightsparkdev/spark-go/so/ent/preimageshare"
	"github.com/lightsparkdev/spark-go/so/ent/schema"
	"github.com/lightsparkdev/spark-go/so/ent/treenode"
	"github.com/lightsparkdev/spark-go/so/helper"
	"github.com/lightsparkdev/spark-go/so/objects"
	"google.golang.org/protobuf/proto"
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

func (h *LightningHandler) storeUserSignedTransactions(ctx context.Context, preimageShare *ent.PreimageShare, transactions []*pb.UserSignedRefund, userIdentityPubkey []byte) error {
	db := ent.GetDbFromContext(ctx)
	preimageRequest, err := db.PreimageRequest.Create().
		SetPreimageShares(preimageShare).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("unable to create preimage request: %v", err)
	}

	for _, transaction := range transactions {
		commitmentsBytes, err := proto.Marshal(transaction.SigningCommitments)
		if err != nil {
			return fmt.Errorf("unable to marshal signing commitments: %v", err)
		}
		_, err = db.UserSignedTransaction.Create().
			SetTransaction(transaction.RefundTx).
			SetUserSignature(transaction.UserSignature).
			SetSigningCommitments(commitmentsBytes).
			SetPreimageRequest(preimageRequest).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("unable to store user signed transaction: %v", err)
		}

		nodeID, err := uuid.Parse(transaction.NodeId)
		if err != nil {
			return fmt.Errorf("unable to parse node id: %v", err)
		}
		node, err := db.TreeNode.Get(ctx, nodeID)
		if err != nil {
			return fmt.Errorf("unable to get node: %v", err)
		}
		db.TreeNode.UpdateOne(node).
			SetStatus(schema.TreeNodeStatusDestinationLock).
			SetDestinationLockIdentityPubkey(userIdentityPubkey).
			Exec(ctx)
	}
	return nil
}

// GetPreimageShare gets the preimage share for the given payment hash.
func (h *LightningHandler) GetPreimageShare(ctx context.Context, req *pbinternal.GetPreimageShareRequest) ([]byte, error) {
	err := h.validateGetPreimageRequest(ctx, req.UserSignedRefunds, req.InvoiceAmount)
	if err != nil {
		return nil, fmt.Errorf("unable to validate request: %v", err)
	}

	db := ent.GetDbFromContext(ctx)
	preimageShare, err := db.PreimageShare.Query().Where(preimageshare.PaymentHash(req.PaymentHash)).First(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get preimage share: %v", err)
	}

	err = h.storeUserSignedTransactions(ctx, preimageShare, req.UserSignedRefunds, preimageShare.OwnerIdentityPubkey)
	if err != nil {
		return nil, fmt.Errorf("unable to store user signed transactions: %v", err)
	}

	return preimageShare.PreimageShare, nil
}

// GetPreimage gets the preimage for the given payment hash.
func (h *LightningHandler) GetPreimage(ctx context.Context, req *pb.GetPreimageRequest) (*pb.GetPreimageResponse, error) {
	err := h.validateGetPreimageRequest(ctx, req.UserSignedRefunds, req.InvoiceAmount)
	if err != nil {
		return nil, fmt.Errorf("unable to validate request: %v", err)
	}

	db := ent.GetDbFromContext(ctx)
	preimageShare, err := db.PreimageShare.Query().Where(preimageshare.PaymentHash(req.PaymentHash)).First(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get preimage share: %v", err)
	}
	if err = authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, preimageShare.OwnerIdentityPubkey); err != nil {
		return nil, err
	}

	err = h.storeUserSignedTransactions(ctx, preimageShare, req.UserSignedRefunds, preimageShare.OwnerIdentityPubkey)
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
		response, err := client.GetPreimageShare(ctx, &pbinternal.GetPreimageShareRequest{
			PaymentHash:       req.PaymentHash,
			UserSignedRefunds: req.UserSignedRefunds,
			InvoiceAmount:     req.InvoiceAmount,
		})
		if err != nil {
			return nil, fmt.Errorf("unable to get preimage shares: %v", err)
		}
		return response.PreimageShare, nil
	})
	if err != nil {
		return nil, fmt.Errorf("unable to execute task with all operators: %v", err)
	}

	shares := make([]*secretsharing.SecretShare, 0)
	for identifier, share := range result {
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

	secretShare, err := secretsharing.RecoverSecret(shares)
	if err != nil {
		return nil, fmt.Errorf("unable to recover secret: %v", err)
	}

	hash := sha256.Sum256(secretShare.Bytes())
	if !bytes.Equal(hash[:], req.PaymentHash) {
		return nil, fmt.Errorf("invalid preimage")
	}

	return &pb.GetPreimageResponse{Preimage: hash[:]}, nil
}
