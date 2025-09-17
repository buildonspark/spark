package grpctest

import (
	"crypto/sha256"
	"testing"

	"github.com/btcsuite/btcd/rpcclient"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightsparkdev/spark/common"
	bitcointransaction "github.com/lightsparkdev/spark/common/bitcoin_transaction"
	"github.com/lightsparkdev/spark/common/keys"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/require"
)

func createNodeTx(t *testing.T, coin *sparktesting.FaucetCoin, amount int64) (*wire.MsgTx, keys.Private) {
	signingKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err)

	publicKey := signingKey.Public()

	taprootAddr, err := common.P2TRRawAddressFromPublicKey(publicKey, common.Regtest)
	require.NoError(t, err)

	// Create proper P2TR pkScript for the output
	taprootPkScript, err := txscript.PayToAddrScript(taprootAddr)
	require.NoError(t, err)

	nodeTx := wire.NewMsgTx(3)
	nodeTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: *coin.OutPoint,
		SignatureScript:  nil,
		Witness:          nil,
	})
	nodeTx.AddTxOut(wire.NewTxOut(amount, taprootPkScript))
	return nodeTx, signingKey
}

func TestBroadcastAndSenderSpendHTLCTransaction(t *testing.T) {
	client := sparktesting.GetBitcoinClient()

	// Use a 32-byte preimage (more standard)
	preimage := make([]byte, 32)
	preimage[0] = 0x11
	paymentHash := sha256.Sum256(preimage)

	htlcTx, receiverKey, senderKey, minerKey := setupHTLCTransaction(t, preimage, client)
	minderAddress, err := common.P2TRRawAddressFromPublicKey(minerKey.Public(), common.Regtest)
	require.NoError(t, err)
	receiverPubKey := receiverKey.Public()
	// Broadcast HTLC transaction
	_, err = client.SendRawTransaction(htlcTx, true)
	require.NoError(t, err)

	// Mine block
	_, err = client.GenerateToAddress(1, minderAddress, nil)
	require.NoError(t, err)

	// Wait for sequence lock to expire (need 5 more blocks since sequence is 5)
	_, err = client.GenerateToAddress(5, minderAddress, nil)
	require.NoError(t, err)

	senderSpendTx := createSenderSpendTx(t, htlcTx, senderKey, 5, paymentHash[:], receiverPubKey)
	// Broadcast sender spend transaction
	_, err = client.SendRawTransaction(senderSpendTx, true)
	require.NoError(t, err)
}

func TestBroadcastAndSenderSpendHTLCTransactionWithinSequenceLock(t *testing.T) {
	client := sparktesting.GetBitcoinClient()

	// Use a 32-byte preimage (more standard)
	preimage := make([]byte, 32)
	preimage[0] = 0x11
	paymentHash := sha256.Sum256(preimage)

	htlcTx, receiverKey, senderKey, minerKey := setupHTLCTransaction(t, preimage, client)
	minderAddress, err := common.P2TRRawAddressFromPublicKey(minerKey.Public(), common.Regtest)
	require.NoError(t, err)
	receiverPubKey := receiverKey.Public()
	// Broadcast HTLC transaction
	_, err = client.SendRawTransaction(htlcTx, true)
	require.NoError(t, err)

	// Mine block
	_, err = client.GenerateToAddress(1, minderAddress, nil)
	require.NoError(t, err)

	lessTimeSenderTx := createSenderSpendTx(t, htlcTx, senderKey, 5, paymentHash[:], receiverPubKey)
	// Broadcast sender spend transaction
	_, err = client.SendRawTransaction(lessTimeSenderTx, true)
	require.Error(t, err)

	// Wait for sequence lock to expire (need 5 more blocks since sequence is 5)
	_, err = client.GenerateToAddress(5, minderAddress, nil)
	require.NoError(t, err)

	lessSequenceSenderTx := createSenderSpendTx(t, htlcTx, senderKey, 3, paymentHash[:], receiverPubKey)
	// Broadcast sender spend transaction
	_, err = client.SendRawTransaction(lessSequenceSenderTx, true)
	require.Error(t, err)
}

