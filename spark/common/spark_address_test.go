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
	tokensAddress := "sprt1pgss9n9jdwnecca27cxfryhasa97xzr6arv8qvn4mu89tpcy5mf6fufjzfnssqgjzqqes6qh0dhh4duxm3x8083hwjkp5fs2yzww7epj0vwp7x8tfdy5flrs58lfmkzdjzzv0k4ww5w72dd64l2f7yszq05z5pnd09xk2mt0xgss9n9jdwnecca27cxfryhasa97xzr6arv8qvn4mu89tpcy5mf6fufjcfxyx0"

	res, err := DecodeSparkAddress(tokensAddress)
	require.NoError(t, err, "failed to decode tokens address")

	expectedIdentityPubKey, _ := hex.DecodeString("02ccb26ba79c63aaf60c9192fd874be3087ae8d8703275df0e558704a6d3a4f132")
	require.Equal(t, expectedIdentityPubKey, res.SparkAddress.IdentityPublicKey, "identity public key does not match for tokens address")

	tokensPayment, ok := res.SparkAddress.SparkInvoiceFields.PaymentType.(*pb.SparkInvoiceFields_TokensPayment)
	require.True(t, ok, "expected tokens payment, got: %T", res.SparkAddress.SparkInvoiceFields.PaymentType)

	require.Equal(t, uint32(1), res.SparkAddress.SparkInvoiceFields.Version, "version does not match")
	require.NotNil(t, res.SparkAddress.SparkInvoiceFields.Id, "id should not be nil")

	expectedId, _ := hex.DecodeString("019868177b6f7ab786dc4c779e3774ac")
	require.Equal(t, expectedId, res.SparkAddress.SparkInvoiceFields.Id, "id does not match")

	expectedTokenId, _ := hex.DecodeString("9cef64327b1c1f18eb4b4944fc70a1fe9dd84d9084c7daae751de535baafd49f")
	require.Equal(t, expectedTokenId, tokensPayment.TokensPayment.TokenIdentifier, "token identifier does not match")

	amount := tokensPayment.TokensPayment.Amount
	expectedAmount := big.NewInt(1000).Bytes()
	require.Equal(t, expectedAmount, amount, "amount does not match")

	require.NotNil(t, res.SparkAddress.SparkInvoiceFields.Memo, "memo should not be nil")
	require.Equal(t, "myMemo", *res.SparkAddress.SparkInvoiceFields.Memo, "memo does not match")

	require.Nil(t, res.SparkAddress.SparkInvoiceFields.ExpiryTime, "expiry time should be nil")

	require.NotNil(t, res.SparkAddress.SparkInvoiceFields.SenderPublicKey, "sender public key should not be nil")
	require.Equal(t, expectedIdentityPubKey, res.SparkAddress.SparkInvoiceFields.SenderPublicKey, "sender public key does not match")
}

func TestDecodeKnownSatsSparkInvoice(t *testing.T) {
	satsAddress := "sprt1pgss9n9jdwnecca27cxfryhasa97xzr6arv8qvn4mu89tpcy5mf6fufjzffqsqgjzqqe3qgg3u7hnmdqfft7ash9vrfjyqcgaqrj5pnd09xk2mt0xgss9n9jdwnecca27cxfryhasa97xzr6arv8qvn4mu89tpcy5mf6fufj8gxq3a5n6nzqvyxqmtkesqskz5jyy"

	res, err := DecodeSparkAddress(satsAddress)
	require.NoError(t, err, "failed to decode sats address")

	expectedIdentityPubKey, _ := hex.DecodeString("02ccb26ba79c63aaf60c9192fd874be3087ae8d8703275df0e558704a6d3a4f132")
	require.Equal(t, expectedIdentityPubKey, res.SparkAddress.IdentityPublicKey, "identity public key does not match for sats address")

	satsPayment, ok := res.SparkAddress.SparkInvoiceFields.PaymentType.(*pb.SparkInvoiceFields_SatsPayment)
	require.True(t, ok, "expected sats payment, got: %T", res.SparkAddress.SparkInvoiceFields.PaymentType)
	require.Equal(t, uint32(1), res.SparkAddress.SparkInvoiceFields.Version, "version does not match")

	require.NotNil(t, res.SparkAddress.SparkInvoiceFields.Id, "id should not be nil")
	expectedId, _ := hex.DecodeString("019881088f3d79eda04a57eec2e560d3")
	require.Equal(t, expectedId, res.SparkAddress.SparkInvoiceFields.Id, "id does not match")

	require.Equal(t, uint64(1000), *satsPayment.SatsPayment.Amount, "sats amount does not match")

	require.NotNil(t, res.SparkAddress.SparkInvoiceFields.ExpiryTime, "expiry time should not be nil")
	expectedExpiryTime := time.Date(2025, 8, 7, 20, 17, 58, 589000000, time.UTC)
	require.Equal(t, expectedExpiryTime, res.SparkAddress.SparkInvoiceFields.ExpiryTime.AsTime(), "expiry time does not match")

	require.NotNil(t, res.SparkAddress.SparkInvoiceFields.Memo, "memo should not be nil")
	require.Equal(t, "myMemo", *res.SparkAddress.SparkInvoiceFields.Memo, "memo does not match")

	require.NotNil(t, res.SparkAddress.SparkInvoiceFields.SenderPublicKey, "sender public key should not be nil")
	require.Equal(t, expectedIdentityPubKey, res.SparkAddress.SparkInvoiceFields.SenderPublicKey, "sender public key does not match")
}

