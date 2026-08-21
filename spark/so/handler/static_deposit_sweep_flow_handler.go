package handler

import (
	"context"
	"encoding/hex"
	"fmt"

	"crypto/sha256"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/common/sighash"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pbspark "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authz"
	"github.com/lightsparkdev/spark/so/consensus"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	entutxo "github.com/lightsparkdev/spark/so/ent/utxo"
	entutxoswap "github.com/lightsparkdev/spark/so/ent/utxoswap"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/frost"
	"github.com/lightsparkdev/spark/so/handler/signing_handler"
	"github.com/lightsparkdev/spark/so/helper"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// maxSweepInputs mirrors the max_items rule on SignStaticDepositSweepTxRequest.
// The generated validator cannot re-apply that rule to a prepare op — it arrives
// wrapped in an Any, so PGV never sees its type — leaving the participant path
// bounded only by the gRPC request size. Re-checking it here keeps every operator
// enforcing the same limit as the coordinator, which matters because the
// coordinator is already untrusted for forgery on this path.
const maxSweepInputs = 200

// maxSatoshis is the total supply, the ceiling on any value a sweep may carry.
const maxSatoshis uint64 = 21_000_000 * 100_000_000

// sweepIneligibleReason is why an operator will not sign a spend of one UTXO.
// Mirrored onto the SSP-facing enum by the RPC layer.
type sweepIneligibleReason int

const (
	sweepUnknownUtxo sweepIneligibleReason = iota
	sweepNoSwap
	sweepSwapNotCompleted
	sweepNotOwnedByCaller
	sweepRefundSwap
)

func (r sweepIneligibleReason) String() string {
	switch r {
	case sweepUnknownUtxo:
		return "unknown utxo"
	case sweepNoSwap:
		return "no swap"
	case sweepSwapNotCompleted:
		return "swap not completed"
	case sweepNotOwnedByCaller:
		return "not owned by caller"
	case sweepRefundSwap:
		return "refund swap"
	default:
		return fmt.Sprintf("unknown reason %d", int(r))
	}
}

// sweepRefusal names one input this operator declines to sign a spend of.
type sweepRefusal struct {
	utxo   *pbspark.UTXO
	reason sweepIneligibleReason
}

// sweepInput is one requested input after it has been bound to the transaction
// and resolved against this operator's records.
type sweepInput struct {
	vin uint32
	// Checked to match the UTXO the caller named at this vin.
	outpoint  wire.OutPoint
	requested *pbinternal.StaticDepositSweepInput
	utxo      *ent.Utxo
}

func (in *sweepInput) jobID() uuid.UUID {
	return staticDepositSweepJobID(in.requested.GetOnChainUtxo().GetTxid(), in.outpoint.Index)
}

// StaticDepositSweepFlowHandler implements consensus.FlowHandler for
// CONSENSUS_OPERATION_TYPE_STATIC_DEPOSIT_SWEEP, which co-signs one transaction
// sweeping many already-claimed static deposit UTXOs into the owning SSP's wallet.
//
// Routing the signing through the engine is what makes the eligibility rules
// binding. A direct FROST fan-out has every other operator produce a share for
// whatever message the coordinator hands it, leaving the checks coordinator-local;
// here each SO re-derives the prevouts from its own Utxo rows, re-runs the checks,
// and one dissenter aborts the sweep.
//
// Round-1 commitments are collected by the coordinator before Execute and carried
// in the prepare op, so round-2 runs inside Prepare — the engine's fan-out is the
// round-2 trip — and the public RPC stays a single call.
type StaticDepositSweepFlowHandler struct {
	config *so.Config
}

var _ consensus.FlowHandler = (*StaticDepositSweepFlowHandler)(nil)

func NewStaticDepositSweepFlowHandler(config *so.Config) *StaticDepositSweepFlowHandler {
	return &StaticDepositSweepFlowHandler{config: config}
}

// staticDepositSweepJobNamespace is a fixed UUIDv4 mixed into NewSHA1 to derive a
// deterministic per-input signing-job id from the outpoint, so every SO and the
// coordinator correlate round-2 shares without sending job ids over the wire.
var staticDepositSweepJobNamespace = uuid.MustParse("6f2a9c41-5b8e-4d07-a3c2-1e9f4b6d8057")

// txid is the raw bytes from the proto UTXO field (reversed display order), which
// is what participants and coordinator both pass. Outpoints are unique within a
// sweep (bindSweepInputsToTx rejects repeats), so ids never collide inside one
// Execute — the only scope in which shares are correlated.
func staticDepositSweepJobID(txid []byte, vout uint32) uuid.UUID {
	return uuid.NewSHA1(staticDepositSweepJobNamespace, fmt.Appendf(nil, "%x:%d", txid, vout))
}

// CreateStaticDepositSweepStatement builds the hash an SSP signs to authorize a
// sweep. The txid commits to every input the transaction spends and every output
// it pays, so one signature authorizes exactly this sweep and nothing else.
func CreateStaticDepositSweepStatement(network btcnetwork.Network, sweepTxid chainhash.Hash) []byte {
	hasher := sha256.New()
	// Writing to a sha256 never returns an error.
	_, _ = hasher.Write([]byte("sweep_static_deposits"))
	_, _ = hasher.Write([]byte(network.String()))
	_, _ = hasher.Write(sweepTxid[:])
	return hasher.Sum(nil)
}

// preparedSweep is an operator's validated view of a sweep: which inputs it will
// sign, and the exact message for each.
type preparedSweep struct {
	tx        *wire.MsgTx
	inputs    []*sweepInput
	sighashes map[uuid.UUID]sighash.Hash
}

// Prepare runs on every SO. Any input this SO will not sign a spend of fails the
// whole prepare: unlike the coordinator's pre-check, which reports ineligible
// inputs so the caller can drop them and rebuild, a disagreement here means
// operators hold different views of the same UTXO and the sweep must not proceed.
func (h *StaticDepositSweepFlowHandler) Prepare(ctx context.Context, op proto.Message) (proto.Message, error) {
	req, ok := op.(*pbinternal.StaticDepositSweepPrepareRequest)
	if !ok {
		return nil, fmt.Errorf("unexpected operation type %T for static deposit sweep prepare", op)
	}
	sspIdentityPubKey, err := keys.ParsePublicKey(req.GetSspIdentityPublicKey())
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("invalid ssp identity public key: %w", err))
	}
	network, err := btcnetwork.FromProtoNetwork(req.GetNetwork())
	if err != nil {
		return nil, sparkerrors.InvalidArgumentNetworkNotSupported(fmt.Errorf("invalid network: %w", err))
	}
	// Enforced per operator, not just on the coordinator, so freezing a wallet on
	// every SO actually stops the sweep rather than relying on the coordinator to
	// honour it.
	if err := authz.EnforceWalletNotKillSwitched(ctx, sspIdentityPubKey); err != nil {
		return nil, err
	}

	prepared, refusals, err := PrepareSweep(ctx, network, req.GetRawTx(), req.GetInputs(), sspIdentityPubKey, req.GetSspSignature())
	if err != nil {
		return nil, err
	}
	if len(refusals) > 0 {
		return nil, sparkerrors.FailedPreconditionInvalidState(fmt.Errorf(
			"refusing to sign sweep: %d of %d inputs are not sweepable by this operator, first is %x:%d (%s)",
			len(refusals), len(req.GetInputs()),
			refusals[0].utxo.GetTxid(), refusals[0].utxo.GetVout(), refusals[0].reason,
		))
	}

	// Operators outside the coordinator's round-1 set contribute no share; the
	// engine still counts their prepare as a vote on eligibility. Membership is
	// all-or-nothing across the sweep, so a payload that includes this operator on
	// only some inputs is rejected here rather than reaching FROST with a job that
	// has no commitments.
	inSigningSet, err := sweepSigningSetMembership(req.GetInputs(), h.config.Identifier)
	if err != nil {
		return nil, err
	}
	if !inSigningSet {
		return nil, nil
	}
	jobs, err := h.buildSweepRound2Jobs(req.GetInputs(), prepared)
	if err != nil {
		return nil, err
	}
	frostResp, err := signing_handler.NewFrostSigningHandler(h.config).FrostRound2(ctx, &pbinternal.FrostRound2Request{SigningJobs: jobs})
	if err != nil {
		return nil, fmt.Errorf("local frost round 2 failed during prepare: %w", err)
	}
	return frostResp, nil
}

