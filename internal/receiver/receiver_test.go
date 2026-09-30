// These are the receiver's own tests, and they live in the package rather than
// beside it because three of the things they drive are unexported on purpose:
// the bounds behind the 429 and the 503, and the router seam — which is how a
// routing error and a filtered event are driven without a board. Everything
// else is the real thing: a signed delivery through the handler, the router's
// own decision over a stubbed board, the poster over a stubbed gateway, and a
// real audit trail.
//
// Every fixture is a signed delivery and a real audit trail in a temporary
// directory. Nothing here touches the network, and nothing depends on the wall
// clock: the two places the receiver reads time — the dedup TTL and the rate
// limit — are handed a clock the test pins.
package receiver

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aivara-se/dispatcher/internal/audit"
	"github.com/aivara-se/dispatcher/internal/config"
	"github.com/aivara-se/dispatcher/internal/router"
	"github.com/aivara-se/dispatcher/internal/wake"
)

const (
	githubSecret  = "the-inbound-webhook-secret"
	gatewaySecret = "the-outbound-gateway-secret"

	// reason is what the stubbed router answers with. The real router composes
	// its own, and TestADeliveryThroughTheRealRouterWakesTheCardHolder asserts
	// that one.
	reason = `aivara-se/dispatcher#6 was assigned to you: "The receiver and the verifier: the only thing that faces the network". It is in Todo on the board. Read the card and start it.`
)

// fixture is a receiver over real files in a temporary directory, with the
// router and the poster stubbed so that one answer can be driven at a time.
type fixture struct {
	rec    *Receiver
	cfg    *config.Config
	trail  *audit.Log
	dead   *audit.DeadLetter
	router *fakeRouter
	poster *fakePoster
	dir    string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	t.Setenv("TEST_GITHUB_SECRET", githubSecret)
	t.Setenv("TEST_GATEWAY_SECRET", gatewaySecret)
	dir := t.TempDir()

	trail, err := audit.Open(filepath.Join(dir, "audit.jsonl"), audit.DefaultRotateBytes)
	if err != nil {
		t.Fatalf("opening the audit file: %v", err)
	}
	t.Cleanup(func() { _ = trail.Close() })
	dead, err := audit.OpenDeadLetter(filepath.Join(dir, "dead-letter.jsonl"))
	if err != nil {
		t.Fatalf("opening the dead-letter file: %v", err)
	}
	t.Cleanup(func() { _ = dead.Close() })

	cfg := &config.Config{
		EndpointPath:   "/github",
		Repositories:   []string{"aivara-se/dispatcher"},
		Events:         []string{"issues", "issue_comment", "pull_request"},
		LogPath:        filepath.Join(dir, "audit.jsonl"),
		DeadLetterPath: filepath.Join(dir, "dead-letter.jsonl"),
		RequestTimeout: 10 * time.Second,
		RetryBound:     3,
		DedupTTL:       time.Hour,
		DedupBound:     8,
		Routes: []config.Route{{
			Name:             "mimi",
			Bot:              "mimi",
			Profile:          "mimi",
			GatewayRoute:     "mimi-queue",
			SecretRef:        "TEST_GITHUB_SECRET",
			GatewaySecretRef: "TEST_GATEWAY_SECRET",
		}},
	}

	// The concrete router and poster are what main builds, and building them is
	// what keeps New's signature honest; the two stubs take over for the
	// branches that need a fixed answer, and the test below drives the real
	// router's own decision with a board of its own.
	routerStub := &fakeRouter{decision: router.Decision{Wake: true, Bot: "mimi", Reason: reason}}
	posterStub := &fakePoster{outcome: wake.Outcome{Status: http.StatusAccepted, Attempts: 1}}
	rec := New(cfg, router.New(cfg, nil), wake.New(cfg, &http.Client{Timeout: cfg.RequestTimeout}), trail, dead)
	rec.router = routerStub
	rec.wake = posterStub

	return &fixture{rec: rec, cfg: cfg, trail: trail, dead: dead, router: routerStub, poster: posterStub, dir: dir}
}

// issueAssigned is a delivery of the shape GitHub sends when a card is
// assigned: every field the router's input carries, and nothing else.
func issueAssigned(number int) []byte {
	return []byte(fmt.Sprintf(
		`{"action":"assigned","repository":{"full_name":"aivara-se/dispatcher"},"issue":{"number":%d},"assignee":{"login":"thani-sh-mimi"},"sender":{"login":"thani-sh"}}`,
		number))
}

// headers is GitHub's own set for a body: the signature over those exact bytes
// and the two ids the service reads.
func headers(body []byte, delivery string) map[string]string {
	return map[string]string{
		"X-Hub-Signature-256": signature(body, githubSecret),
		"X-GitHub-Event":      "issues",
		"X-GitHub-Delivery":   delivery,
	}
}

