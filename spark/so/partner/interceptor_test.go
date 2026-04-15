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
	jwtkeys "github.com/lightsparkdev/spark/common/keys/jwt"
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

// makeP256Key generates a fresh P-256 key pair, returning the private key (for signing JWTs)
// and the corresponding jwtkeys.Public (for test lookup entries).
func makeP256Key(t *testing.T) (*ecdsa.PrivateKey, jwtkeys.Public) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	p256, err := keys.P256PublicFromECDSA(&priv.PublicKey)
	require.NoError(t, err)
	return priv, jwtkeys.PublicFromP256(p256)
}

// makeSecp256k1Key generates a fresh secp256k1 key pair, returning the private key (for signing JWTs)
// and the corresponding jwtkeys.Public (for test lookup entries).
func makeSecp256k1Key(t *testing.T) (keys.Private, jwtkeys.Public) {
	t.Helper()
	priv := keys.GeneratePrivateKey()
	return priv, jwtkeys.PublicFromSecp256k1(priv.Public())
}

// makeES256JWTWithClaims signs an ES256 JWT with arbitrary claims.
func makeES256JWTWithClaims(t *testing.T, key *ecdsa.PrivateKey, claims map[string]any) string {
	t.Helper()
	header, err := json.Marshal(map[string]string{"alg": "ES256", "typ": "JWT"})
	require.NoError(t, err)
	claimsJSON, err := json.Marshal(claims)
	require.NoError(t, err)

	signingInput := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claimsJSON)
	digest := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	require.NoError(t, err)
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
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
	pubKey jwtkeys.Public
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

func noopHandler(ctx context.Context, _ any) (any, error) {
	return ctx, nil
}

// respCtx extracts the context.Context from the interceptor response, failing the test if
// the type assertion fails.
func respCtx(t *testing.T, resp any) context.Context {
	t.Helper()
	ctx, ok := resp.(context.Context)
	require.True(t, ok, "expected interceptor response to be context.Context")
	return ctx
}

const testLabel = "client-1"

// --- ES256 (P-256) interceptor tests ---

func TestPartnerJWTInterceptor_NoHeader(t *testing.T) {
	i := newInterceptorWithKeyLookup(mapKeyLookup(map[string]*testPartnerEntry{}))

	info := &grpc.UnaryServerInfo{FullMethod: "/spark.SparkService/Transfer"}
	resp, err := i.PartnerJWTInterceptor(t.Context(), nil, info, noopHandler)

	require.NoError(t, err)
	_, ok := GetPartnerInfoFromContext(respCtx(t, resp))
	assert.False(t, ok)
}

func TestPartnerJWTInterceptor_ValidES256JWT(t *testing.T) {
	priv, pub := makeP256Key(t)
	partnerID := "partner-a"
	dbID := uuid.New()
	i := newInterceptorWithKeyLookup(mapKeyLookup(map[string]*testPartnerEntry{
		partnerID + "/" + testLabel: {pubKey: pub, dbID: dbID},
	}))

	token := makeES256JWT(t, priv, partnerID, testLabel, time.Now().Add(time.Hour).Unix())
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs(partnerJWTHeader, token))

	info := &grpc.UnaryServerInfo{}
	resp, err := i.PartnerJWTInterceptor(ctx, nil, info, noopHandler)
	require.NoError(t, err)

	got, ok := GetPartnerInfoFromContext(respCtx(t, resp))
	assert.True(t, ok)
	assert.Equal(t, partnerID, got.PartnerID)
	assert.Equal(t, testLabel, got.Label)
	assert.Equal(t, dbID, got.PartnerDBID)
}

func TestPartnerJWTInterceptor_ExpiredES256JWT(t *testing.T) {
	priv, pub := makeP256Key(t)
	partnerID := "partner-a"
	i := newInterceptorWithKeyLookup(mapKeyLookup(map[string]*testPartnerEntry{
		partnerID + "/" + testLabel: {pubKey: pub, dbID: uuid.New()},
	}))

	token := makeES256JWT(t, priv, partnerID, testLabel, time.Now().Add(-time.Hour).Unix())
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs(partnerJWTHeader, token))

	info := &grpc.UnaryServerInfo{}
	resp, err := i.PartnerJWTInterceptor(ctx, nil, info, noopHandler)
	require.NoError(t, err)

	_, ok := GetPartnerInfoFromContext(respCtx(t, resp))
	assert.False(t, ok)
}

