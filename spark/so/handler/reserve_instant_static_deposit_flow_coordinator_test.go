//go:build lightspark

package handler

import (
	"context"
	"math"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbssp "github.com/lightsparkdev/spark/proto/spark_ssp_internal"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// These tests exercise the coordinator entrypoint's pre-engine fast-fail
// validation on the instant static deposit reserve path — the fund-moving
// branches that run before consensus.GetEngine/engine.Execute, so no live
// multi-SO engine is needed. Full multi-SO behavior is covered by the
// minikube-gated grpc_test integration tests.

// enableInstantKnob injects a knobs service with the instant static deposit
// knob turned on for REGTEST, matching the entrypoint's per-network lookup.
func enableInstantKnob(ctx context.Context) context.Context {
	return knobs.InjectKnobsService(ctx, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobEnableInstantStaticDeposit + "@REGTEST": 1,
	}))
}

// validRegtestUtxo returns a REGTEST UTXO with a valid 32-byte txid that does
// not correspond to any persisted Utxo row, so the soft UTXO check resolves to
// "not found" (nil) and falls through to the amount/address/leaf/signature
// checks under test.
func validRegtestUtxo() *pb.UTXO {
	return &pb.UTXO{Network: pb.Network_REGTEST, Txid: make([]byte, 32), Vout: 0}
}

// TestReserveInstantStaticDepositUtxoSwapConsensus_ErrorIfMissingTransferPackage
// covers the structural transfer_package presence check, which sits below the
// enabled-knob and duplicate gates so those report their own errors first.
func TestReserveInstantStaticDepositUtxoSwapConsensus_ErrorIfMissingTransferPackage(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	ctx = enableInstantKnob(ctx)
	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	handler := NewStaticDepositHandler(cfg)
	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)

	req := &pbssp.ReserveInstantStaticDepositUtxoSwapRequest{
		OnChainUtxo: validRegtestUtxo(),
		Transfer:    &pb.StartTransferRequest{}, // no TransferPackage
	}

	_, err := handler.reserveInstantStaticDepositUtxoSwapConsensus(ctx, cfg, req)
	require.ErrorContains(t, err, "transfer_package is required")
}

// TestReserveInstantStaticDepositUtxoSwapConsensus_ErrorIfNotEnabled covers
// branch #1: the instant-enabled knob gate.
func TestReserveInstantStaticDepositUtxoSwapConsensus_ErrorIfNotEnabled(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	handler := NewStaticDepositHandler(cfg)

	// No knob injected → instant deposit is disabled.
	req := &pbssp.ReserveInstantStaticDepositUtxoSwapRequest{
		OnChainUtxo: validRegtestUtxo(),
		Transfer:    &pb.StartTransferRequest{TransferPackage: &pb.TransferPackage{}},
	}

	_, err := handler.reserveInstantStaticDepositUtxoSwapConsensus(ctx, cfg, req)
	require.ErrorContains(t, err, "instant static deposit is not enabled")
}

// TestReserveInstantStaticDepositUtxoSwapConsensus_ErrorIfAmountsInvalid covers
// branch #2: value_sats must be positive and credit amounts non-negative.
func TestReserveInstantStaticDepositUtxoSwapConsensus_ErrorIfAmountsInvalid(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)
	ctx = enableInstantKnob(ctx)

	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	handler := NewStaticDepositHandler(cfg)

	req := &pbssp.ReserveInstantStaticDepositUtxoSwapRequest{
		OnChainUtxo: validRegtestUtxo(),
		Transfer:    &pb.StartTransferRequest{TransferPackage: &pb.TransferPackage{}},
		ValueSats:   0, // must be positive
	}

	_, err := handler.reserveInstantStaticDepositUtxoSwapConsensus(ctx, cfg, req)
	require.ErrorContains(t, err, "amounts must be non-negative and value_sats must be positive")
}

// TestReserveInstantStaticDepositUtxoSwapConsensus_ErrorIfCreditOverflow covers
// branch #3: an overflow-safe headroom check. A naive credit+secondary sum
// overflows int64 to a negative value that would slip past a `> value_sats`
// cap; the per-term headroom comparison must still reject it. A secondary
// transfer id is set so the overflow check (which runs first) is what rejects
// it, not the later cross-field invariant.
func TestReserveInstantStaticDepositUtxoSwapConsensus_ErrorIfCreditOverflow(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)
	ctx = enableInstantKnob(ctx)

	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	handler := NewStaticDepositHandler(cfg)

	req := &pbssp.ReserveInstantStaticDepositUtxoSwapRequest{
		OnChainUtxo:                  validRegtestUtxo(),
		Transfer:                     &pb.StartTransferRequest{TransferPackage: &pb.TransferPackage{}},
		ValueSats:                    10000,
		CreditAmountSats:             1,
		SecondaryCreditAmountSats:    math.MaxInt64,
		RequestedSecondaryTransferId: uuid.NewString(),
	}

	_, err := handler.reserveInstantStaticDepositUtxoSwapConsensus(ctx, cfg, req)
	require.ErrorContains(t, err, "total credit amount exceeds value_sats")
}