func TestBroadcastAndReceiverSpendHTLCTransaction(t *testing.T) {
	client := sparktesting.GetBitcoinClient()

	// Use a 32-byte preimage (more standard)
	preimage := make([]byte, 32)
	preimage[0] = 0x11
	paymentHash := sha256.Sum256(preimage)

	htlcTx, receiverKey, senderKey, minerKey := setupHTLCTransaction(t, preimage, client)
	minderAddress, err := common.P2TRRawAddressFromPublicKey(minerKey.Public(), common.Regtest)
	require.NoError(t, err)
	senderPubKey := senderKey.Public()

	// Broadcast HTLC transaction
	_, err = client.SendRawTransaction(htlcTx, true)
	require.NoError(t, err)

	// Mine block
	_, err = client.GenerateToAddress(1, minderAddress, nil)
	require.NoError(t, err)

	receiverSpendTxWithWrongPreimage := createReceiverSpendTx(t, htlcTx, receiverKey, paymentHash[:], senderPubKey, []byte{0x11})
	// Broadcast receiver spend transaction
	_, err = client.SendRawTransaction(receiverSpendTxWithWrongPreimage, true)
	require.Error(t, err)

	receiverSpendTx := createReceiverSpendTx(t, htlcTx, receiverKey, paymentHash[:], senderPubKey, preimage)
	// Broadcast receiver spend transaction
	_, err = client.SendRawTransaction(receiverSpendTx, true)
	require.NoError(t, err)
}

func setupHTLCTransaction(t *testing.T, preimage []byte, client *rpcclient.Client) (*wire.MsgTx, keys.Private, keys.Private, keys.Private) {
	minerKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err)

	minderAddress, err := common.P2TRRawAddressFromPublicKey(minerKey.Public(), common.Regtest)
	require.NoError(t, err)

	coin, err := faucet.Fund()
	require.NoError(t, err)

	// Create and broadcast node tx
	nodeTx, nodeSigningKey := createNodeTx(t, &coin, 100000)

	signedNodeTx, err := sparktesting.SignFaucetCoin(nodeTx, coin.TxOut, coin.Key)
	require.NoError(t, err)

	_, err = client.SendRawTransaction(signedNodeTx, true)
	require.NoError(t, err)

	// Mine block
	_, err = client.GenerateToAddress(1, minderAddress, nil)
	require.NoError(t, err)

	// Setup HTLC transaction
	receiverKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err)
	receiverPubKey := receiverKey.Public()

	senderKey, err := keys.GeneratePrivateKey()
	require.NoError(t, err)
	senderPubKey := senderKey.Public()

	paymentHash := sha256.Sum256(preimage)

	htlcTx, err := bitcointransaction.CreateLightningHTLCTransactionWithSequence(signedNodeTx, 0, common.Regtest, 0, 5, paymentHash[:], receiverPubKey, senderPubKey, true)
	require.NoError(t, err)

	prevOutputFetcher := txscript.NewCannedPrevOutputFetcher(
		signedNodeTx.TxOut[0].PkScript, signedNodeTx.TxOut[0].Value,
	)
	sighashes := txscript.NewTxSigHashes(htlcTx, prevOutputFetcher)
	fakeTapscriptRootHash := []byte{}
	sig, err := txscript.RawTxInTaprootSignature(
		htlcTx, sighashes, 0, signedNodeTx.TxOut[0].Value, signedNodeTx.TxOut[0].PkScript,
		fakeTapscriptRootHash, txscript.SigHashDefault, nodeSigningKey.ToBTCEC(),
	)
	require.NoError(t, err)

	htlcTx.TxIn[0].Witness = wire.TxWitness{sig}

	return htlcTx, receiverKey, senderKey, minerKey
}

func createSenderSpendTx(t *testing.T, htlcTx *wire.MsgTx, senderKey keys.Private, sequence uint32, paymentHash []byte, receiverPubKey keys.Public) *wire.MsgTx {
	senderPubKey := senderKey.Public()

	// Create transaction to spend HTLC via sender path (sequence lock)
	senderSpendTx := wire.NewMsgTx(3)
	htlcOutPoint := wire.OutPoint{
		Hash:  htlcTx.TxHash(),
		Index: 0, // First output is the HTLC output
	}
	senderSpendTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: htlcOutPoint,
		SignatureScript:  nil,
		Witness:          nil,
		Sequence:         sequence, // Must match the sequence used in HTLC creation
	})

	// Send funds back to sender
	senderAddr, err := common.P2TRRawAddressFromPublicKey(senderPubKey, common.Regtest)
	require.NoError(t, err)
	senderPkScript, err := txscript.PayToAddrScript(senderAddr)
	require.NoError(t, err)

	// Subtract fee from output
	outputAmount := common.MaybeApplyFee(htlcTx.TxOut[0].Value)
	senderSpendTx.AddTxOut(wire.NewTxOut(outputAmount, senderPkScript))

	// Create the sequence lock script for signing
	sequenceLockScript, err := bitcointransaction.CreateSequencLockScript(5, senderPubKey)
	require.NoError(t, err)
	sequenceLockLeaf := txscript.NewBaseTapLeaf(sequenceLockScript)

	// Create hash lock script (needed for taproot tree)
	hashLockScript, err := bitcointransaction.CreateHashLockScript(paymentHash[:], receiverPubKey)
	require.NoError(t, err)
	hashLockLeaf := txscript.NewBaseTapLeaf(hashLockScript)

	// Build the taproot tree (same as in HTLC creation)
	tapTree := txscript.AssembleTaprootScriptTree(hashLockLeaf, sequenceLockLeaf)

	// Sign using the sequence lock path
	prevOutputFetcher2 := txscript.NewCannedPrevOutputFetcher(
		htlcTx.TxOut[0].PkScript, htlcTx.TxOut[0].Value,
	)
	sighashes2 := txscript.NewTxSigHashes(senderSpendTx, prevOutputFetcher2)

	sig2, err := txscript.RawTxInTapscriptSignature(
		senderSpendTx, sighashes2, 0, htlcTx.TxOut[0].Value,
		htlcTx.TxOut[0].PkScript, sequenceLockLeaf,
		txscript.SigHashDefault, senderKey.ToBTCEC(),
	)
	require.NoError(t, err)

	// Set witness: signature + script + control block
	controlBlock := tapTree.LeafMerkleProofs[1].ToControlBlock(bitcointransaction.NUMSPoint().ToBTCEC())
	controlBlockBytes, err := controlBlock.ToBytes()
	require.NoError(t, err)

	senderSpendTx.TxIn[0].Witness = wire.TxWitness{
		sig2,
		sequenceLockScript,
		controlBlockBytes,
	}
	return senderSpendTx
}

