# dispatcher: product

GitHub posts to `dispatcher` when something happens in the `aivara-se` organisation. It verifies the delivery, decides which agent the fact concerns, assigns the card to that agent, and starts that agent's run with the reason attached. The operator of a small fleet — one person, four bots, one board — installs it, trusts it, and reads its log when a wake is missed; the bots are the ones woken. The service does not do the work.

A wake is a **reason, not an instruction**: the service says *why now*, and the agent re-reads the board and the pull request and decides what to do. A wake may therefore be early, late, duplicated or out of order and the outcome is the same. The mechanism behind it is the subject of the decisions in `docs/adrs/`.

## The board

One project, four columns. The operator's only act is to add a card to `Backlog`.

- `Backlog` — the operator's inbox, and MaMa's. Every card added here is assigned to her.
- `Refined` — ready to build. A card here is assigned to a developer.
- `Started` — being built. A developer holds one card at a time, and it stays here until its pull request merges.
- `Finished` — merged, and in most cases deployed.

A card is a GitHub issue. The board is the source of truth: the service reads it, and writes one field, the assignee.

## The agents

- **MaMa** refines. She checks the card is feasible, fills in what is missing, splits it into sub-cards when it is too big, links the cards that block it, and moves it to `Refined` when it is ready. A blocked card stays in `Backlog`.
- **MeMe and MiMi** build, one card at a time, to a merged pull request.
- **Every agent reviews.** A review is not a card and never takes a developer's one slot. An agent reviews a peer's pull request and never pushes to it: the author applies every change.
- **MoMo** is not in this flow. Each agent tests its own change, and the two developers review each other's; a separate quality step is later work.

## What the dispatcher does

- **A card is added to `Backlog`** — the service assigns it to MaMa and starts her run.
- **A card is moved to `Refined`** — the service assigns it to a developer that holds no card and is not already working, and starts that developer's run. A card with no developer free waits in `Refined` until one is.
- **A card is moved to `Finished`** — the service starts MaMa's run, to see which blocked cards it has unblocked.
- **A review is requested** — the service starts the named reviewer's run.
- **A comment, a review or a label change lands on a pull request** — the service starts every agent on that pull request, its author and its reviewers, except the agent that made it.
- **A pull request merges** — the service starts its author's run, to move the card to `Finished`.
- **A pull request closes unmerged** — the service starts its author's run, to reopen or rework it.
- **A comment lands on a card** — the service starts the card's assignee, except the agent that made it.
- **Anything else** is accepted, logged, and woken for nobody. It is never refused.

## What it never does

- Never starts an agent an event does not concern, and never the operator.
- Never starts an agent twice for one fact.
- Never starts a second card for a developer that already holds one.
- Never writes a field other than the assignee, and never moves a card.
- Never parses a body before the signature verifies, and never logs a payload body or a secret value.
- Never orders events, and never depends on their order.

## The edge cases, and what handles each

- **A redelivered delivery.** The delivery id is remembered for an hour; the repeat wakes nobody.
- **One fact, two deliveries.** A card added with a status is two events and a burst of comments is many. A second wake is a reason to re-read and changes nothing, and a burst on one card coalesces into one wake.
- **Every developer is busy.** A card in `Refined` is not lost: it is assigned when one frees.
- **Two cards refined at once.** Each is assigned to a different developer; no card is assigned twice.
- **A restart.** The board holds the state the service needs, so a restart loses no work, and the cost is at most one duplicate wake.
- **A card moved into `Started` by hand.** It is not in `Refined`, so it is offered to nobody; the board is obeyed.
- **The agent acted.** No agent is started for its own action.
- **A wrong or missing signature.** Refused before anything is parsed, so a sender configured with the wrong secret fails loudly in GitHub's delivery list.
- **A body over the limit.** Refused at the same size the gateway refuses, so a delivery passes both hops or neither.
- **An event that is not ours.** Accepted and ignored, never a `4xx`, so the delivery list still shows the real failures.
- **A run that cannot be started, or a board read that fails.** Retried, then dead-lettered and the audit line says so; GitHub redelivers.

## What v1 does not do

- Not a scheduler. Nothing fires on a clock; the hourly stalled-work probe stays where it is.
- Not a dependency resolver. MaMa reads the links between cards and works out what a `Finished` card unblocked; the service models no blockers.
- Not a board writer beyond the assignee, and not a second source of truth.
- Not a deployment step. A merge ends the card; what a merge deploys is the repository's own pipeline.

## Acceptance

v1 is done when each of these holds, and each is checkable by the operator.

- One card added to `Backlog`, and nothing else touched, reaches a merged pull request: MaMa refines it, a developer builds it, a peer reviews it, and the service carries every wake on the way.
- A card added to `Backlog` is assigned to MaMa and her run starts within seconds of the event (provisional; the first live delivery freezes the number).
- A card moved to `Refined` is assigned to one developer that holds no card, and no card is assigned to two.
- A card moved to `Finished` starts MaMa's run and no other.
- A review request starts the named reviewer alone, and a merge starts the author alone.
- A redelivered delivery wakes nobody, and the audit log shows it was seen.
- A run the service could not start is dead-lettered rather than dropped.
- The audit log answers, for any delivery, whether an agent was started and, when not, why.
