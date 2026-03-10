package handler

import (
	"testing"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/pendingsendtransfer"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/helper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadLeafRefundMapsFromTransferPackage_NilFields(t *testing.T) {
	pkg := &pb.TransferPackage{}
	cpfp, direct, directFromCpfp := loadLeafRefundMapsFromTransferPackage(pkg)
	assert.Empty(t, cpfp)
	assert.Empty(t, direct)
	assert.Empty(t, directFromCpfp)
}

func TestLoadLeafRefundMapsFromTransferPackage_EmptyLeaves(t *testing.T) {
	pkg := &pb.TransferPackage{
		LeavesToSend:               []*pb.UserSignedTxSigningJob{},
		DirectLeavesToSend:         []*pb.UserSignedTxSigningJob{},
		DirectFromCpfpLeavesToSend: []*pb.UserSignedTxSigningJob{},
	}
	cpfp, direct, directFromCpfp := loadLeafRefundMapsFromTransferPackage(pkg)
	assert.Empty(t, cpfp)
	assert.Empty(t, direct)
	assert.Empty(t, directFromCpfp)
}

func TestLoadLeafRefundMapsFromTransferPackage_MultipleLeaves(t *testing.T) {
	pkg := &pb.TransferPackage{
		LeavesToSend: []*pb.UserSignedTxSigningJob{
			{LeafId: "leaf1", RawTx: []byte{0x01}},
			{LeafId: "leaf2", RawTx: []byte{0x02}},
		},
		DirectLeavesToSend: []*pb.UserSignedTxSigningJob{
			{LeafId: "leaf1", RawTx: []byte{0x11}},
		},
		DirectFromCpfpLeavesToSend: []*pb.UserSignedTxSigningJob{
			{LeafId: "leaf2", RawTx: []byte{0x22}},
		},
	}

	cpfp, direct, directFromCpfp := loadLeafRefundMapsFromTransferPackage(pkg)

	require.Len(t, cpfp, 2)
	assert.Equal(t, []byte{0x01}, cpfp["leaf1"])
	assert.Equal(t, []byte{0x02}, cpfp["leaf2"])

	require.Len(t, direct, 1)
	assert.Equal(t, []byte{0x11}, direct["leaf1"])

	require.Len(t, directFromCpfp, 1)
	assert.Equal(t, []byte{0x22}, directFromCpfp["leaf2"])
}

func TestLoadLeafRefundMapsFromTransferPackage_MixedRefundTypes(t *testing.T) {
	pkg := &pb.TransferPackage{
		LeavesToSend: []*pb.UserSignedTxSigningJob{
			{LeafId: "a", RawTx: []byte{0xAA}},
		},
		DirectLeavesToSend: []*pb.UserSignedTxSigningJob{
			{LeafId: "a", RawTx: []byte{0xBB}},
			{LeafId: "b", RawTx: []byte{0xCC}},
		},
		DirectFromCpfpLeavesToSend: []*pb.UserSignedTxSigningJob{
			{LeafId: "a", RawTx: []byte{0xDD}},
			{LeafId: "b", RawTx: []byte{0xEE}},
			{LeafId: "c", RawTx: []byte{0xFF}},
		},
	}

	cpfp, direct, directFromCpfp := loadLeafRefundMapsFromTransferPackage(pkg)

	assert.Len(t, cpfp, 1)
	assert.Len(t, direct, 2)
	assert.Len(t, directFromCpfp, 3)
	assert.Equal(t, []byte{0xAA}, cpfp["a"])
	assert.Equal(t, []byte{0xCC}, direct["b"])
	assert.Equal(t, []byte{0xFF}, directFromCpfp["c"])
}

// --- loadLeafRefundMaps tests (StartTransferRequest wrapper) ---

func TestLoadLeafRefundMaps_DelegatesToTransferPackage(t *testing.T) {
	req := &pb.StartTransferRequest{
		TransferPackage: &pb.TransferPackage{
			LeavesToSend: []*pb.UserSignedTxSigningJob{
				{LeafId: "leaf1", RawTx: []byte{0x01}},
			},
			DirectLeavesToSend: []*pb.UserSignedTxSigningJob{
				{LeafId: "leaf1", RawTx: []byte{0x02}},
			},
			DirectFromCpfpLeavesToSend: []*pb.UserSignedTxSigningJob{
				{LeafId: "leaf1", RawTx: []byte{0x03}},
			},
		},
	}

	cpfp, direct, directFromCpfp := loadLeafRefundMaps(req)

	assert.Equal(t, []byte{0x01}, cpfp["leaf1"])
	assert.Equal(t, []byte{0x02}, direct["leaf1"])
	assert.Equal(t, []byte{0x03}, directFromCpfp["leaf1"])
}