func signature(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// deliver sends one request through the handler the process serves.
func (f *fixture) deliver(method, path string, body io.Reader, h map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, body)
	for name, value := range h {
		req.Header.Set(name, value)
	}
	rec := httptest.NewRecorder()
	f.rec.Handler().ServeHTTP(rec, req)
	return rec
}

// post is a signed delivery to the endpoint.
func (f *fixture) post(body []byte, h map[string]string) *httptest.ResponseRecorder {
	return f.deliver(http.MethodPost, f.cfg.EndpointPath, bytes.NewReader(body), h)
}

// audited is the audit trail as decoded lines.
func (f *fixture) audited(t *testing.T) []map[string]any {
	t.Helper()
	return lines(t, filepath.Join(f.dir, "audit.jsonl"))
}

// deadLettered is the dead-letter file as decoded lines.
func (f *fixture) deadLettered(t *testing.T) []map[string]any {
	t.Helper()
	return lines(t, filepath.Join(f.dir, "dead-letter.jsonl"))
}

func lines(t *testing.T, path string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		if line == "" {
			continue
		}
		var got map[string]any
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatalf("%s holds a line that is not JSON: %v (%q)", path, err, line)
		}
		out = append(out, got)
	}
	return out
}

// replyOf decodes the body of a response.
func replyOf(t *testing.T, rec *httptest.ResponseRecorder) reply {
	t.Helper()
	var got reply
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("the response is not the JSON this service writes: %v (%q)", err, rec.Body.String())
	}
	return got
}

// freeze pins the two clocks the receiver reads and returns the handle a test
// advances by hand.
func (f *fixture) freeze(at time.Time) *fakeClock {
	c := &fakeClock{at: at}
	f.rec.dedup.now = c.now
	f.rec.limit.now = c.now
	return c
}

type fakeClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

// fakeRouter stands in for card #8's Resolve.
type fakeRouter struct {
	mu       sync.Mutex
	decision router.Decision
	err      error
	seen     []router.Event
}

func (f *fakeRouter) Resolve(_ context.Context, ev router.Event) (router.Decision, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, ev)
	return f.decision, f.err
}

func (f *fakeRouter) set(decision router.Decision, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.decision, f.err = decision, err
}

func (f *fakeRouter) events() []router.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]router.Event(nil), f.seen...)
}

// fakePoster stands in for card #7's Post. It can be held open, which is how
// the capacity answer is measured without a stopwatch.
type fakePoster struct {
	mu       sync.Mutex
	outcome  wake.Outcome
	route    config.Route
	envelope wake.Envelope
	calls    int
	entered  chan struct{}
	block    chan struct{}
}

func (f *fakePoster) Post(ctx context.Context, route config.Route, env wake.Envelope) wake.Outcome {
	f.mu.Lock()
	f.calls++
	f.route = route
	f.envelope = env
	entered, block := f.entered, f.block
	f.mu.Unlock()

	if entered != nil {
		entered <- struct{}{}
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.outcome
}

func (f *fakePoster) set(outcome wake.Outcome) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.outcome = outcome
}

func (f *fakePoster) posted() (config.Route, wake.Envelope, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.route, f.envelope, f.calls
}

// The endpoint is one path and one method: anything else is a 404, and a
// request this service does not serve is not a delivery, so nothing about it is
// audited.
func TestOnlyTheEndpointAndOnlyPost(t *testing.T) {
	f := newFixture(t)
	for _, tc := range []struct{ name, method, path string }{
		{"another path", http.MethodPost, "/elsewhere"},
		{"below the endpoint", http.MethodPost, "/github/extra"},
		{"the endpoint with the wrong method", http.MethodGet, "/github"},
		{"the root", http.MethodPost, "/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := f.deliver(tc.method, tc.path, strings.NewReader("{}"), nil)
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s %s = %d, want 404: %s", tc.method, tc.path, rec.Code, rec.Body.String())
			}
		})
	}
	if got := f.audited(t); len(got) != 0 {
		t.Errorf("a request the service does not serve wrote %d audit line(s): %v", len(got), got)
	}
}

// 413: the body is read up to the limit ADR 002 fixes, before anything parses
// it and before any signature is checked.
func TestBodyOverTheLimitAnswers413(t *testing.T) {
	f := newFixture(t)
	body := make([]byte, maxBodyBytes+1)
	rec := f.post(body, headers(body, "d-too-large"))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("a body over the limit = %d, want 413: %s", rec.Code, rec.Body.String())
	}
	if got := replyOf(t, rec).Status; got != "too-large" {
		t.Errorf("the response names %q, want too-large", got)
	}
	got := f.audited(t)
	if len(got) != 1 {
		t.Fatalf("audit lines = %d, want 1", len(got))
	}
	if got[0]["decision"] != "too-large" || got[0]["response"] != float64(http.StatusRequestEntityTooLarge) {
		t.Errorf("the audit line = %v", got[0])
	}
}

