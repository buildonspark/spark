package grpc

import (
	"context"
	"fmt"

	"github.com/lightsparkdev/spark/common/keys"

	"github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/limiter"
	"github.com/lightsparkdev/spark/so/protoconverter"

	"github.com/lightsparkdev/spark/so/handler/tokens"

	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/logging"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/handler"
	"github.com/lightsparkdev/spark/so/knobs"
	events "github.com/lightsparkdev/spark/so/stream"
	"google.golang.org/protobuf/types/known/emptypb"
)

// SparkServer is the grpc server for the Spark protocol.
// It will be used by the user or Spark service provider.
type SparkServer struct {
	pb.UnimplementedSparkServiceServer
	config *so.Config

	// ResourceGuard to limit concurrent ClaimTransferSignRefunds calls for the same transfer ID. This
	// is already enforced through row locking, but is more to prevent unnecessary load on the DB.
	claimTransferSignRefundsTransferGuard limiter.ResourceGuard
	eventsRouter                          *events.EventRouter
}

var emptyResponse = &emptypb.Empty{}

// NewSparkServer creates a new SparkServer.
func NewSparkServer(config *so.Config, eventsRouter *events.EventRouter) *SparkServer {
	return &SparkServer{config: config, claimTransferSignRefundsTransferGuard: limiter.NewResourceGuard(), eventsRouter: eventsRouter}
}

// GenerateDepositAddress generates a deposit address for the given public key.
func (s *SparkServer) GenerateDepositAddress(ctx context.Context, req *pb.GenerateDepositAddressRequest) (*pb.GenerateDepositAddressResponse, error) {
	idPubKey, err := keys.ParsePublicKey(req.GetIdentityPublicKey())
	if err != nil {
		return nil, fmt.Errorf("failed to parse identity public key: %w", err)
	}
	ctx, _ = logging.WithIdentityPubkey(ctx, idPubKey)
	depositHandler := handler.NewDepositHandler(s.config)
	return depositHandler.GenerateDepositAddress(ctx, s.config, req)
}

// GenerateStaticDepositAddress generates a static deposit address for the given public key.
func (s *SparkServer) GenerateStaticDepositAddress(ctx context.Context, req *pb.GenerateStaticDepositAddressRequest) (*pb.GenerateStaticDepositAddressResponse, error) {
	idPubKey, err := keys.ParsePublicKey(req.GetIdentityPublicKey())
	if err != nil {
		return nil, fmt.Errorf("failed to parse identity public key: %w", err)
	}
	ctx, _ = logging.WithIdentityPubkey(ctx, idPubKey)
	depositHandler := handler.NewDepositHandler(s.config)
	return depositHandler.GenerateStaticDepositAddress(ctx, s.config, req)
}

// StartDepositTreeCreation verifies the on chain utxo, and then verifies and signs the offchain root and refund transactions.
func (s *SparkServer) StartDepositTreeCreation(ctx context.Context, req *pb.StartDepositTreeCreationRequest) (*pb.StartDepositTreeCreationResponse, error) {
	idPubKey, err := keys.ParsePublicKey(req.GetIdentityPublicKey())
	if err != nil {
		return nil, fmt.Errorf("failed to parse identity public key: %w", err)
	}
	ctx, _ = logging.WithIdentityPubkey(ctx, idPubKey)
	depositHandler := handler.NewDepositHandler(s.config)
	return depositHandler.StartDepositTreeCreation(ctx, s.config, req)
}

// StartTreeCreation is deprecated.
// Deprecated: Use StartDepositTreeCreation instead
func (s *SparkServer) StartTreeCreation(ctx context.Context, req *pb.StartTreeCreationRequest) (*pb.StartTreeCreationResponse, error) {
	idPubKey, err := keys.ParsePublicKey(req.GetIdentityPublicKey())
	if err != nil {
		return nil, fmt.Errorf("failed to parse identity public key: %w", err)
	}
	ctx, _ = logging.WithIdentityPubkey(ctx, idPubKey)
	depositHandler := handler.NewDepositHandler(s.config)
	return depositHandler.StartTreeCreation(ctx, s.config, req)
}

// FinalizeNodeSignatures verifies the node signatures and updates the node.
func (s *SparkServer) FinalizeNodeSignatures(ctx context.Context, req *pb.FinalizeNodeSignaturesRequest) (*pb.FinalizeNodeSignaturesResponse, error) {
	finalizeSignatureHandler := handler.NewFinalizeSignatureHandler(s.config)
	return finalizeSignatureHandler.FinalizeNodeSignatures(ctx, req)
}

