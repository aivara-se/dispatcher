package receiver

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/aivara-se/dispatcher/internal/router"
)

// errTooLarge is how readBody reports the limit being reached: the 413 of
// docs/SYSTEMS.md section 3, told apart from a body that could not be read.
var errTooLarge = errors.New("the body is over the limit")

// readBody reads the raw body up to the limit. MaxBytesReader is what makes the
// limit a refusal rather than a memory problem, and it is also why the body is
// read before the signature is checked: the signature covers the bytes.
func readBody(w http.ResponseWriter, req *http.Request) ([]byte, error) {
	req.Body = http.MaxBytesReader(w, req.Body, maxBodyBytes)
	body, err := io.ReadAll(req.Body)
	if err != nil {
		var over *http.MaxBytesError
		if errors.As(err, &over) {
			return nil, errTooLarge
		}
		return nil, err
	}
	return body, nil
}

// payload is the part of a GitHub delivery the service parses: the action, the
// repository, the card number, the assignee and the actor. Every field a
// decision uses is here, and none of them becomes a path, a command or a URL
// (ADR 002). A field the router needs later is a field added here.
type payload struct {
	Action     string `json:"action"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	Issue *struct {
		Number int `json:"number"`
	} `json:"issue"`
	PullRequest *struct {
		Number int `json:"number"`
	} `json:"pull_request"`
	Assignee *struct {
		Login string `json:"login"`
	} `json:"assignee"`
	Sender *struct {
		Login string `json:"login"`
	} `json:"sender"`
}

// parseEvent turns the verified body and the two id headers into the router's
// input. It is the first thing that parses anything, and it runs only after the
// signature has verified.
func parseEvent(body []byte, event, delivery string) (router.Event, error) {
	if event == "" {
		return router.Event{}, errors.New("X-GitHub-Event is missing")
	}
	if delivery == "" {
		return router.Event{}, errors.New("X-GitHub-Delivery is missing")
	}
	var p payload
	if err := json.Unmarshal(body, &p); err != nil {
		return router.Event{}, errors.New("the body is not the JSON the signature covered")
	}
	if p.Repository.FullName == "" {
		return router.Event{}, errors.New("the envelope carries no repository")
	}
	ev := router.Event{
		DeliveryID: delivery,
		Event:      event,
		Action:     p.Action,
		Repository: p.Repository.FullName,
	}
	if p.Issue != nil {
		number := p.Issue.Number
		ev.Card = &number
	}
	if p.PullRequest != nil {
		number := p.PullRequest.Number
		ev.Card = &number
	}
	if p.Assignee != nil {
		ev.Assignee = p.Assignee.Login
	}
	if p.Sender != nil {
		ev.Actor = p.Sender.Login
	}
	return ev, nil
}
