package common

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"

	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
)

// TODO: replace all other code to use this function to create the ephemeral anchor output.
func EphemeralAnchorOutput() *wire.TxOut {
	return wire.NewTxOut(0, []byte{txscript.OP_TRUE, 0x02, 0x4e, 0x73})
}

func MaybeApplyFee(amount int64) int64 {
	if amount > int64(DefaultFeeSats) {
		return amount - int64(DefaultFeeSats)
	}
	return amount
}

const (
	// Estimated transaction size in bytes for fee calculation
	estimatedTxSize = 191
	// Default fee rate in satoshis per vbyte
	defaultSatsPerVbyte = 5
	// DefaultFeeSats is the default fee in satoshis (estimatedTxSize * defaultSatsPerVbyte)
	DefaultFeeSats = estimatedTxSize * defaultSatsPerVbyte
)

// P2TRScriptFromPubKey returns a P2TR script from a public key.
func P2TRScriptFromPubKey(pubKey keys.Public) ([]byte, error) {
	taprootKey := txscript.ComputeTaprootKeyNoScript(pubKey.ToBTCEC())
	return txscript.PayToTaprootScript(taprootKey)
}

func P2TRRawAddressFromPublicKey(pubKey keys.Public, network btcnetwork.Network) (btcutil.Address, error) {
	// Tweak the internal key with empty merkle root
	taprootKey := txscript.ComputeTaprootKeyNoScript(pubKey.ToBTCEC())
	return btcutil.NewAddressTaproot(
		// Convert a 33 byte public key to a 32 byte x-only public key
		schnorr.SerializePubKey(taprootKey),
		network.Params(),
	)
}

// P2TRAddressFromPublicKey returns a P2TR address from a public key.
func P2TRAddressFromPublicKey(pubKey keys.Public, network btcnetwork.Network) (string, error) {
	addrRaw, err := P2TRRawAddressFromPublicKey(pubKey, network)
	if err != nil {
		return "", err
	}
	return addrRaw.EncodeAddress(), nil
}

