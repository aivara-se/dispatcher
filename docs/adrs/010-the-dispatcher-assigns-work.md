# 010 - The Dispatcher Assigns Work

## Context

Version 1 wakes an agent for a fact and leaves the assignment to the board: a card nobody holds is a *claim*, woken for the first bot that holds nothing, and every bot reads the board to find out what it holds. What is missing is the decision itself. Nothing but habit stops two bots from being handed the same card, nothing enforces one development task per bot, review duty sits in a column no rule owns, and a `blocked` label that is lifted wakes nobody at all. The wake path is right; the assignment has to move into the service.

## Decision

Version 2 of the product: the service decides who holds a card, and a bot is a worker with one open session rather than a reader of the board.

- The service **writes the board**, one field: the assignee of a card nobody holds. The pick is deterministic — the first bot in the routes file's order with no open session and no card in `In Progress`.
- The board token therefore gains write access to the assignee and to nothing else. The service never moves a card, never closes one and never writes a comment.
- A **session** is a run the service started, one per bot, opened when the wake is accepted and closed when its card leaves `In Progress`. A review is not a session. A card for a bot that is already in one waits on an in-memory queue.
- The board is **three columns**: `Todo`, `In Progress`, `Done`. `In Review` and `Ready to Ship` are removed — review duty is the pull request's reviewer, and a card is finished when its pull request merges.
- The service listens to **label changes** — `blocked` going on or off, on a card or a pull request — and to comments. A lifted block is the wake that resumes a blocked card.
- The rules above the mechanism are the fleet's and live in [RULESET.md](../RULESET.md): operator, then `blocked`, then the assignee.
- The operator is **not** added as a reviewer on every pull request. The author requests a peer bot, and the operator only when the operator asks to be.

## Consequences

- The assignment, the wake and the session are one decision, made once, in code, from facts read at that moment: two bots cannot be handed one card, and a card cannot wait on a bot that is already working.
- A card's stage and the sessions the service holds can disagree, and the board wins: a session closes when its card leaves `In Progress`. A card its owner never moves holds that bot's session — visible in the queue and to the operator's stalled-work probe — and no second card is started for it.
- The queue is in memory, so a restart loses the wakes waiting in it ([ADR 007](007-persistence.md) still stands). The cards are not lost: they stay assigned and unstarted, so the cost is latency, and the probe is what sees them.
- The service is now a writer of the board, so its token is a credential with a mutation on the other side of it. It writes exactly one field, and every other write stays a human's.
- Version 1's wake path — one event, one bot, one reason — is unchanged. The routing table, the ruleset and the board's columns are what move.

## Rejected

- **Letting each bot claim the next card.** It is the race the board cannot arbitrate: two bots read the same free card and the faster one wins, not the free one.
- **Assigning by workload.** Load has no definition the board carries. A bot is free or it is not, and that is the whole difference between one running card and two.
- **Keeping `In Review` and waking the card's holder to review.** It asks an author to review their own work, or needs a second assignment a card cannot express. The pull request's reviewer is the fact, and GitHub already carries it.
- **Marking the session in the board instead of in the service.** It would make the board a second bookkeeper of what is running, which is what the queue is for.
- **A persistent queue.** Storage is still not worth its cost at this size; an in-memory queue over a board that never loses a card is enough.
