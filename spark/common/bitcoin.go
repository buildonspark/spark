package common

import (
	"bytes"
	"encoding/hex"
	"fmt"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/ent/schema"
)

// Network is the type for Bitcoin networks used with the operator.
type Network int

const (
	Unspecified Network = iota
	// Mainnet is the main Bitcoin network.
	Mainnet Network = 10
	// Regtest is the regression test network.
	Regtest Network = 20
	// Testnet is the test network.
	Testnet Network = 30
	// Signet is the signet network.
	Signet Network = 40
)

func (n Network) String() string {
	switch n {
	case Mainnet, Unspecified:
		return "mainnet"
	case Regtest:
		return "regtest"
	case Testnet:
		return "testnet"
	case Signet:
		return "signet"
	default:
		return "mainnet"
	}
}

func NetworkFromString(network string) (Network, error) {
	switch network {
	case "mainnet":
		return Mainnet, nil
	case "regtest":
		return Regtest, nil
	case "testnet":
		return Testnet, nil
	case "signet":
		return Signet, nil
	default:
		return Unspecified, fmt.Errorf("invalid network: %s", network)
	}
}

func NetworkFromProtoNetwork(protoNetwork pb.Network) (Network, error) {
	switch protoNetwork {
	case pb.Network_MAINNET:
		return Mainnet, nil
	case pb.Network_REGTEST:
		return Regtest, nil
	case pb.Network_TESTNET:
		return Testnet, nil
	case pb.Network_SIGNET:
		return Signet, nil
	default:
		return Unspecified, fmt.Errorf("invalid network")
	}
}

func NetworkFromSchemaNetwork(schemaNetwork schema.Network) (Network, error) {
	switch schemaNetwork {
	case schema.NetworkMainnet:
		return Mainnet, nil
	case schema.NetworkRegtest:
		return Regtest, nil
	case schema.NetworkTestnet:
		return Testnet, nil
	case schema.NetworkSignet:
		return Signet, nil
	case schema.NetworkUnspecified:
		return Unspecified, fmt.Errorf("invalid network")
	default:
		return Unspecified, fmt.Errorf("invalid network")
	}
}

func SchemaNetworkFromNetwork(network Network) (schema.Network, error) {
	switch network {
	case Mainnet:
		return schema.NetworkMainnet, nil
	case Regtest:
		return schema.NetworkRegtest, nil
	case Testnet:
		return schema.NetworkTestnet, nil
	case Signet:
		return schema.NetworkSignet, nil
	default:
		return schema.NetworkUnspecified, fmt.Errorf("invalid network")
	}
}

func ProtoNetworkFromNetwork(network Network) (pb.Network, error) {
	switch network {
	case Mainnet:
		return pb.Network_MAINNET, nil
	case Regtest:
		return pb.Network_REGTEST, nil
	case Testnet:
		return pb.Network_TESTNET, nil
	case Signet:
		return pb.Network_SIGNET, nil
	default:
		return pb.Network_MAINNET, fmt.Errorf("invalid network")
	}
}

func SchemaNetworkFromProtoNetwork(protoNetwork pb.Network) (schema.Network, error) {
	switch protoNetwork {
	case pb.Network_MAINNET:
		return schema.NetworkMainnet, nil
	case pb.Network_REGTEST:
		return schema.NetworkRegtest, nil
	case pb.Network_TESTNET:
		return schema.NetworkTestnet, nil
	case pb.Network_SIGNET:
		return schema.NetworkSignet, nil
	default:
		return schema.NetworkUnspecified, fmt.Errorf("invalid network")
	}
}

// NetworkParams converts a Network to its corresponding chaincfg.Params
func NetworkParams(network Network) *chaincfg.Params {
	switch network {
	case Mainnet:
		return &chaincfg.MainNetParams
	case Regtest:
		return &chaincfg.RegressionNetParams
	case Testnet:
		return &chaincfg.TestNet3Params
	default:
		return &chaincfg.MainNetParams
	}
}

func SchemaNetwork(network Network) schema.Network {
	switch network {
	case Mainnet:
		return schema.NetworkMainnet
	case Regtest:
		return schema.NetworkRegtest
	case Testnet:
		return schema.NetworkTestnet
	default:
		return schema.NetworkMainnet
	}
}

// P2TRScriptFromPubKey returns a P2TR script from a public key.
func P2TRScriptFromPubKey(pubKey *secp256k1.PublicKey) ([]byte, error) {
	taprootKey := txscript.ComputeTaprootKeyNoScript(pubKey)
	return txscript.PayToTaprootScript(taprootKey)
}

func P2TRRawAddressFromPublicKey(pubKey []byte, network Network) (btcutil.Address, error) {
	if len(pubKey) != 33 {
		return nil, fmt.Errorf("public key must be 33 bytes")
	}

	internalKey, err := btcec.ParsePubKey(pubKey)
	if err != nil {
		return nil, err
	}

	// Tweak the internal key with empty merkle root
	taprootKey := txscript.ComputeTaprootKeyNoScript(internalKey)
	taprootAddress, err := btcutil.NewAddressTaproot(
		// Convert a 33 byte public key to a 32 byte x-only public key
		schnorr.SerializePubKey(taprootKey),
		NetworkParams(network),
	)
	if err != nil {
		return nil, err
	}

	return taprootAddress, nil
}

// P2TRAddressFromPublicKey returns a P2TR address from a public key.
func P2TRAddressFromPublicKey(pubKey []byte, network Network) (*string, error) {
	addrRaw, err := P2TRRawAddressFromPublicKey(pubKey, network)
	if err != nil {
		return nil, err
	}
	addr := addrRaw.EncodeAddress()
	return &addr, nil
}

