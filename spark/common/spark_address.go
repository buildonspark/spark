package common

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"
	"time"

	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/btcutil/bech32"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func EncodeSparkAddress(identityPublicKey []byte, network Network, sparkInvoiceFields *pb.SparkInvoiceFields) (string, error) {
	return EncodeSparkAddressWithSignature(identityPublicKey, network, sparkInvoiceFields, nil)
}

// EncodeSparkAddressWithSignature encodes a SparkAddress including optional signature bytes.
// If signature is nil or empty, the resulting address will have no signature.
func EncodeSparkAddressWithSignature(identityPublicKey []byte, network Network, sparkInvoiceFields *pb.SparkInvoiceFields, signature []byte) (string, error) {
	if len(identityPublicKey) == 0 {
		return "", fmt.Errorf("identity public key is required")
	}
	if sparkInvoiceFields != nil {
		if sparkInvoiceFields.Version != 1 {
			return "", fmt.Errorf("version must be 1")
		}
		if sparkInvoiceFields.Id == nil {
			return "", fmt.Errorf("id is required")
		}
		if _, err := uuid.FromBytes(sparkInvoiceFields.Id); err != nil {
			return "", fmt.Errorf("id is not a valid uuid: %w", err)
		}
		paymentType := sparkInvoiceFields.PaymentType
		switch pt := paymentType.(type) {
		case *pb.SparkInvoiceFields_TokensPayment:
			tokensPayment := pt.TokensPayment
			if tokensPayment == nil {
				return "", fmt.Errorf("tokens payment is required")
			}
		case *pb.SparkInvoiceFields_SatsPayment:
			satsPayment := pt.SatsPayment
			const MAX_SATS_AMOUNT = 2_100_000_000_000_000 // 21_000_000 BTC * 100_000_000 sats/BTC
			if satsPayment == nil {
				return "", fmt.Errorf("sats payment is required")
			}
			if satsPayment.Amount != nil && *satsPayment.Amount > MAX_SATS_AMOUNT {
				return "", fmt.Errorf("sats amount must be between 0 and %d", MAX_SATS_AMOUNT)
			}
		default:
			return "", fmt.Errorf("invalid payment type: %T", paymentType)
		}
	}

	sparkAddress := &pb.SparkAddress{
		IdentityPublicKey:  identityPublicKey,
		SparkInvoiceFields: sparkInvoiceFields,
	}
	if len(signature) > 0 {
		sparkAddress.Signature = signature
	}
	sparkAddressBytes, err := proto.MarshalOptions{Deterministic: true}.Marshal(sparkAddress)
	if err != nil {
		return "", err
	}

	// Convert 8-bit bytes to 5-bit bech32 data
	bech32Data, err := bech32.ConvertBits(sparkAddressBytes, 8, 5, true)
	if err != nil {
		return "", err
	}

	hrp, err := NetworkToHrp(network)
	if err != nil {
		return "", err
	}

	data, err := bech32.EncodeM(hrp, bech32Data)
	if err != nil {
		return "", err
	}
	return data, nil
}

type DecodedSparkAddress struct {
	SparkAddress *pb.SparkAddress
	Network      Network
}

func DecodeSparkAddress(address string) (*DecodedSparkAddress, error) {
	hrp, data, err := bech32.DecodeNoLimit(address)
	if err != nil {
		return nil, err
	}
	network := HrpToNetwork(hrp)
	if network == Unspecified {
		return nil, sparkerrors.InvalidUserInputErrorf("unknown network: %s", hrp)
	}

	// Convert 5-bit bech32 data to 8-bit bytes
	byteData, err := bech32.ConvertBits(data, 5, 8, false)
	if err != nil {
		return nil, err
	}

	sparkAddress := &pb.SparkAddress{}
	if err := proto.Unmarshal(byteData, sparkAddress); err != nil {
		return nil, err
	}

	return &DecodedSparkAddress{
		SparkAddress: sparkAddress,
		Network:      network,
	}, nil
}

