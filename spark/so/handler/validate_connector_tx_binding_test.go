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
	tx := wire.NewMsgTx(3)
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
	tx := wire.NewMsgTx(3)
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
	require.ErrorContains(t, err, "failed to parse connector transaction")
}

func TestValidateConnectorTxBindsToExitTxid_RejectsNonCanonicalConnectorTx(t *testing.T) {
	var parent chainhash.Hash
	for i := range parent {
		parent[i] = byte(i + 1)
	}
	exitTxid, err := st.NewTxIDFromBytes(parent[:])
	require.NoError(t, err)

	tests := []struct {
		name        string
		mutate      func(*wire.MsgTx)
		errContains string
	}{
		{
			name: "wrong version",
			mutate: func(tx *wire.MsgTx) {
				tx.Version = 2
			},
			errContains: "connector transaction must use version 3",
		},
		{
			name: "extra input",
			mutate: func(tx *wire.MsgTx) {
				tx.AddTxIn(wire.NewTxIn(wire.NewOutPoint(&chainhash.Hash{}, 0), nil, nil))
			},
			errContains: "connector transaction must have exactly 1 input",
		},
		{
			name: "signature script",
			mutate: func(tx *wire.MsgTx) {
				tx.TxIn[0].SignatureScript = []byte{0x51}
			},
			errContains: "must not include signature script",
		},
		{
			name: "witness",
			mutate: func(tx *wire.MsgTx) {
				tx.TxIn[0].Witness = wire.TxWitness{[]byte{0x51}}
			},
			errContains: "must not include witness",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tx := wire.NewMsgTx(3)
			tx.AddTxIn(&wire.TxIn{
				PreviousOutPoint: wire.OutPoint{Hash: parent, Index: 1},
			})
			tx.AddTxOut(&wire.TxOut{Value: 354, PkScript: []byte{0x51}})
			test.mutate(tx)
			raw, err := common.SerializeTx(tx)
			require.NoError(t, err)

			err = validateConnectorTxBindsToExitTxid(raw, exitTxid)
			require.ErrorContains(t, err, test.errContains)
		})
	}
}
