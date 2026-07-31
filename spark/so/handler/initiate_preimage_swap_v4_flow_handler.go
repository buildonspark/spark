package handler

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/bolt11"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/logging"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	pbspark "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authz"
	"github.com/lightsparkdev/spark/so/consensus"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/preimageshare"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/helper"
	"github.com/lightsparkdev/spark/so/knobs"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// InitiatePreimageSwapV4FlowHandler implements consensus.FlowHandler for
// CONSENSUS_OPERATION_TYPE_INITIATE_PREIMAGE_SWAP_V4.
//
// It embeds the v3 handler because only Prepare differs: commit and rollback resolve the
// transfer from the decision payload's transfer_id, which carries no version, so those paths
// are shared verbatim. What v4 changes is the transfer it prepares — riding
// StartTransferV3Request, every leaf names its own receiver, which is what makes a fee-bearing
// receive expressible at all. v3's single receiver_identity_public_key was both the swap
// counterparty and the sole destination; v4 separates them, so the counterparty keys only the
// preimage request while destinations come from the per-leaf map.
type InitiatePreimageSwapV4FlowHandler struct {
	*InitiatePreimageSwapFlowHandler
}

var (
	_ consensus.FlowHandler             = (*InitiatePreimageSwapV4FlowHandler)(nil)
	_ consensus.PrepareBoundFlowHandler = (*InitiatePreimageSwapV4FlowHandler)(nil)
)

func NewInitiatePreimageSwapV4FlowHandler(config *so.Config) *InitiatePreimageSwapV4FlowHandler {
	return &InitiatePreimageSwapV4FlowHandler{NewInitiatePreimageSwapFlowHandler(config)}
}

// v4PreparedTransferID resolves the id the same way production persisted it, so the fence
// compares like with like.
func v4PreparedTransferID(req *pbspark.InitiatePreimageSwapV4Request) string {
	return req.GetTransferV3Request().GetTransferId()
}

// isSendPackagePreimageSwapV4 mirrors isSendPackagePreimageSwap: transfer_v3_request is
// required on v4, so the package half is always satisfied and only the Reason decides.
func isSendPackagePreimageSwapV4(req *pbspark.InitiatePreimageSwapV4Request) bool {
	return req.GetReason() != pbspark.InitiatePreimageSwapRequest_REASON_RECEIVE
}

func (h *InitiatePreimageSwapV4FlowHandler) ValidateDecisionAgainstPrepare(prepareOp proto.Message, decisionOp proto.Message) error {
	prepare, ok := prepareOp.(*pbinternal.InitiatePreimageSwapV4PrepareRequest)
	if !ok {
		return fmt.Errorf("unexpected prepare op type %T for initiate preimage swap v4", prepareOp)
	}
	preparedTransferID := v4PreparedTransferID(prepare.GetOriginalRequest())

	// The reconciler's presumed-abort path echoes the prepare op itself, which for v4 is the
	// v4 carrier — the shared fence below cannot type-match it.
	if echo, isEcho := decisionOp.(*pbinternal.InitiatePreimageSwapV4PrepareRequest); isEcho {
		echoedTransferID := v4PreparedTransferID(echo.GetOriginalRequest())
		if !sameTransferID(echoedTransferID, preparedTransferID) {
			return fmt.Errorf("presumed-abort rollback transfer id %s does not match the prepared transfer id %s", echoedTransferID, preparedTransferID)
		}
		return nil
	}

	return validatePreimageSwapDecisionAgainstPrepare(preparedTransferID, isSendPackagePreimageSwapV4(prepare.GetOriginalRequest()), decisionOp)
}

