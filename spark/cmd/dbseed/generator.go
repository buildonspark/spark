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
	statusSum   int
	typeCDF     []int
	typeSum     int
	receiverCDF []int
	receiverSum int
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

	g.statusSum, g.statusCDF = cumulativeStatus(cfg.TransferStatuses)
	g.typeSum, g.typeCDF = cumulativeType(cfg.TransferTypes)
	g.receiverSum, g.receiverCDF = cumulativeReceiverStatus(cfg.ReceiverStatuses)
	return g
}

func cumulativeStatus(w []StatusWeight) (int, []int) {
	cdf := make([]int, len(w))
	sum := 0
	for i, x := range w {
		sum += x.Weight
		cdf[i] = sum
	}
	return sum, cdf
}

func cumulativeType(w []TypeWeight) (int, []int) {
	cdf := make([]int, len(w))
	sum := 0
	for i, x := range w {
		sum += x.Weight
		cdf[i] = sum
	}
	return sum, cdf
}

func cumulativeReceiverStatus(w []ReceiverStatusWeight) (int, []int) {
	cdf := make([]int, len(w))
	sum := 0
	for i, x := range w {
		sum += x.Weight
		cdf[i] = sum
	}
	return sum, cdf
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
			status:         g.pickReceiverStatus(),
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
	r := g.rng.IntN(g.statusSum)
	for i, cum := range g.statusCDF {
		if r < cum {
			return g.cfg.TransferStatuses[i].Status
		}
	}
	return g.cfg.TransferStatuses[len(g.cfg.TransferStatuses)-1].Status
}

func (g *generator) pickType() st.TransferType {
	r := g.rng.IntN(g.typeSum)
	for i, cum := range g.typeCDF {
		if r < cum {
			return g.cfg.TransferTypes[i].Type
		}
	}
	return g.cfg.TransferTypes[len(g.cfg.TransferTypes)-1].Type
}

func (g *generator) pickReceiverStatus() st.TransferReceiverStatus {
	r := g.rng.IntN(g.receiverSum)
	for i, cum := range g.receiverCDF {
		if r < cum {
			return g.cfg.ReceiverStatuses[i].Status
		}
	}
	return g.cfg.ReceiverStatuses[len(g.cfg.ReceiverStatuses)-1].Status
}