func TestLoadLeafRefundMaps_LegacyLeavesToSend(t *testing.T) {
	req := &pb.StartTransferRequest{
		LeavesToSend: []*pb.LeafRefundTxSigningJob{
			{
				LeafId:                           "leaf1",
				RefundTxSigningJob:               &pb.SigningJob{RawTx: []byte{0x10}},
				DirectRefundTxSigningJob:         &pb.SigningJob{RawTx: []byte{0x20}},
				DirectFromCpfpRefundTxSigningJob: &pb.SigningJob{RawTx: []byte{0x30}},
			},
			{
				LeafId:             "leaf2",
				RefundTxSigningJob: &pb.SigningJob{RawTx: []byte{0x40}},
			},
		},
	}

	cpfp, direct, directFromCpfp := loadLeafRefundMaps(req)

	require.Len(t, cpfp, 2)
	assert.Equal(t, []byte{0x10}, cpfp["leaf1"])
	assert.Equal(t, []byte{0x40}, cpfp["leaf2"])

	require.Len(t, direct, 1)
	assert.Equal(t, []byte{0x20}, direct["leaf1"])

	require.Len(t, directFromCpfp, 1)
	assert.Equal(t, []byte{0x30}, directFromCpfp["leaf1"])
}

func TestLoadLeafRefundMaps_LegacyCpfpOnly(t *testing.T) {
	req := &pb.StartTransferRequest{
		LeavesToSend: []*pb.LeafRefundTxSigningJob{
			{
				LeafId:             "leaf1",
				RefundTxSigningJob: &pb.SigningJob{RawTx: []byte{0xAA}},
			},
		},
	}

	cpfp, direct, directFromCpfp := loadLeafRefundMaps(req)

	require.Len(t, cpfp, 1)
	assert.Equal(t, []byte{0xAA}, cpfp["leaf1"])
	assert.Empty(t, direct)
	assert.Empty(t, directFromCpfp)
}

// --- loadInternalLeafRefundMaps tests (InitiateTransferRequest wrapper) ---

func TestLoadInternalLeafRefundMaps_DelegatesToTransferPackage(t *testing.T) {
	req := &pbinternal.InitiateTransferRequest{
		TransferPackage: &pb.TransferPackage{
			LeavesToSend: []*pb.UserSignedTxSigningJob{
				{LeafId: "leaf1", RawTx: []byte{0x01}},
			},
			DirectLeavesToSend: []*pb.UserSignedTxSigningJob{
				{LeafId: "leaf1", RawTx: []byte{0x02}},
			},
			DirectFromCpfpLeavesToSend: []*pb.UserSignedTxSigningJob{
				{LeafId: "leaf1", RawTx: []byte{0x03}},
			},
		},
	}

	cpfp, direct, directFromCpfp := loadInternalLeafRefundMaps(req)

	assert.Equal(t, []byte{0x01}, cpfp["leaf1"])
	assert.Equal(t, []byte{0x02}, direct["leaf1"])
	assert.Equal(t, []byte{0x03}, directFromCpfp["leaf1"])
}

func TestLoadInternalLeafRefundMaps_LegacyLeaves(t *testing.T) {
	req := &pbinternal.InitiateTransferRequest{
		Leaves: []*pbinternal.InitiateTransferLeaf{
			{
				LeafId:                 "leaf1",
				RawRefundTx:            []byte{0x10},
				DirectRefundTx:         []byte{0x20},
				DirectFromCpfpRefundTx: []byte{0x30},
			},
			{
				LeafId:      "leaf2",
				RawRefundTx: []byte{0x40},
			},
		},
	}

	cpfp, direct, directFromCpfp := loadInternalLeafRefundMaps(req)

	require.Len(t, cpfp, 2)
	assert.Equal(t, []byte{0x10}, cpfp["leaf1"])
	assert.Equal(t, []byte{0x40}, cpfp["leaf2"])

	require.Len(t, direct, 2)
	assert.Equal(t, []byte{0x20}, direct["leaf1"])

	require.Len(t, directFromCpfp, 2)
	assert.Equal(t, []byte{0x30}, directFromCpfp["leaf1"])
}

