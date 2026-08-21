//go:build lightspark

package handler

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"math/rand/v2"
	"testing"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbssp "github.com/lightsparkdev/spark/proto/spark_ssp_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authn"
	"github.com/lightsparkdev/spark/so/authz"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/stretchr/testify/require"
)

func sweepInputsFor(t *testing.T, rng io.Reader, fixtures []sweepFixture) []*pbssp.SweepInput {
	t.Helper()

	inputs := make([]*pbssp.SweepInput, 0, len(fixtures))
	for i, fixture := range fixtures {
		inputs = append(inputs, &pbssp.SweepInput{
			OnChainUtxo: fixture.protoUtxo(),
			Vin:         uint32(i),
			UserSigningCommitment: &pbcommon.SigningCommitment{
				Hiding:  keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize(),
				Binding: keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize(),
			},
		})
	}
	return inputs
}

func newSweepTest(t *testing.T) (context.Context, *ent.Client, *rand.ChaCha8, keys.Private, *SspRequestHandler) {
	t.Helper()

	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	rng := rand.NewChaCha8([32]byte{})
	sspKey := keys.MustGeneratePrivateKeyFromRand(rng)
	ctx = authn.InjectSessionForTests(ctx, sspKey.Public(), 0)
	return ctx, sessionCtx.Client, rng, sspKey, NewSspRequestHandler(setUpTestConfigWithRegtestNoAuthz(t))
}

func TestSignStaticDepositSweepTxRejectsNilRequest(t *testing.T) {
	handler := NewSspRequestHandler(&so.Config{})

	resp, err := handler.SignStaticDepositSweepTx(t.Context(), nil)

	require.Nil(t, resp)
	require.ErrorContains(t, err, "request is required")
}

func TestSignStaticDepositSweepTxRequiresSession(t *testing.T) {
	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	rng := rand.NewChaCha8([32]byte{})
	sspKey := keys.MustGeneratePrivateKeyFromRand(rng)
	handler := NewSspRequestHandler(setUpTestConfigWithRegtestNoAuthz(t))

	fixtures := []sweepFixture{createSweepFixture(t, ctx, sessionCtx.Client, rng, 0, 10000)}

	rawTx := buildSweepTx(t, fixtures, 9000)
	resp, err := handler.SignStaticDepositSweepTx(ctx, &pbssp.SignStaticDepositSweepTxRequest{
		Network:      pb.Network_REGTEST,
		RawTx:        rawTx,
		Inputs:       sweepInputsFor(t, rng, fixtures),
		SspSignature: sweepSignature(t, rawTx, sspKey),
	})

	require.Nil(t, resp)
	require.Error(t, err)
}

func TestSignStaticDepositSweepTxRejectsInputCountMismatch(t *testing.T) {
	ctx, client, rng, sspKey, handler := newSweepTest(t)

	fixtures := []sweepFixture{
		createSweepFixture(t, ctx, client, rng, 0, 10000),
		createSweepFixture(t, ctx, client, rng, 1, 10000),
	}

	rawTx := buildSweepTx(t, fixtures, 19000)
	resp, err := handler.SignStaticDepositSweepTx(ctx, &pbssp.SignStaticDepositSweepTxRequest{
		Network:      pb.Network_REGTEST,
		RawTx:        rawTx,
		Inputs:       sweepInputsFor(t, rng, fixtures[:1]),
		SspSignature: sweepSignature(t, rawTx, sspKey),
	})

	require.Nil(t, resp)
	require.ErrorContains(t, err, "spends 2 inputs but 1 were described")
}

func TestSignStaticDepositSweepTxRejectsInputDescribingADifferentUtxo(t *testing.T) {
	ctx, client, rng, sspKey, handler := newSweepTest(t)
	ssp := sspKey.Public()

	owned := createSweepFixture(t, ctx, client, rng, 0, 10000)
	createSweepSwap(t, ctx, client, rng, owned, st.UtxoSwapStatusCompleted, st.UtxoSwapRequestTypeFixedAmount, ssp)
	someoneElses := createSweepFixture(t, ctx, client, rng, 1, 10000)

	rawTx := buildSweepTx(t, []sweepFixture{someoneElses}, 9000)
	resp, err := handler.SignStaticDepositSweepTx(ctx, &pbssp.SignStaticDepositSweepTxRequest{
		Network:      pb.Network_REGTEST,
		RawTx:        rawTx,
		Inputs:       sweepInputsFor(t, rng, []sweepFixture{owned}),
		SspSignature: sweepSignature(t, rawTx, sspKey),
	})

	require.Nil(t, resp)
	require.ErrorContains(t, err, "but inputs[0] describes")
}

