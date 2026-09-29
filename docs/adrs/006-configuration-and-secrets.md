# 006 - Configuration and Secrets

## Context

The service's behaviour changes without the code: which repositories and events it accepts, which bot a route wakes, which gateway route and secret it posts to. These change a handful of times a year, and some of them are credentials. A repository is cloned, reviewed and read by agents; the host's environment is not.

## Decision

- **A YAML routes file holds behaviour.** The listen address and port, the allowlist of repositories and events, the inbound endpoint's path, the log and dead-letter paths, the request timeout and retry bound, the dedup TTL and bound — and one entry per route: its name, the bot it wakes, the gateway route and profile it posts to, and the **name** of the secret it verifies with.
- **The routes file is committed.** It holds no secret value, so it belongs in the repository where a reviewer can see a routing change in a diff and a new repository or event is a reviewed line rather than a host edit made by hand.
- **Secret values are reached by reference only**: either an environment file the systemd unit loads, or a path to a file with mode `600` owned by the service user. The routes file names the variable or the path; it never holds the value.
- **No secret on the command line.** Flags are visible in a process list to every user on the host, so the value can arrive by file or environment and by nothing else.
- **Nothing secret is logged, at any verbosity** — not the value, not a prefix, not a hash of it. Payload bodies are not logged either, because a body is third-party text.
- **The routes file is validated at startup and the process refuses to run on an unknown bot, a duplicate route name or a secret reference that resolves to nothing.** A configuration fault must fail loudly at boot rather than silently at the first event.
- **Secrets are per route and per hop.** The secret GitHub signs the inbound POST with and the secret the dispatcher signs the wake with are different values: one leak must not be a credential on both sides.

## Consequences

- A routing change is a pull request, a reviewable diff and a deploy; a secret change is an edit to a mode-600 file that never touches the repository.
- A leaked routes file leaks topology — which bot owns what — and no access.
- Adding a bot is two changes in two places: a routes entry and a secret. That is the cost of keeping the two kinds of value apart, and it is the right way round: forgetting the secret makes the service refuse to start, and forgetting the routes entry means the bot is never named.
- Rotation is a host operation with a visible failure window (see the systems document): the gateway holds one secret per route and GitHub holds one per webhook, so the two must be changed together at a quiet moment.

## Rejected

- **Secrets in the routes file.** A credential in git history and in every clone, with a rewrite needed to remove it.
- **A secret passed as a flag.** Visible in `ps` to every user on the host, and in the shell's history.
- **One global secret for every route.** Smaller to configure, and a single leak becomes every route and every bot.
- **Configuration in the gateway's YAML.** The dispatch rules would live where the repository's own reviewers cannot see them, and host configuration has no diff, no review and no history worth the name. The gateway keeps what the gateway owns: the routes, their profiles and their secrets.
- **A database or a config service for a file this small.** A store to run and back up for something that changes a handful of times a year and that a person should be able to read in a page.
