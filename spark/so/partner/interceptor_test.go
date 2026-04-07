package partner

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// These tests exercise the partner JWT interceptor's cryptographic and security-sensitive
// behavior: signature verification, algorithm enforcement, audience validation, and claim
// extraction. These contracts are invisible at the gRPC application boundary — a correct
// integration test would pass whether or not the interceptor rejects a wrong-curve key,
// but a missing check causes security vulnerabilities in production.

// makeP256Key generates a fresh P-256 key pair for testing.
func makeP256Key(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	return key
}

// makeSecp256k1Key generates a fresh secp256k1 key pair for testing.
func makeSecp256k1Key(t *testing.T) keys.Private {
	t.Helper()
	return keys.GeneratePrivateKey()
}

// makeES256JWT signs an ES256 JWT with a P-256 key using standard claims (iss, sub, aud).
func makeES256JWT(t *testing.T, key *ecdsa.PrivateKey, partnerID, label string, exp int64) string {
	t.Helper()

	header, err := json.Marshal(map[string]string{"alg": "ES256", "typ": "JWT"})
	require.NoError(t, err)
	claims, err := json.Marshal(map[string]any{
		"iss": partnerID,
		"sub": label,
		"aud": expectedAudience,
		"iat": time.Now().Unix(),
		"exp": exp,
	})
	require.NoError(t, err)

	signingInput := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signingInput))

	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	require.NoError(t, err)

	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])

	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// makeES256KJWT signs an ES256K JWT with a secp256k1 key using standard claims (iss, sub, aud).
func makeES256KJWT(t *testing.T, key keys.Private, partnerID, label string, exp int64) string {
	t.Helper()

	header, err := json.Marshal(map[string]string{"alg": "ES256K", "typ": "JWT"})
	require.NoError(t, err)
	claims, err := json.Marshal(map[string]any{
		"iss": partnerID,
		"sub": label,
		"aud": expectedAudience,
		"iat": time.Now().Unix(),
		"exp": exp,
	})
	require.NoError(t, err)

	signingInput := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signingInput))

	ecKey := key.ToBTCEC().ToECDSA()
	r, s, err := ecdsa.Sign(rand.Reader, ecKey, digest[:])
	require.NoError(t, err)

	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])

	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}

type testPartnerEntry struct {
	pubKey *ecdsa.PublicKey
	dbID   uuid.UUID
}

// mapKeyLookup returns a key lookup function backed by an in-memory map, for testing.
// Key format: "partnerID/label".
func mapKeyLookup(entries map[string]*testPartnerEntry) func(ctx context.Context, partnerID, label string) (*keyLookupResult, error) {
	return func(ctx context.Context, partnerID, label string) (*keyLookupResult, error) {
		key := partnerID + "/" + label
		entry, ok := entries[key]
		if !ok {
			return nil, fmt.Errorf("unknown partner_id: %s, label: %s", partnerID, label)
		}
		return &keyLookupResult{
			pubKey:      entry.pubKey,
			partnerDBID: entry.dbID,
		}, nil
	}
}

func noopHandler(ctx context.Context, req any) (any, error) {
	return ctx, nil
}

const testLabel = "client-1"

// --- ES256 (P-256) interceptor tests ---

func TestPartnerJWTInterceptor_NoHeader(t *testing.T) {
	i := newInterceptorWithKeyLookup(mapKeyLookup(map[string]*testPartnerEntry{}))

	info := &grpc.UnaryServerInfo{FullMethod: "/spark.SparkService/Transfer"}
	resp, err := i.PartnerJWTInterceptor(t.Context(), nil, info, noopHandler)

	require.NoError(t, err)
	_, ok := GetPartnerInfoFromContext(resp.(context.Context))
	assert.False(t, ok)
}

