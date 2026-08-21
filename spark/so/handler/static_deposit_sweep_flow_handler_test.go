package handler

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"math/rand/v2"
	"testing"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/authz"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/stretchr/testify/require"
)

// sweepFixture is one static deposit UTXO available to a sweep test. Swaps are
// seeded directly because reaching a settled one through the public flow needs a
// live operator set and FROST signing.
type sweepFixture struct {
	utxo     *ent.Utxo
	outpoint wire.OutPoint
}

func (f sweepFixture) protoUtxo() *pb.UTXO {
	return &pb.UTXO{Txid: f.utxo.Txid, Vout: f.utxo.Vout, Network: pb.Network_REGTEST}
}

// Each index yields a distinct address and txid so one test can hold several.
func createSweepFixture(t *testing.T, ctx context.Context, client *ent.Client, rng io.Reader, index int, amount uint64) sweepFixture {
	t.Helper()

	keyshare := createTestSigningKeyshare(t, ctx, rng, client)
	depositAddress, err := client.DepositAddress.Create().
		SetAddress(fmt.Sprintf("bc1ptest_sweep_deposit_address_%d", index)).
		SetOwnerIdentityPubkey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
		SetOwnerSigningPubkey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
		SetSigningKeyshare(keyshare).
		SetIsStatic(true).
		Save(ctx)
	require.NoError(t, err)

	txid := chainhash.DoubleHashB(fmt.Appendf(nil, "sweep-deposit-%d", index))
	utxo, err := client.Utxo.Create().
		SetNetwork(btcnetwork.Regtest).
		SetTxid(txid).
		SetVout(0).
		SetBlockHeight(100).
		SetAmount(amount).
		SetPkScript([]byte("test_pk_script")).
		SetDepositAddress(depositAddress).
		Save(ctx)
	require.NoError(t, err)

	return sweepFixture{utxo: utxo, outpoint: outpointForTxid(t, txid, 0)}
}

// Stored txids are the hex of wire-order bytes, the reverse of display order.
func outpointForTxid(t *testing.T, txid []byte, vout uint32) wire.OutPoint {
	t.Helper()
	hash, err := chainhash.NewHashFromStr(hex.EncodeToString(txid))
	require.NoError(t, err)
	return wire.OutPoint{Hash: *hash, Index: vout}
}

// sweepSignature is the caller authorization every operator verifies.
func sweepSignature(t *testing.T, rawTx []byte, sspKey keys.Private) []byte {
	t.Helper()
	return sweepSignatureFor(t, btcnetwork.Regtest, rawTx, sspKey)
}

func sweepSignatureFor(t *testing.T, network btcnetwork.Network, rawTx []byte, sspKey keys.Private) []byte {
	t.Helper()
	tx, err := common.TxFromRawTxBytes(rawTx)
	require.NoError(t, err)
	return ecdsa.Sign(sspKey.ToBTCEC(), CreateStaticDepositSweepStatement(network, tx.TxHash())).Serialize()
}

func createSweepSwap(t *testing.T, ctx context.Context, client *ent.Client, rng io.Reader, fixture sweepFixture, status st.UtxoSwapStatus, requestType st.UtxoSwapRequestType, sspIdentityPubKey keys.Public) {
	t.Helper()

	_, err := client.UtxoSwap.Create().
		SetStatus(status).
		SetUtxo(fixture.utxo).
		SetUtxoValueSats(fixture.utxo.Amount).
		SetRequestType(requestType).
		SetCreditAmountSats(fixture.utxo.Amount).
		SetSspSignature(fmt.Appendf(nil, "ssp-signature-%s", fixture.outpoint)).
		SetSspIdentityPublicKey(sspIdentityPubKey).
		SetUserIdentityPublicKey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
		SetCoordinatorIdentityPublicKey(keys.MustGeneratePrivateKeyFromRand(rng).Public()).
		Save(ctx)
	require.NoError(t, err)
}

func buildSweepTx(t *testing.T, fixtures []sweepFixture, totalOut int64) []byte {
	t.Helper()

	tx := wire.NewMsgTx(3)
	for _, fixture := range fixtures {
		tx.AddTxIn(&wire.TxIn{PreviousOutPoint: fixture.outpoint, Sequence: wire.MaxTxInSequenceNum})
	}
	tx.AddTxOut(wire.NewTxOut(totalOut, []byte("ssp_wallet_pk_script")))

	raw, err := common.SerializeTx(tx)
	require.NoError(t, err)
	return raw
}

