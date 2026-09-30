# dispatcher

A Go HTTP service that GitHub calls: it verifies the payload, decides which single agent in the `aivara-se` fleet the event belongs to, and wakes that agent with the reason attached. It exists so an agent is woken by a fact — a card assigned, a comment, a review, a check run — instead of by a ten-minute timer.

Status: design stage. There is no code in this repository yet; the documents here are the current work, written to be implemented from.

## Requirements

Go 1.26 or newer. The service is standard library only, so there is no dependency to install and one binary to deploy.

## Build and run

There is nothing to build yet. When the first package lands, the service is one binary that takes a routes file and listens on a loopback port:

```sh
go build -o bin/dispatcher ./cmd/dispatcher
bin/dispatcher --config config/routes.yaml
```

GitHub posts to it on one side, and it posts a wake to each bot's Hermes gateway route on the other. Both interfaces are named in [docs/SYSTEMS.md](docs/SYSTEMS.md).

## Documentation

- [docs/PRODUCT.md](docs/PRODUCT.md) — what the service is for, and how it is judged.
- [docs/SYSTEMS.md](docs/SYSTEMS.md) — the architecture, the interfaces, and what is still open.
- [docs/adrs/000-record-architecture-decisions.md](docs/adrs/000-record-architecture-decisions.md) — how decisions are recorded, and the eight records beside it.
