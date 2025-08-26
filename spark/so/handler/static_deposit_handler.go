package handler

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"

	"github.com/lightsparkdev/spark/common/keys"

	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/logging"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authz"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/utxo"
	"github.com/lightsparkdev/spark/so/ent/utxoswap"
	"github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/helper"
	"github.com/lightsparkdev/spark/so/knobs"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/proto"
)

// The StaticDepositHandler is responsible for handling static deposit related requests.
type StaticDepositHandler struct {
	config *so.Config
}

// NewStaticDepositHandler creates a new StaticDepositHandler.
func NewStaticDepositHandler(config *so.Config) *StaticDepositHandler {
	return &StaticDepositHandler{
		config: config,
	}
}

func (o *StaticDepositHandler) CreateStaticDepositUtxoSwapForAllOperators(ctx context.Context, config *so.Config, request *pbinternal.CreateStaticDepositUtxoSwapRequest) error {
	ctx, span := tracer.Start(ctx, "StaticDepositHandler.CreateStaticDepositUtxoSwapForAllOperators")
	defer span.End()

	logger := logging.GetLoggerFromContext(ctx)

	// Try to complete with other operators first.
	_, err := helper.ExecuteTaskWithAllOperators(ctx, config, &helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}, func(ctx context.Context, operator *so.SigningOperator) (any, error) {
		conn, err := operator.NewOperatorGRPCConnection()
		if err != nil {
			logger.Error("Failed to connect to operator", "operator", operator.Identifier, "error", err)
			return nil, err
		}
		defer conn.Close()

		client := pbinternal.NewSparkInternalServiceClient(conn)
		internalResp, err := client.CreateStaticDepositUtxoSwap(ctx, request)
		if err != nil {
			logger.Error("Failed to execute utxo swap creation task with operator", "operator", operator.Identifier, "error", err)
			return nil, err
		}
		return internalResp, err
	})
	if err != nil {
		return err
	}
	// If other operators return success, we can complete the swap in self.
	internalDepositHandler := NewStaticDepositInternalHandler(config)
	_, err = internalDepositHandler.CreateStaticDepositUtxoSwap(ctx, config, request)
	return err
}

func GenerateRollbackStaticDepositUtxoSwapForUtxoRequest(ctx context.Context, config *so.Config, utxo *pb.UTXO) (*pbinternal.RollbackUtxoSwapRequest, error) {
	logger := logging.GetLoggerFromContext(ctx)
	if utxo == nil {
		return nil, fmt.Errorf("utxo is required")
	}
	if len(utxo.Txid) == 0 {
		return nil, fmt.Errorf("txid is required")
	}
	if utxo.Network == pb.Network_UNSPECIFIED {
		return nil, fmt.Errorf("network is required")
	}
	rollbackUtxoSwapRequestMessageHash, err := CreateUtxoSwapStatement(
		UtxoSwapStatementTypeRollback,
		hex.EncodeToString(utxo.Txid),
		utxo.Vout,
		common.Network(utxo.Network),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create utxo swap statement: %w", err)
	}
	rollbackUtxoSwapRequestSignature := ecdsa.Sign(config.IdentityPrivateKey.ToBTCEC(), rollbackUtxoSwapRequestMessageHash)
	logger.Debug("Rollback utxo swap request signature", "signature", hex.EncodeToString(rollbackUtxoSwapRequestSignature.Serialize()), "txid", hex.EncodeToString(utxo.Txid), "vout", utxo.Vout, "network", common.Network(utxo.Network).String(), "coordinator", config.IdentityPublicKey(), "message_hash", hex.EncodeToString(rollbackUtxoSwapRequestMessageHash))
	return &pbinternal.RollbackUtxoSwapRequest{
		OnChainUtxo:          utxo,
		Signature:            rollbackUtxoSwapRequestSignature.Serialize(),
		CoordinatorPublicKey: config.IdentityPublicKey().Serialize(),
	}, nil
}

