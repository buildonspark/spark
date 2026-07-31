package authz

import (
	"context"
	"net"
	"slices"
	"strings"

	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so/middleware"
	"github.com/lightsparkdev/spark/so/rpcauth/brontide"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// Auth-path label values for the internalAuthzDecisionCounter.
const (
	authPathBrontide    = "brontide"     // request admitted because brontide.PeerOperator resolved.
	authPathVPCIP       = "vpc-ip"       // request admitted because both peer addr and client IP are in 10.x.x.x.
	authPathAllowlistIP = "allowlist-ip" // request admitted because client IP is on the explicit allowlist.
	authPathIPRejected  = "ip-rejected"  // IP gate considered the request unauthorized (may still be admitted in Warn/LogOnly modes).
)

// newAuthzDecisionsCounter resolves the otel counter at construction time so tests that set a manual meter provider
// before NewAuthzInterceptor still get observable increments. Falls back to a noop counter if registration fails, so
// metric setup never blocks request handling.
func newAuthzDecisionsCounter() metric.Int64Counter {
	meter := otel.GetMeterProvider().Meter("spark.so.authz")
	counter, err := meter.Int64Counter(
		"internal_authz_decisions_total",
		metric.WithDescription("Authz decisions for internal-only gRPC methods, labeled by which auth path produced the verdict."),
	)
	if err != nil {
		otel.Handle(err)
		return noop.Int64Counter{}
	}
	return counter
}

type Mode int

const (
	// ModeUnset means the config is not set, so we can set a sane default instead.
	ModeUnset Mode = iota
	ModeDisabled
	ModeWarn
	ModeEnforce
	ModeLogOnly
	ModeMax
)

func (m Mode) Valid() bool {
	return m > ModeUnset && m < ModeMax
}

// InterceptorConfig parameterizes the internal-only authz interceptor. The interceptor admits a request when either
// the call carries brontide AuthInfo or its source IP is on the VPC / allowlist.
type InterceptorConfig struct {
	// AllowedIPs is the explicit IP allowlist for internal methods that arrive without brontide AuthInfo. An empty
	// list means only VPC-internal (10.x.x.x) sources are accepted on the IP path.
	AllowedIPs []string
	Mode       Mode
	// IsProtectedMethod decides whether a given full gRPC method name is subject to internal-only authz. Production
	// wires this to rpcpolicy.IsInternalOnly. When nil, all methods are treated as protected (when Mode is enforcing).
	IsProtectedMethod func(fullMethod string) bool
	// XffClientIpPosition indicates the position in the x-forwarded-for header to look for the client IP address.
	// Needed because different load balancer setups may place it differently.
	XffClientIpPosition int
}

type Interceptor struct {
	config             *InterceptorConfig
	authDecisionsTotal metric.Int64Counter
}

func NewAuthzInterceptor(config *InterceptorConfig) *Interceptor {
	return &Interceptor{config: config, authDecisionsTotal: newAuthzDecisionsCounter()}
}

// recordAuthDecision increments the per-path counter. Called once per protected request that reaches the IP /
// brontide branches.
func (i *Interceptor) recordAuthDecision(ctx context.Context, path string) {
	i.authDecisionsTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("path", path)))
}

func (i *Interceptor) UnaryServerInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	if err := i.authorizeRequest(ctx, info.FullMethod); err != nil {
		return nil, err
	}
	return handler(ctx, req)
}

func (i *Interceptor) StreamServerInterceptor(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	if err := i.authorizeRequest(ss.Context(), info.FullMethod); err != nil {
		return err
	}
	return handler(srv, ss)
}

// isMethodProtected returns true when this gRPC method should be subjected to IP allowlisting. When the predicate is
// nil, the interceptor defaults to treating every method as protected.
func (i *Interceptor) isMethodProtected(method string) bool {
	if i.config.IsProtectedMethod == nil {
		return true
	}
	return i.config.IsProtectedMethod(method)
}

