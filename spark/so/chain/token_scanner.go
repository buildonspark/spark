package chain

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"unicode/utf8"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/rpcclient"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/logging"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/chain/tokens"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/l1tokencreate"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/tokencreate"
	"go.uber.org/zap"
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

// Construct an L1TokenCreate entity from a token announcement script.
// Returns nil if the transaction is not detected to be a token announcement (even if malformed).
// Returns an error if the script is an invalid or malformed LRC20 token announcement.
func parseTokenAnnouncement(script []byte, network btcnetwork.Network) (*ent.L1TokenCreate, error) {
	buf := bytes.NewBuffer(script)
	if op, err := buf.ReadByte(); err != nil || op != txscript.OP_RETURN {
		return nil, nil // Not an OP_RETURN script
	}
	if err := common.ValidatePushBytes(buf); err != nil {
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
	issuerPubKeyBytes, err := common.ReadBytes(buf, tokenPubKeySizeBytes)
	if err != nil {
		return nil, fmt.Errorf("invalid issuer public key: %w", err)
	}
	issuerPubKey, err := keys.ParsePublicKey(issuerPubKeyBytes)
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

	decimal, err := common.ReadByte(buf)
	if err != nil {
		return nil, fmt.Errorf("invalid decimal: %w", err)
	}

	maxSupply, err := common.ReadBytes(buf, maxSupplySizeBytes)
	if err != nil {
		return nil, fmt.Errorf("invalid max supply: %w", err)
	}

	isFreezable, err := common.ReadByte(buf)
	if err != nil {
		return nil, fmt.Errorf("invalid is_freezable: %w", err)
	}

	// This handles the case where the script says it contains N bytes, and actually does contain N bytes, but
	// we've parsed all the fields out and there are still bytes left over, meaning the script has extra data in it.
	if buf.Len() > 0 {
		return nil, fmt.Errorf("unexpected data after token announcement: got %d extra bytes", buf.Len())
	}

	return &ent.L1TokenCreate{
		IssuerPublicKey: issuerPubKey,
		TokenName:       name,
		TokenTicker:     ticker,
		Decimals:        decimal,
		MaxSupply:       maxSupply,
		IsFreezable:     isFreezable != 0,
		Network:         network,
	}, nil
}

func readVarLenStr(buf *bytes.Buffer, minBytes int, maxBytes int) (string, error) {
	lengthByte, err := common.ReadByte(buf)
	if err != nil {
		return "", fmt.Errorf("invalid length: %w", err)
	}
	length := int(lengthByte)
	if length < minBytes || length > maxBytes {
		return "", fmt.Errorf("invalid length: expected between %d and %d, got %d. %s",
			minBytes, maxBytes, length, expectedFormatOutputStr)
	}
	asBytes, err := common.ReadBytes(buf, length)
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

func createL1TokenEntity(ctx context.Context, dbClient *ent.Client, tokenMetadata *common.TokenMetadata, txid chainhash.Hash, tokenIdentifier []byte) (*ent.L1TokenCreate, error) {
	txidSchema, err := st.NewTxIDFromBytes(txid.CloneBytes())
	if err != nil {
		return nil, fmt.Errorf("failed to create TxID: %w", err)
	}
	// This entity represents the raw parsed L1 announcement data.
	l1TokenCreate, err := dbClient.L1TokenCreate.Create().
		SetIssuerPublicKey(tokenMetadata.IssuerPublicKey).
		SetTokenName(tokenMetadata.TokenName).
		SetTokenTicker(tokenMetadata.TokenTicker).
		SetDecimals(tokenMetadata.Decimals).
		SetMaxSupply(tokenMetadata.MaxSupply).
		SetIsFreezable(tokenMetadata.IsFreezable).
		SetNetwork(tokenMetadata.Network).
		SetTransactionID(txidSchema).
		SetTokenIdentifier(tokenIdentifier).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create l1 token create entity: %w", err)
	}
	return l1TokenCreate, nil
}

func createNativeSparkTokenEntity(ctx context.Context, dbClient *ent.Client, tokenMetadata *common.TokenMetadata, l1TokenCreateID uuid.UUID) error {
	entityDkgKeyPublicKey, err := ent.GetEntityDkgKeyPublicKey(ctx, dbClient)
	if err != nil {
		return fmt.Errorf("failed to get entity DKG key public key: %w", err)
	}
	// Recompute the token identifier using the Spark creation entity public key.
	// The token identifier that was computed above corresponds to the L1 announcement
	// (creation entity key = 0x00..00). For the Spark `token_creates` table we use
	// the SO entity DKG key as the creation entity key.
	sparkTokenMetadata := *tokenMetadata
	sparkTokenMetadata.CreationEntityPublicKey = entityDkgKeyPublicKey
	sparkTokenIdentifier, err := sparkTokenMetadata.ComputeTokenIdentifier()
	if err != nil {
		return fmt.Errorf("failed to compute Spark token identifier: %w", err)
	}

	_, err = dbClient.TokenCreate.Create().
		SetIssuerPublicKey(tokenMetadata.IssuerPublicKey).
		SetTokenName(tokenMetadata.TokenName).
		SetTokenTicker(tokenMetadata.TokenTicker).
		SetDecimals(tokenMetadata.Decimals).
		SetMaxSupply(tokenMetadata.MaxSupply).
		SetIsFreezable(tokenMetadata.IsFreezable).
		SetNetwork(tokenMetadata.Network).
		SetCreationEntityPublicKey(entityDkgKeyPublicKey).
		SetTokenIdentifier(sparkTokenIdentifier).
		SetL1TokenCreateID(l1TokenCreateID).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to create spark native token create entity: %w", err)
	}
	return nil
}

