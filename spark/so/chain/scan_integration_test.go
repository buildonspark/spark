package chain

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/ent/blockheight"
	"github.com/stretchr/testify/require"
)

// fakeBitcoind serves the four RPCs a chain scan makes — getblockcount,
// getblockhash, and getblock at verbosity 1 and 2 — over a scripted chain, so
// tests can drive scanWithRetry through the real reconcile path. countHook
// reproduces a lagging replica: it can report a stale height or fail outright.
type fakeBitcoind struct {
	t          *testing.T
	blocks     []*wire.MsgBlock
	hashes     []chainhash.Hash
	config     so.BitcoindConfig
	countCalls atomic.Int64
	countHook  func(call int64, tip int64) (int64, error)
}

func newFakeBitcoind(t *testing.T, height int64) *fakeBitcoind {
	t.Helper()
	f := &fakeBitcoind{t: t}
	var prev chainhash.Hash
	for h := int64(0); h <= height; h++ {
		block := testBlockWithCoinbaseHeight(t, h)
		block.Header.PrevBlock = prev
		prev = block.BlockHash()
		f.blocks = append(f.blocks, block)
		f.hashes = append(f.hashes, prev)
	}
	server := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(server.Close)
	f.config = so.BitcoindConfig{
		Network:  strings.ToLower(btcnetwork.Regtest.String()),
		Host:     strings.TrimPrefix(server.URL, "http://"),
		User:     "test",
		Password: "test",
	}
	return f
}

func (f *fakeBitcoind) tip() int64 { return int64(len(f.blocks)) - 1 }

func (f *fakeBitcoind) heightOf(hash string) (int64, bool) {
	for h, candidate := range f.hashes {
		if candidate.String() == hash {
			return int64(h), true
		}
	}
	return 0, false
}

