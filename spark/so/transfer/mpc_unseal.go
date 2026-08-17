package transfer

import (
	"fmt"

	eciesgo "github.com/ecies/go/v2"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"google.golang.org/protobuf/proto"
)

var (
	ErrMpcMissingOperatorEntry  = fmt.Errorf("no sealed sub-shares for this operator")
	ErrMpcUnsealFailed          = fmt.Errorf("sealed sub-share cannot be unsealed")
	ErrMpcSealedReplayMismatch  = fmt.Errorf("sealed sub-share names a different transfer")
	ErrMpcSealedLeafSetMismatch = fmt.Errorf("sealed sub-shares do not cover exactly the package's leaves")
)

// UnsealShares decrypts the sealed sub-share blobs addressed to one operator and enforces the sealed contract the
// parser deferred (the per-leaf split lives inside the ciphertext, readable only by its operator): each decrypted
// payload must name this submission's transfer id — the replay guard that holds even if resharing material is ever
// reused across transfers — and must cover exactly the package's leaf set. Returns each leaf's sub-share bytes by
// participant position, the shape CombineMpcLeafTweak consumes. Failures name the participant position, since a
// blob that fails to decrypt or misdescribes the transfer is attributable to the sub-user slot that sealed it.
func (s *MpcSubmission) UnsealShares(operatorID so.Identifier, identityPrivKey keys.Private) (map[uuid.UUID]map[uint32][]byte, error) {
	blobs, ok := s.sealedShares[operatorID]
	if !ok {
		return nil, fmt.Errorf("%w: operator %s", ErrMpcMissingOperatorEntry, operatorID)
	}

	decryptionKey := eciesgo.NewPrivateKeyFromBytes(identityPrivKey.Serialize())
	byLeaf := make(map[uuid.UUID]map[uint32][]byte, len(s.leaves))
	for _, leaf := range s.leaves {
		byLeaf[leaf.leafID] = make(map[uint32][]byte, len(blobs))
	}

	// Iterating the ascending positions list (not the blob map) keeps failure attribution deterministic when
	// several blobs are bad: the lowest bad position is always the one reported.
	for _, position := range s.positions {
		blob, ok := blobs[position]
		if !ok {
			// Unreachable for parser-built submissions (the parser aligns blobs to positions); kept so any
			// future construction path fails closed with attribution.
			return nil, fmt.Errorf("%w: no sealed blob for position %d (operator %s)", ErrMpcMissingSealedShare, position, operatorID)
		}
		plaintext, err := eciesgo.Decrypt(decryptionKey, blob)
		if err != nil {
			return nil, fmt.Errorf("%w: position %d: %w", ErrMpcUnsealFailed, position, err)
		}
		payload := &spark.MpcSealedSharePayload{}
		if err := proto.Unmarshal(plaintext, payload); err != nil {
			return nil, fmt.Errorf("%w: position %d: %w", ErrMpcUnsealFailed, position, err)
		}
		if sealedTransferID := payload.GetTransferId(); sealedTransferID != s.transferID.String() {
			return nil, fmt.Errorf("%w: position %d sealed %q", ErrMpcSealedReplayMismatch, position, truncateForError(sealedTransferID))
		}
		if len(payload.GetLeafShares()) != len(s.leaves) {
			return nil, fmt.Errorf("%w: position %d sealed %d leaves, package has %d",
				ErrMpcSealedLeafSetMismatch, position, len(payload.GetLeafShares()), len(s.leaves))
		}
		for _, leafShare := range payload.GetLeafShares() {
			leafID, err := uuid.Parse(leafShare.GetLeafId())
			if err != nil {
				return nil, fmt.Errorf("%w: position %d: %w", ErrMpcSealedLeafSetMismatch, position, err)
			}
			shares, ok := byLeaf[leafID]
			if !ok {
				return nil, fmt.Errorf("%w: position %d sealed unknown leaf %s", ErrMpcSealedLeafSetMismatch, position, leafID)
			}
			if _, ok := shares[position]; ok {
				return nil, fmt.Errorf("%w: position %d sealed leaf %s twice", ErrMpcSealedLeafSetMismatch, position, leafID)
			}
			shares[position] = leafShare.GetSecretShare()
		}
	}
	return byLeaf, nil
}

// truncateForError caps attacker-supplied text before it reaches an error string: a sealed payload can carry up
// to MaxMpcSealedShareBytes of chosen plaintext (sealing needs only the operator's public key), and errors land
// in logs and gRPC statuses. 64 bytes comfortably fits any legitimate transfer id.
func truncateForError(s string) string {
	const maxLen = 64
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "…"
}
