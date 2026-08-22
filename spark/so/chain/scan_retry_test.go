package chain

import (
	"bytes"
	"testing"
	"time"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	msdk "go.opentelemetry.io/otel/sdk/metric"
	md "go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// scanMetricReader captures the chain watcher's scan counters. The provider is
// installed in init so it is in place before watch_chain.go's init creates the
// counters — OTel's global delegation binds once per process, so setting the
// provider inside a test would leave them unbound.
var scanMetricReader = msdk.NewManualReader()

func init() {
	otel.SetMeterProvider(msdk.NewMeterProvider(msdk.WithReader(scanMetricReader)))
}

// scanCounts totals a counter's data points by reason.
func scanCounts(t *testing.T, name string) map[string]int64 {
	t.Helper()
	var rm md.ResourceMetrics
	require.NoError(t, scanMetricReader.Collect(t.Context(), &rm))
	counts := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			sum, ok := m.Data.(md.Sum[int64])
			if !ok {
				continue
			}
			for _, dp := range sum.DataPoints {
				reason, _ := dp.Attributes.Value(attribute.Key("reason"))
				counts[reason.AsString()] += dp.Value
			}
		}
	}
	return counts
}

func testBlockWithCoinbaseHeight(t *testing.T, height int64) *wire.MsgBlock {
	t.Helper()
	script, err := txscript.NewScriptBuilder().AddInt64(height).Script()
	require.NoError(t, err)

	coinbase := wire.NewMsgTx(wire.TxVersion)
	coinbase.AddTxIn(&wire.TxIn{
		PreviousOutPoint: *wire.NewOutPoint(&chainhash.Hash{}, wire.MaxPrevOutIndex),
		SignatureScript:  script,
	})
	coinbase.AddTxOut(&wire.TxOut{Value: 50e8, PkScript: []byte{txscript.OP_TRUE}})

	block := &wire.MsgBlock{
		Header: wire.BlockHeader{
			Version:   1,
			Timestamp: time.Unix(1e9, 0),
		},
	}
	require.NoError(t, block.AddTransaction(coinbase))
	return block
}

func serializeBlock(t *testing.T, block *wire.MsgBlock) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, block.Serialize(&buf))
	return buf.Bytes()
}

func TestParseBlockNotification(t *testing.T) {
	// Heights 1-16 serialize as OP_1..OP_16 small-int opcodes rather than data
	// pushes; both encodings must parse.
	for _, height := range []int64{5, 16, 17, 842000} {
		block := testBlockWithCoinbaseHeight(t, height)
		notification := parseBlockNotification(serializeBlock(t, block))
		require.True(t, notification.Parsed, "height %d", height)
		require.Equal(t, height, notification.Height)
		require.Equal(t, block.BlockHash(), notification.Hash)
	}
}

func TestParseBlockNotificationInvalid(t *testing.T) {
	require.False(t, parseBlockNotification(nil).Parsed)
	require.False(t, parseBlockNotification([]byte("TESTTESTTEST")).Parsed)

	// A structurally valid block whose coinbase lacks a BIP34 height (e.g.
	// early regtest blocks) must fall back to an unparsed notification.
	block := testBlockWithCoinbaseHeight(t, 100)
	block.Transactions[0].TxIn[0].SignatureScript = []byte{txscript.OP_RETURN}
	require.False(t, parseBlockNotification(serializeBlock(t, block)).Parsed)
}

func TestReachedNotification(t *testing.T) {
	hashA := chainhash.Hash{1}
	hashB := chainhash.Hash{2}
	notification := BlockNotification{Hash: hashA, Height: 100, Parsed: true}

	require.True(t, reachedNotification(Tip{Height: 100, Hash: hashA}, notification))
	require.True(t, reachedNotification(Tip{Height: 101, Hash: hashB}, notification), "chain past the notified height counts as reached")
	require.False(t, reachedNotification(Tip{Height: 99, Hash: hashB}, notification), "stale tip below the notified height")
	require.False(t, reachedNotification(Tip{Height: 100, Hash: hashB}, notification), "same-height different hash is a reorg, not reached")
	require.True(t, reachedNotification(Tip{Height: 1, Hash: hashB}, BlockNotification{}), "unparsed notification has no target; any tip suffices")
}
