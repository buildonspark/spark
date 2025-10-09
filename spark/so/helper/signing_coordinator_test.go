package helper_test

import (
	"bytes"
	"context"
	"errors"
	"math"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/lightsparkdev/spark/common/keys"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pbfrost "github.com/lightsparkdev/spark/proto/frost"
	pbspark "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/helper"
	"github.com/lightsparkdev/spark/so/objects"
	sparktesting "github.com/lightsparkdev/spark/testing"
)

var pubKey = keys.MustGeneratePrivateKeyFromRand(rand.NewChaCha8([32]byte{})).Public()

func mockTxBuf(t *testing.T, values []int64) []byte {
	// A minimal valid Bitcoin transaction
	tx := wire.NewMsgTx(2)
	tx.AddTxIn(&wire.TxIn{})
	for _, v := range values {
		tx.AddTxOut(&wire.TxOut{Value: v, PkScript: []byte("test-pkscript")})
	}
	buf := new(bytes.Buffer)
	require.NoError(t, tx.Serialize(buf))
	return buf.Bytes()
}

func runWithRawTx(keysharePub keys.Public, protoPub keys.Public, rawTx []byte, commitment *pbcommon.SigningCommitment, prevOutputValue int64) (*helper.SigningJob, *wire.MsgTx, error) {
	keyshare := &ent.SigningKeyshare{
		ID:        uuid.New(),
		PublicKey: keysharePub,
	}
	proto := &pbspark.SigningJob{
		SigningPublicKey:       protoPub.Serialize(),
		RawTx:                  rawTx,
		SigningNonceCommitment: commitment,
	}

	prevOutput := &wire.TxOut{
		Value:    prevOutputValue,
		PkScript: []byte("test-pkscript"),
	}

	return helper.NewSigningJob(keyshare, proto, prevOutput)
}

func runWithValues(t *testing.T, prevOutputValue int64, values []int64) (*helper.SigningJob, *wire.MsgTx, error) {
	rawTx := mockTxBuf(t, values)
	commitment := &pbcommon.SigningCommitment{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()}
	return runWithRawTx(pubKey, pubKey, rawTx, commitment, prevOutputValue)
}

func TestNewSigningJob(t *testing.T) {
	tests := []struct {
		name            string
		prevOutputValue int64
		values          []int64
	}{
		{"leaf equal to prev output", 1000000, []int64{1000000}},
		{"sum of leaves equal to prev output", 1000000, []int64{500000, 500000}},
		// TODO: Is it actually correct that they should be able to sum up to a
		// value less than the previous output?
		{"3 leaves less than prev output", 1000000, []int64{200000, 300000, 400000}},
		{"no leaves", 1000000, []int64{}},
		{"1000 leaves", 1000000, slices.Repeat([]int64{1}, 1000)},
		{"max int64 leaf", math.MaxInt64, []int64{math.MaxInt64}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := runWithValues(t, test.prevOutputValue, test.values)
			require.NoError(t, err)
		})
	}
}

func TestNewSigningJob_BadLeafValues(t *testing.T) {
	tests := []struct {
		name            string
		prevOutputValue int64
		values          []int64
		expectedError   error
	}{
		{"negative leaf", 1000000, []int64{-1000000}, helper.ErrNegativeOutputValue},
		{"negative leaf in sum", 1000000, []int64{500000, -1000000}, helper.ErrNegativeOutputValue},
		{"sum greater than prev output", 1000000, []int64{1000001}, helper.ErrTotalOutputValueGreaterThanPrevOutputValue},
		{"sum greater than prev output in sum", 1000000, []int64{500000, 500001}, helper.ErrTotalOutputValueGreaterThanPrevOutputValue},
		{"sum greater than max int64", 1000000, []int64{1, math.MaxInt64}, helper.ErrTotalOutputValueGreaterThanMaxInt64},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := runWithValues(t, test.prevOutputValue, test.values)
			require.ErrorIs(t, err, test.expectedError)
		})
	}
}

