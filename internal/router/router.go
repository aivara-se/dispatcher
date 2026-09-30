// Package router decides which single bot an event belongs to.
//
// One event, one bot, or no wake at all — never two, never all four
// (docs/SYSTEMS.md section 4, docs/adrs/005-routing-an-event-to-one-bot.md).
// The decision is three things in order: whether the repository is one this
// service listens to, whether the event and its action are a wake at all, and
// which bot they belong to.
//
// The owning bot comes from the delivery wherever the delivery carries it —
// `thani-sh-<bot>` maps to profile `<bot>`, and nothing else maps to a bot, the
// operator's own login among them. Where the delivery is silent, the card's
// board item answers, and a card left with nobody on it resolves to the claim
// wake: the router computes the claimable card from the board and wakes the bot
// whose turn the routes file's own order gives it. The board read is the only
// part of the decision that leaves the process, so it is an interface the
// router is handed rather than a client it builds, and every use of it has to
// earn its call.
package router

import (
	"context"
	"fmt"
	"strings"

	"github.com/aivara-se/dispatcher/internal/config"
)

// loginPrefix is the fleet's login convention: `thani-sh-<bot>` is profile
// `<bot>`, and a login that is not one of those wakes nobody (docs/SYSTEMS.md
// section 4).
const loginPrefix = "thani-sh-"

// The board's columns, by the names the board uses. They are the rule
// section 4 and ADR 005 state for a claim, and the three in which a card means
// its holder already has work — In Review is deliberately not one of them,
// because review duty does not count as holding a card.
const (
	columnTodo       = "Todo"
	columnInProgress = "In Progress"
	columnReady      = "Ready to Ship"

	// blockedLabel is the label that keeps a card out of the claim: it is
	// waiting on someone else, so waking anyone for it would be a wake for
	// nothing.
	blockedLabel = "blocked"
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
	Merged     bool   // a pull request that closed by merging, for pull_request/closed
}

// Board is the board as the router reads it: the one place the decision leaves
// the process, and the slow, fallible part of an otherwise local answer
// (docs/adrs/005-routing-an-event-to-one-bot.md).
//
// Each call earns itself. Assignee answers one card, and only an event whose
// delivery is silent makes it; Items answers the whole board, and only the
// claim actions make it — the card, the free bot and the claimable card all
// come from that one read.
type Board interface {
	// Assignee is the login holding the card this delivery names, and "" when
	// nobody holds it. A delivery that names a pull request carries the pull
	// request's number, and reading the card that pull request belongs to is
	// this read's business rather than the router's.
	Assignee(ctx context.Context, repository string, card int) (string, error)

	// Items is every card on the board, for the claim wake and for the actions
	// that can leave a card with nobody on it.
	Items(ctx context.Context) ([]Item, error)
}

// Item is one card on the board, as the claim rule reads it.
type Item struct {
	Repository string   // owner/name
	Number     int      // the card's number in its repository
	Status     string   // the board's Status field: Todo, In Progress, Ready to Ship, In Review
	Title      string   // what the card is, for the wake's reason
	Assignees  []string // the logins on it
	Labels     []string // its labels, one of which may be blockedLabel
}

// Decision is the routing answer: a bot to wake with a reason, or nothing.
type Decision struct {
	Wake   bool
	Bot    string
	Reason string
}

// Router resolves events. It is constructed once and used from more than one
// request goroutine, and it holds nothing that changes as it does: the
// configuration it reads and the board it was handed.
type Router struct {
	cfg   *config.Config
	board Board
}

// New builds the router. A nil Board is the router with no board read: the
// events that need one then resolve to no wake rather than to a guess.
func New(cfg *config.Config, board Board) *Router {
	return &Router{cfg: cfg, board: board}
}

// Resolve answers one event: the single bot to wake, or no wake at all.
//
// The order is section 4's. The repository allowlist comes first: a repository
// that is not on it concerns nobody here. Then the table decides whether the
// event and its action are a wake. Then the owning bot is resolved, from the
// delivery where it names one and from the board where it does not.
//
// An event that stops at any of those steps is no wake, and that is not an
// error: it was delivered, it was read, and it concerns nobody. The one thing
// that comes back as an error is a board read that failed, because it is the
// one thing the router cannot answer — and the request path, not the router,
// decides what that costs (docs/SYSTEMS.md sections 3 and 7).
func (r *Router) Resolve(ctx context.Context, ev Event) (Decision, error) {
	if !contains(r.cfg.Repositories, ev.Repository) {
		return Decision{}, nil
	}
	row, ok := ruleFor(ev)
	if !ok {
		return Decision{}, nil
	}
	if row.whenMerged && !ev.Merged {
		// A pull request that closed without merging is not the fact this row
		// names, and waking nobody for it is the table's answer rather than a
		// board read's.
		return Decision{}, nil
	}
	switch row.from {
	case fromPayload:
		if bot := r.bot(ev.Assignee); bot != "" {
			return held(row, bot, ev), nil
		}
		return Decision{}, nil
	case fromPayloadOrHolder:
		if bot := r.bot(ev.Assignee); bot != "" {
			return held(row, bot, ev), nil
		}
		return r.holder(ctx, row, ev)
	case fromHolder:
		return r.holder(ctx, row, ev)
	default:
		return r.claimOrHolder(ctx, row, ev)
	}
}

