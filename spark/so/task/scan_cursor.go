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
// position in memcache between ticks. A cursor that never persists is silent:
// every run re-walks the head of the table and the tail is never reached. Tasks
// relying on these helpers should therefore expose whether passes are actually
// completing, rather than treating cursor loss as an error.

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

// newScanCursorMemcacheClient returns nil when no server is configured, which
// the load/save/delete helpers treat as "no cursor available".
func newScanCursorMemcacheClient(cacheURI string) *memcache.Client {
	addrs := parseScanCursorMemcacheAddrs(cacheURI)
	if len(addrs) == 0 {
		return nil
	}
	mc := memcache.New(addrs...)
	mc.Timeout = scanCursorMemcacheTimeout
	return mc
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
