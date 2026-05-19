package handler

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"math/big"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	secretsharing "github.com/lightsparkdev/spark/common/secret_sharing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/distributed-lab/gripmock"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestMain(m *testing.M) {
	if sparktesting.IsGripmock() {
		err := gripmock.InitEmbeddedGripmock("../../../protos", []int{8535, 8536, 8537, 8538, 8539})
		if err != nil {
			panic(fmt.Sprintf("Failed to init embedded gripmock: %v", err))
		}
		defer gripmock.StopEmbeddedGripmock()
	}

	stop := db.StartPostgresServer()
	defer stop()

	m.Run()
}

var (
	bindingCommitment     = base64.StdEncoding.EncodeToString([]byte("\x02test_binding_commitment_33___\x00\x00\x00"))
	hidingCommitment      = base64.StdEncoding.EncodeToString([]byte("\x02test_binding_commitment_33___\x00\x00\x00"))
	frostRound1StubOutput = map[string]any{
		"signing_commitments": []map[string]any{
			{
				"binding": bindingCommitment,
				"hiding":  hidingCommitment,
			},
			{
				"binding": bindingCommitment,
				"hiding":  hidingCommitment,
			},
			{
				"binding": bindingCommitment,
				"hiding":  hidingCommitment,
			},
		},
	}

	signatureShare = base64.StdEncoding.EncodeToString([]byte("test_signature_share"))

	frostRound2StubOutput = map[string]any{
		"results": map[string]any{
			"a99a8b7c-8bd2-40ee-893b-aeefb00f1bf8": map[string]any{
				"signature_share": signatureShare,
			},
			"43579ecc-d5a4-4115-80b7-fe86f8ac4586": map[string]any{
				"signature_share": signatureShare,
			},
		},
	}
)

func createValidBitcoinTxBytes(t *testing.T, receiverPubKey keys.Public) []byte {
	return createValidBitcoinTxBytesWithSequence(t, receiverPubKey, 9000)
}

func createValidBitcoinTxBytesWithSequence(t *testing.T, receiverPubKey keys.Public, sequence uint32) []byte {
	p2trScript, err := common.P2TRScriptFromPubKey(receiverPubKey)
	require.NoError(t, err)

	tx := wire.NewMsgTx(3)

	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{
			Hash:  chainhash.Hash{},
			Index: 0xffffffff,
		},
		Sequence: sequence,
	})

	tx.AddTxOut(&wire.TxOut{
		Value:    1000,
		PkScript: p2trScript,
	})

	var buf bytes.Buffer
	err = tx.Serialize(&buf)
	require.NoError(t, err)

	return buf.Bytes()
}

func createTestSigningKeyshare(t *testing.T, ctx context.Context, rng io.Reader, client *ent.Client) *ent.SigningKeyshare {
	keysharePrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	pubSharePrivKey := keys.MustGeneratePrivateKeyFromRand(rng)

	signingKeyshare, err := client.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusInUse).
		SetSecretShare(keysharePrivKey).
		SetPublicShares(map[string]keys.Public{"operator1": pubSharePrivKey.Public()}).
		SetPublicKey(keysharePrivKey.Public()).
		SetMinSigners(2).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)
	return signingKeyshare
}

func createTestTreeForClaim(t *testing.T, ctx context.Context, ownerIdentityPubKey keys.Public, client *ent.Client) *ent.Tree {
	baseTxid := st.NewRandomTxIDForTesting(t)

	tree, err := client.Tree.Create().
		SetStatus(st.TreeStatusAvailable).
		SetNetwork(btcnetwork.Regtest).
		SetOwnerIdentityPubkey(ownerIdentityPubKey).
		SetBaseTxid(baseTxid).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)
	return tree
}

func createTestTreeNode(t *testing.T, ctx context.Context, rng io.Reader, client *ent.Client, tree *ent.Tree, keyshare *ent.SigningKeyshare) *ent.TreeNode {
	verifyingPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	ownerPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	ownerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	leafAmount := int64(1000)
	leafVout := int16(0)
	parentTxBytes, parentTxHash := createVersion3ParentTx(t, ownerPubKey, leafAmount, uint32(leafVout))

	cpfpRefundTx := createVersion3CPFPRefundTx(t, parentTxHash, uint32(leafVout), ownerPubKey, leafAmount, (1<<30)|1900)

	directRefundTx := createVersion3DirectRefundTx(t, parentTxHash, uint32(leafVout), ownerPubKey, leafAmount, (1<<30)|1900)

	directFromCpfpRefundTx := createVersion3DirectRefundTx(t, parentTxHash, uint32(leafVout), ownerPubKey, leafAmount, (1<<30)|1900)

	leaf, err := client.TreeNode.Create().
		SetStatus(st.TreeNodeStatusTransferLocked).
		SetTree(tree).
		SetNetwork(tree.Network).
		SetSigningKeyshare(keyshare).
		SetValue(uint64(leafAmount)).
		SetVerifyingPubkey(verifyingPubKey).
		SetOwnerIdentityPubkey(ownerPubKey).
		SetOwnerSigningPubkey(ownerSigningPubKey).
		SetRawTx(parentTxBytes).
		SetRawRefundTx(cpfpRefundTx).
		SetDirectTx(parentTxBytes).
		SetDirectRefundTx(directRefundTx).
		SetDirectFromCpfpRefundTx(directFromCpfpRefundTx).
		SetVout(leafVout).
		Save(ctx)
	require.NoError(t, err)
	return leaf
}

