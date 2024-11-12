package so

type Config struct {
	Identifier         string
	IdentityPrivateKey []byte
	SigningOperatorMap map[string]*SigningOperator
	Threshold          uint64
}

func NewConfig(identifier string, identityPrivateKey []byte, signingOperatorMap map[string]*SigningOperator, threshold uint64) *Config {
	return &Config{
		Identifier:         identifier,
		IdentityPrivateKey: identityPrivateKey,
		SigningOperatorMap: signingOperatorMap,
		Threshold:          threshold,
	}
}
