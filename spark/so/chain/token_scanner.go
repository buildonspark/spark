package chain

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"unicode/utf8"

	"github.com/lightsparkdev/spark/common/keys"
	"go.uber.org/zap"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/l1tokencreate"
	"github.com/lightsparkdev/spark/so/ent/tokencreate"
	"golang.org/x/text/unicode/norm"
)

const (
	// announcementPrefix is the constant prefix to differentiate lrc20 announcements from other protocols
	announcementPrefix = "LRC20"
	// announcementPrefixSizeBytes is the length of the announcement prefix in bytes
	announcementPrefixSizeBytes = 5
	// announcementKindSizeBytes is the length of the announcement kind in bytes
	announcementKindSizeBytes = 2
	// minNameSizeBytes is the minimum size of the name in bytes
	minNameSizeBytes = 3
	// maxNameSizeBytes is the maximum size of the name in bytes
	maxNameSizeBytes = 20
	// minTickerSizeBytes is the minimum size of the ticker in bytes
	minTickerSizeBytes = 3
	// maxTickerSizeBytes is the maximum size of the ticker in bytes
	maxTickerSizeBytes = 6
	// tokenPubKeySizeBytes is the size of the token pubkey in bytes
	tokenPubKeySizeBytes = 33
	// maxSupplySizeBytes is the size of the max supply in bytes
	maxSupplySizeBytes = 16
	// expectedFormatOutputStr is the expected format of the token announcement for error logs
	expectedFormatOutputStr = "Expected format: [token_pubkey(33)] + [name_len(1)] + [name(variable)] + [ticker_len(1)] + [ticker(variable)] + [decimal(1)] + [max_supply(16)] + [is_freezable(1)]"
)

// creationAnnouncementKind indicates this Announcement is for token creation
var creationAnnouncementKind = [2]byte{0, 0}

// validatePushBytes parses a Bitcoin script push operation and returns any error. It advances the provided buffer to
// the end of the push metadata. If an error occurs, there are no guarantees about the buffer's subsequent state.
// It handles OP_PUSHDATA1 (0x4c), OP_PUSHDATA2 (0x4d), and OP_PUSHDATA4 (0x4e) as well as direct pushes (0x01-0x4b).
func validatePushBytes(script *bytes.Buffer) error {
	totalLen := script.Len() + 1 // For OP_RETURN
	if totalLen <= 2 {
		return fmt.Errorf("script too short: no push operation")
	}

	pushOp, err := readByte(script)
	if err != nil {
		return err
	}

	// Parse data length based on push operation
	var dataLength int
	switch {
	case pushOp >= 0x01 && pushOp < 0x4b:
		// Direct push of 1-75 bytes
		dataLength = int(pushOp)
	case pushOp == txscript.OP_PUSHDATA1:
		// OP_PUSHDATA1: next byte is length
		length, err := readByte(script)
		if err != nil {
			return fmt.Errorf("script too short for OP_PUSHDATA1")
		}
		dataLength = int(length)
	case pushOp == txscript.OP_PUSHDATA2:
		// OP_PUSHDATA2: next 2 bytes are length (little-endian)
		lengthBytes := script.Next(2)
		if len(lengthBytes) != 2 {
			return fmt.Errorf("script too short for OP_PUSHDATA2")
		}
		dataLength = int(binary.LittleEndian.Uint16(lengthBytes))
	case pushOp == txscript.OP_PUSHDATA4:
		// OP_PUSHDATA4: next 4 bytes are length (little-endian)
		lengthBytes := script.Next(4)
		if len(lengthBytes) != 4 {
			return fmt.Errorf("script too short for OP_PUSHDATA4")
		}
		dataLength = int(binary.LittleEndian.Uint32(lengthBytes))
	default:
		// Not a standard push operation, so we can't parse it.
		return fmt.Errorf("unparseable pushBytes")
	}

	// Verify we have exactly the right amount of data. dataLength holds the number of bytes that the script has told
	// us remain to be parsed, while script.Len() is the number of bytes that actually remain unread in the buffer.
	if script.Len() != dataLength {
		return fmt.Errorf("script length mismatch: expected %d bytes total, got %d (pushOp=0x%02x, dataLength=%d, offset=%d)", dataLength, totalLen, pushOp, dataLength, totalLen-script.Len()-1)
	}

	return nil
}

