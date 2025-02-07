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

type FaucetCoin struct {
	Key      *secp256k1.PrivateKey
	OutPoint *wire.OutPoint
	TxOut    *wire.TxOut
}

type Faucet struct {
	client *rpcclient.Client
	coins  []FaucetCoin
	mu     sync.Mutex
}

func NewFaucet(client *rpcclient.Client) *Faucet {
	return &Faucet{
		client: client,
		coins:  make([]FaucetCoin, 0),
		mu:     sync.Mutex{},
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
	f.mu.Lock()
	defer f.mu.Unlock()
	coin := f.coins[0]
	f.coins = f.coins[1:]
	return coin, nil
}

// Refill mines a block and splits it into a bunch outputs,
// which is freely gives away for various tests to use.
func (f *Faucet) Refill() error {
	f.mu.Lock()
	defer f.mu.Unlock()
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

	// Mine 100 blocks to allow funds to be spendable
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
	fundingTxid := fundingTx.TxHash()
	fundingOutPoint := wire.NewOutPoint(&fundingTxid, 0)
	splitTx := wire.NewMsgTx(2)
	splitTx.AddTxIn(wire.NewTxIn(fundingOutPoint, nil, nil))
	initialValue := fundingTx.TxOut[0].Value
	coinAmount := int64(10_000_000)
	coinKeys := make([]*secp256k1.PrivateKey, 0)
	for initialValue > coinAmount+100_000 { // 100_000 for a fee buffer
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
		splitTx.AddTxOut(wire.NewTxOut(coinAmount, coinScript))
		initialValue -= coinAmount
	}
	signedSplitTx, err := SignFaucetCoin(splitTx, fundingTx.TxOut[0], key)
	if err != nil {
		return err
	}
	_, err = f.client.SendRawTransaction(signedSplitTx, true)
	if err != nil {
		return err
	}

	splitTxid := signedSplitTx.TxHash()
	for i := 0; i < len(signedSplitTx.TxOut); i++ {
		faucetCoin := FaucetCoin{
			Key:      coinKeys[i],
			OutPoint: wire.NewOutPoint(&splitTxid, uint32(i)),
			TxOut:    signedSplitTx.TxOut[i],
		}
		f.coins = append(f.coins, faucetCoin)
	}
	log.Printf("Refilled faucet with %d coins", len(f.coins))

	return nil
}

// SignOnChainTx signs the first input of the given transaction with the given key,
// and returns the signed transaction. Note this expects to be spending
// a taproot output, mainly created by `faucet.Fund()`.
func SignFaucetCoin(unsignedTx *wire.MsgTx, fundingTxOut *wire.TxOut, key *secp256k1.PrivateKey) (*wire.MsgTx, error) {
	prevOutputFetcher := txscript.NewCannedPrevOutputFetcher(
		fundingTxOut.PkScript, fundingTxOut.Value,
	)
	sighashes := txscript.NewTxSigHashes(unsignedTx, prevOutputFetcher)
	fakeTapscriptRootHash := []byte{}
	sig, err := txscript.RawTxInTaprootSignature(
		unsignedTx, sighashes, 0, fundingTxOut.Value, fundingTxOut.PkScript,
		fakeTapscriptRootHash, txscript.SigHashDefault, key,
	)
	if err != nil {
		return nil, err
	}

	var exitTxBuf bytes.Buffer
	err = unsignedTx.Serialize(&exitTxBuf)
	if err != nil {
		return nil, err
	}

	signedExitTxBytes, err := common.UpdateTxWithSignature(exitTxBuf.Bytes(), 0, sig)
	if err != nil {
		return nil, err
	}
	signedExitTx, err := common.TxFromRawTxBytes(signedExitTxBytes)
	if err != nil {
		return nil, err
	}

	err = common.VerifySignature(signedExitTx, 0, fundingTxOut)
	if err != nil {
		return nil, err
	}

	return signedExitTx, nil
}