// TestReserveInstantStaticDepositUtxoSwapConsensus_ErrorIfSecondaryTransferIdWithoutAmount
// covers branch #4: a requested_secondary_transfer_id with no secondary amount.
func TestReserveInstantStaticDepositUtxoSwapConsensus_ErrorIfSecondaryTransferIdWithoutAmount(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)
	ctx = enableInstantKnob(ctx)

	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	handler := NewStaticDepositHandler(cfg)

	req := &pbssp.ReserveInstantStaticDepositUtxoSwapRequest{
		OnChainUtxo:                  validRegtestUtxo(),
		Transfer:                     &pb.StartTransferRequest{TransferPackage: &pb.TransferPackage{}},
		ValueSats:                    10000,
		CreditAmountSats:             1000,
		SecondaryCreditAmountSats:    0,
		RequestedSecondaryTransferId: uuid.NewString(),
	}

	_, err := handler.reserveInstantStaticDepositUtxoSwapConsensus(ctx, cfg, req)
	require.ErrorContains(t, err, "without secondary_credit_amount_sats")
}

// TestReserveInstantStaticDepositUtxoSwapConsensus_ErrorIfSecondaryAmountWithoutTransferId
// covers branch #5: a secondary amount with no requested_secondary_transfer_id.
func TestReserveInstantStaticDepositUtxoSwapConsensus_ErrorIfSecondaryAmountWithoutTransferId(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)
	ctx = enableInstantKnob(ctx)

	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	handler := NewStaticDepositHandler(cfg)

	req := &pbssp.ReserveInstantStaticDepositUtxoSwapRequest{
		OnChainUtxo:               validRegtestUtxo(),
		Transfer:                  &pb.StartTransferRequest{TransferPackage: &pb.TransferPackage{}},
		ValueSats:                 10000,
		CreditAmountSats:          0,
		SecondaryCreditAmountSats: 500,
		// no RequestedSecondaryTransferId
	}

	_, err := handler.reserveInstantStaticDepositUtxoSwapConsensus(ctx, cfg, req)
	require.ErrorContains(t, err, "without requested_secondary_transfer_id")
}

// TestReserveInstantStaticDepositUtxoSwapConsensus_ErrorIfDepositAddressNotFound
// covers branch #6: the destination address is not a known static deposit
// address for the receiver.
func TestReserveInstantStaticDepositUtxoSwapConsensus_ErrorIfDepositAddressNotFound(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)
	ctx = enableInstantKnob(ctx)

	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	handler := NewStaticDepositHandler(cfg)

	receiverIdentityPubKey := keys.GeneratePrivateKey().Public()
	req := &pbssp.ReserveInstantStaticDepositUtxoSwapRequest{
		OnChainUtxo: validRegtestUtxo(),
		Transfer: &pb.StartTransferRequest{
			ReceiverIdentityPublicKey: receiverIdentityPubKey.Serialize(),
			TransferPackage:           &pb.TransferPackage{},
		},
		DestinationAddress: "bc1p_unknown_destination_address",
		ValueSats:          10000,
		CreditAmountSats:   1000,
	}

	_, err := handler.reserveInstantStaticDepositUtxoSwapConsensus(ctx, cfg, req)
	require.ErrorContains(t, err, "not found")
}

// TestReserveInstantStaticDepositUtxoSwapConsensus_ErrorIfLeavesNotLoaded covers
// the pre-engine leaf-load fast-fail on the fund-moving path: a transfer with no
// leaves cannot be loaded. Note this exercises the loadLeaves failure at
// coordinator line 195-197 ("unable to load leaves"), not the immediately
// following len==0 guard ("no leaves found", line 199-201): loadLeaves returns
// an error ("no network found") for the zero-leaf case, so that guard is never
// reached with a real leaf set — see the report note on branch #7.
func TestReserveInstantStaticDepositUtxoSwapConsensus_ErrorIfLeavesNotLoaded(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)
	ctx = enableInstantKnob(ctx)

	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	handler := NewStaticDepositHandler(cfg)

	rng := rand.NewChaCha8([32]byte{9})
	receiverIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	ownerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	keyshare := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)
	depositAddress := createTestStaticDepositAddress(t, ctx, sessionCtx.Client, keyshare, receiverIdentityPubKey, ownerSigningPubKey)

	req := &pbssp.ReserveInstantStaticDepositUtxoSwapRequest{
		OnChainUtxo: validRegtestUtxo(),
		Transfer: &pb.StartTransferRequest{
			ReceiverIdentityPublicKey: receiverIdentityPubKey.Serialize(),
			TransferPackage:           &pb.TransferPackage{}, // no leaves
		},
		DestinationAddress: depositAddress.Address,
		ValueSats:          10000,
		CreditAmountSats:   0,
	}

	_, err := handler.reserveInstantStaticDepositUtxoSwapConsensus(ctx, cfg, req)
	require.ErrorContains(t, err, "unable to load leaves")
}

