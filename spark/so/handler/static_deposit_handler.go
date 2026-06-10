package handler

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"math"

	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/sighash"
	"go.uber.org/zap"

	"github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
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
	"github.com/lightsparkdev/spark/so/staticdeposit"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/proto"
)

// The StaticDepositHandler is responsible for handling static deposit related requests.
type StaticDepositHandler struct {
	config *so.Config
}

func schemaToProtoUtxoSwapStatus(s st.UtxoSwapStatus) pb.UtxoSwapStatus {
	switch s {
	case st.UtxoSwapStatusCreated:
		return pb.UtxoSwapStatus_UTXO_SWAP_STATUS_CREATED
	case st.UtxoSwapStatusCompleted:
		return pb.UtxoSwapStatus_UTXO_SWAP_STATUS_COMPLETED
	case st.UtxoSwapStatusCancelled:
		return pb.UtxoSwapStatus_UTXO_SWAP_STATUS_CANCELLED
	default:
		return pb.UtxoSwapStatus_UTXO_SWAP_STATUS_UNSPECIFIED
	}
}

func schemaToProtoUtxoSwapStatuses(statuses []st.UtxoSwapStatus) []pb.UtxoSwapStatus {
	result := make([]pb.UtxoSwapStatus, len(statuses))
	for i, s := range statuses {
		result[i] = schemaToProtoUtxoSwapStatus(s)
	}
	return result
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

// GenerateRollbackStaticDepositUtxoSwapForUtxoRequest builds a signed
// RollbackUtxoSwapRequest. confirmationThreshold is propagated to the
// receiving operator so its UTXO re-verification matches the threshold the
// swap was originally created with; nil falls back to receiver-side defaults.
func GenerateRollbackStaticDepositUtxoSwapForUtxoRequest(ctx context.Context, config *so.Config, utxo *pb.UTXO, confirmationThreshold *uint32) (*pbinternal.RollbackUtxoSwapRequest, error) {
	logger := logging.GetLoggerFromContext(ctx)
	if utxo == nil {
		return nil, fmt.Errorf("utxo is required")
	}
	if len(utxo.GetTxid()) == 0 {
		return nil, fmt.Errorf("txid is required")
	}
	network, err := btcnetwork.FromProtoNetwork(utxo.GetNetwork())
	if err != nil {
		return nil, fmt.Errorf("network is required")
	}

	rollbackUtxoSwapRequestMessageHash, err := CreateUtxoSwapStatement(
		UtxoSwapStatementTypeRollback,
		hex.EncodeToString(utxo.GetTxid()),
		utxo.GetVout(),
		network,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create utxo swap statement: %w", err)
	}
	rollbackUtxoSwapRequestSignature := ecdsa.Sign(config.IdentityPrivateKey.ToBTCEC(), rollbackUtxoSwapRequestMessageHash)
	logger.Sugar().Debugf(
		"Rollback utxo swap request signature (signature %x, txid %x, vout %d, network %s, coordinator %x, message: %x)",
		rollbackUtxoSwapRequestSignature.Serialize(),
		utxo.GetTxid(),
		utxo.GetVout(),
		network,
		config.IdentityPublicKey(),
		rollbackUtxoSwapRequestMessageHash,
	)
	return &pbinternal.RollbackUtxoSwapRequest{
		OnChainUtxo:           utxo,
		Signature:             rollbackUtxoSwapRequestSignature.Serialize(),
		CoordinatorPublicKey:  config.IdentityPublicKey().Serialize(),
		ConfirmationThreshold: confirmationThreshold,
	}, nil
}

func (o *StaticDepositHandler) rollbackUtxoSwapUsingGossip(ctx context.Context, config *so.Config, utxo *pb.UTXO, confirmationThreshold *uint32) {
	logger := logging.GetLoggerFromContext(ctx)

	selection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}
	participants, err := selection.OperatorIdentifierList(config)
	if err != nil {
		logger.With(zap.Error(err)).Sugar().Errorf("Failed to get operator list for rollback utxo swap %x:%d", utxo.GetTxid(), utxo.GetVout())
		return
	}
	rollbackRequest, err := GenerateRollbackStaticDepositUtxoSwapForUtxoRequest(ctx, config, utxo, confirmationThreshold)
	if err != nil {
		logger.With(zap.Error(err)).Sugar().Errorf("Failed to create rollback request for rollback utxo swap %x:%d", utxo.GetTxid(), utxo.GetVout())
		return
	}
	sendGossipHandler := NewSendGossipHandler(config)
	_, err = sendGossipHandler.CreateAndSendGossipMessage(ctx, &pbgossip.GossipMessage{
		Message: &pbgossip.GossipMessage_RollbackUtxoSwap{
			RollbackUtxoSwap: &pbgossip.GossipMessageRollbackUtxoSwap{
				OnChainUtxo:           utxo,
				Signature:             rollbackRequest.GetSignature(),
				CoordinatorPublicKey:  rollbackRequest.GetCoordinatorPublicKey(),
				ConfirmationThreshold: confirmationThreshold,
			},
		},
	}, participants)
	if err != nil {
		logger.With(zap.Error(err)).Sugar().Errorf("Failed to create and send gossip message for rollback utxo swap %x:%d", utxo.GetTxid(), utxo.GetVout())
		return
	}
	logger.Sugar().Infof("UTXO swap rollback for %x:%d with gossip completed", utxo.GetTxid(), utxo.GetVout())
}

