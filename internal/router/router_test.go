// These are the router's own tests, and they live beside the package rather
// than in it because what they drive is the decision the rest of the service
// calls: given a delivery and a board, which one bot does it belong to. The
// board is the stub below, so nothing here touches the network, a token or the
// board itself — the two reads are counted instead, because the rule that each
// of them has to earn its call is part of the contract
// (docs/SYSTEMS.md section 4, docs/adrs/005-routing-an-event-to-one-bot.md).
package router_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/aivara-se/dispatcher/internal/config"
	"github.com/aivara-se/dispatcher/internal/router"
)

const (
	// allowlisted is the one repository every fixture's delivery names.
	allowlisted = "aivara-se/dispatcher"
	// unknown is a login that is nobody's: not a bot of the fleet, and not the
	// operator.
	unknown = "octocat"
)

// fleet is the four bots of config/routes.example.yaml in that file's order,
// which is the order the claim is offered in. A test that pins the order pins
// the claim rule, so it is written here as the file writes it.
var fleet = []string{"mama", "meme", "mimi", "momo"}

// routes is the configuration every fixture resolves against: the allowlist,
// and one route per bot in the fleet's order.
func routes() *config.Config {
	cfg := &config.Config{Repositories: []string{allowlisted}, Events: []string{"issues"}}
	for _, bot := range fleet {
		cfg.Routes = append(cfg.Routes, config.Route{
			Name:         bot,
			Bot:          bot,
			Profile:      bot,
			GatewayRoute: bot + "-queue",
		})
	}
	return cfg
}

// board is the router's reader, stubbed. Each read answers what the fixture
// says and counts that it was asked at all.
type board struct {
	assignees     map[string]string // `repository#number` -> login
	items         []router.Item
	err           error
	assigneeCalls int
	itemCalls     int
}

func (b *board) Assignee(_ context.Context, repository string, card int) (string, error) {
	b.assigneeCalls++
	if b.err != nil {
		return "", b.err
	}
	return b.assignees[fmt.Sprintf("%s#%d", repository, card)], nil
}

func (b *board) Items(context.Context) ([]router.Item, error) {
	b.itemCalls++
	if b.err != nil {
		return nil, b.err
	}
	return b.items, nil
}

// fixture is one delivery and the board it arrives on, with what the decision
// should be and which reads it is allowed to spend making it.
type fixture struct {
	name   string
	event  router.Event
	board  *board
	want   string // the bot woken; "" means no wake
	reason string // the reason, checked whenever a bot is woken
	reads  reads  // the board reads the event may make
}

// reads is how often a fixture may touch the board. Each use of the read has to
// earn its call, so a fixture that spends one is saying so.
type reads struct{ assignee, items int }

// delivery is an event with an action and a card number.
func delivery(event, action string, number *int) router.Event {
	return router.Event{
		DeliveryID: "d-1",
		Event:      event,
		Action:     action,
		Repository: allowlisted,
		Card:       number,
		Actor:      "thani-sh-root",
	}
}

// card is a card number for a delivery.
func card(number int) *int { return &number }

// item is a card on the board. An item with no assignee is the claimable shape.
func item(number int, status, title string, assignees ...string) router.Item {
	return router.Item{
		Repository: allowlisted,
		Number:     number,
		Status:     status,
		Title:      title,
		Assignees:  assignees,
	}
}

// blocked is an item in Todo with nobody on it that the claim must not offer.
func blocked(number int) router.Item {
	it := item(number, "Todo", "waiting on somebody else")
	it.Labels = []string{"blocked"}
	return it
}

