package handler

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"math/rand/v2"
	"testing"
	"time"

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
			"operator1": map[string]any{
				"signature_share": signatureShare,
			},
			"operator2": map[string]any{
				"signature_share": signatureShare,
			},
		},
	}
)

func createValidBitcoinTxBytes(t *testing.T, receiverPubKey keys.Public) []byte {
	p2trScript, err := common.P2TRScriptFromPubKey(receiverPubKey)
	require.NoError(t, err)

	// sequence = 9000 = 0x2328 (little-endian: 28 23 00 00)
	scriptLen := fmt.Sprintf("%02x", len(p2trScript))
	hexStr := "02000000010000000000000000000000000000000000000000000000000000000000000000ffffffff002823000001e803000000000000" +
		scriptLen +
		hex.EncodeToString(p2trScript) +
		"000000000000000000000000000000000000000000"
	bytes, _ := hex.DecodeString(hexStr)
	return bytes
}

func createTestSigningKeyshare(t *testing.T, ctx context.Context, rng io.Reader, client *ent.Client) *ent.SigningKeyshare {
	keysharePrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	pubSharePrivKey := keys.MustGeneratePrivateKeyFromRand(rng)

	signingKeyshare, err := client.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusInUse).
		SetSecretShare(keysharePrivKey.Serialize()).
		SetPublicShares(map[string]keys.Public{"operator1": pubSharePrivKey.Public()}).
		SetPublicKey(keysharePrivKey.Public()).
		SetMinSigners(2).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)
	return signingKeyshare
}

func createTestTreeForClaim(t *testing.T, ctx context.Context, ownerIdentityPubKey keys.Public, client *ent.Client) *ent.Tree {
	tree, err := client.Tree.Create().
		SetStatus(st.TreeStatusAvailable).
		SetNetwork(st.NetworkRegtest).
		SetOwnerIdentityPubkey(ownerIdentityPubKey).
		SetBaseTxid([]byte("test_base_txid")).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)
	return tree
}

func createTestTreeNode(t *testing.T, ctx context.Context, rng io.Reader, client *ent.Client, tree *ent.Tree, keyshare *ent.SigningKeyshare) *ent.TreeNode {
	verifyingPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	ownerPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	ownerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	validTx := createOldBitcoinTxBytes(t, ownerPubKey)

	leaf, err := client.TreeNode.Create().
		SetStatus(st.TreeNodeStatusTransferLocked).
		SetTree(tree).
		SetSigningKeyshare(keyshare).
		SetValue(1000).
		SetVerifyingPubkey(verifyingPubKey.Serialize()).
		SetOwnerIdentityPubkey(ownerPubKey.Serialize()).
		SetOwnerSigningPubkey(ownerSigningPubKey.Serialize()).
		SetRawTx(validTx).
		SetRawRefundTx(validTx).
		SetDirectTx(validTx).
		SetDirectRefundTx(validTx).
		SetDirectFromCpfpRefundTx(validTx).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)
	return leaf
}

func createTestTransfer(t *testing.T, ctx context.Context, rng io.Reader, client *ent.Client, status st.TransferStatus) *ent.Transfer {
	senderPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	receiverPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	transfer, err := client.Transfer.Create().
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
		SetPreviousRefundTx([]byte("test_previous_refund_tx")).
		SetIntermediateRefundTx([]byte("test_intermediate_refund_tx")).
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

func createTestLeafRefundTxSigningJob(t *testing.T, rng io.Reader, leaf *ent.TreeNode) *pb.LeafRefundTxSigningJob {
	receiverPubKey, err := keys.ParsePublicKey(leaf.OwnerIdentityPubkey)
	require.NoError(t, err)
	validTxBytes := createValidBitcoinTxBytes(t, receiverPubKey)

	return &pb.LeafRefundTxSigningJob{
		LeafId: leaf.ID.String(),
		RefundTxSigningJob: &pb.SigningJob{
			SigningPublicKey:       keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize(),
			RawTx:                  validTxBytes,
			SigningNonceCommitment: createTestSigningCommitment(rng),
		},
		DirectRefundTxSigningJob: &pb.SigningJob{
			SigningPublicKey:       keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize(),
			RawTx:                  validTxBytes,
			SigningNonceCommitment: createTestSigningCommitment(rng),
		},
		DirectFromCpfpRefundTxSigningJob: &pb.SigningJob{
			SigningPublicKey:       keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize(),
			RawTx:                  validTxBytes,
			SigningNonceCommitment: createTestSigningCommitment(rng),
		},
	}
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
	tweakPubKey := tweakPrivKey.Public()
	pubkeyShareTweakPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	claimKeyTweak := &pb.ClaimLeafKeyTweak{
		SecretShareTweak: &pb.SecretShare{
			SecretShare: tweakPrivKey.Serialize(),
			Proofs:      [][]byte{tweakPubKey.Serialize()},
		},
		PubkeySharesTweak: map[string][]byte{
			"operator1": pubkeyShareTweakPubKey.Serialize(),
		},
	}

	claimKeyTweakBytes, err := proto.Marshal(claimKeyTweak)
	require.NoError(t, err)

	_, err = transferLeaf.Update().SetKeyTweak(claimKeyTweakBytes).Save(ctx)
	require.NoError(t, err)

	cfg := sparktesting.TestConfig(t)
	req := &pb.ClaimTransferSignRefundsRequest{
		TransferId:             transfer.ID.String(),
		OwnerIdentityPublicKey: transfer.ReceiverIdentityPubkey.Serialize(),
		SigningJobs: []*pb.LeafRefundTxSigningJob{
			createTestLeafRefundTxSigningJob(t, rng, leaf),
		},
	}
	handler := NewTransferHandler(cfg)
	resp, err := handler.ClaimTransferSignRefunds(ctx, req)

	require.NoError(t, err)
	assert.NotNil(t, resp)

	updatedTransfer, err := sessionCtx.Client.Transfer.Get(ctx, transfer.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TransferStatusReceiverKeyTweakApplied, updatedTransfer.Status)
}
