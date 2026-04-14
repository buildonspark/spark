package handler

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"

	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common"
	bitcointransaction "github.com/lightsparkdev/spark/common/bitcoin_transaction"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/logging"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pbfrost "github.com/lightsparkdev/spark/proto/frost"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/depositaddress"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/tree"
	"github.com/lightsparkdev/spark/so/ent/treenode"
	entutxo "github.com/lightsparkdev/spark/so/ent/utxo"
	"github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/frost"
	"github.com/lightsparkdev/spark/so/helper"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// validateSigningJobFields validates that all required fields in a UserSignedTxSigningJob are present
func validateSigningJobFields(job *pb.UserSignedTxSigningJob, jobName string) error {
	if job == nil {
		return status.Errorf(codes.InvalidArgument, "%s is required", jobName)
	}

	if len(job.SigningPublicKey) == 0 {
		return status.Errorf(codes.InvalidArgument, "%s.signing_public_key is required", jobName)
	}

	if len(job.RawTx) == 0 {
		return status.Errorf(codes.InvalidArgument, "%s.raw_tx is required", jobName)
	}

	if job.SigningNonceCommitment == nil {
		return status.Errorf(codes.InvalidArgument, "%s.signing_nonce_commitment is required", jobName)
	}

	if len(job.UserSignature) == 0 {
		return status.Errorf(codes.InvalidArgument, "%s.user_signature is required", jobName)
	}

	if job.SigningCommitments == nil {
		return status.Errorf(codes.InvalidArgument, "%s.signing_commitments is required", jobName)
	}

	if len(job.SigningCommitments.SigningCommitments) == 0 {
		return status.Errorf(codes.InvalidArgument, "%s.signing_commitments.signing_commitments map is empty", jobName)
	}

	return nil
}

func validateFinalizeDepositTreeCreationRequest(
	req *pb.FinalizeDepositTreeCreationRequest,
) error {
	if err := validateSigningJobFields(req.RootTxSigningJob, "root_tx_signing_job"); err != nil {
		return err
	}

	if err := validateSigningJobFields(req.RefundTxSigningJob, "refund_tx_signing_job"); err != nil {
		return err
	}

	if err := validateSigningJobFields(req.DirectFromCpfpRefundTxSigningJob, "direct_from_cpfp_refund_tx_signing_job"); err != nil {
		return err
	}

	// Validate additional inputs match additional UTXOs count
	const maxAdditionalUtxos = 10
	if len(req.AdditionalOnChainUtxos) > maxAdditionalUtxos {
		return status.Errorf(codes.InvalidArgument,
			"too many additional UTXOs (%d), maximum is %d",
			len(req.AdditionalOnChainUtxos), maxAdditionalUtxos)
	}
	if len(req.AdditionalOnChainUtxos) > 0 {
		if len(req.RootTxSigningJob.AdditionalInputs) != len(req.AdditionalOnChainUtxos) {
			return status.Errorf(codes.InvalidArgument,
				"additional_inputs count (%d) must match additional_on_chain_utxos count (%d)",
				len(req.RootTxSigningJob.AdditionalInputs), len(req.AdditionalOnChainUtxos))
		}
		for i, input := range req.RootTxSigningJob.AdditionalInputs {
			if err := validateInputSigningData(input, fmt.Sprintf("root_tx_signing_job.additional_inputs[%d]", i)); err != nil {
				return err
			}
		}
	}

	return nil
}

// validateInputSigningData validates that an InputSigningData has all required fields.
// fieldPath identifies the field for error messages (e.g. "root_tx_signing_job.additional_inputs[0]").
func validateInputSigningData(input *pb.InputSigningData, fieldPath string) error {
	if input == nil {
		return status.Errorf(codes.InvalidArgument, "%s is required", fieldPath)
	}
	if input.SigningNonceCommitment == nil {
		return status.Errorf(codes.InvalidArgument, "%s.signing_nonce_commitment is required", fieldPath)
	}
	if len(input.UserSignature) == 0 {
		return status.Errorf(codes.InvalidArgument, "%s.user_signature is required", fieldPath)
	}
	if input.SigningCommitments == nil || len(input.SigningCommitments.SigningCommitments) == 0 {
		return status.Errorf(codes.InvalidArgument, "%s.signing_commitments is required", fieldPath)
	}
	return nil
}

// validateSigningJob validates that a signing job's public key matches the expected key
func validateSigningJob(job *pb.UserSignedTxSigningJob, expectedPubKey keys.Public, jobName string) error {
	if job == nil {
		return nil
	}
	pubKey, err := keys.ParsePublicKey(job.SigningPublicKey)
	if err != nil {
		return fmt.Errorf("invalid %s signing public key: %w", jobName, err)
	}
	if !pubKey.Equals(expectedPubKey) {
		return fmt.Errorf("%s signing public key does not match", jobName)
	}
	return nil
}

// additionalUtxoData holds parsed data for each additional UTXO in a multi-input deposit.
type additionalUtxoData struct {
	onChainTx     *wire.MsgTx
	onChainOutput *wire.TxOut
	vout          uint32
}