// Construct an L1TokenCreate entity from a token announcement script.
// Returns nil if the transaction is not detected to be a token announcement (even if malformed).
// Returns an error if the script is an invalid or malformed LRC20 token announcement.
func parseTokenAnnouncement(script []byte, network common.Network) (*ent.L1TokenCreate, error) {
	buf := bytes.NewBuffer(script)
	if op, err := buf.ReadByte(); err != nil || op != txscript.OP_RETURN {
		return nil, nil // Not an OP_RETURN script
	}
	if err := validatePushBytes(buf); err != nil {
		return nil, nil // Invalid OP_RETURN script.
	}

	// Check for LRC20 prefix
	if prefix := buf.Next(announcementPrefixSizeBytes); !bytes.Equal(prefix, []byte(announcementPrefix)) {
		return nil, nil // Not an LRC20 announcement
	}
	if announcementKind := buf.Next(announcementKindSizeBytes); !bytes.Equal(announcementKind, creationAnnouncementKind[:]) {
		return nil, nil // Not a token creation announcement
	}

	// Format: [token_pubkey(33)] + [name_len(1)] + [name(variable)] + [ticker_len(1)] + [ticker(variable)] + [decimal(1)] + [max_supply(16)] + [is_freezable(1)]
	issuerPubkey, err := readBytes(buf, tokenPubKeySizeBytes)
	if err != nil {
		return nil, fmt.Errorf("invalid issuer public key: %w", err)
	}

	name, err := readVarLenStr(buf, minNameSizeBytes, maxNameSizeBytes)
	if err != nil {
		return nil, fmt.Errorf("invalid name: %w", err)
	}

	ticker, err := readVarLenStr(buf, minTickerSizeBytes, maxTickerSizeBytes)
	if err != nil {
		return nil, fmt.Errorf("invalid ticker: %w", err)
	}

	decimal, err := readByte(buf)
	if err != nil {
		return nil, fmt.Errorf("invalid decimal: %w", err)
	}

	maxSupply, err := readBytes(buf, maxSupplySizeBytes)
	if err != nil {
		return nil, fmt.Errorf("invalid max supply: %w", err)
	}

	isFreezable, err := readByte(buf)
	if err != nil {
		return nil, fmt.Errorf("invalid is_freezable: %w", err)
	}

	// This handles the case where the script says it contains N bytes, and actually does contain N bytes, but
	// we've parsed all the fields out and there are still bytes left over, meaning the script has extra data in it.
	if buf.Len() > 0 {
		return nil, fmt.Errorf("unexpected data after token announcement: got %d extra bytes", buf.Len())
	}

	return &ent.L1TokenCreate{
		IssuerPublicKey: issuerPubkey,
		TokenName:       name,
		TokenTicker:     ticker,
		Decimals:        decimal,
		MaxSupply:       maxSupply,
		IsFreezable:     isFreezable != 0,
		Network:         common.SchemaNetwork(network),
	}, nil
}

func readVarLenStr(buf *bytes.Buffer, minBytes int, maxBytes int) (string, error) {
	lengthByte, err := readByte(buf)
	if err != nil {
		return "", fmt.Errorf("invalid length: %w", err)
	}
	length := int(lengthByte)
	if length < minBytes || length > maxBytes {
		return "", fmt.Errorf("invalid length: expected between %d and %d, got %d. %s",
			minBytes, maxBytes, length, expectedFormatOutputStr)
	}
	asBytes, err := readBytes(buf, length)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(asBytes) {
		return "", fmt.Errorf("invalid UTF-8. %s", expectedFormatOutputStr)
	}
	if !norm.NFC.IsNormal(asBytes) {
		return "", fmt.Errorf("not NFC-normalized. %s", expectedFormatOutputStr)
	}
	return string(asBytes), nil
}

func readBytes(buf *bytes.Buffer, want int) ([]byte, error) {
	asBytes := buf.Next(want)
	if len(asBytes) != want {
		return nil, fmt.Errorf("insufficient data: expected %d byte(s), got %d bytes. %s", want, len(asBytes), expectedFormatOutputStr)
	}
	return asBytes, nil
}

func readByte(buf *bytes.Buffer) (byte, error) {
	asByte, err := buf.ReadByte()
	if err != nil {
		return 0, fmt.Errorf("insufficient data: expected 1 byte, got 0 bytes. %s", expectedFormatOutputStr)
	}
	return asByte, nil
}

func createL1TokenEntity(ctx context.Context, dbTx *ent.Tx, tokenMetadata *common.TokenMetadata, txid chainhash.Hash, tokenIdentifier []byte) (*ent.L1TokenCreate, error) {
	schemaNetwork, err := common.SchemaNetworkFromNetwork(tokenMetadata.Network)
	if err != nil {
		return nil, fmt.Errorf("failed to convert network to schema network: %w", err)
	}
	// This entity represents the raw parsed L1 announcement data.
	l1TokenCreate, err := dbTx.L1TokenCreate.Create().
		SetIssuerPublicKey(tokenMetadata.IssuerPublicKey).
		SetTokenName(tokenMetadata.TokenName).
		SetTokenTicker(tokenMetadata.TokenTicker).
		SetDecimals(tokenMetadata.Decimals).
		SetMaxSupply(tokenMetadata.MaxSupply).
		SetIsFreezable(tokenMetadata.IsFreezable).
		SetNetwork(schemaNetwork).
		SetTransactionID(txid.CloneBytes()).
		SetTokenIdentifier(tokenIdentifier).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create l1 token create entity: %w", err)
	}
	return l1TokenCreate, nil
}