// P2TRAddressFromPkScript returns a P2TR address from a public script.
func P2TRAddressFromPkScript(pkScript []byte, network Network) (*string, error) {
	parsedScript, err := txscript.ParsePkScript(pkScript)
	if err != nil {
		return nil, err
	}

	networkParams := NetworkParams(network)
	if parsedScript.Class() == txscript.WitnessV1TaprootTy {
		address, err := parsedScript.Address(networkParams)
		if err != nil {
			return nil, err
		}
		taprootAddress, err := btcutil.NewAddressTaproot(address.ScriptAddress(), networkParams)
		if err != nil {
			return nil, err
		}
		p2trAddress := taprootAddress.String()
		return &p2trAddress, nil
	}

	return nil, fmt.Errorf("not a Taproot address")
}

// TxFromRawTxHex returns a btcd MsgTx from a raw tx hex.
func TxFromRawTxHex(rawTxHex string) (*wire.MsgTx, error) {
	txBytes, err := hex.DecodeString(rawTxHex)
	if err != nil {
		return nil, err
	}
	return TxFromRawTxBytes(txBytes)
}

// TxFromRawTxBytes returns a btcd MsgTx from a raw tx bytes.
func TxFromRawTxBytes(rawTxBytes []byte) (*wire.MsgTx, error) {
	var tx wire.MsgTx
	err := tx.Deserialize(bytes.NewReader(rawTxBytes))
	if err != nil {
		return nil, err
	}
	return &tx, nil
}

func SerializeTx(tx *wire.MsgTx) ([]byte, error) {
	buf := bytes.NewBuffer(make([]byte, 0, tx.SerializeSize()))
	if err := tx.Serialize(buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// SigHashFromTx returns sighash from a tx.
func SigHashFromTx(tx *wire.MsgTx, inputIndex int, prevOutput *wire.TxOut) ([]byte, error) {
	prevOutputFetcher := txscript.NewCannedPrevOutputFetcher(
		prevOutput.PkScript, prevOutput.Value,
	)
	sighashes := txscript.NewTxSigHashes(tx, prevOutputFetcher)

	sigHash, err := txscript.CalcTaprootSignatureHash(sighashes, txscript.SigHashDefault, tx, inputIndex, prevOutputFetcher)
	if err != nil {
		return nil, err
	}
	return sigHash, nil
}

// UpdateTxWithSignature verifies the signature and update the transaction with the signature.
// Callsites should verify the signature using `VerifySignature` after calling this function.
func UpdateTxWithSignature(rawTxBytes []byte, vin int, signature []byte) ([]byte, error) {
	tx, err := TxFromRawTxBytes(rawTxBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse tx: %v", err)
	}
	tx.TxIn[vin].Witness = wire.TxWitness{signature}
	var buf bytes.Buffer
	err = tx.Serialize(&buf)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize tx: %v", err)
	}
	return buf.Bytes(), nil
}

// VerifySignatureSingleInput verifies that a signed transaction's input
// properly spends the prevOutput provided.
func VerifySignatureSingleInput(signedTx *wire.MsgTx, vin int, prevOutput *wire.TxOut) error {
	prevOutputFetcher := txscript.NewCannedPrevOutputFetcher(
		prevOutput.PkScript, prevOutput.Value,
	)
	hashCache := txscript.NewTxSigHashes(signedTx, prevOutputFetcher)
	// We skip erroring on witness version because btcd is behind bitcoin core on v3 transactions
	verifyFlags := txscript.StandardVerifyFlags & ^txscript.ScriptVerifyDiscourageUpgradeableWitnessProgram
	vm, err := txscript.NewEngine(prevOutput.PkScript, signedTx, vin, verifyFlags,
		nil, hashCache, prevOutput.Value, prevOutputFetcher)
	if err != nil {
		return err
	}
	if err := vm.Execute(); err != nil {
		return err
	}
	return nil
}

func VerifySignatureMultiInput(signedTx *wire.MsgTx, prevOutputFetcher txscript.PrevOutputFetcher) error {
	hashCache := txscript.NewTxSigHashes(signedTx, prevOutputFetcher)
	for vin, txIn := range signedTx.TxIn {
		txOut := prevOutputFetcher.FetchPrevOutput(txIn.PreviousOutPoint)
		// We skip erroring on witness version because btcd is behind bitcoin core on v3 transactions
		verifyFlags := txscript.StandardVerifyFlags & ^txscript.ScriptVerifyDiscourageUpgradeableWitnessProgram & ^txscript.ScriptVerifyCleanStack
		vm, err := txscript.NewEngine(txOut.PkScript, signedTx, vin, verifyFlags,
			nil, hashCache, txOut.Value, prevOutputFetcher)
		if err != nil {
			return err
		}
		// We allow witness version errors because btcd is behind bitcoin core on v3 transactions
		if err := vm.Execute(); err != nil {
			return fmt.Errorf("failed to verify signature on input %d: %v", vin, err)
		}
	}
	return nil
}

// NetworkFromTokenTransaction extracts the Network from a TokenTransaction.
// It determines the network by examining the transaction's network field.
func NetworkFromTokenTransaction(tx *pb.TokenTransaction) (Network, error) {
	if tx == nil {
		return Unspecified, fmt.Errorf("token transaction cannot be nil")
	}

	return NetworkFromProtoNetwork(tx.Network)
}
