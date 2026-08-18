package transfer

import (
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	"github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/frost"
)

// MaxMpcSealedShareBytes caps the aggregate size of the sealed sub-share material (summed across all operator
// entries) to prevent resource exhaustion from a malicious client, mirroring MaxKeyTweakPackageSize for the
// single-party package. MaxMpcSubUsers and MaxMpcProofsPerVector cap the participant count and the Feldman vector
// length for the same reason — both are far above any plausible sub-user group or operator signing threshold, and
// they bound the curve-point parses a pathologically-shaped request can force (total parse work is otherwise bounded
// only by the transport message-size limit). The signature sizes are exact lengths: 64 bytes for a BIP-340 Schnorr
// signature, 32 bytes for a sub-user's FROST signature share scalar, 32 bytes for a SHA-256 digest.
const (
	MaxMpcSealedShareBytes    = 4 * 1024 * 1024
	MaxMpcSubUsers            = 128
	MaxMpcProofsPerVector     = 32
	SchnorrSignatureSize      = 64
	MpcPartialSignatureSize   = 32
	RefundSighashesDigestSize = 32
)

var (
	ErrMpcMissingPackage               = fmt.Errorf("mpc transfer package is required")
	ErrMpcMissingAuthorization         = fmt.Errorf("transfer authorization is required")
	ErrMpcInvalidTransferID            = fmt.Errorf("invalid transfer id")
	ErrMpcTransferIDMismatch           = fmt.Errorf("authorization transfer id does not match request")
	ErrMpcInvalidSenderIdentityKey     = fmt.Errorf("invalid sender identity public key")
	ErrMpcInvalidAuthSignature         = fmt.Errorf("invalid authorization signature")
	ErrMpcMissingExpiry                = fmt.Errorf("authorization expiry time is required")
	ErrMpcInvalidExpiry                = fmt.Errorf("authorization expiry time must have zero nanos")
	ErrMpcInvalidRefundSighashesDigest = fmt.Errorf("invalid refund sighashes digest")
	ErrMpcNoLeaves                     = fmt.Errorf("mpc transfer package has no leaves")
	ErrMpcLeafSetMismatch              = fmt.Errorf("leaf id sets disagree across the submission")
	ErrMpcInvalidLeafAuthorization     = fmt.Errorf("invalid leaf authorization")
	ErrMpcMissingSecretCipher          = fmt.Errorf("secret cipher is required")
	ErrMpcInvalidLeafSignature         = fmt.Errorf("invalid per-leaf identity signature")
	ErrMpcInvalidPositions             = fmt.Errorf("participant positions must be >= 1 and strictly ascending")
	ErrMpcInvalidProofs                = fmt.Errorf("invalid sub-user commitment proofs")
	ErrMpcPositionSetMismatch          = fmt.Errorf("list not aligned to the participant positions")
	ErrMpcNoSealedShares               = fmt.Errorf("mpc transfer package has no sealed key tweaks")
	ErrMpcMissingSealedShare           = fmt.Errorf("missing or empty sealed sub-share")
	ErrMpcSealedSharesTooLarge         = fmt.Errorf("sealed sub-share material too large")
	ErrMpcSingleSignerFieldsPresent    = fmt.Errorf("signing job carries single-signer fields")
	ErrMpcInvalidPartialSignature      = fmt.Errorf("invalid sub-user partial signature")
	ErrMpcAdditionalInputsNotAllowed   = fmt.Errorf("signing job carries additional inputs")
)