type PaymentKind int

const (
	PaymentKindTokens PaymentKind = iota + 1
	PaymentKindSats
)

type ParsedPayment struct {
	Kind          PaymentKind
	TokensPayment *pb.TokensPayment
	SatsPayment   *pb.SatsPayment
}

type ParsedSparkInvoice struct {
	Version           uint32
	Id                uuid.UUID
	ReceiverPublicKey keys.Public
	Payment           ParsedPayment
	Memo              string
	SenderPublicKey   keys.Public
	ExpiryTime        *timestamppb.Timestamp
	Signature         []byte
	Network           Network
}

func ParseSparkInvoice(addr string) (*ParsedSparkInvoice, error) {
	decoded, err := DecodeSparkAddress(addr)
	if err != nil {
		return nil, sparkerrors.InvalidUserInputErrorf("failed to decode spark address: %w", err)
	}
	if decoded.SparkAddress == nil || decoded.SparkAddress.SparkInvoiceFields == nil {
		return nil, fmt.Errorf("spark address or invoice fields are nil")
	}

	if err = enforceCanonicalBytes(addr, decoded.SparkAddress); err != nil {
		return nil, err
	}

	// version is required
	if decoded.SparkAddress.SparkInvoiceFields.Version != 1 {
		return nil, sparkerrors.InvalidUserInputErrorf("invoice version is not supported, expected: 1, got: %d", decoded.SparkAddress.SparkInvoiceFields.Version)
	}
	// receiver public key is required
	receiverPublicKey, err := keys.ParsePublicKey(decoded.SparkAddress.IdentityPublicKey)
	if err != nil {
		return nil, sparkerrors.InvalidUserInputErrorf("failed to parse receiver public key: %w", err)
	}
	// id is required
	decodedUUID, err := uuid.FromBytes(decoded.SparkAddress.SparkInvoiceFields.Id)
	if err != nil {
		return nil, sparkerrors.InvalidUserInputErrorf("failed to parse invoice id: %w", err)
	}

	var payment ParsedPayment
	switch pt := decoded.SparkAddress.SparkInvoiceFields.PaymentType.(type) {
	case *pb.SparkInvoiceFields_TokensPayment:
		if pt.TokensPayment == nil {
			return nil, sparkerrors.InvalidUserInputErrorf("tokens payment is nil")
		}
		payment = ParsedPayment{
			Kind:          PaymentKindTokens,
			TokensPayment: pt.TokensPayment,
		}
	case *pb.SparkInvoiceFields_SatsPayment:
		if pt.SatsPayment == nil {
			return nil, sparkerrors.InvalidUserInputErrorf("sats payment is nil")
		}
		payment = ParsedPayment{
			Kind:        PaymentKindSats,
			SatsPayment: pt.SatsPayment,
		}
	default:
		return nil, sparkerrors.InvalidUserInputErrorf("unknown payment type in invoice")
	}
	// sender public key is optional
	var senderPublicKey keys.Public
	if len(decoded.SparkAddress.SparkInvoiceFields.SenderPublicKey) > 0 {
		senderPublicKey, err = keys.ParsePublicKey(decoded.SparkAddress.SparkInvoiceFields.SenderPublicKey)
		if err != nil {
			return nil, sparkerrors.InvalidUserInputErrorf("failed to parse sender public key: %w", err)
		}
	}
	// signature is optional. validate if present
	if len(decoded.SparkAddress.Signature) > 0 {
		err = VerifySparkAddressSignature(decoded.SparkAddress, decoded.Network)
		if err != nil {
			return nil, sparkerrors.InvalidUserInputErrorf("invalid spark invoice signature: %w", err)
		}
	}
	memo := ""
	if m := decoded.SparkAddress.SparkInvoiceFields.Memo; m != nil {
		memo = *m
	}

	return &ParsedSparkInvoice{
		Version:           decoded.SparkAddress.SparkInvoiceFields.Version,
		Id:                decodedUUID,
		ReceiverPublicKey: receiverPublicKey,
		Payment:           payment,
		Memo:              memo,
		SenderPublicKey:   senderPublicKey,
		ExpiryTime:        decoded.SparkAddress.SparkInvoiceFields.ExpiryTime,
		Signature:         decoded.SparkAddress.Signature,
		Network:           decoded.Network,
	}, nil
}

