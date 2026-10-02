# dispatcher: product

This is the product document: what the service is, who it is for, and how it is judged. The architecture is in [SYSTEMS.md](SYSTEMS.md), the rules the service and the agents follow are in [RULESET.md](RULESET.md), and the decisions behind it are in [adrs/000-record-architecture-decisions.md](adrs/000-record-architecture-decisions.md) and the records beside it.

## 1. The problem

Work in the fleet is tracked on the `aivara-se` board, and a bot's day used to be set by a poll: one program per bot, on a ten-minute timer, printing that bot's cards and re-reading the board through `gh`. The poll's cost was not tokens — the fleet's cron runs in monitor mode, so an unchanged printout suppresses the agent run and costs zero model calls — it was time, and it was blindness.

- **Latency.** The fact and the wake were up to ten minutes apart. A reviewer who asked for a change waited for the author's next tick. A card whose comment landed one second after a tick waited nearly ten minutes, and so did every reply after it.
- **Reads that buy nothing.** Four bots read the same board every ten minutes, and almost every tick found nothing. The reads were cheap but the habit was not: the fleet's answer to "how does a bot learn that something happened" was a timer, so every event the poll could not see needed another timer, and every new bot multiplied both.
- **Blindness.** The poll printed a comment count on the cards a bot already held, so a comment there was visible. What it could not see is everything that leaves no mark on a card's column or comment count: a review submitted on a pull request, a check run finishing, a review request (a request is not an assignment), a comment on a card nobody had been assigned yet. Those are exactly the moments an agent needs to know about, and nothing reported them.
- **Work that had nobody on it, or two bots on it.** Nothing assigned a card except the operator's hand or a bot's own claim, and a claim is a race: two bots read the same free card and the faster one takes it, not the free one. Nothing stopped a bot holding two cards at once, nothing woke a bot whose blocked card had just been unblocked, and review duty sat in a board column no rule owned — so an author could be woken to review their own work.

## 2. Who it is for

The operator of a small fleet of agents: one person running four bots in the `aivara-se` organisation, each with its own profile, GitHub identity and chat. The bots are the users of the wake path — the ones woken — and the operator is who installs it, trusts it, and reads its log when a wake is missed.

## 3. What this is

`dispatcher` is an HTTP service that GitHub calls. It verifies the payload's signature, decides which single agent a fact concerns and who holds a card, writes that assignment to the board when the card has nobody on it, and wakes the agent through the Hermes gateway with the reason attached: one card, one bot, one wake.

Three properties matter more than the mechanism:

- The event is a **reason, not an instruction** — the card, its comments and the board remain the source of truth, and the agent re-reads them; the wake only says why now.
- A bot is woken **once per fact, by exactly one source**: the dispatcher, because the poll is removed rather than left running beside it (section 5).
- **One card at a time.** The service decides the assignment itself, from the board and the fleet's order, and starts no second card for a bot that is already in a session. A review is not a session and never takes the slot.

## 4. What it is not

- Not a chat product, and not a second inbox. The agent's own delivery, memory and skills are unchanged; the dispatcher speaks to the gateway, never to a bot directly.
- Not the place that says what to do. It assigns a card and wakes its holder; the card's text, its comments and its stage are the work. No manual for the agents lives in this service.
- Not a scheduler for periodic work. "Untouched for a day" needs a clock, so the hourly stalled-work probe stays where it is; cron keeps the job it is good at.
- Not the board's bookkeeper. It writes exactly one field — the assignee of a card nobody holds — and moves nothing: the columns stay the agent's own record of its work.
- Not a second source of truth for the board, beyond that one field. It chooses who holds a card and who is woken; the card says what to do.

## 5. How success is measured

- **Wake latency**: seconds between the GitHub event and the agent's wake, rather than minutes.
- **One wake per fact, and no missed fact**: a redelivered or coalesced event wakes an agent once; a comment, a reply, a review, a review request, a finished check run, and a `blocked` label going on or off each wake the agent that owns it. The audit log is where both are read from.
- **Nothing woken for nothing**: a bot with no work is not woken at all, and no event wakes a bot it does not concern — including a bot that is not the named reviewer of a pull request.
- **No card with nobody on it, and none handed out twice**: every card that arrives is assigned, by the service or by the operator, and two bots are never given one card. Read from the board, and from the audit line of the assignment.
- **One card in progress per bot**: a bot develops one card at a time, and the reviews it is asked for do not cost it the next card.
- **The poll removed, not shadowed**: the end state is reached *before* the dispatcher is live — every bot's `queue_poll` entry gone and its programs deleted with it — so there is no migration to run and no moment at which two sources could wake one bot. The cost is named rather than implied: during the gap, and during any later outage, the operator dispatches by hand and the recovery is fixing the dispatcher, not a return to the timer.
