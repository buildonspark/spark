package wallet

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/lightsparkdev/spark-go/common"
	pbauthn "github.com/lightsparkdev/spark-go/proto/spark_authn"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
)

// AuthenticateWithServer authenticates the user with the server and returns a session token.
func AuthenticateWithServer(ctx context.Context, config *Config) (string, error) {
	conn, err := common.NewGRPCConnection(config.CoodinatorAddress())
	if err != nil {
		return "", fmt.Errorf("failed to connect to coordinator: %v", err)
	}
	defer conn.Close()

	client := pbauthn.NewSparkAuthnServiceClient(conn)

	challengeResp, err := client.GetChallenge(ctx, &pbauthn.GetChallengeRequest{
		PublicKey: config.IdentityPublicKey(),
	})
	if err != nil {
		return "", fmt.Errorf("failed to get challenge: %v", err)
	}

	challengeBytes, err := proto.Marshal(challengeResp.ProtectedChallenge.Challenge)
	if err != nil {
		return "", fmt.Errorf("failed to marshal challenge: %v", err)
	}

	hash := sha256.Sum256(challengeBytes)
	signature := ecdsa.Sign(&config.IdentityPrivateKey, hash[:])

	verifyResp, err := client.VerifyChallenge(ctx, &pbauthn.VerifyChallengeRequest{
		ProtectedChallenge: challengeResp.ProtectedChallenge,
		Signature:          signature.Serialize(),
		PublicKey:          config.IdentityPublicKey(),
	})
	if err != nil {
		return "", fmt.Errorf("failed to verify challenge: %v", err)
	}

	return verifyResp.SessionToken, nil
}

// ContextWithToken adds the session token to the context.
func ContextWithToken(ctx context.Context, token string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
}