func TestPartnerJWTInterceptor_ValidES256JWT(t *testing.T) {
	key := makeP256Key(t)
	partnerID := "partner-a"
	dbID := uuid.New()
	i := newInterceptorWithKeyLookup(mapKeyLookup(map[string]*testPartnerEntry{
		partnerID + "/" + testLabel: {pubKey: &key.PublicKey, dbID: dbID},
	}))

	token := makeES256JWT(t, key, partnerID, testLabel, time.Now().Add(time.Hour).Unix())
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs(partnerJWTHeader, token))

	info := &grpc.UnaryServerInfo{}
	resp, err := i.PartnerJWTInterceptor(ctx, nil, info, noopHandler)

	require.NoError(t, err)
	got, ok := GetPartnerInfoFromContext(resp.(context.Context))
	assert.True(t, ok)
	assert.Equal(t, partnerID, got.PartnerID)
	assert.Equal(t, testLabel, got.Label)
	assert.Equal(t, dbID, got.PartnerDBID)
}

func TestPartnerJWTInterceptor_ExpiredES256JWT(t *testing.T) {
	key := makeP256Key(t)
	partnerID := "partner-a"
	i := newInterceptorWithKeyLookup(mapKeyLookup(map[string]*testPartnerEntry{
		partnerID + "/" + testLabel: {pubKey: &key.PublicKey, dbID: uuid.New()},
	}))

	token := makeES256JWT(t, key, partnerID, testLabel, time.Now().Add(-time.Hour).Unix())
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs(partnerJWTHeader, token))

	info := &grpc.UnaryServerInfo{}
	resp, err := i.PartnerJWTInterceptor(ctx, nil, info, noopHandler)

	require.NoError(t, err)
	_, ok := GetPartnerInfoFromContext(resp.(context.Context))
	assert.False(t, ok)
}

func TestPartnerJWTInterceptor_WrongKey(t *testing.T) {
	key := makeP256Key(t)
	otherKey := makeP256Key(t)
	partnerID := "partner-a"
	i := newInterceptorWithKeyLookup(mapKeyLookup(map[string]*testPartnerEntry{
		partnerID + "/" + testLabel: {pubKey: &otherKey.PublicKey, dbID: uuid.New()},
	}))

	token := makeES256JWT(t, key, partnerID, testLabel, time.Now().Add(time.Hour).Unix())
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs(partnerJWTHeader, token))

	info := &grpc.UnaryServerInfo{}
	resp, err := i.PartnerJWTInterceptor(ctx, nil, info, noopHandler)

	require.NoError(t, err)
	_, ok := GetPartnerInfoFromContext(resp.(context.Context))
	assert.False(t, ok)
}

func TestPartnerJWTInterceptor_UnknownPartner(t *testing.T) {
	key := makeP256Key(t)
	i := newInterceptorWithKeyLookup(mapKeyLookup(map[string]*testPartnerEntry{}))

	token := makeES256JWT(t, key, "unknown-partner", testLabel, time.Now().Add(time.Hour).Unix())
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs(partnerJWTHeader, token))

	info := &grpc.UnaryServerInfo{}
	resp, err := i.PartnerJWTInterceptor(ctx, nil, info, noopHandler)

	require.NoError(t, err)
	_, ok := GetPartnerInfoFromContext(resp.(context.Context))
	assert.False(t, ok)
}

func TestPartnerJWTInterceptor_MalformedToken(t *testing.T) {
	i := newInterceptorWithKeyLookup(mapKeyLookup(map[string]*testPartnerEntry{}))

	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs(partnerJWTHeader, "not.a.valid.jwt.token"))

	info := &grpc.UnaryServerInfo{}
	resp, err := i.PartnerJWTInterceptor(ctx, nil, info, noopHandler)

	require.NoError(t, err)
	_, ok := GetPartnerInfoFromContext(resp.(context.Context))
	assert.False(t, ok)
}

