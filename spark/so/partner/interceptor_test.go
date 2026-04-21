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
	"github.com/lightsparkdev/spark/so/db"
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

type testPartnerKeyEntry struct {
	pubKey       jwtkeys.Public
	partnerKeyID uuid.UUID
}

// mockPartnerKeyLookup returns a partner key lookup backed by an in-memory map, for testing.
func mockPartnerKeyLookup(entries map[string]*testPartnerKeyEntry) func(ctx context.Context, partnerID string) (*partnerKeyResult, error) {
	return func(_ context.Context, partnerID string) (*partnerKeyResult, error) {
		entry, ok := entries[partnerID]
		if !ok {
			return nil, fmt.Errorf("unknown partner_id: %s", partnerID)
		}
		return &partnerKeyResult{
			pubKey:       entry.pubKey,
			partnerKeyID: entry.partnerKeyID,
		}, nil
	}
}

// mockPartnerLookup returns a partner label lookup backed by an in-memory map, for testing.
// Key format: "partnerKeyID/label" → partners.id.
func mockPartnerLookup(entries map[string]uuid.UUID) func(ctx context.Context, partnerKeyID uuid.UUID, label string) (uuid.UUID, error) {
	return func(_ context.Context, partnerKeyID uuid.UUID, label string) (uuid.UUID, error) {
		key := partnerKeyID.String() + "/" + label
		dbID, ok := entries[key]
		if !ok {
			return uuid.Nil, fmt.Errorf("unknown label %s for partner key %s", label, partnerKeyID)
		}
		return dbID, nil
	}
}

// noopCreatePartner is a createPartner that always fails (no auto-create).
func noopCreatePartner(_ context.Context, _ uuid.UUID, _, _ string, _ jwtkeys.Public) (uuid.UUID, error) {
	return uuid.Nil, fmt.Errorf("auto-create not configured")
}

// makeTestInterceptor creates an interceptor with both partner key and partner label lookups.
func makeTestInterceptor(keys map[string]*testPartnerKeyEntry, labels map[string]uuid.UUID) *Interceptor {
	return newInterceptorWithLookups(mockPartnerKeyLookup(keys), mockPartnerLookup(labels), noopCreatePartner)
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
	i := makeTestInterceptor(map[string]*testPartnerKeyEntry{}, map[string]uuid.UUID{})

	info := &grpc.UnaryServerInfo{FullMethod: "/spark.SparkService/Transfer"}
	resp, err := i.PartnerJWTInterceptor(t.Context(), nil, info, noopHandler)

	require.NoError(t, err)
	_, ok := GetPartnerInfoFromContext(respCtx(t, resp))
	assert.False(t, ok)
}

func TestPartnerJWTInterceptor_ValidES256JWT(t *testing.T) {
	priv, pub := makeP256Key(t)
	partnerID := "partner-a"
	pkID := uuid.New()
	dbID := uuid.New()
	i := makeTestInterceptor(
		map[string]*testPartnerKeyEntry{partnerID: {pubKey: pub, partnerKeyID: pkID}},
		map[string]uuid.UUID{pkID.String() + "/" + testLabel: dbID},
	)

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
	pkID := uuid.New()
	i := makeTestInterceptor(
		map[string]*testPartnerKeyEntry{partnerID: {pubKey: pub, partnerKeyID: pkID}},
		map[string]uuid.UUID{pkID.String() + "/" + testLabel: uuid.New()},
	)

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
	pkID := uuid.New()
	i := makeTestInterceptor(
		map[string]*testPartnerKeyEntry{partnerID: {pubKey: otherPub, partnerKeyID: pkID}},
		map[string]uuid.UUID{pkID.String() + "/" + testLabel: uuid.New()},
	)

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
	i := makeTestInterceptor(map[string]*testPartnerKeyEntry{}, map[string]uuid.UUID{})

	token := makeES256JWT(t, priv, "unknown-partner", testLabel, time.Now().Add(time.Hour).Unix())
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs(partnerJWTHeader, token))

	info := &grpc.UnaryServerInfo{}
	resp, err := i.PartnerJWTInterceptor(ctx, nil, info, noopHandler)
	require.NoError(t, err)

	_, ok := GetPartnerInfoFromContext(respCtx(t, resp))
	assert.False(t, ok)
}