func (o *StaticDepositHandler) CreateInstantStaticDepositUtxoSwapForAllOperators(ctx context.Context, config *so.Config, request *pbinternal.CreateInstantStaticDepositUtxoSwapRequest) error {
	ctx, span := tracer.Start(ctx, "StaticDepositHandler.CreateInstantStaticDepositUtxoSwapForAllOperators")
	defer span.End()

	logger := logging.GetLoggerFromContext(ctx)

	// Try to complete with other operators first.
	_, err := helper.ExecuteTaskWithAllOperators(ctx, config, &helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}, func(ctx context.Context, operator *so.SigningOperator) (*pbinternal.CreateInstantStaticDepositUtxoSwapResponse, error) {
		conn, err := operator.NewOperatorGRPCConnection()
		if err != nil {
			logger.With(zap.Error(err)).Sugar().Errorf("Failed to connect to operator %s", operator.Identifier)
			return nil, err
		}
		defer conn.Close()

		client := pbinternal.NewSparkInternalServiceClient(conn)
		internalResp, err := client.CreateInstantStaticDepositUtxoSwap(ctx, request)
		if err != nil {
			logger.With(zap.Error(err)).Sugar().Errorf(
				"Failed to execute instant utxo swap creation task with operator %s",
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
	_, err = internalDepositHandler.CreateInstantStaticDepositUtxoSwap(ctx, config, request)
	return err
}

func (o *StaticDepositHandler) SaveUtxoForInstantStaticDepositForAllOperators(ctx context.Context, config *so.Config, request *pbinternal.SaveUtxoForInstantStaticDepositRequest) error {
	ctx, span := tracer.Start(ctx, "StaticDepositHandler.SaveUtxoForInstantStaticDepositForAllOperators")
	defer span.End()

	logger := logging.GetLoggerFromContext(ctx)

	_, err := helper.ExecuteTaskWithAllOperators(ctx, config, &helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}, func(ctx context.Context, operator *so.SigningOperator) (*pbinternal.SaveUtxoForInstantStaticDepositResponse, error) {
		conn, err := operator.NewOperatorGRPCConnection()
		if err != nil {
			logger.With(zap.Error(err)).Sugar().Errorf("Failed to connect to operator %s", operator.Identifier)
			return nil, err
		}
		defer conn.Close()

		client := pbinternal.NewSparkInternalServiceClient(conn)
		internalResp, err := client.SaveUtxoForInstantStaticDeposit(ctx, request)
		if err != nil {
			logger.With(zap.Error(err)).Sugar().Warnf(
				"Failed to save utxo for instant static deposit with operator %s (will retry via SSP)",
				operator.Identifier,
			)
			return nil, err
		}
		return internalResp, err
	})
	if err != nil {
		return err
	}
	internalDepositHandler := NewStaticDepositInternalHandler(config)
	_, err = internalDepositHandler.SaveUtxoForInstantStaticDeposit(ctx, config, request)
	return err
}

// LinkUtxoSwapTransferForOtherOperators links the transfer edge to a utxo swap on non-coordinator SOs.
// The coordinator already linked the edge in initiateUtxoSwapTransfer (ssp_request_handler.go:1484-1492).
func (o *StaticDepositHandler) LinkUtxoSwapTransferForOtherOperators(ctx context.Context, config *so.Config, request *pbinternal.LinkUtxoSwapTransferRequest) error {
	ctx, span := tracer.Start(ctx, "StaticDepositHandler.LinkUtxoSwapTransferForOtherOperators")
	defer span.End()

	logger := logging.GetLoggerFromContext(ctx)

	_, err := helper.ExecuteTaskWithAllOperators(ctx, config, &helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}, func(ctx context.Context, operator *so.SigningOperator) (*pbinternal.LinkUtxoSwapTransferResponse, error) {
		conn, err := operator.NewOperatorGRPCConnection()
		if err != nil {
			logger.With(zap.Error(err)).Sugar().Errorf("Failed to connect to operator %s", operator.Identifier)
			return nil, err
		}
		defer conn.Close()

		client := pbinternal.NewSparkInternalServiceClient(conn)
		internalResp, err := client.LinkUtxoSwapTransfer(ctx, request)
		if err != nil {
			logger.With(zap.Error(err)).Sugar().Errorf("Failed to link utxo swap transfer with operator %s", operator.Identifier)
			return nil, err
		}
		return internalResp, err
	})
	return err
}

func (o *StaticDepositHandler) rollbackInstantStaticDepositUtxoSwapUsingGossip(ctx context.Context, config *so.Config, utxo *pb.UTXO, rollbackFromStatus []st.UtxoSwapStatus, rollbackToStatus st.UtxoSwapStatus) {
	logger := logging.GetLoggerFromContext(ctx)

	selection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}
	participants, err := selection.OperatorIdentifierList(config)
	if err != nil {
		logger.With(zap.Error(err)).Sugar().Errorf("Failed to get operator list for rollback instant utxo swap %x:%d", utxo.GetTxid(), utxo.GetVout())
		return
	}
	// RollbackInstantUtxoSwap on the receiver doesn't re-verify confirmations,
	// so the threshold is unused here.
	rollbackRequest, err := GenerateRollbackStaticDepositUtxoSwapForUtxoRequest(ctx, config, utxo, nil)
	if err != nil {
		logger.With(zap.Error(err)).Sugar().Errorf("Failed to create rollback request for rollback utxo swap %x:%d", utxo.GetTxid(), utxo.GetVout())
		return
	}
	sendGossipHandler := NewSendGossipHandler(config)
	_, err = sendGossipHandler.CreateAndSendGossipMessage(ctx, &pbgossip.GossipMessage{
		Message: &pbgossip.GossipMessage_RollbackInstantUtxoSwap{
			RollbackInstantUtxoSwap: &pbgossip.GossipMessageRollbackInstantUtxoSwap{
				OnChainUtxo:          utxo,
				Signature:            rollbackRequest.GetSignature(),
				CoordinatorPublicKey: rollbackRequest.GetCoordinatorPublicKey(),
				RollbackFromStatuses: schemaToProtoUtxoSwapStatuses(rollbackFromStatus),
				RollbackToStatus:     schemaToProtoUtxoSwapStatus(rollbackToStatus),
			},
		},
	}, participants)
	if err != nil {
		logger.With(zap.Error(err)).Sugar().Errorf("Failed to create and send gossip message for rollback utxo swap %x:%d", utxo.GetTxid(), utxo.GetVout())
		return
	}
	logger.Sugar().Infof("UTXO swap rollback for %x:%d with gossip completed", utxo.GetTxid(), utxo.GetVout())
}

