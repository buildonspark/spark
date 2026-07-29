package transfer

import (
	"testing"

	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	"github.com/lightsparkdev/spark/proto/spark"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testOperatorID = "0000000000000000000000000000000000000000000000000000000000000001"

// testSigningCommitment builds a structurally valid, non-zero common.SigningCommitment.
func testSigningCommitment(t *testing.T) *pbcommon.SigningCommitment {
	t.Helper()
	return &pbcommon.SigningCommitment{
		Hiding:  keys.GeneratePrivateKey().Public().Serialize(),
		Binding: keys.GeneratePrivateKey().Public().Serialize(),
	}
}

// testRawTx serializes a tx with numInputs distinct inputs and a single output.
func testRawTx(t *testing.T, numInputs int) []byte {
	t.Helper()
	tx := wire.NewMsgTx(wire.TxVersion)
	for i := range numInputs {
		tx.AddTxIn(wire.NewTxIn(&wire.OutPoint{Hash: [32]byte{byte(i + 1)}, Index: uint32(i)}, nil, nil))
	}
	tx.AddTxOut(wire.NewTxOut(1000, []byte{0x51}))
	raw, err := common.SerializeTx(tx)
	require.NoError(t, err)
	return raw
}

// validSigningJob builds a single-input, fully-populated proto signing job that ParsePackage accepts.
func validSigningJob(t *testing.T) *spark.UserSignedTxSigningJob {
	t.Helper()
	return &spark.UserSignedTxSigningJob{
		LeafId:                 uuid.NewString(),
		SigningPublicKey:       keys.GeneratePrivateKey().Public().Serialize(),
		RawTx:                  testRawTx(t, 1),
		SigningNonceCommitment: testSigningCommitment(t),
		SigningCommitments: &spark.SigningCommitments{
			SigningCommitments: map[string]*pbcommon.SigningCommitment{testOperatorID: testSigningCommitment(t)},
		},
		UserSignature: []byte{0x01},
	}
}

// validKeyTweakPackage builds a minimal non-empty key-tweak package that ParsePackage accepts.
func validKeyTweakPackage() map[string][]byte {
	return map[string][]byte{testOperatorID: {0x01}}
}

func parseSingleJob(job *spark.UserSignedTxSigningJob) (*RefundSigningJob, error) {
	pkg, err := ParsePackage(&spark.TransferPackage{
		LeavesToSend:    []*spark.UserSignedTxSigningJob{job},
		KeyTweakPackage: validKeyTweakPackage(),
		UserSignature:   []byte{0x01},
	})
	if err != nil {
		return nil, err
	}
	return pkg.LeavesToSend()[0], nil
}

// TestParsePackage_MinimalValid verifies a package with only leaves-to-send (plus the required key-tweak package and
// user signature) parses with empty direct/direct-from-cpfp projections.
func TestParsePackage_MinimalValid(t *testing.T) {
	pkg, err := ParsePackage(&spark.TransferPackage{
		LeavesToSend:    []*spark.UserSignedTxSigningJob{validSigningJob(t)},
		KeyTweakPackage: validKeyTweakPackage(),
		UserSignature:   []byte{0x01},
	})
	require.NoError(t, err)
	assert.Len(t, pkg.LeavesToSend(), 1)
	assert.Empty(t, pkg.DirectLeavesToSend())
	assert.Empty(t, pkg.DirectFromCPFPLeavesToSend())
	assert.Len(t, pkg.CPFPRefundTxByLeafID(), 1)
	assert.NotEmpty(t, pkg.KeyTweakPackage())
}

func TestParsePackage_PackageLevelErrors(t *testing.T) {
	leaf := validSigningJob(t)
	cases := []struct {
		name        string
		pkg         *spark.TransferPackage
		expectedErr error
	}{
		{
			name:        "empty key tweak package",
			pkg:         &spark.TransferPackage{LeavesToSend: []*spark.UserSignedTxSigningJob{validSigningJob(t)}, UserSignature: []byte{0x01}},
			expectedErr: ErrEmptyKeyTweakPackage,
		},
		{
			name:        "key tweak package too large",
			pkg:         &spark.TransferPackage{LeavesToSend: []*spark.UserSignedTxSigningJob{validSigningJob(t)}, KeyTweakPackage: map[string][]byte{testOperatorID: make([]byte, MaxKeyTweakPackageSize+1)}, UserSignature: []byte{0x01}},
			expectedErr: ErrKeyTweakPackageTooLarge,
		},
		{
			name:        "empty user signature",
			pkg:         &spark.TransferPackage{LeavesToSend: []*spark.UserSignedTxSigningJob{validSigningJob(t)}, KeyTweakPackage: validKeyTweakPackage()},
			expectedErr: ErrEmptyUserSignature,
		},
		{
			name:        "user signature too large",
			pkg:         &spark.TransferPackage{LeavesToSend: []*spark.UserSignedTxSigningJob{validSigningJob(t)}, KeyTweakPackage: validKeyTweakPackage(), UserSignature: make([]byte, MaxSignatureSize+1)},
			expectedErr: ErrUserSignatureTooLarge,
		},
		{
			name: "orphan direct leaf",
			pkg: &spark.TransferPackage{
				LeavesToSend:       []*spark.UserSignedTxSigningJob{leaf},
				DirectLeavesToSend: []*spark.UserSignedTxSigningJob{validSigningJob(t)},
				KeyTweakPackage:    validKeyTweakPackage(),
				UserSignature:      []byte{0x01},
			},
			expectedErr: ErrOrphanLeaf,
		},
		{
			name: "direct-from-cpfp count mismatch",
			pkg: &spark.TransferPackage{
				LeavesToSend:               []*spark.UserSignedTxSigningJob{leaf, validSigningJob(t)},
				DirectFromCpfpLeavesToSend: []*spark.UserSignedTxSigningJob{leaf},
				KeyTweakPackage:            validKeyTweakPackage(),
				UserSignature:              []byte{0x01},
			},
			expectedErr: ErrMismatchedLeafCount,
		},
		{
			// Count matches leaves-to-send (2 == 2), but the second entry references a leaf that isn't in
			// leaves-to-send, so the count check passes and the orphan check must catch it.
			name: "orphan direct-from-cpfp leaf",
			pkg: &spark.TransferPackage{
				LeavesToSend:               []*spark.UserSignedTxSigningJob{leaf, validSigningJob(t)},
				DirectFromCpfpLeavesToSend: []*spark.UserSignedTxSigningJob{leaf, validSigningJob(t)},
				KeyTweakPackage:            validKeyTweakPackage(),
				UserSignature:              []byte{0x01},
			},
			expectedErr: ErrOrphanLeaf,
		},
		{
			name: "unknown hash variant",
			pkg: &spark.TransferPackage{
				LeavesToSend:    []*spark.UserSignedTxSigningJob{validSigningJob(t)},
				KeyTweakPackage: validKeyTweakPackage(),
				UserSignature:   []byte{0x01},
				HashVariant:     spark.HashVariant(99),
			},
			expectedErr: ErrUnknownHashVariant,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParsePackage(tc.pkg)
			require.ErrorIs(t, err, tc.expectedErr)
		})
	}
}

