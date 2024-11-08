package so

import "crypto/ecdsa"

type SigningOperator struct {
	Identifier        string
	Address           string
	IdentityPublicKey *ecdsa.PublicKey
}
