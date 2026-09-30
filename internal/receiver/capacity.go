package receiver

import "context"

// capacity is the gate behind the 503: a bound on the dispatches in flight and
// a bound on the requests waiting for one (docs/SYSTEMS.md section 7). Past
// both, the answer is 503 — honest, because the fact is not lost: GitHub
// retries it, and the audit log names the moment the service was over capacity.
type capacity struct {
	slots   chan struct{}
	pending chan struct{}
}

func newCapacity(inFlight, pending int) *capacity {
	if inFlight < 1 {
		inFlight = 1
	}
	if pending < 0 {
		pending = 0
	}
	return &capacity{slots: make(chan struct{}, inFlight), pending: make(chan struct{}, pending)}
}

// enter takes a dispatch slot, waiting in the bounded queue when every slot is
// busy, and reports false when the queue is full as well.
func (c *capacity) enter(ctx context.Context) bool {
	select {
	case c.slots <- struct{}{}:
		return true
	default:
	}
	select {
	case c.pending <- struct{}{}:
	default:
		return false
	}
	defer func() { <-c.pending }()
	select {
	case c.slots <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

// leave releases a slot taken by enter.
func (c *capacity) leave() { <-c.slots }