// PrepareSweep verifies the caller's authorization, binds the requested inputs to
// the transaction, resolves each against this operator's own records, and
// computes the per-input sighashes. A non-empty refusal list means the caller
// must drop those inputs and rebuild; prepared is nil in that case.
func PrepareSweep(ctx context.Context, network btcnetwork.Network, rawTx []byte, requested []*pbinternal.StaticDepositSweepInput, sspIdentityPubKey keys.Public, sspSignature []byte) (*preparedSweep, []sweepRefusal, error) {
	tx, err := common.TxFromRawTxBytes(rawTx)
	if err != nil {
		return nil, nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("failed to parse sweep transaction: %w", err))
	}
	if err := common.VerifyECDSASignature(sspIdentityPubKey, sspSignature, CreateStaticDepositSweepStatement(network, tx.TxHash())); err != nil {
		return nil, nil, sparkerrors.FailedPreconditionBadSignature(fmt.Errorf("ssp sweep signature validation failed: %w", err))
	}
	inputs, err := bindSweepInputsToTx(tx, requested)
	if err != nil {
		return nil, nil, err
	}
	eligible, refusals, err := resolveSweepInputs(ctx, network, inputs, sspIdentityPubKey)
	if err != nil {
		return nil, nil, err
	}
	if len(refusals) > 0 {
		return nil, refusals, nil
	}
	if err := validateSweepTxValues(tx, eligible); err != nil {
		return nil, nil, err
	}

	prevOuts := make(map[wire.OutPoint]*wire.TxOut, len(eligible))
	for _, in := range eligible {
		prevOuts[in.outpoint] = wire.NewTxOut(int64(in.utxo.Amount), in.utxo.PkScript)
	}
	hasher := sighash.NewMultiPrevOutSigHasher(tx, prevOuts)
	sighashes := make(map[uuid.UUID]sighash.Hash, len(eligible))
	for _, in := range eligible {
		msg, err := hasher.For(int(in.vin))
		if err != nil {
			return nil, nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("failed to compute sighash for input %d: %w", in.vin, err))
		}
		sighashes[in.jobID()] = msg
	}
	return &preparedSweep{tx: tx, inputs: eligible, sighashes: sighashes}, nil, nil
}