// held is the decision for an event whose owner is settled: the bot, and the
// reason that says why it is being woken.
func held(row rule, bot string, ev Event) Decision {
	return Decision{
		Wake:   true,
		Bot:    bot,
		Reason: fmt.Sprintf("%s %s. %s", cardKey(ev.Repository, ev.Card), row.what, row.next),
	}
}

// cardKey names the card a reason is about: `repository#number`, or the
// repository alone for a delivery that carries no number.
func cardKey(repository string, card *int) string {
	if card == nil {
		return repository
	}
	return fmt.Sprintf("%s#%d", repository, *card)
}

// holder resolves an event whose delivery is silent: the card's board item
// says who holds it, and a card nobody holds is no wake — the actions that can
// leave a card unheld are the claim path, not this one (section 4).
func (r *Router) holder(ctx context.Context, row rule, ev Event) (Decision, error) {
	if ev.Card == nil || r.board == nil {
		return Decision{}, nil
	}
	login, err := r.board.Assignee(ctx, ev.Repository, *ev.Card)
	if err != nil {
		return Decision{}, fmt.Errorf("the card's assignee could not be read: %w", err)
	}
	if bot := r.bot(login); bot != "" {
		return held(row, bot, ev), nil
	}
	// Nobody is woken: either nobody holds the card, or whoever holds it is not
	// a bot of this fleet — the operator's own login, usually.
	return Decision{}, nil
}

// claimOrHolder resolves an action that can leave a card with nobody on it: an
// `unassigned`, or a card that has just arrived. The board is read whole once,
// and it decides three things — whether the event is our case at all, who holds
// the card now, and, when nobody does, the claim wake (section 4).
func (r *Router) claimOrHolder(ctx context.Context, row rule, ev Event) (Decision, error) {
	if ev.Card == nil || r.board == nil {
		return Decision{}, nil
	}
	items, err := r.board.Items(ctx)
	if err != nil {
		return Decision{}, fmt.Errorf("the board could not be read: %w", err)
	}
	item, ok := find(items, ev.Repository, *ev.Card)
	if !ok {
		// A card that is not on the board is not our case: the event is logged
		// and dropped rather than guessed at (section 4).
		return Decision{}, nil
	}
	if bot := r.owner(item.Assignees); bot != "" {
		// The card still has a holder of ours — an `unassigned` from a second
		// login, or a card that arrived already assigned.
		return held(row, bot, ev), nil
	}
	if len(item.Assignees) > 0 {
		// It is held by somebody who is not a bot of this fleet.
		return Decision{}, nil
	}
	return r.claim(items), nil
}

// claim is the claim wake of section 4: the card nobody holds that the board
// says is next, and the bot whose turn it is. It is the rule the poll
// implemented, carried over deliberately — with the poll retired this is the
// only path that starts a card — and it is no wake at all when there is nothing
// to claim or nobody free to take it.
func (r *Router) claim(items []Item) Decision {
	card, ok := claimable(items)
	if !ok {
		return Decision{}
	}
	bot := r.turn(items)
	if bot == "" {
		return Decision{}
	}
	return Decision{
		Wake: true,
		Bot:  bot,
		Reason: fmt.Sprintf("%s#%d is claimable, and it is your turn: %q. It is in %s on the board with nobody on it. Read the card and claim it.",
			card.Repository, card.Number, card.Title, columnTodo),
	}
}

// claimable is the lowest-numbered card nobody holds: on the board in Todo,
// with no assignee and not labelled blocked (section 4, ADR 005). The number is
// the whole board's, so the oldest card anywhere is the one offered.
func claimable(items []Item) (Item, bool) {
	var next Item
	found := false
	for _, item := range items {
		if item.Status != columnTodo || len(item.Assignees) > 0 || contains(item.Labels, blockedLabel) {
			continue
		}
		if !found || item.Number < next.Number {
			next, found = item, true
		}
	}
	return next, found
}

// turn is the bot whose turn the claim is: the first bot in the routes file's
// own order that holds nothing. The order is the routes file's, so a new bot
// and a different claim order are both configuration rather than code
// (sections 4 and 8).
func (r *Router) turn(items []Item) string {
	for _, route := range r.cfg.Routes {
		if !busy(items, route.Bot) {
			return route.Bot
		}
	}
	return ""
}

