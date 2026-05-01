package entephemeral

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	_ "github.com/lightsparkdev/spark/so/entephemeral/runtime"
	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/require"
)

const ephemeralSQLitePath = "file:%s?mode=memory&_fk=1"

type testSession struct {
	provider TxProvider
	tx       *Tx
}

func newTestSession(client *Client) *testSession {
	return &testSession{
		provider: NewEntClientTxProvider(client),
	}
}

func (s *testSession) GetOrBeginTx(ctx context.Context) (*Tx, error) {
	if s.tx != nil {
		return s.tx, nil
	}

	tx, err := s.provider.GetOrBeginTx(ctx)
	if err != nil {
		return nil, err
	}
	s.tx = tx
	return tx, nil
}

func (s *testSession) GetClient(ctx context.Context) (*Client, error) {
	tx, err := s.GetOrBeginTx(ctx)
	if err != nil {
		return nil, err
	}
	return tx.Client(), nil
}

func (s *testSession) GetTxIfExists() *Tx {
	return s.tx
}

func (s *testSession) CommitError() error {
	return nil
}

func (s *testSession) TxWasStarted() bool {
	return s.tx != nil
}

func newSQLiteContextWithSession(t *testing.T) (context.Context, *Client, *testSession) {
	t.Helper()

	dbName := strings.ReplaceAll(fmt.Sprintf("entephemeral_%s", t.Name()), "/", "_")
	client, err := Open("sqlite3", fmt.Sprintf(ephemeralSQLitePath, dbName))
	require.NoError(t, err)
	require.NoError(t, client.Schema.Create(t.Context()))

	session := newTestSession(client)
	ctx := Inject(t.Context(), session)

	t.Cleanup(func() {
		if tx := session.GetTxIfExists(); tx != nil {
			_ = tx.Rollback()
		}
		require.NoError(t, client.Close())
	})

	return ctx, client, session
}

func TestSigningKeyshareSecretVersion_ContextRequired(t *testing.T) {
	signingKeyshareID := uuid.New()
	secretShare := keys.GeneratePrivateKey()

	_, err := GetSigningKeyshareSecretVersion(t.Context(), signingKeyshareID, 0)
	require.ErrorContains(t, err, "no transaction provider found in context")

	_, err = GetLatestSigningKeyshareSecretVersionForUpdate(t.Context(), signingKeyshareID)
	require.ErrorContains(t, err, "no transaction provider found in context")

	_, err = AddSigningKeyshareSecretVersion(t.Context(), signingKeyshareID, secretShare)
	require.ErrorContains(t, err, "no transaction provider found in context")

	_, err = CreateSigningKeyshareSecretVersion(t.Context(), signingKeyshareID, 0, secretShare)
	require.ErrorContains(t, err, "no transaction provider found in context")

	err = DeleteSigningKeyshareSecretVersion(t.Context(), signingKeyshareID, 0)
	require.ErrorContains(t, err, "no transaction provider found in context")
}

func TestGetSigningKeyshareSecretVersion_ReturnsErrNoSecretVersionWhenMissing(t *testing.T) {
	ctx, _, _ := newSQLiteContextWithSession(t)

	_, err := GetSigningKeyshareSecretVersion(ctx, uuid.New(), 0)
	require.ErrorIs(t, err, ErrNoSecretVersion)
}

func TestDeleteSigningKeyshareSecretVersion_ReturnsErrNoSecretVersionWhenMissing(t *testing.T) {
	ctx, _, _ := newSQLiteContextWithSession(t)

	err := DeleteSigningKeyshareSecretVersion(ctx, uuid.New(), 7)
	require.ErrorIs(t, err, ErrNoSecretVersion)
}

func TestDeleteSigningKeyshareSecretVersion_DeletesExistingVersion(t *testing.T) {
	ctx, _, _ := newSQLiteContextWithSession(t)
	signingKeyshareID := uuid.New()

	tx, err := GetTxFromContext(ctx)
	require.NoError(t, err)

	_, err = createSigningKeyshareSecretVersionLocked(ctx, tx, signingKeyshareID, 2, keys.GeneratePrivateKey())
	require.NoError(t, err)

	err = DeleteSigningKeyshareSecretVersion(ctx, signingKeyshareID, 2)
	require.NoError(t, err)

	_, err = GetSigningKeyshareSecretVersion(ctx, signingKeyshareID, 2)
	require.ErrorIs(t, err, ErrNoSecretVersion)
}

func TestCreateSigningKeyshareSecretVersionLocked_DuplicateVersionFails(t *testing.T) {
	ctx, _, _ := newSQLiteContextWithSession(t)
	signingKeyshareID := uuid.New()

	tx, err := GetTxFromContext(ctx)
	require.NoError(t, err)

	_, err = createSigningKeyshareSecretVersionLocked(ctx, tx, signingKeyshareID, 3, keys.GeneratePrivateKey())
	require.NoError(t, err)

	_, err = createSigningKeyshareSecretVersionLocked(ctx, tx, signingKeyshareID, 3, keys.GeneratePrivateKey())
	require.Error(t, err)
	require.True(t, IsConstraintError(err), "expected a constraint error on duplicate version insert, got: %v", err)
}