// load the deposit address and validate it
func loadAndValidateDepositAddress(
	ctx context.Context,
	network btcnetwork.Network,
	req *pb.FinalizeDepositTreeCreationRequest,
	reqIDPubKey keys.Public,
) (depositAddress *ent.DepositAddress, onChainTx *wire.MsgTx, onChainOutput *wire.TxOut, additionalUtxos []additionalUtxoData, err error) {
	// Parse on-chain UTXO
	onChainTx, err = common.TxFromRawTxBytes(req.OnChainUtxo.RawTx)
	if err != nil {
		err = fmt.Errorf("invalid on-chain transaction: %w", err)
		return
	}

	if int(req.OnChainUtxo.Vout) >= len(onChainTx.TxOut) {
		err = fmt.Errorf("utxo index out of bounds")
		return
	}
	onChainOutput = onChainTx.TxOut[req.OnChainUtxo.Vout]

	// Reject zero-value deposits: a zero-value UTXO cannot back a meaningful Spark leaf.
	if onChainOutput.Value <= 0 {
		err = status.Errorf(codes.InvalidArgument, "deposit UTXO output value must be greater than zero")
		return
	}

	utxoAddress, err := common.P2TRAddressFromPkScript(onChainOutput.PkScript, network)
	if err != nil {
		err = fmt.Errorf("invalid utxo address: %w", err)
		return
	}

	// Parse and validate additional UTXOs
	seenOutpoints := make(map[wire.OutPoint]bool)
	primaryTxHash := onChainTx.TxHash()
	seenOutpoints[wire.OutPoint{Hash: primaryTxHash, Index: req.OnChainUtxo.Vout}] = true

	for i, additionalUtxo := range req.AdditionalOnChainUtxos {
		var addTx *wire.MsgTx
		addTx, err = common.TxFromRawTxBytes(additionalUtxo.RawTx)
		if err != nil {
			err = fmt.Errorf("invalid additional on-chain transaction %d: %w", i, err)
			return
		}
		if int(additionalUtxo.Vout) >= len(addTx.TxOut) {
			err = fmt.Errorf("additional utxo %d: vout index out of bounds", i)
			return
		}

		// Reject duplicate UTXOs to prevent value inflation
		addTxHash := addTx.TxHash()
		outpoint := wire.OutPoint{Hash: addTxHash, Index: additionalUtxo.Vout}
		if seenOutpoints[outpoint] {
			err = fmt.Errorf("duplicate utxo %s:%d", addTxHash.String(), additionalUtxo.Vout)
			return
		}
		seenOutpoints[outpoint] = true

		addOutput := addTx.TxOut[additionalUtxo.Vout]

		// Reject zero-value additional UTXOs
		if addOutput.Value <= 0 {
			err = status.Errorf(codes.InvalidArgument, "additional utxo %d output value must be greater than zero", i)
			return
		}

		// Verify additional UTXO pays to the same deposit address
		var addAddress *string
		addAddress, err = common.P2TRAddressFromPkScript(addOutput.PkScript, network)
		if err != nil {
			err = fmt.Errorf("invalid additional utxo %d address: %w", i, err)
			return
		}
		if *addAddress != *utxoAddress {
			err = fmt.Errorf("additional utxo %d pays to different address than primary utxo", i)
			return
		}

		additionalUtxos = append(additionalUtxos, additionalUtxoData{
			onChainTx:     addTx,
			onChainOutput: addOutput,
			vout:          additionalUtxo.Vout,
		})
	}

	// Look up deposit address
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		err = fmt.Errorf("failed to get database: %w", err)
		return
	}

	depositAddress, err = db.DepositAddress.Query().
		Where(depositaddress.Address(*utxoAddress)).
		Where(depositaddress.IsStatic(false)).
		Where(depositaddress.NetworkEQ(network)).
		WithTree().
		WithSigningKeyshare().
		ForUpdate().
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			err = errors.NotFoundMissingEntity(fmt.Errorf("the requested deposit address could not be found: %s", *utxoAddress))
			return
		}
		if ent.IsNotSingular(err) {
			err = fmt.Errorf("multiple deposit addresses found for: %s", *utxoAddress)
			return
		}
		return
	}

	if !depositAddress.OwnerIdentityPubkey.Equals(reqIDPubKey) {
		err = fmt.Errorf("identity public key does not match deposit address owner")
		return
	}

	// Validate signing public keys
	rootSigningPubKey, err := keys.ParsePublicKey(req.RootTxSigningJob.SigningPublicKey)
	if err != nil {
		err = fmt.Errorf("invalid root tx signing public key: %w", err)
		return
	}
	if !depositAddress.OwnerSigningPubkey.Equals(rootSigningPubKey) {
		err = fmt.Errorf("signing public key does not match deposit address owner")
		return
	}

	// Validate all signing jobs have matching public keys
	if err = validateSigningJob(req.RefundTxSigningJob, rootSigningPubKey, "refund"); err != nil {
		return
	}
	if err = validateSigningJob(req.DirectFromCpfpRefundTxSigningJob, rootSigningPubKey, "direct_from_cpfp_refund"); err != nil {
		return
	}

	signingKeyShare := depositAddress.Edges.SigningKeyshare
	if signingKeyShare == nil {
		err = fmt.Errorf("signing keyshare not found for deposit address")
		return
	}

	// For multi-UTXO deposits, all UTXOs (primary + additional) must be confirmed on-chain.
	// Each UTXO is verified individually against the Utxo table rather than relying on
	// depositAddress.AvailabilityConfirmedAt, which only indicates that some UTXO to that
	// address was confirmed — not necessarily the primary UTXO in this request.
	if len(additionalUtxos) > 0 {
		// Build list of all UTXOs to verify: primary first, then additional
		type utxoToVerify struct {
			label string
			txID  string
			vout  uint32
		}
		allUtxos := make([]utxoToVerify, 0, 1+len(additionalUtxos))
		allUtxos = append(allUtxos, utxoToVerify{
			label: "primary",
			txID:  onChainTx.TxID(),
			vout:  req.OnChainUtxo.Vout,
		})
		for i, add := range additionalUtxos {
			allUtxos = append(allUtxos, utxoToVerify{
				label: fmt.Sprintf("additional utxo %d", i),
				txID:  add.onChainTx.TxID(),
				vout:  add.vout,
			})
		}

		for _, u := range allUtxos {
			var txidBytes []byte
			txidBytes, err = hex.DecodeString(u.txID)
			if err != nil {
				err = fmt.Errorf("failed to encode %s txid: %w", u.label, err)
				return
			}
			var utxoEntity *ent.Utxo
			utxoEntity, err = db.Utxo.Query().
				Where(entutxo.NetworkEQ(network)).
				Where(entutxo.Txid(txidBytes)).
				Where(entutxo.Vout(u.vout)).
				Only(ctx)
			if err != nil {
				if ent.IsNotFound(err) {
					err = fmt.Errorf("%s utxo not found on-chain", u.label)
					return
				}
				err = fmt.Errorf("failed to query %s utxo: %w", u.label, err)
				return
			}
			if utxoEntity.AvailabilityConfirmedAt == nil {
				err = fmt.Errorf("%s utxo is not yet confirmed", u.label)
				return
			}
		}
	}

	// For single-input deposits, verify the primary UTXO matches the confirmed
	// on-chain deposit. Without this check, an attacker could supply fabricated
	// raw tx bytes paying to a valid deposit address with an inflated value,
	// and the server would create a tree backed by a UTXO that doesn't exist.
	if len(additionalUtxos) == 0 && depositAddress.ConfirmationTxid != "" {
		onChainTxid := onChainTx.TxHash().String()
		if onChainTxid != depositAddress.ConfirmationTxid {
			err = fmt.Errorf("primary utxo txid %s does not match confirmed deposit txid %s", onChainTxid, depositAddress.ConfirmationTxid)
			return
		}
	}

	// Cross-check the claimed UTXO value against the chain-watcher's Utxo table
	// to prevent balance inflation from fabricated raw TX bytes.
	if err = validateDepositUtxoValueAgainstChain(ctx, db, network, onChainTx, req.OnChainUtxo.Vout, onChainOutput); err != nil {
		return
	}

	// Also validate additional UTXOs values against the chain
	for i, add := range additionalUtxos {
		if err = validateDepositUtxoValueAgainstChain(ctx, db, network, add.onChainTx, add.vout, add.onChainOutput); err != nil {
			err = fmt.Errorf("additional utxo %d: %w", i, err)
			return
		}
	}

	if len(additionalUtxos) == 0 {
		// Single-input: full validation of root tx, refund txs, and direct txs
		combinedPublicKey := signingKeyShare.PublicKey.Add(depositAddress.OwnerSigningPubkey)
		err = validateBitcoinTransactions(
			ctx,
			req.OnChainUtxo.RawTx,
			req.OnChainUtxo.Vout,
			req.RootTxSigningJob.RawTx,
			req.RefundTxSigningJob.RawTx,
			req.DirectFromCpfpRefundTxSigningJob.RawTx,
			nil, // directRootTx - not used in FinalizeDepositTreeCreation
			nil, // directRefundTx - not used in FinalizeDepositTreeCreation
			combinedPublicKey,
			depositAddress.OwnerSigningPubkey,
			network.String(),
		)
		if err != nil {
			err = fmt.Errorf("failed to validate transaction in tree creation request: %w", err)
			return
		}
	} else {
		// Multi-input: root tx is validated separately by verifyMultiInputRootTransaction.
		// Refund txs spend root tx output[0] and are independent of the number of root inputs,
		// so we validate them with the standard reconstruction check.
		cpfpTimelock := spark.InitialTimeLock + spark.TimeLockInterval
		networkStr := network.String()
		err = bitcointransaction.VerifyTransactionWithSource(
			ctx, req.RefundTxSigningJob.RawTx, req.RootTxSigningJob.RawTx,
			0, cpfpTimelock, bitcointransaction.TxTypeRefundCPFP,
			depositAddress.OwnerSigningPubkey, networkStr,
		)
		if err != nil {
			err = fmt.Errorf("cpfp refund transaction verification failed: %w", err)
			return
		}
		err = bitcointransaction.VerifyTransactionWithSource(
			ctx, req.DirectFromCpfpRefundTxSigningJob.RawTx, req.RootTxSigningJob.RawTx,
			0, cpfpTimelock, bitcointransaction.TxTypeRefundDirectFromCPFP,
			depositAddress.OwnerSigningPubkey, networkStr,
		)
		if err != nil {
			err = fmt.Errorf("direct-from-cpfp refund transaction verification failed: %w", err)
			return
		}
	}

	return
}

