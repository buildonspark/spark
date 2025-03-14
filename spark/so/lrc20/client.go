package lrc20

import (
	"context"
	"fmt"
	"log"
	"slices"

	"github.com/lightsparkdev/spark-go/common"
	pblrc20 "github.com/lightsparkdev/spark-go/proto/lrc20"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	"github.com/lightsparkdev/spark-go/so"
	"google.golang.org/grpc"
)

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

// exceuteLrc20Call handles common LRC20 RPC call pattern with proper connection management
func (c *Client) exceuteLrc20Call(operation func(client pblrc20.SparkServiceClient) error) error {
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
	return c.exceuteLrc20Call(func(client pblrc20.SparkServiceClient) error {
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
	return c.exceuteLrc20Call(func(client pblrc20.SparkServiceClient) error {
		_, err := client.FreezeTokens(ctx, req)
		return err
	})
}

// VerifySparkTx verifies a token transaction with the LRC20 node
func (c *Client) VerifySparkTx(
	ctx context.Context,
	tokenTransaction *pb.TokenTransaction,
) error {
	return c.exceuteLrc20Call(func(client pblrc20.SparkServiceClient) error {
		res, err := client.VerifySparkTx(ctx, &pblrc20.VerifySparkTxRequest{
			FinalTokenTransaction: tokenTransaction,
		})
		if err != nil {
			return err
		}

		// TODO(DL-92): Remove is_valid boolean in response and use error codes only instead.
		if !res.IsValid {
			return fmt.Errorf("LRC20 node validation: invalid token transaction")
		}
		return nil
	})
}

// shouldSkipLrc20Call checks if LRC20 RPCs are disabled for the given network
func (c *Client) shouldSkipLrc20Call(network string) bool {
	if lrc20Config, ok := c.config.Lrc20Configs[network]; ok && lrc20Config.DisableRpcs {
		log.Printf("Skipping LRC20 node call due to DisableRpcs flag")
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
		InitialBackoffSecs:   1,
		MaxBackoffSecs:       20,
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
		log.Printf("Failed to connect to the lrc20 node to verify a token transaction: %v", err)
		return nil, err
	}
	return conn, nil
}
