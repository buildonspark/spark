package common

import (
	"encoding/hex"
	"math/big"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
)

func TestEncodeDecodeSparkInvoice(t *testing.T) {
	testCases := []struct {
		name                   string
		emptyAmount            bool
		emptyMemo              bool
		emptyExpiryTime        bool
		emptySenderPublicKey   bool
		emptyId                bool
		emptyIdentityPublicKey bool
		emptyTokenIdentifier   bool
		overMaxSatsAmount      bool
		invalidPaymentType     bool
		invalidVersion         bool
		invalidId              bool
	}{
		{
			name: "no empty fields",
		},
		{
			name:        "empty amount",
			emptyAmount: true,
		},
		{
			name:      "empty memo",
			emptyMemo: true,
		},
		{
			name:            "empty expiry time",
			emptyExpiryTime: true,
		},
		{
			name:                 "empty sender public key",
			emptySenderPublicKey: true,
		},
		{
			name:    "empty id",
			emptyId: true,
		},
		{
			name:                   "empty identity public key",
			emptyIdentityPublicKey: true,
		},
		{
			name:                 "empty token identifier",
			emptyTokenIdentifier: true,
		},
		{
			name:              "over max sats amount",
			overMaxSatsAmount: true,
		},
		{
			name:               "invalid payment type",
			invalidPaymentType: true,
		},
		{
			name:           "invalid version",
			invalidVersion: true,
		},
		{
			name:      "invalid id",
			invalidId: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			identityKey, err := hex.DecodeString("02ccb26ba79c63aaf60c9192fd874be3087ae8d8703275df0e558704a6d3a4f132")
			require.NoError(t, err)
			identityPublicKey, err := keys.ParsePublicKey(identityKey)
			require.NoError(t, err)
			senderPublicKey, err := keys.ParsePublicKey(identityKey)
			require.NoError(t, err)

			testUUID, err := uuid.NewV7()
			if err != nil {
				t.Fatalf("failed to generate uuid: %v", err)
			}
			tokenIdentifier, err := hex.DecodeString("9cef64327b1c1f18eb4b4944fc70a1fe9dd84d9084c7daae751de535baafd49f")
			if err != nil {
				t.Fatalf("failed to decode token identifier: %v", err)
			}
			var amount uint64 = 1000
			satsAmount := &amount
			tokenAmount := big.NewInt(1000).Bytes()
			expiryTime := time.Now().Add(24 * time.Hour).UTC()
			expiryTimePtr := &expiryTime
			memo := "myMemo"

			if tc.emptyMemo {
				memo = ""
			}
			if tc.emptyExpiryTime {
				expiryTimePtr = nil
			}
			if tc.emptySenderPublicKey {
				senderPublicKey = keys.Public{}
			}
			if tc.emptyIdentityPublicKey {
				identityPublicKey = keys.Public{}
			}
			if tc.emptyAmount {
				tokenAmount = nil
				satsAmount = nil
			}
			if tc.overMaxSatsAmount {
				satsAmount = new(uint64)
				*satsAmount = 2_100_000_000_000_001
			}
			if tc.emptyTokenIdentifier {
				tokenIdentifier = nil
			}

			tokenInvoiceFields := CreateTokenSparkInvoiceFields(
				testUUID[:],
				tokenIdentifier,
				tokenAmount,
				&memo,
				senderPublicKey,
				expiryTimePtr,
			)
			satsInvoiceFields := CreateSatsSparkInvoiceFields(
				testUUID[:],
				satsAmount,
				&memo,
				senderPublicKey,
				expiryTimePtr,
			)

			if tc.invalidVersion {
				tokenInvoiceFields.Version = 9999
				satsInvoiceFields.Version = 9999
			}
			if tc.invalidId {
				tokenInvoiceFields.Id = []byte{1, 2, 3}
				satsInvoiceFields.Id = []byte{1, 2, 3}
			}
			if tc.invalidPaymentType {
				tokenInvoiceFields.PaymentType = nil
				satsInvoiceFields.PaymentType = nil
			}

			tokensInvoice, err := EncodeSparkAddress(identityPublicKey.Serialize(), Regtest, tokenInvoiceFields)
			if tc.invalidPaymentType || tc.invalidVersion || tc.invalidId || tc.emptyIdentityPublicKey {
				require.Error(t, err, "expected error")
			} else {
				require.NoError(t, err, "failed to encode spark address")
			}

			satsInvoice, err := EncodeSparkAddress(identityPublicKey.Serialize(), Regtest, satsInvoiceFields)
			if tc.invalidPaymentType || tc.invalidVersion || tc.invalidId || tc.emptyIdentityPublicKey || tc.overMaxSatsAmount {
				require.Error(t, err, "expected error")
				return // Early return to avoid decoding the invalid invoices
			} else {
				require.NoError(t, err, "failed to encode spark address")
			}

			// ==== DecodeSparkAddress Tests ====
			decodedTokensInvoice, err := DecodeSparkAddress(tokensInvoice)
			require.NoError(t, err, "failed to decode spark address")

			decodedSatsInvoice, err := DecodeSparkAddress(satsInvoice)
			require.NoError(t, err, "failed to decode spark address")

			if tc.emptyExpiryTime {
				require.Nil(t, decodedTokensInvoice.SparkAddress.SparkInvoiceFields.ExpiryTime, "expiry time should be nil")
				require.Nil(t, decodedSatsInvoice.SparkAddress.SparkInvoiceFields.ExpiryTime, "expiry time should be nil")
			} else {
				require.Equal(t, *expiryTimePtr, decodedTokensInvoice.SparkAddress.SparkInvoiceFields.ExpiryTime.AsTime(), "expiry time does not match")
				require.Equal(t, *expiryTimePtr, decodedSatsInvoice.SparkAddress.SparkInvoiceFields.ExpiryTime.AsTime(), "expiry time does not match")
			}

			require.Equal(t, Regtest, decodedTokensInvoice.Network, "network does not match")
			require.Equal(t, identityPublicKey.Serialize(), decodedTokensInvoice.SparkAddress.IdentityPublicKey, "identity public key does not match")
			require.Equal(t, testUUID[:], decodedTokensInvoice.SparkAddress.SparkInvoiceFields.Id, "id does not match")
			require.Equal(t, memo, *decodedTokensInvoice.SparkAddress.SparkInvoiceFields.Memo, "memo does not match")
			require.Equal(t, senderPublicKey.Serialize(), decodedTokensInvoice.SparkAddress.SparkInvoiceFields.SenderPublicKey, "sender public key does not match")
			require.Equal(t, tokenIdentifier, decodedTokensInvoice.SparkAddress.SparkInvoiceFields.PaymentType.(*pb.SparkInvoiceFields_TokensPayment).TokensPayment.TokenIdentifier, "token identifier does not match")
			require.Equal(t, tokenAmount, decodedTokensInvoice.SparkAddress.SparkInvoiceFields.PaymentType.(*pb.SparkInvoiceFields_TokensPayment).TokensPayment.Amount, "amount does not match")

			require.NoError(t, err, "failed to decode spark address")
			require.Equal(t, Regtest, decodedSatsInvoice.Network, "network does not match")
			require.Equal(t, identityPublicKey.Serialize(), decodedSatsInvoice.SparkAddress.IdentityPublicKey, "identity public key does not match")
			require.Equal(t, testUUID[:], decodedSatsInvoice.SparkAddress.SparkInvoiceFields.Id, "id does not match")
			require.Equal(t, memo, *decodedSatsInvoice.SparkAddress.SparkInvoiceFields.Memo, "memo does not match")
			require.Equal(t, senderPublicKey.Serialize(), decodedSatsInvoice.SparkAddress.SparkInvoiceFields.SenderPublicKey, "sender public key does not match")
			require.Equal(t, satsAmount, decodedSatsInvoice.SparkAddress.SparkInvoiceFields.PaymentType.(*pb.SparkInvoiceFields_SatsPayment).SatsPayment.Amount, "amount does not match")

			// ==== ParseSparkInvoice Tests ====
			parsedTokensInvoice, err := ParseSparkInvoice(tokensInvoice)
			require.NoError(t, err, "failed to parse spark tokens invoice")
			parsedSatsInvoice, err := ParseSparkInvoice(satsInvoice)
			require.NoError(t, err, "failed to parse spark sats invoice")

			if tc.emptyExpiryTime {
				require.Nil(t, parsedTokensInvoice.ExpiryTime, "expiry time should be nil")
				require.Nil(t, parsedSatsInvoice.ExpiryTime, "expiry time should be nil")
			} else {
				require.Equal(t, *expiryTimePtr, parsedTokensInvoice.ExpiryTime.AsTime(), "expiry time does not match")
				require.Equal(t, *expiryTimePtr, parsedSatsInvoice.ExpiryTime.AsTime(), "expiry time does not match")
			}

			if tc.emptyAmount {
				require.Nil(t, parsedSatsInvoice.Payment.SatsPayment.Amount, "sats amount should be nil")
				require.Nil(t, parsedTokensInvoice.Payment.TokensPayment.Amount, "token amount should be nil")
			} else {
				require.NotNil(t, parsedSatsInvoice.Payment.SatsPayment, "sats amount should not be nil")
				require.NotNil(t, parsedTokensInvoice.Payment.TokensPayment.Amount, "token amount should not be nil")
				require.Equal(t, *satsAmount, *parsedSatsInvoice.Payment.SatsPayment.Amount, "sats amount does not match")
				require.Equal(t, tokenAmount, parsedTokensInvoice.Payment.TokensPayment.Amount, "token amount does not match")
			}

			require.Equal(t, testUUID, parsedTokensInvoice.Id, "id does not match")
			require.Equal(t, memo, parsedTokensInvoice.Memo, "memo does not match")
			require.Equal(t, senderPublicKey, parsedTokensInvoice.SenderPublicKey, "sender public key does not match")
			require.Equal(t, tokenIdentifier, parsedTokensInvoice.Payment.TokensPayment.TokenIdentifier, "token identifier does not match")
		})
	}
}