// MpcSubmission is a parsed, structurally-validated view of a spark.StartTransferMpcRequest. The only way to get one
// is via ParseMpcSubmission, so holding a non-nil *MpcSubmission guarantees every field's structure: ids parse,
// points are valid curve points, the leaf-id sets of the authorization, the public leaves, and each signing-job list
// agree, and every per-sub-user list (commitment vectors, sealed blobs, signing contributions) aligns one-to-one
// with the package's participant positions.
//
// Like Package, an MpcSubmission does not guarantee that the cryptography makes sense: the authorization signature is
// not verified, sealed shares are not decrypted, and no authorized fact is checked against operator state. Those are
// operator-side verification, layered on top of this parse. Coverage of the sealed-share map against the cluster's
// operator set is also deferred there (cluster configuration is out of scope here, mirroring
// parseOperatorCommitments).
type MpcSubmission struct {
	transferID  uuid.UUID
	senderIDPub keys.Public
	expiryTime  time.Time

	// positions is the submission-wide participant set, strictly ascending; every per-sub-user list aligns to it.
	positions []uint32

	leaves []*MpcLeaf

	// refundSighashesDigest is the group-signed digest over the BIP-341 sighashes of the submission's refund
	// transactions. Opaque here; the operator-side verifier recomputes it from transactions it rebuilds.
	refundSighashesDigest []byte
	// The group's scheme-tagged identity signature over the authorization payload. Structure-checked only
	// (defined scheme, plausible length); not verified here.
	authSignatureScheme pbcommon.SignatureScheme
	authSignature       []byte
	// authorization is the raw signed statement, kept so the verifier can recompute the signed payload from the
	// exact wire fields. Callers must not mutate it.
	authorization *spark.TransferAuthorization

	// sealedShares maps operator identifier -> sub-user position -> ECIES blob readable only by that operator (each
	// blob carries that sub-user's sub-shares for all leaves).
	sealedShares map[so.Identifier]map[uint32][]byte

	leavesToSend               []*MpcRefundSigningJob
	directLeavesToSend         []*MpcRefundSigningJob
	directFromCPFPLeavesToSend []*MpcRefundSigningJob
}

func (s *MpcSubmission) TransferID() uuid.UUID                { return s.transferID }
func (s *MpcSubmission) SenderIdentityPublicKey() keys.Public { return s.senderIDPub }
func (s *MpcSubmission) ExpiryTime() time.Time                { return s.expiryTime }
func (s *MpcSubmission) Positions() []uint32                  { return slices.Clone(s.positions) }
func (s *MpcSubmission) Leaves() []*MpcLeaf                   { return slices.Clone(s.leaves) }
func (s *MpcSubmission) RefundSighashesDigest() []byte {
	return slices.Clone(s.refundSighashesDigest)
}
func (s *MpcSubmission) AuthSignatureScheme() pbcommon.SignatureScheme {
	return s.authSignatureScheme
}
func (s *MpcSubmission) AuthSignature() []byte { return slices.Clone(s.authSignature) }

// AuthorizationProto returns the raw signed authorization for payload recomputation. Callers must not mutate it.
func (s *MpcSubmission) AuthorizationProto() *spark.TransferAuthorization { return s.authorization }

func (s *MpcSubmission) LeavesToSend() []*MpcRefundSigningJob { return slices.Clone(s.leavesToSend) }
func (s *MpcSubmission) DirectLeavesToSend() []*MpcRefundSigningJob {
	return slices.Clone(s.directLeavesToSend)
}
func (s *MpcSubmission) DirectFromCPFPLeavesToSend() []*MpcRefundSigningJob {
	return slices.Clone(s.directFromCPFPLeavesToSend)
}

