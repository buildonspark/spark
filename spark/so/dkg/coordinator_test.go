package dkg

import (
	"context"
	"net"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	pbdkg "github.com/lightsparkdev/spark/proto/dkg"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/signingkeyshare"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func TestMain(m *testing.M) {
	stop := db.StartPostgresServer()
	defer stop()
	os.Exit(m.Run())
}

// mockDKGServiceServer implements a mock DKG service for testing confirmation flow
type mockDKGServiceServer struct {
	pbdkg.UnimplementedDKGServiceServer
	availableKeys    map[string]bool // key ID -> available
	unavailableDelay time.Duration   // how long keys stay unavailable
	startTime        time.Time
}

func (m *mockDKGServiceServer) RoundConfirmation(ctx context.Context, req *pbdkg.RoundConfirmationRequest) (*pbdkg.RoundConfirmationResponse, error) {
	// Simulate keys becoming available after a delay
	if m.unavailableDelay > 0 && time.Since(m.startTime) < m.unavailableDelay {
		// All keys unavailable
		return &pbdkg.RoundConfirmationResponse{
			AvailableKeyIds:   []string{},
			UnavailableKeyIds: req.KeyIds,
		}, nil
	}

	available := []string{}
	unavailable := []string{}
	for _, keyID := range req.KeyIds {
		if m.availableKeys[keyID] {
			available = append(available, keyID)
		} else {
			unavailable = append(unavailable, keyID)
		}
	}

	return &pbdkg.RoundConfirmationResponse{
		AvailableKeyIds:   available,
		UnavailableKeyIds: unavailable,
	}, nil
}

// TestConfirmAndMarkAvailableKeys_Success tests the happy path where all operators
// immediately return AVAILABLE for all requested keys
func TestConfirmAndMarkAvailableKeys_Success(t *testing.T) {
	ctx, testCtx := db.ConnectToTestPostgres(t)
	config := createTestConfig(t, 3)

	// Create PENDING keyshares for the coordinator
	keyIDs := createPendingKeyshares(t, ctx, testCtx.Client, config.Index, 5)

	// Set up mock gRPC servers for other operators (all keys available immediately)
	mockServers := setupMockDKGServers(t, keyIDs, 0)
	defer cleanupMockServers(mockServers)

	// Update config with test connection factory
	updateConfigWithMockConnections(config, mockServers)

	// Call the confirmation function
	err := ConfirmAndMarkAvailableKeys(ctx, config, keyIDs, uuid.New())
	require.NoError(t, err)

	// Verify all keyshares are now AVAILABLE
	verifyKeysharesStatus(t, ctx, keyIDs, schematype.KeyshareStatusAvailable)
}

// TestConfirmAndMarkAvailableKeys_AllUnavailable tests that when all operators
// report all keys as unavailable, the function returns an error
func TestConfirmAndMarkAvailableKeys_AllUnavailable(t *testing.T) {
	ctx, testCtx := db.ConnectToTestPostgres(t)
	config := createTestConfig(t, 3)
	keyIDs := createPendingKeyshares(t, ctx, testCtx.Client, config.Index, 5)

	// Set up mocks that return all keys as unavailable
	mockServers := setupMockDKGServers(t, keyIDs, 10*time.Minute) // long delay = all unavailable
	defer cleanupMockServers(mockServers)

	updateConfigWithMockConnections(config, mockServers)

	// Should fail since no keys are available on all operators
	err := ConfirmAndMarkAvailableKeys(ctx, config, keyIDs, uuid.New())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no keys available across all operators")

	// Verify all keyshares are still PENDING
	verifyKeysharesStatus(t, ctx, keyIDs, schematype.KeyshareStatusPending)
}

// TestConfirmAndMarkAvailableKeys_PartialSuccess tests that when some keys are available
// on all operators but others are not, only the available ones are marked AVAILABLE
func TestConfirmAndMarkAvailableKeys_PartialSuccess(t *testing.T) {
	ctx, testCtx := db.ConnectToTestPostgres(t)
	config := createTestConfig(t, 3)
	keyIDs := createPendingKeyshares(t, ctx, testCtx.Client, config.Index, 5)

	// Set up mocks where some keys are available and some are not
	mockServers := setupMockDKGServers(t, keyIDs, 0)
	defer cleanupMockServers(mockServers)

	// Make the first operator report that keys 0 and 1 are unavailable
	mockServers[0].mock.availableKeys[keyIDs[0].String()] = false
	mockServers[0].mock.availableKeys[keyIDs[1].String()] = false

	updateConfigWithMockConnections(config, mockServers)

	// Should succeed but only mark keys 2, 3, 4 as AVAILABLE
	err := ConfirmAndMarkAvailableKeys(ctx, config, keyIDs, uuid.New())
	require.NoError(t, err)

	// Verify keys 2, 3, 4 are AVAILABLE
	verifyKeysharesStatus(t, ctx, keyIDs[2:], schematype.KeyshareStatusAvailable)
	// Verify keys 0, 1 are still PENDING
	verifyKeysharesStatus(t, ctx, keyIDs[0:2], schematype.KeyshareStatusPending)
}