// FinalizeNodeSignaturesV2 verifies the node signatures and updates the node.
func (s *SparkServer) FinalizeNodeSignaturesV2(ctx context.Context, req *pb.FinalizeNodeSignaturesRequest) (*pb.FinalizeNodeSignaturesResponse, error) {
	finalizeSignatureHandler := handler.NewFinalizeSignatureHandler(s.config)
	return finalizeSignatureHandler.FinalizeNodeSignaturesV2(ctx, req)
}

// StartTransfer initiates a transfer from sender.
func (s *SparkServer) StartTransfer(ctx context.Context, req *pb.StartTransferRequest) (*pb.StartTransferResponse, error) {
	ownerIDPubKey, err := keys.ParsePublicKey(req.GetOwnerIdentityPublicKey())
	if err != nil {
		return nil, fmt.Errorf("failed to parse owner identity public key: %w", err)
	}
	ctx, _ = logging.WithIdentityPubkey(ctx, ownerIDPubKey)
	transferHander := handler.NewTransferHandler(s.config)
	return transferHander.StartTransfer(ctx, req)
}

// StartTransferV2 initiates a transfer from sender.
func (s *SparkServer) StartTransferV2(ctx context.Context, req *pb.StartTransferRequest) (*pb.StartTransferResponse, error) {
	ownerIDPubKey, err := keys.ParsePublicKey(req.GetOwnerIdentityPublicKey())
	if err != nil {
		return nil, fmt.Errorf("failed to parse owner identity public key: %w", err)
	}
	ctx, _ = logging.WithIdentityPubkey(ctx, ownerIDPubKey)
	transferHander := handler.NewTransferHandler(s.config)
	return transferHander.StartTransferV2(ctx, req)
}

// FinalizeTransfer completes a transfer from sender.
func (s *SparkServer) FinalizeTransfer(ctx context.Context, req *pb.FinalizeTransferRequest) (*pb.FinalizeTransferResponse, error) {
	ownerIDPubKey, err := keys.ParsePublicKey(req.GetOwnerIdentityPublicKey())
	if err != nil {
		return nil, fmt.Errorf("failed to parse owner identity public key: %w", err)
	}
	ctx, _ = logging.WithIdentityPubkey(ctx, ownerIDPubKey)
	transferHander := handler.NewTransferHandler(s.config)
	return transferHander.FinalizeTransfer(ctx, req)
}

func (s *SparkServer) FinalizeTransferWithTransferPackage(ctx context.Context, req *pb.FinalizeTransferWithTransferPackageRequest) (*pb.FinalizeTransferResponse, error) {
	ownerIDPubKey, err := keys.ParsePublicKey(req.GetOwnerIdentityPublicKey())
	if err != nil {
		return nil, fmt.Errorf("failed to parse owner identity public key: %w", err)
	}
	ctx, _ = logging.WithIdentityPubkey(ctx, ownerIDPubKey)
	transferHandler := handler.NewTransferHandler(s.config)
	return transferHandler.FinalizeTransferWithTransferPackage(ctx, req)
}

// CancelTransfer cancels a transfer from sender before key is tweaked.
func (s *SparkServer) CancelTransfer(ctx context.Context, req *pb.CancelTransferRequest) (*pb.CancelTransferResponse, error) {
	senderIDPubKey, err := keys.ParsePublicKey(req.GetSenderIdentityPublicKey())
	if err != nil {
		return nil, fmt.Errorf("failed to parse sender identity public key: %w", err)
	}
	ctx, _ = logging.WithIdentityPubkey(ctx, senderIDPubKey)
	transferHander := handler.NewTransferHandler(s.config)
	return transferHander.CancelTransfer(ctx, req)
}

// QueryPendingTransfers queries the pending transfers to claim.
func (s *SparkServer) QueryPendingTransfers(ctx context.Context, req *pb.TransferFilter) (*pb.QueryTransfersResponse, error) {
	transferHander := handler.NewTransferHandler(s.config)
	return transferHander.QueryPendingTransfers(ctx, req)
}

