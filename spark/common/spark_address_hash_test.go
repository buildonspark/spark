package common

import (
	"bytes"
	"encoding/hex"
	"math/big"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
)

func TestHashSparkInvoiceFields_TokensBasic(t *testing.T) {
	identityPublicKey, err := hex.DecodeString("02ccb26ba79c63aaf60c9192fd874be3087ae8d8703275df0e558704a6d3a4f132")
	require.NoError(t, err)
	senderPublicKey, err := keys.ParsePublicKey(identityPublicKey)
	require.NoError(t, err)

	uid, err := uuid.NewV7()
	require.NoError(t, err)
	id := uid[:]

	tokenIdentifier, err := hex.DecodeString("9cef64327b1c1f18eb4b4944fc70a1fe9dd84d9084c7daae751de535baafd49f")
	require.NoError(t, err)

	amount := big.NewInt(1000).Bytes()
	memo := "memo-1"
	expiry := time.Now().Add(2 * time.Hour).UTC()

	network := Regtest
	receiverPublicKey, err := keys.ParsePublicKey(identityPublicKey)
	require.NoError(t, err)

	f1 := CreateTokenSparkInvoiceFields(id, tokenIdentifier, amount, &memo, senderPublicKey, &expiry)

	h1, err := HashSparkInvoiceFields(f1, network, receiverPublicKey)
	require.NoError(t, err)
	require.Len(t, h1, 32)

	// Same values -> same hash
	f2 := CreateTokenSparkInvoiceFields(append([]byte{}, id...), append([]byte{}, tokenIdentifier...), append([]byte{}, amount...), &memo, senderPublicKey, &expiry)
	h2, err := HashSparkInvoiceFields(f2, network, receiverPublicKey)
	require.NoError(t, err)
	require.True(t, bytes.Equal(h1, h2))

	// Change memo -> different hash
	memo2 := "memo-2"
	f3 := CreateTokenSparkInvoiceFields(id, tokenIdentifier, amount, &memo2, senderPublicKey, &expiry)
	h3, err := HashSparkInvoiceFields(f3, network, receiverPublicKey)
	require.NoError(t, err)
	require.False(t, bytes.Equal(h1, h3))
}

func TestHashSparkInvoiceFields_SatsBasic(t *testing.T) {
	identityPublicKey, err := hex.DecodeString("02ccb26ba79c63aaf60c9192fd874be3087ae8d8703275df0e558704a6d3a4f132")
	require.NoError(t, err)
	senderPublicKey, err := keys.ParsePublicKey(identityPublicKey)
	require.NoError(t, err)

	uid, err := uuid.NewV7()
	require.NoError(t, err)
	id := uid[:]

	var sats uint64 = 1000
	memo := "sats-memo"
	expiry := time.Now().Add(1 * time.Hour).UTC()

	network := Regtest
	receiverPublicKey, err := keys.ParsePublicKey(identityPublicKey)
	require.NoError(t, err)

	f1 := CreateSatsSparkInvoiceFields(id, &sats, &memo, senderPublicKey, &expiry)

	h1, err := HashSparkInvoiceFields(f1, network, receiverPublicKey)
	require.NoError(t, err)
	require.Len(t, h1, 32)

	// Change amount -> different hash
	newsats := uint64(2000)
	f2 := CreateSatsSparkInvoiceFields(id, &newsats, &memo, senderPublicKey, &expiry)
	h2, err := HashSparkInvoiceFields(f2, network, receiverPublicKey)
	require.NoError(t, err)
	require.False(t, bytes.Equal(h1, h2))
}