// internalSweepInputsFor builds the prepare-op inputs a participant receives,
// with a stand-in operator commitment set so the signing-set membership check
// can be exercised without a signer.
func internalSweepInputsFor(t *testing.T, rng io.Reader, fixtures []sweepFixture, operatorID string) []*pbinternal.StaticDepositSweepInput {
	t.Helper()

	inputs := make([]*pbinternal.StaticDepositSweepInput, 0, len(fixtures))
	for i, fixture := range fixtures {
		commitment := &pbcommon.SigningCommitment{
			Hiding:  keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize(),
			Binding: keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize(),
		}
		inputs = append(inputs, &pbinternal.StaticDepositSweepInput{
			OnChainUtxo:           fixture.protoUtxo(),
			Vin:                   uint32(i),
			UserSigningCommitment: commitment,
			SigningCommitments:    map[string]*pbcommon.SigningCommitment{operatorID: commitment},
		})
	}
	return inputs
}

func newSweepPrepareTest(t *testing.T) (context.Context, *ent.Client, *rand.ChaCha8, keys.Private, *so.Config) {
	t.Helper()

	ctx, sessionCtx := db.ConnectToTestPostgres(t)
	rng := rand.NewChaCha8([32]byte{})
	sspKey := keys.MustGeneratePrivateKeyFromRand(rng)
	return ctx, sessionCtx.Client, rng, sspKey, setUpTestConfigWithRegtestNoAuthz(t)
}

// Prepare is the operator's own vote on whether the sweep may be signed. The
// coordinator has already accepted these inputs, so a refusal here means the
// operators disagree — it must abort rather than sign.

func TestStaticDepositSweepPrepareAbortsOnUnsettledSwap(t *testing.T) {
	ctx, client, rng, sspKey, cfg := newSweepPrepareTest(t)
	ssp := sspKey.Public()

	fixtures := []sweepFixture{createSweepFixture(t, ctx, client, rng, 0, 10000)}
	createSweepSwap(t, ctx, client, rng, fixtures[0], st.UtxoSwapStatusCreated, st.UtxoSwapRequestTypeFixedAmount, ssp)

	rawTx := buildSweepTx(t, fixtures, 9000)

	_, err := NewStaticDepositSweepFlowHandler(cfg).Prepare(ctx, &pbinternal.StaticDepositSweepPrepareRequest{
		Network:              pb.Network_REGTEST,
		RawTx:                rawTx,
		Inputs:               internalSweepInputsFor(t, rng, fixtures, cfg.Identifier),
		SspIdentityPublicKey: ssp.Serialize(),
		SspSignature:         sweepSignature(t, rawTx, sspKey),
	})

	require.ErrorContains(t, err, "not sweepable by this operator")
	require.ErrorContains(t, err, "swap not completed")
}

func TestStaticDepositSweepPrepareAbortsOnRefundSwap(t *testing.T) {
	ctx, client, rng, sspKey, cfg := newSweepPrepareTest(t)
	ssp := sspKey.Public()

	fixtures := []sweepFixture{createSweepFixture(t, ctx, client, rng, 0, 10000)}
	createSweepSwap(t, ctx, client, rng, fixtures[0], st.UtxoSwapStatusCompleted, st.UtxoSwapRequestTypeRefund, ssp)

	rawTx := buildSweepTx(t, fixtures, 9000)

	_, err := NewStaticDepositSweepFlowHandler(cfg).Prepare(ctx, &pbinternal.StaticDepositSweepPrepareRequest{
		Network:              pb.Network_REGTEST,
		RawTx:                rawTx,
		Inputs:               internalSweepInputsFor(t, rng, fixtures, cfg.Identifier),
		SspIdentityPublicKey: ssp.Serialize(),
		SspSignature:         sweepSignature(t, rawTx, sspKey),
	})

	require.ErrorContains(t, err, "refund swap")
}