// 401: no signature at all, and a signature under the wrong secret. Neither is
// parsed, and neither is dispatched.
func TestSignatureRefusals(t *testing.T) {
	body := issueAssigned(6)
	for _, tc := range []struct {
		name    string
		headers map[string]string
	}{
		{"missing", map[string]string{"X-GitHub-Event": "issues", "X-GitHub-Delivery": "d-unsigned"}},
		{"wrong secret", func() map[string]string {
			h := headers(body, "d-wrong")
			h["X-Hub-Signature-256"] = signature(body, "some other secret")
			return h
		}()},
		{"not a signature", func() map[string]string {
			h := headers(body, "d-wrong")
			h["X-Hub-Signature-256"] = "sha256=not-hex-at-all"
			return h
		}()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			rec := f.post(body, tc.headers)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("%s signature = %d, want 401: %s", tc.name, rec.Code, rec.Body.String())
			}
			if got := replyOf(t, rec).Status; got != "unauthorized" {
				t.Errorf("the response names %q, want unauthorized", got)
			}
			if _, _, calls := f.poster.posted(); calls != 0 {
				t.Errorf("an unverified delivery was dispatched %d time(s)", calls)
			}
			got := f.audited(t)
			if len(got) != 1 || got[0]["decision"] != "unauthorized" {
				t.Errorf("the audit trail = %v, want one unauthorized line", got)
			}
		})
	}
}

// The ordering requirement, tested as one case: with a wrong signature, a body
// that is not JSON at all still answers 401 rather than 400, because nothing
// parsed it — and the third party's text reaches no file.
func TestWrongSignatureIsDecidedBeforeTheBodyIsParsed(t *testing.T) {
	f := newFixture(t)
	body := []byte("this is not JSON, and a stranger wrote it")
	h := headers(body, "d-wrong")
	h["X-Hub-Signature-256"] = signature(body, "some other secret")

	rec := f.post(body, h)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("a wrong signature over a body that is not JSON = %d, want 401 — nothing may be parsed before the signature verifies", rec.Code)
	}
	for _, name := range []string{"audit.jsonl", "dead-letter.jsonl"} {
		raw, err := os.ReadFile(filepath.Join(f.dir, name))
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		if strings.Contains(string(raw), "a stranger wrote it") {
			t.Errorf("%s carries the body's text", name)
		}
	}
}

// A body that cannot be read is not a body that is over the limit: 400, and the
// audit line says which.
func TestUnreadableBodyAnswers400(t *testing.T) {
	f := newFixture(t)
	req := httptest.NewRequest(http.MethodPost, f.cfg.EndpointPath, brokenBody{})
	req.Header.Set("X-GitHub-Event", "issues")
	req.Header.Set("X-GitHub-Delivery", "d-broken")
	rec := httptest.NewRecorder()
	f.rec.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a body that could not be read = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if got := replyOf(t, rec).Status; got != "unreadable" {
		t.Errorf("the response names %q, want unreadable", got)
	}
	got := f.audited(t)
	if len(got) != 1 || got[0]["decision"] != "unreadable" {
		t.Errorf("the audit trail = %v", got)
	}
}

type brokenBody struct{}

func (brokenBody) Read([]byte) (int, error) { return 0, errors.New("the client went away") }
func (brokenBody) Close() error             { return nil }

