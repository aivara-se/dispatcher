# 005 - Routing an Event to One Bot

## Context

Four bots share one board, and every event must wake exactly one of them or none. The rule the poll already uses is: the card's assignee is who acts on it, its board stage says whether they do, and a card that is in Todo and unassigned is claimable by whichever bot is holding nothing. The payload carries the assignee for most events and nothing at all for some — a review request, a finished check run on a pull request whose card lives elsewhere.

## Decision

One router, in one place, as a pure function of data where it can be.

- **Inputs**: the event and its action, the repository, and the payload's actor, assignee and card number.
- **The login convention**: `thani-sh-<bot>` maps to profile `<bot>`. The bot list — profile and login — is configuration, the same pair of values the queue monitors read, so a new bot is a config entry and not a code change.
- **Where the payload is silent**, the router reads the card's board item and uses its assignee and stage — the same two facts the poll uses. Where the routing depends on that read and the read fails, the event is dead-lettered with the reason; it is never guessed and never fanned out.
- **An event with no resolvable owner is logged and dropped.** This service never wakes more than one bot for one event, and never all four.
- **Nothing is cached across events.** A board fact read for one event is not reused for the next; the board is the truth and a router with a memory of it is a second source of it.
- **The service never writes to the board.** It reads the assignee; it does not set it.

## Consequences

- The rule is one file, testable without HTTP, a gateway or `gh`: given a payload and a board item, it returns a bot or nothing. Every routing mistake is reproducible in a unit test.
- A wrong wake has the same shape as a wrong claim in the poll, because both read the same two facts; the two mechanisms cannot disagree about who owns a card.
- The board read is the router's only dependency on the GitHub API, and it is the slow, fallible part of an otherwise local decision. It runs only where the payload is silent, and each new use of it must earn its call.
- A bot is woken because it is the card's assignee, not because its name appeared in text. Mentioning another agent in a comment wakes nobody.

## Rejected

- **Routing on the event's actor alone.** The actor is whoever clicked, not whoever acts: a reviewer commenting on a card they are reviewing would wake themselves and not its owner.
- **Waking every bot whose name appears in the payload.** Comment text and titles are prose written by people; a name in a sentence is not an assignment, and four agents would pay for one fact.
- **A routing table in the gateway's route configuration.** It would put the fleet's own rules in host configuration, where a pull request cannot review them and a repository's map cannot describe them.
- **Letting each bot decide whether an event is theirs.** A fan-out wake: the event is delivered to all four and three of them read the board to find out it was not theirs. The economy of an event-driven dispatcher is that it decides once.
- **Routing on the board's stage alone.** Stage says whether a card is being worked; the assignee says by whom. A card in In Review is someone's to review, and only the payload or the assignee says whose.