func TestPartnerJWTInterceptor_WrongAlgorithm(t *testing.T) {
	key := makeP256Key(t)
	partnerID := "partner-a"
	i := newInterceptorWithKeyLookup(mapKeyLookup(map[string]*testPartnerEntry{
		partnerID + "/" + testLabel: {pubKey: &key.PublicKey, dbID: uuid.New()},
	}))

	header, err := json.Marshal(map[string]string{"alg": "RS256"})
	require.NoError(t, err)
	claims, err := json.Marshal(map[string]any{
		"iss": partnerID,
		"sub": testLabel,
		"aud": expectedAudience,
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	require.NoError(t, err)
	token := fmt.Sprintf("%s.%s.fakesig",
		base64.RawURLEncoding.EncodeToString(header),
		base64.RawURLEncoding.EncodeToString(claims),
	)

	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs(partnerJWTHeader, token))
	info := &grpc.UnaryServerInfo{}
	resp, err := i.PartnerJWTInterceptor(ctx, nil, info, noopHandler)

	require.NoError(t, err)
	_, ok := GetPartnerInfoFromContext(resp.(context.Context))
	assert.False(t, ok)
}

func TestPartnerJWTInterceptor_CorrectlyIdentifiesEachPartner(t *testing.T) {
	partnerAKey := makeP256Key(t)
	partnerBKey := makeP256Key(t)
	i := newInterceptorWithKeyLookup(mapKeyLookup(map[string]*testPartnerEntry{
		"partner-a/" + testLabel: {pubKey: &partnerAKey.PublicKey, dbID: uuid.New()},
		"partner-b/" + testLabel: {pubKey: &partnerBKey.PublicKey, dbID: uuid.New()},
	}))

	info := &grpc.UnaryServerInfo{}

	for partnerID, key := range map[string]*ecdsa.PrivateKey{"partner-a": partnerAKey, "partner-b": partnerBKey} {
		token := makeES256JWT(t, key, partnerID, testLabel, time.Now().Add(time.Hour).Unix())
		ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs(partnerJWTHeader, token))

		resp, err := i.PartnerJWTInterceptor(ctx, nil, info, noopHandler)
		require.NoError(t, err)

		got, ok := GetPartnerInfoFromContext(resp.(context.Context))
		assert.True(t, ok)
		assert.Equal(t, partnerID, got.PartnerID)
		assert.Equal(t, testLabel, got.Label)
	}
}

// --- ES256K (secp256k1) interceptor tests ---

func TestPartnerJWTInterceptor_ValidES256KJWT(t *testing.T) {
	key := makeSecp256k1Key(t)
	partnerID := "partner-a"
	i := newInterceptorWithKeyLookup(mapKeyLookup(map[string]*testPartnerEntry{
		partnerID + "/" + testLabel: {pubKey: key.Public().ToBTCEC().ToECDSA(), dbID: uuid.New()},
	}))

	token := makeES256KJWT(t, key, partnerID, testLabel, time.Now().Add(time.Hour).Unix())
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs(partnerJWTHeader, token))

	info := &grpc.UnaryServerInfo{}
	resp, err := i.PartnerJWTInterceptor(ctx, nil, info, noopHandler)

	require.NoError(t, err)
	got, ok := GetPartnerInfoFromContext(resp.(context.Context))
	assert.True(t, ok)
	assert.Equal(t, partnerID, got.PartnerID)
	assert.Equal(t, testLabel, got.Label)
}

func TestPartnerJWTInterceptor_ExpiredES256KJWT(t *testing.T) {
	key := makeSecp256k1Key(t)
	partnerID := "partner-a"
	i := newInterceptorWithKeyLookup(mapKeyLookup(map[string]*testPartnerEntry{
		partnerID + "/" + testLabel: {pubKey: key.Public().ToBTCEC().ToECDSA(), dbID: uuid.New()},
	}))

	token := makeES256KJWT(t, key, partnerID, testLabel, time.Now().Add(-time.Hour).Unix())
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs(partnerJWTHeader, token))

	info := &grpc.UnaryServerInfo{}
	resp, err := i.PartnerJWTInterceptor(ctx, nil, info, noopHandler)

	require.NoError(t, err)
	_, ok := GetPartnerInfoFromContext(resp.(context.Context))
	assert.False(t, ok)
}

