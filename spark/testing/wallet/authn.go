package wallet

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	pbauthn "github.com/lightsparkdev/spark/proto/spark_authn"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
)

// AuthenticateWithServer authenticates with the coordinator and returns a session token.
func AuthenticateWithServer(ctx context.Context, config *TestWalletConfig) (string, error) {
	conn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		return "", fmt.Errorf("failed to connect to coordinator: %w", err)
	}
	defer conn.Close()
	return AuthenticateWithConnection(ctx, config, conn)
}

// AuthenticateWithConnection authenticates to the server using an existing GRPC connection.
func AuthenticateWithConnection(ctx context.Context, config *TestWalletConfig, conn grpc.ClientConnInterface) (string, error) {
	client := pbauthn.NewSparkAuthnServiceClient(conn)

	challengeResp, err := client.GetChallenge(ctx, &pbauthn.GetChallengeRequest{
		PublicKey: config.IdentityPublicKey().Serialize(),
	})
	if err != nil {
		return "", fmt.Errorf("failed to get challenge: %w", err)
	}

	challengeBytes, err := proto.Marshal(challengeResp.ProtectedChallenge.Challenge)
	if err != nil {
		return "", fmt.Errorf("failed to marshal challenge: %w", err)
	}

	hash := sha256.Sum256(challengeBytes)
	signature := ecdsa.Sign(config.IdentityPrivateKey.ToBTCEC(), hash[:])

	verifyResp, err := client.VerifyChallenge(ctx, &pbauthn.VerifyChallengeRequest{
		ProtectedChallenge: challengeResp.ProtectedChallenge,
		Signature:          signature.Serialize(),
		PublicKey:          config.IdentityPublicKey().Serialize(),
	})
	if err != nil {
		return "", fmt.Errorf("failed to verify challenge: %w", err)
	}

	return verifyResp.SessionToken, nil
}

// ContextWithToken adds the session token to the context. If there is an existing session token, it will be replaced.
func ContextWithToken(ctx context.Context, token string) context.Context {
	const authKey = "authorization"
	authValue := "Bearer " + token

	md, ok := metadata.FromOutgoingContext(ctx)
	if !ok {
		md = metadata.MD{}
	}

	newMd := md.Copy()
	newMd.Set(authKey, authValue)
	return metadata.NewOutgoingContext(ctx, newMd)
}
