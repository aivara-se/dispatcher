# 009 - Reading the Board

## Context

The routing decision leaves the process exactly once: the owning bot of a delivery that is silent about its owner, and the claimable card when a card is left with nobody on it ([005](005-routing-an-event-to-one-bot.md), systems document section 4). Until now that read was an interface the router was handed and nothing filled it, so every silent delivery resolved to no wake and the claim wake could not fire at all. Filling it means asking GitHub two questions: one card, which a delivery may name by pull request rather than by card, and the whole board, with the stage each card is in.

## Decision

**One read, over both of GitHub's APIs, with a token of its own.**

- **One card is read through the REST API** (`GET /repos/{owner}/{name}/issues/{number}`). It answers an issue and a pull request alike, with a `pull_request` block on the second. The GraphQL API answers the *other* kind of number with a `NOT_FOUND` error rather than a null, so a single query asking for both cannot be written without reading a refusal as a normal answer.
- **A pull request's card is the issue it closes**, from `closingIssuesReferences`, filtered to the same repository and taken as the lowest number when there are several, so the answer does not depend on the order GitHub gave. The delivery carries the pull request's number and never its card's, and resolving that gap is the read's business rather than the router's.
- **The whole board is read through the GraphQL API's project items**, a page at a time, with the board's Status field by name. The REST projects API is the previous generation of the same thing; the organisation's project items are only in GraphQL.
- **A read that fails is an error and never an empty answer.** A card that is not there answers nobody — it is not a wake for anyone and a redelivery of the same fact would be inconclusive again — but a fault is a fault: a status, a refusal inside a 200, a `data: null`, or a project the token cannot see. An empty board is a claim wake that never happens and a silent delivery that never wakes anyone, which is the failure the service exists to remove, so the request path answers `503` for a fault and GitHub delivers the fact again (sections 3 and 7).
- **The token is a reference in the routes file**, resolved at boot the way every route's secret is ([006](006-configuration-and-secrets.md)), and read-only for the board. Reading the project items needs `read:project`; a card's own assignees need no scope beyond the repository.

## Consequences

- The decision spends an API call only where the payload is silent, and the claim case spends one whole-board read: the rule [005](005-routing-an-event-to-one-bot.md) set, now with a reader behind it.
- The read is a seam: it is stubbed in every test above it and its own tests run it over a stubbed API, so nothing in the suite touches the network, and a change on GitHub's side is one package to change.
- The card a claim wake is about can sit in a repository the event did not come from. The envelope names it (section 5), so the route's burst-grouping key is the card rather than the delivery.

## Rejected

- **One GraphQL query asking for the issue and the pull request at once.** It fails for whichever kind the number is not, with an error rather than a null, and a read that treats a refusal as an answer will one day treat a real refusal as one.
- **Reading a card's assignee from its board item rather than from the issue.** The board's assignee column *is* the issue's assignees; the issue costs one call instead of a project-item lookup by content, and the board's own shape is what the claim rule's whole-board read is for.
- **Caching the board between events.** [005](005-routing-an-event-to-one-bot.md) settled it: the board is the truth, and a router with a memory of it is a second source of it.
- **A token with write access.** The service never writes to the board; a read-only token makes the accident impossible rather than merely unlikely.
- **Following a pull request's card into another repository** when it closes an issue elsewhere. The fleet's cards live in the repository being changed, and following a cross-repository link would make the answer depend on which link was written first. The restriction is deliberate, and the read says so rather than guessing.
