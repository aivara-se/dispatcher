// Package board is the router's board read: the one part of a routing decision
// that leaves the process, and the service's only dependency on the GitHub API
// (docs/SYSTEMS.md section 4, docs/adrs/005-routing-an-event-to-one-bot.md).
//
// Two reads, and each earns its call. Assignees answers one card — the logins
// holding it, or none — and only a delivery that is silent about its owner
// makes it. A delivery that names a pull request carries the pull request's
// number rather than its card's, and the card a pull request belongs to is the
// issue it closes, so that step happens here instead of in the router. Items
// answers the whole board, for the claim wake and for the actions that can
// leave a card with nobody on it.
//
// The board is read with a token of its own, read-only for the board: it is
// resolved at boot from the reference the routes file names, and it is never
// logged, never named in an error, and never written anywhere
// (docs/SYSTEMS.md sections 8 and 10, docs/adrs/006-configuration-and-secrets.md).
//
// The one payload field that reaches a URL is the repository a delivery names:
// it is used only for a repository the allowlist already accepted, and its
// shape is checked here before it becomes a path.
package board

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/aivara-se/dispatcher/internal/config"
	"github.com/aivara-se/dispatcher/internal/router"
)

const (
	// apiURL is GitHub's own API. Nothing else is reachable from here: the base
	// is a constant of this package, and no caller names a host.
	apiURL = "https://api.github.com"

	// graphqlPath is where the board itself is read. The organisation's project
	// items are only in the GraphQL API; a single card is read through the REST
	// API, which answers an issue and a pull request alike instead of failing
	// for one of them (the GraphQL API answers a pull request's number with a
	// NOT_FOUND error rather than a null, so asking for both kinds at once
	// cannot work).
	graphqlPath = "/graphql"

	// apiVersion pins the REST answer this read was written against, so a
	// change on the far side is a change someone chooses rather than one that
	// arrives unannounced.
	apiVersion = "2022-11-28"

	// pageSize is one page of board items, and maxPages bounds the walk over
	// them so a cursor the far end repeats cannot spin here forever.
	pageSize = 100
	maxPages = 50

	// loginsPerCard is how many logins of a card are read. The board's cards
	// carry one, or two during a hand-off.
	loginsPerCard = 20

	// maxAnswerBytes bounds one answer before it is decoded. The whole board on
	// one page is well under it.
	maxAnswerBytes = 8 << 20
)

// Client is the board as the router reads it. It is built once and used from
// more than one request goroutine, and it holds the token, so nothing it
// returns, logs or errors on ever carries the value.
type Client struct {
	base    string
	owner   string
	project int
	token   string
	http    *http.Client
}

// New builds the board read. The token is resolved here, at boot, so a
// reference that resolves to nothing stops the process rather than the first
// silent event — the same rule every route's secrets follow
// (docs/adrs/006-configuration-and-secrets.md).
func New(cfg *config.Config, client *http.Client) (*Client, error) {
	token, err := cfg.Board.Token()
	if err != nil {
		return nil, fmt.Errorf("board: %w", err)
	}
	return &Client{
		base:    apiURL,
		owner:   cfg.Board.Owner,
		project: cfg.Board.Project,
		token:   token,
		http:    client,
	}, nil
}

// Assignees is the router's one-card read: the logins on the card this delivery
// names, and none when nobody holds it.
//
// A delivery that names a pull request carries the pull request's number, and
// the card that pull request belongs to is the issue it closes: the read
// follows that link, in the same repository, and takes the lowest-numbered one
// when a pull request closes several, so the answer is the same on every
// delivery. A number that names nothing — a card deleted since the delivery —
// is an empty answer and not an error: section 4 drops an inconclusive read,
// and a redelivery of the same fact would be inconclusive again. A fault of the
// API itself is an error, and the request path decides what that costs
// (docs/SYSTEMS.md sections 3 and 7).
func (c *Client) Assignees(ctx context.Context, repository string, card int) ([]string, error) {
	if err := shape(repository); err != nil {
		return nil, err
	}
	issue, found, err := c.card(ctx, repository, card)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	if issue.PullRequest == nil {
		return loginsOf(issue.Assignees), nil
	}
	return c.closingIssue(ctx, repository, card)
}