func TestParsePackage_AllVariants(t *testing.T) {
	cpfp := validSigningJob(t)
	// The direct and direct-from-cpfp variants are alternate refunds for the same leaf, so they share cpfp's leaf ID.
	direct := validSigningJob(t)
	direct.LeafId = cpfp.GetLeafId()
	dfc := validSigningJob(t)
	dfc.LeafId = cpfp.GetLeafId()

	pkg, err := ParsePackage(&spark.TransferPackage{
		LeavesToSend:               []*spark.UserSignedTxSigningJob{cpfp},
		DirectLeavesToSend:         []*spark.UserSignedTxSigningJob{direct},
		DirectFromCpfpLeavesToSend: []*spark.UserSignedTxSigningJob{dfc},
		KeyTweakPackage:            validKeyTweakPackage(),
		UserSignature:              []byte{0x01},
	})
	require.NoError(t, err)

	require.Len(t, pkg.LeavesToSend(), 1)
	require.Len(t, pkg.DirectLeavesToSend(), 1)
	require.Len(t, pkg.DirectFromCPFPLeavesToSend(), 1)

	job := pkg.LeavesToSend()[0]
	assert.Equal(t, cpfp.GetLeafId(), job.LeafID().String())
	assert.Equal(t, cpfp.GetSigningPublicKey(), job.SigningPubKey().Serialize())
	assert.Equal(t, cpfp.GetRawTx(), job.RawTx())
	require.NotNil(t, job.RefundTx())
	assert.Len(t, job.RefundTx().TxIn, 1)
	require.Len(t, job.Inputs(), 1)
	assert.Equal(t, []byte{0x01}, job.Inputs()[0].UserSignature())
	assert.NotZero(t, job.Inputs()[0].SigningNonceCommitment())
	assert.Contains(t, job.Inputs()[0].SigningCommitments(), testOperatorID)
}

