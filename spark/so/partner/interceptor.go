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
	"github.com/lightsparkdev/spark/so/ent/partnerkey"
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
	// PartnerDBID is the UUID primary key of the partners row (for transfer attribution).
	// Empty when label is absent or label lookup/creation failed.
	PartnerDBID uuid.UUID
	// PartnerID is the partner identifier (JWT "iss" claim).
	PartnerID string
	// Label is the partner label (JWT "sub" claim).
	Label string
}

// partnerKeyResult contains the public key and database ID from partner_keys.
type partnerKeyResult struct {
	pubKey       jwtkeys.Public
	partnerKeyID uuid.UUID
}

// Interceptor validates partner JWTs and injects partner info into the context.
type Interceptor struct {
	lookupPartnerKey func(ctx context.Context, partnerID string) (*partnerKeyResult, error)
	lookupPartner    func(ctx context.Context, partnerKeyID uuid.UUID, label string) (uuid.UUID, error)
	createPartner    func(ctx context.Context, partnerKeyID uuid.UUID, partnerID, label string, pubKey jwtkeys.Public) (uuid.UUID, error)
}

// NewInterceptor creates a new partner JWT Interceptor backed by the database.
func NewInterceptor(dbClient *ent.Client) *Interceptor {
	return &Interceptor{
		lookupPartnerKey: dbPartnerKeyLookup(dbClient),
		lookupPartner:    dbPartnerLookup(dbClient),
		createPartner:    dbCreatePartner(dbClient),
	}
}

// newInterceptorWithLookups creates an Interceptor with custom lookup functions, for testing.
func newInterceptorWithLookups(
	lookupPartnerKey func(ctx context.Context, partnerID string) (*partnerKeyResult, error),
	lookupPartner func(ctx context.Context, partnerKeyID uuid.UUID, label string) (uuid.UUID, error),
	createPartner func(ctx context.Context, partnerKeyID uuid.UUID, partnerID, label string, pubKey jwtkeys.Public) (uuid.UUID, error),
) *Interceptor {
	return &Interceptor{
		lookupPartnerKey: lookupPartnerKey,
		lookupPartner:    lookupPartner,
		createPartner:    createPartner,
	}
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
// Always looks up the public key by partner_id (from partner_keys table).
// When "sub" is present, also resolves the partners row for attribution tracking.
// When "sub" is absent, returns read-only access with empty PartnerDBID.
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

	// Step 1: Look up partner key by partner_id and verify signature.
	pkResult, err := i.lookupPartnerKey(ctx, partnerID)
	if err != nil {
		return nil, err
	}
	if err := i.verifySignature(tokenStr, pkResult.pubKey); err != nil {
		return nil, err
	}

	// Step 2: If label is present, look up or create the partners row for attribution.
	if label != "" {
		partnerDBID, err := i.lookupPartner(ctx, pkResult.partnerKeyID, label)
		if err == nil {
			return &PartnerInfo{
				PartnerDBID: partnerDBID,
				PartnerID:   partnerID,
				Label:       label,
			}, nil
		}
		// Label not found — auto-create.
		partnerDBID, err = i.createPartner(ctx, pkResult.partnerKeyID, partnerID, label, pkResult.pubKey)
		if err == nil {
			return &PartnerInfo{
				PartnerDBID: partnerDBID,
				PartnerID:   partnerID,
				Label:       label,
			}, nil
		}
		logging.GetLoggerFromContext(ctx).Sugar().Warnf(
			"partner JWT verified but auto-create failed for %s/%s: %v", partnerID, label, err)
		// JWT valid but no attribution — return with label but empty PartnerDBID.
		return &PartnerInfo{
			PartnerID: partnerID,
			Label:     label,
		}, nil
	}

	// No label — read-only access, no attribution.
	return &PartnerInfo{
		PartnerID: partnerID,
	}, nil
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

// dbPartnerKeyLookup returns a lookup function that fetches the public key
// for a given partner_id from the partner_keys table.
func dbPartnerKeyLookup(dbClient *ent.Client) func(ctx context.Context, partnerID string) (*partnerKeyResult, error) {
	return func(ctx context.Context, partnerID string) (*partnerKeyResult, error) {
		pk, err := dbClient.PartnerKey.Query().
			Where(partnerkey.PartnerIDEQ(partnerID)).
			Only(ctx)
		if err != nil {
			if ent.IsNotFound(err) {
				return nil, fmt.Errorf("unknown partner_id %s", partnerID)
			}
			return nil, fmt.Errorf("partner key lookup failed for %s: %w", partnerID, err)
		}
		if pk.JwtPublicKey.IsZero() {
			return nil, fmt.Errorf("partner %s has no public key", partnerID)
		}
		return &partnerKeyResult{
			pubKey:       pk.JwtPublicKey,
			partnerKeyID: pk.ID,
		}, nil
	}
}

// dbPartnerLookup returns a lookup function that fetches the partners.id
// for a given (partner_key_id, label) combination.
func dbPartnerLookup(dbClient *ent.Client) func(ctx context.Context, partnerKeyID uuid.UUID, label string) (uuid.UUID, error) {
	return func(ctx context.Context, partnerKeyID uuid.UUID, label string) (uuid.UUID, error) {
		p, err := dbClient.Partner.Query().
			Where(
				entpartner.LabelEQ(label),
				entpartner.HasPartnerKeyWith(partnerkey.IDEQ(partnerKeyID)),
			).
			Only(ctx)
		if err != nil {
			if ent.IsNotFound(err) {
				return uuid.Nil, fmt.Errorf("unknown label %s for partner key %s", label, partnerKeyID)
			}
			logging.GetLoggerFromContext(ctx).Error("failed to look up partner from database",
				zap.String("partner_key_id", partnerKeyID.String()),
				zap.String("label", label),
				zap.Error(err))
			return uuid.Nil, fmt.Errorf("partner lookup failed for key %s / label %s: %w", partnerKeyID, label, err)
		}
		return p.ID, nil
	}
}

// dbCreatePartner creates a partners row for the given (partner_key, label).
// On conflict (row already exists), looks up the existing ID.
func dbCreatePartner(dbClient *ent.Client) func(ctx context.Context, partnerKeyID uuid.UUID, partnerID, label string, pubKey jwtkeys.Public) (uuid.UUID, error) {
	return func(ctx context.Context, partnerKeyID uuid.UUID, partnerID, label string, pubKey jwtkeys.Public) (uuid.UUID, error) {
		p, err := dbClient.Partner.Create().
			SetLabel(label).
			SetPartnerKeyID(partnerKeyID).
			SetPartnerID(partnerID).
			SetPartnerName(partnerID).
			SetJwtPublicKey(pubKey).
			Save(ctx)
		if err == nil {
			return p.ID, nil
		}
		if !ent.IsConstraintError(err) {
			return uuid.Nil, fmt.Errorf("failed to create partner for %s/%s: %w", partnerID, label, err)
		}
		// Already exists — look up the existing ID.
		existing, lookupErr := dbClient.Partner.Query().
			Where(
				entpartner.LabelEQ(label),
				entpartner.HasPartnerKeyWith(partnerkey.IDEQ(partnerKeyID)),
			).
			Only(ctx)
		if lookupErr != nil {
			return uuid.Nil, fmt.Errorf("failed to look up existing partner for %s/%s: %w", partnerID, label, lookupErr)
		}
		return existing.ID, nil
	}
}