func TestCreateSigningKeyshareSecretVersionLocked_GetReturnsInsertedValue(t *testing.T) {
	ctx, _, _ := newSQLiteContextWithSession(t)
	signingKeyshareID := uuid.New()

	tx, err := GetTxFromContext(ctx)
	require.NoError(t, err)

	secretShare := keys.GeneratePrivateKey()
	created, err := createSigningKeyshareSecretVersionLocked(ctx, tx, signingKeyshareID, 9, secretShare)
	require.NoError(t, err)
	require.Equal(t, int32(9), created.Version)

	got, err := GetSigningKeyshareSecretVersion(ctx, signingKeyshareID, 9)
	require.NoError(t, err)
	require.True(t, got.SecretShare.Equals(secretShare), "retrieved secret share should match the one that was inserted")
}

func TestCreateSigningKeyshareSecretVersion_DuplicateVersionFails(t *testing.T) {
	ctx, _, _ := newSQLiteContextWithSession(t)
	signingKeyshareID := uuid.New()

	_, err := CreateSigningKeyshareSecretVersion(ctx, signingKeyshareID, 0, keys.GeneratePrivateKey())
	require.NoError(t, err)

	_, err = CreateSigningKeyshareSecretVersion(ctx, signingKeyshareID, 0, keys.GeneratePrivateKey())
	require.Error(t, err)
	require.True(t, IsConstraintError(err))
}

func TestSigningKeyshareSecretVersionMutationMethods_WorkOnSQLite(t *testing.T) {
	ctx, _, _ := newSQLiteContextWithSession(t)
	signingKeyshareID := uuid.New()
	secretShare := keys.GeneratePrivateKey()

	latest, err := GetLatestSigningKeyshareSecretVersionForUpdate(ctx, signingKeyshareID)
	require.NoError(t, err)
	require.Nil(t, latest)

	created, err := AddSigningKeyshareSecretVersion(ctx, signingKeyshareID, secretShare)
	require.NoError(t, err)
	require.Equal(t, int32(0), created.Version)
	require.True(t, created.SecretShare.Equals(secretShare))

	otherSecret := keys.GeneratePrivateKey()
	created, err = CreateSigningKeyshareSecretVersion(ctx, signingKeyshareID, 1, otherSecret)
	require.NoError(t, err)
	require.Equal(t, int32(1), created.Version)
	require.True(t, created.SecretShare.Equals(otherSecret))
}

func TestSigningKeyshareIDToAdvisoryLockKey_IsDeterministic(t *testing.T) {
	id := uuid.MustParse("00112233-4455-6677-8899-aabbccddeeff")
	hi1, lo1 := signingKeyshareIDToAdvisoryLockKey(id)
	hi2, lo2 := signingKeyshareIDToAdvisoryLockKey(id)

	require.Equal(t, hi1, hi2)
	require.Equal(t, lo1, lo2)

	otherID := uuid.MustParse("00112233-4455-6677-8899-aabbccddeefe")
	otherHi, otherLo := signingKeyshareIDToAdvisoryLockKey(otherID)
	require.NotEqual(t, [2]int32{hi1, lo1}, [2]int32{otherHi, otherLo})
}

func TestSigningKeyshareIDToAdvisoryLockKey_AvoidsXORFoldCollisionCase(t *testing.T) {
	idA := uuid.MustParse("00000000-0000-0000-0000-000000000000")
	idB := uuid.MustParse("12345678-9abc-def0-1234-56789abcdef0")

	hiA, loA := signingKeyshareIDToAdvisoryLockKey(idA)
	hiB, loB := signingKeyshareIDToAdvisoryLockKey(idB)

	require.NotEqual(t, [2]int32{hiA, loA}, [2]int32{hiB, loB})
}

func TestNextVersion(t *testing.T) {
	tests := []struct {
		name        string
		latest      *SigningKeyshareSecret
		wantVersion int32
		wantErr     bool
	}{
		{
			name:        "nil returns 0",
			latest:      nil,
			wantVersion: 0,
		},
		{
			name:        "increments from 0",
			latest:      &SigningKeyshareSecret{Version: 0},
			wantVersion: 1,
		},
		{
			name:        "increments from non-zero",
			latest:      &SigningKeyshareSecret{Version: 5},
			wantVersion: 6,
		},
		{
			name:    "overflow returns error",
			latest:  &SigningKeyshareSecret{Version: math.MaxInt32},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := nextVersion(tt.latest)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.wantVersion, got)
			}
		})
	}
}
