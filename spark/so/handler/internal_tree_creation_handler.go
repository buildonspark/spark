package handler

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/lightsparkdev/spark/common/keys"

	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
)

// InternalTreeCreationHandler is a handler for internal tree creation operations.
type InternalTreeCreationHandler struct {
	config *so.Config
}

// NewInternalTreeCreationHandler creates a new InternalTreeCreationHandler.
func NewInternalTreeCreationHandler(config *so.Config) *InternalTreeCreationHandler {
	return &InternalTreeCreationHandler{config: config}
}

func (h *InternalTreeCreationHandler) markExistingSigningKeysharesAsUsed(ctx context.Context, req *pbinternal.PrepareTreeAddressRequest) (map[string]*ent.SigningKeyshare, error) {
	keyshareIDs := make([]uuid.UUID, 0)

	parentKeyshardID, err := uuid.Parse(req.TargetKeyshareId)
	if err != nil {
		return nil, err
	}

	keyshareIDs = append(keyshareIDs, parentKeyshardID)

	nodeQueue := make([]*pbinternal.PrepareTreeAddressNode, 0)
	nodeQueue = append(nodeQueue, req.Node)

	for len(nodeQueue) > 0 {
		node := nodeQueue[0]
		nodeQueue = nodeQueue[1:]

		if len(node.Children) == 0 {
			continue
		}

		for _, childNode := range node.Children[:len(node.Children)-1] {
			keyshareID, err := uuid.Parse(childNode.SigningKeyshareId)
			if err != nil {
				return nil, err
			}
			keyshareIDs = append(keyshareIDs, keyshareID)
			nodeQueue = append(nodeQueue, childNode)
		}

		nodeQueue = append(nodeQueue, node.Children[len(node.Children)-1])
	}

	_, err = ent.MarkSigningKeysharesAsUsed(ctx, h.config, keyshareIDs)
	if err != nil {
		return nil, err
	}

	keysharesMap, err := ent.GetSigningKeysharesMap(ctx, keyshareIDs)
	if err != nil {
		return nil, err
	}

	result := make(map[string]*ent.SigningKeyshare, len(keysharesMap))
	for _, keyshare := range keysharesMap {
		result[keyshare.ID.String()] = keyshare
	}

	return result, nil
}

func (h *InternalTreeCreationHandler) generateAndStoreDepositAddress(ctx context.Context, network common.Network, seKeyshare *ent.SigningKeyshare, userPubkey, identityPubkey []byte, save bool) (string, []byte, error) {
	combinedPublicKeyBytes, err := common.AddPublicKeys(seKeyshare.PublicKey, userPubkey)
	if err != nil {
		return "", nil, err
	}
	combinedPublicKey, err := keys.ParsePublicKey(combinedPublicKeyBytes)
	if err != nil {
		return "", nil, err
	}
	address, err := common.P2TRAddressFromPublicKey(combinedPublicKey, network)
	if err != nil {
		return "", nil, err
	}
	if save {
		db, err := ent.GetDbFromContext(ctx)
		if err != nil {
			return "", nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
		}
		schemaNetwork, err := common.SchemaNetworkFromNetwork(network)
		if err != nil {
			return "", nil, err
		}
		_, err = db.DepositAddress.Create().
			SetSigningKeyshareID(seKeyshare.ID).
			SetOwnerIdentityPubkey(identityPubkey).
			SetOwnerSigningPubkey(userPubkey).
			SetAddress(address).
			SetNetwork(schemaNetwork).
			Save(ctx)
		if err != nil {
			return "", nil, err
		}
	}

	addressHash := sha256.Sum256([]byte(address))
	signature := ecdsa.Sign(h.config.IdentityPrivateKey.ToBTCEC(), addressHash[:])
	return address, signature.Serialize(), nil
}

func (h *InternalTreeCreationHandler) prepareDepositAddress(ctx context.Context, req *pbinternal.PrepareTreeAddressRequest, existingSigningKeyshares map[string]*ent.SigningKeyshare) (map[string][]byte, error) {
	type element struct {
		targetKeyshare *ent.SigningKeyshare
		nodes          []*pbinternal.PrepareTreeAddressNode
	}

	queue := make([]element, 0)
	queue = append(queue, element{
		targetKeyshare: existingSigningKeyshares[req.TargetKeyshareId],
		nodes:          []*pbinternal.PrepareTreeAddressNode{req.Node},
	})

	depositAddressSignatures := make(map[string][]byte)
	for len(queue) > 0 {
		currentElement := queue[0]
		queue = queue[1:]

		if len(currentElement.nodes) == 0 {
			continue
		}

		selectedSigningKeyshares := make([]*ent.SigningKeyshare, 0)
		for _, node := range currentElement.nodes[:len(currentElement.nodes)-1] {
			selectedSigningKeyshare, ok := existingSigningKeyshares[node.SigningKeyshareId]
			if !ok {
				return nil, fmt.Errorf("signing keyshare %s not found", node.SigningKeyshareId)
			}
			selectedSigningKeyshares = append(selectedSigningKeyshares, selectedSigningKeyshare)

			network, err := common.NetworkFromProtoNetwork(req.Network)
			if err != nil {
				return nil, err
			}
			if !h.config.IsNetworkSupported(network) {
				return nil, fmt.Errorf("network not supported")
			}
			address, signature, err := h.generateAndStoreDepositAddress(ctx, network, selectedSigningKeyshare, node.UserPublicKey, req.UserIdentityPublicKey, true)
			if err != nil {
				return nil, err
			}
			depositAddressSignatures[address] = signature
			queue = append(queue, element{
				targetKeyshare: selectedSigningKeyshare,
				nodes:          node.Children,
			})
		}

		lastNode := currentElement.nodes[len(currentElement.nodes)-1]
		keyshareID, err := uuid.Parse(lastNode.SigningKeyshareId)
		if err != nil {
			return nil, err
		}
		lastKeyShare, err := ent.CalculateAndStoreLastKey(ctx, h.config, currentElement.targetKeyshare, selectedSigningKeyshares, keyshareID)
		if err != nil {
			return nil, err
		}

		network, err := common.NetworkFromProtoNetwork(req.Network)
		if err != nil {
			return nil, err
		}
		if !h.config.IsNetworkSupported(network) {
			return nil, fmt.Errorf("network not supported")
		}
		address, signature, err := h.generateAndStoreDepositAddress(ctx, network, lastKeyShare, lastNode.UserPublicKey, req.UserIdentityPublicKey, len(currentElement.nodes) > 1)
		if err != nil {
			return nil, err
		}
		depositAddressSignatures[address] = signature
		queue = append(queue, element{
			targetKeyshare: lastKeyShare,
			nodes:          lastNode.Children,
		})
	}

	return depositAddressSignatures, nil
}

// PrepareTreeAddress prepares the tree address.
func (h *InternalTreeCreationHandler) PrepareTreeAddress(ctx context.Context, req *pbinternal.PrepareTreeAddressRequest) (*pbinternal.PrepareTreeAddressResponse, error) {
	existingSigningKeyshares, err := h.markExistingSigningKeysharesAsUsed(ctx, req)
	if err != nil {
		return nil, err
	}

	depositAddressSignatures, err := h.prepareDepositAddress(ctx, req, existingSigningKeyshares)
	if err != nil {
		return nil, err
	}

	return &pbinternal.PrepareTreeAddressResponse{
		Signatures: depositAddressSignatures,
	}, nil
}
