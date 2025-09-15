package handler

import (
	"context"
	"encoding/hex"
	"fmt"

	"github.com/lightsparkdev/spark/common/keys"
	"go.uber.org/zap"

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
	"github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/helper"
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/lightsparkdev/spark/so/staticdeposit"
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
	_, err := helper.ExecuteTaskWithAllOperators(ctx, config, &helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}, func(ctx context.Context, operator *so.SigningOperator) (*pbinternal.CreateStaticDepositUtxoSwapResponse, error) {
		conn, err := operator.NewOperatorGRPCConnection()
		if err != nil {
			logger.With(zap.Error(err)).Sugar().Errorf("Failed to connect to operator %s", operator.Identifier)
			return nil, err
		}
		defer conn.Close()

		client := pbinternal.NewSparkInternalServiceClient(conn)
		internalResp, err := client.CreateStaticDepositUtxoSwap(ctx, request)
		if err != nil {
			logger.With(zap.Error(err)).Sugar().Errorf(
				"Failed to execute utxo swap creation task with operator %s",
				operator.Identifier,
			)
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
	logger.Sugar().Debugf(
		"Rollback utxo swap request signature (signature %x, txid %x, vout %d, network %s, coordinator %x, message: %x)",
		rollbackUtxoSwapRequestSignature.Serialize(),
		utxo.Txid,
		utxo.Vout,
		common.Network(utxo.Network).String(),
		config.IdentityPublicKey(),
		rollbackUtxoSwapRequestMessageHash,
	)
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
		logger.With(zap.Error(err)).Sugar().Errorf("Failed to create rollback request for %x:%d", utxo.Txid, utxo.Vout)
		return
	}

	allSelection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionAll}
	_, err = helper.ExecuteTaskWithAllOperators(ctx, config, &allSelection, func(ctx context.Context, operator *so.SigningOperator) (*pbinternal.RollbackUtxoSwapResponse, error) {
		conn, err := operator.NewOperatorGRPCConnection()
		if err != nil {
			logger.Error("Failed to connect to operator for rollback utxo swap", zap.Error(err))
			return nil, err
		}
		defer conn.Close()

		client := pbinternal.NewSparkInternalServiceClient(conn)
		internalResp, err := client.RollbackUtxoSwap(ctx, rollbackRequest)
		if err != nil {
			logger.Sugar().Errorf(
				"Failed to execute rollback utxo swap for %x:%d task with operator %s",
				operator.Identifier,
				rollbackRequest.OnChainUtxo.Txid,
				rollbackRequest.OnChainUtxo.Vout,
			)
			return nil, err
		}
		return internalResp, err
	})

	if err != nil {
		logger.With(zap.Error(err)).Sugar().Errorf("Failed to rollback utxo swap for %x:%d", utxo.Txid, utxo.Vout)
	} else {
		logger.Sugar().Infof("UTXO swap rollback completed for %x:%d", utxo.Txid, utxo.Vout)
	}
}

func (o *StaticDepositHandler) rollbackUtxoSwaUsingGossip(ctx context.Context, config *so.Config, utxo *pb.UTXO) {
	logger := logging.GetLoggerFromContext(ctx)

	selection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}
	participants, err := selection.OperatorIdentifierList(config)
	if err != nil {
		logger.With(zap.Error(err)).Sugar().Errorf("Failed to get operator list for rollback utxo swap %x:%d", utxo.Txid, utxo.Vout)
		return
	}
	rollbackRequest, err := GenerateRollbackStaticDepositUtxoSwapForUtxoRequest(ctx, config, utxo)
	if err != nil {
		logger.With(zap.Error(err)).Sugar().Errorf("Failed to create rollback request for rollback utxo swap %x:%d", utxo.Txid, utxo.Vout)
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
		logger.With(zap.Error(err)).Sugar().Errorf("Failed to create and send gossip message for rollback utxo swap %x:%d", utxo.Txid, utxo.Vout)
		return
	}
	logger.Sugar().Infof("UTXO swap rollback for %x:%d with gossip completed", utxo.Txid, utxo.Vout)
}

