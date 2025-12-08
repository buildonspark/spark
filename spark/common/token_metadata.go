package common

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"reflect"
	"unicode/utf8"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/protohash"
	pb "github.com/lightsparkdev/spark/proto/spark"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	tokeninternalpb "github.com/lightsparkdev/spark/proto/spark_token_internal"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"golang.org/x/text/unicode/norm"
)

const (
	// CreationEntityPublicKeyLength is the required length in bytes for creation entity public keys
	CreationEntityPublicKeyLength = 33
	// MaxExtraMetadataLength is the maximum length in bytes for extra metadata
	MaxExtraMetadataLength = 1024
	// TokenIdentifierWithExtensionsVersion is the version of the token identifier that includes extensions like extra metadata
	TokenIdentifierWithExtensionsVersion = 2
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
	ErrInvalidCreationEntityPublicKeyLength = fmt.Errorf("creation entity public key must be %d bytes", CreationEntityPublicKeyLength)
	ErrNetworkUnspecified                   = errors.New("network must not be unspecified")
	ErrInvalidExtraMetadataLength           = fmt.Errorf("extra metadata length must be no more than %d bytes", MaxExtraMetadataLength)
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
	CreationEntityPublicKey keys.Public
	Network                 btcnetwork.Network
	ExtraMetadata           []byte
}

var (
	trueHash     = chainhash.HashB([]byte{1})
	falseHash    = chainhash.HashB([]byte{0})
	version1Hash = chainhash.HashB([]byte{1})
)

// NewTokenMetadataFromCreateInput creates a new TokenMetadata object from a
// TokenCreateInput protobuf message and a network.
func NewTokenMetadataFromCreateInput(
	createInput *tokenpb.TokenCreateInput,
	networkProto pb.Network,
) (*TokenMetadata, error) {
	network, err := btcnetwork.FromProtoNetwork(networkProto)
	if err != nil {
		return nil, err
	}
	issuerPubKey, err := keys.ParsePublicKey(createInput.GetIssuerPublicKey())
	if err != nil {
		return nil, sparkerrors.InternalObjectMalformedField(fmt.Errorf("invalid issuer public key: %w", err))
	}

	var creationEntityPubKey keys.Public // The zero value of keys.Public represents the L1 creation entity public key
	creationEntityPubKeyBytes := createInput.GetCreationEntityPublicKey()

	if len(creationEntityPubKeyBytes) > 0 && !bytes.Equal(creationEntityPubKeyBytes, L1CreationEntityPublicKey) {
		var err error
		creationEntityPubKey, err = keys.ParsePublicKey(creationEntityPubKeyBytes)
		if err != nil {
			return nil, sparkerrors.InternalObjectMalformedField(fmt.Errorf("invalid creation entity public key: %w", err))
		}
	}
	return &TokenMetadata{
		IssuerPublicKey:         issuerPubKey,
		TokenName:               createInput.GetTokenName(),
		TokenTicker:             createInput.GetTokenTicker(),
		Decimals:                uint8(createInput.GetDecimals()),
		MaxSupply:               createInput.GetMaxSupply(),
		IsFreezable:             createInput.GetIsFreezable(),
		CreationEntityPublicKey: creationEntityPubKey,
		Network:                 network,
		ExtraMetadata:           createInput.GetExtraMetadata(),
	}, nil
}

func (tm *TokenMetadata) ToTokenMetadataProto() *tokenpb.TokenMetadata {
	tokenIdentifier, err := tm.ComputeTokenIdentifier()
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
		CreationEntityPublicKey: tm.CreationEntityPublicKey.Serialize(),
		TokenIdentifier:         tokenIdentifier,
		ExtraMetadata:           tm.ExtraMetadata,
	}
}

func (tm *TokenMetadata) ComputeTokenIdentifier() (TokenIdentifier, error) {
	if err := tm.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidTokenMetadata, err)
	}

	if tm.HasExtensions() {
		return tm.ComputeTokenIdentifierWithExtensions()
	}
	return tm.ComputeTokenIdentifierV1()
}