// ClaimTransferTweakKeys starts claiming a pending transfer by tweaking keys of leaves.
func (s *SparkServer) ClaimTransferTweakKeys(ctx context.Context, req *pb.ClaimTransferTweakKeysRequest) (*emptypb.Empty, error) {
	ownerIDPubKey, err := keys.ParsePublicKey(req.GetOwnerIdentityPublicKey())
	if err != nil {
		return nil, fmt.Errorf("failed to parse owner identity public key: %w", err)
	}
	ctx, _ = logging.WithIdentityPubkey(ctx, ownerIDPubKey)
	transferHander := handler.NewTransferHandler(s.config)
	return emptyResponse, transferHander.ClaimTransferTweakKeys(ctx, req)
}

// ClaimTransferSignRefundsV2 signs new refund transactions as part of the transfer.
func (s *SparkServer) ClaimTransferSignRefundsV2(ctx context.Context, req *pb.ClaimTransferSignRefundsRequest) (*pb.ClaimTransferSignRefundsResponse, error) {
	ownerIDPubKey, err := keys.ParsePublicKey(req.GetOwnerIdentityPublicKey())
	if err != nil {
		return nil, fmt.Errorf("failed to parse owner identity public key: %w", err)
	}
	ctx, _ = logging.WithIdentityPubkey(ctx, ownerIDPubKey)
	transferHander := handler.NewTransferHandler(s.config)
	return transferHander.ClaimTransferSignRefundsV2(ctx, req)
}

// ClaimTransferSignRefunds signs new refund transactions as part of the transfer.
func (s *SparkServer) ClaimTransferSignRefunds(ctx context.Context, req *pb.ClaimTransferSignRefundsRequest) (*pb.ClaimTransferSignRefundsResponse, error) {
	ownerIDPubKey, err := keys.ParsePublicKey(req.GetOwnerIdentityPublicKey())
	if err != nil {
		return nil, fmt.Errorf("failed to parse owner identity public key: %w", err)
	}
	ctx, _ = logging.WithIdentityPubkey(ctx, ownerIDPubKey)

	if !s.claimTransferSignRefundsTransferGuard.Acquire(req.TransferId) {
		return nil, errors.ResourceExhaustedConcurrencyLimitExceeded(fmt.Errorf("concurrency limit exceeded"))
	}
	defer s.claimTransferSignRefundsTransferGuard.Release(req.TransferId)

	transferHander := handler.NewTransferHandler(s.config)
	return transferHander.ClaimTransferSignRefunds(ctx, req)
}

// StorePreimageShare stores the preimage share for the given payment hash.
func (s *SparkServer) StorePreimageShare(ctx context.Context, req *pb.StorePreimageShareRequest) (*emptypb.Empty, error) {
	userIDPubKey, err := keys.ParsePublicKey(req.GetUserIdentityPublicKey())
	if err != nil {
		return nil, fmt.Errorf("failed to parse user identity public key: %w", err)
	}
	ctx, _ = logging.WithIdentityPubkey(ctx, userIDPubKey)
	lightningHandler := handler.NewLightningHandler(s.config)
	return emptyResponse, lightningHandler.StorePreimageShare(ctx, req)
}

// GetSigningCommitments gets the signing commitments for the given node ids.
func (s *SparkServer) GetSigningCommitments(ctx context.Context, req *pb.GetSigningCommitmentsRequest) (*pb.GetSigningCommitmentsResponse, error) {
	lightningHandler := handler.NewLightningHandler(s.config)
	return lightningHandler.GetSigningCommitments(ctx, req)
}

// InitiatePreimageSwap initiates a preimage swap for the given payment hash.
func (s *SparkServer) InitiatePreimageSwap(ctx context.Context, req *pb.InitiatePreimageSwapRequest) (*pb.InitiatePreimageSwapResponse, error) {
	ownerIDPubKey, err := keys.ParsePublicKey(req.GetTransfer().GetOwnerIdentityPublicKey())
	if err != nil {
		return nil, fmt.Errorf("failed to parse owner identity public key: %w", err)
	}
	ctx, _ = logging.WithIdentityPubkey(ctx, ownerIDPubKey)
	lightningHandler := handler.NewLightningHandler(s.config)
	return lightningHandler.InitiatePreimageSwap(ctx, req)
}

// InitiatePreimageSwapV2 initiates a preimage swap for the given payment hash.
func (s *SparkServer) InitiatePreimageSwapV2(ctx context.Context, req *pb.InitiatePreimageSwapRequest) (*pb.InitiatePreimageSwapResponse, error) {
	ownerIDPubKey, err := keys.ParsePublicKey(req.GetTransfer().GetOwnerIdentityPublicKey())
	if err != nil {
		return nil, fmt.Errorf("failed to parse owner identity public key: %w", err)
	}
	ctx, _ = logging.WithIdentityPubkey(ctx, ownerIDPubKey)
	lightningHandler := handler.NewLightningHandler(s.config)
	return lightningHandler.InitiatePreimageSwapV2(ctx, req)
}