// 400: the body is not the JSON the signature covered (an array a stranger
// could have signed too).
func TestBodyThatIsNotJSONAnswers400(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"a sentence", "this is not JSON"},
		{"an array", `["not","an","envelope"]`},
		{"a bare string", `"an envelope"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			body := []byte(tc.body)
			rec := f.post(body, headers(body, "d-not-json"))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s = %d, want 400: %s", tc.name, rec.Code, rec.Body.String())
			}
			if got := replyOf(t, rec).Status; got != "malformed" {
				t.Errorf("the response names %q, want malformed", got)
			}
		})
	}
}

// 400: an incomplete envelope — no repository to match, or no id to audit and
// dedup with.
func TestIncompleteEnvelopeAnswers400(t *testing.T) {
	body := issueAssigned(6)
	for _, tc := range []struct {
		name    string
		body    []byte
		headers map[string]string
	}{
		{"no repository", []byte(`{"action":"assigned"}`), nil},
		{"null body", []byte(`null`), nil},
		{"no event header", body, without(headers(body, "d-1"), "X-GitHub-Event")},
		{"no delivery header", body, without(headers(body, "d-1"), "X-GitHub-Delivery")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			h := tc.headers
			if h == nil {
				h = headers(tc.body, "d-incomplete")
			}
			rec := f.post(tc.body, h)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s = %d, want 400: %s", tc.name, rec.Code, rec.Body.String())
			}
			if got := replyOf(t, rec).Status; got != "malformed" {
				t.Errorf("the response names %q, want malformed", got)
			}
		})
	}
}

func without(h map[string]string, name string) map[string]string {
	delete(h, name)
	return h
}

// 202, not 4xx: a repository or an event that is not on the allowlist concerns
// nobody here (ADR 002).
func TestNotAllowlistedIsAcceptedAndIgnored(t *testing.T) {
	f := newFixture(t)

	otherRepo := []byte(`{"action":"assigned","repository":{"full_name":"thani-sh/something"},"issue":{"number":1}}`)
	if rec := f.post(otherRepo, headers(otherRepo, "d-other-repo")); rec.Code != http.StatusAccepted || replyOf(t, rec).Status != "ignored" {
		t.Errorf("a repository off the allowlist = %d %s, want 202 ignored", rec.Code, rec.Body.String())
	}

	otherEvent := issueAssigned(6)
	h := with(headers(otherEvent, "d-other-event"), "X-GitHub-Event", "push")
	if rec := f.post(otherEvent, h); rec.Code != http.StatusAccepted || replyOf(t, rec).Status != "ignored" {
		t.Errorf("an event off the allowlist = %d %s, want 202 ignored", rec.Code, rec.Body.String())
	}

	// Neither reached the router, and the trail names which list refused it.
	if got := f.router.events(); len(got) != 0 {
		t.Errorf("an ignored event was routed: %+v", got)
	}
	got := f.audited(t)
	if len(got) != 2 || got[0]["decision"] != "ignored-repository" || got[1]["decision"] != "ignored-event" {
		t.Errorf("the audit trail = %v", got)
	}
}

func with(h map[string]string, name, value string) map[string]string {
	h[name] = value
	return h
}

// 202 duplicate: the same delivery id twice is one wake (ADR 004).
func TestDuplicateDeliveryAnswers202AndDispatchesNothing(t *testing.T) {
	f := newFixture(t)
	body := issueAssigned(6)
	h := headers(body, "d-6")

	first := f.post(body, h)
	second := f.post(body, h)
	if first.Code != http.StatusAccepted {
		t.Fatalf("the first delivery = %d, want 202: %s", first.Code, first.Body.String())
	}
	if second.Code != http.StatusAccepted || replyOf(t, second).Status != "duplicate" {
		t.Fatalf("the second delivery = %d %s, want 202 duplicate", second.Code, second.Body.String())
	}
	if _, _, calls := f.poster.posted(); calls != 1 {
		t.Errorf("the wake was posted %d time(s), want 1", calls)
	}
	got := f.audited(t)
	if len(got) != 2 || got[1]["decision"] != "duplicate" {
		t.Fatalf("the audit trail = %v", got)
	}
	if got[0]["delivery"] != got[1]["delivery"] {
		t.Errorf("the two lines name different deliveries: %v", got)
	}
}

// The whole path: a signed delivery on the allowlist is routed, and the wake
// carries section 5's envelope to that bot's own gateway route.
func TestAValidDeliveryIsWokenWithTheEnvelope(t *testing.T) {
	f := newFixture(t)
	body := issueAssigned(6)
	rec := f.post(body, headers(body, "d-6"))
	if rec.Code != http.StatusAccepted || replyOf(t, rec).Status != "accepted" {
		t.Fatalf("a valid delivery = %d %s, want 202 accepted", rec.Code, rec.Body.String())
	}

	events := f.router.events()
	if len(events) != 1 {
		t.Fatalf("the router saw %d event(s), want 1", len(events))
	}
	card := 6
	wantEvent := router.Event{
		DeliveryID: "d-6",
		Event:      "issues",
		Action:     "assigned",
		Repository: "aivara-se/dispatcher",
		Card:       &card,
		Actor:      "thani-sh",
		Assignee:   "thani-sh-mimi",
	}
	if !reflect.DeepEqual(events[0], wantEvent) {
		t.Errorf("the router saw %+v, want %+v", events[0], wantEvent)
	}

	route, envelope, calls := f.poster.posted()
	if calls != 1 {
		t.Fatalf("the wake was posted %d time(s), want 1", calls)
	}
	if route.Bot != "mimi" || route.GatewayRoute != "mimi-queue" {
		t.Errorf("the wake went to %+v, want mimi's own gateway route", route)
	}
	wantEnvelope := wake.Envelope{
		Source:     "dispatcher",
		Delivery:   "d-6",
		Event:      "issues",
		Action:     "assigned",
		Repository: "aivara-se/dispatcher",
		Card:       &wake.Card{Number: 6, URL: "https://github.com/aivara-se/dispatcher/issues/6"},
		Bot:        "mimi",
		Reason:     reason,
	}
	if !reflect.DeepEqual(envelope, wantEnvelope) {
		t.Errorf("the envelope was %+v, want %+v", envelope, wantEnvelope)
	}

	got := f.audited(t)
	if len(got) != 1 {
		t.Fatalf("audit lines = %d, want 1", len(got))
	}
	line := got[0]
	if line["decision"] != "wake" || line["outcome"] != "accepted" || line["bot"] != "mimi" {
		t.Errorf("the audit line = %v", line)
	}
	if line["card"] != float64(6) || line["response"] != float64(http.StatusAccepted) {
		t.Errorf("the audit line = %v", line)
	}
}

// The gap card #6's own comment named, closed: a verified delivery through the
// real router, over a board of this test's own. The comment's payload is silent
// about the holder, so the delivery exercises the read the router makes and the
// decision it reaches — and the wake carries the reason the router composed
// rather than one a stub returned.
func TestADeliveryThroughTheRealRouterWakesTheCardHolder(t *testing.T) {
	f := newFixture(t)
	holder := &stubBoard{assignees: []string{"thani-sh-mimi"}}
	f.rec.router = router.New(f.cfg, holder)

	body := issueComment(6)
	h := headers(body, "d-6")
	h["X-GitHub-Event"] = "issue_comment"
	rec := f.post(body, h)
	if rec.Code != http.StatusAccepted || replyOf(t, rec).Status != "accepted" {
		t.Fatalf("a delivery through the real router = %d %s, want 202 accepted", rec.Code, rec.Body.String())
	}

	if holder.calls != 1 {
		t.Errorf("the board was read %d time(s), want 1", holder.calls)
	}
	route, envelope, calls := f.poster.posted()
	if calls != 1 {
		t.Fatalf("the wake was posted %d time(s), want 1", calls)
	}
	if route.Bot != "mimi" || route.GatewayRoute != "mimi-queue" {
		t.Errorf("the wake went to %+v, want mimi's own gateway route", route)
	}
	const want = `aivara-se/dispatcher#6 has a new comment, and it is yours. Read the card and answer it.`
	if envelope.Reason != want {
		t.Errorf("the reason was\n  %q\nwant\n  %q", envelope.Reason, want)
	}
	if envelope.Bot != "mimi" {
		t.Errorf("the envelope names %q, want mimi", envelope.Bot)
	}
	if envelope.Card == nil || envelope.Card.Number != 6 {
		t.Errorf("the envelope's card was %+v, want #6", envelope.Card)
	}
	got := f.audited(t)
	if len(got) != 1 || got[0]["decision"] != "wake" || got[0]["bot"] != "mimi" {
		t.Errorf("the audit trail = %v", got)
	}
}

