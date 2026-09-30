// Package receiver is the inbound half of the service: the one listener, the
// signature check over the raw body before anything parses it, the dedup cache,
// and the hand-off to the router and the wake.
//
// The request path is docs/SYSTEMS.md section 3, the verification is
// docs/adrs/002-github-webhook-ingestion.md, the dedup rule is
// docs/adrs/004-idempotency-and-replay.md. Three things in it are worth saying
// out loud:
//
//   - The route a delivery belongs to is the one whose secret verifies it. The
//     service answers on a single endpoint path and one secret belongs to each
//     route, so an unverified request does not say which route it is for;
//     trying the configured secrets is the only way to check the signature
//     before parsing the body, which is the whole point of the order. The bot
//     is not read from that route either: the router decides the bot, and the
//     wake goes to that bot's own gateway route, because the inbound secret and
//     the outbound one are different values for different hops (section 8).
//   - Every delivery to the endpoint is audited, whatever the answer, and the
//     line goes down before the response does. A delivery that cannot be
//     audited is refused with 503 rather than handled unaudited.
//   - The answers are section 3's set and no others: 202 acted on, ignored,
//     filtered or duplicate; 400 malformed; 401 unauthorized; 404 for a path or
//     method this service does not serve; 413 over the limit; 429 rate-limited;
//     503 no capacity.
package receiver

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/aivara-se/dispatcher/internal/audit"
	"github.com/aivara-se/dispatcher/internal/config"
	"github.com/aivara-se/dispatcher/internal/router"
	"github.com/aivara-se/dispatcher/internal/wake"
)

const (
	// maxBodyBytes is the body limit: 1 MiB, the same limit the gateway's own
	// webhook adapter enforces, so a payload is refused at both hops or at
	// neither (ADR 002).
	maxBodyBytes = 1 << 20

	// The inbound rate limit: a token bucket over the whole endpoint, because
	// the endpoint is public and an unauthenticated request must not be able to
	// cost more than a token. A refusal is a 429, which GitHub retries, so a
	// burst that arrives too fast is delayed rather than lost.
	defaultRateBurst     = 60
	defaultRatePerSecond = 1

	// The capacity behind the 503: dispatches in flight, and the requests
	// waiting for one (docs/SYSTEMS.md section 7). Past both, the answer is
	// 503 — honest, because the fact is not lost, GitHub retries it.
	defaultInFlight = 8
	defaultPending  = 32
)

// resolver is the router as this package uses it. The concrete *router.Router
// satisfies it; substituting one inside this package's tests is how the
// receiver's own branches — a routing error, a filtered event — are driven
// without a board to read.
type resolver interface {
	Resolve(ctx context.Context, ev router.Event) (router.Decision, error)
}

// poster is the wake as this package uses it, and for the same reason.
type poster interface {
	Post(ctx context.Context, route config.Route, env wake.Envelope) wake.Outcome
}

// Receiver holds what the request path needs: the configuration it enforces,
// the router, the poster, the audit trail, the dead-letter file, the dedup
// cache, the inbound rate limit and the capacity gate.
type Receiver struct {
	cfg     *config.Config
	router  resolver
	wake    poster
	trail   *audit.Log
	dead    *audit.DeadLetter
	dedup   *dedup
	limit   *bucket
	running *capacity
}

// New builds the receiver.
func New(cfg *config.Config, r *router.Router, w *wake.Poster, trail *audit.Log, dead *audit.DeadLetter) *Receiver {
	now := time.Now
	return &Receiver{
		cfg:     cfg,
		router:  r,
		wake:    w,
		trail:   trail,
		dead:    dead,
		dedup:   newDedup(cfg.DedupTTL, cfg.DedupBound, now),
		limit:   newBucket(defaultRateBurst, defaultRatePerSecond, now),
		running: newCapacity(defaultInFlight, defaultPending),
	}
}

// Handler is what the process serves: POST on the configured endpoint path and
// nothing else, so any other path or method is a 404 (docs/SYSTEMS.md
// section 3). The check is explicit rather than a mux's, because a mux answers
// 405 to the right path with the wrong method, and 405 is not in section 3's
// set of answers.
func (rec *Receiver) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != rec.cfg.EndpointPath || req.Method != http.MethodPost {
			http.NotFound(w, req)
			return
		}
		rec.serve(w, req)
	})
}

