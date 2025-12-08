package ent

import (
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
)

func (r *L1TokenCreate) ToTokenMetadata() (*common.TokenMetadata, error) {
	return &common.TokenMetadata{
		IssuerPublicKey:         r.IssuerPublicKey,
		TokenName:               r.TokenName,
		TokenTicker:             r.TokenTicker,
		Decimals:                r.Decimals,
		MaxSupply:               r.MaxSupply,
		IsFreezable:             r.IsFreezable,
		Network:                 r.Network,
		CreationEntityPublicKey: keys.Public{}, // L1 creation entity public key denoted by nil value
	}, nil
}