// SealedOperatorIDs returns the operator identifiers the sealed-share map carries entries for, sorted.
func (s *MpcSubmission) SealedOperatorIDs() []so.Identifier {
	ids := make([]so.Identifier, 0, len(s.sealedShares))
	for id := range s.sealedShares {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// SealedSharesFor returns one operator's sealed sub-shares as sub-user position -> ECIES blob, or nil if the
// submission carries no entry for that operator. The map is copied; the blobs are shared.
func (s *MpcSubmission) SealedSharesFor(operator so.Identifier) map[uint32][]byte {
	return maps.Clone(s.sealedShares[operator])
}

// Receivers returns the distinct receiver identity public keys across all leaves, in first-seen leaf order.
func (s *MpcSubmission) Receivers() []keys.Public {
	var out []keys.Public
	for _, leaf := range s.leaves {
		if !slices.ContainsFunc(out, leaf.receiverIDPub.Equals) {
			out = append(out, leaf.receiverIDPub)
		}
	}
	return out
}

// MpcLeaf is the merged per-leaf view of a submission: the group-authorized facts (amount, on-file owner pubkey, mask
// commitment, receiver) joined with the public sender material (sub-user reshare commitments, encrypted mask
// hand-off, per-leaf signatures) for the same leaf id.
type MpcLeaf struct {
	leafID uuid.UUID

	// Authorized facts, from the group-signed TransferAuthorization.
	amountSats         uint64
	ownerSigningPubKey keys.Public
	maskCommitment     keys.Public
	receiverIDPub      keys.Public

	// Public sender material.
	subUserCommitments []SubUserCommitmentVector
	secretCipher       []byte
	// The sender's scheme-tagged per-leaf identity signature over Sha256(leaf_id||transfer_id||secret_cipher),
	// verified by the receiver, not here. Structure-checked only (defined scheme, plausible length).
	signatureScheme pbcommon.SignatureScheme
	signature       []byte
}

func (l *MpcLeaf) LeafID() uuid.UUID                   { return l.leafID }
func (l *MpcLeaf) AmountSats() uint64                  { return l.amountSats }
func (l *MpcLeaf) OwnerSigningPubKey() keys.Public     { return l.ownerSigningPubKey }
func (l *MpcLeaf) MaskCommitment() keys.Public         { return l.maskCommitment }
func (l *MpcLeaf) ReceiverIdentityPubKey() keys.Public { return l.receiverIDPub }
func (l *MpcLeaf) SubUserCommitments() []SubUserCommitmentVector {
	return slices.Clone(l.subUserCommitments)
}
func (l *MpcLeaf) SecretCipher() []byte { return slices.Clone(l.secretCipher) }
func (l *MpcLeaf) SignatureScheme() pbcommon.SignatureScheme {
	return l.signatureScheme
}
func (l *MpcLeaf) Signature() []byte { return slices.Clone(l.signature) }

// Positions returns the leaf's participating sub-user positions, sorted ascending.
func (l *MpcLeaf) Positions() []uint32 {
	out := make([]uint32, len(l.subUserCommitments))
	for i, c := range l.subUserCommitments {
		out[i] = c.position
	}
	slices.Sort(out)
	return out
}

// SubUserCommitmentVector is one sub-user's Feldman coefficient commitments for one leaf's resharing, constant term
// first.
type SubUserCommitmentVector struct {
	position uint32
	proofs   []keys.Public
}

func (v SubUserCommitmentVector) Position() uint32      { return v.position }
func (v SubUserCommitmentVector) Proofs() []keys.Public { return slices.Clone(v.proofs) }

// MpcRefundSigningJob is the parsed multi-sub-user form of a spark.UserSignedTxSigningJob: the single-signer pair
// (signing_nonce_commitment, user_signature) is absent and per-sub-user FROST contributions are present instead.
// additional_inputs is rejected — transfer refund txs are single-input on the SO-signed side.
type MpcRefundSigningJob struct {
	leafID        uuid.UUID
	signingPubKey keys.Public
	// rawTx is the serialized refund tx; refundTx is its parsed form (see RefundSigningJob for why both are kept).
	rawTx    []byte
	refundTx *wire.MsgTx
	// signingCommitments is the SOs' nonce commitments for the refund tx input, keyed by operator identifier.
	signingCommitments OperatorCommitments
	contributions      []SubUserSigningContribution
}

func (j *MpcRefundSigningJob) LeafID() uuid.UUID          { return j.leafID }
func (j *MpcRefundSigningJob) SigningPubKey() keys.Public { return j.signingPubKey }
func (j *MpcRefundSigningJob) RawTx() []byte              { return j.rawTx }
func (j *MpcRefundSigningJob) RefundTx() *wire.MsgTx      { return j.refundTx }
func (j *MpcRefundSigningJob) SigningCommitments() OperatorCommitments {
	return j.signingCommitments
}
func (j *MpcRefundSigningJob) Contributions() []SubUserSigningContribution {
	return slices.Clone(j.contributions)
}

// SubUserSigningContribution is one sub-user's FROST nonce commitment and signature share for one signing job.
type SubUserSigningContribution struct {
	position         uint32
	nonceCommitment  frost.SigningCommitment
	partialSignature []byte
}

func (c SubUserSigningContribution) Position() uint32 { return c.position }
func (c SubUserSigningContribution) NonceCommitment() frost.SigningCommitment {
	return c.nonceCommitment
}
func (c SubUserSigningContribution) PartialSignature() []byte {
	return slices.Clone(c.partialSignature)
}

// ParseMpcSubmission parses and structurally validates a multiparty send submission. It checks, in order: the request
// envelope (ids, sender key, presence of the package and authorization), the authorization statement's structure, the
// participant positions, the per-leaf public material, the sealed sub-share map, and the three refund signing-job
// lists. Cross-field structure is where the value is: the leaf-id sets of the authorization, the public leaves, and
// each signing-job list must be identical, and every per-sub-user list — commitment vectors, sealed blobs, signing
// contributions — must align one-to-one with the positions list, so downstream verification can index by leaf id and
// position without re-checking existence. Sealed blobs are opaque here (the per-leaf split lives inside the
// ciphertext), so per-leaf sealed coverage is deliberately NOT established at parse time: it is verified after
// unsealing, by the operator that can decrypt its own entry.
func ParseMpcSubmission(req *spark.StartTransferMpcRequest) (*MpcSubmission, error) {
	transferID, err := uuid.Parse(req.GetTransferId())
	if err != nil {
		return nil, fmt.Errorf("%w %q: %w", ErrMpcInvalidTransferID, req.GetTransferId(), err)
	}
	senderIDPub, err := keys.ParsePublicKey(req.GetOwnerIdentityPublicKey())
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMpcInvalidSenderIdentityKey, err)
	}
	pkg := req.GetMpcTransferPackage()
	if pkg == nil {
		return nil, ErrMpcMissingPackage
	}

	auth := pkg.GetAuthorization()
	if auth == nil {
		return nil, ErrMpcMissingAuthorization
	}
	if auth.GetTransferId() != req.GetTransferId() {
		return nil, fmt.Errorf("%w: %q vs %q", ErrMpcTransferIDMismatch, auth.GetTransferId(), req.GetTransferId())
	}
	expiryTime, err := parseMpcExpiry(auth)
	if err != nil {
		return nil, err
	}
	if len(auth.GetRefundSighashesDigest()) != RefundSighashesDigestSize {
		return nil, fmt.Errorf("%w: %d bytes (want %d)", ErrMpcInvalidRefundSighashesDigest, len(auth.GetRefundSighashesDigest()), RefundSighashesDigestSize)
	}
	scheme, sigBytes, err := parseMpcSignature(auth.GetSignature())
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMpcInvalidAuthSignature, err)
	}

	positions, err := parseMpcPositions(pkg.GetPositions())
	if err != nil {
		return nil, err
	}

	leaves, err := parseMpcLeaves(pkg.GetLeaves(), auth.GetLeaves(), positions)
	if err != nil {
		return nil, err
	}
	leafSet := make(map[uuid.UUID]*MpcLeaf, len(leaves))
	for _, leaf := range leaves {
		leafSet[leaf.leafID] = leaf
	}

	sealedShares, err := parseMpcSealedShares(pkg.GetKeyTweaks(), positions)
	if err != nil {
		return nil, err
	}

	leavesToSend, err := parseMpcSigningJobs(pkg.GetLeavesToSend(), leafSet, positions, "cpfp")
	if err != nil {
		return nil, err
	}
	directLeavesToSend, err := parseMpcSigningJobs(pkg.GetDirectLeavesToSend(), leafSet, positions, "direct")
	if err != nil {
		return nil, err
	}
	directFromCPFPLeavesToSend, err := parseMpcSigningJobs(pkg.GetDirectFromCpfpLeavesToSend(), leafSet, positions, "direct-from-cpfp")
	if err != nil {
		return nil, err
	}

	return &MpcSubmission{
		transferID:                 transferID,
		senderIDPub:                senderIDPub,
		expiryTime:                 expiryTime,
		positions:                  positions,
		leaves:                     leaves,
		refundSighashesDigest:      auth.GetRefundSighashesDigest(),
		authSignatureScheme:        scheme,
		authSignature:              sigBytes,
		authorization:              auth,
		sealedShares:               sealedShares,
		leavesToSend:               leavesToSend,
		directLeavesToSend:         directLeavesToSend,
		directFromCPFPLeavesToSend: directFromCPFPLeavesToSend,
	}, nil
}

