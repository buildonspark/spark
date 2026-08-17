package transfer

import (
	"strings"
	"testing"

	eciesgo "github.com/ecies/go/v2"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/proto/spark"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// sealTestPayload encrypts an MpcSealedSharePayload to the given identity key, as a sub-user would.
func sealTestPayload(t *testing.T, recipient keys.Public, payload *spark.MpcSealedSharePayload) []byte {
	t.Helper()
	plaintext, err := proto.Marshal(payload)
	require.NoError(t, err)
	pub, err := eciesgo.NewPublicKeyFromBytes(recipient.Serialize())
	require.NoError(t, err)
	blob, err := eciesgo.Encrypt(pub, plaintext)
	require.NoError(t, err)
	return blob
}

// unsealFixture builds a parsed submission whose sealed entry for one operator is genuinely encrypted, seeded from
// the shared request fixture (two leaves, positions {1, 3}).
func unsealFixture(t *testing.T, mutatePayload func(position uint32, payload *spark.MpcSealedSharePayload)) (*MpcSubmission, keys.Private) {
	t.Helper()
	operatorKey := keys.GeneratePrivateKey()
	req := validMpcRequest(t)

	shares := make([]*spark.MpcSealedShare, len(testMpcPositions))
	for k, position := range testMpcPositions {
		payload := &spark.MpcSealedSharePayload{TransferId: req.GetTransferId()}
		for _, leaf := range req.GetMpcTransferPackage().GetLeaves() {
			payload.LeafShares = append(payload.LeafShares, &spark.MpcLeafSubShare{
				LeafId:      leaf.GetLeafId(),
				SecretShare: append([]byte{byte(position)}, make([]byte, 31)...),
			})
		}
		if mutatePayload != nil {
			mutatePayload(position, payload)
		}
		shares[k] = &spark.MpcSealedShare{Ecies: sealTestPayload(t, operatorKey.Public(), payload)}
	}
	req.GetMpcTransferPackage().GetKeyTweaks()[testOperatorID] = &spark.MpcOperatorShares{Shares: shares}

	submission, err := ParseMpcSubmission(req)
	require.NoError(t, err)
	return submission, operatorKey
}

func TestUnsealShares_Honest(t *testing.T) {
	submission, operatorKey := unsealFixture(t, nil)

	byLeaf, err := submission.UnsealShares(testOperatorID, operatorKey)
	require.NoError(t, err)

	require.Len(t, byLeaf, 2)
	for _, leaf := range submission.Leaves() {
		shares := byLeaf[leaf.LeafID()]
		require.Len(t, shares, len(testMpcPositions))
		for _, position := range testMpcPositions {
			assert.Equal(t, byte(position), shares[position][0])
			assert.Len(t, shares[position], 32)
		}
	}
}

func TestUnsealShares_Rejections(t *testing.T) {
	t.Run("no entry for this operator", func(t *testing.T) {
		submission, operatorKey := unsealFixture(t, nil)
		_, err := submission.UnsealShares("ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", operatorKey)
		require.ErrorIs(t, err, ErrMpcMissingOperatorEntry)
	})

	t.Run("wrong recipient key", func(t *testing.T) {
		submission, _ := unsealFixture(t, nil)
		_, err := submission.UnsealShares(testOperatorID, keys.GeneratePrivateKey())
		require.ErrorIs(t, err, ErrMpcUnsealFailed)
	})

	t.Run("sealed transfer id differs", func(t *testing.T) {
		submission, operatorKey := unsealFixture(t, func(position uint32, payload *spark.MpcSealedSharePayload) {
			if position == 3 {
				payload.TransferId = uuid.NewString()
			}
		})
		_, err := submission.UnsealShares(testOperatorID, operatorKey)
		require.ErrorIs(t, err, ErrMpcSealedReplayMismatch)
		assert.ErrorContains(t, err, "position 3")
	})

	t.Run("huge sealed transfer id is truncated in the error", func(t *testing.T) {
		submission, operatorKey := unsealFixture(t, func(position uint32, payload *spark.MpcSealedSharePayload) {
			payload.TransferId = strings.Repeat("A", 100_000)
		})
		_, err := submission.UnsealShares(testOperatorID, operatorKey)
		require.ErrorIs(t, err, ErrMpcSealedReplayMismatch)
		assert.Less(t, len(err.Error()), 256)
		assert.ErrorContains(t, err, "…")
	})

	t.Run("lowest bad position is reported when several are bad", func(t *testing.T) {
		submission, operatorKey := unsealFixture(t, func(position uint32, payload *spark.MpcSealedSharePayload) {
			payload.TransferId = uuid.NewString()
		})
		_, err := submission.UnsealShares(testOperatorID, operatorKey)
		require.ErrorIs(t, err, ErrMpcSealedReplayMismatch)
		assert.ErrorContains(t, err, "position 1")
	})

	t.Run("missing leaf", func(t *testing.T) {
		submission, operatorKey := unsealFixture(t, func(position uint32, payload *spark.MpcSealedSharePayload) {
			payload.LeafShares = payload.GetLeafShares()[:1]
		})
		_, err := submission.UnsealShares(testOperatorID, operatorKey)
		require.ErrorIs(t, err, ErrMpcSealedLeafSetMismatch)
	})

	t.Run("unknown leaf", func(t *testing.T) {
		foreign := uuid.NewString()
		submission, operatorKey := unsealFixture(t, func(position uint32, payload *spark.MpcSealedSharePayload) {
			payload.LeafShares[0].LeafId = foreign
		})
		_, err := submission.UnsealShares(testOperatorID, operatorKey)
		require.ErrorIs(t, err, ErrMpcSealedLeafSetMismatch)
	})

	t.Run("duplicate leaf", func(t *testing.T) {
		submission, operatorKey := unsealFixture(t, func(position uint32, payload *spark.MpcSealedSharePayload) {
			payload.LeafShares[1].LeafId = payload.GetLeafShares()[0].GetLeafId()
		})
		_, err := submission.UnsealShares(testOperatorID, operatorKey)
		require.ErrorIs(t, err, ErrMpcSealedLeafSetMismatch)
	})
}
