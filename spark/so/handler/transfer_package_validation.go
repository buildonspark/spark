package handler

import (
	"bytes"
	"context"
	"fmt"
	"math/big"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	eciesgo "github.com/ecies/go/v2"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	secretsharing "github.com/lightsparkdev/spark/common/secret_sharing"
	pbspark "github.com/lightsparkdev/spark/proto/spark"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/knobs"
	"google.golang.org/protobuf/proto"
)

// senderTweakProofSource declares which side of the transfer-package fan-out a
// ValidateTransferPackage caller is on. Each SO can decrypt only its own slice
// of the key-tweak package, and the package signature binds the ciphertext map,
// not the equality of the plaintexts — so slices built from different
// polynomials all pass slice-local validation, and only comparing plaintext
// proofs across operators catches the divergence. Making the source a required
// parameter means no caller can skip that comparison by omission.
type senderTweakProofSource struct {
	isCoordinator bool
	proofs        map[string]*pbspark.SecretProof
}

// asCoordinator marks the caller as the operator that fans the package out: its
// own decrypted slice is the reference the other operators are checked against,
// so there is nothing to compare locally.
func asCoordinator() senderTweakProofSource {
	return senderTweakProofSource{isCoordinator: true}
}

// asParticipant carries the coordinator's fanned-out proofs, which must be
// present and match the proofs this SO decrypts from its own slice.
func asParticipant(proofs map[string]*pbspark.SecretProof) senderTweakProofSource {
	return senderTweakProofSource{proofs: proofs}
}

// ValidateTransferPackage checks a sender-supplied TransferPackage and returns
// this SO's validated per-leaf key tweaks, keyed by leaf ID. The returned map
// is the boundary between request handling and the transfer core: everything
// downstream (persistence, key rotation) consumes these validated tweaks and
// never re-reads the wire package.
//
// The stage order is load-bearing: it determines which error a multiply-invalid
// package reports (e.g. the user signature is verified only after this SO's
// key-tweak slice decrypts and covers the refund leaves), and callers and
// clients observe that precedence.
func (h *BaseTransferHandler) ValidateTransferPackage(
	ctx context.Context,
	transferID uuid.UUID,
	pkg *pbspark.TransferPackage,
	senderIdentityPubKey keys.Public,
	requireDirectFromCpfpLeaves bool,
	proofSource senderTweakProofSource,
) (map[string]validatedKeyTweak, error) {
	// If the transfer package is nil, we don't need to validate it.
	if pkg == nil {
		return nil, nil
	}

	transferLimit := transferLeafLimit(ctx)

	leavesToSendIDs, err := validateTransferPackageStructure(pkg, transferLimit, requireDirectFromCpfpLeaves)
	if err != nil {
		return nil, err
	}

	leafTweaksMap, err := h.decryptOwnKeyTweaks(pkg, transferLimit)
	if err != nil {
		return nil, err
	}

	if err := checkKeyTweakCoverage(transferID, leafTweaksMap, leavesToSendIDs); err != nil {
		return nil, err
	}

	if err := verifyTransferPackageSignature(transferID, pkg, senderIdentityPubKey); err != nil {
		return nil, err
	}

	validated, err := h.validateKeyTweakShares(leafTweaksMap)
	if err != nil {
		return nil, err
	}

	if !proofSource.isCoordinator {
		if err := verifySenderKeyTweakProofsMatch(validated, proofSource.proofs); err != nil {
			return nil, err
		}
	}

	return validated, nil
}

// transferLeafLimit returns the per-transfer leaf limit, runtime-configurable
// via KnobSoTransferLimit with MaxLeavesToSend as the fallback.
func transferLeafLimit(ctx context.Context) int {
	transferLimit := MaxLeavesToSend
	knobService := knobs.GetKnobsService(ctx)
	if knobService != nil {
		knobLimit := knobService.GetValue(knobs.KnobSoTransferLimit, 0)
		if knobLimit > 0 {
			transferLimit = int(knobLimit)
		}
	}
	return transferLimit
}