// A board read that failed, driven the same way: the router's error is the
// request path's routing error, nothing is dispatched, and the delivery is not
// recorded as dealt with, so GitHub's retry arrives as a new delivery rather
// than as a duplicate (sections 3 and 7).
func TestADeliveryWhoseBoardReadFailsAsksForTheDeliveryAgain(t *testing.T) {
	f := newFixture(t)
	f.rec.router = router.New(f.cfg, &stubBoard{err: errors.New("the board answered 502")})

	body := issueComment(6)
	h := headers(body, "d-6")
	h["X-GitHub-Event"] = "issue_comment"

	rec := f.post(body, h)
	if rec.Code != http.StatusServiceUnavailable || replyOf(t, rec).Status != "routing-error" {
		t.Fatalf("a delivery whose board read failed = %d %s, want 503 routing-error", rec.Code, rec.Body.String())
	}
	if _, _, calls := f.poster.posted(); calls != 0 {
		t.Errorf("an unrouted delivery was dispatched %d time(s)", calls)
	}
	got := f.audited(t)
	if len(got) != 1 || got[0]["decision"] != "routing-error" {
		t.Errorf("the audit trail = %v", got)
	}
}

// issueComment is a delivery of the shape GitHub sends for a new comment: no
// assignee anywhere in it, which is why the router reads the board.
func issueComment(number int) []byte {
	return []byte(fmt.Sprintf(
		`{"action":"created","repository":{"full_name":"aivara-se/dispatcher"},"issue":{"number":%d},"comment":{"id":1},"sender":{"login":"thani-sh-root"}}`,
		number))
}

// stubBoard is the board the real router is handed here: one holder, no items,
// and a count of how often it was asked.
type stubBoard struct {
	assignees []string
	err       error
	calls     int
}

