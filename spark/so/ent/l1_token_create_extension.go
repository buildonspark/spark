package ent

import (
	"github.com/lightsparkdev/spark/common"
)

func (r *L1TokenCreate) ToTokenMetadata() (*common.TokenMetadata, error) {
	return &common.TokenMetadata{
		IssuerPublicKey:         r.IssuerPublicKey,
		TokenName:               r.TokenName,
		TokenTicker:             r.TokenTicker,
		Decimals:                r.Decimals,
		MaxSupply:               r.MaxSupply,
		IsFreezable:             r.IsFreezable,
		CreationEntityPublicKey: common.L1CreationEntityPublicKey,
		Network:                 r.Network,
	}, nil
}