func (tm *TokenMetadata) ComputeTokenIdentifierWithExtensions() (TokenIdentifier, error) {
	unencodedTokenIdentifier, err := tm.ToUnencodedTokenIdentifier()
	if err != nil {
		return nil, fmt.Errorf("failed to convert token metadata to proto hash: %w", err)
	}
	hash, err := protohash.Hash(unencodedTokenIdentifier)
	if err != nil {
		return nil, fmt.Errorf("failed to compute token identifier: %w", err)
	}
	return hash, nil
}

// ComputeTokenIdentifierV1 computes the token identifier from this metadata and network
func (tm *TokenMetadata) ComputeTokenIdentifierV1() (TokenIdentifier, error) {
	h := sha256.New()

	// Hash version (1 byte)
	h.Write(version1Hash)

	// Hash issuer public key (33 bytes)
	h.Write(chainhash.HashB(tm.IssuerPublicKey.Serialize()))

	// Hash token name (variable length)
	h.Write(chainhash.HashB([]byte(tm.TokenName)))

	// Hash token symbol/ticker (variable length)
	h.Write(chainhash.HashB([]byte(tm.TokenTicker)))

	// Hash decimals (1 byte)
	h.Write(chainhash.HashB([]byte{tm.Decimals}))

	// Hash max supply (16 bytes)
	h.Write(chainhash.HashB(tm.MaxSupply))

	// Hash freezable flag (1 byte)
	if tm.IsFreezable {
		h.Write(trueHash)
	} else {
		h.Write(falseHash)
	}

	// Hash network (4 bytes)
	networkMagic, err := tm.Network.ToBitcoinNetworkIdentifier()
	if err != nil {
		return nil, sparkerrors.InternalObjectMalformedField(fmt.Errorf("invalid network: %w", err))
	}
	h.Write(chainhash.HashB(binary.BigEndian.AppendUint32(nil, networkMagic)))

	// If L1:
	// Sha256(0 single byte) (not provided)
	// If Spark:
	// Sha256(1 single byte + 33 byte creation entity pub key)  (provided)
	tokenCreateLayer, err := tm.GetTokenCreateLayer()
	if err != nil {
		return nil, fmt.Errorf("failed to get token create layer: %w", err)
	}
	if tokenCreateLayer == TokenCreateLayerL1 {
		h.Write(chainhash.HashB([]byte{byte(tokenCreateLayer)}))
	} else {
		var creationEntityPublicKeyBytes []byte
		if tm.CreationEntityPublicKey.IsZero() {
			creationEntityPublicKeyBytes = L1CreationEntityPublicKey
		} else {
			creationEntityPublicKeyBytes = tm.CreationEntityPublicKey.Serialize()
		}
		h.Write(chainhash.HashB(append([]byte{byte(tokenCreateLayer)}, creationEntityPublicKeyBytes...)))
	}
	return h.Sum(nil), nil
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
	if tm.CreationEntityPublicKey.IsZero() {
		return TokenCreateLayerL1, nil
	}
	return TokenCreateLayerSpark, nil
}