// handleTokenAnnouncements processes any token announcements in the block
func handleTokenAnnouncements(ctx context.Context, config *so.Config, dbClient *ent.Client, txs []wire.MsgTx, network btcnetwork.Network) error {
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
						"Failed to parse token announcement (txid: %s, idx: %v, script: %s)",
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
	for _, ann := range announcements {
		logger.With(zap.Stringer("issuer_public_key", ann.l1TokenToCreate.IssuerPublicKey)).
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

		tokenIdentifier, err := tokenMetadata.ComputeTokenIdentifier()
		if err != nil {
			logger.Error("Failed to compute token identifier", zap.Error(err))
			continue
		}

		isDuplicate, err := isDuplicateAnnouncement(ctx, dbClient, tokenIdentifier, tokenIdentifiersAnnouncedInBlock)
		if err != nil {
			logger.With(zap.Error(err)).Sugar().Errorf("Failed to check for duplicate announcement (txid %s)", ann.txHash)
			continue
		}
		if isDuplicate {
			logger.With(zap.Stringer("issuer_public_key", tokenMetadata.IssuerPublicKey)).
				Sugar().
				Infof("Token with this issuer public key already exists. Ignoring the announcement (txid %s)", ann.txHash)
			continue
		}

		l1TokenCreate, err := createL1TokenEntity(ctx, dbClient, tokenMetadata, ann.txHash, tokenIdentifier)
		if err != nil {
			logger.With(zap.Error(err)).Sugar().Errorf("Failed to create l1 token create entity (txid %s)", ann.txHash)
			continue
		}
		logger.With(zap.String("issuer_public_key", l1TokenCreate.IssuerPublicKey.ToHex())).
			Sugar().
			Infof(
				"Successfully created L1 token entity (txid %s, output_idex %d, name %s, identifier %s)",
				ann.txHash,
				ann.outputIdx,
				l1TokenCreate.TokenName,
				hex.EncodeToString(l1TokenCreate.TokenIdentifier),
			)

		if !config.Token.DisableSparkTokenCreationForL1TokenAnnouncements {
			exists, err := tokenIdentifierAlreadyExists(ctx, dbClient, tokenIdentifier, tokenIdentifiersAnnouncedInBlock)
			if err != nil {
				logger.Error("Failed to check for existing spark token", zap.Error(err))
				continue
			}
			if exists {
				logger.With(
					zap.String("token_identifier", hex.EncodeToString(tokenIdentifier)),
					zap.Stringer("issuer_public_key", tokenMetadata.IssuerPublicKey)).
					Sugar().
					Infof("Issuer already has a Spark token with this identifier (txid %s).", ann.txHash)
			} else {
				if err := createNativeSparkTokenEntity(ctx, dbClient, tokenMetadata, l1TokenCreate.ID); err != nil {
					logger.With(zap.Error(err)).Sugar().Errorf("Failed to create spark native token create entity (txid %s)", ann.txHash)
				}
			}
		}
		tokenIdentifiersAnnouncedInBlock[hex.EncodeToString(tokenIdentifier)] = struct{}{}
	}
	return nil
}

func handleTokenUpdatesForBlock(
	ctx context.Context,
	config *so.Config,
	bitcoinClient *rpcclient.Client,
	dbClient *ent.Client,
	txs []wire.MsgTx,
	blockHeight int64,
	blockHash chainhash.Hash,
	network btcnetwork.Network,
) {
	logger := logging.GetLoggerFromContext(ctx)
	logger.Sugar().Infof("Checking for token announcements (block height %d)", blockHeight)
	if err := handleTokenAnnouncements(ctx, config, dbClient, txs, network); err != nil {
		logger.With(zap.Error(err)).Sugar().Errorf("Failed to handle token announcements (block height %d)", blockHeight)
	}
	logger.Sugar().Infof("Checking for token withdrawals (block height %d)", blockHeight)
	if err := tokens.HandleTokenWithdrawals(ctx, config, bitcoinClient, dbClient, txs, network, uint64(blockHeight), blockHash); err != nil {
		logger.With(zap.Error(err)).Sugar().Errorf("Failed to handle token withdrawals (block height %d)", blockHeight)
	}
}

func isDuplicateAnnouncement(ctx context.Context, dbClient *ent.Client, tokenIdentifier []byte, tokenIdentifiersAnnouncedInBlock map[string]struct{}) (bool, error) {
	exists, err := dbClient.L1TokenCreate.Query().
		Where(l1tokencreate.TokenIdentifierEQ(tokenIdentifier)).
		Exist(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to query for existing l1 token create: %w", err)
	}
	if exists {
		return true, nil
	}
	_, ok := tokenIdentifiersAnnouncedInBlock[hex.EncodeToString(tokenIdentifier)]
	return ok, nil
}

func tokenIdentifierAlreadyExists(ctx context.Context, dbClient *ent.Client, tokenIdentifier common.TokenIdentifier, tokenIdentifiersAnnouncedInBlock map[string]struct{}) (bool, error) {
	exists, err := dbClient.TokenCreate.Query().
		Where(tokencreate.TokenIdentifierEQ(tokenIdentifier)).
		Exist(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to query for existing spark token: %w", err)
	}
	if exists {
		return true, nil
	}
	_, duplicateInBlock := tokenIdentifiersAnnouncedInBlock[string(tokenIdentifier)]
	return duplicateInBlock, nil
}