func createTestTransfer(t *testing.T, ctx context.Context, rng io.Reader, client *ent.Client, status st.TransferStatus) *ent.Transfer {
	senderPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	receiverPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	transfer, err := client.Transfer.Create().
		SetNetwork(btcnetwork.Regtest).
		SetStatus(status).
		SetType(st.TransferTypeTransfer).
		SetSenderIdentityPubkey(senderPubKey).
		SetReceiverIdentityPubkey(receiverPubKey).
		SetTotalValue(1000).
		SetExpiryTime(time.Now().Add(24 * time.Hour)).
		Save(ctx)
	require.NoError(t, err)
	return transfer
}

func createTestTransferLeaf(t *testing.T, ctx context.Context, client *ent.Client, transfer *ent.Transfer, leaf *ent.TreeNode) *ent.TransferLeaf {
	transferLeaf, err := client.TransferLeaf.Create().
		SetTransfer(transfer).
		SetLeaf(leaf).
		SetPreviousRefundTx(createTestTxBytes(t, 2000)).
		SetIntermediateRefundTx(createTestTxBytes(t, 2001)).
		Save(ctx)
	require.NoError(t, err)
	return transferLeaf
}

func createTestSigningCommitment(rng io.Reader) *pbcommon.SigningCommitment {
	return &pbcommon.SigningCommitment{
		Binding: keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize(),
		Hiding:  keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize(),
	}
}

// createRefundTxBytes creates a refund transaction that spends from the given sourceTxBytes
func createRefundTxBytes(t *testing.T, sourceTxBytes []byte, receiverPubKey keys.Public, sequence uint32, isWatchtowerTx bool) []byte {
	p2trScript, err := common.P2TRScriptFromPubKey(receiverPubKey)
	require.NoError(t, err)

	// Parse source transaction to get the txid and output value
	sourceTx, err := common.TxFromRawTxBytes(sourceTxBytes)
	require.NoError(t, err)
	require.NotEmpty(t, sourceTx.TxOut, "source transaction must have outputs")

	sourceValue := sourceTx.TxOut[0].Value

	// Calculate refund amount based on whether this is a watchtower tx
	var refundAmount int64
	if isWatchtowerTx {
		refundAmount = common.MaybeApplyFee(sourceValue)
	} else {
		refundAmount = sourceValue
	}

	tx := wire.NewMsgTx(3) // Version 3

	// Add input spending from the previous transaction
	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{
			Hash:  sourceTx.TxHash(),
			Index: 0,
		},
		Sequence: sequence,
	})

	// Add output with the receiver's P2TR script
	tx.AddTxOut(&wire.TxOut{
		Value:    refundAmount,
		PkScript: p2trScript,
	})

	// CPFP transactions have an ephemeral anchor output
	if !isWatchtowerTx {
		tx.AddTxOut(common.EphemeralAnchorOutput())
	}

	var buf bytes.Buffer
	err = tx.Serialize(&buf)
	require.NoError(t, err)

	return buf.Bytes()
}

