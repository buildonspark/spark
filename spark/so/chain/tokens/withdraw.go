package tokens

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"time"

	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
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
	"github.com/lightsparkdev/spark/so/knobs"
)

// tokenOutputKey identifies a token output by its creating transaction hash and vout.
type tokenOutputKey struct {
	txHash [32]byte
	vout   uint32
}

func newTokenOutputKey(txHash []byte, vout uint32) tokenOutputKey {
	var hash [32]byte
	copy(hash[:], txHash)
	return tokenOutputKey{txHash: hash, vout: vout}
}

func (k tokenOutputKey) String() string {
	return fmt.Sprintf("%x:%d", k.txHash, k.vout)
}

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

	withdrawnInBlock := make(map[tokenOutputKey]struct{})

	for _, withdrawal := range withdrawals {
		if err := processWithdrawal(ctx, dbClient, logger, withdrawal, latestSeEntityPubKey, latestSeEntity, withdrawnInBlock); err != nil {
			logger.With(zap.Error(err)).Sugar().Errorf("Failed to process withdrawal %s at block height %d", withdrawal.txHash, blockHeight)
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
				logger.With(zap.Error(err)).Sugar().Warnf("Failed to parse token withdrawal %s at block height %d vout %d (expected format: %s)",
					tx.TxHash(), blockHeight, txOutIdx, WithdrawalExpectedFormat)
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

			logger.Sugar().Infof("Parsed token withdrawal %s", txHash)
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
	withdrawnInBlock map[tokenOutputKey]struct{},
) error {
	if withdrawal.withdrawalTx.seEntityPubKey != expectedSePubKey {
		logger.Sugar().Infof("Rejecting withdrawal %s: invalid SE entity public key (expected: %s, got: %s)",
			withdrawal.txHash, expectedSePubKey, withdrawal.withdrawalTx.seEntityPubKey)
		return nil
	}

	tokenOutputMap, err := queryTokenOutputs(ctx, dbClient, withdrawal.outputsToWithdraw)
	if err != nil {
		return fmt.Errorf("failed to query token outputs: %w", err)
	}

	// Build ordered slice matching OP_RETURN order for signature validation.
	// The client signs over SE signatures in this order, so we must validate in the same order.
	orderedOutputs := make([]*ent.TokenOutput, 0, len(withdrawal.outputsToWithdraw))
	for _, output := range withdrawal.outputsToWithdraw {
		key := newTokenOutputKey(output.sparkTxHash, output.sparkTxVout)
		tokenOutput, ok := tokenOutputMap[key]
		if !ok {
			logger.Sugar().Infof("Rejecting withdrawal %s: output %s not found in database", withdrawal.txHash, key)
			return nil
		}
		orderedOutputs = append(orderedOutputs, tokenOutput)
	}

	if err := validateOwnerSignature(ctx, orderedOutputs, withdrawal.withdrawalTx.entity.OwnerSignature); err != nil {
		logger.With(zap.Error(err)).Sugar().Errorf("Rejecting withdrawal %s: invalid owner signature", withdrawal.txHash)
		return nil
	}

	var approvedWithdrawals []parsedOutputWithdrawal
	var tokenOutputs []*ent.TokenOutput

	for _, outputToWithdraw := range withdrawal.outputsToWithdraw {
		key := newTokenOutputKey(outputToWithdraw.sparkTxHash, outputToWithdraw.sparkTxVout)

		tokenOutput, err := validateOutputWithdrawable(outputToWithdraw, withdrawnInBlock, tokenOutputMap)
		if err != nil {
			logger.With(zap.Error(err)).Sugar().Infof("Rejecting withdrawal %s output %s", withdrawal.txHash, key)
			// TODO: broadcast justice transaction for invalid withdrawals
			continue
		}

		if err := validateWithdrawalTxOutput(withdrawal.tx, &outputToWithdraw.withdrawal, tokenOutput); err != nil {
			logger.With(zap.Error(err)).Sugar().Errorf("Rejecting withdrawal %s output %s: invalid transaction output", withdrawal.txHash, key)
			// TODO: broadcast justice transaction for invalid withdrawals
			continue
		}

		approvedWithdrawals = append(approvedWithdrawals, outputToWithdraw)
		tokenOutputs = append(tokenOutputs, tokenOutput)
		withdrawnInBlock[key] = struct{}{}
	}

	if len(approvedWithdrawals) == 0 {
		logger.Sugar().Infof("Skipping withdrawal tx %s: no valid outputs", withdrawal.txHash)
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

	return nil
}

// parseTokenWithdrawal parses a BTKN withdrawal from an OP_RETURN script.
// Returns (nil, nil, nil) if not a BTKN withdrawal.
// Returns (nil, nil, error) if the script is a BTKN withdrawal but malformed.
func parseTokenWithdrawal(script []byte) (*parsedWithdrawalTransaction, []parsedOutputWithdrawal, error) {
	buf := bytes.NewBuffer(script)

	if op, err := buf.ReadByte(); err != nil || op != txscript.OP_RETURN {
		return nil, nil, nil
	}
	if err := common.ValidatePushBytes(buf); err != nil {
		return nil, nil, nil
	}

	if prefix := buf.Next(len(btknWithdrawal.Prefix)); !bytes.Equal(prefix, []byte(btknWithdrawal.Prefix)) {
		return nil, nil, nil
	}
	if kind := buf.Next(withdrawalKindSizeBytes); !bytes.Equal(kind, btknWithdrawal.Kind[:]) {
		return nil, nil, nil
	}

	seEntityPubKeyBytes, err := common.ReadBytes(buf, seEntityPubKeySizeBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid SE public key: %w", err)
	}
	seEntityPubKey, err := keys.ParsePublicKey(seEntityPubKeyBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid SE public key: %w", err)
	}

	ownerSignatureBytes, err := common.ReadBytes(buf, ownerSignatureSizeBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid owner signature: %w", err)
	}

	withdrawnCount, err := common.ReadByte(buf)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid withdrawn count: %w", err)
	}
	if withdrawnCount == 0 {
		return nil, nil, fmt.Errorf("invalid withdrawn count: must be greater than zero")
	}

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

func validateOutputWithdrawable(
	output parsedOutputWithdrawal,
	withdrawnInBlock map[tokenOutputKey]struct{},
	tokenOutputs map[tokenOutputKey]*ent.TokenOutput,
) (*ent.TokenOutput, error) {
	key := newTokenOutputKey(output.sparkTxHash, output.sparkTxVout)

	if _, ok := withdrawnInBlock[key]; ok {
		return nil, ErrOutputAlreadyWithdrawnInBlock
	}

	tokenOutput, ok := tokenOutputs[key]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrOutputNotFound, key)
	}

	if tokenOutput.Status != schematype.TokenOutputStatusCreatedFinalized {
		spentTx := tokenOutput.Edges.OutputSpentTokenTransaction
		if spentTx == nil {
			return nil, fmt.Errorf("%w: status is %s with no spending transaction", ErrOutputNotWithdrawable, tokenOutput.Status)
		}
		if err := checkSpendingTransactionAllowsWithdrawal(spentTx); err != nil {
			return nil, err
		}
	}

	if tokenOutput.Edges.Withdrawal != nil {
		return nil, ErrOutputAlreadyWithdrawnOnChain
	}

	return tokenOutput, nil
}

