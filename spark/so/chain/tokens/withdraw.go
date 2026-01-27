package tokens

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/predicate"
	"github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/tokenoutput"
)

// parsedWithdrawal represents a parsed withdrawal transaction from L1.
type parsedWithdrawal struct {
	withdrawalTx      *parsedWithdrawalTransaction
	outputsToWithdraw []parsedOutputWithdrawal
	txHash            chainhash.Hash
	tx                *wire.MsgTx
	outputIdx         int
}

type parsedWithdrawalTransaction struct {
	entity         ent.L1WithdrawalTransaction
	seEntityPubKey keys.Public
}

type parsedOutputWithdrawal struct {
	withdrawal  ent.L1TokenOutputWithdrawal
	sparkTxHash []byte
	sparkTxVout uint32
}

// HandleTokenWithdrawals scans transactions for BTKN withdrawal announcements
// and records valid withdrawals in the database.
func HandleTokenWithdrawals(
	ctx context.Context,
	config *so.Config,
	dbClient *ent.Client,
	txs []wire.MsgTx,
	network btcnetwork.Network,
	blockHeight uint64,
	blockHash chainhash.Hash,
) error {
	logger := logging.GetLoggerFromContext(ctx)

	withdrawals := parseWithdrawalsFromBlock(ctx, txs, blockHeight, blockHash)
	if len(withdrawals) == 0 {
		return nil
	}

	latestSeEntity, err := ent.GetEntityDkgKey(ctx, dbClient)
	if err != nil {
		return fmt.Errorf("failed to query latest SE entity: %w", err)
	}
	latestSeEntityPubKey := latestSeEntity.Edges.SigningKeyshare.PublicKey

	// Track outputs withdrawn in this block to prevent duplicates
	withdrawnInBlock := make(map[string]struct{})

	for _, withdrawal := range withdrawals {
		if err := processWithdrawal(ctx, dbClient, logger, withdrawal, latestSeEntityPubKey, latestSeEntity, withdrawnInBlock, blockHash); err != nil {
			logger.With(zap.Stringer("withdrawal_txid", withdrawal.txHash)).
				With(zap.Uint64("block_height", blockHeight)).
				With(zap.Error(err)).
				Error("Failed to process withdrawal")
		}
	}

	return nil
}

func parseWithdrawalsFromBlock(ctx context.Context, txs []wire.MsgTx, blockHeight uint64, blockHash chainhash.Hash) []parsedWithdrawal {
	logger := logging.GetLoggerFromContext(ctx)
	var withdrawals []parsedWithdrawal

	for _, tx := range txs {
		for txOutIdx, txOut := range tx.TxOut {
			parsedTx, parsedOutputs, err := parseTokenWithdrawal(txOut.PkScript)
			if err != nil {
				logger.With(zap.Stringer("txid", tx.TxHash())).
					With(zap.Int("output_idx", txOutIdx)).
					With(zap.Uint64("block_height", blockHeight)).
					With(zap.String("expected_format", WithdrawalExpectedFormat)).
					With(zap.Error(err)).
					Warn("Failed to parse token withdrawal")
				continue
			}

			if parsedTx == nil {
				continue
			}

			txHash := tx.TxHash()
			parsedTx.entity.ConfirmationTxid = schematype.NewTxID(txHash)
			parsedTx.entity.ConfirmationBlockHash = blockHash[:]
			parsedTx.entity.ConfirmationHeight = blockHeight
			parsedTx.entity.DetectedAt = time.Now()

			txCopy := tx
			withdrawals = append(withdrawals, parsedWithdrawal{
				withdrawalTx:      parsedTx,
				outputsToWithdraw: parsedOutputs,
				txHash:            txHash,
				tx:                &txCopy,
				outputIdx:         txOutIdx,
			})

			logger.With(zap.Stringer("withdrawal_txid", txHash)).Info("Parsed token withdrawal")
		}
	}

	return withdrawals
}

