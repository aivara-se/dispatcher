package wake_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aivara-se/dispatcher/internal/config"
	"github.com/aivara-se/dispatcher/internal/wake"
)

const (
	// secretRef and secret are the outbound hop's reference and value: what the
	// bot's own route on the gateway holds, which the wake is signed with.
	secretRef = "TEST_WAKE_GATEWAY_SECRET"
	secret    = "gateway-secret-value"
)

// envelope is one wake of the shape docs/SYSTEMS.md section 5 gives, with the
// reason this service composed.
func envelope() wake.Envelope {
	return wake.Envelope{
		Source:     "dispatcher",
		Delivery:   "d1e2f3",
		Event:      "issues",
		Action:     "assigned",
		Repository: "aivara-se/dispatcher",
		Card:       wake.CardFor("aivara-se/dispatcher", 7),
		Bot:        "meme",
		Reason:     `aivara-se/dispatcher#7 was assigned to you: "The wake". It is in Todo on the board. Read the card and start it.`,
	}
}

// routes is a configuration with one bot's route, the gateway address the stub
// server answers on, and the outbound secret read from the environment the way
// the process reads it.
func routes(t *testing.T, baseURL string) *config.Config {
	t.Helper()
	t.Setenv(secretRef, secret)
	return &config.Config{
		Gateway:    config.Gateway{BaseURL: baseURL, MultiplexProfiles: true},
		RetryBound: 2,
		Routes: []config.Route{{
			Name:             "meme",
			Bot:              "meme",
			Profile:          "meme",
			GatewayRoute:     "meme-queue",
			GatewaySecretRef: secretRef,
		}},
	}
}

// record is one request a stub gateway was sent, kept whole so a test can put
// the bytes and the headers side by side.
type record struct {
	path   string
	header http.Header
	body   []byte
}

// stub is a gateway route that answers the statuses it is given, in order and
// then repeating the last one, and keeps every request.
type stub struct {
	mu       sync.Mutex
	statuses []int
	seen     []record
	server   *httptest.Server
}

func newStub(t *testing.T, statuses ...int) *stub {
	t.Helper()
	s := &stub{statuses: statuses}
	s.server = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.server.Close)
	return s
}

func (s *stub) handle(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "unreadable body", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	index := len(s.seen)
	if index >= len(s.statuses) {
		index = len(s.statuses) - 1
	}
	status := s.statuses[index]
	s.seen = append(s.seen, record{path: r.URL.Path, header: r.Header.Clone(), body: body})
	s.mu.Unlock()
	w.WriteHeader(status)
}

func (s *stub) requests() []record {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]record(nil), s.seen...)
}

// post sends one wake through a poster built the way the process builds it.
func post(t *testing.T, cfg *config.Config, client *http.Client, env wake.Envelope) wake.Outcome {
	t.Helper()
	if client == nil {
		client = http.DefaultClient
	}
	return wake.New(cfg, client).Post(context.Background(), cfg.Routes[0], env)
}