// rollbackUtxoSwap attempts to roll back a UTXO swap if an error occurred during creation.
// It logs warnings for rollback failures but doesn't return errors since the original error is more important.
func (o *StaticDepositHandler) rollbackUtxoSwap(ctx context.Context, config *so.Config, utxo *pb.UTXO) {
	logger := logging.GetLoggerFromContext(ctx)

	rollbackRequest, err := GenerateRollbackStaticDepositUtxoSwapForUtxoRequest(ctx, config, utxo)
	if err != nil {
		logger.Error("Failed to create rollback request", "error", err, "txid", hex.EncodeToString(utxo.Txid), "vout", utxo.Vout)
		return
	}

	allSelection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionAll}
	_, err = helper.ExecuteTaskWithAllOperators(ctx, config, &allSelection, func(ctx context.Context, operator *so.SigningOperator) (any, error) {
		conn, err := operator.NewOperatorGRPCConnection()
		if err != nil {
			logger.Error("Failed to connect to operator for rollback utxo swap", "error", err)
			return nil, err
		}
		defer conn.Close()

		client := pbinternal.NewSparkInternalServiceClient(conn)
		internalResp, err := client.RollbackUtxoSwap(ctx, rollbackRequest)
		if err != nil {
			logger.Error("Failed to execute rollback utxo swap task with operator", "operator", operator.Identifier, "error", err, "txid", hex.EncodeToString(rollbackRequest.OnChainUtxo.Txid), "vout", rollbackRequest.OnChainUtxo.Vout)
			return nil, err
		}
		return internalResp, err
	})

	if err != nil {
		logger.Error("Failed to rollback utxo swap", "error", err, "txid", hex.EncodeToString(utxo.Txid), "vout", utxo.Vout)
	} else {
		logger.Info("UTXO swap rollback completed", "txid", hex.EncodeToString(utxo.Txid), "vout", utxo.Vout)
	}
}

func (o *StaticDepositHandler) rollbackUtxoSwaUsingGossip(ctx context.Context, config *so.Config, utxo *pb.UTXO) {
	logger := logging.GetLoggerFromContext(ctx)

	selection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}
	participants, err := selection.OperatorIdentifierList(config)
	if err != nil {
		logger.Error("Failed to get operator list for rollback utxo swap", "error", err, "txid", hex.EncodeToString(utxo.Txid), "vout", utxo.Vout)
		return
	}
	rollbackRequest, err := GenerateRollbackStaticDepositUtxoSwapForUtxoRequest(ctx, config, utxo)
	if err != nil {
		logger.Error("Failed to create rollback request for rollback utxo swap", "error", err, "txid", hex.EncodeToString(utxo.Txid), "vout", utxo.Vout)
		return
	}
	sendGossipHandler := NewSendGossipHandler(config)
	_, err = sendGossipHandler.CreateAndSendGossipMessage(ctx, &pbgossip.GossipMessage{
		Message: &pbgossip.GossipMessage_RollbackUtxoSwap{
			RollbackUtxoSwap: &pbgossip.GossipMessageRollbackUtxoSwap{
				OnChainUtxo:          utxo,
				Signature:            rollbackRequest.Signature,
				CoordinatorPublicKey: rollbackRequest.CoordinatorPublicKey,
			},
		},
	}, participants)
	if err != nil {
		logger.Error("Failed to create and send gossip message for rollback utxo swap", "error", err, "txid", hex.EncodeToString(utxo.Txid), "vout", utxo.Vout)
		return
	}
	logger.Info("UTXO swap rollback with gossip completed", "txid", hex.EncodeToString(utxo.Txid), "vout", utxo.Vout)
}

