package signing_handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
	"maps"
	"slices"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/uuids"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pbfrost "github.com/lightsparkdev/spark/proto/frost"
	pb "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/frost"
)

type FrostSigningHandler struct {
	config *so.Config
}

func NewFrostSigningHandler(config *so.Config) *FrostSigningHandler {
	return &FrostSigningHandler{config: config}
}

func (h *FrostSigningHandler) GenerateRandomNonces(ctx context.Context, count uint32) (*pb.FrostRound1Response, error) {
	commitments := make([]*pbcommon.SigningCommitment, count)
	entSigningNonces := make([]*ent.SigningNonceCreate, count)
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}

	for i := range commitments {
		nonce := frost.GenerateSigningNonce()
		commitment := nonce.SigningCommitment()

		entSigningNonces[i] = db.SigningNonce.Create().
			SetNonce(nonce).
			SetNonceCommitment(commitment)
		commitments[i], _ = commitment.MarshalProto()
	}

	if err := db.SigningNonce.CreateBulk(entSigningNonces...).Exec(ctx); err != nil {
		return nil, err
	}

	return &pb.FrostRound1Response{SigningCommitments: commitments}, nil
}

func (h *FrostSigningHandler) FrostRound1(ctx context.Context, req *pb.FrostRound1Request) (*pb.FrostRound1Response, error) {
	totalCount := req.RandomNonceCount
	if req.RandomNonceCount <= 0 {
		count := req.Count
		if count == 0 {
			count = 1
		}

		totalCount = count * uint32(len(req.KeyshareIds))
	}

	if totalCount > 1_000_000 {
		return nil, fmt.Errorf("too many nonces requested in one request, please split into multiple requests")
	}

	return h.GenerateRandomNonces(ctx, totalCount)
}

// FrostRound2 handles FROST signing.
func (h *FrostSigningHandler) FrostRound2(ctx context.Context, req *pb.FrostRound2Request) (*pb.FrostRound2Response, error) {
	// Fetch key packages in one call.
	keyshareIDs, err := uuids.ParseSliceFunc(req.GetSigningJobs(), (*pb.SigningJob).GetKeyshareId)
	if err != nil {
		return nil, err
	}
	keyPackages, err := ent.GetKeyPackages(ctx, h.config, keyshareIDs)
	if err != nil {
		return nil, err
	}

	// Fetch nonces in one call.
	commitments := make([]frost.SigningCommitment, len(req.SigningJobs))
	for i, job := range req.SigningJobs {
		commitments[i] = frost.SigningCommitment{}
		err = commitments[i].UnmarshalProto(job.Commitments[h.config.Identifier])
		if err != nil {
			return nil, err
		}
	}
	nonces, err := ent.GetSigningNoncesForUpdate(ctx, h.config, commitments)
	if err != nil {
		return nil, err
	}

	var signingJobProtos []*pbfrost.FrostSigningJob
	bulkUpdates := make(map[frost.SigningCommitment][]byte)

	// First pass: validate all nonces and collect updates
	for _, job := range req.SigningJobs {
		commitment := frost.SigningCommitment{}
		err = commitment.UnmarshalProto(job.Commitments[h.config.Identifier])
		if err != nil {
			return nil, err
		}
		nonceEnt := nonces[commitment]
		if nonceEnt == nil {
			commitmentHex := hex.EncodeToString(commitment.MarshalBinary())
			return nil, fmt.Errorf("signing nonce for commitment %s not found", commitmentHex)
		}
		// TODO(zhenlu): Add a test for this (LIG-7596).
		jobRetryFingerprint := retryFingerprint(job)
		if len(nonceEnt.RetryFingerprint) > 0 {
			if !bytes.Equal(nonceEnt.RetryFingerprint, jobRetryFingerprint) {
				return nil, fmt.Errorf("this signing nonce is already used for a different signing job, cannot use it for this signing job")
			}
		} else if len(nonceEnt.Message) > 0 {
			// TODO: Probably remove this branch later after the retry fingerprint branch is deployed.
			if !bytes.Equal(nonceEnt.Message, job.Message) {
				return nil, fmt.Errorf("this signing nonce is already used for a different message %s, cannot use it for this message %s", hex.EncodeToString(nonceEnt.Message), hex.EncodeToString(job.Message))
			}
		} else {
			// Collect this nonce for bulk update
			bulkUpdates[commitment] = jobRetryFingerprint
		}
	}

	// Batch update all nonces that need retry fingerprints
	if len(bulkUpdates) > 0 {
		err = ent.BulkUpdateRetryFingerprints(ctx, nonces, bulkUpdates)
		if err != nil {
			return nil, fmt.Errorf("failed to batch update retry fingerprints: %w", err)
		}
	}

	// Second pass: build signing job protos
	for _, job := range req.SigningJobs {
		keyshareID, err := uuid.Parse(job.KeyshareId)
		if err != nil {
			return nil, err
		}
		commitment := frost.SigningCommitment{}
		if err := commitment.UnmarshalProto(job.Commitments[h.config.Identifier]); err != nil {
			return nil, err
		}

		nonceEnt := nonces[commitment]
		nonceObject := nonceEnt.Nonce
		nonceProto, _ := nonceObject.MarshalProto()
		signingJobProto := &pbfrost.FrostSigningJob{
			JobId:            job.JobId,
			Message:          job.Message,
			KeyPackage:       keyPackages[keyshareID],
			VerifyingKey:     job.VerifyingKey,
			Nonce:            nonceProto,
			Commitments:      job.Commitments,
			UserCommitments:  job.UserCommitments,
			AdaptorPublicKey: job.AdaptorPublicKey,
		}
		signingJobProtos = append(signingJobProtos, signingJobProto)
	}

	frostConn, err := h.config.NewFrostGRPCConnection()
	if err != nil {
		return nil, err
	}
	defer frostConn.Close()
	frostClient := pbfrost.NewFrostServiceClient(frostConn)

	round2Request := &pbfrost.SignFrostRequest{
		SigningJobs: signingJobProtos,
		Role:        pbfrost.SigningRole_STATECHAIN,
	}
	round2Response, err := frostClient.SignFrost(ctx, round2Request)
	if err != nil {
		return nil, err
	}

	return &pb.FrostRound2Response{Results: round2Response.Results}, nil
}

func retryFingerprint(job *pb.SigningJob) []byte {
	hashState := sha256.New()

	writeBytesCollisionResistant(hashState, job.Message)

	if job.UserCommitments != nil {
		writeBytesCollisionResistant(hashState, job.UserCommitments.Hiding)
		writeBytesCollisionResistant(hashState, job.UserCommitments.Binding)
	}

	hashState.Write(binary.BigEndian.AppendUint64(nil, uint64(len(job.Commitments))))

	for _, operatorIdentifier := range slices.Sorted(maps.Keys(job.Commitments)) {
		writeBytesCollisionResistant(hashState, []byte(operatorIdentifier))

		com := job.Commitments[operatorIdentifier]
		if com != nil {
			writeBytesCollisionResistant(hashState, com.Hiding)
			writeBytesCollisionResistant(hashState, com.Binding)
		}
	}

	return hashState.Sum(nil)
}

func writeBytesCollisionResistant(hashState hash.Hash, b []byte) {
	hashState.Write(binary.BigEndian.AppendUint64(nil, uint64(len(b))))
	hashState.Write(b)
}