func TestPartnerJWTInterceptor_WrongKey(t *testing.T) {
	priv, _ := makeP256Key(t)
	_, otherPub := makeP256Key(t)
	partnerID := "partner-a"
	i := newInterceptorWithKeyLookup(mapKeyLookup(map[string]*testPartnerEntry{
		partnerID + "/" + testLabel: {pubKey: otherPub, dbID: uuid.New()},
	}))

	token := makeES256JWT(t, priv, partnerID, testLabel, time.Now().Add(time.Hour).Unix())
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs(partnerJWTHeader, token))

	info := &grpc.UnaryServerInfo{}
	resp, err := i.PartnerJWTInterceptor(ctx, nil, info, noopHandler)
	require.NoError(t, err)

	_, ok := GetPartnerInfoFromContext(respCtx(t, resp))
	assert.False(t, ok)
}

func TestPartnerJWTInterceptor_UnknownPartner(t *testing.T) {
	priv, _ := makeP256Key(t)
	i := newInterceptorWithKeyLookup(mapKeyLookup(map[string]*testPartnerEntry{}))

	token := makeES256JWT(t, priv, "unknown-partner", testLabel, time.Now().Add(time.Hour).Unix())
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs(partnerJWTHeader, token))

	info := &grpc.UnaryServerInfo{}
	resp, err := i.PartnerJWTInterceptor(ctx, nil, info, noopHandler)
	require.NoError(t, err)

	_, ok := GetPartnerInfoFromContext(respCtx(t, resp))
	assert.False(t, ok)
}

func TestPartnerJWTInterceptor_MalformedToken(t *testing.T) {
	i := newInterceptorWithKeyLookup(mapKeyLookup(map[string]*testPartnerEntry{}))

	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs(partnerJWTHeader, "not.a.valid.jwt.token"))

	info := &grpc.UnaryServerInfo{}
	resp, err := i.PartnerJWTInterceptor(ctx, nil, info, noopHandler)
	require.NoError(t, err)

	_, ok := GetPartnerInfoFromContext(respCtx(t, resp))
	assert.False(t, ok)
}

func TestPartnerJWTInterceptor_WrongAlgorithm(t *testing.T) {
	_, pub := makeP256Key(t)
	partnerID := "partner-a"
	i := newInterceptorWithKeyLookup(mapKeyLookup(map[string]*testPartnerEntry{
		partnerID + "/" + testLabel: {pubKey: pub, dbID: uuid.New()},
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

	_, ok := GetPartnerInfoFromContext(respCtx(t, resp))
	assert.False(t, ok)
}

func TestPartnerJWTInterceptor_CorrectlyIdentifiesEachPartner(t *testing.T) {
	privA, pubA := makeP256Key(t)
	privB, pubB := makeP256Key(t)
	i := newInterceptorWithKeyLookup(mapKeyLookup(map[string]*testPartnerEntry{
		"partner-a/" + testLabel: {pubKey: pubA, dbID: uuid.New()},
		"partner-b/" + testLabel: {pubKey: pubB, dbID: uuid.New()},
	}))

	info := &grpc.UnaryServerInfo{}

	for partnerID, priv := range map[string]*ecdsa.PrivateKey{"partner-a": privA, "partner-b": privB} {
		token := makeES256JWT(t, priv, partnerID, testLabel, time.Now().Add(time.Hour).Unix())
		ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs(partnerJWTHeader, token))

		resp, err := i.PartnerJWTInterceptor(ctx, nil, info, noopHandler)
		require.NoError(t, err)

		got, ok := GetPartnerInfoFromContext(respCtx(t, resp))
		assert.True(t, ok)
		assert.Equal(t, partnerID, got.PartnerID)
		assert.Equal(t, testLabel, got.Label)
	}
}

// --- ES256K (secp256k1) interceptor tests ---

func TestPartnerJWTInterceptor_ValidES256KJWT(t *testing.T) {
	priv, pub := makeSecp256k1Key(t)
	partnerID := "partner-a"
	i := newInterceptorWithKeyLookup(mapKeyLookup(map[string]*testPartnerEntry{
		partnerID + "/" + testLabel: {pubKey: pub, dbID: uuid.New()},
	}))

	token := makeES256KJWT(t, priv, partnerID, testLabel, time.Now().Add(time.Hour).Unix())
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs(partnerJWTHeader, token))

	info := &grpc.UnaryServerInfo{}
	resp, err := i.PartnerJWTInterceptor(ctx, nil, info, noopHandler)

	require.NoError(t, err)
	got, ok := GetPartnerInfoFromContext(respCtx(t, resp))
	assert.True(t, ok)
	assert.Equal(t, partnerID, got.PartnerID)
	assert.Equal(t, testLabel, got.Label)
}

func TestPartnerJWTInterceptor_ExpiredES256KJWT(t *testing.T) {
	priv, pub := makeSecp256k1Key(t)
	partnerID := "partner-a"
	i := newInterceptorWithKeyLookup(mapKeyLookup(map[string]*testPartnerEntry{
		partnerID + "/" + testLabel: {pubKey: pub, dbID: uuid.New()},
	}))

	token := makeES256KJWT(t, priv, partnerID, testLabel, time.Now().Add(-time.Hour).Unix())
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs(partnerJWTHeader, token))

	info := &grpc.UnaryServerInfo{}
	resp, err := i.PartnerJWTInterceptor(ctx, nil, info, noopHandler)
	require.NoError(t, err)

	_, ok := GetPartnerInfoFromContext(respCtx(t, resp))
	assert.False(t, ok)
}