// fixtures is every case the routing table has to answer: each accepted event
// and action, the actions that are not wakes, the logins that map to nobody,
// and every way a board read can come back inconclusive or empty.
func fixtures() []fixture {
	return []fixture{
		{
			name:  "issues/assigned names the bot in the delivery",
			event: withAssignee(delivery("issues", "assigned", card(8)), "thani-sh-mimi"),
			board: &board{},
			want:  "mimi", reason: "aivara-se/dispatcher#8 was assigned to you. Read the card and start it.",
		},
		{
			name:  "issues/unassigned that leaves a holder wakes that holder",
			event: delivery("issues", "unassigned", card(8)),
			board: &board{items: []router.Item{item(8, "In Review", "the router", "thani-sh-mimi")}},
			want:  "mimi", reason: "aivara-se/dispatcher#8 was unassigned from someone else and is still yours. Read the card and act by its stage on the board.",
			reads: reads{items: 1},
		},
		{
			name:  "issues/unassigned with nobody on the card is the claim",
			event: delivery("issues", "unassigned", card(8)),
			board: &board{items: []router.Item{item(8, "Todo", "the router")}},
			want:  "mama", reason: `aivara-se/dispatcher#8 is claimable, and it is your turn: "the router". It is in Todo on the board with nobody on it. Read the card and claim it.`,
			reads: reads{items: 1},
		},
		{
			name:  "issues/opened that arrives already held wakes the holder",
			event: delivery("issues", "opened", card(9)),
			board: &board{items: []router.Item{item(9, "Todo", "a card that arrived assigned", "thani-sh-meme")}},
			want:  "meme", reason: "aivara-se/dispatcher#9 was opened and it is yours. Read the card and start it.",
			reads: reads{items: 1},
		},
		{
			name:  "issues/closed names the holder in the delivery",
			event: withAssignee(delivery("issues", "closed", card(8)), "thani-sh-momo"),
			board: &board{},
			want:  "momo", reason: "aivara-se/dispatcher#8 was closed. Read the card.",
		},
		{
			name:  "issues/closed with a silent delivery reads the card",
			event: delivery("issues", "closed", card(8)),
			board: &board{assignees: map[string]string{"aivara-se/dispatcher#8": "thani-sh-mama"}},
			want:  "mama", reason: "aivara-se/dispatcher#8 was closed. Read the card.",
			reads: reads{assignee: 1},
		},
		{
			name:  "issues/reopened reads the card",
			event: delivery("issues", "reopened", card(6)),
			board: &board{assignees: map[string]string{"aivara-se/dispatcher#6": "thani-sh-mimi"}},
			want:  "mimi", reason: "aivara-se/dispatcher#6 was reopened. Read the card and act by its stage on the board.",
			reads: reads{assignee: 1},
		},
		{
			name:  "issue_comment/created wakes the card's holder",
			event: delivery("issue_comment", "created", card(6)),
			board: &board{assignees: map[string]string{"aivara-se/dispatcher#6": "thani-sh-mimi"}},
			want:  "mimi", reason: "aivara-se/dispatcher#6 has a new comment, and it is yours. Read the card and answer it.",
			reads: reads{assignee: 1},
		},
		{
			name:  "pull_request_review_comment/created wakes the card's holder",
			event: delivery("pull_request_review_comment", "created", card(12)),
			board: &board{assignees: map[string]string{"aivara-se/dispatcher#12": "thani-sh-momo"}},
			want:  "momo", reason: "aivara-se/dispatcher#12 has a new review comment, and it is yours. Read the review and answer it.",
			reads: reads{assignee: 1},
		},
		{
			name:  "pull_request_review/submitted wakes the card's holder",
			event: delivery("pull_request_review", "submitted", card(12)),
			board: &board{assignees: map[string]string{"aivara-se/dispatcher#12": "thani-sh-momo"}},
			want:  "momo", reason: "aivara-se/dispatcher#12 had a review submitted on it. Read the review and act on it.",
			reads: reads{assignee: 1},
		},
		{
			name:  "pull_request/review_requested reads the card, not the actor",
			event: delivery("pull_request", "review_requested", card(12)),
			board: &board{assignees: map[string]string{"aivara-se/dispatcher#12": "thani-sh-meme"}},
			want:  "meme", reason: "aivara-se/dispatcher#12 has your review requested on it. Read the review request and start it.",
			reads: reads{assignee: 1},
		},
		{
			name:  "pull_request/ready_for_review reads the card",
			event: delivery("pull_request", "ready_for_review", card(12)),
			board: &board{assignees: map[string]string{"aivara-se/dispatcher#12": "thani-sh-meme"}},
			want:  "meme", reason: "aivara-se/dispatcher#12 is ready for review. Read the card and review it.",
			reads: reads{assignee: 1},
		},
		{
			name:  "pull_request/closed by merging reads the card",
			event: merged(delivery("pull_request", "closed", card(12))),
			board: &board{assignees: map[string]string{"aivara-se/dispatcher#12": "thani-sh-momo"}},
			want:  "momo", reason: "aivara-se/dispatcher#12 had its pull request merged. Read the card and finish it.",
			reads: reads{assignee: 1},
		},
		{
			name:  "check_run/completed reads the card the run names",
			event: delivery("check_run", "completed", card(12)),
			board: &board{assignees: map[string]string{"aivara-se/dispatcher#12": "thani-sh-momo"}},
			want:  "momo", reason: "aivara-se/dispatcher#12 had its check run finish. Read the card and see whether the gate passed.",
			reads: reads{assignee: 1},
		},
		{
			name:  "workflow_run/completed reads the card the run names",
			event: delivery("workflow_run", "completed", card(12)),
			board: &board{assignees: map[string]string{"aivara-se/dispatcher#12": "thani-sh-momo"}},
			want:  "momo", reason: "aivara-se/dispatcher#12 had its workflow run finish. Read the card and see whether the gate passed.",
			reads: reads{assignee: 1},
		},

		// The actions that are not wakes. None of them reads the board: an
		// event nobody acts on must not spend an API call finding that out.
		{name: "issues/labeled is not a wake", event: delivery("issues", "labeled", card(8)), board: &board{}},
		{name: "issues/edited is not a wake", event: delivery("issues", "edited", card(8)), board: &board{}},
		{name: "issue_comment/edited is not a wake", event: delivery("issue_comment", "edited", card(6)), board: &board{}},
		{name: "pull_request_review/dismissed is not a wake", event: delivery("pull_request_review", "dismissed", card(12)), board: &board{}},
		{name: "pull_request_review/edited is not a wake", event: delivery("pull_request_review", "edited", card(12)), board: &board{}},
		{name: "pull_request/opened is not a wake", event: delivery("pull_request", "opened", card(12)), board: &board{}},
		{name: "pull_request/synchronize is not a wake", event: delivery("pull_request", "synchronize", card(12)), board: &board{}},
		{name: "a pull request closed without merging is not a wake", event: delivery("pull_request", "closed", card(12)), board: &board{}},
		{name: "check_run/created is not a wake", event: delivery("check_run", "created", card(12)), board: &board{}},
		{name: "workflow_run/requested is not a wake", event: delivery("workflow_run", "requested", card(12)), board: &board{}},
		{name: "push is not a wake", event: delivery("push", "", nil), board: &board{}},

		// The logins that map to nobody. The operator's own login is the one
		// that matters: an assignment to him is not a wake for a bot.
		{name: "an assignment to the operator wakes nobody", event: withAssignee(delivery("issues", "assigned", card(8)), "thani-sh"), board: &board{}},
		{name: "an assignment to an unknown login wakes nobody", event: withAssignee(delivery("issues", "assigned", card(8)), unknown), board: &board{}},
		{name: "an assignment to a login that is not a bot's wakes nobody", event: withAssignee(delivery("issues", "assigned", card(8)), "thani-sh-miriam"), board: &board{}},

		// The inconclusive and empty reads. Each one is dropped rather than
		// guessed at, and each names one bot or none.
		{
			name:  "a comment on a card nobody holds wakes nobody",
			event: delivery("issue_comment", "created", card(6)),
			board: &board{},
			reads: reads{assignee: 1},
		},
		{
			name:  "a comment held by the operator wakes nobody",
			event: delivery("issue_comment", "created", card(6)),
			board: &board{assignees: map[string]string{"aivara-se/dispatcher#6": "thani-sh"}},
			reads: reads{assignee: 1},
		},
		{
			name:  "a check run that names no pull request wakes nobody",
			event: delivery("check_run", "completed", nil),
			board: &board{},
		},
		{
			name:  "a card that is not on the board is not the claim case",
			event: delivery("issues", "unassigned", card(8)),
			board: &board{items: []router.Item{item(7, "Todo", "another card")}},
			reads: reads{items: 1},
		},
		{
			name:  "a card left held by the operator wakes nobody",
			event: delivery("issues", "unassigned", card(8)),
			board: &board{items: []router.Item{item(8, "Todo", "the operator's own", "thani-sh")}},
			reads: reads{items: 1},
		},
		{
			name:  "a blocked card is not claimable",
			event: delivery("issues", "unassigned", card(8)),
			board: &board{items: []router.Item{blocked(8)}},
			reads: reads{items: 1},
		},
		{
			name:  "a card in In Review is not claimable",
			event: delivery("issues", "unassigned", card(8)),
			board: &board{items: []router.Item{item(8, "In Review", "somebody else's review")}},
			reads: reads{items: 1},
		},
		{
			name:  "the claim is the lowest-numbered card, not the event's",
			event: delivery("issues", "unassigned", card(9)),
			board: &board{items: []router.Item{
				item(9, "Todo", "the card that was left"),
				item(4, "Todo", "an older card nobody took"),
			}},
			want: "mama", reason: `aivara-se/dispatcher#4 is claimable, and it is your turn: "an older card nobody took". It is in Todo on the board with nobody on it. Read the card and claim it.`,
			reads: reads{items: 1},
		},
		{
			name:  "the claim is offered to the first free bot in the routes order",
			event: delivery("issues", "unassigned", card(8)),
			board: &board{items: []router.Item{
				item(8, "Todo", "the router"),
				item(20, "Todo", "mama's own work", "thani-sh-mama"),
			}},
			want: "meme", reason: `aivara-se/dispatcher#8 is claimable, and it is your turn: "the router". It is in Todo on the board with nobody on it. Read the card and claim it.`,
			reads: reads{items: 1},
		},
		{
			name:  "review duty does not make a bot busy",
			event: delivery("issues", "unassigned", card(8)),
			board: &board{items: []router.Item{
				item(8, "Todo", "the router"),
				item(20, "In Review", "mama's review", "thani-sh-mama"),
			}},
			want: "mama", reason: `aivara-se/dispatcher#8 is claimable, and it is your turn: "the router". It is in Todo on the board with nobody on it. Read the card and claim it.`,
			reads: reads{items: 1},
		},
		{
			name:  "a bot holding a card from any column of work is busy",
			event: delivery("issues", "unassigned", card(8)),
			board: &board{items: []router.Item{
				item(8, "Todo", "the router"),
				item(20, "Todo", "mama holds this", "thani-sh-mama"),
				item(21, "In Progress", "meme holds this", "thani-sh-meme"),
				item(22, "Ready to Ship", "mimi holds this", "thani-sh-mimi"),
			}},
			want: "momo", reason: `aivara-se/dispatcher#8 is claimable, and it is your turn: "the router". It is in Todo on the board with nobody on it. Read the card and claim it.`,
			reads: reads{items: 1},
		},
		{
			name:  "the claim waits when every bot holds something",
			event: delivery("issues", "unassigned", card(8)),
			board: &board{items: []router.Item{
				item(8, "Todo", "the router"),
				item(20, "Todo", "mama holds this", "thani-sh-mama"),
				item(21, "In Progress", "meme holds this", "thani-sh-meme"),
				item(22, "Ready to Ship", "mimi holds this", "thani-sh-mimi"),
				item(23, "Todo", "momo holds this", "thani-sh-momo"),
			}},
			reads: reads{items: 1},
		},
	}
}

