package common

import (
	"encoding/hex"
	"math/big"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/btcnetwork"

	"github.com/lightsparkdev/spark/common/keys"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/lightsparkdev/spark/proto/spark"
)

var (
	testExpiryTime  = time.Date(2025, 12, 31, 23, 59, 59, 0, time.UTC)
	testTokenAmount = big.NewInt(1000).Bytes()
	testUUID        = uuid.Must(uuid.Parse("56ec4b25-c86d-4218-97d4-3dcc4300df8f"))
	testTokenID, _  = hex.DecodeString("9cef64327b1c1f18eb4b4944fc70a1fe9dd84d9084c7daae751de535baafd49f")
	testIDPubKey    = keys.MustGeneratePrivateKeyFromRand(rand.NewChaCha8([32]byte{})).Public()
)

const (
	testSats = 1000
	testMemo = "myMemo"
)

func TestEncodeDecodeSparkInvoiceSats(t *testing.T) {
	tests := []struct {
		name  string
		setUp func(fields *pb.SparkInvoiceFields)
	}{
		{
			name:  "base",
			setUp: func(fields *pb.SparkInvoiceFields) {},
		},
		{
			name:  "empty amount",
			setUp: func(fields *pb.SparkInvoiceFields) { fields.PaymentType = satsPaymentOf(0) },
		},
		{
			name:  "empty memo",
			setUp: func(fields *pb.SparkInvoiceFields) { fields.Memo = nil },
		},
		{
			name:  "empty expiry time",
			setUp: func(fields *pb.SparkInvoiceFields) { fields.ExpiryTime = nil },
		},
		{
			name:  "empty sender public key",
			setUp: func(fields *pb.SparkInvoiceFields) { fields.SenderPublicKey = nil },
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			invoiceFields := &pb.SparkInvoiceFields{
				Version:         1,
				Id:              testUUID[:],
				PaymentType:     satsPaymentOf(testSats),
				Memo:            proto.String(testMemo),
				SenderPublicKey: testIDPubKey.Serialize(),
				ExpiryTime:      timestamppb.New(testExpiryTime),
			}
			tc.setUp(invoiceFields)

			invoice, err := EncodeSparkAddress(testIDPubKey, btcnetwork.Regtest, invoiceFields)
			require.NoError(t, err, "failed to encode spark address")
			decoded, err := DecodeSparkAddress(invoice)
			require.NoError(t, err, "failed to decode spark address")

			want := &DecodedSparkAddress{
				Network: btcnetwork.Regtest,
				SparkAddress: &pb.SparkAddress{
					IdentityPublicKey:  testIDPubKey.Serialize(),
					SparkInvoiceFields: invoiceFields,
				},
			}
			assert.EqualExportedValues(t, want, decoded)
		})
	}
}

func TestEncodeSparkAddress_Errors(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{})
	senderPublicKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	identityPublicKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	tests := []struct {
		name              string
		identityPublicKey keys.Public
		setUp             func(fields *pb.SparkInvoiceFields)
	}{
		{
			name:              "empty identity public key",
			identityPublicKey: keys.Public{},
			setUp:             func(fields *pb.SparkInvoiceFields) {},
		},
		{
			name:              "payment sats over limit",
			identityPublicKey: identityPublicKey,
			setUp:             func(fields *pb.SparkInvoiceFields) { fields.PaymentType = satsPaymentOf(btcutil.MaxSatoshi + 1) },
		},
		{
			name:              "invalid payment type",
			identityPublicKey: identityPublicKey,
			setUp:             func(fields *pb.SparkInvoiceFields) { fields.PaymentType = nil },
		},
		{
			name:              "invalid version",
			identityPublicKey: identityPublicKey,
			setUp:             func(fields *pb.SparkInvoiceFields) { fields.Version = 999999 },
		},
		{
			name:              "invalid ID",
			identityPublicKey: identityPublicKey,
			setUp:             func(fields *pb.SparkInvoiceFields) { fields.Id = []byte{1, 2, 3} },
		},
		{
			name:              "empty ID",
			identityPublicKey: identityPublicKey,
			setUp:             func(fields *pb.SparkInvoiceFields) { fields.Id = nil },
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fields := &pb.SparkInvoiceFields{
				Version:         1,
				Id:              testUUID[:],
				PaymentType:     satsPaymentOf(testSats),
				Memo:            proto.String(testMemo),
				SenderPublicKey: senderPublicKey.Serialize(),
				ExpiryTime:      timestamppb.New(testExpiryTime),
			}
			tc.setUp(fields)

			_, err := EncodeSparkAddress(tc.identityPublicKey, btcnetwork.Regtest, fields)
			require.Error(t, err)
		})
	}
}