// TestReserveInstantStaticDepositUtxoSwapConsensus_ErrorIfTotalLeafAmountMismatch
// covers branch #8: the summed leaf value must equal credit_amount_sats.
func TestReserveInstantStaticDepositUtxoSwapConsensus_ErrorIfTotalLeafAmountMismatch(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)
	ctx = enableInstantKnob(ctx)

	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	handler := NewStaticDepositHandler(cfg)

	rng := rand.NewChaCha8([32]byte{11})
	receiverIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiverIdentityPubKey := receiverIdentityPrivKey.Public()
	ownerIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	ownerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	keyshare := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)
	depositAddress := createTestStaticDepositAddress(t, ctx, sessionCtx.Client, keyshare, receiverIdentityPubKey, ownerSigningPubKey)
	tree := createTestTreeForClaim(t, ctx, ownerIdentityPubKey, sessionCtx.Client)
	leaf := createTestTreeNodeForStaticDeposit(t, ctx, rng, sessionCtx.Client, tree, keyshare, ownerIdentityPubKey, ownerSigningPubKey)

	req := &pbssp.ReserveInstantStaticDepositUtxoSwapRequest{
		OnChainUtxo: validRegtestUtxo(),
		Transfer: &pb.StartTransferRequest{
			TransferId:                uuid.NewString(),
			ReceiverIdentityPublicKey: receiverIdentityPubKey.Serialize(),
			ExpiryTime:                timestamppb.New(time.Now().Add(24 * time.Hour)),
			TransferPackage: &pb.TransferPackage{
				LeavesToSend: []*pb.UserSignedTxSigningJob{
					createUserSignedTxSigningJob(rng, leaf.ID, leaf.RawRefundTx, cfg.Identifier),
				},
			},
		},
		DestinationAddress: depositAddress.Address,
		ValueSats:          10000,
		CreditAmountSats:   int64(leaf.Value) - 1, // != total leaf value
	}

	_, err := handler.reserveInstantStaticDepositUtxoSwapConsensus(ctx, cfg, req)
	require.ErrorContains(t, err, "does not match credit_amount_sats")
}

// TestReserveInstantStaticDepositUtxoSwapConsensus_ErrorIfUserSignatureInvalid
// covers branch #9: the instant user signature fast-fail. All prior checks pass
// (amounts, address, matching leaf total) so validation reaches — and rejects —
// the malformed signature before any cross-operator work.
func TestReserveInstantStaticDepositUtxoSwapConsensus_ErrorIfUserSignatureInvalid(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	createTestBlockHeight(t, ctx, sessionCtx.Client, 100)
	ctx = enableInstantKnob(ctx)

	cfg := setUpTestConfigWithRegtestNoAuthz(t)
	handler := NewStaticDepositHandler(cfg)

	rng := rand.NewChaCha8([32]byte{13})
	receiverIdentityPrivKey := keys.MustGeneratePrivateKeyFromRand(rng)
	receiverIdentityPubKey := receiverIdentityPrivKey.Public()
	ownerIdentityPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	ownerSigningPubKey := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	keyshare := createTestSigningKeyshare(t, ctx, rng, sessionCtx.Client)
	depositAddress := createTestStaticDepositAddress(t, ctx, sessionCtx.Client, keyshare, receiverIdentityPubKey, ownerSigningPubKey)
	tree := createTestTreeForClaim(t, ctx, ownerIdentityPubKey, sessionCtx.Client)
	leaf := createTestTreeNodeForStaticDeposit(t, ctx, rng, sessionCtx.Client, tree, keyshare, ownerIdentityPubKey, ownerSigningPubKey)

	req := &pbssp.ReserveInstantStaticDepositUtxoSwapRequest{
		OnChainUtxo:   validRegtestUtxo(),
		SspSignature:  []byte("test_ssp_signature"),
		UserSignature: []byte("not_a_valid_ecdsa_signature"),
		Transfer: &pb.StartTransferRequest{
			TransferId:                uuid.NewString(),
			ReceiverIdentityPublicKey: receiverIdentityPubKey.Serialize(),
			ExpiryTime:                timestamppb.New(time.Now().Add(24 * time.Hour)),
			TransferPackage: &pb.TransferPackage{
				LeavesToSend: []*pb.UserSignedTxSigningJob{
					createUserSignedTxSigningJob(rng, leaf.ID, leaf.RawRefundTx, cfg.Identifier),
				},
			},
		},
		DestinationAddress: depositAddress.Address,
		ValueSats:          10000,
		CreditAmountSats:   int64(leaf.Value), // matches total leaf value
	}

	_, err := handler.reserveInstantStaticDepositUtxoSwapConsensus(ctx, cfg, req)
	require.ErrorContains(t, err, "user signature validation failed")
}
