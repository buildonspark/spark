package so

import "crypto/ecdsa"

type Config struct {
	Identifier                string
	PrivateKey                *ecdsa.PrivateKey
	PublicKeyMap              map[string]ecdsa.PublicKey
	SigningOperatorAddressMap map[string]string
}

func NewConfig(identifier string, privateKey *ecdsa.PrivateKey, publicKeyMap map[string]ecdsa.PublicKey, signingOperatorAddressMap map[string]string) *Config {
	return &Config{
		Identifier:                identifier,
		PrivateKey:                privateKey,
		PublicKeyMap:              publicKeyMap,
		SigningOperatorAddressMap: signingOperatorAddressMap,
	}
}