// verifyMultiInputRootTransaction validates a multi-input root tx by reconstructing
// the expected transaction server-side and comparing byte-for-byte.
// Input ordering convention: primary UTXO first, then additional UTXOs in request array order.
func verifyMultiInputRootTransaction(
	clientRootTx *wire.MsgTx,
	onChainTx *wire.MsgTx,
	onChainVout uint32,
	onChainOutput *wire.TxOut,
	additionalUtxos []additionalUtxoData,
) error {
	if err := common.ValidateBitcoinTxVersion(clientRootTx); err != nil {
		return fmt.Errorf("root tx version validation failed: %w", err)
	}

	// Reconstruct expected root transaction
	expectedTx := wire.NewMsgTx(3)

	// Input 0: primary UTXO
	primaryTxHash := onChainTx.TxHash()
	expectedTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: primaryTxHash, Index: onChainVout},
		Sequence:         spark.ZeroSequence,
	})
	totalValue := onChainOutput.Value

	// Inputs 1..N: additional UTXOs in request array order
	for _, add := range additionalUtxos {
		addHash := add.onChainTx.TxHash()
		expectedTx.AddTxIn(&wire.TxIn{
			PreviousOutPoint: wire.OutPoint{Hash: addHash, Index: add.vout},
			Sequence:         spark.ZeroSequence,
		})
		totalValue += add.onChainOutput.Value
	}

	// Output 0: combined value, same pkScript as deposit address
	expectedTx.AddTxOut(wire.NewTxOut(totalValue, onChainOutput.PkScript))
	// Output 1: ephemeral anchor
	expectedTx.AddTxOut(common.EphemeralAnchorOutput())

	// Compare structurally first for a helpful error message
	if err := common.CompareTransactions(expectedTx, clientRootTx); err != nil {
		return fmt.Errorf("multi-input root tx does not match expected construction: %w", err)
	}

	// Byte-level comparison without witness data
	expectedBytes, err := common.SerializeTxNoWitness(expectedTx)
	if err != nil {
		return fmt.Errorf("failed to serialize expected root tx: %w", err)
	}
	clientBytes, err := common.SerializeTxNoWitness(clientRootTx)
	if err != nil {
		return fmt.Errorf("failed to serialize client root tx: %w", err)
	}
	if !bytes.Equal(expectedBytes, clientBytes) {
		return fmt.Errorf("multi-input root tx bytes do not match expected construction")
	}

	return nil
}

