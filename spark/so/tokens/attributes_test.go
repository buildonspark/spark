package tokens

import (
	"bytes"
	"crypto/rand"
	"testing"

	"github.com/google/uuid"
	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func randomBytes(n int) []byte {
	b := make([]byte, n)
	_, err := rand.Read(b)
	if err != nil {
		panic(err)
	}
	return b
}

func makeMinimalV3MintProto() *tokenpb.TokenTransaction {
	issuer := randomBytes(33)
	owner := randomBytes(33)
	operator := randomBytes(33)
	tokenID := randomBytes(32)
	amount := randomBytes(16)
	revocation := randomBytes(33)
	outputID := "test-output-id"
	bondSats := uint64(1000)
	locktime := uint64(144)

	return &tokenpb.TokenTransaction{
		Version: 3,
		TokenInputs: &tokenpb.TokenTransaction_MintInput{
			MintInput: &tokenpb.TokenMintInput{
				IssuerPublicKey: issuer,
				TokenIdentifier: tokenID,
			},
		},
		TokenOutputs: []*tokenpb.TokenOutput{{
			Id:                            &outputID,
			OwnerPublicKey:                owner,
			RevocationCommitment:          revocation,
			TokenIdentifier:               tokenID,
			TokenAmount:                   amount,
			WithdrawBondSats:              &bondSats,
			WithdrawRelativeBlockLocktime: &locktime,
		}},
		SparkOperatorIdentityPublicKeys: [][]byte{operator},
		Network:                         sparkpb.Network_MAINNET,
		ClientCreatedTimestamp:          timestamppb.Now(),
		ExpiryTime:                      timestamppb.Now(),
	}
}

func makeMinimalV3CreateProto() *tokenpb.TokenTransaction {
	issuer := randomBytes(33)
	operator := randomBytes(33)
	creationEntity := randomBytes(33)
	maxSupply := randomBytes(16)

	return &tokenpb.TokenTransaction{
		Version: 3,
		TokenInputs: &tokenpb.TokenTransaction_CreateInput{
			CreateInput: &tokenpb.TokenCreateInput{
				IssuerPublicKey:         issuer,
				TokenName:               "TEST",
				TokenTicker:             "TST",
				Decimals:                8,
				MaxSupply:               maxSupply,
				IsFreezable:             true,
				CreationEntityPublicKey: creationEntity,
			},
		},
		TokenOutputs:                    []*tokenpb.TokenOutput{},
		SparkOperatorIdentityPublicKeys: [][]byte{operator},
		Network:                         sparkpb.Network_MAINNET,
		ClientCreatedTimestamp:          timestamppb.Now(),
		ExpiryTime:                      timestamppb.Now(),
	}
}

func makeMinimalV3TransferProto(prevTxHash []byte, useTokenIdentifier bool) *tokenpb.TokenTransaction {
	receiver := randomBytes(33)
	operator := randomBytes(33)
	tokenID := randomBytes(32)
	amount := randomBytes(16)
	revocation := randomBytes(33)
	outputID := "test-output-id"
	bondSats := uint64(1000)
	locktime := uint64(144)

	out := &tokenpb.TokenOutput{
		Id:                            &outputID,
		OwnerPublicKey:                receiver,
		RevocationCommitment:          revocation,
		TokenAmount:                   amount,
		TokenIdentifier:               tokenID,
		WithdrawBondSats:              &bondSats,
		WithdrawRelativeBlockLocktime: &locktime,
	}

	return &tokenpb.TokenTransaction{
		Version: 3,
		TokenInputs: &tokenpb.TokenTransaction_TransferInput{
			TransferInput: &tokenpb.TokenTransferInput{
				OutputsToSpend: []*tokenpb.TokenOutputToSpend{{
					PrevTokenTransactionHash: prevTxHash,
					PrevTokenTransactionVout: 0,
				}},
			},
		},
		TokenOutputs:                    []*tokenpb.TokenOutput{out},
		SparkOperatorIdentityPublicKeys: [][]byte{operator},
		Network:                         sparkpb.Network_MAINNET,
		ClientCreatedTimestamp:          timestamppb.Now(),
		ExpiryTime:                      timestamppb.Now(),
	}
}

func makePartialV3MintProto() *tokenpb.TokenTransaction {
	issuer := randomBytes(33)
	owner := randomBytes(33)
	operator := randomBytes(33)
	tokenID := randomBytes(32)
	amount := randomBytes(16)

	return &tokenpb.TokenTransaction{
		Version: 3,
		TokenInputs: &tokenpb.TokenTransaction_MintInput{
			MintInput: &tokenpb.TokenMintInput{
				IssuerPublicKey: issuer,
				TokenIdentifier: tokenID,
			},
		},
		TokenOutputs: []*tokenpb.TokenOutput{{
			OwnerPublicKey:  owner,
			TokenIdentifier: tokenID,
			TokenAmount:     amount,
			// Missing SO-filled fields: Id, RevocationCommitment, WithdrawBondSats, WithdrawRelativeBlockLocktime
		}},
		SparkOperatorIdentityPublicKeys: [][]byte{operator},
		Network:                         sparkpb.Network_MAINNET,
		ClientCreatedTimestamp:          timestamppb.Now(),
		// Missing SO-filled field: ExpiryTime
	}
}

func TestGetTokenTxAttrStringsFromEnt_PartialOnly(t *testing.T) {
	tx := &ent.TokenTransaction{ID: uuid.New()}
	// 32-byte partial hash
	tx.PartialTokenTransactionHash = bytes.Repeat([]byte{0x01}, 32)

	attrs := GetTokenTxAttrStringsFromEnt(t.Context(), tx)
	// Ent inference returns TRANSFER for minimal structs without edges
	require.Equal(t, "TRANSFER", attrs.Type)
	require.Len(t, attrs.PartialHashHex, 64)
	require.Equal(t, "unknown", attrs.FinalHashHex)
}

func TestGetTokenTxAttrStringsFromEnt_Finalized(t *testing.T) {
	tx := &ent.TokenTransaction{ID: uuid.New()}
	tx.PartialTokenTransactionHash = bytes.Repeat([]byte{0xaa}, 32)
	tx.FinalizedTokenTransactionHash = bytes.Repeat([]byte{0xbb}, 32)

	attrs := GetTokenTxAttrStringsFromEnt(t.Context(), tx)
	require.Equal(t, "TRANSFER", attrs.Type)
	require.Len(t, attrs.PartialHashHex, 64)
	require.Len(t, attrs.FinalHashHex, 64)
}

func TestGetTokenTxAttrStringsFromProto_V3Minimal(t *testing.T) {
	tx := makeMinimalV3MintProto()
	attrs := GetTokenTxAttrStringsFromProto(t.Context(), tx)
	require.Equal(t, "MINT", attrs.Type)
	require.Len(t, attrs.PartialHashHex, 64)
	// Now has all SO-filled fields (expiry_time, revocation_commitment, etc.), so final hash computes
	require.Len(t, attrs.FinalHashHex, 64)
}

func TestGetTokenTxAttrStringsFromProto_CreateType(t *testing.T) {
	tx := makeMinimalV3CreateProto()
	attrs := GetTokenTxAttrStringsFromProto(t.Context(), tx)
	require.Equal(t, "CREATE", attrs.Type)
	require.Len(t, attrs.PartialHashHex, 64)
	// Now has all SO-filled fields (expiry_time, creation_entity_public_key), so final hash computes
	require.Len(t, attrs.FinalHashHex, 64)
}

func TestGetTokenTxAttrStringsFromProto_TransferType(t *testing.T) {
	prev := bytes.Repeat([]byte{0xAB}, 32)
	tx := makeMinimalV3TransferProto(prev, true)
	attrs := GetTokenTxAttrStringsFromProto(t.Context(), tx)
	require.Equal(t, "TRANSFER", attrs.Type)
	require.Len(t, attrs.PartialHashHex, 64)
	// Now has all SO-filled fields (expiry_time, revocation_commitment, etc.), so final hash computes
	require.Len(t, attrs.FinalHashHex, 64)
}

func TestGetTokenTxAttrStringsFromProto_PartialTransaction(t *testing.T) {
	// Create a partial mint transaction without SO-filled fields
	partialTx := makePartialV3MintProto()

	attrs := GetTokenTxAttrStringsFromProto(t.Context(), partialTx)
	require.Equal(t, "MINT", attrs.Type)
	require.Len(t, attrs.PartialHashHex, 64, "Partial hash should compute for partial transaction")
	require.Equal(t, "unknown", attrs.FinalHashHex, "Final hash should be unknown for partial transaction")
}
