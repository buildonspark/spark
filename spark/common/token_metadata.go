package common

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"golang.org/x/text/unicode/norm"
)

const (
	// CreationEntityPublicKeyLength is the required length in bytes for creation entity public keys
	CreationEntityPublicKeyLength = 33
)

var (
	// L1CreationEntityPublicKey is a zero-filled byte array of length CreationEntityPublicKeyLength
	L1CreationEntityPublicKey = make([]byte, CreationEntityPublicKeyLength)

	ErrInvalidTokenMetadata                 = errors.New("token metadata is invalid")
	ErrInvalidIssuerPublicKey               = errors.New("issuer public key must be set")
	ErrTokenNameEmpty                       = errors.New("token name cannot be empty")
	ErrTokenNameUTF8                        = errors.New("token name contains invalid or non-normalized UTF-8")
	ErrTokenNameLength                      = errors.New("token name must be between 3 and 20 bytes")
	ErrTokenTickerEmpty                     = errors.New("token ticker cannot be empty")
	ErrTokenTickerUTF8                      = errors.New("token ticker contains invalid or non-normalized UTF-8")
	ErrTokenTickerLength                    = errors.New("token ticker must be between 3 and 6 bytes")
	ErrInvalidMaxSupplyLength               = errors.New("max supply must be 16 bytes")
	ErrCreationEntityPublicKeyEmpty         = errors.New("creation entity public key cannot be empty")
	ErrInvalidCreationEntityPublicKeyLength = errors.New("creation entity public key must be 33 bytes")
	ErrNetworkUnspecified                   = errors.New("network must not be unspecified")
)

// TokenMetadataProvider is an interface for objects that can be converted to TokenMetadata.
type TokenMetadataProvider interface {
	ToTokenMetadata() (*TokenMetadata, error)
}

// TokenIdentifier represents a unique identifier for a token
type TokenIdentifier []byte

// TokenMetadata represents the core metadata needed to compute a token identifier
type TokenMetadata struct {
	IssuerPublicKey         keys.Public
	TokenName               string
	TokenTicker             string
	Decimals                uint8
	MaxSupply               []byte
	IsFreezable             bool
	CreationEntityPublicKey []byte
	Network                 Network
}

var (
	trueHash     = sha256Slice([]byte{1})
	falseHash    = sha256Slice([]byte{0})
	version1Hash = sha256Slice([]byte{1})
)

// NewTokenMetadataFromCreateInput creates a new TokenMetadata object from a
// TokenCreateInput protobuf message and a network.
func NewTokenMetadataFromCreateInput(
	createInput *tokenpb.TokenCreateInput,
	networkProto pb.Network,
) (*TokenMetadata, error) {
	network, err := NetworkFromProtoNetwork(networkProto)
	if err != nil {
		return nil, fmt.Errorf("invalid network: %w", err)
	}
	issuerPubKey, err := keys.ParsePublicKey(createInput.GetIssuerPublicKey())
	if err != nil {
		return nil, fmt.Errorf("invalid issuer public key: %w", err)
	}
	return &TokenMetadata{
		IssuerPublicKey:         issuerPubKey,
		TokenName:               createInput.GetTokenName(),
		TokenTicker:             createInput.GetTokenTicker(),
		Decimals:                uint8(createInput.GetDecimals()),
		MaxSupply:               createInput.GetMaxSupply(),
		IsFreezable:             createInput.GetIsFreezable(),
		CreationEntityPublicKey: createInput.GetCreationEntityPublicKey(),
		Network:                 network,
	}, nil
}

func (tm *TokenMetadata) ToTokenMetadataProto() *tokenpb.TokenMetadata {
	tokenIdentifier, err := tm.ComputeTokenIdentifierV1()
	if err != nil {
		return nil
	}
	return &tokenpb.TokenMetadata{
		IssuerPublicKey:         tm.IssuerPublicKey.Serialize(),
		TokenName:               tm.TokenName,
		TokenTicker:             tm.TokenTicker,
		Decimals:                uint32(tm.Decimals),
		MaxSupply:               tm.MaxSupply,
		IsFreezable:             tm.IsFreezable,
		CreationEntityPublicKey: tm.CreationEntityPublicKey,
		TokenIdentifier:         tokenIdentifier,
	}
}

