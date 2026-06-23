package brontide

import (
	"github.com/lightningnetwork/lnd/keychain"
	"github.com/lightsparkdev/spark/common/keys"
)

// newLocalECDH wraps a Spark identity private key in lnd's SingleKeyECDH so it can be handed to brontide.NewBrontideMachine
// as the local static key.
func newLocalECDH(priv keys.Private) keychain.SingleKeyECDH {
	return &keychain.PrivKeyECDH{PrivKey: priv.ToBTCEC()}
}
