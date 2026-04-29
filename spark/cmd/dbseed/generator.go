package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"math/rand/v2"
	"time"

	"github.com/google/uuid"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
)

// transferRow is the subset of columns the generator writes.
// Ordering here matches the COPY column list in seed.go — don't reorder.
type transferRow struct {
	id                     uuid.UUID
	createTime             time.Time
	updateTime             time.Time
	senderIdentityPubkey   []byte
	receiverIdentityPubkey []byte
	network                string
	totalValue             int64
	status                 st.TransferStatus
	transferType           st.TransferType
	expiryTime             time.Time
	// completionTime, spark_invoice_id — written as NULL.
}

// senderRow / receiverRow — same field set on the DB side except receivers have
// status and completion_time. We emit separate slices per table.
type senderRow struct {
	id             uuid.UUID
	createTime     time.Time
	updateTime     time.Time
	transferID     uuid.UUID
	identityPubkey []byte
}

type receiverRow struct {
	id             uuid.UUID
	createTime     time.Time
	updateTime     time.Time
	transferID     uuid.UUID
	identityPubkey []byte
	status         st.TransferReceiverStatus
}

// generator emits rows for one wallet's contribution to the three tables.
// Each wallet gets its own generator so progress and memory are bounded.
type generator struct {
	rng         *rand.Rand
	wallet      walletID
	cfg         *Config
	statusCDF   []int
	statusOrder []st.TransferStatus
	statusSum   int
	typeCDF     []int
	typeOrder   []st.TransferType
	typeSum     int
	baseTime    time.Time
	spanNanos   int64
}

// walletID identifies one wallet (tier + index-within-tier).
type walletID struct {
	tierLabel string
	tierIdx   int // index within the tier (0 for single-wallet tiers)
	globalIdx int // sequential global index — used to derive a unique pubkey
}

// newGenerator constructs a per-wallet generator. Each wallet seed derives from
// the global seed + globalIdx so runs are deterministic and wallets don't
// collide when parallelized.
func newGenerator(cfg *Config, w walletID, globalSeed int64) *generator {
	g := &generator{
		wallet:    w,
		cfg:       cfg,
		baseTime:  time.Now().UTC().Add(-time.Duration(cfg.CreateTimeSpanDays) * 24 * time.Hour),
		spanNanos: int64(cfg.CreateTimeSpanDays) * 24 * int64(time.Hour),
	}
	// Derive a 64-bit seed per wallet.
	h := sha256.Sum256([]byte(
		string(rune(globalSeed)) + "|" + w.tierLabel + "|" +
			string(rune(w.tierIdx)) + "|" + string(rune(w.globalIdx))))
	s1 := binary.LittleEndian.Uint64(h[:8])
	s2 := binary.LittleEndian.Uint64(h[8:16])
	g.rng = rand.New(rand.NewPCG(s1, s2))

	g.statusSum, g.statusCDF, g.statusOrder = cumulativeStatus(cfg.TransferStatuses)
	g.typeSum, g.typeCDF, g.typeOrder = cumulativeType(cfg.TransferTypes)
	return g
}

