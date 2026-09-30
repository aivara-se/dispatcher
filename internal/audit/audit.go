// Package audit writes the two files the service keeps: one line per delivery
// in arrival order, and the dead-letter file a wake lands in when its retries
// are exhausted.
//
// The line carries identifiers and nothing else — no payload body, no comment
// text, no login beyond the actor's — because a body is third-party text. See
// docs/SYSTEMS.md sections 7 and 9 and docs/adrs/004-idempotency-and-replay.md.
package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// DefaultRotateBytes is where the audit file is rotated. The file is a JSON
// Lines trail an operator reads with tail and grep, so one generation of
// history is kept beside the current one: <path> and <path>.1.
const DefaultRotateBytes int64 = 8 << 20

// Entry is one audit line, and it is exactly the list in docs/SYSTEMS.md
// section 9: the time in UTC, the delivery id, the event and action, the
// repository, the card number when there is one, the routing decision and the
// bot it woke, the outbound outcome, and the response code the sender was
// given. A field added here is a field an operator will read as a promise, so
// there is no room in it for anything a third party wrote.
type Entry struct {
	Time       time.Time `json:"time"`
	DeliveryID string    `json:"delivery"`
	Event      string    `json:"event"`
	Action     string    `json:"action"`
	Repository string    `json:"repository"`
	Card       *int      `json:"card,omitempty"`
	Decision   string    `json:"decision"`
	Bot        string    `json:"bot,omitempty"`
	Outcome    string    `json:"outcome"`
	Response   int       `json:"response"`
}

// Log is the audit file. It is safe for concurrent use: the receiver appends
// from more than one request goroutine.
type Log struct {
	mu          sync.Mutex
	path        string
	rotateBytes int64
	file        *os.File
	size        int64
}

// Open opens the audit file for appending, creating it if it is not there.
func Open(path string, rotateBytes int64) (*Log, error) {
	if rotateBytes <= 0 {
		rotateBytes = DefaultRotateBytes
	}
	file, size, err := open(path)
	if err != nil {
		return nil, err
	}
	return &Log{path: path, rotateBytes: rotateBytes, file: file, size: size}, nil
}

// Append writes one line. An entry with no time is stamped now, in UTC.
func (l *Log) Append(entry Entry) error {
	if entry.Time.IsZero() {
		entry.Time = time.Now()
	}
	entry.Time = entry.Time.UTC()
	line, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("audit line: %w", err)
	}
	line = append(line, '\n')

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.size > 0 && l.size+int64(len(line)) > l.rotateBytes {
		if err := l.rotate(); err != nil {
			return err
		}
	}
	n, err := l.file.Write(line)
	l.size += int64(n)
	if err != nil {
		return fmt.Errorf("audit file %s: %w", l.path, err)
	}
	return nil
}

// Close closes the file. The trail is never rewritten, so there is nothing to
// flush.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

// rotate keeps one generation: the current file becomes <path>.1, and a new one
// starts empty. A reader following <path> sees the trail restart rather than
// stop.
func (l *Log) rotate() error {
	if err := l.file.Close(); err != nil {
		return fmt.Errorf("audit file %s: %w", l.path, err)
	}
	l.file = nil
	previous := l.path + ".1"
	if err := os.Remove(previous); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("audit rotation: %w", err)
	}
	if err := os.Rename(l.path, previous); err != nil {
		return fmt.Errorf("audit rotation: %w", err)
	}
	file, _, err := open(l.path)
	if err != nil {
		return err
	}
	l.file = file
	l.size = 0
	return nil
}

func open(path string) (*os.File, int64, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, 0, fmt.Errorf("audit file %s: %w", path, err)
		}
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, 0, fmt.Errorf("audit file %s: %w", path, err)
	}
	info, err := file.Stat()
	if err != nil {
		return nil, 0, fmt.Errorf("audit file %s: %w", path, err)
	}
	return file, info.Size(), nil
}

// Record is one dead-letter entry: the delivery whose wake could not be
// delivered, and the transport failure that ended it — never the payload.
type Record struct {
	Time       time.Time `json:"time"`
	DeliveryID string    `json:"delivery"`
	Event      string    `json:"event"`
	Action     string    `json:"action"`
	Repository string    `json:"repository"`
	Card       *int      `json:"card,omitempty"`
	Bot        string    `json:"bot,omitempty"`
	Attempts   int       `json:"attempts"`
	Reason     string    `json:"reason"`
}

// DeadLetter is the file a wake lands in when its retries are exhausted. A
// failed wake is never silently dropped: an event nobody saw is the failure the
// whole service exists to prevent (docs/SYSTEMS.md section 7).
type DeadLetter struct {
	mu   sync.Mutex
	path string
	file *os.File
}

// OpenDeadLetter opens the dead-letter file for appending.
func OpenDeadLetter(path string) (*DeadLetter, error) {
	file, _, err := open(path)
	if err != nil {
		return nil, err
	}
	return &DeadLetter{path: path, file: file}, nil
}

// Append writes one record.
func (d *DeadLetter) Append(record Record) error {
	if record.Time.IsZero() {
		record.Time = time.Now()
	}
	record.Time = record.Time.UTC()
	line, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("dead-letter line: %w", err)
	}
	line = append(line, '\n')

	d.mu.Lock()
	defer d.mu.Unlock()
	if _, err := d.file.Write(line); err != nil {
		return fmt.Errorf("dead-letter file %s: %w", d.path, err)
	}
	return nil
}

// Close closes the file.
func (d *DeadLetter) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.file == nil {
		return nil
	}
	err := d.file.Close()
	d.file = nil
	return err
}
