package ent

import (
	"bytes"
	"testing"
	"time"

	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/stretchr/testify/require"
)

func signedDirectTransaction(t *testing.T, owner keys.Public) []byte {
	t.Helper()

	pkScript, err := common.P2TRScriptFromPubKey(owner)
	require.NoError(t, err)
	tx := wire.NewMsgTx(3)
	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Index: 0},
		Sequence:         1,
		Witness:          wire.TxWitness{bytes.Repeat([]byte{0x42}, 64)},
	})
	tx.AddTxOut(&wire.TxOut{Value: 1000, PkScript: pkScript})
	rawTx, err := common.SerializeTx(tx)
	require.NoError(t, err)
	return rawTx
}

func treeNodeWithLoadedEdges(t *testing.T) *TreeNode {
	t.Helper()

	owner := keys.GeneratePrivateKey().Public()
	keyshare := keys.GeneratePrivateKey().Public()
	now := time.Now()
	return &TreeNode{
		ID:                     uuid.New(),
		CreateTime:             now,
		UpdateTime:             now,
		Value:                  1000,
		Network:                btcnetwork.Regtest,
		Status:                 st.TreeNodeStatusAvailable,
		VerifyingPubkey:        owner.Add(keyshare),
		OwnerIdentityPubkey:    owner,
		OwnerSigningPubkey:     owner,
		Vout:                   0,
		RawTx:                  signedDirectTransaction(t, owner),
		RawRefundTx:            signedDirectTransaction(t, owner),
		DirectTx:               signedDirectTransaction(t, owner),
		DirectRefundTx:         signedDirectTransaction(t, owner),
		DirectFromCpfpRefundTx: signedDirectTransaction(t, owner),
		Edges: TreeNodeEdges{
			Tree: &Tree{
				ID:      uuid.New(),
				Network: btcnetwork.Regtest,
			},
			Parent: &TreeNode{ID: uuid.New()},
			SigningKeyshare: &SigningKeyshare{
				ID:           uuid.New(),
				PublicKey:    keyshare,
				PublicShares: map[string]keys.Public{"operator": keyshare},
				MinSigners:   1,
				UpdateTime:   now,
			},
		},
	}
}

func TestTreeNodeMarshalSparkProtoPreservesDirectTransactionWitnesses(t *testing.T) {
	node := treeNodeWithLoadedEdges(t)

	publicProto, err := node.MarshalSparkProto(t.Context())
	require.NoError(t, err)
	internalProto, err := node.MarshalInternalProto(t.Context())
	require.NoError(t, err)

	require.Equal(t, node.DirectTx, publicProto.GetDirectTx())
	require.Equal(t, node.DirectRefundTx, publicProto.GetDirectRefundTx())
	require.Equal(t, node.DirectFromCpfpRefundTx, publicProto.GetDirectFromCpfpRefundTx())
	require.Equal(t, node.RawTx, publicProto.GetNodeTx())
	require.Equal(t, node.RawRefundTx, publicProto.GetRefundTx())
	require.Equal(t, node.DirectTx, internalProto.GetDirectTx())
	require.Equal(t, node.DirectRefundTx, internalProto.GetDirectRefundTx())
	require.Equal(t, node.DirectFromCpfpRefundTx, internalProto.GetDirectFromCpfpRefundTx())
}

func TestTreeNodeMarshalSparkProtoPreservesNilDirectTransactions(t *testing.T) {
	node := treeNodeWithLoadedEdges(t)
	node.DirectTx = nil
	node.DirectRefundTx = nil
	node.DirectFromCpfpRefundTx = nil

	publicProto, err := node.MarshalSparkProto(t.Context())
	require.NoError(t, err)
	require.Nil(t, publicProto.GetDirectTx())
	require.Nil(t, publicProto.GetDirectRefundTx())
	require.Nil(t, publicProto.GetDirectFromCpfpRefundTx())
}

func TestTransferLeafMarshalProtoPreservesDirectTransactionWitnesses(t *testing.T) {
	node := treeNodeWithLoadedEdges(t)
	transferLeaf := &TransferLeaf{
		ID:                                 uuid.New(),
		IntermediateRefundTx:               node.RawRefundTx,
		IntermediateDirectRefundTx:         signedDirectTransaction(t, node.OwnerSigningPubkey),
		IntermediateDirectFromCpfpRefundTx: signedDirectTransaction(t, node.OwnerSigningPubkey),
	}

	publicProto, err := transferLeaf.marshalTransferLeafProto(t.Context(), node)
	require.NoError(t, err)
	require.Equal(t, transferLeaf.IntermediateDirectRefundTx, publicProto.GetIntermediateDirectRefundTx())
	require.Equal(t, transferLeaf.IntermediateDirectFromCpfpRefundTx, publicProto.GetIntermediateDirectFromCpfpRefundTx())
	require.Equal(t, node.DirectTx, publicProto.GetLeaf().GetDirectTx())
	require.Equal(t, transferLeaf.IntermediateRefundTx, publicProto.GetIntermediateRefundTx())
}