func TestLoadInternalLeafRefundMaps_EmptyRequest(t *testing.T) {
	req := &pbinternal.InitiateTransferRequest{}

	cpfp, direct, directFromCpfp := loadInternalLeafRefundMaps(req)

	assert.Empty(t, cpfp)
	assert.Empty(t, direct)
	assert.Empty(t, directFromCpfp)
}

// --- applyRefundSignatures tests ---

func TestApplyRefundSignatures_AllNilSigs(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)

	cpfp := map[string][]byte{"a": {0x01}}
	direct := map[string][]byte{"a": {0x02}}
	directFromCpfp := map[string][]byte{"a": {0x03}}

	outCpfp, outDirect, outDirectFromCpfp, err := applyRefundSignatures(
		ctx, "test-transfer",
		cpfp, direct, directFromCpfp,
		nil, nil, nil,
	)
	require.NoError(t, err)
	assert.Equal(t, cpfp, outCpfp)
	assert.Equal(t, direct, outDirect)
	assert.Equal(t, directFromCpfp, outDirectFromCpfp)
}

func TestApplyRefundSignatures_CpfpOnlyWithEmptySigs(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)

	cpfp := map[string][]byte{"a": {0x01}}
	direct := map[string][]byte{"a": {0x02}}
	directFromCpfp := map[string][]byte{"a": {0x03}}

	// Empty (non-nil) cpfp sigs triggers the call but processes no leaves.
	outCpfp, outDirect, outDirectFromCpfp, err := applyRefundSignatures(
		ctx, "test-transfer",
		cpfp, direct, directFromCpfp,
		map[string][]byte{}, nil, nil,
	)
	require.NoError(t, err)
	// cpfp map replaced by empty result from applySignaturesToTransactionsAndVerify.
	assert.Empty(t, outCpfp)
	// direct maps unchanged because directSigs is nil.
	assert.Equal(t, direct, outDirect)
	assert.Equal(t, directFromCpfp, outDirectFromCpfp)
}

func TestApplyRefundSignatures_DirectSkippedWhenOnlyOnePresent(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)

	direct := map[string][]byte{"a": {0x02}}
	directFromCpfp := map[string][]byte{"a": {0x03}}

	// directSigs present but directFromCpfpSigs nil → both skipped.
	_, outDirect, outDirectFromCpfp, err := applyRefundSignatures(
		ctx, "test-transfer",
		nil, direct, directFromCpfp,
		nil, map[string][]byte{}, nil,
	)
	require.NoError(t, err)
	assert.Equal(t, direct, outDirect)
	assert.Equal(t, directFromCpfp, outDirectFromCpfp)

	// directFromCpfpSigs present but directSigs nil → both skipped.
	_, outDirect, outDirectFromCpfp, err = applyRefundSignatures(
		ctx, "test-transfer",
		nil, direct, directFromCpfp,
		nil, nil, map[string][]byte{},
	)
	require.NoError(t, err)
	assert.Equal(t, direct, outDirect)
	assert.Equal(t, directFromCpfp, outDirectFromCpfp)
}

func TestApplyRefundSignatures_CpfpErrorPropagation(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)

	// Non-UUID key will cause applySignaturesToTransactionsAndVerify to fail.
	badSigs := map[string][]byte{"not-a-uuid": {0xFF}}

	_, _, _, err := applyRefundSignatures(
		ctx, "test-transfer-123",
		map[string][]byte{}, map[string][]byte{}, map[string][]byte{},
		badSigs, nil, nil,
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cpfp")
	assert.Contains(t, err.Error(), "test-transfer-123")
}

func TestApplyRefundSignatures_DirectErrorPropagation(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)

	badSigs := map[string][]byte{"not-a-uuid": {0xFF}}

	_, _, _, err := applyRefundSignatures(
		ctx, "test-transfer-456",
		map[string][]byte{}, map[string][]byte{}, map[string][]byte{},
		nil, badSigs, map[string][]byte{},
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "direct refund")
	assert.Contains(t, err.Error(), "test-transfer-456")
}

func TestApplyRefundSignatures_DirectFromCpfpErrorPropagation(t *testing.T) {
	ctx, _ := db.NewTestSQLiteContext(t)

	badSigs := map[string][]byte{"not-a-uuid": {0xFF}}

	// Valid empty directSigs so the first call succeeds, bad directFromCpfpSigs.
	_, _, _, err := applyRefundSignatures(
		ctx, "test-transfer-789",
		map[string][]byte{}, map[string][]byte{}, map[string][]byte{},
		nil, map[string][]byte{}, badSigs,
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "direct from cpfp")
	assert.Contains(t, err.Error(), "test-transfer-789")
}