// ValidatePartial checks if the TokenMetadata has all required fields except for the creation entity public key
// This allows validation of a partial token metadata object before the creation entity public key is set
func (tm *TokenMetadata) ValidatePartial() error {
	if tm.IssuerPublicKey.IsZero() {
		return sparkerrors.InternalObjectMissingField(ErrInvalidIssuerPublicKey)
	}
	if tm.TokenName == "" {
		return sparkerrors.InternalObjectMissingField(ErrTokenNameEmpty)
	}
	if !utf8.ValidString(tm.TokenName) || !norm.NFC.IsNormalString(tm.TokenName) {
		return ErrTokenNameUTF8
	}
	if len(tm.TokenName) < 3 || len(tm.TokenName) > 20 {
		return sparkerrors.InternalObjectMalformedField(fmt.Errorf("%w: got %d", ErrTokenNameLength, len(tm.TokenName)))
	}
	if tm.TokenTicker == "" {
		return sparkerrors.InternalObjectMissingField(ErrTokenTickerEmpty)
	}
	if !utf8.ValidString(tm.TokenTicker) || !norm.NFC.IsNormalString(tm.TokenTicker) {
		return sparkerrors.InternalObjectMalformedField(ErrTokenTickerUTF8)
	}
	if len(tm.TokenTicker) < 3 || len(tm.TokenTicker) > 6 {
		return sparkerrors.InternalObjectMalformedField(fmt.Errorf("%w: got %d", ErrTokenTickerLength, len(tm.TokenTicker)))
	}
	if len(tm.MaxSupply) != 16 {
		return sparkerrors.InternalObjectMalformedField(fmt.Errorf("%w: got %d", ErrInvalidMaxSupplyLength, len(tm.MaxSupply)))
	}

	if tm.Network == btcnetwork.Unspecified {
		return sparkerrors.InternalObjectMalformedField(fmt.Errorf("%w: got %s", ErrNetworkUnspecified, tm.Network))
	}

	return nil
}

func (tm *TokenMetadata) ValidateExtensions() error {
	if len(tm.ExtraMetadata) > MaxExtraMetadataLength {
		return sparkerrors.InternalObjectMalformedField(fmt.Errorf("%w: got %d", ErrInvalidExtraMetadataLength, len(tm.ExtraMetadata)))
	}
	return nil
}

// Validate checks if the TokenMetadata has all required fields
func (tm *TokenMetadata) Validate() error {
	if err := tm.ValidatePartial(); err != nil {
		return err
	}
	if err := tm.ValidateExtensions(); err != nil {
		return err
	}
	return nil
}

// v1Fields is a map of field names that are part of the v1 token metadata.
var v1Fields = map[string]struct{}{
	"IssuerPublicKey":         {},
	"TokenName":               {},
	"TokenTicker":             {},
	"Decimals":                {},
	"MaxSupply":               {},
	"IsFreezable":             {},
	"CreationEntityPublicKey": {},
	"Network":                 {},
}

// If TokenMetadata has any fields that are not part of the v1Fields map, it has extensions and should be hashed using the ComputeTokenIdentifierWithExtensions method.
func (tm *TokenMetadata) HasExtensions() bool {
	tmValue := reflect.ValueOf(*tm)
	tmType := tmValue.Type()

	for i := 0; i < tmValue.NumField(); i++ {
		fieldName := tmType.Field(i).Name

		if _, isV1Field := v1Fields[fieldName]; isV1Field {
			continue
		}

		if !tmValue.Field(i).IsZero() {
			return true
		}
	}
	return false
}

func (tm *TokenMetadata) ToUnencodedTokenIdentifier() (*tokeninternalpb.UnencodedTokenIdentifier, error) {
	networkProto, err := tm.Network.ToProtoNetwork()
	if err != nil {
		return nil, sparkerrors.InternalTypeConversionError(fmt.Errorf("failed to convert network (numerical value: %s) to proto: %w", tm.Network, err))
	}

	var creationEntityPubKeyBytes []byte
	if tm.CreationEntityPublicKey.IsZero() {
		creationEntityPubKeyBytes = L1CreationEntityPublicKey
	} else {
		creationEntityPubKeyBytes = tm.CreationEntityPublicKey.Serialize()
	}
	return &tokeninternalpb.UnencodedTokenIdentifier{
		Version:                 uint32(TokenIdentifierWithExtensionsVersion),
		IssuerPublicKey:         tm.IssuerPublicKey.Serialize(),
		TokenName:               tm.TokenName,
		TokenTicker:             tm.TokenTicker,
		Decimals:                uint32(tm.Decimals),
		MaxSupply:               tm.MaxSupply,
		IsFreezable:             tm.IsFreezable,
		Network:                 networkProto,
		CreationEntityPublicKey: creationEntityPubKeyBytes,
		ExtraMetadata:           tm.ExtraMetadata,
	}, nil
}