// createTestLeafRefundTxSigningJob builds a signing job whose SigningPublicKey and
// tx-output destination both use destPubKey. validateReceivedRefundTransactions now
// enforces SigningPublicKey == leaf.OwnerSigningPubkey, and SettleReceiverKeyTweak
// rewrites leaf.OwnerSigningPubkey to leaf.VerifyingPubkey.Sub(tweakedKeyshare.PublicKey)
// (= VerifyingPubkey - keyshare.PublicKey - pubKeyTweak) during the claim flow, so
// callers must compute and pass that post-settle value here.
func createTestLeafRefundTxSigningJob(t *testing.T, rng io.Reader, leaf *ent.TreeNode, destPubKey keys.Public) *pb.LeafRefundTxSigningJob {
	rawRefundTx, err := common.TxFromRawTxBytes(leaf.RawRefundTx)
	require.NoError(t, err)
	require.NotEmpty(t, rawRefundTx.TxIn)

	currentTimelock := rawRefundTx.TxIn[0].Sequence & 0xFFFF

	expectedCpfpTimelock := currentTimelock - 100
	expectedDirectTimelock := expectedCpfpTimelock + 50

	pubKey := destPubKey

	// Create transactions that spend from the correct UTXOs
	cpfpTxBytes := createRefundTxBytes(t, leaf.RawTx, pubKey, expectedCpfpTimelock, false)
	directTxBytes := createRefundTxBytes(t, leaf.DirectTx, pubKey, expectedDirectTimelock, true)
	directFromCpfpTxBytes := createRefundTxBytes(t, leaf.RawTx, pubKey, expectedDirectTimelock, true)

	return &pb.LeafRefundTxSigningJob{
		LeafId: leaf.ID.String(),
		RefundTxSigningJob: &pb.SigningJob{
			SigningPublicKey:       pubKey.Serialize(),
			RawTx:                  cpfpTxBytes,
			SigningNonceCommitment: createTestSigningCommitment(rng),
		},
		DirectRefundTxSigningJob: &pb.SigningJob{
			SigningPublicKey:       pubKey.Serialize(),
			RawTx:                  directTxBytes,
			SigningNonceCommitment: createTestSigningCommitment(rng),
		},
		DirectFromCpfpRefundTxSigningJob: &pb.SigningJob{
			SigningPublicKey:       pubKey.Serialize(),
			RawTx:                  directFromCpfpTxBytes,
			SigningNonceCommitment: createTestSigningCommitment(rng),
		},
	}
}

// createTestTreeNodeForValidation creates a TreeNode with valid transactions for validateReceivedRefundTransactions tests
func createTestTreeNodeForValidation(t *testing.T, rng io.Reader, ownerPubKey keys.Public) *ent.TreeNode {
	leafAmount := int64(1000)
	leafVout := int16(0)

	// Create parent tx (RawTx)
	parentTxBytes, parentTxHash := createVersion3ParentTx(t, ownerPubKey, leafAmount, uint32(leafVout))

	// Create direct tx (same as parent tx for simplicity)
	directTxBytes, _ := createVersion3ParentTx(t, ownerPubKey, leafAmount, uint32(leafVout))

	// Create existing CPFP refund tx with timelock
	cpfpTimelock := uint32((1 << 30) | 1900)
	cpfpRefundTx := createVersion3CPFPRefundTx(t, parentTxHash, uint32(leafVout), ownerPubKey, leafAmount, cpfpTimelock)

	// Create a mock TreeNode (not persisted to DB, just for unit testing the validation function)
	return &ent.TreeNode{
		RawTx:              parentTxBytes,
		DirectTx:           directTxBytes,
		RawRefundTx:        cpfpRefundTx,
		Vout:               leafVout,
		OwnerSigningPubkey: ownerPubKey,
	}
}

// createValidSigningJobForLeaf creates a LeafRefundTxSigningJob with valid transactions that should pass validation
func createValidSigningJobForLeaf(t *testing.T, rng io.Reader, leaf *ent.TreeNode, isSwap bool) *pb.LeafRefundTxSigningJob {
	return createSigningJobForLeafWithDest(t, rng, leaf, leaf.OwnerSigningPubkey, isSwap)
}

