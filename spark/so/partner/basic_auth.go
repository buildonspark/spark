package partner

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/lightsparkdev/spark/common/logging"
	pbpartner "github.com/lightsparkdev/spark/proto/spark_partner"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/partnerkey"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"golang.org/x/crypto/argon2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
)

// errPartnerAuthUnavailable marks a failure that is NOT credential-related (e.g. the DB lookup
// failed). It maps to codes.Unavailable so an operator-side outage isn't reported to partners as
// an authentication failure. errPartnerAuthOverloaded marks load-shedding when too many argon2id
// verifications are already in flight; it maps to codes.ResourceExhausted.
var (
	errPartnerAuthUnavailable = errors.New("partner auth temporarily unavailable")
	errPartnerAuthOverloaded  = errors.New("partner auth overloaded")
)

// maxConcurrentArgon2Verifications bounds how many argon2id verifications run at once. Each costs
// ~64 MiB (argon2idMemKiB), and the endpoint is public + pre-rate-limit (auth must run before the
// identity-keyed limiter), so without a cap a flood of bogus Basic headers could exhaust memory.
// Excess requests are shed with ResourceExhausted rather than queued.
const maxConcurrentArgon2Verifications = 8

const authorizationHeader = "authorization"

// basicAuthScheme is the (case-insensitive) Authorization scheme we accept.
const basicAuthScheme = "basic"

// SparkPartnerServiceMethodPrefix is the gRPC FullMethod prefix for SparkPartnerService. It's the
// single source of truth for "is this a partner-facing method" (used here and by the DB middleware
// to scope partner requests to a read-only session).
var SparkPartnerServiceMethodPrefix = "/" + pbpartner.SparkPartnerService_ServiceDesc.ServiceName + "/"

// argon2id parameters for the timing-parity dummy hash (see runDummyArgon2id). The real
// verifier reads each stored hash's own parameters from its PHC string.
const (
	argon2idTime    = 3
	argon2idMemKiB  = 65536 // memory cost, in KiB (= 64 MiB)
	argon2idThreads = 4
	argon2idKeyLen  = 32
)

// Upper bounds on the parameters parsed from a stored PHC string. The hash is provisioned by hand
// (SQL), so these guard against a fat-fingered cost — a huge m or t — that would make a single
// argon2.IDKey call allocate gigabytes or burn CPU long enough to OOM/stall the operator (the
// concurrency semaphore caps how many verifications run at once, not how expensive each one is).
// The bounds are generous relative to the canonical cost above; any legitimate value stays well
// under them.
const (
	argon2idMaxMemKiB      = 256 * 1024 // 256 MiB
	argon2idMaxTime        = 16
	argon2idMaxParallelism = 16
)

var timingSentinelSalt = []byte("partner-basic-auth-timing")

// BasicAuthInterceptor authenticates SparkPartnerService requests via HTTP Basic Auth,
// verifying the presented secret against partner_keys.basic_auth_secret_hash. It is a no-op
// for every other service (which authenticate via session tokens or partner JWTs).
type BasicAuthInterceptor struct {
	lookupSecretHash func(ctx context.Context, partnerID string) (string, error)
	// argon2Sem caps concurrent argon2id verifications (see maxConcurrentArgon2Verifications).
	argon2Sem chan struct{}
}

// NewBasicAuthInterceptor creates a BasicAuthInterceptor backed by the database.
func NewBasicAuthInterceptor(dbClient *ent.Client) *BasicAuthInterceptor {
	return &BasicAuthInterceptor{
		lookupSecretHash: dbBasicAuthSecretHashLookup(dbClient),
		argon2Sem:        make(chan struct{}, maxConcurrentArgon2Verifications),
	}
}