func enforceCanonicalBytes(address string, decodedSparkAddr *pb.SparkAddress) error {
	decodedHrp, addrData, err := bech32.DecodeNoLimit(address)
	if err != nil {
		return sparkerrors.InvalidUserInputErrorf("failed to decode spark address: %v", err)
	}
	decodedNetwork := HrpToNetwork(decodedHrp)
	if decodedNetwork == Unspecified {
		return sparkerrors.InvalidUserInputErrorf("unknown network: %s", decodedHrp)
	}
	canonBytes, err := proto.MarshalOptions{Deterministic: true}.Marshal(decodedSparkAddr)
	if err != nil {
		return fmt.Errorf("failed to encode invoice: %w", err)
	}
	canonData, err := bech32.ConvertBits(canonBytes, 8, 5, true)
	if err != nil {
		return fmt.Errorf("failed to encode invoice: %w", err)
	}
	canonHrp, err := NetworkToHrp(decodedNetwork)
	if err != nil {
		return fmt.Errorf("failed to map network: %w", err)
	}
	canonStr, err := bech32.EncodeM(canonHrp, canonData)
	if err != nil {
		return fmt.Errorf("failed to encode invoice: %w", err)
	}
	if !bytes.Equal(canonData, addrData) {
		return sparkerrors.InvalidUserInputErrorf("invoice does not adhere to canonical encoding: original: %s, re-encoded: %s", address, canonStr)
	}

	// The sparkl1 prefix for local invoices is not canonical and maps to regtest.
	// Skip the check on the full string for local invoices.
	lower := strings.ToLower(address)
	if !strings.HasPrefix(lower, "sparkl1") && lower != canonStr {
		return sparkerrors.InvalidUserInputErrorf(
			"invoice does not adhere to canonical encoding: original: %s, re-encoded: %s",
			address, canonStr,
		)
	}
	return nil
}

// CreateTokenSparkInvoiceFields creates SparkInvoiceFields for token payments
func CreateTokenSparkInvoiceFields(id []byte, tokenIdentifier []byte, amount []byte, memo *string, senderPublicKey keys.Public, expiryTime *time.Time) *pb.SparkInvoiceFields {
	sparkInvoiceFields := &pb.SparkInvoiceFields{
		Version: 1,
		Id:      id,
		PaymentType: &pb.SparkInvoiceFields_TokensPayment{
			TokensPayment: &pb.TokensPayment{
				TokenIdentifier: tokenIdentifier,
				Amount:          amount,
			},
		},
		Memo:            memo,
		SenderPublicKey: senderPublicKey.Serialize(),
	}
	if expiryTime != nil {
		sparkInvoiceFields.ExpiryTime = timestamppb.New(*expiryTime)
	}
	return sparkInvoiceFields
}

// CreateSatsSparkInvoiceFields creates SparkInvoiceFields for sats payments
func CreateSatsSparkInvoiceFields(id []byte, amount *uint64, memo *string, senderPublicKey keys.Public, expiryTime *time.Time) *pb.SparkInvoiceFields {
	sparkInvoiceFields := &pb.SparkInvoiceFields{
		Version: 1,
		Id:      id,
		PaymentType: &pb.SparkInvoiceFields_SatsPayment{
			SatsPayment: &pb.SatsPayment{
				Amount: amount,
			},
		},
		Memo:            memo,
		SenderPublicKey: senderPublicKey.Serialize(),
	}
	if expiryTime != nil {
		sparkInvoiceFields.ExpiryTime = timestamppb.New(*expiryTime)
	}
	return sparkInvoiceFields
}

