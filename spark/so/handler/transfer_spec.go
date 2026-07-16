package handler

import (
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
)

// transferSpec is the seam between send-request handling and the
// transfer core. It carries everything a send commits, after the
// request-handling front has structurally validated it and — for the key
// tweaks — verified it against the sender's signature and this SO's share
// checks (ValidateTransferPackage). The core (createTransferV3 and below)
// consumes the spec and never re-reads the wire request, so a different
// request front producing the same spec commits the same transfer.
//
// Not a sealed type: its fields are package-visible and some (refunds, leaf
// IDs) are still raw, so holding one is a convention, not a proof — until the
// seam gets validating constructors and fully-validated fields in its own
// package.
//
// Check ownership across the seam: the request front owns authoritative
// validation (structure, signature, Feldman share checks, per-operator
// pubkey-share consistency). Commit-time re-validation of the pubkey-share
// map in helper.TweakLeafKeyUpdate is deliberate defense-in-depth for the
// persisted-then-reloaded tweak, not a second authority; new checks belong in
// the front.
//
// keyTweaks holds validatedKeyTweak values, each wrapping the wire proto
// (*pbspark.SendLeafKeyTweak): that proto stays the carrier end to end — it
// is the persisted encoding on TransferLeaf rows and is re-parsed at commit.
// Changing the persisted encoding is a deploy-compatibility problem (old
// rows, in-flight transfers) and is intentionally out of scope here.
type transferSpec struct {
	transferID      uuid.UUID
	senderIDPK      keys.Public
	receivers       []keys.Public
	leafReceiverMap map[string]keys.Public
	expiryTime      time.Time
	// pkgLeafIDs distinguishes the package shape (non-nil) from the legacy
	// request shape (nil) and carries the leaf-ID lists the core's
	// consistency checks run over.
	pkgLeafIDs *transferPackageLeafIDs
	// Per-leaf refund transactions to persist, keyed by leaf ID. For request
	// fronts that receive user signatures separately (legacy participant
	// path), these are the signature-applied transactions.
	cpfpRefunds   map[string][]byte
	directRefunds map[string][]byte
	dfcRefunds    map[string][]byte
	// keyTweaks holds this SO's validated per-leaf key tweaks, as returned by
	// ValidateTransferPackage.
	keyTweaks map[string]validatedKeyTweak
	// sparkInvoice is non-empty only for invoice-paying sends.
	sparkInvoice string
}