// withAssignee is an event whose delivery names an assignee.
func withAssignee(ev router.Event, login string) router.Event {
	ev.Assignee = login
	return ev
}

// merged is a pull request that closed by merging.
func merged(ev router.Event) router.Event {
	ev.Merged = true
	return ev
}

// The table, case by case: which bot is woken, what the reason says, and which
// board read — if any — the answer cost.
func TestTheRoutingTable(t *testing.T) {
	for _, tc := range fixtures() {
		t.Run(tc.name, func(t *testing.T) {
			got, err := router.New(routes(), tc.board).Resolve(context.Background(), tc.event)
			if err != nil {
				t.Fatalf("resolving %s: %v", tc.name, err)
			}
			if got.Bot != tc.want {
				t.Errorf("woke %q, want %q", got.Bot, tc.want)
			}
			if tc.want == "" && got.Wake {
				t.Errorf("the decision woke %q with %q, want no wake at all", got.Bot, got.Reason)
			}
			if tc.want != "" {
				if !got.Wake {
					t.Fatalf("the decision is not a wake: %+v", got)
				}
				if got.Reason != tc.reason {
					t.Errorf("the reason was\n  %q\nwant\n  %q", got.Reason, tc.reason)
				}
			}
			if tc.board.assigneeCalls != tc.reads.assignee || tc.board.itemCalls != tc.reads.items {
				t.Errorf("the board was read %d time(s) for a holder and %d time(s) whole, want %d and %d",
					tc.board.assigneeCalls, tc.board.itemCalls, tc.reads.assignee, tc.reads.items)
			}
		})
	}
}

