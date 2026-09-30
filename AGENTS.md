# Agent Instructions

A Go service that receives GitHub webhooks and wakes the right `aivara-se` agent with the reason the event matters, for the operator of the fleet.

Design documents, the agent configuration, and the Go service they describe. `docs/PRODUCT.md` is what the service is for and how it is judged, `docs/SYSTEMS.md` is the architecture and its interfaces, and `docs/adrs/` holds the decisions and what each rejected.

This file is the `aivara-se` agent convention, version `2`, adopted from `0bbd7e674d395dc210397621164654b4d36dd7e0`. Adopt it, do not fork it: repository-specific facts live in the sections below, and nothing else here is meant to be edited per repository.

## Current Project Focus

Settle the open questions in `docs/SYSTEMS.md` section 12 with the operator — above all which bot an event on an unassigned card should wake — and then finish the service: the router is the last stub. Do not guess the routing table while the question that decides its shape is open.

This section is steering, not policy. It is the one place where what matters right now outranks the standing rules below, it changes often, and it is replaced rather than appended to. Keep it short enough to read in full, and current enough to be worth reading.

## House rules

- **Never** wake more than one bot for one event, and **never** write to the board: the service wakes agents, the card says what to do.
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

CI runs those three commands on every pull request and on `main` (`.github/workflows/checks.yml`). The gate covers the packages, the loader's refusals, the shape of the audit and dead-letter lines, the wake's outbound half against a stub gateway, and signed POSTs driven through the receiver with the router and the poster stubbed; it does not cover a delivery through the real router, which is the last stub the check arrives with. Then the four things no script sees: every relative link in a document resolves to a file in the tree, no convention slot is left unfilled, `docs/SYSTEMS.md` section 12 still names the questions the cards are blocked on, and each ADR's Consequences says what its choice costs, not only what it buys.

## Version Control

- **Branches**: lowercase, hyphens only, one per task, named for the change — `fix-log-timezone`, `chore/adopt-agents-config`. No uppercase, no underscores, no personal prefixes.
- **Commits**: Conventional Commits, lowercase, single line, no scopes — `type: short description`.
- **Never** commit to `main` directly. **Never** force-push a branch another agent or person has seen.
- Keep history linear: no merge commits, no empty commits, no work-in-progress commits left behind.
- Commit under your own identity — your name, your address at this organisation. Never a generic bot, never another agent's identity.
- Remote work is always a branch plus a pull request. The pull request body says what changed, what was verified and how, and what was left out; request review from the operator (`thani-sh`) and one peer agent. Leave the working tree clean: no scratch files, no editor backups, no `.env` you created.

## Repository Structure

- `cmd/` and `internal/`: the service — `config`, `audit`, `receiver` and `wake` whole, `router` the last stub
- `config/routes.example.yaml`: the routes file template; no secret value lives in it
- `.github/workflows/checks.yml`: the gate
- `README.md`: what the service is, how it builds and runs, and where the documents are
- `docs/PRODUCT.md`: what the service is for, who it is for, and how it is judged
- `docs/SYSTEMS.md`: the architecture, the interfaces, and what is settled and what is open
- `docs/adrs/`: the architecture decision records, `000` being the convention the rest follow
- `AGENTS.md`: this file
- `.agents/skills/`: the convention's skills

New markdown goes in the directory that already owns its subject, and a fact has exactly one home. Never add a second copy of something a document already says; link to it. If a path in the map above stops being true, fix the map in the same pull request. A map that lies is worse than no map.

## Agent Skills

`.agents/skills/` holds one skill per kind of work — the procedure to follow, not a second copy of these instructions. A skill stands on its own: it names no file of this convention and points at no other skill, so a reader who has it has everything it needs. This file is what points at the skills; they never point back. Each skill declares in its front matter what it covers and its `when-to-use`: the situation in which you must open it. Read the skill that covers the work before you start it.

- .agents/skills/coding/SKILL.md
- .agents/skills/testing/SKILL.md
- .agents/skills/writing/SKILL.md
- .agents/skills/review/SKILL.md

Every skill on disk is listed above, and every skill listed above exists. A new skill is added here in the same pull request that adds it, and a skill deleted from disk is deleted from this list in the same commit. An index that has drifted is worse than a short one.

Front matter is exactly three keys: `name`, equal to the directory; `description`, one sentence; `when-to-use`, the trigger in the reader's words. A skill stays under about 120 lines, covers one concern, and names every file it ships. Skills are flat until this repository has more than eight of them or two clearly unrelated groups, then they are grouped under `.agents/skills/<group>/<skill>/` and this index is updated with them.

A skill that is true only of this repository stays here. A skill that would be true of every repository belongs in the `aivara-se` convention instead, in a pull request of its own.