// parseMpcPositions validates the submission-wide participant set: non-empty, capped, every position >= 1, strictly
// ascending (which implies unique). Every per-sub-user list in the package aligns to it by index.
func parseMpcPositions(positions []uint32) ([]uint32, error) {
	if len(positions) == 0 {
		return nil, fmt.Errorf("%w: empty", ErrMpcInvalidPositions)
	}
	if len(positions) > MaxMpcSubUsers {
		return nil, fmt.Errorf("%w: %d positions (max: %d)", ErrMpcInvalidPositions, len(positions), MaxMpcSubUsers)
	}
	if positions[0] == 0 {
		return nil, fmt.Errorf("%w: position 0", ErrMpcInvalidPositions)
	}
	for i := 1; i < len(positions); i++ {
		if positions[i] <= positions[i-1] {
			return nil, fmt.Errorf("%w: %d after %d", ErrMpcInvalidPositions, positions[i], positions[i-1])
		}
	}
	return slices.Clone(positions), nil
}

func parseMpcExpiry(auth *spark.TransferAuthorization) (time.Time, error) {
	expiry := auth.GetExpiryTime()
	if expiry == nil {
		return time.Time{}, ErrMpcMissingExpiry
	}
	// The signed payload carries the expiry as whole seconds, so nonzero nanos could never round-trip through
	// signature verification; reject them at the door.
	if expiry.GetNanos() != 0 {
		return time.Time{}, fmt.Errorf("%w: %d nanos", ErrMpcInvalidExpiry, expiry.GetNanos())
	}
	return expiry.AsTime(), nil
}

