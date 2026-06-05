package dkg

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/logging"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pbdkg "github.com/lightsparkdev/spark/proto/dkg"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/signingkeyshare"
	"github.com/lightsparkdev/spark/so/helper"
	"go.uber.org/zap"
)

const defaultDelayBeforeConfirmation = 15 * time.Second

// GenerateKeys runs the DKG protocol to generate the keys.
func GenerateKeys(ctx context.Context, config *so.Config, keyCount uint64) error {
	logger := logging.GetLoggerFromContext(ctx)
	start := time.Now()

	// Init clients
	clientMap := make(map[string]pbdkg.DKGServiceClient)
	for identifier, operator := range config.SigningOperatorMap {
		connection, err := operator.NewOperatorGRPCConnectionForDKG()
		if err != nil {
			return fmt.Errorf("failed to connect to signing operator %s: %w", identifier, err)
		}
		defer connection.Close()
		client := pbdkg.NewDKGServiceClient(connection)
		clientMap[identifier] = client
	}

	// Initiate DKG
	requestID, err := uuid.NewV7()
	if err != nil {
		return err
	}
	requestIDString := requestID.String()
	initRequest := &pbdkg.InitiateDkgRequest{
		RequestId:        requestIDString,
		KeyCount:         keyCount,
		MinSigners:       config.Threshold,
		MaxSigners:       uint64(len(config.SigningOperatorMap)),
		CoordinatorIndex: config.Index,
	}

	round1Packages := make([]*pbcommon.PackageMap, int(keyCount))

	for identifier, client := range clientMap {
		round1Response, err := client.InitiateDkg(ctx, initRequest)
		if err != nil {
			return fmt.Errorf("failed to initiate DKG with signing operator %s: %w", identifier, err)
		}
		for i, p := range round1Response.GetRound1Package() {
			if round1Packages[i] == nil {
				round1Packages[i] = &pbcommon.PackageMap{
					Packages: make(map[string][]byte),
				}
			}
			round1Packages[i].Packages[round1Response.GetIdentifier()] = p
		}
	}

	// Round 1 Validation
	round1Signatures := make(map[string][]byte)

	for identifier, client := range clientMap {
		round1SignatureRequest := &pbdkg.Round1PackagesRequest{
			RequestId:      requestIDString,
			Round1Packages: round1Packages,
		}
		round1SignatureResponse, err := client.Round1Packages(ctx, round1SignatureRequest)
		if err != nil {
			return fmt.Errorf("failed to get round 1 signatures from signing operator %s: %w", identifier, err)
		}
		round1Signatures[round1SignatureResponse.GetIdentifier()] = round1SignatureResponse.GetRound1Signature()
	}

	wg := sync.WaitGroup{}

	// Round 1 Signature Delivery
	for identifier, client := range clientMap {
		wg.Go(func() {
			round1SignatureRequest := &pbdkg.Round1SignatureRequest{
				RequestId:        requestIDString,
				Round1Signatures: round1Signatures,
			}
			round1SignatureResponse, err := client.Round1Signature(ctx, round1SignatureRequest)
			if err != nil {
				logger.With(zap.Error(err)).Sugar().Warnf("failed to deliver round 1 signatures to signing operator %s", identifier)
				return
			}

			if len(round1SignatureResponse.GetValidationFailures()) > 0 {
				logger.Sugar().Warnf("round 1 signature delivery to signing operator %s returned %d validation failures", identifier, len(round1SignatureResponse.GetValidationFailures()))
				return
			}
		})
	}

	wg.Wait()
	logger.Sugar().Infof("DKG protocol phases complete in %s (request_id: %s)", time.Since(start), requestIDString)

	// Optionally confirm and mark keys AVAILABLE only when the feature is enabled.
	if config.DKGConfig.EnableKeyConfirmation {

		keyIDs := make([]uuid.UUID, int(keyCount))
		for i := range keyIDs {
			keyIDs[i] = deriveKeyIndex(requestID, uint16(i))
		}

		// Give participants time to complete Round3 (crypto + DB writes) before polling for confirmation
		delay := defaultDelayBeforeConfirmation
		if config.DKGConfig.InitialDelayBeforeConfirmation != nil && *config.DKGConfig.InitialDelayBeforeConfirmation > 0 {
			delay = *config.DKGConfig.InitialDelayBeforeConfirmation
		}
		logger.Sugar().Infof("DKG sleeping %s before confirmation (elapsed: %s, request_id: %s)", delay, time.Since(start), requestIDString)
		select {
		case <-time.After(delay):
			logger.Sugar().Infof("DKG confirmation delay complete, starting confirmation queries (elapsed: %s, request_id: %s)", time.Since(start), requestIDString)
			err := ConfirmAndMarkAvailableKeys(ctx, config, keyIDs, requestID)
			logger.Sugar().Infof("DKG ConfirmAndMarkAvailableKeys returned (total elapsed: %s, request_id: %s, err: %v)", time.Since(start), requestIDString, err)
			return err
		case <-ctx.Done():
			logger.With(zap.Error(ctx.Err())).Sugar().Warnf("DKG cancelled during confirmation delay (elapsed: %s, request_id: %s)", time.Since(start), requestIDString)
			return ctx.Err()
		}
	}

	return nil
}

