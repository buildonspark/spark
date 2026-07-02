package transfer

import (
	"fmt"
	"maps"

	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	"github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/frost"
)

// MaxKeyTweakPackageSize caps the aggregate size of the encrypted key-tweak package (summed across all SO entries) to
// prevent resource exhaustion from a malicious client. MaxSignatureSize caps the user signature length; a secp256k1
// ECDSA signature is at most 73 bytes.
const (
	MaxKeyTweakPackageSize = 4 * 1024 * 1024
	MaxSignatureSize       = 73
)

var (
	ErrDuplicateLeafID             = fmt.Errorf("duplicate leaf id")
	ErrEmptyKeyTweakPackage        = fmt.Errorf("key tweak package is empty")
	ErrEmptyUserSignature          = fmt.Errorf("user signature is empty")
	ErrInvalidLeafID               = fmt.Errorf("invalid leaf id")
	ErrInvalidNonceCommitment      = fmt.Errorf("invalid signing nonce commitment")
	ErrInvalidOperatorCommitment   = fmt.Errorf("invalid operator signing commitment")
	ErrInvalidRefundTx             = fmt.Errorf("invalid refund tx")
	ErrInvalidSigningPublicKey     = fmt.Errorf("invalid signing public key")
	ErrKeyTweakPackageTooLarge     = fmt.Errorf("key tweak package too large")
	ErrMismatchedLeafCount         = fmt.Errorf("mismatched leaf count")
	ErrMissingDirectFromCPFPLeaves = fmt.Errorf("missing direct from cpfp leaves")
	ErrMissingOperatorCommitment   = fmt.Errorf("missing operator signing commitments")
	ErrMissingUserSignature        = fmt.Errorf("missing user signature share")
	ErrOrphanLeaf                  = fmt.Errorf("leaf not in leaves to send")
	ErrTooManySigningInputs        = fmt.Errorf("more signing inputs than refund tx inputs")
	ErrTooManyLeaves               = fmt.Errorf("too many leaves")
	ErrUnknownHashVariant          = fmt.Errorf("unknown hash variant")
	ErrUserSignatureTooLarge       = fmt.Errorf("user signature too large")
)

// Package is a parsed, structurally-validated view of a spark.TransferPackage. The only way to get one is via ParsePackage,
// so holding a non-nil *Package guarantees every field's structure.
//
// Note that Package doesn't guarantee that the cryptography makes sense (e.g. it doesn't guarantee that userSignature
// is a valid signature).
type Package struct {
	leavesToSend               []*RefundSigningJob
	directLeavesToSend         []*RefundSigningJob
	directFromCPFPLeavesToSend []*RefundSigningJob

	// keyTweakPackage is the map of SO identifier to ciphertext of SendLeafTweaks.
	keyTweakPackage map[so.Identifier][]byte
	// userSignature is the signature of the user, to prove that the keyTweakPackage has not been tampered with.
	userSignature []byte
	// hashVariant is the hash variant to use for computing the transfer package signing payload.
	hashVariant spark.HashVariant
}

// parseConfig holds the tunable validation parameters for ParsePackage.
type parseConfig struct {
	requireDirectFromCPFPLeaves bool
	// maxLeavesToSend caps LeavesToSend (and each alternate refund list). 0 means unlimited.
	maxLeavesToSend int
}

type ParseOption func(*parseConfig)

// RequireDirectFromCPFPLeaves controls whether the direct-from-cpfp refund list may be empty. When required is true,
// ParsePackage rejects a package whose direct-from-cpfp list doesn't cover leaves-to-send one-for-one.
func RequireDirectFromCPFPLeaves(required bool) ParseOption {
	return func(c *parseConfig) { c.requireDirectFromCPFPLeaves = required }
}

// WithMaxLeavesToSend caps the number of leaves in each refund list. A value <= 0 disables the cap.
func WithMaxLeavesToSend(maxLeaves int) ParseOption {
	return func(c *parseConfig) { c.maxLeavesToSend = maxLeaves }
}