// busy reports whether a bot is already holding work: its login on a card in
// Todo, In Progress or Ready to Ship. In Review is deliberately not one of
// them — review duty is not a card held, and it does not stop a bot claiming.
func busy(items []Item, bot string) bool {
	login := loginPrefix + bot
	for _, item := range items {
		if holdsWork(item.Status) && contains(item.Assignees, login) {
			return true
		}
	}
	return false
}

// holdsWork reports whether a card in this column means its holder already has
// work.
func holdsWork(status string) bool {
	switch status {
	case columnTodo, columnInProgress, columnReady:
		return true
	}
	return false
}

// find is the board item for a repository and a card number, and whether the
// board carries it at all.
func find(items []Item, repository string, card int) (Item, bool) {
	for _, item := range items {
		if item.Repository == repository && item.Number == card {
			return item, true
		}
	}
	return Item{}, false
}

// bot maps a login to the fleet: `thani-sh-<bot>` is that bot, and nothing else
// maps to one. An unknown login, a name that is not a bot's, and the operator's
// own login all wake nobody (section 4).
func (r *Router) bot(login string) string {
	name, ok := strings.CutPrefix(login, loginPrefix)
	if !ok || name == "" {
		return ""
	}
	for _, route := range r.cfg.Routes {
		if route.Bot == name {
			return route.Bot
		}
	}
	return ""
}

// owner is the bot among these logins: the first one the convention maps, or ""
// when none of them is a bot's.
func (r *Router) owner(logins []string) string {
	for _, login := range logins {
		if bot := r.bot(login); bot != "" {
			return bot
		}
	}
	return ""
}

// rule is one row of the routing table: what happened, where the owning bot is
// read from, and what to tell the bot to do about it.
type rule struct {
	what       string // the fact, as the reason's first clause
	next       string // what the woken bot should do
	from       source
	whenMerged bool // the row is the wake only for a pull request that merged
}

// source is where a row reads the owning bot from.
type source uint8

const (
	// fromPayload: the delivery itself names the login that holds the card, so
	// no board read is spent.
	fromPayload source = iota
	// fromPayloadOrHolder: it names it when it can, and the card's board item
	// answers when it cannot.
	fromPayloadOrHolder
	// fromHolder: the delivery is silent, so the card's board item is read.
	fromHolder
	// fromBoard: the action can leave a card with nobody on it, so the board is
	// read whole — the card's own item says whether the event is our case, and
	// the claimable card and the free bot come from the same read.
	fromBoard
)

// table is the routing table of docs/SYSTEMS.md section 4: one row per accepted
// event and action, and nothing else is a wake. An assignment, a comment, a
// review, a review request and a finished check run are wakes; a label edit
// nobody acts on, a review dismissed and a pull request pushed to are not.
//
// The claim actions are the ones whose delivery names the login that *left* the
// card rather than the one that has it, which is why they read the board: an
// `unassigned` payload's assignee is the login that was removed, and an
// `opened` card may or may not have arrived with somebody on it.
var table = map[string]map[string]rule{
	"issues": {
		"assigned":   {what: "was assigned to you", next: "Read the card and start it.", from: fromPayload},
		"unassigned": {what: "was unassigned from someone else and is still yours", next: "Read the card and act by its stage on the board.", from: fromBoard},
		"opened":     {what: "was opened and it is yours", next: "Read the card and start it.", from: fromBoard},
		"closed":     {what: "was closed", next: "Read the card.", from: fromPayloadOrHolder},
		"reopened":   {what: "was reopened", next: "Read the card and act by its stage on the board.", from: fromPayloadOrHolder},
	},
	"issue_comment": {
		"created": {what: "has a new comment, and it is yours", next: "Read the card and answer it.", from: fromHolder},
	},
	"pull_request_review_comment": {
		"created": {what: "has a new review comment, and it is yours", next: "Read the review and answer it.", from: fromHolder},
	},
	"pull_request_review": {
		"submitted": {what: "had a review submitted on it", next: "Read the review and act on it.", from: fromHolder},
	},
	"pull_request": {
		"review_requested": {what: "has your review requested on it", next: "Read the review request and start it.", from: fromHolder},
		"ready_for_review": {what: "is ready for review", next: "Read the card and review it.", from: fromHolder},
		"closed":           {what: "had its pull request merged", next: "Read the card and finish it.", from: fromHolder, whenMerged: true},
	},
	"check_run": {
		"completed": {what: "had its check run finish", next: "Read the card and see whether the gate passed.", from: fromHolder},
	},
	"workflow_run": {
		"completed": {what: "had its workflow run finish", next: "Read the card and see whether the gate passed.", from: fromHolder},
	},
}

// ruleFor is the table's row for an event and action, and whether it has one.
func ruleFor(ev Event) (rule, bool) {
	actions, ok := table[ev.Event]
	if !ok {
		return rule{}, false
	}
	found, ok := actions[ev.Action]
	return found, ok
}

// contains reports whether a value is in a list.
func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}