// CooperativeExit asks for signatures for refund transactions spending leaves
// and connector outputs on another user's L1 transaction.
func (s *SparkServer) CooperativeExit(ctx context.Context, req *pb.CooperativeExitRequest) (*pb.CooperativeExitResponse, error) {
	ownerIDPubKey, err := keys.ParsePublicKey(req.GetTransfer().GetOwnerIdentityPublicKey())
	if err != nil {
		return nil, fmt.Errorf("failed to parse owner identity public key: %w", err)
	}
	ctx, _ = logging.WithIdentityPubkey(ctx, ownerIDPubKey)
	coopExitHandler := handler.NewCooperativeExitHandler(s.config)
	return coopExitHandler.CooperativeExit(ctx, req)
}

// CooperativeExitV2 is the same as CooperativeExit, but enforces use of direct transactions for unilateral exits
func (s *SparkServer) CooperativeExitV2(ctx context.Context, req *pb.CooperativeExitRequest) (*pb.CooperativeExitResponse, error) {
	ownerIDPubKey, err := keys.ParsePublicKey(req.GetTransfer().GetOwnerIdentityPublicKey())
	if err != nil {
		return nil, fmt.Errorf("failed to parse owner identity public key: %w", err)
	}
	ctx, _ = logging.WithIdentityPubkey(ctx, ownerIDPubKey)
	coopExitHandler := handler.NewCooperativeExitHandler(s.config)
	return coopExitHandler.CooperativeExit(ctx, req)
}

// StartLeafSwap initiates a swap of leaves between two users.
func (s *SparkServer) StartLeafSwap(ctx context.Context, req *pb.StartTransferRequest) (*pb.StartTransferResponse, error) {
	ownerIDPubKey, err := keys.ParsePublicKey(req.GetOwnerIdentityPublicKey())
	if err != nil {
		return nil, fmt.Errorf("failed to parse owner identity public key: %w", err)
	}
	ctx, _ = logging.WithIdentityPubkey(ctx, ownerIDPubKey)
	transferHander := handler.NewTransferHandler(s.config)
	return transferHander.StartLeafSwap(ctx, req)
}

// StartLeafSwapV2 initiates a swap of leaves between two users.
func (s *SparkServer) StartLeafSwapV2(ctx context.Context, req *pb.StartTransferRequest) (*pb.StartTransferResponse, error) {
	ownerIDPubKey, err := keys.ParsePublicKey(req.GetOwnerIdentityPublicKey())
	if err != nil {
		return nil, fmt.Errorf("failed to parse owner identity public key: %w", err)
	}
	ctx, _ = logging.WithIdentityPubkey(ctx, ownerIDPubKey)
	transferHander := handler.NewTransferHandler(s.config)
	return transferHander.StartLeafSwapV2(ctx, req)
}

// LeafSwap starts the reverse side of a swap of leaves between two users.
// This is deprecated but remains for backwards compatibility,
// CounterLeafSwap should be used instead.
func (s *SparkServer) LeafSwap(ctx context.Context, req *pb.CounterLeafSwapRequest) (*pb.CounterLeafSwapResponse, error) {
	ownerIDPubKey, err := keys.ParsePublicKey(req.GetTransfer().GetOwnerIdentityPublicKey())
	if err != nil {
		return nil, fmt.Errorf("failed to parse owner identity public key: %w", err)
	}
	ctx, _ = logging.WithIdentityPubkey(ctx, ownerIDPubKey)
	transferHander := handler.NewTransferHandler(s.config)
	return transferHander.CounterLeafSwap(ctx, req)
}

// CounterLeafSwap starts the reverse side of a swap of leaves between two users.
func (s *SparkServer) CounterLeafSwap(ctx context.Context, req *pb.CounterLeafSwapRequest) (*pb.CounterLeafSwapResponse, error) {
	ownerIDPubKey, err := keys.ParsePublicKey(req.GetTransfer().GetOwnerIdentityPublicKey())
	if err != nil {
		return nil, fmt.Errorf("failed to parse owner identity public key: %w", err)
	}
	ctx, _ = logging.WithIdentityPubkey(ctx, ownerIDPubKey)
	transferHander := handler.NewTransferHandler(s.config)
	return transferHander.CounterLeafSwap(ctx, req)
}