// parseMpcSignature structure-checks a scheme-tagged signature: a defined scheme and a plausible length for it.
// Callers wrap the error with the sentinel for their field.
func parseMpcSignature(sig *pbcommon.Signature) (pbcommon.SignatureScheme, []byte, error) {
	if sig == nil || len(sig.GetSignature()) == 0 {
		return 0, nil, fmt.Errorf("missing")
	}
	switch sig.GetScheme() {
	case pbcommon.SignatureScheme_SIGNATURE_SCHEME_ECDSA:
		if len(sig.GetSignature()) > MaxSignatureSize {
			return 0, nil, fmt.Errorf("%d bytes (max %d for ECDSA)", len(sig.GetSignature()), MaxSignatureSize)
		}
	case pbcommon.SignatureScheme_SIGNATURE_SCHEME_SCHNORR:
		if len(sig.GetSignature()) != SchnorrSignatureSize {
			return 0, nil, fmt.Errorf("%d bytes (want %d for Schnorr)", len(sig.GetSignature()), SchnorrSignatureSize)
		}
	default:
		return 0, nil, fmt.Errorf("unsupported scheme %d", sig.GetScheme())
	}
	return sig.GetScheme(), sig.GetSignature(), nil
}

// parseMpcLeaves joins the public per-leaf material with the authorized per-leaf facts, requiring the two lists to
// carry exactly the same leaf-id set with no duplicates on either side.
func parseMpcLeaves(pubLeaves []*spark.MpcSendLeaf, authLeaves []*spark.LeafAuthorization, positions []uint32) ([]*MpcLeaf, error) {
	if len(pubLeaves) == 0 {
		return nil, ErrMpcNoLeaves
	}
	if len(pubLeaves) != len(authLeaves) {
		return nil, fmt.Errorf("%w: %d package leaves, %d authorized leaves", ErrMpcLeafSetMismatch, len(pubLeaves), len(authLeaves))
	}

	authByLeaf := make(map[uuid.UUID]*spark.LeafAuthorization, len(authLeaves))
	for _, authLeaf := range authLeaves {
		leafID, err := uuid.Parse(authLeaf.GetLeafId())
		if err != nil {
			return nil, fmt.Errorf("%w %q (authorization): %w", ErrInvalidLeafID, authLeaf.GetLeafId(), err)
		}
		if _, ok := authByLeaf[leafID]; ok {
			return nil, fmt.Errorf("%w %s (authorization)", ErrDuplicateLeafID, leafID)
		}
		authByLeaf[leafID] = authLeaf
	}

	leaves := make([]*MpcLeaf, len(pubLeaves))
	seen := make(map[uuid.UUID]struct{}, len(pubLeaves))
	for i, pubLeaf := range pubLeaves {
		leafID, err := uuid.Parse(pubLeaf.GetLeafId())
		if err != nil {
			return nil, fmt.Errorf("%w %q: %w", ErrInvalidLeafID, pubLeaf.GetLeafId(), err)
		}
		if _, ok := seen[leafID]; ok {
			return nil, fmt.Errorf("%w %s", ErrDuplicateLeafID, leafID)
		}
		seen[leafID] = struct{}{}
		authLeaf, ok := authByLeaf[leafID]
		if !ok {
			return nil, fmt.Errorf("%w: leaf %s not in authorization", ErrMpcLeafSetMismatch, leafID)
		}

		leaves[i], err = parseMpcLeaf(leafID, pubLeaf, authLeaf, positions)
		if err != nil {
			return nil, err
		}
	}
	return leaves, nil
}