// validateTransferPackageStructure enforces the request-shape rules that need
// no key material: presence of the key-tweak package, size and count limits
// (resource-exhaustion defense), leaf-ID consistency across the three refund
// variants, and user-signature presence. Returns the set of leaf IDs being
// sent, against which the decrypted key tweaks are later checked for coverage.
func validateTransferPackageStructure(
	pkg *pbspark.TransferPackage,
	transferLimit int,
	requireDirectFromCpfpLeaves bool,
) (map[string]struct{}, error) {
	if len(pkg.GetKeyTweakPackage()) == 0 {
		return nil, fmt.Errorf("key tweak package is empty")
	}

	// Input size and count validation - prevent resource exhaustion
	if len(pkg.GetLeavesToSend()) > transferLimit {
		return nil, fmt.Errorf("too many leaves to send: %d (max: %d)", len(pkg.GetLeavesToSend()), transferLimit)
	}

	if len(pkg.GetDirectLeavesToSend()) > transferLimit {
		return nil, fmt.Errorf("too many direct leaves to send: %d (max: %d)", len(pkg.GetDirectLeavesToSend()), transferLimit)
	}

	if len(pkg.GetDirectFromCpfpLeavesToSend()) > transferLimit {
		return nil, fmt.Errorf("too many direct from cpfp leaves to send: %d (max: %d)", len(pkg.GetDirectFromCpfpLeavesToSend()), transferLimit)
	}

	// Validate key tweak package size
	totalSize := 0
	for _, ciphertext := range pkg.GetKeyTweakPackage() {
		totalSize += len(ciphertext)
	}
	if totalSize > MaxKeyTweakPackageSize {
		return nil, fmt.Errorf("key tweak package too large: %d bytes (max: %d)", totalSize, MaxKeyTweakPackageSize)
	}

	// Validate leaf IDs and check for duplicates/orphans/mismatches across lists.
	leavesToSendIDs := make(map[string]struct{}, len(pkg.GetLeavesToSend()))
	for i, leaf := range pkg.GetLeavesToSend() {
		if leaf == nil {
			return nil, fmt.Errorf("leaves_to_send[%d] is required", i)
		}
		parsed, err := uuid.Parse(leaf.GetLeafId())
		if err != nil {
			return nil, fmt.Errorf("unable to parse leaf_id as a uuid %s: %w", leaf.GetLeafId(), err)
		}
		leafID := parsed.String()
		if _, exists := leavesToSendIDs[leafID]; exists {
			return nil, fmt.Errorf("duplicate leaf id in LeavesToSend: %s", leafID)
		}
		leavesToSendIDs[leafID] = struct{}{}
	}

	directLeafIDs := make(map[string]struct{}, len(pkg.GetDirectLeavesToSend()))
	for i, leaf := range pkg.GetDirectLeavesToSend() {
		if leaf == nil {
			return nil, fmt.Errorf("direct_leaves_to_send[%d] is required", i)
		}
		parsed, err := uuid.Parse(leaf.GetLeafId())
		if err != nil {
			return nil, fmt.Errorf("unable to parse direct_leaves_to_send leaf_id as a uuid %s: %w", leaf.GetLeafId(), err)
		}
		leafID := parsed.String()
		if _, ok := leavesToSendIDs[leafID]; !ok {
			return nil, fmt.Errorf("orphan leaf in DirectLeavesToSend with ID %s not found in LeavesToSend", leaf.GetLeafId())
		}
		if _, exists := directLeafIDs[leafID]; exists {
			return nil, fmt.Errorf("duplicate leaf id in DirectLeavesToSend: %s", leafID)
		}
		directLeafIDs[leafID] = struct{}{}
	}

	if requireDirectFromCpfpLeaves {
		if len(pkg.GetLeavesToSend()) != len(pkg.GetDirectFromCpfpLeavesToSend()) {
			return nil, fmt.Errorf("mismatched number of leaves: LeavesToSend (%d) and DirectFromCpfpLeavesToSend (%d) must be equal", len(pkg.GetLeavesToSend()), len(pkg.GetDirectFromCpfpLeavesToSend()))
		}
	} else if len(pkg.GetDirectFromCpfpLeavesToSend()) > 0 && len(pkg.GetLeavesToSend()) != len(pkg.GetDirectFromCpfpLeavesToSend()) {
		return nil, fmt.Errorf("mismatched number of leaves: LeavesToSend (%d) and DirectFromCpfpLeavesToSend (%d) must be equal", len(pkg.GetLeavesToSend()), len(pkg.GetDirectFromCpfpLeavesToSend()))
	}
	directFromCpfpLeafIDs := make(map[string]struct{}, len(pkg.GetDirectFromCpfpLeavesToSend()))
	for i, leaf := range pkg.GetDirectFromCpfpLeavesToSend() {
		if leaf == nil {
			return nil, fmt.Errorf("direct_from_cpfp_leaves_to_send[%d] is required", i)
		}
		parsed, err := uuid.Parse(leaf.GetLeafId())
		if err != nil {
			return nil, fmt.Errorf("unable to parse direct_from_cpfp_leaves_to_send leaf_id as a uuid %s: %w", leaf.GetLeafId(), err)
		}
		leafID := parsed.String()
		if _, ok := leavesToSendIDs[leafID]; !ok {
			return nil, fmt.Errorf("mismatched leaves: DirectFromCpfpLeavesToSend contains leaf ID %s not in LeavesToSend", leaf.GetLeafId())
		}
		if _, exists := directFromCpfpLeafIDs[leafID]; exists {
			return nil, fmt.Errorf("duplicate leaf id in DirectFromCpfpLeavesToSend: %s", leafID)
		}
		directFromCpfpLeafIDs[leafID] = struct{}{}
	}

	// Signature validation - prevent replay/DoS
	if len(pkg.GetUserSignature()) == 0 {
		return nil, fmt.Errorf("user signature cannot be empty")
	}

	if len(pkg.GetUserSignature()) > MaxSignatureSize {
		return nil, fmt.Errorf("user signature too large: %d bytes (max: %d)", len(pkg.GetUserSignature()), MaxSignatureSize)
	}

	return leavesToSendIDs, nil
}