// ConfirmAndMarkAvailableKeys queries each operator to see which of the provided keys are AVAILABLE,
// then marks only the subset of keys that are available on ALL operators as AVAILABLE locally.
// This allows partial batches to complete even if some keys were lost (e.g., due to rollback).
func ConfirmAndMarkAvailableKeys(ctx context.Context, config *so.Config, keyIDs []uuid.UUID, batchID uuid.UUID) error {
	logger := logging.GetLoggerFromContext(ctx)

	if len(keyIDs) == 0 {
		return nil
	}
	keyIDsStr := make([]string, 0, len(keyIDs))
	for _, id := range keyIDs {
		keyIDsStr = append(keyIDsStr, id.String())
	}

	// Query each operator for which keys are unavailable (no lock needed - each goroutine writes to its own result)
	selection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}
	unavailablePerOperator, _ := helper.ExecuteTaskWithAllOperators(ctx, config, &selection, func(ctx context.Context, operator *so.SigningOperator) ([]string, error) {
		conn, err := operator.NewOperatorGRPCConnectionForDKG()
		if err != nil {
			// Connection error - all keys unavailable for this operator
			return keyIDsStr, nil
		}
		defer conn.Close()

		client := pbdkg.NewDKGServiceClient(conn)

		resp, err := client.RoundConfirmation(ctx, &pbdkg.RoundConfirmationRequest{
			KeyIds: keyIDsStr,
		})
		if err != nil {
			// RPC error - all keys unavailable for this operator
			return keyIDsStr, nil
		}
		return resp.GetUnavailableKeyIds(), nil
	})

	missingPerKey := make(map[string][]string) // keyID -> list of operator identifiers
	for operatorID, unavailableKeys := range unavailablePerOperator {
		for _, keyID := range unavailableKeys {
			missingPerKey[keyID] = append(missingPerKey[keyID], operatorID)
		}
	}

	var availableOnAll []uuid.UUID
	for _, id := range keyIDs {
		idStr := id.String()
		if _, missing := missingPerKey[idStr]; !missing {
			availableOnAll = append(availableOnAll, id)
		}
	}

	unconfirmedCount := len(missingPerKey)
	if len(availableOnAll) == 0 {
		logger.Sugar().Warnf("No keys available across all operators yet (checked: %d, unconfirmed: %d, batch_id: %s)", len(keyIDs), unconfirmedCount, batchID)
		return fmt.Errorf("no keys available across all operators yet (checked %d)", len(keyIDs))
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return err
	}

	if err := db.SigningKeyshare.Update().
		Where(
			signingkeyshare.IDIn(availableOnAll...),
			signingkeyshare.StatusEQ(st.KeyshareStatusPending),
			signingkeyshare.CoordinatorIndexEQ(config.Index),
		).
		SetStatus(st.KeyshareStatusAvailable).
		Exec(ctx); err != nil {
		return err
	}

	if len(availableOnAll) < len(keyIDs) {
		logger.Sugar().Warnf("Partial key confirmation (available: %d, requested: %d, unconfirmed: %d, batch_id: %s)", len(availableOnAll), len(keyIDs), unconfirmedCount, batchID)
	}

	return nil
}