func (b *stubBoard) Assignees(context.Context, string, int) ([]string, error) {
	b.calls++
	return b.assignees, b.err
}

func (b *stubBoard) Items(context.Context) ([]router.Item, error) {
	b.calls++
	return nil, b.err
}

// An event the router answers "not a wake" for is 202, and nothing is posted.
func TestAnEventThatIsNotAWakeIsFiltered(t *testing.T) {
	f := newFixture(t)
	f.router.set(router.Decision{Wake: false}, nil)
	body := issueAssigned(6)
	rec := f.post(body, headers(body, "d-6"))
	if rec.Code != http.StatusAccepted || replyOf(t, rec).Status != "filtered" {
		t.Fatalf("an event that is not a wake = %d %s, want 202 filtered", rec.Code, rec.Body.String())
	}
	if _, _, calls := f.poster.posted(); calls != 0 {
		t.Errorf("a filtered event was dispatched %d time(s)", calls)
	}
	got := f.audited(t)
	if len(got) != 1 || got[0]["decision"] != "no-wake" {
		t.Errorf("the audit trail = %v", got)
	}
}

// A routing failure dispatches nothing, so it records nothing as dealt with:
// GitHub's retry arrives as a new delivery and is woken.
func TestARoutingErrorAsksForTheDeliveryAgain(t *testing.T) {
	f := newFixture(t)
	f.router.set(router.Decision{}, errors.New("the board could not be read"))
	body := issueAssigned(6)
	h := headers(body, "d-6")

	rec := f.post(body, h)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("a delivery that could not be routed = %d, want 503: %s", rec.Code, rec.Body.String())
	}
	if _, _, calls := f.poster.posted(); calls != 0 {
		t.Errorf("an unrouted delivery was dispatched %d time(s)", calls)
	}

	f.router.set(router.Decision{Wake: true, Bot: "mimi", Reason: reason}, nil)
	rec = f.post(body, h)
	if rec.Code != http.StatusAccepted || replyOf(t, rec).Status != "accepted" {
		t.Fatalf("the redelivery = %d %s, want 202 accepted — the failure must not leave a dedup entry behind", rec.Code, rec.Body.String())
	}
	if _, _, calls := f.poster.posted(); calls != 1 {
		t.Errorf("the wake was posted %d time(s), want 1", calls)
	}
}

// A wake that does not land goes to the dead-letter file with the reason, and
// its dedup entry goes with it so the operator's re-dispatch after the fix is
// not swallowed as a duplicate (ADR 004, section 7).
func TestAFailedWakeIsDeadLetteredAndTheRetryIsNotSwallowed(t *testing.T) {
	f := newFixture(t)
	f.poster.set(wake.Outcome{Attempts: 3, Err: errors.New("connection refused")})
	body := issueAssigned(6)
	h := headers(body, "d-6")

	rec := f.post(body, h)
	if rec.Code != http.StatusAccepted || replyOf(t, rec).Status != "dead-lettered" {
		t.Fatalf("a failed wake = %d %s, want 202 dead-lettered", rec.Code, rec.Body.String())
	}
	dead := f.deadLettered(t)
	if len(dead) != 1 {
		t.Fatalf("dead-letter lines = %d, want 1", len(dead))
	}
	if dead[0]["delivery"] != "d-6" || dead[0]["card"] != float64(6) || dead[0]["bot"] != "mimi" {
		t.Errorf("the dead-letter line = %v", dead[0])
	}
	if dead[0]["attempts"] != float64(3) || dead[0]["reason"] != "connection refused" {
		t.Errorf("the dead-letter line = %v", dead[0])
	}
	got := f.audited(t)
	if len(got) != 1 || got[0]["outcome"] != "dead-lettered" {
		t.Errorf("the audit trail = %v", got)
	}

	f.poster.set(wake.Outcome{Status: http.StatusAccepted, Attempts: 1})
	rec = f.post(body, h)
	if rec.Code != http.StatusAccepted || replyOf(t, rec).Status != "accepted" {
		t.Fatalf("the re-dispatch = %d %s, want 202 accepted", rec.Code, rec.Body.String())
	}
}

// A bot the routes file does not carry is a configuration fault, not a retry:
// the delivery is recorded in the dead-letter file rather than dropped.
func TestABotWithNoRouteIsDeadLettered(t *testing.T) {
	f := newFixture(t)
	f.router.set(router.Decision{Wake: true, Bot: "momo", Reason: reason}, nil)
	body := issueAssigned(6)
	rec := f.post(body, headers(body, "d-6"))
	if rec.Code != http.StatusAccepted || replyOf(t, rec).Status != "dead-lettered" {
		t.Fatalf("a wake for a bot with no route = %d %s, want 202 dead-lettered", rec.Code, rec.Body.String())
	}
	dead := f.deadLettered(t)
	if len(dead) != 1 {
		t.Fatalf("dead-letter lines = %d, want 1", len(dead))
	}
	if dead[0]["bot"] != "momo" || !strings.Contains(fmt.Sprint(dead[0]["reason"]), "no route") {
		t.Errorf("the dead-letter line = %v", dead[0])
	}
	if _, _, calls := f.poster.posted(); calls != 0 {
		t.Errorf("a wake with no route was posted %d time(s)", calls)
	}
}

