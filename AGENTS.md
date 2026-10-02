# Agent Instructions

A Go service that receives GitHub webhooks, assigns the cards nobody holds, and wakes the right `aivara-se` agent with the reason the event matters, for the operator of the fleet.

Design documents, the agent configuration, and the Go service they describe. `docs/PRODUCT.md` is what the service is for and how it is judged, `docs/SYSTEMS.md` is the architecture and its interfaces, `docs/RULESET.md` is which event wakes whom and under what precedence, and `docs/adrs/` holds the decisions and what each rejected.

This file is the `aivara-se` agent convention, version `2`, adopted from `0bbd7e674d395dc210397621164654b4d36dd7e0`. Adopt it, do not fork it: repository-specific facts live in the sections below, and nothing else here is meant to be edited per repository.

## Current Project Focus

Build version 2 as `docs/adrs/010-the-dispatcher-assigns-work.md` decides it, against the rules in `docs/RULESET.md`: the service assigns the cards nobody holds itself, keeps one session per bot with an in-memory queue behind it, listens to the board's own event and to label changes, and stops reading a board column that no longer exists. Nothing in `docs/SYSTEMS.md` section 12 is open; what it records is what the code has to implement.

This section is steering, not policy. It is the one place where what matters right now outranks the standing rules below, it changes often, and it is replaced rather than appended to. Keep it short enough to read in full, and current enough to be worth reading.

## House rules

- **Never** wake more than one bot for one event, and **never** move a card: the service assigns a card nobody holds, wakes the agent that holds it, and the card says what to do.
- **Never** parse a request body before its signature verifies.
- **Always** keep secret values out of this repository: the routes file names a secret and never holds it.
- **Never** log a payload body or a secret value, at any verbosity.
- **Always** record a significant technology choice as an ADR before the code that depends on it lands.
- **Never** let a bot be woken by the dispatcher and the poll at the same time: the switch is one step, per bot.

## Tooling

- **Bun is the runtime for scripts.** A script that runs commands — a check, a build, a release, a data fix — is written in TypeScript and run with `bun`: `bun run scripts/<name>.ts`. **Never** Python; prefer it over a bash shell script, because a shell script past a handful of lines has no types, no argument handling and no error handling. A one-line command typed at the prompt is not a script.
- **Never** add a second package manager, a second lockfile, a second formatter or a second test runner. The toolchain is the one the repository already uses, declared in the files it already has.
- **Never** report "tests pass", "it builds" or "verified" without the command and the tree it ran against.

## Verify before pushing

```bash
gofmt -l .        # prints nothing
go vet ./...
go test ./...
```

Run the whole sequence, not just its fast part, and read every result — the exit code of the last command says nothing about the first.

CI runs those three commands on every pull request and on `main` (`.github/workflows/checks.yml`). The gate covers the packages, the loader's refusals, the shape of the audit and dead-letter lines, the wake's outbound half against a stub gateway, and signed POSTs driven through the receiver and the real router; it does not cover a delivery against the live board, which is what a stubbed board stands in for. Then the four things no script sees: every relative link in a document resolves to a file in the tree, no convention slot is left unfilled, `docs/SYSTEMS.md` section 12 still records what is settled and leaves no architecture question open, and each ADR's Consequences says what its choice costs, not only what it buys.

## Version Control

- **Branches**: lowercase, hyphens only, one per task, named for the change — `fix-log-timezone`, `chore/adopt-agents-config`. No uppercase, no underscores, no personal prefixes.
- **Commits**: Conventional Commits, lowercase, single line, no scopes — `type: short description`.
- **Never** commit to `main` directly. **Never** force-push a branch another agent or person has seen.
- Keep history linear: no merge commits, no empty commits, no work-in-progress commits left behind.
- Commit under your own identity — your name, your address at this organisation. Never a generic bot, never another agent's identity.
- Remote work is always a branch plus a pull request. The pull request body says what changed, what was verified and how, and what was left out; request review from one peer agent, and never from the author of the branch — an agent never reviews its own work. The operator is added as a reviewer only when the operator asks to be. Leave the working tree clean: no scratch files, no editor backups, no `.env` you created.

## Repository Structure

- `cmd/` and `internal/`: the service — `config`, `audit`, `receiver`, `router`, `board` and `wake`
- `config/routes.example.yaml`: the routes file template; no secret value lives in it
- `.github/workflows/checks.yml`: the gate
- `README.md`: what the service is, how it builds and runs, and where the documents are
- `docs/PRODUCT.md`: what the service is for, who it is for, and how it is judged
- `docs/SYSTEMS.md`: the architecture, the interfaces, and what is settled and what is open
- `docs/RULESET.md`: which event wakes whom, and the precedence the rules are read under
- `docs/adrs/`: the architecture decision records, `000` being the convention the rest follow
- `AGENTS.md`: this file

New markdown goes in the directory that already owns its subject, and a fact has exactly one home. Never add a second copy of something a document already says; link to it. If a path in the map above stops being true, fix the map in the same pull request. A map that lies is worse than no map.