// createSigningJobForLeafWithDest builds a LeafRefundTxSigningJob whose SigningPublicKey
// and tx-output destination both use refundDestPubKey. Pass leaf.OwnerSigningPubkey for
// the canonical/valid case; pass any other key to simulate a client that consistently
// supplies a non-canonical destination (the shape that downstream byte-comparison alone
// does not catch).
func createSigningJobForLeafWithDest(t *testing.T, rng io.Reader, leaf *ent.TreeNode, refundDestPubKey keys.Public, isSwap bool) *pb.LeafRefundTxSigningJob {
	// Parse existing refund tx to get the current timelock
	rawRefundTx, err := common.TxFromRawTxBytes(leaf.RawRefundTx)
	require.NoError(t, err)
	require.NotEmpty(t, rawRefundTx.TxIn)

	currentTimelock := rawRefundTx.TxIn[0].Sequence & 0xFFFF

	// New timelock should be TimeLockInterval shorter
	expectedCpfpTimelock := currentTimelock - 100       // spark.TimeLockInterval is 100
	expectedDirectTimelock := expectedCpfpTimelock + 50 // spark.DirectTimelockOffset is 50

	// Create new refund transactions
	cpfpTxBytes := createRefundTxBytes(t, leaf.RawTx, refundDestPubKey, expectedCpfpTimelock, false)

	job := &pb.LeafRefundTxSigningJob{
		LeafId: "test-leaf-id",
		RefundTxSigningJob: &pb.SigningJob{
			SigningPublicKey:       refundDestPubKey.Serialize(),
			RawTx:                  cpfpTxBytes,
			SigningNonceCommitment: createTestSigningCommitment(rng),
		},
	}

	// For transfers (not swaps), we also need direct refund txs
	if !isSwap {
		directFromCpfpTxBytes := createRefundTxBytes(t, leaf.RawTx, refundDestPubKey, expectedDirectTimelock, true)
		job.DirectFromCpfpRefundTxSigningJob = &pb.SigningJob{
			SigningPublicKey:       refundDestPubKey.Serialize(),
			RawTx:                  directFromCpfpTxBytes,
			SigningNonceCommitment: createTestSigningCommitment(rng),
		}

		// DirectRefundTx is optional but let's include it
		directTxBytes := createRefundTxBytes(t, leaf.DirectTx, refundDestPubKey, expectedDirectTimelock, true)
		job.DirectRefundTxSigningJob = &pb.SigningJob{
			SigningPublicKey:       refundDestPubKey.Serialize(),
			RawTx:                  directTxBytes,
			SigningNonceCommitment: createTestSigningCommitment(rng),
		}
	}

	return job
}

func TestValidateReceivedRefundTransactions_Transfer_Success(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{1})
	ctx, _ := db.ConnectToTestPostgres(t)
	ownerPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	leaf := createTestTreeNodeForValidation(t, rng, ownerPubKey)
	job := createValidSigningJobForLeaf(t, rng, leaf, false /* isSwap */)

	err := validateReceivedRefundTransactions(ctx, job, leaf, st.TransferTypeTransfer /* isSwap */)
	require.NoError(t, err)
}

func TestValidateReceivedRefundTransactions_Swap_Success(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{2})
	ctx, _ := db.ConnectToTestPostgres(t)
	ownerPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	leaf := createTestTreeNodeForValidation(t, rng, ownerPubKey)
	job := createValidSigningJobForLeaf(t, rng, leaf, true /* isSwap */)

	err := validateReceivedRefundTransactions(ctx, job, leaf, st.TransferTypeSwap /* isSwap */)
	require.NoError(t, err)
}

// TestValidateReceivedRefundTransactions_RejectsNonCanonicalDestination simulates the
// exploit shape: the client consistently supplies an attacker-controlled key in both
// the SigningPublicKey field AND the tx-output destination. The downstream
// reconstruct-and-compare in validateSingleLeafRefundTxs reconstructs from the same
// caller-supplied SigningPublicKey, so a self-consistent payload passes that check.
// Only the explicit equality against leaf.OwnerSigningPubkey catches it.
func TestValidateReceivedRefundTransactions_RejectsNonCanonicalDestination(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{99})
	ctx, _ := db.ConnectToTestPostgres(t)
	ownerPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	leaf := createTestTreeNodeForValidation(t, rng, ownerPubKey)

	attackerPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	job := createSigningJobForLeafWithDest(t, rng, leaf, attackerPubKey, false /* isSwap */)

	err := validateReceivedRefundTransactions(ctx, job, leaf, st.TransferTypeTransfer /* isSwap */)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match leaf owner signing pubkey")
}

func TestValidateReceivedRefundTransactions_RetrySkipsValidation(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{3})
	ctx, _ := db.ConnectToTestPostgres(t)
	ownerPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	leaf := createTestTreeNodeForValidation(t, rng, ownerPubKey)

	// Create a job where RawTx matches the existing RawRefundTx in the leaf
	// This simulates a retry scenario
	job := &pb.LeafRefundTxSigningJob{
		LeafId: "test-leaf-id",
		RefundTxSigningJob: &pb.SigningJob{
			SigningPublicKey:       ownerPubKey.Serialize(),
			RawTx:                  leaf.RawRefundTx, // Same as leaf.RawRefundTx
			SigningNonceCommitment: createTestSigningCommitment(rng),
		},
	}

	// When bytes.Equal(job.RefundTxSigningJob.RawTx, leaf.RawRefundTx) is true,
	// validation should be skipped and return nil
	err := validateReceivedRefundTransactions(ctx, job, leaf, st.TransferTypeTransfer /* isSwap */)
	require.NoError(t, err)

	// Also works for swap
	err = validateReceivedRefundTransactions(ctx, job, leaf, st.TransferTypeSwap /* isSwap */)
	require.NoError(t, err)
}

