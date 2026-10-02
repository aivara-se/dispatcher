# dispatcher

A Go HTTP service that GitHub calls: it verifies the payload, decides which single agent in the `aivara-se` fleet the event belongs to, assigns a card that has nobody on it, and wakes that agent with the reason attached. It exists so an agent is woken by a fact — a card assigned, a comment, a review, a check run — instead of by a ten-minute timer, and so a card never waits on somebody noticing it.

Status: version 1 is whole on `main`, and nothing is deployed. The module, the configuration it loads, the board read, the audit and dead-letter writers and the package boundaries are on `main`. `internal/receiver` verifies a signed delivery and deduplicates it; `internal/router` resolves it to exactly one bot — the delivery's own assignee where it carries one, the card's board item where it is silent, and the claim wake when a card is left with nobody on it — over `internal/board`, which reads one card through the REST API and the whole board through the GraphQL API with a token of its own; and `internal/wake` posts the signed envelope to that bot's own gateway route. Putting it on the host — the unit, the public path, the gateway's routes, and the cutover that removes the poll — is the last card's business, and until it runs nothing is woken by this service.

[docs/PRODUCT.md](docs/PRODUCT.md), [docs/SYSTEMS.md](docs/SYSTEMS.md) and [docs/RULESET.md](docs/RULESET.md) describe **version 2**, which the code above does not implement yet: the service assigns the cards nobody holds instead of waking a claim, keeps one session per bot with an in-memory queue behind it, listens to the board's own event and to label changes, and the board loses its `In Review` and `Ready to Ship` columns. [ADR 010](docs/adrs/010-the-dispatcher-assigns-work.md) is that decision, and implementing it is the next card.

## Requirements

Go 1.26 or newer. The binary is the standard library plus one module, `gopkg.in/yaml.v3`, for the routes file: `go build` fetches it from the module proxy, and what comes out is one binary with no runtime to install. [ADR 001](docs/adrs/001-go-and-the-standard-library.md) sets the bar a dependency has to clear and [ADR 006](docs/adrs/006-configuration-and-secrets.md) is why the routes file is YAML.

## Build and run

The service is one binary that takes a routes file and listens on a loopback port:

```sh
go build -o bin/dispatcher ./cmd/dispatcher
bin/dispatcher --config config/routes.yaml
```

Copy `config/routes.example.yaml` to get started: it names every field, one route per bot, and two secret *references* per route — an environment variable, or a path to a file with mode 600. No secret value lives in the file, and a reference that resolves to nothing stops the process at boot rather than at the first event ([docs/SYSTEMS.md](docs/SYSTEMS.md) section 8).

GitHub posts to it on one side, and it posts a wake to each bot's Hermes gateway route on the other. Both interfaces are named in [docs/SYSTEMS.md](docs/SYSTEMS.md).

## Checks

```sh
gofmt -l .        # prints nothing
go vet ./...
go test ./...
```

That is the whole gate, and CI runs it on every pull request and on `main` (`.github/workflows/checks.yml`). It covers the packages, the loader's refusals, the shape of the audit and dead-letter lines, the wake's outbound half against a stub gateway — the envelope's bytes, its signature in both forms, the gateway URL it is posted to, and the retry bound — the routing table fixture by fixture against a stubbed board, and a signed POST driven through the receiver and the real router. It does not cover the board read against the live API: the read has its own tests, which drive it over a stubbed one — the URL it names, the header it authorizes with, and the answer it makes of what comes back.

## Documentation

- [docs/PRODUCT.md](docs/PRODUCT.md) — what the service is for, and how it is judged.
- [docs/SYSTEMS.md](docs/SYSTEMS.md) — the architecture, the interfaces, and the settled record.
- [docs/RULESET.md](docs/RULESET.md) — which event wakes whom, and the precedence the rules are read under.
- [docs/adrs/000-record-architecture-decisions.md](docs/adrs/000-record-architecture-decisions.md) — how decisions are recorded, and the ten records beside it.