func ParsePackage(asProto *spark.TransferPackage, opts ...ParseOption) (*Package, error) {
	var cfg parseConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	switch asProto.GetHashVariant() {
	case spark.HashVariant_HASH_VARIANT_UNSPECIFIED, spark.HashVariant_HASH_VARIANT_V2:
	default:
		return nil, fmt.Errorf("%w: %d", ErrUnknownHashVariant, asProto.GetHashVariant())
	}

	leavesToSend, err := ParseRefundSigningJobs(asProto.GetLeavesToSend(), cfg.maxLeavesToSend)
	if err != nil {
		return nil, err
	}
	directLeavesToSend, err := ParseRefundSigningJobs(asProto.GetDirectLeavesToSend(), cfg.maxLeavesToSend)
	if err != nil {
		return nil, err
	}
	directFromCPFPLeavesToSend, err := ParseRefundSigningJobs(asProto.GetDirectFromCpfpLeavesToSend(), cfg.maxLeavesToSend)
	if err != nil {
		return nil, err
	}

	if cfg.requireDirectFromCPFPLeaves && len(directFromCPFPLeavesToSend) == 0 {
		return nil, fmt.Errorf("%w: direct-from-cpfp refunds are required", ErrMissingDirectFromCPFPLeaves)
	}

	// The direct and direct-from-cpfp variants are alternate refunds for the leaves being sent, so every leaf they
	// reference must also appear in leavesToSend. Otherwise their leaf-ID-keyed projections would surface leaves the
	// package never committed to sending.
	leavesToSendIDs := make(map[uuid.UUID]struct{}, len(leavesToSend))
	for _, job := range leavesToSend {
		leavesToSendIDs[job.leafID] = struct{}{}
	}
	for _, job := range directLeavesToSend {
		if _, ok := leavesToSendIDs[job.leafID]; !ok {
			return nil, fmt.Errorf("%w: direct leaf %s", ErrOrphanLeaf, job.leafID)
		}
	}
	// Direct-from-cpfp refunds, when present, must cover the leaves-to-send set one-for-one.
	if len(directFromCPFPLeavesToSend) > 0 && len(directFromCPFPLeavesToSend) != len(leavesToSend) {
		return nil, fmt.Errorf("%w: %d direct-from-cpfp leaves, %d leaves to send", ErrMismatchedLeafCount, len(directFromCPFPLeavesToSend), len(leavesToSend))
	}
	for _, job := range directFromCPFPLeavesToSend {
		if _, ok := leavesToSendIDs[job.leafID]; !ok {
			return nil, fmt.Errorf("%w: direct-from-cpfp leaf %s", ErrOrphanLeaf, job.leafID)
		}
	}

	keyTweakPackage := maps.Clone(asProto.GetKeyTweakPackage())
	if len(keyTweakPackage) == 0 {
		return nil, ErrEmptyKeyTweakPackage
	}
	keyTweakPackageSize := 0
	for _, ciphertext := range keyTweakPackage {
		keyTweakPackageSize += len(ciphertext)
	}
	if keyTweakPackageSize > MaxKeyTweakPackageSize {
		return nil, fmt.Errorf("%w: %d bytes (max: %d)", ErrKeyTweakPackageTooLarge, keyTweakPackageSize, MaxKeyTweakPackageSize)
	}

	userSignature := asProto.GetUserSignature()
	if len(userSignature) == 0 {
		return nil, ErrEmptyUserSignature
	}
	if len(userSignature) > MaxSignatureSize {
		return nil, fmt.Errorf("%w: %d bytes (max: %d)", ErrUserSignatureTooLarge, len(userSignature), MaxSignatureSize)
	}

	return &Package{
		leavesToSend:               leavesToSend,
		directLeavesToSend:         directLeavesToSend,
		directFromCPFPLeavesToSend: directFromCPFPLeavesToSend,
		keyTweakPackage:            keyTweakPackage,
		userSignature:              userSignature,
		hashVariant:                asProto.GetHashVariant(),
	}, nil
}

func (p *Package) LeavesToSend() []*RefundSigningJob       { return p.leavesToSend }
func (p *Package) DirectLeavesToSend() []*RefundSigningJob { return p.directLeavesToSend }
func (p *Package) DirectFromCPFPLeavesToSend() []*RefundSigningJob {
	return p.directFromCPFPLeavesToSend
}

// KeyTweakPackage returns the SO-identifier-keyed ciphertext map.
func (p *Package) KeyTweakPackage() map[so.Identifier][]byte { return maps.Clone(p.keyTweakPackage) }
func (p *Package) UserSignature() []byte                     { return p.userSignature }
func (p *Package) HashVariant() spark.HashVariant            { return p.hashVariant }

// CPFPRefundTxByLeafID projects the CPFP refund txs to a leaf-ID-keyed map, for callers that only need the raw refund bytes.
func (p *Package) CPFPRefundTxByLeafID() map[uuid.UUID][]byte {
	return RefundTxByLeafID(p.leavesToSend)
}

// DirectRefundTxByLeafID projects the direct refund txs to a leaf-ID-keyed map, for callers that only need the raw refund bytes.
func (p *Package) DirectRefundTxByLeafID() map[uuid.UUID][]byte {
	return RefundTxByLeafID(p.directLeavesToSend)
}