func TestSignStaticDepositSweepTxRejectsDuplicateVin(t *testing.T) {
	ctx, client, rng, sspKey, handler := newSweepTest(t)

	fixtures := []sweepFixture{
		createSweepFixture(t, ctx, client, rng, 0, 10000),
		createSweepFixture(t, ctx, client, rng, 1, 10000),
	}
	inputs := sweepInputsFor(t, rng, fixtures)
	inputs[1].Vin = 0

	rawTx := buildSweepTx(t, fixtures, 19000)
	resp, err := handler.SignStaticDepositSweepTx(ctx, &pbssp.SignStaticDepositSweepTxRequest{
		Network:      pb.Network_REGTEST,
		RawTx:        rawTx,
		Inputs:       inputs,
		SspSignature: sweepSignature(t, rawTx, sspKey),
	})

	require.Nil(t, resp)
	require.ErrorContains(t, err, "repeats vin 0")
}

func TestSignStaticDepositSweepTxRejectsVinOutOfRange(t *testing.T) {
	ctx, client, rng, sspKey, handler := newSweepTest(t)

	fixtures := []sweepFixture{createSweepFixture(t, ctx, client, rng, 0, 10000)}
	inputs := sweepInputsFor(t, rng, fixtures)
	inputs[0].Vin = 7

	rawTx := buildSweepTx(t, fixtures, 9000)
	resp, err := handler.SignStaticDepositSweepTx(ctx, &pbssp.SignStaticDepositSweepTxRequest{
		Network:      pb.Network_REGTEST,
		RawTx:        rawTx,
		Inputs:       inputs,
		SspSignature: sweepSignature(t, rawTx, sspKey),
	})

	require.Nil(t, resp)
	require.ErrorContains(t, err, "out of range")
}

func TestSignStaticDepositSweepTxReportsIneligibleInputsWithoutSigning(t *testing.T) {
	ctx, client, rng, sspKey, handler := newSweepTest(t)
	ssp := sspKey.Public()
	otherSsp := keys.MustGeneratePrivateKeyFromRand(rng).Public()

	settled := createSweepFixture(t, ctx, client, rng, 0, 10000)
	createSweepSwap(t, ctx, client, rng, settled, st.UtxoSwapStatusCompleted, st.UtxoSwapRequestTypeFixedAmount, ssp)

	noSwap := createSweepFixture(t, ctx, client, rng, 1, 10000)

	inFlight := createSweepFixture(t, ctx, client, rng, 2, 10000)
	createSweepSwap(t, ctx, client, rng, inFlight, st.UtxoSwapStatusCreated, st.UtxoSwapRequestTypeFixedAmount, ssp)

	refunded := createSweepFixture(t, ctx, client, rng, 3, 10000)
	createSweepSwap(t, ctx, client, rng, refunded, st.UtxoSwapStatusCompleted, st.UtxoSwapRequestTypeRefund, keys.MustGeneratePrivateKeyFromRand(rng).Public())

	otherOwner := createSweepFixture(t, ctx, client, rng, 4, 10000)
	createSweepSwap(t, ctx, client, rng, otherOwner, st.UtxoSwapStatusCompleted, st.UtxoSwapRequestTypeInstant, otherSsp)

	unknownTxid := chainhash.DoubleHashB([]byte("never-seen-by-this-operator"))
	unknown := sweepFixture{
		utxo:     &ent.Utxo{Txid: unknownTxid, Vout: 0, Amount: 10000},
		outpoint: outpointForTxid(t, unknownTxid, 0),
	}

	fixtures := []sweepFixture{settled, noSwap, inFlight, refunded, otherOwner, unknown}
	rawTx := buildSweepTx(t, fixtures, 50000)
	resp, err := handler.SignStaticDepositSweepTx(ctx, &pbssp.SignStaticDepositSweepTxRequest{
		Network:      pb.Network_REGTEST,
		RawTx:        rawTx,
		Inputs:       sweepInputsFor(t, rng, fixtures),
		SspSignature: sweepSignature(t, rawTx, sspKey),
	})

	require.NoError(t, err)
	require.Nil(t, resp.GetSigned())
	require.NotNil(t, resp.GetIneligible())

	reasons := make(map[string]pbssp.SweepIneligibleReason, len(resp.GetIneligible().GetInputs()))
	for _, in := range resp.GetIneligible().GetInputs() {
		reasons[hex.EncodeToString(in.GetOnChainUtxo().GetTxid())] = in.GetReason()
	}
	require.Len(t, reasons, 5)
	require.NotContains(t, reasons, hex.EncodeToString(settled.utxo.Txid))
	require.Equal(t, pbssp.SweepIneligibleReason_SWEEP_INELIGIBLE_REASON_NO_SWAP, reasons[hex.EncodeToString(noSwap.utxo.Txid)])
	require.Equal(t, pbssp.SweepIneligibleReason_SWEEP_INELIGIBLE_REASON_SWAP_NOT_COMPLETED, reasons[hex.EncodeToString(inFlight.utxo.Txid)])
	require.Equal(t, pbssp.SweepIneligibleReason_SWEEP_INELIGIBLE_REASON_REFUND_SWAP, reasons[hex.EncodeToString(refunded.utxo.Txid)])
	require.Equal(t, pbssp.SweepIneligibleReason_SWEEP_INELIGIBLE_REASON_NOT_OWNED_BY_CALLER, reasons[hex.EncodeToString(otherOwner.utxo.Txid)])
	require.Equal(t, pbssp.SweepIneligibleReason_SWEEP_INELIGIBLE_REASON_UNKNOWN_UTXO, reasons[hex.EncodeToString(unknown.utxo.Txid)])
}

