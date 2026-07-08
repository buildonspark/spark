package bolt11

import (
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testInvoice     = "lnbcrt123450n1pnj6uf4pp5l26hsdxssmr52vd4xmn5xran7puzx34hpr6uevaq7ta0ayzrp8esdqqcqzpgxqyz5vqrzjqtr2vd60g57hu63rdqk87u3clac6jlfhej4kldrrjvfcw3mphcw8sqqqqzp3jlj6zyqqqqqqqqqqqqqq9qsp5w22fd8aqn7sdum7hxdf59ptgk322fkv589ejxjltngvgehlcqcyq9qxpqysgqvykwsxdx64qrj0s5pgcgygmrpj8w25jsjgltwn09yp24l9nvghe3dl3y0ycy70ksrlqmcn42hxn24e0ucuy3g9fjltudvhv4lrhhamgq3stqgp"
	testPaymentHash = "fab57834d086c74531b536e7430fb3f0782346b708f5ccb3a0f2fafe904309f3"
)

func TestParse(t *testing.T) {
	paymentHash, err := hex.DecodeString(testPaymentHash)
	require.NoError(t, err)

	invoice, err := Parse(testInvoice, paymentHash)
	require.NoError(t, err)

	assert.Equal(t, testInvoice, invoice.Raw())
	assert.Equal(t, int64(12_345_000), invoice.MilliSatoshi())
	assert.Equal(t, paymentHash, invoice.PaymentHash())
}

func TestParse_InvalidInvoice(t *testing.T) {
	_, err := Parse("lnbcrt1notaninvoice", nil)
	require.ErrorIs(t, err, ErrInvalidInvoice)
}

func TestParse_PaymentHashMismatch(t *testing.T) {
	paymentHash, err := hex.DecodeString(testPaymentHash)
	require.NoError(t, err)
	paymentHash[0] ^= 0xff

	_, err = Parse(testInvoice, paymentHash)
	require.ErrorIs(t, err, ErrPaymentHashMismatch)
}
