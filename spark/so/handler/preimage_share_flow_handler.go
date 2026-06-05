package handler

import (
	"bytes"
	"context"
	dbSql "database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	eciesgo "github.com/ecies/go/v2"
	"github.com/lightsparkdev/spark/common/keys"
	secretsharing "github.com/lightsparkdev/spark/common/secret_sharing"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authz"
	"github.com/lightsparkdev/spark/so/consensus"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/preimageshare"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	decodepay "github.com/nbd-wtf/ln-decodepay"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// ---------------------------------------------------------------------------
// PreimageShareFlowHandler — participant side (Prepare / Commit / Rollback)
// ---------------------------------------------------------------------------

var _ consensus.FlowHandler = (*PreimageShareFlowHandler)(nil)

type PreimageShareFlowHandler struct {
	config *so.Config
}

func NewPreimageShareFlowHandler(config *so.Config) *PreimageShareFlowHandler {
	return &PreimageShareFlowHandler{config: config}
}

// Prepare validates the preimage share (decrypt, VSS validate, invoice verify)
// and writes it to DB immediately. This ensures each SO persists the share it
// independently validated — a malicious coordinator cannot substitute a different
// share in the Commit phase.
func (h *PreimageShareFlowHandler) Prepare(ctx context.Context, op proto.Message) (proto.Message, error) {
	req, ok := op.(*pbinternal.StorePreimageSharePrepareRequest)
	if !ok {
		return nil, fmt.Errorf("unexpected operation type %T for preimage share prepare", op)
	}

	origReq := req.GetOriginalRequest()
	if origReq == nil {
		return nil, fmt.Errorf("original_request is required")
	}

	secretShare, err := validatePreimageShare(h.config, origReq)
	if err != nil {
		return nil, err
	}

	if err := writePreimageShare(ctx, origReq, secretShare); err != nil {
		return nil, fmt.Errorf("failed to write preimage share during prepare: %w", err)
	}

	return nil, nil
}

// Commit is a no-op — the share was already written during Prepare.
// The write happens in Prepare so each SO persists the share it validated,
// preventing a malicious coordinator from substituting a different share.
func (h *PreimageShareFlowHandler) Commit(_ context.Context, _ proto.Message) error {
	return nil
}

// Rollback is a no-op. The share written during Prepare is idempotent (upsert
// with DoNothing on conflict) and harmless if it lingers — a retry will succeed.
// Deleting would risk removing a pre-existing legitimate share that Prepare
// didn't actually insert (the DoNothing path).
func (h *PreimageShareFlowHandler) Rollback(_ context.Context, _ proto.Message) error {
	return nil
}

// ---------------------------------------------------------------------------
// preimageShareCoordinatorFlow — coordinator side
// ---------------------------------------------------------------------------

var _ consensus.CoordinatorFlow = (*preimageShareCoordinatorFlow)(nil)

type preimageShareCoordinatorFlow struct {
	*PreimageShareFlowHandler // embeds Prepare/Commit/Rollback

	prepareReq *pbinternal.StorePreimageSharePrepareRequest
}

func (f *preimageShareCoordinatorFlow) PrepareOp() proto.Message {
	return f.prepareReq
}

// BuildCommitPayload is a no-op — no signing to aggregate and shares are
// already written during Prepare. Returns the prepare request as commit payload.
func (f *preimageShareCoordinatorFlow) BuildCommitPayload(_ context.Context, _ map[string]*anypb.Any) (proto.Message, error) {
	return f.prepareReq, nil
}

func (f *preimageShareCoordinatorFlow) RollbackPayload() proto.Message {
	return &pbinternal.StorePreimageSharePrepareRequest{}
}

// ---------------------------------------------------------------------------
// Validation + DB write (split from decryptAndStorePreimageShare)
// ---------------------------------------------------------------------------

// validatePreimageShare decrypts and validates a preimage share without writing
// to DB. Returns the decrypted SecretShare for writePreimageShare to persist.
func validatePreimageShare(config *so.Config, req *pb.StorePreimageShareV2Request) (*pb.SecretShare, error) {
	ciphertext, ok := req.GetEncryptedPreimageShares()[config.Identifier]
	if !ok {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("no encrypted preimage share found for SO %s", config.Identifier))
	}

	decryptionPrivateKey := eciesgo.NewPrivateKeyFromBytes(config.IdentityPrivateKey.Serialize())
	plaintext, err := eciesgo.Decrypt(decryptionPrivateKey, ciphertext)
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("failed to decrypt preimage share: %w", err))
	}

	secretShare := &pb.SecretShare{}
	if err := proto.Unmarshal(plaintext, secretShare); err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("failed to unmarshal preimage share: %w", err))
	}

	if len(secretShare.GetProofs()) == 0 {
		return nil, sparkerrors.InvalidArgumentMissingField(fmt.Errorf("preimage share proofs is empty"))
	}

	if uint64(req.GetThreshold()) != config.Threshold {
		return nil, sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("threshold mismatch: expected %d, got %d", config.Threshold, req.GetThreshold()))
	}

	err = secretsharing.ValidateShare(
		&secretsharing.VerifiableSecretShare{
			SecretShare: secretsharing.SecretShare{
				FieldModulus: secp256k1.S256().N,
				Threshold:    int(config.Threshold),
				Index:        big.NewInt(int64(config.Index + 1)),
				Share:        new(big.Int).SetBytes(secretShare.GetSecretShare()),
			},
			Proofs: secretShare.GetProofs(),
		},
	)
	if err != nil {
		return nil, sparkerrors.FailedPreconditionBadSignature(fmt.Errorf("unable to validate share: %w", err))
	}

	bolt11, err := decodepay.Decodepay(req.GetInvoiceString())
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("unable to decode invoice: %w", err))
	}

	paymentHash, err := hex.DecodeString(bolt11.PaymentHash)
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("unable to decode payment hash: %w", err))
	}

	if !bytes.Equal(paymentHash, req.GetPaymentHash()) {
		return nil, sparkerrors.FailedPreconditionHashMismatch(fmt.Errorf("payment hash mismatch"))
	}

	return secretShare, nil
}

// writePreimageShare writes the already-validated and decrypted secret share to DB.
func writePreimageShare(ctx context.Context, req *pb.StorePreimageShareV2Request, secretShare *pb.SecretShare) error {
	tx, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to get db from context: %w", err)
	}

	userIdentityPubKey, err := keys.ParsePublicKey(req.GetUserIdentityPublicKey())
	if err != nil {
		return sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("unable to parse user identity public key: %w", err))
	}
	// No session-identity check on the V2 path either: LNURL/hosted callers
	// may store on behalf of the user. Apply the wallet kill switch on the
	// affected user identity directly so an SSP (or any other caller) cannot
	// store shares for a frozen wallet on this SO. Runs on every SO during
	// the consensus Prepare phase, so a frozen identity aborts the run on all
	// operators rather than just the coordinator.
	if err := authz.EnforceWalletNotKillSwitched(ctx, userIdentityPubKey); err != nil {
		return err
	}

	err = tx.PreimageShare.Create().
		SetPaymentHash(req.GetPaymentHash()).
		SetPreimageShare(secretShare.GetSecretShare()).
		SetThreshold(int32(req.GetThreshold())).
		SetInvoiceString(req.GetInvoiceString()).
		SetOwnerIdentityPubkey(userIdentityPubKey).
		OnConflictColumns(preimageshare.FieldPaymentHash).
		DoNothing().
		Exec(ctx)
	if err != nil && !errors.Is(err, dbSql.ErrNoRows) {
		return fmt.Errorf("unable to store preimage share: %w", err)
	}

	return nil
}