func TestSignStaticDepositSweepTxRefusesRefundSwapEvenWhenOwnedByCaller(t *testing.T) {
	ctx, client, rng, sspKey, handler := newSweepTest(t)
	ssp := sspKey.Public()

	fixtures := []sweepFixture{createSweepFixture(t, ctx, client, rng, 0, 10000)}
	createSweepSwap(t, ctx, client, rng, fixtures[0], st.UtxoSwapStatusCompleted, st.UtxoSwapRequestTypeRefund, ssp)

	rawTx := buildSweepTx(t, fixtures, 9000)
	resp, err := handler.SignStaticDepositSweepTx(ctx, &pbssp.SignStaticDepositSweepTxRequest{
		Network:      pb.Network_REGTEST,
		RawTx:        rawTx,
		Inputs:       sweepInputsFor(t, rng, fixtures),
		SspSignature: sweepSignature(t, rawTx, sspKey),
	})

	require.NoError(t, err)
	require.Nil(t, resp.GetSigned())
	require.Len(t, resp.GetIneligible().GetInputs(), 1)
	require.Equal(t, pbssp.SweepIneligibleReason_SWEEP_INELIGIBLE_REASON_REFUND_SWAP, resp.GetIneligible().GetInputs()[0].GetReason())
}

func TestSignStaticDepositSweepTxRejectsOutputsExceedingInputs(t *testing.T) {
	ctx, client, rng, sspKey, handler := newSweepTest(t)
	ssp := sspKey.Public()

	fixtures := []sweepFixture{
		createSweepFixture(t, ctx, client, rng, 0, 10000),
		createSweepFixture(t, ctx, client, rng, 1, 10000),
	}
	for _, fixture := range fixtures {
		createSweepSwap(t, ctx, client, rng, fixture, st.UtxoSwapStatusCompleted, st.UtxoSwapRequestTypeFixedAmount, ssp)
	}

	rawTx := buildSweepTx(t, fixtures, 20001)
	resp, err := handler.SignStaticDepositSweepTx(ctx, &pbssp.SignStaticDepositSweepTxRequest{
		Network:      pb.Network_REGTEST,
		RawTx:        rawTx,
		Inputs:       sweepInputsFor(t, rng, fixtures),
		SspSignature: sweepSignature(t, rawTx, sspKey),
	})

	require.Nil(t, resp)
	require.ErrorContains(t, err, "spends 20001 sats but its inputs are worth 20000")
}

