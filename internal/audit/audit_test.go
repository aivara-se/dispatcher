package audit_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aivara-se/dispatcher/internal/audit"
)

// The audit line is the list in docs/SYSTEMS.md section 9 and nothing else: a
// field added to it is a field an operator will read as a promise, and a field
// carrying what a third party wrote is a leak.
func TestAuditLineIsExactlyTheDocumentedShape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	trail, err := audit.Open(path, audit.DefaultRotateBytes)
	if err != nil {
		t.Fatalf("opening the audit file: %v", err)
	}
	card := 59
	entry := audit.Entry{
		Time:       time.Date(2026, 9, 30, 12, 0, 0, 0, time.FixedZone("elsewhere", 2*60*60)),
		DeliveryID: "d1e2f3",
		Event:      "issues",
		Action:     "assigned",
		Repository: "aivara-se/learn-chess",
		Card:       &card,
		Decision:   "wake",
		Bot:        "mimi",
		Outcome:    "accepted",
		Response:   202,
	}
	if err := trail.Append(entry); err != nil {
		t.Fatalf("appending a line: %v", err)
	}
	if err := trail.Close(); err != nil {
		t.Fatalf("closing: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("one delivery is one line, got %d", len(lines))
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("the line is not JSON: %v", err)
	}
	want := []string{"time", "delivery", "event", "action", "repository", "card", "decision", "bot", "outcome", "response"}
	if len(got) != len(want) {
		t.Errorf("the line has %d field(s), the document names %d: %v", len(got), len(want), keys(got))
	}
	for _, key := range want {
		if _, ok := got[key]; !ok {
			t.Errorf("the line is missing %q", key)
		}
	}
	// The time is UTC whatever zone the caller wrote it in.
	stamp, err := time.Parse(time.RFC3339Nano, got["time"].(string))
	if err != nil {
		t.Fatalf("the time is not RFC 3339: %v", err)
	}
	if !stamp.Equal(entry.Time) || stamp.Location() != time.UTC {
		t.Errorf("time = %s, want %s in UTC", got["time"], entry.Time.UTC())
	}
	if n, ok := got["card"].(float64); !ok || int(n) != 59 {
		t.Errorf("card = %v, want 59", got["card"])
	}
}

// A delivery with no card names no card: the number is omitted, not zero.
func TestAuditLineOmitsWhatThereIsNot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	trail, err := audit.Open(path, audit.DefaultRotateBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := trail.Append(audit.Entry{DeliveryID: "d1", Event: "push", Action: "created", Repository: "a", Decision: "ignored", Outcome: "not ours", Response: 202}); err != nil {
		t.Fatal(err)
	}
	if err := trail.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(raw))), &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"card", "bot"} {
		if _, ok := got[key]; ok {
			t.Errorf("%q is in a line that has none", key)
		}
	}
}

// The file is rotated at its size, so a reader following it sees the trail
// restart rather than stop, and one generation of history stays beside it.
func TestAuditRotates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	trail, err := audit.Open(path, 64)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		entry := audit.Entry{DeliveryID: fmt.Sprintf("d%d", i), Event: "issues", Action: "assigned", Repository: "a/b", Decision: "wake", Bot: "mimi", Outcome: "accepted", Response: 202}
		if err := trail.Append(entry); err != nil {
			t.Fatal(err)
		}
	}
	if err := trail.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("the generation before the current one must be kept: %v", err)
	}
	current := lines(t, path)
	previous := lines(t, path+".1")
	if len(current) != 1 || len(previous) != 1 {
		t.Fatalf("a rotation at every line holds one line each, got current %d and previous %d", len(current), len(previous))
	}
	// The generation kept is the one written just before the current file, so
	// the oldest delivery is the line the rotation dropped: one generation, and
	// no more, is what the file is for.
	if !strings.Contains(previous[0], `"d1"`) || !strings.Contains(current[0], `"d2"`) {
		t.Errorf("rotation kept the wrong lines:\n  previous %s\n  current  %s", previous[0], current[0])
	}
}

// The dead-letter file holds the delivery whose wake could not be delivered and
// the transport failure that ended it — never the payload.
func TestDeadLetterAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dead-letter.jsonl")
	dead, err := audit.OpenDeadLetter(path)
	if err != nil {
		t.Fatalf("opening the dead-letter file: %v", err)
	}
	card := 5
	record := audit.Record{
		DeliveryID: "d1e2f3",
		Event:      "issue_comment",
		Action:     "created",
		Repository: "aivara-se/dispatcher",
		Card:       &card,
		Bot:        "mimi",
		Attempts:   3,
		Reason:     "the gateway did not answer: connection refused",
	}
	if err := dead.Append(record); err != nil {
		t.Fatalf("appending a record: %v", err)
	}
	if err := dead.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(raw))), &got); err != nil {
		t.Fatalf("the record is not JSON: %v", err)
	}
	want := []string{"time", "delivery", "event", "action", "repository", "card", "bot", "attempts", "reason"}
	if len(got) != len(want) {
		t.Errorf("the record has %d field(s), want %d: %v", len(got), len(want), keys(got))
	}
	for _, key := range want {
		if _, ok := got[key]; !ok {
			t.Errorf("the record is missing %q", key)
		}
	}
	if got["attempts"].(float64) != 3 {
		t.Errorf("attempts = %v, want 3", got["attempts"])
	}
}

func lines(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	trimmed := strings.TrimSuffix(string(raw), "\n")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	return out
}