// receiverStatusForTransfer maps a transfer's status to the corresponding
// receiver status, modeling the state-machine progression where the receiver
// row tracks the transfer it belongs to:
//
//   - Sender-side pending (pre-tweak) → INITIATED. The sender hasn't
//     completed its key-tweak handoff yet, so the receiver can't act.
//   - SENDER_KEY_TWEAKED → RECEIVER_CLAIM_PENDING. The sender finished;
//     the receiver should now claim.
//   - The four post-claim RECEIVER_* transfer statuses → their 1:1
//     receiver-side counterpart (this is the "stuck receiver" range —
//     receiver started claiming but didn't finish).
//   - COMPLETED → COMPLETED.
//   - RETURNED / EXPIRED → CANCELLED. There's no receiver-side
//     RETURNED/EXPIRED enum value, so both terminal-failure transfer
//     states collapse onto the receiver's CANCELLED.
//
// Deterministic — keeps transfer.status and transfer_receivers.status
// consistent in seeded data instead of sampling them independently. Plan
// validation against EXISTS-style joins or any predicate that hops the two
// status columns is now meaningful.
func receiverStatusForTransfer(s st.TransferStatus) st.TransferReceiverStatus {
	switch s {
	case st.TransferStatusCompleted:
		return st.TransferReceiverStatusCompleted
	case st.TransferStatusReturned, st.TransferStatusExpired:
		return st.TransferReceiverStatusCancelled
	case st.TransferStatusSenderInitiated,
		st.TransferStatusSenderInitiatedCoordinator,
		st.TransferStatusSenderKeyTweakPending,
		st.TransferStatusApplyingSenderKeyTweak:
		return st.TransferReceiverStatusSenderInitiated
	case st.TransferStatusSenderKeyTweaked:
		return st.TransferReceiverStatusReceiverClaimPending
	case st.TransferStatusReceiverKeyTweaked:
		return st.TransferReceiverStatusKeyTweaked
	case st.TransferStatusReceiverKeyTweakLocked:
		return st.TransferReceiverStatusKeyTweakLocked
	case st.TransferStatusReceiverKeyTweakApplied:
		return st.TransferReceiverStatusKeyTweakApplied
	case st.TransferStatusReceiverRefundSigned:
		return st.TransferReceiverStatusRefundSigned
	}
	// All TransferStatus values are enumerated above; if a new value is
	// added to the schematype enum, the dbseed build will compile but the
	// mapping won't cover it. Catching that at runtime is preferable to
	// silently emitting a wrong receiver state.
	panic("dbseed: unmapped TransferStatus " + string(s) + " — extend receiverStatusForTransfer")
}

// cumulativeStatus / cumulativeType return the total weight, prefix-sum
// CDF, and the value slice in declaration order. The CDF + order pair is
// what pickFromCDF needs to sample without holding a reference to the
// original weight slice — used by both the tier-driven emit() (CDFs cached
// on the generator) and the WalletGroup-driven emitPhase() (CDFs built
// per-phase). Receiver status is not sampled — see
// receiverStatusForTransfer.
func cumulativeStatus(w []StatusWeight) (int, []int, []st.TransferStatus) {
	cdf := make([]int, len(w))
	order := make([]st.TransferStatus, len(w))
	sum := 0
	for i, x := range w {
		sum += x.Weight
		cdf[i] = sum
		order[i] = x.Status
	}
	return sum, cdf, order
}

func cumulativeType(w []TypeWeight) (int, []int, []st.TransferType) {
	cdf := make([]int, len(w))
	order := make([]st.TransferType, len(w))
	sum := 0
	for i, x := range w {
		sum += x.Weight
		cdf[i] = sum
		order[i] = x.Type
	}
	return sum, cdf, order
}

// phaseCDFs bundles the two caller-built distributions emitPhase samples
// from. Bundling avoids passing the underlying triplets as positional args.
// Receiver status is derived from the picked transfer status via
// receiverStatusForTransfer, so it's not sampled here.
type phaseCDFs struct {
	statusSum   int
	statusCDF   []int
	statusOrder []st.TransferStatus
	typeSum     int
	typeCDF     []int
	typeOrder   []st.TransferType
}

// newPhaseCDFs builds the CDF bundle for a phase. Cheap — phase weight
// slices are at most a handful of entries each.
func newPhaseCDFs(phase WalletPhase) phaseCDFs {
	statusSum, statusCDF, statusOrder := cumulativeStatus(phase.TransferStatuses)
	typeSum, typeCDF, typeOrder := cumulativeType(phase.TransferTypes)
	return phaseCDFs{
		statusSum:   statusSum,
		statusCDF:   statusCDF,
		statusOrder: statusOrder,
		typeSum:     typeSum,
		typeCDF:     typeCDF,
		typeOrder:   typeOrder,
	}
}

