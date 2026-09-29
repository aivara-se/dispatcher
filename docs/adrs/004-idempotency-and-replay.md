# 004 - Idempotency and Replay

## Context

GitHub delivers at least once and redelivers any delivery the endpoint did not answer with a 2xx, including one it answered too slowly to answer at all. The same event therefore arrives more than once through no fault of anyone's. A second wake for one fact costs an agent turn and, worse, a second message in the bot's chat, which is the thing a human notices.

## Decision

- **Idempotent at the delivery id, not at the event.** `X-GitHub-Delivery` is the key. A delivery already seen is answered `202` with a duplicate status and dispatches nothing.
- **A bounded cache with a one-hour TTL**, matching the TTL the gateway's own webhook adapter uses, so the two hops forget a delivery at the same rate and neither remembers one the other has dropped.
- **In memory.** No store, no file, no migration; see [007](007-persistence.md) for what that costs.
- **No ordering assumption anywhere.** Events are treated as reasons to re-read: the wake tells the agent that something happened and which card it concerns, and the card is right whatever order the wakes arrived in. Nothing in the service buffers events to restore an order, and nothing depends on one.
- **A dead-lettered delivery loses its dedup entry**, so an operator's redelivery after fixing the cause is not swallowed as a duplicate.
- **The reason, not the payload, is what the wake carries.** A duplicate wake is therefore one agent turn against a source of truth that has not changed, not a second action on stale data.

## Consequences

- Failure and retry are covered by one mechanism for both hops: the receiver answers a duplicate honestly, and the gateway drops a repeated delivery id of its own accord.
- The cache is lost on restart, so a redelivery inside the TTL after a restart can wake an agent twice. This is accepted. The worst case is one extra agent turn, and the agent re-reads the card, so the outcome cannot be wrong — only repeated.
- Memory is bounded by the cache bound, not by traffic; the bound is config and must be sized for a burst, not for a day.
- A duplicate that a bot's chat shows twice is the observable symptom, and the audit log is where it is confirmed — two lines with the same delivery id, one of them after a restart, is the shape to look for.

## Rejected

- **No dedup at all.** Every failed delivery would become a second wake, and failure is exactly when the sender retries hardest.
- **A persistent dedup store in v1.** A database or a key-value file to run, back up and monitor — for a problem whose worst case is one extra agent turn. It also drags in the reason to keep state, which v1 is better off not having.
- **Processing events in order.** Reconstructing an order from a stream whose delivery order is not guaranteed buys nothing when the card already carries the truth, and it needs a buffer, a head-of-line block and a timeout policy that only exist to serve the ordering.
- **Deduplicating on the event payload.** Two genuine events can be byte-identical in the fields that matter — the same action on the same card twice — and the second is real. The delivery id distinguishes them; a payload hash cannot.