// CounterLeafSwapV2 starts the reverse side of a swap of leaves between two users.
func (s *SparkServer) CounterLeafSwapV2(ctx context.Context, req *pb.CounterLeafSwapRequest) (*pb.CounterLeafSwapResponse, error) {
	ownerIDPubKey, err := keys.ParsePublicKey(req.GetTransfer().GetOwnerIdentityPublicKey())
	if err != nil {
		return nil, fmt.Errorf("failed to parse owner identity public key: %w", err)
	}
	ctx, _ = logging.WithIdentityPubkey(ctx, ownerIDPubKey)
	transferHander := handler.NewTransferHandler(s.config)
	return transferHander.CounterLeafSwapV2(ctx, req)
}

// RefreshTimelock refreshes the timelocks of a leaf and its ancestors.
func (s *SparkServer) RefreshTimelock(ctx context.Context, req *pb.RefreshTimelockRequest) (*pb.RefreshTimelockResponse, error) {
	ownerIDPubKey, err := keys.ParsePublicKey(req.GetOwnerIdentityPublicKey())
	if err != nil {
		return nil, fmt.Errorf("failed to parse owner identity public key: %w", err)
	}
	ctx, _ = logging.WithIdentityPubkey(ctx, ownerIDPubKey)
	timelockHandler := handler.NewRefreshTimelockHandler(s.config)
	return timelockHandler.RefreshTimelock(ctx, req)
}

// RefreshTimelockV2 is the same as RefreshTimelock, but requires the direct TX to be included.
func (s *SparkServer) RefreshTimelockV2(ctx context.Context, req *pb.RefreshTimelockRequest) (*pb.RefreshTimelockResponse, error) {
	ownerIDPubKey, err := keys.ParsePublicKey(req.GetOwnerIdentityPublicKey())
	if err != nil {
		return nil, fmt.Errorf("failed to parse owner identity public key: %w", err)
	}
	ctx, _ = logging.WithIdentityPubkey(ctx, ownerIDPubKey)
	leafHandler := handler.NewRefreshTimelockHandler(s.config)
	return leafHandler.RefreshTimelockV2(ctx, req)
}

func (s *SparkServer) ExtendLeaf(ctx context.Context, req *pb.ExtendLeafRequest) (*pb.ExtendLeafResponse, error) {
	ownerIDPubKey, err := keys.ParsePublicKey(req.GetOwnerIdentityPublicKey())
	if err != nil {
		return nil, fmt.Errorf("failed to parse owner identity public key: %w", err)
	}
	ctx, _ = logging.WithIdentityPubkey(ctx, ownerIDPubKey)
	leafHandler := handler.NewExtendLeafHandler(s.config)
	return leafHandler.ExtendLeaf(ctx, req)
}

func (s *SparkServer) ExtendLeafV2(ctx context.Context, req *pb.ExtendLeafRequest) (*pb.ExtendLeafResponse, error) {
	ownerIDPubKey, err := keys.ParsePublicKey(req.GetOwnerIdentityPublicKey())
	if err != nil {
		return nil, fmt.Errorf("failed to parse owner identity public key: %w", err)
	}
	ctx, _ = logging.WithIdentityPubkey(ctx, ownerIDPubKey)
	leafHandler := handler.NewExtendLeafHandler(s.config)
	return leafHandler.ExtendLeafV2(ctx, req)
}

func (s *SparkServer) RenewLeaf(ctx context.Context, req *pb.RenewLeafRequest) (*pb.RenewLeafResponse, error) {
	// Check if RenewLeaf feature is disabled via knob
	knobsService := knobs.GetKnobsService(ctx)
	if knobsService == nil || knobsService.GetValue(knobs.KnobRenewLeafDisabled, 0) >= 1 {
		return nil, errors.UnavailableErrorf("RenewLeaf endpoint is currently unavailable")
	}

	ownerIDPubKey, err := keys.ParsePublicKey(req.GetOwnerIdentityPublicKey())
	if err != nil {
		return nil, fmt.Errorf("failed to parse owner identity public key: %w", err)
	}
	ctx, _ = logging.WithIdentityPubkey(ctx, ownerIDPubKey)
	leafHandler := handler.NewRenewLeafHandler(s.config)
	return leafHandler.RenewLeaf(ctx, req)
}