// P2TRAddressFromPkScript returns a P2TR address from a public script.
func P2TRAddressFromPkScript(pkScript []byte, network btcnetwork.Network) (*string, error) {
	parsedScript, err := txscript.ParsePkScript(pkScript)
	if err != nil {
		return nil, err
	}

	networkParams := network.Params()
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

// MaxTxSize is the maximum allowed transaction size in bytes.
// This prevents memory exhaustion attacks from malicious transactions that claim
// huge input/output counts. Set to 400KB which is well above the standard
// transaction size limit (100KB) but provides a reasonable safety margin.
const MaxTxSize = 400_000

// MaxTxInputs is the maximum number of inputs allowed in a transaction.
// This is a sanity check to prevent memory exhaustion from malformed transactions.
const MaxTxInputs = 10000

// MaxTxOutputs is the maximum number of outputs allowed in a transaction.
// This is a sanity check to prevent memory exhaustion from malformed transactions.
const MaxTxOutputs = 10000

// TxFromRawTxBytes returns a btcd MsgTx from a raw tx bytes.
func TxFromRawTxBytes(rawTxBytes []byte) (*wire.MsgTx, error) {
	// Validate transaction size to prevent memory exhaustion attacks.
	// Malicious transactions can claim huge input/output counts via VarInts,
	// causing btcd to pre-allocate massive slices before discovering the data is truncated.
	if len(rawTxBytes) > MaxTxSize {
		return nil, fmt.Errorf("transaction size %d exceeds maximum allowed size %d", len(rawTxBytes), MaxTxSize)
	}

	// Pre-validate the transaction structure to prevent memory exhaustion.
	// This checks that claimed input/output counts are reasonable before btcd allocates memory.
	if err := validateTxStructure(rawTxBytes); err != nil {
		return nil, fmt.Errorf("invalid transaction structure: %w", err)
	}

	var tx wire.MsgTx
	err := tx.Deserialize(bytes.NewReader(rawTxBytes))
	if err != nil {
		return nil, err
	}
	return &tx, nil
}

// validateTxStructure performs a lightweight pre-validation of the transaction
// to ensure claimed input/output counts are reasonable before btcd allocates memory.
func validateTxStructure(rawTxBytes []byte) error {
	if len(rawTxBytes) < 10 {
		return fmt.Errorf("transaction too short: %d bytes", len(rawTxBytes))
	}

	// Skip version (4 bytes)
	offset := 4

	// Check for segwit marker and flag
	if rawTxBytes[offset] == 0x00 && rawTxBytes[offset+1] == 0x01 {
		offset += 2
	}

	// Read input count
	inputCount, bytesRead := readVarInt(rawTxBytes[offset:])
	if bytesRead == 0 {
		return fmt.Errorf("failed to read input count")
	}
	if inputCount > MaxTxInputs {
		return fmt.Errorf("input count %d exceeds maximum %d", inputCount, MaxTxInputs)
	}
	offset += bytesRead

	// Skip inputs - we just need to get past them to read output count
	// Each input is at minimum 41 bytes (32 prevout hash + 4 index + 1 script len + 4 sequence)
	minInputSize := 41
	for range inputCount {
		if offset+minInputSize > len(rawTxBytes) {
			return fmt.Errorf("transaction truncated while reading inputs")
		}
		// Skip prevout (36 bytes)
		offset += 36
		// Read script length and skip script
		scriptLen, bytesReadLoop := readVarInt(rawTxBytes[offset:])
		if bytesReadLoop == 0 {
			return fmt.Errorf("failed to read input script length")
		}
		offset += bytesReadLoop + int(scriptLen)
		// Skip sequence (4 bytes)
		offset += 4
		if offset > len(rawTxBytes) {
			return fmt.Errorf("transaction truncated while reading inputs")
		}
	}

	// Read output count
	if offset >= len(rawTxBytes) {
		return fmt.Errorf("transaction truncated before output count")
	}
	outputCount, bytesRead := readVarInt(rawTxBytes[offset:])
	if bytesRead == 0 {
		return fmt.Errorf("failed to read output count")
	}
	if outputCount > MaxTxOutputs {
		return fmt.Errorf("output count %d exceeds maximum %d", outputCount, MaxTxOutputs)
	}
	return nil
}

// readVarInt reads a variable length integer from the byte slice.
// Returns the value and number of bytes read, or 0 bytes read on error.
func readVarInt(buf []byte) (uint64, int) {
	if len(buf) == 0 {
		return 0, 0
	}

	switch discriminant := buf[0]; discriminant {
	case 0xFD:
		if len(buf) < 3 {
			return 0, 0
		}
		return uint64(binary.LittleEndian.Uint16(buf[1:])), 3
	case 0xFE:
		if len(buf) < 5 {
			return 0, 0
		}
		return uint64(binary.LittleEndian.Uint32(buf[1:])), 5
	case 0xFF:
		if len(buf) < 9 {
			return 0, 0
		}
		return binary.LittleEndian.Uint64(buf[1:]), 9
	default:
		return uint64(discriminant), 1
	}
}

// ValidateBitcoinTxVersion validates that a Bitcoin transaction has a valid version (>= 2).
func ValidateBitcoinTxVersion(tx *wire.MsgTx) error {
	if tx.Version < 2 {
		return fmt.Errorf("transaction version must be greater than or equal to 2, got v%d", tx.Version)
	}
	return nil
}

func SerializeTx(tx *wire.MsgTx) ([]byte, error) {
	buf := bytes.NewBuffer(make([]byte, 0, tx.SerializeSize()))
	if err := tx.Serialize(buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func SerializeTxHex(tx *wire.MsgTx) (string, error) {
	txBytes, err := SerializeTx(tx)
	if err != nil {
		return "", err
	}

	return hex.EncodeToString(txBytes), nil
}

func SerializeTxNoWitness(tx *wire.MsgTx) ([]byte, error) {
	buf := bytes.NewBuffer(make([]byte, 0, tx.SerializeSizeStripped()))
	if err := tx.SerializeNoWitness(buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func SerializeTxNoWitnessHex(tx *wire.MsgTx) (string, error) {
	txBytes, err := SerializeTxNoWitness(tx)
	if err != nil {
		return "", err
	}

	return hex.EncodeToString(txBytes), nil
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

// UpdateTxWithSignature applies the signature to the transaction.
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
	if err := ValidateBitcoinTxVersion(signedTx); err != nil {
		return fmt.Errorf("transaction version validation failed: %w", err)
	}

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
	if !pubKey.Verify(sig, messageHash) {
		return fmt.Errorf("invalid signature")
	}

	return nil
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