func HrpToNetwork(hrp string) Network {
	switch hrp {
	case "sparkl", "spl": // for local testing
		return Regtest
	case "sparkrt", "sprt":
		return Regtest
	case "sparkt", "spt":
		return Testnet
	case "sparks", "sps":
		return Signet
	case "spark", "sp":
		return Mainnet
	}
	return Unspecified
}

func NetworkToHrp(network Network) (string, error) {
	switch network {
	case Regtest:
		return "sparkrt", nil
	case Testnet:
		return "sparkt", nil
	case Signet:
		return "sparks", nil
	case Mainnet:
		return "spark", nil
	default:
		return "", fmt.Errorf("unknown network: %v", network)
	}
}

// HashSparkInvoiceFields computes a deterministic hash of SparkInvoiceFields by:
// - Hashing each field (or group) separately using SHA-256, in a fixed order
// - Concatenating those field-level hashes
// - Hashing the concatenation once more with SHA-256
//
// Field order and encoding:
// 1) version: uint32 big-endian (required)
// 2) id: 16 bytes (required)
// 3) network: 4 bytes (required)
// 4) receiver_public_key: 33 bytes (required)
// 5) payment_type discriminator (1 byte) + contents:
//   - TokensPayment: discriminator {1}
//     token_identifier: 32 bytes (0-filled if nil)
//     amount: raw bytes (0..16 bytes) (empty if nil)
//   - SatsPayment:     discriminator {2}
//     amount: uint64 big-endian (0 if nil)
//
// 6) memo: raw UTF-8 bytes (empty if nil)
// 7) sender_public_key: 33 bytes (0-filled if nil)
// 8) expiry_time (seconds): uint64 big-endian (0 if nil)
func HashSparkInvoiceFields(f *pb.SparkInvoiceFields, network Network, receiverPublicKey keys.Public) ([]byte, error) {
	if f == nil {
		return nil, fmt.Errorf("spark invoice fields cannot be nil")
	}

	h := sha256.New()
	var all []byte

	// 1) version
	h.Reset()
	v := make([]byte, 4)
	binary.BigEndian.PutUint32(v, f.GetVersion())
	h.Write(v)
	all = append(all, h.Sum(nil)...)

	// 2) id
	h.Reset()
	id := f.GetId()
	if id == nil || len(id) != 16 {
		return nil, fmt.Errorf("invoice id must be exactly 16 bytes")
	}
	h.Write(id)
	all = append(all, h.Sum(nil)...)

	// 3) Network (4 bytes)
	h.Reset()
	networkMagic, err := BitcoinNetworkIdentifierFromNetwork(network)
	if err != nil {
		return nil, fmt.Errorf("invalid network: %w", err)
	}
	h.Write(sha256Slice(binary.BigEndian.AppendUint32(nil, networkMagic)))
	all = append(all, h.Sum(nil)...)

	// 4) receiver_public_key
	h.Reset()
	h.Write(receiverPublicKey.Serialize())
	all = append(all, h.Sum(nil)...)

	switch pt := f.PaymentType.(type) {
	case *pb.SparkInvoiceFields_TokensPayment:
		h.Reset()
		h.Write([]byte{1}) // tokens discriminator
		all = append(all, h.Sum(nil)...)

		h.Reset()
		tokenIdentifier := pt.TokensPayment.GetTokenIdentifier()
		if tokenIdentifier == nil || len(tokenIdentifier) == 0 {
			h.Write(make([]byte, 32))
		} else {
			if len(tokenIdentifier) != 32 {
				return nil, fmt.Errorf("token identifier must be exactly 32 bytes")
			}
			h.Write(tokenIdentifier)
		}
		all = append(all, h.Sum(nil)...)

		h.Reset()
		amount := pt.TokensPayment.GetAmount()
		if len(amount) > 16 {
			return nil, fmt.Errorf("token amount exceeds 16 bytes")
		}
		if amount != nil {
			h.Write(amount)
		}
		all = append(all, h.Sum(nil)...)

	case *pb.SparkInvoiceFields_SatsPayment:
		h.Reset()
		h.Write([]byte{2}) // sats discriminator
		all = append(all, h.Sum(nil)...)

		h.Reset()
		var sats uint64
		if pt.SatsPayment != nil && pt.SatsPayment.GetAmount() != 0 {
			sats = pt.SatsPayment.GetAmount()
		}
		b8 := make([]byte, 8)
		binary.BigEndian.PutUint64(b8, sats)
		h.Write(b8)
		all = append(all, h.Sum(nil)...)
	default:
		return nil, fmt.Errorf("unsupported or missing payment type")
	}

	h.Reset()
	if f.Memo != nil {
		h.Write([]byte(*f.Memo))
	}
	all = append(all, h.Sum(nil)...)

	h.Reset()
	spk := f.GetSenderPublicKey()
	if spk == nil || len(spk) == 0 {
		h.Write(make([]byte, 33))
	} else {
		if len(spk) != 33 {
			return nil, fmt.Errorf("sender public key must be exactly 33 bytes")
		}
		h.Write(spk)
	}
	all = append(all, h.Sum(nil)...)

	h.Reset()
	expBytes := make([]byte, 8)
	if ts := f.GetExpiryTime(); ts != nil {
		binary.BigEndian.PutUint64(expBytes, uint64(ts.AsTime().Unix()))
	}
	h.Write(expBytes)
	all = append(all, h.Sum(nil)...)

	h.Reset()
	h.Write(all)
	return h.Sum(nil), nil
}