// GetSigningOperatorList gets the list of signing operators.
func (s *SparkServer) GetSigningOperatorList(_ context.Context, _ *emptypb.Empty) (*pb.GetSigningOperatorListResponse, error) {
	return &pb.GetSigningOperatorListResponse{SigningOperators: s.config.GetSigningOperatorList()}, nil
}

func (s *SparkServer) QueryUserSignedRefunds(ctx context.Context, req *pb.QueryUserSignedRefundsRequest) (*pb.QueryUserSignedRefundsResponse, error) {
	idPubKey, err := keys.ParsePublicKey(req.GetIdentityPublicKey())
	if err != nil {
		return nil, fmt.Errorf("failed to parse identity public key: %w", err)
	}
	ctx, _ = logging.WithIdentityPubkey(ctx, idPubKey)
	lightningHandler := handler.NewLightningHandler(s.config)
	return lightningHandler.QueryUserSignedRefunds(ctx, req)
}

func (s *SparkServer) ProvidePreimage(ctx context.Context, req *pb.ProvidePreimageRequest) (*pb.ProvidePreimageResponse, error) {
	idPubKey, err := keys.ParsePublicKey(req.GetIdentityPublicKey())
	if err != nil {
		return nil, fmt.Errorf("failed to parse identity public key: %w", err)
	}
	ctx, _ = logging.WithIdentityPubkey(ctx, idPubKey)
	lightningHandler := handler.NewLightningHandler(s.config)
	return lightningHandler.ProvidePreimage(ctx, req)
}

func (s *SparkServer) ReturnLightningPayment(ctx context.Context, req *pb.ReturnLightningPaymentRequest) (*emptypb.Empty, error) {
	userIDPubKey, err := keys.ParsePublicKey(req.GetUserIdentityPublicKey())
	if err != nil {
		return nil, fmt.Errorf("failed to parse user identity public key: %w", err)
	}
	ctx, _ = logging.WithIdentityPubkey(ctx, userIDPubKey)
	lightningHandler := handler.NewLightningHandler(s.config)
	return lightningHandler.ReturnLightningPayment(ctx, req, false)
}

// StartTokenTransaction reserves revocation keyshares, and fills the revocation commitment (and other SO-derived fields) to create the final token transaction.
func (s *SparkServer) StartTokenTransaction(ctx context.Context, req *pb.StartTokenTransactionRequest) (*pb.StartTokenTransactionResponse, error) {
	idPubKey, err := keys.ParsePublicKey(req.GetIdentityPublicKey())
	if err != nil {
		return nil, fmt.Errorf("failed to parse identity public key: %w", err)
	}
	ctx, _ = logging.WithIdentityPubkey(ctx, idPubKey)
	tokenTransactionHandler := tokens.NewStartTokenTransactionHandler(s.config)

	network, err := common.NetworkFromProtoNetwork(req.PartialTokenTransaction.Network)
	if err != nil {
		return nil, fmt.Errorf("failed to parse network: %w", err)
	}
	startTransaction, err := protoconverter.TokenProtoStartTransactionRequestFromSpark(req, s.config.Lrc20Configs[network.String()].TransactionExpiryDuration)
	if err != nil {
		return nil, fmt.Errorf("failed to convert request into v0: %w", err)
	}

	startTransactionResponse, err := tokenTransactionHandler.StartTokenTransaction(ctx, startTransaction)
	if err != nil {
		return nil, fmt.Errorf("failed to start token transaction: %w", err)
	}

	response, err := protoconverter.SparkStartTokenTransactionResponseFromTokenProto(startTransactionResponse)
	if err != nil {
		return nil, fmt.Errorf("failed to convert request into v0: %w", err)
	}

	return response, nil
}

// QueryNodes queries the details of nodes given either the owner identity public key or a list of node ids.
func (s *SparkServer) QueryNodes(ctx context.Context, req *pb.QueryNodesRequest) (*pb.QueryNodesResponse, error) {
	treeQueryHandler := handler.NewTreeQueryHandler(s.config)
	return treeQueryHandler.QueryNodes(ctx, req, false)
}

