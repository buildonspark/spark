package wallet

import (
	"context"
	"fmt"
	"log"

	"github.com/lightsparkdev/spark/common/keys"

	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/lightsparkdev/spark/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
)

// QueryTokenTransactionsParams holds the parameters for QueryTokenTransactionsV2
type QueryTokenTransactionsParams struct {
	SparkAddresses    []string
	IssuerPublicKeys  []keys.Public
	OwnerPublicKeys   []keys.Public
	TokenIdentifiers  [][]byte
	OutputIDs         []string
	TransactionHashes [][]byte
	Order             pb.Order
	Offset            int64
	Limit             int64
}

// QueryTokenOutputs retrieves the token outputs for the given owner and token public keys.
func QueryTokenOutputs(
	ctx context.Context,
	config *TestWalletConfig,
	ownerPublicKeys []keys.Public,
	tokenPublicKeys []keys.Public,
) (*tokenpb.QueryTokenOutputsResponse, error) {
	sparkConn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		log.Printf("Error while establishing gRPC connection to coordinator at %s: %v", config.CoordinatorAddress(), err)
		return nil, err
	}
	defer sparkConn.Close()

	token, err := AuthenticateWithConnection(ctx, config, sparkConn)
	if err != nil {
		return nil, fmt.Errorf("failed to authenticate with server: %w", err)
	}
	tmpCtx := ContextWithToken(ctx, token)
	tokenClient := tokenpb.NewSparkTokenServiceClient(sparkConn)

	network, err := common.ProtoNetworkFromNetwork(config.Network)
	if err != nil {
		return nil, fmt.Errorf("failed to convert network to proto network: %w", err)
	}

	request := &tokenpb.QueryTokenOutputsRequest{
		OwnerPublicKeys:  serializeAll(ownerPublicKeys),
		IssuerPublicKeys: serializeAll(tokenPublicKeys), // Field name change: TokenPublicKeys -> IssuerPublicKeys
		Network:          network,                       // Uses pb.Network (same as sparkpb)
	}

	response, err := tokenClient.QueryTokenOutputs(tmpCtx, request)
	if err != nil {
		return nil, fmt.Errorf("failed to get token outputs: %w", err)
	}
	return response, nil
}

// QueryTokenTransactions retrieves token transactions for the given input filters.
func QueryTokenTransactions(
	ctx context.Context,
	config *TestWalletConfig,
	params QueryTokenTransactionsParams,
) (*tokenpb.QueryTokenTransactionsResponse, error) {
	sparkConn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		log.Printf("Error while establishing gRPC connection to coordinator at %s: %v", config.CoordinatorAddress(), err)
		return nil, err
	}
	defer sparkConn.Close()

	token, err := AuthenticateWithConnection(ctx, config, sparkConn)
	if err != nil {
		return nil, fmt.Errorf("failed to authenticate with server: %w", err)
	}
	tmpCtx := ContextWithToken(ctx, token)
	tokenClient := tokenpb.NewSparkTokenServiceClient(sparkConn)

	// Decode spark addresses to get owner public keys
	var decodedOwnerPublicKeys []keys.Public
	for _, address := range params.SparkAddresses {
		decoded, err := common.DecodeSparkAddress(address)
		if err != nil {
			return nil, fmt.Errorf("failed to decode spark address: %w", err)
		}
		pubKey, err := keys.ParsePublicKey(decoded.SparkAddress.IdentityPublicKey)
		if err != nil {
			return nil, fmt.Errorf("failed to parse identity public key from spark address: %w", err)
		}
		decodedOwnerPublicKeys = append(decodedOwnerPublicKeys, pubKey)
	}

	// Combine decoded owner public keys with direct owner public keys
	allOwnerPublicKeys := append(decodedOwnerPublicKeys, params.OwnerPublicKeys...)

	request := &tokenpb.QueryTokenTransactionsRequest{
		OwnerPublicKeys:        serializeAll(allOwnerPublicKeys),
		IssuerPublicKeys:       serializeAll(params.IssuerPublicKeys), // Field name change: TokenPublicKeys -> IssuerPublicKeys
		TokenIdentifiers:       params.TokenIdentifiers,
		OutputIds:              params.OutputIDs,
		TokenTransactionHashes: params.TransactionHashes,
		Order:                  params.Order,
		Limit:                  params.Limit,
		Offset:                 params.Offset,
	}

	response, err := tokenClient.QueryTokenTransactions(tmpCtx, request)
	if err != nil {
		return nil, fmt.Errorf("failed to query token transactions: %w", err)
	}

	return response, nil
}

// SignHashSlice is a helper function to create either Schnorr or ECDSA signature
func SignHashSlice(config *TestWalletConfig, privKey keys.Private, hash []byte) ([]byte, error) {
	if config.UseTokenTransactionSchnorrSignatures {
		sig, err := schnorr.Sign(privKey.ToBTCEC(), hash)
		if err != nil {
			return nil, fmt.Errorf("failed to create Schnorr signature: %w", err)
		}
		return sig.Serialize(), nil
	}

	sig := ecdsa.Sign(privKey.ToBTCEC(), hash)
	return sig.Serialize(), nil
}

func serializeAll(pubKeys []keys.Public) [][]byte {
	result := make([][]byte, len(pubKeys))
	for i, key := range pubKeys {
		result[i] = key.Serialize()
	}
	return result
}

// QueryTokenMetadata retrieves token metadata for given token identifiers or issuer public keys.
func QueryTokenMetadata(
	ctx context.Context,
	config *TestWalletConfig,
	tokenIdentifiers [][]byte,
	issuerPublicKeys []keys.Public,
) (*tokenpb.QueryTokenMetadataResponse, error) {
	sparkConn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		log.Printf("Error while establishing gRPC connection to coordinator at %s: %v", config.CoordinatorAddress(), err)
		return nil, err
	}
	defer sparkConn.Close()

	token, err := AuthenticateWithConnection(ctx, config, sparkConn)
	if err != nil {
		return nil, fmt.Errorf("failed to authenticate with server: %w", err)
	}
	tmpCtx := ContextWithToken(ctx, token)
	tokenClient := tokenpb.NewSparkTokenServiceClient(sparkConn)

	request := &tokenpb.QueryTokenMetadataRequest{
		TokenIdentifiers: tokenIdentifiers,
		IssuerPublicKeys: serializeAll(issuerPublicKeys),
	}

	response, err := tokenClient.QueryTokenMetadata(tmpCtx, request)
	if err != nil {
		return nil, fmt.Errorf("failed to query token metadata: %w", err)
	}

	return response, nil
}