// The property the whole table exists for, over every fixture above: one event,
// exactly one bot or nobody — never two, never all four (section 4, ADR 005).
func TestNoFixtureWakesMoreThanOneBot(t *testing.T) {
	for _, tc := range fixtures() {
		t.Run(tc.name, func(t *testing.T) {
			got, err := router.New(routes(), tc.board).Resolve(context.Background(), tc.event)
			if err != nil {
				t.Fatalf("resolving %s: %v", tc.name, err)
			}
			if got.Wake && got.Bot == "" {
				t.Errorf("a wake names no bot: %+v", got)
			}
			if got.Bot != "" && !inFleet(got.Bot) {
				t.Errorf("the decision named %q, which is not one bot of the fleet %v", got.Bot, fleet)
			}
			if got.Wake != (got.Bot != "") {
				t.Errorf("wake=%v with bot %q: a wake names exactly one bot, and no wake names none", got.Wake, got.Bot)
			}
			if got.Wake && strings.TrimSpace(got.Reason) == "" {
				t.Errorf("a wake carries no reason: %+v", got)
			}
		})
	}
}

// A repository that is not on the allowlist concerns nobody here, and the
// answer costs no board read: the table is not even reached (section 4).
func TestTheAllowlistGatesBeforeAnythingElse(t *testing.T) {
	b := &board{assignees: map[string]string{"thani-sh/other#1": "thani-sh-mimi"}}
	ev := delivery("issues", "assigned", card(1))
	ev.Repository = "thani-sh/other"
	ev.Assignee = "thani-sh-mimi"

	got, err := router.New(routes(), b).Resolve(context.Background(), ev)
	if err != nil {
		t.Fatalf("resolving an event off the allowlist: %v", err)
	}
	if got.Wake || got.Bot != "" {
		t.Errorf("an event off the allowlist woke %q: %+v", got.Bot, got)
	}
	if b.assigneeCalls != 0 || b.itemCalls != 0 {
		t.Errorf("an event off the allowlist read the board %d and %d time(s)", b.assigneeCalls, b.itemCalls)
	}
}

