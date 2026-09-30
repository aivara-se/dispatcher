// These are the board read's own tests. The read is the one part of the service
// that talks to GitHub, so what is driven here is the client over a stubbed API:
// the URL it names, the header it authorizes with, the query it sends, and the
// answer it makes of what comes back. Nothing here touches the network.
package board_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/aivara-se/dispatcher/internal/board"
	"github.com/aivara-se/dispatcher/internal/config"
	"github.com/aivara-se/dispatcher/internal/router"
)

// theToken is what the stubbed read authorizes with. It is not a secret and it
// is named here only so that the tests can assert it never reaches an error.
const theToken = "tok-not-a-secret"

// answer is one answer the stubbed API gives.
type answer struct {
	status int
	body   string
}

// api is GitHub's API, stubbed: the answers are given in the order the read
// asks for them, and what the read sent is kept.
type api struct {
	*httptest.Server
	mu      sync.Mutex
	calls   int
	auths   []string
	bodies  []string
	answers []answer
}

func newAPI(t *testing.T, answers ...answer) *api {
	t.Helper()
	stub := &api{answers: answers}
	stub.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		stub.mu.Lock()
		stub.calls++
		call := stub.calls
		stub.auths = append(stub.auths, r.Header.Get("Authorization"))
		stub.bodies = append(stub.bodies, string(body))
		stub.mu.Unlock()

		if call > len(stub.answers) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"call":"the read made one call more than the test gives answers for"}`)
			return
		}
		one := stub.answers[call-1]
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(one.status)
		_, _ = io.WriteString(w, one.body)
	}))
	t.Cleanup(stub.Close)
	return stub
}

func (a *api) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

func (a *api) authorized() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.auths...)
}

func (a *api) sent() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.bodies...)
}

// redirect stands between the read and the stub: it keeps the URL the read
// named — the read's own target, which a test can then assert — and sends the
// request to the stub instead.
type redirect struct {
	target string
	mu     sync.Mutex
	asked  []string
}

func (rt *redirect) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.mu.Lock()
	rt.asked = append(rt.asked, req.Method+" "+req.URL.String())
	rt.mu.Unlock()
	to, err := url.Parse(rt.target)
	if err != nil {
		return nil, err
	}
	next := req.Clone(req.Context())
	next.URL.Scheme, next.URL.Host = to.Scheme, to.Host
	return http.DefaultTransport.RoundTrip(next)
}

func (rt *redirect) named() []string {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return append([]string(nil), rt.asked...)
}

// newRead builds the board read over the stub. The token is an environment
// reference, which is the shape ADR 006 allows, and it is resolved at boot.
func newRead(t *testing.T, stub *api, tokenRef string) (*board.Client, *redirect) {
	t.Helper()
	t.Setenv("TEST_BOARD_TOKEN", theToken)
	rt := &redirect{target: stub.URL}
	read, err := board.New(
		&config.Config{Board: config.Board{Owner: "aivara-se", Project: 2, TokenRef: tokenRef}},
		&http.Client{Transport: rt},
	)
	if err != nil {
		t.Fatalf("building the board read: %v", err)
	}
	return read, rt
}

// A token reference that resolves to nothing is refused where the read is
// built, which is at boot, and never at the first silent event
// (docs/adrs/006-configuration-and-secrets.md).
func TestTheTokenIsResolvedWhereTheReadIsBuilt(t *testing.T) {
	stub := newAPI(t)
	if _, err := board.New(
		&config.Config{Board: config.Board{Owner: "aivara-se", Project: 2, TokenRef: "TEST_BOARD_TOKEN_UNSET"}},
		&http.Client{},
	); err == nil {
		t.Fatalf("a token that resolves to nothing must be refused")
	}
	if stub.count() != 0 {
		t.Errorf("the read made %d call(s) with no token", stub.count())
	}
}

// The board is read whole, a page at a time, and what the router is handed is
// one item per card: its repository, number, stage, title, logins and labels.
// An item that is not a card in a repository — a draft the board holds on its
// own — is not read at all (docs/SYSTEMS.md section 4).
func TestTheBoardIsReadAsTheRoutersItems(t *testing.T) {
	stub := newAPI(t, answer{http.StatusOK, page1}, answer{http.StatusOK, page2})
	read, rt := newRead(t, stub, "TEST_BOARD_TOKEN")

	items, err := read.Items(context.Background())
	if err != nil {
		t.Fatalf("reading the board: %v", err)
	}
	want := []router.Item{
		{
			Repository: "aivara-se/dispatcher",
			Number:     9,
			Status:     "In Progress",
			Title:      "the deployment",
			Assignees:  []string{"thani-sh-meme"},
			Labels:     []string{"enhancement"},
		},
		{
			Repository: "aivara-se/learn-chess",
			Number:     4,
			Title:      "an older card nobody took",
			Labels:     []string{"blocked"},
		},
	}
	if !reflect.DeepEqual(items, want) {
		t.Errorf("the board read as\n  %+v\nwant\n  %+v", items, want)
	}

	// The read names the API's own GraphQL endpoint, and nothing else.
	asked := rt.named()
	if len(asked) != 2 || asked[0] != "POST https://api.github.com/graphql" {
		t.Errorf("the read named %v, want two calls to the API's own /graphql", asked)
	}
	// The second page is asked for with the cursor the first page ended on.
	if sent := stub.sent(); len(sent) != 2 || !strings.Contains(sent[0], `"page":100`) || strings.Contains(sent[0], `"after"`) ||
		!strings.Contains(sent[1], `"after":"c1"`) {
		t.Errorf("the pages were asked for as %v", sent)
	}
	for _, header := range stub.authorized() {
		if header != "Bearer "+theToken {
			t.Errorf("a call authorized as %q", header)
		}
	}
}

// One card is read through the REST API, which answers an issue and a pull
// request alike, and the logins on it are the answer — all of them, in the
// order the API gave them: which of them is a bot of this fleet is the router's
// rule and not this package's.
func TestACardIsAnsweredFromItsOwnIssue(t *testing.T) {
	stub := newAPI(t, answer{http.StatusOK, `{"number":9,"assignees":[{"login":"thani-sh"},{"login":"thani-sh-meme"}]}`})
	read, rt := newRead(t, stub, "TEST_BOARD_TOKEN")

	logins, err := read.Assignees(context.Background(), "aivara-se/dispatcher", 9)
	if err != nil {
		t.Fatalf("reading the card: %v", err)
	}
	if !reflect.DeepEqual(logins, []string{"thani-sh", "thani-sh-meme"}) {
		t.Errorf("the card answered %v", logins)
	}
	if asked := rt.named(); len(asked) != 1 || asked[0] != "GET https://api.github.com/repos/aivara-se/dispatcher/issues/9" {
		t.Errorf("the read named %v, want one call to that card", asked)
	}
}

// A delivery that names a pull request carries the pull request's number, and
// the card that pull request belongs to is the issue it closes: the read
// follows it, and takes the lowest-numbered one in the same repository so the
// answer does not depend on the order the API happened to give.
func TestAPullRequestIsAnsweredFromTheIssueItCloses(t *testing.T) {
	stub := newAPI(t,
		answer{http.StatusOK, `{"number":16,"pull_request":{"url":"https://api.github.com/repos/aivara-se/dispatcher/pulls/16"},"assignees":[{"login":"thani-sh-momo"}]}`},
		answer{http.StatusOK, `{"data":{"repository":{"pullRequest":{"closingIssuesReferences":{"nodes":[
			{"number":12,"repository":{"nameWithOwner":"aivara-se/dispatcher"},"assignees":{"nodes":[{"login":"thani-sh-meme"}]}},
			{"number":8,"repository":{"nameWithOwner":"aivara-se/dispatcher"},"assignees":{"nodes":[{"login":"thani-sh-momo"}]}},
			{"number":5,"repository":{"nameWithOwner":"aivara-se/learn-chess"},"assignees":{"nodes":[{"login":"thani-sh-mama"}]}}
		]}}}}}`},
	)
	read, rt := newRead(t, stub, "TEST_BOARD_TOKEN")

	logins, err := read.Assignees(context.Background(), "aivara-se/dispatcher", 16)
	if err != nil {
		t.Fatalf("reading the pull request's card: %v", err)
	}
	if !reflect.DeepEqual(logins, []string{"thani-sh-momo"}) {
		t.Errorf("the pull request answered %v, want the logins on the card it closes", logins)
	}
	asked := rt.named()
	if len(asked) != 2 || asked[0] != "GET https://api.github.com/repos/aivara-se/dispatcher/issues/16" ||
		asked[1] != "POST https://api.github.com/graphql" {
		t.Errorf("the read named %v, want the card and then the link it closes", asked)
	}
	if sent := stub.sent(); len(sent) != 2 || !strings.Contains(sent[1], `"name":"dispatcher"`) || !strings.Contains(sent[1], `"number":16`) {
		t.Errorf("the closing issues were asked for as %v", sent)
	}
}

// A pull request that closes no issue in its own repository holds no card this
// read can name, and that is an empty answer rather than a guess.
func TestAPullRequestThatClosesNothingHoldsNobody(t *testing.T) {
	stub := newAPI(t,
		answer{http.StatusOK, `{"number":16,"pull_request":{}}`},
		answer{http.StatusOK, `{"data":{"repository":{"pullRequest":{"closingIssuesReferences":{"nodes":[]}}}}}`},
	)
	read, _ := newRead(t, stub, "TEST_BOARD_TOKEN")

	logins, err := read.Assignees(context.Background(), "aivara-se/dispatcher", 16)
	if err != nil {
		t.Fatalf("reading the pull request: %v", err)
	}
	if len(logins) != 0 {
		t.Errorf("a pull request that closes nothing answered %v", logins)
	}
}

// A project the token cannot see is a configuration fault and not an empty
// board: an empty board is every claim wake there will ever be, silently gone.
func TestAProjectThatIsNotThereIsAFault(t *testing.T) {
	for _, body := range []string{`{"data":{"organization":null}}`, `{"data":{"organization":{"projectV2":null}}}`} {
		stub := newAPI(t, answer{http.StatusOK, body})
		read, _ := newRead(t, stub, "TEST_BOARD_TOKEN")

		_, err := read.Items(context.Background())
		if err == nil || !strings.Contains(err.Error(), "no project") {
			t.Errorf("%s = %v, want a refusal naming the project", body, err)
		}
	}
}

// The REST read classifies a number as a pull request; a GraphQL read that then
// cannot see it is a failed read, not a pull request that holds nobody.
func TestAPullRequestTheBoardReadCannotSeeIsAFault(t *testing.T) {
	stub := newAPI(t,
		answer{http.StatusOK, `{"number":16,"pull_request":{}}`},
		answer{http.StatusOK, `{"data":{"repository":{"pullRequest":null}}}`},
	)
	read, _ := newRead(t, stub, "TEST_BOARD_TOKEN")

	if _, err := read.Assignees(context.Background(), "aivara-se/dispatcher", 16); err == nil {
		t.Errorf("a pull request the read cannot see must be an error")
	}
}

// A number that names nothing is an empty answer and not an error: section 4
// drops an inconclusive read, and a redelivery of the same fact would be
// inconclusive again.
func TestANumberThatIsNotACardIsAnEmptyAnswer(t *testing.T) {
	stub := newAPI(t, answer{http.StatusNotFound, `{"message":"Not Found"}`})
	read, _ := newRead(t, stub, "TEST_BOARD_TOKEN")

	logins, err := read.Assignees(context.Background(), "aivara-se/dispatcher", 909)
	if err != nil {
		t.Fatalf("a card that is not there must be an empty answer, not a failure: %v", err)
	}
	if len(logins) != 0 {
		t.Errorf("a card that is not there answered %v", logins)
	}
}

// Every fault of a card read is an error, so the request path can answer 503
// and GitHub can deliver the fact again (docs/SYSTEMS.md sections 3 and 7) —
// and none of those errors carries the token. An answer that carries no card is
// one of those faults: reading it as "nobody holds it" would drop a wake
// silently.
func TestEveryFaultOfTheCardReadIsAnErrorWithoutTheToken(t *testing.T) {
	cases := []struct {
		name string
		one  answer
	}{
		{name: "an answer the API could not give", one: answer{http.StatusInternalServerError, `{"message":"Server Error"}`}},
		{name: "a rate limit", one: answer{http.StatusForbidden, `{"message":"API rate limit exceeded"}`}},
		{name: "an answer that is not JSON", one: answer{http.StatusOK, `not json at all`}},
		{name: "an answer that is JSON and not a card", one: answer{http.StatusOK, `{"message":"Not Found"}`}},
		{name: "an answer with no card in it", one: answer{http.StatusOK, `{"assignees":[{"login":"thani-sh-meme"}]}`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := newAPI(t, tc.one)
			read, _ := newRead(t, stub, "TEST_BOARD_TOKEN")

			_, err := read.Assignees(context.Background(), "aivara-se/dispatcher", 9)
			if err == nil {
				t.Fatalf("%s must be an error", tc.name)
			}
			if strings.Contains(err.Error(), theToken) {
				t.Errorf("the error carries the token: %v", err)
			}
		})
	}
}

// The same rule for the board itself, which is read through the GraphQL API:
// that API refuses inside a 200 as well as with a status, and a refusal is an
// error rather than a board that reads as empty — an empty board is a claim
// wake that never happens and a silent event that never wakes anyone.
func TestEveryFaultOfTheBoardReadIsAnErrorWithoutTheToken(t *testing.T) {
	cases := []struct {
		name string
		one  answer
	}{
		{name: "an answer the API could not give", one: answer{http.StatusInternalServerError, `{"message":"Server Error"}`}},
		{name: "a rate limit", one: answer{http.StatusForbidden, `{"message":"API rate limit exceeded"}`}},
		{name: "an answer that is not JSON", one: answer{http.StatusOK, `not json at all`}},
		{name: "a refusal inside a 200", one: answer{http.StatusOK, `{"data":null,"errors":[{"message":"Something went wrong while executing your query"}]}`}},
		{name: "no data and no error", one: answer{http.StatusOK, `{"data":null}`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := newAPI(t, tc.one)
			read, _ := newRead(t, stub, "TEST_BOARD_TOKEN")

			_, err := read.Items(context.Background())
			if err == nil {
				t.Fatalf("%s must be an error", tc.name)
			}
			if strings.Contains(err.Error(), theToken) {
				t.Errorf("the error carries the token: %v", err)
			}
		})
	}
}

// The repository a delivery names is the one payload field that reaches a URL.
// It is checked before it becomes a path, and a shape that is not owner/name is
// refused without a call.
func TestARepositoryThatIsNotOneIsRefused(t *testing.T) {
	for _, repository := range []string{"", "aivara-se", "aivara-se/", "/dispatcher", "owner/name/extra", "aivara-se/../..", "aivara-se/a b"} {
		t.Run(repository, func(t *testing.T) {
			stub := newAPI(t)
			read, _ := newRead(t, stub, "TEST_BOARD_TOKEN")

			if _, err := read.Assignees(context.Background(), repository, 9); err == nil {
				t.Errorf("%q must be refused", repository)
			}
			if stub.count() != 0 {
				t.Errorf("%q reached the API", repository)
			}
		})
	}
}

// A page cursor that does not move is refused rather than walked forever, and a
// board longer than the walk is refused rather than silently truncated.
func TestTheWalkOverTheBoardIsBounded(t *testing.T) {
	t.Run("a cursor that does not move", func(t *testing.T) {
		stub := newAPI(t, answer{http.StatusOK, `{"data":{"organization":{"projectV2":{"items":{
			"pageInfo":{"hasNextPage":true,"endCursor":""},"nodes":[]}}}}}`})
		read, _ := newRead(t, stub, "TEST_BOARD_TOKEN")

		_, err := read.Items(context.Background())
		if err == nil || !strings.Contains(err.Error(), "no cursor") {
			t.Fatalf("a repeated page = %v, want a refusal", err)
		}
		if stub.count() != 1 {
			t.Errorf("the read made %d call(s) for a page that does not move", stub.count())
		}
	})

	t.Run("a board longer than the walk", func(t *testing.T) {
		answers := make([]answer, 0, 64)
		for i := 0; i < 64; i++ {
			answers = append(answers, answer{http.StatusOK, fmt.Sprintf(
				`{"data":{"organization":{"projectV2":{"items":{"pageInfo":{"hasNextPage":true,"endCursor":"c%d"},"nodes":[]}}}}}`,
				i)})
		}
		stub := newAPI(t, answers...)
		read, _ := newRead(t, stub, "TEST_BOARD_TOKEN")

		_, err := read.Items(context.Background())
		if err == nil || !strings.Contains(err.Error(), "longer than") {
			t.Fatalf("a board that never ends = %v, want a refusal", err)
		}
	})
}

// page1 and page2 are two pages of the board as the API answers it: a card
// with everything the router reads, an item that is not a card, and a card with
// no stage of its own.
const page1 = `{"data":{"organization":{"projectV2":{"items":{
  "pageInfo":{"hasNextPage":true,"endCursor":"c1"},
  "nodes":[
    {
      "fieldValueByName":{"__typename":"ProjectV2ItemFieldSingleSelectValue","name":"In Progress"},
      "content":{
        "__typename":"Issue","number":9,"title":"the deployment",
        "repository":{"nameWithOwner":"aivara-se/dispatcher"},
        "assignees":{"nodes":[{"login":"thani-sh-meme"}]},
        "labels":{"nodes":[{"name":"enhancement"}]}
      }
    },
    {
      "fieldValueByName":null,
      "content":{"__typename":"DraftIssue"}
    }
  ]
}}}}}`

const page2 = `{"data":{"organization":{"projectV2":{"items":{
  "pageInfo":{"hasNextPage":false,"endCursor":"c2"},
  "nodes":[
    {
      "fieldValueByName":{"__typename":"ProjectV2ItemFieldTextValue"},
      "content":{
        "__typename":"Issue","number":4,"title":"an older card nobody took",
        "repository":{"nameWithOwner":"aivara-se/learn-chess"},
        "assignees":{"nodes":[]},
        "labels":{"nodes":[{"name":"blocked"}]}
      }
    }
  ]
}}}}}`
