// Package bolt11 provides a parsed, validated view of bolt11 Lightning invoices.
package bolt11

import (
	"bytes"
	"encoding/hex"
	"fmt"

	decodepay "github.com/nbd-wtf/ln-decodepay"
)

var (
	ErrInvalidInvoice      = fmt.Errorf("invalid bolt11 invoice")
	ErrPaymentHashMismatch = fmt.Errorf("payment hash mismatch")
)

// Invoice is a parsed bolt11 invoice bound to an expected payment hash. The only way to get one is Parse, so holding
// an Invoice guarantees the string decoded and its payment hash equals the expected hash the caller passed in.
type Invoice struct {
	raw         string
	paymentHash []byte
	milliSats   int64
}

// Parse decodes invoiceString and returns ErrPaymentHashMismatch (wrapped) unless its payment hash equals
// expectedPaymentHash. The SO always knows which payment an invoice must belong to (a request field or a stored row),
// so there is deliberately no way to parse an invoice without binding it to a payment hash.
func Parse(invoiceString string, expectedPaymentHash []byte) (Invoice, error) {
	decoded, err := decodepay.Decodepay(invoiceString)
	if err != nil {
		return Invoice{}, fmt.Errorf("%w: unable to decode invoice: %w", ErrInvalidInvoice, err)
	}
	paymentHash, err := hex.DecodeString(decoded.PaymentHash)
	if err != nil {
		return Invoice{}, fmt.Errorf("%w: unable to decode payment hash: %w", ErrInvalidInvoice, err)
	}
	if !bytes.Equal(paymentHash, expectedPaymentHash) {
		return Invoice{}, fmt.Errorf("%w: invoice has %x, expected %x", ErrPaymentHashMismatch, paymentHash, expectedPaymentHash)
	}
	return Invoice{
		raw:         invoiceString,
		paymentHash: paymentHash,
		milliSats:   decoded.MSatoshi,
	}, nil
}

// Raw returns the bolt11 string the invoice was parsed from.
func (i Invoice) Raw() string { return i.raw }

// PaymentHash returns the invoice's payment hash.
func (i Invoice) PaymentHash() []byte { return bytes.Clone(i.paymentHash) }

// MilliSatoshi returns the invoice amount in millisatoshis, or 0 for an amountless invoice.
func (i Invoice) MilliSatoshi() int64 { return i.milliSats }