// ComputeTokenIdentifierV1 computes the token identifier from this metadata and network
func (tm *TokenMetadata) ComputeTokenIdentifierV1() (TokenIdentifier, error) {
	if err := tm.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidTokenMetadata, err)
	}

	h := sha256.New()

	// Hash version (1 byte)
	h.Write(version1Hash)

	// Hash issuer public key (33 bytes)
	h.Write(sha256Slice(tm.IssuerPublicKey.Serialize()))

	// Hash token name (variable length)
	h.Write(sha256Slice([]byte(tm.TokenName)))

	// Hash token symbol/ticker (variable length)
	h.Write(sha256Slice([]byte(tm.TokenTicker)))

	// Hash decimals (1 byte)
	h.Write(sha256Slice([]byte{tm.Decimals}))

	// Hash max supply (16 bytes)
	h.Write(sha256Slice(tm.MaxSupply))

	// Hash freezable flag (1 byte)
	if tm.IsFreezable {
		h.Write(trueHash)
	} else {
		h.Write(falseHash)
	}

	// Hash network (4 bytes)
	networkMagic, err := BitcoinNetworkIdentifierFromNetwork(tm.Network)
	if err != nil {
		return nil, fmt.Errorf("invalid network: %w", err)
	}
	h.Write(sha256Slice(binary.BigEndian.AppendUint32(nil, networkMagic)))

	// If L1:
	// Sha256(0 single byte) (not provided)
	// If Spark:
	// Sha256(1 single byte + 33 byte creation entity pub key)  (provided)
	tokenCreateLayer, err := tm.GetTokenCreateLayer()
	if err != nil {
		return nil, fmt.Errorf("failed to get token create layer: %w", err)
	}
	if tokenCreateLayer == TokenCreateLayerL1 {
		h.Write(sha256Slice([]byte{byte(tokenCreateLayer)}))
	} else {
		h.Write(sha256Slice(append([]byte{byte(tokenCreateLayer)}, tm.CreationEntityPublicKey...)))
	}
	return h.Sum(nil), nil
}

func sha256Slice(bytes []byte) []byte {
	hash := sha256.Sum256(bytes)
	return hash[:]
}

type TokenCreateLayer int

const (
	TokenCreateLayerUnknown TokenCreateLayer = iota
	TokenCreateLayerL1
	TokenCreateLayerSpark
)

// GetTokenCreateLayer returns the layer where the token was created (L1 or Spark).
// A token is considered L1-created if its CreationEntityPublicKey is all zeros.
func (tm *TokenMetadata) GetTokenCreateLayer() (TokenCreateLayer, error) {
	if tm.CreationEntityPublicKey == nil {
		return TokenCreateLayerUnknown, ErrCreationEntityPublicKeyEmpty
	}
	if len(tm.CreationEntityPublicKey) != CreationEntityPublicKeyLength {
		return TokenCreateLayerUnknown, fmt.Errorf("%w: creation entity public key must be %d bytes", ErrInvalidCreationEntityPublicKeyLength, CreationEntityPublicKeyLength)
	}
	if bytes.Equal(tm.CreationEntityPublicKey, L1CreationEntityPublicKey) {
		return TokenCreateLayerL1, nil
	}
	return TokenCreateLayerSpark, nil
}

// ValidatePartial checks if the TokenMetadata has all required fields except for the creation entity public key
// This allows validation of a partial token metadata object before the creation entity public key is set
func (tm *TokenMetadata) ValidatePartial() error {
	if tm.IssuerPublicKey.IsZero() {
		return ErrInvalidIssuerPublicKey
	}
	if tm.TokenName == "" {
		return ErrTokenNameEmpty
	}
	if !utf8.ValidString(tm.TokenName) || !norm.NFC.IsNormalString(tm.TokenName) {
		return ErrTokenNameUTF8
	}
	if len(tm.TokenName) < 3 || len(tm.TokenName) > 20 {
		return fmt.Errorf("%w: got %d", ErrTokenNameLength, len(tm.TokenName))
	}
	if tm.TokenTicker == "" {
		return ErrTokenTickerEmpty
	}
	if !utf8.ValidString(tm.TokenTicker) || !norm.NFC.IsNormalString(tm.TokenTicker) {
		return ErrTokenTickerUTF8
	}
	if len(tm.TokenTicker) < 3 || len(tm.TokenTicker) > 6 {
		return fmt.Errorf("%w: got %d", ErrTokenTickerLength, len(tm.TokenTicker))
	}
	if len(tm.MaxSupply) != 16 {
		return fmt.Errorf("%w: got %d", ErrInvalidMaxSupplyLength, len(tm.MaxSupply))
	}

	if tm.Network == Unspecified {
		return fmt.Errorf("%w: got %s", ErrNetworkUnspecified, tm.Network)
	}

	return nil
}

// Validate checks if the TokenMetadata has all required fields
func (tm *TokenMetadata) Validate() error {
	if err := tm.ValidatePartial(); err != nil {
		return err
	}
	if len(tm.CreationEntityPublicKey) != CreationEntityPublicKeyLength {
		return fmt.Errorf("%w: got %d", ErrInvalidCreationEntityPublicKeyLength, len(tm.CreationEntityPublicKey))
	}
	return nil
}