// decryptOwnKeyTweaks decrypts this SO's slice of the key-tweak package with
// the SO identity key and unmarshals it into a per-leaf map, enforcing the
// per-field size and count limits (resource-exhaustion defense) along the way.
func (h *BaseTransferHandler) decryptOwnKeyTweaks(
	pkg *pbspark.TransferPackage,
	transferLimit int,
) (map[string]*pbspark.SendLeafKeyTweak, error) {
	leafTweaksCipherText := pkg.GetKeyTweakPackage()[h.config.Identifier]
	if leafTweaksCipherText == nil {
		return nil, fmt.Errorf("no key tweaks found for SO %s", h.config.Identifier)
	}

	// Encrypted data validation - prevent decryption attacks
	if len(leafTweaksCipherText) == 0 {
		return nil, fmt.Errorf("encrypted key tweaks cannot be empty")
	}

	if len(leafTweaksCipherText) > MaxKeyTweakPackageSize {
		return nil, fmt.Errorf("encrypted key tweaks too large: %d bytes (max: %d)", len(leafTweaksCipherText), MaxKeyTweakPackageSize)
	}

	decryptionPrivateKey := eciesgo.NewPrivateKeyFromBytes(h.config.IdentityPrivateKey.Serialize())
	leafTweaksBinary, err := eciesgo.Decrypt(decryptionPrivateKey, leafTweaksCipherText)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt key tweaks: %w", err)
	}

	leafTweaks := &pbspark.SendLeafKeyTweaks{}
	err = proto.Unmarshal(leafTweaksBinary, leafTweaks)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal key tweaks: %w", err)
	}

	// Memory usage validation - prevent OOM
	totalLeafCount := len(leafTweaks.GetLeavesToSend())
	if totalLeafCount > transferLimit {
		return nil, fmt.Errorf("too many leaves in key tweaks: %d (max: %d)", totalLeafCount, transferLimit)
	}

	// This should equal the number of SOs
	maxPubkeySharesTweakCount := len(h.config.GetSigningOperatorList())
	maxProofsCount := int(h.config.Threshold)

	// Estimate memory usage for the map
	estimatedMemory := totalLeafCount * (MaxLeafIdLength + MaxSecretShareSize + maxProofsCount*33 + maxPubkeySharesTweakCount*33)
	if estimatedMemory > MaxEstimatedMemoryUsage {
		return nil, fmt.Errorf("estimated memory usage too high: %d bytes (max: %d)", estimatedMemory, MaxEstimatedMemoryUsage)
	}

	leafTweaksMap := make(map[string]*pbspark.SendLeafKeyTweak)
	for _, leafTweak := range leafTweaks.GetLeavesToSend() {
		// Validate leaf ID in key tweaks
		parsedLeafID, err := uuid.Parse(leafTweak.GetLeafId())
		if err != nil {
			return nil, fmt.Errorf("unable to parse key tweaks leaf_id as a uuid %s: %w", leafTweak.GetLeafId(), err)
		}
		leafID := parsedLeafID.String()
		if _, exists := leafTweaksMap[leafID]; exists {
			return nil, fmt.Errorf("duplicate leaf id in encrypted key tweaks: %s", leafID)
		}

		// Validate secret share size
		if len(leafTweak.GetSecretShareTweak().GetSecretShare()) > MaxSecretShareSize {
			return nil, fmt.Errorf("secret share too large: %d bytes (max: %d)", len(leafTweak.GetSecretShareTweak().GetSecretShare()), MaxSecretShareSize)
		}

		// Validate proofs count
		if len(leafTweak.GetSecretShareTweak().GetProofs()) > maxProofsCount {
			return nil, fmt.Errorf("too many proofs: %d (max: %d)", len(leafTweak.GetSecretShareTweak().GetProofs()), maxProofsCount)
		}

		// Validate pubkey shares count
		if len(leafTweak.GetPubkeySharesTweak()) > maxPubkeySharesTweakCount {
			return nil, fmt.Errorf("too many pubkey shares: %d (max: %d)", len(leafTweak.GetPubkeySharesTweak()), maxPubkeySharesTweakCount)
		}

		// The tweaks arrive ECIES-encrypted, so the gRPC validation interceptor
		// never sees them. Enforce the typed signature's proto contract (defined
		// non-zero scheme, non-empty bytes) here, before it is stored and
		// forwarded to the receiver.
		if ts := leafTweak.GetTypedSignature(); ts != nil {
			if err := ts.Validate(); err != nil {
				return nil, fmt.Errorf("invalid typed signature for leaf %s: %w", leafID, err)
			}
		}

		leafTweaksMap[leafID] = leafTweak
	}

	return leafTweaksMap, nil
}

