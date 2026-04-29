package main

import (
	"sync"
	"testing"

	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
)

// TestRealisticSSPDistribution drains the realistic_ssp profile's WalletGroups
// through the generator into in-memory slices and asserts the produced row
// shape matches what the profile claims. Avoids the full pgx COPY pipeline so
// the test runs without any database — useful as a smoke check that the
// per-phase distributions are correctly wired to the new emitPhase path and
// that the dispatcher can produce the SSP shapes the profile is supposed to
// produce.
func TestRealisticSSPDistribution(t *testing.T) {
	cfg := mustProfile(t, "realistic_ssp")
	if len(cfg.Tiers) != 0 {
		t.Errorf("expected no tiers in realistic_ssp profile; got %d", len(cfg.Tiers))
	}
	if len(cfg.WalletGroups) != 2 {
		t.Fatalf("expected 2 wallet groups in realistic_ssp profile; got %d", len(cfg.WalletGroups))
	}
	results := drainProfile(t, cfg)
	mainnet, ok := results.byGroup["ssp-mainnet"]
	if !ok {
		t.Fatalf("expected ssp-mainnet results; got groups %v", results.groupLabels())
	}
	regtest, ok := results.byGroup["ssp-regtest"]
	if !ok {
		t.Fatalf("expected ssp-regtest results; got groups %v", results.groupLabels())
	}

	// Pending counts are exact (no rng on count itself — only on per-row
	// status/type sampling).
	if got := mainnet.phaseRows["pending"]; got != 92 {
		t.Errorf("ssp-mainnet/pending: want 92 receivers, got %d", got)
	}
	if got := regtest.phaseRows["pending"]; got != 64 {
		t.Errorf("ssp-regtest/pending: want 64 receivers, got %d", got)
	}

	// Receiver rows for both SSP wallets must always have the wallet's pubkey
	// in the receiver position (PhaseRoleReceiver). Sample the pending phase.
	if mainnet.distinctPubkeysReceiverSide != 1 {
		t.Errorf("ssp-mainnet pending: expected exactly 1 distinct receiver pubkey (the SSP itself); got %d",
			mainnet.distinctPubkeysReceiverSide)
	}
	if regtest.distinctPubkeysReceiverSide != 1 {
		t.Errorf("ssp-regtest pending: expected exactly 1 distinct receiver pubkey; got %d",
			regtest.distinctPubkeysReceiverSide)
	}

	// Mainnet pending: PREIMAGE_SWAP must be the most common type (~53%).
	if !isMostCommonType(mainnet.pendingTypeCounts, st.TransferTypePreimageSwap) {
		t.Errorf("ssp-mainnet/pending: expected PREIMAGE_SWAP dominant; counts=%v",
			mainnet.pendingTypeCounts)
	}
	// Regtest pending: SWAP must be the most common type (~81%).
	if !isMostCommonType(regtest.pendingTypeCounts, st.TransferTypeSwap) {
		t.Errorf("ssp-regtest/pending: expected SWAP dominant; counts=%v",
			regtest.pendingTypeCounts)
	}

	// Mainnet pending status: INITIATED must be the most common (~93%).
	if !isMostCommonReceiverStatus(mainnet.pendingReceiverStatus, st.TransferReceiverStatusSenderInitiated) {
		t.Errorf("ssp-mainnet/pending: expected INITIATED dominant; counts=%v",
			mainnet.pendingReceiverStatus)
	}

	// Both pending phases must use the appropriate network on transfers.network.
	if got := mainnet.networks["pending"]["MAINNET"]; got != mainnet.phaseRows["pending"] {
		t.Errorf("ssp-mainnet/pending: expected all rows on MAINNET, got %v", mainnet.networks["pending"])
	}
	if got := regtest.networks["pending"]["REGTEST"]; got != regtest.phaseRows["pending"] {
		t.Errorf("ssp-regtest/pending: expected all rows on REGTEST, got %v", regtest.networks["pending"])
	}
}

