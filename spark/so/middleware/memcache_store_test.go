package middleware

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bradfitz/gomemcache/memcache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeMemcacheClient struct {
	values map[string][]byte
}

func newFakeMemcacheClient() *fakeMemcacheClient {
	return &fakeMemcacheClient{values: make(map[string][]byte)}
}

func (f *fakeMemcacheClient) Get(key string) (*memcache.Item, error) {
	if v, ok := f.values[key]; ok {
		return &memcache.Item{Key: key, Value: v}, nil
	}
	return nil, memcache.ErrCacheMiss
}

func (f *fakeMemcacheClient) Set(item *memcache.Item) error {
	f.values[item.Key] = append([]byte(nil), item.Value...)
	return nil
}

func (f *fakeMemcacheClient) Decrement(key string, delta uint64) (uint64, error) {
	v, ok := f.values[key]
	if !ok {
		return 0, memcache.ErrCacheMiss
	}
	// Real memcache may pad values with whitespace due to fixed byte lengths,
	// so trim before parsing to match production behavior.
	curr, err := strconv.ParseUint(strings.TrimSpace(string(v)), 10, 64)
	if err != nil {
		return 0, err
	}
	if curr > delta {
		curr -= delta
	} else {
		curr = 0
	}
	// After decrement, real memcache stores the new value with potential padding.
	// To simulate this, we pad the new value to the same length as the original.
	newVal := strconv.FormatUint(curr, 10)
	if len(newVal) < len(v) {
		// Pad with leading spaces to maintain fixed length (like real memcache).
		padding := make([]byte, len(v)-len(newVal))
		for i := range padding {
			padding[i] = ' '
		}
		newVal = string(padding) + newVal
	}
	f.values[key] = []byte(newVal)
	return curr, nil
}

func TestMemcacheStore_SetGetTake(t *testing.T) {
	ctx := t.Context()
	fc := newFakeMemcacheClient()
	store := NewMemcacheStoreWithClient(fc)

	// Missing keys should return zeroes and no error.
	tokens, remaining, err := store.Get(ctx, "bucketA")
	require.NoError(t, err)
	assert.Equal(t, uint64(0), tokens)
	assert.Equal(t, uint64(0), remaining)

	// Initialize with capacity=3
	require.NoError(t, store.Set(ctx, "bucketA", 3, 2*time.Second))

	// Get should reflect capacity and remaining
	tokens, remaining, err = store.Get(ctx, "bucketA")
	require.NoError(t, err)
	assert.Equal(t, uint64(3), tokens)
	assert.Equal(t, uint64(3), remaining)

	// Take should decrement
	ok, err := store.Take(ctx, "bucketA")
	require.NoError(t, err)
	assert.True(t, ok)

	// Subsequent Get should show remaining=2
	tokens, remaining, err = store.Get(ctx, "bucketA")
	require.NoError(t, err)
	assert.Equal(t, uint64(3), tokens)
	assert.Equal(t, uint64(2), remaining)

	// Take down to zero
	ok, err = store.Take(ctx, "bucketA")
	require.NoError(t, err)
	assert.True(t, ok)
	ok, err = store.Take(ctx, "bucketA")
	require.NoError(t, err)
	assert.True(t, ok) // Decrement clamps at 0, still ok

	// Remaining should be 0
	_, remaining, err = store.Get(ctx, "bucketA")
	require.NoError(t, err)
	assert.Equal(t, uint64(0), remaining)
}