func TestEncodeDecodeSparkInvoiceTokens(t *testing.T) {
	tests := []struct {
		name  string
		setUp func(fields *pb.SparkInvoiceFields)
	}{
		{
			name:  "base",
			setUp: func(fields *pb.SparkInvoiceFields) {},
		},
		{
			name:  "empty amount",
			setUp: func(fields *pb.SparkInvoiceFields) { fields.PaymentType = tokensPaymentOf(testTokenID, nil) },
		},
		{
			name:  "empty token identifier",
			setUp: func(fields *pb.SparkInvoiceFields) { fields.PaymentType = tokensPaymentOf(nil, testTokenAmount) },
		},
		{
			name:  "empty memo",
			setUp: func(fields *pb.SparkInvoiceFields) { fields.Memo = nil },
		},
		{
			name:  "empty expiry time",
			setUp: func(fields *pb.SparkInvoiceFields) { fields.ExpiryTime = nil },
		},
		{
			name:  "empty sender public key",
			setUp: func(fields *pb.SparkInvoiceFields) { fields.SenderPublicKey = nil },
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			invoiceFields := &pb.SparkInvoiceFields{
				Version:         1,
				Id:              testUUID[:],
				PaymentType:     tokensPaymentOf(testTokenID, testTokenAmount),
				Memo:            proto.String(testMemo),
				SenderPublicKey: testIDPubKey.Serialize(),
				ExpiryTime:      timestamppb.New(testExpiryTime),
			}
			tc.setUp(invoiceFields)

			invoice, err := EncodeSparkAddress(testIDPubKey, btcnetwork.Regtest, invoiceFields)
			require.NoError(t, err, "failed to encode spark address")
			decoded, err := DecodeSparkAddress(invoice)
			require.NoError(t, err, "failed to decode spark address")

			want := &DecodedSparkAddress{
				Network: btcnetwork.Regtest,
				SparkAddress: &pb.SparkAddress{
					IdentityPublicKey:  testIDPubKey.Serialize(),
					SparkInvoiceFields: invoiceFields,
				},
			}
			assert.EqualExportedValues(t, want, decoded)
		})
	}
}

func TestDecodeKnownTokensSparkInvoice(t *testing.T) {
	tokensAddress := "sparkrt1pgssx5us3wkqjza8g80xz3a9gznx25msq6g3ty8exfym9q3ahcv86vsnzfmssqgjzqqejtaxmwj8ms9rn58574nvlq4j5zr5v4ehgnt9d4hnyggr2wgghtqfpwn5rhnpg7j5pfn92dcqdyg4jrunyjdjsg7muxraxgfn5rqgandgr3sxzrqdmew8qydzvz3qpylysylkgcaw9vpm2jzspls0qtr5kfmlwz244rvuk25w5w2sgc2pyqsraqdyp8tf57a6cn2egttaas9ms3whssenmjqt8wag3lgyvdzjskfeupt8xwwdx4agxdm9f0wefzj28jmdxqeudwcwdj9vfl9sdr65x06r0tasf5fwz2"

	res, err := DecodeSparkAddress(tokensAddress)
	require.NoError(t, err, "failed to decode tokens address")

	expectedIdentityPubKey, _ := hex.DecodeString("0353908bac090ba741de6147a540a665537006911590f93249b2823dbe187d3213")
	expectedId, _ := hex.DecodeString("01992fa6dba47dc0a39d0f4f566cf82b")
	expectedTokenId, _ := hex.DecodeString("093e4813f6463ae2b03b548500fe0f02c74b277f70955a8d9cb2a8ea39504614")
	expectedSignature, _ := hex.DecodeString("9d69a7bbac4d5942d7dec0bb845d784333dc80b3bba88fd046345285939e0567339cd357a8337654bdd948a4a3cb6d3033c6bb0e6c8ac4fcb068f5433f437afb")
	want := &DecodedSparkAddress{
		Network: btcnetwork.Regtest,
		SparkAddress: &pb.SparkAddress{
			IdentityPublicKey: expectedIdentityPubKey,
			SparkInvoiceFields: &pb.SparkInvoiceFields{
				Version:         1,
				Id:              expectedId,
				PaymentType:     tokensPaymentOf(expectedTokenId, big.NewInt(1000).Bytes()),
				Memo:            proto.String("testMemo"),
				SenderPublicKey: expectedIdentityPubKey,
				ExpiryTime:      timestamppb.New(time.Date(2025, time.September, 9, 18, 9, 48, 419000000, time.UTC)),
			},
			Signature: expectedSignature,
		},
	}
	require.EqualExportedValues(t, want, res)
}

