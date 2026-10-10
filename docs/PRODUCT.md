# dispatcher: product

GitHub posts to `dispatcher` when something happens in the `aivara-se` organisation. It verifies the delivery, decides which agents the fact concerns, and wakes them through the Hermes gateway with the reason attached. It exists so a bot learns that a card arrived, a comment landed, a review was asked for or a pull request merged from the event itself, instead of from a ten-minute poll or the operator's memory. It wakes agents; it does not do their work, and it never writes to the board. The mechanism — the signature, the delivery id, the wake and its retries — is the subject of the decisions in `docs/adrs/`; this document is what the service is for and how it is judged.

## The problem

The fleet's work lives on one board, and today each bot learns about it from a timer: a program per bot on a ten-minute tick, printing that bot's cards. That costs time, and it is blind.

- **The delay.** The fact and the wake are up to ten minutes apart, so a reviewer who asks for a change waits for the author's next tick.
- **The blindness.** A tick sees a card's column and its comment count. It cannot see a review submitted, a check run finished, or a review requested — exactly the moments an agent needs to know about — and nothing else reports them.
- **The operator as the dispatcher.** With the poll removed, someone still has to notice a change and tell the bot it concerns. That someone is the operator, and this service exists to give the job back to the machine.

## Who it is for

The operator of a small fleet: one person, four bots, one board. The operator installs the service, trusts it, and reads its log when a wake is missed. The bots are the ones woken; they are the users of the wake path, and nothing else in their profiles changes.

## The board

One project, four columns. The operator's only act is to add a card to `Backlog`.

- `Backlog` — the operator's inbox. Every card here is MaMa's.
- `Refined` — ready to build. A card waits here for a developer.
- `Started` — being built. A developer holds one card here, and stays on it until the pull request merges.
- `Finished` — merged, and in most cases deployed.

A card is a GitHub issue. The board is the source of truth, not the service: the service reads it and never writes to it.

## The agents

- **MaMa** refines. She checks the card is feasible, fills in what is missing, splits it into sub-cards when it is too big, links the cards that block it, and moves it to `Refined` when it is ready. A card that is blocked stays in `Backlog`.
- **MeMe and MiMi** build, one card at a time. A developer takes the card it is offered from `Refined`, assigns itself, moves it to `Started`, and works it to a merged pull request.
- **Every agent reviews.** A review is not a card and never takes a developer's one slot. An agent reviews a peer's pull request and never pushes to it: the author applies every change.
- **MoMo** is not in this flow. Each agent tests its own change before it opens a pull request, and the two developers review each other's; a separate quality step is later work.

## A wake is a reason, not an instruction

That distinction is the product. The service says *why now* — this card arrived, this review landed, this pull request merged — and the agent re-reads the board and the pull request and decides what to do with it. So a wake may be early, late, duplicated or out of order and the outcome is the same, because the agent looks at the current truth rather than at what the event said. The service never tells an agent what to do; the agent's own manual is not written here.

The other half of the pair is what it ignores. A fact that is valid but concerns nobody — a label that is not one of the four columns, a pull request pushed to, a repository that is not this organisation's — is accepted and logged, and wakes no one. It is never refused, because a refused delivery shows up in GitHub as a failure and drowns the real ones.

## The wakes

Each rule names a fact and the agent or agents it wakes.

- **A card arrives in `Backlog`** — added to the board without a status, or moved into `Backlog` — wakes MaMa.
- **A card arrives in `Refined`** wakes one free developer, and the wake names the card.
- **A card arrives in `Finished`** wakes MaMa, to see which blocked cards it has unblocked.
- **A developer becomes free** — the card in its `Started` left it — wakes a free developer about the first card waiting in `Refined`.
- **A review is requested** wakes the reviewer the request names, when it is one of the fleet.
- **A comment, a review or a label change lands on a pull request** wakes every agent on that pull request — its author and its reviewers — except the agent that made it.
- **A pull request merges** wakes its author, to move its card to `Finished`.
- **A pull request closes unmerged** wakes its author, to reopen or rework it.
- **A comment lands on a card** wakes the card's assignee, except the agent that made it.
- **The operator assigns a card to an agent** wakes that agent. This is the manual override, and it is obeyed as it stands.
- **Anything else** is accepted, logged, and woken for nobody.

A fact that concerns two agents wakes each of them once: the author's comment on a pull request wakes the reviewers, and the reviewer's review wakes the author, and neither wakes the agent that acted.

### One developer, one card

