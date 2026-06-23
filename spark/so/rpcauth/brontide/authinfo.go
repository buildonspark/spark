package brontide

import (
	"context"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
)

// AuthType is the credentials.AuthInfo identifier returned by AuthInfo.AuthType.
const AuthType = "spark-brontide"

// AuthInfo is attached to every gRPC peer that completes a brontide handshake.
// It carries both the cryptographically-authenticated remote operator (recovered from the Noise_XK exchange) and the
// underlying TLS state of the inner transport, so downstream code keeps visibility into the connection's security
// parameters.
type AuthInfo struct {
	credentials.CommonAuthInfo

	// Peer is the resolved remote operator. Always non-nil on a connection that completed both the brontide handshake
	// and the PeerLookup check.
	Peer *Peer

	// TLS is the TLS state of the inner transport. Meant for logs and policy checks that care about the TLS session.
	TLS credentials.TLSInfo
}

// AuthType implements credentials.AuthInfo.
func (AuthInfo) AuthType() string { return AuthType }

// ValidateAuthority delegates to the wrapped TLSInfo so that the optional credentials.AuthorityValidator implemented by
// credentials.NewTLS survives the wrap. Without this, any client RPC that uses grpc.CallAuthority would fail before
// being sent because gRPC checks the resolved AuthInfo for this method and the inner TLSInfo it would have used is now
// buried inside our AuthInfo.
func (a AuthInfo) ValidateAuthority(authority string) error {
	return a.TLS.ValidateAuthority(authority)
}

// PeerOperator returns the brontide-authenticated remote operator for the given server-side context, or nil if the call
// didn't arrive over a brontide-authenticated connection.
func PeerOperator(ctx context.Context) *Peer {
	p, ok := peer.FromContext(ctx)
	if !ok || p == nil {
		return nil
	}
	info, ok := p.AuthInfo.(AuthInfo)
	if !ok {
		return nil
	}
	return info.Peer
}
