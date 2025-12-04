package ent

import (
	"fmt"

	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
)

func (tc *TokenCreate) ToTokenMetadata() (*common.TokenMetadata, error) {
	network, err := btcnetwork.FromSchemaNetwork(tc.Network)
	if err != nil {
		return nil, fmt.Errorf("failed to convert network: %w", err)
	}

	return &common.TokenMetadata{
		IssuerPublicKey:         tc.IssuerPublicKey,
		TokenName:               tc.TokenName,
		TokenTicker:             tc.TokenTicker,
		Decimals:                tc.Decimals,
		MaxSupply:               tc.MaxSupply,
		IsFreezable:             tc.IsFreezable,
		CreationEntityPublicKey: tc.CreationEntityPublicKey.Serialize(),
		Network:                 network,
	}, nil
}
