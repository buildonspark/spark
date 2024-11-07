package so

import "crypto/ecdsa"

type Config struct {
	Identifier string
	PrivateKey *ecdsa.PrivateKey
}

func NewConfig(identifier string, privateKey *ecdsa.PrivateKey) *Config {
	return &Config{
		Identifier: identifier,
		PrivateKey: privateKey,
	}
}