func TestSignStaticDepositSweepTxScopesUtxoLookupToRequestNetwork(t *testing.T) {
	ctx, client, rng, sspKey, handler := newSweepTest(t)
	ssp := sspKey.Public()

	fixtures := []sweepFixture{createSweepFixture(t, ctx, client, rng, 0, 10000)}
	createSweepSwap(t, ctx, client, rng, fixtures[0], st.UtxoSwapStatusCompleted, st.UtxoSwapRequestTypeFixedAmount, ssp)

	inputs := sweepInputsFor(t, rng, fixtures)
	inputs[0].OnChainUtxo.Network = pb.Network_MAINNET

	rawTx := buildSweepTx(t, fixtures, 9000)
	resp, err := handler.SignStaticDepositSweepTx(ctx, &pbssp.SignStaticDepositSweepTxRequest{
		Network:      pb.Network_MAINNET,
		RawTx:        rawTx,
		Inputs:       inputs,
		SspSignature: sweepSignatureFor(t, btcnetwork.Mainnet, rawTx, sspKey),
	})

	require.NoError(t, err)
	require.Nil(t, resp.GetSigned())
	require.Len(t, resp.GetIneligible().GetInputs(), 1)
	require.Equal(t, pbssp.SweepIneligibleReason_SWEEP_INELIGIBLE_REASON_UNKNOWN_UTXO, resp.GetIneligible().GetInputs()[0].GetReason())
}

// The 200-input cap bounds the FROST fan-out one call can trigger, so both sides
// of it are pinned by generated request validation.
func TestSignStaticDepositSweepTxInputCapBoundary(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{})
	inputAt := func(i int) *pbssp.SweepInput {
		return &pbssp.SweepInput{
			OnChainUtxo: &pb.UTXO{Txid: chainhash.DoubleHashB(fmt.Appendf(nil, "cap-%d", i)), Vout: 0, Network: pb.Network_REGTEST},
			Vin:         uint32(i),
			UserSigningCommitment: &pbcommon.SigningCommitment{
				Hiding:  keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize(),
				Binding: keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize(),
			},
		}
	}
	requestWith := func(n int) *pbssp.SignStaticDepositSweepTxRequest {
		inputs := make([]*pbssp.SweepInput, 0, n)
		for i := range n {
			inputs = append(inputs, inputAt(i))
		}
		return &pbssp.SignStaticDepositSweepTxRequest{
			Network:      pb.Network_REGTEST,
			RawTx:        []byte{0x01},
			Inputs:       inputs,
			SspSignature: []byte{0x01},
		}
	}

	require.NoError(t, requestWith(200).Validate())
	require.ErrorContains(t, requestWith(201).Validate(), "Inputs")
	require.ErrorContains(t, requestWith(0).Validate(), "Inputs")
}

// A malformed verifying key must not reach the caller's FROST key package.
func TestSweepInputSigningResultRejectsMalformedVerifyingKey(t *testing.T) {
	result := &pbssp.SweepInputSigningResult{Vin: 0, VerifyingKey: []byte{0x02, 0x03}}
	require.ErrorContains(t, result.Validate(), "VerifyingKey")

	result.VerifyingKey = keys.GeneratePrivateKey().Public().Serialize()
	require.NoError(t, result.Validate())
}

// The coordinator-side half of the kill switch: a frozen SSP with a still-valid
// session must be refused before any cross-operator work begins.
func TestSignStaticDepositSweepTxBlocksKillSwitchedSsp(t *testing.T) {
	ctx, client, rng, sspKey, handler := newSweepTest(t)
	ssp := sspKey.Public()

	fixtures := []sweepFixture{createSweepFixture(t, ctx, client, rng, 0, 10000)}
	createSweepSwap(t, ctx, client, rng, fixtures[0], st.UtxoSwapStatusCompleted, st.UtxoSwapRequestTypeFixedAmount, ssp)

	killSwitched := knobs.InjectKnobsService(ctx, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobKillSwitchWallet + "@" + ssp.ToHex(): 1,
	}))
	rawTx := buildSweepTx(t, fixtures, 9000)
	resp, err := handler.SignStaticDepositSweepTx(killSwitched, &pbssp.SignStaticDepositSweepTxRequest{
		Network:      pb.Network_REGTEST,
		RawTx:        rawTx,
		Inputs:       sweepInputsFor(t, rng, fixtures),
		SspSignature: sweepSignature(t, rawTx, sspKey),
	})

	require.Nil(t, resp)
	var authzErr *authz.Error
	require.ErrorAs(t, err, &authzErr)
	require.Equal(t, authz.ErrorCodeWalletKillSwitched, authzErr.Code)
}