// sweepSigningSetMembership reports whether this operator is in the round-1 set
// for the sweep, requiring the answer to be the same for every input.
func sweepSigningSetMembership(requested []*pbinternal.StaticDepositSweepInput, identifier string) (bool, error) {
	inFirst := false
	for i, in := range requested {
		_, present := in.GetSigningCommitments()[identifier]
		if i == 0 {
			inFirst = present
			continue
		}
		if present != inFirst {
			return false, sparkerrors.InvalidArgumentMalformedField(
				fmt.Errorf("signing commitments for this operator are present on some sweep inputs but not others"),
			)
		}
	}
	return inFirst, nil
}

func (h *StaticDepositSweepFlowHandler) buildSweepRound2Jobs(requested []*pbinternal.StaticDepositSweepInput, prepared *preparedSweep) ([]*pbinternal.SigningJob, error) {
	commitmentsByOutpoint := make(map[string]map[string]*pbcommon.SigningCommitment, len(requested))
	for _, in := range requested {
		commitmentsByOutpoint[outpointKey(in.GetOnChainUtxo())] = in.GetSigningCommitments()
	}

	jobs := make([]*pbinternal.SigningJob, 0, len(prepared.inputs))
	for _, in := range prepared.inputs {
		keyMaterial, err := sweepInputKeyMaterial(in)
		if err != nil {
			return nil, err
		}
		jobID := in.jobID()
		jobs = append(jobs, &pbinternal.SigningJob{
			JobId:           jobID.String(),
			Message:         prepared.sighashes[jobID].Serialize(),
			KeyshareId:      keyMaterial.keyshareID.String(),
			VerifyingKey:    keyMaterial.verifyingKey.Serialize(),
			Commitments:     commitmentsByOutpoint[outpointKey(in.requested.GetOnChainUtxo())],
			UserCommitments: in.requested.GetUserSigningCommitment(),
		})
	}
	return jobs, nil
}

