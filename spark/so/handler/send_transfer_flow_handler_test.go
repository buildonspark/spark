package handler

import (
	"context"
	"io"
	"math/rand/v2"
	"testing"

	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSendTransferJobID_Deterministic verifies that the same (transferID,
// leafID, txKind) tuple produces the same UUID across invocations — the
// load-bearing property that lets every SO derive matching job IDs without
// sending them over the wire.
func TestSendTransferJobID_Deterministic(t *testing.T) {
	transferID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	leafID := "22222222-2222-2222-2222-222222222222"

	a1 := sendTransferJobID(transferID, leafID, "cpfp")
	a2 := sendTransferJobID(transferID, leafID, "cpfp")
	assert.Equal(t, a1, a2, "same args must produce the same UUID")

	// Different txKind → different UUID.
	b := sendTransferJobID(transferID, leafID, "direct")
	assert.NotEqual(t, a1, b, "txKind must affect the UUID")

	c := sendTransferJobID(transferID, leafID, "directFromCpfp")
	assert.NotEqual(t, a1, c)
	assert.NotEqual(t, b, c)

	// Different transferID → different UUID.
	otherTransfer := uuid.MustParse("33333333-3333-3333-3333-333333333333")
	d := sendTransferJobID(otherTransfer, leafID, "cpfp")
	assert.NotEqual(t, a1, d)

	// Different leafID → different UUID.
	e := sendTransferJobID(transferID, "44444444-4444-4444-4444-444444444444", "cpfp")
	assert.NotEqual(t, a1, e)
}

// TestSplitLeafSignatures verifies the commit-payload signature split.
func TestSplitLeafSignatures(t *testing.T) {
	in := []*pbinternal.SendTransferLeafSignatures{
		{
			LeafId:                        "leaf-a",
			RefundSignature:               []byte{0x01},
			DirectRefundSignature:         []byte{0x02},
			DirectFromCpfpRefundSignature: []byte{0x03},
		},
		{
			LeafId:          "leaf-b",
			RefundSignature: []byte{0x04},
			// no direct sigs — these maps should not contain leaf-b
		},
		{
			// All empty — entry contributes nothing.
			LeafId: "leaf-c",
		},
	}

	cpfp, direct, dfc := splitLeafSignatures(in)

	assert.Equal(t, []byte{0x01}, cpfp["leaf-a"])
	assert.Equal(t, []byte{0x04}, cpfp["leaf-b"])
	assert.NotContains(t, cpfp, "leaf-c")

	assert.Equal(t, []byte{0x02}, direct["leaf-a"])
	assert.NotContains(t, direct, "leaf-b")
	assert.NotContains(t, direct, "leaf-c")

	assert.Equal(t, []byte{0x03}, dfc["leaf-a"])
	assert.NotContains(t, dfc, "leaf-b")
	assert.NotContains(t, dfc, "leaf-c")
}

// TestParseSendTransferRequest_Errors covers the validation guards that turn
// malformed v3 requests into typed sparkerrors before any DB work happens.
func TestParseSendTransferRequest_Errors(t *testing.T) {
	validSenderPK := keys.GeneratePrivateKey().Public().Serialize()
	validReceiverPK := keys.GeneratePrivateKey().Public().Serialize()
	validTransferID := "11111111-1111-1111-1111-111111111111"

	makeValid := func() *pb.StartTransferV3Request {
		return &pb.StartTransferV3Request{
			TransferId: validTransferID,
			SenderPackages: []*pb.SenderTransferPackage{{
				OwnerIdentityPublicKey: validSenderPK,
				TransferPackage:        &pb.TransferPackage{},
				ReceiverIdentityPublicKeys: map[string][]byte{
					"leaf-1": validReceiverPK,
				},
			}},
		}
	}

	cases := []struct {
		name    string
		mutate  func(*pb.StartTransferV3Request)
		wantSub string
	}{
		{
			name:    "zero sender packages",
			mutate:  func(r *pb.StartTransferV3Request) { r.SenderPackages = nil },
			wantSub: "expected exactly 1 sender package",
		},
		{
			name: "two sender packages",
			mutate: func(r *pb.StartTransferV3Request) {
				r.SenderPackages = append(r.SenderPackages, r.SenderPackages[0])
			},
			wantSub: "expected exactly 1 sender package",
		},
		{
			name: "nil transfer package",
			mutate: func(r *pb.StartTransferV3Request) {
				r.SenderPackages[0].TransferPackage = nil
			},
			wantSub: "transfer_package is required",
		},
		{
			name:    "invalid transfer id",
			mutate:  func(r *pb.StartTransferV3Request) { r.TransferId = "not-a-uuid" },
			wantSub: "invalid transfer id",
		},
		{
			name: "invalid sender pubkey",
			mutate: func(r *pb.StartTransferV3Request) {
				r.SenderPackages[0].OwnerIdentityPublicKey = []byte{0x00}
			},
			wantSub: "owner identity public key",
		},
		{
			name: "no receivers",
			mutate: func(r *pb.StartTransferV3Request) {
				r.SenderPackages[0].ReceiverIdentityPublicKeys = nil
			},
			wantSub: "at least one receiver",
		},
		{
			name: "invalid receiver pubkey",
			mutate: func(r *pb.StartTransferV3Request) {
				r.SenderPackages[0].ReceiverIdentityPublicKeys["leaf-1"] = []byte{0x00}
			},
			wantSub: "receiver pubkey",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := makeValid()
			tc.mutate(req)
			_, err := parseSendTransferRequest(req)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantSub)
		})
	}
}