// A board read that fails is returned rather than guessed around: the request
// path decides what a failed read costs, and the router never answers it with a
// bot it could not resolve (ADR 005).
func TestAFailedBoardReadIsAnErrorAndNeverAWake(t *testing.T) {
	cases := []struct {
		name  string
		event router.Event
	}{
		{"the holder read", delivery("issue_comment", "created", card(6))},
		{"the claim read", delivery("issues", "unassigned", card(8))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := &board{err: errors.New("the board answered 502")}
			got, err := router.New(routes(), b).Resolve(context.Background(), tc.event)
			if err == nil {
				t.Fatalf("a failed board read resolved to %+v, want an error", got)
			}
			if got.Wake || got.Bot != "" {
				t.Errorf("a failed board read woke %q: %+v", got.Bot, got)
			}
			if !strings.Contains(err.Error(), "the board answered 502") {
				t.Errorf("the error should carry what the read failed with, got: %v", err)
			}
		})
	}
}

// A router with no board read is not a guesser: the events that need the board
// resolve to no wake, which is what main's nil board means today (section 4).
func TestARouterWithNoBoardWakesNobodyForASilentEvent(t *testing.T) {
	got, err := router.New(routes(), nil).Resolve(context.Background(), delivery("issue_comment", "created", card(6)))
	if err != nil {
		t.Fatalf("resolving without a board: %v", err)
	}
	if got.Wake || got.Bot != "" {
		t.Errorf("a router with no board woke %q: %+v", got.Bot, got)
	}
}

// inFleet reports whether a bot is one of the fleet's.
func inFleet(bot string) bool {
	for _, b := range fleet {
		if b == bot {
			return true
		}
	}
	return false
}