// A coordinator asserting the wrong caller cannot reach a swap that settled for
// somebody else: every operator re-checks the owner against its own row.
func TestStaticDepositSweepPrepareAbortsWhenSwapSettledForAnotherSsp(t *testing.T) {
	ctx, client, rng, sspKey, cfg := newSweepPrepareTest(t)
	ssp := sspKey.Public()

	fixtures := []sweepFixture{createSweepFixture(t, ctx, client, rng, 0, 10000)}
	otherSsp := keys.MustGeneratePrivateKeyFromRand(rng).Public()
	createSweepSwap(t, ctx, client, rng, fixtures[0], st.UtxoSwapStatusCompleted, st.UtxoSwapRequestTypeFixedAmount, otherSsp)

	rawTx := buildSweepTx(t, fixtures, 9000)

	_, err := NewStaticDepositSweepFlowHandler(cfg).Prepare(ctx, &pbinternal.StaticDepositSweepPrepareRequest{
		Network:              pb.Network_REGTEST,
		RawTx:                rawTx,
		Inputs:               internalSweepInputsFor(t, rng, fixtures, cfg.Identifier),
		SspIdentityPublicKey: ssp.Serialize(),
		SspSignature:         sweepSignature(t, rawTx, sspKey),
	})

	require.ErrorContains(t, err, "not owned by caller")
}

// The prepare op is re-bound to the transaction on every operator, so a payload
// naming UTXOs the transaction does not spend is refused cluster-wide.
func TestStaticDepositSweepPrepareAbortsWhenTxSpendsADifferentUtxo(t *testing.T) {
	ctx, client, rng, sspKey, cfg := newSweepPrepareTest(t)
	ssp := sspKey.Public()

	owned := createSweepFixture(t, ctx, client, rng, 0, 10000)
	createSweepSwap(t, ctx, client, rng, owned, st.UtxoSwapStatusCompleted, st.UtxoSwapRequestTypeFixedAmount, ssp)
	someoneElses := createSweepFixture(t, ctx, client, rng, 1, 10000)

	rawTx := buildSweepTx(t, []sweepFixture{someoneElses}, 9000)

	_, err := NewStaticDepositSweepFlowHandler(cfg).Prepare(ctx, &pbinternal.StaticDepositSweepPrepareRequest{
		Network:              pb.Network_REGTEST,
		RawTx:                rawTx,
		Inputs:               internalSweepInputsFor(t, rng, []sweepFixture{owned}, cfg.Identifier),
		SspIdentityPublicKey: ssp.Serialize(),
		SspSignature:         sweepSignature(t, rawTx, sspKey),
	})

	require.ErrorContains(t, err, "but inputs[0] describes")
}

func TestStaticDepositSweepPrepareAbortsOnOutputsExceedingInputs(t *testing.T) {
	ctx, client, rng, sspKey, cfg := newSweepPrepareTest(t)
	ssp := sspKey.Public()

	fixtures := []sweepFixture{createSweepFixture(t, ctx, client, rng, 0, 10000)}
	createSweepSwap(t, ctx, client, rng, fixtures[0], st.UtxoSwapStatusCompleted, st.UtxoSwapRequestTypeFixedAmount, ssp)

	rawTx := buildSweepTx(t, fixtures, 10001)

	_, err := NewStaticDepositSweepFlowHandler(cfg).Prepare(ctx, &pbinternal.StaticDepositSweepPrepareRequest{
		Network:              pb.Network_REGTEST,
		RawTx:                rawTx,
		Inputs:               internalSweepInputsFor(t, rng, fixtures, cfg.Identifier),
		SspIdentityPublicKey: ssp.Serialize(),
		SspSignature:         sweepSignature(t, rawTx, sspKey),
	})

	require.ErrorContains(t, err, "spends 10001 sats but its inputs are worth 10000")
}

// An operator outside the coordinator's round-1 set still validates and votes,
// it just contributes no share.
func TestStaticDepositSweepPrepareOutsideSigningSetReturnsNoShare(t *testing.T) {
	ctx, client, rng, sspKey, cfg := newSweepPrepareTest(t)
	ssp := sspKey.Public()

	fixtures := []sweepFixture{createSweepFixture(t, ctx, client, rng, 0, 10000)}
	createSweepSwap(t, ctx, client, rng, fixtures[0], st.UtxoSwapStatusCompleted, st.UtxoSwapRequestTypeFixedAmount, ssp)

	rawTx := buildSweepTx(t, fixtures, 9000)

	result, err := NewStaticDepositSweepFlowHandler(cfg).Prepare(ctx, &pbinternal.StaticDepositSweepPrepareRequest{
		Network:              pb.Network_REGTEST,
		RawTx:                rawTx,
		Inputs:               internalSweepInputsFor(t, rng, fixtures, "some-other-operator"),
		SspIdentityPublicKey: ssp.Serialize(),
		SspSignature:         sweepSignature(t, rawTx, sspKey),
	})

	require.NoError(t, err)
	require.Nil(t, result)
}