// Commit is a no-op: a sweep changes no operator state. The swaps it spends stay
// COMPLETED, which is deliberate — that is what keeps the UTXOs re-signable so a
// stalled sweep can be replaced at a higher fee.
func (h *StaticDepositSweepFlowHandler) Commit(ctx context.Context, op proto.Message) error {
	if req, ok := op.(*pbinternal.StaticDepositSweepCommitRequest); ok {
		logging.GetLoggerFromContext(ctx).Sugar().Debugf("static deposit sweep 2pc commit for tx %x", req.GetSweepTxid())
	}
	return nil
}

// Rollback is a no-op: Prepare takes no locks and writes nothing. Argument-tolerant
// per the FlowHandler contract, since the participant reconciler may pass the
// prepare op instead of the rollback payload.
func (h *StaticDepositSweepFlowHandler) Rollback(_ context.Context, _ proto.Message) error {
	return nil
}

// ---------------------------------------------------------------------------
// Shared validation
// ---------------------------------------------------------------------------

func outpointKey(utxo *pbspark.UTXO) string {
	return fmt.Sprintf("%x:%d", utxo.GetTxid(), utxo.GetVout())
}

// bindSweepInputsToTx checks the transaction spends exactly the UTXOs the caller
// named. Sighashes come from the transaction, so without this the eligibility
// check would guard one set of outpoints while signatures covered another.
func bindSweepInputsToTx(tx *wire.MsgTx, requested []*pbinternal.StaticDepositSweepInput) ([]*sweepInput, error) {
	if len(requested) == 0 {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("inputs are required"))
	}
	if len(requested) > maxSweepInputs {
		return nil, sparkerrors.InvalidArgumentOutOfRange(
			fmt.Errorf("sweep describes %d inputs, more than the %d allowed", len(requested), maxSweepInputs),
		)
	}
	if len(tx.TxIn) != len(requested) {
		return nil, sparkerrors.InvalidArgumentMalformedField(
			fmt.Errorf("sweep transaction spends %d inputs but %d were described", len(tx.TxIn), len(requested)),
		)
	}
	inputs := make([]*sweepInput, 0, len(requested))
	seenVins := make(map[uint32]struct{}, len(requested))
	seenOutpoints := make(map[wire.OutPoint]struct{}, len(requested))
	for i, in := range requested {
		if in == nil {
			return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("inputs[%d] is required", i))
		}
		if in.GetOnChainUtxo() == nil {
			return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("inputs[%d].on_chain_utxo is required", i))
		}
		if in.GetUserSigningCommitment() == nil {
			return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("inputs[%d].user_signing_commitment is required", i))
		}
		vin := in.GetVin()
		if vin >= uint32(len(tx.TxIn)) {
			return nil, sparkerrors.InvalidArgumentOutOfRange(
				fmt.Errorf("inputs[%d].vin %d is out of range for a transaction with %d inputs", i, vin, len(tx.TxIn)),
			)
		}
		if _, ok := seenVins[vin]; ok {
			return nil, sparkerrors.InvalidArgumentDuplicateField(fmt.Errorf("inputs[%d] repeats vin %d", i, vin))
		}
		seenVins[vin] = struct{}{}

		txid := in.GetOnChainUtxo().GetTxid()
		if len(txid) != chainhash.HashSize {
			return nil, sparkerrors.InvalidArgumentMalformedField(
				fmt.Errorf("inputs[%d].on_chain_utxo.txid must be %d bytes, got %d", i, chainhash.HashSize, len(txid)),
			)
		}
		// Stored txids are the hex of wire-order bytes, the reverse of display
		// order, so route the conversion through the hex string.
		hash, err := chainhash.NewHashFromStr(hex.EncodeToString(txid))
		if err != nil {
			return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("inputs[%d]: failed to parse txid: %w", i, err))
		}
		outpoint := wire.OutPoint{Hash: *hash, Index: in.GetOnChainUtxo().GetVout()}
		if _, ok := seenOutpoints[outpoint]; ok {
			return nil, sparkerrors.InvalidArgumentDuplicateField(fmt.Errorf("inputs[%d] repeats utxo %s", i, outpoint))
		}
		seenOutpoints[outpoint] = struct{}{}

		if spent := tx.TxIn[vin].PreviousOutPoint; spent != outpoint {
			return nil, sparkerrors.InvalidArgumentMalformedField(
				fmt.Errorf("sweep transaction input %d spends %s but inputs[%d] describes %s", vin, spent, i, outpoint),
			)
		}
		inputs = append(inputs, &sweepInput{vin: vin, outpoint: outpoint, requested: in})
	}
	return inputs, nil
}