func TestPartnerJWTInterceptor_WrongSecp256k1Key(t *testing.T) {
	priv, _ := makeSecp256k1Key(t)
	_, otherPub := makeSecp256k1Key(t)
	partnerID := "partner-a"
	i := newInterceptorWithKeyLookup(mapKeyLookup(map[string]*testPartnerEntry{
		partnerID + "/" + testLabel: {pubKey: otherPub, dbID: uuid.New()},
	}))

	token := makeES256KJWT(t, priv, partnerID, testLabel, time.Now().Add(time.Hour).Unix())
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs(partnerJWTHeader, token))

	info := &grpc.UnaryServerInfo{}
	resp, err := i.PartnerJWTInterceptor(ctx, nil, info, noopHandler)
	require.NoError(t, err)

	_, ok := GetPartnerInfoFromContext(respCtx(t, resp))
	assert.False(t, ok)
}

func TestPartnerJWTInterceptor_ES256KJWTRejectedForES256Key(t *testing.T) {
	secpPriv, _ := makeSecp256k1Key(t)
	_, p256Pub := makeP256Key(t)
	partnerID := "partner-a"
	i := newInterceptorWithKeyLookup(mapKeyLookup(map[string]*testPartnerEntry{
		partnerID + "/" + testLabel: {pubKey: p256Pub, dbID: uuid.New()},
	}))

	token := makeES256KJWT(t, secpPriv, partnerID, testLabel, time.Now().Add(time.Hour).Unix())
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs(partnerJWTHeader, token))

	info := &grpc.UnaryServerInfo{}
	resp, err := i.PartnerJWTInterceptor(ctx, nil, info, noopHandler)
	require.NoError(t, err)

	_, ok := GetPartnerInfoFromContext(respCtx(t, resp))
	assert.False(t, ok)
}

func TestPartnerJWTInterceptor_ES256JWTRejectedForSecp256k1Key(t *testing.T) {
	p256Priv, _ := makeP256Key(t)
	_, secpPub := makeSecp256k1Key(t)
	partnerID := "partner-a"
	i := newInterceptorWithKeyLookup(mapKeyLookup(map[string]*testPartnerEntry{
		partnerID + "/" + testLabel: {pubKey: secpPub, dbID: uuid.New()},
	}))

	token := makeES256JWT(t, p256Priv, partnerID, testLabel, time.Now().Add(time.Hour).Unix())
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs(partnerJWTHeader, token))

	info := &grpc.UnaryServerInfo{}
	resp, err := i.PartnerJWTInterceptor(ctx, nil, info, noopHandler)
	require.NoError(t, err)

	_, ok := GetPartnerInfoFromContext(respCtx(t, resp))
	assert.False(t, ok)
}