// TestStuckUserDistribution mirrors the realistic_ssp test but for the
// stuck-user profile. Asserts the primary wallet's pending shape is
// overwhelmingly TRANSFER + INITIATED, that all secondary wallets are
// generated with their declared counts, and that everything is MAINNET.
func TestStuckUserDistribution(t *testing.T) {
	cfg := mustProfile(t, "stuck_user")
	if len(cfg.Tiers) != 0 {
		t.Errorf("expected no tiers in stuck_user profile; got %d", len(cfg.Tiers))
	}
	expectedGroups := map[string]int{
		"stuck-user-primary":  58_953,
		"stuck-user-02c65776": 27_657,
		"stuck-user-035a3abb": 14_092,
		"stuck-user-038215a6": 10_800,
		"stuck-user-0344608d": 4_563,
		"stuck-user-023efa8b": 3_274,
	}
	if len(cfg.WalletGroups) != len(expectedGroups) {
		t.Fatalf("expected %d wallet groups in stuck_user profile; got %d",
			len(expectedGroups), len(cfg.WalletGroups))
	}
	results := drainProfile(t, cfg)
	for label, wantPending := range expectedGroups {
		group, ok := results.byGroup[label]
		if !ok {
			t.Errorf("missing group %q in results; have %v", label, results.groupLabels())
			continue
		}
		if got := group.phaseRows["pending"]; got != wantPending {
			t.Errorf("%s/pending: want %d receivers, got %d", label, wantPending, got)
		}
		// Every stuck-user wallet must be MAINNET.
		if got := group.networks["pending"]["MAINNET"]; got != group.phaseRows["pending"] {
			t.Errorf("%s/pending: expected all rows on MAINNET, got %v", label, group.networks["pending"])
		}
	}
	primary := results.byGroup["stuck-user-primary"]

	// Primary stuck-user pending must be ≥99.9% TRANSFER and ≥99.9% INITIATED.
	transferShare := float64(primary.pendingTypeCounts[st.TransferTypeTransfer]) / float64(primary.phaseRows["pending"])
	if transferShare < 0.999 {
		t.Errorf("stuck-user-primary/pending: expected ≥99.9%% TRANSFER, got %.4f%% (counts=%v)",
			transferShare*100, primary.pendingTypeCounts)
	}
	initiatedShare := float64(primary.pendingReceiverStatus[st.TransferReceiverStatusSenderInitiated]) / float64(primary.phaseRows["pending"])
	if initiatedShare < 0.999 {
		t.Errorf("stuck-user-primary/pending: expected ≥99.9%% INITIATED, got %.4f%% (counts=%v)",
			initiatedShare*100, primary.pendingReceiverStatus)
	}

	// The completed phase must exist for the primary wallet only — secondaries
	// are pending-phase-only by design.
	if got := primary.phaseRows["completed"]; got != 2_447 {
		t.Errorf("stuck-user-primary/completed: want 2447 receivers, got %d", got)
	}
	for label := range expectedGroups {
		if label == "stuck-user-primary" {
			continue
		}
		if got := results.byGroup[label].phaseRows["completed"]; got != 0 {
			t.Errorf("%s: secondary stuck-user must have no completed phase, got %d", label, got)
		}
	}
}

// TestFullProfileUnchanged verifies the existing full profile still matches
// its declared shape — same tiers, same dual-role transfers — so this commit
// is purely additive at the profile-config level.
func TestFullProfileUnchanged(t *testing.T) {
	cfg := mustProfile(t, "full")
	if cfg.DualRoleTransfers != 2_000 {
		t.Errorf("full profile DualRoleTransfers regressed: want 2000, got %d", cfg.DualRoleTransfers)
	}
	if cfg.DualRoleTierLabel != "T4" {
		t.Errorf("full profile DualRoleTierLabel regressed: want T4, got %q", cfg.DualRoleTierLabel)
	}
	if len(cfg.WalletGroups) != 0 {
		t.Errorf("full profile must not introduce wallet groups; got %d", len(cfg.WalletGroups))
	}
	wantTiers := []struct {
		label    string
		wallets  int
		countMin int
		countMax int
	}{
		{"T1", 1, 50_000_000, 50_000_000},
		{"T2", 1, 10_000_000, 10_000_000},
		{"T3", 1, 1_000_000, 1_000_000},
		{"T4", 1, 100_000, 100_000},
		{"T5", 3, 50_000, 75_000},
		{"TAIL", 1000, 10, 500},
	}
	if len(cfg.Tiers) != len(wantTiers) {
		t.Fatalf("full profile tier count regressed: want %d, got %d", len(wantTiers), len(cfg.Tiers))
	}
	for i, want := range wantTiers {
		got := cfg.Tiers[i]
		if got.Label != want.label || got.WalletsInTier != want.wallets ||
			got.CountMin != want.countMin || got.CountMax != want.countMax {
			t.Errorf("tier %d: want %+v, got %+v", i, want, got)
		}
	}
	if got := cfg.totalTransfers(); got != 61_542_500 {
		t.Errorf("full profile totalTransfers regressed: want 61542500, got %d", got)
	}
}

// TestSmokeProfileUnchanged verifies the smoke profile still has the same
// shape it did before this commit.
func TestSmokeProfileUnchanged(t *testing.T) {
	cfg := mustProfile(t, "smoke")
	if cfg.DualRoleTransfers != 20 {
		t.Errorf("smoke profile DualRoleTransfers regressed: want 20, got %d", cfg.DualRoleTransfers)
	}
	if len(cfg.WalletGroups) != 0 {
		t.Errorf("smoke profile must not introduce wallet groups; got %d", len(cfg.WalletGroups))
	}
}

// --- harness ---

func mustProfile(t *testing.T, name string) *Config {
	t.Helper()
	cfg, err := profileConfig(name)
	if err != nil {
		t.Fatalf("profileConfig(%q): %v", name, err)
	}
	cfg.Seed = 42
	return cfg
}

// drainResults aggregates emitted rows by group and phase. Built up by
// drainProfile by scanning channels rather than hitting Postgres.
type drainResults struct {
	byGroup map[string]*groupCounts
}

