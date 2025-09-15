package authz

import (
	"context"
	"net"
	"slices"
	"strings"

	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so/middleware"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

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

const (
	XForwardedForHeader = "x-forwarded-for"
)

func (m Mode) Valid() bool {
	return m > ModeUnset && m < ModeMax
}

// InterceptorConfig is, for now, a simple IP-based authorization interceptor, but we will
// extend this to better authorization in the future.
type InterceptorConfig struct {
	// AllowedIPs is a list of IP addresses that are allowed to access the SOs
	// An empty list disables the authorization check
	AllowedIPs []string
	Mode       Mode
	// ProtectedServices is a list of gRPC service prefixes (e.g., "/spark_ssp.SparkSspInternalService")
	// If empty, all services are protected when mode is AuthzModeEnforce
	ProtectedServices []string
	// Indicates the position in the x-forwarded-for header to look for the
	// client IP address. Needed because different infrastructure and load
	// balancer setups may place it differently.
	XffClientIpPosition int
}

type Interceptor struct {
	config *InterceptorConfig
}

func NewAuthzInterceptor(config *InterceptorConfig) *Interceptor {
	return &Interceptor{
		config: config,
	}
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

func (i *Interceptor) authorizeRequest(ctx context.Context, method string) error {
	logger := logging.GetLoggerFromContext(ctx)

	if !i.config.Mode.Valid() {
		logger.Sugar().Warnf("invalid authz mode %d - treating authz as disabled", i.config.Mode)
	}

	// If authorization is disabled or unset, allow all requests
	if i.config.Mode == ModeDisabled {
		return nil
	}

	// Check if this method's service is protected
	if len(i.config.ProtectedServices) > 0 {
		protected := false
		for _, prefix := range i.config.ProtectedServices {
			if strings.HasPrefix(method, prefix) {
				protected = true
				break
			}
		}
		if !protected {
			return nil
		}
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
		logger.With(zap.Error(err)).Sugar().Errorf("Failed to split host and port from peer address %s", p.Addr.String())
		if i.config.Mode == ModeEnforce {
			return status.Error(codes.Internal, "failed to get peer information")
		}
		return nil
	}

	// Internal APIs must only be called from an internal IP on the VPC, even if
	// that means going through a load balancer.
	if !strings.HasPrefix(p.Addr.String(), "10.") {
		logger.Sugar().Warnf("internal API call from peer address %s not internal to VPC", p.Addr.String())
		switch i.config.Mode {
		case ModeEnforce:
			return status.Error(codes.PermissionDenied, "request not allowed from "+p.Addr.String())
		case ModeWarn:
			logger.Sugar().Warnf("warn authz mode - request would be denied - peer address %s is not internal to VPC", p.Addr.String())
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
	if !strings.HasPrefix(clientIP, "10.") && i.config.Mode != ModeLogOnly && !slices.Contains(i.config.AllowedIPs, clientIP) {
		if i.config.Mode == ModeEnforce {
			logger.Sugar().Warnf("internal API call from non-internal or allowlisted IP %s (allowed: %+q) - request denied", clientIP, i.config.AllowedIPs)
			return status.Error(codes.PermissionDenied, "request not allowed from "+clientIP)
		}
		logger.Sugar().Warnf("warn authz mode - internal API call from non-internal or allowlisted IP %s (allowed: %+q) - request would be denied", clientIP, i.config.AllowedIPs)
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

func WithProtectedServices(protectedServices []string) InterceptorConfigOption {
	fullProtectedServicesNames := make([]string, len(protectedServices))
	for i, service := range protectedServices {
		fullProtectedServicesNames[i] = "/" + service + "/"
	}
	return func(config *InterceptorConfig) {
		config.ProtectedServices = fullProtectedServicesNames
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
		ProtectedServices:   []string{},
		XffClientIpPosition: 0,
	}

	for _, opt := range opts {
		opt(config)
	}
	return config
}
