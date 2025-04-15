package lrc20

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go/common"
	pblrc20 "github.com/lightsparkdev/spark-go/proto/lrc20"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/ent"
	"github.com/lightsparkdev/spark-go/so/ent/tokenoutput"
	"google.golang.org/grpc"
)

// DefaultPageSize defines the default number of results to fetch per page in paginated requests
const DefaultPageSize = 200

// Client provides methods for interacting with the LRC20 node with built-in retries
type Client struct {
	config *so.Config
}

// NewClient creates a new LRC20 client
func NewClient(config *so.Config) *Client {
	return &Client{
		config: config,
	}
}

// executeLrc20Call handles common LRC20 RPC call pattern with proper connection management
func (c *Client) executeLrc20Call(operation func(client pblrc20.SparkServiceClient) error) error {
	network := common.Regtest.String()
	if c.shouldSkipLrc20Call(network) {
		return nil
	}

	conn, err := c.connectToLrc20Node()
	if err != nil {
		return err
	}
	defer conn.Close()

	client := pblrc20.NewSparkServiceClient(conn)
	return operation(client)
}

// SendSparkSignature sends a token transaction signature to the LRC20 node
func (c *Client) SendSparkSignature(
	ctx context.Context,
	signatureData *pblrc20.SparkSignatureData,
) error {
	return c.executeLrc20Call(func(client pblrc20.SparkServiceClient) error {
		_, err := client.SendSparkSignature(ctx, &pblrc20.SendSparkSignatureRequest{
			SignatureData: signatureData,
		})
		return err
	})
}

// FreezeTokens freezes or unfreezes tokens on the LRC20 node
func (c *Client) FreezeTokens(
	ctx context.Context,
	req *pb.FreezeTokensRequest,
) error {
	return c.executeLrc20Call(func(client pblrc20.SparkServiceClient) error {
		_, err := client.FreezeTokens(ctx, req)
		return err
	})
}

// VerifySparkTx verifies a token transaction with the LRC20 node
func (c *Client) VerifySparkTx(
	ctx context.Context,
	tokenTransaction *pb.TokenTransaction,
) error {
	return c.executeLrc20Call(func(client pblrc20.SparkServiceClient) error {
		_, err := client.VerifySparkTx(ctx, &pblrc20.VerifySparkTxRequest{
			FinalTokenTransaction: tokenTransaction,
		})
		if err != nil {
			return err
		}
		// If the error response is null the transaction is valid.
		return nil
	})
}

// shouldSkipLrc20Call checks if LRC20 RPCs are disabled for the given network
func (c *Client) shouldSkipLrc20Call(network string) bool {
	if lrc20Config, ok := c.config.Lrc20Configs[network]; ok && lrc20Config.DisableRpcs {
		slog.Info("Skipping LRC20 node call due to DisableRpcs flag")
		return true
	}
	return false
}

// connectToLrc20Node creates a connection to the LRC20 node with retry policy
func (c *Client) connectToLrc20Node() (*grpc.ClientConn, error) {
	if !slices.Contains(c.config.SupportedNetworks, common.Regtest) {
		return nil, fmt.Errorf("regtest network not supported by this operator")
	}

	lrc20Config := c.config.Lrc20Configs[common.Regtest.String()]
	var conn *grpc.ClientConn
	var err error

	// Increase retries from 3 to 5 and retry on NOT_FOUND status code
	// NOT_FOUND retries are expected in response to withdraw updates
	// (the SO asks LRC20 node for block info, but its a race condition
	// and the LRC20 may not have the block info when the SO makes the first call).
	retryConfig := common.RetryPolicyConfig{
		MaxAttempts:          5,
		InitialBackoff:       1 * time.Second,
		MaxBackoff:           20 * time.Second,
		BackoffMultiplier:    2.0,
		RetryableStatusCodes: []string{"UNAVAILABLE", "NOT_FOUND"},
	}

	if lrc20Config.RelativeCertPath != "" {
		certPath := fmt.Sprintf("%s/%s", c.config.RunDirectory, lrc20Config.RelativeCertPath)
		conn, err = common.NewGRPCConnectionWithCert(lrc20Config.Host, certPath, &retryConfig)
	} else {
		conn, err = common.NewGRPCConnectionWithoutTLS(lrc20Config.Host, &retryConfig)
	}
	if err != nil {
		slog.Error("Failed to connect to the lrc20 node to verify a token transaction", "error", err)
		return nil, err
	}
	return conn, nil
}

