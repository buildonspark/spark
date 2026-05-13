package grpctest

import (
	"math/big"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/lightsparkdev/spark/common/keys"
	secretsharing "github.com/lightsparkdev/spark/common/secret_sharing"
	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/testing/wallet"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const duplicateLeafClaimAmountSats = 100_000

func buildDuplicateClaimLeafTweaksByOperator(
	t *testing.T,
	config *wallet.TestWalletConfig,
	leaf wallet.LeafKeyTweak,
) map[string]*sparkpb.ClaimLeafKeyTweak {
	t.Helper()

	privKeyTweak := leaf.SigningPrivKey.Sub(leaf.NewSigningPrivKey)
	shares, err := secretsharing.SplitSecretWithProofs(
		new(big.Int).SetBytes(privKeyTweak.Serialize()),
		secp256k1.S256().N,
		config.Threshold,
		len(config.SigningOperators),
	)
	require.NoError(t, err)

	pubkeySharesTweak := make(map[string]keys.Public, len(config.SigningOperators))
	for identifier, operator := range config.SigningOperators {
		share := findDuplicateClaimShare(shares, operator.ID)
		require.NotNil(t, share, "missing share for operator %s", identifier)

		shareTweak, err := keys.PrivateKeyFromBigInt(share.GetShare())
		require.NoError(t, err)
		pubkeySharesTweak[identifier] = shareTweak.Public()
	}

	leafTweaks := make(map[string]*sparkpb.ClaimLeafKeyTweak, len(config.SigningOperators))
	for identifier, operator := range config.SigningOperators {
		share := findDuplicateClaimShare(shares, operator.ID)
		require.NotNil(t, share, "missing share for operator %s", identifier)

		secretShareBytes := make([]byte, 32)
		share.Share.FillBytes(secretShareBytes)

		leafTweaks[identifier] = &sparkpb.ClaimLeafKeyTweak{
			LeafId: leaf.Leaf.Id,
			SecretShareTweak: &sparkpb.SecretShare{
				SecretShare: secretShareBytes,
				Proofs:      share.Proofs,
			},
			PubkeySharesTweak: keys.ToBytesMap(pubkeySharesTweak),
		}
	}

	return leafTweaks
}

func findDuplicateClaimShare(
	shares []*secretsharing.VerifiableSecretShare,
	operatorID uint64,
) *secretsharing.VerifiableSecretShare {
	targetShareIndex := big.NewInt(int64(operatorID + 1))
	for _, share := range shares {
		if share.Index.Cmp(targetShareIndex) == 0 {
			return share
		}
	}
	return nil
}

func findPendingTransferByID(t *testing.T, transfers []*sparkpb.Transfer, transferID string) *sparkpb.Transfer {
	t.Helper()

	for _, transfer := range transfers {
		if transfer.Id == transferID {
			return transfer
		}
	}
	t.Fatalf("transfer %s not found in pending transfers", transferID)
	return nil
}

// TestClaimTransferTweakKeys_DuplicateLeafIDsRejected_Regression verifies that the SO rejects
// ClaimTransferTweakKeys requests containing duplicate leaf IDs in a full end-to-end environment
// with real Bitcoin trees and transfers.
func TestClaimTransferTweakKeys_DuplicateLeafIDsRejected_Regression(t *testing.T) {
	senderIdentity := keys.GeneratePrivateKey()
	receiverIdentity := keys.GeneratePrivateKey()

	senderConfig := wallet.NewTestWalletConfigWithIdentityKey(t, senderIdentity)
	receiverConfig := wallet.NewTestWalletConfigWithIdentityKey(t, receiverIdentity)

	leafKey1 := keys.GeneratePrivateKey()
	leafKey2 := keys.GeneratePrivateKey()
	node1, err := wallet.CreateNewTree(senderConfig, faucet, leafKey1, duplicateLeafClaimAmountSats)
	require.NoError(t, err)
	node2, err := wallet.CreateNewTree(senderConfig, faucet, leafKey2, duplicateLeafClaimAmountSats)
	require.NoError(t, err)

	newLeafKey1 := keys.GeneratePrivateKey()
	newLeafKey2 := keys.GeneratePrivateKey()

	senderToken, err := wallet.AuthenticateWithServer(t.Context(), senderConfig)
	require.NoError(t, err)
	senderCtx := wallet.ContextWithToken(t.Context(), senderToken)

	transfer, err := wallet.SendTransferWithKeyTweaks(
		senderCtx,
		senderConfig,
		[]wallet.LeafKeyTweak{
			{Leaf: node1, SigningPrivKey: leafKey1, NewSigningPrivKey: newLeafKey1},
			{Leaf: node2, SigningPrivKey: leafKey2, NewSigningPrivKey: newLeafKey2},
		},
		receiverIdentity.Public(),
		time.Now().Add(10*time.Minute),
	)
	require.NoError(t, err)

	receiverToken, err := wallet.AuthenticateWithServer(t.Context(), receiverConfig)
	require.NoError(t, err)
	receiverCtx := wallet.ContextWithToken(t.Context(), receiverToken)

	pending, err := wallet.QueryPendingTransfers(receiverCtx, receiverConfig)
	require.NoError(t, err)
	receiverTransfer := findPendingTransferByID(t, pending.Transfers, transfer.Id)
	require.Len(t, receiverTransfer.Leaves, 2)

	receiverLeaves := make(map[string]*sparkpb.TreeNode, len(receiverTransfer.Leaves))
	for _, leaf := range receiverTransfer.Leaves {
		receiverLeaves[leaf.Leaf.Id] = leaf.Leaf
	}

	finalLeafKey1 := keys.GeneratePrivateKey()
	duplicateLeaf := wallet.LeafKeyTweak{
		Leaf:              receiverLeaves[node1.Id],
		SigningPrivKey:    newLeafKey1,
		NewSigningPrivKey: finalLeafKey1,
	}
	require.NotNil(t, duplicateLeaf.Leaf)

	duplicateTweaks := buildDuplicateClaimLeafTweaksByOperator(t, receiverConfig, duplicateLeaf)

	for identifier, operator := range receiverConfig.SigningOperators {
		conn, err := operator.NewOperatorGRPCConnection()
		require.NoError(t, err)

		token, err := wallet.AuthenticateWithConnection(t.Context(), receiverConfig, conn)
		require.NoError(t, err)
		ctx := wallet.ContextWithToken(t.Context(), token)
		grpcClient := sparkpb.NewSparkServiceClient(conn)

		_, err = grpcClient.ClaimTransferTweakKeys(ctx, &sparkpb.ClaimTransferTweakKeysRequest{
			TransferId:             transfer.Id,
			OwnerIdentityPublicKey: receiverConfig.IdentityPublicKey().Serialize(),
			LeavesToReceive: []*sparkpb.ClaimLeafKeyTweak{
				duplicateTweaks[identifier],
				duplicateTweaks[identifier],
			},
		})
		require.NoError(t, conn.Close())
		require.Error(t, err, "operator %s accepted duplicate leaf IDs", identifier)

		grpcStatus, ok := status.FromError(err)
		require.True(t, ok, "expected gRPC status error, got %T: %v", err, err)
		require.Equal(t, codes.InvalidArgument, grpcStatus.Code())
	}
}
