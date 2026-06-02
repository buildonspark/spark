package tokens

import (
	"context"

	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authn"
	"github.com/lightsparkdev/spark/so/authz"
	"github.com/lightsparkdev/spark/so/knobs"
)

// canBroadcastForSession returns true if the current session is authorized to broadcast token
// transactions on behalf of any identity key, bypassing the sender identity check.
// Authorization is controlled via the KnobTokenBroadcastAllowedPubkeys knob:
// set spark.so.tokens.broadcast_allowed_pubkeys@<identityPubKeyHex> = 1 to grant access.
func canBroadcastForSession(ctx context.Context) bool {
	session, err := authn.GetSessionFromContext(ctx)
	if err != nil {
		return false
	}
	k := knobs.GetKnobsService(ctx)
	return k.GetValueTarget(knobs.KnobTokenBroadcastAllowedPubkeys, new(session.IdentityPublicKey().ToHex()), 0) > 0
}

// enforceBroadcastPolicy checks whether the caller is allowed to broadcast a token transaction
// on behalf of idPubKey. If the session holds broadcaster privileges (via knob), the identity
// check is bypassed and the bypass is logged. Otherwise, the session identity must match idPubKey.
//
// In either branch, the wallet kill switch is enforced against idPubKey — a privileged
// broadcaster must not be able to bypass the freeze on the target wallet.
func enforceBroadcastPolicy(ctx context.Context, config *so.Config, idPubKey keys.Public) error {
	if canBroadcastForSession(ctx) {
		logging.GetLoggerFromContext(ctx).Sugar().Infof(
			"authorized broadcaster bypassing sender identity check for target %s", idPubKey.ToHex(),
		)
	} else {
		if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, config, idPubKey); err != nil {
			return err
		}
	}
	return authz.EnforceWalletNotKillSwitched(ctx, idPubKey)
}
