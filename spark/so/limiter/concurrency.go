package limiter

import "sync"

// A ResourceGuard manages access to resources identified by string keys. Useful for limiting
// concurrent access (within a single process) to resources identified by strings.
type ResourceGuard struct {
	resources sync.Map
}

func NewResourceGuard() ResourceGuard {
	return ResourceGuard{resources: sync.Map{}}
}

func (rg *ResourceGuard) Acquire(key string) bool {
	_, loaded := rg.resources.LoadOrStore(key, struct{}{})
	return !loaded
}

func (rg *ResourceGuard) Release(key string) {
	rg.resources.Delete(key)
}