// Items is the whole board, as the claim rule and the actions that can leave a
// card with nobody on it read it: every item that is a card in a repository,
// with the stage the board's Status field gives it.
//
// An item that is not a card in a repository — a draft the board holds on its
// own — is not read: the routing rule is about cards, and a draft has no
// repository and no number to route on.
func (c *Client) Items(ctx context.Context) ([]router.Item, error) {
	items := []router.Item{}
	after := ""
	for page := 1; page <= maxPages; page++ {
		var answer struct {
			Organization *struct {
				ProjectV2 *struct {
					Items struct {
						PageInfo struct {
							HasNextPage bool   `json:"hasNextPage"`
							EndCursor   string `json:"endCursor"`
						} `json:"pageInfo"`
						Nodes []struct {
							Status *struct {
								Name string `json:"name"`
							} `json:"fieldValueByName"`
							Content *struct {
								Number     int    `json:"number"`
								Title      string `json:"title"`
								Repository *struct {
									NameWithOwner string `json:"nameWithOwner"`
								} `json:"repository"`
								Assignees struct {
									Nodes []login `json:"nodes"`
								} `json:"assignees"`
								Labels struct {
									Nodes []label `json:"nodes"`
								} `json:"labels"`
							} `json:"content"`
						} `json:"nodes"`
					} `json:"items"`
				} `json:"projectV2"`
			} `json:"organization"`
		}
		variables := map[string]any{"owner": c.owner, "project": c.project, "page": pageSize}
		if after != "" {
			variables["after"] = after
		}
		if err := c.graphql(ctx, itemsDocument, variables, &answer); err != nil {
			return nil, err
		}

		if answer.Organization == nil || answer.Organization.ProjectV2 == nil {
			// The organisation or the project is not on the far end's answer,
			// which is what a project number that names nothing looks like.
			// That is a configuration fault and not an empty board: an empty
			// board would silently end every claim wake there will ever be.
			return nil, fmt.Errorf("board: %s has no project %d this token can read", c.owner, c.project)
		}
		board := answer.Organization.ProjectV2.Items
		for _, node := range board.Nodes {
			if node.Content == nil || node.Content.Repository == nil || node.Content.Number == 0 {
				continue
			}
			item := router.Item{
				Repository: node.Content.Repository.NameWithOwner,
				Number:     node.Content.Number,
				Title:      node.Content.Title,
				Assignees:  loginsOf(node.Content.Assignees.Nodes),
				Labels:     namesOf(node.Content.Labels.Nodes),
			}
			if node.Status != nil {
				item.Status = node.Status.Name
			}
			items = append(items, item)
		}

		if !board.PageInfo.HasNextPage {
			return items, nil
		}
		if board.PageInfo.EndCursor == "" {
			return nil, fmt.Errorf("board: the board names another page and no cursor, which would be the same page read forever")
		}
		after = board.PageInfo.EndCursor
	}
	return nil, fmt.Errorf("board: the board is longer than %d pages of %d items", maxPages, pageSize)
}

// restIssue is one card as the REST API answers it: an issue, or a pull request
// — which the same endpoint answers with a `pull_request` block beside the
// assignees.
type restIssue struct {
	Number      int     `json:"number"`
	PullRequest *any    `json:"pull_request"`
	Assignees   []login `json:"assignees"`
}

// login is one login in an answer, from either API.
type login struct {
	Login string `json:"login"`
}

// label is one label name in an answer.
type label struct {
	Name string `json:"name"`
}

