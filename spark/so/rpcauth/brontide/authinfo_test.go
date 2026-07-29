package brontide_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/rpcauth/brontide"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
)

// Compile-time assertion that brontide.AuthInfo satisfies credentials.AuthorityValidator. Otherwise, any
// client RPC using grpc.CallAuthority will fail before being sent.
var _ credentials.AuthorityValidator = brontide.AuthInfo{}

func TestAuthInfoAuthType(t *testing.T) {
	assert.Equal(t, "spark-brontide", brontide.AuthInfo{}.AuthType())
	assert.Equal(t, brontide.AuthType, brontide.AuthInfo{}.AuthType())
}

func TestValidateAuthorityDelegatesToTLS(t *testing.T) {
	t.Run("empty TLS state returns the inner error", func(t *testing.T) {
		// TLSInfo.ValidateAuthority returns a specific "no peer certificates" error when State.PeerCertificates is
		// empty. We just need to confirm our wrapper returns the same error rather than nil.
		err := brontide.AuthInfo{}.ValidateAuthority("example.test")
		require.ErrorContains(t, err, "no peer certificates")
	})

	t.Run("populated TLS state validates the leaf cert hostname", func(t *testing.T) {
		rng := rand.NewChaCha8([32]byte{})
		cert := mintTestCert(t, "good.example", rng)

		info := brontide.AuthInfo{
			TLS: credentials.TLSInfo{State: tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}},
		}

		assert.NoError(t, info.ValidateAuthority("good.example"))
		assert.NoError(t, info.ValidateAuthority("good.example:443"))
		assert.Error(t, info.ValidateAuthority("bad.example"))
	})
}

// mintTestCert returns a self-signed cert whose only SAN is dnsName.
func mintTestCert(t *testing.T, dnsName string, rng io.Reader) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rng)
	require.NoError(t, err)
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: dnsName},
		DNSNames:     []string{dnsName},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rng, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return cert
}

func TestPeerOperator(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{})
	pub := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	expected := &brontide.Peer{Identifier: "op-a", IdentityPublicKey: pub}

	t.Run("nil when no peer in context", func(t *testing.T) {
		require.Nil(t, brontide.PeerOperator(t.Context()))
	})

	t.Run("nil when AuthInfo is not brontide", func(t *testing.T) {
		ctx := peer.NewContext(t.Context(), &peer.Peer{AuthInfo: credentials.TLSInfo{}})
		require.Nil(t, brontide.PeerOperator(ctx))
	})

	t.Run("nil when peer has no AuthInfo", func(t *testing.T) {
		ctx := peer.NewContext(t.Context(), &peer.Peer{})
		require.Nil(t, brontide.PeerOperator(ctx))
	})

	t.Run("returns peer when AuthInfo is brontide", func(t *testing.T) {
		ctx := peer.NewContext(t.Context(), &peer.Peer{AuthInfo: brontide.AuthInfo{Peer: expected}})
		operator := brontide.PeerOperator(ctx)
		require.NotNil(t, operator)

		assert.Equal(t, expected.Identifier, operator.Identifier)
		assert.Equal(t, expected.IdentityPublicKey, operator.IdentityPublicKey)
	})
}

func TestPeerLookupFunc(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{})
	pub := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	other := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	calls := 0
	lookup := brontide.PeerLookupFunc(func(p keys.Public) *brontide.Peer {
		calls++
		if p == pub {
			return &brontide.Peer{Identifier: "match", IdentityPublicKey: p}
		}
		return nil
	})

	peer := lookup.LookupPeer(pub)
	require.NotNil(t, peer)
	assert.Equal(t, "match", peer.Identifier)

	assert.Nil(t, lookup.LookupPeer(other))
	assert.Equal(t, 2, calls)
}