// InitiateStaticDepositUtxoRefund processes a request to refund a UTXO back to the User.
func (o *StaticDepositHandler) InitiateStaticDepositUtxoRefund(ctx context.Context, config *so.Config, req *pb.InitiateStaticDepositUtxoRefundRequest) (*pb.InitiateStaticDepositUtxoRefundResponse, error) {
	ctx, span := tracer.Start(ctx, "StaticDepositHandler.InitiateStaticDepositUtxoRefund", trace.WithAttributes(
		transferTypeKey.String(string(st.TransferTypeUtxoSwap)),
	))
	defer span.End()

	logger := logging.GetLoggerFromContext(ctx)
	logger.Info("Start InitiateStaticDepositUtxoRefund request for on-chain utxo", "txid", hex.EncodeToString(req.OnChainUtxo.Txid), "vout", req.OnChainUtxo.Vout, "coordinator", config.Identifier)

	// Check if the swap is already completed for the caller
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

	utxoSwap, err := db.UtxoSwap.Query().
		Where(utxoswap.HasUtxoWith(utxo.IDEQ(targetUtxo.ID))).
		Where(utxoswap.StatusIn(st.UtxoSwapStatusCreated, st.UtxoSwapStatusCompleted)).
		First(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return nil, fmt.Errorf("unable to check if utxo swap is already completed: %w", err)
	}
	if utxoSwap != nil {
		// Once a static deposit has been refunded it can no longer be used in a
		// swap and must be claimed on L1. The owner can sign multiple refund
		// transactions after this point.
		depositAddress, err := targetUtxo.QueryDepositAddress().Only(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get deposit address: %w", err)
		}
		if utxoSwap.Status == st.UtxoSwapStatusCompleted && utxoSwap.RequestType == st.UtxoSwapRequestTypeRefund && bytes.Equal(utxoSwap.UserIdentityPublicKey, depositAddress.OwnerIdentityPubkey) {
			userIDPubKey, err := keys.ParsePublicKey(utxoSwap.UserIdentityPublicKey)
			if err != nil {
				return nil, fmt.Errorf("invalid identity public key: %w", err)
			}
			if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, config, userIDPubKey); err != nil {
				return nil, fmt.Errorf("utxo swap is already completed by another user")
			}
			spendTxSigningResult, depositAddressQueryResult, err := GetSpendTxSigningResult(ctx, config, req.OnChainUtxo, req.RefundTxSigningJob)
			if err != nil {
				return nil, fmt.Errorf("failed to get spend tx signing result: %w", err)
			}

			return &pb.InitiateStaticDepositUtxoRefundResponse{
				RefundTxSigningResult: spendTxSigningResult,
				DepositAddress:        depositAddressQueryResult,
			}, nil
		}
		logger.Info("utxo swap is already registered", "txid", hex.EncodeToString(req.OnChainUtxo.Txid), "vout", req.OnChainUtxo.Vout, "request_type", utxoSwap.RequestType)
		return nil, errors.AlreadyExistsErrorf("utxo swap is already registered")
	}

	// **********************************************************************************************
	// Create a swap record in all SEs so they can not be called concurrently to spend the same utxo.
	// This will validate the swap request and store it in the database with status CREATED,
	// blocking any other swap requests. If this step fails, the caller will receive an error and
	// the swap will be cancelled.
	// **********************************************************************************************
	if err := o.createStaticDepositUtxoRefundWithRollback(ctx, config, req); err != nil {
		return nil, fmt.Errorf("failed to create utxo swap: %w", err)
	}

	utxoSwap, err = db.UtxoSwap.Query().
		Where(utxoswap.HasUtxoWith(utxo.IDEQ(targetUtxo.ID))).
		Where(utxoswap.StatusIn(st.UtxoSwapStatusCreated, st.UtxoSwapStatusCompleted)).
		First(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get utxo swap: %w", err)
	}

	// **********************************************************************************************
	// Signing the spend transactions.
	// **********************************************************************************************
	spendTxSigningResult, depositAddressQueryResult, err := GetSpendTxSigningResult(ctx, config, req.OnChainUtxo, req.RefundTxSigningJob)
	if err != nil {
		return nil, fmt.Errorf("failed to get spend tx signing result: %w", err)
	}
	spendTxSigningResultBytes, err := proto.Marshal(spendTxSigningResult)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal spend tx signing result: %w", err)
	}
	_, err = db.UtxoSwap.UpdateOne(utxoSwap).SetSpendTxSigningResult(spendTxSigningResultBytes).Save(ctx)
	if err != nil {
		logger.Warn("failed to update utxo swap", "error", err, "txid", hex.EncodeToString(req.OnChainUtxo.Txid), "vout", req.OnChainUtxo.Vout)
	}

	// **********************************************************************************************
	// Mark the utxo swap as completed.
	// At this point the swap is considered successful. We will not return an error if this step fails.
	// The user can retry calling this API to get the signed spend transaction.
	// **********************************************************************************************
	completedUtxoSwapRequest, err := CreateCompleteSwapForUtxoRequest(config, req.OnChainUtxo)
	if err != nil {
		logger.Warn("Failed to get complete swap for utxo request, cron task to retry", "error", err)
	} else {
		internalDepositHandler := NewInternalDepositHandler(config)
		if err := internalDepositHandler.CompleteSwapForAllOperators(ctx, config, completedUtxoSwapRequest); err != nil {
			logger.Warn("Failed to mark a utxo swap as completed in all operators, cron task to retry", "error", err)
		}
	}

	return &pb.InitiateStaticDepositUtxoRefundResponse{
		RefundTxSigningResult: spendTxSigningResult,
		DepositAddress:        depositAddressQueryResult,
	}, nil
}

