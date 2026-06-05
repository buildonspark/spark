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
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/uuids"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pbfrost "github.com/lightsparkdev/spark/proto/frost"
	pb "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/frost"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type FrostSigningHandler struct {
	config *so.Config
}

const maxFrostRound1Nonces uint64 = 1_000_000

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
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}

	var totalCount uint64
	if req.GetRandomNonceCount() > 0 {
		totalCount = uint64(req.GetRandomNonceCount())
	} else {
		count := req.GetCount()
		if count == 0 {
			count = 1
		}

		keyshareCount := uint64(len(req.GetKeyshareIds()))
		if uint64(count) > maxFrostRound1Nonces || keyshareCount > maxFrostRound1Nonces {
			return nil, status.Error(codes.InvalidArgument, "too many nonces requested in one request, please split into multiple requests")
		}

		totalCount = uint64(count) * keyshareCount
	}

	if totalCount > maxFrostRound1Nonces {
		return nil, status.Error(codes.InvalidArgument, "too many nonces requested in one request, please split into multiple requests")
	}

	return h.GenerateRandomNonces(ctx, uint32(totalCount))
}

// FrostRound2 handles FROST signing. It loads each job's KeyPackage from the
// DB (or ephemeral store) and signs with it. For callers that need to sign
// with an in-memory-modified KeyPackage (e.g. the consensus claim-transfer
// Prepare phase, which signs with a post-tweak share without persisting the
// tweak), use FrostRound2WithKeyPackages instead.
func (h *FrostSigningHandler) FrostRound2(ctx context.Context, req *pb.FrostRound2Request) (*pb.FrostRound2Response, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	if len(req.GetSigningJobs()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "signing_jobs is required")
	}
	for i, job := range req.GetSigningJobs() {
		if job == nil {
			return nil, status.Errorf(codes.InvalidArgument, "signing_jobs[%d] is required", i)
		}
	}
	keyshareIDs, err := uuids.ParseSliceFunc(req.GetSigningJobs(), (*pb.SigningJob).GetKeyshareId)
	if err != nil {
		return nil, err
	}
	keyPackages, err := ent.GetKeyPackages(ctx, h.config, keyshareIDs)
	if err != nil {
		return nil, err
	}
	return h.FrostRound2WithKeyPackages(ctx, req, keyPackages)
}

