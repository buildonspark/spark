package tokens

import (
	"fmt"

	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/txscript"
)

// RevocationCsvTaprootOutput contains the components of a P2TR output
// with a CSV-locked revocation script path.
type RevocationCsvTaprootOutput struct {
	ScriptPubKey   []byte // P2TR locking script
	TimelockScript []byte // CSV tapscript leaf
	LeafHash       []byte // Tapleaf hash (merkle root)
	TweakedXOnly   []byte // Tweaked output key
}

// ConstructRevocationCsvTaprootOutput builds a P2TR output with a CSV timelock script.
// The output can be spent via key path (using revocationXOnly) or script path
// (using ownerXOnly after csvBlocks).
func ConstructRevocationCsvTaprootOutput(
	revocationXOnly []byte,
	ownerXOnly []byte,
	csvBlocks uint64,
) (*RevocationCsvTaprootOutput, error) {
	if len(revocationXOnly) != schnorr.PubKeyBytesLen {
		return nil, fmt.Errorf("revocationXOnly must be 32 bytes")
	}
	if len(ownerXOnly) != schnorr.PubKeyBytesLen {
		return nil, fmt.Errorf("ownerXOnly must be 32 bytes")
	}

	// Build timelock script: <csvBlocks> OP_CSV OP_DROP <owner_xonly> OP_CHECKSIG
	timelockScript, err := txscript.NewScriptBuilder().
		AddInt64(int64(csvBlocks)).
		AddOp(txscript.OP_CHECKSEQUENCEVERIFY).
		AddOp(txscript.OP_DROP).
		AddData(ownerXOnly).
		AddOp(txscript.OP_CHECKSIG).
		Script()
	if err != nil {
		return nil, fmt.Errorf("failed to build timelock script: %w", err)
	}

	leaf := txscript.NewBaseTapLeaf(timelockScript)
	leafHash := leaf.TapHash()

	internalKey, err := schnorr.ParsePubKey(revocationXOnly)
	if err != nil {
		return nil, fmt.Errorf("failed to parse revocation x-only pubkey: %w", err)
	}

	outputKey := txscript.ComputeTaprootOutputKey(internalKey, leafHash[:])
	tweakedXOnly := schnorr.SerializePubKey(outputKey)

	scriptPubKey, err := txscript.NewScriptBuilder().
		AddOp(txscript.OP_1).
		AddData(tweakedXOnly).
		Script()
	if err != nil {
		return nil, fmt.Errorf("failed to build scriptPubKey: %w", err)
	}

	return &RevocationCsvTaprootOutput{
		ScriptPubKey:   scriptPubKey,
		TimelockScript: timelockScript,
		LeafHash:       leafHash[:],
		TweakedXOnly:   tweakedXOnly,
	}, nil
}