// MarkWithdrawnTokenOutputs gets a list of withdrawn token outputs from the LRC20 node.  This
// marks these outputs as 'Withdrawn' and accordingly does not return them as 'owned outputs'
// when requested by wallets / external parties (which also allows for updating balance).
func (c *Client) MarkWithdrawnTokenOutputs(
	ctx context.Context,
	_ common.Network,
	dbTx *ent.Tx,
	blockHash *chainhash.Hash,
) error {
	network := common.Regtest.String()
	if lrc20Config, ok := c.config.Lrc20Configs[network]; ok && lrc20Config.DisableRpcs {
		slog.Info("Skipping LRC20 node call due to DisableRpcs flag")
		return nil
	}
	if lrc20Config, ok := c.config.Lrc20Configs[network]; ok && lrc20Config.DisableL1 {
		slog.Info("Skipping LRC20 node call due to DisableL1 flag")
		return nil
	}

	allOutputs := []*pb.TokenOutput{}

	var pageResponse *pblrc20.ListWithdrawnOutputsResponse
	err := c.executeLrc20Call(func(client pblrc20.SparkServiceClient) error {
		pageSize := uint32(DefaultPageSize)
		var err error
		pageResponse, err = client.ListWithdrawnOutputs(ctx, &pblrc20.ListWithdrawnOutputsRequest{
			// TODO(DL-99): Fetch just for the latest blockhash instead of all withdrawn outputs.
			// TODO(DL-98): Add support for pagination.
			PageSize: &pageSize,
		})
		return err
	})
	if err != nil {
		return fmt.Errorf("error fetching withdrawn outputs: %w", err)
	}

	// Add the current page of results to our collection
	allOutputs = append(allOutputs, pageResponse.Outputs...)

	slog.Info("Completed fetching all withdrawn outputs", "total", len(allOutputs))

	// Mark each output as withdrawn in the database
	if len(allOutputs) > 0 {
		client := dbTx.TokenOutput
		var outputIDs []uuid.UUID

		// First collect all valid output IDs
		for _, output := range allOutputs {
			outputUUID, err := uuid.Parse(*output.Id)
			if err != nil {
				slog.Warn("Failed to parse output ID as UUID",
					"output_id", output.Id,
					"error", err)
				continue
			}
			outputIDs = append(outputIDs, outputUUID)
		}

		if len(outputIDs) > 0 {
			_, err := client.Update().
				Where(tokenoutput.IDIn(outputIDs...)).
				SetConfirmedWithdrawBlockHash(blockHash.CloneBytes()).
				Save(ctx)
			if err != nil {
				return fmt.Errorf("failed to bulk update token outputs: %w", err)
			}

			slog.Debug("Successfully marked token outputs as withdrawn",
				"count", len(outputIDs))
		}
	}

	return nil
}

// UnmarkWithdrawnTokenOutputs clears the withdrawn status for token outputs that were previously
// marked as withdrawn in a specific block. This is used during blockchain reorganizations to
// restore token outputs that were withdrawn in blocks that are no longer part of the main chain.
func (c *Client) UnmarkWithdrawnTokenOutputs(
	ctx context.Context,
	dbTx *ent.Tx,
	blockHash *chainhash.Hash,
) error {
	// Get all token outputs that were marked as withdrawn in this block
	tokenOutputs, err := dbTx.TokenOutput.Query().
		Where(tokenoutput.ConfirmedWithdrawBlockHashEQ(blockHash.CloneBytes())).
		All(ctx)
	if err != nil {
		return fmt.Errorf("error querying withdrawn outputs for block %s: %w", blockHash.String(), err)
	}

	count := len(tokenOutputs)
	if count == 0 {
		slog.Info("No withdrawn token outputs found for block", "block_hash", blockHash.String())
		return nil
	}

	slog.Info("Unmarking withdrawn token outputs due to reorg",
		"block_hash", blockHash.String(),
		"count", count)

	// Clear the confirmed_withdraw_block_hash field for all affected outputs
	outputIDs := make([]uuid.UUID, len(tokenOutputs))
	for i, output := range tokenOutputs {
		outputIDs[i] = output.ID
	}
	_, err = dbTx.TokenOutput.Update().
		Where(tokenoutput.IDIn(outputIDs...)).
		ClearConfirmedWithdrawBlockHash().
		Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to clear withdraw block hash for outputs: %w", err)
	}

	slog.Info("Successfully unmarked token outputs",
		"block_hash", blockHash.String(),
		"count", count)

	return nil
}