func TestDecodeKnownTokensSparkInvoice(t *testing.T) {
	tokensAddress := "sparkrt1pgssx5us3wkqjza8g80xz3a9gznx25msq6g3ty8exfym9q3ahcv86vsnzfmssqgjzqqejtaxmwj8ms9rn58574nvlq4j5zr5v4ehgnt9d4hnyggr2wgghtqfpwn5rhnpg7j5pfn92dcqdyg4jrunyjdjsg7muxraxgfn5rqgandgr3sxzrqdmew8qydzvz3qpylysylkgcaw9vpm2jzspls0qtr5kfmlwz244rvuk25w5w2sgc2pyqsraqdyp8tf57a6cn2egttaas9ms3whssenmjqt8wag3lgyvdzjskfeupt8xwwdx4agxdm9f0wefzj28jmdxqeudwcwdj9vfl9sdr65x06r0tasf5fwz2"

	res, err := DecodeSparkAddress(tokensAddress)
	require.NoError(t, err, "failed to decode tokens address")
	expectedIdentityPubKey, _ := hex.DecodeString("0353908bac090ba741de6147a540a665537006911590f93249b2823dbe187d3213")
	require.Equal(t, expectedIdentityPubKey, res.SparkAddress.IdentityPublicKey, "identity public key does not match for tokens address")

	tokensPayment, ok := res.SparkAddress.SparkInvoiceFields.PaymentType.(*pb.SparkInvoiceFields_TokensPayment)
	require.True(t, ok, "expected tokens payment, got: %T", res.SparkAddress.SparkInvoiceFields.PaymentType)

	require.Equal(t, uint32(1), res.SparkAddress.SparkInvoiceFields.Version, "version does not match")
	require.NotNil(t, res.SparkAddress.SparkInvoiceFields.Id, "id should not be nil")

	expectedId, _ := hex.DecodeString("01992fa6dba47dc0a39d0f4f566cf82b")
	require.Equal(t, expectedId, res.SparkAddress.SparkInvoiceFields.Id, "id does not match")

	expectedTokenId, _ := hex.DecodeString("093e4813f6463ae2b03b548500fe0f02c74b277f70955a8d9cb2a8ea39504614")
	require.Equal(t, expectedTokenId, tokensPayment.TokensPayment.TokenIdentifier, "token identifier does not match")

	amount := tokensPayment.TokensPayment.Amount
	expectedAmount := big.NewInt(1000).Bytes()
	require.Equal(t, expectedAmount, amount, "amount does not match")

	require.NotNil(t, res.SparkAddress.SparkInvoiceFields.Memo, "memo should not be nil")
	require.Equal(t, "testMemo", *res.SparkAddress.SparkInvoiceFields.Memo, "memo does not match")

	require.NotNil(t, res.SparkAddress.SparkInvoiceFields.ExpiryTime, "expiry time should not be nil")

	require.Equal(t,
		time.Date(2025, time.September, 9, 18, 9, 48, 419000000, time.UTC),
		res.SparkAddress.SparkInvoiceFields.ExpiryTime.AsTime(),
		"expiry time does not match",
	)

	require.NotNil(t, res.SparkAddress.SparkInvoiceFields.SenderPublicKey, "sender public key should not be nil")
	require.Equal(t, expectedIdentityPubKey, res.SparkAddress.SparkInvoiceFields.SenderPublicKey, "sender public key does not match")

	require.NotNil(t, res.SparkAddress.Signature, "signature should not be nil")
	require.Equal(t,
		"9d69a7bbac4d5942d7dec0bb845d784333dc80b3bba88fd046345285939e0567339cd357a8337654bdd948a4a3cb6d3033c6bb0e6c8ac4fcb068f5433f437afb", hex.EncodeToString(res.SparkAddress.Signature),
		"signature does not match")
}

