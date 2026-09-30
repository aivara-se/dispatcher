// Package router decides which single bot an event belongs to.
//
// Card #8 owns the body of Resolve. What is here is the boundary the other two
// code cards are written against: the type the receiver parses a delivery into,
// the one board read the decision may need, and the decision itself. One event,
// one bot, or no wake at all — never two, never all four (docs/SYSTEMS.md
// section 4, docs/adrs/005-routing-an-event-to-one-bot.md).
package router

import (
	"context"
	"errors"

	"github.com/aivara-se/dispatcher/internal/config"
)

// Event is a delivery the receiver has verified and parsed. It lives here so
// that the router owns its own input: the receiver imports this package to
// produce one and to call Resolve, and nothing has to import the receiver.
type Event struct {
	DeliveryID string // X-GitHub-Delivery
	Event      string // X-GitHub-Event
	Action     string // the payload's action, when the event has one
	Repository string // owner/name
	Card       *int   // the issue or pull request number, when the event has one
	Actor      string // the login that caused the event
	Assignee   string // the login assigned, for an assignment
}

// Board is the one read the router may make when the payload does not carry the
// owning bot (docs/SYSTEMS.md section 4). Each use of it should earn its API
// call, which is why it is an interface the router is handed rather than a
// client it builds.
type Board interface {
	Assignee(ctx context.Context, repository string, card int) (string, error)
}

// Decision is the routing answer: a bot to wake with a reason, or nothing.
type Decision struct {
	Wake   bool
	Bot    string
	Reason string
}

// Router resolves events. It is constructed once and used from more than one
// request goroutine, so any state it gains has to be safe for that.
type Router struct {
	cfg   *config.Config
	board Board
}

// New builds the router. A nil Board is the router with no board read: the
// events that need one then resolve to no wake rather than to a guess.
func New(cfg *config.Config, board Board) *Router {
	return &Router{cfg: cfg, board: board}
}

// ErrNotImplemented is the skeleton's answer until card #8 lands.
var ErrNotImplemented = errors.New("router: not implemented — card #8 owns this")

// Resolve answers one event. It is the signature card #8 fills in.
func (r *Router) Resolve(ctx context.Context, ev Event) (Decision, error) {
	return Decision{}, ErrNotImplemented
}
