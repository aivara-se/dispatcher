package receiver

import (
	"sync"
	"time"
)

// bucket is the inbound rate limit: a token bucket over the whole endpoint,
// because the endpoint is public and an unauthenticated request must not be
// able to cost more than a token. A refusal is a 429, which GitHub retries, so
// a burst that arrives too fast is delayed rather than lost.
type bucket struct {
	mu        sync.Mutex
	burst     float64
	perSecond float64
	tokens    float64
	last      time.Time
	now       func() time.Time
}

func newBucket(burst, perSecond float64, now func() time.Time) *bucket {
	return &bucket{burst: burst, perSecond: perSecond, tokens: burst, now: now}
}

// allow takes a token if there is one.
func (b *bucket) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	if b.last.IsZero() {
		b.last = now
	} else if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens = min(b.burst, b.tokens+elapsed*b.perSecond)
		b.last = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
