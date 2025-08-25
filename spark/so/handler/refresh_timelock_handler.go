package handler

import (
	"context"
	"fmt"

	"github.com/lightsparkdev/spark/common/keys"

	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authz"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/helper"
	"github.com/lightsparkdev/spark/so/objects"
)

// RefreshTimelockHandler is a handler for refreshing timelocks.
type RefreshTimelockHandler struct {
	config *so.Config
}

// NewRefreshTimelockHandler creates a new RefreshTimelockHandler.
func NewRefreshTimelockHandler(config *so.Config) *RefreshTimelockHandler {
	return &RefreshTimelockHandler{
		config: config,
	}
}

// RefreshTimelock refreshes the timelocks of a leaf and its ancestors.
func (h *RefreshTimelockHandler) RefreshTimelock(ctx context.Context, req *pb.RefreshTimelockRequest) (*pb.RefreshTimelockResponse, error) {
	return h.refreshTimelock(ctx, req, false)
}

// RefreshTimelockV2 refreshes the timelocks of a leaf and its ancestors.
func (h *RefreshTimelockHandler) RefreshTimelockV2(ctx context.Context, req *pb.RefreshTimelockRequest) (*pb.RefreshTimelockResponse, error) {
	return h.refreshTimelock(ctx, req, true)
}