// UnaryServerInterceptor enforces Basic Auth on SparkPartnerService methods and passes every
// other method through untouched. SparkPartnerService is declared AuthPartnerBasic in
// rpcpolicy, so the session authn interceptor leaves the "Basic …" credential alone and it
// reaches this interceptor. On success the verified partner identity is added to the context;
// on failure a generic Unauthenticated error is returned that doesn't reveal whether the
// partner exists or which part of the credential was wrong.
func (i *BasicAuthInterceptor) UnaryServerInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	if !strings.HasPrefix(info.FullMethod, SparkPartnerServiceMethodPrefix) {
		return handler(ctx, req)
	}
	pInfo, err := i.authenticate(ctx)
	if err != nil {
		logging.GetLoggerFromContext(ctx).Sugar().Warnf("partner basic auth failed: %v", err)
		switch {
		case errors.Is(err, errPartnerAuthOverloaded):
			// Load-shedding, not a credential problem: tell the caller to back off and retry.
			return nil, sparkerrors.WrapErrorWithCode(fmt.Errorf("partner authentication temporarily unavailable"), codes.ResourceExhausted)
		case errors.Is(err, errPartnerAuthUnavailable):
			// Operator-side failure (e.g. DB down). Not credential-dependent, so don't report it as
			// an auth failure — that would misdirect partner debugging and skew error dashboards.
			return nil, sparkerrors.WrapErrorWithCode(fmt.Errorf("partner authentication temporarily unavailable"), codes.Unavailable)
		default:
			// Generic so it doesn't reveal whether the partner exists or which part was wrong.
			return nil, sparkerrors.WrapErrorWithCode(fmt.Errorf("invalid partner credentials"), codes.Unauthenticated)
		}
	}
	return handler(context.WithValue(ctx, partnerContextKey, pInfo), req)
}

func (i *BasicAuthInterceptor) authenticate(ctx context.Context) (*PartnerInfo, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, fmt.Errorf("missing request metadata")
	}
	vals := md.Get(authorizationHeader)
	if len(vals) == 0 {
		return nil, fmt.Errorf("missing Authorization header")
	}

	partnerID, secret, err := parseBasicAuth(vals[0])
	if err != nil {
		return nil, err
	}

	// An unknown partner returns ("", nil); a non-nil error means an actual lookup failure
	// (e.g. the DB is down), which we surface as an infra error (not a credential failure) and
	// without spending an argon2id computation.
	hash, err := i.lookupSecretHash(ctx, partnerID)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errPartnerAuthUnavailable, err)
	}

	// Gate the (expensive, ~64 MiB) argon2id work — both the real verify and the timing dummy — so a
	// flood of bogus credentials can't exhaust memory. Shed load rather than queue it.
	select {
	case i.argon2Sem <- struct{}{}:
		defer func() { <-i.argon2Sem }()
	default:
		return nil, fmt.Errorf("%w: too many concurrent verifications", errPartnerAuthOverloaded)
	}

	if hash == "" {
		// Unknown or unconfigured partner: run a dummy argon2id so the response time matches a
		// known partner with a wrong secret, preventing partner enumeration via response timing.
		runDummyArgon2id(secret)
		return nil, fmt.Errorf("no basic auth secret configured for partner %s", partnerID)
	}

	valid, err := verifyArgon2idHash(secret, hash)
	if err != nil {
		return nil, fmt.Errorf("failed to verify secret for partner %s: %w", partnerID, err)
	}
	if !valid {
		return nil, fmt.Errorf("secret mismatch for partner %s", partnerID)
	}

	return &PartnerInfo{PartnerID: partnerID}, nil
}

// runDummyArgon2id performs a throwaway argon2id so unknown/unconfigured partners take the
// same wall time as a known partner with a wrong secret (request-level constant time).
func runDummyArgon2id(secret string) {
	_ = argon2.IDKey([]byte(secret), timingSentinelSalt, argon2idTime, argon2idMemKiB, argon2idThreads, argon2idKeyLen)
}

// parseBasicAuth parses an "Authorization: Basic base64(partner_id:secret)" header value.
func parseBasicAuth(header string) (partnerID, secret string, err error) {
	scheme, encoded, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, basicAuthScheme) {
		return "", "", fmt.Errorf("Authorization header is not Basic")
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return "", "", fmt.Errorf("malformed base64 in Authorization header: %w", err)
	}
	id, sec, ok := strings.Cut(string(decoded), ":")
	if !ok {
		return "", "", fmt.Errorf("Basic credentials not in partner_id:secret form")
	}
	if id == "" || sec == "" {
		return "", "", fmt.Errorf("empty partner_id or secret")
	}
	return id, sec, nil
}