// 429, and the refill: the bucket answers again once the clock has moved.
func TestRateLimitAnswers429(t *testing.T) {
	f := newFixture(t)
	clock := f.freeze(time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC))
	f.rec.limit = newBucket(1, 1, clock.now)

	firstBody := issueAssigned(6)
	if rec := f.post(firstBody, headers(firstBody, "d-1")); rec.Code != http.StatusAccepted {
		t.Fatalf("the first request = %d, want 202: %s", rec.Code, rec.Body.String())
	}

	secondBody := issueAssigned(7)
	second := f.post(secondBody, headers(secondBody, "d-2"))
	if second.Code != http.StatusTooManyRequests || replyOf(t, second).Status != "rate-limited" {
		t.Fatalf("the request past the burst = %d %s, want 429 rate-limited", second.Code, second.Body.String())
	}

	clock.advance(2 * time.Second)
	if rec := f.post(secondBody, headers(secondBody, "d-2")); rec.Code != http.StatusAccepted {
		t.Fatalf("after the bucket refilled = %d, want 202: %s", rec.Code, rec.Body.String())
	}

	got := f.audited(t)
	if len(got) != 3 {
		t.Fatalf("audit lines = %d, want 3", len(got))
	}
	if got[1]["decision"] != "rate-limited" || got[1]["response"] != float64(http.StatusTooManyRequests) {
		t.Errorf("the audit line = %v", got[1])
	}
}

