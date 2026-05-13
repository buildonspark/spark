package handler

// These tests target a security-sensitive internal: the coop-exit binding
// invariant that connector_tx must spend exit_txid. End-to-end coverage of
// the rejection path lives in so/grpc_test/coop_exit_test.go
// (TestCoopExit_RejectsMismatchedExitTxid). The unit tests here lock in the
// byte-order tolerance and the rejection contract narrowly, per the
// testing-philosophy exception for security primitives.

import (
	"slices"
	"testing"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightsparkdev/spark/common"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/stretchr/testify/require"
)

func buildConnectorTxSpending(t *testing.T, parent chainhash.Hash) []byte {
	t.Helper()
	tx := wire.NewMsgTx(2)
	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: parent, Index: 1},
	})
	tx.AddTxOut(&wire.TxOut{Value: 354, PkScript: []byte{0x51}})
	raw, err := common.SerializeTx(tx)
	require.NoError(t, err)
	return raw
}

func TestValidateConnectorTxBindsToExitTxid_AcceptsMatchingParent(t *testing.T) {
	var parent chainhash.Hash
	for i := range parent {
		parent[i] = byte(i + 1)
	}
	connectorTx := buildConnectorTxSpending(t, parent)

	exitTxid, err := st.NewTxIDFromBytes(parent[:])
	require.NoError(t, err)

	require.NoError(t, validateConnectorTxBindsToExitTxid(connectorTx, exitTxid))
}

func TestValidateConnectorTxBindsToExitTxid_AcceptsReversedEndianness(t *testing.T) {
	var parent chainhash.Hash
	for i := range parent {
		parent[i] = byte(i + 1)
	}
	connectorTx := buildConnectorTxSpending(t, parent)

	reversed := slices.Clone(parent[:])
	slices.Reverse(reversed)
	exitTxid, err := st.NewTxIDFromBytes(reversed)
	require.NoError(t, err)

	require.NoError(t, validateConnectorTxBindsToExitTxid(connectorTx, exitTxid))
}

func TestValidateConnectorTxBindsToExitTxid_RejectsAlibiTxid(t *testing.T) {
	var legitimate chainhash.Hash
	for i := range legitimate {
		legitimate[i] = byte(i + 1)
	}
	var alibi chainhash.Hash
	for i := range alibi {
		alibi[i] = byte(255 - i)
	}
	connectorTx := buildConnectorTxSpending(t, legitimate)

	exitTxid, err := st.NewTxIDFromBytes(alibi[:])
	require.NoError(t, err)

	err = validateConnectorTxBindsToExitTxid(connectorTx, exitTxid)
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not match exit_txid")
}

func TestValidateConnectorTxBindsToExitTxid_RejectsEmptyConnectorTx(t *testing.T) {
	exitTxid, err := st.NewTxIDFromBytes(make([]byte, chainhash.HashSize))
	require.NoError(t, err)

	err = validateConnectorTxBindsToExitTxid(nil, exitTxid)
	require.Error(t, err)
	require.Contains(t, err.Error(), "connector_tx is required")
}

func TestValidateConnectorTxBindsToExitTxid_RejectsConnectorTxWithNoInputs(t *testing.T) {
	tx := wire.NewMsgTx(2)
	tx.AddTxOut(&wire.TxOut{Value: 354, PkScript: []byte{0x51}})
	raw, err := common.SerializeTx(tx)
	require.NoError(t, err)

	var parent chainhash.Hash
	for i := range parent {
		parent[i] = byte(i + 1)
	}
	exitTxid, err := st.NewTxIDFromBytes(parent[:])
	require.NoError(t, err)

	err = validateConnectorTxBindsToExitTxid(raw, exitTxid)
	require.Error(t, err)
}