func TestMemcacheStore_TakeOnMissingRemaining(t *testing.T) {
	ctx := t.Context()
	fc := newFakeMemcacheClient()
	store := NewMemcacheStoreWithClient(fc)

	// Set capacity only to simulate eviction of remaining key
	require.NoError(t, store.Set(ctx, "bucketB", 2, time.Second))
	delete(fc.values, remKey("bucketB"))

	// Take should return ok=false with no error (treat as race/miss)
	ok, err := store.Take(ctx, "bucketB")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestMemcacheStore_GetOnMixedCacheMiss(t *testing.T) {
	ctx := t.Context()
	fc := newFakeMemcacheClient()
	store := NewMemcacheStoreWithClient(fc)

	// Initialize then drop remaining only
	require.NoError(t, store.Set(ctx, "bucketC", 5, time.Second))
	delete(fc.values, remKey("bucketC"))

	// Get should treat as miss and return zeros
	tokens, remaining, err := store.Get(ctx, "bucketC")
	require.NoError(t, err)
	assert.Equal(t, uint64(0), tokens)
	assert.Equal(t, uint64(0), remaining)
}

func TestMemcacheStore_GetOnMissingCapacity(t *testing.T) {
	ctx := t.Context()
	fc := newFakeMemcacheClient()
	store := NewMemcacheStoreWithClient(fc)

	// Initialize then drop capacity only (opposite of previous test)
	require.NoError(t, store.Set(ctx, "bucketC2", 5, time.Second))
	delete(fc.values, capKey("bucketC2"))

	// Get should treat as miss and return zeros
	tokens, remaining, err := store.Get(ctx, "bucketC2")
	require.NoError(t, err)
	assert.Equal(t, uint64(0), tokens)
	assert.Equal(t, uint64(0), remaining)
}

func TestMemcacheStore_ExpirationMiss(t *testing.T) {
	ctx := t.Context()
	fc := newFakeMemcacheClient()
	store := NewMemcacheStoreWithClient(fc)

	// Initialize
	require.NoError(t, store.Set(ctx, "bucketD", 4, time.Second))
	// Simulate expiration by deleting both keys
	delete(fc.values, capKey("bucketD"))
	delete(fc.values, remKey("bucketD"))

	// Get should return zeros (store expects caller to re-Set)
	tokens, remaining, err := store.Get(ctx, "bucketD")
	require.NoError(t, err)
	assert.Equal(t, uint64(0), tokens)
	assert.Equal(t, uint64(0), remaining)

	// Take should return ok=false without error
	ok, err := store.Take(ctx, "bucketD")
	require.NoError(t, err)
	assert.False(t, ok)
}

// Error-path tests: ensure non-ErrCacheMiss errors are propagated from the client

type errMemcacheClient struct {
	*fakeMemcacheClient
	getErrKeys map[string]error
	setErrKeys map[string]error
	decErrKeys map[string]error
}

func newErrMemcacheClient() *errMemcacheClient {
	return &errMemcacheClient{
		fakeMemcacheClient: newFakeMemcacheClient(),
		getErrKeys:         map[string]error{},
		setErrKeys:         map[string]error{},
		decErrKeys:         map[string]error{},
	}
}

func (e *errMemcacheClient) Get(key string) (*memcache.Item, error) {
	if err, ok := e.getErrKeys[key]; ok {
		return nil, err
	}
	return e.fakeMemcacheClient.Get(key)
}

func (e *errMemcacheClient) Set(item *memcache.Item) error {
	if err, ok := e.setErrKeys[item.Key]; ok {
		return err
	}
	return e.fakeMemcacheClient.Set(item)
}

func (e *errMemcacheClient) Decrement(key string, delta uint64) (uint64, error) {
	if err, ok := e.decErrKeys[key]; ok {
		return 0, err
	}
	return e.fakeMemcacheClient.Decrement(key, delta)
}

func TestMemcacheStore_GetErrorPropagates(t *testing.T) {
	ctx := t.Context()
	ec := newErrMemcacheClient()
	store := NewMemcacheStoreWithClient(ec)

	ec.getErrKeys[capKey("bucketErrGet")] = fmt.Errorf("backend get failure")

	_, _, err := store.Get(ctx, "bucketErrGet")
	require.Error(t, err)
	require.Contains(t, err.Error(), "backend get failure")
}

func TestMemcacheStore_SetErrorPropagates(t *testing.T) {
	ctx := t.Context()
	ec := newErrMemcacheClient()
	store := NewMemcacheStoreWithClient(ec)

	// Fail setting capacity first
	ec.setErrKeys[capKey("bucketErrSet")] = fmt.Errorf("backend set failure")
	err := store.Set(ctx, "bucketErrSet", 3, time.Second)
	require.Error(t, err)
	require.Contains(t, err.Error(), "backend set failure")

	// Succeed capacity, fail remaining
	ec = newErrMemcacheClient()
	store = NewMemcacheStoreWithClient(ec)
	ec.setErrKeys[remKey("bucketErrSet2")] = fmt.Errorf("backend set failure 2")
	err = store.Set(ctx, "bucketErrSet2", 3, time.Second)
	require.Error(t, err)
	require.Contains(t, err.Error(), "backend set failure 2")
}

func TestMemcacheStore_TakeErrorPropagates(t *testing.T) {
	ctx := t.Context()
	ec := newErrMemcacheClient()
	store := NewMemcacheStoreWithClient(ec)

	require.NoError(t, store.Set(ctx, "bucketErrTake", 2, time.Second))
	// Force a non-ErrCacheMiss error on decrement
	ec.decErrKeys[remKey("bucketErrTake")] = fmt.Errorf("backend decrement failure")

	_, err := store.Take(ctx, "bucketErrTake")
	require.Error(t, err)
	require.Contains(t, err.Error(), "backend decrement failure")
}

func TestMemcacheStore_CorruptedCapacity(t *testing.T) {
	ctx := t.Context()
	fc := newFakeMemcacheClient()
	store := NewMemcacheStoreWithClient(fc)

	// Set valid data first
	require.NoError(t, store.Set(ctx, "bucketCorrupt", 10, time.Second))

	// Corrupt the capacity value to non-numeric data
	fc.values[capKey("bucketCorrupt")] = []byte("not-a-number")

	// Get should return parse error
	_, _, err := store.Get(ctx, "bucketCorrupt")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid syntax")
}

func TestMemcacheStore_CorruptedRemaining(t *testing.T) {
	ctx := t.Context()
	fc := newFakeMemcacheClient()
	store := NewMemcacheStoreWithClient(fc)

	// Set valid data first
	require.NoError(t, store.Set(ctx, "bucketCorrupt2", 10, time.Second))

	// Corrupt the remaining value to non-numeric data
	fc.values[remKey("bucketCorrupt2")] = []byte("invalid")

	// Get should return parse error
	_, _, err := store.Get(ctx, "bucketCorrupt2")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid syntax")
}

func TestMemcacheStore_WhitespaceTrimming(t *testing.T) {
	ctx := t.Context()
	fc := newFakeMemcacheClient()
	store := NewMemcacheStoreWithClient(fc)

	// Start with capacity=100 (3 digits)
	require.NoError(t, store.Set(ctx, "bucketWhitespace", 100, time.Second))

	// Take tokens multiple times to trigger padding
	for range 5 {
		ok, err := store.Take(ctx, "bucketWhitespace")
		require.NoError(t, err)
		assert.True(t, ok)
	}

	// After 5 takes, remaining should be 95 (2 digits)
	// The fake client will pad this to " 95" (3 bytes)
	tokens, remaining, err := store.Get(ctx, "bucketWhitespace")
	require.NoError(t, err)
	assert.Equal(t, uint64(100), tokens)
	assert.Equal(t, uint64(95), remaining)

	// Verify the stored value actually has padding
	storedValue := fc.values[remKey("bucketWhitespace")]
	assert.Len(t, storedValue, 3, "value should maintain original byte length")
	assert.Equal(t, byte(' '), storedValue[0], "value should have leading space padding")

	// Continue taking to get to single digit
	for range 90 {
		ok, err := store.Take(ctx, "bucketWhitespace")
		require.NoError(t, err)
		assert.True(t, ok)
	}

	// Remaining should be 5 (1 digit), padded to "  5" (3 bytes)
	tokens, remaining, err = store.Get(ctx, "bucketWhitespace")
	require.NoError(t, err)
	assert.Equal(t, uint64(100), tokens)
	assert.Equal(t, uint64(5), remaining)

	storedValue = fc.values[remKey("bucketWhitespace")]
	assert.Len(t, storedValue, 3, "value should maintain original byte length")
	assert.Equal(t, []byte("  5"), storedValue, "value should be padded with two spaces")
}

func TestMemcacheStore_ZeroWindowDuration(t *testing.T) {
	ctx := t.Context()
	fc := newFakeMemcacheClient()
	store := NewMemcacheStoreWithClient(fc)

	// Set with zero window (should use expiration=0, meaning no expiration)
	require.NoError(t, store.Set(ctx, "bucketZeroWindow", 5, 0))

	// Should still be able to get and take
	tokens, remaining, err := store.Get(ctx, "bucketZeroWindow")
	require.NoError(t, err)
	assert.Equal(t, uint64(5), tokens)
	assert.Equal(t, uint64(5), remaining)

	ok, err := store.Take(ctx, "bucketZeroWindow")
	require.NoError(t, err)
	assert.True(t, ok)
}
