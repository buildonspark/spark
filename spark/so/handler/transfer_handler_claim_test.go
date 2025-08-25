//go:build gripmock
// +build gripmock

package handler

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/distributed-lab/gripmock"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	testutil "github.com/lightsparkdev/spark/testing"
)

func TestMain(m *testing.M) {
	err := gripmock.InitEmbeddedGripmock("../../../protos", []int{8535, 8536, 8537, 8538, 8539})
	if err != nil {
		panic(fmt.Sprintf("Failed to init embedded gripmock: %v", err))
	}
	defer gripmock.StopEmbeddedGripmock()

	os.Exit(m.Run())
}

var (
	frostRound1StubOutput = map[string]interface{}{
		"signing_commitments": []map[string]interface{}{
			{
				"binding": "AnRlc3RfYmluZGluZ19jb21taXRtZW50XzMzX19fAAAA",
				"hiding":  "AnRlc3RfaGlkaW5nX2NvbW1pdG1lbnRfMzNfX19fAAAA",
			},
			{
				"binding": "AnRlc3RfYmluZGluZ19jb21taXRtZW50XzMzX19fAAAA",
				"hiding":  "AnRlc3RfaGlkaW5nX2NvbW1pdG1lbnRfMzNfX19fAAAA",
			},
			{
				"binding": "AnRlc3RfYmluZGluZ19jb21taXRtZW50XzMzX19fAAAA",
				"hiding":  "AnRlc3RfaGlkaW5nX2NvbW1pdG1lbnRfMzNfX19fAAAA",
			},
		},
	}

	frostRound2StubOutput = map[string]interface{}{
		"results": map[string]interface{}{
			"operator1": map[string]interface{}{
				"signature_share": "dGVzdF9zaWduYXR1cmVfc2hhcmU=",
			},
			"operator2": map[string]interface{}{
				"signature_share": "dGVzdF9zaWduYXR1cmVfc2hhcmU=",
			},
		},
	}
)

func createValidBitcoinTxBytes(receiverPubKey []byte) []byte {
	pubkey, err := secp256k1.ParsePubKey(receiverPubKey)
	if err != nil {
		panic(err)
	}

	p2trScript, err := common.P2TRScriptFromPubKey(keys.PublicKeyFromKey(*pubkey))
	if err != nil {
		panic(err)
	}

	// sequence = 9000 = 0x2328 (little-endian: 28 23 00 00)
	scriptLen := fmt.Sprintf("%02x", len(p2trScript))
	hexStr := "02000000010000000000000000000000000000000000000000000000000000000000000000ffffffff002823000001e803000000000000" +
		scriptLen +
		hex.EncodeToString(p2trScript) +
		"000000000000000000000000000000000000000000"
	bytes, _ := hex.DecodeString(hexStr)
	return bytes
}

func generateFixedKeyPair(idx byte) (privKey32 []byte, pubKey33 []byte) {
	seed := [32]byte{
		0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
		0x09, 0x0A, 0x0B, 0x0C, 0x0D, 0x0E, 0x0F, 0x10,
		0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18,
		0x19, 0x1A, 0x1B, 0x1C, 0x1D, 0x1E, 0x1F, idx,
	}
	priv := secp256k1.PrivKeyFromBytes(seed[:])
	return priv.Serialize(), priv.PubKey().SerializeCompressed()
}

func make33ByteKey(prefix string) []byte {
	key := make([]byte, 33)
	key[0] = 0x02
	copy(key[1:], []byte(prefix))
	return key
}

func make32ByteKey(prefix string) []byte {
	key := make([]byte, 32)
	copy(key, []byte(prefix))
	return key
}

func setupPgTestContext(t *testing.T) (context.Context, *db.TestContext) {
	dsn, stop := db.SpinUpPostgres(t)
	t.Cleanup(stop)

	ctx := context.Background()
	ctx, sessionCtx, err := db.NewPgTestContext(t, ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(sessionCtx.Close)

	return ctx, sessionCtx
}

func createTestSigningKeyshare(t *testing.T, ctx context.Context, client *ent.Client) *ent.SigningKeyshare {
	_, keysharePub := generateFixedKeyPair(3)
	keysharePriv, _ := generateFixedKeyPair(3)
	_, pubSharePub := generateFixedKeyPair(4)

	signingKeyshare, err := client.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusInUse).
		SetSecretShare(keysharePriv).
		SetPublicShares(map[string][]byte{"operator1": pubSharePub}).
		SetPublicKey(keysharePub).
		SetMinSigners(2).
		SetCoordinatorIndex(0).
		Save(ctx)
	require.NoError(t, err)
	return signingKeyshare
}

func createTestTreeForClaim(t *testing.T, ctx context.Context, client *ent.Client) *ent.Tree {
	_, ownerPub := generateFixedKeyPair(1)

	tree, err := client.Tree.Create().
		SetStatus(st.TreeStatusAvailable).
		SetNetwork(st.NetworkRegtest).
		SetOwnerIdentityPubkey(ownerPub).
		SetBaseTxid([]byte("test_base_txid")).
		SetVout(0).
		Save(ctx)
	require.NoError(t, err)
	return tree
}

