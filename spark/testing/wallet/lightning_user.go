package wallet

import (
	"context"
	"crypto/sha256"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/lightsparkdev/spark/common/keys"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/lightsparkdev/spark/common"
	secretsharing "github.com/lightsparkdev/spark/common/secret_sharing"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
)

// LightningInvoiceCreator is an interface that can be used to create a Lightning invoice.
type LightningInvoiceCreator interface {
	CreateInvoice(ctx context.Context, bitcoinNetwork common.Network, amountSats int64, paymentHash []byte, memo string, expiry time.Duration) (string, error)
}

func CreateLightningInvoiceWithPreimageAndHash(
	ctx context.Context,
	config *TestWalletConfig,
	creator LightningInvoiceCreator,
	amountSats uint64,
	memo string,
	preimage [32]byte,
	paymentHash [32]byte,
) (string, error) {
	if amountSats > btcutil.MaxSatoshi {
		return "", fmt.Errorf("amount sats too high: %d", amountSats)
	}
	invoice, err := creator.CreateInvoice(ctx, config.Network, int64(amountSats), paymentHash[:], memo, 30*24*time.Hour)
	if err != nil {
		return "", err
	}

	preimageAsInt := new(big.Int).SetBytes(preimage[:])
	shares, err := secretsharing.SplitSecretWithProofs(preimageAsInt, secp256k1.Params().N, config.Threshold, len(config.SigningOperators))
	if err != nil {
		return "", err
	}

	wg := sync.WaitGroup{}
	results := make(chan error, len(config.SigningOperators))
	for _, operator := range config.SigningOperators {
		share := shares[operator.ID]
		shareProto := share.MarshalProto()

		wg.Add(1)
		go func(operator *so.SigningOperator) {
			defer wg.Done()
			sparkConn, err := operator.NewOperatorGRPCConnection()
			if err != nil {
				results <- fmt.Errorf("failed to connect to operator: %w", err)
				return
			}
			defer sparkConn.Close()
			sparkClient := pb.NewSparkServiceClient(sparkConn)
			token, err := AuthenticateWithConnection(ctx, config, sparkConn)
			if err != nil {
				results <- err
				return
			}
			tmpCtx := ContextWithToken(ctx, token)
			_, err = sparkClient.StorePreimageShare(tmpCtx, &pb.StorePreimageShareRequest{
				PaymentHash:           paymentHash[:],
				PreimageShare:         shareProto,
				Threshold:             uint32(config.Threshold),
				InvoiceString:         invoice,
				UserIdentityPublicKey: config.IdentityPublicKey().Serialize(),
			})
			if err != nil {
				results <- err
			}
		}(operator)
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			return "", err
		}
	}
	return invoice, nil
}

func CreateLightningInvoiceWithPreimage(
	ctx context.Context,
	config *TestWalletConfig,
	creator LightningInvoiceCreator,
	amountSats uint64,
	memo string,
	preimage [32]byte,
) (string, error) {
	paymentHash := sha256.Sum256(preimage[:])
	return CreateLightningInvoiceWithPreimageAndHash(ctx, config, creator, amountSats, memo, preimage, paymentHash)
}

// CreateLightningInvoice creates a Lightning invoice and sends the preimage shares to the signing operators.
func CreateLightningInvoice(ctx context.Context, config *TestWalletConfig, creator LightningInvoiceCreator, amountSats uint64, memo string) (string, error) {
	preimagePrivKey := keys.GeneratePrivateKey()
	preimage := preimagePrivKey.Serialize()
	return CreateLightningInvoiceWithPreimage(ctx, config, creator, amountSats, memo, [32]byte(preimage))
}