// The gate's three answers, without a request in sight: a slot, a place in the
// queue, and the refusal when both are taken.
func TestCapacityGate(t *testing.T) {
	ctx := context.Background()

	full := newCapacity(1, 0)
	if !full.enter(ctx) {
		t.Fatal("the first dispatch must take the only slot")
	}
	if full.enter(ctx) {
		t.Error("a dispatch past the slot, with no room in the queue, must be refused: that is the 503")
	}
	full.leave()

	queued := newCapacity(1, 1)
	if !queued.enter(ctx) {
		t.Fatal("the first dispatch must take the only slot")
	}
	served := make(chan bool, 1)
	go func() { served <- queued.enter(ctx) }()
	queued.leave()
	select {
	case ok := <-served:
		if !ok {
			t.Error("a dispatch with a place in the queue must be served, not refused")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the queued dispatch was never served")
	}
	queued.leave()
}

// 503 at capacity, measured through the handler: the first delivery holds the
// only slot (the poster is held open), so the next one is refused.
func TestCapacityAnswers503(t *testing.T) {
	f := newFixture(t)
	f.rec.running = newCapacity(1, 0)
	f.poster.entered = make(chan struct{}, 1)
	f.poster.block = make(chan struct{})

	heldBody := issueAssigned(6)
	first := make(chan *httptest.ResponseRecorder, 1)
	go func() { first <- f.post(heldBody, headers(heldBody, "d-6")) }()

	select {
	case <-f.poster.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the first delivery never reached the wake")
	}

	nextBody := issueAssigned(7)
	rec := f.post(nextBody, headers(nextBody, "d-7"))
	if rec.Code != http.StatusServiceUnavailable || replyOf(t, rec).Status != "over-capacity" {
		t.Fatalf("a delivery with every slot and the queue taken = %d %s, want 503 over-capacity", rec.Code, rec.Body.String())
	}

	close(f.poster.block)
	select {
	case got := <-first:
		if got.Code != http.StatusAccepted {
			t.Errorf("the first delivery = %d, want 202: %s", got.Code, got.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the first delivery never finished")
	}
}

// The TTL is the cache's whole memory: past it the same delivery id arrives as
// a new delivery, which is what the gateway's own cache does at the same rate.
func TestTheDedupCacheForgetsAfterTheTTL(t *testing.T) {
	f := newFixture(t)
	clock := f.freeze(time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC))
	body := issueAssigned(6)
	h := headers(body, "d-6")

	if rec := f.post(body, h); rec.Code != http.StatusAccepted {
		t.Fatalf("the first delivery = %d, want 202", rec.Code)
	}
	if rec := f.post(body, h); replyOf(t, rec).Status != "duplicate" {
		t.Fatalf("the second delivery = %s, want duplicate", rec.Body.String())
	}

	clock.advance(f.cfg.DedupTTL)
	rec := f.post(body, h)
	if rec.Code != http.StatusAccepted || replyOf(t, rec).Status != "accepted" {
		t.Fatalf("the delivery after the TTL = %d %s, want a new delivery", rec.Code, rec.Body.String())
	}
	if _, _, calls := f.poster.posted(); calls != 2 {
		t.Errorf("the wake was posted %d time(s), want 2", calls)
	}
}

// One line per delivery is a promise, and a delivery the trail cannot record is
// refused rather than passed off as handled. The wake has already gone out by
// the time the line is written — the line carries the outcome, so it cannot be
// written first — which is why the delivery's dedup entry is what stops the
// retry from waking the bot twice.
func TestADeliveryThatCannotBeAuditedIsRefused(t *testing.T) {
	f := newFixture(t)
	if err := f.trail.Close(); err != nil {
		t.Fatalf("closing the audit file: %v", err)
	}
	body := issueAssigned(6)
	h := headers(body, "d-6")

	rec := f.post(body, h)
	if rec.Code != http.StatusServiceUnavailable || replyOf(t, rec).Status != "unaudited" {
		t.Fatalf("a delivery the trail cannot record = %d %s, want 503 unaudited", rec.Code, rec.Body.String())
	}

	// The retry is refused too, and it is not a second wake: the delivery was
	// already recorded as seen, so nothing dispatches again.
	rec = f.post(body, h)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("the retry = %d, want 503 while the trail is broken: %s", rec.Code, rec.Body.String())
	}
	if _, _, calls := f.poster.posted(); calls != 1 {
		t.Errorf("the wake was posted %d time(s), want 1: a retry must not wake the bot twice", calls)
	}
}

// The two files carry identifiers and nothing else: no secret, and not a byte
// of what a third party wrote (ADR 002, section 8).
func TestNoSecretAndNoPayloadTextReachesTheFiles(t *testing.T) {
	f := newFixture(t)
	f.poster.set(wake.Outcome{Attempts: 1, Err: errors.New("connection refused")})
	const stranger = "a third party wrote this title"
	body := []byte(`{"action":"assigned","repository":{"full_name":"aivara-se/dispatcher"},` +
		`"issue":{"number":6,"title":"` + stranger + `"},"sender":{"login":"thani-sh"}}`)

	if rec := f.post(body, headers(body, "d-6")); rec.Code != http.StatusAccepted {
		t.Fatalf("the delivery = %d, want 202: %s", rec.Code, rec.Body.String())
	}
	for _, name := range []string{"audit.jsonl", "dead-letter.jsonl"} {
		raw, err := os.ReadFile(filepath.Join(f.dir, name))
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		for _, secret := range []string{githubSecret, gatewaySecret} {
			if strings.Contains(string(raw), secret) {
				t.Errorf("%s carries a secret", name)
			}
		}
		if strings.Contains(string(raw), stranger) {
			t.Errorf("%s carries the payload's text", name)
		}
	}
}

// The claim wake carries the card it claimed, in the envelope's repository and
// card both. The gateway groups bursts by that pair, so a claim that borrowed
// the delivery's own number could be grouped with a genuine wake about it and
// one of the two would be swallowed (docs/SYSTEMS.md sections 4 and 5).
func TestAClaimWakeCarriesTheCardItClaimed(t *testing.T) {
	f := newFixture(t)
	const claim = "aivara-se/learn-chess#43 is claimable, and it is your turn."
	f.router.set(router.Decision{
		Wake:   true,
		Bot:    "mimi",
		Reason: claim,
		Card:   &router.Card{Repository: "aivara-se/learn-chess", Number: 43},
	}, nil)

	body := issueAssigned(9)
	rec := f.post(body, headers(body, "d-9"))
	if rec.Code != http.StatusAccepted || replyOf(t, rec).Status != "accepted" {
		t.Fatalf("a claim wake = %d %s, want 202 accepted", rec.Code, rec.Body.String())
	}

	_, envelope, calls := f.poster.posted()
	if calls != 1 {
		t.Fatalf("the wake was posted %d time(s), want 1", calls)
	}
	want := wake.Envelope{
		Source:     "dispatcher",
		Delivery:   "d-9",
		Event:      "issues",
		Action:     "assigned",
		Repository: "aivara-se/learn-chess",
		Card:       &wake.Card{Number: 43, URL: "https://github.com/aivara-se/learn-chess/issues/43"},
		Bot:        "mimi",
		Reason:     claim,
	}
	if !reflect.DeepEqual(envelope, want) {
		t.Errorf("the envelope was\n  %+v\nwant\n  %+v", envelope, want)
	}
}
