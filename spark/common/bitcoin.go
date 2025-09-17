package common

import (
	"bytes"
	"encoding/hex"
	"fmt"

	"github.com/lightsparkdev/spark/common/keys"

	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	pb "github.com/lightsparkdev/spark/proto/spark"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
)

// Network is the type for Bitcoin networks used with the operator.
type Network int

// TODO: replace all other code to use this function to create the ephemeral anchor output.
func EphemeralAnchorOutput() *wire.TxOut {
	return wire.NewTxOut(0, []byte{txscript.OP_TRUE, 0x02, 0x4e, 0x73})
}

func DetermineNetwork(protoNetwork pb.Network) (*Network, error) {
	var network Network
	if protoNetwork == pb.Network_UNSPECIFIED {
		network = Mainnet
	} else {
		var err error
		network, err = NetworkFromProtoNetwork(protoNetwork)
		if err != nil {
			return nil, fmt.Errorf("failed to convert proto network to common network: %w", err)
		}
	}
	return &network, nil
}

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

const (
	// Estimated transaction size in bytes for fee calculation
	estimatedTxSize = uint64(191)
	// Default fee rate in satoshis per vbyte
	defaultSatsPerVbyte = uint64(5)
	// Default fee in satoshis (estimatedTxSize * defaultSatsPerVbyte)
	DefaultFeeSats = estimatedTxSize * defaultSatsPerVbyte
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

func NetworkFromSchemaNetwork(schemaNetwork st.Network) (Network, error) {
	switch schemaNetwork {
	case st.NetworkMainnet:
		return Mainnet, nil
	case st.NetworkRegtest:
		return Regtest, nil
	case st.NetworkTestnet:
		return Testnet, nil
	case st.NetworkSignet:
		return Signet, nil
	case st.NetworkUnspecified:
		return Unspecified, fmt.Errorf("invalid network")
	default:
		return Unspecified, fmt.Errorf("invalid network")
	}
}

func ProtoNetworkFromSchemaNetwork(schemaNetwork st.Network) (pb.Network, error) {
	switch schemaNetwork {
	case st.NetworkMainnet:
		return pb.Network_MAINNET, nil
	case st.NetworkRegtest:
		return pb.Network_REGTEST, nil
	case st.NetworkTestnet:
		return pb.Network_TESTNET, nil
	case st.NetworkSignet:
		return pb.Network_SIGNET, nil
	default:
		return pb.Network_MAINNET, fmt.Errorf("invalid network")
	}
}

func SchemaNetworkFromNetwork(network Network) (st.Network, error) {
	switch network {
	case Mainnet:
		return st.NetworkMainnet, nil
	case Regtest:
		return st.NetworkRegtest, nil
	case Testnet:
		return st.NetworkTestnet, nil
	case Signet:
		return st.NetworkSignet, nil
	default:
		return st.NetworkUnspecified, fmt.Errorf("invalid network")
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

// BitcoinNetworkIdentifier returns the standardized bitcoin network identifier.
func BitcoinNetworkIdentifierFromNetwork(network Network) (uint32, error) {
	params := NetworkParams(network)
	return uint32(params.Net), nil
}

func SchemaNetworkFromProtoNetwork(protoNetwork pb.Network) (st.Network, error) {
	switch protoNetwork {
	case pb.Network_MAINNET:
		return st.NetworkMainnet, nil
	case pb.Network_REGTEST:
		return st.NetworkRegtest, nil
	case pb.Network_TESTNET:
		return st.NetworkTestnet, nil
	case pb.Network_SIGNET:
		return st.NetworkSignet, nil
	default:
		return st.NetworkUnspecified, fmt.Errorf("invalid network")
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

func SchemaNetwork(network Network) st.Network {
	switch network {
	case Mainnet:
		return st.NetworkMainnet
	case Regtest:
		return st.NetworkRegtest
	case Testnet:
		return st.NetworkTestnet
	default:
		return st.NetworkMainnet
	}
}

// P2TRScriptFromPubKey returns a P2TR script from a public key.
func P2TRScriptFromPubKey(pubKey keys.Public) ([]byte, error) {
	taprootKey := txscript.ComputeTaprootKeyNoScript(pubKey.ToBTCEC())
	return txscript.PayToTaprootScript(taprootKey)
}

func P2TRRawAddressFromPublicKey(pubKey keys.Public, network Network) (btcutil.Address, error) {
	// Tweak the internal key with empty merkle root
	taprootKey := txscript.ComputeTaprootKeyNoScript(pubKey.ToBTCEC())
	return btcutil.NewAddressTaproot(
		// Convert a 33 byte public key to a 32 byte x-only public key
		schnorr.SerializePubKey(taprootKey),
		NetworkParams(network),
	)
}

// P2TRAddressFromPublicKey returns a P2TR address from a public key.
func P2TRAddressFromPublicKey(pubKey keys.Public, network Network) (string, error) {
	addrRaw, err := P2TRRawAddressFromPublicKey(pubKey, network)
	if err != nil {
		return "", err
	}
	return addrRaw.EncodeAddress(), nil
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

func SigHashFromMultiPrevOutTx(tx *wire.MsgTx, inputIndex int, prevOutputs map[wire.OutPoint]*wire.TxOut) ([]byte, error) {
	prevOutFetcher := txscript.NewMultiPrevOutFetcher(prevOutputs)
	sighashes := txscript.NewTxSigHashes(tx, prevOutFetcher)

	sigHash, err := txscript.CalcTaprootSignatureHash(sighashes, txscript.SigHashDefault, tx, inputIndex, prevOutFetcher)
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
		return nil, fmt.Errorf("failed to parse tx: %w", err)
	}

	if len(tx.TxIn) <= vin || vin < 0 {
		return nil, fmt.Errorf("invalid input index %d for tx with %d inputs", vin, len(tx.TxIn))
	}
	tx.TxIn[vin].Witness = wire.TxWitness{signature}
	var buf bytes.Buffer
	err = tx.Serialize(&buf)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize tx: %w", err)
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
			return fmt.Errorf("failed to verify signature on input %d: %w", vin, err)
		}
	}
	return nil
}

// VerifyECDSASignature verifies an ECDSA signature with comprehensive validation
// including empty input checks and canonical encoding validation to prevent malleability attacks.
func VerifyECDSASignature(pubKey keys.Public, signatureBytes []byte, messageHash []byte) error {
	if len(signatureBytes) == 0 {
		return fmt.Errorf("signature cannot be empty")
	}
	if len(messageHash) == 0 {
		return fmt.Errorf("message hash cannot be empty")
	}

	// Parse the signature - strict DER parsing prevents many malleability issues
	sig, err := ecdsa.ParseDERSignature(signatureBytes)
	if err != nil {
		return fmt.Errorf("invalid signature format: malformed DER signature: %w", err)
	}

	// Additional validation: ensure signature encoding is minimal (no extra padding)
	// This prevents signature malleability attacks through non-canonical encoding
	reencoded := sig.Serialize()
	if len(signatureBytes) != len(reencoded) {
		return fmt.Errorf("signature encoding is not minimal")
	}
	for i, b := range signatureBytes {
		if b != reencoded[i] {
			return fmt.Errorf("signature encoding is not canonical")
		}
	}

	// Verify the signature
	if !sig.Verify(messageHash, pubKey.ToBTCEC()) {
		return fmt.Errorf("invalid signature")
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

// CompareTransactions compares two Bitcoin transactions for structural equality.
// It checks version, locktime, inputs (sequence and previous outpoints), and outputs (value and pkScript).
// This function is useful for validating that user-provided transactions match expected structure.
func CompareTransactions(txA, txB *wire.MsgTx) error {
	if txA.Version != txB.Version {
		return fmt.Errorf("expected version %d, got %d", txA.Version, txB.Version)
	}
	if txA.LockTime != txB.LockTime {
		return fmt.Errorf("expected locktime %d, got %d", txA.LockTime, txB.LockTime)
	}
	if len(txA.TxIn) != len(txB.TxIn) {
		return fmt.Errorf("expected %d inputs, got %d", len(txA.TxIn), len(txB.TxIn))
	}
	for i, txInA := range txA.TxIn {
		txInB := txB.TxIn[i]
		if txInA.Sequence != txInB.Sequence {
			return fmt.Errorf("expected sequence %d on input %d, got %d", txInA.Sequence, i, txInB.Sequence)
		}
		if txInA.PreviousOutPoint != txInB.PreviousOutPoint {
			return fmt.Errorf("expected previous outpoint %s on input %d, got %s", txInA.PreviousOutPoint.String(), i, txInB.PreviousOutPoint.String())
		}
	}
	if len(txA.TxOut) != len(txB.TxOut) {
		return fmt.Errorf("expected %d outputs, got %d", len(txA.TxOut), len(txB.TxOut))
	}
	for i, txOutA := range txA.TxOut {
		txOutB := txB.TxOut[i]
		if txOutA.Value != txOutB.Value {
			return fmt.Errorf("expected value %d on output %d, got %d", txOutA.Value, i, txOutB.Value)
		}
		if !bytes.Equal(txOutA.PkScript, txOutB.PkScript) {
			return fmt.Errorf("expected pkscript %x on output %d, got %x", txOutA.PkScript, i, txOutB.PkScript)
		}
	}
	return nil
}
