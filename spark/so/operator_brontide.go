package so

import (
	"fmt"
	"net"
	"strings"

	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/rpcauth/brontide"
	"google.golang.org/grpc"
)

// EnableBrontideClient installs a brontide-flavored connection factory on this SigningOperator's internal-listener
// slot, using localPriv as the static key. Only the internal-flavored connection methods route through it.
//
// Returns an error if the operator does not have the metadata required for a brontide handshake.
func (s *SigningOperator) EnableBrontideClient(localPriv keys.Private) error {
	if s.InternalAddress == "" {
		return fmt.Errorf("operator %s: internal_address required when internal_rpc.transport is brontide", s.Identifier)
	}
	if s.InternalAddressDkg == "" {
		return fmt.Errorf("operator %s: internal_address_dkg required when internal_rpc.transport is brontide", s.Identifier)
	}
	if s.IdentityPublicKey.IsZero() {
		return fmt.Errorf("operator %s: identity_public_key required when internal_rpc.transport is brontide", s.Identifier)
	}
	// Brontide dials verify the public-listener cert at CertPath against the internal_address hostname (see
	// NewGRPCConnection below), which only works because both listeners share a hostname. Enforce that here so a
	// split-hostname misconfiguration fails at provisioning instead of as a TLS verification error at first dial.
	internalHost, rpcHost := addressHost(s.InternalAddress), addressHost(s.AddressRpc)
	if !strings.EqualFold(internalHost, rpcHost) {
		return fmt.Errorf("operator %s: internal_address host %q must match address host %q so the cert at cert_path verifies for brontide dials",
			s.Identifier, internalHost, rpcHost)
	}
	internalDkgHost, dkgHost := addressHost(s.InternalAddressDkg), addressHost(s.AddressDkg)
	if !strings.EqualFold(internalDkgHost, dkgHost) {
		return fmt.Errorf("operator %s: internal_address_dkg host %q must match address_dkg host %q so the cert at cert_path verifies for brontide dials",
			s.Identifier, internalDkgHost, dkgHost)
	}
	// Exercise the same trust-anchor loading NewGRPCConnection performs on every dial, so a missing or malformed cert
	// fails at provisioning time instead of on the first internal RPC after the knob flips on.
	if _, err := common.BuildTLSCredentialsFromCert(s.InternalAddress, s.CertPath); err != nil {
		return fmt.Errorf("operator %s: load TLS cert %q: %w", s.Identifier, s.CertPath, err)
	}
	if _, err := common.BuildTLSCredentialsFromCert(s.InternalAddressDkg, s.CertPath); err != nil {
		return fmt.Errorf("operator %s: load TLS cert %q for internal_address_dkg: %w", s.Identifier, s.CertPath, err)
	}
	s.brontideAvailable = true
	// Install the brontide factory on the SEPARATE internal slot so that cross-operator SparkService calls keep working
	// over plain TLS against the peer's public listener. Only the explicitly-internal callers route through this factory.
	s.internalConnFactory = &operatorConnectionFactoryBrontide{
		operator:        s,
		localPrivateKey: localPriv,
	}
	return nil
}

// addressHost returns the host portion of a host:port address, or the address itself when it carries no port.
func addressHost(address string) string {
	if host, _, err := net.SplitHostPort(address); err == nil {
		return host
	}
	return address
}

// operatorConnectionFactoryBrontide constructs gRPC client connections that run brontide on top of one-way TLS. It's the
// brontide counterpart to operatorConnectionFactorySecure and is wired up via EnableBrontideClient.
type operatorConnectionFactoryBrontide struct {
	operator        *SigningOperator
	localPrivateKey keys.Private
}

func (o *operatorConnectionFactoryBrontide) NewGRPCConnection(address string, retryPolicy *common.RetryPolicyConfig, clientTimeoutConfig *common.ClientTimeoutConfig) (*grpc.ClientConn, error) {
	// address is one of the operator's internal addresses, while CertPath is shared with its public listener.
	tlsCreds, err := common.BuildTLSCredentialsFromCert(address, o.operator.CertPath)
	if err != nil {
		return nil, fmt.Errorf("brontide client: build TLS credentials: %w", err)
	}

	brontideCreds, err := brontide.NewClientCredentials(brontide.ClientConfig{
		Inner:           tlsCreds,
		LocalPrivateKey: o.localPrivateKey,
		RemotePublicKey: o.operator.IdentityPublicKey,
	})
	if err != nil {
		return nil, fmt.Errorf("brontide client: construct credentials: %w", err)
	}

	return common.NewGRPCConnectionWithCredentials(address, brontideCreds, retryPolicy, clientTimeoutConfig, defaultOperatorDialOpts()...)
}