// Rollback must be overridden: the promoted v3 body only type-matches the v3 carrier, so the
// reconciler's echo of the v4 prepare op would error forever, leaving the leaves locked.
func (h *InitiatePreimageSwapV4FlowHandler) Rollback(ctx context.Context, op proto.Message) error {
	var transferIDStr string
	switch r := op.(type) {
	case *pbinternal.InitiatePreimageSwapRollbackRequest:
		transferIDStr = r.GetTransferId()
	case *pbinternal.InitiatePreimageSwapV4PrepareRequest:
		transferIDStr = v4PreparedTransferID(r.GetOriginalRequest())
	default:
		return fmt.Errorf("unexpected operation type %T for initiate preimage swap v4 rollback", op)
	}
	return h.rollbackPreimageSwapTransfer(ctx, transferIDStr)
}

// InitiatePreimageSwapV4 is the public entrypoint. Two knob gates, both failing closed:
// KnobInitiatePreimageSwapV4Enabled keeps the endpoint answering Unimplemented until an operator
// admits it, and a disabled KnobUseConsensusInitiatePreimageSwap is refused rather than served by
// the legacy fanout — that fanout cannot carry a manifest, so falling back would silently drop the
// binding this endpoint exists to enforce.
func (h *LightningHandler) InitiatePreimageSwapV4(ctx context.Context, req *pbspark.InitiatePreimageSwapV4Request) (resp *pbspark.InitiatePreimageSwapResponse, retErr error) {
	if req == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("request is required"))
	}
	if knobs.GetKnobsService(ctx).GetValue(knobs.KnobInitiatePreimageSwapV4Enabled, 0) <= 0 {
		return nil, sparkerrors.UnimplementedFeatureIncomplete(fmt.Errorf("initiate_preimage_swap_v4 is not enabled"))
	}
	if knobs.GetKnobsService(ctx).GetValue(knobs.KnobUseConsensusInitiatePreimageSwap, 0) <= 0 {
		return nil, sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("initiate_preimage_swap_v4 requires the consensus preimage-swap path"))
	}
	// REASON_SEND is the proto3 default, so an omitted reason must be refused here, not in Prepare.
	if req.GetReason() != pbspark.InitiatePreimageSwapRequest_REASON_RECEIVE {
		return nil, sparkerrors.UnimplementedFeatureIncomplete(fmt.Errorf("initiate_preimage_swap_v4 currently supports REASON_RECEIVE only"))
	}

	// The flow metrics carry no version label, so a v4-specific flow value would orphan this
	// path from every query already written against initiate_preimage_swap.
	flowStart := time.Now()
	flowPath := lightningFlowPathUnknown
	spanOpt := lightningPaymentHashSpanOption(req.GetPaymentHash())
	ctx, span := tracer.Start(ctx, "LightningHandler.InitiatePreimageSwapV4", spanOpt)
	defer func() {
		endSpanWithError(span, retErr)
		observeLightningFlow(ctx, lightningFlowInitiatePreimage, flowPath, flowStart, retErr)
	}()

	var (
		preimageShare    *ent.PreimageShare
		isNonHodlReceive bool
	)
	phaseStart := time.Now()
	validateCtx, validateSpan := tracer.Start(ctx, "LightningHandler.InitiatePreimageSwapV4.validate", spanOpt)
	validateErr := func() error {
		v3req := req.GetTransferV3Request()
		if v3req == nil {
			return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("transfer_v3_request is required"))
		}
		parsed, err := parseSendTransferRequest(v3req)
		if err != nil {
			return err
		}
		// Size cap before the ownership load and the flow builder's preload; Prepare re-checks it.
		if err := validatePreimageSwapPackageSize(validateCtx, parsed.senderPkg.GetTransferPackage()); err != nil {
			return err
		}
		if err := authz.EnforceSessionIdentityPublicKeyMatches(validateCtx, h.config, parsed.senderIDPK); err != nil {
			return err
		}
		if err := authz.EnforceWalletNotKillSwitched(validateCtx, parsed.senderIDPK); err != nil {
			return err
		}
		cpfpJobs := parsed.senderPkg.GetTransferPackage().GetLeavesToSend()
		if len(cpfpJobs) == 0 {
			return sparkerrors.InvalidArgumentMissingField(fmt.Errorf("at least one cpfp leaf tx must be provided"))
		}

		db, err := ent.GetDbFromContext(validateCtx)
		if err != nil {
			return fmt.Errorf("unable to get db context: %w", err)
		}
		preimageShare, err = db.PreimageShare.Query().Where(preimageshare.PaymentHash(req.GetPaymentHash())).First(validateCtx)
		if err != nil {
			if !ent.IsNotFound(err) {
				return fmt.Errorf("unable to get preimage share for payment hash %x: %w", req.GetPaymentHash(), err)
			}
			preimageShare = nil
			flowPath = lightningFlowPathReceiveHodl
			if knobs.GetKnobsService(validateCtx).GetValue(knobs.KnobShutdownHodlInvoices, 0) > 0 {
				return sparkerrors.UnavailableMethodDisabled(fmt.Errorf("hodl invoices are currently disabled"))
			}
		} else {
			flowPath = lightningFlowPathReceiveNonHodl
		}
		isNonHodlReceive = req.GetReason() == pbspark.InitiatePreimageSwapRequest_REASON_RECEIVE && preimageShare != nil
		if isNonHodlReceive {
			// Strip before the fanout so every SO prepares the same transfer: a non-HODL receive must
			// not be reaped by cancel_expired_transfers. The manifest must then omit its expiry too,
			// which the binding exempts only for a sole sender — and a receive has exactly one, the SSP.
			v3req.ExpiryTime = nil
		} else if expiry := v3req.GetExpiryTime().AsTime(); expiry.Unix() != 0 && expiry.Before(time.Now()) {
			return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("expiry time is before current time"))
		}

		// Coordinator-only ownership check, session-based; participants run
		// validateNodeOwnership=false in Prepare. Non-locking — Prepare re-loads FOR UPDATE.
		return h.validateLeafOwnershipForPreimageSwap(validateCtx, cpfpJobs)
	}()
	endSpanWithError(validateSpan, validateErr)
	observeLightningPhase(ctx, lightningFlowInitiatePreimage, lightningPhaseValidate, phaseStart, validateErr)
	if validateErr != nil {
		return nil, validateErr
	}

	flow, err := buildInitiatePreimageSwapV4CoordinatorFlow(h.config, req, isNonHodlReceive)
	if err != nil {
		return nil, fmt.Errorf("unable to build coordinator flow: %w", err)
	}

	phaseStart = time.Now()
	consensusCtx, consensusSpan := tracer.Start(ctx, "LightningHandler.InitiatePreimageSwapV4.execute", spanOpt)
	execErr := func() error {
		engine, err := consensus.GetEngine(consensusCtx)
		if err != nil {
			return err
		}
		selection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionAll}
		if _, err := engine.Execute(consensusCtx,
			pbgossip.ConsensusOperationType_CONSENSUS_OPERATION_TYPE_INITIATE_PREIMAGE_SWAP_V4,
			&selection, flow); err != nil {
			return fmt.Errorf("consensus initiate preimage swap v4 failed: %w", err)
		}
		return nil
	}()
	endSpanWithError(consensusSpan, execErr)
	observeLightningPhase(ctx, lightningFlowInitiatePreimage, lightningPhaseConsensusExecute, phaseStart, execErr)
	if execErr != nil {
		return nil, execErr
	}

	if flow.response == nil {
		return nil, fmt.Errorf("initiate preimage swap v4 consensus completed without building a response for transfer %s", flow.transferID)
	}

	// A missing transfer_partner row is a reporting gap, not a reason to fail a settled receive.
	if isNonHodlReceive {
		baseHandler := NewBaseTransferHandler(h.config)
		transferEnt, err := baseHandler.loadTransferNoUpdate(ctx, flow.transferID)
		if err != nil {
			logging.GetLoggerFromContext(ctx).Sugar().Warnf("failed to load transfer %s for partner attribution: %v", flow.transferID, err)
		} else if err := saveTransferPartnerFromPreimageShare(ctx, preimageShare, transferEnt); err != nil {
			logging.GetLoggerFromContext(ctx).Sugar().Warnf("failed to save transfer partner attribution for transfer %s: %v", flow.transferID, err)
		}
	}

	return flow.response, nil
}