func TestHashSparkInvoiceFields_InvalidInputs(t *testing.T) {
	identityPublicKey, _ := hex.DecodeString("02ccb26ba79c63aaf60c9192fd874be3087ae8d8703275df0e558704a6d3a4f132")
	senderPublicKey, err := keys.ParsePublicKey(identityPublicKey)
	require.NoError(t, err)
	uid, _ := uuid.NewV7()
	id := uid[:]
	memo := "m"

	network := Regtest
	receiverPublicKey, err := keys.ParsePublicKey(identityPublicKey)
	require.NoError(t, err)

	// nil fields
	_, err = HashSparkInvoiceFields(nil, network, receiverPublicKey)
	require.Error(t, err)

	// bad id length
	fBadID := CreateSatsSparkInvoiceFields([]byte{1, 2, 3}, nil, &memo, senderPublicKey, nil)
	_, err = HashSparkInvoiceFields(fBadID, network, receiverPublicKey)
	require.Error(t, err)

	// tokens: bad token identifier length (not 32)
	fBadTokenID := CreateTokenSparkInvoiceFields(id, make([]byte, 31), nil, &memo, senderPublicKey, nil)
	_, err = HashSparkInvoiceFields(fBadTokenID, network, receiverPublicKey)
	require.Error(t, err)

	// tokens: amount too large (>16 bytes)
	fBigAmt := CreateTokenSparkInvoiceFields(id, make([]byte, 32), make([]byte, 17), &memo, senderPublicKey, nil)
	_, err = HashSparkInvoiceFields(fBigAmt, network, receiverPublicKey)
	require.Error(t, err)

	// missing payment type
	fMissingPT := &pb.SparkInvoiceFields{Version: 1, Id: id, Memo: &memo, SenderPublicKey: identityPublicKey}
	_, err = HashSparkInvoiceFields(fMissingPT, network, receiverPublicKey)
	require.Error(t, err)
}

func TestHashSparkInvoiceFields_EmptyAndNilEquivalences(t *testing.T) {
	identityPublicKey, _ := hex.DecodeString("02ccb26ba79c63aaf60c9192fd874be3087ae8d8703275df0e558704a6d3a4f132")
	senderPublicKey, err := keys.ParsePublicKey(identityPublicKey)
	require.NoError(t, err)
	uid, _ := uuid.NewV7()
	id := uid[:]

	network := Regtest
	receiverPublicKey, err := keys.ParsePublicKey(identityPublicKey)
	require.NoError(t, err)

	// tokens: tokenIdentifier nil vs empty slice
	amt := big.NewInt(123).Bytes()
	memo := ""
	fNilTI := CreateTokenSparkInvoiceFields(id, nil, amt, &memo, senderPublicKey, nil)
	fEmptyTI := CreateTokenSparkInvoiceFields(id, []byte{}, amt, &memo, senderPublicKey, nil)
	h1, err := HashSparkInvoiceFields(fNilTI, network, receiverPublicKey)
	require.NoError(t, err)
	h2, err := HashSparkInvoiceFields(fEmptyTI, network, receiverPublicKey)
	require.NoError(t, err)
	require.True(t, bytes.Equal(h1, h2))

	// tokens: amount nil vs empty slice
	fNilAmt := CreateTokenSparkInvoiceFields(id, make([]byte, 32), nil, &memo, senderPublicKey, nil)
	fEmptyAmt := CreateTokenSparkInvoiceFields(id, make([]byte, 32), []byte{}, &memo, senderPublicKey, nil)
	h3, err := HashSparkInvoiceFields(fNilAmt, network, receiverPublicKey)
	require.NoError(t, err)
	h4, err := HashSparkInvoiceFields(fEmptyAmt, network, receiverPublicKey)
	require.NoError(t, err)
	require.True(t, bytes.Equal(h3, h4))

	// sats: amount nil vs 0
	fNilSats := CreateSatsSparkInvoiceFields(id, nil, &memo, senderPublicKey, nil)
	zero := uint64(0)
	fZeroSats := CreateSatsSparkInvoiceFields(id, &zero, &memo, senderPublicKey, nil)
	h5, err := HashSparkInvoiceFields(fNilSats, network, receiverPublicKey)
	require.NoError(t, err)
	h6, err := HashSparkInvoiceFields(fZeroSats, network, receiverPublicKey)
	require.NoError(t, err)
	require.True(t, bytes.Equal(h5, h6))

	// memo nil vs empty string
	fMemoNil := CreateSatsSparkInvoiceFields(id, &zero, nil, senderPublicKey, nil)
	fMemoEmpty := CreateSatsSparkInvoiceFields(id, &zero, &memo, senderPublicKey, nil)
	h7, err := HashSparkInvoiceFields(fMemoNil, network, receiverPublicKey)
	require.NoError(t, err)
	h8, err := HashSparkInvoiceFields(fMemoEmpty, network, receiverPublicKey)
	require.NoError(t, err)
	require.True(t, bytes.Equal(h7, h8))

	fSpkNil := CreateSatsSparkInvoiceFields(id, &zero, nil, keys.Public{}, nil)
	_, err = HashSparkInvoiceFields(fSpkNil, network, receiverPublicKey)
	require.NoError(t, err)
}

