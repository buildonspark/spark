package handler

import (
	"testing"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSignRefundsWithPregeneratedNonce_NilPackage(t *testing.T) {
	_, _, _, err := SignRefundsWithPregeneratedNonce(
		t.Context(),
		nil,
		"test-transfer-id",
		nil, // nil package
		nil, // leafMap
		keys.Public{}, keys.Public{}, keys.Public{},
		nil,
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "transfer package is nil")
}

func TestSignRefundsWithPregeneratedNonce_LeafNotInMap(t *testing.T) {
	pkg := &pb.TransferPackage{
		LeavesToSend: []*pb.UserSignedTxSigningJob{
			{LeafId: "missing-leaf", RawTx: []byte{0x01}},
		},
	}
	leafMap := make(map[string]*ent.TreeNode)

	_, _, _, err := SignRefundsWithPregeneratedNonce(
		t.Context(),
		nil,
		"test-transfer-id",
		pkg,
		leafMap,
		keys.Public{}, keys.Public{}, keys.Public{},
		nil,
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "leaf missing-leaf not found in leafMap")
}

func TestSignRefundsWithPregeneratedNonce_DirectLeafNotInMap(t *testing.T) {
	pkg := &pb.TransferPackage{
		DirectLeavesToSend: []*pb.UserSignedTxSigningJob{
			{LeafId: "missing-direct-leaf", RawTx: []byte{0x01}},
		},
	}
	leafMap := make(map[string]*ent.TreeNode)

	_, _, _, err := SignRefundsWithPregeneratedNonce(
		t.Context(),
		nil,
		"test-transfer-id",
		pkg,
		leafMap,
		keys.Public{}, keys.Public{}, keys.Public{},
		nil,
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "leaf missing-direct-leaf not found in leafMap")
}

func TestSignRefundsWithPregeneratedNonce_DirectFromCpfpLeafNotInMap(t *testing.T) {
	pkg := &pb.TransferPackage{
		DirectFromCpfpLeavesToSend: []*pb.UserSignedTxSigningJob{
			{LeafId: "missing-dcl", RawTx: []byte{0x01}},
		},
	}
	leafMap := make(map[string]*ent.TreeNode)

	_, _, _, err := SignRefundsWithPregeneratedNonce(
		t.Context(),
		nil,
		"test-transfer-id",
		pkg,
		leafMap,
		keys.Public{}, keys.Public{}, keys.Public{},
		nil,
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "leaf missing-dcl not found in leafMap")
}

// --- rollbackTransferInit tests ---

func TestRollbackTransferInit_NoTxInContext(t *testing.T) {
	h := &TransferHandler{}
	err := h.rollbackTransferInit(t.Context(), uuid.New(), false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unable to get database transaction")
}

func TestRollbackTransferInit_NoTxInContext_WithCancelGossip(t *testing.T) {
	h := &TransferHandler{}
	err := h.rollbackTransferInit(t.Context(), uuid.New(), true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unable to get database transaction")
}
