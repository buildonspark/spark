package partner

import (
	"context"
	"crypto"
	"fmt"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	jwtkeys "github.com/lightsparkdev/spark/common/keys/jwt"
	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so/ent"
	entpartner "github.com/lightsparkdev/spark/so/ent/partner"
	"github.com/lightsparkdev/spark/so/knobs"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

func init() {
	// Register ES256K (secp256k1 + SHA-256) as a JWT signing method.
	// This algorithm is not part of the core JWT spec but is widely used in
	// blockchain contexts where partners already hold secp256k1 keys.
	jwt.RegisterSigningMethod("ES256K", func() jwt.SigningMethod {
		return &jwt.SigningMethodECDSA{
			Name:      "ES256K",
			Hash:      crypto.SHA256,
			KeySize:   32,
			CurveBits: 256,
		}
	})
}

const partnerJWTHeader = "x-partner-jwt"

// expectedAudience is the audience value that partner JWTs must contain.
const expectedAudience = "spark-so"

type contextKey string

const partnerContextKey = contextKey("partner_info")

// PartnerInfo holds the verified partner identity extracted from a JWT.
type PartnerInfo struct {
	// PartnerDBID is the UUID primary key of the partner row in the database.
	PartnerDBID uuid.UUID
	// PartnerID is the partner identifier (JWT "iss" claim).
	PartnerID string
	// Label is the partner label (JWT "sub" claim).
	Label string
}

// keyLookupResult contains the public key and database ID for a partner.
type keyLookupResult struct {
	pubKey      jwtkeys.Public
	partnerDBID uuid.UUID
}

// Interceptor validates partner JWTs and injects partner info into the context.
type Interceptor struct {
	lookupKey             func(ctx context.Context, partnerID, label string) (*keyLookupResult, error)
	lookupKeysByPartnerID func(ctx context.Context, partnerID string) ([]*keyLookupResult, error)
}

// NewInterceptor creates a new partner JWT Interceptor backed by the database.
func NewInterceptor(dbClient *ent.Client) *Interceptor {
	return &Interceptor{
		lookupKey:             dbKeyLookup(dbClient),
		lookupKeysByPartnerID: dbKeyLookupByPartnerID(dbClient),
	}
}

// newInterceptorWithKeyLookup creates an Interceptor with a custom key lookup function, for testing.
func newInterceptorWithKeyLookup(lookup func(ctx context.Context, partnerID, label string) (*keyLookupResult, error)) *Interceptor {
	return &Interceptor{lookupKey: lookup}
}

// KnobGatedInterceptor returns a UnaryServerInterceptor that only runs the partner JWT
// check when the KnobEnablePartnerJWT knob is enabled; otherwise it passes through.
func (i *Interceptor) KnobGatedInterceptor(knobsService knobs.Knobs) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if knobsService.GetValue(knobs.KnobEnablePartnerJWT, 0) > 0 {
			return i.PartnerJWTInterceptor(ctx, req, info, handler)
		}
		return handler(ctx, req)
	}
}

// PartnerJWTInterceptor extracts and verifies the x-partner-jwt header.
// If absent or invalid, the request proceeds normally without partner info in context.
func (i *Interceptor) PartnerJWTInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return handler(ctx, req)
	}

	vals := md.Get(partnerJWTHeader)
	if len(vals) == 0 {
		return handler(ctx, req)
	}

	pInfo, err := i.verifyPartnerJWT(ctx, vals[0])
	if err != nil {
		// Per design: invalid JWT → request proceeds normally, unattributed.
		logging.GetLoggerFromContext(ctx).Sugar().Warnf("partner JWT verification failed, request will proceed unattributed: %v", err)
		return handler(ctx, req)
	}

	ctx = context.WithValue(ctx, partnerContextKey, pInfo)
	return handler(ctx, req)
}

// GetPartnerInfoFromContext returns the verified partner info from the context, if present.
func GetPartnerInfoFromContext(ctx context.Context) (*PartnerInfo, bool) {
	val := ctx.Value(partnerContextKey)
	if val == nil {
		return nil, false
	}
	info, ok := val.(*PartnerInfo)
	return info, ok
}

// verifyPartnerJWT parses and verifies a partner JWT (ES256 or ES256K).
// Uses standard claims: "iss" → partner_id, "sub" → label.
// When "sub" is present, the public key is looked up by (partner_id, label).
// When "sub" is absent, all keys for partner_id are tried (read-only access).
// Traffic attribution (write path) still requires both "iss" and "sub".
func (i *Interceptor) verifyPartnerJWT(ctx context.Context, tokenStr string) (*PartnerInfo, error) {
	// Parse without verification first to extract iss/sub for key lookup.
	unverified, _, err := jwt.NewParser().ParseUnverified(tokenStr, &jwt.RegisteredClaims{})
	if err != nil {
		return nil, fmt.Errorf("failed to parse JWT: %w", err)
	}

	claims, ok := unverified.Claims.(*jwt.RegisteredClaims)
	if !ok {
		return nil, fmt.Errorf("failed to extract JWT claims")
	}

	partnerID := claims.Issuer
	label := claims.Subject
	if partnerID == "" {
		return nil, fmt.Errorf("JWT missing iss claim")
	}

	if label != "" {
		return i.verifyWithLabel(ctx, tokenStr, partnerID, label)
	}
	return i.verifyWithoutLabel(ctx, tokenStr, partnerID)
}