func createNativeSparkTokenEntity(ctx context.Context, dbTx *ent.Tx, tokenMetadata *common.TokenMetadata, l1TokenCreateID uuid.UUID) error {
	entityDkgKeyPublicKey, err := ent.GetEntityDkgKeyPublicKey(ctx, dbTx.Client())
	if err != nil {
		return fmt.Errorf("failed to get entity DKG key public key: %w", err)
	}
	// Recompute the token identifier using the Spark creation entity public key.
	// The token identifier that was computed above corresponds to the L1 announcement
	// (creation entity key = 0x00..00). For the Spark `token_creates` table we use
	// the SO entity DKG key as the creation entity key.
	sparkTokenMetadata := *tokenMetadata
	sparkTokenMetadata.CreationEntityPublicKey = entityDkgKeyPublicKey.Serialize()
	sparkTokenIdentifier, err := sparkTokenMetadata.ComputeTokenIdentifierV1()
	if err != nil {
		return fmt.Errorf("failed to compute Spark token identifier: %w", err)
	}
	schemaNetwork, err := common.SchemaNetworkFromNetwork(tokenMetadata.Network)
	if err != nil {
		return fmt.Errorf("failed to convert network to schema network: %w", err)
	}

	_, err = dbTx.TokenCreate.Create().
		SetIssuerPublicKey(tokenMetadata.IssuerPublicKey).
		SetTokenName(tokenMetadata.TokenName).
		SetTokenTicker(tokenMetadata.TokenTicker).
		SetDecimals(tokenMetadata.Decimals).
		SetMaxSupply(tokenMetadata.MaxSupply).
		SetIsFreezable(tokenMetadata.IsFreezable).
		SetNetwork(schemaNetwork).
		SetCreationEntityPublicKey(entityDkgKeyPublicKey.Serialize()).
		SetTokenIdentifier(sparkTokenIdentifier).
		SetL1TokenCreateID(l1TokenCreateID).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to create spark native token create entity: %w", err)
	}
	return nil
}