// TestParseSendTransferRequest_Happy covers the path-of-success: well-formed
// request parses without error and exposes the expected fields.
func TestParseSendTransferRequest_Happy(t *testing.T) {
	validSenderPK := keys.GeneratePrivateKey().Public().Serialize()
	receiverA := keys.GeneratePrivateKey().Public().Serialize()
	receiverB := keys.GeneratePrivateKey().Public().Serialize()

	req := &pb.StartTransferV3Request{
		TransferId: "11111111-1111-1111-1111-111111111111",
		SenderPackages: []*pb.SenderTransferPackage{{
			OwnerIdentityPublicKey: validSenderPK,
			TransferPackage:        &pb.TransferPackage{},
			ReceiverIdentityPublicKeys: map[string][]byte{
				"leaf-1": receiverA,
				"leaf-2": receiverB,
				"leaf-3": receiverA, // duplicate → deduplicated
			},
		}},
	}

	parsed, err := parseSendTransferRequest(req)
	require.NoError(t, err)
	assert.Equal(t, uuid.MustParse("11111111-1111-1111-1111-111111111111"), parsed.transferID)
	assert.Len(t, parsed.leafReceiverMap, 3, "leaf→receiver map preserves every leaf")
	assert.Len(t, parsed.receivers, 2, "duplicate receiver pubkeys collapse into the unique set")
}

// TestFilterJobsForThisOperator verifies the threshold-signing filter: only
// keep jobs whose round1 commitments include this SO's identifier.
func TestFilterJobsForThisOperator(t *testing.T) {
	mkJob := func(id string, opIDs ...string) *pbinternal.SigningJob {
		commitments := make(map[string]*pbcommon.SigningCommitment, len(opIDs))
		for _, oid := range opIDs {
			commitments[oid] = &pbcommon.SigningCommitment{}
		}
		return &pbinternal.SigningJob{JobId: id, Commitments: commitments}
	}

	jobs := []*pbinternal.SigningJob{
		mkJob("job-1", "op-a", "op-b"),         // op-a is in
		mkJob("job-2", "op-b", "op-c"),         // op-a is NOT in
		mkJob("job-3", "op-a", "op-c", "op-d"), // op-a is in
	}

	filtered := filterJobsForThisOperator(jobs, "op-a")
	assert.Len(t, filtered, 2)
	assert.Equal(t, "job-1", filtered[0].JobId)
	assert.Equal(t, "job-3", filtered[1].JobId)
}

