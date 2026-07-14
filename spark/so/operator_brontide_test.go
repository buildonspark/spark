package so

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

// testConnFactory hands out real (but non-connecting) client conns for both the brontide and plain-TLS paths.
type testConnFactory struct {
	t *testing.T
}

func (f *testConnFactory) NewGRPCConnection(_ string, _ *common.RetryPolicyConfig, _ *common.ClientTimeoutConfig) (*grpc.ClientConn, error) {
	return createTestConnection(f.t), nil
}

func (s *SigningOperator) hasPool(key connPoolKey) bool {
	s.connPoolsMu.Lock()
	defer s.connPoolsMu.Unlock()
	_, ok := s.connPools[key]
	return ok
}

// TestBrontidePoolEvictedWhenKnobFlipsOff verifies the kill-switch actually tears down the brontide pool: once the
// knob is off, the internal-connection path must drop the previously-created brontide pool rather than leaving its
// connections alive for the process lifetime.
func TestBrontidePoolEvictedWhenKnobFlipsOff(t *testing.T) {
	factory := &testConnFactory{t: t}
	op := &SigningOperator{
		AddressRpc:                "rpc-addr",
		InternalAddress:           "internal-addr",
		InternalAddressDkg:        "internal-dkg-addr",
		brontideAvailable:         true,
		internalConnFactory:       factory,
		OperatorConnectionFactory: factory,
		connPoolConfig:            DefaultOperatorConnPoolConfig(),
	}
	brontideKey := connPoolKey{transport: transportBrontide, address: op.InternalAddress}
	brontideDkgKey := connPoolKey{transport: transportBrontide, address: op.InternalAddressDkg}

	onCtx := knobs.InjectKnobsService(t.Context(), knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobInternalRPCBrontideEnabled: 1,
	}))
	conn, err := op.NewOperatorInternalGRPCConnection(onCtx)
	require.NoError(t, err)
	require.NoError(t, conn.Close())
	require.True(t, op.hasPool(brontideKey), "brontide pool should exist while the knob is on")
	dkgConn, err := op.NewOperatorGRPCConnectionForDKG(onCtx)
	require.NoError(t, err)
	require.NoError(t, dkgConn.Close())
	require.True(t, op.hasPool(brontideDkgKey), "DKG brontide pool should use the dedicated DKG address")

	offCtx := knobs.InjectKnobsService(t.Context(), knobs.NewFixedKnobs(map[string]float64{}))
	conn, err = op.NewOperatorInternalGRPCConnection(offCtx)
	require.NoError(t, err)
	require.NoError(t, conn.Close())
	require.False(t, op.hasPool(brontideKey), "brontide pool should be evicted once the knob is off")
	require.False(t, op.hasPool(brontideDkgKey), "DKG brontide pool should be evicted once the knob is off")
}

// TestBrontidePoolNotEvictedWhenUnavailable ensures the eviction path is inert for plain-TLS-only operators: with
// brontide never provisioned, an internal call must not touch (or create) a brontide pool.
func TestBrontidePoolNotEvictedWhenUnavailable(t *testing.T) {
	factory := &testConnFactory{t: t}
	op := &SigningOperator{
		AddressRpc:                "rpc-addr",
		InternalAddress:           "internal-addr",
		InternalAddressDkg:        "internal-dkg-addr",
		OperatorConnectionFactory: factory,
		connPoolConfig:            DefaultOperatorConnPoolConfig(),
	}

	offCtx := knobs.InjectKnobsService(t.Context(), knobs.NewFixedKnobs(map[string]float64{}))
	conn, err := op.NewOperatorInternalGRPCConnection(offCtx)
	require.NoError(t, err)
	require.NoError(t, conn.Close())

	assert.False(t, op.hasPool(connPoolKey{transport: transportBrontide, address: op.InternalAddress}))
	assert.True(t, op.hasPool(connPoolKey{transport: transportTLS, address: op.AddressRpc}),
		"internal call should have fallen through to the plain-TLS AddressRpc pool")
}

func newTestOperator(t *testing.T, rng io.Reader) *SigningOperator {
	t.Helper()
	peer := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	return &SigningOperator{
		ID:                 1,
		Identifier:         "operator-1",
		AddressRpc:         "operator.example:9000",
		AddressDkg:         "operator.example:9001",
		InternalAddress:    "operator.example:9999",
		InternalAddressDkg: "operator.example:9998",
		IdentityPublicKey:  peer,
		CertPath:           writeTestCertPEM(t, rng),
	}
}

// writeTestCertPEM writes a self-signed certificate to a temp file and returns its path.
func writeTestCertPEM(t *testing.T, rng io.Reader) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rng)
	require.NoError(t, err)
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "operator.example"},
		DNSNames:     []string{"operator.example"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rng, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "cert.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	require.NoError(t, os.WriteFile(path, pemBytes, 0o600))
	return path
}

func TestEnableBrontideClient(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{})
	local := keys.MustGeneratePrivateKeyFromRand(rng)

	t.Run("rejects when internal address is missing", func(t *testing.T) {
		op := newTestOperator(t, rng)
		op.InternalAddress = ""

		require.ErrorContains(t, op.EnableBrontideClient(local), "internal_address required")
		assert.False(t, op.brontideAvailable)
	})

	t.Run("rejects when identity public key is zero", func(t *testing.T) {
		op := newTestOperator(t, rng)
		op.IdentityPublicKey = keys.Public{}

		require.ErrorContains(t, op.EnableBrontideClient(local), "identity_public_key required")
		assert.False(t, op.brontideAvailable)
	})

	t.Run("rejects when internal DKG address is missing", func(t *testing.T) {
		op := newTestOperator(t, rng)
		op.InternalAddressDkg = ""

		require.ErrorContains(t, op.EnableBrontideClient(local), "internal_address_dkg required")
		assert.False(t, op.brontideAvailable)
	})

	t.Run("rejects when internal_address host differs from public address host", func(t *testing.T) {
		op := newTestOperator(t, rng)
		op.InternalAddress = "other.example:9999"

		require.ErrorContains(t, op.EnableBrontideClient(local), "must match address host")
		assert.False(t, op.brontideAvailable)
	})

	t.Run("rejects when internal_address_dkg host differs from DKG address host", func(t *testing.T) {
		op := newTestOperator(t, rng)
		op.InternalAddressDkg = "other.example:9998"

		require.ErrorContains(t, op.EnableBrontideClient(local), "must match address_dkg host")
		assert.False(t, op.brontideAvailable)
	})

	t.Run("rejects when cert does not load", func(t *testing.T) {
		op := newTestOperator(t, rng)
		op.CertPath = filepath.Join(t.TempDir(), "missing.pem")

		require.ErrorContains(t, op.EnableBrontideClient(local), "load TLS cert")
		assert.False(t, op.brontideAvailable)
	})

	t.Run("installs the brontide factory on the internal slot only", func(t *testing.T) {
		op := newTestOperator(t, rng)
		op.OperatorConnectionFactory = NewOperatorConnectionFactorySecure(op)
		publicBefore := op.OperatorConnectionFactory

		require.NoError(t, op.EnableBrontideClient(local))
		assert.True(t, op.brontideAvailable)
		assert.IsType(t, (*operatorConnectionFactoryBrontide)(nil), op.internalConnFactory)
		// Public factory is left alone so cross-operator SparkService calls keep going over plain TLS against AddressRpc.
		assert.Same(t, publicBefore, op.OperatorConnectionFactory)
	})
}
