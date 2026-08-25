package handler

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	bitcointransaction "github.com/lightsparkdev/spark/common/bitcoin_transaction"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/uuids"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/depositaddress"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/signingkeyshare"
	"github.com/lightsparkdev/spark/so/ent/treenode"
	"github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/lightsparkdev/spark/so/utils"
)

var ancestorChainRootSkipCutoff = time.Date(2026, time.March, 17, 0, 0, 0, 0, time.UTC)

var ancestorChainMaxDepth = 1024

const DefaultMaxQueryNodesByID = 1000

// TreeQueryHandler handles queries related to tree nodes.
type TreeQueryHandler struct {
	config *so.Config
}

// NewTreeQueryHandler creates a new TreeQueryHandler.
func NewTreeQueryHandler(config *so.Config) *TreeQueryHandler {
	return &TreeQueryHandler{config: config}
}

// QueryNodes queries the details of nodes given either the owner identity public key or a list of node ids.
func (h *TreeQueryHandler) QueryNodes(ctx context.Context, req *pb.QueryNodesRequest, isSSP bool) (*pb.QueryNodesResponse, error) {
	if req == nil {
		return nil, errors.InvalidArgumentMissingField(fmt.Errorf("request is required"))
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	query := db.TreeNode.
		Query().
		WithSigningKeyshare().
		WithTree().
		WithParent()
	limit := int(req.GetLimit())
	offset := int(req.GetOffset())

	var network btcnetwork.Network
	if req.GetNetwork() == pb.Network_UNSPECIFIED {
		network = btcnetwork.Mainnet
	} else {
		var err error
		network, err = btcnetwork.FromProtoNetwork(req.GetNetwork())
		if err != nil {
			return nil, errors.InvalidArgumentMalformedField(fmt.Errorf("failed to convert proto network to schema network: %w", err))
		}
	}

	switch req.GetSource().(type) {
	case *pb.QueryNodesRequest_OwnerIdentityPubkey:
		if limit < 0 || offset < 0 {
			return nil, errors.InvalidArgumentOutOfRange(fmt.Errorf("expect non-negative offset and limit"))
		}
		ownerIdentityPubKey, err := keys.ParsePublicKey(req.GetOwnerIdentityPubkey())
		if err != nil {
			return nil, errors.InvalidArgumentMalformedKey(fmt.Errorf("failed to parse owner identity public key: %w", err))
		}
		if !isSSP {
			hasReadAccess, err := NewWalletSettingHandler(h.config).HasReadAccessToWallet(ctx, ownerIdentityPubKey)
			if err != nil {
				return nil, fmt.Errorf("failed to check if privacy is enabled for owner: %w", err)
			}
			if !hasReadAccess {
				return &pb.QueryNodesResponse{
					Nodes:  make(map[string]*pb.TreeNode),
					Offset: -1,
				}, nil
			}
		}

		if len(req.GetStatuses()) == 0 {
			query = query.Where(treenode.StatusNotIn(st.TreeNodeStatusCreating, st.TreeNodeStatusSplitted, st.TreeNodeStatusCreationAbandoned))
		}

		query = query.
			Where(treenode.StatusNotIn(st.TreeNodeStatusInvestigation, st.TreeNodeStatusLost, st.TreeNodeStatusReimbursed)).
			Where(treenode.NetworkEQ(network)).
			Where(treenode.OwnerIdentityPubkey(ownerIdentityPubKey)).
			Order(ent.Desc(treenode.FieldID))

		if limit > 0 {
			limit = min(limit, 100)
			query = query.Offset(offset).Limit(limit)
		} else {
			offset = -1
		}

	case *pb.QueryNodesRequest_NodeIds:
		offset = -1
		rawNodeIDs := req.GetNodeIds().GetNodeIds()
		if len(rawNodeIDs) > DefaultMaxQueryNodesByID {
			return nil, errors.InvalidArgumentOutOfRange(fmt.Errorf("there were %d node ids provided, but the max is %d", len(rawNodeIDs), DefaultMaxQueryNodesByID))
		}
		nodeIDs, err := uuids.ParseSlice(rawNodeIDs)
		if err != nil {
			return nil, errors.InvalidArgumentMalformedField(fmt.Errorf("unable to parse node IDs as UUIDs: %w", err))
		}
		query = query.Where(treenode.IDIn(nodeIDs...))
	default:
		return nil, errors.InvalidArgumentMissingField(fmt.Errorf("either owner identity pubkey or node ids to query must be provided"))
	}

	if len(req.GetStatuses()) > 0 {
		statuses := make([]st.TreeNodeStatus, len(req.GetStatuses()))
		for i, stat := range req.GetStatuses() {
			var err error
			statuses[i], err = ent.TreeNodeStatusSchema(stat)
			if err != nil {
				return nil, fmt.Errorf("invalid transfer status: %w", err)
			}
		}
		query = query.Where(treenode.StatusIn(statuses...))
	}

	// If parent chains are requested, eager-load parent of parent to reduce follow-up queries
	if req.GetIncludeParents() {
		query = query.WithParent(func(q *ent.TreeNodeQuery) { q.WithParent() })
	}

	nodes, err := query.All(ctx)
	if err != nil {
		return nil, err
	}

	// Ancestor (non-leaf) nodes are exempt from the wallet privacy filter and
	// are masked instead: ownership is only meaningful on leaves — transfers
	// re-own the leaf while ancestors keep their original owner — so dropping
	// ancestors would only break clients that fetch the exit chain
	// node-by-node (e.g. the SDK unilateral-exit walk), while returning their
	// recorded owner could reveal a privacy-enabled wallet's identity.
	// Masking is deliberately unconditional: a non-leaf node's recorded owner
	// is a stale artifact of past transfers, not meaningful ownership, so no
	// external caller may rely on it. Masking every non-leaf node keeps the
	// response independent of who asks and requires no per-owner privacy
	// lookups.
	var ancestorIDSet map[uuid.UUID]struct{}
	if !isSSP {
		ancestorIDSet, err = queryNodeIDsWithChildren(ctx, nodes)
		if err != nil {
			return nil, err
		}
	}

	if _, ok := req.GetSource().(*pb.QueryNodesRequest_NodeIds); ok && !isSSP {
		nodes, err = filterNodesByWalletAccess(ctx, h.config, nodes, ancestorIDSet)
		if err != nil {
			return nil, err
		}
	}

	protoNodeMap := make(map[string]*pb.TreeNode)
	for _, node := range nodes {
		protoNode, err := node.MarshalSparkProto(ctx)
		if err != nil {
			return nil, fmt.Errorf("unable to marshal node %s: %w", node.ID, err)
		}
		if _, isAncestor := ancestorIDSet[node.ID]; isAncestor {
			protoNode.OwnerIdentityPublicKey = bitcointransaction.NUMSPoint().Serialize()
		}
		protoNodeMap[node.ID.String()] = protoNode
	}

	if req.GetIncludeParents() {
		// Snapshotted before the merge below so the metric can tell an ancestor the walk
		// discovered apart from one the caller had already asked for by ID.
		requestedNodeIDs := make(map[string]struct{}, len(nodes))
		for _, node := range nodes {
			requestedNodeIDs[node.ID.String()] = struct{}{}
		}

		useBatched := knobs.GetKnobsService(ctx).RolloutRandom(knobs.KnobUseBatchedAncestorChain, 0)

		start := time.Now()
		ancestors, path, err := resolveAncestorChains(ctx, db, nodes, isSSP, useBatched)
		elapsed := time.Since(start)
		maps.Copy(protoNodeMap, ancestors)
		recordAncestorChainDuration(ctx, path, elapsed, additionalAncestorNodeCount(protoNodeMap, requestedNodeIDs), err)
		if err != nil {
			return nil, err
		}
	}

	response := &pb.QueryNodesResponse{Nodes: protoNodeMap}
	if offset != -1 {
		nextOffset := -1
		if len(nodes) == limit {
			nextOffset = offset + len(nodes)
		}
		response.Offset = int64(nextOffset)
	}
	return response, nil
}

func (h *TreeQueryHandler) QueryBalance(ctx context.Context, req *pb.QueryBalanceRequest) (*pb.QueryBalanceResponse, error) {
	if req == nil {
		return nil, errors.InvalidArgumentMissingField(fmt.Errorf("request is required"))
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	if req.GetNetwork() == pb.Network_UNSPECIFIED {
		return nil, errors.InvalidArgumentMissingField(fmt.Errorf("network must be specified"))
	}
	network, err := btcnetwork.FromProtoNetwork(req.GetNetwork())
	if err != nil {
		return nil, errors.InvalidArgumentMalformedField(fmt.Errorf("failed to convert proto network to schema network: %w", err))
	}

	identityPubKey, err := keys.ParsePublicKey(req.GetIdentityPublicKey())
	if err != nil {
		return nil, errors.InvalidArgumentMalformedKey(fmt.Errorf("failed to parse identity public key: %w", err))
	}

	hasReadAccess, err := NewWalletSettingHandler(h.config).HasReadAccessToWallet(ctx, identityPubKey)
	if err != nil {
		return nil, fmt.Errorf("failed to check if privacy is enabled for owner: %w", err)
	}
	if !hasReadAccess {
		return &pb.QueryBalanceResponse{}, nil
	}

	nodes, err := db.TreeNode.Query().
		Where(treenode.NetworkEQ(network)).
		Where(treenode.StatusEQ(st.TreeNodeStatusAvailable)).
		Where(treenode.OwnerIdentityPubkey(identityPubKey)).
		All(ctx)
	if err != nil {
		return nil, err
	}

	balance := uint64(0)
	nodeBalances := make(map[string]uint64)
	for _, node := range nodes {
		balance += node.Value
		nodeBalances[node.ID.String()] = node.Value
	}

	return &pb.QueryBalanceResponse{
		Balance:      balance,
		NodeBalances: nodeBalances,
	}, nil
}

// resolveAncestorChains returns the ancestors of every requested node, excluding the requested
// nodes themselves, along with the metric label for the implementation it used. Returning the
// label keeps the choice of path and the name of that path in one place, so they can't disagree.
func resolveAncestorChains(ctx context.Context, db *ent.Client, nodes []*ent.TreeNode, isSSP bool, useBatched bool) (map[string]*pb.TreeNode, string, error) {
	if useBatched {
		ancestors, err := getAncestorChainsBatched(ctx, db, nodes, isSSP)
		return ancestors, ancestorChainPathBatched, err
	}
	ancestors := make(map[string]*pb.TreeNode)
	for _, node := range nodes {
		if err := getAncestorChain(ctx, db, node, ancestors, isSSP); err != nil {
			return nil, ancestorChainPathLegacy, err
		}
	}
	return ancestors, ancestorChainPathLegacy, nil
}

// shouldSuppressRootForChild decides whether a tree's root may be exposed to a non-SSP caller,
// given one of the root's direct children. Legacy mainnet nodes predate the rollout that made
// roots safe to return. Shared by both resolution paths so the business rule can't drift while
// their tree-walking mechanisms differ.
func shouldSuppressRootForChild(childCreateTime time.Time, network btcnetwork.Network, isSSP bool) bool {
	return !isSSP && network == btcnetwork.Mainnet && childCreateTime.Before(ancestorChainRootSkipCutoff)
}

// marshalAncestor marshals a non-leaf node for an ancestor chain. Ancestors are by definition
// non-leaf nodes, so their recorded owner is masked for external callers (see QueryNodes).
func marshalAncestor(ctx context.Context, node *ent.TreeNode, isSSP bool) (*pb.TreeNode, error) {
	proto, err := node.MarshalSparkProto(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to marshal node %s: %w", node.ID, err)
	}
	if !isSSP {
		proto.OwnerIdentityPublicKey = bitcointransaction.NUMSPoint().Serialize()
	}
	return proto, nil
}

func getAncestorChain(ctx context.Context, db *ent.Client, node *ent.TreeNode, nodeMap map[string]*pb.TreeNode, isSSP bool) error {
	var err error
	// Prefer eager-loaded edge when available
	parent := node.Edges.Parent
	if parent == nil {
		parent, err = node.QueryParent().Only(ctx)
		if err != nil {
			if !ent.IsNotFound(err) {
				return err
			}
			return nil
		}
	}

	// Legacy mainnet nodes created before the rollout cutoff should not expose the root ancestor to non-SSP callers.
	if !isSSP && node.CreateTime.Before(ancestorChainRootSkipCutoff) {
		// Check if parent's parent exists; prefer eager-loaded value
		if parent.Edges.Parent == nil {
			if _, err := parent.QueryParent().Only(ctx); err != nil {
				if !ent.IsNotFound(err) {
					return err
				}
				nodeTree, err := node.QueryTree().Only(ctx)
				if err != nil {
					return err
				}
				if shouldSuppressRootForChild(node.CreateTime, nodeTree.Network, isSSP) {
					return nil
				}
			}
		}
	}

	// Parent exists, continue search
	protoParent, err := marshalAncestor(ctx, parent, isSSP)
	if err != nil {
		return err
	}
	nodeMap[parent.ID.String()] = protoParent

	return getAncestorChain(ctx, db, parent, nodeMap, isSSP)
}

// getAncestorChainsBatched resolves the same ancestors as getAncestorChain, but in a fixed two
// queries for the whole node set rather than one per ancestor hop. The recursive CTE walks only
// UUID columns, so the wide tx bytea columns are fetched once per distinct node in the hydration
// query that follows, after deduplication.
func getAncestorChainsBatched(ctx context.Context, db *ent.Client, nodes []*ent.TreeNode, isSSP bool) (map[string]*pb.TreeNode, error) {
	if len(nodes) == 0 {
		return map[string]*pb.TreeNode{}, nil
	}

	ids := make([]uuid.UUID, len(nodes))
	for i, node := range nodes {
		ids[i] = node.ID
	}

	ancestorIDs, err := queryAncestorIDs(ctx, db, ids)
	if err != nil {
		return nil, err
	}
	if len(ancestorIDs) == 0 {
		return map[string]*pb.TreeNode{}, nil
	}

	// Requested nodes are hydrated alongside the ancestors because a requested leaf whose parent
	// is already the root is itself an input to root visibility, even though it is never returned.
	hydrated, err := hydrateNodes(ctx, db, append(ids, ancestorIDs...))
	if err != nil {
		return nil, err
	}

	return marshalAncestors(ctx, hydrated, ancestorIDs, visibleRoots(hydrated, isSSP), isSSP)
}

// queryAncestorIDs follows tree_node_parent upward from every id and returns the distinct ancestors
// it reached. A walk stopped by ancestorChainMaxDepth is an error rather than a short list, which
// is why the query reports each ancestor's depth.
func queryAncestorIDs(ctx context.Context, db *ent.Client, ids []uuid.UUID) ([]uuid.UUID, error) {
	//nolint:forbidigo // a recursive CTE isn't expressible through the ent query builder.
	rows, err := db.QueryContext(ctx, `
		WITH RECURSIVE ancestor_ids(id, depth) AS (
			SELECT tree_node_parent, 0 FROM tree_nodes WHERE id = ANY($1::uuid[])
		  UNION ALL
			SELECT t.tree_node_parent, a.depth + 1
			FROM tree_nodes t JOIN ancestor_ids a ON t.id = a.id
			WHERE a.depth <= $2
		)
		SELECT id, max(depth) AS depth FROM ancestor_ids WHERE id IS NOT NULL GROUP BY id
	`, pq.Array(ids), ancestorChainMaxDepth)
	if err != nil {
		return nil, fmt.Errorf("failed to query ancestor ids: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var ancestorIDs []uuid.UUID
	deepest := 0
	for rows.Next() {
		var id uuid.UUID
		var depth int
		if err := rows.Scan(&id, &depth); err != nil {
			return nil, fmt.Errorf("failed to scan ancestor id: %w", err)
		}
		ancestorIDs = append(ancestorIDs, id)
		if depth > deepest {
			deepest = depth
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to read ancestor ids: %w", err)
	}
	// The recursion is allowed one step past the bound so a chain that ends exactly at it still
	// emits its NULL terminator and is recognised as complete; only a chain still climbing here
	// was truncated.
	if deepest > ancestorChainMaxDepth {
		return nil, fmt.Errorf("ancestor chain for %d nodes reached the depth bound of %d: a parent pointer is cyclic, or the tree is deeper than this path supports", len(ids), ancestorChainMaxDepth)
	}
	return ancestorIDs, nil
}

// hydrateNodeBatch caps how many ids one hydration statement binds. Ent renders IDIn as one
// placeholder per id and issues a further statement per eager-loaded edge, so an unbounded
// wallet-wide QueryNodes over deep trees could otherwise cross Postgres' 65535-parameter ceiling
// — a limit the per-hop legacy walk never approaches.
const hydrateNodeBatch = 10000

func hydrateNodes(ctx context.Context, db *ent.Client, ids []uuid.UUID) ([]*ent.TreeNode, error) {
	hydrated := make([]*ent.TreeNode, 0, len(ids))
	for chunk := range slices.Chunk(ids, hydrateNodeBatch) {
		batch, err := db.TreeNode.Query().
			Where(treenode.IDIn(chunk...)).
			WithTree().
			WithSigningKeyshare().
			WithParent().
			All(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to hydrate ancestor nodes: %w", err)
		}
		hydrated = append(hydrated, batch...)
	}
	return hydrated, nil
}

// visibleRoots reports which roots may be returned to this caller. A root is visible as soon as
// any one of its direct children is allowed to see it, matching getAncestorChain, which walks
// every requested node's chain into one shared response map. Entries whose key turns out to be a
// branch rather than a root are simply never looked up.
func visibleRoots(hydrated []*ent.TreeNode, isSSP bool) map[uuid.UUID]bool {
	visible := make(map[uuid.UUID]bool)
	for _, node := range hydrated {
		parent := node.Edges.Parent
		if parent == nil {
			continue
		}
		if !shouldSuppressRootForChild(node.CreateTime, node.Edges.Tree.Network, isSSP) {
			visible[parent.ID] = true
		}
	}
	return visible
}

// marshalAncestors returns the ancestors among hydrated, dropping any root no child made visible.
func marshalAncestors(ctx context.Context, hydrated []*ent.TreeNode, ancestorIDs []uuid.UUID, visibleRoots map[uuid.UUID]bool, isSSP bool) (map[string]*pb.TreeNode, error) {
	isAncestor := make(map[uuid.UUID]bool, len(ancestorIDs))
	for _, id := range ancestorIDs {
		isAncestor[id] = true
	}

	result := make(map[string]*pb.TreeNode, len(ancestorIDs))
	for _, node := range hydrated {
		if !isAncestor[node.ID] {
			continue
		}
		if node.Edges.Parent == nil && !visibleRoots[node.ID] {
			continue
		}

		protoAncestor, err := marshalAncestor(ctx, node, isSSP)
		if err != nil {
			return nil, err
		}
		result[node.ID.String()] = protoAncestor
	}
	return result, nil
}

// filterNodesByWalletAccess drops leaves whose owner's wallet the requester
// has no read access to. Ancestor (non-leaf) nodes are kept regardless, since
// ownership is only meaningful on leaves; the caller masks their owner
// identity pubkey instead.
func filterNodesByWalletAccess(ctx context.Context, config *so.Config, nodes []*ent.TreeNode, ancestorIDs map[uuid.UUID]struct{}) ([]*ent.TreeNode, error) {
	walletSettingHandler := NewWalletSettingHandler(config)
	accessCache := make(map[keys.Public]bool)
	filtered := nodes[:0]
	for _, node := range nodes {
		if _, isAncestor := ancestorIDs[node.ID]; isAncestor {
			filtered = append(filtered, node)
			continue
		}
		ownerKey := node.OwnerIdentityPubkey
		hasAccess, cached := accessCache[ownerKey]
		if !cached {
			var err error
			hasAccess, err = walletSettingHandler.HasReadAccessToWallet(ctx, node.OwnerIdentityPubkey)
			if err != nil {
				return nil, fmt.Errorf("failed to check wallet access for node %s: %w", node.ID, err)
			}
			accessCache[ownerKey] = hasAccess
		}
		if hasAccess {
			filtered = append(filtered, node)
		}
	}
	return filtered, nil
}

func queryNodeIDsWithChildren(ctx context.Context, nodes []*ent.TreeNode) (map[uuid.UUID]struct{}, error) {
	if len(nodes) == 0 {
		return nil, nil
	}
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}
	nodeIDs := make([]uuid.UUID, len(nodes))
	for i, node := range nodes {
		nodeIDs[i] = node.ID
	}
	ancestorIDs, err := db.TreeNode.Query().
		Where(treenode.IDIn(nodeIDs...), treenode.HasChildren()).
		IDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to query nodes with children: %w", err)
	}
	ancestorIDSet := make(map[uuid.UUID]struct{}, len(ancestorIDs))
	for _, id := range ancestorIDs {
		ancestorIDSet[id] = struct{}{}
	}
	return ancestorIDSet, nil
}

func (h *TreeQueryHandler) QueryUnusedDepositAddresses(ctx context.Context, req *pb.QueryUnusedDepositAddressesRequest) (*pb.QueryUnusedDepositAddressesResponse, error) {
	if req == nil {
		return nil, errors.InvalidArgumentMissingField(fmt.Errorf("request is required"))
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, errors.InternalDatabaseTransactionLifecycleError(fmt.Errorf("failed to get or create current tx for request: %w", err))
	}

	idPubKey, err := keys.ParsePublicKey(req.GetIdentityPublicKey())
	if err != nil {
		return nil, errors.InvalidArgumentMalformedKey(fmt.Errorf("unable to parse identity public key: %w", err))
	}

	hasReadAccess, err := NewWalletSettingHandler(h.config).HasReadAccessToWallet(ctx, idPubKey)
	if err != nil {
		return nil, errors.InternalDatabaseReadError(fmt.Errorf("failed to check if privacy is enabled for owner: %w", err))
	}
	if !hasReadAccess {
		return &pb.QueryUnusedDepositAddressesResponse{
			DepositAddresses: nil,
			Offset:           -1,
		}, nil
	}

	if req.GetNetwork() == pb.Network_UNSPECIFIED {
		return nil, errors.InvalidArgumentMissingField(fmt.Errorf("network must be specified"))
	}
	network, err := btcnetwork.FromProtoNetwork(req.GetNetwork())
	if err != nil {
		return nil, errors.InvalidArgumentMalformedField(fmt.Errorf("failed to convert proto network to common network: %w", err))
	}
	query := db.DepositAddress.Query().
		Where(depositaddress.OwnerIdentityPubkey(idPubKey)).
		// Exclude static deposit addresses, because they always can be used,
		// whereas express deposit addresses can be used only once
		Where(depositaddress.IsStatic(false)).
		Order(ent.Desc(depositaddress.FieldID)).
		WithSigningKeyshare()

	// Validate offset and limit
	if req.GetLimit() < 0 || req.GetOffset() < 0 {
		return nil, errors.InvalidArgumentOutOfRange(fmt.Errorf("expect non-negative offset and limit"))
	}

	usePagination := req.GetLimit() > 0 || req.GetOffset() > 0
	limit := 100
	offset := int(req.GetOffset())

	// If limit and offset are provided, update query to include them otherwise don't add limit and offset to maintain backwards compatibility
	if usePagination {
		if req.GetLimit() > 0 && req.GetLimit() < 100 {
			limit = int(req.GetLimit())
		}

		query = query.Offset(offset).Limit(limit)
	}

	depositAddresses, err := query.All(ctx)
	if err != nil {
		return nil, errors.InternalDatabaseReadError(fmt.Errorf("failed to query deposit addresses: %w", err))
	}

	var unusedDepositAddresses []*pb.DepositAddressQueryResult
	for _, depositAddress := range depositAddresses {
		treeNodes, err := db.TreeNode.Query().Where(treenode.HasSigningKeyshareWith(signingkeyshare.ID(depositAddress.Edges.SigningKeyshare.ID))).All(ctx)
		if len(treeNodes) == 0 || ent.IsNotFound(err) {
			verifyingPublicKey := depositAddress.OwnerSigningPubkey.Add(depositAddress.Edges.SigningKeyshare.PublicKey)
			if utils.IsBitcoinAddressForNetwork(depositAddress.Address, network) {
				unusedDepositAddresses = append(unusedDepositAddresses, &pb.DepositAddressQueryResult{
					DepositAddress:       depositAddress.Address,
					UserSigningPublicKey: depositAddress.OwnerSigningPubkey.Serialize(),
					VerifyingPublicKey:   verifyingPublicKey.Serialize(),
					LeafId:               new(depositAddress.NodeID.String()),
				})
			}
		}
	}

	nextOffset := -1
	if usePagination && len(unusedDepositAddresses) == limit {
		nextOffset = offset + limit
	}

	return &pb.QueryUnusedDepositAddressesResponse{
		DepositAddresses: unusedDepositAddresses,
		Offset:           int64(nextOffset),
	}, nil
}

func (h *TreeQueryHandler) QueryStaticDepositAddresses(ctx context.Context, req *pb.QueryStaticDepositAddressesRequest, isSSP bool) (*pb.QueryStaticDepositAddressesResponse, error) {
	if req == nil {
		return nil, errors.InvalidArgumentMissingField(fmt.Errorf("request is required"))
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}

	limit := int(req.GetLimit())
	offset := int(req.GetOffset())
	if limit < 0 || offset < 0 {
		return nil, errors.InvalidArgumentOutOfRange(fmt.Errorf("expect non-negative offset and limit"))
	}
	if limit > 100 || limit == 0 {
		limit = 100
	}

	idPubKey, err := keys.ParsePublicKey(req.GetIdentityPublicKey())
	if err != nil {
		return nil, errors.InvalidArgumentMalformedKey(fmt.Errorf("unable to parse identity public key: %w", err))
	}

	// Non-SSP reads of static deposit addresses are filtered to wallets the caller
	// has read access to, behind a rollout knob; the SSP-internal endpoint
	// (isSSP=true) is exempt.
	knobService := knobs.GetKnobsService(ctx)
	if !isSSP && knobService != nil && knobService.RolloutRandom(knobs.KnobStaticDepositAddressPrivacyEnabled, 0) {
		hasReadAccess, err := NewWalletSettingHandler(h.config).HasReadAccessToWallet(ctx, idPubKey)
		if err != nil {
			return nil, errors.InternalDatabaseReadError(fmt.Errorf("failed to check if privacy is enabled for owner: %w", err))
		}
		if !hasReadAccess {
			return &pb.QueryStaticDepositAddressesResponse{DepositAddresses: nil}, nil
		}
	}

	if req.GetNetwork() == pb.Network_UNSPECIFIED {
		return nil, errors.InvalidArgumentMissingField(fmt.Errorf("network must be specified"))
	}
	network, err := btcnetwork.FromProtoNetwork(req.GetNetwork())
	if err != nil {
		return nil, errors.InvalidArgumentMalformedField(fmt.Errorf("failed to convert proto network to common network: %w", err))
	}
	query := db.DepositAddress.Query().
		Where(depositaddress.OwnerIdentityPubkey(idPubKey)).
		Where(depositaddress.IsStatic(true)).
		Order(ent.Desc(depositaddress.FieldID)).
		WithSigningKeyshare().
		Offset(offset).
		Limit(limit)
	if req.DepositAddress != nil {
		query = query.Where(depositaddress.Address(req.GetDepositAddress()))
	}
	depositAddresses, err := query.All(ctx)
	if err != nil {
		return nil, err
	}

	var staticDepositAddresses []*pb.DepositAddressQueryResult
	for _, depositAddress := range depositAddresses {
		if utils.IsBitcoinAddressForNetwork(depositAddress.Address, network) {
			queryResult, err := h.depositAddressToQueryResult(ctx, depositAddress, req.GetHashVariant())
			if err != nil {
				return nil, err
			}
			// If the query result is nil, it means that the proofs of possession can not be obtained for some SOs.
			if queryResult != nil {
				staticDepositAddresses = append(staticDepositAddresses, queryResult)
			}
		}
	}

	return &pb.QueryStaticDepositAddressesResponse{DepositAddresses: staticDepositAddresses}, nil
}

func (h *TreeQueryHandler) depositAddressToQueryResult(ctx context.Context, depositAddress *ent.DepositAddress, hashVariant pb.HashVariant) (*pb.DepositAddressQueryResult, error) { // Get local keyshare for the deposit address.
	keyshare, err := depositAddress.Edges.SigningKeyshareOrErr()
	if err != nil {
		return nil, fmt.Errorf("failed to get keyshare for static deposit address: %w", err)
	}
	verifyingPublicKey := depositAddress.OwnerSigningPubkey.Add(keyshare.PublicKey)

	// Return the proofs of possession if they are cached.
	// Caching is done in the GenerateStaticDepositAddressResponse handler on the coordinator.
	// If there are no proofs of possession, the user is advised to generate them by calling the GenerateStaticDepositAddressProofs RPC.
	addressSignatures, proofOfPossessionSignature, err := generateStaticDepositAddressProofs(ctx, h.config, keyshare, depositAddress, hashVariant)
	if err != nil {
		return nil, err
	}
	if addressSignatures == nil {
		return nil, nil
	}

	proofOfPossession := &pb.DepositAddressProof{
		AddressSignatures:          addressSignatures,
		ProofOfPossessionSignature: proofOfPossessionSignature,
	}

	return &pb.DepositAddressQueryResult{
		DepositAddress:       depositAddress.Address,
		UserSigningPublicKey: depositAddress.OwnerSigningPubkey.Serialize(),
		VerifyingPublicKey:   verifyingPublicKey.Serialize(),
		LeafId:               new(depositAddress.NodeID.String()),
		ProofOfPossession:    proofOfPossession,
	}, nil
}
