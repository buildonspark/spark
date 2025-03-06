package helper

import (
	"fmt"
	"log"
	"slices"

	"github.com/lightsparkdev/spark-go/common"
	"github.com/lightsparkdev/spark-go/so"
	"google.golang.org/grpc"
)

// GenerateProofOfPossessionSignatures generates the proof of possession signatures for the given messages and keyshares.
func ConnectToLrc20Node(config *so.Config) (*grpc.ClientConn, error) {
	// TODO: Add network parameter to token transaction so the wallet can specify which network to broadcast the TX on.
	// Verify regtest is in supported networks
	if !slices.Contains(config.SupportedNetworks, common.Regtest) {
		return nil, fmt.Errorf("regtest network not supported by this operator")
	}

	lrc20Config := config.Lrc20Configs[common.Regtest.String()]
	var conn *grpc.ClientConn
	var err error

	if lrc20Config.RelativeCertPath != "" {
		certPath := fmt.Sprintf("%s/%s", config.RunDirectory, lrc20Config.RelativeCertPath)
		conn, err = common.NewGRPCConnectionWithCert(lrc20Config.Host, certPath)
	} else {
		conn, err = common.NewGRPCConnectionWithoutTLS(lrc20Config.Host)
	}
	if err != nil {
		log.Printf("Failed to connect to the lrc20 node to verify a token transaction: %v", err)
		return nil, err
	}
	return conn, nil
}