func TestNewSigningJob_InvalidInputs(t *testing.T) {
	tx := wire.NewMsgTx(2)
	tx.AddTxIn(&wire.TxIn{})
	tx.AddTxOut(&wire.TxOut{Value: 1000000, PkScript: []byte("test-pkscript")})
	buf := new(bytes.Buffer)
	require.NoError(t, tx.Serialize(buf))
	goodTx := buf.Bytes()

	tests := []struct {
		name          string
		keyshare      keys.Public
		protoPub      keys.Public
		rawTx         []byte
		commit        *pbcommon.SigningCommitment
		expectedError string
	}{
		{
			name:          "malformed raw tx",
			keyshare:      pubKey,
			protoPub:      pubKey,
			rawTx:         []byte{0x00, 0x01, 0x02},
			commit:        &pbcommon.SigningCommitment{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
			expectedError: "unexpected EOF",
		},
		{
			name:          "malformed commitment",
			keyshare:      pubKey,
			protoPub:      pubKey,
			rawTx:         goodTx,
			commit:        &pbcommon.SigningCommitment{Binding: pubKey.Serialize()[:30], Hiding: pubKey.Serialize()},
			expectedError: "invalid nonce commitment length",
		},
		{
			name:          "nil commitment",
			keyshare:      pubKey,
			protoPub:      pubKey,
			rawTx:         goodTx,
			commit:        nil,
			expectedError: "nil proto",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := runWithRawTx(tc.keyshare, tc.protoPub, tc.rawTx, tc.commit, 1000000)
			require.ErrorContains(t, err, tc.expectedError)
		})
	}
}

type mockSparkServiceFrostSignerFactory struct {
	conn *mockSparkServiceFrostSigner
}

func (m *mockSparkServiceFrostSignerFactory) NewFrostSigner(_ *so.Config) (helper.SparkServiceFrostSigner, error) {
	return m.conn, nil
}

func (m *mockSparkServiceFrostSignerFactory) IsMock() bool {
	return true
}

type mockSparkServiceFrostSigner struct {
	frostRound1Response *pbinternal.FrostRound1Response
	frostRound2Response *pbinternal.FrostRound2Response
	frostRound1Error    error
	frostRound2Error    error
}

func (m *mockSparkServiceFrostSigner) CallFrostRound1(_ context.Context, _ *so.SigningOperator, _ *pbinternal.FrostRound1Request) (*pbinternal.FrostRound1Response, error) {
	return m.frostRound1Response, m.frostRound1Error
}

func (m *mockSparkServiceFrostSigner) CallFrostRound2(_ context.Context, _ *so.SigningOperator, _ *pbinternal.FrostRound2Request) (*pbinternal.FrostRound2Response, error) {
	return m.frostRound2Response, m.frostRound2Error
}