func TestPartnerJWTInterceptor_CorrectlyIdentifiesEachPartnerMixedKeyTypes(t *testing.T) {
	p256Priv, p256Pub := makeP256Key(t)
	secpPriv, secpPub := makeSecp256k1Key(t)
	i := newInterceptorWithKeyLookup(mapKeyLookup(map[string]*testPartnerEntry{
		"partner-a/" + testLabel: {pubKey: p256Pub, dbID: uuid.New()},
		"partner-b/" + testLabel: {pubKey: secpPub, dbID: uuid.New()},
	}))

	info := &grpc.UnaryServerInfo{}

	tokenA := makeES256JWT(t, p256Priv, "partner-a", testLabel, time.Now().Add(time.Hour).Unix())
	ctxA := metadata.NewIncomingContext(t.Context(), metadata.Pairs(partnerJWTHeader, tokenA))
	respA, err := i.PartnerJWTInterceptor(ctxA, nil, info, noopHandler)
	require.NoError(t, err)
	gotA, ok := GetPartnerInfoFromContext(respCtx(t, respA))
	assert.True(t, ok)
	assert.Equal(t, "partner-a", gotA.PartnerID)
	assert.Equal(t, testLabel, gotA.Label)

	tokenB := makeES256KJWT(t, secpPriv, "partner-b", testLabel, time.Now().Add(time.Hour).Unix())
	ctxB := metadata.NewIncomingContext(t.Context(), metadata.Pairs(partnerJWTHeader, tokenB))
	respB, err := i.PartnerJWTInterceptor(ctxB, nil, info, noopHandler)
	require.NoError(t, err)
	gotB, ok := GetPartnerInfoFromContext(respCtx(t, respB))
	assert.True(t, ok)
	assert.Equal(t, "partner-b", gotB.PartnerID)
	assert.Equal(t, testLabel, gotB.Label)
}

func TestPartnerJWTInterceptor_WrongAudience(t *testing.T) {
	priv, pub := makeP256Key(t)
	partnerID := "partner-a"
	i := newInterceptorWithKeyLookup(mapKeyLookup(map[string]*testPartnerEntry{
		partnerID + "/" + testLabel: {pubKey: pub, dbID: uuid.New()},
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
	r, s, err := ecdsa.Sign(rand.Reader, priv, digest[:])
	require.NoError(t, err)
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	token := signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)

	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs(partnerJWTHeader, token))
	info := &grpc.UnaryServerInfo{}
	resp, err := i.PartnerJWTInterceptor(ctx, nil, info, noopHandler)
	require.NoError(t, err)

	_, ok := GetPartnerInfoFromContext(respCtx(t, resp))
	assert.False(t, ok)
}

func TestPartnerJWTInterceptor_WrongLabel(t *testing.T) {
	priv, pub := makeP256Key(t)
	partnerID := "partner-a"
	// Register with testLabel, but JWT will use a different label.
	i := newInterceptorWithKeyLookup(mapKeyLookup(map[string]*testPartnerEntry{
		partnerID + "/" + testLabel: {pubKey: pub, dbID: uuid.New()},
	}))

	token := makeES256JWT(t, priv, partnerID, "wrong-label", time.Now().Add(time.Hour).Unix())
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs(partnerJWTHeader, token))

	info := &grpc.UnaryServerInfo{}
	resp, err := i.PartnerJWTInterceptor(ctx, nil, info, noopHandler)
	require.NoError(t, err)

	_, ok := GetPartnerInfoFromContext(respCtx(t, resp))
	assert.False(t, ok)
}

// Iss-only JWT (no sub) falls back gracefully when lookupKeysByPartnerID is nil.
func TestPartnerJWTInterceptor_MissingSubClaim_NoFallback(t *testing.T) {
	priv, pub := makeP256Key(t)
	partnerID := "partner-a"
	i := newInterceptorWithKeyLookup(mapKeyLookup(map[string]*testPartnerEntry{
		partnerID + "/" + testLabel: {pubKey: pub, dbID: uuid.New()},
	}))

	token := makeES256JWTWithClaims(t, priv, map[string]any{
		"iss": partnerID,
		"aud": expectedAudience,
		"iat": time.Now().Unix(),
		"exp": time.Now().Add(time.Hour).Unix(),
	})

	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs(partnerJWTHeader, token))
	info := &grpc.UnaryServerInfo{}
	resp, err := i.PartnerJWTInterceptor(ctx, nil, info, noopHandler)
	require.NoError(t, err)

	_, ok := GetPartnerInfoFromContext(respCtx(t, resp))
	assert.False(t, ok)
}