// Nothing was locked or written, so both decisions are no-ops on every operator.
func TestStaticDepositSweepCommitAndRollbackAreNoOps(t *testing.T) {
	ctx, _ := db.ConnectToTestPostgres(t)
	handler := NewStaticDepositSweepFlowHandler(setUpTestConfigWithRegtestNoAuthz(t))

	require.NoError(t, handler.Commit(ctx, &pbinternal.StaticDepositSweepCommitRequest{SweepTxid: []byte("txid")}))
	require.NoError(t, handler.Rollback(ctx, &pbinternal.StaticDepositSweepRollbackRequest{SweepTxid: []byte("txid")}))
	require.NoError(t, handler.Rollback(ctx, &pbinternal.StaticDepositSweepPrepareRequest{}))
}

// Signing a sweep leaves its swaps settled and sweepable, which is what lets a
// stalled sweep be re-signed at a higher fee. Asserted at the eligibility layer
// because that is the only thing that could refuse the second attempt.
func TestStaticDepositSweepInputsStaySweepableForAReplacementTx(t *testing.T) {
	ctx, client, rng, sspKey, cfg := newSweepPrepareTest(t)
	ssp := sspKey.Public()

	fixtures := []sweepFixture{createSweepFixture(t, ctx, client, rng, 0, 10000)}
	createSweepSwap(t, ctx, client, rng, fixtures[0], st.UtxoSwapStatusCompleted, st.UtxoSwapRequestTypeFixedAmount, ssp)
	inputs := internalSweepInputsFor(t, rng, fixtures, cfg.Identifier)

	firstTx := buildSweepTx(t, fixtures, 9000)
	first, refusals, err := PrepareSweep(ctx, btcnetwork.Regtest, firstTx, inputs, ssp, sweepSignature(t, firstTx, sspKey))
	require.NoError(t, err)
	require.Empty(t, refusals)
	require.Len(t, first.inputs, 1)

	secondTx := buildSweepTx(t, fixtures, 8000)
	second, refusals, err := PrepareSweep(ctx, btcnetwork.Regtest, secondTx, inputs, ssp, sweepSignature(t, secondTx, sspKey))
	require.NoError(t, err)
	require.Empty(t, refusals)
	require.Len(t, second.inputs, 1)

	firstJobID := first.inputs[0].jobID()
	require.Equal(t, firstJobID, second.inputs[0].jobID())
	require.NotEqual(t, first.sighashes[firstJobID], second.sighashes[firstJobID],
		"a replacement transaction must be signed over a different message")
}

// Resolution must not depend on the order the caller lists its inputs, so
// overlapping sweeps — a fee replacement and the sweep it replaces — reach the
// same verdict whichever way each was assembled.
func TestStaticDepositSweepResolvesInputsRegardlessOfOrder(t *testing.T) {
	ctx, client, rng, sspKey, cfg := newSweepPrepareTest(t)
	ssp := sspKey.Public()

	fixtures := []sweepFixture{
		createSweepFixture(t, ctx, client, rng, 0, 10000),
		createSweepFixture(t, ctx, client, rng, 1, 10000),
	}
	for _, fixture := range fixtures {
		createSweepSwap(t, ctx, client, rng, fixture, st.UtxoSwapStatusCompleted, st.UtxoSwapRequestTypeFixedAmount, ssp)
	}

	forwardTx := buildSweepTx(t, fixtures, 19000)
	forward, refusals, err := PrepareSweep(ctx, btcnetwork.Regtest, forwardTx, internalSweepInputsFor(t, rng, fixtures, cfg.Identifier), ssp, sweepSignature(t, forwardTx, sspKey))
	require.NoError(t, err)
	require.Empty(t, refusals)

	reversedFixtures := []sweepFixture{fixtures[1], fixtures[0]}
	reversedTx := buildSweepTx(t, reversedFixtures, 19000)
	reversed, refusals, err := PrepareSweep(ctx, btcnetwork.Regtest, reversedTx, internalSweepInputsFor(t, rng, reversedFixtures, cfg.Identifier), ssp, sweepSignature(t, reversedTx, sspKey))
	require.NoError(t, err)
	require.Empty(t, refusals)

	// Assert the reversal was real rather than normalised away by the caller.
	require.Len(t, forward.inputs, 2)
	require.Len(t, reversed.inputs, 2)
	require.NotEqual(t, forward.inputs[0].outpoint, reversed.inputs[0].outpoint)
}

