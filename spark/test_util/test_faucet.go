package testutil

import (
	"bytes"
	"log"
	"sync"

	"github.com/btcsuite/btcd/rpcclient"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/lightsparkdev/spark-go/common"
	"github.com/lightsparkdev/spark-go/so/chain"
)

// NewRegtestClient returns a new rpcclient.Client with a hard-coded
// config for our integration tests.
func NewRegtestClient() (*rpcclient.Client, error) {
	connConfig := rpcclient.ConnConfig{
		Host:         "127.0.0.1:8332",
		User:         "testutil",
		Pass:         "testutilpassword",
		Params:       "regtest",
		DisableTLS:   true,
		HTTPPostMode: true,
	}
	return rpcclient.New(
		&connConfig,
		nil,
	)
}

type FaucetCoin struct {
	Key      *secp256k1.PrivateKey
	OutPoint *wire.OutPoint
	TxOut    *wire.TxOut
}

type Faucet struct {
	client  *rpcclient.Client
	coinsMu sync.Mutex
	coins   []FaucetCoin
}

func NewFaucet(client *rpcclient.Client) *Faucet {
	return &Faucet{
		client:  client,
		coinsMu: sync.Mutex{},
		coins:   make([]FaucetCoin, 0),
	}
}

// Fund returns a faucet coin, which is a UTXO that can be spent in a test.
func (f *Faucet) Fund() (FaucetCoin, error) {
	if len(f.coins) == 0 {
		err := f.Refill()
		if err != nil {
			return FaucetCoin{}, err
		}
	}
	f.coinsMu.Lock()
	defer f.coinsMu.Unlock()
	coin := f.coins[0]
	f.coins = f.coins[1:]
	return coin, nil
}

// Refill mines a block to the faucet, mines 100 blocks to make
// that output spendable, then crafts a new transaction to split it
// into a bunch outputs (coins), which are then freely given away for
// various tests to use.
func (f *Faucet) Refill() error {
	f.coinsMu.Lock()
	defer f.coinsMu.Unlock()
	// Mine a block sending some coins to an address
	key, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		return err
	}
	pubKey := key.PubKey()
	address, err := common.P2TRRawAddressFromPublicKey(pubKey.SerializeCompressed(), common.Regtest)
	if err != nil {
		return err
	}
	blockHash, err := f.client.GenerateToAddress(1, address, nil)
	if err != nil {
		return err
	}

	block, err := f.client.GetBlockVerboseTx(blockHash[0])
	if err != nil {
		return err
	}
	fundingTx, err := chain.TxFromRPCTx(block.Tx[0])
	if err != nil {
		return err
	}

	// Mine 100 blocks to a random address to allow funds to be spendable.
	// This is necessary because coinbase transactions require 100 confirmations
	// to be spendable.
	randomKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		return err
	}
	randomPubKey := randomKey.PubKey()
	randomAddress, err := common.P2TRRawAddressFromPublicKey(randomPubKey.SerializeCompressed(), common.Regtest)
	if err != nil {
		return err
	}
	_, err = f.client.GenerateToAddress(100, randomAddress, nil)
	if err != nil {
		return err
	}

	// Split the output into 1 BTC outputs
	splitTx := wire.NewMsgTx(2)

	fundingTxid := fundingTx.TxHash()
	fundingOutPoint := wire.NewOutPoint(&fundingTxid, 0)
	splitTx.AddTxIn(wire.NewTxIn(fundingOutPoint, nil, nil))

	initialValueSats := fundingTx.TxOut[0].Value
	coinAmountSats := int64(10_000_000)
	feeSats := int64(100_000) // Arbitrary large fee to ensure we have enough
	coinKeys := make([]*secp256k1.PrivateKey, 0)
	coinCount := (initialValueSats - feeSats) / coinAmountSats
	for i := int64(0); i < coinCount; i++ {
		coinKey, err := secp256k1.GeneratePrivateKey()
		if err != nil {
			return err
		}
		coinPubKey := coinKey.PubKey()
		coinKeys = append(coinKeys, coinKey)
		coinScript, err := common.P2TRScriptFromPubKey(coinPubKey)
		if err != nil {
			return err
		}
		splitTx.AddTxOut(wire.NewTxOut(coinAmountSats, coinScript))
	}
	signedSplitTx, err := SignFaucetCoin(splitTx, fundingTx.TxOut[0], key)
	if err != nil {
		return err
	}
	_, err = f.client.SendRawTransaction(signedSplitTx, true)
	if err != nil {
		return err
	}

	// Add coins (outputs of the split tx) to the faucets bag
	splitTxid := signedSplitTx.TxHash()
	for i, txOut := range signedSplitTx.TxOut {
		faucetCoin := FaucetCoin{
			Key:      coinKeys[i],
			OutPoint: wire.NewOutPoint(&splitTxid, uint32(i)),
			TxOut:    txOut,
		}
		f.coins = append(f.coins, faucetCoin)
	}
	log.Printf("Refilled faucet with %d coins", len(f.coins))

	return nil
}

// SignFaucetCoin signs the first input of the given transaction with the given key,
// and returns the signed transaction. Note this expects to be spending
// a taproot output, with the spendingTxOut and key coming from a FaucetCoin from `faucet.Fund()`.
func SignFaucetCoin(unsignedTx *wire.MsgTx, spendingTxOut *wire.TxOut, key *secp256k1.PrivateKey) (*wire.MsgTx, error) {
	prevOutputFetcher := txscript.NewCannedPrevOutputFetcher(
		spendingTxOut.PkScript, spendingTxOut.Value,
	)
	sighashes := txscript.NewTxSigHashes(unsignedTx, prevOutputFetcher)
	fakeTapscriptRootHash := []byte{}
	sig, err := txscript.RawTxInTaprootSignature(
		unsignedTx, sighashes, 0, spendingTxOut.Value, spendingTxOut.PkScript,
		fakeTapscriptRootHash, txscript.SigHashDefault, key,
	)
	if err != nil {
		return nil, err
	}

	var signedTxBuf bytes.Buffer
	err = unsignedTx.Serialize(&signedTxBuf)
	if err != nil {
		return nil, err
	}

	signedTxBytes, err := common.UpdateTxWithSignature(signedTxBuf.Bytes(), 0, sig)
	if err != nil {
		return nil, err
	}
	signedTx, err := common.TxFromRawTxBytes(signedTxBytes)
	if err != nil {
		return nil, err
	}

	err = common.VerifySignature(signedTx, 0, spendingTxOut)
	if err != nil {
		return nil, err
	}

	return signedTx, nil
}