// VerifySparkAddressSignature verifies that the optional signature included in a SparkAddress
// is a valid Schnorr signature by the receiver (identity_public_key) over the
// SHA-256 hash computed by HashSparkInvoiceFields(spark_invoice_fields).
//
// Returns nil if the signature is present and valid. Returns an error if:
// - the address or required fields are nil
// - the signature is present but invalid
// - the public key cannot be parsed
// If the signature is nil/empty, this function returns an error.
func VerifySparkAddressSignature(addr *pb.SparkAddress, network Network) error {
	if addr == nil {
		return fmt.Errorf("spark address cannot be nil")
	}
	if addr.SparkInvoiceFields == nil {
		return fmt.Errorf("spark invoice fields cannot be nil")
	}
	sig := addr.GetSignature()
	if len(sig) == 0 {
		return fmt.Errorf("signature is required")
	}
	receiverPublicKey, err := keys.ParsePublicKey(addr.IdentityPublicKey)
	if err != nil {
		return fmt.Errorf("failed to parse identity public key: %w", err)
	}
	hash, err := HashSparkInvoiceFields(addr.SparkInvoiceFields, network, receiverPublicKey)
	if err != nil {
		return fmt.Errorf("failed to hash spark invoice fields: %w", err)
	}

	pubKeyBytes := addr.GetIdentityPublicKey()
	if len(pubKeyBytes) == 0 {
		return fmt.Errorf("identity public key is required")
	}
	pubKey, err := secp256k1.ParsePubKey(pubKeyBytes)
	if err != nil {
		return fmt.Errorf("failed to parse identity public key: %w", err)
	}

	schnorrSig, err := schnorr.ParseSignature(sig)
	if err != nil {
		return fmt.Errorf("failed to parse schnorr signature: %w", err)
	}
	if !schnorrSig.Verify(hash, pubKey) {
		return fmt.Errorf("invalid spark address signature with hash: %x, 	sparkinvoicefields: %v, network: %s, receiver public key: %x, sig: %x",
			hash,
			addr.SparkInvoiceFields,
			network,
			receiverPublicKey.Serialize(),
			sig,
		)
	}
	return nil
}
