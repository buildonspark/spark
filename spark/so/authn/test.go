package authn

import (
	"context"

	"github.com/lightsparkdev/spark/common/keys"
)

// InjectSessionForTests injects a test session with the provided public key into the context.
// This is intended for tests to simulate an authenticated session.
func InjectSessionForTests(ctx context.Context, publicKey keys.Public, expirationTimestamp int64) context.Context {
	return context.WithValue(ctx, authnContextKey, &Context{
		Session: &Session{
			identityPublicKey:   publicKey,
			expirationTimestamp: expirationTimestamp,
		},
	})
}