// GetTokenTransactionRevocationKeyshares allows the wallet to retrieve the revocation keyshares from each individual SO to
// allow the wallet to combine these shares into the fully resolved revocation secret necessary for transaction finalization.
func (s *SparkServer) SignTokenTransaction(ctx context.Context, req *pb.SignTokenTransactionRequest) (*pb.SignTokenTransactionResponse, error) {
	idPubKey, err := keys.ParsePublicKey(req.GetIdentityPublicKey())
	if err != nil {
		return nil, fmt.Errorf("failed to parse identity public key: %w", err)
	}
	ctx, _ = logging.WithIdentityPubkey(ctx, idPubKey)
	signTokenHandler := tokens.NewSignTokenHandler(s.config)
	return signTokenHandler.SignTokenTransaction(ctx, req)
}

// FinalizeTokenTransaction verifies the revocation secrets constructed by the wallet and passes these keys to the LRC20 Node
// to finalize the transaction. This operation irreversibly spends the inputs associated with the transaction.
func (s *SparkServer) FinalizeTokenTransaction(ctx context.Context, req *pb.FinalizeTokenTransactionRequest) (*emptypb.Empty, error) {
	idPubKey, err := keys.ParsePublicKey(req.GetIdentityPublicKey())
	if err != nil {
		return nil, fmt.Errorf("failed to parse identity public key: %w", err)
	}
	ctx, _ = logging.WithIdentityPubkey(ctx, idPubKey)
	finalizeTokenHandler := tokens.NewFinalizeTokenHandler(s.config)
	return finalizeTokenHandler.FinalizeTokenTransaction(ctx, req)
}

// FreezeTokens prevents transfer of all outputs owned now and in the future by the provided owner public key.
// Unfreeze undos this operation and re-enables transfers.
func (s *SparkServer) FreezeTokens(ctx context.Context, req *pb.FreezeTokensRequest) (*pb.FreezeTokensResponse, error) {
	ownerPubKey, err := keys.ParsePublicKey(req.GetFreezeTokensPayload().GetOwnerPublicKey())
	if err != nil {
		return nil, fmt.Errorf("failed to parse owner public key: %w", err)
	}
	ctx, _ = logging.WithIdentityPubkey(ctx, ownerPubKey)
	tokenReq := protoconverter.TokenProtoFreezeTokensRequestFromSpark(req)
	freezeTokenHandler := tokens.NewFreezeTokenHandler(s.config)

	tokenRes, err := freezeTokenHandler.FreezeTokens(ctx, tokenReq)
	if err != nil {
		return nil, fmt.Errorf("failed to freeze tokens: %w", err)
	}
	return protoconverter.SparkFreezeTokensResponseFromTokenProto(tokenRes), nil
}

// QueryTokenTransactions returns the token transactions currently owned by the provided owner public key.
func (s *SparkServer) QueryTokenTransactions(ctx context.Context, req *pb.QueryTokenTransactionsRequest) (*pb.QueryTokenTransactionsResponse, error) {
	queryTokenHandler := tokens.NewQueryTokenHandler(s.config)
	return queryTokenHandler.QueryTokenTransactions(ctx, req)
}

// QueryTokenOutputs returns the token outputs currently owned by the provided owner public key.
func (s *SparkServer) QueryTokenOutputs(ctx context.Context, req *pb.QueryTokenOutputsRequest) (*pb.QueryTokenOutputsResponse, error) {
	queryTokenHandler := tokens.NewQueryTokenHandler(s.config)
	return queryTokenHandler.QueryTokenOutputs(ctx, req)
}

func (s *SparkServer) QueryAllTransfers(ctx context.Context, req *pb.TransferFilter) (*pb.QueryTransfersResponse, error) {
	transferHander := handler.NewTransferHandler(s.config)
	return transferHander.QueryAllTransfers(ctx, req)
}

func (s *SparkServer) QueryUnusedDepositAddresses(ctx context.Context, req *pb.QueryUnusedDepositAddressesRequest) (*pb.QueryUnusedDepositAddressesResponse, error) {
	idPubKey, err := keys.ParsePublicKey(req.GetIdentityPublicKey())
	if err != nil {
		return nil, fmt.Errorf("failed to parse identity public key: %w", err)
	}
	ctx, _ = logging.WithIdentityPubkey(ctx, idPubKey)
	treeQueryHandler := handler.NewTreeQueryHandler(s.config)
	return treeQueryHandler.QueryUnusedDepositAddresses(ctx, req)
}

