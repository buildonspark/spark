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

// PhaseRole describes which side(s) of a transfer the wallet appears on for a
// given phase. Tiers always emit "both" implicitly via the half/half split in
// copyRows; phases name the role explicitly so a wallet can model receive-only
// pending traffic (real SSP receivers, stuck-user inbound backlog) without
// fabricating spurious sender activity for the same identity.
type PhaseRole string

const (
	// PhaseRoleReceiver: the group's wallet is the receiver; counterparty is sender.
	PhaseRoleReceiver PhaseRole = "receiver"
	// PhaseRoleSender: the group's wallet is the sender; counterparty is receiver.
	PhaseRoleSender PhaseRole = "sender"
)

// WalletGroup models a single concrete production wallet — one identity pubkey,
// one network, one or more emit phases. Used by profiles that reproduce a
// specific wallet's traffic shape (e.g. a real SSP's pending mix, a stuck-user
// backlog) rather than a global distribution sampled across many wallets.
//
// All phases share the wallet's identity pubkey (deterministic from globalIdx)
// and network. Each phase emits Count rows with its own role and per-phase
// status/type/receiver-status mix — independent of the Config-level defaults.
type WalletGroup struct {
	Label   string
	Network string // "MAINNET" or "REGTEST" — overrides Config.Network for this group
	Purpose string // documentation only; printed in --dry-run plan
	Phases  []WalletPhase
}