func parseMpcLeaf(leafID uuid.UUID, pubLeaf *spark.MpcSendLeaf, authLeaf *spark.LeafAuthorization, positions []uint32) (*MpcLeaf, error) {
	ownerSigningPubKey, err := keys.ParsePublicKey(authLeaf.GetOwnerSigningPublicKey())
	if err != nil {
		return nil, fmt.Errorf("%w: owner signing public key for leaf %s: %w", ErrMpcInvalidLeafAuthorization, leafID, err)
	}
	maskCommitment, err := keys.ParsePublicKey(authLeaf.GetMaskCommitment())
	if err != nil {
		return nil, fmt.Errorf("%w: mask commitment for leaf %s: %w", ErrMpcInvalidLeafAuthorization, leafID, err)
	}
	receiverIDPub, err := keys.ParsePublicKey(authLeaf.GetReceiverIdentityPublicKey())
	if err != nil {
		return nil, fmt.Errorf("%w: receiver identity public key for leaf %s: %w", ErrMpcInvalidLeafAuthorization, leafID, err)
	}

	if len(pubLeaf.GetSecretCipher()) == 0 {
		return nil, fmt.Errorf("%w: leaf %s", ErrMpcMissingSecretCipher, leafID)
	}
	signatureScheme, signature, err := parseMpcSignature(pubLeaf.GetSignature())
	if err != nil {
		return nil, fmt.Errorf("%w: leaf %s: %w", ErrMpcInvalidLeafSignature, leafID, err)
	}

	commitments, err := parseSubUserCommitments(leafID, pubLeaf.GetSubuserCommitments(), positions)
	if err != nil {
		return nil, err
	}

	return &MpcLeaf{
		leafID:             leafID,
		amountSats:         authLeaf.GetAmountSats(),
		ownerSigningPubKey: ownerSigningPubKey,
		maskCommitment:     maskCommitment,
		receiverIDPub:      receiverIDPub,
		subUserCommitments: commitments,
		secretCipher:       pubLeaf.GetSecretCipher(),
		signatureScheme:    signatureScheme,
		signature:          signature,
	}, nil
}

