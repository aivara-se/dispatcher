// Package wake composes the wake text and posts one signed JSON envelope to a
// bot's gateway route.
//
// Card #7 owns the body of Post. The Envelope below is the wire shape in
// docs/SYSTEMS.md section 5, which is settled, and the reason is composed here
// rather than in the route's own template because the facts that make the
// reason — the card, its stage, who acts next — are what the router just
// resolved.
package wake

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/aivara-se/dispatcher/internal/config"
)

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

// Outcome is what became of one wake: the status the gateway answered, how many
// attempts it took, and the transport failure when there was one. A 4xx is
// never retried (docs/SYSTEMS.md section 5).
type Outcome struct {
	Status   int
	Attempts int
	Err      error
}

// OK reports whether the wake was accepted.
func (o Outcome) OK() bool { return o.Err == nil && o.Status >= 200 && o.Status < 300 }

// Poster posts wakes. Card #7 fills in Post; the outbound URL is either
// /webhooks/<route> on a single-profile gateway or /p/<profile>/webhooks/<route>
// where profile multiplexing is on, which is a host fact settled when the route
// is created (docs/SYSTEMS.md sections 5 and 10).
type Poster struct {
	cfg    *config.Config
	client *http.Client
}

// New builds the poster over the process's HTTP client, which carries the
// configured request timeout.
func New(cfg *config.Config, client *http.Client) *Poster {
	return &Poster{cfg: cfg, client: client}
}

// ErrNotImplemented is the skeleton's answer until card #7 lands.
var ErrNotImplemented = errors.New("wake: not implemented — card #7 owns this")

// Post signs the envelope with the route's gateway secret and posts it, with a
// bounded retry for a connection error or a 5xx. It is the signature card #7
// fills in.
func (p *Poster) Post(ctx context.Context, route config.Route, env Envelope) Outcome {
	return Outcome{Attempts: 0, Err: ErrNotImplemented}
}