type initiatePreimageSwapV4CoordinatorFlow struct {
	*InitiatePreimageSwapV4FlowHandler

	req              *pbspark.InitiatePreimageSwapV4Request
	transferID       uuid.UUID
	isNonHodlReceive bool

	// response is populated during BuildCommitPayload so the public entrypoint can return it
	// once engine.Execute completes.
	response *pbspark.InitiatePreimageSwapResponse
}

var _ consensus.CoordinatorFlow = (*initiatePreimageSwapV4CoordinatorFlow)(nil)

func buildInitiatePreimageSwapV4CoordinatorFlow(config *so.Config, req *pbspark.InitiatePreimageSwapV4Request, isNonHodlReceive bool) (*initiatePreimageSwapV4CoordinatorFlow, error) {
	transferID, err := uuid.Parse(v4PreparedTransferID(req))
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("unable to parse transfer id: %w", err))
	}
	return &initiatePreimageSwapV4CoordinatorFlow{
		InitiatePreimageSwapV4FlowHandler: NewInitiatePreimageSwapV4FlowHandler(config),
		req:                               req,
		transferID:                        transferID,
		isNonHodlReceive:                  isNonHodlReceive,
	}, nil
}

func (f *initiatePreimageSwapV4CoordinatorFlow) PrepareOp() proto.Message {
	return &pbinternal.InitiatePreimageSwapV4PrepareRequest{OriginalRequest: f.req}
}

