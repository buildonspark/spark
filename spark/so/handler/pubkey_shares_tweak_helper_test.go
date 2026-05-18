package handler

import (
	"math/big"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	secretsharing "github.com/lightsparkdev/spark/common/secret_sharing"
	"github.com/lightsparkdev/spark/so"
	"github.com/stretchr/testify/require"
)

// buildValidPubkeySharesTweak returns pubkey_shares_tweak entries derived
// from the polynomial committed to by `proofs` — one per operator in
// cfg.SigningOperatorMap. Each entry equals f(operator.ID+1)·G, the value
// helper.ValidatePubkeySharesTweak (added in #6867 to fix the 2026-05-15
// prod keyshare divergence) expects. Use this when a test already has its
// own `secret_share_tweak.Proofs` (because it also needs the underlying
// tweak private key for downstream assertions) and just needs a consistent
// pubkey_shares_tweak map.
//
// The function lives in a non-_test.go file so OSS-tagged tests (e.g.
// transfer_handler_claim_test.go, transfer_handler_mimo_test.go) can use
// it without depending on the `//go:build lightspark` test fixtures.
func buildValidPubkeySharesTweak(t *testing.T, cfg *so.Config, proofs [][]byte) map[string][]byte {
	t.Helper()
	fieldModulus := secp256k1.S256().N
	pubkeySharesTweak := make(map[string][]byte, len(cfg.SigningOperatorMap))
	for identifier, operator := range cfg.SigningOperatorMap {
		index := new(big.Int).SetUint64(operator.ID)
		index.Add(index, big.NewInt(1))
		pub, err := secretsharing.EvaluatePolynomialCommitment(proofs, index, fieldModulus)
		require.NoError(t, err, "evaluate polynomial commitment for operator %s", identifier)
		pubkeySharesTweak[identifier] = pub.Serialize()
	}
	return pubkeySharesTweak
}
