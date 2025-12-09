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
		CreationEntityPublicKey: tc.CreationEntityPublicKey.Serialize(),
		Network:                 tc.Network,
	}, nil
}
