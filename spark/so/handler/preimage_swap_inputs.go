package handler

import (
	"bytes"
	"fmt"

	pbspark "github.com/lightsparkdev/spark/proto/spark"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// preimageSwapInputs carries the request fields the preimage-swap paths read from
// whichever shape is present (legacy transfer, else transfer_request). The validation*
// lists feed the P2TR-refund validator (ValidateGetPreimageRequest): they hold the
// package lists for a transfer-less RECEIVE and are empty for a transfer-less SEND
// (isPackageOnlySend), whose HTLC refunds the byte-match and signing paths validate.
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

// preimageSwapInputsFromRequest dispatches on request shape. TODO(SP-3285): the transfer
// case and preimageSwapInputsFromTransfer go away with the legacy field, leaving only
// the transfer_request path.
func preimageSwapInputsFromRequest(req *pbspark.InitiatePreimageSwapRequest) (*preimageSwapInputs, error) {
	switch {
	case req.GetTransfer() != nil:
		return preimageSwapInputsFromTransfer(req), nil
	case req.GetTransferRequest() != nil:
		return preimageSwapInputsFromTransferRequest(req)
	default:
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("transfer_request is required"))
	}
}

// support for legacy Transfer type
func preimageSwapInputsFromTransfer(req *pbspark.InitiatePreimageSwapRequest) *preimageSwapInputs {
	t := req.GetTransfer()
	return &preimageSwapInputs{
		ownerIdentityPublicKey:    t.GetOwnerIdentityPublicKey(),
		receiverIdentityPublicKey: t.GetReceiverIdentityPublicKey(),
		transferID:                t.GetTransferId(),
		expiryTime:                t.GetExpiryTime(),
		validationCpfp:            t.GetLeavesToSend(),
		validationDirect:          t.GetDirectLeavesToSend(),
		validationDirectFromCpfp:  t.GetDirectFromCpfpLeavesToSend(),
	}
}

func preimageSwapInputsFromTransferRequest(req *pbspark.InitiatePreimageSwapRequest) (*preimageSwapInputs, error) {
	tr := req.GetTransferRequest()
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

	// build the inputs based on the newer transfer_request form
	inputs := &preimageSwapInputs{
		ownerIdentityPublicKey:    tr.GetOwnerIdentityPublicKey(),
		receiverIdentityPublicKey: req.GetReceiverIdentityPublicKey(),
		transferID:                tr.GetTransferId(),
		expiryTime:                tr.GetExpiryTime(),
		packageCpfp:               pkg.GetLeavesToSend(),
	}

	// RECEIVE packages carry P2TR refunds (like the legacy transfer), so they feed the
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

// preimageSwapTransferIDFromRequest returns the transfer ID from whichever shape is present.
func preimageSwapTransferIDFromRequest(req *pbspark.InitiatePreimageSwapRequest) string {
	if t := req.GetTransfer(); t != nil {
		return t.GetTransferId()
	}
	return req.GetTransferRequest().GetTransferId()
}

// setPreimageSwapExpiry writes a server-resolved expiry onto whichever request
// shapes are present, so the peer fanout and the cross-check agree.
func setPreimageSwapExpiry(req *pbspark.InitiatePreimageSwapRequest, expiry *timestamppb.Timestamp) {
	if req.GetTransfer() != nil {
		req.Transfer.ExpiryTime = expiry
	}
	if req.GetTransferRequest() != nil {
		req.TransferRequest.ExpiryTime = expiry
	}
}