// The consensus channel authenticates an operator, not the SSP session, so a
// coordinator that names a different owner must fail on every participant.
func TestStaticDepositSweepPrepareAbortsOnSubstitutedSspIdentity(t *testing.T) {
	ctx, client, rng, sspKey, cfg := newSweepPrepareTest(t)
	ssp := sspKey.Public()

	fixtures := []sweepFixture{createSweepFixture(t, ctx, client, rng, 0, 10000)}
	createSweepSwap(t, ctx, client, rng, fixtures[0], st.UtxoSwapStatusCompleted, st.UtxoSwapRequestTypeFixedAmount, ssp)
	rawTx := buildSweepTx(t, fixtures, 9000)

	dishonestCoordinator := keys.MustGeneratePrivateKeyFromRand(rng)
	_, err := NewStaticDepositSweepFlowHandler(cfg).Prepare(ctx, &pbinternal.StaticDepositSweepPrepareRequest{
		Network:              pb.Network_REGTEST,
		RawTx:                rawTx,
		Inputs:               internalSweepInputsFor(t, rng, fixtures, cfg.Identifier),
		SspIdentityPublicKey: ssp.Serialize(),
		SspSignature:         sweepSignature(t, rawTx, dishonestCoordinator),
	})

	require.ErrorContains(t, err, "ssp sweep signature validation failed")
}

// The signature commits to the txid, so it cannot be lifted onto another sweep.
func TestStaticDepositSweepPrepareAbortsWhenSignatureIsForAnotherTx(t *testing.T) {
	ctx, client, rng, sspKey, cfg := newSweepPrepareTest(t)
	ssp := sspKey.Public()

	fixtures := []sweepFixture{createSweepFixture(t, ctx, client, rng, 0, 10000)}
	createSweepSwap(t, ctx, client, rng, fixtures[0], st.UtxoSwapStatusCompleted, st.UtxoSwapRequestTypeFixedAmount, ssp)

	authorized := buildSweepTx(t, fixtures, 9000)
	substituted := buildSweepTx(t, fixtures, 8000)
	_, err := NewStaticDepositSweepFlowHandler(cfg).Prepare(ctx, &pbinternal.StaticDepositSweepPrepareRequest{
		Network:              pb.Network_REGTEST,
		RawTx:                substituted,
		Inputs:               internalSweepInputsFor(t, rng, fixtures, cfg.Identifier),
		SspIdentityPublicKey: ssp.Serialize(),
		SspSignature:         sweepSignature(t, authorized, sspKey),
	})

	require.ErrorContains(t, err, "ssp sweep signature validation failed")
}

// The statement commits to the network, so a signature cannot be replayed from
// one network onto another.
func TestStaticDepositSweepPrepareAbortsOnSignatureForAnotherNetwork(t *testing.T) {
	ctx, client, rng, sspKey, cfg := newSweepPrepareTest(t)
	ssp := sspKey.Public()

	fixtures := []sweepFixture{createSweepFixture(t, ctx, client, rng, 0, 10000)}
	createSweepSwap(t, ctx, client, rng, fixtures[0], st.UtxoSwapStatusCompleted, st.UtxoSwapRequestTypeFixedAmount, ssp)
	rawTx := buildSweepTx(t, fixtures, 9000)

	_, err := NewStaticDepositSweepFlowHandler(cfg).Prepare(ctx, &pbinternal.StaticDepositSweepPrepareRequest{
		Network:              pb.Network_REGTEST,
		RawTx:                rawTx,
		Inputs:               internalSweepInputsFor(t, rng, fixtures, cfg.Identifier),
		SspIdentityPublicKey: ssp.Serialize(),
		SspSignature:         sweepSignatureFor(t, btcnetwork.Mainnet, rawTx, sspKey),
	})

	require.ErrorContains(t, err, "ssp sweep signature validation failed")
}

