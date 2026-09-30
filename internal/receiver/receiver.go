// Package receiver is the inbound half of the service: the one listener, the
// signature check over the raw body before anything parses it, the dedup cache,
// and the hand-off to the router and the wake.
//
// Card #6 owns both halves — the receiver and the verifier — and replaces the
// body of handle. This file fixes the boundary instead: what the receiver is
// constructed with and the handler the process serves (docs/SYSTEMS.md
// section 3, docs/adrs/002-github-webhook-ingestion.md).
package receiver

import (
	"net/http"

	"github.com/aivara-se/dispatcher/internal/audit"
	"github.com/aivara-se/dispatcher/internal/config"
	"github.com/aivara-se/dispatcher/internal/router"
	"github.com/aivara-se/dispatcher/internal/wake"
)

// Receiver holds what the request path needs: the configuration it enforces, the
// router, the poster, the audit trail and the dead-letter file.
type Receiver struct {
	cfg    *config.Config
	router *router.Router
	wake   *wake.Poster
	trail  *audit.Log
	dead   *audit.DeadLetter
}

// New builds the receiver.
func New(cfg *config.Config, r *router.Router, w *wake.Poster, trail *audit.Log, dead *audit.DeadLetter) *Receiver {
	return &Receiver{cfg: cfg, router: r, wake: w, trail: trail, dead: dead}
}

// Handler is what the process serves: the configured endpoint path and nothing
// else, so a path this service does not serve is a 404 (docs/SYSTEMS.md
// section 3).
//
// Until card #6 lands, every request to the endpoint is answered 501 and names
// the card. That is the honest answer for a skeleton — the path exists, the
// request path in section 3 does not.
func (rec *Receiver) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(rec.cfg.EndpointPath, func(w http.ResponseWriter, req *http.Request) {
		// TODO(#6): read the raw body up to the limit, verify
		// X-Hub-Signature-256 against the route's secret before parsing
		// anything, take X-GitHub-Event and X-GitHub-Delivery from the headers,
		// check the allowlist, check the dedup cache, resolve the bot, post the
		// wake, and append the audit line whatever happened.
		// docs/SYSTEMS.md section 3 is the sequence.
		http.Error(w, "the receiver is not implemented yet: card #6 owns this", http.StatusNotImplemented)
	})
	return mux
}
