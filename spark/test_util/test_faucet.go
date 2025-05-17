package testutil

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"sync"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/rpcclient"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/wallet"
)

var (
	// Static keys for deterministic testing
	// P2TRAddress: bcrt1p2uy9zw5ltayucsuzl4tet6ckelzawp08qrtunacscsszflye907q62uqhl
	staticFaucetKeyBytes, _ = hex.DecodeString("deadbeef1337cafe4242424242424242deadbeef1337cafe4242424242424242")
	staticFaucetKey         = secp256k1.PrivKeyFromBytes(staticFaucetKeyBytes)

	// P2TRAddress: bcrt1pwr5k38p68ceyrnm2tvrp50dvmg3grh6uvayjl3urwtxejhd3dw4swz6p58
	staticMiningKeyBytes, _ = hex.DecodeString("1337cafe4242deadbeef4242424242421337cafe4242deadbeef424242424242")
	staticMiningKey         = secp256k1.PrivKeyFromBytes(staticMiningKeyBytes)

	// Constants for coin amounts
	coinAmountSats int64 = 10_000_000
	feeAmountSats  int64 = 1_000
	targetNumCoins       = 20

	// Singleton instance
	instance *Faucet
	once     sync.Once
)

// scanUnspent represents an unspent output found by scanning
type scanUnspent struct {
	TxID   string      `json:"txid"`
	Vout   uint32      `json:"vout"`
	Amount json.Number `json:"amount"`
	Height int64       `json:"height"`
}

// scanTxOutSetResult represents the result of scanning the UTXO set
type scanTxOutSetResult struct {
	Success  bool          `json:"success"`
	Height   int64         `json:"height"`
	Unspents []scanUnspent `json:"unspents"`
}

type uTXO struct {
	TxID   string
	Vout   uint32
	Amount int64
	Height int64
}

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

