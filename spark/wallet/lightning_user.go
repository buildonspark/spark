package wallet

import (
	"context"
	"crypto/sha256"
	"math/big"
	"sync"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/lightsparkdev/spark-go/common"
	secretsharing "github.com/lightsparkdev/spark-go/common/secret_sharing"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	"github.com/lightsparkdev/spark-go/so"
)

// LightningInvoiceCreator is an interface that can be used to create a Lightning invoice.
type LightningInvoiceCreator interface {
	CreateInvoice(amountSats uint64, paymentHash []byte, memo string) (*string, error)
}

func CreateLightningInvoiceWithPreimage(
	ctx context.Context,
	config *Config,
	creator LightningInvoiceCreator,
	amountSats uint64,
	memo string,
	preimage []byte,
) (*string, error) {
	paymentHash := sha256.Sum256(preimage)
	invoice, err := creator.CreateInvoice(amountSats, paymentHash[:], memo)
	if err != nil {
		return nil, err
	}

	preimageAsInt := new(big.Int).SetBytes(preimage)
	shares, err := secretsharing.SplitSecretWithProofs(preimageAsInt, secp256k1.Params().N, config.Threshold, len(config.SigningOperators))
	if err != nil {
		return nil, err
	}

	wg := sync.WaitGroup{}
	results := make(chan error, len(config.SigningOperators))
	for _, operator := range config.SigningOperators {
		share := shares[operator.ID]
		shareProto := share.MarshalProto()

		wg.Add(1)
		go func(operator *so.SigningOperator) {
			defer wg.Done()
			sparkConn, err := common.NewGRPCConnection(operator.Address)
			if err != nil {
				results <- err
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
				InvoiceString:         *invoice,
				UserIdentityPublicKey: config.IdentityPublicKey(),
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
			return nil, err
		}
	}
	return invoice, nil
}

// CreateLightningInvoice creates a Lightning invoice and sends the preimage shares to the signing operators.
func CreateLightningInvoice(ctx context.Context, config *Config, creator LightningInvoiceCreator, amountSats uint64, memo string) (*string, error) {
	preimagePrivKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		return nil, err
	}

	preimage := preimagePrivKey.Serialize()
	return CreateLightningInvoiceWithPreimage(ctx, config, creator, amountSats, memo, preimage)
}