func TestValidateReceivedRefundTransactions_RetryWithDifferentDirectTx_RunsValidation(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{8})
	ctx, _ := db.ConnectToTestPostgres(t)
	ownerPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	leaf := createTestTreeNodeForValidation(t, rng, ownerPubKey)

	// Simulate that direct refund txs have already been stored from a previous call
	// by setting DirectRefundTx and DirectFromCpfpRefundTx on the leaf
	validDirectRefundTx := []byte("valid-direct-refund-tx")
	validDirectFromCpfpRefundTx := []byte("valid-direct-from-cpfp-refund-tx")
	leaf.DirectRefundTx = validDirectRefundTx
	leaf.DirectFromCpfpRefundTx = validDirectFromCpfpRefundTx

	// Create a job where RefundTx matches (would trigger old retry behavior)
	// but DirectRefundTx is different (the exploit attempt)
	maliciousDirectRefundTx := []byte("malicious-direct-refund-tx")
	maliciousDirectFromCpfpRefundTx := []byte("malicious-direct-from-cpfp-refund-tx")

	job := &pb.LeafRefundTxSigningJob{
		LeafId: "test-leaf-id",
		RefundTxSigningJob: &pb.SigningJob{
			SigningPublicKey:       ownerPubKey.Serialize(),
			RawTx:                  leaf.RawRefundTx, // Same as leaf - would trigger old retry
			SigningNonceCommitment: createTestSigningCommitment(rng),
		},
		DirectRefundTxSigningJob: &pb.SigningJob{
			SigningPublicKey:       ownerPubKey.Serialize(),
			RawTx:                  maliciousDirectRefundTx, // DIFFERENT - exploit attempt
			SigningNonceCommitment: createTestSigningCommitment(rng),
		},
		DirectFromCpfpRefundTxSigningJob: &pb.SigningJob{
			SigningPublicKey:       ownerPubKey.Serialize(),
			RawTx:                  maliciousDirectFromCpfpRefundTx, // DIFFERENT - exploit attempt
			SigningNonceCommitment: createTestSigningCommitment(rng),
		},
	}

	// This should NOT be treated as a retry because DirectRefundTx differs.
	err := validateReceivedRefundTransactions(ctx, job, leaf, st.TransferTypeTransfer)
	require.Error(t, err, "Expected validation to run and fail for mismatched direct txs, but it passed (retry detection bypassed validation)")
}

func TestValidateRefundSigningRetryMatchesStoredRejectsChangedRefundTx(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{9})
	ownerPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	leaf := createTestTreeNodeForValidation(t, rng, ownerPubKey)

	job := &pb.LeafRefundTxSigningJob{
		LeafId: leaf.ID.String(),
		RefundTxSigningJob: &pb.SigningJob{
			RawTx: append(append([]byte(nil), leaf.RawRefundTx...), 0x01),
		},
	}

	err := validateRefundSigningRetryMatchesStored(job, leaf)
	require.ErrorContains(t, err, "must not change refund transaction")
}

func TestValidateReceivedRefundTransactions_MissingRefundTxSigningJob(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{4})
	ctx, _ := db.ConnectToTestPostgres(t)
	ownerPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	leaf := createTestTreeNodeForValidation(t, rng, ownerPubKey)

	// Job without RefundTxSigningJob
	job := &pb.LeafRefundTxSigningJob{
		LeafId:             "test-leaf-id",
		RefundTxSigningJob: nil,
	}

	err := validateReceivedRefundTransactions(ctx, job, leaf, st.TransferTypeTransfer /* isSwap */)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing RefundTxSigningJob")
}

func TestValidateReceivedRefundTransactions_Transfer_MissingDirectFromCpfp(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{5})
	ctx, _ := db.ConnectToTestPostgres(t)
	ownerPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	leaf := createTestTreeNodeForValidation(t, rng, ownerPubKey)

	// Parse existing refund tx to get the current timelock
	rawRefundTx, err := common.TxFromRawTxBytes(leaf.RawRefundTx)
	require.NoError(t, err)
	currentTimelock := rawRefundTx.TxIn[0].Sequence & 0xFFFF
	expectedCpfpTimelock := currentTimelock - 100

	refundDestPubKey := leaf.OwnerSigningPubkey
	cpfpTxBytes := createRefundTxBytes(t, leaf.RawTx, refundDestPubKey, expectedCpfpTimelock, false)

	// Job with only CPFP refund tx but no DirectFromCpfp (required for transfers)
	job := &pb.LeafRefundTxSigningJob{
		LeafId: "test-leaf-id",
		RefundTxSigningJob: &pb.SigningJob{
			SigningPublicKey:       refundDestPubKey.Serialize(),
			RawTx:                  cpfpTxBytes,
			SigningNonceCommitment: createTestSigningCommitment(rng),
		},
		// DirectFromCpfpRefundTxSigningJob is nil - this is required for transfers
	}

	err = validateReceivedRefundTransactions(ctx, job, leaf, st.TransferTypeTransfer /* isSwap */)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "direct from CPFP refund tx")
}

