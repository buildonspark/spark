package tokens

import (
	"testing"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConstructAndSignJusticeTransaction_InsufficientFunds(t *testing.T) {
	signingKey := keys.GeneratePrivateKey()
	pubKeyHash := btcutil.Hash160(signingKey.Public().Serialize())
	addr, err := btcutil.NewAddressWitnessPubKeyHash(pubKeyHash, &chaincfg.RegressionNetParams)
	require.NoError(t, err)

	var dummyHash chainhash.Hash
	input := JusticeInputWithBond{
		TxIn:          wire.NewTxIn(wire.NewOutPoint(&dummyHash, 0), nil, nil),
		PrevValueSats: 100, // Less than fee
	}

	tx, err := ConstructAndSignJusticeTransaction(signingKey, input, addr, 500)

	assert.Nil(t, tx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "insufficient funds")
}

func TestConstructAndSignJusticeTransaction_FundsEqualToFee(t *testing.T) {
	signingKey := keys.GeneratePrivateKey()
	pubKeyHash := btcutil.Hash160(signingKey.Public().Serialize())
	addr, err := btcutil.NewAddressWitnessPubKeyHash(pubKeyHash, &chaincfg.RegressionNetParams)
	require.NoError(t, err)

	var dummyHash chainhash.Hash
	input := JusticeInputWithBond{
		TxIn:          wire.NewTxIn(wire.NewOutPoint(&dummyHash, 0), nil, nil),
		PrevValueSats: 500, // Equal to fee - should still fail (need > fee)
	}

	tx, err := ConstructAndSignJusticeTransaction(signingKey, input, addr, 500)

	assert.Nil(t, tx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "insufficient funds")
}

func TestConstructAndSignJusticeTransaction_DustOutput(t *testing.T) {
	signingKey := keys.GeneratePrivateKey()
	pubKeyHash := btcutil.Hash160(signingKey.Public().Serialize())
	addr, err := btcutil.NewAddressWitnessPubKeyHash(pubKeyHash, &chaincfg.RegressionNetParams)
	require.NoError(t, err)

	var dummyHash chainhash.Hash
	input := JusticeInputWithBond{
		TxIn:          wire.NewTxIn(wire.NewOutPoint(&dummyHash, 0), nil, nil),
		PrevValueSats: 600, // With 500 fee, sendValue = 100 which is below dust threshold (546)
	}

	tx, err := ConstructAndSignJusticeTransaction(signingKey, input, addr, 500)

	assert.Nil(t, tx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "output would be dust")
}

func TestBroadcastJusticeTransaction_NilTokenOutput(t *testing.T) {
	ctx := t.Context()
	soPrivKey := keys.GeneratePrivateKey()

	tokenToWithdraw := &parsedOutputWithdrawal{
		sparkTxHash: []byte{1, 2, 3},
		sparkTxVout: 0,
	}

	tx, entTx, err := BroadcastJusticeTransaction(
		ctx,
		nil, // bitcoin client
		soPrivKey,
		btcnetwork.Regtest,
		nil, // nil token output
		&parsedWithdrawal{},
		tokenToWithdraw,
	)

	assert.Nil(t, tx)
	assert.Nil(t, entTx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "token output not found")
}

func TestBroadcastJusticeTransaction_ZeroRevocationSecret(t *testing.T) {
	ctx := t.Context()
	soPrivKey := keys.GeneratePrivateKey()

	tokenOutput := &ent.TokenOutput{
		// SpentRevocationSecret is zero-value (not set)
	}

	tokenToWithdraw := &parsedOutputWithdrawal{
		sparkTxHash: []byte{1, 2, 3},
		sparkTxVout: 0,
	}

	tx, entTx, err := BroadcastJusticeTransaction(
		ctx,
		nil, // bitcoin client
		soPrivKey,
		btcnetwork.Regtest,
		tokenOutput,
		&parsedWithdrawal{},
		tokenToWithdraw,
	)

	assert.Nil(t, tx)
	assert.Nil(t, entTx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "revocation secret is not set")
}

func TestConstructAndSignJusticeTransaction_Success(t *testing.T) {
	revocationKey := keys.GeneratePrivateKey()
	revocationXOnly := revocationKey.Public().SerializeXOnly()

	ownerKey := keys.GeneratePrivateKey()
	ownerXOnly := ownerKey.Public().SerializeXOnly()

	scriptData, err := ConstructRevocationCsvTaprootOutput(revocationXOnly, ownerXOnly, 144)
	require.NoError(t, err)

	receiverKey := keys.GeneratePrivateKey()
	pubKeyHash := btcutil.Hash160(receiverKey.Public().Serialize())
	receiverAddr, err := btcutil.NewAddressWitnessPubKeyHash(pubKeyHash, &chaincfg.RegressionNetParams)
	require.NoError(t, err)

	var dummyHash chainhash.Hash
	inputValue := int64(10000)
	feeSats := int64(500)
	input := JusticeInputWithBond{
		TxIn:           wire.NewTxIn(wire.NewOutPoint(&dummyHash, 0), nil, nil),
		PrevPkScript:   scriptData.ScriptPubKey,
		PrevValueSats:  inputValue,
		TimelockScript: scriptData.TimelockScript,
	}

	tx, err := ConstructAndSignJusticeTransaction(revocationKey, input, receiverAddr, feeSats)

	require.NoError(t, err)
	require.NotNil(t, tx)
	assert.Len(t, tx.TxIn, 1)
	assert.Len(t, tx.TxOut, 1)
	assert.Equal(t, inputValue-feeSats, tx.TxOut[0].Value)
	assert.NotEmpty(t, tx.TxIn[0].Witness)
}