func (i *Interceptor) authorizeRequest(ctx context.Context, method string) error {
	logger := logging.GetLoggerFromContext(ctx)

	if !i.config.Mode.Valid() {
		logger.Sugar().Warnf("invalid authz mode %d - treating authz as disabled", i.config.Mode)
	}

	// If authorization is disabled or unset, allow all requests
	if i.config.Mode == ModeDisabled {
		return nil
	}

	if !i.isMethodProtected(method) {
		return nil
	}

	// A brontide-authenticated caller has cryptographically proven possession of a known operator's identity private
	// key via the Noise_XK handshake. That's strictly stronger than the IP allowlist, so we let the call through
	// regardless of source IP. The IP allowlist is still the gate for non-brontide traffic — including calls to the
	// rpcpolicy.AuthOperatorBrontide methods that arrive on the public listener.
	if peerOp := brontide.PeerOperator(ctx); peerOp != nil {
		i.recordAuthDecision(ctx, authPathBrontide)
		logger.Sugar().Debugf("internal API call authenticated via brontide as operator %s", peerOp.Identifier)
		return nil
	}

	var (
		p        *peer.Peer
		clientIP string
		err      error
		ok       bool
	)
	if p, ok = peer.FromContext(ctx); !ok {
		if i.config.Mode == ModeEnforce {
			return status.Error(codes.Internal, "failed to get peer information")
		}
		return nil
	}
	if clientIP, _, err = net.SplitHostPort(p.Addr.String()); err != nil {
		logger.With(zap.Error(err)).Sugar().Errorf("Failed to split host and port from peer address %s", p.Addr)
		if i.config.Mode == ModeEnforce {
			return status.Error(codes.Internal, "failed to get peer information")
		}
		return nil
	}

	// Internal APIs must only be called from an internal IP on the VPC, even if
	// that means going through a load balancer.
	if !strings.HasPrefix(p.Addr.String(), "10.") {
		i.recordAuthDecision(ctx, authPathIPRejected)
		logger.Sugar().Warnf("internal API call (path=%s) from peer address %s not internal to VPC", authPathIPRejected, p.Addr)
		switch i.config.Mode {
		case ModeEnforce:
			return status.Error(codes.PermissionDenied, "request not allowed from "+p.Addr.String())
		case ModeWarn:
			logger.Sugar().Warnf("warn authz mode - request would be denied - peer address %s is not internal to VPC", p.Addr)
		default:
			break
		}
		return nil
	}

	// If an x-forwarded-for header is present, we use the client IP from that
	// header. However, if that doesn't exist (and is an error that we ignore),
	// we instead stick with the peer connection's IP address.
	if xffClientIP, err := middleware.GetClientIpFromHeader(ctx, i.config.XffClientIpPosition); err == nil {
		clientIP = xffClientIP
	}

	// Only allow requests from internal IPs on the VPC, which are all 10.x.x.x IPs, or allowlisted IPs.
	switch {
	case strings.HasPrefix(clientIP, "10."):
		i.recordAuthDecision(ctx, authPathVPCIP)
	case slices.Contains(i.config.AllowedIPs, clientIP):
		i.recordAuthDecision(ctx, authPathAllowlistIP)
	default:
		// In LogOnly mode the call is admitted regardless; we still want to count it as ip-rejected so dashboards can
		// surface "would have been denied" volume during rollout.
		i.recordAuthDecision(ctx, authPathIPRejected)
		if i.config.Mode == ModeLogOnly {
			return nil
		}
		if i.config.Mode == ModeEnforce {
			logger.Sugar().Warnf("internal API call (path=%s) from non-internal or allowlisted IP %s (allowed: %+q) - request denied", authPathIPRejected, clientIP, i.config.AllowedIPs)
			return status.Error(codes.PermissionDenied, "request not allowed from "+clientIP)
		}
		logger.Sugar().Warnf("warn authz mode (path=%s) - internal API call from non-internal or allowlisted IP %s (allowed: %+q) - request would be denied", authPathIPRejected, clientIP, i.config.AllowedIPs)
	}
	return nil
}

type InterceptorConfigOption func(*InterceptorConfig)

func WithMode(mode Mode) InterceptorConfigOption {
	return func(config *InterceptorConfig) {
		config.Mode = mode
	}
}

func WithAllowedIPs(ips []string) InterceptorConfigOption {
	return func(config *InterceptorConfig) {
		config.AllowedIPs = ips
	}
}

// WithIsProtectedMethod configures the authz interceptor to consult a per-method predicate (typically
// rpcpolicy.IsInternalOnly) to decide whether internal-only authz applies. When unset, every method is treated as protected.
func WithIsProtectedMethod(fn func(fullMethod string) bool) InterceptorConfigOption {
	return func(config *InterceptorConfig) {
		config.IsProtectedMethod = fn
	}
}

func WithXffClientIpPosition(position int) InterceptorConfigOption {
	return func(config *InterceptorConfig) {
		config.XffClientIpPosition = position
	}
}

func NewAuthzConfig(opts ...InterceptorConfigOption) *InterceptorConfig {
	config := &InterceptorConfig{
		AllowedIPs:          []string{},
		Mode:                ModeDisabled,
		XffClientIpPosition: 0,
	}

	for _, opt := range opts {
		opt(config)
	}
	return config
}