func TestVerifySparkAddressSignature_Valid(t *testing.T) {
	seededRand := rand.NewChaCha8([32]byte{})
	privKey := keys.MustGeneratePrivateKeyFromRand(seededRand)
	pubKey := privKey.Public()
	pubKeyBytes := pubKey.Serialize()

	uid, err := uuid.NewV7()
	require.NoError(t, err)
	id := uid[:]
	memo := "invoice-memo"
	amount := big.NewInt(42).Bytes()

	network := Regtest

	fields := CreateTokenSparkInvoiceFields(id, make([]byte, 32), amount, &memo, pubKey, nil)
	hash, err := HashSparkInvoiceFields(fields, network, pubKey)
	require.NoError(t, err)

	sig, err := schnorr.Sign(privKey.ToBTCEC(), hash)
	require.NoError(t, err)

	addr := &pb.SparkAddress{
		IdentityPublicKey:  pubKeyBytes,
		SparkInvoiceFields: fields,
		Signature:          sig.Serialize(),
	}
	require.NoError(t, VerifySparkAddressSignature(addr, network))
}

func TestVerifySparkAddressSignature_Errors(t *testing.T) {

	t.Run("missing signature", func(t *testing.T) {
		seededRand := rand.NewChaCha8([32]byte{})
		privKey := keys.MustGeneratePrivateKeyFromRand(seededRand)
		pubKey := privKey.Public()

		network := Regtest

		uid, _ := uuid.NewV7()
		id := uid[:]
		memo := "m"
		fields := CreateSatsSparkInvoiceFields(id, nil, &memo, pubKey, nil)

		addr := &pb.SparkAddress{IdentityPublicKey: pubKey.Serialize(), SparkInvoiceFields: fields}
		err := VerifySparkAddressSignature(addr, network)
		require.Error(t, err)
	})

	t.Run("wrong public key", func(t *testing.T) {
		seededRand := rand.NewChaCha8([32]byte{})
		// signer key
		signerKey := keys.MustGeneratePrivateKeyFromRand(seededRand)
		// receiver key (will not match signer)
		receiverKey := keys.MustGeneratePrivateKeyFromRand(seededRand)

		network := Regtest
		receiverPublicKey := receiverKey.Public()

		uid, _ := uuid.NewV7()
		id := uid[:]
		memo := "m2"
		fields := CreateSatsSparkInvoiceFields(id, nil, &memo, receiverPublicKey, nil)
		hash, err := HashSparkInvoiceFields(fields, network, receiverPublicKey)
		require.NoError(t, err)

		sig, err := schnorr.Sign(signerKey.ToBTCEC(), hash)
		require.NoError(t, err)

		addr := &pb.SparkAddress{
			IdentityPublicKey:  receiverKey.Public().Serialize(),
			SparkInvoiceFields: fields,
			Signature:          sig.Serialize(),
		}
		err = VerifySparkAddressSignature(addr, network)
		require.Error(t, err)
	})

	t.Run("modified fields after signing", func(t *testing.T) {
		seededRand := rand.NewChaCha8([32]byte{})
		privKey := keys.MustGeneratePrivateKeyFromRand(seededRand)
		pubKey := privKey.Public()
		pubKeyBytes := pubKey.Serialize()

		network := Regtest
		receiverPublicKey, err := keys.ParsePublicKey(pubKeyBytes)
		require.NoError(t, err)

		uid, _ := uuid.NewV7()
		id := uid[:]
		memo := "original"
		fields := CreateSatsSparkInvoiceFields(id, nil, &memo, pubKey, nil)
		hash, err := HashSparkInvoiceFields(fields, network, receiverPublicKey)
		require.NoError(t, err)

		sig, err := schnorr.Sign(privKey.ToBTCEC(), hash)
		require.NoError(t, err)

		// mutate memo
		memo2 := "tampered"
		fields.Memo = &memo2

		addr := &pb.SparkAddress{
			IdentityPublicKey:  pubKeyBytes,
			SparkInvoiceFields: fields,
			Signature:          sig.Serialize(),
		}
		err = VerifySparkAddressSignature(addr, network)
		require.Error(t, err)
	})

	t.Run("invalid public key bytes", func(t *testing.T) {
		seededRand := rand.NewChaCha8([32]byte{})
		uid, _ := uuid.NewV7()
		id := uid[:]
		memo := "x"
		randKey := keys.MustGeneratePrivateKeyFromRand(seededRand).Public().Serialize()

		receiverPriv := keys.MustGeneratePrivateKeyFromRand(seededRand)
		senderPriv := keys.MustGeneratePrivateKeyFromRand(seededRand)
		senderPublicKey, err := keys.ParsePublicKey(senderPriv.Public().Serialize())
		require.NoError(t, err)

		receiverPubCompressed := receiverPriv.Public().Serialize()
		network := Regtest
		receiverPublicKey, err := keys.ParsePublicKey(receiverPubCompressed)
		require.NoError(t, err)

		fields := CreateSatsSparkInvoiceFields(id, nil, &memo, senderPublicKey, nil)
		hash, err := HashSparkInvoiceFields(fields, network, receiverPublicKey)
		require.NoError(t, err)

		// Sign with a real key but set identity key to random bytes
		privKey := keys.MustGeneratePrivateKeyFromRand(seededRand)
		sig, err := schnorr.Sign(privKey.ToBTCEC(), hash)
		require.NoError(t, err)

		addr := &pb.SparkAddress{
			IdentityPublicKey:  randKey,
			SparkInvoiceFields: fields,
			Signature:          sig.Serialize(),
		}
		err = VerifySparkAddressSignature(addr, network)
		require.Error(t, err)
	})
}