// Iss-only JWT succeeds when lookupKeysByPartnerID is configured and a matching key exists.
// Verifies the key-iteration loop and that PartnerInfo has Label="" (read-only access).
func TestPartnerJWTInterceptor_IssOnlyJWT_SuccessPath(t *testing.T) {
	priv, pub := makeP256Key(t)
	partnerID := "partner-a"

	i := &Interceptor{
		lookupKey: mapKeyLookup(map[string]*testPartnerEntry{}),
		lookupKeysByPartnerID: func(_ context.Context, pid string) ([]*keyLookupResult, error) {
			if pid != partnerID {
				return nil, fmt.Errorf("unknown partner_id: %s", pid)
			}
			return []*keyLookupResult{{pubKey: pub, partnerDBID: uuid.New()}}, nil
		},
	}

	token := makeES256JWTWithClaims(t, priv, map[string]any{
		"iss": partnerID,
		"aud": expectedAudience,
		"iat": time.Now().Unix(),
		"exp": time.Now().Add(time.Hour).Unix(),
	})

	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs(partnerJWTHeader, token))
	info := &grpc.UnaryServerInfo{}
	resp, err := i.PartnerJWTInterceptor(ctx, nil, info, noopHandler)
	require.NoError(t, err)

	got, ok := GetPartnerInfoFromContext(respCtx(t, resp))
	require.True(t, ok, "expected partner info in context for iss-only JWT")
	assert.Equal(t, partnerID, got.PartnerID)
	assert.Empty(t, got.Label, "iss-only JWT should have empty Label")
}

// Iss-only JWT fails when none of the registered keys match the signature.
func TestPartnerJWTInterceptor_IssOnlyJWT_NoMatchingKey(t *testing.T) {
	priv, _ := makeP256Key(t)
	_, otherPub := makeP256Key(t) // different key
	partnerID := "partner-a"

	i := &Interceptor{
		lookupKey: mapKeyLookup(map[string]*testPartnerEntry{}),
		lookupKeysByPartnerID: func(_ context.Context, pid string) ([]*keyLookupResult, error) {
			return []*keyLookupResult{{pubKey: otherPub, partnerDBID: uuid.New()}}, nil
		},
	}

	token := makeES256JWTWithClaims(t, priv, map[string]any{
		"iss": partnerID,
		"aud": expectedAudience,
		"iat": time.Now().Unix(),
		"exp": time.Now().Add(time.Hour).Unix(),
	})

	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs(partnerJWTHeader, token))
	info := &grpc.UnaryServerInfo{}
	resp, err := i.PartnerJWTInterceptor(ctx, nil, info, noopHandler)
	require.NoError(t, err)

	_, ok := GetPartnerInfoFromContext(respCtx(t, resp))
	assert.False(t, ok, "iss-only JWT with wrong key should not set partner info")
}

// Regression test: verifyPartnerJWT must correctly handle secp256k1 keys stored as
// jwtkeys.Public. A prior bug unconditionally called .P256().ToECDSA() on the sum type,
// which returned nil for secp256k1 keys and panicked in validMethodsForKey.
func TestPartnerJWTInterceptor_Secp256k1KeyViaJWTPublicSumType(t *testing.T) {
	priv, pub := makeSecp256k1Key(t)
	partnerID := "partner-secp"
	dbID := uuid.New()

	// The lookup returns a jwtkeys.Public wrapping a secp256k1 key — the exact path
	// that dbKeyLookup takes when reading from the database.
	i := newInterceptorWithKeyLookup(mapKeyLookup(map[string]*testPartnerEntry{
		partnerID + "/" + testLabel: {pubKey: pub, dbID: dbID},
	}))

	token := makeES256KJWT(t, priv, partnerID, testLabel, time.Now().Add(time.Hour).Unix())
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs(partnerJWTHeader, token))

	info := &grpc.UnaryServerInfo{}
	resp, err := i.PartnerJWTInterceptor(ctx, nil, info, noopHandler)

	require.NoError(t, err)
	got, ok := GetPartnerInfoFromContext(respCtx(t, resp))
	require.True(t, ok, "expected partner info in context for secp256k1 key")
	assert.Equal(t, partnerID, got.PartnerID)
	assert.Equal(t, testLabel, got.Label)
	assert.Equal(t, dbID, got.PartnerDBID)
}
