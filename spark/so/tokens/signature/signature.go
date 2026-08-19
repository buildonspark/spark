package signature

import (
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
)

// GetEffectiveSingleSignature extracts the single-signature bytes from a
// SignatureWithIndex, handling the authority_signatures oneof with fallback
// to the deprecated signature field. Returns nil for multisig signatures.
// For the delegated allowance arm it returns the spender's signature bytes;
// whether that arm is acceptable in context (and against which key it must
// verify) is the caller's responsibility, not this extractor's.
func GetEffectiveSingleSignature(sig *tokenpb.SignatureWithIndex) []byte {
	if sig == nil {
		return nil
	}
	switch v := sig.GetAuthoritySignatures().(type) {
	case *tokenpb.SignatureWithIndex_SingleSignature:
		return v.SingleSignature.GetSignature()
	case *tokenpb.SignatureWithIndex_MultisigSignatures:
		return nil
	case *tokenpb.SignatureWithIndex_AllowanceSignature:
		return v.AllowanceSignature.GetSpenderSignature().GetSignature()
	default:
		return sig.GetSignature()
	}
}
