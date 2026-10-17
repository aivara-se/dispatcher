# Agent Instructions

A Go service that receives GitHub's deliveries for the `aivara-se` organisation and wakes the one agent the fact concerns, with the reason — built for the operator of a small fleet of agents.

## Current Project Focus

The service is built and its gate is green on `main`. What is left is the live cutover ([`#74`](https://github.com/aivara-se/dispatcher/issues/74)) — the systemd unit, the Caddy path and the first real delivery — and a child that outlives the service ([`#91`](https://github.com/aivara-se/dispatcher/issues/91)): it sits in no run table, so the boot look starts a second run for its profile and one-run-per-agent cannot see the first. Both are open on the board.

This section is steering, not policy. It is the one place where what matters right now outranks the standing rules below, it changes often, and it is replaced rather than appended to. Keep it short enough to read in full, and current enough to be worth reading.

## House rules

- **Never** write a field other than the assignee, and **never** move a card: the board is the source of truth and the service writes one field of it, the issue's assignee.
- **Never** parse a request body before its signature verifies; **never** log a payload body or a secret value, at any verbosity — not the value, not a prefix, not a hash of it.
- **Never** start an agent the board's state does not call for, and **never** the operator; **never** start a second run for one agent, and **never** carry text a third party wrote into a run — the reason is identifiers and the service's own words ([`009`](docs/adrs/009-the-reason-carries-no-third-party-text.md)).
- **Never** keep durable state of the service's own. The board is the state; there is no database, no service-owned queue and no ordering of events.
- **Always** keep secret values out of this repository: the config names a secret and holds none, and a reference that resolves to nothing stops the process at boot rather than at the first event.
- **Always** record a significant technology choice as an ADR before the code that depends on it lands.
- The config is **JSON**, standard library only: `encoding/json`, no second format anywhere in the service, and no dependency until a real need is written down in the pull request that adds it.

## Tooling

- **Bun is the runtime for scripts.** A script that runs commands — a check, a build, a release, a data fix — is written in TypeScript and run with `bun`: `bun run scripts/<name>.ts`. **Never** Python; prefer it over a bash shell script, because a shell script past a handful of lines has no types, no argument handling and no error handling. A one-line command typed at the prompt is not a script.
- **Never** add a second package manager, a second lockfile, a second formatter or a second test runner. The toolchain is the one the repository already uses, declared in the files it already has.
- **Never** report "tests pass", "it builds" or "verified" without the command and the tree it ran against.

## Verify before pushing

```bash
gofmt -l .        # empty output is the pass
go vet ./...
go test ./...
```

Run the whole sequence, not just its fast part, and read every result — the exit code of the last command says nothing about the first.

These three are the gate `docs/SYSTEMS.md` (*Checks*) fixes, and CI (`.github/workflows/checks.yml`) runs them on every pull request and on `main`. What no machine can see, a reviewer covers: that every relative link in a document resolves to a file in the tree, that no unfilled slot survives, and that each ADR's Consequences says what its choice costs and not only what it buys.

## Version Control

- **Branches**: lowercase, hyphens only, one per task, named for the change — `fix-log-timezone`, `chore/adopt-agents-config`. No uppercase, no underscores, no personal prefixes.
- **Commits**: Conventional Commits, lowercase, single line, no scopes — `type: short description`.
- **Never** commit to `main` directly. **Never** force-push a branch another agent or person has seen.
- Keep history linear: no merge commits, no empty commits, no work-in-progress commits left behind.
- Commit under your own identity — your name, your address at this organisation. Never a generic agent, never another agent's identity.
- Remote work is always a branch plus a pull request. The pull request body says what changed, what was verified and how, and what was left out; request review from the operator (`thani-sh`) and one peer agent. Leave the working tree clean: no scratch files, no editor backups, no `.env` you created.

## Repository Structure

- `docs/PRODUCT.md`: what the service is for, who it is for, and how it is judged
- `docs/SYSTEMS.md`: the architecture — where it runs, the request path, and the interfaces
- `docs/adrs/`: one decision per file; merged into `main` means Accepted
- `AGENTS.md`: the entry file an agent reads first
- `README.md` (how it builds and runs, and where the documents are), `go.mod`, `.gitignore`
- `cmd/dispatcher/` (the binary), `internal/{receiver,board,router,runner}/` (the four parts), `internal/{config,audit}/` (what it loads and what it writes), and `.github/workflows/checks.yml` (CI)

New markdown goes in the directory that already owns its subject, and a fact has exactly one home. Never add a second copy of something a document already says; link to it. If a path in the map above stops being true, fix the map in the same pull request. A map that lies is worse than no map.
