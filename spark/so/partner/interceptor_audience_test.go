package partner

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// A partner's `aud` may be a string or an array (RFC 7519), and a regression in the containment
// match would silently unattribute every multi-audience token rather than failing a request.
func TestPartnerJWTInterceptor_AudienceShapes(t *testing.T) {
	const peerAudience = "spark-ssp"

	for _, tc := range []struct {
		name       string
		aud        any
		omitAud    bool
		attributed bool
	}{
		{name: "single string", aud: expectedAudience, attributed: true},
		{name: "array containing ours", aud: []string{expectedAudience, peerAudience}, attributed: true},
		{name: "array with ours last", aud: []string{peerAudience, expectedAudience}, attributed: true},
		{name: "array with ours and an unrecognized audience", aud: []string{expectedAudience, "audience-we-know-nothing-about"}, attributed: true},
		{name: "single element array", aud: []string{expectedAudience}, attributed: true},
		{name: "array without ours", aud: []string{peerAudience}, attributed: false},
		{name: "empty array", aud: []string{}, attributed: false},
		{name: "array with a non-string element", aud: []any{123, expectedAudience}, attributed: false},
		{name: "claim is neither a string nor an array", aud: 123, attributed: false},
		{name: "claim omitted", omitAud: true, attributed: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			priv, pub := makeP256Key(t)
			partnerID := "partner-a"
			partnerKeyID := uuid.New()
			partnerDBID := uuid.New()
			i := makeTestInterceptor(
				map[string]*testPartnerKeyEntry{partnerID: {pubKey: pub, partnerKeyID: partnerKeyID}},
				map[string]uuid.UUID{partnerKeyID.String() + "/" + testLabel: partnerDBID},
			)

			claims := map[string]any{
				"iss": partnerID,
				"sub": testLabel,
				"iat": time.Now().Unix(),
				"exp": time.Now().Add(time.Hour).Unix(),
			}
			if !tc.omitAud {
				claims["aud"] = tc.aud
			}
			token := makeES256JWTWithClaims(t, priv, claims)

			ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs(partnerJWTHeader, token))
			resp, err := i.PartnerJWTInterceptor(ctx, nil, &grpc.UnaryServerInfo{}, noopHandler)
			require.NoError(t, err, "an unusable audience must leave the request unattributed, never fail it")

			partnerInfo, present := GetPartnerInfoFromContext(respCtx(t, resp))
			require.Equal(t, tc.attributed, present)
			if tc.attributed {
				assert.Equal(t, partnerDBID, partnerInfo.PartnerDBID)
				assert.Equal(t, partnerID, partnerInfo.PartnerID)
				assert.Equal(t, testLabel, partnerInfo.Label)
			}
		})
	}
}
