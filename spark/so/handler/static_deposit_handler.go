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
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authz"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/helper"
	"go.opentelemetry.io/otel/trace"
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

func (o *StaticDepositHandler) SaveUtxoForInstantStaticDepositForAllOperators(ctx context.Context, config *so.Config, request *pbinternal.SaveUtxoForInstantStaticDepositRequest) error {
	ctx, span := tracer.Start(ctx, "StaticDepositHandler.SaveUtxoForInstantStaticDepositForAllOperators")
	defer span.End()

	logger := logging.GetLoggerFromContext(ctx)

	_, err := helper.ExecuteTaskWithAllOperators(ctx, config, &helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}, func(ctx context.Context, operator *so.SigningOperator) (*pbinternal.SaveUtxoForInstantStaticDepositResponse, error) {
		conn, err := operator.NewOperatorInternalGRPCConnection(ctx)
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
		conn, err := operator.NewOperatorInternalGRPCConnection(ctx)
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

// InitiateStaticDepositUtxoRefund processes a request to refund a UTXO back to the User.
func (o *StaticDepositHandler) InitiateStaticDepositUtxoRefund(ctx context.Context, config *so.Config, req *pb.InitiateStaticDepositUtxoRefundRequest) (*pb.InitiateStaticDepositUtxoRefundResponse, error) {
	ctx, span := tracer.Start(ctx, "StaticDepositHandler.InitiateStaticDepositUtxoRefund", trace.WithAttributes(
		transferTypeKey.String(string(st.TransferTypeUtxoSwap)),
	))
	defer span.End()

	if req == nil {
		return nil, errors.InvalidArgumentMissingField(fmt.Errorf("request is required"))
	}
	if req.GetOnChainUtxo() == nil {
		return nil, errors.InvalidArgumentMissingField(fmt.Errorf("on_chain_utxo is required"))
	}

	return o.initiateStaticDepositUtxoRefundConsensus(ctx, config, req)
}

// handleAlreadyRegisteredSwapOnRefund resolves a refund request against a UTXO
// whose swap is already registered. Once a static deposit has been refunded it
// can no longer be used in a swap and must be claimed on L1, but the owner may
// sign additional refund transactions after that point (e.g. to adjust fees) —
// so a COMPLETED refund swap owned by the caller is re-signed and returned,
// while any other registered swap is a conflict.
func (o *StaticDepositHandler) handleAlreadyRegisteredSwapOnRefund(ctx context.Context, config *so.Config, utxoSwap *ent.UtxoSwap, targetUtxo *VerifiedTargetUtxo, schemaNetwork btcnetwork.Network, req *pb.InitiateStaticDepositUtxoRefundRequest) (*pb.InitiateStaticDepositUtxoRefundResponse, error) {
	logger := logging.GetLoggerFromContext(ctx)
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