// verifyWithLabel verifies a JWT using the public key for (partner_id, label).
func (i *Interceptor) verifyWithLabel(ctx context.Context, tokenStr, partnerID, label string) (*PartnerInfo, error) {
	result, err := i.lookupKey(ctx, partnerID, label)
	if err != nil {
		return nil, err
	}
	if err := i.verifySignature(tokenStr, result.pubKey); err != nil {
		return nil, err
	}
	return &PartnerInfo{
		PartnerDBID: result.partnerDBID,
		PartnerID:   partnerID,
		Label:       label,
	}, nil
}

// verifyWithoutLabel verifies a JWT by trying all keys registered for the partner_id.
// Returns PartnerInfo with Label="" indicating aggregate (read-only) access.
func (i *Interceptor) verifyWithoutLabel(ctx context.Context, tokenStr, partnerID string) (*PartnerInfo, error) {
	if i.lookupKeysByPartnerID == nil {
		return nil, fmt.Errorf("iss-only JWT verification is not supported")
	}

	results, err := i.lookupKeysByPartnerID(ctx, partnerID)
	if err != nil {
		return nil, err
	}

	for _, result := range results {
		if err := i.verifySignature(tokenStr, result.pubKey); err == nil {
			return &PartnerInfo{
				PartnerID: partnerID,
				Label:     "",
			}, nil
		}
	}
	return nil, fmt.Errorf("JWT verification failed: no matching key for partner_id %s", partnerID)
}

// verifySignature verifies the JWT signature against the given public key.
func (i *Interceptor) verifySignature(tokenStr string, pubKey jwtkeys.Public) error {
	_, err := jwt.ParseWithClaims(tokenStr, &jwt.RegisteredClaims{}, func(token *jwt.Token) (any, error) {
		if _, ok := token.Method.(*jwt.SigningMethodECDSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %s", token.Header["alg"])
		}
		return pubKey.ToECDSA(), nil
	},
		jwt.WithExpirationRequired(),
		jwt.WithValidMethods(validMethodsForKey(pubKey)),
		jwt.WithAudience(expectedAudience),
	)
	if err != nil {
		return fmt.Errorf("JWT verification failed: %w", err)
	}
	return nil
}

// validMethodsForKey returns the JWT algorithm name(s) accepted for the given key's curve.
func validMethodsForKey(pub jwtkeys.Public) []string {
	if pub.KeyType() == jwtkeys.KeyTypeP256 {
		return []string{"ES256"}
	}
	return []string{"ES256K"}
}

// dbKeyLookupByPartnerID returns a lookup function that fetches all public keys
// for a given partner_id. Used when the JWT has no "sub" claim (iss-only).
func dbKeyLookupByPartnerID(dbClient *ent.Client) func(ctx context.Context, partnerID string) ([]*keyLookupResult, error) {
	return func(ctx context.Context, partnerID string) ([]*keyLookupResult, error) {
		partners, err := dbClient.Partner.Query().
			Where(entpartner.PartnerID(partnerID)).
			All(ctx)
		if err != nil {
			return nil, fmt.Errorf("partner lookup failed for %s: %w", partnerID, err)
		}
		if len(partners) == 0 {
			return nil, fmt.Errorf("unknown partner_id %s", partnerID)
		}

		seen := make(map[string]bool)
		var results []*keyLookupResult
		for _, p := range partners {
			if p.JwtPublicKey.IsZero() {
				continue
			}
			keyStr := p.JwtPublicKey.ToHex()
			if seen[keyStr] {
				continue
			}
			seen[keyStr] = true
			results = append(results, &keyLookupResult{
				pubKey:      p.JwtPublicKey,
				partnerDBID: p.ID,
			})
		}
		if len(results) == 0 {
			return nil, fmt.Errorf("partner %s has no public keys", partnerID)
		}
		return results, nil
	}
}

// dbKeyLookup returns a key lookup function that fetches the partner's JwtPubKey
// from the database using the composite (partner_id, label) key.
func dbKeyLookup(dbClient *ent.Client) func(ctx context.Context, partnerID, label string) (*keyLookupResult, error) {
	return func(ctx context.Context, partnerID, label string) (*keyLookupResult, error) {
		p, err := dbClient.Partner.Query().
			Where(
				entpartner.PartnerID(partnerID),
				entpartner.LabelEQ(label),
			).
			Only(ctx)
		if err != nil {
			if ent.IsNotFound(err) {
				return nil, fmt.Errorf("unknown partner_id %s with label %s", partnerID, label)
			}
			logging.GetLoggerFromContext(ctx).Error("failed to look up partner from database",
				zap.String("partner_id", partnerID),
				zap.String("label", label),
				zap.Error(err))
			return nil, fmt.Errorf("partner lookup failed for %s/%s: %w", partnerID, label, err)
		}

		pubKey := p.JwtPublicKey
		if pubKey.IsZero() {
			return nil, fmt.Errorf("partner %s/%s has no public key", partnerID, label)
		}
		return &keyLookupResult{
			pubKey:      pubKey,
			partnerDBID: p.ID,
		}, nil
	}
}
