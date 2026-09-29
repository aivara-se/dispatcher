# 008 - Deployment and Coexistence with the Poll

## Context

The poll and the dispatcher wake the same agents, and two wake sources for one bot means two wakes for one fact: duplicate work, duplicate messages, and a bot whose queue behaviour nobody can reason about because which mechanism woke it is not visible from the card. The new service has to run beside the Hermes gateway on a shared VPS, next to the cron jobs it is meant to replace.

## Decision

**Run it as one systemd user unit, beside the gateway.**

- The service binds loopback only; TLS terminates at the reverse proxy in front of it, which is what exposes the endpoint to GitHub. The proxy owns the public path and the certificate.
- Secrets arrive through the unit's environment file, mode `600`, owned by the service user — never on the unit's `ExecStart` line.
- Restart on failure, so a crash is a few seconds of missed deliveries rather than a night of them; GitHub's redelivery covers the gap.
- The gateway's webhook platform owns the routes: one per bot, bound to that bot's profile, its own secret. The subscriptions live in `~/.hermes/webhook_subscriptions.json` and are hot-reloaded, so a route change is live on the next event with no gateway restart.
- The board reads use a token of its own, read-only for the board, kept outside the routes file like every other secret.

**Migration is per bot, and one step in each direction.**

1. Create the bot's gateway route, bound to its profile with its own secret.
2. Point the repository's webhook at the dispatcher once.
3. Move the bot: its route goes live **and** its `queue_poll` cron entry stops being scheduled, in the same commit.
4. Read the audit log for that bot's first real event before moving the next one.

Rollback is the same step in reverse. The poll program is not deleted, only unscheduled, so a bot can return to a timer without a code change — which is the whole reason step 3 is one step and not two.

**The hourly stalled-work probe stays.** It is the fleet's clock, and it covers the one thing an event-driven service cannot see: nothing happening. "Untouched for a day" needs a timer and always will.

## Consequences

- Each bot's wake source is unambiguous at every commit: the move changes both facts together, so there is no window in which a bot is woken by both, and none in which it is covered by neither.
- The switch is small enough to do at a quiet moment and to reverse in one step, and the blast radius of a bad wake is one bot until the next bot moves.
- While both mechanisms exist, there are two things to understand and two places a rule can drift. That is the cost of migrating rather than cutting over, and it shrinks with every bot moved.
- The migration is only finished when `queue_poll` is scheduled for no bot. A migration that stalls half-way leaves the fleet paying for both, which is a worse state than either one alone.

## Rejected

- **Keeping the poll running as a permanent fallback.** It would wake a bot for every event the dispatcher also handled — exactly the duplication the service exists to remove — and it would hide a dispatcher that is quietly broken, because the fleet would keep working through the poll.
- **A flag day for all four bots at once.** One bad wake would then have four blast radii and no clean rollback; per-bot migration makes a mistake cheap to find and cheap to undo.
- **Routing inside the gateway process.** The gateway is the delivery platform; the fleet's rules are not its business, and coupling them would push every routing change into host configuration where a pull request cannot reach it.
- **Deleting the poll when the dispatcher lands.** Rollback would become a rebuild, and the poll is the only thing that works when the dispatcher does not.
- **Dropping the hourly probe too.** It answers a question no event can raise, and the fleet would lose its only warning about a card that has gone quiet.