func (h *RefreshTimelockHandler) refreshTimelock(ctx context.Context, req *pb.RefreshTimelockRequest, requireDirectTx bool) (*pb.RefreshTimelockResponse, error) {
	reqOwnerIDPubKey, err := keys.ParsePublicKey(req.OwnerIdentityPublicKey)
	if err != nil {
		return nil, fmt.Errorf("invalid identity public key: %w", err)
	}
	if err := authz.EnforceSessionIdentityPublicKeyMatches(ctx, h.config, reqOwnerIDPubKey); err != nil {
		return nil, err
	}

	leafUUID, err := uuid.Parse(req.LeafId)
	if err != nil {
		return nil, err
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create current tx for request: %w", err)
	}
	leaf, err := db.TreeNode.Get(ctx, leafUUID)
	if err != nil {
		return nil, err
	}

	// Start at the node and collect txs by going through the signing jobs
	node := leaf
	nodes := make([]*ent.TreeNode, len(req.SigningJobs))
	currentTxs := make([]*wire.MsgTx, len(req.SigningJobs))
	signingTxs := make([]*wire.MsgTx, len(req.SigningJobs))
	for i, signingJob := range req.SigningJobs {
		var rawTxBytes []byte
		if len(req.SigningJobs) == 3 && len(node.DirectRefundTx) > 0 && len(node.DirectFromCpfpRefundTx) > 0 {
			// Only refund signing jobs are present
			switch i {
			case 0:
				rawTxBytes = node.RawRefundTx
			case 1:
				rawTxBytes = node.DirectRefundTx
			default:
				rawTxBytes = node.DirectFromCpfpRefundTx
			}
		} else if len(req.SigningJobs) >= 5 && len(node.DirectTx) > 0 && len(node.DirectRefundTx) > 0 && len(node.DirectFromCpfpRefundTx) > 0 {
			switch i {
			case len(req.SigningJobs) - 1:
				rawTxBytes = node.DirectFromCpfpRefundTx
			case len(req.SigningJobs) - 2:
				rawTxBytes = node.DirectRefundTx
			case len(req.SigningJobs) - 3:
				rawTxBytes = node.RawRefundTx
			case len(req.SigningJobs) - 4:
				rawTxBytes = node.DirectTx
			case len(req.SigningJobs) - 5:
				rawTxBytes = node.RawTx
			default:
				node, err = node.QueryParent().First(ctx)
				if err != nil {
					return nil, fmt.Errorf("unable to query parent node: %w", err)
				}
				if i%2 == 0 {
					rawTxBytes = node.RawTx
				} else {
					rawTxBytes = node.DirectTx
				}
			}
		} else if requireDirectTx && len(node.DirectTx) > 0 {
			if len(req.SigningJobs) != 3 && len(req.SigningJobs) != 5 {
				return nil, fmt.Errorf("received %d signing jobs, expected either 3 or 5 to include direct TX signing jobs", len(req.SigningJobs))
			} else {
				return nil, fmt.Errorf("leaf %s does not have a direct TX present", node.ID)
			}
		} else if len(req.SigningJobs) == 1 {
			rawTxBytes = node.RawRefundTx
		} else {
			if i == len(req.SigningJobs)-1 {
				rawTxBytes = node.RawRefundTx
			} else if i == len(req.SigningJobs)-2 {
				rawTxBytes = node.RawTx
			} else {
				node, err = node.QueryParent().First(ctx)
				if err != nil {
					return nil, fmt.Errorf("unable to query parent node: %w", err)
				}
				rawTxBytes = node.RawTx
			}
		}

		if i == len(req.SigningJobs)-1 && node.Status != st.TreeNodeStatusAvailable && node.Status != st.TreeNodeStatusOnChain {
			return nil, fmt.Errorf("cannot refresh leaf node %s because it is not available or on-chain", node.ID)
		}

		currentTx, err := common.TxFromRawTxBytes(rawTxBytes)
		if err != nil {
			return nil, fmt.Errorf("unable to deserialize current tx at index %d (RawRefundTx len: %d, RawTx len: %d): %w", i, len(node.RawRefundTx), len(node.RawTx), err)
		}

		signingTx, err := common.TxFromRawTxBytes(signingJob.RawTx)
		if err != nil {
			return nil, fmt.Errorf("unable to deserialize signing job signing tx: %w", err)
		}

		nodes[i] = node
		signingTxs[i] = signingTx
		currentTxs[i] = currentTx
	}

	// Validate the signing requests
	for i := range signingTxs {
		signingTx := signingTxs[i]
		currentTx := currentTxs[i]

		// Output should just be destination + ephemeral anchor
		if len(signingTx.TxOut) > 2 {
			return nil, fmt.Errorf("unexpected number of outputs on signing tx: %d", len(signingTx.TxOut))
		}
		if len(currentTx.TxOut) > 2 {
			return nil, fmt.Errorf("unexpected number of outputs on current tx: %d", len(currentTx.TxOut))
		}
		// TODO(mo) Reinstate value check once CPFP Refund Transactions are re-introduced into timelock flow
		if len(node.DirectTx) > 0 && i == len(signingTxs)-3 && signingTx.TxOut[0].Value != currentTx.TxOut[0].Value {
			return nil, fmt.Errorf("expected output value to be %d, got %d", currentTx.TxOut[0].Value, signingTx.TxOut[0].Value)
		} else if node.DirectTx == nil && i == len(signingTxs)-1 && signingTx.TxOut[0].Value != currentTx.TxOut[0].Value {
			return nil, fmt.Errorf("expected output value to be %d, got %d", currentTx.TxOut[0].Value, signingTx.TxOut[0].Value)
		}

		signingSequence := signingTx.TxIn[0].Sequence
		currentSequence := currentTx.TxIn[0].Sequence

		if (len(node.DirectTx) > 0 && len(signingTxs) == 3 && signingSequence >= currentSequence) || (len(node.DirectTx) == 0 && len(signingTxs) == 1 && signingSequence >= currentSequence) {
			// If we are only refreshing refund txs, we should be decrementing all the timelocks
			return nil, fmt.Errorf("sequence %d should be less than %d", signingSequence, currentSequence)
		} else if (len(node.DirectTx) > 0 && len(signingTxs) > 3 && i >= len(signingTxs)-3 && signingSequence < spark.InitialSequence()) || (len(node.DirectTx) == 0 && len(signingTxs) > 1 && i >= len(signingTxs)-1 && signingSequence != spark.InitialSequence()) {
			// Else, refund tx timelocks should be reset
			return nil, fmt.Errorf("sequence %d should be %d", signingSequence, spark.InitialSequence())
		}
	}

	// Prepare frost signing jobs
	signingJobs := make([]*helper.SigningJob, 0, len(req.SigningJobs))
	for i, signingJob := range req.SigningJobs {
		var parentTx *wire.MsgTx
		if len(node.DirectTx) > 0 && len(node.DirectRefundTx) > 0 && len(node.DirectFromCpfpRefundTx) > 0 && (len(req.SigningJobs) == 3 || len(req.SigningJobs) >= 5) {
			if len(nodes) == 3 {
				// Only signing refund txs
				if i == 0 || i == 2 {
					// CPFP refund txs should spend from CPFP node tx
					parentTx, err = common.TxFromRawTxBytes(nodes[0].RawTx)
					if err != nil {
						return nil, fmt.Errorf("unable to deserialize refund signing tx: %w", err)
					}
				} else {
					// Direct refund tx should spend from direct node tx
					parentTx, err = common.TxFromRawTxBytes(nodes[0].DirectTx)
					if err != nil {
						return nil, fmt.Errorf("unable to deserialize direct refund signing tx: %w", err)
					}
				}
			} else if i < 2 {
				// Greatest ancestor tx
				parentNode, err := nodes[0].QueryParent().First(ctx)
				if err != nil {
					return nil, fmt.Errorf("unable to query parent node: %w", err)
				}
				parentTx, err = common.TxFromRawTxBytes(parentNode.RawTx)
				if err != nil {
					return nil, fmt.Errorf("unable to deserialize parent signing tx: %w", err)
				}
			} else if i < len(signingTxs)-1 {
				parentTx = signingTxs[i-2]
			} else {
				parentTx = signingTxs[i-4]
			}
		} else {
			if i == 0 && len(nodes) == 1 {
				// Only signing refund tx
				parentTx, err = common.TxFromRawTxBytes(nodes[0].RawTx)
				if err != nil {
					return nil, fmt.Errorf("unable to deserialize refund signing tx: %w", err)
				}
			} else if i == 0 {
				// Greatest ancestor tx
				parentNode, err := nodes[0].QueryParent().First(ctx)
				if err != nil {
					return nil, fmt.Errorf("unable to query parent node: %w", err)
				}
				parentTx, err = common.TxFromRawTxBytes(parentNode.RawTx)
				if err != nil {
					return nil, fmt.Errorf("unable to deserialize parent signing tx: %w", err)
				}
			} else {
				parentTx = signingTxs[i-1]
			}
		}
		parentTxOut := parentTx.TxOut[nodes[i].Vout]

		// Validate the current tx spends the parent tx
		parentTxHash := parentTx.TxHash()
		if !signingTxs[i].TxIn[0].PreviousOutPoint.Hash.IsEqual(&parentTxHash) || signingTxs[i].TxIn[0].PreviousOutPoint.Index != uint32(nodes[i].Vout) {
			return nil, fmt.Errorf("signing tx must spend parent tx vout, expected %s:%d, got %s:%d", parentTxHash, nodes[i].Vout, signingTxs[i].TxIn[0].PreviousOutPoint.Hash, signingTxs[i].TxIn[0].PreviousOutPoint.Index)
		}

		sigHash, err := common.SigHashFromTx(signingTxs[i], 0, parentTxOut)
		if err != nil {
			return nil, fmt.Errorf("unable to calculate sighash from refund tx: %w", err)
		}
		userNonceCommitment, err := objects.NewSigningCommitment(signingJob.SigningNonceCommitment.Binding, signingJob.SigningNonceCommitment.Hiding)
		if err != nil {
			return nil, fmt.Errorf("unable to create user nonce commitment: %w", err)
		}

		signingKeyshare, err := nodes[i].QuerySigningKeyshare().Only(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get signing keyshare id: %w", err)
		}

		verifyingPubKey, err := keys.ParsePublicKey(nodes[i].VerifyingPubkey)
		if err != nil {
			return nil, fmt.Errorf("unable to parse verifying public key: %w", err)
		}

		signingJobs = append(signingJobs, &helper.SigningJob{
			JobID:             uuid.New().String(),
			SigningKeyshareID: signingKeyshare.ID,
			Message:           sigHash,
			VerifyingKey:      &verifyingPubKey,
			UserCommitment:    userNonceCommitment,
		})
	}

	// Save new raw txs in the DB
	for i, signingJob := range req.SigningJobs {
		var err error
		if (len(req.SigningJobs) == 3 || len(req.SigningJobs) >= 5) && len(node.DirectTx) > 0 && len(node.DirectRefundTx) > 0 && len(node.DirectFromCpfpRefundTx) > 0 {
			if i == len(req.SigningJobs)-1 {
				_, err = nodes[i].Update().SetDirectFromCpfpRefundTx(signingJob.RawTx).Save(ctx)
			} else if i == len(req.SigningJobs)-2 {
				_, err = nodes[i].Update().SetDirectRefundTx(signingJob.RawTx).Save(ctx)
			} else if i == len(req.SigningJobs)-3 {
				_, err = nodes[i].Update().SetRawRefundTx(signingJob.RawTx).Save(ctx)
			} else if i%2 == 0 {
				_, err = nodes[i].Update().SetRawTx(signingJob.RawTx).Save(ctx)
			} else {
				_, err = nodes[i].Update().SetDirectTx(signingJob.RawTx).Save(ctx)
			}
		} else {
			if i == len(req.SigningJobs)-1 {
				_, err = nodes[i].Update().SetRawRefundTx(signingJob.RawTx).Save(ctx)
			} else {
				_, err = nodes[i].Update().SetRawTx(signingJob.RawTx).Save(ctx)
			}
		}
		if err != nil {
			return nil, err
		}
	}

	// Sign the transactions with all the SOs
	signingResults, err := helper.SignFrost(ctx, h.config, signingJobs)
	if err != nil {
		return nil, err
	}

	// Prepare response
	var pbSigningResults []*pb.RefreshTimelockSigningResult
	for i, signingResult := range signingResults {
		signingResultProto, err := signingResult.MarshalProto()
		if err != nil {
			return nil, err
		}
		pbSigningResults = append(pbSigningResults, &pb.RefreshTimelockSigningResult{
			SigningResult: signingResultProto,
			VerifyingKey:  nodes[i].VerifyingPubkey,
		})
	}

	return &pb.RefreshTimelockResponse{
		SigningResults: pbSigningResults,
	}, nil
}