// prepareSigningJobs creates signing jobs for all transactions.
// For multi-input root transactions, it creates one signing job per root tx input,
// followed by 1 refund job and 1 directFromCpfpRefund job.
// rootTxInputCount returns the number of root tx signing jobs created.
func prepareSigningJobs(
	req *pb.FinalizeDepositTreeCreationRequest,
	depositAddress *ent.DepositAddress,
	onChainTx *wire.MsgTx,
	onChainOutput *wire.TxOut,
	additionalUtxos []additionalUtxoData,
) (signingJobs []*helper.SigningJob, verifyingKey keys.Public, rootTxInputCount int, err error) {
	// Parse and validate root transaction
	cpfpRootTx, err := common.TxFromRawTxBytes(req.RootTxSigningJob.RawTx)
	if err != nil {
		err = fmt.Errorf("invalid root transaction: %w", err)
		return
	}

	isMultiInput := len(additionalUtxos) > 0
	if isMultiInput {
		if err = verifyMultiInputRootTransaction(cpfpRootTx, onChainTx, req.OnChainUtxo.Vout, onChainOutput, additionalUtxos); err != nil {
			err = fmt.Errorf("multi-input root transaction verification failed: %w", err)
			return
		}
	} else {
		if err = verifyRootTransaction(cpfpRootTx, onChainTx, req.OnChainUtxo.Vout, false); err != nil {
			err = fmt.Errorf("root transaction verification failed: %w", err)
			return
		}
	}

	// Parse and validate refund transaction
	cpfpRefundTx, err := common.TxFromRawTxBytes(req.RefundTxSigningJob.RawTx)
	if err != nil {
		err = fmt.Errorf("invalid refund transaction: %w", err)
		return
	}
	if err = verifyRefundTransaction(cpfpRootTx, cpfpRefundTx); err != nil {
		err = fmt.Errorf("cpfp refund verification failed: %w", err)
		return
	}

	// Get keyshare and verifying key (nil-check already done in loadAndValidateDepositAddress)
	signingKeyShare := depositAddress.Edges.SigningKeyshare
	verifyingKey = signingKeyShare.PublicKey.Add(depositAddress.OwnerSigningPubkey)

	// Build root tx signing jobs (one per input)
	rootTxInputCount = len(cpfpRootTx.TxIn)
	if isMultiInput {
		// Build prevOutputs map for multi-input sighash computation
		prevOutputs := make(map[wire.OutPoint]*wire.TxOut)
		primaryTxHash := onChainTx.TxHash()
		prevOutputs[wire.OutPoint{Hash: primaryTxHash, Index: req.OnChainUtxo.Vout}] = onChainOutput
		for _, add := range additionalUtxos {
			addHash := add.onChainTx.TxHash()
			prevOutputs[wire.OutPoint{Hash: addHash, Index: add.vout}] = add.onChainOutput
		}

		for i := range rootTxInputCount {
			var sigHash []byte
			sigHash, err = common.SigHashFromMultiPrevOutTx(cpfpRootTx, i, prevOutputs)
			if err != nil {
				err = fmt.Errorf("failed to compute root tx sighash for input %d: %w", i, err)
				return
			}

			var userCommitment frost.SigningCommitment
			if i == 0 {
				// Input 0 uses existing fields
				if err = userCommitment.UnmarshalProto(req.RootTxSigningJob.SigningNonceCommitment); err != nil {
					err = fmt.Errorf("invalid root tx signing commitment for input 0: %w", err)
					return
				}
			} else {
				// Inputs 1..N use additional_inputs
				addInput := req.RootTxSigningJob.AdditionalInputs[i-1]
				if err = userCommitment.UnmarshalProto(addInput.SigningNonceCommitment); err != nil {
					err = fmt.Errorf("invalid root tx signing commitment for input %d: %w", i, err)
					return
				}
			}

			signingJobs = append(signingJobs, &helper.SigningJob{
				JobID:             uuid.New(),
				SigningKeyshareID: signingKeyShare.ID,
				Message:           sigHash,
				VerifyingKey:      &verifyingKey,
				UserCommitment:    &userCommitment,
			})
		}
	} else {
		// Single-input: original behavior
		var cpfpRootTxSigHash []byte
		cpfpRootTxSigHash, err = common.SigHashFromTx(cpfpRootTx, 0, onChainOutput)
		if err != nil {
			err = fmt.Errorf("failed to compute root tx sighash: %w", err)
			return
		}

		userCpfpRootTxCommitment := frost.SigningCommitment{}
		if err = userCpfpRootTxCommitment.UnmarshalProto(req.RootTxSigningJob.SigningNonceCommitment); err != nil {
			err = fmt.Errorf("invalid root tx signing commitment: %w", err)
			return
		}

		signingJobs = append(signingJobs, &helper.SigningJob{
			JobID:             uuid.New(),
			SigningKeyshareID: signingKeyShare.ID,
			Message:           cpfpRootTxSigHash,
			VerifyingKey:      &verifyingKey,
			UserCommitment:    &userCpfpRootTxCommitment,
		})
	}

	// Refund tx signing job
	cpfpRefundTxSigHash, err := common.SigHashFromTx(cpfpRefundTx, 0, cpfpRootTx.TxOut[0])
	if err != nil {
		err = fmt.Errorf("failed to compute refund tx sighash: %w", err)
		return
	}

	userCpfpRefundTxCommitment := frost.SigningCommitment{}
	if err = userCpfpRefundTxCommitment.UnmarshalProto(req.RefundTxSigningJob.SigningNonceCommitment); err != nil {
		err = fmt.Errorf("invalid refund tx signing commitment: %w", err)
		return
	}

	signingJobs = append(signingJobs, &helper.SigningJob{
		JobID:             uuid.New(),
		SigningKeyshareID: signingKeyShare.ID,
		Message:           cpfpRefundTxSigHash,
		VerifyingKey:      &verifyingKey,
		UserCommitment:    &userCpfpRefundTxCommitment,
	})

	// DirectFromCpfpRefund tx signing job
	directFromCpfpRefundTx, err := common.TxFromRawTxBytes(req.DirectFromCpfpRefundTxSigningJob.RawTx)
	if err != nil {
		err = fmt.Errorf("invalid direct from cpfp refund transaction: %w", err)
		return
	}
	if err = verifyRefundTransaction(cpfpRootTx, directFromCpfpRefundTx); err != nil {
		err = fmt.Errorf("direct from cpfp refund verification failed: %w", err)
		return
	}
	directFromCpfpRefundTxSigHash, err := common.SigHashFromTx(directFromCpfpRefundTx, 0, cpfpRootTx.TxOut[0])
	if err != nil {
		err = fmt.Errorf("failed to compute direct from cpfp refund tx sighash: %w", err)
		return
	}

	userDirectFromCpfpRefundTxCommitment := frost.SigningCommitment{}
	if err = userDirectFromCpfpRefundTxCommitment.UnmarshalProto(req.DirectFromCpfpRefundTxSigningJob.SigningNonceCommitment); err != nil {
		err = fmt.Errorf("invalid direct from cpfp refund tx signing commitment: %w", err)
		return
	}
	signingJobs = append(signingJobs, &helper.SigningJob{
		JobID:             uuid.New(),
		SigningKeyshareID: signingKeyShare.ID,
		Message:           directFromCpfpRefundTxSigHash,
		VerifyingKey:      &verifyingKey,
		UserCommitment:    &userDirectFromCpfpRefundTxCommitment,
	})

	return
}

