# dispatcher: product

This is the product document: what the service is, who it is for, and how it is judged. The architecture is in [SYSTEMS.md](SYSTEMS.md), and the decisions behind it are in [adrs/000-record-architecture-decisions.md](adrs/000-record-architecture-decisions.md) and the records beside it.

## 1. The problem

Work in the fleet is tracked on the `aivara-se` board, and an agent is woken by a poll: one program per bot, on a ten-minute timer, printing that bot's cards and re-reading the board through `gh`. The poll's cost is not tokens — the fleet's cron runs in monitor mode, so an unchanged printout suppresses the agent run and costs zero model calls — it is time, and it is blindness.

- **Latency.** The fact and the wake are up to ten minutes apart. A reviewer who asks for a change waits for the author's next tick. A card whose comment lands one second after a tick waits nearly ten minutes, and so does every reply after it.
- **Reads that buy nothing.** Four bots read the same board every ten minutes, and almost every tick finds nothing. The reads are cheap, but the habit is not: today the fleet's answer to "how does a bot learn that something happened" is a timer, so every event the poll cannot see needs another timer, and every new bot multiplies both.
- **Blindness.** The poll prints a comment count on the cards a bot already holds, so a comment there is visible. What it cannot see is everything that leaves no mark on a card's column or comment count: a review submitted on a pull request, a check run finishing, a review request (a request is not an assignment), a comment on a card that nobody has been assigned yet. Those are exactly the moments an agent needs to know about, and today nothing reports them.

## 2. Who it is for

The operator of a small fleet of agents: one person running four bots in the `aivara-se` organisation, each with its own profile, GitHub identity and chat. The bots are the users of the wake path — the ones woken — and the operator is who installs it, trusts it, and reads its log when a wake is missed.

## 3. What this is

`dispatcher` is an HTTP service that GitHub calls. It verifies the payload's signature, decides which single agent the event belongs to, and wakes that agent through the Hermes gateway with the reason attached: one event, one wake, one bot.

Two properties matter more than the mechanism. The event is a **reason, not an instruction** — the card, its comments and the board remain the source of truth, and the agent re-reads them, as it does today; the wake only says why now. And a bot is woken **once per fact, by exactly one source**: never the dispatcher and the poll for the same event.

## 4. What it is not

- Not a chat product, and not a second inbox. The agent's own delivery, memory and skills are unchanged; the dispatcher speaks to the gateway, never to a bot directly.
- Not a replacement for the agents, nor for the manual each of them follows. It wakes them; it does not decide their work.
- Not a scheduler for periodic work. "Untouched for a day" needs a clock, so the hourly stalled-work probe stays where it is; cron keeps the job it is good at.
- Not a second source of truth for the board, and not a writer to it. It chooses who to wake; the card says what to do.

## 5. How success is measured

- **Wake latency**: seconds between the GitHub event and the agent's wake, rather than minutes.
- **One wake per fact, and no missed fact**: a redelivered or coalesced event wakes an agent once; a comment, a reply, a review, a review request or a finished check run wakes the agent that owns it. The audit log is where both are read from.
- **Nothing woken for nothing**: a bot with no work is not woken at all, and no event wakes a bot it does not concern.
- **The poll switched off**: each bot's migration is one step at a known commit, and the end state is `queue_poll` scheduled for no bot — not two wake sources left running side by side.