// The envelope is the byte-for-byte shape of docs/SYSTEMS.md section 5 and the
// signature is the one the gateway verifies it with.
func TestPostDeliversTheEnvelopeAndItsSignature(t *testing.T) {
	cases := []struct {
		name string
		env  wake.Envelope
		want string
	}{
		{
			name: "the card this event concerns",
			env:  envelope(),
			want: `{"source":"dispatcher","delivery":"d1e2f3","event":"issues","action":"assigned","repository":"aivara-se/dispatcher","card":{"number":7,"url":"https://github.com/aivara-se/dispatcher/issues/7"},"bot":"meme","reason":"aivara-se/dispatcher#7 was assigned to you: \"The wake\". It is in Todo on the board. Read the card and start it."}`,
		},
		{
			name: "an event with no card",
			env: wake.Envelope{
				Source:     "dispatcher",
				Delivery:   "d4e5f6",
				Event:      "check_run",
				Action:     "completed",
				Repository: "aivara-se/learn-chess",
				Bot:        "meme",
				Reason:     "a check run finished. The card is the truth; re-read it.",
			},
			want: `{"source":"dispatcher","delivery":"d4e5f6","event":"check_run","action":"completed","repository":"aivara-se/learn-chess","bot":"meme","reason":"a check run finished. The card is the truth; re-read it."}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newStub(t, http.StatusAccepted)
			cfg := routes(t, s.server.URL)
			outcome := post(t, cfg, s.server.Client(), tc.env)
			if !outcome.OK() || outcome.Attempts != 1 {
				t.Fatalf("outcome = %+v, want one accepted attempt", outcome)
			}

			got := s.requests()
			if len(got) != 1 {
				t.Fatalf("the gateway saw %d request(s), want 1", len(got))
			}
			if string(got[0].body) != tc.want {
				t.Errorf("the envelope is not the shape section 5 names:\n got %s\nwant %s", got[0].body, tc.want)
			}
			if got[0].header.Get("Content-Type") != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", got[0].header.Get("Content-Type"))
			}
			if want := "sha256=" + hmacHex(secret, "", got[0].body); got[0].header.Get("X-Hub-Signature-256") != want {
				t.Errorf("X-Hub-Signature-256 = %q, want the HMAC over the bytes sent (%q)", got[0].header.Get("X-Hub-Signature-256"), want)
			}
		})
	}
}

// Every route this service wakes posts to the bot's own route on the gateway:
// multiplexed, /p/<profile>/webhooks/<route>; single-profile, /webhooks/<route>
// (docs/SYSTEMS.md sections 5 and 10).
func TestPostTargetsTheBotsOwnRoute(t *testing.T) {
	cases := []struct {
		name      string
		multiplex bool
		wantPath  string
	}{
		{"the gateway multiplexes profiles", true, "/p/meme/webhooks/meme-queue"},
		{"the gateway serves one profile", false, "/webhooks/meme-queue"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newStub(t, http.StatusAccepted)
			cfg := routes(t, s.server.URL)
			cfg.Gateway.MultiplexProfiles = tc.multiplex
			if outcome := post(t, cfg, s.server.Client(), envelope()); !outcome.OK() {
				t.Fatalf("outcome = %+v, want an accepted wake", outcome)
			}
			got := s.requests()
			if len(got) != 1 {
				t.Fatalf("the gateway saw %d request(s), want 1", len(got))
			}
			if got[0].path != tc.wantPath {
				t.Errorf("the wake went to %s, want %s", got[0].path, tc.wantPath)
			}
		})
	}
}

// A retry is for a connection error or a 5xx: the attempt after it carries the
// same envelope, because it is the same fact (docs/SYSTEMS.md section 5).
func TestPostRetriesA5xxAndStopsAtTheFirstSuccess(t *testing.T) {
	s := newStub(t, http.StatusBadGateway, http.StatusAccepted)
	cfg := routes(t, s.server.URL)

	outcome := post(t, cfg, s.server.Client(), envelope())
	if !outcome.OK() || outcome.Attempts != 2 {
		t.Fatalf("outcome = %+v, want the second attempt accepted", outcome)
	}
	got := s.requests()
	if len(got) != 2 {
		t.Fatalf("the gateway saw %d request(s), want 2", len(got))
	}
	if string(got[0].body) != string(got[1].body) {
		t.Errorf("the retry carried a different envelope:\n first %s\nsecond %s", got[0].body, got[1].body)
	}
}

// The far end keys its own dedup on a header, not on the envelope: the adapter
// builds the key from X-GitHub-Delivery and falls back to a millisecond clock,
// so the id has to travel in the header and be the same one on every attempt. A
// retry is then the same delivery to the gateway rather than a second one, which
// is the property ADR 004 asks both hops to hold at the same rate. The event
// header is the same idea: the adapter reads its event type from the headers,
// and the envelope's own field is named `event`, so without it every wake arrives
// as "unknown" and a route that declares its events answers an accepted-looking
// 200 that nothing ran.
func TestPostCarriesTheDeliveryIdOnEveryAttempt(t *testing.T) {
	s := newStub(t, http.StatusBadGateway, http.StatusAccepted)
	cfg := routes(t, s.server.URL)
	env := envelope()

	outcome := post(t, cfg, s.server.Client(), env)
	if !outcome.OK() || outcome.Attempts != 2 {
		t.Fatalf("outcome = %+v, want the second attempt accepted", outcome)
	}
	got := s.requests()
	if len(got) != 2 {
		t.Fatalf("the gateway saw %d request(s), want 2", len(got))
	}
	for i, request := range got {
		if id := request.header.Get("X-GitHub-Delivery"); id != env.Delivery {
			t.Errorf("attempt %d carried X-GitHub-Delivery %q, want the envelope's %q", i+1, id, env.Delivery)
		}
		if event := request.header.Get("X-GitHub-Event"); event != env.Event {
			t.Errorf("attempt %d carried X-GitHub-Event %q, want the envelope's %q", i+1, event, env.Event)
		}
	}
}

// A 4xx is never retried: it means the route, the secret or the envelope is
// wrong, and repeating it repeats the failure (docs/SYSTEMS.md sections 5 and 7).
func TestPostNeverRetriesA4xx(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusNotFound, http.StatusTooManyRequests} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			s := newStub(t, status)
			cfg := routes(t, s.server.URL)

			outcome := post(t, cfg, s.server.Client(), envelope())
			if outcome.OK() {
				t.Fatalf("a %d is not an accepted wake", status)
			}
			if outcome.Attempts != 1 || outcome.Status != status || outcome.Err != nil {
				t.Errorf("outcome = %+v, want the first attempt, the status and no transport failure", outcome)
			}
			if got := s.requests(); len(got) != 1 {
				t.Errorf("the gateway saw %d request(s): a configuration fault is not retried", len(got))
			}
		})
	}
}

// The retries are bounded: a wake whose connection never succeeds is attempted
// retry_bound times after the first, and the outcome says so with the failure
// that ended it, which is what the caller dead-letters from.
func TestPostSpendsTheRetryBoundAndReportsAConnectionError(t *testing.T) {
	cases := []struct {
		name     string
		bound    int
		attempts int
	}{
		{"no retries", 0, 1},
		{"the configured two", 2, 3},
		{"the example's three", 3, 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A listener that has been closed answers nothing at all, so every
			// attempt ends in a refused connection.
			dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			target := dead.URL
			dead.Close()

			cfg := routes(t, target)
			cfg.RetryBound = tc.bound
			outcome := post(t, cfg, &http.Client{Timeout: time.Second}, envelope())

			if outcome.OK() {
				t.Fatalf("a wake nobody received is not accepted")
			}
			if outcome.Attempts != tc.attempts {
				t.Errorf("attempts = %d, want %d", outcome.Attempts, tc.attempts)
			}
			if outcome.Err == nil {
				t.Fatalf("a wake the gateway never answered must say why")
			}
			if strings.Contains(outcome.Err.Error(), secret) {
				t.Errorf("a transport failure must not carry the secret: %v", outcome.Err)
			}
		})
	}
}

// The client the poster is built over carries the process's request timeout, so
// a gateway that never answers is a failed wake rather than a stuck one
// (docs/adrs/006-configuration-and-secrets.md, docs/SYSTEMS.md section 5).
func TestPostHonoursTheClientsRequestTimeout(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(slow.Close)

	cfg := routes(t, slow.URL)
	cfg.RetryBound = 1
	outcome := post(t, cfg, &http.Client{Timeout: 20 * time.Millisecond}, envelope())

	if outcome.OK() {
		t.Errorf("a gateway that does not answer inside the timeout has not accepted the wake")
	}
	if outcome.Attempts != 2 {
		t.Errorf("attempts = %d, want the first attempt and its one retry", outcome.Attempts)
	}
}

// Which signature form a route takes is settled when the route is created: the
// X-Hub-Signature-256 form GitHub uses, or the gateway's timestamped generic
// form, which carries replay protection the plain one does not
// (docs/SYSTEMS.md section 5).
func TestPostSignsTheFormTheRouteAccepts(t *testing.T) {
	cases := []struct {
		name string
		v2   bool
	}{
		{"the form both hops share", false},
		{"the gateway's timestamped form", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newStub(t, http.StatusAccepted)
			cfg := routes(t, s.server.URL)
			cfg.Routes[0].SignatureV2 = tc.v2

			before := time.Now().Unix()
			if outcome := post(t, cfg, s.server.Client(), envelope()); !outcome.OK() {
				t.Fatalf("outcome = %+v, want an accepted wake", outcome)
			}
			after := time.Now().Unix()

			got := s.requests()
			if len(got) != 1 {
				t.Fatalf("the gateway saw %d request(s), want 1", len(got))
			}
			header, body := got[0].header, got[0].body

			if !tc.v2 {
				if want := "sha256=" + hmacHex(secret, "", body); header.Get("X-Hub-Signature-256") != want {
					t.Errorf("X-Hub-Signature-256 = %q, want %q", header.Get("X-Hub-Signature-256"), want)
				}
				for _, absent := range []string{"X-Webhook-Signature-V2", "X-Webhook-Timestamp"} {
					if header.Get(absent) != "" {
						t.Errorf("%s is on a wake that is signed the plain way", absent)
					}
				}
				return
			}

			timestamp := header.Get("X-Webhook-Timestamp")
			seconds, err := strconv.ParseInt(timestamp, 10, 64)
			if err != nil {
				t.Fatalf("X-Webhook-Timestamp = %q, want Unix seconds", timestamp)
			}
			if seconds < before || seconds > after {
				t.Errorf("the timestamp %d is not the one this attempt was signed with", seconds)
			}
			if want := hmacHex(secret, timestamp+".", body); header.Get("X-Webhook-Signature-V2") != want {
				t.Errorf("X-Webhook-Signature-V2 = %q, want the HMAC over <timestamp>.<body> (%q)", header.Get("X-Webhook-Signature-V2"), want)
			}
			if header.Get("X-Hub-Signature-256") != "" {
				t.Errorf("a route takes one signature form, not both")
			}
		})
	}
}

// A secret reference that no longer resolves is a configuration fault, not a
// delivery: the wake is not attempted at all.
func TestPostReportsAnUnresolvableSecretWithoutAttempting(t *testing.T) {
	s := newStub(t, http.StatusAccepted)
	cfg := routes(t, s.server.URL)
	cfg.Routes[0].GatewaySecretRef = "TEST_WAKE_SECRET_THAT_IS_NOT_SET"

	outcome := post(t, cfg, s.server.Client(), envelope())
	if outcome.Attempts != 0 {
		t.Errorf("attempts = %d, want none: the wake could not be signed", outcome.Attempts)
	}
	if outcome.Err == nil || !strings.Contains(outcome.Err.Error(), "neither set in the environment nor a path") {
		t.Errorf("outcome.Err = %v, want the unresolved reference named", outcome.Err)
	}
	if got := s.requests(); len(got) != 0 {
		t.Errorf("the gateway saw %d request(s), want none", len(got))
	}
}

// The secret is read, used and dropped: it is what the wake is signed with, and
// no failure this package reports can carry its value
// (docs/adrs/006-configuration-and-secrets.md).
func TestPostNeverReportsTheSecretValue(t *testing.T) {
	const fileSecret = "file-secret-value"
	path := filepath.Join(t.TempDir(), "gateway.secret")
	if err := os.WriteFile(path, []byte(fileSecret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := newStub(t, http.StatusServiceUnavailable)
	cfg := routes(t, s.server.URL)
	cfg.Routes[0].GatewaySecretRef = path
	cfg.RetryBound = 0

	outcome := post(t, cfg, s.server.Client(), envelope())
	if outcome.OK() {
		t.Fatalf("a 5xx is not an accepted wake")
	}
	if strings.Contains(fmt.Sprintf("%+v", outcome), fileSecret) {
		t.Errorf("the outcome carries the secret value: %+v", outcome)
	}
	got := s.requests()
	if len(got) != 1 {
		t.Fatalf("the gateway saw %d request(s), want 1", len(got))
	}
	// The file's value was used: the signature the gateway received verifies
	// with it, which is what makes the absence above a property and not a
	// secret that was never read.
	if want := "sha256=" + hmacHex(fileSecret, "", got[0].body); got[0].header.Get("X-Hub-Signature-256") != want {
		t.Errorf("the wake was not signed with the file's value: %q", got[0].header.Get("X-Hub-Signature-256"))
	}
}

// A shutting-down process stops between attempts and says so, rather than
// holding the delivery for the whole retry budget (docs/SYSTEMS.md section 7).
func TestPostStopsWhenTheProcessIsShuttingDown(t *testing.T) {
	s := newStub(t, http.StatusServiceUnavailable)
	cfg := routes(t, s.server.URL)
	cfg.RetryBound = 10

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	outcome := wake.New(cfg, s.server.Client()).Post(ctx, cfg.Routes[0], envelope())

	if !errors.Is(outcome.Err, context.Canceled) {
		t.Errorf("outcome.Err = %v, want the shutdown to be why it stopped", outcome.Err)
	}
	if outcome.Attempts != 1 {
		t.Errorf("attempts = %d, want the one that was in flight", outcome.Attempts)
	}
}

// hmacHex is the signature the gateway computes, written out here so the test
// checks the bytes rather than the implementation that produced them.
func hmacHex(key, prefix string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(prefix))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
