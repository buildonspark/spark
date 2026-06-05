package signature

import (
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
)

// GetEffectiveSingleSignature extracts the single-signature bytes from a
// SignatureWithIndex, handling the authority_signatures oneof with fallback
// to the deprecated signature field. Returns nil for multisig signatures.
func GetEffectiveSingleSignature(sig *tokenpb.SignatureWithIndex) []byte {
	if sig == nil {
		return nil
	}
	switch v := sig.GetAuthoritySignatures().(type) {
	case *tokenpb.SignatureWithIndex_SingleSignature:
		return v.SingleSignature.GetSignature()
	case *tokenpb.SignatureWithIndex_MultisigSignatures:
		return nil
	default:
		return sig.GetSignature()
	}
}
