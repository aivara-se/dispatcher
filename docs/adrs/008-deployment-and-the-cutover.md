# 008 - Deployment and the Cutover

## Context

The poll and the dispatcher wake the same agents, and two wake sources for one bot means two wakes for one fact: duplicate work, duplicate messages, and a bot whose queue behaviour nobody can reason about because which mechanism woke it is not visible from the card. The new service has to run beside the Hermes gateway on a shared VPS, next to the cron jobs it is meant to replace.

**Amended 2026-09-30, by the operator's decision recorded in #3.** The coexistence half of this record is void. The poll-backed process is removed entirely before the dispatcher runs, so the two are never alive at the same time because there is no longer a second one; there is no per-bot migration, and the downtime between the poll going and the service being stable is accepted. The deployment half below is unchanged.

## Decision

**Run it as one systemd user unit, beside the gateway.**

- The service binds loopback only; TLS terminates at the reverse proxy in front of it, which is what exposes the endpoint to GitHub. The proxy owns the public path and the certificate.
- Secrets arrive through the unit's environment file, mode `600`, owned by the service user — never on the unit's `ExecStart` line.
- Restart on failure, so a crash is a few seconds of missed deliveries rather than a night of them; GitHub's redelivery covers the gap.
- The gateway's webhook platform owns the routes: one per bot, bound to that bot's profile, its own secret. The subscriptions live in `~/.hermes/webhook_subscriptions.json` and are hot-reloaded, so a route change is live on the next event with no gateway restart.
- The board reads use a token of its own, read-only for the board, kept outside the routes file like every other secret.

**The cutover is a clean slate** (settled 2026-09-30, replacing the per-bot migration this record first described):

1. The service is finished, reviewed and green on `main`.
2. Every bot's `queue_poll` cron entry is removed, and the poll's programs are deleted with them.
3. Only then is the dispatcher's unit started.

The downtime between step 2 and the service being stable is accepted, and during it the operator reaches the fleet by dispatching tasks by hand. Nothing keeps the poll alive for the transition and nothing rolls back to it: the programs go with their cron entries, so an outage is recovered by fixing the dispatcher rather than by returning to a timer.

**The hourly stalled-work probe stays.** It is the fleet's clock, and it covers the one thing an event-driven service cannot see: nothing happening. "Untouched for a day" needs a timer and always will. It was never the poll, and with the poll retired it is also the only scheduled thing left that can notice a bot has gone quiet.

## Consequences

- Each bot's wake source is unambiguous at every commit: there is one source, and no commit exists in which two could wake the same bot.
- Nothing is migrated, so no rule has to hold in two mechanisms at once — the cost the per-bot migration was paying is simply not paid.
- What it costs: there is **no rollback to the poll**. A dispatcher outage is a fleet nothing is waking, and the way to reach a bot during one is the operator dispatching by hand. That is the accepted price, and it was chosen knowingly: the poll is also what would have hidden a dispatcher that was quietly broken.
- The gap between the poll going and the service being stable is real downtime, taken at a moment the operator chooses rather than discovered later.
- The blast radius of a bad first wake is the whole fleet rather than one bot — one step, so one mistake. That is exactly why the code is finished, reviewed and green before step 2 is taken.
- Nothing wakes anyone after step 2 except the dispatcher, so the routes, their secrets and a real event through them have to be exercised before it.

## Rejected

- **Keeping the poll running as a permanent fallback.** It would wake a bot for every event the dispatcher also handled — exactly the duplication the service exists to remove — and it would hide a dispatcher that is quietly broken, because the fleet would keep working through the poll.
- **Routing inside the gateway process.** The gateway is the delivery platform; the fleet's rules are not its business, and coupling them would push every routing change into host configuration where a pull request cannot reach it.
- **Dropping the hourly probe too.** It answers a question no event can raise, and the fleet would lose its only warning about a card that has gone quiet.
- **A flag day for all four bots at once** *(rejected first, taken 2026-09-30)*. It was rejected for having four blast radii and no clean rollback. It is taken now because the alternative — migrating one bot at a time — keeps both wake sources alive for the length of the migration, which is precisely the state the service exists to end; and because the rollback it protects is to a mechanism that is going away.
- **Deleting the poll when the dispatcher lands** *(rejected first, taken 2026-09-30)*. It was rejected because rollback would become a rebuild. It is taken now because the poll's programs are the second implementation of every rule, and an escape hatch nobody has to keep correct is worth less than one fewer mechanism: the fleet fixes forward.