func TestParsePackage_Projections(t *testing.T) {
	cpfp := validSigningJob(t)
	// Alternate refunds for the same leaf share cpfp's leaf ID; each variant keeps its own raw tx.
	direct := validSigningJob(t)
	direct.LeafId = cpfp.GetLeafId()
	dfc := validSigningJob(t)
	dfc.LeafId = cpfp.GetLeafId()

	pkg, err := ParsePackage(&spark.TransferPackage{
		LeavesToSend:               []*spark.UserSignedTxSigningJob{cpfp},
		DirectLeavesToSend:         []*spark.UserSignedTxSigningJob{direct},
		DirectFromCpfpLeavesToSend: []*spark.UserSignedTxSigningJob{dfc},
		KeyTweakPackage:            validKeyTweakPackage(),
		UserSignature:              []byte{0x01},
	})
	require.NoError(t, err)

	cpfpMap := pkg.CPFPRefundTxByLeafID()
	require.Len(t, cpfpMap, 1)
	assert.Equal(t, cpfp.GetRawTx(), cpfpMap[uuid.MustParse(cpfp.GetLeafId())])

	assert.Equal(t, direct.GetRawTx(), pkg.DirectRefundTxByLeafID()[uuid.MustParse(direct.GetLeafId())])
	assert.Equal(t, dfc.GetRawTx(), pkg.DirectFromCPFPRefundTxByLeafID()[uuid.MustParse(dfc.GetLeafId())])
}

func TestPackage_LeafIDs(t *testing.T) {
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	withLeafID := func(id uuid.UUID) *spark.UserSignedTxSigningJob {
		j := validSigningJob(t)
		j.LeafId = id.String()
		return j
	}

	pkg, err := ParsePackage(&spark.TransferPackage{
		LeavesToSend:               []*spark.UserSignedTxSigningJob{withLeafID(a), withLeafID(b), withLeafID(c)},
		DirectLeavesToSend:         []*spark.UserSignedTxSigningJob{withLeafID(a)}, // duplicate of cpfp
		DirectFromCpfpLeavesToSend: []*spark.UserSignedTxSigningJob{withLeafID(a), withLeafID(b), withLeafID(c)},
		KeyTweakPackage:            validKeyTweakPackage(),
		UserSignature:              []byte{0x01},
	})
	require.NoError(t, err)

	// Distinct, in first-seen order across cpfp → direct → direct-from-cpfp.
	assert.Equal(t, []uuid.UUID{a, b, c}, pkg.LeafIDs())
}

func TestParsePackage_PassthroughFields(t *testing.T) {
	pkg, err := ParsePackage(&spark.TransferPackage{
		KeyTweakPackage: map[string][]byte{"op-1": {0xaa}, "op-2": {0xbb}},
		UserSignature:   []byte{0xde, 0xad},
		HashVariant:     spark.HashVariant_HASH_VARIANT_V2,
	})
	require.NoError(t, err)
	assert.Equal(t, []byte{0xaa}, pkg.KeyTweakPackage()["op-1"])
	assert.Equal(t, []byte{0xbb}, pkg.KeyTweakPackage()["op-2"])
	assert.Equal(t, []byte{0xde, 0xad}, pkg.UserSignature())
	assert.Equal(t, spark.HashVariant_HASH_VARIANT_V2, pkg.HashVariant())
}