func TestPartnerJWTInterceptor_MalformedToken(t *testing.T) {
	i := makeTestInterceptor(map[string]*testPartnerKeyEntry{}, map[string]uuid.UUID{})

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
	pkID := uuid.New()
	i := makeTestInterceptor(
		map[string]*testPartnerKeyEntry{partnerID: {pubKey: pub, partnerKeyID: pkID}},
		map[string]uuid.UUID{pkID.String() + "/" + testLabel: uuid.New()},
	)

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
	pkIDA := uuid.New()
	pkIDB := uuid.New()
	i := makeTestInterceptor(
		map[string]*testPartnerKeyEntry{
			"partner-a": {pubKey: pubA, partnerKeyID: pkIDA},
			"partner-b": {pubKey: pubB, partnerKeyID: pkIDB},
		},
		map[string]uuid.UUID{
			pkIDA.String() + "/" + testLabel: uuid.New(),
			pkIDB.String() + "/" + testLabel: uuid.New(),
		},
	)

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
	pkID := uuid.New()
	i := makeTestInterceptor(
		map[string]*testPartnerKeyEntry{partnerID: {pubKey: pub, partnerKeyID: pkID}},
		map[string]uuid.UUID{pkID.String() + "/" + testLabel: uuid.New()},
	)

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
	pkID := uuid.New()
	i := makeTestInterceptor(
		map[string]*testPartnerKeyEntry{partnerID: {pubKey: pub, partnerKeyID: pkID}},
		map[string]uuid.UUID{pkID.String() + "/" + testLabel: uuid.New()},
	)

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
	pkID := uuid.New()
	i := makeTestInterceptor(
		map[string]*testPartnerKeyEntry{partnerID: {pubKey: otherPub, partnerKeyID: pkID}},
		map[string]uuid.UUID{pkID.String() + "/" + testLabel: uuid.New()},
	)

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
	pkID := uuid.New()
	i := makeTestInterceptor(
		map[string]*testPartnerKeyEntry{partnerID: {pubKey: p256Pub, partnerKeyID: pkID}},
		map[string]uuid.UUID{pkID.String() + "/" + testLabel: uuid.New()},
	)

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
	pkID := uuid.New()
	i := makeTestInterceptor(
		map[string]*testPartnerKeyEntry{partnerID: {pubKey: secpPub, partnerKeyID: pkID}},
		map[string]uuid.UUID{pkID.String() + "/" + testLabel: uuid.New()},
	)

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
	pkIDA := uuid.New()
	pkIDB := uuid.New()
	i := makeTestInterceptor(
		map[string]*testPartnerKeyEntry{
			"partner-a": {pubKey: p256Pub, partnerKeyID: pkIDA},
			"partner-b": {pubKey: secpPub, partnerKeyID: pkIDB},
		},
		map[string]uuid.UUID{
			pkIDA.String() + "/" + testLabel: uuid.New(),
			pkIDB.String() + "/" + testLabel: uuid.New(),
		},
	)

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
	pkID := uuid.New()
	i := makeTestInterceptor(
		map[string]*testPartnerKeyEntry{partnerID: {pubKey: pub, partnerKeyID: pkID}},
		map[string]uuid.UUID{pkID.String() + "/" + testLabel: uuid.New()},
	)

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
	pkID := uuid.New()
	// Register with testLabel, but JWT will use a different label.
	// JWT still verifies (key lookup is by partner_id), but label lookup fails.
	// createPartner (noopCreatePartner) also fails, so PartnerDBID is empty.
	i := makeTestInterceptor(
		map[string]*testPartnerKeyEntry{partnerID: {pubKey: pub, partnerKeyID: pkID}},
		map[string]uuid.UUID{pkID.String() + "/" + testLabel: uuid.New()},
	)

	token := makeES256JWT(t, priv, partnerID, "wrong-label", time.Now().Add(time.Hour).Unix())
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs(partnerJWTHeader, token))

	info := &grpc.UnaryServerInfo{}
	resp, err := i.PartnerJWTInterceptor(ctx, nil, info, noopHandler)
	require.NoError(t, err)

	got, ok := GetPartnerInfoFromContext(respCtx(t, resp))
	require.True(t, ok, "JWT should still be verified even with wrong label")
	assert.Equal(t, partnerID, got.PartnerID)
	assert.Equal(t, "wrong-label", got.Label)
}

// Iss-only JWT (no sub) succeeds with read-only access (empty label).
func TestPartnerJWTInterceptor_MissingSubClaim_ReadOnly(t *testing.T) {
	priv, pub := makeP256Key(t)
	partnerID := "partner-a"
	pkID := uuid.New()
	i := makeTestInterceptor(
		map[string]*testPartnerKeyEntry{partnerID: {pubKey: pub, partnerKeyID: pkID}},
		map[string]uuid.UUID{pkID.String() + "/" + testLabel: uuid.New()},
	)

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
	require.True(t, ok, "iss-only JWT should succeed with read-only access")
	assert.Equal(t, partnerID, got.PartnerID)
	assert.Empty(t, got.Label)
}