// serve is the request path of docs/SYSTEMS.md section 3, in that order.
func (rec *Receiver) serve(w http.ResponseWriter, req *http.Request) {
	// The three headers the service reads, and no others.
	signature := req.Header.Get("X-Hub-Signature-256")
	event := req.Header.Get("X-GitHub-Event")
	delivery := req.Header.Get("X-GitHub-Delivery")

	entry := audit.Entry{DeliveryID: delivery, Event: event}

	// 429 — the inbound rate limit, before anything is read. A refusal here
	// costs no parse, and GitHub retries rather than losing the delivery.
	if !rec.limit.allow() {
		entry.Decision = "rate-limited"
		rec.respond(w, entry, http.StatusTooManyRequests, "rate-limited", "the inbound rate limit is exhausted; GitHub retries")
		return
	}

	// 413 — the raw body, up to the limit, before anything is parsed.
	body, err := readBody(w, req)
	if err != nil {
		if errors.Is(err, errTooLarge) {
			entry.Decision = "too-large"
			rec.respond(w, entry, http.StatusRequestEntityTooLarge, "too-large", "the body is over the limit")
			return
		}
		entry.Decision = "unreadable"
		rec.respond(w, entry, http.StatusBadRequest, "unreadable", "the request body could not be read")
		return
	}

	// 401 — the signature over those raw bytes. Nothing has parsed them.
	if !rec.verify(body, signature) {
		entry.Decision = "unauthorized"
		rec.respond(w, entry, http.StatusUnauthorized, "unauthorized", "X-Hub-Signature-256 is missing or does not verify")
		return
	}

	// 400 — the envelope: the JSON the signature covered, and the identifiers
	// an audit line and the dedup cache need.
	ev, err := parseEvent(body, event, delivery)
	if err != nil {
		entry.Decision = "malformed"
		rec.respond(w, entry, http.StatusBadRequest, "malformed", err.Error())
		return
	}
	entry.Action = ev.Action
	entry.Repository = ev.Repository
	entry.Card = ev.Card

	// 202 — the allowlist. An event that concerns nobody here is accepted and
	// ignored, never a 4xx (ADR 002).
	if !contains(rec.cfg.Repositories, ev.Repository) {
		entry.Decision = "ignored-repository"
		rec.respond(w, entry, http.StatusAccepted, "ignored", "the repository is not on the allowlist")
		return
	}
	if !contains(rec.cfg.Events, ev.Event) {
		entry.Decision = "ignored-event"
		rec.respond(w, entry, http.StatusAccepted, "ignored", "the event is not on the allowlist")
		return
	}

	// 503 — capacity, taken before the dedup entry is written so that a refusal
	// here leaves GitHub's retry able to dispatch (section 7).
	if !rec.running.enter(req.Context()) {
		entry.Decision = "over-capacity"
		rec.respond(w, entry, http.StatusServiceUnavailable, "over-capacity", "every dispatch slot and the wait queue behind it are taken")
		return
	}
	defer rec.running.leave()

	// 202 — a delivery already seen was dealt with (ADR 004).
	if rec.dedup.seen(ev.DeliveryID) {
		entry.Decision = "duplicate"
		rec.respond(w, entry, http.StatusAccepted, "duplicate", "this delivery id was already dealt with")
		return
	}

	decision, err := rec.router.Resolve(req.Context(), ev)
	if err != nil {
		// Nothing was dispatched, so nothing was dealt with: the dedup entry
		// goes and the 503 asks GitHub to deliver it again.
		rec.dedup.drop(ev.DeliveryID)
		entry.Decision = "routing-error"
		rec.respond(w, entry, http.StatusServiceUnavailable, "routing-error", "the event could not be routed")
		return
	}
	if !decision.Wake {
		entry.Decision = "no-wake"
		rec.respond(w, entry, http.StatusAccepted, "filtered", "the event is not a wake for anyone")
		return
	}
	entry.Bot = decision.Bot

	// The wake goes to that bot's own route on the gateway, which is not the
	// route the signature named: the two hops hold two different secrets
	// (section 8), and the bot came from the router, not from the route.
	wakeRoute, ok := rec.routeFor(decision.Bot)
	if !ok {
		rec.dedup.drop(ev.DeliveryID)
		entry.Decision = "no-route"
		record := audit.Record{
			DeliveryID: ev.DeliveryID,
			Event:      ev.Event,
			Action:     ev.Action,
			Repository: ev.Repository,
			Card:       ev.Card,
			Bot:        decision.Bot,
			Reason:     "no route is configured for bot " + decision.Bot,
		}
		if err := rec.dead.Append(record); err != nil {
			rec.respond(w, entry, http.StatusServiceUnavailable, "dead-letter-failed", "no route is configured for that bot and the delivery could not be recorded")
			return
		}
		entry.Outcome = "dead-lettered"
		rec.respond(w, entry, http.StatusAccepted, "dead-lettered", "no route is configured for that bot; the delivery is in the dead-letter file")
		return
	}

	envelope := wake.Envelope{
		Source:     "dispatcher",
		Delivery:   ev.DeliveryID,
		Event:      ev.Event,
		Action:     ev.Action,
		Repository: ev.Repository,
		Bot:        decision.Bot,
		Reason:     decision.Reason,
	}
	if ev.Card != nil {
		envelope.Card = wake.CardFor(ev.Repository, *ev.Card)
	}

	outcome := rec.wake.Post(req.Context(), wakeRoute, envelope)
	if !outcome.OK() {
		// A failed wake is never silently dropped (section 7): it goes to the
		// dead-letter file, and its dedup entry goes with it, so an operator's
		// re-dispatch after the fix is not swallowed as a duplicate.
		rec.dedup.drop(ev.DeliveryID)
		entry.Decision = "wake"
		record := audit.Record{
			DeliveryID: ev.DeliveryID,
			Event:      ev.Event,
			Action:     ev.Action,
			Repository: ev.Repository,
			Card:       ev.Card,
			Bot:        decision.Bot,
			Attempts:   outcome.Attempts,
			Reason:     failureReason(outcome),
		}
		if err := rec.dead.Append(record); err != nil {
			rec.respond(w, entry, http.StatusServiceUnavailable, "dead-letter-failed", "the wake failed and the delivery could not be recorded")
			return
		}
		entry.Outcome = "dead-lettered"
		rec.respond(w, entry, http.StatusAccepted, "dead-lettered", "the wake failed; the delivery is in the dead-letter file")
		return
	}

	entry.Decision = "wake"
	entry.Outcome = "accepted"
	rec.respond(w, entry, http.StatusAccepted, "accepted", "the wake was accepted for "+decision.Bot)
}

