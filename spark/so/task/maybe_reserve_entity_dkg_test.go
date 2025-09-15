package task

import (
	"context"
	"math/rand/v2"
	"net"
	"testing"

	"github.com/lightsparkdev/spark/common/keys"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	_ "github.com/lib/pq"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/knobs"
	sparktesting "github.com/lightsparkdev/spark/testing"
)

// getReserveEntityDkgTask returns the startup task we are testing.
func getReserveEntityDkgTask() (StartupTaskSpec, error) {
	for _, t := range AllStartupTasks() {
		if t.Name == "maybe_reserve_entity_dkg" {
			return t, nil
		}
	}
	return StartupTaskSpec{}, assert.AnError
}

// pruneOperators keeps only the current operator in the SigningOperatorMap so
// that ExecuteTaskWithAllOperators() becomes a cheap no-op in unit tests.
func pruneOperators(cfg *so.Config) {
	for id := range cfg.SigningOperatorMap {
		if id != cfg.Identifier {
			delete(cfg.SigningOperatorMap, id)
		}
	}
}

// mockSparkInternalServiceServer provides a mock implementation of the gRPC service
// for testing cross-operator communication.
type mockSparkInternalServiceServer struct {
	spark_internal.UnimplementedSparkInternalServiceServer
	errToReturn        error
	expectedKeyshareID string
}

func (s *mockSparkInternalServiceServer) ReserveEntityDkgKey(_ context.Context, req *spark_internal.ReserveEntityDkgKeyRequest) (*emptypb.Empty, error) {
	if s.errToReturn != nil {
		return nil, s.errToReturn
	}
	if s.expectedKeyshareID != "" && s.expectedKeyshareID != req.KeyshareId {
		return nil, status.Errorf(codes.InvalidArgument, "expected keyshare %s, got %s", s.expectedKeyshareID, req.KeyshareId)
	}
	return &emptypb.Empty{}, nil
}

func TestMain(m *testing.M) {
	stop := db.StartPostgresServer()
	defer stop()
	m.Run()
}

func TestReserveEntityDkg_OperatorDown(t *testing.T) {
	tests := []struct {
		name            string
		failOneOperator bool
	}{
		{
			name:            "all operators succeed",
			failOneOperator: false,
		},
		{
			name:            "one operator fails",
			failOneOperator: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, sessionCtx := db.ConnectToTestPostgres(t)
			client := sessionCtx.Client

			cfg, err := sparktesting.TestConfig()
			require.NoError(t, err)
			rng := rand.NewChaCha8([32]byte{})
			cfg.Index = 0 // coordinator

			var failingOperatorID string
			for id := range cfg.SigningOperatorMap {
				if id != cfg.Identifier {
					failingOperatorID = id
					break
				}
			}

			var servers []*grpc.Server
			for id, operator := range cfg.SigningOperatorMap {
				if id == cfg.Identifier {
					continue
				}
				lis, err := net.Listen("tcp", "127.0.0.1:0")
				require.NoError(t, err)
				operator.AddressRpc = lis.Addr().String()
				operator.OperatorConnectionFactory = &sparktesting.DangerousTestOperatorConnectionFactoryNoTLS{}

				var injectedErr error
				if tc.failOneOperator && id == failingOperatorID {
					injectedErr = status.Error(codes.Unavailable, "I am down for maintenance")
				}

				s := grpc.NewServer()
				spark_internal.RegisterSparkInternalServiceServer(s, &mockSparkInternalServiceServer{errToReturn: injectedErr})
				servers = append(servers, s)
				go func() { _ = s.Serve(lis) }()
			}
			t.Cleanup(func() {
				for _, s := range servers {
					s.Stop()
				}
			})

			// Seed keyshare.
			secret := keys.MustGeneratePrivateKeyFromRand(rng)
			sk := client.SigningKeyshare.Create().
				SetStatus(st.KeyshareStatusAvailable).
				SetSecretShare(secret.Serialize()).
				SetPublicKey(secret.Public()).
				SetPublicShares(map[string]keys.Public{}).
				SetMinSigners(2).
				SetCoordinatorIndex(0).
				SaveX(ctx)

			reserveTask, err := getReserveEntityDkgTask()
			require.NoError(t, err)
			err = reserveTask.RunOnce(ctx, cfg, client, knobs.NewFixedKnobs(map[string]float64{}))

			if tc.failOneOperator {
				require.ErrorContains(t, err, "Unavailable")
				// EntityDkgKey should still exist due to earlier commit.
			} else {
				require.NoError(t, err)
			}
			edkg, qerr := client.EntityDkgKey.Query().WithSigningKeyshare().Only(ctx)
			require.NoError(t, qerr)
			require.NotNil(t, edkg.Edges.SigningKeyshare)
			assert.Equal(t, sk.ID, edkg.Edges.SigningKeyshare.ID)
		})
	}
}

// TestReserveEntityDkg_Idempotent ensures running the task twice is safe.
func TestReserveEntityDkg_Idempotent(t *testing.T) {
	t.Parallel()
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	client := sessionCtx.Client

	cfg, err := sparktesting.TestConfig()
	require.NoError(t, err)
	cfg.Index = 0
	pruneOperators(cfg)

	// Pre-create an EntityDkgKey and mark keyshare in use
	rng := rand.NewChaCha8([32]byte{})
	secret := keys.MustGeneratePrivateKeyFromRand(rng)
	sk := client.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(secret.Serialize()).
		SetPublicKey(secret.Public()).
		SetPublicShares(map[string]keys.Public{}).
		SetMinSigners(2).
		SetCoordinatorIndex(0).
		SaveX(ctx)

	reserveTask, err := getReserveEntityDkgTask()
	require.NoError(t, err)
	err = reserveTask.RunOnce(ctx, cfg, client, knobs.NewFixedKnobs(map[string]float64{}))
	require.NoError(t, err)

	// Run again to ensure idempotency.
	err = reserveTask.RunOnce(ctx, cfg, client, knobs.NewFixedKnobs(map[string]float64{}))
	require.NoError(t, err)

	count, err := client.EntityDkgKey.Query().Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "should still have exactly one EntityDkgKey")

	edkg, qerr := client.EntityDkgKey.Query().WithSigningKeyshare().Only(ctx)
	require.NoError(t, qerr)
	require.NotNil(t, edkg.Edges.SigningKeyshare)
	assert.Equal(t, sk.ID, edkg.Edges.SigningKeyshare.ID)
}

// TestReserveEntityDkg_NonCoordinator verifies that non-coordinator operators
// do not attempt to reserve an entity DKG key.
func TestReserveEntityDkg_NonCoordinator(t *testing.T) {
	t.Parallel()
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	client := sessionCtx.Client

	cfg, err := sparktesting.TestConfig()
	require.NoError(t, err)
	cfg.Index = 1 // non-coordinator
	pruneOperators(cfg)

	reserveTask, err := getReserveEntityDkgTask()
	require.NoError(t, err)
	err = reserveTask.RunOnce(ctx, cfg, client, knobs.NewFixedKnobs(map[string]float64{}))
	require.NoError(t, err)

	count, err := client.EntityDkgKey.Query().Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, count, "non-coordinators should not create an EntityDkgKey")
}
