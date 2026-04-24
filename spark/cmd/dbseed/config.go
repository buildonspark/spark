package main

import (
	"fmt"
	"io"
	"text/tabwriter"

	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
)

// WalletTier describes a contiguous group of wallets that share a transfer-count
// range. For a single-wallet tier, CountMin == CountMax and WalletsInTier == 1.
// For the long tail, WalletsInTier > 1 and each wallet's row count is sampled
// uniformly from [CountMin, CountMax].
type WalletTier struct {
	Label         string
	WalletsInTier int
	CountMin      int // rows per side (senders AND receivers) for each wallet in this tier
	CountMax      int
	// Purpose is documentation — it doesn't affect generation. It exists so the
	// --dry-run output explains why each tier is here.
	Purpose string
}

// StatusWeight is a (status, weight) pair. Weights are unnormalized; the
// generator sums them and picks by proportional cumulative.
type StatusWeight struct {
	Status st.TransferStatus
	Weight int
}

// ReceiverStatusWeight mirrors StatusWeight for the transfer_receivers.status
// enum (different enum type).
type ReceiverStatusWeight struct {
	Status st.TransferReceiverStatus
	Weight int
}

// TypeWeight is a (type, weight) pair.
type TypeWeight struct {
	Type   st.TransferType
	Weight int
}

// Config is the full seed plan. Everything the generator needs comes from here.
type Config struct {
	Profile string
	Seed    int64

	// Wallet tiers. The first tier's pubkey gets deterministic seed 1, second
	// gets 2, etc. Long-tail wallets get seeds starting at len(non-tail) + 1.
	Tiers []WalletTier

	// Dual-role transfers — same pubkey appears on sender and receiver sides of
	// the same transfer. Exercises the anti-join dedup path. Implementation:
	// after generating single-role edges, pick DualRoleTransfers transfer_ids
	// from the T3 wallet and add a matching opposite-side edge row.
	DualRoleTransfers int
	DualRoleTierLabel string // which tier's pubkey to use for dual-role rows

	// Status & type distributions for transfers.
	TransferStatuses []StatusWeight
	TransferTypes    []TypeWeight

	// Receiver-side status distribution. Independent of transfer.status because
	// the receiver enum is a different set of values.
	ReceiverStatuses []ReceiverStatusWeight

	// create_time is distributed uniformly in [now - CreateTimeSpan, now].
	// For realistic ORDER BY behavior and uuid v7 temporal alignment.
	CreateTimeSpanDays int

	// Network label written to transfers.network. Must be "MAINNET" for plan
	// fidelity with prod — the partial indexes and production query patterns
	// all have WHERE network = 'MAINNET' leading equality.
	Network string

	// Batch size for COPY. pgx streams so this is only about flushing progress
	// logs frequently; doesn't bound memory.
	ReportEvery int
}

// profileConfig returns the named seed profile.
func profileConfig(profile string) (*Config, error) {
	switch profile {
	case "full":
		return fullConfig(true), nil
	case "full-no-ssp":
		return fullConfig(false), nil
	case "smoke":
		return smokeConfig(), nil
	default:
		return nil, fmt.Errorf("unknown profile %q (expected 'full', 'full-no-ssp', or 'smoke')", profile)
	}
}