func TestHashSparkInvoiceFieldsProducesKnownHash(t *testing.T) {
	identityPKHex := "026c943bfef71040371ca1c1d1ee1d5b203573dc97fdf6497a0b74e5aec0220e21"
	tokenIdHex := "49046dd67bbe5fc8e3abb45bc4f809b9cb5cb5871a19292fa5c7120389641363"
	senderPKHex := "02b0e3203121de9df0bd7c2b3846100e25c63310392e05961d8042fa81906d6f2b"
	idStr := "0198b4ec-3d20-7e4b-b288-1107ecf64d49"
	expiryStr := "2025-08-16T22:12:17.791Z"

	identityPK, _ := hex.DecodeString(identityPKHex)
	tokenId, _ := hex.DecodeString(tokenIdHex)
	senderPK, _ := hex.DecodeString(senderPKHex)
	senderPublicKey, err := keys.ParsePublicKey(senderPK)
	require.NoError(t, err)

	uid := uuid.MustParse(idStr)
	id := uid[:]
	amount := big.NewInt(1000).Bytes()
	memo := "memo"
	expiry, err := time.Parse(time.RFC3339Nano, expiryStr)
	require.NoError(t, err)

	sparkInvoiceFields := CreateTokenSparkInvoiceFields(id, tokenId, amount, &memo, senderPublicKey, &expiry)

	network := Regtest
	receiverPublicKey, err := keys.ParsePublicKey(identityPK)
	require.NoError(t, err)

	hash, err := HashSparkInvoiceFields(sparkInvoiceFields, network, receiverPublicKey)
	require.NoError(t, err)
	require.Len(t, hash, 32)
	require.Equal(t, "21f91b971cccc74f76fcac5384ba99d8629baff87d602f9614f6c032a2e6fb2d", hex.EncodeToString(hash))
}