func TestBuildSigningJobForRefundValidatesParentOutpoint(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{7})
	ctx, leaf, parentTx := createSendTransferSigningJobTestLeaf(t, rng)
	refundScript, err := common.P2TRScriptFromPubKey(keys.MustGeneratePrivateKeyFromRand(rng).Public())
	require.NoError(t, err)

	parentOutPoint := wire.OutPoint{Hash: parentTx.TxHash(), Index: 0}
	validRefundRaw := createSendTransferSigningJobTestTx(t, parentOutPoint, 900, refundScript, nil)
	_, err = buildSigningJobForRefund(
		ctx,
		createSendTransferUserSignedJob(t, rng, leaf.ID.String(), validRefundRaw),
		leaf,
		leaf.RawTx,
		uuid.New(),
	)
	require.NoError(t, err)

	wrongOutPoint := wire.OutPoint{Hash: [32]byte{0x99}, Index: 0}
	wrongOutpointRaw := createSendTransferSigningJobTestTx(t, wrongOutPoint, 900, refundScript, nil)
	_, err = buildSigningJobForRefund(
		ctx,
		createSendTransferUserSignedJob(t, rng, leaf.ID.String(), wrongOutpointRaw),
		leaf,
		leaf.RawTx,
		uuid.New(),
	)
	require.ErrorContains(t, err, "refund tx input 0 must spend parent tx output 0")

	extraInputRaw := createSendTransferSigningJobTestTx(t, parentOutPoint, 900, refundScript, &wrongOutPoint)
	_, err = buildSigningJobForRefund(
		ctx,
		createSendTransferUserSignedJob(t, rng, leaf.ID.String(), extraInputRaw),
		leaf,
		leaf.RawTx,
		uuid.New(),
	)
	require.ErrorContains(t, err, "refund tx must have exactly 1 input")
}

func TestBuildSendTransferAggregationJobsValidatesAllRefundPackageOutpoints(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{8})
	ctx, leaf, cpfpParentTx := createSendTransferSigningJobTestLeaf(t, rng)
	refundScript, err := common.P2TRScriptFromPubKey(keys.MustGeneratePrivateKeyFromRand(rng).Public())
	require.NoError(t, err)

	directParentRaw := createSendTransferSigningJobTestTx(
		t,
		wire.OutPoint{Hash: [32]byte{0x42}, Index: 0},
		950,
		refundScript,
		nil,
	)
	directParentTx, err := common.TxFromRawTxBytes(directParentRaw)
	require.NoError(t, err)
	leaf, err = leaf.Update().SetDirectTx(directParentRaw).Save(ctx)
	require.NoError(t, err)

	wrongOutPoint := wire.OutPoint{Hash: [32]byte{0x77}, Index: 0}
	makeWrongJob := func() *pb.UserSignedTxSigningJob {
		rawTx := createSendTransferSigningJobTestTx(t, wrongOutPoint, 900, refundScript, nil)
		return createSendTransferUserSignedJob(t, rng, leaf.ID.String(), rawTx)
	}
	makeValidJob := func(parentTx *wire.MsgTx) *pb.UserSignedTxSigningJob {
		rawTx := createSendTransferSigningJobTestTx(
			t,
			wire.OutPoint{Hash: parentTx.TxHash(), Index: 0},
			900,
			refundScript,
			nil,
		)
		return createSendTransferUserSignedJob(t, rng, leaf.ID.String(), rawTx)
	}

	tests := []struct {
		name    string
		pkg     func() *pb.TransferPackage
		wantErr string
	}{
		{
			name: "cpfp leaves",
			pkg: func() *pb.TransferPackage {
				return &pb.TransferPackage{LeavesToSend: []*pb.UserSignedTxSigningJob{makeWrongJob()}}
			},
			wantErr: "build cpfp signing job",
		},
		{
			name: "direct leaves",
			pkg: func() *pb.TransferPackage {
				return &pb.TransferPackage{
					LeavesToSend:       []*pb.UserSignedTxSigningJob{makeValidJob(cpfpParentTx)},
					DirectLeavesToSend: []*pb.UserSignedTxSigningJob{makeWrongJob()},
				}
			},
			wantErr: "build direct signing job",
		},
		{
			name: "direct from cpfp leaves",
			pkg: func() *pb.TransferPackage {
				return &pb.TransferPackage{
					LeavesToSend:               []*pb.UserSignedTxSigningJob{makeValidJob(cpfpParentTx)},
					DirectLeavesToSend:         []*pb.UserSignedTxSigningJob{makeValidJob(directParentTx)},
					DirectFromCpfpLeavesToSend: []*pb.UserSignedTxSigningJob{makeWrongJob()},
				}
			},
			wantErr: "build direct-from-cpfp signing job",
		},
	}

	leafMap := map[string]*ent.TreeNode{leaf.ID.String(): leaf}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := buildSendTransferAggregationJobs(ctx, uuid.New(), tt.pkg(), leafMap)
			require.ErrorContains(t, err, tt.wantErr)
			require.ErrorContains(t, err, "refund tx input 0 must spend parent tx output 0")
		})
	}
}