func TestValidateReceivedRefundTransactions_Swap_DoesNotRequireDirectTx(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{6})
	ctx, _ := db.ConnectToTestPostgres(t)
	ownerPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	leaf := createTestTreeNodeForValidation(t, rng, ownerPubKey)

	// Parse existing refund tx to get the current timelock
	rawRefundTx, err := common.TxFromRawTxBytes(leaf.RawRefundTx)
	require.NoError(t, err)
	currentTimelock := rawRefundTx.TxIn[0].Sequence & 0xFFFF
	expectedCpfpTimelock := currentTimelock - 100

	refundDestPubKey := leaf.OwnerSigningPubkey
	cpfpTxBytes := createRefundTxBytes(t, leaf.RawTx, refundDestPubKey, expectedCpfpTimelock, false)

	// Job with only CPFP refund tx - this is sufficient for swaps
	job := &pb.LeafRefundTxSigningJob{
		LeafId: uuid.NewString(),
		RefundTxSigningJob: &pb.SigningJob{
			SigningPublicKey:       refundDestPubKey.Serialize(),
			RawTx:                  cpfpTxBytes,
			SigningNonceCommitment: createTestSigningCommitment(rng),
		},
	}

	// For swaps, only CPFP refund tx is required
	err = validateReceivedRefundTransactions(ctx, job, leaf, st.TransferTypeSwap /* isSwap */)
	require.NoError(t, err)
}

func TestClaimTransferSignRefunds_Success(t *testing.T) {
	sparktesting.RequireGripMock(t)
	ctx, sessionCtx := db.ConnectToTestPostgres(t)

	err := gripmock.AddStub("spark_internal.SparkInternalService", "initiate_settle_receiver_key_tweak", nil, nil)
	require.NoError(t, err, "Failed to add initiate_settle_receiver_key_tweak stub")

	err = gripmock.AddStub("spark_internal.SparkInternalService", "settle_receiver_key_tweak", nil, nil)
	require.NoError(t, err, "Failed to add settle_receiver_key_tweak stub")

	err = gripmock.AddStub("spark_internal.SparkInternalService", "frost_round1", nil, frostRound1StubOutput)
	require.NoError(t, err, "Failed to add frost_round1 stub")

	err = gripmock.AddStub("spark_internal.SparkInternalService", "frost_round2", nil, frostRound2StubOutput)
	require.NoError(t, err, "Failed to add frost_round2 stub")

	rng := rand.NewChaCha8([32]byte{})
	keyshare := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)
	ownerIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	tree := createTestTreeForClaim(t, ctx, ownerIdentityPrivKey.Public(), sessionCtx.Client)
	leaf := createTestTreeNode(t, ctx, rng, sessionCtx.Client, tree, keyshare)
	transfer := createTestTransfer(t, ctx, rng, sessionCtx.Client, st.TransferStatusReceiverKeyTweaked)
	transferLeaf := createTestTransferLeaf(t, ctx, sessionCtx.Client, transfer, leaf)

	tweakPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	secretInt := new(big.Int).SetBytes(tweakPrivKey.Serialize())

	cfg := sparktesting.TestConfig(t)
	threshold := int(cfg.Threshold)
	numberOfShares := len(cfg.SigningOperatorMap)

	// Create proper VSS shares with correct number of proofs matching the threshold
	shares, err := secretsharing.SplitSecretWithProofs(secretInt, secp256k1.S256().N, threshold, numberOfShares)
	require.NoError(t, err)
	require.NotEmpty(t, shares, "expected at least one share")

	// Use the first share (all shares have the same proofs)
	share := shares[0]
	secretShareBytes := make([]byte, 32)
	share.Share.FillBytes(secretShareBytes)

	// pubkey_shares_tweak entries must equal f(operator.ID+1)·G where f is
	// the polynomial committed to by share.Proofs — helper.ValidatePubkeySharesTweak
	// rejects anything else (added in #6867 to close the prod divergence).
	pubkeySharesTweak := buildValidPubkeySharesTweak(t, cfg, share.Proofs)

	claimKeyTweak := &pb.ClaimLeafKeyTweak{
		SecretShareTweak: &pb.SecretShare{
			SecretShare: secretShareBytes,
			Proofs:      share.Proofs,
		},
		PubkeySharesTweak: pubkeySharesTweak,
	}

	claimKeyTweakBytes, err := proto.Marshal(claimKeyTweak)
	require.NoError(t, err)

	_, err = transferLeaf.Update().SetKeyTweak(claimKeyTweakBytes).Save(ctx)
	require.NoError(t, err)

	postSettleOwnerKey := leaf.VerifyingPubkey.Sub(keyshare.PublicKey).Sub(tweakPrivKey.Public())
	req := &pb.ClaimTransferSignRefundsRequest{
		TransferId:             transfer.ID.String(),
		OwnerIdentityPublicKey: transfer.ReceiverIdentityPubkey.Serialize(),
		SigningJobs: []*pb.LeafRefundTxSigningJob{
			createTestLeafRefundTxSigningJob(t, rng, leaf, postSettleOwnerKey),
		},
	}
	handler := NewTransferHandler(cfg)
	resp, err := handler.ClaimTransferSignRefunds(ctx, req)

	require.NoError(t, err)
	assert.NotNil(t, resp)

	// Read back from the session's transaction. ClaimTransferSignRefunds no
	// longer calls entTx.Commit() inside InitiateSettleReceiverKeyTweak or
	// SettleReceiverKeyTweak — those mid-flow commits used to release the
	// FOR UPDATE row lock between Phase 1 SELF and Phase 2 SELF, letting a
	// concurrent ROLLBACK flip the coordinator back to SENDER_KEY_TWEAKED.
	// The handler's transaction is now committed by the gRPC middleware on
	// return; in this in-process test we read via ent.GetDbFromContext to
	// see the still-open tx state.
	//
	// The final status is ReceiverRefundSigned because the handler advances
	// past ReceiverKeyTweakApplied (the post-settle state) to RRS at the
	// status-update step that previously sat after the mid-flow commit;
	// asserting RKA only used to pass because the bare-client read could
	// only see the explicitly-committed-state, not the further in-tx update.
	txClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	updatedTransfer, err := txClient.Transfer.Get(ctx, transfer.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TransferStatusReceiverRefundSigned, updatedTransfer.Status)
}