// InitiateStaticDepositUtxoRefund processes a request to refund a UTXO back to the User.
func (o *StaticDepositHandler) InitiateStaticDepositUtxoRefund(ctx context.Context, config *so.Config, req *pb.InitiateStaticDepositUtxoRefundRequest) (*pb.InitiateStaticDepositUtxoRefundResponse, error) {
	ctx, span := tracer.Start(ctx, "StaticDepositHandler.InitiateStaticDepositUtxoRefund", trace.WithAttributes(
		transferTypeKey.String(string(st.TransferTypeUtxoSwap)),
	))
	defer span.End()

	logger := logging.GetLoggerFromContext(ctx)
	logger.Sugar().Infof("Start InitiateStaticDepositUtxoRefund request for on-chain utxo %x:%d with coordinator %s", req.OnChainUtxo.Txid, req.OnChainUtxo.Vout, config.Identifier)

	// Check if the swap is already completed for the caller
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get db: %w", err)
	}
	schemaNetwork, err := common.SchemaNetworkFromProtoNetwork(req.OnChainUtxo.Network)
	if err != nil {
		return nil, err
	}
	// Validate the on-chain UTXO
	onChainUtxoTxId, err := NewValidatedTxID(req.OnChainUtxo.Txid)
	if err != nil {
		return nil, fmt.Errorf("failed to validate on-chain UTXO txid: %w", err)
	}
	targetUtxo, err := VerifiedTargetUtxo(ctx, config, db, schemaNetwork, onChainUtxoTxId, req.OnChainUtxo.Vout)
	if err != nil {
		return nil, err
	}

	utxoSwap, err := staticdeposit.GetRegisteredUtxoSwapForUtxo(ctx, db, targetUtxo)
	if err != nil {
		return nil, err
	}
	if utxoSwap != nil {
		// Once a static deposit has been refunded it can no longer be used in a
		// swap and must be claimed on L1. The owner can sign multiple refund
		// transactions after this point.
		depositAddress, err := targetUtxo.QueryDepositAddress().Only(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get deposit address: %w", err)
		}
		userIDPubKey, err := keys.ParsePublicKey(utxoSwap.UserIdentityPublicKey)
		if err != nil {
			return nil, fmt.Errorf("invalid identity public key: %w", err)
		}

		if utxoSwap.Status == st.UtxoSwapStatusCompleted && utxoSwap.RequestType == st.UtxoSwapRequestTypeRefund && userIDPubKey.Equals(depositAddress.OwnerIdentityPubkey) {
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
		logger.Sugar().Infof("utxo swap %x:%d is already registered (request type %s)", req.OnChainUtxo.Txid, req.OnChainUtxo.Vout, utxoSwap.RequestType)
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

	utxoSwap, err = staticdeposit.GetRegisteredUtxoSwapForUtxo(ctx, db, targetUtxo)
	if err != nil || utxoSwap == nil {
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
		logger.With(zap.Error(err)).Sugar().Warnf("Failed to update utxo swap for %x:%d", req.OnChainUtxo.Txid, req.OnChainUtxo.Vout)
		return nil, fmt.Errorf("failed to update utxo swap with spend tx signing result: %w", err)
	}

	// **********************************************************************************************
	// Mark the utxo swap as completed.
	// At this point the swap is considered successful. We will not return an error if this step fails.
	// The user can retry calling this API to get the signed spend transaction.
	// **********************************************************************************************
	completedUtxoSwapRequest, err := CreateCompleteSwapForUtxoRequest(config, req.OnChainUtxo)
	if err != nil {
		logger.Warn("Failed to get complete swap for utxo request, cron task to retry", zap.Error(err))
	} else {
		internalDepositHandler := NewInternalDepositHandler(config)
		if err := internalDepositHandler.CompleteSwapForAllOperators(ctx, config, completedUtxoSwapRequest); err != nil {
			logger.Warn("Failed to mark a utxo swap as completed in all operators, cron task to retry", zap.Error(err))
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
		logger.Warn("Failed to create utxo swap request, cron task to retry", zap.Error(err))
		return err
	}

	if err := o.CreateSwapRefundForAllOperators(ctx, config, createRequest); err != nil {
		logger.With(zap.Error(err)).Sugar().Infof(
			"Failed to create utxo swap %x:%d with all operators, rolling back",
			req.OnChainUtxo.Txid,
			req.OnChainUtxo.Vout,
		)
		if k := knobs.GetKnobsService(ctx); k != nil && k.GetValue(knobs.KnobSoRollbackUtxoSwapUsingGossip, 0) > 0 {
			o.rollbackUtxoSwaUsingGossip(ctx, config, req.OnChainUtxo)
		} else {
			o.rollbackUtxoSwap(ctx, config, req.OnChainUtxo)
		}
		return err
	}

	logger.Sugar().Infof("Created utxo swap %x:%d", req.OnChainUtxo.Txid, req.OnChainUtxo.Vout)
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

	_, err := helper.ExecuteTaskWithAllOperators(ctx, config, &helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}, func(ctx context.Context, operator *so.SigningOperator) (*pbinternal.CreateStaticDepositUtxoRefundResponse, error) {
		conn, err := operator.NewOperatorGRPCConnection()
		if err != nil {
			logger.With(zap.Error(err)).Sugar().Errorf("Failed to connect to operator %s", operator.Identifier)
			return nil, err
		}
		defer conn.Close()

		client := pbinternal.NewSparkInternalServiceClient(conn)
		internalResp, err := client.CreateStaticDepositUtxoRefund(ctx, request)
		if err != nil {
			logger.With(zap.Error(err)).Sugar().Errorf("Failed to execute utxo swap completed task with operator %s", operator.Identifier)
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
