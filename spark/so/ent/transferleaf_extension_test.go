package ent_test

import (
	"testing"

	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// marshalTransferLeafProto re-splits the stored (signature, signature_scheme)
// columns back into the wire's mutually exclusive sig oneof, with a fallback to
// the embedded key_tweak blob. These tests pin the split rules for every
// storage state.
func TestMarshalProto_LeafSignatureSplit(t *testing.T) {
	sig := []byte("per-leaf-signature-bytes")

	keyTweakBlob := func(t *testing.T, tweak *pb.SendLeafKeyTweak) []byte {
		t.Helper()
		tweak.SecretShareTweak = &pb.SecretShare{Proofs: [][]byte{{0x02}}}
		blob, err := proto.Marshal(tweak)
		require.NoError(t, err)
		return blob
	}

	tests := []struct {
		name              string
		signature         []byte
		signatureScheme   int32
		keyTweak          func(t *testing.T) []byte
		expectedLegacySig []byte
		expectedTypedSig  *pbcommon.Signature
	}{
		{
			name:              "legacy bytes with no scheme emit the legacy arm",
			signature:         sig,
			expectedLegacySig: sig,
		},
		{
			name:             "ECDSA scheme emits the typed arm",
			signature:        sig,
			signatureScheme:  int32(pbcommon.SignatureScheme_SIGNATURE_SCHEME_ECDSA),
			expectedTypedSig: &pbcommon.Signature{Scheme: pbcommon.SignatureScheme_SIGNATURE_SCHEME_ECDSA, Signature: sig},
		},
		{
			name:             "Schnorr scheme emits the typed arm",
			signature:        sig,
			signatureScheme:  int32(pbcommon.SignatureScheme_SIGNATURE_SCHEME_SCHNORR),
			expectedTypedSig: &pbcommon.Signature{Scheme: pbcommon.SignatureScheme_SIGNATURE_SCHEME_SCHNORR, Signature: sig},
		},
		{
			name: "key_tweak blob fallback picks up the embedded typed signature",
			keyTweak: func(t *testing.T) []byte {
				return keyTweakBlob(t, &pb.SendLeafKeyTweak{Sig: &pb.SendLeafKeyTweak_TypedSignature{
					TypedSignature: &pbcommon.Signature{Scheme: pbcommon.SignatureScheme_SIGNATURE_SCHEME_SCHNORR, Signature: sig},
				}})
			},
			expectedTypedSig: &pbcommon.Signature{Scheme: pbcommon.SignatureScheme_SIGNATURE_SCHEME_SCHNORR, Signature: sig},
		},
		{
			name:            "key_tweak blob legacy arm ignores a stale column scheme",
			signatureScheme: int32(pbcommon.SignatureScheme_SIGNATURE_SCHEME_SCHNORR),
			keyTweak: func(t *testing.T) []byte {
				return keyTweakBlob(t, &pb.SendLeafKeyTweak{Sig: &pb.SendLeafKeyTweak_Signature{Signature: sig}})
			},
			expectedLegacySig: sig,
		},
		{
			name: "no signature anywhere emits no sig",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			transfer, _, _ := preloadedTransfer(t)
			transferLeaf := transfer.Edges.TransferLeaves[0]
			transferLeaf.Signature = tc.signature
			transferLeaf.SignatureScheme = tc.signatureScheme
			if tc.keyTweak != nil {
				transferLeaf.KeyTweak = tc.keyTweak(t)
			}
			leafNodeID := transferLeaf.Edges.Leaf.ID.String()

			transferProto, err := transfer.MarshalProto(t.Context())
			require.NoError(t, err)

			var marshaledLeaf *pb.TransferLeaf
			for _, l := range transferProto.GetLeaves() {
				if l.GetLeaf().GetId() == leafNodeID {
					marshaledLeaf = l
				}
			}
			require.NotNil(t, marshaledLeaf)

			if tc.expectedTypedSig != nil {
				typed := marshaledLeaf.GetTypedSignature()
				require.NotNil(t, typed, "expected the typed arm")
				require.Equal(t, tc.expectedTypedSig.GetScheme(), typed.GetScheme())
				require.Equal(t, tc.expectedTypedSig.GetSignature(), typed.GetSignature())
				require.Empty(t, marshaledLeaf.GetSignature(), "legacy arm must be empty when typed is set")
			} else if tc.expectedLegacySig != nil {
				require.Equal(t, tc.expectedLegacySig, marshaledLeaf.GetSignature())
				require.Nil(t, marshaledLeaf.GetTypedSignature(), "typed arm must be empty for legacy signatures")
			} else {
				require.Nil(t, marshaledLeaf.GetSig(), "no sig arm expected")
			}
		})
	}
}