// Round-1 membership is all-or-nothing across a sweep, so a payload including
// this operator on only some inputs is malformed rather than half-signable.
func TestStaticDepositSweepPrepareAbortsOnPartialSigningSetMembership(t *testing.T) {
	ctx, client, rng, sspKey, cfg := newSweepPrepareTest(t)
	ssp := sspKey.Public()

	fixtures := []sweepFixture{
		createSweepFixture(t, ctx, client, rng, 0, 10000),
		createSweepFixture(t, ctx, client, rng, 1, 10000),
	}
	for _, fixture := range fixtures {
		createSweepSwap(t, ctx, client, rng, fixture, st.UtxoSwapStatusCompleted, st.UtxoSwapRequestTypeFixedAmount, ssp)
	}
	inputs := internalSweepInputsFor(t, rng, fixtures, cfg.Identifier)
	delete(inputs[1].GetSigningCommitments(), cfg.Identifier)

	rawTx := buildSweepTx(t, fixtures, 19000)
	_, err := NewStaticDepositSweepFlowHandler(cfg).Prepare(ctx, &pbinternal.StaticDepositSweepPrepareRequest{
		Network:              pb.Network_REGTEST,
		RawTx:                rawTx,
		Inputs:               inputs,
		SspIdentityPublicKey: ssp.Serialize(),
		SspSignature:         sweepSignature(t, rawTx, sspKey),
	})

	require.ErrorContains(t, err, "present on some sweep inputs but not others")
}

// Freezing an SSP has to stop the sweep on every operator, not just wherever the
// request happened to land — it is the only control that still bites once a
// session token is out and valid.
func TestStaticDepositSweepPrepareBlocksKillSwitchedSsp(t *testing.T) {
	ctx, client, rng, sspKey, cfg := newSweepPrepareTest(t)
	ssp := sspKey.Public()

	fixtures := []sweepFixture{createSweepFixture(t, ctx, client, rng, 0, 10000)}
	createSweepSwap(t, ctx, client, rng, fixtures[0], st.UtxoSwapStatusCompleted, st.UtxoSwapRequestTypeFixedAmount, ssp)
	rawTx := buildSweepTx(t, fixtures, 9000)

	killSwitched := knobs.InjectKnobsService(ctx, knobs.NewFixedKnobs(map[string]float64{
		knobs.KnobKillSwitchWallet + "@" + ssp.ToHex(): 1,
	}))
	_, err := NewStaticDepositSweepFlowHandler(cfg).Prepare(killSwitched, &pbinternal.StaticDepositSweepPrepareRequest{
		Network:              pb.Network_REGTEST,
		RawTx:                rawTx,
		Inputs:               internalSweepInputsFor(t, rng, fixtures, cfg.Identifier),
		SspIdentityPublicKey: ssp.Serialize(),
		SspSignature:         sweepSignature(t, rawTx, sspKey),
	})

	require.Error(t, err)
	var authzErr *authz.Error
	require.ErrorAs(t, err, &authzErr)
	require.Equal(t, authz.ErrorCodeWalletKillSwitched, authzErr.Code)
}

// The public cap cannot be re-applied to a prepare op — it arrives inside an Any,
// so the generated validator never sees its type — leaving this the one bound a
// participant has to enforce itself.
func TestStaticDepositSweepPrepareRejectsMoreInputsThanTheCap(t *testing.T) {
	ctx, _, rng, sspKey, cfg := newSweepPrepareTest(t)
	ssp := sspKey.Public()

	oversized := make([]*pbinternal.StaticDepositSweepInput, 0, maxSweepInputs+1)
	for i := range maxSweepInputs + 1 {
		oversized = append(oversized, &pbinternal.StaticDepositSweepInput{
			OnChainUtxo: &pb.UTXO{
				Txid:    chainhash.DoubleHashB(fmt.Appendf(nil, "oversized-%d", i)),
				Vout:    0,
				Network: pb.Network_REGTEST,
			},
			Vin: uint32(i),
			UserSigningCommitment: &pbcommon.SigningCommitment{
				Hiding:  keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize(),
				Binding: keys.MustGeneratePrivateKeyFromRand(rng).Public().Serialize(),
			},
		})
	}

	// A one-input transaction: the cap must be refused before anything walks the
	// inputs, so the transaction never needs to match.
	rawTx := buildSweepTx(t, []sweepFixture{{outpoint: outpointForTxid(t, chainhash.DoubleHashB([]byte("oversized-0")), 0)}}, 1000)
	_, err := NewStaticDepositSweepFlowHandler(cfg).Prepare(ctx, &pbinternal.StaticDepositSweepPrepareRequest{
		Network:              pb.Network_REGTEST,
		RawTx:                rawTx,
		Inputs:               oversized,
		SspIdentityPublicKey: ssp.Serialize(),
		SspSignature:         sweepSignature(t, rawTx, sspKey),
	})

	require.ErrorContains(t, err, fmt.Sprintf("more than the %d allowed", maxSweepInputs))
}
