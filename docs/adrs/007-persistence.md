# 007 - Persistence

## Context

The service has one piece of state worth naming — the dedup cache — and two records worth keeping: the audit trail and the dead-letter file. Everything else about the service is either configuration, which is read at boot, or the event in front of it. The question is whether v1 needs storage at all.

## Decision

v1 keeps **no state that cannot be rebuilt**:

- The **dedup cache** is in memory, bounded, with a TTL.
- The **audit log** is a size-rotated file, appended to in arrival order and read by a human or a script, never by the service.
- The **dead-letter file** is a file, appended to when a wake could not be delivered after its retries.
- No database, no queue, no key-value store, no volume beyond the directory the two files live in.

## Consequences

Deployment is a binary, a routes file and a log directory, and a restart needs no migration, no lock and no recovery step.

What breaks with the simpler choice, named rather than waved away:

- **A restart forgets recent deliveries.** A redelivery inside the TTL after a restart can wake an agent twice. The worst case is one extra agent turn against an unchanged card, so the outcome cannot be wrong, only repeated. The audit log shows it as two lines sharing a delivery id.
- **A failed wake is recovered by hand.** There is no replay daemon: the dead-letter file names the delivery, and GitHub's own redeliver is the mechanism — which is why the delivery id is in the file and why dead-lettering drops the dedup entry.
- **No coalescing state survives a restart.** It does not need to: the burst grouping is the gateway route's, not ours, and a restart loses at most the current quiet window on the gateway's side, not on ours.
- **There is no history of decisions** beyond the audit file. Rotation bounds it, so the trail has a horizon; an operator who needs longer keeps the files, and the service does not become an archive.

Revisit when the audit log shows duplicates or missed wakes often enough that files are not enough — and then add the smallest store that fixes the observed problem. Not a platform in anticipation of it.

## Rejected

- **A database in v1.** A service to run, back up, monitor and version, for a problem whose worst case is one extra agent turn.
- **An outbound queue with its own retry daemon.** A second delivery mechanism beside the HTTP dispatch and the dead-letter file, with its own failure modes and its own thing to keep alive; the bounded retry already covers the failure anyone has seen.
- **Writing state into the repository.** The repository is the design; a runtime that commits is a runtime that conflicts with its own deployments.
- **Replaying the audit log on startup.** The log would become an input instead of a record, and its reader would have to be exactly as idempotent as the dispatcher — one more chance to wake an agent twice for a fact it has already seen.