// checkKeyTweakCoverage enforces that the decrypted key tweaks and the refund
// transactions cover exactly the same set of leaves.
func checkKeyTweakCoverage(
	transferID uuid.UUID,
	leafTweaksMap map[string]*pbspark.SendLeafKeyTweak,
	leavesToSendIDs map[string]struct{},
) error {
	if len(leafTweaksMap) != len(leavesToSendIDs) {
		return fmt.Errorf("key tweak count mismatch in transfer %s: refund transactions have %d leaves, key tweak package has %d entries",
			transferID, len(leavesToSendIDs), len(leafTweaksMap))
	}
	for leafID := range leavesToSendIDs {
		if _, ok := leafTweaksMap[leafID]; !ok {
			return fmt.Errorf("key tweak missing for leaf %s in transfer %s",
				leafID, transferID)
		}
	}
	return nil
}

// verifyTransferPackageSignature verifies the sender's identity signature over
// the package signing payload. The signed payload covers the encrypted
// key-tweak package, so the tweaks (including each leaf's secret cipher and
// per-leaf signature) are bound to the sender even though this SO can only
// decrypt its own slice.
func verifyTransferPackageSignature(
	transferID uuid.UUID,
	pkg *pbspark.TransferPackage,
	senderIdentityPubKey keys.Public,
) error {
	payloadToVerify := common.GetTransferPackageSigningPayload(transferID, pkg)

	if err := common.VerifyECDSASignature(senderIdentityPubKey, pkg.GetUserSignature(), payloadToVerify); err != nil {
		return fmt.Errorf("unable to verify user signature: %w", err)
	}
	return nil
}

// validatedKeyTweak is a sender key tweak that has passed this SO's share
// validation (Feldman check plus per-operator pubkey-share consistency) and,
// on participants, ValidateTransferPackage's cross-operator proof comparison.
// The zero value is meaningless; the only construction site is
// validateKeyTweakShares, so holding one is evidence of validation.
type validatedKeyTweak struct{ pb *pbspark.SendLeafKeyTweak }

// Proto returns the tweak's wire encoding, which is also its persistence
// encoding on TransferLeaf rows.
func (v validatedKeyTweak) Proto() *pbspark.SendLeafKeyTweak { return v.pb }