// resolveSweepInputs decides which inputs this operator will sign a spend of.
// Only the eligible ones have their utxo populated. Refusals come back as values
// rather than errors so one stale entry does not cost a round trip per input.
//
// Everything is loaded in a fixed number of queries regardless of input count: a
// sweep may carry 200 inputs, and a per-input walk would put 800 round trips on
// every operator, not just the coordinator.
func resolveSweepInputs(ctx context.Context, network btcnetwork.Network, inputs []*sweepInput, sspIdentityPubKey keys.Public) ([]*sweepInput, []sweepRefusal, error) {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, nil, sparkerrors.InternalDatabaseTransactionLifecycleError(fmt.Errorf("failed to get or create current tx for request: %w", err))
	}

	txids := make([][]byte, 0, len(inputs))
	for _, in := range inputs {
		txids = append(txids, in.requested.GetOnChainUtxo().GetTxid())
	}
	utxos, err := db.Utxo.Query().
		Where(entutxo.NetworkEQ(network), entutxo.TxidIn(txids...)).
		WithDepositAddress(func(q *ent.DepositAddressQuery) { q.WithSigningKeyshare() }).
		All(ctx)
	if err != nil {
		return nil, nil, sparkerrors.InternalDatabaseReadError(fmt.Errorf("failed to load sweep utxos: %w", err))
	}
	// TxidIn cannot express (txid, vout) pairs, so vout is matched here.
	utxoByOutpoint := make(map[string]*ent.Utxo, len(utxos))
	utxoIDs := make([]uuid.UUID, 0, len(utxos))
	for _, utxo := range utxos {
		utxoByOutpoint[fmt.Sprintf("%x:%d", utxo.Txid, utxo.Vout)] = utxo
		utxoIDs = append(utxoIDs, utxo.ID)
	}

	// Read without FOR UPDATE. The predicate this resolves — COMPLETED, not a
	// refund — is terminal: CancelUtxoSwap refuses a COMPLETED swap, the instant
	// rollback only moves CREATED rows, request_type and ssp_identity_public_key
	// are write-once at creation, and nothing deletes swaps. So there is no
	// interleaving for a lock to exclude, and taking one would be actively
	// harmful: the coordinator holds its request transaction open across round-1
	// collection and the engine fan-out, so two overlapping sweeps driven by
	// different coordinators could each hold rows the other's Prepare needs and
	// wait on each other across operators, where no single database sees the cycle.
	//
	// A partial unique index on the utxo edge admits at most one non-cancelled
	// swap per UTXO, so the mapping below is unambiguous, and a COMPLETED row
	// cannot be displaced by a new one for the same UTXO.
	//
	// Confirmation depth is deliberately not re-checked: a settled swap says more
	// than any depth this operator can measure now, and re-checking would let an
	// operator briefly behind on blocks fail a sweep whose UTXOs it already
	// accepted at claim time.
	swapByUtxoID := make(map[uuid.UUID]*ent.UtxoSwap, len(utxoIDs))
	if len(utxoIDs) > 0 {
		swaps, err := db.UtxoSwap.Query().
			Where(
				entutxoswap.HasUtxoWith(entutxo.IDIn(utxoIDs...)),
				entutxoswap.StatusNEQ(st.UtxoSwapStatusCancelled),
			).
			WithUtxo().
			All(ctx)
		if err != nil {
			return nil, nil, sparkerrors.InternalDatabaseReadError(fmt.Errorf("failed to load sweep utxo swaps: %w", err))
		}
		for _, swap := range swaps {
			if swap.Edges.Utxo == nil {
				return nil, nil, sparkerrors.InternalDatabaseMissingEdge(fmt.Errorf("utxo swap %s has no utxo edge", swap.ID))
			}
			swapByUtxoID[swap.Edges.Utxo.ID] = swap
		}
	}

	eligible := make([]*sweepInput, 0, len(inputs))
	var refusals []sweepRefusal
	refuse := func(in *sweepInput, reason sweepIneligibleReason) {
		refusals = append(refusals, sweepRefusal{utxo: in.requested.GetOnChainUtxo(), reason: reason})
	}

	for _, in := range inputs {
		utxo, ok := utxoByOutpoint[fmt.Sprintf("%x:%d", in.requested.GetOnChainUtxo().GetTxid(), in.outpoint.Index)]
		if !ok {
			refuse(in, sweepUnknownUtxo)
			continue
		}
		swap, ok := swapByUtxoID[utxo.ID]
		if !ok {
			refuse(in, sweepNoSwap)
			continue
		}

		// Checked before ownership: a refund swap holds the depositor in
		// ssp_identity_public_key, so the ownership check alone would call it
		// someone else's.
		if swap.RequestType == st.UtxoSwapRequestTypeRefund {
			refuse(in, sweepRefundSwap)
			continue
		}
		if swap.Status != st.UtxoSwapStatusCompleted {
			refuse(in, sweepSwapNotCompleted)
			continue
		}
		if !swap.SspIdentityPublicKey.Equals(sspIdentityPubKey) {
			refuse(in, sweepNotOwnedByCaller)
			continue
		}

		in.utxo = utxo
		eligible = append(eligible, in)
	}
	return eligible, refusals, nil
}

