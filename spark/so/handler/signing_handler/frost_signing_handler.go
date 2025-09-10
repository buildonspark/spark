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
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pbfrost "github.com/lightsparkdev/spark/proto/frost"
	pb "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/objects"
)

type FrostSigningHandler struct {
	config *so.Config
}

func NewFrostSigningHandler(config *so.Config) *FrostSigningHandler {
	return &FrostSigningHandler{config: config}
}

func (h *FrostSigningHandler) GenerateRandomNonces(ctx context.Context, count uint32) (*pb.FrostRound1Response, error) {
	commitments := make([]*pbcommon.SigningCommitment, 0)
	entSigningNonces := make([]*ent.SigningNonceCreate, 0)
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}

	for i := 0; i < int(count); i++ {
		nonce, err := objects.RandomSigningNonce()
		if err != nil {
			return nil, err
		}

		commitment := nonce.SigningCommitment()

		entSigningNonces = append(
			entSigningNonces,
			db.SigningNonce.Create().
				SetNonce(nonce.MarshalBinary()).
				SetNonceCommitment(commitment.MarshalBinary()),
		)

		commitmentProto, err := nonce.SigningCommitment().MarshalProto()
		if err != nil {
			return nil, err
		}
		commitments = append(commitments, commitmentProto)
	}

	if err := db.SigningNonce.CreateBulk(entSigningNonces...).Exec(ctx); err != nil {
		return nil, err
	}

	return &pb.FrostRound1Response{
		SigningCommitments: commitments,
	}, nil
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

	if totalCount > 1000000 {
		return nil, fmt.Errorf("too many nonces requested in one request, please split into multiple requests")
	}

	return h.GenerateRandomNonces(ctx, totalCount)
}

// FrostRound2 handles FROST signing.
func (h *FrostSigningHandler) FrostRound2(ctx context.Context, req *pb.FrostRound2Request) (*pb.FrostRound2Response, error) {
	// Fetch key packages in one call.
	uuids := make([]uuid.UUID, len(req.SigningJobs))
	for i, job := range req.SigningJobs {
		uuid, err := uuid.Parse(job.KeyshareId)
		if err != nil {
			return nil, err
		}
		uuids[i] = uuid
	}

	keyPackages, err := ent.GetKeyPackages(ctx, h.config, uuids)
	if err != nil {
		return nil, err
	}

	// Fetch nonces in one call.
	commitments := make([]objects.SigningCommitment, len(req.SigningJobs))
	for i, job := range req.SigningJobs {
		commitments[i] = objects.SigningCommitment{}
		err = commitments[i].UnmarshalProto(job.Commitments[h.config.Identifier])
		if err != nil {
			return nil, err
		}
	}
	nonces, err := ent.GetSigningNonces(ctx, h.config, commitments)
	if err != nil {
		return nil, err
	}

	signingJobProtos := make([]*pbfrost.FrostSigningJob, 0)

	for _, job := range req.SigningJobs {
		keyshareID, err := uuid.Parse(job.KeyshareId)
		if err != nil {
			return nil, err
		}
		commitment := objects.SigningCommitment{}
		err = commitment.UnmarshalProto(job.Commitments[h.config.Identifier])
		if err != nil {
			return nil, err
		}
		nonceEnt := nonces[commitment.Key()]
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
			_, err = nonceEnt.Update().SetRetryFingerprint(jobRetryFingerprint).Save(ctx)
			if err != nil {
				return nil, err
			}
		}
		nonceObject := objects.SigningNonce{}
		err = nonceObject.UnmarshalBinary(nonceEnt.Nonce)
		if err != nil {
			return nil, err
		}
		nonceProto, err := nonceObject.MarshalProto()
		if err != nil {
			return nil, err
		}
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

	return &pb.FrostRound2Response{
		Results: round2Response.Results,
	}, nil
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
