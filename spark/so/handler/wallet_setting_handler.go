package handler

import (
	"context"
	"fmt"

	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"

	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/logging"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authn"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/walletsetting"
	"github.com/lightsparkdev/spark/so/helper"
	"github.com/lightsparkdev/spark/so/knobs"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type WalletSettingHandler struct {
	config *so.Config
}

func NewWalletSettingHandler(config *so.Config) *WalletSettingHandler {
	return &WalletSettingHandler{
		config: config,
	}
}

func (h *WalletSettingHandler) UpdateWalletSetting(ctx context.Context, request *pb.UpdateWalletSettingRequest) (*pb.UpdateWalletSettingResponse, error) {
	logger := logging.GetLoggerFromContext(ctx)

	// Get session and identity public key
	session, err := authn.GetSessionFromContext(ctx)
	if err != nil {
		return nil, err
	}
	identityPubKey := session.IdentityPublicKey()

	// Validate that at least one field is provided
	if request.PrivateEnabled == nil {
		return nil, status.Error(codes.InvalidArgument, "at least one field must be provided for update")
	}

	walletSetting, err := h.UpdateWalletSettingInternal(ctx, identityPubKey, request.GetPrivateEnabled())
	if err != nil {
		logger.Error("failed to update wallet setting", zap.Error(err))
		return nil, fmt.Errorf("failed to update wallet setting: %w", err)
	}

	// Send gossip message to notify other operators
	err = h.sendWalletSettingUpdateGossipMessage(ctx, identityPubKey, request.GetPrivateEnabled())
	if err != nil {
		logger.Error("failed to send wallet setting update gossip message", zap.Error(err))
		return nil, fmt.Errorf("failed to send wallet setting update gossip message: %w", err)
	}

	// Convert to proto response
	response := &pb.UpdateWalletSettingResponse{
		WalletSetting: h.marshalWalletSettingToProto(walletSetting),
	}

	return response, nil
}

func (h *WalletSettingHandler) UpdateWalletSettingInternal(ctx context.Context, ownerIdentityPublicKey keys.Public, privateEnabled bool) (*ent.WalletSetting, error) {
	logger := logging.GetLoggerFromContext(ctx)

	// Get current wallet setting from database
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get database from context: %w", err)
	}

	walletSetting, err := db.WalletSetting.
		Query().
		Where(walletsetting.OwnerIdentityPublicKey(ownerIdentityPublicKey)).
		ForUpdate().
		Only(ctx)

	if err == nil {
		// Update existing wallet setting
		walletSetting, err = walletSetting.Update().
			SetPrivateEnabled(privateEnabled).
			Save(ctx)
		if err != nil {
			logger.Error("failed to update wallet setting", zap.Error(err))
			return nil, fmt.Errorf("failed to update wallet setting: %w", err)
		}
	} else if ent.IsNotFound(err) {
		// Create new wallet setting
		walletSetting, err = db.WalletSetting.Create().
			SetOwnerIdentityPublicKey(ownerIdentityPublicKey).
			SetPrivateEnabled(privateEnabled).
			Save(ctx)
		if err != nil {
			logger.Error("failed to create wallet setting", zap.Error(err))
			return nil, fmt.Errorf("failed to create wallet setting: %w", err)
		}
	} else {
		logger.Error("failed to query wallet setting", zap.Error(err))
		return nil, fmt.Errorf("failed to query wallet setting: %w", err)
	}

	return walletSetting, nil
}