func TestClaimTransferSignRefundsV2RejectsNotFoundAndInvalidStatus(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	rng := rand.NewChaCha8([32]byte{})
	cfg := sparktesting.TestConfig(t)
	handler := NewTransferHandler(cfg)

	receiverIdentity := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	t.Run("missing transfer", func(t *testing.T) {
		_, err := handler.ClaimTransferSignRefundsV2(ctx, &pb.ClaimTransferSignRefundsRequest{
			TransferId:             uuid.New().String(),
			OwnerIdentityPublicKey: receiverIdentity.Serialize(),
		})

		require.Error(t, err)
		require.Equal(t, codes.NotFound, status.Code(err))
	})

	t.Run("sender key tweaked transfer", func(t *testing.T) {
		transfer := createTestTransfer(t, ctx, rng, sessionCtx.Client, st.TransferStatusSenderKeyTweaked)

		_, err := handler.ClaimTransferSignRefundsV2(ctx, &pb.ClaimTransferSignRefundsRequest{
			TransferId:             transfer.ID.String(),
			OwnerIdentityPublicKey: transfer.ReceiverIdentityPubkey.Serialize(),
		})

		require.Error(t, err)
		require.Equal(t, codes.FailedPrecondition, status.Code(err))
		require.ErrorContains(t, err, "expected to be at status")
	})
}
func TestClaimTransferSignRefunds_RejectsChangedRefundTxAfterRefundSigned(t *testing.T) {
	ctx, client, req, handler, transfer, leaves := setupClaimTransferSignRefundsFixture(t, 1)
	defer func() { _ = gripmock.Clear() }()

	leaf := leaves[0]
	job := req.SigningJobs[0]
	_, err := leaf.Update().
		SetRawRefundTx(job.RefundTxSigningJob.RawTx).
		SetDirectRefundTx(job.DirectRefundTxSigningJob.RawTx).
		SetDirectFromCpfpRefundTx(job.DirectFromCpfpRefundTxSigningJob.RawTx).
		Save(ctx)
	require.NoError(t, err)
	_, err = transfer.Update().SetStatus(st.TransferStatusReceiverRefundSigned).Save(ctx)
	require.NoError(t, err)

	job.RefundTxSigningJob.RawTx = append(append([]byte(nil), job.RefundTxSigningJob.RawTx...), 0x01)

	_, err = handler.ClaimTransferSignRefunds(ctx, req)
	require.ErrorContains(t, err, "must not change refund transaction")

	refreshed, err := client.Transfer.Get(ctx, transfer.ID)
	require.NoError(t, err)
	require.Equal(t, st.TransferStatusReceiverRefundSigned, refreshed.Status)
}

