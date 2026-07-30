package task

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bradfitz/gomemcache/memcache"
	"github.com/google/uuid"
)

// Tasks that walk a table too large to cover in a single run persist their scan
// position in memcache between ticks. A cursor that never persists costs
// coverage rather than correctness: every run re-walks the head of the table and
// the tail is never reached. Tasks relying on these helpers should therefore
// expose whether passes are actually completing, rather than treating cursor loss
// as an error.
//
// Only the load path is silent about that — it cannot distinguish a cache miss
// from an unreachable cache, and both mean "start from the oldest row". Save and
// delete failures are returned and warned on by the callers, once per run.
//
// The scheme in a CacheURI (`memcaches://`) is parsed off and discarded, and
// gomemcache dials plain TCP unless Client.DialContext is set with a tls.Dialer.
// Against an endpoint that requires in-transit encryption the client never
// negotiates TLS at all — it writes the memcache protocol straight onto the socket
// and the failure surfaces per operation as an EOF, reset, or timeout, which these
// helpers treat as an absent cursor. Keying DialContext off the scheme belongs
// here if that is ever needed. The rate limiter's store
// (so/middleware/memcache_store.go) consumes the same CacheURI over plain TCP, so a
// working rate-limiter memcache is evidence that no TLS dialer is required.

const (
	// Cursor TTL exceeds any task's run interval by a wide margin so a transient
	// memcache outage doesn't reset progress on a healthy table. Loss of the
	// cursor is harmless — the next run just restarts at the oldest row.
	scanCursorTTL = 7 * 24 * 3600

	scanCursorMemcacheTimeout = 2 * time.Second
)

func scanCursorKey(prefix string, operatorIndex uint64) string {
	return fmt.Sprintf("%s:%d", prefix, operatorIndex)
}

// newScanCursorMemcacheClient returns a nil client when no usable server is
// configured, which the load/save/delete helpers treat as "no cursor available".
//
// Server resolution is all-or-nothing: one bad address leaves no usable server, so
// the returned error names the addresses that failed rather than reporting only
// that the client has none. Callers warn instead of failing, since the client is
// rebuilt per run and an unusable one costs coverage rather than correctness.
func newScanCursorMemcacheClient(cacheURI string) (*memcache.Client, error) {
	addrs := parseScanCursorMemcacheAddrs(cacheURI)
	if len(addrs) == 0 {
		return nil, nil
	}

	servers := new(memcache.ServerList)
	if err := servers.SetServers(addrs...); err != nil {
		return nil, fmt.Errorf("failed to resolve memcache servers: %w", attributeUnresolvableAddrs(addrs, err))
	}
	mc := memcache.NewFromSelector(servers)
	mc.Timeout = scanCursorMemcacheTimeout
	return mc, nil
}

// attributeUnresolvableAddrs resolves each address on its own to work out which
// ones a whole-list failure came from, since the list-level error does not say.
// Runs only on the failure path, so the duplicated resolution costs nothing in the
// normal case. Falls back to the original error if every address resolves alone,
// which would mean the failure was not attributable to a single one.
func attributeUnresolvableAddrs(addrs []string, listErr error) error {
	var errs []error
	for _, addr := range addrs {
		if err := new(memcache.ServerList).SetServers(addr); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", addr, err))
		}
	}
	if len(errs) == 0 {
		return listErr
	}
	return errors.Join(errs...)
}

// parseScanCursorMemcacheAddrs splits a CacheURI into individual server
// addresses. The URI carries an optional scheme and may list several servers,
// e.g. memcaches://host:11211,host2:11211 — gomemcache resolves each address
// separately, so a comma-joined string handed to it whole never connects.
func parseScanCursorMemcacheAddrs(cacheURI string) []string {
	addrs := make([]string, 0, 1)
	for addr := range strings.SplitSeq(cacheURI, ",") {
		addr = strings.TrimSpace(addr)
		addr = strings.TrimPrefix(addr, "memcaches://")
		addr = strings.TrimPrefix(addr, "memcache://")
		if addr != "" {
			addrs = append(addrs, addr)
		}
	}
	return addrs
}

// loadScanCursor returns the persisted scan cursor, or nil if no usable cursor
// is available (no client, cache miss, or malformed value). nil means "start
// from the oldest row".
func loadScanCursor(mc *memcache.Client, key string) *uuid.UUID {
	if mc == nil {
		return nil
	}
	item, err := mc.Get(key)
	if err != nil {
		return nil
	}
	parsed, err := uuid.Parse(string(item.Value))
	if err != nil {
		return nil
	}
	return &parsed
}

func saveScanCursor(mc *memcache.Client, key string, cursor uuid.UUID) error {
	if mc == nil {
		return nil
	}
	return mc.Set(&memcache.Item{
		Key:        key,
		Value:      []byte(cursor.String()),
		Expiration: scanCursorTTL,
	})
}

func deleteScanCursor(mc *memcache.Client, key string) error {
	if mc == nil {
		return nil
	}
	if err := mc.Delete(key); err != nil && !errors.Is(err, memcache.ErrCacheMiss) {
		return err
	}
	return nil
}