func TestPartnerJWTInterceptor_WrongSecp256k1Key(t *testing.T) {
	key := makeSecp256k1Key(t)
	otherKey := makeSecp256k1Key(t)
	partnerID := "partner-a"
	i := newInterceptorWithKeyLookup(mapKeyLookup(map[string]*testPartnerEntry{
		partnerID + "/" + testLabel: {pubKey: otherKey.Public().ToBTCEC().ToECDSA(), dbID: uuid.New()},
	}))

	token := makeES256KJWT(t, key, partnerID, testLabel, time.Now().Add(time.Hour).Unix())
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs(partnerJWTHeader, token))

	info := &grpc.UnaryServerInfo{}
	resp, err := i.PartnerJWTInterceptor(ctx, nil, info, noopHandler)

	require.NoError(t, err)
	_, ok := GetPartnerInfoFromContext(resp.(context.Context))
	assert.False(t, ok)
}

func TestPartnerJWTInterceptor_ES256KJWTRejectedForES256Key(t *testing.T) {
	secp256k1Key := makeSecp256k1Key(t)
	p256Key := makeP256Key(t)
	partnerID := "partner-a"
	i := newInterceptorWithKeyLookup(mapKeyLookup(map[string]*testPartnerEntry{
		partnerID + "/" + testLabel: {pubKey: &p256Key.PublicKey, dbID: uuid.New()},
	}))

	token := makeES256KJWT(t, secp256k1Key, partnerID, testLabel, time.Now().Add(time.Hour).Unix())
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs(partnerJWTHeader, token))

	info := &grpc.UnaryServerInfo{}
	resp, err := i.PartnerJWTInterceptor(ctx, nil, info, noopHandler)

	require.NoError(t, err)
	_, ok := GetPartnerInfoFromContext(resp.(context.Context))
	assert.False(t, ok)
}

func TestPartnerJWTInterceptor_ES256JWTRejectedForSecp256k1Key(t *testing.T) {
	p256Key := makeP256Key(t)
	secp256k1Key := makeSecp256k1Key(t)
	partnerID := "partner-a"
	i := newInterceptorWithKeyLookup(mapKeyLookup(map[string]*testPartnerEntry{
		partnerID + "/" + testLabel: {pubKey: secp256k1Key.Public().ToBTCEC().ToECDSA(), dbID: uuid.New()},
	}))

	token := makeES256JWT(t, p256Key, partnerID, testLabel, time.Now().Add(time.Hour).Unix())
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs(partnerJWTHeader, token))

	info := &grpc.UnaryServerInfo{}
	resp, err := i.PartnerJWTInterceptor(ctx, nil, info, noopHandler)

	require.NoError(t, err)
	_, ok := GetPartnerInfoFromContext(resp.(context.Context))
	assert.False(t, ok)
}

func TestPartnerJWTInterceptor_CorrectlyIdentifiesEachPartnerMixedKeyTypes(t *testing.T) {
	p256Key := makeP256Key(t)
	secp256k1Key := makeSecp256k1Key(t)
	i := newInterceptorWithKeyLookup(mapKeyLookup(map[string]*testPartnerEntry{
		"partner-a/" + testLabel: {pubKey: &p256Key.PublicKey, dbID: uuid.New()},
		"partner-b/" + testLabel: {pubKey: secp256k1Key.Public().ToBTCEC().ToECDSA(), dbID: uuid.New()},
	}))

	info := &grpc.UnaryServerInfo{}

	tokenA := makeES256JWT(t, p256Key, "partner-a", testLabel, time.Now().Add(time.Hour).Unix())
	ctxA := metadata.NewIncomingContext(t.Context(), metadata.Pairs(partnerJWTHeader, tokenA))
	respA, err := i.PartnerJWTInterceptor(ctxA, nil, info, noopHandler)
	require.NoError(t, err)
	gotA, ok := GetPartnerInfoFromContext(respA.(context.Context))
	assert.True(t, ok)
	assert.Equal(t, "partner-a", gotA.PartnerID)
	assert.Equal(t, testLabel, gotA.Label)

	tokenB := makeES256KJWT(t, secp256k1Key, "partner-b", testLabel, time.Now().Add(time.Hour).Unix())
	ctxB := metadata.NewIncomingContext(t.Context(), metadata.Pairs(partnerJWTHeader, tokenB))
	respB, err := i.PartnerJWTInterceptor(ctxB, nil, info, noopHandler)
	require.NoError(t, err)
	gotB, ok := GetPartnerInfoFromContext(respB.(context.Context))
	assert.True(t, ok)
	assert.Equal(t, "partner-b", gotB.PartnerID)
	assert.Equal(t, testLabel, gotB.Label)
}