func TestSignFrostInternal(t *testing.T) {
	t.Run("SignFrost", func(t *testing.T) {
		config := sparktesting.TestConfig(t)

		// Add a mock operator to the config with identifier "operator1"
		if config.SigningOperatorMap == nil {
			config.SigningOperatorMap = make(map[string]*so.SigningOperator)
		}
		config.SigningOperatorMap["operator1"] = &so.SigningOperator{Identifier: "operator1"}

		keyshareID := uuid.New()
		job := &helper.SigningJob{
			JobID:             "test-job-id",
			SigningKeyshareID: keyshareID,
			Message:           []byte("test message to sign"),
			VerifyingKey:      &pubKey,
			UserCommitment:    &objects.SigningCommitment{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
			AdaptorPublicKey:  nil,
		}

		frostSigner := &mockSparkServiceFrostSigner{
			frostRound1Response: &pbinternal.FrostRound1Response{
				SigningCommitments: []*pbcommon.SigningCommitment{
					{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
				},
			},
			frostRound2Response: &pbinternal.FrostRound2Response{
				Results: map[string]*pbcommon.SigningResult{
					"test-job-id": {
						SignatureShare: []byte("test-signature-share"),
					},
				},
			},
		}

		frostSignerFactory := &mockSparkServiceFrostSignerFactory{
			conn: frostSigner,
		}

		results, err := helper.SignFrostInternal(t.Context(), config, []*helper.SigningJob{job}, mockGetKeyPackages, frostSignerFactory)
		require.NoError(t, err)
		require.Len(t, results, 1)

		result := results[0]
		assert.Equal(t, job.JobID, result.JobID)
		assert.NotEmpty(t, result.SignatureShares)
		assert.NotEmpty(t, result.SigningCommitments)
		assert.NotEmpty(t, result.PublicKeys)
		assert.EqualValues(t, 1, result.KeyshareThreshold)
	})

	t.Run("ErrorCases", func(t *testing.T) {
		config := sparktesting.TestConfig(t)

		t.Run("EmptyJobsList", func(t *testing.T) {
			getKeyPackages := func(_ context.Context, _ *so.Config, _ []uuid.UUID) (map[uuid.UUID]*pbfrost.KeyPackage, error) {
				return make(map[uuid.UUID]*pbfrost.KeyPackage), nil
			}
			frostSignerFactory := &mockSparkServiceFrostSignerFactory{
				conn: &mockSparkServiceFrostSigner{
					frostRound1Response: &pbinternal.FrostRound1Response{},
					frostRound2Response: &pbinternal.FrostRound2Response{},
				},
			}

			// TODO Should we actually be successful on a frost sign with an empty jobs list?
			results, err := helper.SignFrostInternal(t.Context(), config, []*helper.SigningJob{}, getKeyPackages, frostSignerFactory)
			require.NoError(t, err)
			assert.Empty(t, results)
		})

		t.Run("GetKeyPackagesError", func(t *testing.T) {
			// Add a mock operator to the config with identifier "operator1"
			if config.SigningOperatorMap == nil {
				config.SigningOperatorMap = make(map[string]*so.SigningOperator)
			}
			config.SigningOperatorMap["operator1"] = &so.SigningOperator{Identifier: "operator1"}

			failedGetKeyPackages := func(_ context.Context, _ *so.Config, _ []uuid.UUID) (map[uuid.UUID]*pbfrost.KeyPackage, error) {
				return nil, errors.New("database connection failed")
			}

			frostSignerFactory := &mockSparkServiceFrostSignerFactory{
				conn: &mockSparkServiceFrostSigner{},
			}

			job := &helper.SigningJob{
				JobID:             "test-job-id",
				SigningKeyshareID: uuid.New(),
				Message:           []byte("test message"),
				VerifyingKey:      &pubKey,
				UserCommitment:    &objects.SigningCommitment{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
			}

			_, err := helper.SignFrostInternal(t.Context(), config, []*helper.SigningJob{job}, failedGetKeyPackages, frostSignerFactory)
			require.ErrorContains(t, err, "database connection failed")
		})

		// Test with missing keyshare in getKeyPackages response
		t.Run("MissingKeyshare", func(t *testing.T) {
			keyshareID := uuid.New()
			mockGetKeyPackages := func(_ context.Context, _ *so.Config, _ []uuid.UUID) (map[uuid.UUID]*pbfrost.KeyPackage, error) {
				return make(map[uuid.UUID]*pbfrost.KeyPackage), nil // Return empty map, missing the requested keyshare
			}

			frostSignerFactory := &mockSparkServiceFrostSignerFactory{
				conn: &mockSparkServiceFrostSigner{},
			}

			job := &helper.SigningJob{
				JobID:             "test-job-id",
				SigningKeyshareID: keyshareID,
				Message:           []byte("test message"),
				VerifyingKey:      &pubKey,
				UserCommitment:    &objects.SigningCommitment{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
			}

			_, err := helper.SignFrostInternal(t.Context(), config, []*helper.SigningJob{job}, mockGetKeyPackages, frostSignerFactory)
			require.Error(t, err)
		})

		// Test with frostRound1 error
		t.Run("FrostRound1Error", func(t *testing.T) {
			keyshareID := uuid.New()
			frostSignerFactory := &mockSparkServiceFrostSignerFactory{
				conn: &mockSparkServiceFrostSigner{
					frostRound1Error: errors.New("frost round 1 failed"),
				},
			}

			job := &helper.SigningJob{
				JobID:             "test-job-id",
				SigningKeyshareID: keyshareID,
				Message:           []byte("test message"),
				VerifyingKey:      &pubKey,
				UserCommitment:    &objects.SigningCommitment{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
			}

			_, err := helper.SignFrostInternal(t.Context(), config, []*helper.SigningJob{job}, mockGetKeyPackages, frostSignerFactory)
			require.ErrorContains(t, err, "frost round 1 failed")
		})

		// Test with frostRound2 error
		t.Run("FrostRound2Error", func(t *testing.T) {
			keyshareID := uuid.New()
			frostSignerFactory := &mockSparkServiceFrostSignerFactory{
				conn: &mockSparkServiceFrostSigner{
					frostRound1Response: &pbinternal.FrostRound1Response{
						SigningCommitments: []*pbcommon.SigningCommitment{
							{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
						},
					},
					frostRound2Error: errors.New("frost round 2 failed"),
				},
			}

			if config.SigningOperatorMap == nil {
				config.SigningOperatorMap = make(map[string]*so.SigningOperator)
			}
			config.SigningOperatorMap["operator1"] = &so.SigningOperator{Identifier: "operator1"}

			job := &helper.SigningJob{
				JobID:             "test-job-id",
				SigningKeyshareID: keyshareID,
				Message:           []byte("test message"),
				VerifyingKey:      &pubKey,
				UserCommitment:    &objects.SigningCommitment{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
			}

			_, err := helper.SignFrostInternal(t.Context(), config, []*helper.SigningJob{job}, mockGetKeyPackages, frostSignerFactory)
			require.ErrorContains(t, err, "frost round 2 failed")
		})
	})

	t.Run("EdgeCases", func(t *testing.T) {
		config := sparktesting.TestConfig(t)

		t.Run("MultipleJobs", func(t *testing.T) {
			keyshareID1 := uuid.New()
			keyshareID2 := uuid.New()

			mockGetKeyPackages := func(_ context.Context, _ *so.Config, keyshareIDs []uuid.UUID) (map[uuid.UUID]*pbfrost.KeyPackage, error) {
				result := make(map[uuid.UUID]*pbfrost.KeyPackage)
				for _, id := range keyshareIDs {
					result[id] = &pbfrost.KeyPackage{
						Identifier:  "test-identifier",
						SecretShare: []byte("test-secret-share-32-bytes-long"),
						PublicShares: map[string][]byte{
							"test-identifier": pubKey.Serialize(),
						},
						PublicKey:  pubKey.Serialize(),
						MinSigners: 1,
					}
				}
				return result, nil
			}

			frostSigner := &mockSparkServiceFrostSigner{
				frostRound1Response: &pbinternal.FrostRound1Response{
					SigningCommitments: []*pbcommon.SigningCommitment{
						{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
						{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
					},
				},
				frostRound2Response: &pbinternal.FrostRound2Response{
					Results: map[string]*pbcommon.SigningResult{
						"job-1": {
							SignatureShare: []byte("signature-share-1"),
						},
						"job-2": {
							SignatureShare: []byte("signature-share-2"),
						},
					},
				},
			}

			frostSignerFactory := &mockSparkServiceFrostSignerFactory{
				conn: frostSigner,
			}

			jobs := []*helper.SigningJob{
				{
					JobID:             "job-1",
					SigningKeyshareID: keyshareID1,
					Message:           []byte("message 1"),
					VerifyingKey:      &pubKey,
					UserCommitment:    &objects.SigningCommitment{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
				},
				{
					JobID:             "job-2",
					SigningKeyshareID: keyshareID2,
					Message:           []byte("message 2"),
					VerifyingKey:      &pubKey,
					UserCommitment:    &objects.SigningCommitment{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
				},
			}

			results, err := helper.SignFrostInternal(t.Context(), config, jobs, mockGetKeyPackages, frostSignerFactory)
			require.NoError(t, err)
			require.Len(t, results, 2)

			assert.Equal(t, "job-1", results[0].JobID)
			assert.Equal(t, "job-2", results[1].JobID)
		})

		t.Run("NilUserCommitment", func(t *testing.T) {
			keyshareID := uuid.New()

			frostSigner := &mockSparkServiceFrostSigner{
				frostRound1Response: &pbinternal.FrostRound1Response{
					SigningCommitments: []*pbcommon.SigningCommitment{
						{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
					},
				},
				frostRound2Response: &pbinternal.FrostRound2Response{
					Results: map[string]*pbcommon.SigningResult{
						"test-job-id": {
							SignatureShare: []byte("test-signature-share"),
						},
					},
				},
			}

			frostSignerFactory := &mockSparkServiceFrostSignerFactory{conn: frostSigner}

			job := &helper.SigningJob{
				JobID:             "test-job-id",
				SigningKeyshareID: keyshareID,
				Message:           []byte("test message"),
				VerifyingKey:      &pubKey,
				UserCommitment:    nil, // Test with nil user commitment
				AdaptorPublicKey:  nil,
			}

			results, err := helper.SignFrostInternal(t.Context(), config, []*helper.SigningJob{job}, mockGetKeyPackages, frostSignerFactory)
			require.NoError(t, err)
			require.Len(t, results, 1)
		})

		t.Run("WithAdaptorPublicKey", func(t *testing.T) {
			keyshareID := uuid.New()
			frostSigner := &mockSparkServiceFrostSigner{
				frostRound1Response: &pbinternal.FrostRound1Response{
					SigningCommitments: []*pbcommon.SigningCommitment{
						{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
					},
				},
				frostRound2Response: &pbinternal.FrostRound2Response{
					Results: map[string]*pbcommon.SigningResult{
						"test-job-id": {
							SignatureShare: []byte("test-signature-share"),
						},
					},
				},
			}

			frostSignerFactory := &mockSparkServiceFrostSignerFactory{conn: frostSigner}

			job := &helper.SigningJob{
				JobID:             "test-job-id",
				SigningKeyshareID: keyshareID,
				Message:           []byte("test message"),
				VerifyingKey:      &pubKey,
				UserCommitment:    &objects.SigningCommitment{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
				AdaptorPublicKey:  &pubKey,
			}

			results, err := helper.SignFrostInternal(t.Context(), config, []*helper.SigningJob{job}, mockGetKeyPackages, frostSignerFactory)
			require.NoError(t, err)
			require.Len(t, results, 1)
		})

		t.Run("DifferentThresholds", func(t *testing.T) {
			keyshareID := uuid.New()
			differentThreshold := func(ctx context.Context, config *so.Config, keyshareIDs []uuid.UUID) (map[uuid.UUID]*pbfrost.KeyPackage, error) {
				result, _ := mockGetKeyPackages(ctx, config, keyshareIDs)
				for _, v := range result {
					v.MinSigners = 3 // Test with different threshold
				}
				return result, nil
			}

			frostSigner := &mockSparkServiceFrostSigner{
				frostRound1Response: &pbinternal.FrostRound1Response{
					SigningCommitments: []*pbcommon.SigningCommitment{
						{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
					},
				},
				frostRound2Response: &pbinternal.FrostRound2Response{
					Results: map[string]*pbcommon.SigningResult{
						"test-job-id": {
							SignatureShare: []byte("test-signature-share"),
						},
					},
				},
			}

			frostSignerFactory := &mockSparkServiceFrostSignerFactory{conn: frostSigner}

			job := &helper.SigningJob{
				JobID:             "test-job-id",
				SigningKeyshareID: keyshareID,
				Message:           []byte("test message"),
				VerifyingKey:      &pubKey,
				UserCommitment:    &objects.SigningCommitment{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
			}

			results, err := helper.SignFrostInternal(t.Context(), config, []*helper.SigningJob{job}, differentThreshold, frostSignerFactory)
			require.NoError(t, err)
			require.Len(t, results, 1)

			assert.EqualValues(t, 3, results[0].KeyshareThreshold)
		})
	})

	t.Run("SigningKeyshareIDsFromSigningJobs", func(t *testing.T) {
		keyshareID1 := uuid.New()
		keyshareID2 := uuid.New()

		jobs := []*helper.SigningJob{
			{SigningKeyshareID: keyshareID1},
			{SigningKeyshareID: keyshareID2},
		}

		ids := helper.SigningKeyshareIDsFromSigningJobs(jobs)
		require.Len(t, ids, 2)
		assert.Equal(t, keyshareID1, ids[0])
		assert.Equal(t, keyshareID2, ids[1])
	})

	t.Run("ContextCancellation", func(t *testing.T) {
		config := sparktesting.TestConfig(t)

		ctx, cancel := context.WithCancel(t.Context())
		cancel() // Cancel immediately

		keyshareID := uuid.New()
		frostSignerFactory := &mockSparkServiceFrostSignerFactory{
			conn: &mockSparkServiceFrostSigner{
				frostRound1Error: context.Canceled,
			},
		}

		job := &helper.SigningJob{
			JobID:             "test-job-id",
			SigningKeyshareID: keyshareID,
			Message:           []byte("test message"),
			VerifyingKey:      &pubKey,
			UserCommitment:    &objects.SigningCommitment{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
		}

		_, err := helper.SignFrostInternal(ctx, config, []*helper.SigningJob{job}, mockGetKeyPackages, frostSignerFactory)
		require.Error(t, err)
	})
}

func mockGetKeyPackages(_ context.Context, _ *so.Config, keyshareIDs []uuid.UUID) (map[uuid.UUID]*pbfrost.KeyPackage, error) {
	result := make(map[uuid.UUID]*pbfrost.KeyPackage)
	for _, id := range keyshareIDs {
		result[id] = &pbfrost.KeyPackage{
			Identifier:  "test-identifier",
			SecretShare: keys.GeneratePrivateKey().Serialize(),
			PublicShares: map[string][]byte{
				"test-identifier": pubKey.Serialize(),
			},
			PublicKey:  pubKey.Serialize(),
			MinSigners: 1,
		}
	}
	return result, nil
}

// Test SignFrostWithPregeneratedNonce tests the SignFrostWithPregeneratedNonce function
func TestSignFrostWithPregeneratedNonce(t *testing.T) {
	t.Run("BasicFunctionality", func(t *testing.T) {
		config := sparktesting.TestConfig(t)

		// Add mock operators to the config with identifiers "operator1" and "operator2"
		if config.SigningOperatorMap == nil {
			config.SigningOperatorMap = make(map[string]*so.SigningOperator)
		}
		config.SigningOperatorMap["operator1"] = &so.SigningOperator{Identifier: "operator1"}
		config.SigningOperatorMap["operator2"] = &so.SigningOperator{Identifier: "operator2"}

		keyshareID := uuid.New()
		job := &helper.SigningJobWithPregeneratedNonce{
			SigningJob: helper.SigningJob{
				JobID:             "test-job-id",
				SigningKeyshareID: keyshareID,
				Message:           []byte("test message"),
				VerifyingKey:      &pubKey,
				UserCommitment:    &objects.SigningCommitment{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
			},
			Round1Packages: map[string]objects.SigningCommitment{
				"operator1": {Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
				"operator2": {Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
			},
		}

		frostSigner := &mockSparkServiceFrostSigner{
			frostRound1Response: &pbinternal.FrostRound1Response{
				SigningCommitments: []*pbcommon.SigningCommitment{
					{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
				},
			},
			frostRound2Response: &pbinternal.FrostRound2Response{
				Results: map[string]*pbcommon.SigningResult{
					"test-job-id": {
						SignatureShare: []byte("test-signature-share"),
					},
				},
			},
		}

		frostSignerFactory := &mockSparkServiceFrostSignerFactory{conn: frostSigner}

		results, err := helper.SignFrostWithPregeneratedNonceInternal(t.Context(), config, []*helper.SigningJobWithPregeneratedNonce{job}, mockGetKeyPackages, frostSignerFactory)
		require.NoError(t, err)
		require.Len(t, results, 1)
		assert.Equal(t, "test-job-id", results[0].JobID)
	})

	t.Run("ErrorCases", func(t *testing.T) {
		config := sparktesting.TestConfig(t)

		t.Run("GetKeyPackagesError", func(t *testing.T) {
			// Add a mock operator to the config with identifier "operator1"
			if config.SigningOperatorMap == nil {
				config.SigningOperatorMap = make(map[string]*so.SigningOperator)
			}
			config.SigningOperatorMap["operator1"] = &so.SigningOperator{Identifier: "operator1"}

			mockGetKeyPackages := func(_ context.Context, _ *so.Config, _ []uuid.UUID) (map[uuid.UUID]*pbfrost.KeyPackage, error) {
				return nil, errors.New("database connection failed")
			}

			frostSignerFactory := &mockSparkServiceFrostSignerFactory{
				conn: &mockSparkServiceFrostSigner{},
			}

			job := &helper.SigningJobWithPregeneratedNonce{
				SigningJob: helper.SigningJob{
					JobID:             "test-job-id",
					SigningKeyshareID: uuid.New(),
					Message:           []byte("test message"),
					VerifyingKey:      &pubKey,
					UserCommitment:    &objects.SigningCommitment{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
				},
				Round1Packages: map[string]objects.SigningCommitment{
					"operator1": {Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
				},
			}

			_, err := helper.SignFrostWithPregeneratedNonceInternal(t.Context(), config, []*helper.SigningJobWithPregeneratedNonce{job}, mockGetKeyPackages, frostSignerFactory)
			require.ErrorContains(t, err, "database connection failed")
		})

		t.Run("FrostRound2Error", func(t *testing.T) {
			frostSignerFactory := &mockSparkServiceFrostSignerFactory{
				conn: &mockSparkServiceFrostSigner{
					frostRound2Error: errors.New("frost round 2 failed"),
				},
			}

			job := &helper.SigningJobWithPregeneratedNonce{
				SigningJob: helper.SigningJob{
					JobID:             "test-job-id",
					SigningKeyshareID: uuid.New(),
					Message:           []byte("test message"),
					VerifyingKey:      &pubKey,
					UserCommitment:    &objects.SigningCommitment{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
				},
				Round1Packages: map[string]objects.SigningCommitment{
					"operator1": {Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
				},
			}

			_, err := helper.SignFrostWithPregeneratedNonceInternal(t.Context(), config, []*helper.SigningJobWithPregeneratedNonce{job}, mockGetKeyPackages, frostSignerFactory)
			require.ErrorContains(t, err, "frost round 2 failed")
		})
	})
}

// TestGetSigningCommitments tests the GetSigningCommitments function
func TestGetSigningCommitments(t *testing.T) {
	t.Run("BasicFunctionality", func(t *testing.T) {
		config := sparktesting.TestConfig(t)
		keyshareIDs := []uuid.UUID{uuid.New(), uuid.New()}

		frostSigner := &mockSparkServiceFrostSigner{
			frostRound1Response: &pbinternal.FrostRound1Response{
				SigningCommitments: []*pbcommon.SigningCommitment{
					{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
					{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
				},
			},
		}

		frostSignerFactory := &mockSparkServiceFrostSignerFactory{
			conn: frostSigner,
		}

		_, err := helper.GetSigningCommitmentsInternal(t.Context(), config, keyshareIDs, mockGetKeyPackages, 1, frostSignerFactory)
		require.NoError(t, err)
	})

	t.Run("ErrorCases", func(t *testing.T) {
		config := sparktesting.TestConfig(t)

		t.Run("FrostRound1Error", func(t *testing.T) {
			frostSignerFactory := &mockSparkServiceFrostSignerFactory{
				conn: &mockSparkServiceFrostSigner{
					frostRound1Error: errors.New("frost round 1 failed"),
				},
			}

			keyshareIDs := []uuid.UUID{uuid.New()}

			_, err := helper.GetSigningCommitmentsInternal(t.Context(), config, keyshareIDs, mockGetKeyPackages, 1, frostSignerFactory)
			require.ErrorContains(t, err, "frost round 1 failed")
		})

		// Test with getKeyPackages error
		t.Run("GetKeyPackagesError", func(t *testing.T) {
			frostSignerFactory := &mockSparkServiceFrostSignerFactory{
				conn: &mockSparkServiceFrostSigner{},
			}
			keyshareIDs := []uuid.UUID{uuid.New()}

			mockGetKeyPackages := func(_ context.Context, _ *so.Config, _ []uuid.UUID) (map[uuid.UUID]*pbfrost.KeyPackage, error) {
				return nil, errors.New("database connection failed")
			}

			_, err := helper.GetSigningCommitmentsInternal(t.Context(), config, keyshareIDs, mockGetKeyPackages, 1, frostSignerFactory)
			require.ErrorContains(t, err, "database connection failed")
		})
	})
}

// TestNewSigningJobEdgeCases tests edge cases for NewSigningJob
func TestNewSigningJobEdgeCases(t *testing.T) {
	t.Run("InvalidKeyshare", func(t *testing.T) {
		proto := &pbspark.SigningJob{
			SigningPublicKey:       pubKey.Serialize(),
			RawTx:                  mockTxBuf(t, []int64{1000000}),
			SigningNonceCommitment: &pbcommon.SigningCommitment{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
		}

		prevOutput := &wire.TxOut{
			Value:    1000000,
			PkScript: []byte("test-pkscript"),
		}

		// Nil keyshare is invalid
		_, _, err := helper.NewSigningJob(nil, proto, prevOutput)
		require.Error(t, err)
	})

	t.Run("InvalidPrevOutput", func(t *testing.T) {
		keyshare := &ent.SigningKeyshare{
			ID:        uuid.New(),
			PublicKey: pubKey,
		}

		proto := &pbspark.SigningJob{
			SigningPublicKey:       pubKey.Serialize(),
			RawTx:                  mockTxBuf(t, []int64{1000000}),
			SigningNonceCommitment: &pbcommon.SigningCommitment{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
		}

		// Test with nil prevOutput
		_, _, err := helper.NewSigningJob(keyshare, proto, nil)
		require.Error(t, err)
	})

	t.Run("InvalidPublicKeyCombination", func(t *testing.T) {
		keyshare := &ent.SigningKeyshare{
			ID:        uuid.New(),
			PublicKey: pubKey,
		}

		// Test with invalid proto public key (wrong length)
		proto := &pbspark.SigningJob{
			SigningPublicKey:       []byte("invalid-key"), // Wrong length
			RawTx:                  mockTxBuf(t, []int64{1000000}),
			SigningNonceCommitment: &pbcommon.SigningCommitment{Binding: pubKey.Serialize(), Hiding: pubKey.Serialize()},
		}

		prevOutput := &wire.TxOut{
			Value:    1000000,
			PkScript: []byte("test-pkscript"),
		}

		_, _, err := helper.NewSigningJob(keyshare, proto, prevOutput)
		require.Error(t, err)
	})
}