func TestDecodeKnownSparkAddressProducesExpectedFields(t *testing.T) {
	network := Regtest
	identityPublicKey := "03c16c3cd36eec99efb6bc1dd91ff24a53b34ce6142e68fa52f76ccedd15bb12da"
	version := uint32(1)
	id := "0198b583d66a7fabbf987a84fa648b3f"
	tokenIdentifier := "2a3894759c81187d18e7fdfef4772c26dc98bccc85e6387818aa8f4eb9431cba"
	memo := "test"
	senderPublicKey := "039be718d1ebad6e1431c86de8cfbabe125529f73db9952f7fb00c75e0690c1342"
	expiryTime := "2025-08-17T00:57:52.969Z"
	signature := "e5b5d413fc098315df215a0804d9d07ecf56055659d21d9dcc84a280c5466e41f6c0607bda52d32c088ff756a4b04df61165401030f6069a61257551ec0a989a"
	knownTwoGo := "sprt1pgss8stv8nfkamyea7mtc8werley55anfnnpgtnglff0wmxwm52mkyk6zfeqsqgjzqqe3dvr6e48l2alnpagf7ny3vlj5pr5v4ehgv3pqwd7wxx3awkku9p3epk73na6hcf9220h8kue2tmlkqx8tcrfpsf5ywsvpzgd9px9qcgvpzy8ecp35fg2yq4r39r4njq3slgcul7laarh9sndex9uejz7vwrcrz4g7n4egvwt5yspvsdyped46sflczvrzh0jzksgqnvaqlk02cz4vkwjrkwuep9zsrz5vmjp7mqxq7762tfjczy07at2fvzd7cgk2sqsxrmqdxnpy464rmq2nzdqzpuhme"
	decoded, err := DecodeSparkAddress(knownTwoGo)
	require.NoError(t, err)

	require.Equal(t, network, decoded.Network)
	require.Equal(t, identityPublicKey, hex.EncodeToString(decoded.SparkAddress.IdentityPublicKey))
	require.Equal(t, version, decoded.SparkAddress.SparkInvoiceFields.Version)
	require.Equal(t, id, hex.EncodeToString(decoded.SparkAddress.SparkInvoiceFields.Id))
	require.Equal(t, tokenIdentifier, hex.EncodeToString(decoded.SparkAddress.SparkInvoiceFields.PaymentType.(*pb.SparkInvoiceFields_TokensPayment).TokensPayment.TokenIdentifier))

	tpTwo := decoded.SparkAddress.SparkInvoiceFields.GetPaymentType().(*pb.SparkInvoiceFields_TokensPayment).TokensPayment
	amtTwo := new(big.Int).SetBytes(tpTwo.GetAmount())
	require.EqualValues(t, 100, amtTwo.Uint64())

	require.Equal(t, senderPublicKey, hex.EncodeToString(decoded.SparkAddress.SparkInvoiceFields.SenderPublicKey))

	require.Equal(t, memo, *decoded.SparkAddress.SparkInvoiceFields.Memo)
	require.Equal(t, expiryTime, decoded.SparkAddress.SparkInvoiceFields.ExpiryTime.AsTime().Format(time.RFC3339Nano))
	require.Equal(t, signature, hex.EncodeToString(decoded.SparkAddress.Signature))
}

func TestDecodeAndEncodeKnownSparkAddressProducesSameAddress(t *testing.T) {
	expectedFromJs := "sprt1pgss8qg32urljkew9hug6ewhh935jqee8e34yxe7w3xau48gq9uxfcm3zfcssqgjzqqe305uhy888wvc0rxsk9elsxpz5zr5v4ehgnt9d4hnyggztzy4kyrgxepyv55ra77ns3637d5fgp6dxcura9e7rna7yxpwk5qn5psgctagm3gxrgnq5gqjkujdn0279hyzpkd4g72egx67h5hlh8gncyu8s7y6f4ugkwyckqfqypxjrfqzfazzhvwr4ldpfwgvmjlhseealf25h3cxx0mnzqpp608ypnp2779uv30zeys50caeunr45y3nk4w0rhtsxqml99u0twr5cdrd3mzwey9yyfcy"
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
