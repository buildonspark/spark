package common

import (
	"crypto/sha256"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/lightsparkdev/spark/common/keys"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	"github.com/stretchr/testify/require"
)

func TestVerifySignatureWithScheme(t *testing.T) {
	priv := keys.GeneratePrivateKey()
	otherPriv := keys.GeneratePrivateKey()
	digest := sha256.Sum256([]byte("signed payload"))
	otherDigest := sha256.Sum256([]byte("some other payload"))

	ecdsaSig := ecdsa.Sign(priv.ToBTCEC(), digest[:]).Serialize()
	schnorrSig, err := schnorr.Sign(priv.ToBTCEC(), digest[:])
	require.NoError(t, err)

	for name, tc := range map[string]struct {
		pub                  keys.Public
		scheme               pbcommon.SignatureScheme
		sig                  []byte
		hash                 []byte
		expectedErrSubstring string
	}{
		"valid ecdsa":   {priv.Public(), pbcommon.SignatureScheme_SIGNATURE_SCHEME_ECDSA, ecdsaSig, digest[:], ""},
		"valid schnorr": {priv.Public(), pbcommon.SignatureScheme_SIGNATURE_SCHEME_SCHNORR, schnorrSig.Serialize(), digest[:], ""},
		"ecdsa wrong message": {
			priv.Public(), pbcommon.SignatureScheme_SIGNATURE_SCHEME_ECDSA, ecdsaSig, otherDigest[:], "invalid signature",
		},
		"schnorr wrong message": {
			priv.Public(), pbcommon.SignatureScheme_SIGNATURE_SCHEME_SCHNORR, schnorrSig.Serialize(), otherDigest[:], "invalid signature",
		},
		"ecdsa wrong key": {
			otherPriv.Public(), pbcommon.SignatureScheme_SIGNATURE_SCHEME_ECDSA, ecdsaSig, digest[:], "invalid signature",
		},
		"schnorr wrong key": {
			otherPriv.Public(), pbcommon.SignatureScheme_SIGNATURE_SCHEME_SCHNORR, schnorrSig.Serialize(), digest[:], "invalid signature",
		},
		"ecdsa bytes under schnorr scheme": {
			priv.Public(), pbcommon.SignatureScheme_SIGNATURE_SCHEME_SCHNORR, ecdsaSig, digest[:], "malformed schnorr signature",
		},
		"schnorr bytes under ecdsa scheme": {
			priv.Public(), pbcommon.SignatureScheme_SIGNATURE_SCHEME_ECDSA, schnorrSig.Serialize(), digest[:], "malformed DER signature",
		},
		"unspecified scheme": {
			priv.Public(), pbcommon.SignatureScheme_SIGNATURE_SCHEME_UNSPECIFIED, ecdsaSig, digest[:], "unsupported signature scheme",
		},
		"empty message hash": {
			priv.Public(), pbcommon.SignatureScheme_SIGNATURE_SCHEME_SCHNORR, schnorrSig.Serialize(), nil, "message hash cannot be empty",
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := VerifySignatureWithScheme(tc.pub, tc.scheme, tc.sig, tc.hash)
			if tc.expectedErrSubstring == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.expectedErrSubstring)
			}
		})
	}
}
