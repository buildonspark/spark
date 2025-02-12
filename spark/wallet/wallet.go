package wallet

import (
	"context"
	"encoding/hex"

	sspapi "github.com/lightsparkdev/spark-go/wallet/ssp_api"
)

// SignleKeyWallet is a wallet that uses a single private key for all signing keys.
// This is the most simple type of wallet and for testing purposes only.
type SignleKeyWallet struct {
	Config            *Config
	SigningPrivateKey []byte
}

// NewSignleKeyWallet creates a new single key wallet.
func NewSignleKeyWallet(config *Config, signingPrivateKey []byte) *SignleKeyWallet {
	return &SignleKeyWallet{
		Config:            config,
		SigningPrivateKey: signingPrivateKey,
	}
}

func (w *SignleKeyWallet) CreateLightningInvoice(ctx context.Context, amount int64, memo string) (*string, int64, error) {
	requester, err := sspapi.NewRequesterWithBaseURL(hex.EncodeToString(w.Config.IdentityPublicKey()), nil)
	if err != nil {
		return nil, 0, err
	}
	api := sspapi.NewSparkServiceAPI(requester)
	invoice, fees, err := CreateLightningInvoice(ctx, w.Config, api, uint64(amount), memo)
	if err != nil {
		return nil, 0, err
	}
	return invoice, fees, nil
}
