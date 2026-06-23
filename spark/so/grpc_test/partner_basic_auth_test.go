package grpctest

import (
	"context"
	"encoding/base64"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/argon2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/lightsparkdev/spark/common/keys"
	jwtkeys "github.com/lightsparkdev/spark/common/keys/jwt"
	pbpartner "github.com/lightsparkdev/spark/proto/spark_partner"
	"github.com/lightsparkdev/spark/so/db"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/lightsparkdev/spark/testing/wallet"
)

// argon2idPHC produces a PHC-encoded argon2id hash, matching what an operator stores in
// partner_keys.basic_auth_secret_hash.
func argon2idPHC(secret string) string {
	const (
		memKiB  = 65536
		time    = 3
		threads = 4
		keyLen  = 32
	)
	salt := []byte("0123456789abcdef")
	h := argon2.IDKey([]byte(secret), salt, time, memKiB, threads, keyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, memKiB, time, threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(h),
	)
}

func basicAuthHeaderValue(partnerID, secret string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(partnerID+":"+secret))
}

// TestSparkPartnerServiceBasicAuth exercises the HTTP Basic Auth layer on SparkPartnerService through
// the real operator interceptor chain. It asserts only on the auth outcome (the query handler isn't
// implemented yet, so a valid credential reaches it and gets Unimplemented): a valid credential gets
// past auth (so the status is anything other than Unauthenticated), while bad/missing credentials are
// rejected with Unauthenticated before the handler runs.
func TestSparkPartnerServiceBasicAuth(t *testing.T) {
	const secret = "s3cret-shared-with-partner"
	partnerID := "test-partner-" + uuid.New().String()[:8]

	config := wallet.NewTestWalletConfig(t)

	// Provision the partner_keys row with a Basic Auth secret hash on the coordinator.
	coordSetupClient := db.NewPostgresEntClientForIntegrationTest(t, config.CoordinatorDatabaseURI)
	defer coordSetupClient.Close()
	jwtPubKey := jwtkeys.PublicFromSecp256k1(keys.GeneratePrivateKey().Public())
	pk, err := coordSetupClient.PartnerKey.Create().
		SetPartnerID(partnerID).
		SetPartnerName("Integration Test Partner (Basic Auth)").
		SetJwtPublicKey(jwtPubKey).
		SetBasicAuthSecretHash(argon2idPHC(secret)).
		Save(t.Context())
	require.NoError(t, err, "failed to create partner key on coordinator")
	t.Cleanup(func() {
		// Open a fresh client: coordSetupClient's deferred Close runs before this cleanup.
		cleanupClient := db.NewPostgresEntClientForIntegrationTest(t, config.CoordinatorDatabaseURI)
		defer cleanupClient.Close()
		// t.Context() is canceled just before cleanups run, so derive an uncanceled context for the delete.
		if err := cleanupClient.PartnerKey.DeleteOneID(pk.ID).Exec(context.WithoutCancel(t.Context())); err != nil {
			t.Logf("cleanup: failed to delete partner key %s: %v", pk.ID, err)
		}
	})

	conn, err := sparktesting.DangerousNewGRPCConnectionWithoutVerifyTLS(config.CoordinatorAddress(), nil)
	require.NoError(t, err)
	defer conn.Close()
	client := pbpartner.NewSparkPartnerServiceClient(conn)

	req := &pbpartner.QuerySparkTransactionVolumesRequest{
		StartDate: "2024-01-01",
		EndDate:   "2024-01-31",
	}

	call := func(authValue string) error {
		ctx := t.Context()
		if authValue != "" {
			ctx = metadata.AppendToOutgoingContext(ctx, "authorization", authValue)
		}
		_, err := client.QuerySparkTransactionVolumes(ctx, req)
		return err
	}

	t.Run("valid credentials pass auth", func(t *testing.T) {
		// Auth succeeds; the request reaches the handler. The handler isn't implemented yet, so it
		// returns Unimplemented — but the point is that it must not be an auth rejection.
		err := call(basicAuthHeaderValue(partnerID, secret))
		require.NotEqual(t, codes.Unauthenticated, status.Code(err),
			"valid Basic Auth credentials must not be rejected as Unauthenticated (got: %v)", err)
	})

	rejected := map[string]string{
		"wrong secret":      basicAuthHeaderValue(partnerID, "wrong-secret"),
		"unknown partner":   basicAuthHeaderValue("does-not-exist", secret),
		"missing header":    "",
		"non-basic scheme":  "Bearer " + base64.StdEncoding.EncodeToString([]byte(partnerID+":"+secret)),
		"malformed payload": "Basic not-base64!!",
	}
	for name, authValue := range rejected {
		t.Run("rejects "+name, func(t *testing.T) {
			err := call(authValue)
			require.Equal(t, codes.Unauthenticated, status.Code(err),
				"expected Unauthenticated for %q (got: %v)", name, err)
		})
	}
}