// Iss-only JWT fails when none of the registered keys match the signature.
func TestPartnerJWTInterceptor_IssOnlyJWT_NoMatchingKey(t *testing.T) {
	priv, _ := makeP256Key(t)
	_, otherPub := makeP256Key(t) // different key
	partnerID := "partner-a"

	i := makeTestInterceptor(
		map[string]*testPartnerKeyEntry{partnerID: {pubKey: otherPub, partnerKeyID: uuid.New()}},
		map[string]uuid.UUID{},
	)

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

// --- Auto-create write path tests ---

// When a verified JWT has a label not in the partners table and createPartner is configured,
// the interceptor should auto-create the partner and return a valid PartnerDBID.
func TestPartnerJWTInterceptor_AutoCreateOnNewLabel(t *testing.T) {
	priv, pub := makeP256Key(t)
	partnerID := "partner-a"
	pkID := uuid.New()
	createdDBID := uuid.New()

	i := &Interceptor{
		lookupPartnerKey: mockPartnerKeyLookup(map[string]*testPartnerKeyEntry{
			partnerID: {pubKey: pub, partnerKeyID: pkID},
		}),
		lookupPartner: mockPartnerLookup(map[string]uuid.UUID{}), // empty — label not found
		createPartner: func(_ context.Context, partnerKeyID uuid.UUID, pid, label string, _ jwtkeys.Public) (uuid.UUID, error) {
			assert.Equal(t, pkID, partnerKeyID)
			assert.Equal(t, partnerID, pid)
			assert.Equal(t, "new-label", label)
			return createdDBID, nil
		},
	}

	token := makeES256JWT(t, priv, partnerID, "new-label", time.Now().Add(time.Hour).Unix())
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs(partnerJWTHeader, token))

	info := &grpc.UnaryServerInfo{}
	resp, err := i.PartnerJWTInterceptor(ctx, nil, info, noopHandler)
	require.NoError(t, err)

	got, ok := GetPartnerInfoFromContext(respCtx(t, resp))
	require.True(t, ok)
	assert.Equal(t, partnerID, got.PartnerID)
	assert.Equal(t, "new-label", got.Label)
	assert.Equal(t, createdDBID, got.PartnerDBID)
}

// When createPartner fails, the interceptor should still return PartnerInfo with the label
// but without a PartnerDBID (empty).
func TestPartnerJWTInterceptor_AutoCreateFails_StillReturnsInfo(t *testing.T) {
	priv, pub := makeP256Key(t)
	partnerID := "partner-a"
	pkID := uuid.New()

	i := &Interceptor{
		lookupPartnerKey: mockPartnerKeyLookup(map[string]*testPartnerKeyEntry{
			partnerID: {pubKey: pub, partnerKeyID: pkID},
		}),
		lookupPartner: mockPartnerLookup(map[string]uuid.UUID{}), // empty — label not found
		createPartner: func(_ context.Context, _ uuid.UUID, _, _ string, _ jwtkeys.Public) (uuid.UUID, error) {
			return uuid.Nil, fmt.Errorf("db error")
		},
	}

	token := makeES256JWT(t, priv, partnerID, "new-label", time.Now().Add(time.Hour).Unix())
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs(partnerJWTHeader, token))

	info := &grpc.UnaryServerInfo{}
	resp, err := i.PartnerJWTInterceptor(ctx, nil, info, noopHandler)
	require.NoError(t, err)

	got, ok := GetPartnerInfoFromContext(respCtx(t, resp))
	require.True(t, ok)
	assert.Equal(t, partnerID, got.PartnerID)
	assert.Equal(t, "new-label", got.Label)
	assert.Equal(t, uuid.Nil, got.PartnerDBID, "PartnerDBID should be empty when auto-create fails")
}

// Test dbCreatePartner directly with a real Postgres to verify the constraint error
// fallback works end-to-end (create twice, second should return the same ID).
func TestDbCreatePartner_Idempotent(t *testing.T) {
	_, tc := db.ConnectToTestPostgres(t)
	client := tc.Client

	pubKey := jwtkeys.MustParsePublicHex("0102112b5bc18676433c593f8b02127354b9db8de6070088c1646a3cd58a60b90be3")
	pk, err := client.PartnerKey.Create().
		SetPartnerID("test-partner").
		SetPartnerName("Test").
		SetJwtPublicKey(pubKey).
		Save(t.Context())
	require.NoError(t, err)

	create := dbCreatePartner(client)

	// First create — should succeed.
	id1, err := create(t.Context(), pk.ID, "test-partner", "label-1", pubKey)
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, id1)

	// Second create with same (partner_key, label) — should return same ID.
	id2, err := create(t.Context(), pk.ID, "test-partner", "label-1", pubKey)
	require.NoError(t, err)
	assert.Equal(t, id1, id2, "second create should return the same partner ID")
}

// Regression test: verifyPartnerJWT must correctly handle secp256k1 keys stored as
// jwtkeys.Public. A prior bug unconditionally called .P256().ToECDSA() on the sum type,
// which returned nil for secp256k1 keys and panicked in validMethodsForKey.
func TestPartnerJWTInterceptor_Secp256k1KeyViaJWTPublicSumType(t *testing.T) {
	priv, pub := makeSecp256k1Key(t)
	partnerID := "partner-secp"
	pkID := uuid.New()
	dbID := uuid.New()

	// The lookup returns a jwtkeys.Public wrapping a secp256k1 key — the exact path
	// that dbPartnerKeyLookup takes when reading from the database.
	i := makeTestInterceptor(
		map[string]*testPartnerKeyEntry{partnerID: {pubKey: pub, partnerKeyID: pkID}},
		map[string]uuid.UUID{pkID.String() + "/" + testLabel: dbID},
	)

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