// validateSweepTxValues rejects a transaction that cannot be valid on chain.
// Amounts come from this operator's own records: the taproot sighash commits to
// every input's amount and script, so a caller supplying them would be choosing
// what gets signed over.
func validateSweepTxValues(tx *wire.MsgTx, inputs []*sweepInput) error {
	var totalIn uint64
	for _, in := range inputs {
		if in.utxo.Amount > maxSatoshis || totalIn > maxSatoshis-in.utxo.Amount {
			return sparkerrors.InternalDataInconsistency(fmt.Errorf("total input value overflows for utxo %s", in.outpoint))
		}
		totalIn += in.utxo.Amount
	}

	var totalOut uint64
	for i, out := range tx.TxOut {
		if out.Value < 0 {
			return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("output %d has negative value %d", i, out.Value))
		}
		value := uint64(out.Value)
		if value > maxSatoshis || totalOut > maxSatoshis-value {
			return sparkerrors.InvalidArgumentOutOfRange(fmt.Errorf("total output value overflows at output %d", i))
		}
		totalOut += value
	}
	if totalOut > totalIn {
		return sparkerrors.InvalidArgumentMalformedField(
			fmt.Errorf("sweep transaction spends %d sats but its inputs are worth %d", totalOut, totalIn),
		)
	}
	return nil
}