func processWithdrawal(
	ctx context.Context,
	dbClient *ent.Client,
	logger *zap.Logger,
	withdrawal parsedWithdrawal,
	expectedSePubKey keys.Public,
	seEntity *ent.EntityDkgKey,
	withdrawnInBlock map[string]struct{},
	blockHash chainhash.Hash,
) error {
	// Verify SE entity public key
	if withdrawal.withdrawalTx.seEntityPubKey != expectedSePubKey {
		logger.With(zap.Stringer("withdrawal_txid", withdrawal.txHash)).
			Sugar().Infof("Rejecting withdrawal: invalid SE entity public key. Expected: %s Got: %s",
			expectedSePubKey, withdrawal.withdrawalTx.seEntityPubKey)
		return nil
	}

	// Query all token outputs for this withdrawal
	tokenOutputMap, err := queryTokenOutputs(ctx, dbClient, withdrawal.outputsToWithdraw)
	if err != nil {
		return fmt.Errorf("failed to query token outputs: %w", err)
	}

	// Validate each output
	var approvedWithdrawals []parsedOutputWithdrawal
	var tokenOutputs []*ent.TokenOutput

	for _, outputToWithdraw := range withdrawal.outputsToWithdraw {
		key := sparkTxHashVoutKey(outputToWithdraw.sparkTxHash, outputToWithdraw.sparkTxVout)

		tokenOutput, err := validateOutputWithdrawable(outputToWithdraw, withdrawnInBlock, tokenOutputMap)
		if err != nil {
			logger.With(zap.Stringer("withdrawal_txid", withdrawal.txHash)).
				With(zap.String("spark_output", key)).
				With(zap.Error(err)).
				Info("Rejecting withdrawal output")
			// TODO: broadcast justice transaction for invalid withdrawals
			continue
		}

		if err := validateWithdrawalTxOutput(withdrawal.tx, &outputToWithdraw.withdrawal, tokenOutput); err != nil {
			logger.With(zap.Stringer("withdrawal_txid", withdrawal.txHash)).
				With(zap.String("spark_output", key)).
				With(zap.Error(err)).
				Error("Rejecting withdrawal: invalid transaction output")
			// TODO: broadcast justice transaction for invalid withdrawals
			continue
		}

		approvedWithdrawals = append(approvedWithdrawals, outputToWithdraw)
		tokenOutputs = append(tokenOutputs, tokenOutput)
		withdrawnInBlock[key] = struct{}{}
	}

	// TODO: Validate owner signature

	if len(approvedWithdrawals) == 0 {
		logger.With(zap.Stringer("withdrawal_txid", withdrawal.txHash)).
			Info("Skipping withdrawal tx: no valid outputs")
		return nil
	}

	savedTx, err := ent.SaveWithdrawalTransaction(ctx, dbClient, &withdrawal.withdrawalTx.entity, seEntity)
	if err != nil {
		return fmt.Errorf("failed to save withdrawal transaction: %w", err)
	}

	bitcoinVouts := make([]uint16, len(approvedWithdrawals))
	for i, w := range approvedWithdrawals {
		bitcoinVouts[i] = w.withdrawal.BitcoinVout
	}

	if _, err := ent.SaveOutputWithdrawals(ctx, dbClient, bitcoinVouts, tokenOutputs, savedTx); err != nil {
		return fmt.Errorf("failed to save output withdrawals: %w", err)
	}

	tokenOutputIDs := make([]uuid.UUID, len(tokenOutputs))
	for i, to := range tokenOutputs {
		tokenOutputIDs[i] = to.ID
	}

	if err := ent.MarkOutputsWithdrawn(ctx, dbClient, tokenOutputIDs, blockHash[:]); err != nil {
		return fmt.Errorf("failed to mark outputs as withdrawn: %w", err)
	}

	return nil
}

