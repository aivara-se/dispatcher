# 002 - GitHub Webhook Ingestion

## Context

The endpoint is on the public internet and anyone may POST to it. GitHub signs what it sends so the receiver can tell a delivery from a stranger's request; the signature covers the raw bytes of the body, and the body is JSON written by GitHub but containing text authored by arbitrary third parties — issue titles, comments, commit messages.

## Decision

- **Read the raw body first, verify before parsing.** The signature is `X-Hub-Signature-256`, an HMAC-SHA256 over the raw request bytes, hex-encoded and prefixed with `sha256=`. It is compared with a constant-time comparison. Nothing is unmarshalled until it verifies.
- **One secret per route**, never one global secret: a secret belongs to a source — the organisation webhook, or one repository — so a leak is one route's and its rotation is one route's.
- **Reject before parsing**: `401` for a missing or non-verifying signature, `413` when the body limit is reached, `400` for a body that is not the JSON the signature covered.
- **A body limit of 1 MiB**, matching the limit the Hermes gateway's own webhook adapter enforces, so a payload is refused at both hops or at neither.
- **An allowlist of repositories and events.** Anything not on it is accepted and ignored rather than rejected: a 4xx is for a request that was wrong, not for one that is merely ours to ignore, and a delivery list full of `4xx` cannot be used to find the real failures.
- **The payload is untrusted input.** Every field a decision uses is matched against a known set or against configuration; no field becomes a path, a command, a URL or a query. Signature verification authenticates the sender, never the content.
- **No body is logged.** An audit line carries identifiers — delivery id, event, repository, card number — and never the text a third party wrote.

## Consequences

- The endpoint can be exposed with a clear conscience: an unsigned request costs a header check, and a wrong signature costs nothing else.
- A sender configured with the wrong secret fails loudly — `401`, visible in GitHub's delivery list — rather than silently doing nothing.
- Per-route secrets mean the secret store grows with the routes, and every new route is two changes: a routes entry and a secret. Accepted; a single shared secret would be smaller and one leak would be every route.
- The 1 MiB limit is a number that must be re-checked if GitHub ever raises its own ceiling; if the two disagree, the smaller one silently wins and deliveries fail with a `413` nobody is watching for.

## Rejected

- **Parsing first, then verifying.** The parsed view is not the signed view, and a decoder is the largest attack surface in the request path; verifying first means an unsigned body never reaches one.
- **Signing a re-serialised body.** HMAC over parsed-then-re-encoded JSON is broken by construction — key order and whitespace are not byte-stable — so a signature computed that way verifies only by accident.
- **A single global secret** for every route and every source.
- **No signature, with an IP allowlist instead.** GitHub's published source ranges change and a reverse proxy in front of the service sees the proxy, not GitHub.
- **Rejecting events that are not on the allowlist with `4xx`.** It looks stricter and is worse: the delivery list stops distinguishing an excluded event from a broken one.