// TestParsePackage_NormalizesInputs verifies that input 0's signing material and additional_inputs collapse
// into one ordered inputs slice.
func TestParsePackage_NormalizesInputs(t *testing.T) {
	job := validSigningJob(t)
	job.RawTx = testRawTx(t, 2)
	job.UserSignature = []byte{0x01} // input 0
	job.AdditionalInputs = []*spark.InputSigningData{
		{
			SigningNonceCommitment: testSigningCommitment(t),
			UserSignature:          []byte{0x02}, // input 1
			SigningCommitments: &spark.SigningCommitments{
				SigningCommitments: map[string]*pbcommon.SigningCommitment{testOperatorID: testSigningCommitment(t)},
			},
		},
	}

	parsed, err := parseSingleJob(job)
	require.NoError(t, err)
	require.Len(t, parsed.Inputs(), 2)
	assert.Equal(t, []byte{0x01}, parsed.Inputs()[0].UserSignature(), "inputs[0] comes from the top-level fields")
	assert.Equal(t, []byte{0x02}, parsed.Inputs()[1].UserSignature(), "inputs[1] comes from additional_inputs[0]")
}

// TestParseRefundSigningJob_FewerSigningInputsThanTxInputs covers the cooperative-exit shape: a 2-input refund tx where
// only input 0 is SE-signed, so the job carries a single signing input.
func TestParseRefundSigningJob_FewerSigningInputsThanTxInputs(t *testing.T) {
	job := validSigningJob(t)
	job.RawTx = testRawTx(t, 2) // connector adds a second, unsigned input

	parsed, err := parseSingleJob(job)
	require.NoError(t, err)
	assert.Len(t, parsed.RefundTx().TxIn, 2)
	assert.Len(t, parsed.Inputs(), 1)
}

func TestParseRefundSigningJob_Errors(t *testing.T) {
	cases := []struct {
		name        string
		mutate      func(*spark.UserSignedTxSigningJob)
		expectedErr error
	}{
		{
			name:        "invalid leaf id",
			mutate:      func(j *spark.UserSignedTxSigningJob) { j.LeafId = "not-a-uuid" },
			expectedErr: ErrInvalidLeafID,
		},
		{
			name:        "invalid signing public key",
			mutate:      func(j *spark.UserSignedTxSigningJob) { j.SigningPublicKey = []byte{0x00} },
			expectedErr: ErrInvalidSigningPublicKey,
		},
		{
			name:        "invalid raw tx",
			mutate:      func(j *spark.UserSignedTxSigningJob) { j.RawTx = []byte{0x01, 0x02} },
			expectedErr: ErrInvalidRefundTx,
		},
		{
			name:        "missing user signature",
			mutate:      func(j *spark.UserSignedTxSigningJob) { j.UserSignature = nil },
			expectedErr: ErrMissingUserSignature,
		},
		{
			name: "multiparty contributions on a single-signer job",
			mutate: func(j *spark.UserSignedTxSigningJob) {
				j.SubuserContributions = []*spark.SubUserSigningContribution{{PartialSignature: []byte{0x01}}}
			},
			expectedErr: ErrUnexpectedSubUserContributions,
		},
		{
			name:        "nil nonce commitment",
			mutate:      func(j *spark.UserSignedTxSigningJob) { j.SigningNonceCommitment = nil },
			expectedErr: ErrInvalidNonceCommitment,
		},
		{
			name: "invalid operator commitment",
			mutate: func(j *spark.UserSignedTxSigningJob) {
				j.SigningCommitments = &spark.SigningCommitments{
					SigningCommitments: map[string]*pbcommon.SigningCommitment{testOperatorID: {Hiding: []byte{0x00}, Binding: []byte{0x00}}},
				}
			},
			expectedErr: ErrInvalidOperatorCommitment,
		},
		{
			name:        "nil operator commitments",
			mutate:      func(j *spark.UserSignedTxSigningJob) { j.SigningCommitments = nil },
			expectedErr: ErrMissingOperatorCommitment,
		},
		{
			name: "empty operator commitments",
			mutate: func(j *spark.UserSignedTxSigningJob) {
				j.SigningCommitments = &spark.SigningCommitments{SigningCommitments: map[string]*pbcommon.SigningCommitment{}}
			},
			expectedErr: ErrMissingOperatorCommitment,
		},
		{
			name: "more signing inputs than tx inputs",
			mutate: func(j *spark.UserSignedTxSigningJob) {
				j.AdditionalInputs = []*spark.InputSigningData{
					{
						SigningNonceCommitment: testSigningCommitment(t),
						UserSignature:          []byte{0x02},
						SigningCommitments: &spark.SigningCommitments{
							SigningCommitments: map[string]*pbcommon.SigningCommitment{testOperatorID: testSigningCommitment(t)},
						},
					},
				}
			},
			expectedErr: ErrTooManySigningInputs,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			job := validSigningJob(t)
			tc.mutate(job)
			_, err := parseSingleJob(job)
			require.ErrorIs(t, err, tc.expectedErr)
		})
	}
}