func createSendTransferSigningJobTestLeaf(t *testing.T, rng io.Reader) (context.Context, *ent.TreeNode, *wire.MsgTx) {
	t.Helper()
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	client := sessionCtx.Client

	ownerIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	ownerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	signingKeyshare := createTestSigningKeyshare(t, ctx, rng, client)
	tree := createTestTreeForClaim(t, ctx, ownerIdentityPubKey, client)
	parentScript, err := common.P2TRScriptFromPubKey(ownerSigningPubKey)
	require.NoError(t, err)

	parentTx := wire.NewMsgTx(wire.TxVersion)
	parentTx.AddTxIn(wire.NewTxIn(&wire.OutPoint{Hash: [32]byte{0x41}, Index: 0}, nil, nil))
	parentTx.AddTxOut(wire.NewTxOut(1000, parentScript))
	parentTxRaw, err := common.SerializeTx(parentTx)
	require.NoError(t, err)

	leaf, err := client.TreeNode.Create().
		SetStatus(st.TreeNodeStatusTransferLocked).
		SetTree(tree).
		SetNetwork(btcnetwork.Regtest).
		SetSigningKeyshare(signingKeyshare).
		SetValue(1000).
		SetVerifyingPubkey(signingKeyshare.PublicKey.Add(ownerSigningPubKey)).
		SetOwnerIdentityPubkey(ownerIdentityPubKey).
		SetOwnerSigningPubkey(ownerSigningPubKey).
		SetRawTx(parentTxRaw).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)
	return ctx, leaf, parentTx
}

func createSendTransferSigningJobTestTx(t *testing.T, prevOut wire.OutPoint, value int64, pkScript []byte, extraPrevOut *wire.OutPoint) []byte {
	t.Helper()
	tx := wire.NewMsgTx(wire.TxVersion)
	tx.AddTxIn(wire.NewTxIn(&prevOut, nil, nil))
	if extraPrevOut != nil {
		tx.AddTxIn(wire.NewTxIn(extraPrevOut, nil, nil))
	}
	tx.AddTxOut(wire.NewTxOut(value, pkScript))
	raw, err := common.SerializeTx(tx)
	require.NoError(t, err)
	return raw
}

func createSendTransferUserSignedJob(t *testing.T, rng io.Reader, leafID string, rawTx []byte) *pb.UserSignedTxSigningJob {
	t.Helper()
	return &pb.UserSignedTxSigningJob{
		LeafId:                 leafID,
		SigningPublicKey:       keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize(),
		RawTx:                  rawTx,
		SigningNonceCommitment: createTestSigningCommitment(rng),
		SigningCommitments:     &pb.SigningCommitments{SigningCommitments: map[string]*pbcommon.SigningCommitment{"operator": createTestSigningCommitment(rng)}},
		UserSignature:          []byte{0x01},
	}
}