func (f *fakeBitcoind) serve(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Method string            `json:"method"`
		Params []json.RawMessage `json:"params"`
		ID     json.RawMessage   `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	result, rpcErr := f.dispatch(req.Method, req.Params)
	var raw json.RawMessage
	if rpcErr == nil {
		encoded, err := json.Marshal(result)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		raw = encoded
	}
	_ = json.NewEncoder(w).Encode(struct {
		Result json.RawMessage   `json:"result"`
		Error  *btcjson.RPCError `json:"error"`
		ID     json.RawMessage   `json:"id"`
	}{Result: raw, Error: rpcErr, ID: req.ID})
}

func (f *fakeBitcoind) dispatch(method string, params []json.RawMessage) (any, *btcjson.RPCError) {
	switch method {
	case "getblockcount":
		call := f.countCalls.Add(1)
		if f.countHook == nil {
			return f.tip(), nil
		}
		reported, err := f.countHook(call, f.tip())
		if err != nil {
			return nil, &btcjson.RPCError{Code: btcjson.ErrRPCBlockNotFound, Message: err.Error()}
		}
		return reported, nil

	case "getblockhash":
		var height int64
		if err := json.Unmarshal(params[0], &height); err != nil {
			return nil, &btcjson.RPCError{Code: btcjson.ErrRPCInvalidParameter, Message: err.Error()}
		}
		if height < 0 || height > f.tip() {
			return nil, &btcjson.RPCError{Code: btcjson.ErrRPCOutOfRange, Message: "block height out of range"}
		}
		return f.hashes[height].String(), nil

	case "getblock":
		var hash string
		if err := json.Unmarshal(params[0], &hash); err != nil {
			return nil, &btcjson.RPCError{Code: btcjson.ErrRPCInvalidParameter, Message: err.Error()}
		}
		height, ok := f.heightOf(hash)
		if !ok {
			return nil, &btcjson.RPCError{Code: btcjson.ErrRPCBlockNotFound, Message: "block not found"}
		}
		if verbosity(params) >= 2 {
			return f.blockVerboseTx(height), nil
		}
		return f.blockVerbose(height), nil
	}
	return nil, &btcjson.RPCError{Code: btcjson.ErrRPCInvalidParameter, Message: "unexpected method " + method}
}

// verbosity reads getblock's second argument.
func verbosity(params []json.RawMessage) int {
	if len(params) < 2 {
		return 1
	}
	var level int
	if err := json.Unmarshal(params[1], &level); err != nil {
		return 1
	}
	return level
}

func (f *fakeBitcoind) blockVerbose(height int64) btcjson.GetBlockVerboseResult {
	result := btcjson.GetBlockVerboseResult{
		Hash:   f.hashes[height].String(),
		Height: height,
	}
	if height > 0 {
		result.PreviousHash = f.hashes[height-1].String()
	}
	for _, tx := range f.blocks[height].Transactions {
		result.Tx = append(result.Tx, tx.TxHash().String())
	}
	return result
}

func (f *fakeBitcoind) blockVerboseTx(height int64) btcjson.GetBlockVerboseTxResult {
	result := btcjson.GetBlockVerboseTxResult{
		Hash:   f.hashes[height].String(),
		Height: height,
	}
	if height > 0 {
		result.PreviousHash = f.hashes[height-1].String()
	}
	for _, tx := range f.blocks[height].Transactions {
		var buf bytes.Buffer
		require.NoError(f.t, tx.Serialize(&buf))
		result.Tx = append(result.Tx, btcjson.TxRawResult{
			Txid: tx.TxHash().String(),
			Hex:  hex.EncodeToString(buf.Bytes()),
		})
	}
	return result
}

// scanFixture wires a fake bitcoind to a SQLite-backed operator DB seeded one
// or more blocks behind the fake's tip.
type scanFixture struct {
	client  *ent.Client
	config  *so.Config
	node    *fakeBitcoind
	network btcnetwork.Network
	ctx     context.Context
}

func newScanFixture(t *testing.T, chainHeight, dbHeight int64) *scanFixture {
	t.Helper()
	ctx, tc := db.NewTestSQLiteContext(t)
	node := newFakeBitcoind(t, chainHeight)
	_, err := tc.Client.BlockHeight.Create().
		SetHeight(dbHeight).
		SetNetwork(btcnetwork.Regtest).
		SetBlockHash(node.hashes[dbHeight][:]).
		Save(ctx)
	require.NoError(t, err)

	return &scanFixture{
		ctx:     ctx,
		client:  tc.Client,
		node:    node,
		network: btcnetwork.Regtest,
		config: &so.Config{
			SupportedNetworks: []btcnetwork.Network{btcnetwork.Regtest},
			BitcoindConfigs: map[string]so.BitcoindConfig{
				strings.ToLower(btcnetwork.Regtest.String()): node.config,
			},
		},
	}
}

func (f *scanFixture) run(notification BlockNotification) error {
	return f.runCtx(f.ctx, notification)
}

func (f *scanFixture) runCtx(ctx context.Context, notification BlockNotification) error {
	return scanWithRetry(ctx, f.config, f.client, nil, f.node.config, f.network, notification)
}

// staleForever makes every getblockcount answer one block behind, the way a
// replica that never catches up would.
func (f *scanFixture) staleForever() {
	f.node.countHook = func(_ int64, tip int64) (int64, error) {
		return tip - 1, nil
	}
}

func (f *scanFixture) dbHeight(t *testing.T) int64 {
	t.Helper()
	row, err := f.client.BlockHeight.Query().Where(blockheight.NetworkEQ(f.network)).Only(f.ctx)
	require.NoError(t, err)
	return row.Height
}

func (f *scanFixture) notificationFor(height int64) BlockNotification {
	return BlockNotification{Hash: f.node.hashes[height], Height: height, Parsed: true}
}

// withFastRetries shrinks the retry schedule so policy tests do not sleep. The
// package already swaps tweakKeysForCoopExitFunc this way; tests in a package
// run sequentially unless they opt into t.Parallel, which these do not.
func withFastRetries(t *testing.T, delays ...time.Duration) {
	t.Helper()
	original := scanRetryDelays
	scanRetryDelays = delays
	t.Cleanup(func() { scanRetryDelays = original })
}

// TestScanWithRetryRescuesStaleTip is the case this retry exists for: the first
// scan lands on a replica that has not seen the notified block, returns no
// error, and reconciles to nothing. The retry must reach the real tip.
func TestScanWithRetryRescuesStaleTip(t *testing.T) {
	withFastRetries(t, time.Millisecond, time.Millisecond, time.Millisecond)
	fixture := newScanFixture(t, 101, 100)
	fixture.node.countHook = func(call int64, tip int64) (int64, error) {
		if call == 1 {
			return tip - 1, nil // lagging replica: still on 100
		}
		return tip, nil
	}

	require.NoError(t, fixture.run(fixture.notificationFor(101)))
	require.Equal(t, int64(101), fixture.dbHeight(t), "the retry must reconcile to the notified block")
	require.Equal(t, int64(2), fixture.node.countCalls.Load(), "one stale scan, then one that reaches the tip")
}

func TestScanWithRetryRetriesErrors(t *testing.T) {
	withFastRetries(t, time.Millisecond, time.Millisecond, time.Millisecond)
	fixture := newScanFixture(t, 101, 100)
	fixture.node.countHook = func(call int64, tip int64) (int64, error) {
		if call == 1 {
			return 0, errors.New("block height out of range")
		}
		return tip, nil
	}

	require.NoError(t, fixture.run(fixture.notificationFor(101)))
	require.Equal(t, int64(101), fixture.dbHeight(t))
	require.Equal(t, int64(2), fixture.node.countCalls.Load())
}

// TestScanWithRetryGivesUpAfterSchedule pins that exhausting the schedule is not
// fatal: the watcher keeps running and the poll tick re-drives the scan.
func TestScanWithRetryGivesUpAfterSchedule(t *testing.T) {
	withFastRetries(t, time.Millisecond, time.Millisecond, time.Millisecond)
	fixture := newScanFixture(t, 101, 100)
	fixture.staleForever()

	require.NoError(t, fixture.run(fixture.notificationFor(101)))
	require.Equal(t, int64(4), fixture.node.countCalls.Load(), "one attempt per delay, plus the initial one")
	require.Equal(t, int64(100), fixture.dbHeight(t), "a replica stuck behind must not advance the stored tip")
}

// TestScanWithRetryGivesUpBeforeOverrunningPollInterval covers the lookahead: a
// delay that would outlast the poll interval is never entered, so the loop stops
// instead of sleeping past the tick that would have re-driven it anyway.
func TestScanWithRetryGivesUpBeforeOverrunningPollInterval(t *testing.T) {
	withFastRetries(t, 4*time.Second) // regtest polls every 3s
	fixture := newScanFixture(t, 101, 100)
	fixture.staleForever()

	start := time.Now()
	require.NoError(t, fixture.run(fixture.notificationFor(101)))
	require.Equal(t, int64(1), fixture.node.countCalls.Load(), "the schedule is not exhausted; the lookahead stops the loop")
	require.Less(t, time.Since(start), time.Second, "the too-long delay must never be entered")
}

func TestScanWithRetryStopsOnCancelledContext(t *testing.T) {
	withFastRetries(t, time.Millisecond, time.Millisecond, time.Millisecond)
	fixture := newScanFixture(t, 101, 100)
	fixture.staleForever()

	ctx, cancel := context.WithCancel(fixture.ctx)
	cancel()
	require.NoError(t, fixture.runCtx(ctx, fixture.notificationFor(101)), "shutdown is not a watcher failure")
	require.Equal(t, int64(1), fixture.node.countCalls.Load(), "shutdown must not wait out the retry schedule")
}

// TestScanWithRetryCountsRetries pins the accounting: each retried attempt is
// counted; the attempt that gives up is not a retry and is only logged.
func TestScanWithRetryCountsRetries(t *testing.T) {
	before := scanCounts(t, "chain_watcher.scan_retries")

	withFastRetries(t, time.Millisecond, time.Millisecond, time.Millisecond)
	fixture := newScanFixture(t, 101, 100)
	fixture.staleForever()
	require.NoError(t, fixture.run(fixture.notificationFor(101)))

	retries := scanCounts(t, "chain_watcher.scan_retries")["stale_tip"] - before["stale_tip"]
	require.Equal(t, fixture.node.countCalls.Load()-1, retries, "every attempt except the giving-up one is counted as a retry")
}