// DirectFromCPFPRefundTxByLeafID projects the direct-from-CPFP refund txs to a leaf-ID-keyed map, for callers that only need the raw refund bytes.
func (p *Package) DirectFromCPFPRefundTxByLeafID() map[uuid.UUID][]byte {
	return RefundTxByLeafID(p.directFromCPFPLeavesToSend)
}

// RefundTxByLeafID keys each job's raw refund tx by its leaf ID.
func RefundTxByLeafID(jobs []*RefundSigningJob) map[uuid.UUID][]byte {
	out := make(map[uuid.UUID][]byte, len(jobs))
	for _, job := range jobs {
		out[job.leafID] = job.rawTx
	}
	return out
}

// LeafIDs returns the distinct leaf IDs referenced across all three refund variants, in first-seen order
// (cpfp, then direct, then direct-from-cpfp).
func (p *Package) LeafIDs() []uuid.UUID {
	return DistinctLeafIDs(p.leavesToSend, p.directLeavesToSend, p.directFromCPFPLeavesToSend)
}

// DistinctLeafIDs returns the distinct leaf IDs across the given job lists, in first-seen order.
func DistinctLeafIDs(jobLists ...[]*RefundSigningJob) []uuid.UUID {
	seen := make(map[uuid.UUID]struct{})
	var ids []uuid.UUID
	for _, jobs := range jobLists {
		for _, job := range jobs {
			if _, ok := seen[job.leafID]; ok {
				continue
			}
			seen[job.leafID] = struct{}{}
			ids = append(ids, job.leafID)
		}
	}
	return ids
}

// RefundSigningJob is the parsed form of a spark.UserSignedTxSigningJob carrying a refund tx.
type RefundSigningJob struct {
	leafID        uuid.UUID
	signingPubKey keys.Public
	// rawTx is the serialized refund tx; refundTx is its parsed form. The refund tx spends the node transaction to the
	// receiver. We have both so callers can hash the raw bytes without re-serializing and inspect the structure without reparsing.
	rawTx []byte
	// refundTx is the parsed version of rawTx.
	refundTx *wire.MsgTx
	// inputs holds the signing data for the SO-signed refund tx inputs, in tx-input order starting at input 0. It's
	// non-empty and never longer than refundTx.TxIn; trailing tx inputs signed by another party
	// (e.g. a coop-exit connector input) have no entry here.
	inputs []SigningInput
}

func (j *RefundSigningJob) LeafID() uuid.UUID          { return j.leafID }
func (j *RefundSigningJob) SigningPubKey() keys.Public { return j.signingPubKey }
func (j *RefundSigningJob) RawTx() []byte              { return j.rawTx }
func (j *RefundSigningJob) RefundTx() *wire.MsgTx      { return j.refundTx }
func (j *RefundSigningJob) Inputs() []SigningInput     { return j.inputs }

// ParseRefundSigningJobs parses a list of proto signing jobs into validated RefundSigningJobs. Each leaf may appear at
// most once within a single list; a leaf legitimately appearing across the cpfp/direct/direct-from-cpfp variants is
// fine, but a repeat within one list is malformed input whose downstream leaf-ID-keyed projections would silently
// collapse.
// When maxJobs is less than one, it is ignored.
func ParseRefundSigningJobs(jobs []*spark.UserSignedTxSigningJob, maxJobs int) (parsed []*RefundSigningJob, err error) {
	if maxJobs > 0 && len(jobs) > maxJobs {
		return nil, fmt.Errorf("%w: %d jobs (max: %d)", ErrTooManyLeaves, len(jobs), maxJobs)
	}

	parsed = make([]*RefundSigningJob, len(jobs))
	seen := make(map[uuid.UUID]struct{}, len(jobs))
	for i, job := range jobs {
		parsed[i], err = parseRefundSigningJob(job)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[parsed[i].leafID]; ok {
			return nil, fmt.Errorf("%w %s", ErrDuplicateLeafID, parsed[i].leafID)
		}
		seen[parsed[i].leafID] = struct{}{}
	}
	return parsed, nil
}

