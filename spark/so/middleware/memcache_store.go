package middleware

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/bradfitz/gomemcache/memcache"
)

// memcacheClient is the minimal surface used by memcacheStore.
type memcacheClient interface {
	Get(key string) (*memcache.Item, error)
	Set(item *memcache.Item) error
	Decrement(key string, delta uint64) (uint64, error)
}

// memcacheStore implements MemoryStore using Memcached.
// It stores two keys per bucket:
// - cap:<key> => capacity (tokens) with TTL = window
// - rem:<key> => remaining tokens with TTL = window
//
// Window resets are handled by Memcached expiration. When a key expires,
// Get returns (0, 0), which prompts the caller to reinitialize the bucket via Set.
type memcacheStore struct {
	client memcacheClient
}

func NewMemcacheStore(maxIdleConns int, addrs ...string) (MemoryStore, error) {
	trimmed := make([]string, 0, len(addrs))
	for _, a := range addrs {
		a = strings.TrimSpace(a)
		a = strings.TrimPrefix(a, "memcaches://")
		a = strings.TrimPrefix(a, "memcache://")
		if a != "" {
			trimmed = append(trimmed, a)
		}
	}
	c := memcache.New(trimmed...)
	if maxIdleConns > 0 {
		// We expect relatively high parallel traffic to memcache from the rate
		// limiter. Use a configurable MaxIdleConns so we can tune connection
		// reuse and avoid excessive connection churn and connect timeouts.
		c.MaxIdleConns = maxIdleConns
	}
	return &memcacheStore{client: c}, nil
}

// NewMemcacheStoreWithClient is intended for tests.
func NewMemcacheStoreWithClient(c memcacheClient) MemoryStore {
	return &memcacheStore{client: c}
}

func capKey(key string) string { return "cap:" + key }
func remKey(key string) string { return "rem:" + key }

func seconds(d time.Duration) int32 {
	if d <= 0 {
		return 0
	}
	return int32(d.Seconds())
}

func (s *memcacheStore) Get(ctx context.Context, key string) (tokens uint64, remaining uint64, err error) {
	capItem, err := s.client.Get(capKey(key))
	if err != nil {
		if errors.Is(err, memcache.ErrCacheMiss) {
			return 0, 0, nil
		}
		return 0, 0, err
	}
	remItem, err := s.client.Get(remKey(key))
	if err != nil {
		if errors.Is(err, memcache.ErrCacheMiss) {
			return 0, 0, nil
		}
		return 0, 0, err
	}

	// Memcache values have fixed byte lengths, so when going to smaller values if the byte string is shorter we introduce whitespace.
	capacity, err := strconv.ParseUint(strings.TrimSpace(string(capItem.Value)), 10, 64)
	if err != nil {
		return 0, 0, err
	}
	rem, err := strconv.ParseUint(strings.TrimSpace(string(remItem.Value)), 10, 64)
	if err != nil {
		return 0, 0, err
	}
	return capacity, rem, nil
}

func (s *memcacheStore) Set(ctx context.Context, key string, tokens uint64, window time.Duration) error {
	exp := seconds(window)
	capacityStr := strconv.FormatUint(tokens, 10)
	if err := s.client.Set(&memcache.Item{
		Key:        capKey(key),
		Value:      []byte(capacityStr),
		Expiration: exp,
	}); err != nil {
		return err
	}
	if err := s.client.Set(&memcache.Item{
		Key:        remKey(key),
		Value:      []byte(capacityStr),
		Expiration: exp,
	}); err != nil {
		return err
	}
	return nil
}

func (s *memcacheStore) Take(ctx context.Context, key string) (ok bool, err error) {
	_, derr := s.client.Decrement(remKey(key), 1)
	if derr != nil {
		if errors.Is(derr, memcache.ErrCacheMiss) {
			// Treat as a race/uninitialized window: signal no token without error.
			return false, nil
		}
		return false, derr
	}
	return true, nil
}
