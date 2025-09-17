package bitcointransaction

import (
	"encoding/hex"

	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
)

var NUMSPoint = func() keys.Public {
	// Taking from bip341, it's the x value public key of the hash of generator point G on secp256k1 curve.
	numsBytes, _ := hex.DecodeString("0250929b74c1a04954b78b4b6035e97a5e078a5a0f28ec96d547bfee9ace803ac0")
	numsKey, _ := keys.ParsePublicKey(numsBytes)
	return numsKey
}

var LightningHTLCSequence = uint32(2160)

func CreateLightningHTLCTransaction(nodeTx *wire.MsgTx, vout uint32, network common.Network, transactionSequence uint32, hash []byte, hashLockDestinationPubkey keys.Public, sequenceLockDestinationPubkey keys.Public) (*wire.MsgTx, error) {
	return CreateLightningHTLCTransactionWithSequence(nodeTx, vout, network, transactionSequence, LightningHTLCSequence, hash, hashLockDestinationPubkey, sequenceLockDestinationPubkey, false)
}

func CreateDirectLightningHTLCTransaction(nodeTx *wire.MsgTx, vout uint32, network common.Network, transactionSequence uint32, hash []byte, hashLockDestinationPubkey keys.Public, sequenceLockDestinationPubkey keys.Public) (*wire.MsgTx, error) {
	return CreateLightningHTLCTransactionWithSequence(nodeTx, vout, network, transactionSequence, LightningHTLCSequence, hash, hashLockDestinationPubkey, sequenceLockDestinationPubkey, true)
}

func CreateLightningHTLCTransactionWithSequence(nodeTx *wire.MsgTx, vout uint32, network common.Network, transactionSequence uint32, timelockSequence uint32, hash []byte, hashLockDestinationPubkey keys.Public, sequenceLockDestinationPubkey keys.Public, includeFee bool) (*wire.MsgTx, error) {
	htlcTransaction := wire.NewMsgTx(3)
	previousOutpoint := wire.OutPoint{
		Hash:  nodeTx.TxHash(),
		Index: vout,
	}
	htlcTransaction.AddTxIn(&wire.TxIn{
		PreviousOutPoint: previousOutpoint,
		SignatureScript:  nil,
		Witness:          nil,
		Sequence:         transactionSequence,
	})

	value := nodeTx.TxOut[vout].Value
	if includeFee {
		value = common.MaybeApplyFee(value)
	}

	taprootAddr, err := CreateLightningHTLCTaprootAddressWithSequence(network, hash, hashLockDestinationPubkey, timelockSequence, sequenceLockDestinationPubkey)
	if err != nil {
		return nil, err
	}
	// Build a standard P2TR pkScript for the HTLC output instead of using ScriptAddress directly
	htlcPkScript, err := txscript.PayToAddrScript(taprootAddr)
	if err != nil {
		return nil, err
	}
	htlcTransaction.AddTxOut(wire.NewTxOut(value, htlcPkScript))
	if !includeFee {
		htlcTransaction.AddTxOut(common.EphemeralAnchorOutput())
	}

	return htlcTransaction, nil
}

func CreateLightningHTLCTaprootAddressWithSequence(network common.Network, hash []byte, hashLockDestinationPubkey keys.Public, sequence uint32, sequenceLockDestinationPubkey keys.Public) (btcutil.Address, error) {
	return createHTLCTaprootAddress(network, hash, hashLockDestinationPubkey, sequence, sequenceLockDestinationPubkey)
}

func createHTLCTaprootAddress(network common.Network, hash []byte, hashLockDestinationPubkey keys.Public, sequence uint32, sequenceLockDestinationPubkey keys.Public) (btcutil.Address, error) {
	numsKey := NUMSPoint()

	hashLockScript, err := CreateHashLockScript(hash, hashLockDestinationPubkey)
	if err != nil {
		return nil, err
	}
	hashLockLeaf := txscript.NewBaseTapLeaf(hashLockScript)

	sequenceLockScript, err := CreateSequencLockScript(sequence, sequenceLockDestinationPubkey)
	if err != nil {
		return nil, err
	}
	sequenceLockLeaf := txscript.NewBaseTapLeaf(sequenceLockScript)

	tapTree := txscript.AssembleTaprootScriptTree(hashLockLeaf, sequenceLockLeaf)
	tapRoot := tapTree.RootNode.TapHash()

	// Compute the taproot key
	taprootKey := txscript.ComputeTaprootOutputKey(numsKey.ToBTCEC(), tapRoot[:])

	taprootAddr, err := btcutil.NewAddressTaproot(
		schnorr.SerializePubKey(taprootKey),
		common.NetworkParams(network),
	)
	if err != nil {
		return nil, err
	}
	return taprootAddr, nil
}

func CreateHashLockScript(hash []byte, destinationPubkey keys.Public) (script []byte, err error) {
	builder := txscript.NewScriptBuilder()
	builder.AddOp(txscript.OP_SHA256)
	builder.AddData(hash)
	builder.AddOp(txscript.OP_EQUALVERIFY)
	builder.AddData(destinationPubkey.SerializeXOnly())
	builder.AddOp(txscript.OP_CHECKSIG)
	return builder.Script()
}

func CreateSequencLockScript(sequence uint32, destinationPubkey keys.Public) (script []byte, err error) {
	builder := txscript.NewScriptBuilder()
	builder.AddInt64(int64(sequence))
	builder.AddOp(txscript.OP_CHECKSEQUENCEVERIFY)
	builder.AddOp(txscript.OP_DROP)
	builder.AddData(destinationPubkey.SerializeXOnly())
	builder.AddOp(txscript.OP_CHECKSIG)
	return builder.Script()
}