func TestPartnerJWTInterceptor_WrongAudience(t *testing.T) {
	key := makeP256Key(t)
	partnerID := "partner-a"
	i := newInterceptorWithKeyLookup(mapKeyLookup(map[string]*testPartnerEntry{
		partnerID + "/" + testLabel: {pubKey: &key.PublicKey, dbID: uuid.New()},
	}))

	header, err := json.Marshal(map[string]string{"alg": "ES256", "typ": "JWT"})
	require.NoError(t, err)
	claims, err := json.Marshal(map[string]any{
		"iss": partnerID,
		"sub": testLabel,
		"aud": "wrong-audience",
		"iat": time.Now().Unix(),
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	require.NoError(t, err)

	signingInput := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	require.NoError(t, err)
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	token := signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)

	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs(partnerJWTHeader, token))
	info := &grpc.UnaryServerInfo{}
	resp, err := i.PartnerJWTInterceptor(ctx, nil, info, noopHandler)

	require.NoError(t, err)
	_, ok := GetPartnerInfoFromContext(resp.(context.Context))
	assert.False(t, ok)
}

func TestPartnerJWTInterceptor_WrongLabel(t *testing.T) {
	key := makeP256Key(t)
	partnerID := "partner-a"
	// Register with testLabel, but JWT will use a different label.
	i := newInterceptorWithKeyLookup(mapKeyLookup(map[string]*testPartnerEntry{
		partnerID + "/" + testLabel: {pubKey: &key.PublicKey, dbID: uuid.New()},
	}))

	token := makeES256JWT(t, key, partnerID, "wrong-label", time.Now().Add(time.Hour).Unix())
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs(partnerJWTHeader, token))

	info := &grpc.UnaryServerInfo{}
	resp, err := i.PartnerJWTInterceptor(ctx, nil, info, noopHandler)

	require.NoError(t, err)
	_, ok := GetPartnerInfoFromContext(resp.(context.Context))
	assert.False(t, ok)
}

func TestPartnerJWTInterceptor_MissingSubClaim(t *testing.T) {
	key := makeP256Key(t)
	partnerID := "partner-a"
	i := newInterceptorWithKeyLookup(mapKeyLookup(map[string]*testPartnerEntry{
		partnerID + "/" + testLabel: {pubKey: &key.PublicKey, dbID: uuid.New()},
	}))

	header, err := json.Marshal(map[string]string{"alg": "ES256", "typ": "JWT"})
	require.NoError(t, err)
	claims, err := json.Marshal(map[string]any{
		"iss": partnerID,
		"aud": expectedAudience,
		"iat": time.Now().Unix(),
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	require.NoError(t, err)

	signingInput := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	require.NoError(t, err)
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	token := signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)

	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs(partnerJWTHeader, token))
	info := &grpc.UnaryServerInfo{}
	resp, err := i.PartnerJWTInterceptor(ctx, nil, info, noopHandler)

	require.NoError(t, err)
	_, ok := GetPartnerInfoFromContext(resp.(context.Context))
	assert.False(t, ok)
}
