package receiver

import (
	"sync"
	"time"
)

// dedup is the delivery-id cache of ADR 004: bounded, in memory, one TTL, and
// nothing else. It holds identifiers, never payloads, and a restart empties it
// — which the decision record accepts, because the worst case is one extra
// agent turn against a card that is still the truth.
type dedup struct {
	mu      sync.Mutex
	entries map[string]time.Time
	ttl     time.Duration
	bound   int
	now     func() time.Time
}

func newDedup(ttl time.Duration, bound int, now func() time.Time) *dedup {
	if bound < 1 {
		bound = 1
	}
	return &dedup{entries: make(map[string]time.Time), ttl: ttl, bound: bound, now: now}
}

// seen records the delivery id and reports whether it was already there. The
// check and the record are one call on purpose: two deliveries of the same id
// arriving at once cannot both be told they are new.
func (d *dedup) seen(id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	d.sweep(now)
	if _, ok := d.entries[id]; ok {
		return true
	}
	if len(d.entries) >= d.bound {
		d.evictOldest()
	}
	d.entries[id] = now
	return false
}

// drop forgets a delivery, which is what a dead-lettered wake needs so that an
// operator's re-dispatch after the fix is not swallowed as a duplicate.
func (d *dedup) drop(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.entries, id)
}

// sweep forgets every entry older than the TTL, which is what keeps the two
// hops forgetting a delivery at the same rate.
func (d *dedup) sweep(now time.Time) {
	for id, seen := range d.entries {
		if now.Sub(seen) >= d.ttl {
			delete(d.entries, id)
		}
	}
}

// evictOldest forgets the entry seen longest ago, so the cache is bounded by
// its bound rather than by traffic.
func (d *dedup) evictOldest() {
	var oldest string
	var at time.Time
	for id, seen := range d.entries {
		if oldest == "" || seen.Before(at) {
			oldest, at = id, seen
		}
	}
	if oldest != "" {
		delete(d.entries, oldest)
	}
}