func TestDecodeKnownSatsSparkInvoice(t *testing.T) {
	satsAddress := "sparkrt1pgssx5us3wkqjza8g80xz3a9gznx25msq6g3ty8exfym9q3ahcv86vsnzffssqgjzqqejta89sa8su5f05g0vunfzzkj5zr5v4ehgnt9d4hnyggr2wgghtqfpwn5rhnpg7j5pfn92dcqdyg4jrunyjdjsg7muxraxgfn5zcgs8dcr3sxzrqdetshygps36q8rfqg49d0p0447trnpyxh9f76kt9cwrfx4342jym5emx049chkfsz6j9qc0z8cl7ymmsckx42k76c2qm5f5n5kfvyd26x78eyw0ygs502vg42n8ls"

	res, err := DecodeSparkAddress(satsAddress)
	require.NoError(t, err, "failed to decode sats address")

	expectedIdentityPubKey, _ := hex.DecodeString("0353908bac090ba741de6147a540a665537006911590f93249b2823dbe187d3213")
	require.Equal(t, expectedIdentityPubKey, res.SparkAddress.IdentityPublicKey, "identity public key does not match for sats address")

	satsPayment, ok := res.SparkAddress.SparkInvoiceFields.PaymentType.(*pb.SparkInvoiceFields_SatsPayment)
	require.True(t, ok, "expected sats payment, got: %T", res.SparkAddress.SparkInvoiceFields.PaymentType)
	require.Equal(t, uint32(1), res.SparkAddress.SparkInvoiceFields.Version, "version does not match")

	require.NotNil(t, res.SparkAddress.SparkInvoiceFields.Id, "id should not be nil")
	expectedId, _ := hex.DecodeString("01992fa72c3a7872897d10f6726910ad")
	require.Equal(t, expectedId, res.SparkAddress.SparkInvoiceFields.Id, "id does not match")

	require.Equal(t, uint64(1000), *satsPayment.SatsPayment.Amount, "sats amount does not match")

	require.NotNil(t, res.SparkAddress.SparkInvoiceFields.ExpiryTime, "expiry time should not be nil")
	expectedExpiryTime := time.Date(2025, time.September, 9, 18, 10, 9, 49000000, time.UTC)
	require.Equal(t, expectedExpiryTime, res.SparkAddress.SparkInvoiceFields.ExpiryTime.AsTime(), "expiry time does not match")

	require.NotNil(t, res.SparkAddress.SparkInvoiceFields.Memo, "memo should not be nil")
	require.Equal(t, "testMemo", *res.SparkAddress.SparkInvoiceFields.Memo, "memo does not match")

	require.NotNil(t, res.SparkAddress.SparkInvoiceFields.SenderPublicKey, "sender public key should not be nil")
	require.Equal(t, expectedIdentityPubKey, res.SparkAddress.SparkInvoiceFields.SenderPublicKey, "sender public key does not match")

	require.NotNil(t, res.SparkAddress.Signature, "signature should not be nil")
	require.Equal(t,
		"8a95af0beb5f2c73090d72a7dab2cb870d26ac6aa91374ceccfa9717b2602d48a0c3c47c7fc4dee18b1aaab7b58503744d274b25846ab46f1f2473c88851ea62", hex.EncodeToString(res.SparkAddress.Signature),
		"signature does not match")
}

func TestDecodeAndEncodeKnownSparkAddressProducesSameAddress(t *testing.T) {
	expectedFromJs := "sparkrt1pgssx5us3wkqjza8g80xz3a9gznx25msq6g3ty8exfym9q3ahcv86vsnzffssqgjzqqejta89sa8su5f05g0vunfzzkj5zr5v4ehgnt9d4hnyggr2wgghtqfpwn5rhnpg7j5pfn92dcqdyg4jrunyjdjsg7muxraxgfn5zcgs8dcr3sxzrqdetshygps36q8rfqg49d0p0447trnpyxh9f76kt9cwrfx4342jym5emx049chkfsz6j9qc0z8cl7ymmsckx42k76c2qm5f5n5kfvyd26x78eyw0ygs502vg42n8ls"
	dec, err := DecodeSparkAddress(expectedFromJs)
	require.NoError(t, err)
	addr, err := EncodeSparkAddressWithSignature(
		dec.SparkAddress.GetIdentityPublicKey(),
		dec.Network,
		dec.SparkAddress.GetSparkInvoiceFields(),
		dec.SparkAddress.GetSignature(),
	)
	require.NoError(t, err)
	require.Equal(t, expectedFromJs, addr)
}
