package so

import "crypto/ecdsa"

type Config struct {
	Identifier         string
	IdentityPrivateKey *ecdsa.PrivateKey
	SigningOperatorMap map[string]*SigningOperator
	Threshold          uint64
}

func NewConfig(identifier string, identityPrivateKey *ecdsa.PrivateKey, signingOperatorMap map[string]*SigningOperator, threshold uint64) *Config {
	return &Config{
		Identifier:         identifier,
		IdentityPrivateKey: identityPrivateKey,
		SigningOperatorMap: signingOperatorMap,
		Threshold:          threshold,
	}
}