func createReceiverSpendTx(t *testing.T, htlcTx *wire.MsgTx, receiverKey keys.Private, paymentHash []byte, senderPubKey keys.Public, preimage []byte) *wire.MsgTx {
	receiverPubKey := receiverKey.Public()
	// Create transaction to spend HTLC via receiver path (hash lock)
	receiverSpendTx := wire.NewMsgTx(3)
	htlcOutPoint := wire.OutPoint{
		Hash:  htlcTx.TxHash(),
		Index: 0, // First output is the HTLC output
	}
	receiverSpendTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: htlcOutPoint,
		SignatureScript:  nil,
		Witness:          nil,
		Sequence:         wire.MaxTxInSequenceNum, // No sequence lock needed for hash path
	})

	// Send funds to receiver
	receiverAddr, err := common.P2TRRawAddressFromPublicKey(receiverPubKey, common.Regtest)
	require.NoError(t, err)
	receiverPkScript, err := txscript.PayToAddrScript(receiverAddr)
	require.NoError(t, err)

	// Subtract fee from output
	outputAmount := common.MaybeApplyFee(htlcTx.TxOut[0].Value)
	receiverSpendTx.AddTxOut(wire.NewTxOut(outputAmount, receiverPkScript))

	// Create the hash lock script for signing
	hashLockScript, err := bitcointransaction.CreateHashLockScript(paymentHash, receiverPubKey)
	require.NoError(t, err)
	hashLockLeaf := txscript.NewBaseTapLeaf(hashLockScript)

	// Create sequence lock script (needed for taproot tree) - must match HTLC creation
	sequenceLockScript, err := bitcointransaction.CreateSequencLockScript(5, senderPubKey)
	require.NoError(t, err)
	sequenceLockLeaf := txscript.NewBaseTapLeaf(sequenceLockScript)

	// Build the taproot tree (same as in HTLC creation)
	tapTree := txscript.AssembleTaprootScriptTree(hashLockLeaf, sequenceLockLeaf)

	// Sign using the hash lock path
	prevOutputFetcher3 := txscript.NewCannedPrevOutputFetcher(
		htlcTx.TxOut[0].PkScript, htlcTx.TxOut[0].Value,
	)
	sighashes3 := txscript.NewTxSigHashes(receiverSpendTx, prevOutputFetcher3)

	sig3, err := txscript.RawTxInTapscriptSignature(
		receiverSpendTx, sighashes3, 0, htlcTx.TxOut[0].Value,
		htlcTx.TxOut[0].PkScript, hashLockLeaf,
		txscript.SigHashDefault, receiverKey.ToBTCEC(),
	)
	require.NoError(t, err)

	// Set witness: preimage + signature + script + control block
	controlBlock := tapTree.LeafMerkleProofs[0].ToControlBlock(bitcointransaction.NUMSPoint().ToBTCEC())
	controlBlockBytes, err := controlBlock.ToBytes()
	require.NoError(t, err)

	receiverSpendTx.TxIn[0].Witness = wire.TxWitness{
		sig3,              // Receiver's signature (for OP_CHECKSIG) - bottom of stack
		preimage,          // Preimage that hashes to paymentHash (for OP_SHA256) - top of stack
		hashLockScript,    // Hash lock script
		controlBlockBytes, // Control block for hash lock leaf (index 0)
	}

	return receiverSpendTx
}