A developer is free when it holds no card in `Started`. The service offers a card to one developer, offers a developer one card at a time, and never offers the same card to two developers — so two developers cannot claim one card and one developer cannot be handed two. A card whose developers are all busy waits in `Refined`, and the rule that a developer became free is what picks it up, so waiting costs nothing. The offer is a reason: the developer still claims the card itself, by writing its own assignee and moving the card to `Started`, and the board's assignee is the claim.

## The wake as the agent receives it

The service posts one signed JSON envelope to the target agent's own route on the Hermes gateway. It carries the fact — the event and its action, the repository, and the card with its column, or the pull request — and one `reason` sentence composed from it: what happened, where the card sits, and why this agent. The agent's chat, profile, memory and skills are unchanged; the route belongs to the agent, so which profile runs is a property of the address rather than of this service.

## What it never does

- Never writes to GitHub. It reads the board and a pull request, and writes only its own audit log.
- Never parses a body before the signature verifies.
- Never logs a payload body or a secret value.
- Never wakes an agent an event does not concern, and never the operator.
- Never wakes an agent twice for one fact.
- Never orders events, and never depends on their order.
- Never speaks to an agent directly; the gateway is the only path.

## The edge cases, and what handles each

- **A redelivered delivery.** GitHub's delivery id is remembered for an hour; the repeat wakes nobody.
- **One fact, two deliveries.** A card added to the board with a status is two events, and a burst of comments is many. The second wake is a reason to re-read and changes nothing, and a burst on one card coalesces into one wake.
- **A restart.** The board holds everything the service needs, so a restart loses no work; the delivery-id cache is the only thing forgotten, and the cost is at most one duplicate wake.
- **Every developer is busy.** A card in `Refined` is not lost: the wake that frees a developer carries it.
- **A card moved into `Started` by hand.** It is not in `Refined`, so it is offered to nobody; the board is obeyed.
- **A card in `Started` with no assignee.** It belongs to no developer, so it frees nobody. The rule that a developer assigns itself before it moves the card is what keeps this shape from occurring.
- **A pull request with no card.** The review rules need only the pull request, so its agents are still woken; the merge wake needs the card, and the author holds it.
- **The agent acted, not a peer.** No agent is woken for its own action.
- **A wrong or missing signature.** Refused before anything is parsed, so a sender configured with the wrong secret fails loudly in GitHub's delivery list instead of silently doing nothing.
- **A body over the limit.** Refused at the same size the gateway refuses, so a delivery passes both hops or neither.
- **An event that is not ours.** Accepted and ignored, never a `4xx`.
- **The gateway is unreachable or answers `5xx`.** A short bounded retry, then the delivery is dead-lettered and the audit line says so; GitHub redelivers.
- **The board read fails.** An error, not a "nobody to wake": the delivery is refused so GitHub sends it again.
- **A wrong route or secret on the gateway.** A `4xx`, recorded and never retried — a configuration fault for a human, not for another attempt.

## What v1 does not do

- Not a scheduler. Nothing fires on a clock; the hourly stalled-work probe stays where it is.
- Not a dependency resolver. MaMa reads the links between cards and works out what a `Finished` card unblocked; the service does not model blockers.
- Not a board writer, and not a second source of truth beyond the read.
- Not a chat product and not a second inbox. It speaks to the gateway, never to a bot.
- Not a quality gate. MoMo and a separate testing step are later work.
- Not a deployment step. A merge ends the card; what a merge deploys is the repository's own pipeline.

## Acceptance

v1 is done when each of these holds, and each is checkable by the operator.

- One card added to `Backlog`, and nothing else touched, reaches a merged pull request: the fleet refines it, a developer builds it, a peer reviews it, and the service carries every wake on the way.
- A card added to `Backlog` wakes MaMa within seconds of the event (provisional; the first live delivery freezes the number).
- A card moved to `Refined` wakes exactly one free developer. With two developers free and two cards waiting, both are woken, one card each.
- A card moved to `Finished` wakes MaMa and nobody else.
- A review request wakes the named reviewer alone, and a merge wakes the author alone.
- A redelivered delivery wakes nobody, and the audit log shows it was seen.
- A delivery the gateway cannot take is dead-lettered rather than dropped.
- The audit log answers, for any delivery, whether it was woken and, when not, why.

## The name

`dispatcher` has to keep meaning *it decides who hears about a fact, and nothing else*. The day it starts writing to the board or deciding the work, the name is wrong.