// pubkey returns a deterministic 33-byte compressed-pubkey-shaped value for a
// wallet. Not a real EC point — just a unique bytea that matches the storage
// shape. keys.Public stores the raw bytes and we bypass that validation by
// writing directly to Postgres.
func pubkey(globalIdx int) []byte {
	h := sha256.Sum256(binary.BigEndian.AppendUint64(nil, uint64(globalIdx)))
	// First byte 0x02 or 0x03 matches compressed-pubkey convention.
	pk := make([]byte, 33)
	pk[0] = 0x02 | byte(globalIdx&1)
	copy(pk[1:], h[:])
	return pk
}

// counterpartyPubkey returns the pubkey of the *other* participant for a
// transfer originating from the given wallet. Strategy: pick a wallet from the
// long tail deterministically per-row so counter-parties look heterogeneous.
// The specific choice matters little for plan testing — what matters is that
// a wallet's edges don't all point at the same other pubkey (which would let
// the planner make bogus correlation estimates).
func (g *generator) counterpartyPubkey(rowIdx int) []byte {
	// Span of pseudo-counter-parties: the first 10_000 globalIdx slots in the
	// long tail range. We're not constrained to actually having those wallets
	// exist in the edge tables; the pubkey only lands in transfers as the
	// "other side" field.
	counterIdx := 100_000 + int(g.rng.Uint64()%10_000)
	_ = rowIdx
	return pubkey(counterIdx)
}