func setupClaimTransferSignRefundsFixture(t *testing.T, leafCount int) (context.Context, *ent.Client, *pb.ClaimTransferSignRefundsRequest, *TransferHandler, *ent.Transfer, []*ent.TreeNode) {
	t.Helper()
	sparktesting.RequireGripMock(t)
	ctx, sessionCtx := db.ConnectToTestPostgres(t)

	err := gripmock.AddStub("spark_internal.SparkInternalService", "initiate_settle_receiver_key_tweak", nil, nil)
	require.NoError(t, err, "Failed to add initiate_settle_receiver_key_tweak stub")

	err = gripmock.AddStub("spark_internal.SparkInternalService", "settle_receiver_key_tweak", nil, nil)
	require.NoError(t, err, "Failed to add settle_receiver_key_tweak stub")

	err = gripmock.AddStub("spark_internal.SparkInternalService", "frost_round1", nil, frostRound1StubOutput)
	require.NoError(t, err, "Failed to add frost_round1 stub")

	err = gripmock.AddStub("spark_internal.SparkInternalService", "frost_round2", nil, frostRound2StubOutput)
	require.NoError(t, err, "Failed to add frost_round2 stub")

	rng := rand.NewChaCha8([32]byte{})
	keyshare := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)
	ownerIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	tree := createTestTreeForClaim(t, ctx, ownerIdentityPrivKey.Public(), sessionCtx.Client)
	transfer := createTestTransfer(t, ctx, rng, sessionCtx.Client, st.TransferStatusReceiverKeyTweaked)
	if leafCount > 1 {
		_, err = transfer.Update().SetTotalValue(uint64(leafCount) * 1000).Save(ctx)
		require.NoError(t, err)
	}

	leaves := make([]*ent.TreeNode, 0, leafCount)
	signingJobs := make([]*pb.LeafRefundTxSigningJob, 0, leafCount)
	for range leafCount {
		leaf := createTestTreeNode(t, ctx, rng, sessionCtx.Client, tree, keyshare)
		transferLeaf := createTestTransferLeaf(t, ctx, sessionCtx.Client, transfer, leaf)
		tweakPubKey := setTestClaimKeyTweak(t, ctx, rng, transferLeaf)
		postSettleOwnerKey := leaf.VerifyingPubkey.Sub(keyshare.PublicKey).Sub(tweakPubKey)
		leaves = append(leaves, leaf)
		signingJobs = append(signingJobs, createTestLeafRefundTxSigningJob(t, rng, leaf, postSettleOwnerKey))
	}

	req := &pb.ClaimTransferSignRefundsRequest{
		TransferId:             transfer.ID.String(),
		OwnerIdentityPublicKey: transfer.ReceiverIdentityPubkey.Serialize(),
		SigningJobs:            signingJobs,
	}
	return ctx, sessionCtx.Client, req, NewTransferHandler(sparktesting.TestConfig(t)), transfer, leaves
}

func setTestClaimKeyTweak(t *testing.T, ctx context.Context, rng io.Reader, transferLeaf *ent.TransferLeaf) keys.Public {
	t.Helper()
	tweakPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	secretInt := new(big.Int).SetBytes(tweakPrivKey.Serialize())
	pubkeyShareTweakPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	cfg := sparktesting.TestConfig(t)
	threshold := int(cfg.Threshold)
	numberOfShares := len(cfg.SigningOperatorMap)

	shares, err := secretsharing.SplitSecretWithProofs(secretInt, secp256k1.S256().N, threshold, numberOfShares)
	require.NoError(t, err)
	require.NotEmpty(t, shares, "expected at least one share")

	share := shares[0]
	secretShareBytes := make([]byte, 32)
	share.Share.FillBytes(secretShareBytes)

	claimKeyTweak := &pb.ClaimLeafKeyTweak{
		SecretShareTweak: &pb.SecretShare{
			SecretShare: secretShareBytes,
			Proofs:      share.Proofs,
		},
		PubkeySharesTweak: map[string][]byte{
			"operator1": pubkeyShareTweakPubKey.Serialize(),
		},
	}

	claimKeyTweakBytes, err := proto.Marshal(claimKeyTweak)
	require.NoError(t, err)

	_, err = transferLeaf.Update().SetKeyTweak(claimKeyTweakBytes).Save(ctx)
	require.NoError(t, err)
	return tweakPrivKey.Public()
}
