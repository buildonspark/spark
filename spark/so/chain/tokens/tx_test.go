package tokens

import (
	"testing"

	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/txscript"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConstructRevocationCsvTaprootOutput_Success(t *testing.T) {
	// Generate test keys
	revocationPrivKey := keys.GeneratePrivateKey()
	ownerPrivKey := keys.GeneratePrivateKey()

	revocationXOnly := revocationPrivKey.Public().SerializeXOnly()
	ownerXOnly := ownerPrivKey.Public().SerializeXOnly()
	csvBlocks := uint64(1000)

	output, err := ConstructRevocationCsvTaprootOutput(revocationXOnly, ownerXOnly, csvBlocks)
	require.NoError(t, err)
	require.NotNil(t, output)

	// Verify scriptPubKey format: OP_1 <32-byte x-only pubkey>
	assert.Len(t, output.ScriptPubKey, 34)
	assert.Equal(t, byte(txscript.OP_1), output.ScriptPubKey[0])
	assert.Equal(t, byte(0x20), output.ScriptPubKey[1]) // OP_DATA_32

	// Verify timelock script contains expected components
	assert.NotEmpty(t, output.TimelockScript)

	// Verify leaf hash is 32 bytes
	assert.Len(t, output.LeafHash, 32)

	// Verify tweaked x-only key is 32 bytes
	assert.Len(t, output.TweakedXOnly, 32)
}

func TestConstructRevocationCsvTaprootOutput_DeterministicOutput(t *testing.T) {
	// Same inputs should produce same outputs
	revocationPrivKey := keys.GeneratePrivateKey()
	ownerPrivKey := keys.GeneratePrivateKey()

	revocationXOnly := revocationPrivKey.Public().SerializeXOnly()
	ownerXOnly := ownerPrivKey.Public().SerializeXOnly()
	csvBlocks := uint64(144)

	output1, err := ConstructRevocationCsvTaprootOutput(revocationXOnly, ownerXOnly, csvBlocks)
	require.NoError(t, err)

	output2, err := ConstructRevocationCsvTaprootOutput(revocationXOnly, ownerXOnly, csvBlocks)
	require.NoError(t, err)

	assert.Equal(t, output1.ScriptPubKey, output2.ScriptPubKey)
	assert.Equal(t, output1.TimelockScript, output2.TimelockScript)
	assert.Equal(t, output1.LeafHash, output2.LeafHash)
	assert.Equal(t, output1.TweakedXOnly, output2.TweakedXOnly)
}

func TestConstructRevocationCsvTaprootOutput_DifferentCSV(t *testing.T) {
	revocationPrivKey := keys.GeneratePrivateKey()
	ownerPrivKey := keys.GeneratePrivateKey()

	revocationXOnly := revocationPrivKey.Public().SerializeXOnly()
	ownerXOnly := ownerPrivKey.Public().SerializeXOnly()

	output1, err := ConstructRevocationCsvTaprootOutput(revocationXOnly, ownerXOnly, 100)
	require.NoError(t, err)

	output2, err := ConstructRevocationCsvTaprootOutput(revocationXOnly, ownerXOnly, 200)
	require.NoError(t, err)

	// Different CSV blocks should produce different scripts
	assert.NotEqual(t, output1.TimelockScript, output2.TimelockScript)
	assert.NotEqual(t, output1.LeafHash, output2.LeafHash)
	assert.NotEqual(t, output1.ScriptPubKey, output2.ScriptPubKey)
}

func TestConstructRevocationCsvTaprootOutput_DifferentKeys(t *testing.T) {
	revocationPrivKey1 := keys.GeneratePrivateKey()
	revocationPrivKey2 := keys.GeneratePrivateKey()
	ownerPrivKey := keys.GeneratePrivateKey()

	revocationXOnly1 := revocationPrivKey1.Public().SerializeXOnly()
	revocationXOnly2 := revocationPrivKey2.Public().SerializeXOnly()
	ownerXOnly := ownerPrivKey.Public().SerializeXOnly()
	csvBlocks := uint64(1000)

	output1, err := ConstructRevocationCsvTaprootOutput(revocationXOnly1, ownerXOnly, csvBlocks)
	require.NoError(t, err)

	output2, err := ConstructRevocationCsvTaprootOutput(revocationXOnly2, ownerXOnly, csvBlocks)
	require.NoError(t, err)

	// Different revocation keys should produce different outputs
	assert.NotEqual(t, output1.ScriptPubKey, output2.ScriptPubKey)
	assert.NotEqual(t, output1.TweakedXOnly, output2.TweakedXOnly)
	// But same timelock script (only depends on owner and CSV)
	assert.Equal(t, output1.TimelockScript, output2.TimelockScript)
}

func TestConstructRevocationCsvTaprootOutput_InvalidRevocationKeyLength(t *testing.T) {
	ownerPrivKey := keys.GeneratePrivateKey()

	invalidRevocationXOnly := make([]byte, 31) // Should be 32 bytes
	ownerXOnly := ownerPrivKey.Public().SerializeXOnly()

	_, err := ConstructRevocationCsvTaprootOutput(invalidRevocationXOnly, ownerXOnly, 1000)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "revocationXOnly must be 32 bytes")
}

func TestConstructRevocationCsvTaprootOutput_InvalidOwnerKeyLength(t *testing.T) {
	revocationPrivKey := keys.GeneratePrivateKey()

	revocationXOnly := revocationPrivKey.Public().SerializeXOnly()
	invalidOwnerXOnly := make([]byte, 31) // Should be 32 bytes

	_, err := ConstructRevocationCsvTaprootOutput(revocationXOnly, invalidOwnerXOnly, 1000)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ownerXOnly must be 32 bytes")
}

func TestConstructRevocationCsvTaprootOutput_TimelockScriptFormat(t *testing.T) {
	revocationPrivKey := keys.GeneratePrivateKey()
	ownerPrivKey := keys.GeneratePrivateKey()

	revocationXOnly := revocationPrivKey.Public().SerializeXOnly()
	ownerXOnly := ownerPrivKey.Public().SerializeXOnly()
	csvBlocks := uint64(144)

	output, err := ConstructRevocationCsvTaprootOutput(revocationXOnly, ownerXOnly, csvBlocks)
	require.NoError(t, err)

	// Disassemble and verify the timelock script
	// Expected: <csvBlocks> OP_CSV OP_DROP <owner_xonly> OP_CHECKSIG
	disasm, err := txscript.DisasmString(output.TimelockScript)
	require.NoError(t, err)

	// The disassembly should contain OP_CHECKSEQUENCEVERIFY, OP_DROP, and OP_CHECKSIG
	assert.Contains(t, disasm, "OP_CHECKSEQUENCEVERIFY")
	assert.Contains(t, disasm, "OP_DROP")
	assert.Contains(t, disasm, "OP_CHECKSIG")
}

func TestConstructRevocationCsvTaprootOutput_CanParseTweakedKey(t *testing.T) {
	revocationPrivKey := keys.GeneratePrivateKey()
	ownerPrivKey := keys.GeneratePrivateKey()

	revocationXOnly := revocationPrivKey.Public().SerializeXOnly()
	ownerXOnly := ownerPrivKey.Public().SerializeXOnly()
	csvBlocks := uint64(1000)

	output, err := ConstructRevocationCsvTaprootOutput(revocationXOnly, ownerXOnly, csvBlocks)
	require.NoError(t, err)

	// Verify we can parse the tweaked key back
	_, err = schnorr.ParsePubKey(output.TweakedXOnly)
	require.NoError(t, err)
}