func (r *drainResults) groupLabels() []string {
	out := make([]string, 0, len(r.byGroup))
	for k := range r.byGroup {
		out = append(out, k)
	}
	return out
}

type groupCounts struct {
	phaseRows                   map[string]int                    // phase → receiver count
	pendingTypeCounts           map[st.TransferType]int           // type → count (pending phase)
	pendingReceiverStatus       map[st.TransferReceiverStatus]int // status → count (pending phase)
	networks                    map[string]map[string]int         // phase → network → count
	distinctPubkeysReceiverSide int                               // distinct receiver pubkeys for pending phase
}

func newGroupCounts() *groupCounts {
	return &groupCounts{
		phaseRows:             map[string]int{},
		pendingTypeCounts:     map[st.TransferType]int{},
		pendingReceiverStatus: map[st.TransferReceiverStatus]int{},
		networks:              map[string]map[string]int{},
	}
}

// drainProfile runs the profile's WalletGroup phases through the generator and
// accumulates results in-process. Differs from copyRows in seed.go in that the
// channels go to the test harness, not pgx COPY — no Postgres connection
// required, and we can introspect each row.
//
// This is intentionally a near-copy of the WalletGroup loop in copyRows. If
// that loop changes, this should match it.
func drainProfile(t *testing.T, cfg *Config) *drainResults {
	t.Helper()
	const buf = 1024
	transferCh := make(chan transferRow, buf)
	senderCh := make(chan senderRow, buf)
	receiverCh := make(chan receiverRow, buf)

	type row struct {
		t              transferRow
		r              receiverRow
		receiverPubkey string
	}
	rows := make(chan row, buf)

	// Fanin goroutine: pair the three streams into row records keyed by phase.
	// Each emit produces one (transfer, sender, receiver) triple in order, so
	// pairing-by-position is sound under the channel ordering guarantee.
	pairDone := make(chan struct{})
	go func() {
		defer close(pairDone)
		defer close(rows)
		for {
			tr, ok := <-transferCh
			if !ok {
				return
			}
			<-senderCh
			rr := <-receiverCh
			rows <- row{
				t:              tr,
				r:              rr,
				receiverPubkey: string(rr.identityPubkey),
			}
		}
	}()

	results := &drainResults{byGroup: map[string]*groupCounts{}}
	pubkeySetByGroup := map[string]map[string]struct{}{}
	ctx := t.Context()

	var producerWG sync.WaitGroup
	producerWG.Go(func() {
		defer close(transferCh)
		defer close(senderCh)
		defer close(receiverCh)
		for groupIdx, group := range cfg.WalletGroups {
			groupGlobalIdx := walletGroupBaseIdx + groupIdx
			for phaseIdx, phase := range group.Phases {
				w := walletID{
					tierLabel: group.Label + "/" + phase.Label,
					tierIdx:   phaseIdx,
					globalIdx: groupGlobalIdx,
				}
				g := newGenerator(cfg, w, cfg.Seed^int64(0xA00+phaseIdx))
				if err := g.emitPhase(ctx, phase.Count, phase.Role, group.Network,
					newPhaseCDFs(phase),
					transferCh, senderCh, receiverCh); err != nil {
					t.Errorf("emitPhase(%s/%s): %v", group.Label, phase.Label, err)
					return
				}
			}
		}
	})

	// Drain rows in order. Producer emits all rows of one phase contiguously;
	// after a phase's Count rows, the next phase begins. We accumulate the
	// per-phase received count in the consumer loop so phaseRows reflects what
	// emitPhase actually produced — not the config-declared Count, which would
	// make assertions tautological.
	for _, group := range cfg.WalletGroups {
		gc, ok := results.byGroup[group.Label]
		if !ok {
			gc = newGroupCounts()
			results.byGroup[group.Label] = gc
			pubkeySetByGroup[group.Label] = map[string]struct{}{}
		}
		for _, phase := range group.Phases {
			if gc.networks[phase.Label] == nil {
				gc.networks[phase.Label] = map[string]int{}
			}
			for i := 0; i < phase.Count; i++ {
				r := <-rows
				gc.phaseRows[phase.Label]++
				gc.networks[phase.Label][r.t.network]++
				if phase.Label == "pending" {
					gc.pendingTypeCounts[r.t.transferType]++
					gc.pendingReceiverStatus[r.r.status]++
					pubkeySetByGroup[group.Label][r.receiverPubkey] = struct{}{}
				}
			}
			gc.distinctPubkeysReceiverSide = len(pubkeySetByGroup[group.Label])
		}
	}
	producerWG.Wait()
	<-pairDone
	return results
}

func isMostCommonType(counts map[st.TransferType]int, want st.TransferType) bool {
	wantCount := counts[want]
	for k, v := range counts {
		if k == want {
			continue
		}
		if v >= wantCount {
			return false
		}
	}
	return wantCount > 0
}

func isMostCommonReceiverStatus(counts map[st.TransferReceiverStatus]int, want st.TransferReceiverStatus) bool {
	wantCount := counts[want]
	for k, v := range counts {
		if k == want {
			continue
		}
		if v >= wantCount {
			return false
		}
	}
	return wantCount > 0
}