// parseTokenWithdrawal parses a BTKN withdrawal from an OP_RETURN script.
// Returns (transaction, outputs, nil) on success with the parsed withdrawal data.
// Returns (nil, nil, nil) if not a BTKN withdrawal (different prefix, not OP_RETURN, etc.).
// Returns (nil, nil, error) if the script is a BTKN withdrawal but malformed.
func parseTokenWithdrawal(script []byte) (*parsedWithdrawalTransaction, []parsedOutputWithdrawal, error) {
	buf := bytes.NewBuffer(script)

	// Check for OP_RETURN
	if op, err := buf.ReadByte(); err != nil || op != txscript.OP_RETURN {
		return nil, nil, nil
	}
	if err := common.ValidatePushBytes(buf); err != nil {
		return nil, nil, nil
	}

	// Check for BTKN prefix
	if prefix := buf.Next(len(btknWithdrawal.Prefix)); !bytes.Equal(prefix, []byte(btknWithdrawal.Prefix)) {
		return nil, nil, nil
	}
	if kind := buf.Next(withdrawalKindSizeBytes); !bytes.Equal(kind, btknWithdrawal.Kind[:]) {
		return nil, nil, nil
	}

	// Parse SE entity public key
	seEntityPubKeyBytes, err := common.ReadBytes(buf, seEntityPubKeySizeBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid SE public key: %w", err)
	}
	seEntityPubKey, err := keys.ParsePublicKey(seEntityPubKeyBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid SE public key: %w", err)
	}

	// Parse owner signature
	ownerSignatureBytes, err := common.ReadBytes(buf, ownerSignatureSizeBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid owner signature: %w", err)
	}

	// Parse withdrawal count
	withdrawnCount, err := common.ReadByte(buf)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid withdrawn count: %w", err)
	}
	if withdrawnCount == 0 {
		return nil, nil, fmt.Errorf("invalid withdrawn count: must be greater than zero")
	}

	// Parse each withdrawal
	withdrawals := make([]parsedOutputWithdrawal, 0, withdrawnCount)
	for i := 0; i < int(withdrawnCount); i++ {
		voutBytes, err := common.ReadBytes(buf, withdrawalOutputVoutSizeBytes)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid vout bytes: %w", err)
		}
		vout := binary.BigEndian.Uint16(voutBytes)

		sparkTxHash, err := common.ReadBytes(buf, withdrawalSparkTxHashSizeBytes)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid spark tx hash: %w", err)
		}

		sparkTxVoutBytes, err := common.ReadBytes(buf, withdrawalSparkTxVoutSizeBytes)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid spark tx vout: %w", err)
		}
		sparkTxVout := binary.BigEndian.Uint32(sparkTxVoutBytes)

		withdrawals = append(withdrawals, parsedOutputWithdrawal{
			withdrawal: ent.L1TokenOutputWithdrawal{
				BitcoinVout: vout,
			},
			sparkTxHash: sparkTxHash,
			sparkTxVout: sparkTxVout,
		})
	}

	if buf.Len() > 0 {
		return nil, nil, fmt.Errorf("unexpected trailing data: %d bytes", buf.Len())
	}

	return &parsedWithdrawalTransaction{
		entity: ent.L1WithdrawalTransaction{
			OwnerSignature: ownerSignatureBytes,
		},
		seEntityPubKey: seEntityPubKey,
	}, withdrawals, nil
}

// validateOutputWithdrawable checks if a token output can be withdrawn.
// Returns the token output if valid, or an error explaining why it cannot be withdrawn.
// Errors can be checked with errors.Is() against sentinel errors like ErrOutputNotFound.
func validateOutputWithdrawable(
	output parsedOutputWithdrawal,
	withdrawnInBlock map[string]struct{},
	tokenOutputs map[string]*ent.TokenOutput,
) (*ent.TokenOutput, error) {
	key := sparkTxHashVoutKey(output.sparkTxHash, output.sparkTxVout)

	// Check if already withdrawn in this block
	if _, ok := withdrawnInBlock[key]; ok {
		return nil, ErrOutputAlreadyWithdrawnInBlock
	}

	// Find the token output
	tokenOutput, ok := tokenOutputs[key]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrOutputNotFound, key)
	}

	// Check if output is in a spendable status
	if !isSpendableOutputStatus(tokenOutput.Status) {
		spentTx := tokenOutput.Edges.OutputSpentTokenTransaction
		if spentTx == nil {
			return nil, fmt.Errorf("%w: status is %s with no spending transaction", ErrOutputNotWithdrawable, tokenOutput.Status)
		}
		if err := checkSpendingTransactionAllowsWithdrawal(spentTx); err != nil {
			return nil, err
		}
	}

	// Check if already withdrawn on-chain
	if tokenOutput.ConfirmedWithdrawBlockHash != nil {
		return nil, ErrOutputAlreadyWithdrawnOnChain
	}

	return tokenOutput, nil
}

func isSpendableOutputStatus(status schematype.TokenOutputStatus) bool {
	return status == schematype.TokenOutputStatusCreatedFinalized ||
		status == schematype.TokenOutputStatusSpentStarted
}

