//go:build !lightspark

package authn

// DefaultUnauthenticatedConfig returns the production configuration.
func DefaultUnauthenticatedConfig() UnauthenticatedConfig {
	return UnauthenticatedConfig{
		Methods:         baseUnauthenticatedMethods(),
		ServicePrefixes: baseServicePrefixes(),
	}
}