// aggregateSignatures aggregates SE and user signature shares.
// rootTxInputCount indicates how many of the initial signing results are for root tx inputs.
// Returns signatures in the same order as signingResults.
func aggregateDepositSignatures(
	ctx context.Context,
	config *so.Config,
	req *pb.FinalizeDepositTreeCreationRequest,
	signingResults []*helper.SigningResult,
	verifyingKey keys.Public,
	rootSigningPubKey keys.Public,
	rootTxInputCount int,
) ([][]byte, error) {
	frostConn, err := config.NewFrostGRPCConnection()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to FROST signer: %w", err)
	}
	defer frostConn.Close()

	frostClient := pbfrost.NewFrostServiceClient(frostConn)
	logger := logging.GetLoggerFromContext(ctx)

	signatures := make([][]byte, len(signingResults))

	// Aggregate root transaction signatures (one per input)
	for i := range rootTxInputCount {
		logger.Sugar().Infof("Aggregating cpfp root tx signature for input %d", i)

		var commitments map[string]*pbcommon.SigningCommitment
		var userCommitment *pbcommon.SigningCommitment
		var userSignature []byte
		if i == 0 {
			commitments = req.RootTxSigningJob.SigningCommitments.SigningCommitments
			userCommitment = req.RootTxSigningJob.SigningNonceCommitment
			userSignature = req.RootTxSigningJob.UserSignature
		} else {
			addInput := req.RootTxSigningJob.AdditionalInputs[i-1]
			commitments = addInput.SigningCommitments.SigningCommitments
			userCommitment = addInput.SigningNonceCommitment
			userSignature = addInput.UserSignature
		}

		result, err := frostClient.AggregateFrost(ctx, &pbfrost.AggregateFrostRequest{
			Message:            signingResults[i].Message,
			SignatureShares:    signingResults[i].SignatureShares,
			PublicShares:       signingResults[i].PublicKeys,
			VerifyingKey:       verifyingKey.Serialize(),
			Commitments:        commitments,
			UserCommitments:    userCommitment,
			UserPublicKey:      rootSigningPubKey.Serialize(),
			UserSignatureShare: userSignature,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to aggregate root tx signature for input %d: %w", i, err)
		}
		signatures[i] = result.Signature
	}

	// Aggregate refund transaction signature
	refundIdx := rootTxInputCount
	logger.Sugar().Infof("Aggregating cpfp refund tx signature")
	refundSigResult, err := frostClient.AggregateFrost(ctx, &pbfrost.AggregateFrostRequest{
		Message:            signingResults[refundIdx].Message,
		SignatureShares:    signingResults[refundIdx].SignatureShares,
		PublicShares:       signingResults[refundIdx].PublicKeys,
		VerifyingKey:       verifyingKey.Serialize(),
		Commitments:        req.RefundTxSigningJob.SigningCommitments.SigningCommitments,
		UserCommitments:    req.RefundTxSigningJob.SigningNonceCommitment,
		UserPublicKey:      rootSigningPubKey.Serialize(),
		UserSignatureShare: req.RefundTxSigningJob.UserSignature,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate refund tx signature: %w", err)
	}
	signatures[refundIdx] = refundSigResult.Signature

	// Aggregate DirectFromCpfpRefund signature
	directIdx := rootTxInputCount + 1
	logger.Sugar().Infof("Aggregating direct from cpfp refund tx signature")
	directFromCpfpRefundSigResult, err := frostClient.AggregateFrost(ctx, &pbfrost.AggregateFrostRequest{
		Message:            signingResults[directIdx].Message,
		SignatureShares:    signingResults[directIdx].SignatureShares,
		PublicShares:       signingResults[directIdx].PublicKeys,
		VerifyingKey:       verifyingKey.Serialize(),
		Commitments:        req.DirectFromCpfpRefundTxSigningJob.SigningCommitments.SigningCommitments,
		UserCommitments:    req.DirectFromCpfpRefundTxSigningJob.SigningNonceCommitment,
		UserPublicKey:      rootSigningPubKey.Serialize(),
		UserSignatureShare: req.DirectFromCpfpRefundTxSigningJob.UserSignature,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate direct from cpfp refund tx signature: %w", err)
	}
	signatures[directIdx] = directFromCpfpRefundSigResult.Signature

	return signatures, nil
}

// applySignaturesToTransactions applies aggregated signatures to the raw transactions.
// rootTxInputCount indicates how many of the initial signatures are for root tx inputs.
func applySignaturesToTransactions(
	req *pb.FinalizeDepositTreeCreationRequest,
	signatures [][]byte,
	rootTxInputCount int,
) (signedCpfpRootTx []byte, signedCpfpRefundTx []byte, signedDirectFromCpfpRefundTx []byte, err error) {
	// Apply signatures to CPFP root transaction (one per input)
	signedCpfpRootTx = req.RootTxSigningJob.RawTx
	for i := range rootTxInputCount {
		signedCpfpRootTx, err = common.UpdateTxWithSignature(signedCpfpRootTx, i, signatures[i])
		if err != nil {
			err = fmt.Errorf("failed to apply signature to cpfp root tx input %d: %w", i, err)
			return
		}
	}

	// Apply signature to CPFP refund transaction
	refundIdx := rootTxInputCount
	signedCpfpRefundTx, err = common.UpdateTxWithSignature(req.RefundTxSigningJob.RawTx, 0, signatures[refundIdx])
	if err != nil {
		err = fmt.Errorf("failed to apply signature to cpfp refund tx: %w", err)
		return
	}

	// Apply signature to DirectFromCpfpRefund transaction
	directIdx := rootTxInputCount + 1
	signedDirectFromCpfpRefundTx, err = common.UpdateTxWithSignature(req.DirectFromCpfpRefundTxSigningJob.RawTx, 0, signatures[directIdx])
	if err != nil {
		err = fmt.Errorf("failed to apply signature to direct from cpfp refund tx: %w", err)
		return
	}

	return
}

// verifySignedTransactions verifies all signed transactions using the Bitcoin script engine.
// For the root tx (which may have multiple inputs), it verifies each input against its prev output.
// For the refund txs (single input each), it verifies against the root tx output.
func verifySignedTransactions(
	signedCpfpRootTxBytes []byte,
	signedCpfpRefundTxBytes []byte,
	signedDirectFromCpfpRefundTxBytes []byte,
	onChainTx *wire.MsgTx,
	onChainOutput *wire.TxOut,
	additionalUtxos []additionalUtxoData,
) error {
	// Verify root tx signatures against on-chain prev outputs
	signedRootTx, err := common.TxFromRawTxBytes(signedCpfpRootTxBytes)
	if err != nil {
		return fmt.Errorf("failed to parse signed root tx: %w", err)
	}

	if len(signedRootTx.TxIn) == 1 {
		// Single-input: use simpler verification
		if err := common.VerifySignatureSingleInput(signedRootTx, 0, onChainOutput); err != nil {
			return fmt.Errorf("root tx signature verification failed: %w", err)
		}
	} else {
		// Multi-input: build prev output fetcher for all inputs
		expectedInputCount := len(additionalUtxos) + 1
		if len(signedRootTx.TxIn) != expectedInputCount {
			return fmt.Errorf("signed root tx has %d inputs, expected %d", len(signedRootTx.TxIn), expectedInputCount)
		}
		prevOutMap := make(map[wire.OutPoint]*wire.TxOut)
		primaryOutPoint := signedRootTx.TxIn[0].PreviousOutPoint
		prevOutMap[primaryOutPoint] = onChainOutput
		for i, utxo := range additionalUtxos {
			outPoint := signedRootTx.TxIn[i+1].PreviousOutPoint
			prevOutMap[outPoint] = utxo.onChainOutput
		}
		fetcher := txscript.NewMultiPrevOutFetcher(prevOutMap)
		if err := common.VerifySignatureMultiInput(signedRootTx, fetcher); err != nil {
			return fmt.Errorf("root tx multi-input signature verification failed: %w", err)
		}
	}

	// Verify CPFP refund tx signature against root tx output
	signedRefundTx, err := common.TxFromRawTxBytes(signedCpfpRefundTxBytes)
	if err != nil {
		return fmt.Errorf("failed to parse signed refund tx: %w", err)
	}
	if err := common.VerifySignatureSingleInput(signedRefundTx, 0, signedRootTx.TxOut[0]); err != nil {
		return fmt.Errorf("cpfp refund tx signature verification failed: %w", err)
	}

	// Verify direct-from-cpfp refund tx signature against root tx output
	if len(signedDirectFromCpfpRefundTxBytes) > 0 {
		signedDirectFromCpfpRefundTx, err := common.TxFromRawTxBytes(signedDirectFromCpfpRefundTxBytes)
		if err != nil {
			return fmt.Errorf("failed to parse signed direct-from-cpfp refund tx: %w", err)
		}
		if err := common.VerifySignatureSingleInput(signedDirectFromCpfpRefundTx, 0, signedRootTx.TxOut[0]); err != nil {
			return fmt.Errorf("direct-from-cpfp refund tx signature verification failed: %w", err)
		}
	}

	return nil
}

// createTreeAndNode creates the tree and root node in the database.
// For multi-UTXO deposits, the root node value is the sum of all UTXO amounts.
// UTXO confirmation is enforced during validation in loadAndValidateDepositAddress.
func createTreeAndNode(
	ctx context.Context,
	config *so.Config,
	depositAddress *ent.DepositAddress,
	onChainTx *wire.MsgTx,
	onChainOutput *wire.TxOut,
	additionalUtxos []additionalUtxoData,
	vout uint32,
	network btcnetwork.Network,
	verifyingKey keys.Public,
	signedCpfpRootTx []byte,
	signedCpfpRefundTx []byte,
	signedDirectFromCpfpRefundTx []byte,
) (*ent.Tree, *ent.TreeNode, error) {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get database: %w", err)
	}

	logger := logging.GetLoggerFromContext(ctx)
	txid := onChainTx.TxHash()

	// Check if tree already exists
	existingTree, err := db.Tree.Query().
		Where(tree.BaseTxid(st.NewTxID(txid))).
		Where(tree.Vout(int16(vout))).
		WithRoot().
		First(ctx)

	if err != nil && !ent.IsNotFound(err) {
		return nil, nil, fmt.Errorf("failed to query for existing tree: %w", err)
	}

	if existingTree != nil {
		logger.Sugar().Warnf("Tree already exists for txid %s vout %d", txid.String(), vout)

		// Use the Tree→Root relationship to get the root node
		if existingTree.Edges.Root != nil {
			return existingTree, existingTree.Edges.Root, nil
		}

		// If Root edge is not populated, query for the root node belonging to this tree
		rootNode, err := db.TreeNode.Query().
			Where(treenode.HasTreeWith(tree.ID(existingTree.ID))).
			Where(treenode.Not(treenode.HasParent())).
			First(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to find root node for existing tree: %w", err)
		}
		return existingTree, rootNode, nil
	}

	// Calculate total value from all UTXOs
	totalValue := uint64(onChainOutput.Value)
	for _, add := range additionalUtxos {
		totalValue += uint64(add.onChainOutput.Value)
	}

	// Determine tree status based on deposit confirmation.
	// For multi-UTXO deposits, all UTXOs are already confirmed (enforced in
	// loadAndValidateDepositAddress), so the tree starts as Available.
	// For single-UTXO deposits, rely on durable confirmation state instead of
	// just availability_confirmed_at on the locked deposit address row. The
	// chain watcher can determine that the deposit has enough confirmations
	// before its UPDATE on deposit_addresses commits, and this handler may still
	// be holding a FOR UPDATE lock on that row.
	depositConfirmed := len(additionalUtxos) > 0
	if !depositConfirmed {
		depositConfirmed, err = isDepositUtxoAvailableForTreeCreation(
			ctx,
			config,
			network,
			depositAddress,
			onChainTx,
			vout,
		)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to determine deposit confirmation state: %w", err)
		}
	}

	var treeStatus st.TreeStatus
	var treeNodeStatus st.TreeNodeStatus
	if !depositConfirmed {
		treeStatus = st.TreeStatusPending
		treeNodeStatus = st.TreeNodeStatusCreating
	} else {
		treeStatus = st.TreeStatusAvailable
		treeNodeStatus = st.TreeNodeStatusAvailable
	}

	// Create new tree following StartDepositTreeCreation pattern
	signingKeyShare := depositAddress.Edges.SigningKeyshare

	// Create tree with Pending status if the DepositAddress is not available yet.
	// chain watcher will mark it Available after confirming the transaction and
	// verifying signatures
	newTree := db.Tree.Create().
		SetOwnerIdentityPubkey(depositAddress.OwnerIdentityPubkey).
		SetNetwork(network).
		SetBaseTxid(st.NewTxID(txid)).
		SetVout(int16(vout)).
		SetDepositAddress(depositAddress).
		SetStatus(treeStatus)

	createdTree, err := newTree.Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, nil, errors.AlreadyExistsDuplicateOperation(fmt.Errorf("tree already exists: %w", err))
		}
		return nil, nil, err
	}

	// Create root node with signed transactions.
	// Deposit root nodes are always zero-timelock nodes, so no direct tx is needed.
	rootNode := db.TreeNode.Create().
		SetTree(createdTree).
		SetNetwork(network).
		SetStatus(treeNodeStatus).
		SetOwnerIdentityPubkey(depositAddress.OwnerIdentityPubkey).
		SetOwnerSigningPubkey(depositAddress.OwnerSigningPubkey).
		SetValue(totalValue).
		SetVerifyingPubkey(verifyingKey).
		SetSigningKeyshare(signingKeyShare).
		SetRawTx(signedCpfpRootTx).
		SetRawRefundTx(signedCpfpRefundTx).
		SetDirectFromCpfpRefundTx(signedDirectFromCpfpRefundTx).
		SetVout(int16(vout))

	if depositAddress.NodeID != uuid.Nil {
		rootNode.SetID(depositAddress.NodeID)
	}

	createdNode, err := rootNode.Save(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create root node: %w", err)
	}

	logger.Sugar().Infof("Created root node %s for tree %s", createdNode.ID, createdTree.ID)

	// Update tree with root node
	createdTree, err = createdTree.Update().SetRoot(createdNode).Save(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to update tree with root: %w", err)
	}

	return createdTree, createdNode, nil
}