// card reads one card through the REST API. found is false when the number
// names nothing in that repository, which is an empty answer rather than a
// failure.
func (c *Client) card(ctx context.Context, repository string, number int) (restIssue, bool, error) {
	url := c.base + "/repos/" + repository + "/issues/" + strconv.Itoa(number)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return restIssue{}, false, fmt.Errorf("board: the card read: %w", err)
	}
	c.authorize(req)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", apiVersion)

	resp, err := c.http.Do(req)
	if err != nil {
		return restIssue{}, false, fmt.Errorf("board: the card read: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswerBytes))
	if err != nil {
		return restIssue{}, false, fmt.Errorf("board: reading the card: %w", err)
	}
	switch resp.StatusCode {
	case http.StatusOK:
		var issue restIssue
		if err := json.Unmarshal(raw, &issue); err != nil {
			return restIssue{}, false, fmt.Errorf("board: the card is not the JSON this read asks for: %w", err)
		}
		if issue.Number == 0 {
			// A card this read asks for always carries its number. An answer
			// that carries none is not a card that nobody holds — it is an
			// answer this read cannot read, and saying "nobody holds it" would
			// drop a wake silently, which is the one thing this service exists
			// to stop (docs/PRODUCT.md section 5).
			return restIssue{}, false, fmt.Errorf("board: the card read answered 200 and no card")
		}
		return issue, true, nil
	case http.StatusNotFound:
		return restIssue{}, false, nil
	default:
		return restIssue{}, false, fmt.Errorf("board: the card read answered %d", resp.StatusCode)
	}
}

// closingIssue follows the link the delivery does not carry: the card a pull
// request belongs to is the issue it closes. The lowest-numbered one in the
// same repository is the card, so several closing issues still give one answer;
// a pull request that closes no issue in its own repository holds no card this
// read can name, and that is an empty answer rather than a guess.
func (c *Client) closingIssue(ctx context.Context, repository string, number int) ([]string, error) {
	owner, name, _ := strings.Cut(repository, "/")
	var answer struct {
		Repository struct {
			PullRequest *struct {
				ClosingIssues struct {
					Nodes []struct {
						Number     int `json:"number"`
						Repository struct {
							NameWithOwner string `json:"nameWithOwner"`
						} `json:"repository"`
						Assignees struct {
							Nodes []login `json:"nodes"`
						} `json:"assignees"`
					} `json:"nodes"`
				} `json:"closingIssuesReferences"`
			} `json:"pullRequest"`
		} `json:"repository"`
	}
	variables := map[string]any{"owner": owner, "name": name, "number": number, "logins": loginsPerCard}
	if err := c.graphql(ctx, closingIssueDocument, variables, &answer); err != nil {
		return nil, err
	}
	pull := answer.Repository.PullRequest
	if pull == nil {
		// The REST read has already said this number is a pull request, so a
		// GraphQL read that cannot see it is a failed read and not a pull
		// request that holds nobody.
		return nil, fmt.Errorf("board: the pull request read answered no pull request")
	}
	found := false
	card := 0
	var logins []string
	for _, node := range pull.ClosingIssues.Nodes {
		if node.Repository.NameWithOwner != repository {
			continue
		}
		if found && node.Number >= card {
			continue
		}
		found, card, logins = true, node.Number, loginsOf(node.Assignees.Nodes)
	}
	if !found {
		return nil, nil
	}
	return logins, nil
}

// graphql posts one query and decodes its data into out. GitHub answers a
// refusal with HTTP 200 and an errors array, so a non-empty one is a failure
// whatever the status was: the read never guesses from a partial answer. The
// messages it carries back are GitHub's own text; the body of a delivery is not
// in them, and neither is the token.
func (c *Client) graphql(ctx context.Context, document string, variables map[string]any, out any) error {
	body, err := json.Marshal(map[string]any{"query": document, "variables": variables})
	if err != nil {
		return fmt.Errorf("board: the query: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+graphqlPath, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("board: the query: %w", err)
	}
	c.authorize(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("board: the board read: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswerBytes))
	if err != nil {
		return fmt.Errorf("board: reading the answer: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("board: the board read answered %d", resp.StatusCode)
	}
	var answer struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &answer); err != nil {
		return fmt.Errorf("board: the answer is not the JSON this read asks for: %w", err)
	}
	if len(answer.Errors) > 0 {
		messages := make([]string, 0, len(answer.Errors))
		for _, failure := range answer.Errors {
			messages = append(messages, failure.Message)
		}
		return fmt.Errorf("board: the board read was refused: %s", strings.Join(messages, "; "))
	}
	if len(answer.Data) == 0 || string(bytes.TrimSpace(answer.Data)) == "null" {
		// A query that answered no data did not answer "the board is empty":
		// it failed. Reading it as an empty board would be the quietest
		// possible failure — no claim wake, and no silent event woken, ever.
		return fmt.Errorf("board: the board read answered no data and no error")
	}
	if err := json.Unmarshal(answer.Data, out); err != nil {
		return fmt.Errorf("board: the answer is not the shape this read asks for: %w", err)
	}
	return nil
}

// authorize puts the token in the one place it belongs. It is set on every
// request and read from nowhere else: it is never a URL, never a query
// parameter and never a log line.
func (c *Client) authorize(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.token)
}

// shape checks the repository a delivery named before it becomes a URL path.
// The allowlist has already accepted it, and this is the second half of the
// same rule: a payload field is not a path until it is known to be one.
func shape(repository string) error {
	parts := strings.Split(repository, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("board: %q is not an owner/name repository", repository)
	}
	for _, part := range parts {
		if part == "." || part == ".." || strings.ContainsAny(part, " \t?#%\\") {
			return fmt.Errorf("board: %q is not an owner/name repository", repository)
		}
	}
	return nil
}

// loginsOf is the logins of one answer, in the order the API gave them. The
// router's own rule picks the bot among them, which is what keeps the fleet's
// login convention out of this package.
func loginsOf(nodes []login) []string {
	if len(nodes) == 0 {
		return nil
	}
	logins := make([]string, 0, len(nodes))
	for _, node := range nodes {
		logins = append(logins, node.Login)
	}
	return logins
}

// namesOf is the label names of one answer.
func namesOf(nodes []label) []string {
	if len(nodes) == 0 {
		return nil
	}
	names := make([]string, 0, len(nodes))
	for _, node := range nodes {
		names = append(names, node.Name)
	}
	return names
}

// itemsDocument reads the whole board: one page of the organisation's project
// items, each with its stage and the card behind it. The stage comes from the
// board's Status field by name, and only a single-select value carries one,
// which is what the fragment restricts it to.
const itemsDocument = `query($owner: String!, $project: Int!, $page: Int!, $after: String) {
  organization(login: $owner) {
    projectV2(number: $project) {
      items(first: $page, after: $after) {
        pageInfo { hasNextPage endCursor }
        nodes {
          fieldValueByName(name: "Status") {
            __typename
            ... on ProjectV2ItemFieldSingleSelectValue { name }
          }
          content {
            __typename
            ... on Issue {
              number
              title
              repository { nameWithOwner }
              assignees(first: 20) { nodes { login } }
              labels(first: 20) { nodes { name } }
            }
            ... on PullRequest {
              number
              title
              repository { nameWithOwner }
              assignees(first: 20) { nodes { login } }
              labels(first: 20) { nodes { name } }
            }
          }
        }
      }
    }
  }
}`

// closingIssueDocument reads the issues a pull request closes, with the logins
// on each: the card that pull request belongs to, which is what a delivery
// naming a pull request leaves out.
const closingIssueDocument = `query($owner: String!, $name: String!, $number: Int!, $logins: Int!) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      closingIssuesReferences(first: 20) {
        nodes {
          number
          repository { nameWithOwner }
          assignees(first: $logins) { nodes { login } }
        }
      }
    }
  }
}`