func (h *WalletSettingHandler) sendWalletSettingUpdateGossipMessage(ctx context.Context, ownerIdentityPublicKey keys.Public, privateEnabled bool) error {
	// Get operator selection to exclude self
	selection := helper.OperatorSelection{
		Option: helper.OperatorSelectionOptionExcludeSelf,
	}
	participants, err := selection.OperatorIdentifierList(h.config)
	if err != nil {
		return fmt.Errorf("unable to get operator list: %w", err)
	}

	// Create and send gossip message
	sendGossipHandler := NewSendGossipHandler(h.config)
	_, err = sendGossipHandler.CreateAndSendGossipMessage(ctx, &pbgossip.GossipMessage{
		Message: &pbgossip.GossipMessage_UpdateWalletSetting{
			UpdateWalletSetting: &pbgossip.GossipMessageUpdateWalletSetting{
				OwnerIdentityPublicKey: ownerIdentityPublicKey.Serialize(),
				PrivateEnabled:         proto.Bool(privateEnabled),
			},
		},
	}, participants)
	if err != nil {
		return fmt.Errorf("unable to create and send gossip message: %w", err)
	}

	return nil
}

func (h *WalletSettingHandler) HasReadAccessToWallet(ctx context.Context, walletIdentityPublicKey keys.Public) (bool, error) {
	knobService := knobs.GetKnobsService(ctx)
	if knobService != nil {
		if !knobService.RolloutRandom(knobs.KnobPrivacyEnabled, 0) {
			return true, nil
		}
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to get database from context: %w", err)
	}

	walletSetting, err := db.WalletSetting.
		Query().
		Where(walletsetting.OwnerIdentityPublicKey(walletIdentityPublicKey)).
		Only(ctx)

	if err != nil {
		if ent.IsNotFound(err) {
			// No wallet setting exists, return default value (true)
			return true, nil
		}
		return false, fmt.Errorf("failed to query wallet setting: %w", err)
	}

	if walletSetting.PrivateEnabled == false {
		return true, nil
	}

	session, err := authn.GetSessionFromContext(ctx)
	if err != nil {
		// If there's no session, return false (no access) without error
		// This allows callers to handle "no access" gracefully vs actual errors
		return false, nil
	}

	return session.IdentityPublicKey().Equals(walletSetting.OwnerIdentityPublicKey) || (walletSetting.MasterIdentityPublicKey != nil && session.IdentityPublicKey().Equals(*walletSetting.MasterIdentityPublicKey)), nil
}

// marshalWalletSettingToProto converts a WalletSetting to a spark protobuf WalletSetting.
func (h *WalletSettingHandler) marshalWalletSettingToProto(ws *ent.WalletSetting) *pb.WalletSetting {
	return &pb.WalletSetting{
		OwnerIdentityPublicKey: ws.OwnerIdentityPublicKey.Serialize(),
		PrivateEnabled:         ws.PrivateEnabled,
	}
}

func (h *WalletSettingHandler) QueryWalletSetting(ctx context.Context, _ *pb.QueryWalletSettingRequest) (*pb.QueryWalletSettingResponse, error) {
	logger := logging.GetLoggerFromContext(ctx)

	// Get session and identity public key
	session, err := authn.GetSessionFromContext(ctx)
	if err != nil {
		return nil, err
	}
	identityPubKey := session.IdentityPublicKey()

	// Get current wallet setting from database
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get database from context: %w", err)
	}

	walletSetting, err := db.WalletSetting.
		Query().
		Where(walletsetting.OwnerIdentityPublicKey(identityPubKey)).
		Only(ctx)

	if err == nil {
		// Wallet setting exists, return it
		response := &pb.QueryWalletSettingResponse{
			WalletSetting: h.marshalWalletSettingToProto(walletSetting),
		}
		return response, nil
	} else if ent.IsNotFound(err) {
		// Wallet setting doesn't exist, create a default one
		defaultSetting, err := db.WalletSetting.
			Create().
			SetOwnerIdentityPublicKey(identityPubKey).
			Save(ctx)
		if err != nil {
			logger.Error("failed to create default wallet setting", zap.Error(err))
			return nil, status.Error(codes.Internal, "failed to create default wallet setting")
		}

		response := &pb.QueryWalletSettingResponse{
			WalletSetting: h.marshalWalletSettingToProto(defaultSetting),
		}
		return response, nil
	} else {
		// Other database error
		logger.Error("failed to query wallet setting", zap.Error(err))
		return nil, status.Error(codes.Internal, "failed to query wallet setting")
	}
}