// verifyArgon2idHash verifies secret against a PHC-encoded argon2id hash using a constant-time
// compare. The expected format is exactly:
//
//	$argon2id$v=19$m=<mem-KiB>,t=<time>,p=<par>$<base64 salt>$<base64 hash>
//
// i.e. six "$"-separated parts, the parameter field in m,t,p order, and no extra fields (a `keyid`,
// `data`, or reordered params will fail to parse and reject the credential). Because the hash is
// provisioned by hand via SQL, the provisioning runbook MUST emit this canonical shape — generate it
// with golang.org/x/crypto/argon2 (as makeArgon2idHash does in the tests), not an arbitrary tool.
func verifyArgon2idHash(secret, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	// ["", "argon2id", "v=19", "m=..,t=..,p=..", salt, hash]
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, fmt.Errorf("invalid argon2id PHC format")
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return false, fmt.Errorf("invalid argon2 version field: %w", err)
	}
	if version != argon2.Version {
		return false, fmt.Errorf("unsupported argon2 version %d", version)
	}

	var memory, time uint32
	var parallelism uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &parallelism); err != nil {
		return false, fmt.Errorf("invalid argon2 parameters: %w", err)
	}
	// argon2.IDKey panics if time < 1 or threads < 1, so reject a malformed PHC with
	// zero parameters rather than crashing the request goroutine on every auth attempt.
	if memory == 0 || time == 0 || parallelism == 0 {
		return false, fmt.Errorf("argon2 parameters must be non-zero (m=%d, t=%d, p=%d)", memory, time, parallelism)
	}
	// Reject absurd costs before handing them to argon2.IDKey: a hand-provisioned hash with a huge
	// m or t would otherwise let a single request allocate gigabytes / burn CPU and OOM the operator.
	if memory > argon2idMaxMemKiB || time > argon2idMaxTime || parallelism > argon2idMaxParallelism {
		return false, fmt.Errorf("argon2 parameters exceed bounds (m=%d, t=%d, p=%d; max m=%d, t=%d, p=%d)",
			memory, time, parallelism, argon2idMaxMemKiB, argon2idMaxTime, argon2idMaxParallelism)
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, fmt.Errorf("invalid argon2 salt: %w", err)
	}
	if len(salt) == 0 {
		return false, fmt.Errorf("empty argon2 salt in stored PHC string")
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, fmt.Errorf("invalid argon2 hash: %w", err)
	}
	// Guard against an empty hash component (e.g. a stored string ending in "$"): with
	// keyLen 0, argon2.IDKey returns an empty digest and ConstantTimeCompare of two empty
	// slices returns 1, which would authenticate any secret.
	if len(want) == 0 {
		return false, fmt.Errorf("empty argon2 hash in stored PHC string")
	}

	got := argon2.IDKey([]byte(secret), salt, time, memory, parallelism, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// dbBasicAuthSecretHashLookup returns a lookup that fetches basic_auth_secret_hash for a
// partner_id. An unknown partner_id returns ("", nil) — treated as "no secret configured"
// by the caller — so only genuine lookup failures (e.g. the DB being down) surface as errors.
func dbBasicAuthSecretHashLookup(dbClient *ent.Client) func(ctx context.Context, partnerID string) (string, error) {
	return func(ctx context.Context, partnerID string) (string, error) {
		pk, err := dbClient.PartnerKey.Query().
			Where(partnerkey.PartnerIDEQ(partnerID)).
			Only(ctx)
		if ent.IsNotFound(err) {
			return "", nil
		}
		if err != nil {
			return "", fmt.Errorf("partner key lookup failed for %s: %w", partnerID, err)
		}
		return pk.BasicAuthSecretHash, nil
	}
}
