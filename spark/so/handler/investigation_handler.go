package handler

import (
	"context"
	"fmt"

	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/logging"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/treenode"
	"github.com/lightsparkdev/spark/so/helper"
	"google.golang.org/protobuf/types/known/emptypb"
)

type InvestigationHandler struct {
	config *so.Config
}

func NewInvestigationHandler(config *so.Config) *InvestigationHandler {
	return &InvestigationHandler{config: config}
}

func (h *InvestigationHandler) InvestigateLeaves(ctx context.Context) error {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	leaves, err := db.TreeNode.
		Query().
		Where(treenode.StatusEQ(st.TreeNodeStatusInvestigation)).
		Limit(1000).
		WithSigningKeyshare().
		All(ctx)
	if err != nil {
		return err
	}

	leafIDs := make([]string, len(leaves))
	for i, leaf := range leaves {
		leafIDs[i] = leaf.ID.String()
	}

	selection := helper.OperatorSelection{Option: helper.OperatorSelectionOptionExcludeSelf}
	results, err := helper.ExecuteTaskWithAllOperators(ctx, h.config, &selection, func(ctx context.Context, operator *so.SigningOperator) (*pbinternal.QueryLeafSigningPubkeysResponse, error) {
		conn, err := operator.NewOperatorGRPCConnection()
		if err != nil {
			return nil, err
		}
		defer conn.Close()

		client := pbinternal.NewSparkInternalServiceClient(conn)
		return client.QueryLeafSigningPubkeys(ctx, &pbinternal.QueryLeafSigningPubkeysRequest{LeafIds: leafIDs})
	})
	if err != nil {
		return err
	}

	badNodes := make(map[string]bool)
	for _, leaf := range leaves {
		for _, result := range results {
			resultPubKey, err := keys.ParsePublicKey(result.SigningPubkeys[leaf.ID.String()])
			if err != nil {
				return fmt.Errorf("failed to parse public key for leaf %v: %w", leaf.ID, err)
			}
			if !resultPubKey.Equals(leaf.Edges.SigningKeyshare.PublicKey) {
				badNodes[leaf.ID.String()] = true
				logger := logging.GetLoggerFromContext(ctx)
				logger.Sugar().Warnf("Tree Node %s is marked as lost", leaf.ID)
			}
		}
	}

	var badNodesArray []string
	var goodNodesArray []string
	for _, leaf := range leaves {
		if _, ok := badNodes[leaf.ID.String()]; ok {
			badNodesArray = append(badNodesArray, leaf.ID.String())
		} else {
			goodNodesArray = append(goodNodesArray, leaf.ID.String())
		}
	}

	_, err = helper.ExecuteTaskWithAllOperators(ctx, h.config, &selection, func(ctx context.Context, operator *so.SigningOperator) (any, error) {
		conn, err := operator.NewOperatorGRPCConnection()
		if err != nil {
			return nil, err
		}
		defer conn.Close()

		client := pbinternal.NewSparkInternalServiceClient(conn)
		_, err = client.ResolveLeafInvestigation(ctx, &pbinternal.ResolveLeafInvestigationRequest{
			LostLeafIds:      badNodesArray,
			AvailableLeafIds: goodNodesArray,
		})
		if err != nil {
			return nil, err
		}

		return nil, nil
	})
	if err != nil {
		return err
	}

	for _, leaf := range leaves {
		if _, ok := badNodes[leaf.ID.String()]; ok {
			_, err = leaf.Update().SetStatus(st.TreeNodeStatusLost).Save(ctx)
			if err != nil {
				return err
			}
		} else {
			_, err = leaf.Update().SetStatus(st.TreeNodeStatusAvailable).Save(ctx)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func (h *InvestigationHandler) QueryLeafSigningPubkeys(ctx context.Context, req *pbinternal.QueryLeafSigningPubkeysRequest) (*pbinternal.QueryLeafSigningPubkeysResponse, error) {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	leafIDs, err := common.StringUUIDArrayToUUIDArray(req.LeafIds)
	if err != nil {
		return nil, err
	}

	leaves, err := db.TreeNode.Query().Where(treenode.IDIn(leafIDs...)).WithSigningKeyshare().All(ctx)
	if err != nil {
		return nil, err
	}

	signingPubKeys := make(map[string][]byte)
	for _, leaf := range leaves {
		signingPubKeys[leaf.ID.String()] = leaf.Edges.SigningKeyshare.PublicKey.Serialize()
	}

	return &pbinternal.QueryLeafSigningPubkeysResponse{SigningPubkeys: signingPubKeys}, nil
}

func (h *InvestigationHandler) ResolveLeafInvestigation(ctx context.Context, req *pbinternal.ResolveLeafInvestigationRequest) (*emptypb.Empty, error) {
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	lostLeafIDs, err := common.StringUUIDArrayToUUIDArray(req.LostLeafIds)
	if err != nil {
		return nil, err
	}

	availableLeafIDs, err := common.StringUUIDArrayToUUIDArray(req.AvailableLeafIds)
	if err != nil {
		return nil, err
	}

	lostLeaves, err := db.TreeNode.Query().Where(treenode.IDIn(lostLeafIDs...)).ForUpdate().All(ctx)
	if err != nil {
		return nil, err
	}

	availableLeaves, err := db.TreeNode.Query().Where(treenode.IDIn(availableLeafIDs...)).ForUpdate().All(ctx)
	if err != nil {
		return nil, err
	}

	for _, leaf := range lostLeaves {
		_, err = leaf.Update().SetStatus(st.TreeNodeStatusLost).Save(ctx)
		if err != nil {
			return nil, err
		}
	}

	for _, leaf := range availableLeaves {
		_, err = leaf.Update().SetStatus(st.TreeNodeStatusAvailable).Save(ctx)
		if err != nil {
			return nil, err
		}
	}

	return &emptypb.Empty{}, nil
}