// BuildCommitPayload has no FROST-aggregation branch because v4 is receive-only: the SOs sign no
// refunds, so the only collected results are preimage shares.
func (f *initiatePreimageSwapV4CoordinatorFlow) BuildCommitPayload(ctx context.Context, results map[string]*anypb.Any) (proto.Message, error) {
	commitReq := &pbinternal.InitiatePreimageSwapCommitRequest{TransferId: f.transferID.String()}

	if f.isNonHodlReceive {
		preimage, err := f.recoverPreimage(f.req.GetPaymentHash(), results)
		if err != nil {
			return nil, err
		}
		commitReq.Preimage = preimage
		proofs, err := f.deriveKeyTweakProofs(ctx, f.transferID)
		if err != nil {
			return nil, err
		}
		commitReq.KeyTweakProofs = proofs
	}

	if err := f.applyInitiatePreimageSwapCommit(ctx, commitReq); err != nil {
		return nil, fmt.Errorf("failed to apply commit on coordinator: %w", err)
	}

	transferEnt, err := f.transfer.loadTransferForUpdate(ctx, f.transferID)
	if err != nil {
		return nil, fmt.Errorf("unable to reload transfer %s after commit: %w", f.transferID, err)
	}
	transferProto, err := transferEnt.MarshalProto(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to marshal transfer %s for response: %w", f.transferID, err)
	}
	f.response = &pbspark.InitiatePreimageSwapResponse{Transfer: transferProto}
	if f.isNonHodlReceive {
		f.response.Preimage = commitReq.GetPreimage()
	}
	return commitReq, nil
}

func (f *initiatePreimageSwapV4CoordinatorFlow) RollbackPayload() proto.Message {
	return &pbinternal.InitiatePreimageSwapRollbackRequest{TransferId: f.transferID.String()}
}

