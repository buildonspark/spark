package handler

import (
	"bytes"
	"fmt"

	pbspark "github.com/lightsparkdev/spark/proto/spark"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// preimageSwapInputs carries the request fields the preimage-swap paths read from the
// transfer_request. The validation* lists feed the P2TR-refund validator
// (ValidateGetPreimageRequest): they hold the package lists for a RECEIVE and are empty
// for a package-only SEND (isPackageOnlySend), whose HTLC refunds the byte-match and
// signing paths validate.
type preimageSwapInputs struct {
	ownerIdentityPublicKey    []byte
	receiverIdentityPublicKey []byte
	transferID                string
	expiryTime                *timestamppb.Timestamp

	validationCpfp           []*pbspark.UserSignedTxSigningJob
	validationDirect         []*pbspark.UserSignedTxSigningJob
	validationDirectFromCpfp []*pbspark.UserSignedTxSigningJob
	packageCpfp              []*pbspark.UserSignedTxSigningJob
	isPackageOnlySend        bool
}

// cpfpLeaves returns the cpfp leaf list for checks that don't care which refund
// rides on it (leaf count, ownership). A package-only send keeps them in packageCpfp
// (its validation* lists are empty); every other shape uses validationCpfp.
func (inputs *preimageSwapInputs) cpfpLeaves() []*pbspark.UserSignedTxSigningJob {
	if inputs.isPackageOnlySend {
		return inputs.packageCpfp
	}
	return inputs.validationCpfp
}

// preimageSwapInputsFromRequest builds the inputs from the request's transfer_request.
func preimageSwapInputsFromRequest(req *pbspark.InitiatePreimageSwapRequest) (*preimageSwapInputs, error) {
	tr := req.GetTransferRequest()
	if tr == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("transfer_request is required"))
	}
	pkg := tr.GetTransferPackage()
	if pkg == nil {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("transfer_request.transfer_package is required"))
	}

	// Invariant: the top-level req receiver must equal transfer_request's — it's the
	// HTLC refund destination, and a split would let SOs record different receivers for
	// one swap. Read the top-level field below so every SO uses one source.
	if !bytes.Equal(req.GetReceiverIdentityPublicKey(), tr.GetReceiverIdentityPublicKey()) {
		return nil, sparkerrors.InvalidArgumentPublicKeyMismatch(fmt.Errorf("receiver identity public key mismatch between request and transfer_request"))
	}

	inputs := &preimageSwapInputs{
		ownerIdentityPublicKey:    tr.GetOwnerIdentityPublicKey(),
		receiverIdentityPublicKey: req.GetReceiverIdentityPublicKey(),
		transferID:                tr.GetTransferId(),
		expiryTime:                tr.GetExpiryTime(),
		packageCpfp:               pkg.GetLeavesToSend(),
	}

	// RECEIVE packages carry P2TR refunds, so they feed the
	// refund validators directly. SEND packages carry HTLC refunds those validators
	// reject, so mark it package-only send and let the package-aware path validate.
	if req.GetReason() == pbspark.InitiatePreimageSwapRequest_REASON_RECEIVE {
		inputs.validationCpfp = pkg.GetLeavesToSend()
		inputs.validationDirect = pkg.GetDirectLeavesToSend()
		inputs.validationDirectFromCpfp = pkg.GetDirectFromCpfpLeavesToSend()
	} else {
		inputs.isPackageOnlySend = true
	}

	return inputs, nil
}

// preimageSwapTransferIDFromRequest returns the transfer ID from the transfer_request.
func preimageSwapTransferIDFromRequest(req *pbspark.InitiatePreimageSwapRequest) string {
	return req.GetTransferRequest().GetTransferId()
}

// isSendPackagePreimageSwap reports whether a preimage-swap request is the
// send-with-transfer-package shape whose commit settles by applying aggregated
// HTLC refund signatures (see BuildCommitPayload). This is the only shape whose
// commit must carry non-empty leaf_signatures.
//
// Classified as "not a receive" to match production, which treats REASON_RECEIVE
// as the one special path and everything else as a send (see
// preimageSwapInputsFromTransferRequest's RECEIVE-else branch). Prepare rejects
// any Reason that is neither SEND nor RECEIVE up front, so only those two ever
// reach the fence and this predicate agrees with production's ==REASON_SEND
// signature-producing paths for every reachable input.
func isSendPackagePreimageSwap(req *pbspark.InitiatePreimageSwapRequest) bool {
	return req.GetReason() != pbspark.InitiatePreimageSwapRequest_REASON_RECEIVE && req.GetTransferRequest() != nil
}

// setPreimageSwapExpiry writes a server-resolved expiry onto the transfer_request so the
// peer fanout agrees.
func setPreimageSwapExpiry(req *pbspark.InitiatePreimageSwapRequest, expiry *timestamppb.Timestamp) {
	if req.GetTransferRequest() != nil {
		req.TransferRequest.ExpiryTime = expiry
	}
}
