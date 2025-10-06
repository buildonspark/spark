package authn

import (
	"context"

	"github.com/lightsparkdev/spark/common/keys"
)

// InjectSessionForTests injects a test session with the provided public key hex into the context.
// This is intended for tests to simulate an authenticated session.
func InjectSessionForTests(ctx context.Context, publicKeyHex string, expirationTimestamp int64) context.Context {
	key, err := keys.ParsePublicKeyHex(publicKeyHex)
	if err != nil {
		// If the provided key is invalid, return the original context without modification.
		return ctx
	}
	return context.WithValue(ctx, authnContextKey, &Context{
		Session: &Session{
			identityPublicKey:   key,
			expirationTimestamp: expirationTimestamp,
		},
	})
}
