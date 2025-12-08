package ent

import (
	"github.com/lightsparkdev/spark/common"
)

func (tc *TokenCreate) ToTokenMetadata() (*common.TokenMetadata, error) {
	return &common.TokenMetadata{
		IssuerPublicKey:         tc.IssuerPublicKey,
		TokenName:               tc.TokenName,
		TokenTicker:             tc.TokenTicker,
		Decimals:                tc.Decimals,
		MaxSupply:               tc.MaxSupply,
		IsFreezable:             tc.IsFreezable,
		Network:                 tc.Network,
		CreationEntityPublicKey: tc.CreationEntityPublicKey,
	}, nil
}
