package brontide

import (
	"github.com/lightsparkdev/spark/common/keys"
)

// Peer is the minimum identifying information about a remote operator that the brontide credentials need to expose on
// AuthInfo. This is deliberately small (and not *so.SigningOperator) so the brontide package doesn't depend on the so package.
//
// Callers that need richer operator metadata can resolve Peer.IdentityPublicKey back to a *so.SigningOperator via
// Config.SigningOperatorMap.
type Peer struct {
	// Identifier is the operator's stable string identifier (matches so.SigningOperator.Identifier).
	Identifier string

	// IdentityPublicKey is the operator's secp256k1 identity public key, recovered from the Noise_XK handshake.
	IdentityPublicKey keys.Public
}

// PeerLookup resolves a remote static public key (recovered from a brontide handshake) to a Peer record. It must return
// nil if the key does not correspond to a known operator.
//
// Implementations must be safe for concurrent use.
type PeerLookup interface {
	LookupPeer(identityPublicKey keys.Public) *Peer
}

// PeerLookupFunc adapts a plain function to the PeerLookup interface.
type PeerLookupFunc func(keys.Public) *Peer

func (f PeerLookupFunc) LookupPeer(p keys.Public) *Peer { return f(p) }
