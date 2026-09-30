# dispatcher

A Go HTTP service that GitHub calls: it verifies the payload, decides which single agent in the `aivara-se` fleet the event belongs to, and wakes that agent with the reason attached. It exists so an agent is woken by a fact — a card assigned, a comment, a review, a check run — instead of by a ten-minute timer.

Status: the skeleton is in. The module, the configuration it loads, the audit and dead-letter writers, and the package boundaries are on `main`; `internal/receiver` verifies a signed delivery and `internal/wake` posts the signed envelope — the gateway's route, the retry bound, and the two signature forms a route may take — and `internal/router` is the one stub the last code card replaces. Nothing dispatches a wake from a delivery yet: a delivery is verified, deduplicated and audited, then refused where the router's stub answers, before `Post` is reached.

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

That is the whole gate, and CI runs it on every pull request and on `main` (`.github/workflows/checks.yml`). It covers the packages, the loader's refusals, the shape of the audit and dead-letter lines, the wake's outbound half against a stub gateway — the envelope's bytes, its signature in both forms, the gateway URL it is posted to, and the retry bound — and signed POSTs driven through the receiver with the router and the poster stubbed. It does not cover a delivery through the real router: a delivery is verified, deduplicated and audited, then refused where that stub answers, and the check that closes the gap arrives with `internal/router`.

## Documentation

- [docs/PRODUCT.md](docs/PRODUCT.md) — what the service is for, and how it is judged.
- [docs/SYSTEMS.md](docs/SYSTEMS.md) — the architecture, the interfaces, and what is still open.
- [docs/adrs/000-record-architecture-decisions.md](docs/adrs/000-record-architecture-decisions.md) — how decisions are recorded, and the eight records beside it.
