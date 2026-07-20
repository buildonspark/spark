package handler

import (
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
)

// sameTransferID reports whether two transfer-id strings denote the same UUID,
// tolerating the non-canonical forms uuid.Parse accepts (uppercase, braced,
// urn, no-hyphen). A prepare op persists the client's verbatim transfer_id while
// the commit/rollback payloads canonicalize it through uuid.Parse(...).String()
// first, so a raw string comparison would spuriously reject a valid
// non-canonical id — permanently fencing the coordinator's decision and
// stranding participants IN_FLIGHT. An unparseable id on either side is treated
// as a mismatch (it can only be forged: real ids are parsed upstream before
// reaching here). Used by PrepareBoundFlowHandler implementations that bind a
// transfer id.
func sameTransferID(a, b string) bool {
	ua, err := uuid.Parse(a)
	if err != nil {
		return false
	}
	ub, err := uuid.Parse(b)
	if err != nil {
		return false
	}
	return ua == ub
}

// samePublicKey reports whether two serialized public keys denote the same
// point, tolerating the different ANSI X9.62 encodings keys.ParsePublicKey
// accepts (e.g. 33-byte compressed vs 65-byte uncompressed). A prepare op
// persists the client's verbatim identity key while the commit/rollback payloads
// canonicalize it through Public.Serialize() (compressed), so a raw bytes.Equal
// would spuriously reject a valid uncompressed-supplied key and permanently
// fence the decision, stranding participants IN_FLIGHT. An unparseable key on
// either side is treated as a mismatch (real keys are parsed upstream before
// reaching here). Used by PrepareBoundFlowHandler implementations that bind an
// identity key.
func samePublicKey(a, b []byte) bool {
	pa, err := keys.ParsePublicKey(a)
	if err != nil {
		return false
	}
	pb, err := keys.ParsePublicKey(b)
	if err != nil {
		return false
	}
	return pa.Equals(pb)
}
