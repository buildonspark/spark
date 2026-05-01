package grpc

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/entephemeral"
	entephemeraltest "github.com/lightsparkdev/spark/so/entephemeral/enttest"
	"github.com/lightsparkdev/spark/so/entephemeral/signingkeysharesecret"
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

// countingEphemeralFactory is a test-only EphemeralSessionFactory that counts
// NewSession/NewReadOnlySession invocations. It intentionally returns nil sessions,
// so tests using it must not attempt to call any methods on the returned session value.
type countingEphemeralFactory struct {
	newSessionCount         int
	newReadOnlySessionCount int
}

func (f *countingEphemeralFactory) NewSession(context.Context, ...db.SessionOption) entephemeral.Session {
	f.newSessionCount++
	return nil
}

func (f *countingEphemeralFactory) NewReadOnlySession(context.Context, ...db.SessionOption) entephemeral.Session {
	f.newReadOnlySessionCount++
	return nil
}

func TestDatabaseSessionMiddleware_EphemeralCommitFailureSkipsMainCommit(t *testing.T) {
	mainClient := db.NewTestSQLiteClient(t)
	defer mainClient.Close()

	ephemeralClient := entephemeraltest.Open(t, "sqlite3", fmt.Sprintf("file:%s?mode=memory&_fk=1", t.Name()))
	defer func() {
		require.NoError(t, ephemeralClient.Close())
	}()

	interceptor := DatabaseSessionMiddleware(
		mainClient,
		db.NewDefaultSessionFactory(mainClient),
		db.NewDefaultEphemeralSessionFactory(ephemeralClient),
		nil,
	)

	mainCommitCalled := make(chan struct{}, 1)
	ephemeralCommitErr := errors.New("ephemeral commit failed")

	handler := func(ctx context.Context, _ any) (any, error) {
		mainTx, err := ent.GetTxFromContext(ctx)
		require.NoError(t, err)

		ephemeralTx, err := entephemeral.GetTxFromContext(ctx)
		require.NoError(t, err)

		mainTx.OnCommit(func(fn ent.Committer) ent.Committer {
			return ent.CommitFunc(func(ctx context.Context, tx *ent.Tx) error {
				mainCommitCalled <- struct{}{}
				return fn.Commit(ctx, tx)
			})
		})

		ephemeralTx.OnCommit(func(fn entephemeral.Committer) entephemeral.Committer {
			return entephemeral.CommitFunc(func(context.Context, *entephemeral.Tx) error {
				return ephemeralCommitErr
			})
		})

		return "ok", nil
	}

	_, err := interceptor(t.Context(), nil, &grpc.UnaryServerInfo{FullMethod: "/spark.Operator/UnitTest"}, handler)
	require.Error(t, err)
	require.ErrorContains(t, err, "failed to commit ephemeral transaction")
	require.ErrorContains(t, err, ephemeralCommitErr.Error())

	select {
	case <-mainCommitCalled:
		t.Fatal("main transaction commit should not be attempted after ephemeral commit failure")
	default:
	}
}

func TestDatabaseSessionMiddleware_CommitsEphemeralBeforeMain(t *testing.T) {
	mainClient := db.NewTestSQLiteClient(t)
	defer mainClient.Close()

	ephemeralClient := entephemeraltest.Open(t, "sqlite3", fmt.Sprintf("file:%s?mode=memory&_fk=1", t.Name()))
	defer func() {
		require.NoError(t, ephemeralClient.Close())
	}()

	interceptor := DatabaseSessionMiddleware(
		mainClient,
		db.NewDefaultSessionFactory(mainClient),
		db.NewDefaultEphemeralSessionFactory(ephemeralClient),
		nil,
	)

	mainCommitErr := errors.New("main commit failed")
	commitOrder := make([]string, 0, 2)
	var mu sync.Mutex

	handler := func(ctx context.Context, _ any) (any, error) {
		mainTx, err := ent.GetTxFromContext(ctx)
		require.NoError(t, err)

		ephemeralTx, err := entephemeral.GetTxFromContext(ctx)
		require.NoError(t, err)

		ephemeralTx.OnCommit(func(fn entephemeral.Committer) entephemeral.Committer {
			return entephemeral.CommitFunc(func(ctx context.Context, tx *entephemeral.Tx) error {
				mu.Lock()
				commitOrder = append(commitOrder, "ephemeral")
				mu.Unlock()
				return fn.Commit(ctx, tx)
			})
		})

		mainTx.OnCommit(func(fn ent.Committer) ent.Committer {
			return ent.CommitFunc(func(context.Context, *ent.Tx) error {
				mu.Lock()
				commitOrder = append(commitOrder, "main")
				mu.Unlock()
				return mainCommitErr
			})
		})

		return "ok", nil
	}

	_, err := interceptor(t.Context(), nil, &grpc.UnaryServerInfo{FullMethod: "/spark.Operator/UnitTest"}, handler)
	require.Error(t, err)
	require.ErrorContains(t, err, "failed to commit transaction")
	require.ErrorContains(t, err, mainCommitErr.Error())

	require.Equal(t, []string{"ephemeral", "main"}, commitOrder)
}

