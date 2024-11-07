package so

type Config struct {
	Identifier string
}

func NewConfig(identifier string) *Config {
	return &Config{
		Identifier: identifier,
	}
}