func TestParseRefundSigningJobs_DuplicateLeafInList(t *testing.T) {
	a := validSigningJob(t)
	_, err := ParsePackage(&spark.TransferPackage{LeavesToSend: []*spark.UserSignedTxSigningJob{a, a}})
	require.ErrorIs(t, err, ErrDuplicateLeafID)
}

// TestPackage_KeyTweakPackageIsCopy verifies the getter hands back a copy, so mutating it can't corrupt the Package's
// validated internal state.
func TestPackage_KeyTweakPackageIsCopy(t *testing.T) {
	pkg, err := ParsePackage(&spark.TransferPackage{
		KeyTweakPackage: map[string][]byte{"op-1": {0xaa}},
		UserSignature:   []byte{0x01},
	})
	require.NoError(t, err)

	copied := pkg.KeyTweakPackage()
	copied["op-1"] = []byte{0xff}
	delete(copied, "op-1")
	copied["injected"] = []byte{0x99}

	assert.Equal(t, []byte{0xaa}, pkg.KeyTweakPackage()["op-1"])
	assert.NotContains(t, pkg.KeyTweakPackage(), "injected")
}

// sharedLeafJob builds a valid signing job pinned to leafID, for building alternate-refund lists that reference the
// same leaves as leaves-to-send.
func sharedLeafJob(t *testing.T, leafID string) *spark.UserSignedTxSigningJob {
	t.Helper()
	j := validSigningJob(t)
	j.LeafId = leafID
	return j
}

func TestParsePackage_RequireDirectFromCPFPLeaves(t *testing.T) {
	cpfp := validSigningJob(t)

	t.Run("required and missing fails", func(t *testing.T) {
		_, err := ParsePackage(&spark.TransferPackage{
			LeavesToSend:    []*spark.UserSignedTxSigningJob{validSigningJob(t)},
			KeyTweakPackage: validKeyTweakPackage(),
			UserSignature:   []byte{0x01},
		}, RequireDirectFromCPFPLeaves(true))
		require.ErrorIs(t, err, ErrMissingDirectFromCPFPLeaves)
	})

	t.Run("required and present passes", func(t *testing.T) {
		pkg, err := ParsePackage(&spark.TransferPackage{
			LeavesToSend:               []*spark.UserSignedTxSigningJob{cpfp},
			DirectFromCpfpLeavesToSend: []*spark.UserSignedTxSigningJob{sharedLeafJob(t, cpfp.GetLeafId())},
			KeyTweakPackage:            validKeyTweakPackage(),
			UserSignature:              []byte{0x01},
		}, RequireDirectFromCPFPLeaves(true))
		require.NoError(t, err)
		assert.Len(t, pkg.DirectFromCPFPLeavesToSend(), 1)
	})

	t.Run("not required and missing passes", func(t *testing.T) {
		_, err := ParsePackage(&spark.TransferPackage{
			LeavesToSend:    []*spark.UserSignedTxSigningJob{validSigningJob(t)},
			KeyTweakPackage: validKeyTweakPackage(),
			UserSignature:   []byte{0x01},
		}, RequireDirectFromCPFPLeaves(false))
		require.NoError(t, err)
	})
}

