# dispatcher: ruleset

Which GitHub event carries a fact, who the service wakes for it, and what the woken agent does with it. The architecture is in [SYSTEMS.md](SYSTEMS.md), the product is in [PRODUCT.md](PRODUCT.md), and the decision that the service assigns the work itself is [ADR 010](adrs/010-the-dispatcher-assigns-work.md).

Every rule below is read against one precedence, highest first:

1. **The operator.** An assignment, a comment or a review request the operator made is obeyed as it stands. The service never overrides it, and never reassigns a card the operator has touched.
2. **The `blocked` label.** On a card or a pull request it stops every wake beneath it, whatever the assignee says. Removing it is what restarts the work.
3. **The assignee.** Otherwise the card's assignee is the bot that acts. For a pull request the card is the issue the pull request closes, so the assignee is the author.

## The board

- The columns are `Todo`, `In Progress` and `Done`. There is no `In Review` column and no `Ready to Ship`: review duty belongs to a pull request's reviewer, not to a card's stage, and a card is never moved in order to be reviewed.
- A card is work, and its assignee owns it from `Todo` to `Done`.
- Events: `projects_v2_item` — a card arrived, moved or was assigned. The payload is a hint; the service re-reads the card.

## One task at a time

- A **session** is a run the service started for a card. A bot has at most one open session.
- A review the bot is asked for is not work: it takes no session, never blocks the next card, and is finished on its own.
- The service starts no second card for a bot that has an open session or a card in `In Progress`. The card waits on the service's in-memory queue, in the order it arrived, and is started when that session closes.
- A session closes when its card leaves `In Progress` — moved to `Done`, closed, or handed to another bot.
- Events: `projects_v2_item`, `issues.closed`, `pull_request.closed`.

## Assigning a card

- No bot claims a card, and no bot assigns one to itself. A card with nobody on it is assigned by the service: the first bot in the routes file's order with no open session and no card in `In Progress` takes it.
- The service writes the assignee and wakes that bot in the same step, so the assignment and the wake cannot disagree.
- One card, one bot, by construction: the pick is a function of the board and the fleet's order, so two bots cannot race for the same card, and a card cannot sit unassigned while the operator is away.
- A card the operator assigned is left exactly as it is, including one assigned to the operator — that card wakes no bot at all.
- Events: `issues.opened`, `issues.assigned`, `issues.unassigned`, `issues.reopened`, `projects_v2_item`.

## A card is assigned

- The service wakes the assignee with the card and its stage. The bot reads the card and starts it.
- Events: `issues.assigned`, `projects_v2_item` with the assignee field as the change.

## A card is blocked

- `blocked` wakes nobody. The card keeps its assignee and its column and simply waits, including when it is blocked by another card that has not landed.
- Removing the label wakes the holder again, at whatever stage the card is in. It is the only exit from a block, and it is a real wake: the card is never left to the stalled-work probe.
- No other label is a wake.
- Events: `issues.labeled`, `issues.unlabeled`, `pull_request.labeled`, `pull_request.unlabeled`.

## A comment lands

- A comment on a card or on a pull request wakes the card's assignee — the author, for a pull request — which reads it and answers it.
- Events: `issue_comment.created`, `pull_request_review_comment.created`.

## A review is requested

- The service wakes the reviewer the delivery names, not the author and not the card's assignee.
- An agent never reviews its own work. The author requests a review from a peer bot; the operator is added as a reviewer only when the operator asks to be, never on every pull request.
- A re-review is the same event and the same rule: GitHub re-requests the same reviewer once the author has answered a change request, and the reviewer reads what changed since its last review.
- Events: `pull_request.review_requested`.

## A review is submitted

- A review wakes the author, because the author is the card's assignee.
- `changes_requested` — the author applies the change and re-requests the review from the same reviewer.
- `approved` — the author finishes the card: it moves to `Done`, and the pull request merges.
- A rejected change request is the operator's call, not another agent's. An agent does not judge whether a peer's refusal is valid; it escalates with the reason and acts on the operator's answer.
- Events: `pull_request_review.submitted`, `pull_request_review_comment.created`.

## A pull request is ready or merged

- Ready for review wakes the requested reviewer, the way a request does.
- A merge wakes the author to finish the card. The card's stage, not the pull request, says what is left.
- Events: `pull_request.ready_for_review`, `pull_request.closed` (merged).

## A gate finishes

- A finished check run or workflow wakes the card's holder to read the result. A red gate is the author's problem, not the reviewer's.
- Events: `check_run.completed`, `workflow_run.completed`.

## Not a wake

- A label that is not `blocked`, a pull request pushed to, a review dismissed, a review request taken back, an edit to a card's title or body, and anything in a repository that is not on the allowlist. Each is accepted, logged, and woken for nobody.
- Events: `pull_request.synchronize`, `pull_request_review.dismissed`, `pull_request.review_request_removed`, `issues.edited`, `projects_v2_item` for a field no rule acts on.