// --- createPendingSendTransferAndCommit tests ---

func TestCreatePendingSendTransferAndCommit_Success(t *testing.T) {
	ctx, dbCtx := db.NewTestSQLiteContext(t)

	transferID := uuid.New()
	err := createPendingSendTransferAndCommit(ctx, transferID)
	require.NoError(t, err)

	// Verify the PendingSendTransfer record was created with Pending status.
	pst, err := dbCtx.Client.PendingSendTransfer.Query().
		Where(pendingsendtransfer.TransferID(transferID)).
		Only(ctx)
	require.NoError(t, err)
	assert.Equal(t, st.PendingSendTransferStatusPending, pst.Status)
}

func TestCreatePendingSendTransferAndCommit_Idempotent(t *testing.T) {
	ctx, dbCtx := db.NewTestSQLiteContext(t)

	transferID := uuid.New()

	// First call creates the record.
	err := createPendingSendTransferAndCommit(ctx, transferID)
	require.NoError(t, err)

	// Second call resets it (idempotent via CreateOrResetPendingSendTransfer).
	err = createPendingSendTransferAndCommit(ctx, transferID)
	require.NoError(t, err)

	// Should still be exactly one record.
	count, err := dbCtx.Client.PendingSendTransfer.Query().
		Where(pendingsendtransfer.TransferID(transferID)).
		Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestCreatePendingSendTransferAndCommit_ErrorOnMissingTxContext(t *testing.T) {
	// A bare context without an injected database session should fail.
	err := createPendingSendTransferAndCommit(t.Context(), uuid.New())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unable to get database transaction")
}

// --- buildSigningResultProtos tests ---

func testSigningResult() *helper.SigningResult {
	return &helper.SigningResult{
		JobID:                    uuid.New(),
		KeyshareOwnerIdentifiers: []string{"op1"},
		KeyshareThreshold:        1,
	}
}

func testVerifyingKey() keys.Public {
	return keys.MustParsePublicKeyHex("0279be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798")
}

func TestBuildSigningResults_EmptyMaps(t *testing.T) {
	leafMap := map[string]*ent.TreeNode{}
	results, err := buildSigningResultProtos(leafMap, nil, nil, nil)
	require.NoError(t, err)
	assert.Empty(t, results)
}

func TestBuildSigningResults_CpfpOnly(t *testing.T) {
	vk := testVerifyingKey()
	leafMap := map[string]*ent.TreeNode{
		"leaf1": {VerifyingPubkey: vk},
	}
	cpfp := map[string]*helper.SigningResult{
		"leaf1": testSigningResult(),
	}

	results, err := buildSigningResultProtos(leafMap, cpfp, nil, nil)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "leaf1", results[0].LeafId)
	assert.NotNil(t, results[0].RefundTxSigningResult)
	assert.Nil(t, results[0].DirectRefundTxSigningResult)
	assert.Nil(t, results[0].DirectFromCpfpRefundTxSigningResult)
	assert.Equal(t, vk.Serialize(), results[0].VerifyingKey)
}

func TestBuildSigningResults_AllThreeRefundTypes(t *testing.T) {
	vk := testVerifyingKey()
	leafMap := map[string]*ent.TreeNode{
		"leaf1": {VerifyingPubkey: vk},
	}
	cpfp := map[string]*helper.SigningResult{"leaf1": testSigningResult()}
	direct := map[string]*helper.SigningResult{"leaf1": testSigningResult()}
	directFromCpfp := map[string]*helper.SigningResult{"leaf1": testSigningResult()}

	results, err := buildSigningResultProtos(leafMap, cpfp, direct, directFromCpfp)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.NotNil(t, results[0].RefundTxSigningResult)
	assert.NotNil(t, results[0].DirectRefundTxSigningResult)
	assert.NotNil(t, results[0].DirectFromCpfpRefundTxSigningResult)
}

func TestBuildSigningResults_LeafWithNoCpfpEntry(t *testing.T) {
	vk := testVerifyingKey()
	leafMap := map[string]*ent.TreeNode{
		"leaf1": {VerifyingPubkey: vk},
	}
	// No cpfp entry for leaf1 — all protos should be nil.
	results, err := buildSigningResultProtos(leafMap, map[string]*helper.SigningResult{}, nil, nil)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Nil(t, results[0].RefundTxSigningResult)
	assert.Nil(t, results[0].DirectRefundTxSigningResult)
	assert.Nil(t, results[0].DirectFromCpfpRefundTxSigningResult)
}