// verify reports whether the signature is an HMAC-SHA256 of the raw body under
// one of the routes' secrets (ADR 002), compared in constant time. Nothing is
// parsed before this answers yes.
func (rec *Receiver) verify(body []byte, signature string) bool {
	if signature == "" {
		return false
	}
	for _, route := range rec.cfg.Routes {
		secret, err := route.Secret()
		if err != nil {
			// ADR 006 makes a reference that resolves to nothing a boot
			// failure, so this is unreachable inside the process; a route that
			// cannot be verified is skipped rather than trusted.
			continue
		}
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
		if subtle.ConstantTimeCompare([]byte(want), []byte(signature)) == 1 {
			return true
		}
	}
	return false
}

// routeFor is the route whose gateway route the wake is posted to: the one
// named for the bot the router resolved (section 5).
func (rec *Receiver) routeFor(bot string) (config.Route, bool) {
	for _, route := range rec.cfg.Routes {
		if route.Bot == bot {
			return route, true
		}
	}
	return config.Route{}, false
}

// respond appends the audit line and then writes the response. The line goes
// down first: one line per delivery is the promise the trail exists to keep, so
// a delivery that cannot be audited is refused with 503 rather than handled.
func (rec *Receiver) respond(w http.ResponseWriter, entry audit.Entry, response int, status, detail string) {
	entry.Response = response
	if err := rec.trail.Append(entry); err != nil {
		writeReply(w, http.StatusServiceUnavailable, reply{Status: "unaudited", Detail: "the audit line could not be written; the delivery was refused"})
		return
	}
	writeReply(w, response, reply{Status: status, Detail: detail})
}

// failureReason is what the dead-letter file records for a wake that did not
// land: the transport failure, or the status the gateway answered. Neither
// carries a secret.
func failureReason(outcome wake.Outcome) string {
	if outcome.Err != nil {
		return outcome.Err.Error()
	}
	return "the gateway answered " + strconv.Itoa(outcome.Status)
}

// reply is the body of every answer: which of section 3's cases this was, named
// — the 202s are four different things — with a sentence an operator can read.
type reply struct {
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

func writeReply(w http.ResponseWriter, code int, body reply) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	// A response that cannot be written is a client that has gone away: there
	// is nothing left to answer and nothing to retry.
	_ = json.NewEncoder(w).Encode(body)
}

// contains reports whether a value is in an allowlist.
func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}