func checkSpendingTransactionAllowsWithdrawal(spentTx *ent.TokenTransaction) error {
	if err := spentTx.ValidateNotExpired(); err == nil {
		if spentTx.Status == schematype.TokenTransactionStatusRevealed ||
			spentTx.Status == schematype.TokenTransactionStatusFinalized {
			return fmt.Errorf("%w: already spent by finalized transaction", ErrOutputNotWithdrawable)
		}
		return fmt.Errorf("%w: spending transaction in progress (status: %s)", ErrOutputNotWithdrawable, spentTx.Status)
	}
	return nil
}

func validateWithdrawalTxOutput(tx *wire.MsgTx, withdrawal *ent.L1TokenOutputWithdrawal, tokenOutput *ent.TokenOutput) error {
	if int(withdrawal.BitcoinVout) >= len(tx.TxOut) {
		return fmt.Errorf("%w: vout %d out of range (tx has %d outputs)", ErrVoutOutOfRange, withdrawal.BitcoinVout, len(tx.TxOut))
	}

	txOut := tx.TxOut[withdrawal.BitcoinVout]

	if uint64(txOut.Value) < tokenOutput.WithdrawBondSats {
		return fmt.Errorf("%w: got %d sats, expected at least %d", ErrInsufficientBond, txOut.Value, tokenOutput.WithdrawBondSats)
	}

	revocationXOnly := tokenOutput.WithdrawRevocationCommitment[1:]
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

// validateOwnerSignature validates that the owner signature is valid for the batch of token outputs.
// The owner must sign over the tagged hash of all SE withdrawal signatures.
// By default, owner signature validation is skipped. The KnobEnforceWithdrawalSignatureValidation
// knob can be enabled to require SE signatures and validate the owner signature.
func validateOwnerSignature(ctx context.Context, tokenOutputs []*ent.TokenOutput, signatureBytes []byte) error {
	if len(tokenOutputs) == 0 {
		return fmt.Errorf("no token outputs to validate")
	}

	ownerPublicKey := tokenOutputs[0].OwnerPublicKey
	for _, tokenOutput := range tokenOutputs {
		if tokenOutput.OwnerPublicKey != ownerPublicKey {
			return fmt.Errorf("outputs have different owners: expected %s, got %s",
				ownerPublicKey, tokenOutput.OwnerPublicKey)
		}
	}

	k := knobs.GetKnobsService(ctx)
	if k.GetValue(knobs.KnobEnforceWithdrawalSignatureValidation, 0) == 0 {
		return nil
	}

	seSignatures := make([][]byte, 0, len(tokenOutputs))
	for _, tokenOutput := range tokenOutputs {
		if len(tokenOutput.SeWithdrawalSignature) == 0 {
			return fmt.Errorf("missing SE withdrawal signature for token output")
		}
		seSignatures = append(seSignatures, tokenOutput.SeWithdrawalSignature)
	}

	hash := chainhash.TaggedHash([]byte(TagBTKNWithdrawal), seSignatures...)

	schnorrSig, err := schnorr.ParseSignature(signatureBytes)
	if err != nil {
		return fmt.Errorf("failed to parse schnorr signature: %w", err)
	}

	if !ownerPublicKey.Verify(schnorrSig, hash[:]) {
		return fmt.Errorf("invalid owner signature")
	}

	return nil
}

// queryTokenOutputs fetches token outputs by their (txHash, vout) pairs.
func queryTokenOutputs(ctx context.Context, dbClient *ent.Client, outputs []parsedOutputWithdrawal) (map[tokenOutputKey]*ent.TokenOutput, error) {
	if len(outputs) == 0 {
		return nil, nil
	}

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
		WithWithdrawal().
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to query token outputs: %w", err)
	}

	result := make(map[tokenOutputKey]*ent.TokenOutput, len(tokenOutputs))
	for _, to := range tokenOutputs {
		txHash := to.Edges.OutputCreatedTokenTransaction.FinalizedTokenTransactionHash
		key := newTokenOutputKey(txHash, uint32(to.CreatedTransactionOutputVout))
		result[key] = to
	}

	return result, nil
}