// TestConfirmAndMarkAvailableKeys_OneOperatorMissingAllKeys tests that if one operator
// is missing all keys, no keys are marked AVAILABLE
func TestConfirmAndMarkAvailableKeys_OneOperatorMissingAllKeys(t *testing.T) {
	ctx, testCtx := db.ConnectToTestPostgres(t)
	config := createTestConfig(t, 3)

	keyIDs := createPendingKeyshares(t, ctx, testCtx.Client, config.Index, 5)

	// Set up mocks where one operator reports all keys as unavailable
	mockServers := setupMockDKGServers(t, keyIDs, 0)
	defer cleanupMockServers(mockServers)

	// Make the first operator report all keys as unavailable
	for _, keyID := range keyIDs {
		mockServers[0].mock.availableKeys[keyID.String()] = false
	}

	updateConfigWithMockConnections(config, mockServers)

	// Should fail since no keys are available on ALL operators
	err := ConfirmAndMarkAvailableKeys(ctx, config, keyIDs, uuid.New())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no keys available across all operators")

	// Verify all keyshares are still PENDING
	verifyKeysharesStatus(t, ctx, keyIDs, schematype.KeyshareStatusPending)
}

func createTestConfig(t *testing.T, numOperators int) *so.Config {
	t.Helper()

	operators := sparktesting.GetAllSigningOperators(t)

	// Take only the requested number of operators
	trimmedOperators := make(map[string]*so.SigningOperator)
	count := 0
	for id, op := range operators {
		if count >= numOperators {
			break
		}
		trimmedOperators[id] = op
		count++
	}

	// Get the first operator's config and private key
	var firstOp *so.SigningOperator
	for _, op := range trimmedOperators {
		firstOp = op
		break
	}

	// Use a test private key
	privKey := keys.GeneratePrivateKey()

	return &so.Config{
		Index:              firstOp.ID,
		Identifier:         firstOp.Identifier,
		IdentityPrivateKey: privKey,
		SigningOperatorMap: trimmedOperators,
		Threshold:          uint64(len(trimmedOperators)*2/3 + 1),
		DKGConfig:          so.DkgConfig{},
	}
}

func createPendingKeyshares(t *testing.T, ctx context.Context, client *ent.Client, coordinatorIndex uint64, count int) []uuid.UUID {
	t.Helper()

	keyIDs := make([]uuid.UUID, count)
	for i := 0; i < count; i++ {
		id := uuid.New()
		keyIDs[i] = id

		privKey := keys.GeneratePrivateKey()
		pubKey := privKey.Public()
		publicShares := make(map[string]keys.Public)
		publicShares["op1"] = pubKey

		_, err := client.SigningKeyshare.Create().
			SetID(id).
			SetCoordinatorIndex(coordinatorIndex).
			SetStatus(schematype.KeyshareStatusPending).
			SetSecretShare(privKey).
			SetPublicShares(publicShares).
			SetPublicKey(pubKey).
			SetMinSigners(2).
			Save(ctx)
		require.NoError(t, err)
	}

	return keyIDs
}

func verifyKeysharesStatus(t *testing.T, ctx context.Context, keyIDs []uuid.UUID, expectedStatus schematype.SigningKeyshareStatus) {
	t.Helper()

	db, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)

	keyshares, err := db.SigningKeyshare.Query().
		Where(signingkeyshare.IDIn(keyIDs...)).
		All(ctx)
	require.NoError(t, err)
	require.Len(t, keyshares, len(keyIDs))

	for _, ks := range keyshares {
		assert.Equal(t, expectedStatus, ks.Status, "keyshare %s has wrong status", ks.ID)
	}
}

type mockServerWrapper struct {
	mock     *mockDKGServiceServer
	server   *grpc.Server
	listener net.Listener
	address  string
}

func setupMockDKGServers(t *testing.T, keyIDs []uuid.UUID, unavailableDelay time.Duration) []*mockServerWrapper {
	t.Helper()

	// Set up 2 mock servers (simulating other operators, excluding self)
	servers := make([]*mockServerWrapper, 2)
	for i := 0; i < 2; i++ {
		// Create available keys map
		availableKeys := make(map[string]bool)
		for _, id := range keyIDs {
			availableKeys[id.String()] = true
		}

		mock := &mockDKGServiceServer{
			availableKeys:    availableKeys,
			unavailableDelay: unavailableDelay,
			startTime:        time.Now(),
		}

		// Create gRPC server
		listener, err := net.Listen("tcp", "localhost:0")
		require.NoError(t, err)

		server := grpc.NewServer()
		pbdkg.RegisterDKGServiceServer(server, mock)

		servers[i] = &mockServerWrapper{
			mock:     mock,
			server:   server,
			listener: listener,
			address:  listener.Addr().String(),
		}

		// Start server in background
		go func(srv *grpc.Server, l net.Listener) {
			_ = srv.Serve(l)
		}(server, listener)
	}

	return servers
}

func cleanupMockServers(servers []*mockServerWrapper) {
	for _, srv := range servers {
		srv.server.GracefulStop()
		_ = srv.listener.Close()
	}
}

func updateConfigWithMockConnections(config *so.Config, mockServers []*mockServerWrapper) {
	// Update all non-self operators to use the mock connection factory
	i := 0
	for _, op := range config.SigningOperatorMap {
		if op.ID != config.Index {
			if i < len(mockServers) {
				op.OperatorConnectionFactory = &mockOperatorConnectionFactory{
					address: mockServers[i].address,
				}
				i++
			}
		}
	}
}

type mockOperatorConnectionFactory struct {
	address string
}

func (f *mockOperatorConnectionFactory) NewGRPCConnection(address string, retryPolicy *common.RetryPolicyConfig, timeoutConfig *common.ClientTimeoutConfig) (*grpc.ClientConn, error) {
	// Ignore the address parameter and connect to our mock
	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return grpc.DialContext(ctx, f.address, opts...)
}