// handleTokenAnnouncements processes any token announcements in the block
func handleTokenAnnouncements(ctx context.Context, config *so.Config, dbTx *ent.Tx, txs []wire.MsgTx, network common.Network) error {
	logger := logging.GetLoggerFromContext(ctx)

	type parsedAnnouncement struct {
		l1TokenToCreate *ent.L1TokenCreate
		txHash          chainhash.Hash
		outputIdx       int
	}
	var announcements []parsedAnnouncement
	for _, tx := range txs {
		for txOutIdx, txOut := range tx.TxOut {
			l1TokenToCreate, err := parseTokenAnnouncement(txOut.PkScript, network)
			if err != nil {
				logger.With(zap.Error(err)).
					Sugar().
					Errorf(
						"Failed to parse token announcement (txid: %s, idx: %s, script: %s)",
						tx.TxHash(),
						txOutIdx,
						hex.EncodeToString(txOut.PkScript),
					)
				continue
			}

			if l1TokenToCreate != nil {
				announcements = append(announcements, parsedAnnouncement{
					l1TokenToCreate: l1TokenToCreate,
					txHash:          tx.TxHash(),
					outputIdx:       txOutIdx,
				})
			}
		}
	}

	tokenIdentifiersAnnouncedInBlock := make(map[string]struct{})
	issuerPublicKeysAnnouncedInBlock := make(map[keys.Public]struct{})
	for _, ann := range announcements {
		announcementIssuerPubKey, err := keys.ParsePublicKey(ann.l1TokenToCreate.IssuerPublicKey)
		if err != nil {
			return fmt.Errorf("failed to parse issuer public key: %w", err)
		}
		logger.With(zap.Stringer("issuer_public_key", announcementIssuerPubKey)).
			Sugar().
			Infof(
				"Successfully parsed token announcement (txid: %s, output_idex: %d, name: %s, ticker: %s)",
				ann.txHash,
				ann.outputIdx,
				ann.l1TokenToCreate.TokenName,
				ann.l1TokenToCreate.TokenTicker,
			)

		provider := ann.l1TokenToCreate
		tokenMetadata, err := provider.ToTokenMetadata()
		if err != nil {
			logger.Error("failed to get token metadata", zap.Error(err))
			continue
		}

		if err := tokenMetadata.Validate(); err != nil {
			logger.Error("Invalid token metadata", zap.Error(err))
			continue
		}

		tokenIdentifier, err := tokenMetadata.ComputeTokenIdentifierV1()
		if err != nil {
			logger.Error("Failed to compute token identifier", zap.Error(err))
			continue
		}

		isDuplicate, err := isDuplicateAnnouncement(ctx, dbTx, tokenIdentifier, tokenIdentifiersAnnouncedInBlock)
		if err != nil {
			logger.With(zap.Error(err)).Sugar().Errorf("Failed to check for duplicate announcement (txid %s)", ann.txHash)
			continue
		}
		tokenIssuerPubKey, err := keys.ParsePublicKey(tokenMetadata.IssuerPublicKey)
		if err != nil {
			logger.With(zap.Error(err)).Sugar().Error("Failed to parse issuer public key (txid %s)", ann.txHash)
			continue
		}
		if isDuplicate {
			logger.With(zap.Stringer("issuer_public_key", tokenIssuerPubKey)).
				Sugar().
				Infof("Token with this issuer public key already exists. Ignoring the announcement (txid %s)", ann.txHash)
			continue
		}

		l1TokenCreate, err := createL1TokenEntity(ctx, dbTx, tokenMetadata, ann.txHash, tokenIdentifier)
		if err != nil {
			logger.With(zap.Error(err)).Sugar().Errorf("Failed to create l1 token create entity (txid %s)", ann.txHash)
			continue
		}
		logger.With(zap.String("issuer_public_key", hex.EncodeToString(l1TokenCreate.IssuerPublicKey))).
			Sugar().
			Infof(
				"Successfully created L1 token entity (txid %s, output_idex %d, name %s, identifier %s)",
				ann.txHash,
				ann.outputIdx,
				l1TokenCreate.TokenName,
				hex.EncodeToString(l1TokenCreate.TokenIdentifier),
			)

		if !config.Token.DisableSparkTokenCreationForL1TokenAnnouncements {
			exists, err := issuerAlreadyHasSparkToken(ctx, dbTx, tokenIssuerPubKey, issuerPublicKeysAnnouncedInBlock)
			if err != nil {
				logger.Error("Failed to check for existing spark token", zap.Error(err))
				continue
			}
			if exists {
				logger.With(zap.Stringer("issuer_public_key", tokenIssuerPubKey)).
					Sugar().
					Infof("Issuer already has a Spark token. Not creating a spark native token (txid %s).", ann.txHash)
			} else {
				if err := createNativeSparkTokenEntity(ctx, dbTx, tokenMetadata, l1TokenCreate.ID); err != nil {
					logger.With(zap.Error(err)).Sugar().Errorf("Failed to create spark native token create entity (txid %s)", ann.txHash)
				}
			}
		}
		tokenIdentifiersAnnouncedInBlock[hex.EncodeToString(tokenIdentifier)] = struct{}{}
		issuerPublicKeysAnnouncedInBlock[announcementIssuerPubKey] = struct{}{}
	}
	return nil
}

func handleTokenUpdatesForBlock(
	ctx context.Context,
	config *so.Config,
	dbTx *ent.Tx,
	txs []wire.MsgTx,
	blockHeight int64,
	network common.Network,
) {
	logger := logging.GetLoggerFromContext(ctx)
	logger.Sugar().Infof("Checking for token announcements (block height %d)", blockHeight)
	if err := handleTokenAnnouncements(ctx, config, dbTx, txs, network); err != nil {
		logger.With(zap.Error(err)).Sugar().Errorf("Failed to handle token announcements (block height %d)", blockHeight)
	}
}

func isDuplicateAnnouncement(ctx context.Context, dbTx *ent.Tx, tokenIdentifier []byte, tokenIdentifiersAnnouncedInBlock map[string]struct{}) (bool, error) {
	exists, err := dbTx.L1TokenCreate.Query().
		Where(l1tokencreate.TokenIdentifierEQ(tokenIdentifier)).
		Exist(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to query for existing l1 token create: %w", err)
	}
	if exists {
		return true, nil
	}
	_, ok := tokenIdentifiersAnnouncedInBlock[string(tokenIdentifier)]
	return ok, nil
}

func issuerAlreadyHasSparkToken(ctx context.Context, dbTx *ent.Tx, issuerPublicKey keys.Public, issuerPublicKeysAnnouncedInBlock map[keys.Public]struct{}) (bool, error) {
	exists, err := dbTx.TokenCreate.Query().
		Where(tokencreate.IssuerPublicKeyEQ(issuerPublicKey.Serialize())).
		Exist(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to query for existing spark token: %w", err)
	}
	if exists {
		return true, nil
	}
	_, ok := issuerPublicKeysAnnouncedInBlock[issuerPublicKey]
	return ok, nil
}