func TestParsePackage_WithMaxLeavesToSend(t *testing.T) {
	a, b := validSigningJob(t), validSigningJob(t)

	t.Run("leaves-to-send over cap fails", func(t *testing.T) {
		_, err := ParsePackage(&spark.TransferPackage{
			LeavesToSend:    []*spark.UserSignedTxSigningJob{a, b},
			KeyTweakPackage: validKeyTweakPackage(),
			UserSignature:   []byte{0x01},
		}, WithMaxLeavesToSend(1))
		require.ErrorIs(t, err, ErrTooManyLeaves)
	})

	t.Run("direct list over cap fails", func(t *testing.T) {
		_, err := ParsePackage(&spark.TransferPackage{
			LeavesToSend:       []*spark.UserSignedTxSigningJob{a},
			DirectLeavesToSend: []*spark.UserSignedTxSigningJob{a, b},
			KeyTweakPackage:    validKeyTweakPackage(),
			UserSignature:      []byte{0x01},
		}, WithMaxLeavesToSend(1))
		require.ErrorIs(t, err, ErrTooManyLeaves)
	})

	t.Run("direct-from-cpfp list over cap fails", func(t *testing.T) {
		_, err := ParsePackage(&spark.TransferPackage{
			LeavesToSend:               []*spark.UserSignedTxSigningJob{a},
			DirectFromCpfpLeavesToSend: []*spark.UserSignedTxSigningJob{a, b},
			KeyTweakPackage:            validKeyTweakPackage(),
			UserSignature:              []byte{0x01},
		}, WithMaxLeavesToSend(1))
		require.ErrorIs(t, err, ErrTooManyLeaves)
	})

	t.Run("at cap passes", func(t *testing.T) {
		_, err := ParsePackage(&spark.TransferPackage{
			LeavesToSend:    []*spark.UserSignedTxSigningJob{a, b},
			KeyTweakPackage: validKeyTweakPackage(),
			UserSignature:   []byte{0x01},
		}, WithMaxLeavesToSend(2))
		require.NoError(t, err)
	})

	t.Run("non-positive cap disables the limit", func(t *testing.T) {
		_, err := ParsePackage(&spark.TransferPackage{
			LeavesToSend:    []*spark.UserSignedTxSigningJob{a, b},
			KeyTweakPackage: validKeyTweakPackage(),
			UserSignature:   []byte{0x01},
		}, WithMaxLeavesToSend(0))
		require.NoError(t, err)
	})
}

func TestParseRefundSigningJobs_MaxJobs(t *testing.T) {
	a, b := validSigningJob(t), validSigningJob(t)
	t.Run("over cap fails", func(t *testing.T) {
		_, err := ParseRefundSigningJobs([]*spark.UserSignedTxSigningJob{a, b}, 1)
		require.ErrorIs(t, err, ErrTooManyLeaves)
	})

	t.Run("at cap passes", func(t *testing.T) {
		jobs, err := ParseRefundSigningJobs([]*spark.UserSignedTxSigningJob{a, b}, 2)
		require.NoError(t, err)
		assert.Len(t, jobs, 2)
	})

	t.Run("non-positive cap disables the limit", func(t *testing.T) {
		jobs, err := ParseRefundSigningJobs([]*spark.UserSignedTxSigningJob{a, b}, 0)
		require.NoError(t, err)
		assert.Len(t, jobs, 2)
	})

	t.Run("cap checked before parsing malformed jobs", func(t *testing.T) {
		bad := validSigningJob(t)
		bad.LeafId = "not-a-uuid"
		_, err := ParseRefundSigningJobs([]*spark.UserSignedTxSigningJob{bad, bad}, 1)
		require.ErrorIs(t, err, ErrTooManyLeaves)
	})
}

func TestParsePackage_RejectsMalformedDirectLeaves(t *testing.T) {
	bad := validSigningJob(t)
	bad.LeafId = "not-a-uuid"

	_, err := ParsePackage(&spark.TransferPackage{
		LeavesToSend:       []*spark.UserSignedTxSigningJob{validSigningJob(t)},
		DirectLeavesToSend: []*spark.UserSignedTxSigningJob{bad},
	})
	require.Error(t, err)
}