// FrostRound2WithKeyPackages is like FrostRound2 but takes pre-loaded
// KeyPackages keyed by signing-keyshare ID instead of loading them from the
// DB. The consensus claim-transfer Prepare phase uses this to sign with a
// fresh KeyPackage that has the receiver key tweak applied in-memory — the
// on-disk keyshare stays at the pre-tweak value until Commit.
//
// Every job's KeyshareID must be present in keyPackages or this returns an
// InvalidArgument error.
func (h *FrostSigningHandler) FrostRound2WithKeyPackages(ctx context.Context, req *pb.FrostRound2Request, keyPackages map[uuid.UUID]*pbfrost.KeyPackage) (*pb.FrostRound2Response, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	if len(req.GetSigningJobs()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "signing_jobs is required")
	}
	for i, job := range req.GetSigningJobs() {
		if job == nil {
			return nil, status.Errorf(codes.InvalidArgument, "signing_jobs[%d] is required", i)
		}
	}
	keyshareIDs, err := uuids.ParseSliceFunc(req.GetSigningJobs(), (*pb.SigningJob).GetKeyshareId)
	if err != nil {
		return nil, err
	}
	for _, keyshareID := range keyshareIDs {
		if keyPackages[keyshareID] == nil {
			return nil, status.Errorf(codes.InvalidArgument, "signing keyshare %s not found", keyshareID)
		}
	}

	// Fetch nonces in one call.
	commitments := make([]frost.SigningCommitment, len(req.GetSigningJobs()))
	seenCommitments := make(map[frost.SigningCommitment]int, len(req.GetSigningJobs()))
	for i, job := range req.GetSigningJobs() {
		commitments[i] = frost.SigningCommitment{}
		err = commitments[i].UnmarshalProto(job.GetCommitments()[h.config.Identifier])
		if err != nil {
			return nil, err
		}
		if prevIndex, ok := seenCommitments[commitments[i]]; ok {
			commitmentHex := hex.EncodeToString(commitments[i].MarshalBinary())
			return nil, fmt.Errorf("duplicate signing nonce commitment %s in request (jobs[%d]=%q, jobs[%d]=%q)", commitmentHex, prevIndex, req.GetSigningJobs()[prevIndex].GetJobId(), i, job.GetJobId())
		}
		seenCommitments[commitments[i]] = i
	}
	nonces, err := ent.GetSigningNoncesForUpdate(ctx, h.config, commitments)
	if err != nil {
		return nil, err
	}

	var signingJobProtos []*pbfrost.FrostSigningJob
	bulkUpdates := make(map[frost.SigningCommitment][]byte)

	// First pass: validate all nonces and collect updates
	for _, job := range req.GetSigningJobs() {
		commitment := frost.SigningCommitment{}
		err = commitment.UnmarshalProto(job.GetCommitments()[h.config.Identifier])
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
	for _, job := range req.GetSigningJobs() {
		keyshareID, err := uuid.Parse(job.GetKeyshareId())
		if err != nil {
			return nil, err
		}
		commitment := frost.SigningCommitment{}
		if err := commitment.UnmarshalProto(job.GetCommitments()[h.config.Identifier]); err != nil {
			return nil, err
		}

		nonceEnt := nonces[commitment]
		nonceObject := nonceEnt.Nonce
		nonceProto, _ := nonceObject.MarshalProto()

		keyPackage := keyPackages[keyshareID]
		signingJobProto := &pbfrost.FrostSigningJob{
			JobId:            job.GetJobId(),
			Message:          job.GetMessage(),
			KeyPackage:       keyPackage,
			VerifyingKey:     job.GetVerifyingKey(),
			Nonce:            nonceProto,
			Commitments:      job.GetCommitments(),
			UserCommitments:  job.GetUserCommitments(),
			AdaptorPublicKey: job.GetAdaptorPublicKey(),
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

	return &pb.FrostRound2Response{Results: round2Response.GetResults()}, nil
}

func retryFingerprint(job *pb.SigningJob) []byte {
	hashState := sha256.New()

	writeBytesCollisionResistant(hashState, job.GetMessage())

	writeBytesCollisionResistant(hashState, job.GetVerifyingKey())

	writeBytesCollisionResistant(hashState, job.GetAdaptorPublicKey())

	if job.GetUserCommitments() != nil {
		writeBytesCollisionResistant(hashState, job.GetUserCommitments().GetHiding())
		writeBytesCollisionResistant(hashState, job.GetUserCommitments().GetBinding())
	}

	hashState.Write(binary.BigEndian.AppendUint64(nil, uint64(len(job.GetCommitments()))))

	for _, operatorIdentifier := range slices.Sorted(maps.Keys(job.GetCommitments())) {
		writeBytesCollisionResistant(hashState, []byte(operatorIdentifier))

		com := job.GetCommitments()[operatorIdentifier]
		if com != nil {
			writeBytesCollisionResistant(hashState, com.GetHiding())
			writeBytesCollisionResistant(hashState, com.GetBinding())
		}
	}

	return hashState.Sum(nil)
}

func writeBytesCollisionResistant(hashState hash.Hash, b []byte) {
	hashState.Write(binary.BigEndian.AppendUint64(nil, uint64(len(b))))
	hashState.Write(b)
}

// ApplyKeysharePackageTweak returns a copy of kp with the additive tweak
// components folded in: SecretShare += secretShareTweak; PublicKey +=
// publicKeyTweak; PublicShares[id] += publicSharesTweak[id]. Same math as
// SigningKeyshare.TweakKeyShare but in-memory only — never touches the DB.
// The on-disk keyshare stays at the pre-tweak value; only this fresh
// KeyPackage observes the post-tweak state.
//
// All three components must be present and publicSharesTweak must cover
// every entry in kp.PublicShares; missing components would produce an
// invalid post-tweak share (silently signs with garbage), which the FROST
// signer only flags at aggregation time with a cryptic error. Surfaced here
// so callers see a clear error before the round-2 call fires.
func ApplyKeysharePackageTweak(kp *pbfrost.KeyPackage, secretShareTweak []byte, publicKeyTweak []byte, publicSharesTweak map[string][]byte) (*pbfrost.KeyPackage, error) {
	if kp == nil {
		return nil, fmt.Errorf("nil key package")
	}
	if len(secretShareTweak) == 0 {
		return nil, fmt.Errorf("missing secret_share_tweak")
	}
	if len(publicKeyTweak) == 0 {
		return nil, fmt.Errorf("missing public_key_tweak")
	}
	if len(publicSharesTweak) != len(kp.GetPublicShares()) {
		return nil, fmt.Errorf("public_shares_tweak has %d entries, expected %d to match key package", len(publicSharesTweak), len(kp.GetPublicShares()))
	}

	secretShare, err := keys.ParsePrivateKey(kp.GetSecretShare())
	if err != nil {
		return nil, fmt.Errorf("parse secret_share: %w", err)
	}
	shareTweak, err := keys.ParsePrivateKey(secretShareTweak)
	if err != nil {
		return nil, fmt.Errorf("parse secret_share_tweak: %w", err)
	}
	newSecretShare := secretShare.Add(shareTweak)

	publicKey, err := keys.ParsePublicKey(kp.GetPublicKey())
	if err != nil {
		return nil, fmt.Errorf("parse public_key: %w", err)
	}
	pubKeyTweak, err := keys.ParsePublicKey(publicKeyTweak)
	if err != nil {
		return nil, fmt.Errorf("parse public_key_tweak: %w", err)
	}
	newPublicKey := publicKey.Add(pubKeyTweak)

	newPublicShares := make(map[string][]byte, len(kp.GetPublicShares()))
	for id, oldShareBytes := range kp.GetPublicShares() {
		shareTweakBytes, ok := publicSharesTweak[id]
		if !ok {
			return nil, fmt.Errorf("public_shares_tweak missing entry for operator %s", id)
		}
		oldShare, err := keys.ParsePublicKey(oldShareBytes)
		if err != nil {
			return nil, fmt.Errorf("parse public_share for %s: %w", id, err)
		}
		shareTweakPub, err := keys.ParsePublicKey(shareTweakBytes)
		if err != nil {
			return nil, fmt.Errorf("parse public_shares_tweak for %s: %w", id, err)
		}
		newPublicShares[id] = oldShare.Add(shareTweakPub).Serialize()
	}

	return &pbfrost.KeyPackage{
		Identifier:   kp.GetIdentifier(),
		SecretShare:  newSecretShare.Serialize(),
		PublicShares: newPublicShares,
		PublicKey:    newPublicKey.Serialize(),
		MinSigners:   kp.GetMinSigners(),
	}, nil
}