func createTestTreeNode(t *testing.T, ctx context.Context, client *ent.Client, tree *ent.Tree, keyshare *ent.SigningKeyshare) *ent.TreeNode {
	_, verifyingPub := generateFixedKeyPair(5)
	_, ownerPub := generateFixedKeyPair(1)
	_, ownerSigningPub := generateFixedKeyPair(2)

	validTx := createOldBitcoinTxBytes(ownerPub)

	leaf, err := client.TreeNode.Create().
		SetStatus(st.TreeNodeStatusTransferLocked).
		SetTree(tree).
		SetSigningKeyshare(keyshare).
		SetValue(1000).
		SetVerifyingPubkey(verifyingPub).
		SetOwnerIdentityPubkey(ownerPub).
		SetOwnerSigningPubkey(ownerSigningPub).
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

func createTestTransfer(t *testing.T, ctx context.Context, client *ent.Client, status st.TransferStatus) *ent.Transfer {
	_, senderPub := generateFixedKeyPair(2)
	_, receiverPub := generateFixedKeyPair(1)

	transfer, err := client.Transfer.Create().
		SetStatus(status).
		SetType(st.TransferTypeTransfer).
		SetSenderIdentityPubkey(senderPub).
		SetReceiverIdentityPubkey(receiverPub).
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

func createTestSigningCommitment() *pbcommon.SigningCommitment {
	return &pbcommon.SigningCommitment{
		Binding: make33ByteKey("test_binding_commitment_33___"),
		Hiding:  make33ByteKey("test_hiding_commitment_33____"),
	}
}

func createTestLeafRefundTxSigningJob(leaf *ent.TreeNode) *pb.LeafRefundTxSigningJob {
	receiverPubKey := leaf.OwnerIdentityPubkey
	validTxBytes := createValidBitcoinTxBytes(receiverPubKey)

	return &pb.LeafRefundTxSigningJob{
		LeafId: leaf.ID.String(),
		RefundTxSigningJob: &pb.SigningJob{
			SigningPublicKey:       make33ByteKey("test_signing_pubkey_33_bytes"),
			RawTx:                  validTxBytes,
			SigningNonceCommitment: createTestSigningCommitment(),
		},
		DirectRefundTxSigningJob: &pb.SigningJob{
			SigningPublicKey:       make33ByteKey("test_direct_signing_33_bytes"),
			RawTx:                  validTxBytes,
			SigningNonceCommitment: createTestSigningCommitment(),
		},
		DirectFromCpfpRefundTxSigningJob: &pb.SigningJob{
			SigningPublicKey:       make33ByteKey("test_direct_cpfp_signing_33"),
			RawTx:                  validTxBytes,
			SigningNonceCommitment: createTestSigningCommitment(),
		},
	}
}

func TestClaimTransferSignRefunds_Success(t *testing.T) {
	err := gripmock.AddStub("spark_internal.SparkInternalService", "initiate_settle_receiver_key_tweak", nil, nil)
	require.NoError(t, err, "Failed to add initiate_settle_receiver_key_tweak stub")

	err = gripmock.AddStub("spark_internal.SparkInternalService", "settle_receiver_key_tweak", nil, nil)
	require.NoError(t, err, "Failed to add settle_receiver_key_tweak stub")

	err = gripmock.AddStub("spark_internal.SparkInternalService", "frost_round1", nil, frostRound1StubOutput)
	require.NoError(t, err, "Failed to add frost_round1 stub")

	err = gripmock.AddStub("spark_internal.SparkInternalService", "frost_round2", nil, frostRound2StubOutput)
	require.NoError(t, err, "Failed to add frost_round2 stub")

	ctx, sessionCtx := setupPgTestContext(t)

	keyshare := createTestSigningKeyshare(t, ctx, sessionCtx.Client)
	tree := createTestTreeForClaim(t, ctx, sessionCtx.Client)
	leaf := createTestTreeNode(t, ctx, sessionCtx.Client, tree, keyshare)
	transfer := createTestTransfer(t, ctx, sessionCtx.Client, st.TransferStatusReceiverKeyTweaked)
	transferLeaf := createTestTransferLeaf(t, ctx, sessionCtx.Client, transfer, leaf)

	tweakPriv, tweakPub := generateFixedKeyPair(17)
	_, pubkeyShareTweakPub := generateFixedKeyPair(18)

	claimKeyTweak := &pb.ClaimLeafKeyTweak{
		SecretShareTweak: &pb.SecretShare{
			SecretShare: tweakPriv,
			Proofs:      [][]byte{tweakPub},
		},
		PubkeySharesTweak: map[string][]byte{
			"operator1": pubkeyShareTweakPub,
		},
	}

	claimKeyTweakBytes, err := proto.Marshal(claimKeyTweak)
	require.NoError(t, err)

	_, err = transferLeaf.Update().SetKeyTweak(claimKeyTweakBytes).Save(ctx)
	require.NoError(t, err)

	cfg, err := testutil.TestConfig()
	require.NoError(t, err)

	handler := NewTransferHandler(cfg)

	req := &pb.ClaimTransferSignRefundsRequest{
		TransferId:             transfer.ID.String(),
		OwnerIdentityPublicKey: transfer.ReceiverIdentityPubkey,
		SigningJobs: []*pb.LeafRefundTxSigningJob{
			createTestLeafRefundTxSigningJob(leaf),
		},
	}

	resp, err := handler.ClaimTransferSignRefunds(ctx, req)

	t.Logf("Response: %v, Error: %v", resp, err)

	assert.NoError(t, err)
	assert.NotNil(t, resp)

	updatedTransfer, err := sessionCtx.Client.Transfer.Get(ctx, transfer.ID)
	require.NoError(t, err)
	assert.Equal(t, st.TransferStatusReceiverKeyTweakApplied, updatedTransfer.Status)
}