// emit generates all rows this wallet contributes and delivers them to the
// three channel sinks. Closes nothing — shared channels are closed by the
// orchestrator after all wallets finish. Returns ctx.Err() if the context is
// canceled mid-emit (e.g. a COPY goroutine failed) so the caller can drop
// out instead of deadlocking on the channel.
func (g *generator) emit(
	ctx context.Context,
	rowCount int,
	selfIsReceiver bool,
	transferCh chan<- transferRow,
	senderCh chan<- senderRow,
	receiverCh chan<- receiverRow,
) error {
	selfPk := pubkey(g.wallet.globalIdx)
	for i := range rowCount {
		tid, err := uuid.NewV7()
		if err != nil {
			// NewV7 only fails on clock errors; fall back.
			tid = uuid.New()
		}
		ct := g.randomCreateTime()
		status := g.pickStatus()
		ttype := g.pickType()

		var senderPk, receiverPk []byte
		if selfIsReceiver {
			senderPk = g.counterpartyPubkey(i)
			receiverPk = selfPk
		} else {
			senderPk = selfPk
			receiverPk = g.counterpartyPubkey(i)
		}

		tr := transferRow{
			id:                     tid,
			createTime:             ct,
			updateTime:             ct,
			senderIdentityPubkey:   senderPk,
			receiverIdentityPubkey: receiverPk,
			network:                g.cfg.Network,
			totalValue:             int64(1_000 + g.rng.Uint64()%1_000_000),
			status:                 status,
			transferType:           ttype,
			expiryTime:             g.randomExpiry(ct, status),
		}
		sr := senderRow{
			id:             g.newRowID(),
			createTime:     ct,
			updateTime:     ct,
			transferID:     tid,
			identityPubkey: senderPk,
		}
		rr := receiverRow{
			id:             g.newRowID(),
			createTime:     ct,
			updateTime:     ct,
			transferID:     tid,
			identityPubkey: receiverPk,
			status:         receiverStatusForTransfer(status),
		}
		select {
		case transferCh <- tr:
		case <-ctx.Done():
			return ctx.Err()
		}
		select {
		case senderCh <- sr:
		case <-ctx.Done():
			return ctx.Err()
		}
		select {
		case receiverCh <- rr:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// emitPhase generates rows for one WalletPhase. Differs from emit() in three
// ways: (1) the row count is exact rather than rng-sampled from a tier range,
// (2) the wallet's role on each row is a fixed PhaseRoleSender or
// PhaseRoleReceiver rather than the half/half tier split, and (3) the status
// and type distributions come from the phase (via cdfs) rather than the
// Config defaults cached on the generator — which is the whole reason
// WalletPhase exists.
//
// Receiver status, as in emit(), is derived from the picked transfer status
// via receiverStatusForTransfer rather than sampled independently.
func (g *generator) emitPhase(
	ctx context.Context,
	count int,
	role PhaseRole,
	network string,
	cdfs phaseCDFs,
	transferCh chan<- transferRow,
	senderCh chan<- senderRow,
	receiverCh chan<- receiverRow,
) error {
	selfPk := pubkey(g.wallet.globalIdx)
	for i := range count {
		tid, err := uuid.NewV7()
		if err != nil {
			tid = uuid.New()
		}
		ct := g.randomCreateTime()
		// Phase-local CDF sampling. Same shape as g.pickStatus / pickType
		// but using the caller-supplied CDF bundle.
		status := pickFromCDF(g.rng.IntN(cdfs.statusSum), cdfs.statusCDF, cdfs.statusOrder)
		ttype := pickFromCDF(g.rng.IntN(cdfs.typeSum), cdfs.typeCDF, cdfs.typeOrder)

		var senderPk, receiverPk []byte
		if role == PhaseRoleReceiver {
			senderPk = g.counterpartyPubkey(i)
			receiverPk = selfPk
		} else {
			senderPk = selfPk
			receiverPk = g.counterpartyPubkey(i)
		}

		tr := transferRow{
			id:                     tid,
			createTime:             ct,
			updateTime:             ct,
			senderIdentityPubkey:   senderPk,
			receiverIdentityPubkey: receiverPk,
			network:                network,
			totalValue:             int64(1_000 + g.rng.Uint64()%1_000_000),
			status:                 status,
			transferType:           ttype,
			expiryTime:             g.randomExpiry(ct, status),
		}
		sr := senderRow{
			id:             g.newRowID(),
			createTime:     ct,
			updateTime:     ct,
			transferID:     tid,
			identityPubkey: senderPk,
		}
		rr := receiverRow{
			id:             g.newRowID(),
			createTime:     ct,
			updateTime:     ct,
			transferID:     tid,
			identityPubkey: receiverPk,
			status:         receiverStatusForTransfer(status),
		}
		select {
		case transferCh <- tr:
		case <-ctx.Done():
			return ctx.Err()
		}
		select {
		case senderCh <- sr:
		case <-ctx.Done():
			return ctx.Err()
		}
		select {
		case receiverCh <- rr:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// pickFromCDF is the generic counterpart of pickStatus / pickType, extracted
// so emitPhase can use caller-supplied CDFs without baking them into the
// generator.
func pickFromCDF[T any](r int, cdf []int, order []T) T {
	for i, cum := range cdf {
		if r < cum {
			return order[i]
		}
	}
	return order[len(order)-1]
}

func (g *generator) newRowID() uuid.UUID {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.New()
	}
	return id
}

func (g *generator) randomCreateTime() time.Time {
	// Use unsigned modulo so MinInt64 doesn't roll back to itself via negation
	// (the prior `if offset < 0 { offset = -offset }` guard missed that one
	// value and produced timestamps before baseTime).
	offset := int64(g.rng.Uint64() % uint64(g.spanNanos))
	return g.baseTime.Add(time.Duration(offset)).UTC()
}

// randomExpiry picks expiry_time such that pending-sender rows have expired
// (expiry_time < NOW()). For non-pending statuses, expiry_time is immaterial
// for the queries under test but must still be non-null per schema.
func (g *generator) randomExpiry(createTime time.Time, status st.TransferStatus) time.Time {
	switch status {
	case st.TransferStatusSenderInitiated,
		st.TransferStatusSenderInitiatedCoordinator,
		st.TransferStatusSenderKeyTweakPending:
		// Half expired (eligible for pending query match), half future.
		if g.rng.IntN(2) == 0 {
			return createTime.Add(time.Hour)
		}
		return time.Now().UTC().Add(24 * time.Hour)
	default:
		return createTime.Add(24 * time.Hour)
	}
}

func (g *generator) pickStatus() st.TransferStatus {
	return pickFromCDF(g.rng.IntN(g.statusSum), g.statusCDF, g.statusOrder)
}

func (g *generator) pickType() st.TransferType {
	return pickFromCDF(g.rng.IntN(g.typeSum), g.typeCDF, g.typeOrder)
}