func (s *SparkServer) QueryStaticDepositAddresses(ctx context.Context, req *pb.QueryStaticDepositAddressesRequest) (*pb.QueryStaticDepositAddressesResponse, error) {
	idPubKey, err := keys.ParsePublicKey(req.GetIdentityPublicKey())
	if err != nil {
		return nil, fmt.Errorf("failed to parse identity public key: %w", err)
	}
	ctx, _ = logging.WithIdentityPubkey(ctx, idPubKey)
	treeQueryHandler := handler.NewTreeQueryHandler(s.config)
	return treeQueryHandler.QueryStaticDepositAddresses(ctx, req)
}

func (s *SparkServer) QueryBalance(ctx context.Context, req *pb.QueryBalanceRequest) (*pb.QueryBalanceResponse, error) {
	idPubKey, err := keys.ParsePublicKey(req.GetIdentityPublicKey())
	if err != nil {
		return nil, fmt.Errorf("failed to parse identity public key: %w", err)
	}
	ctx, _ = logging.WithIdentityPubkey(ctx, idPubKey)
	treeQueryHandler := handler.NewTreeQueryHandler(s.config)
	return treeQueryHandler.QueryBalance(ctx, req)
}

func (s *SparkServer) SubscribeToEvents(req *pb.SubscribeToEventsRequest, st pb.SparkService_SubscribeToEventsServer) error {
	idPubKey, err := keys.ParsePublicKey(req.IdentityPublicKey)
	if err != nil {
		return fmt.Errorf("invalid identity public key: %w", err)
	}

	if s.eventsRouter == nil {
		return nil
	}

	return s.eventsRouter.SubscribeToEvents(idPubKey, st)
}

// InitiateUtxoSwap swaps a Spark tree node in exchange for a UTXO.
func (s *SparkServer) InitiateUtxoSwap(ctx context.Context, req *pb.InitiateUtxoSwapRequest) (*pb.InitiateUtxoSwapResponse, error) {
	depositHandler := handler.NewDepositHandler(s.config)
	return depositHandler.InitiateUtxoSwap(ctx, s.config, req)
}

func (s *SparkServer) InitiateStaticDepositUtxoRefund(ctx context.Context, req *pb.InitiateStaticDepositUtxoRefundRequest) (*pb.InitiateStaticDepositUtxoRefundResponse, error) {
	depositHandler := handler.NewStaticDepositHandler(s.config)
	return depositHandler.InitiateStaticDepositUtxoRefund(ctx, s.config, req)
}

func (s *SparkServer) ExitSingleNodeTrees(ctx context.Context, req *pb.ExitSingleNodeTreesRequest) (*pb.ExitSingleNodeTreesResponse, error) {
	treeExitHandler := handler.NewTreeExitHandler(s.config)
	return treeExitHandler.ExitSingleNodeTrees(ctx, req)
}

func (s *SparkServer) InvestigateLeaves(ctx context.Context, req *pb.InvestigateLeavesRequest) (*emptypb.Empty, error) {
	transferHandler := handler.NewTransferHandler(s.config)
	return transferHandler.InvestigateLeaves(ctx, req)
}

func (s *SparkServer) QueryNodesDistribution(ctx context.Context, req *pb.QueryNodesDistributionRequest) (*pb.QueryNodesDistributionResponse, error) {
	treeQueryHandler := handler.NewTreeQueryHandler(s.config)
	return treeQueryHandler.QueryNodesDistribution(ctx, req)
}

func (s *SparkServer) QueryNodesByValue(ctx context.Context, req *pb.QueryNodesByValueRequest) (*pb.QueryNodesByValueResponse, error) {
	treeQueryHandler := handler.NewTreeQueryHandler(s.config)
	return treeQueryHandler.QueryNodesByValue(ctx, req)
}

func (s *SparkServer) GetUtxosForAddress(ctx context.Context, req *pb.GetUtxosForAddressRequest) (*pb.GetUtxosForAddressResponse, error) {
	depositHandler := handler.NewDepositHandler(s.config)
	return depositHandler.GetUtxosForAddress(ctx, req)
}

//nolint:all
func (s *SparkServer) QuerySparkInvoices(ctx context.Context, req *pb.QuerySparkInvoicesRequest) (*pb.QuerySparkInvoicesResponse, error) {
	// TODO: (CNT-493) Re-enable invoice functionality once spark address migration is complete
	return nil, sparkerrors.UnimplementedErrorf("not implemented")
	invoiceHandler := handler.NewSparkInvoiceHandler(s.config)
	return invoiceHandler.QuerySparkInvoices(ctx, req)
}