type sweepKeyMaterial struct {
	keyshareID   uuid.UUID
	verifyingKey keys.Public
}

func sweepInputKeyMaterial(in *sweepInput) (sweepKeyMaterial, error) {
	depositAddress := in.utxo.Edges.DepositAddress
	if depositAddress == nil {
		return sweepKeyMaterial{}, sparkerrors.InternalDatabaseMissingEdge(fmt.Errorf("utxo %s has no deposit address", in.outpoint))
	}
	signingKeyshare := depositAddress.Edges.SigningKeyshare
	if signingKeyshare == nil {
		return sweepKeyMaterial{}, sparkerrors.InternalDatabaseMissingEdge(fmt.Errorf("deposit address %s has no signing keyshare", depositAddress.ID))
	}
	return sweepKeyMaterial{
		keyshareID:   signingKeyshare.ID,
		verifyingKey: signingKeyshare.PublicKey.Add(depositAddress.OwnerSigningPubkey),
	}, nil
}

// ---------------------------------------------------------------------------
// Coordinator flow
// ---------------------------------------------------------------------------

// sweepSigningResult is one input's aggregated signing result, keyed by its
// position in the sweep transaction so the caller can place each signature in
// the right witness without depending on ordering.
type sweepSigningResult struct {
	Vin           uint32
	SigningResult *pbspark.SigningResult
	VerifyingKey  []byte
}

type staticDepositSweepCoordinatorFlow struct {
	*StaticDepositSweepFlowHandler

	prepareReq *pbinternal.StaticDepositSweepPrepareRequest
	// prepared is the coordinator's own validated view, computed in the entrypoint
	// and reused when aggregating so the aggregation signs over the same messages
	// the participants did.
	prepared *preparedSweep
	// round1 holds the parsed operator commitments per signing-job id.
	round1 map[uuid.UUID]map[string]frost.SigningCommitment

	results []sweepSigningResult
}

var _ consensus.CoordinatorFlow = (*staticDepositSweepCoordinatorFlow)(nil)

func (f *staticDepositSweepCoordinatorFlow) PrepareOp() proto.Message {
	return f.prepareReq
}

func (f *staticDepositSweepCoordinatorFlow) RollbackPayload() proto.Message {
	txid := f.prepared.tx.TxHash()
	return &pbinternal.StaticDepositSweepRollbackRequest{SweepTxid: txid[:]}
}

