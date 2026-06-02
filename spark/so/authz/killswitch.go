package authz

import (
	"context"
	"sync"

	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so/knobs"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	"go.uber.org/zap"
)

// IsWalletKillSwitched reports whether the given identity public key is
// gated by the master wallet kill switch knob on this SO.
func IsWalletKillSwitched(ctx context.Context, identityPublicKey keys.Public) bool {
	return knobs.GetKnobsService(ctx).GetValueTarget(
		knobs.KnobKillSwitchWallet, new(identityPublicKey.ToHex()), 0,
	) > 0
}

// EnforceWalletNotKillSwitched returns a permission-denied Error if the wallet
// kill switch knob is set for the given identity public key. The returned
// Error's on-wire format is identical to ErrorCodeIdentityMismatch — same
// gRPC code, same message — so probing callers cannot distinguish a kill
// switch from any other permission failure. The internal ErrorCode is
// ErrorCodeWalletKillSwitched so SO logs and unit tests can differentiate.
//
// Call this from every state-mutating user-facing handler immediately after
// EnforceSessionIdentityPublicKeyMatches. Read-only RPCs must NOT call this.
func EnforceWalletNotKillSwitched(ctx context.Context, identityPublicKey keys.Public) error {
	if !IsWalletKillSwitched(ctx, identityPublicKey) {
		return nil
	}
	logging.GetLoggerFromContext(ctx).
		With(zap.String("identity_public_key", identityPublicKey.ToHex())).
		Sugar().Warn("wallet kill switch enforced")
	incrementKillSwitchBlockedMetric(ctx)
	return &Error{
		Code:    ErrorCodeWalletKillSwitched,
		Message: identityMismatchMessage,
	}
}

var (
	killSwitchMeterOnce    sync.Once
	killSwitchBlockedTotal metric.Int64Counter = noop.Int64Counter{}
)

func incrementKillSwitchBlockedMetric(ctx context.Context) {
	killSwitchMeterOnce.Do(func() {
		counter, err := otel.GetMeterProvider().Meter("spark.so.authz").Int64Counter(
			"spark.so.killswitch.blocked_total",
			metric.WithDescription("Total number of requests blocked by the wallet kill switch"),
			metric.WithUnit("1"),
		)
		if err != nil {
			otel.Handle(err)
			return
		}
		killSwitchBlockedTotal = counter
	})
	killSwitchBlockedTotal.Add(ctx, 1)
}
