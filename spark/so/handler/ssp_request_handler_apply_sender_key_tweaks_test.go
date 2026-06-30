//go:build lightspark

package handler

import (
	"math/big"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/stretchr/testify/require"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	secretsharing "github.com/lightsparkdev/spark/common/secret_sharing"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbssp "github.com/lightsparkdev/spark/proto/spark_ssp_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	enttransfer "github.com/lightsparkdev/spark/so/ent/transfer"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"google.golang.org/protobuf/proto"
)

func TestApplySenderKeyTweaks_RecoversApplyingSenderKeyTweak(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	dbTx, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	rng := rand.NewChaCha8([32]byte{9})
	senderPub := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	receiverPub := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	leaf := createDbLeaf(t, ctx, true)
	originalOwnerSigningPubkey := leaf.node.OwnerSigningPubkey

	transfer, err := dbTx.Transfer.Create().
		SetNetwork(btcnetwork.Regtest).
		SetType(st.TransferTypeTransfer).
		SetStatus(st.TransferStatusApplyingSenderKeyTweak).
		SetExpiryTime(time.Now().Add(24 * time.Hour)).
		SetTotalValue(1000).
		SetSenderIdentityPubkey(senderPub).
		SetReceiverIdentityPubkey(receiverPub).
		Save(ctx)
	require.NoError(t, err)

	// commitSenderKeyTweaks -> helper.TweakLeafKeyUpdate now validates
	// pubkey_shares_tweak against the full cluster's operator map (#6867),
	// so this test needs the real test-cluster config rather than a
	// hand-rolled stub. The leaf's key tweak is built with valid
	// polynomial-derived pubshares for every operator in that config.
	cfg := sparktesting.TestConfig(t)

	_, err = dbTx.TransferLeaf.Create().
		SetTransfer(transfer).
		SetLeaf(leaf.node).
		SetKeyTweak(mustMarshalSimpleSendLeafKeyTweak(t, rng, leaf.node.ID.String(), cfg)).
		SetPreviousRefundTx(leaf.node.RawRefundTx).
		SetIntermediateRefundTx(leaf.node.RawRefundTx).
		Save(ctx)
	require.NoError(t, err)

	sspHandler := NewSspRequestHandler(cfg)
	resp, err := sspHandler.ApplySenderKeyTweaks(ctx, &pbssp.ApplySenderKeyTweaksRequest{
		TransferIds: []string{transfer.ID.String()},
	})
	require.NoError(t, err)
	require.Equal(t, []string{transfer.ID.String()}, resp.GetUpdatedTransferIds())

	readDb, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	updatedTransfer, err := readDb.Transfer.Query().
		Where(enttransfer.IDEQ(transfer.ID)).
		Only(ctx)
	require.NoError(t, err)
	require.Equal(t, st.TransferStatusSenderKeyTweaked, updatedTransfer.Status)

	updatedLeafs, err := updatedTransfer.QueryTransferLeaves().All(ctx)
	require.NoError(t, err)
	require.Len(t, updatedLeafs, 1)
	require.Nil(t, updatedLeafs[0].KeyTweak)
	require.NotNil(t, updatedLeafs[0].SecretCipher)
	require.NotNil(t, updatedLeafs[0].Signature)

	updatedNode, err := updatedLeafs[0].QueryLeaf().Only(ctx)
	require.NoError(t, err)
	require.NotEqual(t, originalOwnerSigningPubkey, updatedNode.OwnerSigningPubkey)
}

func mustMarshalSimpleSendLeafKeyTweak(t *testing.T, rng *rand.ChaCha8, leafID string, cfg *so.Config) []byte {
	t.Helper()

	// Degree-0 polynomial g(x) = sharePriv. Every operator evaluates to
	// the same point sharePriv.Public(), which is what
	// helper.ValidatePubkeySharesTweak expects when Proofs = [sharePub].
	sharePriv := keys.MustGeneratePrivateKeyFromRand(rng)
	proofs := [][]byte{sharePriv.Public().Serialize()}

	fieldModulus := secp256k1.S256().N
	pubkeySharesTweak := make(map[string][]byte, len(cfg.SigningOperatorMap))
	for identifier, operator := range cfg.SigningOperatorMap {
		index := new(big.Int).SetUint64(operator.ID)
		index.Add(index, big.NewInt(1))
		pub, err := secretsharing.EvaluatePolynomialCommitment(proofs, index, fieldModulus)
		require.NoError(t, err)
		pubkeySharesTweak[identifier] = pub.Serialize()
	}

	keyTweakBinary, err := proto.Marshal(&pb.SendLeafKeyTweak{
		LeafId: leafID,
		SecretShareTweak: &pb.SecretShare{
			SecretShare: sharePriv.Serialize(),
			Proofs:      proofs,
		},
		PubkeySharesTweak: pubkeySharesTweak,
		SecretCipher:      []byte("valid-secret-cipher"),
		Sig:               &pb.SendLeafKeyTweak_Signature{Signature: []byte("valid-signature")},
	})
	require.NoError(t, err)
	return keyTweakBinary
}
