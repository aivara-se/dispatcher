# 003 - Waking an Agent Through the Gateway

## Context

An event has to reach exactly one bot's agent, with the reason attached, without this service becoming a runtime for agents. Today the bot is woken by its own Hermes cron job: a monitor program prints the queue, and the job's agent turn runs only when that output changes.

The candidates, as they stood when this was decided:

1. POST to the target bot's Hermes webhook route — the gateway's webhook platform: a subscription per bot, its own HMAC secret, a prompt rendered from the payload, delivery into that bot's chat.
2. A `hermes` subprocess per event on the host.
3. Writing to a queue the bot reads.

How the fleet actually runs, checked rather than assumed. One gateway hosts the bots' profiles and multiplexes them; the webhook adapter is an HTTP server (port 8644 by default) serving routes at `/webhooks/<name>`, and on a multi-profile gateway at `/p/<profile>/webhooks/<name>`. A route carries its own secret, filters, a payload-to-prompt template, and a delivery target, and it is rate-limited, body-limited and idempotent on the delivery id, exactly as the receiver is. Subscriptions live in `~/.hermes/webhook_subscriptions.json` and are hot-reloaded, so a route is live on the next event with no gateway restart. A route can fire an existing cron job instead of starting a fresh agent session, passing the rendered prompt as transient context for that run.

## Decision

POST to the bot's own webhook route — candidate 1 — with the route firing the bot's existing queue job.

- One route per bot, bound to that bot's profile, with its own secret, so which agent runs is a property of the URL and the signature rather than of a payload field.
- The dispatcher composes the wake text and sends it as a small JSON envelope (named in the systems document) whose `reason` field carries the wake; the route's prompt renders from it. The dispatcher composes it because the facts that make the reason — the card, its stage, who acts next — are what this service has just resolved, and a template over the raw GitHub payload cannot know them.
- The route fires the bot's existing queue job rather than starting a separate webhook-only instruction set, so the bot keeps one set of wake instructions, one manual, one memory and one delivery path. The reason arrives as transient context; the job's own prompt, skills and delivery are unchanged.
- The signature on the wake is the same kind GitHub uses (`X-Hub-Signature-256`, HMAC over the raw body), so both hops verify the same way. A timestamped generic signature is preferable where the route accepts it — it carries replay protection the plain form lacks — and that is settled when the route is created.
- Bursts are grouped by the route itself, keyed on repository and card number, so several rapid comments on one card are one wake carrying the latest event.

## Consequences

- The dispatcher depends on a documented HTTP interface rather than on the fleet's internals, and it holds one secret per bot and no GitHub, Telegram or model credential of its own.
- Waking an agent is at-most-once per accepted delivery: the gateway's own dedup and the job's own claim are what protect the far end, so the dispatcher needs to know only that the wake was accepted, not that the agent finished.
- A gateway outage is a dispatcher outage. Nothing wakes while the gateway is down; the dead-letter file is the record and GitHub's redelivery is the recovery.
- The route's template and the dispatcher's reason text are two places that can describe the same wake. The rule that keeps them from drifting: the reason is the dispatcher's, the route holds no prose of its own beyond the envelope field.
- Tying the wake to a `cron_job` route means the route and the job's schedule must be understood together, and whether a job can exist with no schedule of its own is a host detail still open (systems document, section 12).

## Rejected

- **A `hermes` subprocess per event.** It makes this service responsible for the agent's lifecycle — the right profile's environment, the process tree, its exit status, its runaway — for behaviour the gateway already owns. It needs host access beyond an HTTP call, and its failures are invisible to the platform that owns sessions, memory and delivery.
- **A queue the bot reads.** A bot reading a queue is a poll with more moving parts: one more store to run, back up and watch, and the latency it was meant to remove returns as soon as the reader is on a timer. The platform already has an HTTP door with signature validation, idempotency and delivery.
- **Starting a fresh webhook session rather than firing the job.** It would give one bot two instruction sets for the same queue — the job's and the route's — and the second would live in host configuration where a code review cannot reach it.
- **Writing the wake straight into the bot's Telegram chat.** That is a notification, not a wake: it does not reach the agent's session, memory or tools as work.
- **Notifying the operator instead of the agent**, which moves the routing decision to a human.