// InitiateStaticDepositUtxoRefund processes a request to refund a UTXO back to the User.
func (o *StaticDepositHandler) InitiateStaticDepositUtxoRefund(ctx context.Context, config *so.Config, req *pb.InitiateStaticDepositUtxoRefundRequest) (*pb.InitiateStaticDepositUtxoRefundResponse, error) {
	ctx, span := tracer.Start(ctx, "StaticDepositHandler.InitiateStaticDepositUtxoRefund", trace.WithAttributes(
		transferTypeKey.String(string(st.TransferTypeUtxoSwap)),
	))
	defer span.End()

	logger := logging.GetLoggerFromContext(ctx)

	if req == nil {
		return nil, errors.InvalidArgumentMissingField(fmt.Errorf("request is required"))
	}
	if req.GetOnChainUtxo() == nil {
		return nil, errors.InvalidArgumentMissingField(fmt.Errorf("on_chain_utxo is required"))
	}

	logger.Sugar().Infof("Start InitiateStaticDepositUtxoRefund request for on-chain utxo %x:%d with coordinator %s", req.GetOnChainUtxo().GetTxid(), req.GetOnChainUtxo().GetVout(), config.Identifier)

	// Check if the swap is already completed for the caller
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get db: %w", err)
	}
	schemaNetwork, err := btcnetwork.FromProtoNetwork(req.GetOnChainUtxo().GetNetwork())
	if err != nil {
		return nil, err
	}
	// Validate the on-chain UTXO

	targetUtxo, err := VerifiedTargetUtxoFromRequest(ctx, config, db, schemaNetwork, req.GetOnChainUtxo(), nil)
	if err != nil {
		return nil, err
	}

	// Validate that the refund transaction actually spends the requested UTXO.
	// Also validated in CreateStaticDepositUtxoRefund in each SO.
	if err := validateStaticDepositRefundTx(targetUtxo, req.GetRefundTxSigningJob().GetRawTx()); err != nil {
		return nil, err
	}

	utxoSwap, err := staticdeposit.GetRegisteredUtxoSwapForUtxo(ctx, db, targetUtxo.inner)
	if err != nil {
		return nil, err
	}
	if utxoSwap != nil {
		// Once a static deposit has been refunded it can no longer be used in a
		// swap and must be claimed on L1. The owner can sign multiple refund
		// transactions after this point.
		depositAddress, err := targetUtxo.inner.QueryDepositAddress().Only(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get deposit address: %w", err)
		}
		userIDPubKey := utxoSwap.UserIdentityPublicKey

		if utxoSwap.Status == st.UtxoSwapStatusCompleted && utxoSwap.RequestType == st.UtxoSwapRequestTypeRefund && userIDPubKey.Equals(depositAddress.OwnerIdentityPubkey) {
			if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, config, userIDPubKey); err != nil {
				return nil, fmt.Errorf("utxo swap is already completed by another user")
			}
			if err := authz.EnforceWalletNotKillSwitched(ctx, userIDPubKey); err != nil {
				return nil, err
			}
			spendTxSighash, totalAmount, err := GetTxSigningInfo(ctx, targetUtxo.inner, req.GetRefundTxSigningJob().GetRawTx())
			if err != nil {
				return nil, fmt.Errorf("failed to get spend tx sighash: %w", err)
			}
			// Refund retries may use a different transaction, for example to adjust
			// fees, but each distinct transaction still needs a fresh user
			// authorization because the sighash is part of the signed statement.
			if err := validateUserSignature(depositAddress.OwnerIdentityPubkey, req.GetUserSignature(), spendTxSighash.Serialize(), pb.UtxoSwapRequestType_Refund, schemaNetwork, targetUtxo.Hash().String(), targetUtxo.Vout(), totalAmount, req.GetHashVariant()); err != nil {
				return nil, fmt.Errorf("user signature validation failed: %w", err)
			}
			spendTxSigningResult, depositAddressQueryResult, err := getSpendTxSigningResultForVerifiedTargetUtxo(ctx, config, targetUtxo, req.GetRefundTxSigningJob())
			if err != nil {
				return nil, fmt.Errorf("failed to get spend tx signing result: %w", err)
			}

			return &pb.InitiateStaticDepositUtxoRefundResponse{
				RefundTxSigningResult: spendTxSigningResult,
				DepositAddress:        depositAddressQueryResult,
			}, nil
		}
		logger.Sugar().Infof("utxo swap %x:%d is already registered (request type %s)", req.GetOnChainUtxo().GetTxid(), req.GetOnChainUtxo().GetVout(), utxoSwap.RequestType)
		return nil, errors.AlreadyExistsDuplicateOperation(fmt.Errorf("utxo swap is already registered"))
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

	utxoSwap, err = staticdeposit.GetRegisteredUtxoSwapForUtxo(ctx, db, targetUtxo.inner)
	if err != nil || utxoSwap == nil {
		return nil, fmt.Errorf("unable to get utxo swap: %w", err)
	}

	// **********************************************************************************************
	// Signing the spend transactions.
	// **********************************************************************************************
	spendTxSigningResult, depositAddressQueryResult, err := getSpendTxSigningResultForVerifiedTargetUtxo(ctx, config, targetUtxo, req.GetRefundTxSigningJob())
	if err != nil {
		return nil, fmt.Errorf("failed to get spend tx signing result: %w", err)
	}
	spendTxSigningResultBytes, err := proto.Marshal(spendTxSigningResult)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal spend tx signing result: %w", err)
	}
	_, err = db.UtxoSwap.UpdateOne(utxoSwap).SetSpendTxSigningResult(spendTxSigningResultBytes).Save(ctx)
	if err != nil {
		logger.With(zap.Error(err)).Sugar().Warnf("Failed to update utxo swap for %x:%d", req.GetOnChainUtxo().GetTxid(), req.GetOnChainUtxo().GetVout())
		return nil, fmt.Errorf("failed to update utxo swap with spend tx signing result: %w", err)
	}

	// **********************************************************************************************
	// Mark the utxo swap as completed.
	// At this point the swap is considered successful. We will not return an error if this step fails.
	// The user can retry calling this API to get the signed spend transaction.
	// **********************************************************************************************
	// Refund flow uses the network-default threshold; no custom threshold to forward.
	completedUtxoSwapRequest, err := CreateCompleteSwapForUtxoRequest(config, req.GetOnChainUtxo(), nil)
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
			req.GetOnChainUtxo().GetTxid(),
			req.GetOnChainUtxo().GetVout(),
		)
		// Refund flow uses the network-default threshold; no custom threshold to forward.
		o.rollbackUtxoSwapUsingGossip(ctx, config, req.GetOnChainUtxo(), nil)
		return err
	}

	logger.Sugar().Infof("Created utxo swap %x:%d", req.GetOnChainUtxo().GetTxid(), req.GetOnChainUtxo().GetVout())
	return nil
}