// validateKeyTweakShares runs the cryptographic validation on each decrypted
// key tweak: this SO's sub-share must lie on the polynomial committed to by
// the proofs (Feldman verification), and every per-operator pubkey share must
// match that same polynomial's public evaluation at that operator's index.
//
// Both read the proofs out of the slice under test, so they establish only that
// one slice is self-consistent, never that all operators got one polynomial —
// that is ValidateTransferPackage's cross-operator comparison.
func (h *BaseTransferHandler) validateKeyTweakShares(leafTweaksMap map[string]*pbspark.SendLeafKeyTweak) (map[string]validatedKeyTweak, error) {
	validated := make(map[string]validatedKeyTweak, len(leafTweaksMap))
	for leafID, leafTweak := range leafTweaksMap {
		shareInt := new(big.Int).SetBytes(leafTweak.GetSecretShareTweak().GetSecretShare())
		err := secretsharing.ValidateShare(
			&secretsharing.VerifiableSecretShare{
				SecretShare: secretsharing.SecretShare{
					FieldModulus: secp256k1.S256().N,
					Threshold:    int(h.config.Threshold),
					Index:        big.NewInt(int64(h.config.Index + 1)),
					Share:        shareInt,
				},
				Proofs: leafTweak.GetSecretShareTweak().GetProofs(),
			},
		)
		if err != nil {
			return nil, fmt.Errorf("unable to validate share: %w", err)
		}
		// Verify every PubkeySharesTweak entry matches the polynomial commitment derived from
		// the supplied Proofs at each operator's share index.
		for soID, operator := range h.config.SigningOperatorMap {
			pubkeyTweakBytes, ok := leafTweak.GetPubkeySharesTweak()[soID]
			if !ok {
				return nil, fmt.Errorf("pubkey share tweak missing for operator %s in leaf %s", soID, leafTweak.GetLeafId())
			}
			if _, err := keys.ParsePublicKey(pubkeyTweakBytes); err != nil {
				return nil, fmt.Errorf("unable to parse pubkey share tweak for operator %s leaf %s: %w", soID, leafTweak.GetLeafId(), err)
			}
			expectedPub, err := secretsharing.EvaluatePolynomialCommitment(
				leafTweak.GetSecretShareTweak().GetProofs(),
				big.NewInt(int64(operator.ID+1)),
				secp256k1.S256().N,
			)
			if err != nil {
				return nil, fmt.Errorf("unable to evaluate polynomial commitment for operator %s leaf %s: %w", soID, leafTweak.GetLeafId(), err)
			}
			if !bytes.Equal(expectedPub.Serialize(), pubkeyTweakBytes) {
				return nil, fmt.Errorf("pubkey share tweak for operator %s does not match polynomial commitment for leaf %s", soID, leafTweak.GetLeafId())
			}
		}
		validated[leafID] = validatedKeyTweak{pb: leafTweak}
	}
	return validated, nil
}

// coordinatorSenderKeyTweakProofs reads the proofs out of this SO's own slice for
// a coordinator to fan out with its prepare op. Only that slice is decrypted; the
// per-leaf verification stays in Prepare, which runs concurrently on every SO.
func (h *BaseTransferHandler) coordinatorSenderKeyTweakProofs(ctx context.Context, pkg *pbspark.TransferPackage) (map[string]*pbspark.SecretProof, error) {
	if pkg == nil {
		return nil, nil
	}
	leafTweaksMap, err := h.decryptOwnKeyTweaks(pkg, transferLeafLimit(ctx))
	if err != nil {
		return nil, err
	}
	proofs := make(map[string]*pbspark.SecretProof, len(leafTweaksMap))
	for leafID, leafTweak := range leafTweaksMap {
		if leafTweak.GetSecretShareTweak() == nil {
			return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("secret share tweak missing for leaf %s", leafID))
		}
		proofs[leafID] = &pbspark.SecretProof{Proofs: leafTweak.GetSecretShareTweak().GetProofs()}
	}
	return proofs, nil
}

// transferPackageLeafIDs carries just the leaf IDs of a TransferPackage's
// three refund-transaction lists — the only facts the transfer core needs
// from the wire package. A nil value means the request used the legacy
// (packageless) shape.
type transferPackageLeafIDs struct {
	leavesToSend         []string
	directLeavesToSend   []string
	directFromCpfpLeaves []string
}

// transferPackageLeafIDLists digests a wire TransferPackage into its leaf-ID
// lists. Nil in, nil out, preserving the package-vs-legacy distinction.
func transferPackageLeafIDLists(pkg *pbspark.TransferPackage) *transferPackageLeafIDs {
	if pkg == nil {
		return nil
	}
	out := &transferPackageLeafIDs{
		leavesToSend:         make([]string, 0, len(pkg.GetLeavesToSend())),
		directLeavesToSend:   make([]string, 0, len(pkg.GetDirectLeavesToSend())),
		directFromCpfpLeaves: make([]string, 0, len(pkg.GetDirectFromCpfpLeavesToSend())),
	}
	for _, leaf := range pkg.GetLeavesToSend() {
		out.leavesToSend = append(out.leavesToSend, leaf.GetLeafId())
	}
	for _, leaf := range pkg.GetDirectLeavesToSend() {
		out.directLeavesToSend = append(out.directLeavesToSend, leaf.GetLeafId())
	}
	for _, leaf := range pkg.GetDirectFromCpfpLeavesToSend() {
		out.directFromCpfpLeaves = append(out.directFromCpfpLeaves, leaf.GetLeafId())
	}
	return out
}