func TestDecodeKnownSatsSparkInvoice(t *testing.T) {
	satsAddress := "sparkrt1pgssx5us3wkqjza8g80xz3a9gznx25msq6g3ty8exfym9q3ahcv86vsnzffssqgjzqqejta89sa8su5f05g0vunfzzkj5zr5v4ehgnt9d4hnyggr2wgghtqfpwn5rhnpg7j5pfn92dcqdyg4jrunyjdjsg7muxraxgfn5zcgs8dcr3sxzrqdetshygps36q8rfqg49d0p0447trnpyxh9f76kt9cwrfx4342jym5emx049chkfsz6j9qc0z8cl7ymmsckx42k76c2qm5f5n5kfvyd26x78eyw0ygs502vg42n8ls"

	res, err := DecodeSparkAddress(satsAddress)
	require.NoError(t, err, "failed to decode sats address")

	expectedIdentityPubKey, _ := hex.DecodeString("0353908bac090ba741de6147a540a665537006911590f93249b2823dbe187d3213")
	expectedId, _ := hex.DecodeString("01992fa72c3a7872897d10f6726910ad")
	expectedSignature, _ := hex.DecodeString("8a95af0beb5f2c73090d72a7dab2cb870d26ac6aa91374ceccfa9717b2602d48a0c3c47c7fc4dee18b1aaab7b58503744d274b25846ab46f1f2473c88851ea62")
	want := &DecodedSparkAddress{
		Network: btcnetwork.Regtest,
		SparkAddress: &pb.SparkAddress{
			IdentityPublicKey: expectedIdentityPubKey,
			SparkInvoiceFields: &pb.SparkInvoiceFields{
				Version:         1,
				Id:              expectedId,
				PaymentType:     satsPaymentOf(1000),
				Memo:            proto.String("testMemo"),
				SenderPublicKey: expectedIdentityPubKey,
				ExpiryTime:      timestamppb.New(time.Date(2025, time.September, 9, 18, 10, 9, 49000000, time.UTC)),
			},
			Signature: expectedSignature,
		},
	}
	require.EqualExportedValues(t, want, res)
}

func TestDecodeAndEncodeKnownSparkAddressProducesSameAddress(t *testing.T) {
	expectedFromJs := "sparkrt1pgssx5us3wkqjza8g80xz3a9gznx25msq6g3ty8exfym9q3ahcv86vsnzffssqgjzqqejta89sa8su5f05g0vunfzzkj5zr5v4ehgnt9d4hnyggr2wgghtqfpwn5rhnpg7j5pfn92dcqdyg4jrunyjdjsg7muxraxgfn5zcgs8dcr3sxzrqdetshygps36q8rfqg49d0p0447trnpyxh9f76kt9cwrfx4342jym5emx049chkfsz6j9qc0z8cl7ymmsckx42k76c2qm5f5n5kfvyd26x78eyw0ygs502vg42n8ls"
	dec, err := DecodeSparkAddress(expectedFromJs)
	require.NoError(t, err)
	identityPubKey, err := keys.ParsePublicKey(dec.SparkAddress.GetIdentityPublicKey())
	require.NoError(t, err)

	addr, err := EncodeSparkAddressWithSignature(
		identityPubKey,
		dec.Network,
		dec.SparkAddress.GetSparkInvoiceFields(),
		dec.SparkAddress.GetSignature(),
	)

	require.NoError(t, err)
	require.Equal(t, expectedFromJs, addr)
}

func tokensPaymentOf(tokenID, tokenAmount []byte) *pb.SparkInvoiceFields_TokensPayment {
	return &pb.SparkInvoiceFields_TokensPayment{TokensPayment: &pb.TokensPayment{TokenIdentifier: tokenID, Amount: tokenAmount}}
}
func satsPaymentOf(sats uint64) *pb.SparkInvoiceFields_SatsPayment {
	return &pb.SparkInvoiceFields_SatsPayment{SatsPayment: &pb.SatsPayment{Amount: proto.Uint64(sats)}}
}