func TestDatabaseSessionMiddleware_DoesNotRollbackAfterSuccessfulCommit(t *testing.T) {
	mainClient := db.NewTestSQLiteClient(t)
	defer mainClient.Close()

	ephemeralClient := entephemeraltest.Open(t, "sqlite3", fmt.Sprintf("file:%s?mode=memory&_fk=1", t.Name()))
	defer func() {
		require.NoError(t, ephemeralClient.Close())
	}()

	interceptor := DatabaseSessionMiddleware(
		mainClient,
		db.NewDefaultSessionFactory(mainClient),
		db.NewDefaultEphemeralSessionFactory(ephemeralClient),
		nil,
	)

	mainCommitCalled := false
	mainRollbackCalled := false
	ephemeralCommitCalled := false
	ephemeralRollbackCalled := false

	handler := func(ctx context.Context, _ any) (any, error) {
		mainTx, err := ent.GetTxFromContext(ctx)
		require.NoError(t, err)

		ephemeralTx, err := entephemeral.GetTxFromContext(ctx)
		require.NoError(t, err)

		mainTx.OnCommit(func(fn ent.Committer) ent.Committer {
			return ent.CommitFunc(func(ctx context.Context, tx *ent.Tx) error {
				mainCommitCalled = true
				return fn.Commit(ctx, tx)
			})
		})
		mainTx.OnRollback(func(fn ent.Rollbacker) ent.Rollbacker {
			return ent.RollbackFunc(func(ctx context.Context, tx *ent.Tx) error {
				mainRollbackCalled = true
				return fn.Rollback(ctx, tx)
			})
		})

		ephemeralTx.OnCommit(func(fn entephemeral.Committer) entephemeral.Committer {
			return entephemeral.CommitFunc(func(ctx context.Context, tx *entephemeral.Tx) error {
				ephemeralCommitCalled = true
				return fn.Commit(ctx, tx)
			})
		})
		ephemeralTx.OnRollback(func(fn entephemeral.Rollbacker) entephemeral.Rollbacker {
			return entephemeral.RollbackFunc(func(ctx context.Context, tx *entephemeral.Tx) error {
				ephemeralRollbackCalled = true
				return fn.Rollback(ctx, tx)
			})
		})

		return "ok", nil
	}

	resp, err := interceptor(t.Context(), nil, &grpc.UnaryServerInfo{FullMethod: "/spark.Operator/UnitTest"}, handler)
	require.NoError(t, err)
	require.Equal(t, "ok", resp)
	require.True(t, ephemeralCommitCalled)
	require.True(t, mainCommitCalled)
	require.False(t, ephemeralRollbackCalled)
	require.False(t, mainRollbackCalled)
}

func TestDatabaseSessionMiddleware_PreservesEphemeralSigningKeyshareSecretAfterMainCommit(t *testing.T) {
	mainClient := db.NewTestSQLiteClient(t)
	defer mainClient.Close()

	ephemeralClient := entephemeraltest.Open(t, "sqlite3", fmt.Sprintf("file:%s?mode=memory&_fk=1", t.Name()))
	defer func() {
		require.NoError(t, ephemeralClient.Close())
	}()

	interceptor := DatabaseSessionMiddleware(
		mainClient,
		db.NewDefaultSessionFactory(mainClient),
		db.NewDefaultEphemeralSessionFactory(ephemeralClient),
		nil,
	)

	keyID := uuid.New()
	secret := keys.MustParsePrivateKeyHex("7cfb5322f5ba892194f59fd868ab89c7ea3d5f9531d3460f79dd0f46efefcd8f")
	publicKeySource := keys.MustParsePrivateKeyHex("bc605b157cf626f43108cce5fcd6ea7feb7138319d427f6015f4cb8918ea4a22")

	ctx := knobs.InjectKnobsService(t.Context(), knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobSoSigningKeyshareDualWriteSecret: 100,
	}))
	handler := func(ctx context.Context, _ any) (any, error) {
		mainDB, err := ent.GetDbFromContext(ctx)
		if err != nil {
			return nil, err
		}
		create := mainDB.SigningKeyshare.Create().
			SetID(keyID).
			SetStatus(st.KeyshareStatusAvailable).
			SetPublicShares(map[string]keys.Public{"1": publicKeySource.Public()}).
			SetPublicKey(publicKeySource.Public()).
			SetMinSigners(1).
			SetCoordinatorIndex(0)

		create, err = ent.PrepareSigningKeyshareCreateWithSecret(ctx, create, keyID, secret)
		if err != nil {
			return nil, err
		}

		return create.Save(ctx)
	}

	resp, err := interceptor(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/spark.Operator/UnitTest"}, handler)
	require.NoError(t, err)
	created := resp.(*ent.SigningKeyshare)
	require.NotNil(t, created.SecretVersion)
	require.Equal(t, int32(0), *created.SecretVersion)

	persistedSecret, err := ephemeralClient.SigningKeyshareSecret.Query().
		Where(
			signingkeysharesecret.SigningKeyshareIDEQ(keyID),
			signingkeysharesecret.VersionEQ(*created.SecretVersion),
		).
		Only(t.Context())
	require.NoError(t, err)
	require.True(t, persistedSecret.SecretShare.Equals(secret))
}

func TestDatabaseSessionMiddleware_ReadOnlyPathUsesEphemeralFactory(t *testing.T) {
	mainClient := db.NewTestSQLiteClient(t)
	defer mainClient.Close()

	factory := &countingEphemeralFactory{}
	interceptor := DatabaseSessionMiddleware(
		mainClient,
		db.NewDefaultSessionFactory(mainClient),
		factory,
		nil,
	)

	ctx := knobs.InjectKnobsService(t.Context(), knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobReadOnlyEndpoints + "@/spark.Operator/UnitTest": 100,
	}))
	handler := func(context.Context, any) (any, error) {
		return "ok", nil
	}

	_, err := interceptor(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/spark.Operator/UnitTest"}, handler)
	require.NoError(t, err)
	require.Equal(t, 0, factory.newSessionCount)
	require.Equal(t, 1, factory.newReadOnlySessionCount)
}