func GenerateCreateStaticDepositUtxoRefundRequest(ctx context.Context, config *so.Config, req *pb.InitiateStaticDepositUtxoRefundRequest) (*pbinternal.CreateStaticDepositUtxoRefundRequest, error) {
	network, err := btcnetwork.FromProtoNetwork(req.GetOnChainUtxo().GetNetwork())
	if err != nil {
		return nil, err
	}
	createUtxoSwapRequestMessageHash, err := CreateUtxoSwapStatement(
		UtxoSwapStatementTypeCreated,
		hex.EncodeToString(req.GetOnChainUtxo().GetTxid()),
		req.GetOnChainUtxo().GetVout(),
		network,
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

// Verifies the refund transaction, specifically that it spends the expected UTXO.
// This prevents attacks where a caller requests a refund for UTXO A but provides a transaction
// that actually spends UTXO B.
func validateStaticDepositRefundTx(targetUtxo *VerifiedTargetUtxo, rawTx []byte) error {
	_, err := validateStaticDepositSingleInputTx(targetUtxo, rawTx, "refund")
	return err
}

func validateStaticDepositSpendTxSpendsTargetUtxo(targetUtxo *VerifiedTargetUtxo, rawTx []byte) error {
	spendTx, err := validateStaticDepositSingleInputTx(targetUtxo, rawTx, "spend")
	if err != nil {
		return err
	}

	totalOutputValue := int64(0)
	for _, out := range spendTx.TxOut {
		if out.Value < 0 {
			return errors.InvalidArgumentMalformedField(helper.ErrNegativeOutputValue)
		}
		if totalOutputValue > math.MaxInt64-out.Value {
			return errors.InvalidArgumentMalformedField(helper.ErrTotalOutputValueGreaterThanMaxInt64)
		}
		totalOutputValue += out.Value
	}
	onChainTxOut := wire.NewTxOut(int64(targetUtxo.inner.Amount), targetUtxo.inner.PkScript)
	if totalOutputValue > onChainTxOut.Value {
		return errors.InvalidArgumentMalformedField(fmt.Errorf("%w: totalOutputValue: %d, prevOutputValue: %d", helper.ErrTotalOutputValueGreaterThanPrevOutputValue, totalOutputValue, onChainTxOut.Value))
	}
	if _, err := sighash.FromTx(spendTx, 0, onChainTxOut); err != nil {
		return errors.InvalidArgumentMalformedField(fmt.Errorf("spend transaction is not signable: %w", err))
	}
	return nil
}

func validateStaticDepositSingleInputTx(targetUtxo *VerifiedTargetUtxo, rawTx []byte, txLabel string) (*wire.MsgTx, error) {
	if targetUtxo == nil {
		return nil, errors.InvalidArgumentMissingField(fmt.Errorf("target UTXO is nil"))
	}
	if len(rawTx) == 0 {
		return nil, errors.InvalidArgumentMissingField(fmt.Errorf("%s transaction is empty", txLabel))
	}

	parsedTx, err := common.TxFromRawTxBytes(rawTx)
	if err != nil {
		return nil, errors.InvalidArgumentMalformedField(fmt.Errorf("failed to parse %s transaction: %w", txLabel, err))
	}

	expectedTx := wire.NewMsgTx(3)
	expectedTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{
			Hash:  *targetUtxo.Hash(),
			Index: targetUtxo.Vout(),
		},
		Sequence: wire.MaxTxInSequenceNum,
	})
	for _, txOut := range parsedTx.TxOut {
		expectedTx.AddTxOut(txOut)
	}

	var buf bytes.Buffer
	if err := expectedTx.Serialize(&buf); err != nil {
		return nil, fmt.Errorf("unable to serialize expected %s transaction", txLabel)
	}
	expectedTxBytes := buf.Bytes()
	if !bytes.Equal(expectedTxBytes, rawTx) {
		return nil, errors.InvalidArgumentMalformedField(fmt.Errorf("unexpected %s transaction structure: expected %x, got %x", txLabel, expectedTxBytes, rawTx))
	}
	return parsedTx, nil
}