// GetFaucetInstance returns the singleton instance of the Faucet
func GetFaucetInstance(client *rpcclient.Client) *Faucet {
	once.Do(func() {
		instance = &Faucet{
			client:  client,
			coinsMu: sync.Mutex{},
			coins:   make([]FaucetCoin, 0),
		}
	})
	return instance
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

// btcToSats converts a BTC amount (as a decimal string) to satoshis
func btcToSats(btc json.Number) (int64, error) {
	f, err := btc.Float64()
	if err != nil {
		return 0, err
	}
	amount, err := btcutil.NewAmount(f)
	if err != nil {
		return 0, err
	}
	return int64(amount), nil
}

// scanForSpendableUTXOs scans for any spendable UTXOs at the mining address
func (f *Faucet) scanForSpendableUTXOs() ([]uTXO, int64, error) {
	miningPubKey := staticMiningKey.PubKey()
	miningAddress, err := common.P2TRRawAddressFromPublicKey(miningPubKey.SerializeCompressed(), common.Regtest)
	if err != nil {
		return nil, 0, err
	}

	descriptor := fmt.Sprintf("addr(%s)", miningAddress)
	params := []json.RawMessage{
		json.RawMessage(`"start"`),
		json.RawMessage(fmt.Sprintf(`["%s"]`, descriptor)),
	}

	result, err := f.client.RawRequest("scantxoutset", params)
	if err != nil {
		return nil, 0, err
	}

	var scanResult scanTxOutSetResult
	err = json.Unmarshal(result, &scanResult)
	if err != nil {
		return nil, 0, err
	}

	if !scanResult.Success {
		return nil, scanResult.Height, fmt.Errorf("scan failed")
	}

	var utxos []uTXO
	for _, unspent := range scanResult.Unspents {
		sats, err := btcToSats(unspent.Amount)
		if err != nil {
			continue
		}
		utxos = append(utxos, uTXO{
			TxID:   unspent.TxID,
			Vout:   unspent.Vout,
			Amount: sats,
			Height: unspent.Height,
		})
	}

	return utxos, scanResult.Height, nil
}

// findSuitableUTXO finds a UTXO that is large enough and mature enough to use
func (f *Faucet) findSuitableUTXO() (*uTXO, error) {
	utxos, height, err := f.scanForSpendableUTXOs()
	if err != nil {
		return nil, err
	}

	minAmount := coinAmountSats + feeAmountSats
	for _, utxo := range utxos {
		isMature := height-utxo.Height >= 100
		isValueEnough := utxo.Amount >= minAmount

		if isMature && isValueEnough {
			return &utxo, nil
		}
	}

	return nil, nil
}

// Refill mines a block to the faucet if needed, then crafts a new transaction to split it
// into a bunch outputs (coins), which are then freely given away for various tests to use.
func (f *Faucet) Refill() error {
	f.coinsMu.Lock()
	defer f.coinsMu.Unlock()

	selectedUTXO, err := f.findSuitableUTXO()
	if err != nil {
		return err
	}

	var fundingTx *wire.MsgTx
	var fundingTxOut *wire.TxOut

	if selectedUTXO != nil {
		txHash, err := chainhash.NewHashFromStr(selectedUTXO.TxID)
		if err != nil {
			return err
		}
		tx, err := f.client.GetRawTransaction(txHash)
		if err != nil {
			return err
		}
		fundingTx = tx.MsgTx()
		fundingTxOut = fundingTx.TxOut[selectedUTXO.Vout]
	} else {
		// No suitable UTXO found, need to mine a new block
		miningPubKey := staticMiningKey.PubKey()
		miningAddress, err := common.P2TRRawAddressFromPublicKey(miningPubKey.SerializeCompressed(), common.Regtest)
		if err != nil {
			return err
		}

		blockHash, err := f.client.GenerateToAddress(1, miningAddress, nil)
		if err != nil {
			return err
		}

		block, err := f.client.GetBlockVerboseTx(blockHash[0])
		if err != nil {
			return err
		}
		fundingTx = wire.NewMsgTx(3)
		txBytes, err := hex.DecodeString(block.Tx[0].Hex)
		if err != nil {
			return err
		}
		err = fundingTx.Deserialize(bytes.NewReader(txBytes))
		if err != nil {
			return err
		}
		fundingTxOut = fundingTx.TxOut[0]

		// Mine 100 blocks to make funds spendable
		_, err = f.client.GenerateToAddress(100, miningAddress, nil)
		if err != nil {
			return err
		}
	}

	splitTx := wire.NewMsgTx(3)
	var fundingOutPoint *wire.OutPoint
	if selectedUTXO != nil {
		txHash, err := chainhash.NewHashFromStr(selectedUTXO.TxID)
		if err != nil {
			return err
		}
		fundingOutPoint = wire.NewOutPoint(txHash, selectedUTXO.Vout)
	} else {
		fundingTxid := fundingTx.TxHash()
		fundingOutPoint = wire.NewOutPoint(&fundingTxid, 0)
	}
	splitTx.AddTxIn(wire.NewTxIn(fundingOutPoint, nil, nil))

	initialValueSats := fundingTxOut.Value
	maxPossibleCoins := (initialValueSats - feeAmountSats) / coinAmountSats
	numCoinsToCreate := min(int64(targetNumCoins), maxPossibleCoins)

	if numCoinsToCreate < 1 {
		log.Printf("Selected UTXO (%d sats) is too small to create even one faucet coin of %d sats", initialValueSats, coinAmountSats)
		return nil
	}

	faucetPubKey := staticFaucetKey.PubKey()
	faucetScript, err := common.P2TRScriptFromPubKey(faucetPubKey)
	if err != nil {
		return err
	}

	for i := int64(0); i < numCoinsToCreate; i++ {
		splitTx.AddTxOut(wire.NewTxOut(coinAmountSats, faucetScript))
	}

	remainingValue := initialValueSats - (numCoinsToCreate * coinAmountSats) - feeAmountSats
	if remainingValue > 0 {
		miningScript, err := common.P2TRScriptFromPubKey(staticMiningKey.PubKey())
		if err != nil {
			return err
		}
		splitTx.AddTxOut(wire.NewTxOut(remainingValue, miningScript))
	}

	signedSplitTx, err := SignFaucetCoin(splitTx, fundingTxOut, staticMiningKey)
	if err != nil {
		return err
	}
	_, err = f.client.SendRawTransaction(signedSplitTx, true)
	if err != nil {
		return err
	}

	splitTxid := signedSplitTx.TxHash()
	for i := 0; i < int(numCoinsToCreate); i++ {
		faucetCoin := FaucetCoin{
			Key:      staticFaucetKey,
			OutPoint: wire.NewOutPoint(&splitTxid, uint32(i)),
			TxOut:    signedSplitTx.TxOut[i],
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
		fakeTapscriptRootHash, txscript.SigHashAll, key,
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

	err = common.VerifySignatureSingleInput(signedTx, 0, spendingTxOut)
	if err != nil {
		return nil, err
	}

	return signedTx, nil
}

func SignFaucetCoinFeeBump(anchorOutPoint *wire.OutPoint, coin FaucetCoin, outputScript []byte) (*wire.MsgTx, error) {
	feeBumpTx := wire.NewMsgTx(3)
	feeBumpTx.AddTxIn(wire.NewTxIn(coin.OutPoint, nil, nil))
	feeBumpTx.AddTxIn(wire.NewTxIn(anchorOutPoint, nil, nil))
	feeBumpTx.AddTxOut(wire.NewTxOut(coin.TxOut.Value*90/100, outputScript))

	prevOuts := make(map[wire.OutPoint]*wire.TxOut)
	prevOuts[*coin.OutPoint] = coin.TxOut
	prevOuts[*anchorOutPoint] = wallet.EphemeralAnchorOutput()
	prevOutputFetcher := txscript.NewMultiPrevOutFetcher(prevOuts)
	sighashes := txscript.NewTxSigHashes(feeBumpTx, prevOutputFetcher)
	fakeTapscriptRootHash := []byte{}
	sig, err := txscript.RawTxInTaprootSignature(
		feeBumpTx, sighashes, 0, coin.TxOut.Value, coin.TxOut.PkScript,
		fakeTapscriptRootHash, txscript.SigHashDefault, coin.Key,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to sign fee bump tx: %w", err)
	}

	var signedTxBuf bytes.Buffer
	err = feeBumpTx.Serialize(&signedTxBuf)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize fee bump tx: %w", err)
	}

	signedTxBytes, err := common.UpdateTxWithSignature(signedTxBuf.Bytes(), 0, sig)
	if err != nil {
		return nil, fmt.Errorf("failed to update fee bump tx with signature: %w", err)
	}
	signedFeeBumpTx, err := common.TxFromRawTxBytes(signedTxBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse fee bump tx: %w", err)
	}

	err = common.VerifySignatureMultiInput(signedFeeBumpTx, prevOutputFetcher)
	if err != nil {
		return nil, fmt.Errorf("failed to verify fee bump tx: %w", err)
	}

	return signedFeeBumpTx, nil
}