func parseRefundSigningJob(asProto *spark.UserSignedTxSigningJob) (*RefundSigningJob, error) {
	leafID, err := uuid.Parse(asProto.GetLeafId())
	if err != nil {
		return nil, fmt.Errorf("%w %q: %w", ErrInvalidLeafID, asProto.GetLeafId(), err)
	}
	signingPubKey, err := keys.ParsePublicKey(asProto.GetSigningPublicKey())
	if err != nil {
		return nil, fmt.Errorf("%w for leaf %s: %w", ErrInvalidSigningPublicKey, leafID, err)
	}
	refundTx, err := common.TxFromRawTxBytes(asProto.GetRawTx())
	if err != nil {
		return nil, fmt.Errorf("%w for leaf %s: %w", ErrInvalidRefundTx, leafID, err)
	}

	// Input 0's signing material is in the top-level fields; inputs 1..N are in additional_inputs.
	input0, err := parseSigningInput(asProto.GetSigningNonceCommitment(), asProto.GetUserSignature(), asProto.GetSigningCommitments())
	if err != nil {
		return nil, err
	}
	additionalInputs, err := parseSigningInputs(asProto.GetAdditionalInputs())
	if err != nil {
		return nil, err
	}
	inputs := append([]SigningInput{input0}, additionalInputs...)

	// Signing inputs are the SE-signed inputs and map to the first len(inputs) tx inputs; trailing tx inputs may be
	// signed by another party and carry no SigningInput (e.g. a coop-exit refund's connector input). There's always at
	// least one and never more than there are tx inputs.
	if len(inputs) > len(refundTx.TxIn) {
		return nil, fmt.Errorf("%w: leaf %s has %d tx inputs, %d signing inputs", ErrTooManySigningInputs, leafID, len(refundTx.TxIn), len(inputs))
	}

	return &RefundSigningJob{
		leafID:        leafID,
		signingPubKey: signingPubKey,
		rawTx:         asProto.GetRawTx(),
		refundTx:      refundTx,
		inputs:        inputs,
	}, nil
}

// SigningInput carries the signing data for a single refund tx input.
type SigningInput struct {
	signingNonceCommitment frost.SigningCommitment
	// userSignature is the user's FROST signature share for this input. It's verified during FROST aggregation against
	// the input's sighash (which requires the parent tx output), not here.
	userSignature []byte
	// signingCommitments is the SOs' nonce commitments for this input, keyed by operator identifier.
	signingCommitments OperatorCommitments
}

func (d SigningInput) SigningNonceCommitment() frost.SigningCommitment {
	return d.signingNonceCommitment
}
func (d SigningInput) UserSignature() []byte                   { return d.userSignature }
func (d SigningInput) SigningCommitments() OperatorCommitments { return d.signingCommitments }

func parseSigningInputs(asProto []*spark.InputSigningData) ([]SigningInput, error) {
	out := make([]SigningInput, len(asProto))
	for i, input := range asProto {
		parsed, err := parseSigningInput(input.GetSigningNonceCommitment(), input.GetUserSignature(), input.GetSigningCommitments())
		if err != nil {
			return nil, err
		}
		out[i] = parsed
	}
	return out, nil
}

func parseSigningInput(nonceCommitment *pbcommon.SigningCommitment, userSignature []byte, commitments *spark.SigningCommitments) (SigningInput, error) {
	parsedNonceCommitment := frost.SigningCommitment{}
	if err := parsedNonceCommitment.UnmarshalProto(nonceCommitment); err != nil {
		return SigningInput{}, fmt.Errorf("%w: %w", ErrInvalidNonceCommitment, err)
	}

	// The share is opaque here (it is parsed and verified inside the FROST signer against the input's sighash); the
	// most we can assert at parse time is that one was provided.
	if len(userSignature) == 0 {
		return SigningInput{}, ErrMissingUserSignature
	}

	parsedCommitments, err := parseOperatorCommitments(commitments)
	if err != nil {
		return SigningInput{}, err
	}

	return SigningInput{
		signingNonceCommitment: parsedNonceCommitment,
		userSignature:          userSignature,
		signingCommitments:     parsedCommitments,
	}, nil
}

func parseOperatorCommitments(asProto *spark.SigningCommitments) (OperatorCommitments, error) {
	rawSigningCommitments := asProto.GetSigningCommitments()
	// An SE-signed input with no operator commitments can never produce a threshold signature. We can't check the
	// commitment count against the signing threshold here (that config isn't in scope), but we can require at least one.
	if len(rawSigningCommitments) == 0 {
		return nil, ErrMissingOperatorCommitment
	}
	commitments := make(OperatorCommitments, len(rawSigningCommitments))

	for soID, rawSoCommitment := range rawSigningCommitments {
		parsed := frost.SigningCommitment{}
		if err := parsed.UnmarshalProto(rawSoCommitment); err != nil {
			return nil, fmt.Errorf("%w for operator %s: %w", ErrInvalidOperatorCommitment, soID, err)
		}
		commitments[soID] = parsed
	}
	return commitments, nil
}

type OperatorCommitments map[so.Identifier]frost.SigningCommitment