func (h *InitiatePreimageSwapV4FlowHandler) Prepare(ctx context.Context, op proto.Message) (proto.Message, error) {
	prepareReq, ok := op.(*pbinternal.InitiatePreimageSwapV4PrepareRequest)
	if !ok {
		return nil, fmt.Errorf("unexpected operation type %T for initiate preimage swap v4 prepare", op)
	}
	req := prepareReq.GetOriginalRequest()
	if req == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("request is required"))
	}
	// Same fail-closed reason gate as v3: an unrecognized Reason would produce no FROST shares
	// in Prepare and then be permanently fenced at commit, stranding the flow.
	if r := req.GetReason(); r != pbspark.InitiatePreimageSwapRequest_REASON_SEND && r != pbspark.InitiatePreimageSwapRequest_REASON_RECEIVE {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("unrecognized preimage swap reason %d", r))
	}

	state, err := h.prepareStateV4(ctx, req)
	if err != nil {
		return nil, err
	}

	resp := &pbinternal.InitiatePreimageSwapPrepareResponse{}
	if state.preimageShare != nil {
		resp.PreimageShare = state.preimageShare.PreimageShare
	}
	return resp, nil
}

// prepareStateV4 is the v3 prepareState reshaped onto StartTransferV3Request: the per-leaf
// receiver map replaces the single receiver, so createTransferV3 replaces createTransfer and
// the refund validator resolves each leaf's destination individually.
//
// Trust in that map comes from being pinned from two directions, both on every operator:
// validateGetPreimageRequest proves each leaf's refund output pays the receiver the map names,
// and requireAndBindManifest's exact cover proves those receivers aggregate exactly to the
// sender-signed edges. Neither alone suffices — the first attests to routing nobody signed,
// the second to an aggregate whose refunds may pay someone else.
func (h *InitiatePreimageSwapV4FlowHandler) prepareStateV4(ctx context.Context, req *pbspark.InitiatePreimageSwapV4Request) (*preimageSwapPreparedState, error) {
	// v4 SEND needs an HTLC-refund validator for the per-leaf shape that does not exist yet, and
	// send's fee cut rides the real user→SSP edge rather than separate fee legs, so it is a
	// different design problem with no caller today. Fail closed rather than fall through to a
	// receive-shaped validation that would reject every send anyway.
	if req.GetReason() != pbspark.InitiatePreimageSwapRequest_REASON_RECEIVE {
		return nil, sparkerrors.UnimplementedFeatureIncomplete(fmt.Errorf("initiate_preimage_swap_v4 currently supports REASON_RECEIVE only"))
	}

	v3req := req.GetTransferV3Request()
	if v3req == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("transfer_v3_request is required"))
	}
	parsed, err := parseSendTransferRequest(v3req)
	if err != nil {
		return nil, err
	}
	counterparty, err := keys.ParsePublicKey(req.GetCounterpartyIdentityPublicKey())
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedKey(fmt.Errorf("unable to parse counterparty identity public key: %w", err))
	}
	if err := verifyCounterpartyManifestSignature(ctx, req, counterparty); err != nil {
		return nil, err
	}

	tx, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get current tx for request: %w", err)
	}
	// HODL invoices have no preimage share yet — the user provides it later via ProvidePreimage,
	// so a missing share is that path, not an error.
	preimageShare, err := tx.PreimageShare.Query().Where(preimageshare.PaymentHash(req.GetPaymentHash())).First(ctx)
	if err != nil {
		if !ent.IsNotFound(err) {
			return nil, fmt.Errorf("unable to get preimage share for payment hash %x: %w", req.GetPaymentHash(), err)
		}
		preimageShare = nil
	} else if !preimageShare.OwnerIdentityPubkey.Equals(counterparty) {
		return nil, sparkerrors.InvalidArgumentPublicKeyMismatch(
			fmt.Errorf("preimage share owner identity public key mismatch for payment hash %x", req.GetPaymentHash()))
	}

	// Derive the invoice amount from the stored bolt11 rather than the client-claimed amount
	// whenever a share exists, matching v3.
	invoiceAmount := req.GetInvoiceAmount()
	if preimageShare != nil {
		storedInvoice, err := bolt11.Parse(preimageShare.InvoiceString, preimageShare.PaymentHash)
		if err != nil {
			return nil, fmt.Errorf("unable to validate stored invoice: %w", err)
		}
		if storedInvoice.MilliSatoshi() > 0 {
			invoiceAmount = &pbspark.InvoiceAmount{
				ValueSats:          uint64(storedInvoice.MilliSatoshi() / 1000),
				InvoiceAmountProof: &pbspark.InvoiceAmountProof{Bolt11Invoice: preimageShare.InvoiceString},
			}
		}
	}

	pkg := parsed.senderPkg.GetTransferPackage()
	cpfpJobs := pkg.GetLeavesToSend()
	directJobs := pkg.GetDirectLeavesToSend()
	dfcJobs := pkg.GetDirectFromCpfpLeavesToSend()

	if err := h.lightning.ValidateDuplicateLeaves(ctx, cpfpJobs, directJobs, dfcJobs); err != nil {
		return nil, err
	}

	destinations, err := v4LeafDestinations(ctx, parsed.leafReceiverMap)
	if err != nil {
		return nil, err
	}
	// feeSats is 0 because v4 carries no fee_sats: the routing fee rides inside the manifest's
	// own edge, which is the decomposition v4 exists to stop doing.
	if err := h.lightning.validateGetPreimageRequest(
		ctx,
		req.GetPaymentHash(),
		cpfpJobs,
		directJobs,
		dfcJobs,
		invoiceAmount,
		counterparty,
		destinations,
		0,
		req.GetReason(),
		false, // validateNodeOwnership: coordinator-only (session-based), done in the entrypoint
	); err != nil {
		return nil, fmt.Errorf("unable to validate request for payment hash %x: %w", req.GetPaymentHash(), err)
	}

	keyTweakMap, err := h.transfer.ValidateTransferPackage(ctx, parsed.transferID, pkg, parsed.senderIDPK, false)
	if err != nil {
		return nil, fmt.Errorf("unable to validate transfer package: %w", err)
	}

	spec := &transferSpec{
		transferID:      parsed.transferID,
		senderIDPK:      parsed.senderIDPK,
		receivers:       parsed.receivers,
		leafReceiverMap: parsed.leafReceiverMap,
		expiryTime:      v3req.GetExpiryTime().AsTime(),
		pkgLeafIDs:      transferPackageLeafIDLists(pkg),
		cpfpRefunds:     stringKeyedRefundMap(parsed.pkg.CPFPRefundTxByLeafID()),
		directRefunds:   stringKeyedRefundMap(parsed.pkg.DirectRefundTxByLeafID()),
		dfcRefunds:      stringKeyedRefundMap(parsed.pkg.DirectFromCPFPRefundTxByLeafID()),
		keyTweaks:       keyTweakMap,
	}
	transfer, leafMap, err := h.transfer.createTransferV3(ctx, spec, st.TransferTypePreimageSwap, TransferRoleParticipant, true)
	if err != nil {
		return nil, fmt.Errorf("unable to create transfer for payment hash %x: %w", req.GetPaymentHash(), err)
	}

	// Both read the locked rows: the cover check needs their owners and amounts, and what the
	// counterparty is paid has to be summed from those amounts rather than the request's.
	if err := requireAndBindManifest(ctx, manifestEndpointInitiatePreimageSwapV4, v3req, transfer.Network, leafMap); err != nil {
		return nil, err
	}
	if err := assertCounterpartyIsPaid(ctx, counterparty, leafMap, destinations, req.GetReason()); err != nil {
		return nil, err
	}

	status := st.PreimageRequestStatusWaitingForPreimage
	if preimageShare != nil {
		status = st.PreimageRequestStatusPreimageShared
	}
	if _, err := h.lightning.storeUserSignedTransactions(
		ctx,
		req.GetPaymentHash(),
		preimageShare,
		cpfpJobs,
		transfer,
		status,
		counterparty,
		parsed.senderIDPK,
	); err != nil {
		return nil, fmt.Errorf("unable to store user signed transactions for payment hash %x and transfer id %s: %w", req.GetPaymentHash(), transfer.ID, err)
	}

	return &preimageSwapPreparedState{
		transfer:      transfer,
		leafMap:       leafMap,
		preimageShare: preimageShare,
		transferID:    parsed.transferID,
	}, nil
}