// convertToSigningJobsWithPregeneratedNonce converts signing jobs to jobs with pregenerated nonces
// using the SE commitments provided by the client.
// rootTxInputCount indicates how many of the initial signing jobs are for root tx inputs.
func convertToSigningJobsWithPregeneratedNonce(
	signingJobs []*helper.SigningJob,
	req *pb.FinalizeDepositTreeCreationRequest,
	rootTxInputCount int,
) ([]*helper.SigningJobWithPregeneratedNonce, error) {
	result := make([]*helper.SigningJobWithPregeneratedNonce, len(signingJobs))

	// Root transaction signing jobs (one per input)
	for i := range rootTxInputCount {
		var commitmentsMap map[string]*pbcommon.SigningCommitment
		if i == 0 {
			commitmentsMap = req.RootTxSigningJob.SigningCommitments.SigningCommitments
		} else {
			commitmentsMap = req.RootTxSigningJob.AdditionalInputs[i-1].SigningCommitments.SigningCommitments
		}

		commitments := make(map[string]frost.SigningCommitment)
		for key, commitment := range commitmentsMap {
			obj := frost.SigningCommitment{}
			if err := obj.UnmarshalProto(commitment); err != nil {
				return nil, fmt.Errorf("failed to unmarshal root tx SE commitment for input %d key %s: %w", i, key, err)
			}
			commitments[key] = obj
		}
		result[i] = &helper.SigningJobWithPregeneratedNonce{
			SigningJob:     *signingJobs[i],
			Round1Packages: commitments,
		}
	}

	// Refund transaction (at index rootTxInputCount)
	refundIdx := rootTxInputCount
	refundCommitments := make(map[string]frost.SigningCommitment)
	for key, commitment := range req.RefundTxSigningJob.SigningCommitments.SigningCommitments {
		obj := frost.SigningCommitment{}
		if err := obj.UnmarshalProto(commitment); err != nil {
			return nil, fmt.Errorf("failed to unmarshal refund tx SE commitment for key %s: %w", key, err)
		}
		refundCommitments[key] = obj
	}
	result[refundIdx] = &helper.SigningJobWithPregeneratedNonce{
		SigningJob:     *signingJobs[refundIdx],
		Round1Packages: refundCommitments,
	}

	// DirectFromCpfpRefund transaction (at index rootTxInputCount + 1)
	directIdx := rootTxInputCount + 1
	directFromCpfpRefundCommitments := make(map[string]frost.SigningCommitment)
	for key, commitment := range req.DirectFromCpfpRefundTxSigningJob.SigningCommitments.SigningCommitments {
		obj := frost.SigningCommitment{}
		if err := obj.UnmarshalProto(commitment); err != nil {
			return nil, fmt.Errorf("failed to unmarshal direct from cpfp refund tx SE commitment for key %s: %w", key, err)
		}
		directFromCpfpRefundCommitments[key] = obj
	}
	result[directIdx] = &helper.SigningJobWithPregeneratedNonce{
		SigningJob:     *signingJobs[directIdx],
		Round1Packages: directFromCpfpRefundCommitments,
	}

	return result, nil
}

