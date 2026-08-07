package handler

import (
	"testing"

	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	pbfrost "github.com/lightsparkdev/spark/proto/frost"
	"github.com/lightsparkdev/spark/so/helper"
	transferpkg "github.com/lightsparkdev/spark/so/transfer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mpcRefundFixture parses a valid MPC submission whose cpfp refund txs spend
// output 0 of the returned parent tx, and returns the first refund signing
// job along with the parent tx bytes.
func mpcRefundFixture(t *testing.T) (*transferpkg.MpcRefundSigningJob, []byte) {
	t.Helper()
	parent := wire.NewMsgTx(wire.TxVersion)
	parent.AddTxIn(wire.NewTxIn(&wire.OutPoint{Hash: [32]byte{7}, Index: 0}, nil, nil))
	parent.AddTxOut(wire.NewTxOut(1000, []byte{0x51}))
	parentBytes := mustSerializeTx(t, parent)

	refund := wire.NewMsgTx(wire.TxVersion)
	refund.AddTxIn(wire.NewTxIn(&wire.OutPoint{Hash: parent.TxHash(), Index: 0}, nil, nil))
	refund.AddTxOut(wire.NewTxOut(900, []byte{0x51}))
	refundBytes := mustSerializeTx(t, refund)

	req := validMpcTransferRequest(t, keys.GeneratePrivateKey().Public())
	for _, job := range req.GetMpcTransferPackage().GetLeavesToSend() {
		job.RawTx = refundBytes
	}
	submission, err := transferpkg.ParseMpcSubmission(req)
	require.NoError(t, err)
	require.Len(t, submission.LeavesToSend(), 1)
	return submission.LeavesToSend()[0], parentBytes
}

func TestBuildMpcSigningJobForRefund(t *testing.T) {
	job, parentBytes := mpcRefundFixture(t)
	verifyingKey := keys.GeneratePrivateKey().Public()
	keyshareID := uuid.New()
	jobID := uuid.New()

	built, err := buildMpcSigningJobForRefund(job, verifyingKey, keyshareID, parentBytes, jobID)
	require.NoError(t, err)

	assert.Equal(t, jobID, built.JobID)
	assert.Equal(t, keyshareID, built.SigningKeyshareID)
	assert.Equal(t, verifyingKey, *built.VerifyingKey)
	assert.Equal(t, pbfrost.SigningScheme_SIGNING_SCHEME_MPC_USER_GROUP, built.SigningScheme)
	// The user side is the sub-user group: no single-user commitment, no
	// adaptor, and one entry per contribution aligned to the positions.
	assert.Nil(t, built.UserCommitment)
	assert.Nil(t, built.AdaptorPublicKey)
	contributions := job.Contributions()
	require.Len(t, built.SubUserCommitments, len(contributions))
	for i, contribution := range contributions {
		assert.Equal(t, contribution.Position(), built.SubUserCommitments[i].GetPosition())
		expected := contribution.NonceCommitment()
		assert.Equal(t, expected.MarshalProto().GetHiding(), built.SubUserCommitments[i].GetCommitment().GetHiding())
		assert.Equal(t, expected.MarshalProto().GetBinding(), built.SubUserCommitments[i].GetCommitment().GetBinding())
	}
	assert.Equal(t, map[string]struct{}{mpcTestOperatorID: {}}, keySet(built.Round1Packages))
}

func keySet[V any](m map[string]V) map[string]struct{} {
	out := make(map[string]struct{}, len(m))
	for k := range m {
		out[k] = struct{}{}
	}
	return out
}

func TestBuildMpcSigningJobForRefund_WrongParent(t *testing.T) {
	job, _ := mpcRefundFixture(t)
	otherParent := wire.NewMsgTx(wire.TxVersion)
	otherParent.AddTxIn(wire.NewTxIn(&wire.OutPoint{Hash: [32]byte{9}, Index: 0}, nil, nil))
	otherParent.AddTxOut(wire.NewTxOut(1000, []byte{0x51}))

	_, err := buildMpcSigningJobForRefund(
		job,
		keys.GeneratePrivateKey().Public(),
		uuid.New(),
		mustSerializeTx(t, otherParent),
		uuid.New(),
	)
	require.ErrorContains(t, err, "must spend parent tx output 0")
}

func TestMpcSubUserShares(t *testing.T) {
	job, _ := mpcRefundFixture(t)
	contributions := job.Contributions()

	shares := mpcSubUserShares(contributions)
	require.Len(t, shares, len(contributions))
	for i, contribution := range contributions {
		assert.Equal(t, contribution.Position(), shares[i].GetPosition())
		assert.Equal(t, contribution.PartialSignature(), shares[i].GetSignatureShare())
		expected := contribution.NonceCommitment()
		assert.Equal(t, expected.MarshalProto().GetHiding(), shares[i].GetCommitment().GetHiding())
		assert.Equal(t, expected.MarshalProto().GetBinding(), shares[i].GetCommitment().GetBinding())
	}
}

func TestAddMpcJob(t *testing.T) {
	job, parentBytes := mpcRefundFixture(t)
	verifyingKey := keys.GeneratePrivateKey().Public()
	keyshareID := uuid.New()
	built, err := buildMpcSigningJobForRefund(job, verifyingKey, keyshareID, parentBytes, uuid.New())
	require.NoError(t, err)

	batch := &frostAggregationBatch{
		requests: make(map[string]*pbfrost.AggregateFrostRequest),
		keyPackages: map[uuid.UUID]*pbfrost.KeyPackage{
			keyshareID: {PublicShares: map[string][]byte{mpcTestOperatorID: {0x02}}},
		},
	}
	allShares := map[string]map[string][]byte{
		built.JobID.String(): {mpcTestOperatorID: {0x01}},
	}
	subuserShares := mpcSubUserShares(job.Contributions())

	err = batch.addMpcJob(t.Context(), "job-key", built, allShares, verifyingKey, subuserShares)
	require.NoError(t, err)

	request := batch.requests["job-key"]
	require.NotNil(t, request)
	assert.Equal(t, pbfrost.SigningScheme_SIGNING_SCHEME_MPC_USER_GROUP, request.GetSigningScheme())
	assert.Equal(t, subuserShares, request.GetSubuserShares())
	assert.Equal(t, verifyingKey.Serialize(), request.GetVerifyingKey())
	// Single-user fields stay absent: the signer branches on the scheme and
	// rejects mixed forms.
	assert.Nil(t, request.GetUserCommitments())
	assert.Empty(t, request.GetUserPublicKey())
	assert.Empty(t, request.GetUserSignatureShare())
	assert.Empty(t, request.GetAdaptorPublicKey())
}

func TestAddMpcJob_RejectsNonMpcJob(t *testing.T) {
	batch := &frostAggregationBatch{requests: make(map[string]*pbfrost.AggregateFrostRequest)}
	err := batch.addMpcJob(
		t.Context(),
		"job-key",
		&helper.SigningJobWithPregeneratedNonce{},
		nil,
		keys.GeneratePrivateKey().Public(),
		nil,
	)
	require.ErrorContains(t, err, "not an MPC signing job")
}

func TestAddMpcJob_RejectsAdaptor(t *testing.T) {
	adaptor := keys.GeneratePrivateKey().Public()
	batch := &frostAggregationBatch{requests: make(map[string]*pbfrost.AggregateFrostRequest)}
	err := batch.addMpcJob(
		t.Context(),
		"job-key",
		&helper.SigningJobWithPregeneratedNonce{
			SigningJob: helper.SigningJob{
				SigningScheme:    pbfrost.SigningScheme_SIGNING_SCHEME_MPC_USER_GROUP,
				AdaptorPublicKey: &adaptor,
			},
		},
		nil,
		keys.GeneratePrivateKey().Public(),
		nil,
	)
	require.ErrorContains(t, err, "adaptor signatures are not supported")
}