// parseSubUserCommitments parses one leaf's per-sub-user Feldman commitment vectors, aligned to the package
// positions. Every vector must have the same nonzero length (they commit to polynomials of one agreed degree — the
// degree-vs-threshold check is operator-side, where the cluster config lives).
func parseSubUserCommitments(leafID uuid.UUID, asProto []*spark.SubUserCommitment, positions []uint32) ([]SubUserCommitmentVector, error) {
	if len(asProto) != len(positions) {
		return nil, fmt.Errorf("%w: %d commitment vectors, %d positions (leaf %s)", ErrMpcPositionSetMismatch, len(asProto), len(positions), leafID)
	}
	out := make([]SubUserCommitmentVector, len(asProto))
	for i, commitment := range asProto {
		position := positions[i]
		rawProofs := commitment.GetProofs()
		if len(rawProofs) == 0 {
			return nil, fmt.Errorf("%w: empty vector for position %d, leaf %s", ErrMpcInvalidProofs, position, leafID)
		}
		if len(rawProofs) > MaxMpcProofsPerVector {
			return nil, fmt.Errorf("%w: %d proofs for position %d, leaf %s (max: %d)", ErrMpcInvalidProofs, len(rawProofs), position, leafID, MaxMpcProofsPerVector)
		}
		if len(rawProofs) != len(asProto[0].GetProofs()) {
			return nil, fmt.Errorf("%w: position %d has %d proofs, position %d has %d (leaf %s)",
				ErrMpcInvalidProofs, position, len(rawProofs), positions[0], len(asProto[0].GetProofs()), leafID)
		}
		proofs := make([]keys.Public, len(rawProofs))
		var err error
		for k, rawProof := range rawProofs {
			if proofs[k], err = keys.ParsePublicKey(rawProof); err != nil {
				return nil, fmt.Errorf("%w: proof %d for position %d, leaf %s: %w", ErrMpcInvalidProofs, k, position, leafID, err)
			}
		}
		out[i] = SubUserCommitmentVector{position: position, proofs: proofs}
	}
	return out, nil
}

// parseMpcSealedShares validates the operator-keyed sealed sub-share map: every operator entry must carry exactly
// one non-empty sealed blob per participant, aligned to the package positions. Per-leaf coverage is validated after
// unsealing (the leaf structure is inside the ciphertext, readable only by its operator); which operators must be
// covered is checked operator-side against cluster config, not here.
func parseMpcSealedShares(asProto map[string]*spark.MpcOperatorShares, positions []uint32) (map[so.Identifier]map[uint32][]byte, error) {
	if len(asProto) == 0 {
		return nil, ErrMpcNoSealedShares
	}

	totalBytes := 0
	sealed := make(map[so.Identifier]map[uint32][]byte, len(asProto))
	for operatorID, operatorShares := range asProto {
		shares := operatorShares.GetShares()
		if len(shares) != len(positions) {
			return nil, fmt.Errorf("%w: %d sealed blobs, %d positions (operator %s)", ErrMpcPositionSetMismatch, len(shares), len(positions), operatorID)
		}
		byPosition := make(map[uint32][]byte, len(shares))
		for i, share := range shares {
			if len(share.GetEcies()) == 0 {
				return nil, fmt.Errorf("%w: position %d, operator %s", ErrMpcMissingSealedShare, positions[i], operatorID)
			}
			totalBytes += len(share.GetEcies())
			byPosition[positions[i]] = share.GetEcies()
		}
		sealed[operatorID] = byPosition
	}
	if totalBytes > MaxMpcSealedShareBytes {
		return nil, fmt.Errorf("%w: %d bytes (max: %d)", ErrMpcSealedSharesTooLarge, totalBytes, MaxMpcSealedShareBytes)
	}
	return sealed, nil
}

// parseMpcSigningJobs parses one refund variant's signing jobs, requiring the list to cover exactly the package's
// leaf set (multiparty MVP submissions are plain transfers, which require all three refund variants) and every job to
// be in multi-sub-user form.
func parseMpcSigningJobs(jobs []*spark.UserSignedTxSigningJob, leafSet map[uuid.UUID]*MpcLeaf, positions []uint32, variant string) ([]*MpcRefundSigningJob, error) {
	if len(jobs) != len(leafSet) {
		return nil, fmt.Errorf("%w: %d %s signing jobs, %d leaves", ErrMpcLeafSetMismatch, len(jobs), variant, len(leafSet))
	}
	parsed := make([]*MpcRefundSigningJob, len(jobs))
	seen := make(map[uuid.UUID]struct{}, len(jobs))
	for i, job := range jobs {
		leafID, err := uuid.Parse(job.GetLeafId())
		if err != nil {
			return nil, fmt.Errorf("%w %q (%s): %w", ErrInvalidLeafID, job.GetLeafId(), variant, err)
		}
		if _, ok := seen[leafID]; ok {
			return nil, fmt.Errorf("%w %s (%s)", ErrDuplicateLeafID, leafID, variant)
		}
		seen[leafID] = struct{}{}
		if _, ok := leafSet[leafID]; !ok {
			return nil, fmt.Errorf("%w: %s signing job for leaf %s not in package", ErrMpcLeafSetMismatch, variant, leafID)
		}
		if parsed[i], err = parseMpcSigningJob(leafID, job, positions, variant); err != nil {
			return nil, err
		}
	}
	return parsed, nil
}