// createUtxoSwapRefundWithRollback creates a UTXO swap refund and handles rollback on failure.
func (o *StaticDepositHandler) createStaticDepositUtxoRefundWithRollback(ctx context.Context, config *so.Config, req *pb.InitiateStaticDepositUtxoRefundRequest) error {
	logger := logging.GetLoggerFromContext(ctx)

	createRequest, err := GenerateCreateStaticDepositUtxoRefundRequest(ctx, config, req)
	if err != nil {
		logger.Warn("Failed to create utxo swap request, cron task to retry", "error", err)
		return err
	}

	if err := o.CreateSwapRefundForAllOperators(ctx, config, createRequest); err != nil {
		logger.Info("Failed to create utxo swap with all operators, rolling back", "error", err, "txid", hex.EncodeToString(req.OnChainUtxo.Txid), "vout", req.OnChainUtxo.Vout)
		if k := knobs.GetKnobsService(ctx); k != nil && k.GetValue(knobs.KnobSoRollbackUtxoSwapUsingGossip, 0) > 0 {
			o.rollbackUtxoSwaUsingGossip(ctx, config, req.OnChainUtxo)
		} else {
			o.rollbackUtxoSwap(ctx, config, req.OnChainUtxo)
		}
		return err
	}

	logger.Info("Created utxo swap", "txid", hex.EncodeToString(req.OnChainUtxo.Txid), "vout", req.OnChainUtxo.Vout)
	return nil
}

func GenerateCreateStaticDepositUtxoRefundRequest(ctx context.Context, config *so.Config, req *pb.InitiateStaticDepositUtxoRefundRequest) (*pbinternal.CreateStaticDepositUtxoRefundRequest, error) {
	createUtxoSwapRequestMessageHash, err := CreateUtxoSwapStatement(
		UtxoSwapStatementTypeCreated,
		hex.EncodeToString(req.OnChainUtxo.Txid),
		req.OnChainUtxo.Vout,
		common.Network(req.OnChainUtxo.Network),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create utxo swap statement: %w", err)
	}
	createUtxoSwapRequestSignature := ecdsa.Sign(config.IdentityPrivateKey.ToBTCEC(), createUtxoSwapRequestMessageHash)

	return &pbinternal.CreateStaticDepositUtxoRefundRequest{
		Request:              req,
		Signature:            createUtxoSwapRequestSignature.Serialize(),
		CoordinatorPublicKey: config.IdentityPublicKey().Serialize(),
	}, nil
}

func CreateUtxoSwapRefundWithOtherOperators(ctx context.Context, config *so.Config, request *pbinternal.CreateStaticDepositUtxoRefundRequest) error {
	logger := logging.GetLoggerFromContext(ctx)

	_, err := helper.ExecuteTaskWithAllOperators(ctx, config, &helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}, func(ctx context.Context, operator *so.SigningOperator) (any, error) {
		conn, err := operator.NewOperatorGRPCConnection()
		if err != nil {
			logger.Error("Failed to connect to operator", "operator", operator.Identifier, "error", err)
			return nil, err
		}
		defer conn.Close()

		client := pbinternal.NewSparkInternalServiceClient(conn)
		internalResp, err := client.CreateStaticDepositUtxoRefund(ctx, request)
		if err != nil {
			logger.Error("Failed to execute utxo swap completed task with operator", "operator", operator.Identifier, "error", err)
			return nil, err
		}
		return internalResp, err
	})
	return err
}

func (o *StaticDepositHandler) CreateSwapRefundForAllOperators(ctx context.Context, config *so.Config, request *pbinternal.CreateStaticDepositUtxoRefundRequest) error {
	ctx, span := tracer.Start(ctx, "StaticDepositHandler.CreateSwapRefundForAllOperators")
	defer span.End()

	// Try to complete with other operators first.
	if err := CreateUtxoSwapRefundWithOtherOperators(ctx, config, request); err != nil {
		return err
	}
	// If other operators return success, we can complete the swap in self.
	internalDepositHandler := NewStaticDepositInternalHandler(config)
	_, err := internalDepositHandler.CreateStaticDepositUtxoRefund(ctx, config, request)
	return err
}