// fullConfig is the prod-shaped ladder. When includeSSP is true, the T1 tier
// reproduces the SSP mainnet wallet at ~25M edges/side — the rest of the
// ladder is identical either way. Skipping the SSP brings the full run from
// ~15+ min down to ~2-4 min while still exercising every other tier and the
// partial-index branching logic.
func fullConfig(includeSSP bool) *Config {
	profileName := "full"
	if !includeSSP {
		profileName = "full-no-ssp"
	}

	// CountMin/CountMax is the total transfer participations for a wallet.
	// The generator splits 50/50 so a rowCount of 50M yields ~25M edges on
	// each side — matching prod SSP scale.
	allTiers := []WalletTier{
		{Label: "T1", WalletsInTier: 1, CountMin: 50_000_000, CountMax: 50_000_000,
			Purpose: "SSP-scale mainnet wallet. 50M rowCount → ~25M on each side — reproduces the status-selective query pathology at the top of the ladder."},
		{Label: "T2", WalletsInTier: 1, CountMin: 10_000_000, CountMax: 10_000_000,
			Purpose: "large service wallet below SSP scale. ~5M edges per side — exercises the partial-index branch at a scale where status-first would still walk millions of pending rows."},
		{Label: "T3", WalletsInTier: 1, CountMin: 1_000_000, CountMax: 1_000_000,
			Purpose: "multi-million representative"},
		{Label: "T4", WalletsInTier: 1, CountMin: 100_000, CountMax: 100_000,
			Purpose: "UNION + anti-join stress (symmetric both sides)"},
		{Label: "T5", WalletsInTier: 3, CountMin: 50_000, CountMax: 75_000,
			Purpose: "50k-100k danger zone where legacy MIMO silently truncates and pending branches hit the 65535 bind-parameter crash"},
		{Label: "TAIL", WalletsInTier: 1000, CountMin: 10, CountMax: 500,
			Purpose: "long tail so the handler's small-wallet edge-first branch gets exercised"},
	}
	tiers := allTiers
	if !includeSSP {
		tiers = allTiers[1:] // drop T1 (the SSP wallet)
	}

	return &Config{
		Profile: profileName,
		Tiers:   tiers,

		DualRoleTransfers: 2_000,
		DualRoleTierLabel: "T4",

		// Prod-shaped status mix:
		//   ~99.5% COMPLETED
		//   ~0.3% SENDER_KEY_TWEAKED (dominates the receiver-union partial index)
		//   small minorities of the other pending/stuck statuses so partial
		//   index cardinality is realistic for planner cost estimates.
		TransferStatuses: []StatusWeight{
			{Status: st.TransferStatusCompleted, Weight: 9950},
			{Status: st.TransferStatusSenderKeyTweaked, Weight: 30},
			{Status: st.TransferStatusReceiverKeyTweaked, Weight: 5},
			{Status: st.TransferStatusReceiverKeyTweakLocked, Weight: 3},
			{Status: st.TransferStatusReceiverKeyTweakApplied, Weight: 2},
			{Status: st.TransferStatusReceiverRefundSigned, Weight: 2},
			{Status: st.TransferStatusSenderKeyTweakPending, Weight: 3},
			{Status: st.TransferStatusSenderInitiated, Weight: 2},
			{Status: st.TransferStatusSenderInitiatedCoordinator, Weight: 1},
			{Status: st.TransferStatusExpired, Weight: 1},
			{Status: st.TransferStatusReturned, Weight: 1},
		},
		TransferTypes: []TypeWeight{
			{Type: st.TransferTypeTransfer, Weight: 90},
			{Type: st.TransferTypePreimageSwap, Weight: 8},
			{Type: st.TransferTypeCooperativeExit, Weight: 1},
			{Type: st.TransferTypeSwap, Weight: 1},
		},
		ReceiverStatuses: []ReceiverStatusWeight{
			{Status: st.TransferReceiverStatusCompleted, Weight: 9950},
			{Status: st.TransferReceiverStatusKeyTweaked, Weight: 20},
			{Status: st.TransferReceiverStatusKeyTweakLocked, Weight: 10},
			{Status: st.TransferReceiverStatusKeyTweakApplied, Weight: 10},
			{Status: st.TransferReceiverStatusRefundSigned, Weight: 5},
			{Status: st.TransferReceiverStatusSenderInitiated, Weight: 3},
			{Status: st.TransferReceiverStatusCancelled, Weight: 2},
		},
		CreateTimeSpanDays: 365,
		Network:            "MAINNET",
		ReportEvery:        500_000,
	}
}

// smokeConfig is a ~10k-row profile for fast iteration on the generator itself.
// Same shape as full but ~1000× smaller; finishes in seconds.
func smokeConfig() *Config {
	c := fullConfig(true)
	c.Profile = "smoke"
	c.Tiers = []WalletTier{
		{Label: "T1", WalletsInTier: 1, CountMin: 10_000, CountMax: 10_000, Purpose: "smoke: scaled SSP"},
		{Label: "T2", WalletsInTier: 1, CountMin: 2_000, CountMax: 2_000, Purpose: "smoke: scaled large-service"},
		{Label: "T3", WalletsInTier: 1, CountMin: 500, CountMax: 500, Purpose: "smoke: scaled multi-million"},
		{Label: "T4", WalletsInTier: 1, CountMin: 200, CountMax: 200, Purpose: "smoke: scaled UNION-stress"},
		{Label: "T5", WalletsInTier: 3, CountMin: 50, CountMax: 100, Purpose: "smoke: scaled danger zone"},
		{Label: "TAIL", WalletsInTier: 50, CountMin: 1, CountMax: 20, Purpose: "smoke: scaled long tail"},
	}
	c.DualRoleTransfers = 20
	c.DualRoleTierLabel = "T4"
	c.ReportEvery = 1_000
	return c
}

// totalTransfers returns the expected transfer-row count for a Config, assuming
// each wallet generates (CountMin+CountMax)/2 transfers on average.
func (c *Config) totalTransfers() int {
	n := 0
	for _, t := range c.Tiers {
		avg := (t.CountMin + t.CountMax) / 2
		n += t.WalletsInTier * avg
	}
	return n
}

// printPlan writes a human-readable summary of the seed plan. Returned write
// errors are intentionally ignored — this prints to stderr for a human, and
// broken stdio isn't a fatal condition for the seed run.
func printPlan(w io.Writer, c *Config) {
	_, _ = fmt.Fprintf(w, "dbseed profile=%s seed=%d network=%s\n", c.Profile, c.Seed, c.Network)
	_, _ = fmt.Fprintln(w, "")
	_, _ = fmt.Fprintln(w, "Wallet tiers:")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "  label\twallets\tcount/wallet\tpurpose")
	for _, t := range c.Tiers {
		counts := fmt.Sprintf("%d", t.CountMin)
		if t.CountMax != t.CountMin {
			counts = fmt.Sprintf("%d-%d", t.CountMin, t.CountMax)
		}
		_, _ = fmt.Fprintf(tw, "  %s\t%d\t%s\t%s\n", t.Label, t.WalletsInTier, counts, t.Purpose)
	}
	_ = tw.Flush()
	_, _ = fmt.Fprintf(w, "\nDual-role transfers: %d on tier %s\n", c.DualRoleTransfers, c.DualRoleTierLabel)
	_, _ = fmt.Fprintf(w, "Expected transfer rows: ~%d (2x that in edge tables)\n", c.totalTransfers())
	_, _ = fmt.Fprintf(w, "create_time spread: last %d days\n", c.CreateTimeSpanDays)
	_, _ = fmt.Fprintln(w, "")
}
