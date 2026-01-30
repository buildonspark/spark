package tokens

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/rpcclient"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"

	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/watchtower"
)

const (
	justiceEstimatedTxSize = 99 // estimated transaction size in vbytes for justice tx
	justiceDefaultFeeSats  = justiceEstimatedTxSize * common.DefaultSatsPerVbyte
	dustThresholdSats      = 546 // minimum output value to avoid dust rejection
)

// JusticeInputWithBond contains everything needed to sign a Taproot revocation-CSV output.
type JusticeInputWithBond struct {
	TxIn           *wire.TxIn
	PrevPkScript   []byte
	PrevValueSats  int64
	TimelockScript []byte
}

// SpendRevocationCsvTaprootKeypath creates a witness for spending a revocation CSV Taproot output via key path.
func SpendRevocationCsvTaprootKeypath(
	tx *wire.MsgTx,
	inputIndex int,
	inputAmountSats int64,
	pkScript []byte,
	timelockScript []byte,
	internalKeyPriv *btcec.PrivateKey,
) (wire.TxWitness, error) {
	prevOutFetcher := txscript.NewCannedPrevOutputFetcher(pkScript, inputAmountSats)
	sigHashes := txscript.NewTxSigHashes(tx, prevOutFetcher)

	leaf := txscript.NewBaseTapLeaf(timelockScript)
	root := leaf.TapHash()

	sig, err := txscript.RawTxInTaprootSignature(
		tx,
		sigHashes,
		inputIndex,
		inputAmountSats,
		pkScript,
		root[:],
		txscript.SigHashDefault,
		internalKeyPriv,
	)
	if err != nil {
		return nil, err
	}

	return wire.TxWitness{sig}, nil
}

// ConstructAndSignJusticeTransaction builds and signs a justice transaction that claims funds
// from an invalid withdrawal by using the revocation key path.
func ConstructAndSignJusticeTransaction(
	signingKey keys.Private,
	input JusticeInputWithBond,
	receiverAddress btcutil.Address,
	feeSats int64,
) (*wire.MsgTx, error) {
	revPriv := signingKey.ToBTCEC()

	tx := wire.NewMsgTx(wire.TxVersion)
	tx.AddTxIn(input.TxIn)

	if input.PrevValueSats <= feeSats {
		return nil, fmt.Errorf("insufficient funds: input=%d, fee=%d", input.PrevValueSats, feeSats)
	}
	sendValue := input.PrevValueSats - feeSats
	if sendValue < dustThresholdSats {
		return nil, fmt.Errorf("output would be dust: sendValue=%d, threshold=%d", sendValue, dustThresholdSats)
	}

	pkScript, err := txscript.PayToAddrScript(receiverAddress)
	if err != nil {
		return nil, fmt.Errorf("failed to build receiver script: %w", err)
	}
	tx.AddTxOut(&wire.TxOut{
		Value:    sendValue,
		PkScript: pkScript,
	})

	witness, err := SpendRevocationCsvTaprootKeypath(
		tx,
		0,
		input.PrevValueSats,
		input.PrevPkScript,
		input.TimelockScript,
		revPriv,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to sign input: %w", err)
	}
	tx.TxIn[0].Witness = witness

	return tx, nil
}

// BroadcastJusticeTransaction constructs, signs, and broadcasts a justice transaction
// to claim funds from an invalid token withdrawal.
func BroadcastJusticeTransaction(
	ctx context.Context,
	bitcoinClient *rpcclient.Client,
	soPrivateKey keys.Private,
	network btcnetwork.Network,
	tokenOutput *ent.TokenOutput,
	withdrawal *parsedWithdrawal,
	tokenToWithdraw *parsedOutputWithdrawal,
) (*wire.MsgTx, *ent.L1TokenJusticeTransaction, error) {
	if tokenOutput == nil {
		return nil, nil, fmt.Errorf("token output not found (Spark Tx Hash: %x; Vout: %d)",
			tokenToWithdraw.sparkTxHash, tokenToWithdraw.sparkTxVout)
	}

	if tokenOutput.SpentRevocationSecret.IsZero() {
		return nil, nil, fmt.Errorf("revocation secret is not set (Spark Tx Hash: %x; Vout: %d)",
			tokenToWithdraw.sparkTxHash, tokenToWithdraw.sparkTxVout)
	}

	revocationXOnly := tokenOutput.WithdrawRevocationCommitment[1:]
	scriptData, err := ConstructRevocationCsvTaprootOutput(
		revocationXOnly,
		tokenOutput.OwnerPublicKey.SerializeXOnly(),
		tokenOutput.WithdrawRelativeBlockLocktime,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to construct csv script: %w", err)
	}

	output := withdrawal.tx.TxOut[tokenToWithdraw.withdrawal.BitcoinVout]
	input := JusticeInputWithBond{
		TxIn: wire.NewTxIn(
			wire.NewOutPoint(&withdrawal.txHash, uint32(tokenToWithdraw.withdrawal.BitcoinVout)),
			[]byte{},
			[][]byte{},
		),
		PrevPkScript:   output.PkScript,
		PrevValueSats:  output.Value,
		TimelockScript: scriptData.TimelockScript,
	}

	pubKeyHash := btcutil.Hash160(soPrivateKey.Public().Serialize())
	soAddr, err := btcutil.NewAddressWitnessPubKeyHash(pubKeyHash, network.Params())
	if err != nil {
		return nil, nil, fmt.Errorf("failed to compute SO P2WPKH address: %w", err)
	}

	justiceTx, err := ConstructAndSignJusticeTransaction(tokenOutput.SpentRevocationSecret, input, soAddr, justiceDefaultFeeSats)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to construct and sign justice transaction: %w", err)
	}

	var txBuf bytes.Buffer
	if err := justiceTx.Serialize(&txBuf); err != nil {
		return nil, nil, fmt.Errorf("failed to serialize justice transaction: %w", err)
	}

	err = watchtower.BroadcastTransaction(ctx, bitcoinClient, tokenOutput.ID, txBuf.Bytes())
	if err != nil {
		return nil, nil, fmt.Errorf("failed to broadcast justice transaction: %w", err)
	}

	return justiceTx, &ent.L1TokenJusticeTransaction{
		JusticeTxHash: schematype.NewTxID(justiceTx.TxHash()),
		BroadcastAt:   time.Now(),
		AmountSats:    uint64(output.Value),
		TxCostSats:    justiceDefaultFeeSats,
	}, nil
}