func (o *DepositHandler) sendFinalizeNodeGossip(
	ctx context.Context,
	tree *ent.Tree,
	rootNode *ent.TreeNode,
) error {
	selection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}
	participants, err := selection.OperatorIdentifierList(o.config)
	if err != nil {
		return fmt.Errorf("unable to get operator list: %w", err)
	}
	sendGossipHandler := NewSendGossipHandler(o.config)

	protoNetwork, err := tree.Network.ToProtoNetwork()
	if err != nil {
		return err
	}

	// Load the node with all required edges for marshaling
	// This must happen within the transaction before it commits
	// We MUST load all edges that MarshalInternalProto might query
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to get database: %w", err)
	}

	rootNodeWithEdges, err := db.TreeNode.Query().
		Where(treenode.ID(rootNode.ID)).
		WithTree().
		WithSigningKeyshare().
		WithParent(). // Load parent edge to avoid lazy loading in getParentNodeID
		Only(ctx)
	if err != nil {
		return fmt.Errorf("failed to load root node with edges: %w", err)
	}

	// Marshal the node BEFORE CreateCommitAndSendGossipMessage commits the transaction
	// This ensures all database queries happen within the active transaction
	internalNode, err := rootNodeWithEdges.MarshalInternalProto(ctx)
	if err != nil {
		return fmt.Errorf("failed to marshal root node %s: %w", rootNode.ID.String(), err)
	}
	internalNodes := []*spark_internal.TreeNode{internalNode}

	_, err = sendGossipHandler.CreateCommitAndSendGossipMessage(ctx, &pbgossip.GossipMessage{
		Message: &pbgossip.GossipMessage_FinalizeTreeCreation{
			FinalizeTreeCreation: &pbgossip.GossipMessageFinalizeTreeCreation{
				InternalNodes: internalNodes,
				ProtoNetwork:  protoNetwork,
			},
		},
	}, participants)
	if err != nil {
		return fmt.Errorf("unable to create and send gossip message: %w", err)
	}

	return nil
}