// BuildCommitPayload aggregates each input's round-2 shares into a per-input
// SigningResult for the caller to finish with its own share.
func (f *staticDepositSweepCoordinatorFlow) BuildCommitPayload(ctx context.Context, results map[string]*anypb.Any) (proto.Message, error) {
	allShares, _, err := collectSignatureShares(results)
	if err != nil {
		return nil, fmt.Errorf("failed to collect signature shares: %w", err)
	}

	// One key-package load for the whole batch rather than one per input.
	keyshareIDs := make([]uuid.UUID, 0, len(f.prepared.inputs))
	for _, in := range f.prepared.inputs {
		keyMaterial, err := sweepInputKeyMaterial(in)
		if err != nil {
			return nil, err
		}
		keyshareIDs = append(keyshareIDs, keyMaterial.keyshareID)
	}
	keyPackages, err := ent.GetKeyPackages(ctx, f.config, keyshareIDs)
	if err != nil {
		return nil, fmt.Errorf("unable to get key packages: %w", err)
	}

	signed := make([]sweepSigningResult, 0, len(f.prepared.inputs))
	for _, in := range f.prepared.inputs {
		jobID := in.jobID()
		round2, ok := allShares[jobID.String()]
		if !ok {
			return nil, fmt.Errorf("no round-2 shares collected for sweep input %d", in.vin)
		}
		keyMaterial, err := sweepInputKeyMaterial(in)
		if err != nil {
			return nil, err
		}
		commitments := f.round1[jobID]
		operatorIDs := make([]string, 0, len(commitments))
		for id := range commitments {
			operatorIDs = append(operatorIDs, id)
		}
		selection, err := helper.NewPreSelectedOperatorSelection(f.config, operatorIDs)
		if err != nil {
			return nil, fmt.Errorf("unable to build signing operator selection: %w", err)
		}
		userCommitment := frost.SigningCommitment{}
		if err := userCommitment.UnmarshalProto(in.requested.GetUserSigningCommitment()); err != nil {
			return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("input %d: failed to parse signing commitment: %w", in.vin, err))
		}
		verifyingKey := keyMaterial.verifyingKey
		job := &helper.SigningJob{
			JobID:             jobID,
			SigningKeyshareID: keyMaterial.keyshareID,
			Message:           f.prepared.sighashes[jobID],
			VerifyingKey:      &verifyingKey,
			UserCommitment:    &userCommitment,
		}
		signingResults, err := helper.BuildSigningResults(
			f.config, selection,
			[]*helper.SigningJob{job}, keyPackages,
			[]map[string]frost.SigningCommitment{commitments},
			map[uuid.UUID]map[string][]byte{jobID: round2},
		)
		if err != nil {
			return nil, fmt.Errorf("unable to build signing result for input %d: %w", in.vin, err)
		}
		// One job in, one result out, but guard before indexing so a future change
		// can never panic the commit-build path.
		if len(signingResults) == 0 {
			return nil, fmt.Errorf("no signing result produced for sweep input %d", in.vin)
		}
		signed = append(signed, sweepSigningResult{
			Vin:           in.vin,
			SigningResult: signingResults[0].MarshalProto(),
			VerifyingKey:  verifyingKey.Serialize(),
		})
	}
	f.results = signed

	txid := f.prepared.tx.TxHash()
	return &pbinternal.StaticDepositSweepCommitRequest{SweepTxid: txid[:]}, nil
}

// buildStaticDepositSweepCoordinatorFlow pairs each input with its own round-1
// commitment set. GetSigningCommitments is called with one commitment per input,
// so commitments[i] belongs to inputs[i] and is keyed onto that input's job id.
func buildStaticDepositSweepCoordinatorFlow(
	config *so.Config,
	network pbspark.Network,
	rawTx []byte,
	sspIdentityPubKey keys.Public,
	sspSignature []byte,
	prepared *preparedSweep,
	round1 map[string][]frost.SigningCommitment,
) (*staticDepositSweepCoordinatorFlow, error) {
	parsed := make(map[uuid.UUID]map[string]frost.SigningCommitment, len(prepared.inputs))
	internalInputs := make([]*pbinternal.StaticDepositSweepInput, 0, len(prepared.inputs))

	for i, in := range prepared.inputs {
		perInput := make(map[string]*pbcommon.SigningCommitment, len(round1))
		perInputParsed := make(map[string]frost.SigningCommitment, len(round1))
		for opID, commitments := range round1 {
			if len(commitments) != len(prepared.inputs) {
				return nil, fmt.Errorf("expected %d round-1 commitments for operator %s, got %d", len(prepared.inputs), opID, len(commitments))
			}
			perInput[opID] = commitments[i].MarshalProto()
			perInputParsed[opID] = commitments[i]
		}
		parsed[in.jobID()] = perInputParsed
		internalInputs = append(internalInputs, &pbinternal.StaticDepositSweepInput{
			OnChainUtxo:           in.requested.GetOnChainUtxo(),
			Vin:                   in.vin,
			UserSigningCommitment: in.requested.GetUserSigningCommitment(),
			SigningCommitments:    perInput,
		})
	}

	return &staticDepositSweepCoordinatorFlow{
		StaticDepositSweepFlowHandler: NewStaticDepositSweepFlowHandler(config),
		prepareReq: &pbinternal.StaticDepositSweepPrepareRequest{
			Network:              network,
			RawTx:                rawTx,
			Inputs:               internalInputs,
			SspIdentityPublicKey: sspIdentityPubKey.Serialize(),
			SspSignature:         sspSignature,
		},
		prepared: prepared,
		round1:   parsed,
	}, nil
}