// parseMpcSigningJob parses one multi-sub-user signing job, its contributions aligned to the package positions.
func parseMpcSigningJob(leafID uuid.UUID, job *spark.UserSignedTxSigningJob, positions []uint32, variant string) (*MpcRefundSigningJob, error) {
	// Exactly one of the two signing forms may be present; a job carrying both is malformed rather than one form
	// silently winning.
	if job.GetSigningNonceCommitment() != nil || len(job.GetUserSignature()) != 0 {
		return nil, fmt.Errorf("%w: leaf %s (%s)", ErrMpcSingleSignerFieldsPresent, leafID, variant)
	}
	if len(job.GetAdditionalInputs()) != 0 {
		return nil, fmt.Errorf("%w: leaf %s (%s)", ErrMpcAdditionalInputsNotAllowed, leafID, variant)
	}

	signingPubKey, err := keys.ParsePublicKey(job.GetSigningPublicKey())
	if err != nil {
		return nil, fmt.Errorf("%w for leaf %s (%s): %w", ErrInvalidSigningPublicKey, leafID, variant, err)
	}
	refundTx, err := common.TxFromRawTxBytes(job.GetRawTx())
	if err != nil {
		return nil, fmt.Errorf("%w for leaf %s (%s): %w", ErrInvalidRefundTx, leafID, variant, err)
	}
	// The single-party parser guarantees at least one input implicitly (input 0's signing material is mandatory and
	// is checked against TxIn); with per-job contributions there is no per-input material, so check the tx directly.
	if len(refundTx.TxIn) == 0 {
		return nil, fmt.Errorf("%w for leaf %s (%s): no inputs", ErrInvalidRefundTx, leafID, variant)
	}
	operatorCommitments, err := parseOperatorCommitments(job.GetSigningCommitments())
	if err != nil {
		return nil, fmt.Errorf("leaf %s (%s): %w", leafID, variant, err)
	}

	rawContributions := job.GetSubuserContributions()
	if len(rawContributions) != len(positions) {
		return nil, fmt.Errorf("%w: %d contributions, %d positions (leaf %s, %s)", ErrMpcPositionSetMismatch, len(rawContributions), len(positions), leafID, variant)
	}
	contributions := make([]SubUserSigningContribution, len(rawContributions))
	for i, contribution := range rawContributions {
		position := positions[i]
		nonceCommitment := frost.SigningCommitment{}
		if err := nonceCommitment.UnmarshalProto(contribution.GetNonceCommitment()); err != nil {
			return nil, fmt.Errorf("%w for position %d, leaf %s (%s): %w", ErrInvalidNonceCommitment, position, leafID, variant, err)
		}
		if len(contribution.GetPartialSignature()) != MpcPartialSignatureSize {
			return nil, fmt.Errorf("%w: %d bytes for position %d, leaf %s (%s)", ErrMpcInvalidPartialSignature, len(contribution.GetPartialSignature()), position, leafID, variant)
		}
		contributions[i] = SubUserSigningContribution{
			position:         position,
			nonceCommitment:  nonceCommitment,
			partialSignature: contribution.GetPartialSignature(),
		}
	}
	return &MpcRefundSigningJob{
		leafID:             leafID,
		signingPubKey:      signingPubKey,
		rawTx:              job.GetRawTx(),
		refundTx:           refundTx,
		signingCommitments: operatorCommitments,
		contributions:      contributions,
	}, nil
}