// checkSpendingTransactionAllowsWithdrawal checks if a spending transaction allows withdrawal.
// Returns nil if the transaction has expired (allowing withdrawal).
// Returns an error explaining why withdrawal is blocked if the transaction is still active.
func checkSpendingTransactionAllowsWithdrawal(spentTx *ent.TokenTransaction) error {
	if err := spentTx.ValidateNotExpired(); err == nil {
		// Transaction hasn't expired - check if it's finalized
		if spentTx.Status == schematype.TokenTransactionStatusRevealed ||
			spentTx.Status == schematype.TokenTransactionStatusFinalized {
			return fmt.Errorf("%w: already spent by finalized transaction", ErrOutputNotWithdrawable)
		}
		// Transaction is active but not finalized - block withdrawal until it expires or finalizes
		return fmt.Errorf("%w: spending transaction in progress (status: %s)", ErrOutputNotWithdrawable, spentTx.Status)
	}
	return nil
}

// validateWithdrawalTxOutput validates that the L1 transaction output matches expected values.
// Errors can be checked with errors.Is() against sentinel errors like ErrInsufficientBond.
func validateWithdrawalTxOutput(tx *wire.MsgTx, withdrawal *ent.L1TokenOutputWithdrawal, tokenOutput *ent.TokenOutput) error {
	if int(withdrawal.BitcoinVout) >= len(tx.TxOut) {
		return fmt.Errorf("%w: vout %d out of range (tx has %d outputs)", ErrVoutOutOfRange, withdrawal.BitcoinVout, len(tx.TxOut))
	}

	txOut := tx.TxOut[withdrawal.BitcoinVout]

	// Verify bond amount
	if uint64(txOut.Value) < tokenOutput.WithdrawBondSats {
		return fmt.Errorf("%w: got %d sats, expected at least %d", ErrInsufficientBond, txOut.Value, tokenOutput.WithdrawBondSats)
	}

	// Verify script matches expected revocation CSV output
	revocationXOnly := tokenOutput.WithdrawRevocationCommitment[1:] // Strip prefix byte
	expectedOutput, err := ConstructRevocationCsvTaprootOutput(
		revocationXOnly,
		tokenOutput.OwnerPublicKey.SerializeXOnly(),
		tokenOutput.WithdrawRelativeBlockLocktime,
	)
	if err != nil {
		return fmt.Errorf("failed to construct expected script: %w", err)
	}

	if !bytes.Equal(txOut.PkScript, expectedOutput.ScriptPubKey) {
		return fmt.Errorf("%w: expected %x, got %x", ErrScriptMismatch, expectedOutput.ScriptPubKey, txOut.PkScript)
	}

	return nil
}

// queryTokenOutputs fetches token outputs by their (txHash, vout) pairs.
// Uses the denormalized created_transaction_finalized_hash field for efficient lookup.
// Returns a map keyed by "hexEncodedTxHash:vout" (see sparkTxHashVoutKey).
func queryTokenOutputs(ctx context.Context, dbClient *ent.Client, outputs []parsedOutputWithdrawal) (map[string]*ent.TokenOutput, error) {
	if len(outputs) == 0 {
		return nil, nil
	}

	// Build OR predicates for batch query
	predicates := make([]predicate.TokenOutput, 0, len(outputs))
	for _, output := range outputs {
		predicates = append(predicates,
			tokenoutput.And(
				tokenoutput.CreatedTransactionFinalizedHash(output.sparkTxHash),
				tokenoutput.CreatedTransactionOutputVout(int32(output.sparkTxVout)),
			),
		)
	}

	tokenOutputs, err := dbClient.TokenOutput.Query().
		Where(tokenoutput.Or(predicates...)).
		WithOutputCreatedTokenTransaction().
		WithOutputSpentTokenTransaction().
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to query token outputs: %w", err)
	}

	result := make(map[string]*ent.TokenOutput, len(tokenOutputs))
	for _, to := range tokenOutputs {
		txHash := to.Edges.OutputCreatedTokenTransaction.FinalizedTokenTransactionHash
		key := sparkTxHashVoutKey(txHash, uint32(to.CreatedTransactionOutputVout))
		result[key] = to
	}

	return result, nil
}

// sparkTxHashVoutKey generates a cache key for (txHash, vout) pairs.
func sparkTxHashVoutKey(txHash []byte, vout uint32) string {
	return fmt.Sprintf("%s:%d", hex.EncodeToString(txHash), vout)
}
