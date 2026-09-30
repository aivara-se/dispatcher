// Package wake composes the wake text and posts one signed JSON envelope to a
// bot's gateway route.
//
// The envelope is the wire shape in docs/SYSTEMS.md section 5. It carries the
// reason the router resolved rather than letting the route's own template render
// one from the raw payload, which cannot know the card, its stage or who acts
// next — which is what makes the wake a reason to re-read the card instead of a
// command (docs/adrs/003-waking-an-agent-through-the-gateway.md).
//
// Post is the outbound half of the service: one signed request to that bot's own
// route on the gateway, retried only where another attempt can help, reported as
// an Outcome the request path audits and dead-letters from. Writing the audit
// line and the dead-letter file is not this package's: those belong with the
// request path that holds them (docs/SYSTEMS.md sections 3, 5 and 7).
package wake

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
	"net/url"
	"strconv"
	"time"

	"github.com/aivara-se/dispatcher/internal/config"
)

// firstBackoff is how long the first retry after a failed attempt waits; each
// retry after it waits twice as long, up to backoffCap. The gateway is on the
// same host as this service, so a wait that a delivery is worth is a short one
// (docs/SYSTEMS.md section 5).
const firstBackoff = 50 * time.Millisecond

// backoffCap bounds a single wait, so a configuration with an unusually large
// retry bound cannot hold a delivery for minutes.
const backoffCap = time.Second

// drainLimit is how much of the gateway's answer is read before the connection
// is let go. Nothing about the answer is kept (docs/SYSTEMS.md section 8).
const drainLimit = 1 << 20

// Envelope is what the gateway receives, one JSON object per wake.
type Envelope struct {
	Source     string `json:"source"`
	Delivery   string `json:"delivery"`
	Event      string `json:"event"`
	Action     string `json:"action"`
	Repository string `json:"repository"`
	Card       *Card  `json:"card,omitempty"`
	Bot        string `json:"bot"`
	Reason     string `json:"reason"`
}

// Card is the card an event concerns, when it concerns one.
type Card struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
}

// CardFor is the card block for a repository and a number.
func CardFor(repository string, number int) *Card {
	return &Card{Number: number, URL: "https://github.com/" + repository + "/issues/" + strconv.Itoa(number)}
}

// Outcome is what became of one wake: the status the gateway answered (zero
// when no answer came at all), how many requests were made, and the transport
// failure the last attempt ended in.
//
// Attempts counts requests, and it is the retry rule made visible: one when the
// gateway accepted the wake or refused it with a 4xx, and the configured bound
// plus one when every retry was spent. A 4xx is never retried
// (docs/SYSTEMS.md section 5).
type Outcome struct {
	Status   int
	Attempts int
	Err      error
}

// OK reports whether the wake was accepted.
func (o Outcome) OK() bool { return o.Err == nil && o.Status >= 200 && o.Status < 300 }

// Poster posts wakes. The outbound URL is the bot's own route on the gateway —
// /p/<profile>/webhooks/<route> where the gateway multiplexes profiles, and
// /webhooks/<route> where it does not; both the address and the multiplexing
// are host facts the routes file records (docs/SYSTEMS.md sections 5 and 10).
type Poster struct {
	cfg    *config.Config
	client *http.Client
}

// New builds the poster over the process's HTTP client, which carries the
// configured request timeout.
func New(cfg *config.Config, client *http.Client) *Poster {
	return &Poster{cfg: cfg, client: client}
}

// Post signs the envelope with the route's gateway secret and posts it to the
// bot's own route, retrying a connection error or a 5xx up to the configured
// bound with backoff, and never a 4xx: that means the route, the secret or the
// envelope is wrong, and repeating it repeats the failure (docs/SYSTEMS.md
// section 5).
//
// Each attempt is signed over the same body bytes, so a retry is the same fact
// rather than a second one, and the gateway's own delivery-id dedup is what
// keeps the far end from acting on it twice (docs/adrs/004-idempotency-and-replay.md).
func (p *Poster) Post(ctx context.Context, route config.Route, env Envelope) Outcome {
	secret, err := route.GatewaySecret()
	if err != nil {
		// The reference is validated at boot; one that no longer resolves is a
		// configuration fault, and not something to attempt.
		return Outcome{Err: err}
	}
	target, err := p.endpoint(route)
	if err != nil {
		return Outcome{Err: err}
	}
	body, err := json.Marshal(env)
	if err != nil {
		return Outcome{Err: fmt.Errorf("wake envelope: %w", err)}
	}

	var outcome Outcome
	for wait := firstBackoff; ; {
		outcome.Attempts++
		outcome.Status, outcome.Err = p.send(ctx, target, route, secret, body)
		if !retryable(outcome.Status, outcome.Err) {
			return outcome
		}
		if outcome.Attempts > p.cfg.RetryBound {
			return outcome
		}
		select {
		case <-ctx.Done():
			// A shutting-down process stops between attempts rather than
			// mid-delivery, and says why it stopped.
			outcome.Err = ctx.Err()
			return outcome
		case <-time.After(wait):
		}
		if wait *= 2; wait > backoffCap {
			wait = backoffCap
		}
	}
}

// retryable is the one rule that decides whether another attempt can help: a
// connection error or a 5xx, and nothing else. A 4xx repeats the failure, and a
// request this process cannot even build is a fault in the process rather than
// in the delivery, so neither is retried (docs/SYSTEMS.md section 5).
func retryable(status int, err error) bool {
	if err == nil {
		return status >= http.StatusInternalServerError
	}
	var transport *url.Error
	return errors.As(err, &transport)
}

// endpoint is the bot's own route on the gateway. One route per bot, with its
// own secret, is what makes "wake exactly this profile" a property of the URL
// and the signature rather than of a payload field (docs/SYSTEMS.md section 5).
func (p *Poster) endpoint(route config.Route) (*url.URL, error) {
	base, err := url.Parse(p.cfg.Gateway.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("gateway base_url: %w", err)
	}
	if base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("gateway base_url %q is not an absolute URL", p.cfg.Gateway.BaseURL)
	}
	if p.cfg.Gateway.MultiplexProfiles {
		return base.JoinPath("p", route.Profile, "webhooks", route.GatewayRoute), nil
	}
	return base.JoinPath("webhooks", route.GatewayRoute), nil
}

// send makes one attempt: the envelope, the headers the route's signature form
// calls for, and an answer that is drained and dropped. It returns the status
// the gateway answered, or zero when no answer came.
func (p *Poster) send(ctx context.Context, target *url.URL, route config.Route, secret string, body []byte) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("wake request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if route.SignatureV2 {
		// The gateway's timestamped generic form is signed per attempt, so a
		// retry carries its own timestamp inside the replay window rather than
		// the first attempt's.
		timestamp := strconv.FormatInt(time.Now().Unix(), 10)
		req.Header.Set("X-Webhook-Timestamp", timestamp)
		req.Header.Set("X-Webhook-Signature-V2", hmacHex(secret, timestamp+".", body))
	} else {
		req.Header.Set("X-Hub-Signature-256", "sha256="+hmacHex(secret, "", body))
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	// The gateway's answer is nobody's text to keep: it is read to the limit and
	// dropped, and nothing of it can reach a log line or the audit trail.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, drainLimit))
	return resp.StatusCode, nil
}

// hmacHex is HMAC-SHA256 over prefix and body, lowercase hex. The plain form
// has no prefix; the timestamped form's prefix is "<timestamp>.".
func hmacHex(secret, prefix string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(prefix))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