// WalletPhase is one emit pass for a WalletGroup: an exact row count, a fixed
// role, and the three distributions used to sample status/type per row.
//
// Counts are exact (no min/max range) because the whole point of these profiles
// is to reproduce concrete observed cardinality. Distributions are required —
// nil weights would silently fall back to nothing useful.
type WalletPhase struct {
	Label            string
	Count            int
	Role             PhaseRole
	TransferStatuses []StatusWeight
	TransferTypes    []TypeWeight
	ReceiverStatuses []ReceiverStatusWeight
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

	// Wallet groups. Each group models one concrete production wallet's traffic
	// shape — a single identity pubkey, a fixed network, one or more emit phases
	// with their own role and distributions. Used by profiles like realistic_ssp
	// and stuck_user where a single global distribution doesn't reproduce the
	// shape we're modeling. Unset (nil) for the full and smoke profiles, whose
	// shape is captured entirely by Tiers.
	//
	// Group pubkeys are derived from a deterministic globalIdx in a high-numbered
	// range that doesn't collide with Tiers. See copyRows for the offset.
	WalletGroups []WalletGroup

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
	case "realistic_ssp":
		return realisticSSPConfig(), nil
	case "stuck_user":
		return stuckUserConfig(), nil
	default:
		return nil, fmt.Errorf("unknown profile %q (expected 'full', 'full-no-ssp', 'smoke', 'realistic_ssp', or 'stuck_user')", profile)
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

// realisticSSPConfig models a real SSP's pending traffic — a small handful of
// pending receivers (low hundreds) sitting on top of a multi-million completed
// backdrop. Two synthetic SSP wallets, one per network: mainnet (~92 pending,
// ~23.7M completed) and regtest (~64 pending, ~752K completed). Per-phase
// status and type distributions are taken verbatim from prod cardinality
// probes against transfer_receivers (see CLAUDE.md note in this dir for how
// the numbers were captured — they're what the queryPendingTransfersMIMO
// path needs to be validated against).
//
// Why both networks: prod has both, and the regtest SSP's pending type mix
// (SWAP-dominated) differs sharply from mainnet's (PREIMAGE_SWAP-dominated).
// The queryPendingTransfersMIMO path is exercised on both, so we model both.
//
// The completed backdrops matter for plan choice: postgres tracks per-pubkey
// row counts via pg_stats most_common_vals, so a synthetic SSP pubkey with
// only 92 rows total looks nothing like a prod SSP pubkey with 23.7M rows.
// Without the backdrop, the planner under-estimates how much the partial
// index saves over walking the full per-pubkey index.
func realisticSSPConfig() *Config {
	// Pending mix on mainnet SSP (probe 1: status counts; probe 2: type counts).
	// Captured 2026-04-28; total pending = 92 (status) / 91 (types) — the 1-row
	// gap is in-flight mutation between the two probes. Status weights and type
	// weights below are weighted-sample inputs, so 92 sampled rows will yield
	// approximately these counts.
	mainnetPendingStatus := []ReceiverStatusWeight{
		{Status: st.TransferReceiverStatusSenderInitiated, Weight: 86},
		{Status: st.TransferReceiverStatusRefundSigned, Weight: 3},
		{Status: st.TransferReceiverStatusKeyTweaked, Weight: 2},
		{Status: st.TransferReceiverStatusKeyTweakLocked, Weight: 1},
	}
	mainnetPendingTypes := []TypeWeight{
		{Type: st.TransferTypePreimageSwap, Weight: 48},
		{Type: st.TransferTypeSwap, Weight: 32},
		{Type: st.TransferTypeTransfer, Weight: 6},
		{Type: st.TransferTypeCooperativeExit, Weight: 3},
		{Type: st.TransferTypePrimarySwapV3, Weight: 2},
		{Type: st.TransferTypeCounterSwap, Weight: 1},
	}
	// Pending mix on regtest SSP (probes 1 and 2). Total 64 (status) / 60 (types).
	regtestPendingStatus := []ReceiverStatusWeight{
		{Status: st.TransferReceiverStatusSenderInitiated, Weight: 61},
		{Status: st.TransferReceiverStatusRefundSigned, Weight: 2},
		{Status: st.TransferReceiverStatusKeyTweakApplied, Weight: 1},
	}
	regtestPendingTypes := []TypeWeight{
		{Type: st.TransferTypeSwap, Weight: 52},
		{Type: st.TransferTypeCooperativeExit, Weight: 3},
		{Type: st.TransferTypePreimageSwap, Weight: 2},
		{Type: st.TransferTypeTransfer, Weight: 2},
		{Type: st.TransferTypePrimarySwapV3, Weight: 1},
	}
	// Completed backdrop. Each SSP's lifetime-COMPLETED receiver volume is
	// dominated by the same swap families that show up in pending — so we reuse
	// the pending type weights for the completed backdrop too. The status is
	// pinned to COMPLETED.
	completedStatus := []ReceiverStatusWeight{
		{Status: st.TransferReceiverStatusCompleted, Weight: 1},
	}
	// transfers.status is always COMPLETED for completed receivers and a
	// pending-family value for pending receivers. We flatten this — both phases
	// pin transfers.status to a single value via a single-weight slice. (For
	// pending, we pick the dominant pending status; planner only cares that
	// this row appears in the receiver-side partial index, which is keyed off
	// transfer_receivers.status, not transfers.status.)
	pendingTransferStatus := []StatusWeight{
		{Status: st.TransferStatusSenderKeyTweaked, Weight: 1},
	}
	completedTransferStatus := []StatusWeight{
		{Status: st.TransferStatusCompleted, Weight: 1},
	}

	return &Config{
		Profile: "realistic_ssp",
		// No tier-driven generation — this profile is entirely WalletGroup-based.
		Tiers: nil,
		WalletGroups: []WalletGroup{
			{
				Label:   "ssp-mainnet",
				Network: "MAINNET",
				Purpose: "real mainnet SSP — 92 pending receivers (swap-family dominated) on top of a 23.7M-row completed backdrop. Models pubkey 023e33e2920326f64ea31058d44777442d97d7d5cbfcf54e3060bc1695e5261c93 (prod 2026-04-28).",
				Phases: []WalletPhase{
					{
						Label:            "pending",
						Count:            92,
						Role:             PhaseRoleReceiver,
						TransferStatuses: pendingTransferStatus,
						TransferTypes:    mainnetPendingTypes,
						ReceiverStatuses: mainnetPendingStatus,
					},
					{
						Label:            "completed",
						Count:            23_729_623,
						Role:             PhaseRoleReceiver,
						TransferStatuses: completedTransferStatus,
						TransferTypes:    mainnetPendingTypes,
						ReceiverStatuses: completedStatus,
					},
				},
			},
			{
				Label:   "ssp-regtest",
				Network: "REGTEST",
				Purpose: "real regtest SSP — 64 pending (SWAP-dominated, distinct from mainnet's PREIMAGE_SWAP-dominated mix) on top of a 752K completed backdrop. Models pubkey 022bf283544b16c0622daecb79422007d167eca6ce9f0c98c0c49833b1f7170bfe (prod 2026-04-28).",
				Phases: []WalletPhase{
					{
						Label:            "pending",
						Count:            64,
						Role:             PhaseRoleReceiver,
						TransferStatuses: pendingTransferStatus,
						TransferTypes:    regtestPendingTypes,
						ReceiverStatuses: regtestPendingStatus,
					},
					{
						Label:            "completed",
						Count:            752_154,
						Role:             PhaseRoleReceiver,
						TransferStatuses: completedTransferStatus,
						TransferTypes:    regtestPendingTypes,
						ReceiverStatuses: completedStatus,
					},
				},
			},
		},
		// Config-level Network is unused when WalletGroups is set — every group
		// names its own network — but the field is still required to be set
		// somewhere reasonable. Use MAINNET as a sensible default.
		Network:            "MAINNET",
		CreateTimeSpanDays: 365,
		ReportEvery:        500_000,
	}
}

// stuckUserConfig models the tail of stuck-user wallets — identities that have
// accumulated tens of thousands of unclaimed inbound TRANSFERs and almost no
// completed receivers. The primary fixture reproduces the worst-case prod
// pubkey (~58.9k pending, ~100% INITIATED, ~100% TRANSFER); five smaller
// secondary fixtures cover the next-largest stuck users so the planner
// doesn't get confused by the 0329dd outlier alone.
//
// All fixtures are MAINNET — prod's stuck-user wallets are all mainnet. Phase
// counts and type/status weights come from prod probes 4 and 6 (see CLAUDE.md
// in this dir for the probe SQL); captured 2026-04-28.
//
// Each pubkey gets a small completed phase so postgres pg_stats sees a
// realistic active-vs-total ratio — without it, the planner doesn't have the
// right numerator/denominator for its partial-index cost estimates.
func stuckUserConfig() *Config {
	// 100% INITIATED pending status, modeled with a tiny weighted tail to
	// preserve the prod observation that ~2 of ~58.9k pending receivers had
	// non-INITIATED status. The generator's weighted sample distributes
	// proportionally across counts, so this tail produces a vanishing minority.
	primaryPendingStatus := []ReceiverStatusWeight{
		{Status: st.TransferReceiverStatusSenderInitiated, Weight: 58_951},
		{Status: st.TransferReceiverStatusKeyTweakApplied, Weight: 1},
		{Status: st.TransferReceiverStatusRefundSigned, Weight: 1},
	}
	// Pending types: ~all TRANSFER, 1 COUNTER_SWAP per probe 4.
	primaryPendingTypes := []TypeWeight{
		{Type: st.TransferTypeTransfer, Weight: 58_952},
		{Type: st.TransferTypeCounterSwap, Weight: 1},
	}
	// Completed phase types/status (probe 4: 2439 TRANSFER + 8 COUNTER_SWAP).
	primaryCompletedTypes := []TypeWeight{
		{Type: st.TransferTypeTransfer, Weight: 2_439},
		{Type: st.TransferTypeCounterSwap, Weight: 8},
	}
	// Status weights pin to COMPLETED for the completed phase; INITIATED-family
	// for the pending phase's transfers row. (transfers.status is independent of
	// transfer_receivers.status; the partial index that matters is on the
	// receiver side. We pin transfers.status conservatively to a pending-family
	// value for pending, COMPLETED for completed, so any future query that does
	// JOIN to transfers and filters by status sees consistent rows.)
	completedReceiverStatus := []ReceiverStatusWeight{
		{Status: st.TransferReceiverStatusCompleted, Weight: 1},
	}
	pendingTransferStatus := []StatusWeight{
		{Status: st.TransferStatusSenderKeyTweaked, Weight: 1},
	}
	completedTransferStatus := []StatusWeight{
		{Status: st.TransferStatusCompleted, Weight: 1},
	}

	groups := []WalletGroup{
		{
			Label:   "stuck-user-primary",
			Network: "MAINNET",
			Purpose: "worst-case prod stuck user — 58953 pending receivers (~100% INITIATED + TRANSFER). Models pubkey 0329dd5999cc2ac895cb24118c0df7009ab4ca659e5d247f1857de91a869069c24 (prod 2026-04-28).",
			Phases: []WalletPhase{
				{
					Label:            "pending",
					Count:            58_953,
					Role:             PhaseRoleReceiver,
					TransferStatuses: pendingTransferStatus,
					TransferTypes:    primaryPendingTypes,
					ReceiverStatuses: primaryPendingStatus,
				},
				{
					Label:            "completed",
					Count:            2_447,
					Role:             PhaseRoleReceiver,
					TransferStatuses: completedTransferStatus,
					TransferTypes:    primaryCompletedTypes,
					ReceiverStatuses: completedReceiverStatus,
				},
			},
		},
	}
	// Secondary stuck-user pubkeys (probe 6). Counts and dominant type per
	// pubkey from prod; status weights default to 100% INITIATED pending since
	// probe 6 didn't break out status (the broader pattern from probe 4 is that
	// stuck-user pending is ~all INITIATED). No completed phase for these —
	// they're meant to verify the planner doesn't over-fit to the 0329dd
	// outlier alone, which is best done with the partial-index population
	// shape, not the lifetime receiver count.
	type secondary struct {
		label   string
		count   int
		types   []TypeWeight
		purpose string
	}
	secondaries := []secondary{
		{
			label:   "stuck-user-02c65776",
			count:   27_657,
			types:   []TypeWeight{{Type: st.TransferTypeTransfer, Weight: 27_656}, {Type: st.TransferTypeCounterSwap, Weight: 1}},
			purpose: "second-largest stuck user — 27657 pending. Models pubkey 02c65776fb5894c705be6ba206151205801b3db0515b57b7177a4d2ce8da039d88.",
		},
		{
			label:   "stuck-user-035a3abb",
			count:   14_092,
			types:   []TypeWeight{{Type: st.TransferTypeTransfer, Weight: 1}},
			purpose: "third-largest stuck user — 14092 pending, 100% TRANSFER. Models pubkey 035a3abbf7519145d10fda8ec6c12774193bce0181a9adcceadb9f5eddfbd43285.",
		},
		{
			label:   "stuck-user-038215a6",
			count:   10_800,
			types:   []TypeWeight{{Type: st.TransferTypeTransfer, Weight: 10_755}, {Type: st.TransferTypePreimageSwap, Weight: 40}, {Type: st.TransferTypeCounterSwap, Weight: 5}},
			purpose: "fourth-largest stuck user — 10800 pending, mostly TRANSFER. Models pubkey 038215a6de0f05d8200f1cdb1931bde04d1cffb896c72704a674cd4ff3c7d06f09.",
		},
		{
			label:   "stuck-user-0344608d",
			count:   4_563,
			types:   []TypeWeight{{Type: st.TransferTypeTransfer, Weight: 1}},
			purpose: "fifth-largest stuck user — 4563 pending, 100% TRANSFER. Models pubkey 0344608dcf1d3bcd47afd0dd4d753e1365bc6c01c3ff11c97c9c1e9882e42ebae3.",
		},
		{
			label:   "stuck-user-023efa8b",
			count:   3_274,
			types:   []TypeWeight{{Type: st.TransferTypeTransfer, Weight: 3_271}, {Type: st.TransferTypePreimageSwap, Weight: 3}},
			purpose: "sixth-largest stuck user — 3274 pending, ~100% TRANSFER. Models pubkey 023efa8b4ebd1e283cf6c513fc496eb5ff15e1e753e22c5416206eb573c9aebb66.",
		},
	}
	secondaryStatus := []ReceiverStatusWeight{
		{Status: st.TransferReceiverStatusSenderInitiated, Weight: 1},
	}
	for _, s := range secondaries {
		groups = append(groups, WalletGroup{
			Label:   s.label,
			Network: "MAINNET",
			Purpose: s.purpose,
			Phases: []WalletPhase{
				{
					Label:            "pending",
					Count:            s.count,
					Role:             PhaseRoleReceiver,
					TransferStatuses: pendingTransferStatus,
					TransferTypes:    s.types,
					ReceiverStatuses: secondaryStatus,
				},
			},
		})
	}

	return &Config{
		Profile:            "stuck_user",
		Tiers:              nil,
		WalletGroups:       groups,
		Network:            "MAINNET",
		CreateTimeSpanDays: 365,
		ReportEvery:        50_000,
	}
}

// totalTransfers returns the expected transfer-row count for a Config, assuming
// each wallet generates (CountMin+CountMax)/2 transfers on average. Counts
// from WalletGroups (used by realistic_ssp / stuck_user) are exact and added
// in directly.
func (c *Config) totalTransfers() int {
	n := 0
	for _, t := range c.Tiers {
		avg := (t.CountMin + t.CountMax) / 2
		n += t.WalletsInTier * avg
	}
	for _, g := range c.WalletGroups {
		for _, p := range g.Phases {
			n += p.Count
		}
	}
	return n
}

// printPlan writes a human-readable summary of the seed plan. Returned write
// errors are intentionally ignored — this prints to stderr for a human, and
// broken stdio isn't a fatal condition for the seed run.
func printPlan(w io.Writer, c *Config) {
	_, _ = fmt.Fprintf(w, "dbseed profile=%s seed=%d network=%s\n", c.Profile, c.Seed, c.Network)
	_, _ = fmt.Fprintln(w, "")
	if len(c.Tiers) > 0 {
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
	}
	if len(c.WalletGroups) > 0 {
		_, _ = fmt.Fprintln(w, "Wallet groups:")
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "  group\tnetwork\tphase\trole\tcount\tpurpose")
		for _, g := range c.WalletGroups {
			for i, p := range g.Phases {
				purpose := ""
				if i == 0 {
					purpose = g.Purpose
				}
				_, _ = fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%d\t%s\n",
					g.Label, g.Network, p.Label, p.Role, p.Count, purpose)
			}
		}
		_ = tw.Flush()
		_, _ = fmt.Fprintln(w, "")
	}
	_, _ = fmt.Fprintf(w, "Expected transfer rows: ~%d (2x that in edge tables)\n", c.totalTransfers())
	_, _ = fmt.Fprintf(w, "create_time spread: last %d days\n", c.CreateTimeSpanDays)
	_, _ = fmt.Fprintln(w, "")
}
